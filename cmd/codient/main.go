// Command codient is a CLI for local LM Studio agents (OpenAI-compatible API, openai-go client).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/agent"
	"codient/internal/agentlog"
	"codient/internal/assistout"
	"codient/internal/config"
	"codient/internal/lmstudio"
	"codient/internal/planstore"
	"codient/internal/prompt"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		system      = flag.String("system", "", "optional system prompt (merged into default tool-capabilities prompt)")
		promptFlag  = flag.String("prompt", "", "user message: without -repl, stdin is used if flag empty; with -repl, non-empty -prompt is the first turn, then further lines are read from stdin")
		stream      = flag.Bool("stream", false, "single-turn streamed completion without tools (writes to stdout)")
		listModels  = flag.Bool("list-models", false, "print model ids from GET /v1/models and exit")
		listTools   = flag.Bool("list-tools", false, "print registered tool names for current env and exit")
		ping        = flag.Bool("ping", false, "check GET /v1/models and exit")
		timeout     = flag.Duration("timeout", 10*time.Minute, "per-invocation context timeout")
		goal        = flag.String("goal", "", "optional high-level objective; merged into task directive on first turn only (see also AGENTS.md / .codient/instructions.md under workspace, 32KiB cap)")
		taskFile    = flag.String("task-file", "", "optional path to a task description file (capped at 32KiB); merged into task directive on first turn only")
		repl        = flag.Bool("repl", false, "multi-turn REPL: read user lines from stdin until exit or EOF (one system prompt, session history)")
		logPath     = flag.String("log", "", "append JSONL agent events to this file (overrides CODIENT_LOG if set)")
		progress    = flag.Bool("progress", false, "print agent progress to stderr (default: on when -log/CODIENT_LOG is set or stderr is a TTY; off if CODIENT_PROGRESS=0)")
		modeFlag    = flag.String("mode", "", "agent|ask|plan: tool + prompt policy (default agent; when empty, use CODIENT_MODE)")
		plainOut    = flag.Bool("plain", false, "print assistant replies as raw text (no markdown/ANSI); or set CODIENT_PLAIN=1; auto when stdout is not a TTY")
		streamReply = flag.Bool("stream-reply", true, "stream assistant tokens to stdout (TTY; in -mode plan with markdown, only the post-answer full plan is buffered for glamour; CODIENT_STREAM_REPLY=0/1)")
		planSaveDir = flag.String("plan-save-dir", "", "directory for saved implementation plans (default: <workspace>/.codient/plans; overrides CODIENT_PLAN_SAVE_DIR)")
	)
	flag.Parse()

	richAssistant := assistantOutputRich(*plainOut)

	agentMode, err := prompt.ResolveMode(*modeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mode: %v\n", err)
		return 2
	}

	effectiveLog := strings.TrimSpace(*logPath)
	if effectiveLog == "" {
		effectiveLog = strings.TrimSpace(os.Getenv("CODIENT_LOG"))
	}
	progressOut := resolveProgressOut(*progress, effectiveLog != "")

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := lmstudio.New(cfg)

	if *ping {
		if err := client.PingModels(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "ping: %v\n", err)
			return 1
		}
		fmt.Println("ok")
		return 0
	}

	if *listModels {
		ids, err := client.ListModels(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "models: %v\n", err)
			return 1
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return 0
	}

	if *listTools {
		reg := buildRegistry(cfg, agentMode)
		for _, n := range reg.Names() {
			fmt.Println(n)
		}
		return 0
	}

	if *repl && *stream {
		fmt.Fprintf(os.Stderr, "codient: -repl and -stream are incompatible\n")
		return 2
	}

	var logFile *os.File
	var agentLog *agentlog.Logger
	if effectiveLog != "" {
		var err error
		logFile, err = os.OpenFile(effectiveLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "log: %v\n", err)
			return 2
		}
		defer logFile.Close()
		agentLog = agentlog.New(logFile)
	}

	if *repl {
		if err := cfg.RequireModel(); err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 2
		}
		assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
			Plain:     stderrPromptPlain(*plainOut),
			Repl:      true,
			Mode:      string(agentMode),
			Workspace: cfg.EffectiveWorkspace(),
			Model:     cfg.Model,
		})
		reg := buildRegistry(cfg, agentMode)
		if os.Getenv("CODIENT_VERBOSE") == "1" {
			fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", cfg.EffectiveWorkspace(), agentMode, strings.Join(reg.Names(), ", "))
		}
		repoInstr, err := prompt.LoadRepoInstructions(cfg.EffectiveWorkspace())
		if err != nil {
			fmt.Fprintf(os.Stderr, "repo instructions: %v\n", err)
			return 2
		}
		systemPrompt := buildAgentSystemPrompt(cfg, reg, agentMode, *system, repoInstr)
		ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg, Log: agentLog, Progress: progressOut}
		fmt.Fprintf(os.Stderr, "codient REPL mode=%s (empty line ignored, type exit to quit). Workspace: %s\n", agentMode, cfg.EffectiveWorkspace())
		if agentMode == prompt.ModePlan {
			fmt.Fprintf(os.Stderr, "plan: chat history is only this session (new process = fresh context; -log is not replayed). Answer: when blocking; Follow-up or exit otherwise. Hand off with codient -mode agent when Ready to implement.\n")
			fmt.Fprintf(os.Stderr, "plan: replies that include \"Ready to implement\" are saved as markdown under the workspace (.codient/plans/ by default); CODIENT_PLAN_SAVE=0 disables.\n")
		}
		if strings.TrimSpace(*promptFlag) == "" && agentMode != prompt.ModePlan {
			fmt.Fprintf(os.Stderr, "codient: type a message and press Enter (or pass -prompt for the first turn).\n")
		}

		var history []openai.ChatCompletionMessageParamUnion
		sc := bufio.NewScanner(os.Stdin)
		turn := 0
		lastAssistantReply := ""
		var planTaskSlug string

		// -prompt is the first REPL user message; without it we block on stdin (looks like a hang if the user only passed -prompt).
		if seed := strings.TrimSpace(*promptFlag); seed != "" {
			planTaskSlug = planstore.TaskSlug(*goal, *taskFile, seed)
			user, err := applyTaskToFirstTurnIfNeeded(turn, seed, *goal, *taskFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "task: %v\n", err)
				return 2
			}
			turn++
			streamTo := streamWriterForTurn(*streamReply, assistout.StdoutIsInteractive(), agentMode, richAssistant, lastAssistantReply)
			reply, newHist, streamed, err := ar.RunConversation(ctx, systemPrompt, history, user, streamTo)
			if err != nil {
				fmt.Fprintf(os.Stderr, "agent: %v\n", err)
				return 1
			}
			history = newHist
			if err := finishAssistantTurn(os.Stdout, reply, richAssistant, agentMode == prompt.ModePlan, streamed); err != nil {
				fmt.Fprintf(os.Stderr, "write: %v\n", err)
				return 1
			}
			maybeSavePlan(os.Stderr, cfg.EffectiveWorkspace(), *planSaveDir, agentMode, reply, planTaskSlug)
			lastAssistantReply = assistout.PrepareAssistantText(reply, agentMode == prompt.ModePlan)
		}

		for {
			if agentMode == prompt.ModePlan {
				fmt.Fprint(os.Stderr, assistout.PlanStdinPrompt(stderrPromptPlain(*plainOut), lastAssistantReply))
			}
			if !sc.Scan() {
				break
			}
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if strings.EqualFold(line, "exit") || strings.EqualFold(line, "quit") {
				break
			}
			user, err := applyTaskToFirstTurnIfNeeded(turn, line, *goal, *taskFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "task: %v\n", err)
				return 2
			}
			if planTaskSlug == "" {
				planTaskSlug = planstore.TaskSlug(*goal, *taskFile, line)
			}
			turn++
			writePlanDraftPreamble(os.Stdout, agentMode, lastAssistantReply)
			streamTo := streamWriterForTurn(*streamReply, assistout.StdoutIsInteractive(), agentMode, richAssistant, lastAssistantReply)
			reply, newHist, streamed, err := ar.RunConversation(ctx, systemPrompt, history, user, streamTo)
			if err != nil {
				fmt.Fprintf(os.Stderr, "agent: %v\n", err)
				return 1
			}
			history = newHist
			if err := finishAssistantTurn(os.Stdout, reply, richAssistant, agentMode == prompt.ModePlan, streamed); err != nil {
				fmt.Fprintf(os.Stderr, "write: %v\n", err)
				return 1
			}
			maybeSavePlan(os.Stderr, cfg.EffectiveWorkspace(), *planSaveDir, agentMode, reply, planTaskSlug)
			lastAssistantReply = assistout.PrepareAssistantText(reply, agentMode == prompt.ModePlan)
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "stdin: %v\n", err)
			return 2
		}
		return 0
	}

	user, err := resolvePrompt(*promptFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prompt: %v\n", err)
		return 2
	}
	user = strings.TrimSpace(user)
	if user == "" {
		fmt.Fprintf(os.Stderr, "provide -prompt or pipe a message on stdin\n")
		return 2
	}

	if err := cfg.RequireModel(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}

	if *stream {
		msgs := make([]openai.ChatCompletionMessageParamUnion, 0, 2)
		if strings.TrimSpace(*system) != "" {
			msgs = append(msgs, openai.SystemMessage(strings.TrimSpace(*system)))
		}
		msgs = append(msgs, openai.UserMessage(user))
		params := openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(client.Model()),
			Messages: msgs,
		}
		if err := client.StreamChatCompletion(ctx, params, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "\nstream: %v\n", err)
			return 1
		}
		fmt.Fprintln(os.Stdout)
		return 0
	}

	assistout.WriteWelcome(os.Stderr, assistout.WelcomeParams{
		Plain:     stderrPromptPlain(*plainOut),
		Repl:      false,
		Mode:      string(agentMode),
		Workspace: cfg.EffectiveWorkspace(),
		Model:     cfg.Model,
	})

	reg := buildRegistry(cfg, agentMode)
	if os.Getenv("CODIENT_VERBOSE") == "1" {
		fmt.Fprintf(os.Stderr, "codient: workspace=%q mode=%s tools=%s\n", cfg.EffectiveWorkspace(), agentMode, strings.Join(reg.Names(), ", "))
	}
	repoInstr, err := prompt.LoadRepoInstructions(cfg.EffectiveWorkspace())
	if err != nil {
		fmt.Fprintf(os.Stderr, "repo instructions: %v\n", err)
		return 2
	}
	systemPrompt := buildAgentSystemPrompt(cfg, reg, agentMode, *system, repoInstr)
	rawUser := user
	user, err = applyTaskToFirstTurnIfNeeded(0, user, *goal, *taskFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task: %v\n", err)
		return 2
	}
	if agentMode == prompt.ModePlan {
		fmt.Fprintf(os.Stderr, "codient: for interactive planning (one clarifying question per turn with A/B/C options), use: codient -repl -mode plan [-prompt \"…\"]\n")
		fmt.Fprintf(os.Stderr, "plan: replies that include \"Ready to implement\" are saved under the workspace (.codient/plans/ by default); CODIENT_PLAN_SAVE=0 disables.\n")
	}
	ar := &agent.Runner{LLM: client, Cfg: cfg, Tools: reg, Log: agentLog, Progress: progressOut}
	streamTo := streamWriterForTurn(*streamReply, assistout.StdoutIsInteractive(), agentMode, richAssistant, "")
	reply, streamed, err := ar.Run(ctx, systemPrompt, user, streamTo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	if err := finishAssistantTurn(os.Stdout, reply, richAssistant, agentMode == prompt.ModePlan, streamed); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		return 1
	}
	maybeSavePlan(os.Stderr, cfg.EffectiveWorkspace(), *planSaveDir, agentMode, reply, planstore.TaskSlug(*goal, *taskFile, rawUser))
	return 0
}

func stderrPromptPlain(plainFlag bool) bool {
	return plainFlag || strings.TrimSpace(os.Getenv("CODIENT_PLAIN")) == "1"
}

func assistantOutputRich(plainFlag bool) bool {
	if plainFlag || strings.TrimSpace(os.Getenv("CODIENT_PLAIN")) == "1" {
		return false
	}
	return assistout.StdoutIsInteractive()
}

// resolveProgressOut chooses stderr for agent progress lines (model rounds, tool calls).
// Default is on when logging to a file is requested or stderr is an interactive terminal;
// CODIENT_PROGRESS=0 suppresses all progress (including -progress); CODIENT_PROGRESS=1 or -progress forces it on.
func resolveProgressOut(progressFlag, logRequested bool) io.Writer {
	if strings.TrimSpace(os.Getenv("CODIENT_PROGRESS")) == "0" {
		return nil
	}
	if progressFlag || strings.TrimSpace(os.Getenv("CODIENT_PROGRESS")) == "1" {
		return os.Stderr
	}
	if logRequested {
		return os.Stderr
	}
	st, err := os.Stderr.Stat()
	if err != nil {
		return nil
	}
	if (st.Mode() & os.ModeCharDevice) != 0 {
		return os.Stderr
	}
	return nil
}

// resolveStreamReply enables streaming when stdout is a TTY and the flag allows it.
// CODIENT_STREAM_REPLY=0 forces off; =1 forces on (e.g. when piping to a file but still want streaming).
func resolveStreamReply(flag bool, stdoutTTY bool) bool {
	switch strings.TrimSpace(os.Getenv("CODIENT_STREAM_REPLY")) {
	case "0":
		return false
	case "1":
		return true
	}
	return flag && stdoutTTY
}

// streamWriterForTurn returns stdout for token streaming when enabled. For plan mode with
// rich markdown, streaming is disabled only for the turn right after a blocking Question
// (so the full plan can be rendered once with glamour); all other turns stream as usual.
func streamWriterForTurn(streamReplyFlag bool, stdoutTTY bool, mode prompt.Mode, richAssistant bool, lastAssistantReply string) io.Writer {
	if !resolveStreamReply(streamReplyFlag, stdoutTTY) {
		return nil
	}
	if mode == prompt.ModePlan && richAssistant && assistout.ReplySignalsPlanWait(lastAssistantReply) {
		return nil
	}
	return os.Stdout
}

// writePlanDraftPreamble prints a blank line and status line before generating the full
// plan after the user answered a blocking Question (plan mode REPL).
func writePlanDraftPreamble(w io.Writer, mode prompt.Mode, lastAssistantReply string) {
	if mode != prompt.ModePlan || !assistout.ReplySignalsPlanWait(lastAssistantReply) {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Building the implementation plan…")
}

// finishAssistantTurn renders the reply when it was not already streamed as raw text.
func finishAssistantTurn(w io.Writer, reply string, useMarkdown, planMode, streamed bool) error {
	if streamed {
		_, err := fmt.Fprintln(w)
		return err
	}
	return assistout.WriteAssistant(w, reply, useMarkdown, planMode)
}

func resolvePlanSaveDir(flag string) string {
	if s := strings.TrimSpace(os.Getenv("CODIENT_PLAN_SAVE_DIR")); s != "" {
		return s
	}
	return strings.TrimSpace(flag)
}

// maybeSavePlan persists a completed implementation plan (plan mode, contains "Ready to implement").
func maybeSavePlan(stderr io.Writer, workspace, planSaveDirFlag string, mode prompt.Mode, reply string, taskSlug string) {
	if mode != prompt.ModePlan {
		return
	}
	if strings.TrimSpace(os.Getenv("CODIENT_PLAN_SAVE")) == "0" {
		return
	}
	text := assistout.PrepareAssistantText(reply, true)
	if !planstore.LooksLikeReadyToImplement(text) {
		return
	}
	path, err := planstore.Save(workspace, resolvePlanSaveDir(planSaveDirFlag), taskSlug, text, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "codient: saving plan: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "codient: wrote plan to %s\n", path)
}

func resolvePrompt(flagPrompt string) (string, error) {
	if strings.TrimSpace(flagPrompt) != "" {
		return flagPrompt, nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
