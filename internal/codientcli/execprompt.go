package codientcli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codient/internal/tools"
)

// execPromptDenied is bound to the REPL session: prompts on stderr, reads from the session scanner.
func (s *session) execPromptDenied(ctx context.Context, deniedKey string, argv []string) tools.ExecPromptChoice {
	_ = ctx
	if s.scanner == nil || !stdinIsInteractive() {
		fmt.Fprintf(os.Stderr, "codient: command %q is not on the exec allowlist (non-interactive; denied)\n", deniedKey)
		return tools.ExecPromptDeny
	}
	argPreview := strings.Join(argv, " ")
	if len(argPreview) > 120 {
		argPreview = argPreview[:117] + "..."
	}
	fmt.Fprintf(os.Stderr, "\ncodient: command %q is not on the exec allowlist.\n", deniedKey)
	fmt.Fprintf(os.Stderr, "  argv: %s\n", argPreview)
	fmt.Fprintf(os.Stderr, "[y] allow this command for this session  [a] allow all commands for this session  [n] deny: ")
	if !s.scanner.Scan() {
		return tools.ExecPromptDeny
	}
	line := strings.ToLower(strings.TrimSpace(s.scanner.Text()))
	switch line {
	case "y", "yes":
		return tools.ExecPromptAllowSession
	case "a", "all":
		return tools.ExecPromptAllowAll
	case "n", "no", "":
		return tools.ExecPromptDeny
	default:
		fmt.Fprintf(os.Stderr, "codient: not recognized — denying\n")
		return tools.ExecPromptDeny
	}
}
