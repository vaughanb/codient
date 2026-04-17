package codientcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/agent"
	"codient/internal/agentlog"
	"codient/internal/assistout"
	"codient/internal/astgrep"
	"codient/internal/codeindex"
	"codient/internal/config"
	"codient/internal/designstore"
	"codient/internal/gitutil"
	"codient/internal/hooks"
	"codient/internal/imageutil"
	"codient/internal/mcpclient"
	"codient/internal/openaiclient"
	"codient/internal/planstore"
	"codient/internal/projectinfo"
	"codient/internal/prompt"
	"codient/internal/selfupdate"
	"codient/internal/sessionstore"
	"codient/internal/slashcmd"
	"codient/internal/subagent"
	"codient/internal/tokentracker"
	"codient/internal/tools"
)

type session struct {
	cfg              *config.Config
	client           *openaiclient.Client
	registry         *tools.Registry
	agentLog         *agentlog.Logger
	progressOut      io.Writer
	mode             prompt.Mode
	systemPrompt     string
	richOutput       bool
	streamReply      bool
	designSaveDir    string
	goal             string
	taskFile         string
	userSystem       string
	repoInstructions string
	projectContext   string
	memory           string               // cross-session memory (global + workspace), loaded at startup
	memOpts          *tools.MemoryOptions // passed to build-mode registry for memory_update tool

	// REPL state
	history   []openai.ChatCompletionMessageParamUnion
	sessionID string
	turn      int
	lastReply string
	taskSlug  string

	undoStack []undoEntry // per-turn undo records (most recent at end)

	// Git workflow (build mode + git repo)
	gitSessionStartCommit   string // HEAD at session capture; undo-all resets here when git_auto_commit is on
	gitSessionStartBranch   string
	gitMergeTargetBranch    string // protected branch left when auto-creating codient/* (PR merge base)
	gitCodientCreatedBranch string // branch codient created from a protected branch, if any
	gitBranchEnsured        bool   // lazy auto-branch has run once this session
	lastBuildTurnHadChanges bool   // last pushUndoIfChanged saw file changes
	lastTurnGitCommit       bool   // last turn successfully created a codient auto-commit

	// stdinScanner is set for the interactive REPL; used for exec allow prompts.
	scanner   *bufio.Scanner
	execAllow *tools.SessionExecAllow // mutable run_command allowlist for this process; nil if exec disabled

	fetchAllow    *tools.SessionFetchAllow // mutable fetch_url host approvals for this process; nil until first fetch
	fetchPromptMu sync.Mutex               // serializes fetch allow prompts and post-lock re-checks

	// replPromptMu serializes the REPL prompt (no trailing newline) and async stderr lines
	// (e.g. semantic index completion) so messages do not append to the same line as the prompt.
	replPromptMu sync.Mutex
	// replSkipFirstLoopPrompt is set when replAsyncStderrNote redraws the prompt before the first
	// readUserInput; the first loop iteration skips a duplicate "\n" + prompt pair.
	replSkipFirstLoopPrompt bool
	// replInputActive is true while the REPL is blocking on readUserInput. When set,
	// replAsyncStderrNote defers messages to pendingAsyncNotes instead of printing
	// immediately, avoiding visual corruption of the user's input line.
	replInputActive    bool
	pendingAsyncNotes []string

	codeIndex *codeindex.Index // semantic search index; nil when embedding_model is not configured

	mcpMgr *mcpclient.Manager // MCP server connections; nil when no mcp_servers configured

	// Plan lifecycle state (non-nil when a structured plan is active).
	currentPlan *planstore.Plan
	planPhase   planstore.Phase

	// Checkpoint tree: last created/restored snapshot id and logical branch label ("main", fork slugs).
	currentCheckpointID string
	convBranch          string

	// tokenTracker accumulates API-reported token usage for the REPL session.
	tokenTracker *tokentracker.Tracker

	// Headless (-print): single-turn automation; outputFormat is text|json|stream-json.
	printMode    bool
	outputFormat string
	autoApprove  AutoApprovePolicy
	maxTurns     int
	maxCostUSD   float64

	// Multimodal: images from -image (first turn) or /image (next message).
	initialImages []imageutil.ImageAttachment
	pendingImages []imageutil.ImageAttachment

	// hooksMgr is loaded when hooks_enabled is true; nil otherwise.
	hooksMgr *hooks.Manager

	// tui is non-nil when the Bubble Tea split-screen TUI is active.
	// All stdout/stderr writes go through pipes into the TUI viewport,
	// and user input arrives through tui.inputCh instead of os.Stdin.
	tui *tuiSetup
}

type undoEntry struct {
	modifiedFiles []string // tracked files modified during this turn (restore via git checkout)
	createdFiles  []string // untracked files created during this turn (delete)
	historyLen    int      // len(s.history) before this turn started
	commitSHA     string   // non-empty when git_auto_commit recorded this turn as a commit
}

// setMode updates the session mode and notifies the TUI if active.
// All mode assignments should go through this method to keep the TUI in sync.
func (s *session) setMode(m prompt.Mode) {
	s.mode = m
	if s.tui != nil {
		s.tui.prog.Send(tuiModeMsg(string(m)))
	}
}

func (s *session) newRunner() *agent.Runner {
	s.client = openaiclient.New(s.cfg)
	r := &agent.Runner{
		LLM: s.client, Cfg: s.cfg, Tools: s.registry,
		Log: s.agentLog, Progress: s.progressOut,
		ProgressPlain: s.cfg.Plain,
		ProgressMode:  string(s.mode),
		Tracker:       s.tokenTracker,
		Hooks:         s.hooksMgr,
	}
	if s.printMode {
		if s.maxTurns > 0 {
			r.MaxTurns = s.maxTurns
		}
		if s.maxCostUSD > 0 {
			r.MaxCostUSD = s.maxCostUSD
			r.EstimateSessionCost = func(u tokentracker.Usage) (float64, bool) {
				return s.estimateCostForUsage(u)
			}
		}
	}
	if s.mode == prompt.ModeBuild {
		steps := buildAutoCheckSteps(s.cfg)
		if len(steps) > 0 {
			sec := autoCheckTimeoutSec(s.cfg)
			r.AutoCheck = makeAutoCheckSequence(s.cfg.EffectiveWorkspace(), steps, time.Duration(sec)*time.Second, s.cfg.ExecMaxOutputBytes, s.progressOut)
		}
	}
	return r
}

// delegateTaskFn returns the callback used by the delegate_task tool to run sub-agents.
func (s *session) delegateTaskFn() tools.DelegateRunner {
	return func(ctx context.Context, modeStr, task, extraContext string) (string, error) {
		mode, err := prompt.ParseMode(modeStr)
		if err != nil {
			return "", err
		}
		progress := s.progressOut
		if progress != nil {
			progress = newPrefixWriter([]byte("  │ "), progress)
		}
		res, err := subagent.Run(ctx, subagent.RunParams{
			Cfg:      s.cfg,
			Mode:     mode,
			Task:     task,
			Context:  extraContext,
			Log:      s.agentLog,
			Progress: progress,
			Tracker:  s.tokenTracker,
		})
		if err != nil {
			return "", err
		}
		return res.Reply, nil
	}
}

func (s *session) executeTurn(ctx context.Context, runner *agent.Runner, user openai.ChatCompletionMessageParamUnion) (reply string, err error) {
	if err := s.cfg.RequireModel(); err != nil {
		return "", err
	}
	if s.hooksMgr != nil {
		s.hooksMgr.NextTurn()
		up, herr := s.hooksMgr.RunUserPromptSubmit(ctx, agent.UserMessageText(user))
		if herr != nil {
			return "", herr
		}
		if up.Blocked {
			return "", fmt.Errorf("%s", up.Reason)
		}
	}
	if s.tokenTracker != nil {
		s.tokenTracker.MarkTurnStart()
	}
	if !s.printMode {
		fmt.Fprint(os.Stderr, "\n")
	}
	if !s.printMode || s.outputFormat == "text" {
		writePlanDraftPreamble(os.Stdout, s.mode, s.lastReply)
	}
	stdoutTTY := assistout.StdoutIsInteractive()
	if s.printMode && s.outputFormat != "text" {
		stdoutTTY = false
	}
	streamTo := streamWriterForTurn(s.streamReply, stdoutTTY, s.mode, s.richOutput, s.lastReply)

	var spinMu sync.Mutex
	var curStopSpin func()

	startSpin := func() {
		spinMu.Lock()
		defer spinMu.Unlock()
		if curStopSpin != nil {
			curStopSpin()
		}
		if s.tui != nil {
			s.tui.prog.Send(tuiWorkingMsg(true))
		}
		curStopSpin = startWorkingSpinner(os.Stderr)
	}
	stopSpinFn := func() {
		spinMu.Lock()
		defer spinMu.Unlock()
		if curStopSpin != nil {
			curStopSpin()
			curStopSpin = nil
		}
		if s.tui != nil {
			s.tui.prog.Send(tuiWorkingMsg(false))
		}
	}

	startSpin()
	defer stopSpinFn()

	runner.OnWorkingChange = func(working bool) {
		if working {
			startSpin()
		} else {
			stopSpinFn()
		}
	}
	defer func() { runner.OnWorkingChange = nil }()

	if s.mode == prompt.ModeAsk {
		runner.PostReplyCheck = makePostReplyCheck(s, runner.Progress)
	}
	if streamTo != nil {
		streamTo = &spinStopWriter{w: streamTo, stop: stopSpinFn}
	}

	reply, newHist, streamed, runErr := runner.RunConversation(ctx, s.systemPrompt, s.history, user, streamTo)
	if runErr != nil {
		return "", runErr
	}
	s.history = newHist
	out := io.Writer(os.Stdout)
	if s.printMode && (s.outputFormat == "json" || s.outputFormat == "stream-json") {
		out = io.Discard
	}
	if err := finishAssistantTurn(out, reply, s.richOutput, s.mode == prompt.ModePlan, streamed); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	s.printTurnTokenSummary()
	return reply, nil
}

// postReplyVerificationPrompt is injected in Ask mode when the assistant reply looks like
// an actionable multi-item suggestion list. It asks for a quick tool-grounded pass without
// imposing a rigid output template.
const postReplyVerificationPrompt = `Your last reply looked like a list of suggestions or concrete changes. Before we treat it as final, do one short verification pass against this workspace using grep, read_file, or list_dir as needed:

- Drop or revise anything already addressed in the repo (say briefly what you checked).
- For anything still relevant, note the strongest evidence (tool name + what you searched/read + what you found). Keep it concise.

Answer in normal prose. Do not use a fixed "## Verified Suggestions" section or numbered report template unless you truly need it for clarity.`

// postReplyGateSystem asks for a single YES/NO: does the assistant reply warrant
// the post-reply verification pass (concrete codebase change proposals)?
const postReplyGateSystem = `You classify whether a follow-up verification step is needed.

Reply with exactly YES or NO as the first word of your response (then you may add a short phrase if needed).

Answer YES only if the assistant's reply primarily proposes or argues concrete changes to the user's own project or repository (edits, refactors, new files, config changes, what they should implement).

Answer NO if the reply is mainly: summarizing external pages or search results; listing numbered links or citations; quoting documentation; answering factual questions; describing third-party software; or checklist/status formatting without prescribing repo edits.`

// makePostReplyCheck returns a PostReplyCheck function for Ask mode.
// It uses a cheap LLM gate after list-shaped heuristics: only when the model
// says the reply warrants verification do we inject the verification prompt.
func makePostReplyCheck(s *session, progress io.Writer) func(context.Context, agent.PostReplyCheckInfo) string {
	return func(ctx context.Context, info agent.PostReplyCheckInfo) string {
		if !looksLikeSuggestionList(info.Reply) {
			return ""
		}
		if skipSuggestionVerifyForResearchTurn(info) {
			return ""
		}
		if progress != nil {
			if line := agent.FormatStatusProgressLine(s.cfg.Plain, string(s.mode), "checking whether verification is needed…"); line != "" {
				fmt.Fprintf(progress, "%s\n", line)
			}
		}
		want, err := postReplyGateWantsVerification(ctx, s.client, s.tokenTracker, info)
		if err != nil || !want {
			return ""
		}
		return postReplyVerificationPrompt
	}
}

func postReplyGateWantsVerification(ctx context.Context, client *openaiclient.Client, tr *tokentracker.Tracker, info agent.PostReplyCheckInfo) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("nil client")
	}
	user := buildPostReplyGateUserMessage(info)
	params := openai.ChatCompletionNewParams{
		Model:               shared.ChatModel(client.Model()),
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(postReplyGateSystem), openai.UserMessage(user)},
		Temperature:         openai.Float(0),
		MaxCompletionTokens: openai.Int(24),
	}
	res, err := client.ChatCompletion(ctx, params)
	if err != nil {
		return false, err
	}
	if tr != nil {
		tr.Add(tokentracker.Usage{
			PromptTokens:     res.Usage.PromptTokens,
			CompletionTokens: res.Usage.CompletionTokens,
			TotalTokens:      res.Usage.TotalTokens,
		})
	}
	if len(res.Choices) == 0 {
		return false, nil
	}
	content := ""
	if c := res.Choices[0].Message.Content; c != "" {
		content = c
	}
	return parsePostReplyGateAnswer(content), nil
}

const postReplyGateMaxReplyChars = 12000

func buildPostReplyGateUserMessage(info agent.PostReplyCheckInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "User message (this turn):\n%s\n\n", strings.TrimSpace(info.User))
	if len(info.TurnTools) > 0 {
		fmt.Fprintf(&b, "Tools used this turn: %s\n\n", strings.Join(info.TurnTools, ", "))
	} else {
		fmt.Fprintf(&b, "Tools used this turn: (none)\n\n")
	}
	reply := strings.TrimSpace(info.Reply)
	if len(reply) > postReplyGateMaxReplyChars {
		reply = reply[:postReplyGateMaxReplyChars] + "\n…[truncated]"
	}
	fmt.Fprintf(&b, "Assistant reply:\n%s\n", reply)
	return b.String()
}

// parsePostReplyGateAnswer returns true when the gate model affirms verification.
// Only the first word of the first line is considered (strict YES/NO).
func parsePostReplyGateAnswer(content string) bool {
	line := strings.TrimSpace(strings.Split(content, "\n")[0])
	line = strings.Trim(line, "\"'`*_")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToUpper(strings.TrimRight(fields[0], ".!,:;"))
	if strings.HasPrefix(first, "YES") {
		return true
	}
	if strings.HasPrefix(first, "NO") {
		return false
	}
	return false
}

// skipSuggestionVerifyForResearchTurn skips the DISPROVE-suggestions pass when
// the turn was web research (web_search, no file mutations) and the user did
// not ask for codebase change suggestions — list-shaped answers are usually
// citations or summaries, not actionable repo proposals.
func skipSuggestionVerifyForResearchTurn(info agent.PostReplyCheckInfo) bool {
	if !slices.Contains(info.TurnTools, "web_search") {
		return false
	}
	for _, n := range info.TurnTools {
		if agent.ToolIsMutating(n) {
			return false
		}
	}
	return !userIntentSuggestsCodeChanges(info.User)
}

func userIntentSuggestsCodeChanges(u string) bool {
	u = strings.ToLower(u)
	phrases := []string{
		"suggest", "recommend", "refactor", "codebase", "our repo", "this repo",
		"our code", "this code", "this project", "should we ", "code review",
		"review our", "review the code", "improve our", "improve the code",
		"apply to", "change we ", "in this codebase",
	}
	for _, p := range phrases {
		if strings.Contains(u, p) {
			return true
		}
	}
	return false
}

// looksLikeSuggestionList returns true when reply contains 3+ lines that look
// like an actionable suggestion list. It avoids false positives from typical
// web-search formatting: `- [title](url)` link rows and `## Section` headers
// without numbering.
func looksLikeSuggestionList(s string) bool {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if suggestionListLine(trimmed) {
			count++
		}
		if count >= 3 {
			return true
		}
	}
	return false
}

func suggestionListLine(trimmed string) bool {
	if isMarkdownLinkBullet(trimmed) {
		return false
	}
	// Checklist / status bullets (common in web-search summaries) are not "action items".
	if strings.ContainsAny(trimmed, "✅✔☑✓") {
		return false
	}
	if trimmed[0] == '-' || trimmed[0] == '*' {
		return true
	}
	if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
		return isNumberedMarkdownHeading(trimmed)
	}
	if len(trimmed) >= 2 && trimmed[0] >= '1' && trimmed[0] <= '9' && (trimmed[1] == '.' || (len(trimmed) >= 3 && trimmed[1] >= '0' && trimmed[1] <= '9' && trimmed[2] == '.')) {
		return true
	}
	return false
}

func isMarkdownLinkBullet(line string) bool {
	if len(line) < 2 {
		return false
	}
	if line[0] != '-' && line[0] != '*' {
		return false
	}
	rest := strings.TrimSpace(line[1:])
	return strings.HasPrefix(rest, "[")
}

// isNumberedMarkdownHeading is true for "## 1. Title" / "### 2) Foo" but not
// "## Background" or "### See also".
func isNumberedMarkdownHeading(line string) bool {
	var rest string
	switch {
	case strings.HasPrefix(line, "### "):
		rest = strings.TrimPrefix(line, "### ")
	case strings.HasPrefix(line, "## "):
		rest = strings.TrimPrefix(line, "## ")
	default:
		return false
	}
	rest = strings.TrimSpace(rest)
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	if i < len(rest) && (rest[i] == '.' || rest[i] == ')') {
		return true
	}
	return false
}

func (s *session) warnIfNotGitRepo() {
	if s.mode != prompt.ModeBuild {
		return
	}
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return
	}
	if !gitutil.IsRepo(ws) {
		fmt.Fprintf(os.Stderr, "codient: workspace is not a git repository — changes cannot be undone via git\n")
	}
}

func (s *session) captureSnapshot() (modified, untracked []string) {
	ws := s.cfg.EffectiveWorkspace()
	if s.mode != prompt.ModeBuild || ws == "" || !gitutil.IsRepo(ws) {
		return nil, nil
	}
	modified, _ = gitutil.DiffFiles(ws)
	untracked, _ = gitutil.UntrackedFiles(ws)
	return modified, untracked
}

// userMessageForTurn builds the API user message from text, optional @image: paths,
// and images queued via -image (first turn only) or /image.
func (s *session) userMessageForTurn(text string) (openai.ChatCompletionMessageParamUnion, string, error) {
	var attach []imageutil.ImageAttachment
	attach = append(attach, s.pendingImages...)
	s.pendingImages = nil
	if s.turn == 0 {
		attach = append(s.initialImages, attach...)
		s.initialImages = nil
	}
	msg, err := buildUserMessage(s.cfg.EffectiveWorkspace(), text, attach)
	if err != nil {
		return openai.ChatCompletionMessageParamUnion{}, "", err
	}
	line := agent.UserMessageText(msg)
	if strings.TrimSpace(line) == "" && len(attach) > 0 {
		line = "[image]"
	}
	return msg, line, nil
}

func (s *session) runSingleTurn(ctx context.Context, user string, extra []imageutil.ImageAttachment) int {
	if s.mcpMgr != nil {
		defer s.mcpMgr.Close()
	}
	wsEarly := s.cfg.EffectiveWorkspace()
	if strings.TrimSpace(s.sessionID) == "" {
		s.sessionID = sessionstore.NewID(wsEarly)
	}
	if hm, herr := hooks.LoadForConfig(s.cfg.HooksEnabled, wsEarly, s.cfg.Model, s.sessionID); herr != nil {
		fmt.Fprintf(os.Stderr, "codient: hooks: %v\n", herr)
	} else {
		s.hooksMgr = hm
	}
	defer func() {
		if s.hooksMgr != nil {
			s.hooksMgr.RunSessionEnd(context.Background())
		}
	}()
	s.warnIfNotGitRepo()
	if !s.printMode {
		s.probeAndSetContext(ctx)
		assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
			Plain:               s.cfg.Plain,
			Quiet:               s.cfg.Quiet,
			Repl:                false,
			Mode:                string(s.mode),
			Workspace:           s.cfg.EffectiveWorkspace(),
			Model:               s.cfg.Model,
			Version:             Version,
			ContextWindowTokens: s.cfg.ContextWindowTokens,
			EmbeddingModel:      s.cfg.EmbeddingModel,
		})
	}
	if s.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", s.cfg.EffectiveWorkspace(), s.mode, strings.Join(s.registry.Names(), ", "))
	}
	if s.hooksMgr != nil {
		add, herr := s.hooksMgr.RunSessionStart(ctx, hooks.SessionStartup)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "codient: hooks SessionStart: %v\n", herr)
		} else if strings.TrimSpace(add) != "" {
			s.systemPrompt += "\n\n# Hook context (SessionStart)\n" + add
		}
	}
	rawUser := user
	user, err := applyTaskToFirstTurnIfNeeded(0, user, s.goal, s.taskFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task: %v\n", err)
		return 2
	}
	msg, err := buildUserMessage(s.cfg.EffectiveWorkspace(), user, extra)
	if err != nil {
		fmt.Fprintf(os.Stderr, "image: %v\n", err)
		return 2
	}
	runner := s.newRunner()
	reply, err := s.executeTurn(ctx, runner, msg)
	if s.printMode {
		if err == nil {
			maybeSaveDesign(os.Stderr, s.cfg.EffectiveWorkspace(), s.designSaveDir, s.sessionID, s.mode, reply, designstore.TaskSlug(s.goal, s.taskFile, rawUser), s.cfg.DesignSave)
			s.showGitDiffIfBuild()
		}
		return s.finishHeadlessTurn(reply, err)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	maybeSaveDesign(os.Stderr, s.cfg.EffectiveWorkspace(), s.designSaveDir, s.sessionID, s.mode, reply, designstore.TaskSlug(s.goal, s.taskFile, rawUser), s.cfg.DesignSave)
	s.showGitDiffIfBuild()
	return 0
}

// maybePromptUpdate checks for a newer release and interactively asks the user
// whether to install it. Skipped versions are persisted so the user is not
// asked again until an even newer release appears.
func (s *session) maybePromptUpdate(sc *bufio.Scanner) {
	if s.cfg.Quiet || !s.cfg.UpdateNotify {
		return
	}
	stateDir, _ := config.StateDir()
	tag, err := selfupdate.LatestVersion()
	if err != nil || !selfupdate.IsNewer(Version, tag) {
		return
	}
	if skipped := selfupdate.LoadSkippedVersion(stateDir); skipped == tag {
		return
	}
	newVer := strings.TrimPrefix(tag, "v")
	fmt.Fprintf(os.Stderr, "codient: update available %s -> %s\n", Version, newVer)
	fmt.Fprintf(os.Stderr, "Install now? [Y/n] ")
	answer := ""
	if sc.Scan() {
		answer = strings.TrimSpace(sc.Text())
	}
	if answer == "" || strings.HasPrefix(strings.ToLower(answer), "y") {
		fmt.Fprintf(os.Stderr, "codient: downloading %s...\n", newVer)
		if err := selfupdate.Apply(tag); err != nil {
			fmt.Fprintf(os.Stderr, "codient: update failed: %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "codient: updated to %s — restarting...\n", newVer)
		if err := selfupdate.Restart(); err != nil {
			fmt.Fprintf(os.Stderr, "codient: restart failed: %v — please restart codient manually\n", err)
			os.Exit(0)
		}
	}
	if err := selfupdate.SaveSkippedVersion(stateDir, tag); err == nil {
		fmt.Fprintf(os.Stderr, "codient: skipped %s (won't ask again for this version)\n", newVer)
	}
}

// runSession is the main persistent REPL loop with slash commands and session persistence.
func (s *session) runSession(ctx context.Context, initialPrompt string, newSession bool) int {
	ws := s.cfg.EffectiveWorkspace()

	var resumeSummary string
	// Load or create session.
	if !newSession && ws != "" {
		if existing, err := sessionstore.LoadLatest(ws); err == nil && existing != nil {
			msgs, err := sessionstore.ToOpenAI(existing.Messages)
			if err == nil {
				s.history = msgs
				s.sessionID = existing.ID
				mode, modeErr := prompt.ParseMode(existing.Mode)
				if modeErr == nil && mode != s.mode {
					s.setMode(mode)
					s.registry = buildRegistry(s.cfg, mode, s, s.memOpts)
					s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
				}
				if existing.PlanPhase != "" {
					s.planPhase = planstore.Phase(existing.PlanPhase)
				}
				if existing.CurrentCheckpointID != "" {
					s.currentCheckpointID = existing.CurrentCheckpointID
				}
				if b := strings.TrimSpace(existing.CurrentBranch); b != "" {
					s.convBranch = b
				} else {
					s.convBranch = "main"
				}
				s.loadPlanFromDisk()
				resumeSummary = sessionstore.ResumeSummaryLine(s.sessionID, existing.Messages)
				if s.currentPlan != nil && s.planPhase != "" && s.planPhase != planstore.PhaseDone {
					resumeSummary += " · plan: " + string(s.planPhase)
				}
			}
		}
	}
	if s.sessionID == "" {
		s.sessionID = sessionstore.NewID(ws)
	}
	if s.convBranch == "" {
		s.convBranch = "main"
	}

	if hm, herr := hooks.LoadForConfig(s.cfg.HooksEnabled, ws, s.cfg.Model, s.sessionID); herr != nil {
		fmt.Fprintf(os.Stderr, "codient: hooks: %v\n", herr)
	} else {
		s.hooksMgr = hm
	}
	defer func() {
		if s.hooksMgr != nil {
			s.hooksMgr.RunSessionEnd(context.Background())
		}
	}()

	config.SaveLastMode(string(s.mode))

	s.captureGitSessionState(ws)
	s.warnIfNotGitRepo()

	var sc *bufio.Scanner
	if s.tui != nil {
		sc = bufio.NewScanner(&chanReader{ch: s.tui.input.ch})
	} else {
		sc = bufio.NewScanner(os.Stdin)
		enableBracketedPaste()
		defer disableBracketedPaste()
	}
	s.scanner = sc
	resolveAstGrep(s.cfg, sc)
	s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)

	if strings.TrimSpace(s.cfg.Model) == "" {
		s.runSetupWizard(ctx, sc)
		s.client = openaiclient.New(s.cfg)
		s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	}

	s.probeAndSetContext(ctx)

	if s.hooksMgr != nil {
		src := hooks.SessionStartup
		if strings.TrimSpace(resumeSummary) != "" {
			src = hooks.SessionResume
		}
		add, herr := s.hooksMgr.RunSessionStart(ctx, src)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "codient: hooks SessionStart: %v\n", herr)
		} else if strings.TrimSpace(add) != "" {
			s.systemPrompt += "\n\n# Hook context (SessionStart)\n" + add
		}
	}

	assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
		Plain:               s.cfg.Plain,
		Quiet:               s.cfg.Quiet,
		Repl:                true,
		Mode:                string(s.mode),
		Workspace:           ws,
		Model:               s.cfg.Model,
		ResumeSummary:       resumeSummary,
		Version:             Version,
		ContextWindowTokens: s.cfg.ContextWindowTokens,
		EmbeddingModel:      s.cfg.EmbeddingModel,
	})
	if s.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", ws, s.mode, strings.Join(s.registry.Names(), ", "))
	}
	fmt.Fprintf(os.Stderr, "%s\n", assistout.ModeHint(s.cfg.Plain, string(s.mode)))

	if s.mcpMgr != nil {
		defer s.mcpMgr.Close()
	}

	// Print before startCodeIndex: the index goroutine may redraw the REPL prompt via
	// replAsyncStderrNote; any later stderr line without a leading newline would append
	// to that prompt line (e.g. "codient: type /help" glued to "[ask] > ").
	fmt.Fprintf(os.Stderr, "codient: type /help for commands, /exit to quit\n")

	s.startCodeIndex(ctx)

	if s.currentPlan != nil && s.planPhase != "" && s.planPhase != planstore.PhaseDone {
		s.handlePlanResume(ctx, sc)
	}

	s.maybePromptUpdate(sc)

	// Register slash commands.
	cmds := s.buildSlashCommands(ctx, sc)

	runner := s.newRunner()

	// Execute initial prompt if provided.
	if seed := strings.TrimSpace(initialPrompt); seed != "" {
		if s.taskSlug == "" {
			s.taskSlug = designstore.TaskSlug(s.goal, s.taskFile, seed)
		}
		user, err := applyTaskToFirstTurnIfNeeded(s.turn, seed, s.goal, s.taskFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "task: %v\n", err)
			return 2
		}
		userMsg, commitLine, err := s.userMessageForTurn(user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "image: %v\n", err)
			return 2
		}
		preModified, preUntracked := s.captureSnapshot()
		histLen := len(s.history)
		s.turn++
		reply, err := s.executeTurn(ctx, runner, userMsg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 1
		}
		s.pushUndoIfChanged(preModified, preUntracked, histLen, commitLine)
		maybeSaveDesign(os.Stderr, ws, s.designSaveDir, s.sessionID, s.mode, reply, s.taskSlug, s.cfg.DesignSave)
		s.lastReply = assistout.PrepareAssistantText(reply, s.mode == prompt.ModePlan)
		s.autoSave()
		s.maybeAutoCompact(ctx)
		s.showGitDiffIfBuild()
	}
	done := false
	firstReplTurn := true
	for !done {
		if s.tui == nil {
			s.replPrintPromptForTurn(firstReplTurn)
			s.replSetInputActive()
		}
		firstReplTurn = false
		line, ok := readUserInput(sc)
		if s.tui == nil {
			s.replFlushPendingNotes()
		}
		if !ok {
			break
		}
		if s.tui != nil && line != "" {
			fmt.Fprintf(os.Stderr, "\n%s%s\n", assistout.SessionPrompt(s.cfg.Plain, string(s.mode)), line)
		}
		if line == "" {
			continue
		}

		// Check for slash command.
		if cmd, args, ok := cmds.Parse(line); ok {
			if cmd == nil {
				fmt.Fprintf(os.Stderr, "codient: unknown command %q — type /help for available commands\n", strings.SplitN(line, " ", 2)[0])
				continue
			}
			if err := cmd.Run(args); err != nil {
				fmt.Fprintf(os.Stderr, "codient: %s: %v\n", cmd.Name, err)
			}
			// Rebuild the runner after mode/config changes.
			runner = s.newRunner()
			if cmd.Name == "exit" {
				done = true
			}
			continue
		}

		// Normal user message -> execute turn.
		user, err := applyTaskToFirstTurnIfNeeded(s.turn, line, s.goal, s.taskFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "task: %v\n", err)
			return 2
		}
		if s.taskSlug == "" {
			s.taskSlug = designstore.TaskSlug(s.goal, s.taskFile, line)
		}
		userMsg, commitLine, err := s.userMessageForTurn(user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "image: %v\n", err)
			return 2
		}
		preModified, preUntracked := s.captureSnapshot()
		histLen := len(s.history)
		s.turn++
		reply, err := s.executeTurn(ctx, runner, userMsg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 1
		}
		s.pushUndoIfChanged(preModified, preUntracked, histLen, commitLine)
		maybeSaveDesign(os.Stderr, ws, s.designSaveDir, s.sessionID, s.mode, reply, s.taskSlug, s.cfg.DesignSave)
		s.lastReply = assistout.PrepareAssistantText(reply, s.mode == prompt.ModePlan)
		s.autoSave()
		s.maybeAutoCompact(ctx)
		s.showGitDiffIfBuild()

		if s.mode == prompt.ModePlan {
			designText := assistout.PrepareAssistantText(reply, true)
			s.updatePlanFromReply(designText, line)
			if designstore.LooksLikeReadyToImplement(designText) {
				runner = s.offerPlanHandoff(ctx, sc, runner, designText)
			}
		}
	}

	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin: %v\n", err)
		return 2
	}
	return 0
}

// writeREPLPromptUnlocked writes the current REPL prompt (or plan answer prefix) to stderr.
// Caller must hold replPromptMu when used together with replAsyncStderrNote.
func (s *session) writeREPLPromptUnlocked() {
	if s.mode == prompt.ModePlan && assistout.ReplySignalsPlanWait(s.lastReply) {
		fmt.Fprint(os.Stderr, assistout.PlanAnswerPrefix(s.cfg.Plain))
	} else {
		fmt.Fprint(os.Stderr, assistout.SessionPrompt(s.cfg.Plain, string(s.mode)))
	}
}

// replPrintPromptForTurn prints the REPL prompt line. isFirstTurn is true only for the first
// iteration of the main REPL loop; async stderr notes may set replSkipFirstLoopPrompt so we
// do not repeat a prompt already drawn before the first readUserInput.
func (s *session) replPrintPromptForTurn(isFirstTurn bool) {
	s.replPromptMu.Lock()
	defer s.replPromptMu.Unlock()
	if isFirstTurn && s.replSkipFirstLoopPrompt {
		s.replSkipFirstLoopPrompt = false
		return
	}
	if !isFirstTurn {
		s.replSkipFirstLoopPrompt = false // drop flag if async set it after the first prompt
		fmt.Fprint(os.Stderr, "\n")
	}
	s.writeREPLPromptUnlocked()
}

// replAsyncStderrNote prints a full line (or lines) to stderr while the REPL may be showing
// a prompt without a trailing newline. When the REPL is blocking on user input
// (replInputActive), the message is deferred to avoid corrupting the input line;
// otherwise it moves to a new line, prints msg, then redraws the prompt.
func (s *session) replAsyncStderrNote(msg string) {
	if msg == "" {
		return
	}
	// In TUI mode the viewport handles display; just print the message.
	if s.tui != nil {
		fmt.Fprint(os.Stderr, msg)
		if !strings.HasSuffix(msg, "\n") {
			fmt.Fprint(os.Stderr, "\n")
		}
		return
	}
	s.replPromptMu.Lock()
	defer s.replPromptMu.Unlock()
	if s.replInputActive {
		s.pendingAsyncNotes = append(s.pendingAsyncNotes, msg)
		return
	}
	fmt.Fprint(os.Stderr, "\n")
	fmt.Fprint(os.Stderr, msg)
	if !strings.HasSuffix(msg, "\n") {
		fmt.Fprint(os.Stderr, "\n")
	}
	s.writeREPLPromptUnlocked()
	s.replSkipFirstLoopPrompt = true
}

// replSetInputActive marks the REPL as blocking on stdin so that async notes
// are deferred rather than printed immediately.
func (s *session) replSetInputActive() {
	s.replPromptMu.Lock()
	s.replInputActive = true
	s.replPromptMu.Unlock()
}

// replFlushPendingNotes clears the input-active flag and prints any async notes
// that were deferred while the user was typing. Safe to call even when no notes
// are pending.
func (s *session) replFlushPendingNotes() {
	s.replPromptMu.Lock()
	defer s.replPromptMu.Unlock()
	s.replInputActive = false
	for _, msg := range s.pendingAsyncNotes {
		fmt.Fprint(os.Stderr, msg)
		if !strings.HasSuffix(msg, "\n") {
			fmt.Fprint(os.Stderr, "\n")
		}
	}
	s.pendingAsyncNotes = nil
}

// offerPlanHandoff prompts the user with a structured approval dialog after a plan
// is finalized. On approval, it switches to build mode and executes from the
// structured plan. On rejection, it injects feedback and continues planning.
// Returns the updated runner.
func (s *session) offerPlanHandoff(ctx context.Context, sc *bufio.Scanner, runner *agent.Runner, designText string) *agent.Runner {
	if s.currentPlan == nil {
		s.updatePlanFromReply(designText, "")
	}
	plan := s.currentPlan
	plan.Phase = planstore.PhaseAwaitingApproval
	s.planPhase = planstore.PhaseAwaitingApproval
	if err := planstore.Save(plan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}

	decision := s.promptApproval(sc, plan)

	switch decision.Action {
	case "approve":
		recordApproval(plan, "approve", decision.Feedback)
		plan.Phase = planstore.PhaseApproved
		s.planPhase = planstore.PhaseApproved
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		if err := s.executeFromPlan(ctx, plan); err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		}
		return s.newRunner()

	case "reject":
		recordApproval(plan, "reject", decision.Feedback)
		plan.Phase = planstore.PhaseDraft
		s.planPhase = planstore.PhaseDraft
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		feedback := "[Plan rejected."
		if decision.Feedback != "" {
			feedback += " Feedback: " + decision.Feedback + "."
		}
		feedback += " Please revise the plan and address the feedback.]"
		s.history = append(s.history, openai.UserMessage(feedback))
		fmt.Fprintf(os.Stderr, "codient: plan rejected — continuing in plan mode\n")
		return runner

	case "edit":
		plan.Phase = planstore.PhaseDraft
		s.planPhase = planstore.PhaseDraft
		fmt.Fprintf(os.Stderr, "codient: plan edited — continuing in plan mode\n")
		return runner

	default:
		plan.Phase = planstore.PhaseDraft
		s.planPhase = planstore.PhaseDraft
		if err := planstore.Save(plan); err != nil {
			fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "codient: continuing in plan mode\n")
		return runner
	}
}

func (s *session) autoSave() {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return
	}
	state := &sessionstore.SessionState{
		ID:        s.sessionID,
		Workspace: ws,
		Mode:      string(s.mode),
		Model:     s.cfg.Model,
		Messages:  sessionstore.FromOpenAI(s.history),
	}
	if s.currentCheckpointID != "" {
		state.CurrentCheckpointID = s.currentCheckpointID
	}
	if s.convBranch != "" {
		state.CurrentBranch = s.convBranch
	}
	if s.planPhase != "" {
		state.PlanPhase = string(s.planPhase)
		state.PlanPath = planstore.Path(ws, s.sessionID)
	}
	if err := sessionstore.Save(state); err != nil {
		fmt.Fprintf(os.Stderr, "codient: session save: %v\n", err)
	}
	config.SaveLastMode(string(s.mode))
}

func (s *session) buildSlashCommands(ctx context.Context, sc *bufio.Scanner) *slashcmd.Registry {
	cmds := &slashcmd.Registry{}

	cmds.Register(slashcmd.Command{
		Name:        "build",
		Aliases:     []string{"b"},
		Description: "switch to build mode (full write tools)",
		Run:         func(string) error { s.switchMode(prompt.ModeBuild); return nil },
	})
	cmds.Register(slashcmd.Command{
		Name:        "plan",
		Aliases:     []string{"p", "design", "d"},
		Description: "switch to plan mode (read-only, structured implementation design)",
		Run:         func(string) error { s.switchMode(prompt.ModePlan); return nil },
	})
	cmds.Register(slashcmd.Command{
		Name:        "ask",
		Aliases:     []string{"a"},
		Description: "switch to ask mode (read-only Q&A)",
		Run:         func(string) error { s.switchMode(prompt.ModeAsk); return nil },
	})
	cmds.Register(slashcmd.Command{
		Name:        "help",
		Aliases:     []string{"h", "?"},
		Description: "show available commands",
		Run: func(string) error {
			fmt.Fprint(os.Stderr, cmds.Help())
			fmt.Fprint(os.Stderr, "\nTip: end a line with \\ for multiline input. Pasting multiline text is also supported.\n")
			fmt.Fprint(os.Stderr, "Images: /image path.png attaches to your next message; or use @image:path in text; or codient -image path.png …\n")
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "hooks",
		Description: "list configured lifecycle hooks (requires hooks_enabled)",
		Run: func(string) error {
			if s.hooksMgr == nil || s.hooksMgr.IsEmpty() {
				fmt.Fprintf(os.Stderr, "No hooks loaded. Set hooks_enabled=true in /config and add ~/.codient/hooks.json or <workspace>/.codient/hooks.json\n")
				return nil
			}
			desc := s.hooksMgr.ListDescriptors()
			if len(desc) == 0 {
				fmt.Fprintf(os.Stderr, "hooks.json loaded but no command hooks are configured.\n")
				return nil
			}
			var cur string
			for _, d := range desc {
				if d.Event != cur {
					if cur != "" {
						fmt.Fprint(os.Stderr, "\n")
					}
					cur = d.Event
					fmt.Fprintf(os.Stderr, "[%s]\n", d.Event)
				}
				m := d.Matcher
				if strings.TrimSpace(m) == "" {
					m = "(all)"
				}
				src := d.SourcePath
				if src == "" {
					src = "?"
				}
				fmt.Fprintf(os.Stderr, "  matcher %q  timeout %ds  %s\n    %s\n", m, d.TimeoutSec, filepath.Base(src), d.Command)
			}
			fmt.Fprint(os.Stderr, "\n")
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "image",
		Usage:       "/image <path>",
		Description: "attach an image (PNG, JPEG, GIF, WebP) to your next message",
		Run: func(args string) error {
			path := strings.TrimSpace(args)
			if path == "" {
				return fmt.Errorf("usage: /image <path-to-image>")
			}
			a, err := imageutil.LoadImage(path, imageutil.DefaultMaxBytes)
			if err != nil {
				return err
			}
			if a.OrigBytes >= imageutil.WarnLargeBytes {
				fmt.Fprintf(os.Stderr, "codient: warning: large image %q (%d bytes)\n", path, a.OrigBytes)
			}
			s.pendingImages = append(s.pendingImages, a)
			fmt.Fprintf(os.Stderr, "codient: attached %q for next message (%d image(s) pending)\n", filepath.Base(path), len(s.pendingImages))
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "config",
		Usage:       "/config [key] [value]",
		Description: "view or set configuration (no args = show all, key = show one, key value = set and save)",
		Run:         func(args string) error { return s.handleConfig(ctx, args) },
	})
	cmds.Register(slashcmd.Command{
		Name:        "setup",
		Description: "guided setup wizard for API connection, chat model, and optional embedding model for semantic search",
		Run: func(string) error {
			s.runSetupWizard(ctx, sc)
			s.client = openaiclient.New(s.cfg)
			s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
			s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
			s.cfg.ContextWindowTokens = 0
			s.probeAndSetContext(ctx)
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "exit",
		Aliases:     []string{"quit", "q"},
		Description: "quit the session",
		Run:         func(string) error { return nil },
	})
	cmds.Register(slashcmd.Command{
		Name:        "clear",
		Description: "reset conversation history (same session)",
		Run: func(string) error {
			s.history = nil
			s.lastReply = ""
			s.turn = 0
			s.undoStack = nil
			s.currentCheckpointID = ""
			s.convBranch = "main"
			s.currentPlan = nil
			s.planPhase = ""
			s.pendingImages = nil
			if s.tokenTracker != nil {
				s.tokenTracker.Reset()
			}
			fmt.Fprintf(os.Stderr, "codient: history cleared\n")
			s.autoSave()
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "new",
		Aliases:     []string{"n"},
		Description: "start a brand new session (fresh ID, history, and saved-design namespace)",
		Run: func(string) error {
			ws := s.cfg.EffectiveWorkspace()
			s.sessionID = sessionstore.NewID(ws)
			s.history = nil
			s.lastReply = ""
			s.turn = 0
			s.taskSlug = ""
			s.undoStack = nil
			s.currentCheckpointID = ""
			s.convBranch = "main"
			s.currentPlan = nil
			s.planPhase = ""
			s.pendingImages = nil
			if s.tokenTracker != nil {
				s.tokenTracker.Reset()
			}
			if len(s.cfg.ExecAllowlist) > 0 {
				s.execAllow = tools.NewSessionExecAllow(s.cfg.ExecAllowlist)
				s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
				s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
			}
			s.captureGitSessionState(s.cfg.EffectiveWorkspace())
			fmt.Fprintf(os.Stderr, "codient: new session %s\n", s.sessionID)
			fmt.Fprintf(os.Stderr, "%s\n", assistout.ModeHint(s.cfg.Plain, string(s.mode)))
			s.autoSave()
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "status",
		Description: "show current session state",
		Run: func(string) error {
			fmt.Fprintf(os.Stderr, "  session:   %s\n", s.sessionID)
			fmt.Fprintf(os.Stderr, "  mode:      %s\n", s.mode)
			fmt.Fprintf(os.Stderr, "  model:     %s\n", s.cfg.Model)
			fmt.Fprintf(os.Stderr, "  workspace: %s\n", s.cfg.EffectiveWorkspace())
			fmt.Fprintf(os.Stderr, "  turns:     %d\n", s.turn)
			usage := s.estimateFullContextUsage()
			if s.cfg.ContextWindowTokens > 0 {
				pct := usage * 100 / s.cfg.ContextWindowTokens
				fmt.Fprintf(os.Stderr, "  context:   ~%d / %d tokens (%d%%)\n", usage, s.cfg.ContextWindowTokens, pct)
			} else {
				fmt.Fprintf(os.Stderr, "  context:   ~%d tokens (no window limit set)\n", usage)
			}
			fmt.Fprintf(os.Stderr, "  messages:  %d\n", len(s.history))
			if b := effectiveAutoCheckCmd(s.cfg); b != "" {
				fmt.Fprintf(os.Stderr, "  auto-check (build): %s\n", b)
			} else {
				fmt.Fprintf(os.Stderr, "  auto-check (build): off\n")
			}
			if l := effectiveLintCmd(s.cfg); l != "" {
				fmt.Fprintf(os.Stderr, "  auto-check (lint):  %s\n", l)
			} else {
				fmt.Fprintf(os.Stderr, "  auto-check (lint):  off\n")
			}
			if t := effectiveTestCmd(s.cfg); t != "" {
				fmt.Fprintf(os.Stderr, "  auto-check (test):  %s\n", t)
			} else {
				fmt.Fprintf(os.Stderr, "  auto-check (test):  off\n")
			}
			if s.execAllow != nil {
				if s.execAllow.AllowAll() {
					fmt.Fprintf(os.Stderr, "  exec:      all commands allowed for this session\n")
				}
			}
			if ps := s.planStatusLine(); ps != "" {
				fmt.Fprintf(os.Stderr, "  plan:      %s\n", ps)
			}
			if extra := s.formatCostStatusLine(); extra != "" {
				fmt.Fprint(os.Stderr, extra)
			}
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "cost",
		Aliases:     []string{"tokens"},
		Description: "show session token usage and estimated cost",
		Run: func(string) error {
			s.printCostCommand()
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "tools",
		Description: "list tools available in current mode",
		Run: func(string) error {
			names := s.registry.Names()
			fmt.Fprintf(os.Stderr, "Tools (%s mode): %s\n", s.mode, strings.Join(names, ", "))
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "mcp",
		Usage:       "/mcp [server]",
		Description: "list MCP servers and tools (no args = all servers, server = tools for that server)",
		Run: func(args string) error {
			if s.mcpMgr == nil {
				fmt.Fprintf(os.Stderr, "No MCP servers configured. Add mcp_servers to ~/.codient/config.json.\n")
				return nil
			}
			args = strings.TrimSpace(args)
			if args == "" {
				ids := s.mcpMgr.ServerIDs()
				if len(ids) == 0 {
					fmt.Fprintf(os.Stderr, "No MCP servers connected.\n")
					return nil
				}
				fmt.Fprintf(os.Stderr, "MCP servers (%d connected):\n", len(ids))
				for _, id := range ids {
					tt := s.mcpMgr.ServerTools(id)
					fmt.Fprintf(os.Stderr, "  %s (%d tools)\n", id, len(tt))
				}
				return nil
			}
			tt := s.mcpMgr.ServerTools(args)
			if tt == nil {
				fmt.Fprintf(os.Stderr, "MCP server %q not connected.\n", args)
				return nil
			}
			fmt.Fprintf(os.Stderr, "MCP %s (%d tools):\n", args, len(tt))
			for _, t := range tt {
				fmt.Fprintf(os.Stderr, "  %-30s %s\n", t.Name, t.Description)
			}
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "plan-status",
		Aliases:     []string{"ps"},
		Description: "show current plan phase, steps, and approval state",
		Run: func(string) error {
			if s.currentPlan == nil {
				fmt.Fprintf(os.Stderr, "codient: no active plan\n")
				return nil
			}
			fmt.Fprint(os.Stderr, planstore.RenderMarkdown(s.currentPlan))
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "compact",
		Description: "summarize conversation history to save context space",
		Run: func(string) error {
			return s.compactHistory(ctx)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "model",
		Usage:       "/model <name>",
		Description: "switch to a different model",
		Run: func(args string) error {
			name := strings.TrimSpace(args)
			if name == "" {
				fmt.Fprintf(os.Stderr, "current model: %s\n", s.cfg.Model)
				return nil
			}
			return s.handleConfig(ctx, "model "+name)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "workspace",
		Usage:       "/workspace <path>",
		Description: "change the workspace directory",
		Run: func(args string) error {
			path := strings.TrimSpace(args)
			if path == "" {
				fmt.Fprintf(os.Stderr, "current workspace: %s\n", s.cfg.EffectiveWorkspace())
				return nil
			}
			s.cfg.Workspace = path
			s.projectContext = projectinfo.Detect(s.cfg.EffectiveWorkspace())
			s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
			s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
			s.captureGitSessionState(s.cfg.EffectiveWorkspace())
			s.warnIfNotGitRepo()
			fmt.Fprintf(os.Stderr, "codient: workspace set to %s\n", path)
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "log",
		Usage:       "/log [path]",
		Description: "show or change the log file path",
		Run: func(args string) error {
			path := strings.TrimSpace(args)
			if path == "" {
				if s.agentLog != nil {
					fmt.Fprintf(os.Stderr, "logging is active\n")
				} else {
					fmt.Fprintf(os.Stderr, "logging is off (use /log <path> to enable)\n")
				}
				return nil
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}
			s.agentLog = agentlog.New(f)
			fmt.Fprintf(os.Stderr, "codient: logging to %s\n", path)
			return nil
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "undo",
		Usage:       "/undo [all]",
		Description: "undo last build turn (or /undo all to revert everything)",
		Run: func(args string) error {
			ws := s.cfg.EffectiveWorkspace()
			if ws == "" {
				return fmt.Errorf("no workspace set")
			}
			if !gitutil.IsRepo(ws) {
				return fmt.Errorf("workspace is not a git repository")
			}

			if strings.TrimSpace(args) == "all" {
				return s.undoAll(ws)
			}
			return s.undoLast(ws)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "diff",
		Usage:       "/diff [path]",
		Description: "show colored git diff vs HEAD (optional file path under workspace)",
		Run: func(args string) error {
			return s.handleDiff(args)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "branch",
		Usage:       "/branch [name]",
		Description: "show current branch, switch to an existing branch, or create and checkout a new branch",
		Run: func(args string) error {
			return s.handleBranch(args)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "pr",
		Usage:       "/pr [draft]",
		Description: "push current branch and open a GitHub pull request (requires gh CLI)",
		Run: func(args string) error {
			return s.handlePR(args)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "checkpoint",
		Aliases:     []string{"cp"},
		Usage:       "/checkpoint [name]",
		Description: "save a named snapshot of conversation + workspace (default name turn-N)",
		Run: func(args string) error {
			return s.createCheckpoint(strings.TrimSpace(args), args)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "checkpoints",
		Aliases:     []string{"cps"},
		Description: "list checkpoints for this session (tree view)",
		Run: func(string) error {
			return s.listCheckpoints()
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "rollback",
		Aliases:     []string{"rb"},
		Usage:       "/rollback <name|id|turn>",
		Description: "restore conversation and workspace to a checkpoint",
		Run: func(args string) error {
			q := strings.TrimSpace(args)
			if q == "" {
				return fmt.Errorf("usage: /rollback <name|id|turn>")
			}
			cp, err := s.resolveCheckpointQuery(q)
			if err != nil {
				return err
			}
			return s.rollbackToCheckpoint(cp)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "fork",
		Usage:       "/fork <name|id|turn> [branch-name]",
		Description: "rollback to a checkpoint and start a new git branch + conversation branch",
		Run: func(args string) error {
			parts := strings.Fields(strings.TrimSpace(args))
			if len(parts) < 1 {
				return fmt.Errorf("usage: /fork <name|id|turn> [branch-name]")
			}
			branch := ""
			if len(parts) > 1 {
				branch = strings.Join(parts[1:], " ")
			}
			return s.forkFromCheckpoint(parts[0], branch)
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "branches",
		Aliases:     []string{"cbranch"},
		Description: "list logical conversation branches (checkpoint forks)",
		Run: func(string) error {
			return s.listConvBranches()
		},
	})
	cmds.Register(slashcmd.Command{
		Name:        "memory",
		Aliases:     []string{"mem"},
		Usage:       "/memory [show|edit|clear [global|workspace]]",
		Description: "view, edit, or clear cross-session memory files",
		Run: func(args string) error {
			return s.handleMemory(args)
		},
	})

	return cmds
}

func (s *session) handleMemory(args string) error {
	parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
	sub := strings.ToLower(parts[0])
	subArg := ""
	if len(parts) > 1 {
		subArg = strings.TrimSpace(parts[1])
	}

	stateDir := ""
	if s.memOpts != nil {
		stateDir = s.memOpts.StateDir
	}
	ws := s.cfg.EffectiveWorkspace()

	switch sub {
	case "", "show":
		if s.memory == "" {
			fmt.Fprintf(os.Stderr, "codient: no cross-session memory loaded\n")
			if stateDir != "" {
				fmt.Fprintf(os.Stderr, "  global:    %s\n", prompt.GlobalMemoryPath(stateDir))
			}
			if ws != "" {
				fmt.Fprintf(os.Stderr, "  workspace: %s\n", prompt.WorkspaceMemoryPath(ws))
			}
			return nil
		}
		fmt.Fprintf(os.Stderr, "%s\n", s.memory)
		return nil

	case "edit":
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		scope := strings.ToLower(subArg)
		if scope == "" {
			scope = "workspace"
		}
		var path string
		switch scope {
		case "global":
			if stateDir == "" {
				return fmt.Errorf("global state directory not configured")
			}
			path = prompt.GlobalMemoryPath(stateDir)
		case "workspace":
			if ws == "" {
				return fmt.Errorf("no workspace set")
			}
			path = prompt.WorkspaceMemoryPath(ws)
		default:
			return fmt.Errorf("unknown scope %q; use \"global\" or \"workspace\"", scope)
		}
		if editor == "" {
			fmt.Fprintf(os.Stderr, "codient: $EDITOR not set; edit manually:\n  %s\n", path)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "codient: opening %s in %s\n", path, editor)
		return s.runEditor(editor, path)

	case "clear":
		scope := strings.ToLower(subArg)
		if scope == "" {
			return fmt.Errorf("specify scope: /memory clear global or /memory clear workspace")
		}
		var path string
		switch scope {
		case "global":
			if stateDir == "" {
				return fmt.Errorf("global state directory not configured")
			}
			path = prompt.GlobalMemoryPath(stateDir)
		case "workspace":
			if ws == "" {
				return fmt.Errorf("no workspace set")
			}
			path = prompt.WorkspaceMemoryPath(ws)
		default:
			return fmt.Errorf("unknown scope %q; use \"global\" or \"workspace\"", scope)
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "codient: %s does not exist\n", path)
				return nil
			}
			return err
		}
		fmt.Fprintf(os.Stderr, "codient: removed %s\n", path)
		s.reloadMemory()
		return nil

	case "reload":
		s.reloadMemory()
		fmt.Fprintf(os.Stderr, "codient: memory reloaded\n")
		return nil

	default:
		return fmt.Errorf("unknown subcommand %q; use show, edit, clear, or reload", sub)
	}
}

func (s *session) reloadMemory() {
	stateDir := ""
	if s.memOpts != nil {
		stateDir = s.memOpts.StateDir
	}
	mem, err := prompt.LoadMemory(stateDir, s.cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: reload memory: %v\n", err)
		return
	}
	s.memory = mem
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
}

func (s *session) runEditor(editor, path string) error {
	argv := []string{editor, path}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}
	s.reloadMemory()
	return nil
}

func (s *session) handleConfig(ctx context.Context, args string) error {
	parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
	key := strings.ToLower(parts[0])
	value := ""
	if len(parts) > 1 {
		value = strings.TrimSpace(parts[1])
	}

	if key == "" {
		s.printAllConfig()
		return nil
	}

	if value == "" {
		return s.printOneConfig(key)
	}

	if err := s.setConfig(key, value); err != nil {
		return err
	}

	if err := saveCurrentConfig(s.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "codient: %s set to %q (saved)\n", key, value)

	switch key {
	case "model", "base_url", "api_key", "max_concurrent":
		s.client = openaiclient.New(s.cfg)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
		if key == "model" || key == "base_url" {
			s.cfg.ContextWindowTokens = 0
			s.probeAndSetContext(ctx)
		}
	case "fetch_allow_hosts", "fetch_preapproved", "fetch_max_bytes", "fetch_timeout_sec",
		"fetch_web_rate_per_sec", "fetch_web_rate_burst",
		"search_max_results":
		s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	case "autocheck_cmd", "lint_cmd", "test_cmd":
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	case "embedding_model":
		s.startCodeIndex(ctx)
	case "hooks_enabled":
		ws := s.cfg.EffectiveWorkspace()
		if strings.TrimSpace(s.sessionID) == "" {
			s.sessionID = sessionstore.NewID(ws)
		}
		if hm, herr := hooks.LoadForConfig(s.cfg.HooksEnabled, ws, s.cfg.Model, s.sessionID); herr != nil {
			fmt.Fprintf(os.Stderr, "codient: hooks reload: %v\n", herr)
		} else {
			s.hooksMgr = hm
		}
	}

	if mode, _, ok := parseModeConfigKey(key); ok && mode == string(s.mode) {
		s.client = openaiclient.NewForMode(s.cfg, mode)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	}
	return nil
}

func (s *session) printAllConfig() {
	masked := s.cfg.APIKey
	if len(masked) > 4 {
		masked = masked[:4] + strings.Repeat("*", len(masked)-4)
	}
	w := os.Stderr
	fmt.Fprintf(w, "  -- Connection --\n")
	fmt.Fprintf(w, "  base_url:              %s\n", s.cfg.BaseURL)
	fmt.Fprintf(w, "  api_key:               %s\n", masked)
	fmt.Fprintf(w, "  model:                 %s\n", s.cfg.Model)
	fmt.Fprintf(w, "\n  -- Defaults --\n")
	fmt.Fprintf(w, "  mode:                  %s\n", s.cfg.Mode)
	fmt.Fprintf(w, "  workspace:             %s\n", s.cfg.Workspace)
	fmt.Fprintf(w, "\n  -- Agent limits --\n")
	fmt.Fprintf(w, "  max_concurrent:        %d\n", s.cfg.MaxConcurrent)
	fmt.Fprintf(w, "\n  -- Exec --\n")
	fmt.Fprintf(w, "  exec_allowlist:        %s\n", strings.Join(s.cfg.ExecAllowlist, ","))
	fmt.Fprintf(w, "  exec_timeout_sec:      %d\n", s.cfg.ExecTimeoutSeconds)
	fmt.Fprintf(w, "  exec_max_output_bytes: %d\n", s.cfg.ExecMaxOutputBytes)
	fmt.Fprintf(w, "\n  -- Context --\n")
	fmt.Fprintf(w, "  context_window:        %d\n", s.cfg.ContextWindowTokens)
	fmt.Fprintf(w, "  context_reserve:       %d\n", s.cfg.ContextReserveTokens)
	fmt.Fprintf(w, "\n  -- LLM --\n")
	fmt.Fprintf(w, "  max_llm_retries:       %d\n", s.cfg.MaxLLMRetries)
	fmt.Fprintf(w, "  stream_with_tools:     %v\n", s.cfg.StreamWithTools)
	fmt.Fprintf(w, "\n  -- Fetch --\n")
	fmt.Fprintf(w, "  fetch_allow_hosts:     %s\n", strings.Join(s.cfg.FetchAllowHosts, ","))
	fmt.Fprintf(w, "  fetch_preapproved:     %v\n", s.cfg.FetchPreapproved)
	fmt.Fprintf(w, "  fetch_max_bytes:       %d\n", s.cfg.FetchMaxBytes)
	fmt.Fprintf(w, "  fetch_timeout_sec:     %d\n", s.cfg.FetchTimeoutSec)
	fmt.Fprintf(w, "  fetch_web_rate_per_sec: %d\n", s.cfg.FetchWebRatePerSec)
	fmt.Fprintf(w, "  fetch_web_rate_burst:   %d\n", s.cfg.FetchWebRateBurst)
	fmt.Fprintf(w, "\n  -- Search --\n")
	fmt.Fprintf(w, "  search_max_results:    %d\n", s.cfg.SearchMaxResults)
	fmt.Fprintf(w, "\n  -- Auto --\n")
	fmt.Fprintf(w, "  autocompact_threshold: %d\n", s.cfg.AutoCompactPct)
	fmt.Fprintf(w, "  autocheck_cmd:         %s\n", s.cfg.AutoCheckCmd)
	fmt.Fprintf(w, "  lint_cmd:              %s\n", s.cfg.LintCmd)
	fmt.Fprintf(w, "  test_cmd:              %s\n", s.cfg.TestCmd)
	fmt.Fprintf(w, "\n  -- Git (build mode) --\n")
	fmt.Fprintf(w, "  git_auto_commit:       %v\n", s.cfg.GitAutoCommit)
	fmt.Fprintf(w, "  git_protected_branches: %s\n", strings.Join(s.cfg.GitProtectedBranches, ","))
	fmt.Fprintf(w, "  checkpoint_auto:       %s (plan|all|off)\n", s.cfg.CheckpointAuto)
	fmt.Fprintf(w, "\n  -- UI/Output --\n")
	fmt.Fprintf(w, "  plain:                 %v\n", s.cfg.Plain)
	fmt.Fprintf(w, "  quiet:                 %v\n", s.cfg.Quiet)
	fmt.Fprintf(w, "  verbose:               %v\n", s.cfg.Verbose)
	fmt.Fprintf(w, "  log:                   %s\n", s.cfg.LogPath)
	fmt.Fprintf(w, "  stream_reply:          %v\n", s.cfg.StreamReply)
	fmt.Fprintf(w, "  progress:              %v\n", s.cfg.Progress)
	fmt.Fprintf(w, "\n  -- Plan --\n")
	fmt.Fprintf(w, "  design_save_dir:       %s\n", s.cfg.DesignSaveDir)
	fmt.Fprintf(w, "  design_save:           %v\n", s.cfg.DesignSave)
	fmt.Fprintf(w, "\n  -- Project --\n")
	fmt.Fprintf(w, "  project_context:       %s\n", s.cfg.ProjectContext)
	fmt.Fprintf(w, "\n  -- Tools --\n")
	astGrepDisplay := s.cfg.AstGrep
	if astGrepDisplay == "" {
		astGrepDisplay = "(not installed)"
	}
	fmt.Fprintf(w, "  ast_grep:              %s\n", astGrepDisplay)
	embModel := s.cfg.EmbeddingModel
	if embModel == "" {
		embModel = "(not configured)"
	}
	fmt.Fprintf(w, "  embedding_model:       %s\n", embModel)
	fmt.Fprintf(w, "  hooks_enabled:         %v\n", s.cfg.HooksEnabled)
	fmt.Fprintf(w, "\n  -- Cost estimate --\n")
	if s.cfg.CostPerMTok != nil {
		fmt.Fprintf(w, "  cost_per_mtok:         %g %g (input output USD per 1M)\n", s.cfg.CostPerMTok.Input, s.cfg.CostPerMTok.Output)
	} else {
		fmt.Fprintf(w, "  cost_per_mtok:         (built-in table; set two numbers to override)\n")
	}
	fmt.Fprintf(w, "\n  -- Per-mode model overrides --\n")
	for _, mode := range []string{"plan", "build", "ask"} {
		ov := s.cfg.ModeModels[mode]
		base, key, model := ov.BaseURL, ov.APIKey, ov.Model
		if base == "" && key == "" && model == "" {
			fmt.Fprintf(w, "  %s:                    (inherits top-level)\n", mode)
			continue
		}
		if base == "" {
			base = "(inherit)"
		}
		maskedKey := key
		if len(maskedKey) > 4 {
			maskedKey = maskedKey[:4] + strings.Repeat("*", len(maskedKey)-4)
		}
		if maskedKey == "" {
			maskedKey = "(inherit)"
		}
		if model == "" {
			model = "(inherit)"
		}
		fmt.Fprintf(w, "  %s_base_url:           %s\n", mode, base)
		fmt.Fprintf(w, "  %s_api_key:            %s\n", mode, maskedKey)
		fmt.Fprintf(w, "  %s_model:              %s\n", mode, model)
	}
	fmt.Fprintf(w, "\nSet a value: /config <key> <value>\n")
}

func (s *session) printOneConfig(key string) error {
	v, ok := s.getConfigValue(key)
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	fmt.Fprintf(os.Stderr, "  %s: %s\n", key, v)
	return nil
}

func (s *session) getConfigValue(key string) (string, bool) {
	switch key {
	case "base_url":
		return s.cfg.BaseURL, true
	case "api_key":
		masked := s.cfg.APIKey
		if len(masked) > 4 {
			masked = masked[:4] + strings.Repeat("*", len(masked)-4)
		}
		return masked, true
	case "model":
		return s.cfg.Model, true
	case "mode":
		return s.cfg.Mode, true
	case "workspace":
		return s.cfg.Workspace, true
	case "max_concurrent":
		return strconv.Itoa(s.cfg.MaxConcurrent), true
	case "exec_allowlist":
		return strings.Join(s.cfg.ExecAllowlist, ","), true
	case "exec_timeout_sec":
		return strconv.Itoa(s.cfg.ExecTimeoutSeconds), true
	case "exec_max_output_bytes":
		return strconv.Itoa(s.cfg.ExecMaxOutputBytes), true
	case "context_window":
		return strconv.Itoa(s.cfg.ContextWindowTokens), true
	case "context_reserve":
		return strconv.Itoa(s.cfg.ContextReserveTokens), true
	case "max_llm_retries":
		return strconv.Itoa(s.cfg.MaxLLMRetries), true
	case "stream_with_tools":
		return strconv.FormatBool(s.cfg.StreamWithTools), true
	case "fetch_allow_hosts":
		return strings.Join(s.cfg.FetchAllowHosts, ","), true
	case "fetch_preapproved":
		return strconv.FormatBool(s.cfg.FetchPreapproved), true
	case "fetch_max_bytes":
		return strconv.Itoa(s.cfg.FetchMaxBytes), true
	case "fetch_timeout_sec":
		return strconv.Itoa(s.cfg.FetchTimeoutSec), true
	case "fetch_web_rate_per_sec":
		return strconv.Itoa(s.cfg.FetchWebRatePerSec), true
	case "fetch_web_rate_burst":
		return strconv.Itoa(s.cfg.FetchWebRateBurst), true
	case "search_max_results":
		return strconv.Itoa(s.cfg.SearchMaxResults), true
	case "autocompact_threshold":
		return strconv.Itoa(s.cfg.AutoCompactPct), true
	case "autocheck_cmd":
		return s.cfg.AutoCheckCmd, true
	case "lint_cmd":
		return s.cfg.LintCmd, true
	case "test_cmd":
		return s.cfg.TestCmd, true
	case "plain":
		return strconv.FormatBool(s.cfg.Plain), true
	case "quiet":
		return strconv.FormatBool(s.cfg.Quiet), true
	case "verbose":
		return strconv.FormatBool(s.cfg.Verbose), true
	case "log":
		return s.cfg.LogPath, true
	case "stream_reply":
		return strconv.FormatBool(s.cfg.StreamReply), true
	case "progress":
		return strconv.FormatBool(s.cfg.Progress), true
	case "design_save_dir":
		return s.cfg.DesignSaveDir, true
	case "design_save":
		return strconv.FormatBool(s.cfg.DesignSave), true
	case "project_context":
		return s.cfg.ProjectContext, true
	case "ast_grep":
		return s.cfg.AstGrep, true
	case "embedding_model":
		return s.cfg.EmbeddingModel, true
	case "hooks_enabled":
		return strconv.FormatBool(s.cfg.HooksEnabled), true
	case "cost_per_mtok":
		if s.cfg.CostPerMTok == nil {
			return "(not set — built-in pricing table when available)", true
		}
		return fmt.Sprintf("%g %g (input output USD per 1M tokens)", s.cfg.CostPerMTok.Input, s.cfg.CostPerMTok.Output), true
	case "git_auto_commit":
		return strconv.FormatBool(s.cfg.GitAutoCommit), true
	case "git_protected_branches":
		return strings.Join(s.cfg.GitProtectedBranches, ","), true
	case "checkpoint_auto":
		return s.cfg.CheckpointAuto, true
	}

	if mode, field, ok := parseModeConfigKey(key); ok {
		base, apiKey, model := s.cfg.ConnectionForMode(mode)
		switch field {
		case "base_url":
			return base, true
		case "api_key":
			masked := apiKey
			if len(masked) > 4 {
				masked = masked[:4] + strings.Repeat("*", len(masked)-4)
			}
			return masked, true
		case "model":
			return model, true
		}
	}
	return "", false
}

func (s *session) setConfig(key, value string) error {
	parseInt := func(v string) (int, error) { return strconv.Atoi(v) }
	parseBool := func(v string) (bool, error) { return strconv.ParseBool(v) }

	switch key {
	case "base_url":
		s.cfg.BaseURL = strings.TrimRight(value, "/")
	case "api_key":
		s.cfg.APIKey = value
	case "model":
		s.cfg.Model = value
	case "mode":
		s.cfg.Mode = value
	case "workspace":
		s.cfg.Workspace = value
	case "max_concurrent":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("max_concurrent must be a positive integer")
		}
		s.cfg.MaxConcurrent = n
	case "exec_allowlist":
		s.cfg.ExecAllowlist = config.ParseExecAllowlistString(value)
	case "exec_timeout_sec":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("exec_timeout_sec must be a positive integer")
		}
		s.cfg.ExecTimeoutSeconds = n
	case "exec_max_output_bytes":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("exec_max_output_bytes must be a positive integer")
		}
		s.cfg.ExecMaxOutputBytes = n
	case "context_window":
		n, err := parseInt(value)
		if err != nil || n < 0 {
			return fmt.Errorf("context_window must be a non-negative integer")
		}
		s.cfg.ContextWindowTokens = n
	case "context_reserve":
		n, err := parseInt(value)
		if err != nil || n < 0 {
			return fmt.Errorf("context_reserve must be a non-negative integer")
		}
		s.cfg.ContextReserveTokens = n
	case "max_llm_retries":
		n, err := parseInt(value)
		if err != nil || n < 0 {
			return fmt.Errorf("max_llm_retries must be a non-negative integer")
		}
		s.cfg.MaxLLMRetries = n
	case "stream_with_tools":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("stream_with_tools must be true or false")
		}
		s.cfg.StreamWithTools = b
	case "fetch_allow_hosts":
		s.cfg.FetchAllowHosts = config.ParseFetchAllowHostsString(value)
	case "fetch_preapproved":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("fetch_preapproved must be true or false")
		}
		s.cfg.FetchPreapproved = b
	case "fetch_max_bytes":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("fetch_max_bytes must be a positive integer")
		}
		s.cfg.FetchMaxBytes = n
	case "fetch_timeout_sec":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("fetch_timeout_sec must be a positive integer")
		}
		s.cfg.FetchTimeoutSec = n
	case "fetch_web_rate_per_sec":
		n, err := parseInt(value)
		if err != nil || n < 0 {
			return fmt.Errorf("fetch_web_rate_per_sec must be a non-negative integer (0 disables)")
		}
		if n > config.MaxFetchWebRatePerSec {
			n = config.MaxFetchWebRatePerSec
		}
		s.cfg.FetchWebRatePerSec = n
		if n == 0 {
			s.cfg.FetchWebRateBurst = 0
		} else if s.cfg.FetchWebRateBurst < 1 {
			b := n
			if b > config.MaxFetchWebRateBurst {
				b = config.MaxFetchWebRateBurst
			}
			s.cfg.FetchWebRateBurst = b
		}
	case "fetch_web_rate_burst":
		n, err := parseInt(value)
		if err != nil || n < 0 {
			return fmt.Errorf("fetch_web_rate_burst must be a non-negative integer")
		}
		if n > config.MaxFetchWebRateBurst {
			n = config.MaxFetchWebRateBurst
		}
		s.cfg.FetchWebRateBurst = n
	case "search_max_results":
		n, err := parseInt(value)
		if err != nil || n < 1 {
			return fmt.Errorf("search_max_results must be a positive integer")
		}
		s.cfg.SearchMaxResults = n
	case "autocompact_threshold":
		n, err := parseInt(value)
		if err != nil || n < 0 || n > 100 {
			return fmt.Errorf("autocompact_threshold must be 0-100")
		}
		s.cfg.AutoCompactPct = n
	case "autocheck_cmd":
		s.cfg.AutoCheckCmd = value
	case "lint_cmd":
		s.cfg.LintCmd = value
	case "test_cmd":
		s.cfg.TestCmd = value
	case "plain":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("plain must be true or false")
		}
		s.cfg.Plain = b
	case "quiet":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("quiet must be true or false")
		}
		s.cfg.Quiet = b
	case "verbose":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("verbose must be true or false")
		}
		s.cfg.Verbose = b
	case "log":
		s.cfg.LogPath = value
	case "stream_reply":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("stream_reply must be true or false")
		}
		s.cfg.StreamReply = b
	case "progress":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("progress must be true or false")
		}
		s.cfg.Progress = b
	case "design_save_dir":
		s.cfg.DesignSaveDir = value
	case "design_save":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("design_save must be true or false")
		}
		s.cfg.DesignSave = b
	case "project_context":
		s.cfg.ProjectContext = value
	case "ast_grep":
		s.cfg.AstGrep = value
		s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	case "embedding_model":
		s.cfg.EmbeddingModel = value
	case "hooks_enabled":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("hooks_enabled must be true or false")
		}
		s.cfg.HooksEnabled = b
	case "cost_per_mtok":
		fields := strings.Fields(value)
		if len(fields) == 0 || strings.EqualFold(fields[0], "off") || strings.EqualFold(fields[0], "clear") {
			s.cfg.CostPerMTok = nil
			return nil
		}
		if len(fields) != 2 {
			return fmt.Errorf("cost_per_mtok expects two numbers (USD per 1M input and output tokens), or \"off\"")
		}
		in, err1 := strconv.ParseFloat(fields[0], 64)
		out, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("cost_per_mtok: invalid number")
		}
		if in < 0 || out < 0 {
			return fmt.Errorf("cost_per_mtok: rates must be non-negative")
		}
		s.cfg.CostPerMTok = &config.CostPerMTok{Input: in, Output: out}
	case "git_auto_commit":
		b, err := parseBool(value)
		if err != nil {
			return fmt.Errorf("git_auto_commit must be true or false")
		}
		s.cfg.GitAutoCommit = b
	case "git_protected_branches":
		s.cfg.GitProtectedBranches = config.ParseGitProtectedBranches(value)
		if len(s.cfg.GitProtectedBranches) == 0 {
			s.cfg.GitProtectedBranches = []string{"main", "master", "develop"}
		}
	case "checkpoint_auto":
		v := strings.TrimSpace(strings.ToLower(value))
		if v == "" {
			v = "plan"
		}
		if v != "plan" && v != "all" && v != "off" {
			return fmt.Errorf("checkpoint_auto must be plan, all, or off")
		}
		s.cfg.CheckpointAuto = v
	default:
		if mode, field, ok := parseModeConfigKey(key); ok {
			if s.cfg.ModeModels == nil {
				s.cfg.ModeModels = make(map[string]config.ModeConnectionOverride)
			}
			ov := s.cfg.ModeModels[mode]
			switch field {
			case "base_url":
				ov.BaseURL = strings.TrimRight(value, "/")
			case "api_key":
				ov.APIKey = value
			case "model":
				ov.Model = value
			}
			s.cfg.ModeModels[mode] = ov
			return nil
		}
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

// parseModeConfigKey checks if key is a per-mode override like "plan_model" or "build_base_url".
// Returns (mode, field, true) on match.
func parseModeConfigKey(key string) (mode, field string, ok bool) {
	for _, m := range []string{"plan", "build", "ask"} {
		for _, f := range []string{"base_url", "api_key", "model"} {
			if key == m+"_"+f {
				return m, f, true
			}
		}
	}
	return "", "", false
}

// resolveAstGrep resolves the ast-grep binary path into cfg.AstGrep.
// If the binary is not found and a scanner is available (interactive mode),
// the user is prompted to download it. Non-interactive sessions silently skip.
func resolveAstGrep(cfg *config.Config, sc *bufio.Scanner) {
	v := strings.TrimSpace(strings.ToLower(cfg.AstGrep))
	if v == "off" {
		cfg.AstGrep = ""
		return
	}
	if v != "" && v != "auto" {
		if _, err := os.Stat(cfg.AstGrep); err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "codient: configured ast-grep path %q not found, falling back to auto-detect\n", cfg.AstGrep)
	}

	if p := astgrep.Resolve(); p != "" {
		cfg.AstGrep = p
		return
	}

	if sc == nil {
		cfg.AstGrep = ""
		return
	}

	fmt.Fprintf(os.Stderr, "codient: ast-grep not found. Install it for structural code search (find_references)? [Y/n] ")
	if !sc.Scan() {
		cfg.AstGrep = ""
		return
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	if answer != "" && answer != "y" && answer != "yes" {
		cfg.AstGrep = ""
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Fprintf(os.Stderr, "codient: downloading ast-grep...\n")
	destDir, err := astgrep.BinDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: ast-grep setup: %v\n", err)
		cfg.AstGrep = ""
		return
	}
	path, err := astgrep.Download(ctx, destDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: ast-grep download failed: %v\n", err)
		cfg.AstGrep = ""
		return
	}
	cfg.AstGrep = path
	fmt.Fprintf(os.Stderr, "codient: ast-grep installed to %s\n", path)
}

// probeAndSetContext tries to detect the server's context window for the current model.
// If cfg.ContextWindowTokens is already set in config, this is a no-op.
func (s *session) probeAndSetContext(ctx context.Context) {
	if s.cfg.ContextWindowTokens > 0 {
		return
	}
	model := strings.TrimSpace(s.cfg.Model)
	if model == "" {
		return
	}
	c := openaiclient.New(s.cfg)
	n, err := c.ProbeContextWindow(ctx, model)
	if err != nil || n <= 0 {
		return
	}
	s.cfg.ContextWindowTokens = n
}

func messageTextForEstimate(m openai.ChatCompletionMessageParamUnion) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// computeUndoEntry calculates the set of files changed by a single turn by
// diffing the pre-turn and post-turn git state. Returns nil if nothing changed.
func computeUndoEntry(preModified, preUntracked, postModified, postUntracked []string, histLen int) *undoEntry {
	modified := setDiff(postModified, preModified)
	created := setDiff(postUntracked, preUntracked)
	if len(modified) == 0 && len(created) == 0 {
		return nil
	}
	return &undoEntry{
		modifiedFiles: modified,
		createdFiles:  created,
		historyLen:    histLen,
	}
}

// startCodeIndex launches background indexing if an embedding model is configured.
// After the index is built, the registry and system prompt are rebuilt to include semantic_search.
func (s *session) startCodeIndex(ctx context.Context) {
	model := strings.TrimSpace(s.cfg.EmbeddingModel)
	ws := s.cfg.EffectiveWorkspace()
	if model == "" || ws == "" {
		return
	}
	s.codeIndex = codeindex.New(ws, s.client, model)
	s.registry = buildRegistry(s.cfg, s.mode, s, s.memOpts)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, s.memory)
	fmt.Fprintf(os.Stderr, "codient: indexing workspace for semantic search...\n")
	go func() {
		s.codeIndex.BuildOrUpdate(ctx)
		n := s.codeIndex.Len()
		if err := s.codeIndex.BuildErr(); err != nil {
			s.replAsyncStderrNote(fmt.Sprintf("codient: semantic index: %v\n", err))
		} else if n > 0 {
			s.replAsyncStderrNote(fmt.Sprintf("codient: semantic index ready (%d files)\n", n))
		}
	}()
}

// updatePlanFromReply parses the agent's plan-mode markdown into a structured
// plan and persists it. If a plan already exists, the parsed content is merged
// (keeping the existing session/revision metadata).
func (s *session) updatePlanFromReply(markdown, userRequest string) {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" {
		return
	}
	parsed := planstore.ParseFromMarkdown(markdown, userRequest)
	if s.currentPlan == nil {
		parsed.SessionID = s.sessionID
		parsed.Workspace = ws
		parsed.Revision = 1
		s.currentPlan = parsed
	} else {
		s.currentPlan.Summary = parsed.Summary
		s.currentPlan.Steps = parsed.Steps
		s.currentPlan.Assumptions = parsed.Assumptions
		s.currentPlan.OpenQuestions = parsed.OpenQuestions
		s.currentPlan.FilesToModify = parsed.FilesToModify
		s.currentPlan.Verification = parsed.Verification
		s.currentPlan.RawMarkdown = parsed.RawMarkdown
		if s.currentPlan.UserRequest == "" {
			s.currentPlan.UserRequest = parsed.UserRequest
		}
	}
	s.currentPlan.Phase = planstore.PhaseDraft
	s.planPhase = planstore.PhaseDraft
	if err := planstore.Save(s.currentPlan); err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
	}
}

// loadPlanFromDisk loads a previously saved plan for the current session.
func (s *session) loadPlanFromDisk() {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" || s.sessionID == "" {
		return
	}
	plan, err := planstore.Load(ws, s.sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient: plan load: %v\n", err)
		return
	}
	if plan == nil {
		return
	}
	s.currentPlan = plan
	s.planPhase = plan.Phase
}

// handlePlanResume is called on session resume when an active plan exists.
// It shows the plan status and offers resume options.
func (s *session) handlePlanResume(ctx context.Context, sc *bufio.Scanner) {
	plan := s.currentPlan
	done, total := 0, len(plan.Steps)
	for _, st := range plan.Steps {
		if st.Status == planstore.StepDone || st.Status == planstore.StepSkipped {
			done++
		}
	}
	fmt.Fprintf(os.Stderr, "\ncodient: resuming plan (rev %d, phase %s, steps %d/%d)\n", plan.Revision, plan.Phase, done, total)

	switch s.planPhase {
	case planstore.PhaseDraft, planstore.PhaseAwaitingApproval:
		fmt.Fprintf(os.Stderr, "codient: plan is in %s phase — continue in plan mode\n", s.planPhase)
		if s.mode != prompt.ModePlan {
			s.switchMode(prompt.ModePlan)
		}

	case planstore.PhaseApproved, planstore.PhaseExecuting:
		fmt.Fprintf(os.Stderr, "\n  [r] Resume execution from current step\n")
		fmt.Fprintf(os.Stderr, "  [p] Re-plan (switch to plan mode)\n")
		fmt.Fprintf(os.Stderr, "  [i] Ignore plan and start fresh\n")
		fmt.Fprintf(os.Stderr, "\ncodient: choose action: ")
		if !sc.Scan() {
			return
		}
		choice := strings.ToLower(strings.TrimSpace(sc.Text()))
		switch choice {
		case "r", "resume":
			if err := s.executeFromPlan(ctx, plan); err != nil {
				fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			}
		case "p", "replan":
			plan.Phase = planstore.PhaseDraft
			s.planPhase = planstore.PhaseDraft
			planstore.IncrementRevision(plan)
			if err := planstore.Save(plan); err != nil {
				fmt.Fprintf(os.Stderr, "codient: plan save: %v\n", err)
			}
			s.switchMode(prompt.ModePlan)
		default:
			s.currentPlan = nil
			s.planPhase = ""
			s.autoSave()
		}

	case planstore.PhaseReview:
		fmt.Fprintf(os.Stderr, "codient: plan was in review phase — re-running verification\n")
		if s.scanner == nil {
			s.scanner = sc
		}
		passed, err := s.runVerification(ctx, sc, plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "codient: verification error: %v\n", err)
		}
		if passed {
			fmt.Fprintf(os.Stderr, "codient: verification passed\n")
		}
	}
}

// planStatusLine returns a one-line summary of the current plan for /status.
func (s *session) planStatusLine() string {
	if s.currentPlan == nil {
		return ""
	}
	p := s.currentPlan
	done, total := 0, len(p.Steps)
	for _, st := range p.Steps {
		if st.Status == planstore.StepDone || st.Status == planstore.StepSkipped {
			done++
		}
	}
	return fmt.Sprintf("rev %d, phase %s, steps %d/%d", p.Revision, p.Phase, done, total)
}

func setDiff(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := set[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
