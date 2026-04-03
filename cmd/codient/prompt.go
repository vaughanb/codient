package main

import (
	"codient/internal/config"
	"codient/internal/prompt"
	"codient/internal/tools"
)

func buildRegistry(cfg *config.Config, mode prompt.Mode) *tools.Registry {
	if mode == prompt.ModeAsk {
		return tools.DefaultReadOnly(cfg.EffectiveWorkspace())
	}
	if mode == prompt.ModePlan {
		return tools.DefaultReadOnlyPlan(cfg.EffectiveWorkspace())
	}
	var execOpts *tools.ExecOptions
	if len(cfg.ExecAllowlist) > 0 {
		execOpts = &tools.ExecOptions{
			Allowlist:      cfg.ExecAllowlist,
			TimeoutSeconds: cfg.ExecTimeoutSeconds,
			MaxOutputBytes: cfg.ExecMaxOutputBytes,
		}
	}
	return tools.Default(cfg.EffectiveWorkspace(), execOpts)
}

// buildAgentSystemPrompt assembles the layered agent system message (tools, repo notes, -system).
func buildAgentSystemPrompt(cfg *config.Config, reg *tools.Registry, mode prompt.Mode, userSystem, repoInstructions string) string {
	return prompt.Build(prompt.Params{
		Cfg:              cfg,
		Reg:              reg,
		Mode:             mode,
		UserSystem:       userSystem,
		RepoInstructions: repoInstructions,
	})
}
