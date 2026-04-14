package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	"codient/internal/openaiclient"
	"codient/internal/projectinfo"
	"codient/internal/prompt"
	"codient/internal/sessionstore"
	"codient/internal/slashcmd"
	"codient/internal/tools"
)

type session struct {
	cfg      *config.Config
	client   *openaiclient.Client
	registry *tools.Registry
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

	// REPL state
	history   []openai.ChatCompletionMessageParamUnion
	sessionID string
	turn      int
	lastReply string
	taskSlug  string

	undoStack []undoEntry // per-turn undo records (most recent at end)

	// stdinScanner is set for the interactive REPL; used for exec allow prompts.
	scanner   *bufio.Scanner
	execAllow *tools.SessionExecAllow // mutable run_command allowlist for this process; nil if exec disabled

	fetchAllow    *tools.SessionFetchAllow // mutable fetch_url host approvals for this process; nil until first fetch
	fetchPromptMu sync.Mutex               // serializes fetch allow prompts and post-lock re-checks

	codeIndex *codeindex.Index // semantic search index; nil when embedding_model is not configured
}

type undoEntry struct {
	modifiedFiles []string // tracked files modified during this turn (restore via git checkout)
	createdFiles  []string // untracked files created during this turn (delete)
	historyLen    int      // len(s.history) before this turn started
}

func (s *session) newRunner() *agent.Runner {
	s.client = openaiclient.New(s.cfg)
	r := &agent.Runner{
		LLM: s.client, Cfg: s.cfg, Tools: s.registry,
		Log: s.agentLog, Progress: s.progressOut,
		ProgressPlain: s.cfg.Plain,
		ProgressMode:  string(s.mode),
	}
	if s.mode == prompt.ModeBuild {
		if cmd := effectiveAutoCheckCmd(s.cfg); cmd != "" {
			sec := autoCheckTimeoutSec(s.cfg)
			r.AutoCheck = makeAutoCheck(s.cfg.EffectiveWorkspace(), cmd, time.Duration(sec)*time.Second, s.cfg.ExecMaxOutputBytes, s.progressOut)
		}
	}
	return r
}

func (s *session) executeTurn(ctx context.Context, runner *agent.Runner, user string) (reply string, err error) {
	if err := s.cfg.RequireModel(); err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "\n")
	if s.mode == prompt.ModeAsk {
		runner.PostReplyCheck = makePostReplyCheck(s)
	}
	writePlanDraftPreamble(os.Stdout, s.mode, s.lastReply)
	streamTo := streamWriterForTurn(s.streamReply, assistout.StdoutIsInteractive(), s.mode, s.richOutput, s.lastReply)
	reply, newHist, streamed, runErr := runner.RunConversation(ctx, s.systemPrompt, s.history, user, streamTo)
	if runErr != nil {
		return "", runErr
	}
	s.history = newHist
	if err := finishAssistantTurn(os.Stdout, reply, s.richOutput, s.mode == prompt.ModePlan, streamed); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
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
func makePostReplyCheck(s *session) func(context.Context, agent.PostReplyCheckInfo) string {
	return func(ctx context.Context, info agent.PostReplyCheckInfo) string {
		if !looksLikeSuggestionList(info.Reply) {
			return ""
		}
		if skipSuggestionVerifyForResearchTurn(info) {
			return ""
		}
		if s.progressOut != nil {
			if line := agent.FormatStatusProgressLine(s.cfg.Plain, string(s.mode), "checking whether verification is needed…"); line != "" {
				fmt.Fprintf(s.progressOut, "%s\n", line)
			}
		}
		want, err := postReplyGateWantsVerification(ctx, s.client, info)
		if err != nil || !want {
			return ""
		}
		return postReplyVerificationPrompt
	}
}

func postReplyGateWantsVerification(ctx context.Context, client *openaiclient.Client, info agent.PostReplyCheckInfo) (bool, error) {
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
	if userIntentSuggestsCodeChanges(info.User) {
		return false
	}
	return true
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

func (s *session) showGitDiffSummary() {
	ws := s.cfg.EffectiveWorkspace()
	if ws == "" || !gitutil.IsRepo(ws) {
		return
	}
	summary, err := gitutil.DiffSummary(ws)
	if err != nil || summary == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "\ncodient: files changed:\n%s\n", summary)
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

func (s *session) pushUndoIfChanged(preModified, preUntracked []string, histLen int) {
	postModified, postUntracked := s.captureSnapshot()
	entry := computeUndoEntry(preModified, preUntracked, postModified, postUntracked, histLen)
	if entry != nil {
		s.undoStack = append(s.undoStack, *entry)
	}
}

func (s *session) undoLast(ws string) error {
	if len(s.undoStack) == 0 {
		fmt.Fprintf(os.Stderr, "codient: nothing to undo — no build-mode turns in this session\n")
		return nil
	}

	entry := s.undoStack[len(s.undoStack)-1]
	s.undoStack = s.undoStack[:len(s.undoStack)-1]

	// Preview what will be reverted.
	nMod := len(entry.modifiedFiles)
	nNew := len(entry.createdFiles)
	for _, f := range entry.modifiedFiles {
		fmt.Fprintf(os.Stderr, "  restore: %s\n", f)
	}
	for _, f := range entry.createdFiles {
		fmt.Fprintf(os.Stderr, "  remove:  %s\n", f)
	}

	if nMod > 0 {
		if err := gitutil.RestoreFiles(ws, entry.modifiedFiles); err != nil {
			return err
		}
	}
	for _, f := range entry.createdFiles {
		os.Remove(filepath.Join(ws, f))
	}

	msgsTrimmed := len(s.history) - entry.historyLen
	s.history = s.history[:entry.historyLen]
	if s.turn > 0 {
		s.turn--
	}

	// Update lastReply from the new final assistant message, or clear it.
	s.lastReply = ""
	for i := len(s.history) - 1; i >= 0; i-- {
		b, _ := json.Marshal(s.history[i])
		raw := string(b)
		if strings.Contains(raw, `"role":"assistant"`) {
			s.lastReply = raw
			break
		}
	}

	s.autoSave()
	fmt.Fprintf(os.Stderr, "codient: undid last turn (%d files restored, %d files removed, %d messages trimmed)\n", nMod, nNew, msgsTrimmed)
	return nil
}

func (s *session) undoAll(ws string) error {
	before, _ := gitutil.DiffSummary(ws)
	untracked, _ := gitutil.UntrackedFiles(ws)
	if before == "" && len(untracked) == 0 {
		fmt.Fprintf(os.Stderr, "codient: no changes to undo\n")
		return nil
	}
	if before != "" {
		fmt.Fprintf(os.Stderr, "codient: reverting changes:\n%s\n", before)
	}
	if len(untracked) > 0 {
		fmt.Fprintf(os.Stderr, "codient: removing %d untracked file(s)\n", len(untracked))
	}

	if err := gitutil.RestoreAll(ws); err != nil {
		return err
	}
	if err := gitutil.CleanUntracked(ws); err != nil {
		return err
	}

	s.undoStack = nil
	fmt.Fprintf(os.Stderr, "codient: all changes reverted\n")
	return nil
}

// showGitDiffIfBuild prints git diff --stat after a successful build-mode turn.
func (s *session) showGitDiffIfBuild() {
	if s.mode != prompt.ModeBuild {
		return
	}
	s.showGitDiffSummary()
}

func (s *session) runSingleTurn(ctx context.Context, user string) int {
	s.warnIfNotGitRepo()
	assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
		Plain:     s.cfg.Plain,
		Quiet:     s.cfg.Quiet,
		Repl:      false,
		Mode:      string(s.mode),
		Workspace: s.cfg.EffectiveWorkspace(),
		Model:     s.cfg.Model,
	})
	if s.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", s.cfg.EffectiveWorkspace(), s.mode, strings.Join(s.registry.Names(), ", "))
	}
	rawUser := user
	user, err := applyTaskToFirstTurnIfNeeded(0, user, s.goal, s.taskFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task: %v\n", err)
		return 2
	}
	runner := s.newRunner()
	reply, err := s.executeTurn(ctx, runner, user)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	maybeSaveDesign(os.Stderr, s.cfg.EffectiveWorkspace(), s.designSaveDir, s.sessionID, s.mode, reply, designstore.TaskSlug(s.goal, s.taskFile, rawUser), s.cfg.DesignSave)
	s.showGitDiffIfBuild()
	return 0
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
					s.mode = mode
					s.registry = buildRegistry(s.cfg, mode, s)
					s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
				}
				resumeSummary = sessionstore.ResumeSummaryLine(s.sessionID, existing.Messages)
			}
		}
	}
	if s.sessionID == "" {
		s.sessionID = sessionstore.NewID(ws)
	}

	config.SaveLastMode(string(s.mode))

	s.warnIfNotGitRepo()
	assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
		Plain:         s.cfg.Plain,
		Quiet:         s.cfg.Quiet,
		Repl:          true,
		Mode:          string(s.mode),
		Workspace:     ws,
		Model:         s.cfg.Model,
		ResumeSummary: resumeSummary,
	})
	if s.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", ws, s.mode, strings.Join(s.registry.Names(), ", "))
	}
	fmt.Fprintf(os.Stderr, "%s\n", assistout.ModeHint(s.cfg.Plain, string(s.mode)))

	sc := bufio.NewScanner(os.Stdin)
	s.scanner = sc
	enableBracketedPaste()
	defer disableBracketedPaste()
	resolveAstGrep(s.cfg, sc)
	s.registry = buildRegistry(s.cfg, s.mode, s)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))

	if strings.TrimSpace(s.cfg.Model) == "" {
		s.runSetupWizard(ctx, sc)
		s.client = openaiclient.New(s.cfg)
		s.registry = buildRegistry(s.cfg, s.mode, s)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
	}

	s.probeAndSetContext(ctx)
	s.startCodeIndex(ctx)

	fmt.Fprintf(os.Stderr, "codient: type /help for commands, /exit to quit\n")

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
		preModified, preUntracked := s.captureSnapshot()
		histLen := len(s.history)
		s.turn++
		reply, err := s.executeTurn(ctx, runner, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 1
		}
		s.pushUndoIfChanged(preModified, preUntracked, histLen)
		maybeSaveDesign(os.Stderr, ws, s.designSaveDir, s.sessionID, s.mode, reply, s.taskSlug, s.cfg.DesignSave)
		s.lastReply = assistout.PrepareAssistantText(reply, s.mode == prompt.ModePlan)
		s.autoSave()
		s.maybeAutoCompact(ctx)
		s.showGitDiffIfBuild()
	}
	done := false
	for !done {
		fmt.Fprint(os.Stderr, "\n")
		s.printPrompt()
		line, ok := readUserInput(sc)
		if !ok {
			break
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
		preModified, preUntracked := s.captureSnapshot()
		histLen := len(s.history)
		s.turn++
		reply, err := s.executeTurn(ctx, runner, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 1
		}
		s.pushUndoIfChanged(preModified, preUntracked, histLen)
		maybeSaveDesign(os.Stderr, ws, s.designSaveDir, s.sessionID, s.mode, reply, s.taskSlug, s.cfg.DesignSave)
		s.lastReply = assistout.PrepareAssistantText(reply, s.mode == prompt.ModePlan)
		s.autoSave()
		s.maybeAutoCompact(ctx)
		s.showGitDiffIfBuild()

		if s.mode == prompt.ModePlan {
			designText := assistout.PrepareAssistantText(reply, true)
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

func (s *session) printPrompt() {
	if s.mode == prompt.ModePlan && assistout.ReplySignalsPlanWait(s.lastReply) {
		fmt.Fprint(os.Stderr, assistout.PlanAnswerPrefix(s.cfg.Plain))
	} else {
		fmt.Fprint(os.Stderr, assistout.SessionPrompt(s.cfg.Plain, string(s.mode)))
	}
}

// offerPlanHandoff prompts the user to switch to build mode after a plan
// is finalized. If accepted, it switches modes and injects the design as the
// first build turn. Returns the updated runner.
func (s *session) offerPlanHandoff(ctx context.Context, sc *bufio.Scanner, runner *agent.Runner, designText string) *agent.Runner {
	fmt.Fprintf(os.Stderr, "\ncodient: plan complete — would you like to build it? [Y/n] ")
	if !sc.Scan() {
		return runner
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	if answer != "" && answer != "y" && answer != "yes" {
		return runner
	}

	s.switchMode(prompt.ModeBuild)
	// Clear history — the full design is embedded in the handoff user message,
	// so carrying over the design conversation just confuses the model into
	// continuing to design instead of building.
	s.history = nil
	runner = s.newRunner()

	preModified, preUntracked := s.captureSnapshot()
	histLen := len(s.history)
	s.turn++
	userMsg := designHandoffUserMessage(designText, s.registry.Names())
	reply, err := s.executeTurn(ctx, runner, userMsg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return runner
	}
	s.pushUndoIfChanged(preModified, preUntracked, histLen)
	s.lastReply = assistout.PrepareAssistantText(reply, false)
	s.autoSave()
	s.showGitDiffIfBuild()
	return runner
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
			s.registry = buildRegistry(s.cfg, s.mode, s)
			s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
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
			if len(s.cfg.ExecAllowlist) > 0 {
				s.execAllow = tools.NewSessionExecAllow(s.cfg.ExecAllowlist)
				s.registry = buildRegistry(s.cfg, s.mode, s)
				s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
			}
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
			if ac := effectiveAutoCheckCmd(s.cfg); ac != "" {
				fmt.Fprintf(os.Stderr, "  auto-check: %s\n", ac)
			} else {
				fmt.Fprintf(os.Stderr, "  auto-check: off\n")
			}
			if s.execAllow != nil {
				if s.execAllow.AllowAll() {
					fmt.Fprintf(os.Stderr, "  exec:      all commands allowed for this session\n")
				}
			}
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
			s.registry = buildRegistry(s.cfg, s.mode, s)
			s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
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

	return cmds
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
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
		if key == "model" || key == "base_url" {
			s.cfg.ContextWindowTokens = 0
			s.probeAndSetContext(ctx)
		}
	case "fetch_allow_hosts", "fetch_preapproved", "fetch_max_bytes", "fetch_timeout_sec",
		"fetch_web_rate_per_sec", "fetch_web_rate_burst",
		"search_max_results":
		s.registry = buildRegistry(s.cfg, s.mode, s)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
	case "autocheck_cmd":
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
	case "embedding_model":
		s.startCodeIndex(ctx)
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
	default:
		return "", false
	}
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
		s.registry = buildRegistry(s.cfg, s.mode, s)
		s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
	case "embedding_model":
		s.cfg.EmbeddingModel = value
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
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
// If cfg.ContextWindowTokens is already set by the env var, this is a no-op.
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
	fmt.Fprintf(os.Stderr, "codient: detected context window: %d tokens\n", n)
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
	s.registry = buildRegistry(s.cfg, s.mode, s)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, s.mode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))
	fmt.Fprintf(os.Stderr, "codient: indexing workspace for semantic search...\n")
	go func() {
		s.codeIndex.BuildOrUpdate(ctx)
		n := s.codeIndex.Len()
		if err := s.codeIndex.BuildErr(); err != nil {
			fmt.Fprintf(os.Stderr, "codient: semantic index: %v\n", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "codient: semantic index ready (%d files)\n", n)
		}
	}()
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
