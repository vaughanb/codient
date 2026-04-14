package codientcli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codient/internal/config"
	"codient/internal/tools"
)

// fetchPromptUnknownHost prompts on stderr and reads from the session scanner (REPL).
func (s *session) fetchPromptUnknownHost(ctx context.Context, host, pageURL string) tools.FetchHostChoice {
	_ = ctx
	s.fetchPromptMu.Lock()
	defer s.fetchPromptMu.Unlock()

	if tools.HostAllowedFetch(host, s.cfg.FetchAllowHosts) || (s.fetchAllow != nil && s.fetchAllow.IsAllowed(host)) || tools.PreapprovedFetchAllows(host, "/") {
		return tools.FetchHostAllowSession
	}

	if s.scanner == nil || !stdinIsInteractive() {
		fmt.Fprintf(os.Stderr, "codient: fetch host %q not allowlisted (non-interactive; denied)\n", host)
		return tools.FetchHostDeny
	}

	preview := strings.TrimSpace(pageURL)
	if len(preview) > 100 {
		preview = preview[:97] + "..."
	}
	fmt.Fprintf(os.Stderr, "\ncodient: fetch_url to host %q is not on your allowlist.\n", host)
	fmt.Fprintf(os.Stderr, "  %s\n", preview)
	fmt.Fprintf(os.Stderr, "[y] allow once  [s] allow this host for this session  [a] always allow (save to ~/.codient/config.json)  [n] deny: ")
	if !s.scanner.Scan() {
		return tools.FetchHostDeny
	}
	line := strings.ToLower(strings.TrimSpace(s.scanner.Text()))
	switch line {
	case "y", "yes", "once":
		return tools.FetchHostAllowOnce
	case "s", "session":
		return tools.FetchHostAllowSession
	case "a", "always", "all":
		return tools.FetchHostAllowAlways
	case "n", "no", "":
		return tools.FetchHostDeny
	default:
		fmt.Fprintf(os.Stderr, "codient: not recognized — denying fetch\n")
		return tools.FetchHostDeny
	}
}

// persistFetchHostToConfig appends host to persistent fetch_allow_hosts and updates in-memory cfg.
func (s *session) persistFetchHostToConfig(host string) error {
	if err := config.AppendPersistentFetchHost(host); err != nil {
		fmt.Fprintf(os.Stderr, "codient: could not save fetch allowlist: %v\n", err)
		return err
	}
	h := strings.ToLower(strings.TrimSpace(host))
	for _, x := range s.cfg.FetchAllowHosts {
		if x == h {
			return nil
		}
	}
	s.cfg.FetchAllowHosts = append(s.cfg.FetchAllowHosts, h)
	return nil
}
