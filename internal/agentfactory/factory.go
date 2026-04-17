// Package agentfactory builds tool registries and system prompts for a given mode.
// Shared by sub-agents, the A2A server, and any other non-interactive agent entry point.
package agentfactory

import (
	"fmt"
	"io"
	"os"
	"strings"

	"codient/internal/config"
	"codient/internal/projectinfo"
	"codient/internal/sandbox"
	"codient/internal/prompt"
	"codient/internal/repomap"
	"codient/internal/tools"
)

// RegistryForMode builds a tool registry for the given mode with no interactive
// session state (no exec prompts, no MCP, no code index). Suitable for
// sub-agents and the A2A server. rm may be nil (no repo_map tool or shared map).
func RegistryForMode(cfg *config.Config, mode prompt.Mode, rm *repomap.Map) *tools.Registry {
	ws := cfg.EffectiveWorkspace()
	netLimit := tools.NewNetworkLimiter(cfg.FetchWebRatePerSec, cfg.FetchWebRateBurst)
	fetch := FetchOpts(cfg, netLimit)
	search := SearchOpts(cfg, netLimit)
	sgPath := cfg.AstGrep
	switch mode {
	case prompt.ModeAsk:
		return tools.DefaultReadOnly(ws, fetch, search, sgPath, nil, rm)
	case prompt.ModePlan:
		return tools.DefaultReadOnlyPlan(ws, fetch, search, sgPath, nil, rm)
	default:
		var execOpts *tools.ExecOptions
		if len(cfg.ExecAllowlist) > 0 {
			execOpts = &tools.ExecOptions{
				TimeoutSeconds:       cfg.ExecTimeoutSeconds,
				MaxOutputBytes:       cfg.ExecMaxOutputBytes,
				Allowlist:            cfg.ExecAllowlist,
				EnvPassthrough:       append([]string(nil), cfg.ExecEnvPassthrough...),
				SandboxReadOnlyPaths: append([]string(nil), cfg.SandboxReadOnlyPaths...),
				WorkspaceRoot:        cfg.EffectiveWorkspace(),
				SandboxRunner: sandbox.SelectRunner(cfg.SandboxMode, sandbox.SelectOptions{
					ContainerImage: cfg.SandboxContainerImage,
				}),
			}
		}
		stateDir, _ := config.StateDir()
		var memOpts *tools.MemoryOptions
		if stateDir != "" || ws != "" {
			memOpts = &tools.MemoryOptions{
				StateDir:      stateDir,
				WorkspaceRoot: ws,
			}
		}
		return tools.Default(ws, execOpts, fetch, search, sgPath, nil, rm, memOpts)
	}
}

// SystemPromptForMode assembles the system prompt for a non-interactive agent
// (sub-agent or A2A). repoMapText is optional pre-rendered repository map prose
// (same as the interactive REPL injects when repomap is enabled). errWriter receives warnings; pass os.Stderr or io.Discard.
func SystemPromptForMode(cfg *config.Config, reg *tools.Registry, mode prompt.Mode, repoMapText string, errWriter io.Writer) string {
	if errWriter == nil {
		errWriter = os.Stderr
	}
	repoInstr, err := prompt.LoadRepoInstructions(cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(errWriter, "codient: repo instructions: %v\n", err)
	}
	projCtx := projectinfo.Detect(cfg.EffectiveWorkspace())
	stateDir, err := config.StateDir()
	if err != nil {
		fmt.Fprintf(errWriter, "codient: state dir: %v\n", err)
	}
	mem, err := prompt.LoadMemory(stateDir, cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(errWriter, "codient: memory: %v\n", err)
	}
	return prompt.Build(prompt.Params{
		Cfg:              cfg,
		Reg:              reg,
		Mode:             mode,
		RepoInstructions: repoInstr,
		ProjectContext:   projCtx,
		RepoMap:          strings.TrimSpace(repoMapText),
		Memory:           mem,
	})
}

// FetchOpts builds non-interactive fetch options from config.
func FetchOpts(cfg *config.Config, netLimit *tools.RateLimiter) *tools.FetchOptions {
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

// SearchOpts builds non-interactive search options from config.
func SearchOpts(cfg *config.Config, netLimit *tools.RateLimiter) *tools.SearchOptions {
	return &tools.SearchOptions{
		MaxResults:  cfg.SearchMaxResults,
		TimeoutSec:  30,
		RateLimiter: netLimit,
	}
}
