package codientcli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/openai/openai-go/v3"

	"codient/internal/assistout"
	"codient/internal/config"
	"codient/internal/openaiclient"
	"codient/internal/prompt"
	"codient/internal/sessionstore"
)

// switchMode changes the session mode, filtering history to keep only text messages
// and injecting a transition note. The system prompt and tool registry are rebuilt.
func (s *session) switchMode(newMode prompt.Mode) {
	if s.mode == newMode {
		fmt.Fprintf(os.Stderr, "codient: already in %s mode\n", newMode)
		return
	}
	oldMode := s.mode

	s.history = filterHistoryForModeSwitch(s.history)

	note := fmt.Sprintf("[Mode switched from %s to %s. The conversation above is from the previous mode.]", oldMode, newMode)
	s.history = append(s.history, openai.UserMessage(note))

	var spinner *modelSpinner
	if s.cfg.Model != "" && !s.cfg.Plain {
		spinner = startModelSpinner(os.Stderr, s.cfg.Model)
	}

	s.mode = newMode
	s.client = openaiclient.New(s.cfg)
	s.registry = buildRegistry(s.cfg, newMode, s)
	s.systemPrompt = buildAgentSystemPrompt(s.cfg, s.registry, newMode, s.userSystem, s.repoInstructions, s.projectContext, effectiveAutoCheckCmd(s.cfg))

	if spinner != nil {
		spinner.stop(fmt.Sprintf("codient: switched to %s mode (model: %s)", newMode, s.cfg.Model))
	} else {
		fmt.Fprintf(os.Stderr, "codient: switched to %s mode\n", newMode)
	}

	if newMode == prompt.ModeBuild {
		s.warnIfNotGitRepo()
	}
	config.SaveLastMode(string(newMode))
	fmt.Fprintf(os.Stderr, "%s\n", assistout.ModeHint(s.cfg.Plain, string(newMode)))
}

// filterHistoryForModeSwitch keeps user and assistant text messages, dropping tool
// messages and tool-call-only assistant messages. Assistant messages that have both
// text content and tool calls are preserved as text-only.
func filterHistoryForModeSwitch(history []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range history {
		switch {
		case m.OfUser != nil:
			out = append(out, m)
		case m.OfAssistant != nil:
			content := extractAssistantText(m)
			if content != "" {
				out = append(out, openai.AssistantMessage(content))
			}
		case m.OfSystem != nil:
			// Drop old system messages; a new one will be prepended by the runner.
		case m.OfTool != nil:
			// Drop tool result messages.
		}
	}
	return out
}

// extractAssistantText gets the text content from an assistant message,
// ignoring any tool call data.
func extractAssistantText(m openai.ChatCompletionMessageParamUnion) string {
	if m.OfAssistant == nil {
		return ""
	}
	// Try direct content field first.
	b, err := json.Marshal(m.OfAssistant.Content)
	if err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil && s != "" {
		return s
	}
	// For complex content (array of parts), extract text from the raw JSON.
	return sessionstore.MessageContent(mustMarshal(m))
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
