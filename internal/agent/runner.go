// Package agent runs a tool-calling loop against an OpenAI-compatible chat completions API.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/agentlog"
	"codient/internal/config"
	"codient/internal/hooks"
	"codient/internal/tokenest"
	"codient/internal/tokentracker"
	"codient/internal/tools"
)

// EstimateSessionCostFn maps cumulative session usage to estimated USD; ok false means unknown (skip budget check).
type EstimateSessionCostFn func(u tokentracker.Usage) (costUSD float64, ok bool)

// ChatClient is the LLM surface the agent needs (implemented by *openaiclient.Client).
type ChatClient interface {
	ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
	Model() string
}

// streamChatClient is implemented by *openaiclient.Client for token streaming during agent turns.
type streamChatClient interface {
	ChatCompletionStream(ctx context.Context, params openai.ChatCompletionNewParams, w io.Writer) (*openai.ChatCompletion, error)
}

// AutoCheckOutcome is returned by Runner.AutoCheck after file-mutating tools succeed.
// Inject is a full user message to append (empty means nothing to inject). Progress is one line for Progress (empty to skip).
type AutoCheckOutcome struct {
	Inject   string
	Progress string
}

// mutatingTools lists tool names that change files on disk; used to trigger auto-check.
var mutatingTools = map[string]struct{}{
	"write_file": {}, "str_replace": {}, "patch_file": {}, "insert_lines": {},
	"remove_path": {}, "move_path": {}, "copy_path": {},
}

// ToolIsMutating reports whether the named tool may modify files on disk.
func ToolIsMutating(name string) bool {
	_, ok := mutatingTools[name]
	return ok
}

// PostReplyCheckInfo is passed to PostReplyCheck after a text-only model reply.
type PostReplyCheckInfo struct {
	Reply     string
	User      string
	TurnTools []string // tool names invoked this user turn, in order (may repeat)
}

// Runner executes multi-step tool use with bounded LLM concurrency (via the ChatClient implementation).
type Runner struct {
	LLM   ChatClient
	Cfg   *config.Config
	Tools *tools.Registry
	Log   *agentlog.Logger
	// Tracker accumulates API-reported token usage; optional.
	Tracker *tokentracker.Tracker
	// Progress, when non-nil (e.g. os.Stderr), receives human-readable lines during the tool loop.
	Progress io.Writer
	// AutoCheck runs once after a tool batch that successfully used a mutating tool.
	// If Inject is non-empty, it is appended as a user message before the next LLM call.
	AutoCheck func(ctx context.Context) AutoCheckOutcome
	// PostReplyCheck, when non-nil, is called when the model produces a text reply
	// (no tool calls). If it returns a non-empty string, that string is injected as
	// a user message and the loop continues instead of returning. The field is nilled
	// after firing once to prevent infinite loops.
	PostReplyCheck func(ctx context.Context, info PostReplyCheckInfo) string
	// ProgressPlain suppresses ANSI styling on progress lines (e.g. -plain).
	ProgressPlain bool
	// ProgressMode is build|ask|plan; colors the thinking/intent bullet to match the REPL mode.
	ProgressMode string
	// MaxTurns caps LLM completion rounds for this user turn (0 = unlimited). Used for CI guardrails.
	MaxTurns int
	// MaxCostUSD caps estimated cumulative session cost in USD (0 = unlimited). Requires EstimateSessionCost.
	MaxCostUSD float64
	// EstimateSessionCost estimates USD for Tracker.Session() after each LLM call; optional.
	EstimateSessionCost EstimateSessionCostFn
	// Hooks runs lifecycle hooks (PreToolUse, PostToolUse, Stop); nil disables.
	Hooks *hooks.Manager
	// OnWorkingChange is called with true when the runner begins waiting for an
	// LLM response and false when the response arrives. Used to drive spinners.
	OnWorkingChange func(working bool)
	// StopHookActive is true when the next assistant text follows a Stop-hook continuation in this RunConversation.
	StopHookActive bool
	toolUseSeq     atomic.Uint64
}

// Run carries out one user turn (no prior conversation history).
// streamTo is where assistant text deltas are written when streaming (e.g. os.Stdout); nil disables streaming.
// streamed is true when the reply was written incrementally (caller skips glamour for that turn).
func (r *Runner) Run(ctx context.Context, system string, user openai.ChatCompletionMessageParamUnion, streamTo io.Writer) (reply string, streamed bool, err error) {
	reply, _, streamed, err = r.RunConversation(ctx, system, nil, user, streamTo)
	return reply, streamed, err
}

// RunConversation runs one user message with optional prior messages (excluding system).
// history is a slice of user/assistant/tool messages from earlier turns; system is prepended each request.
// Returns the assistant's final text and updated history (including this turn), suitable for REPL.
// streamTo selects streaming for this turn only (nil = non-streaming completion).
// streamed is true when the final reply was produced via streaming (skip glamour in the caller).
func (r *Runner) RunConversation(ctx context.Context, system string, history []openai.ChatCompletionMessageParamUnion, user openai.ChatCompletionMessageParamUnion, streamTo io.Writer) (string, []openai.ChatCompletionMessageParamUnion, bool, error) {
	r.StopHookActive = false
	r.toolUseSeq.Store(0)
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+16)
	sys := strings.TrimSpace(system)
	sysOffset := 0
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
		sysOffset = 1
	}
	msgs = append(msgs, history...)
	userText := UserMessageText(user)
	msgs = append(msgs, user)

	apiTools := r.Tools.OpenAITools()
	toolsOverhead := 0
	if len(apiTools) > 0 {
		b, _ := json.Marshal(apiTools)
		toolsOverhead = tokenest.Estimate(string(b))
	}
	llmRound := 0
	streamedFinal := false
	consecutiveToolFails := 0
	const maxConsecutiveToolFails = 3
	var turnTools []string

	for {
		if r.MaxTurns > 0 && llmRound >= r.MaxTurns {
			return "", nil, false, fmt.Errorf("%w: limit %d LLM rounds", ErrMaxTurns, r.MaxTurns)
		}
		msgs = truncateHistory(msgs, sysOffset, r.Cfg.ContextWindowTokens, r.Cfg.ContextReserveTokens, toolsOverhead)
		params := openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(r.LLM.Model()),
			Messages: msgs,
		}
		toolsDisabled := consecutiveToolFails >= maxConsecutiveToolFails
		if len(apiTools) > 0 && !toolsDisabled {
			params.Tools = apiTools
			params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String("auto"),
			}
			params.ParallelToolCalls = openai.Bool(true)
		}

		if r.OnWorkingChange != nil {
			r.OnWorkingChange(true)
		}
		t0 := time.Now()
		res, wasStreamed, err := r.callLLMWithRetry(ctx, params, streamTo)
		if r.OnWorkingChange != nil {
			r.OnWorkingChange(false)
		}
		if wasStreamed {
			streamedFinal = true
		}
		llmRound++
		llmDur := time.Since(t0)
		var logU *agentlog.TokenUsage
		if res != nil && err == nil {
			if r.Tracker != nil {
				r.Tracker.Add(usageFromCompletionUsage(res.Usage))
			}
			if r.MaxCostUSD > 0 && r.EstimateSessionCost != nil && r.Tracker != nil {
				cost, ok := r.EstimateSessionCost(r.Tracker.Session())
				if ok && cost > r.MaxCostUSD {
					return "", nil, false, fmt.Errorf("%w: estimated %g USD exceeds limit %g USD", ErrMaxCost, cost, r.MaxCostUSD)
				}
			}
			logU = logUsageFromCompletion(res.Usage)
		}
		if r.Log != nil {
			n := 0
			if res != nil {
				n = len(res.Choices)
			}
			r.Log.LLM(llmRound, r.LLM.Model(), llmDur, err, n, logU)
		}
		if err != nil {
			if r.Progress != nil {
				fmt.Fprintf(r.Progress, "  ✗ model %s  %s\n", formatProgressDur(llmDur), progressErrShort(err))
			}
			return "", nil, false, err
		}
		if len(res.Choices) == 0 {
			return "", nil, false, fmt.Errorf("empty choices from model")
		}

		msg := res.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			// Check for XML-style tool calls embedded in text (e.g. Qwen3-coder).
			// Skip if we've already hit the consecutive failure limit — the model
			// is stuck and parsing more text tool calls will loop forever.
			if msg.Content != "" && consecutiveToolFails < maxConsecutiveToolFails && containsTextToolCalls(msg.Content) {
				if parsed := parseTextToolCalls(msg.Content); len(parsed) > 0 {
					msgs = append(msgs, openai.AssistantMessage(msg.Content))

					thinkingPrinted := false
					if r.Progress != nil {
						if line := FormatThinkingProgressLine(r.ProgressPlain, r.ProgressMode, msg.Content); line != "" {
							fmt.Fprintf(r.Progress, "\n%s\n", line)
							thinkingPrinted = true
						}
					}
					if r.Progress != nil && !thinkingPrinted && len(parsed) > 0 {
						tc0 := parsed[0]
						args0 := textToolCallArgsJSON(tc0.Args)
						if line := FormatSyntheticIntentThinkingLine(r.ProgressPlain, r.ProgressMode, tc0.Name, args0); line != "" {
							fmt.Fprintf(r.Progress, "\n%s\n", line)
						}
					}

					type toolResult struct {
						name     string
						content  string
						progress string
					}
					results := make([]toolResult, len(parsed))
					if r.Progress != nil {
						for _, tc := range parsed {
							args := textToolCallArgsJSON(tc.Args)
							fmt.Fprintf(r.Progress, "%s\n", FormatToolIntentProgressLine(tc.Name, args))
						}
					}
					var wg sync.WaitGroup
					for i, tc := range parsed {
						wg.Add(1)
						go func(idx int, tc textToolCall) {
							defer wg.Done()
							args := textToolCallArgsJSON(tc.Args)
							if r.Log != nil {
								r.Log.ToolStart(tc.Name, agentlog.SummarizeArgs(tc.Name, args))
							}
							t1 := time.Now()
							toolID := fmt.Sprintf("tu-%d", r.toolUseSeq.Add(1))
							out, toolErr := r.runOneTool(ctx, tc.Name, args, toolID)
							toolDur := time.Since(t1)
							if r.Log != nil {
								r.Log.ToolEnd(tc.Name, toolDur, toolErr, nil)
							}
							compact := progressToolCompact(tc.Name, args)
							var prog string
							if toolErr != nil {
								prog = fmt.Sprintf("%s ✗ %s", compact, progressErrShort(toolErr))
							} else {
								prog = compact + " " + formatProgressDur(toolDur)
							}
							content := out
							if toolErr != nil {
								content = fmt.Sprintf("error: %v", toolErr)
							}
							results[idx] = toolResult{name: tc.Name, content: content, progress: prog}
						}(i, tc)
					}
					wg.Wait()

					for _, res := range results {
						turnTools = append(turnTools, res.name)
					}

					toolParts := make([]string, 0, len(results))
					allFailed := true
					var resultBuf strings.Builder
					resultBuf.WriteString("[tool results]\n")
					for _, res := range results {
						fmt.Fprintf(&resultBuf, "# %s\n%s\n\n", res.name, res.content)
						toolParts = append(toolParts, res.progress)
						if !strings.HasPrefix(res.content, "error: ") {
							allFailed = false
						}
					}
					acIn := make([]autoCheckInput, len(results))
					for i, res := range results {
						acIn[i] = autoCheckInput{name: res.name, content: res.content}
					}
					inject, prog := r.autoCheckAfterMutations(ctx, acIn)
					if inject != "" {
						fmt.Fprintf(&resultBuf, "\n%s\n", inject)
					}
					if prog != "" && r.Progress != nil {
						fmt.Fprintf(r.Progress, "%s%s\n", progressNestedIndent, prog)
					}
					msgs = append(msgs, openai.UserMessage(resultBuf.String()))
					if allFailed {
						consecutiveToolFails++
					} else {
						consecutiveToolFails = 0
					}
					if r.Progress != nil && len(toolParts) > 0 {
						suf := roundUsageSuffix(res, r.Cfg.ContextWindowTokens)
						fmt.Fprintf(r.Progress, "%sllm %s  ·  %s%s\n", progressNestedIndent, formatProgressDur(llmDur), strings.Join(toolParts, " · "), suf)
					}
					if consecutiveToolFails >= maxConsecutiveToolFails && r.Progress != nil {
						fmt.Fprintf(r.Progress, "  ⚠ %d consecutive tool failures — requesting text reply\n", consecutiveToolFails)
					}
					continue
				}
			}

			if r.Progress != nil {
				suf := roundUsageSuffix(res, r.Cfg.ContextWindowTokens)
				fmt.Fprintf(r.Progress, "%sllm %s  ·  reply%s\n", progressNestedIndent, formatProgressDur(llmDur), suf)
			}
			if msg.Content != "" {
				content := msg.Content
				if containsTextToolCalls(content) {
					content = stripTextToolCallFragments(content)
				}
				if r.PostReplyCheck != nil {
					if inject := r.PostReplyCheck(ctx, PostReplyCheckInfo{
						Reply:     content,
						User:      userText,
						TurnTools: turnTools,
					}); inject != "" {
						r.PostReplyCheck = nil
						msgs = append(msgs, openai.AssistantMessage(content))
						msgs = append(msgs, openai.UserMessage(inject))
						if r.Progress != nil {
							if line := FormatStatusProgressLine(r.ProgressPlain, r.ProgressMode, "verifying suggestions…"); line != "" {
								fmt.Fprintf(r.Progress, "%s\n", line)
							}
						}
						continue
					}
				}
				if r.Hooks != nil {
					sr, err := r.Hooks.RunStop(ctx, content, r.StopHookActive)
					if err != nil {
						return "", nil, false, err
					}
					if strings.TrimSpace(sr.SystemMessage) != "" && r.Progress != nil {
						fmt.Fprintf(r.Progress, "%s\n", sr.SystemMessage)
					}
					if sr.Continue {
						msgs = append(msgs, openai.AssistantMessage(content))
						msgs = append(msgs, openai.UserMessage(sr.ContinuationPrompt))
						r.StopHookActive = true
						continue
					}
				}
				r.StopHookActive = false
				msgs = append(msgs, openai.AssistantMessage(content))
				newHist := msgs[sysOffset:]
				return content, newHist, streamedFinal, nil
			}
			if strings.TrimSpace(msg.Refusal) != "" {
				return "", nil, false, fmt.Errorf("model refusal: %s", strings.TrimSpace(msg.Refusal))
			}
			return "", nil, false, fmt.Errorf("model returned no content and no tool calls")
		}

		msgs = append(msgs, msg.ToParam())

		type toolResult struct {
			id       string
			name     string
			content  string
			progress string
		}
		calls := make([]openai.ChatCompletionMessageFunctionToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			v, ok := tc.AsAny().(openai.ChatCompletionMessageFunctionToolCall)
			if !ok {
				return "", nil, false, fmt.Errorf("unsupported tool call variant")
			}
			calls = append(calls, v)
		}

		thinkingPrinted := false
		if r.Progress != nil && strings.TrimSpace(msg.Content) != "" {
			if line := FormatThinkingProgressLine(r.ProgressPlain, r.ProgressMode, msg.Content); line != "" {
				fmt.Fprintf(r.Progress, "\n%s\n", line)
				thinkingPrinted = true
			}
		}
		if r.Progress != nil && !thinkingPrinted && len(calls) > 0 {
			v0 := calls[0]
			args0 := json.RawMessage(v0.Function.Arguments)
			if line := FormatSyntheticIntentThinkingLine(r.ProgressPlain, r.ProgressMode, v0.Function.Name, args0); line != "" {
				fmt.Fprintf(r.Progress, "\n%s\n", line)
			}
		}

		results := make([]toolResult, len(calls))
		if r.Progress != nil {
			for _, v := range calls {
				args := json.RawMessage(v.Function.Arguments)
				fmt.Fprintf(r.Progress, "%s\n", FormatToolIntentProgressLine(v.Function.Name, args))
			}
		}
		var wg sync.WaitGroup
		for i, v := range calls {
			wg.Add(1)
			go func(idx int, v openai.ChatCompletionMessageFunctionToolCall) {
				defer wg.Done()
				args := json.RawMessage(v.Function.Arguments)
				if r.Log != nil {
					r.Log.ToolStart(v.Function.Name, agentlog.SummarizeArgs(v.Function.Name, args))
				}
				t1 := time.Now()
				toolID := fmt.Sprintf("tu-%d", r.toolUseSeq.Add(1))
				out, toolErr := r.runOneTool(ctx, v.Function.Name, args, toolID)
				toolDur := time.Since(t1)
				summary := map[string]any{}
				if v.Function.Name == "run_command" || v.Function.Name == "run_shell" {
					if ec := parseExitCodeFromRunOutput(out); ec != "" {
						summary["exit_code"] = ec
					}
				}
				if r.Log != nil {
					r.Log.ToolEnd(v.Function.Name, toolDur, toolErr, summary)
				}
				compact := progressToolCompact(v.Function.Name, args)
				var prog string
				if toolErr != nil {
					prog = fmt.Sprintf("%s ✗ %s", compact, progressErrShort(toolErr))
				} else {
					prog = compact + " " + formatProgressDur(toolDur)
					if v.Function.Name == "run_command" || v.Function.Name == "run_shell" {
						if ec := parseExitCodeFromRunOutput(out); ec != "" {
							prog += " exit=" + ec
						}
					}
				}
				content := out
				if toolErr != nil {
					content = fmt.Sprintf("error: %v", toolErr)
				}
				results[idx] = toolResult{id: v.ID, name: v.Function.Name, content: content, progress: prog}
			}(i, v)
		}
		wg.Wait()

		for _, v := range calls {
			turnTools = append(turnTools, v.Function.Name)
		}

		toolParts := make([]string, 0, len(results))
		allFailed := true
		for _, res := range results {
			msgs = append(msgs, openai.ToolMessage(res.content, res.id))
			toolParts = append(toolParts, res.progress)
			if !strings.HasPrefix(res.content, "error: ") {
				allFailed = false
			}
		}
		acIn := make([]autoCheckInput, len(results))
		for i, res := range results {
			acIn[i] = autoCheckInput{name: res.name, content: res.content}
		}
		inject, prog := r.autoCheckAfterMutations(ctx, acIn)
		if inject != "" {
			msgs = append(msgs, openai.UserMessage(inject))
		}
		if prog != "" && r.Progress != nil {
			fmt.Fprintf(r.Progress, "%s%s\n", progressNestedIndent, prog)
		}
		if allFailed {
			consecutiveToolFails++
		} else {
			consecutiveToolFails = 0
		}
		if r.Progress != nil && len(toolParts) > 0 {
			suf := roundUsageSuffix(res, r.Cfg.ContextWindowTokens)
			fmt.Fprintf(r.Progress, "%sllm %s  ·  %s%s\n", progressNestedIndent, formatProgressDur(llmDur), strings.Join(toolParts, " · "), suf)
		}
		if consecutiveToolFails >= maxConsecutiveToolFails && r.Progress != nil {
			fmt.Fprintf(r.Progress, "  ⚠ %d consecutive tool failures — requesting text reply\n", consecutiveToolFails)
		}
	}
}

type autoCheckInput struct {
	name    string
	content string
}

func (r *Runner) runOneTool(ctx context.Context, name string, args json.RawMessage, toolUseID string) (display string, underlyingErr error) {
	if r.Hooks != nil {
		pre, herr := r.Hooks.RunPreToolUse(ctx, name, args, toolUseID)
		if herr != nil {
			return fmt.Sprintf("error: hook: %v", herr), nil
		}
		if !pre.Allow {
			if strings.TrimSpace(pre.SystemMessage) != "" && r.Progress != nil {
				fmt.Fprintf(r.Progress, "%s\n", pre.SystemMessage)
			}
			return fmt.Sprintf("error: blocked by hook: %s", pre.BlockReason), nil
		}
		if strings.TrimSpace(pre.SystemMessage) != "" && r.Progress != nil {
			fmt.Fprintf(r.Progress, "%s\n", pre.SystemMessage)
		}
	}
	out, toolErr := r.Tools.Run(ctx, name, args)
	underlyingErr = toolErr
	display = out
	if toolErr != nil {
		display = fmt.Sprintf("error: %v", toolErr)
	}
	if r.Hooks != nil {
		post, herr := r.Hooks.RunPostToolUse(ctx, name, args, toolUseID, out, toolErr)
		if herr == nil {
			if strings.TrimSpace(post.SystemMessage) != "" && r.Progress != nil {
				fmt.Fprintf(r.Progress, "%s\n", post.SystemMessage)
			}
			if strings.TrimSpace(post.AdditionalContext) != "" {
				display = display + "\n\n[hook context]\n" + post.AdditionalContext
			}
		}
	}
	return display, underlyingErr
}

func (r *Runner) autoCheckAfterMutations(ctx context.Context, results []autoCheckInput) (inject string, progress string) {
	if r.AutoCheck == nil {
		return "", ""
	}
	hasMutation := false
	for _, res := range results {
		if _, ok := mutatingTools[res.name]; ok && !strings.HasPrefix(res.content, "error: ") {
			hasMutation = true
			break
		}
	}
	if !hasMutation {
		return "", ""
	}
	out := r.AutoCheck(ctx)
	return out.Inject, out.Progress
}

func usageFromCompletionUsage(u openai.CompletionUsage) tokentracker.Usage {
	return tokentracker.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func logUsageFromCompletion(u openai.CompletionUsage) *agentlog.TokenUsage {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	return &agentlog.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func roundUsageSuffix(res *openai.ChatCompletion, contextWindow int) string {
	if res == nil {
		return ""
	}
	u := usageFromCompletionUsage(res.Usage)
	if !u.HasAny() {
		return ""
	}
	return " · " + tokentracker.FormatLineCtx(u, contextWindow)
}

func parseExitCodeFromRunOutput(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "exit_code:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "exit_code:"))
		}
	}
	return ""
}
