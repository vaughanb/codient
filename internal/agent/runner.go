// Package agent runs a tool-calling loop against LM Studio (OpenAI-compatible chat completions).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/agentlog"
	"codient/internal/config"
	"codient/internal/tools"
)

// ChatClient is the LLM surface the agent needs (implemented by *lmstudio.Client).
type ChatClient interface {
	ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
	Model() string
}

// streamChatClient is implemented by *lmstudio.Client for token streaming during agent turns.
type streamChatClient interface {
	ChatCompletionStream(ctx context.Context, params openai.ChatCompletionNewParams, w io.Writer) (*openai.ChatCompletion, error)
}

// Runner executes multi-step tool use with bounded LLM concurrency (via the ChatClient implementation).
type Runner struct {
	LLM   ChatClient
	Cfg   *config.Config
	Tools *tools.Registry
	Log   *agentlog.Logger
	// Progress, when non-nil (e.g. os.Stderr), receives human-readable lines during the tool loop.
	Progress io.Writer
}

// Run carries out one user turn (no prior conversation history).
// streamTo is where assistant text deltas are written when streaming (e.g. os.Stdout); nil disables streaming.
// streamed is true when the reply was written incrementally (caller skips glamour for that turn).
func (r *Runner) Run(ctx context.Context, system, user string, streamTo io.Writer) (reply string, streamed bool, err error) {
	reply, _, streamed, err = r.RunConversation(ctx, system, nil, user, streamTo)
	return reply, streamed, err
}

// RunConversation runs one user message with optional prior messages (excluding system).
// history is a slice of user/assistant/tool messages from earlier turns; system is prepended each request.
// Returns the assistant's final text and updated history (including this turn), suitable for REPL.
// streamTo selects streaming for this turn only (nil = non-streaming completion).
// streamed is true when the final reply was produced via streaming (skip glamour in the caller).
func (r *Runner) RunConversation(ctx context.Context, system string, history []openai.ChatCompletionMessageParamUnion, user string, streamTo io.Writer) (string, []openai.ChatCompletionMessageParamUnion, bool, error) {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+16)
	sys := strings.TrimSpace(system)
	sysOffset := 0
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
		sysOffset = 1
	}
	msgs = append(msgs, history...)
	msgs = append(msgs, openai.UserMessage(user))

	apiTools := r.Tools.OpenAITools()
	llmRound := 0
	streamedFinal := false

	for step := 0; step < r.Cfg.MaxToolSteps; step++ {
		params := openai.ChatCompletionNewParams{
			Model:    shared.ChatModel(r.LLM.Model()),
			Messages: msgs,
		}
		if len(apiTools) > 0 {
			params.Tools = apiTools
			params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
				OfAuto: openai.String("auto"),
			}
		}

		t0 := time.Now()
		var res *openai.ChatCompletion
		var err error
		useStream := streamTo != nil
		if useStream {
			if sc, ok := r.LLM.(streamChatClient); ok {
				res, err = sc.ChatCompletionStream(ctx, params, streamTo)
				streamedFinal = true
			} else {
				res, err = r.LLM.ChatCompletion(ctx, params)
			}
		} else {
			res, err = r.LLM.ChatCompletion(ctx, params)
		}
		llmRound++
		llmDur := time.Since(t0)
		if r.Log != nil {
			n := 0
			if res != nil {
				n = len(res.Choices)
			}
			r.Log.LLM(llmRound, r.LLM.Model(), llmDur, err, n)
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
			if r.Progress != nil {
				fmt.Fprintf(r.Progress, "  llm %s  ·  reply\n", formatProgressDur(llmDur))
			}
			if msg.Content != "" {
				msgs = append(msgs, openai.AssistantMessage(msg.Content))
				newHist := msgs[sysOffset:]
				return msg.Content, newHist, streamedFinal, nil
			}
			if strings.TrimSpace(msg.Refusal) != "" {
				return "", nil, false, fmt.Errorf("model refusal: %s", strings.TrimSpace(msg.Refusal))
			}
			return "", nil, false, fmt.Errorf("model returned no content and no tool calls")
		}

		msgs = append(msgs, msg.ToParam())

		toolParts := make([]string, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			switch v := tc.AsAny().(type) {
			case openai.ChatCompletionMessageFunctionToolCall:
				args := json.RawMessage(v.Function.Arguments)
				if r.Log != nil {
					r.Log.ToolStart(v.Function.Name, agentlog.SummarizeArgs(v.Function.Name, args))
				}
				t1 := time.Now()
				out, err := r.Tools.Run(ctx, v.Function.Name, args)
				toolDur := time.Since(t1)
				summary := map[string]any{}
				if v.Function.Name == "run_command" {
					if ec := parseExitCodeFromRunOutput(out); ec != "" {
						summary["exit_code"] = ec
					}
				}
				if r.Log != nil {
					r.Log.ToolEnd(v.Function.Name, toolDur, err, summary)
				}
				compact := ProgressToolCompact(v.Function.Name, args)
				if err != nil {
					toolParts = append(toolParts, fmt.Sprintf("%s ✗ %s", compact, progressErrShort(err)))
				} else {
					seg := compact + " " + formatProgressDur(toolDur)
					if v.Function.Name == "run_command" {
						if ec := parseExitCodeFromRunOutput(out); ec != "" {
							seg += " exit=" + ec
						}
					}
					toolParts = append(toolParts, seg)
				}
				content := out
				if err != nil {
					content = fmt.Sprintf("error: %v", err)
				}
				msgs = append(msgs, openai.ToolMessage(content, v.ID))
			default:
				return "", nil, false, fmt.Errorf("unsupported tool call variant")
			}
		}
		if r.Progress != nil && len(toolParts) > 0 {
			fmt.Fprintf(r.Progress, "  llm %s  ·  %s\n", formatProgressDur(llmDur), strings.Join(toolParts, " · "))
		}
	}

	return "", nil, false, fmt.Errorf("exceeded AGENT_MAX_TOOL_STEPS (%d)", r.Cfg.MaxToolSteps)
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
