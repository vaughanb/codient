package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
)

// Manager runs configured hooks for a session.
type Manager struct {
	loaded *Loaded
	cwd    string
	model  string

	sessionID string
	turn      atomic.Uint64
}

// NewManager returns a manager from loaded hooks (may be nil loaded for no-op).
func NewManager(loaded *Loaded, cwd, model, sessionID string) *Manager {
	if loaded == nil {
		loaded = &Loaded{ByEvent: map[string][]MatcherGroup{}}
	}
	return &Manager{
		loaded:    loaded,
		cwd:       strings.TrimSpace(cwd),
		model:     strings.TrimSpace(model),
		sessionID: sessionID,
	}
}

// NextTurn increments the turn counter (call once per user/agent turn).
func (m *Manager) NextTurn() uint64 {
	return m.turn.Add(1)
}

// Loaded returns the merged hook configuration (for /hooks listing).
func (m *Manager) Loaded() *Loaded { return m.loaded }

// IsEmpty reports whether any hooks are configured.
func (m *Manager) IsEmpty() bool {
	if m == nil || m.loaded == nil {
		return true
	}
	for _, groups := range m.loaded.ByEvent {
		if len(groups) > 0 {
			return false
		}
	}
	return true
}

func (m *Manager) baseEnvelope(event string) map[string]any {
	t := m.turn.Load()
	return map[string]any{
		"session_id":      m.sessionID,
		"cwd":             m.cwd,
		"hook_event_name": event,
		"model":           m.model,
		"turn_id":         fmt.Sprintf("%d", t),
	}
}

// RunSessionStart runs SessionStart hooks; returns text to append to the system prompt.
func (m *Manager) RunSessionStart(ctx context.Context, source SessionStartSource) (additionalContext string, _ error) {
	if m == nil || m.IsEmpty() {
		return "", nil
	}
	groups := m.loaded.ByEvent[EventSessionStart]
	if len(groups) == 0 {
		return "", nil
	}
	src := string(source)
	if src == "" {
		src = string(SessionStartup)
	}
	env := m.baseEnvelope(EventSessionStart)
	env["source"] = src
	out, err := runMatchingHandlers(ctx, m.cwd, groups, src, env)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.SystemMessage) != "" {
		// surfaced by caller if desired
		_ = out.SystemMessage
	}
	return strings.TrimSpace(out.AdditionalContext), nil
}

// RunSessionEnd fires SessionEnd hooks (best-effort observability).
func (m *Manager) RunSessionEnd(ctx context.Context) {
	if m == nil || m.IsEmpty() {
		return
	}
	groups := m.loaded.ByEvent[EventSessionEnd]
	if len(groups) == 0 {
		return
	}
	env := m.baseEnvelope(EventSessionEnd)
	_, _ = runMatchingHandlers(ctx, m.cwd, groups, "", env)
}

// UserPromptResult is the outcome of UserPromptSubmit hooks.
type UserPromptResult struct {
	Blocked bool
	Reason  string
}

// RunUserPromptSubmit runs before the user message is sent to the model.
func (m *Manager) RunUserPromptSubmit(ctx context.Context, prompt string) (UserPromptResult, error) {
	if m == nil || m.IsEmpty() {
		return UserPromptResult{}, nil
	}
	groups := m.loaded.ByEvent[EventUserPromptSubmit]
	if len(groups) == 0 {
		return UserPromptResult{}, nil
	}
	env := m.baseEnvelope(EventUserPromptSubmit)
	env["prompt"] = prompt
	out, err := runMatchingHandlers(ctx, m.cwd, groups, "", env)
	if err != nil {
		return UserPromptResult{}, err
	}
	if strings.EqualFold(out.Decision, "block") {
		r := strings.TrimSpace(out.Reason)
		if r == "" {
			r = "prompt blocked by hook"
		}
		return UserPromptResult{Blocked: true, Reason: r}, nil
	}
	return UserPromptResult{}, nil
}

// PreToolResult is the outcome of PreToolUse hooks.
type PreToolResult struct {
	Allow            bool
	BlockReason      string
	SystemMessage    string
	AdditionalContext string // rarely used before tool
}

// RunPreToolUse runs before a tool executes. toolUseID is a stable id for this invocation.
func (m *Manager) RunPreToolUse(ctx context.Context, toolName string, args json.RawMessage, toolUseID string) (PreToolResult, error) {
	if m == nil || m.IsEmpty() {
		return PreToolResult{Allow: true}, nil
	}
	groups := m.loaded.ByEvent[EventPreToolUse]
	if len(groups) == 0 {
		return PreToolResult{Allow: true}, nil
	}
	env := m.baseEnvelope(EventPreToolUse)
	env["tool_name"] = toolName
	env["tool_use_id"] = toolUseID
	var toolInput any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &toolInput)
	}
	env["tool_input"] = toolInput
	out, err := runMatchingHandlers(ctx, m.cwd, groups, toolName, env)
	if err != nil {
		return PreToolResult{}, err
	}
	if strings.EqualFold(out.Decision, "block") {
		r := strings.TrimSpace(out.Reason)
		if r == "" {
			r = "tool blocked by hook"
		}
		return PreToolResult{Allow: false, BlockReason: r, SystemMessage: out.SystemMessage}, nil
	}
	return PreToolResult{Allow: true, AdditionalContext: strings.TrimSpace(out.AdditionalContext), SystemMessage: out.SystemMessage}, nil
}

// PostToolResult contains optional additions after a tool runs.
type PostToolResult struct {
	AdditionalContext string
	SystemMessage     string
}

// RunPostToolUse runs after a tool executes.
func (m *Manager) RunPostToolUse(ctx context.Context, toolName string, args json.RawMessage, toolUseID, toolResult string, toolErr error) (PostToolResult, error) {
	if m == nil || m.IsEmpty() {
		return PostToolResult{}, nil
	}
	groups := m.loaded.ByEvent[EventPostToolUse]
	if len(groups) == 0 {
		return PostToolResult{}, nil
	}
	env := m.baseEnvelope(EventPostToolUse)
	env["tool_name"] = toolName
	env["tool_use_id"] = toolUseID
	var toolInput any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &toolInput)
	}
	env["tool_input"] = toolInput
	env["tool_response"] = toolResult
	if toolErr != nil {
		env["tool_error"] = toolErr.Error()
	}
	out, err := runMatchingHandlers(ctx, m.cwd, groups, toolName, env)
	if err != nil {
		return PostToolResult{}, err
	}
	// PostToolUse "block" in Codex replaces tool result — we treat as additional context + optional block message
	if strings.EqualFold(out.Decision, "block") {
		r := strings.TrimSpace(out.Reason)
		if r != "" {
			if out.AdditionalContext != "" {
				out.AdditionalContext = r + "\n" + out.AdditionalContext
			} else {
				out.AdditionalContext = r
			}
		}
	}
	return PostToolResult{
		AdditionalContext: strings.TrimSpace(out.AdditionalContext),
		SystemMessage:     strings.TrimSpace(out.SystemMessage),
	}, nil
}

// StopResult is the outcome of Stop hooks (Codex-compatible: decision=block means continue with reason).
type StopResult struct {
	Continue           bool   // false => finish turn and return reply
	ContinuationPrompt string // when Continue true, inject as user message
	Reason               string
	SystemMessage        string
}

// RunStop runs when the model produced a text reply with no tool calls.
// stopHookActive should be true when this reply follows a Stop-hook continuation in the same user turn.
func (m *Manager) RunStop(ctx context.Context, lastAssistant string, stopHookActive bool) (StopResult, error) {
	if m == nil || m.IsEmpty() {
		return StopResult{Continue: false}, nil
	}
	groups := m.loaded.ByEvent[EventStop]
	if len(groups) == 0 {
		return StopResult{Continue: false}, nil
	}
	env := m.baseEnvelope(EventStop)
	env["last_assistant_message"] = lastAssistant
	env["stop_hook_active"] = stopHookActive
	out, err := runMatchingHandlers(ctx, m.cwd, groups, "", env)
	if err != nil {
		return StopResult{}, err
	}
	// Explicit continue: false stops the turn (Codex: overrides continuation).
	if out.Continue != nil && !*out.Continue {
		return StopResult{Continue: false, SystemMessage: out.SystemMessage}, nil
	}
	// Codex: decision "block" + reason => continue with reason as prompt
	if strings.EqualFold(out.Decision, "block") && strings.TrimSpace(out.Reason) != "" {
		return StopResult{
			Continue:           true,
			ContinuationPrompt: strings.TrimSpace(out.Reason),
			SystemMessage:      out.SystemMessage,
		}, nil
	}
	if strings.TrimSpace(out.AdditionalContext) != "" {
		return StopResult{
			Continue:           true,
			ContinuationPrompt: strings.TrimSpace(out.AdditionalContext),
			SystemMessage:      out.SystemMessage,
		}, nil
	}
	return StopResult{Continue: false, SystemMessage: out.SystemMessage}, nil
}

// LoadForConfig loads hooks when enabled and builds a manager.
func LoadForConfig(enabled bool, workspace, model, sessionID string) (*Manager, error) {
	if !enabled {
		return nil, nil
	}
	loaded, err := Load(workspace)
	if err != nil {
		return nil, err
	}
	return NewManager(loaded, workspace, model, sessionID), nil
}
