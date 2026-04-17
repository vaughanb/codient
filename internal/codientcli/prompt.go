package codientcli

import (
	"codient/internal/codeindex"
	"codient/internal/config"
	"codient/internal/prompt"
	"codient/internal/tools"
)

func fetchOptsFrom(cfg *config.Config, s *session, netLimit *tools.RateLimiter) *tools.FetchOptions {
	opts := &tools.FetchOptions{
		AllowHosts:         append([]string(nil), cfg.FetchAllowHosts...),
		MaxBytes:           cfg.FetchMaxBytes,
		TimeoutSec:         cfg.FetchTimeoutSec,
		IncludePreapproved: cfg.FetchPreapproved,
		RateLimiter:        netLimit,
	}
	interactive := s != nil && s.scanner != nil && stdinIsInteractive()
	if interactive {
		if s.fetchAllow == nil {
			s.fetchAllow = tools.NewSessionFetchAllow()
		}
		opts.Session = s.fetchAllow
		opts.PromptUnknownHost = s.fetchPromptUnknownHost
		opts.PersistFetchHost = s.persistFetchHostToConfig
	}
	if len(opts.AllowHosts) == 0 && opts.PromptUnknownHost == nil && !opts.IncludePreapproved {
		return nil
	}
	return opts
}

func searchOptsFrom(cfg *config.Config, netLimit *tools.RateLimiter) *tools.SearchOptions {
	return &tools.SearchOptions{
		MaxResults:  cfg.SearchMaxResults,
		TimeoutSec:  30,
		RateLimiter: netLimit,
	}
}

func buildRegistry(cfg *config.Config, mode prompt.Mode, s *session, memOpts *tools.MemoryOptions) *tools.Registry {
	netLimit := tools.NewNetworkLimiter(cfg.FetchWebRatePerSec, cfg.FetchWebRateBurst)
	fetch := fetchOptsFrom(cfg, s, netLimit)
	search := searchOptsFrom(cfg, netLimit)
	sgPath := cfg.AstGrep
	var idx *codeindex.Index
	if s != nil {
		idx = s.codeIndex
	}
	var reg *tools.Registry
	switch mode {
	case prompt.ModeAsk:
		reg = tools.DefaultReadOnly(cfg.EffectiveWorkspace(), fetch, search, sgPath, idx)
	case prompt.ModePlan:
		reg = tools.DefaultReadOnlyPlan(cfg.EffectiveWorkspace(), fetch, search, sgPath, idx)
	default:
		var execOpts *tools.ExecOptions
		if len(cfg.ExecAllowlist) > 0 {
			execOpts = &tools.ExecOptions{
				TimeoutSeconds: cfg.ExecTimeoutSeconds,
				MaxOutputBytes: cfg.ExecMaxOutputBytes,
			}
			if s != nil {
				execOpts.ProgressWriter = s.progressOut
			}
			if s != nil && s.execAllow != nil {
				execOpts.Session = s.execAllow
				if s.scanner != nil {
					execOpts.PromptOnDenied = s.execPromptDenied
				}
			} else {
				execOpts.Allowlist = cfg.ExecAllowlist
			}
		}
		reg = tools.Default(cfg.EffectiveWorkspace(), execOpts, fetch, search, sgPath, idx, memOpts)
	}
	if s != nil && mode == prompt.ModeBuild {
		tools.RegisterCreatePullRequest(reg, s.gitPullRequestContextFn())
	}
	if s != nil && s.mcpMgr != nil {
		tools.RegisterMCPTools(reg, s.mcpMgr)
	}
	// Register delegate_task for the interactive parent session only.
	// Sub-agent registries (built via agentfactory) never get this tool.
	if s != nil {
		tools.RegisterDelegateTask(reg, string(mode), s.delegateTaskFn())
	}
	return reg
}

// buildAgentSystemPrompt assembles the layered agent system message (tools, repo notes, -system).
func buildAgentSystemPrompt(cfg *config.Config, reg *tools.Registry, mode prompt.Mode, userSystem, repoInstructions, projectContext, memory string) string {
	return prompt.Build(prompt.Params{
		Cfg:                    cfg,
		Reg:                    reg,
		Mode:                   mode,
		UserSystem:             userSystem,
		RepoInstructions:       repoInstructions,
		ProjectContext:         projectContext,
		Memory:                 memory,
		AutoCheckBuildResolved: effectiveAutoCheckCmd(cfg),
		AutoCheckLintResolved:  effectiveLintCmd(cfg),
		AutoCheckTestResolved:  effectiveTestCmd(cfg),
	})
}
