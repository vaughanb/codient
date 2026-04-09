// Package a2aserver exposes codient as an A2A (Agent-to-Agent) server so that
// an orchestrating agent can discover its capabilities, delegate coding tasks,
// and receive results via the standard A2A protocol.
package a2aserver

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"codient/internal/agent"
	"codient/internal/agentlog"
	"codient/internal/config"
	"codient/internal/projectinfo"
	"codient/internal/prompt"
	"codient/internal/tools"
)

// Config holds everything the A2A server needs to handle incoming tasks.
type Config struct {
	Cfg *config.Config
	// LLMForMode returns the chat client for build, ask, or plan. Required for correct
	// per-mode API routing when models.* overrides use different base URLs.
	LLMForMode func(prompt.Mode) agent.ChatClient
	Log        *agentlog.Logger
	Version    string
	Addr       string
}

// New creates an http.Handler (ServeMux) that implements the A2A protocol.
// It serves the agent card at /.well-known/agent-card.json and handles
// JSON-RPC requests at /a2a.
func New(c Config) http.Handler {
	card := buildAgentCard(c.Addr, c.Version)
	exec := &executor{cfg: c.Cfg, llmForMode: c.LLMForMode, log: c.Log}
	caps := &a2a.AgentCapabilities{Streaming: true}
	handler := a2asrv.NewHandler(exec, a2asrv.WithCapabilityChecks(caps))

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/a2a", a2asrv.NewJSONRPCHandler(handler))
	return mux
}

func buildAgentCard(addr, version string) *a2a.AgentCard {
	endpoint := fmt.Sprintf("http://%s/a2a", addr)
	return &a2a.AgentCard{
		Name:        "codient",
		Description: "A coding agent that reads, searches, edits, and executes within a workspace.",
		Version:     version,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(endpoint, a2a.TransportProtocolJSONRPC),
		},
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "code-build",
				Name:        "Build",
				Description: "Implement features, fix bugs, and refactor code in a workspace. Has full read/write/exec access.",
				Tags:        []string{"code", "build", "implement", "refactor"},
				Examples:    []string{"Add input validation to the signup handler", "Refactor the database layer to use connection pooling"},
			},
			{
				ID:          "code-ask",
				Name:        "Ask",
				Description: "Answer questions about a codebase. Read-only: no file writes or command execution.",
				Tags:        []string{"code", "ask", "question", "explain"},
				Examples:    []string{"How does the authentication middleware work?", "Where is the database connection configured?"},
			},
			{
				ID:          "code-plan",
				Name:        "Plan",
				Description: "Create structured implementation designs. Read-only: produces a markdown plan without making changes.",
				Tags:        []string{"code", "plan", "design", "architecture"},
				Examples:    []string{"Design a caching layer for the API", "Plan the migration from REST to GraphQL"},
			},
		},
	}
}

// executor implements a2asrv.AgentExecutor.
type executor struct {
	cfg        *config.Config
	llmForMode func(prompt.Mode) agent.ChatClient
	log        *agentlog.Logger
}

var _ a2asrv.AgentExecutor = (*executor)(nil)

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		userPrompt := extractText(execCtx.Message)
		mode := resolveMode(execCtx.Metadata)

		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}

		if strings.TrimSpace(userPrompt) == "" {
			msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("empty prompt: message must contain text"))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, msg), nil)
			return
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		reg := registryForMode(e.cfg, mode)
		sysprompt := systemPromptForMode(e.cfg, reg, mode)

		if e.llmForMode == nil {
			msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("server misconfigured: LLMForMode is nil"))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, msg), nil)
			return
		}
		llm := e.llmForMode(mode)
		if llm == nil {
			msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("server misconfigured: no LLM client for mode"))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, msg), nil)
			return
		}

		runner := &agent.Runner{
			LLM:   llm,
			Cfg:   e.cfg,
			Tools: reg,
			Log:   e.log,
		}

		reply, _, _, err := runner.RunConversation(ctx, sysprompt, nil, userPrompt, nil)
		if err != nil {
			errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
			return
		}

		if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart(reply)), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

func (e *executor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// extractText concatenates text parts from an A2A message.
func extractText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range msg.Parts {
		if t, ok := p.Content.(a2a.Text); ok {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(string(t))
		}
	}
	return sb.String()
}

// resolveMode reads the "mode" key from request metadata, defaulting to build.
func resolveMode(meta map[string]any) prompt.Mode {
	if meta == nil {
		return prompt.ModeBuild
	}
	raw, ok := meta["mode"]
	if !ok {
		return prompt.ModeBuild
	}
	s, ok := raw.(string)
	if !ok {
		return prompt.ModeBuild
	}
	m, err := prompt.ParseMode(s)
	if err != nil {
		return prompt.ModeBuild
	}
	return m
}

func registryForMode(cfg *config.Config, mode prompt.Mode) *tools.Registry {
	ws := cfg.EffectiveWorkspace()
	netLimit := tools.NewNetworkLimiter(cfg.FetchWebRatePerSec, cfg.FetchWebRateBurst)
	fetch := fetchOpts(cfg, netLimit)
	search := searchOpts(cfg, netLimit)
	sgPath := cfg.AstGrep
	switch mode {
	case prompt.ModeAsk:
		return tools.DefaultReadOnly(ws, fetch, search, sgPath)
	case prompt.ModePlan:
		return tools.DefaultReadOnlyPlan(ws, fetch, search, sgPath)
	default:
		var execOpts *tools.ExecOptions
		if len(cfg.ExecAllowlist) > 0 {
			execOpts = &tools.ExecOptions{
				TimeoutSeconds: cfg.ExecTimeoutSeconds,
				MaxOutputBytes: cfg.ExecMaxOutputBytes,
				Allowlist:      cfg.ExecAllowlist,
			}
		}
		return tools.Default(ws, execOpts, fetch, search, sgPath)
	}
}

func fetchOpts(cfg *config.Config, netLimit *tools.RateLimiter) *tools.FetchOptions {
	opts := &tools.FetchOptions{
		AllowHosts:         append([]string(nil), cfg.FetchAllowHosts...),
		MaxBytes:           cfg.FetchMaxBytes,
		TimeoutSec:         cfg.FetchTimeoutSec,
		IncludePreapproved: cfg.FetchPreapproved,
		RateLimiter:        netLimit,
	}
	if len(opts.AllowHosts) == 0 && !opts.IncludePreapproved {
		return nil
	}
	return opts
}

func searchOpts(cfg *config.Config, netLimit *tools.RateLimiter) *tools.SearchOptions {
	if cfg.SearchBaseURL == "" {
		return nil
	}
	return &tools.SearchOptions{
		BaseURL:     cfg.SearchBaseURL,
		MaxResults:  cfg.SearchMaxResults,
		TimeoutSec:  30,
		RateLimiter: netLimit,
	}
}

func systemPromptForMode(cfg *config.Config, reg *tools.Registry, mode prompt.Mode) string {
	repoInstr, _ := prompt.LoadRepoInstructions(cfg.EffectiveWorkspace())
	projCtx := projectinfo.Detect(cfg.EffectiveWorkspace())
	return prompt.Build(prompt.Params{
		Cfg:              cfg,
		Reg:              reg,
		Mode:             mode,
		RepoInstructions: repoInstr,
		ProjectContext:   projCtx,
	})
}
