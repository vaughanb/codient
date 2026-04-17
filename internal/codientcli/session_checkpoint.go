package codientcli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"codient/internal/checkpoint"
	"codient/internal/config"
	"codient/internal/gitutil"
	"codient/internal/openaiclient"
	"codient/internal/planstore"
	"codient/internal/prompt"
	"codient/internal/sessionstore"
)

func (s *session) ensureCheckpointCommit(userLine string) error {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" || !gitutil.IsRepo(ws) || !s.cfg.GitAutoCommit || s.mode != prompt.ModeBuild {
		return nil
	}
	modified, err := gitutil.DiffFiles(ws)
	if err != nil {
		return err
	}
	untracked, err := gitutil.UntrackedFiles(ws)
	if err != nil {
		return err
	}
	if len(modified) == 0 && len(untracked) == 0 {
		return nil
	}
	if err := s.ensureCodientBranch(ws); err != nil {
		return err
	}
	paths := append(append([]string{}, modified...), untracked...)
	if err := gitutil.Add(ws, paths); err != nil {
		return err
	}
	subj, body := buildCodientCommitMessage(s.turn, userLine)
	if body == "" {
		body = "checkpoint snapshot"
	}
	return gitutil.Commit(ws, subj, body)
}

// createCheckpoint saves a named snapshot. If name is empty, uses turn-N.
func (s *session) createCheckpoint(name string, userCommitLine string) error {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return fmt.Errorf("no workspace set")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("turn-%d", s.turn)
	}
	if s.cfg.GitAutoCommit && s.mode == prompt.ModeBuild {
		if err := s.ensureCheckpointCommit(userCommitLine); err != nil {
			return fmt.Errorf("checkpoint commit: %w", err)
		}
	} else if s.cfg.GitAutoCommit && gitutil.IsRepo(ws) {
		mod, _ := gitutil.DiffFiles(ws)
		ut, _ := gitutil.UntrackedFiles(ws)
		if len(mod) > 0 || len(ut) > 0 {
			fmt.Fprintf(os.Stderr, "codient: warning: uncommitted changes — checkpoint records HEAD only (use build mode + git_auto_commit to snapshot files)\n")
		}
	}

	var gitSHA, gitBr string
	if gitutil.IsRepo(ws) {
		gitSHA, _ = gitutil.HeadSHA(ws)
		gitBr, _ = gitutil.CurrentBranch(ws)
	}
	var planJSON json.RawMessage
	if s.currentPlan != nil {
		var err error
		planJSON, err = planstore.EncodeJSON(s.currentPlan)
		if err != nil {
			return err
		}
	}
	if s.convBranch == "" {
		s.convBranch = "main"
	}
	cp := &checkpoint.Checkpoint{
		SessionID: s.sessionID,
		Name:      name,
		Turn:      s.turn,
		Messages:  sessionstore.FromOpenAI(s.history),
		Mode:      string(s.mode),
		Model:     s.cfg.Model,
		GitSHA:    gitSHA,
		GitBranch: gitBr,
		PlanPhase: string(s.planPhase),
		PlanJSON:  planJSON,
		ParentID:  s.currentCheckpointID,
		Branch:    s.convBranch,
	}
	if err := checkpoint.Save(ws, cp); err != nil {
		return err
	}
	s.currentCheckpointID = cp.ID
	s.autoSave()
	fmt.Fprintf(os.Stderr, "codient: checkpoint %q created (turn %d, %s)\n", name, s.turn, checkpoint.ShortSHA(gitSHA))
	return nil
}

func (s *session) listCheckpoints() error {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return fmt.Errorf("no workspace set")
	}
	out, err := checkpoint.RenderTree(ws, s.sessionID, s.currentCheckpointID)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, out)
	return nil
}

func (s *session) listConvBranches() error {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return fmt.Errorf("no workspace set")
	}
	idx, err := checkpoint.LoadTreeIndex(ws, s.sessionID)
	if err != nil {
		return err
	}
	cur := s.convBranch
	if cur == "" {
		cur = "main"
	}
	summ := checkpoint.SummarizeBranches(idx, cur)
	if len(summ) == 0 {
		fmt.Fprintf(os.Stderr, "codient: no checkpoint branches for this session\n")
		return nil
	}
	fmt.Fprintf(os.Stderr, "codient: conversation branches (session %s)\n", s.sessionID)
	for _, b := range summ {
		mark := " "
		if b.IsCurrent {
			mark = "*"
		}
		n := strings.TrimSpace(b.TipName)
		if n == "" {
			n = "(unnamed)"
		}
		fmt.Fprintf(os.Stderr, "  [%s] %q  tip turn %d  %s  id=%s\n", mark, b.Label, b.TipTurn, n, b.TipID)
	}
	return nil
}

func (s *session) resolveCheckpointQuery(query string) (*checkpoint.Checkpoint, error) {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return nil, fmt.Errorf("no workspace set")
	}
	matches, err := checkpoint.ResolveQuery(ws, s.sessionID, query)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no checkpoint matches %q", query)
	}
	if len(matches) > 1 {
		var b strings.Builder
		fmt.Fprintf(&b, "ambiguous checkpoint %q — matches:\n", query)
		for _, m := range matches {
			fmt.Fprintf(&b, "  %s  turn %d  name %q\n", m.ID, m.Turn, m.Name)
		}
		return nil, fmt.Errorf("%s", b.String())
	}
	return checkpoint.Load(ws, s.sessionID, matches[0].ID)
}

func (s *session) rollbackToCheckpoint(cp *checkpoint.Checkpoint) error {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return fmt.Errorf("no workspace set")
	}
	if cp == nil {
		return fmt.Errorf("nil checkpoint")
	}

	if gitutil.IsRepo(ws) && strings.TrimSpace(cp.GitSHA) != "" {
		if s.cfg.GitAutoCommit {
			stashed := gitutil.WorkingTreeDirty(ws)
			if stashed {
				if err := gitutil.StashPush(ws, "codient rollback safety"); err != nil {
					return fmt.Errorf("stash before rollback: %w", err)
				}
			}
			if err := gitutil.ResetHard(ws, cp.GitSHA); err != nil {
				return fmt.Errorf("git reset: %w", err)
			}
			if err := gitutil.CleanUntracked(ws); err != nil {
				return fmt.Errorf("git clean: %w", err)
			}
			if stashed {
				fmt.Fprintf(os.Stderr, "codient: workspace reset to %s (pre-rollback stash — run git stash pop to recover)\n", checkpoint.ShortSHA(cp.GitSHA))
			} else {
				fmt.Fprintf(os.Stderr, "codient: workspace reset to %s\n", checkpoint.ShortSHA(cp.GitSHA))
			}
		} else {
			fmt.Fprintf(os.Stderr, "codient: warning: git_auto_commit is off — conversation restored; working tree not reset (checkpoint at %s)\n", checkpoint.ShortSHA(cp.GitSHA))
		}
	}

	msgs, err := checkpoint.MessagesToOpenAI(cp.Messages)
	if err != nil {
		return err
	}
	s.history = msgs
	if m := strings.TrimSpace(cp.Model); m != "" {
		s.cfg.Model = m
	}
	mode := s.mode
	if m, err := prompt.ParseMode(cp.Mode); err == nil {
		mode = m
	}
	s.setMode(mode)
	s.client = openaiclient.New(s.cfg)
	s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory, effectiveAutoCheckCmd(s.cfg))
	config.SaveLastMode(string(s.mode))
	if s.mode == prompt.ModeBuild {
		s.warnIfNotGitRepo()
	}
	s.turn = cp.Turn
	if cp.PlanPhase != "" {
		s.planPhase = planstore.Phase(cp.PlanPhase)
	} else {
		s.planPhase = ""
	}
	if len(cp.PlanJSON) > 0 {
		var p planstore.Plan
		if err := json.Unmarshal(cp.PlanJSON, &p); err != nil {
			return fmt.Errorf("restore plan: %w", err)
		}
		s.currentPlan = &p
		if err := planstore.Save(&p); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
	} else {
		s.currentPlan = nil
	}
	s.currentCheckpointID = cp.ID
	if b := strings.TrimSpace(cp.Branch); b != "" {
		s.convBranch = b
	} else {
		s.convBranch = "main"
	}
	s.undoStack = nil
	s.lastReply = ""
	for i := len(s.history) - 1; i >= 0; i-- {
		b, _ := json.Marshal(s.history[i])
		if strings.Contains(string(b), `"role":"assistant"`) {
			s.lastReply = string(b)
			break
		}
	}
	s.autoSave()
	fmt.Fprintf(os.Stderr, "codient: rolled back to checkpoint %q (turn %d)\n", cp.Name, cp.Turn)
	return nil
}

func (s *session) forkFromCheckpoint(query string, branchArg string) error {
	cp, err := s.resolveCheckpointQuery(query)
	if err != nil {
		return err
	}
	if err := s.rollbackToCheckpoint(cp); err != nil {
		return err
	}
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" || !gitutil.IsRepo(ws) {
		fmt.Fprintf(os.Stderr, "codient: not a git repo — conversation forked only (branch label updated)\n")
		s.setForkBranchLabel(branchArg, cp.Turn)
		s.autoSave()
		return nil
	}
	slug := strings.TrimSpace(branchArg)
	if slug == "" {
		slug = fmt.Sprintf("fork-%d", cp.Turn)
	}
	slug = forkSlug(slug)
	base := "codient/" + slug
	for i := 0; i < 50; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		exists, err := gitutil.BranchExists(ws, name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := gitutil.CreateBranch(ws, name); err != nil {
			return err
		}
		if err := gitutil.CheckoutBranch(ws, name); err != nil {
			return err
		}
		s.convBranch = strings.TrimPrefix(name, "codient/")
		s.gitCodientCreatedBranch = name
		fmt.Fprintf(os.Stderr, "codient: forked from checkpoint onto git branch %q (conversation branch %q)\n", name, s.convBranch)
		s.autoSave()
		return nil
	}
	return fmt.Errorf("could not allocate a free branch name")
}

func (s *session) setForkBranchLabel(branchArg string, turn int) {
	slug := strings.TrimSpace(branchArg)
	if slug == "" {
		slug = fmt.Sprintf("fork-%d", turn)
	}
	s.convBranch = forkSlug(slug)
}

func forkSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '/':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "fork"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// maybeAutoCheckpointAfterBuildTurn creates a checkpoint when checkpoint_auto=all and the last turn changed files.
func (s *session) maybeAutoCheckpointAfterBuildTurn(commitLine string) {
	if s.cfg.CheckpointAuto != "all" {
		return
	}
	if !s.lastBuildTurnHadChanges || !s.lastTurnGitCommit {
		return
	}
	if err := s.createCheckpoint(fmt.Sprintf("turn-%d", s.turn), commitLine); err != nil {
		fmt.Fprintf(os.Stderr, "codient: auto-checkpoint: %v\n", err)
	}
}
