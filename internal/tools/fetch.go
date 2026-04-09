package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openai/openai-go/v3/shared"
)

const (
	defaultFetchMaxBytes   = 1024 * 1024
	maxFetchMaxBytes       = 10 * 1024 * 1024
	defaultFetchTimeoutSec = 30
	maxFetchRedirects      = 5
)

// FetchOptions configures the optional fetch_url tool (HTTPS GET with host allowlist).
type FetchOptions struct {
	AllowHosts  []string // Lowercase hostnames; subdomains match (e.g. api.example.com matches example.com).
	MaxBytes    int      // Response body cap (default 1MiB, max 10MiB).
	TimeoutSec  int      // Per-request timeout (default 30s).
	Session     *SessionFetchAllow
	// PromptUnknownHost is called when the URL host is not in AllowHosts or Session. If nil, unknown hosts are rejected.
	PromptUnknownHost func(ctx context.Context, host, pageURL string) FetchHostChoice
	// PersistFetchHost saves host to persistent config when the user chooses FetchHostAllowAlways. Optional.
	PersistFetchHost func(host string) error
	// IncludePreapproved merges a built-in documentation/code-domain allowlist (plus path rules). Default on; set CODIENT_FETCH_PREAPPROVED=0 to disable.
	IncludePreapproved bool
	// RateLimiter throttles fetch requests to prevent abuse. Optional; nil disables rate limiting.
	RateLimiter *RateLimiter
}

func registerFetchURL(r *Registry, opts *FetchOptions) {
	if opts == nil {
		return
	}
	if len(opts.AllowHosts) == 0 && opts.PromptUnknownHost == nil && !opts.IncludePreapproved {
		return
	}
	maxB := opts.MaxBytes
	if maxB < 1 {
		maxB = defaultFetchMaxBytes
	}
	if maxB > maxFetchMaxBytes {
		maxB = maxFetchMaxBytes
	}
	timeout := time.Duration(opts.TimeoutSec) * time.Second
	if timeout < time.Second {
		timeout = defaultFetchTimeoutSec * time.Second
	}
	baseAllow := append([]string(nil), opts.AllowHosts...)
	sess := opts.Session
	prompt := opts.PromptUnknownHost
	persist := opts.PersistFetchHost
	includePre := opts.IncludePreapproved
	limiter := opts.RateLimiter

	r.Register(Tool{
		Name: "fetch_url",
		Description: "HTTPS GET of a URL (text response only). " +
			"A built-in allowlist covers common language and framework documentation hosts (disable with CODIENT_FETCH_PREAPPROVED=0). " +
			"Additional hosts: CODIENT_FETCH_ALLOW_HOSTS or ~/.codient/config.json (fetch_allow_hosts). " +
			"In interactive sessions, other hosts may be approved once, for the session, or always (saved). " +
			"Redirects must stay on HTTPS and on an allowed host/path. " +
			"Response body is capped by max_bytes (default 1MiB). " +
			"Use for public documentation—never for secrets or internal networks.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "HTTPS URL to fetch (GET only).",
				},
				"max_bytes": map[string]any{
					"type":        "integer",
					"description": "Optional cap on response bytes (default from config, max 10MiB).",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				URL      string `json:"url"`
				MaxBytes *int   `json:"max_bytes"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			limit := maxB
			if p.MaxBytes != nil && *p.MaxBytes > 0 {
				limit = *p.MaxBytes
				if limit > maxFetchMaxBytes {
					limit = maxFetchMaxBytes
				}
			}
			raw := strings.TrimSpace(p.URL)
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" {
				return "", fmt.Errorf("invalid URL")
			}
			if u.Scheme != "https" {
				return "", fmt.Errorf("only https URLs are allowed")
			}
			host := u.Hostname()
			if host == "" {
				return "", fmt.Errorf("missing host")
			}
			if ip := net.ParseIP(host); ip != nil && isDisallowedFetchIP(ip) {
				return "", fmt.Errorf("refusing to fetch disallowed IP %q", host)
			}

			reqPath := urlPathForFetch(u)
			allowOnce := make(map[string]struct{})
			allowed := func(h, p string) bool {
				if includePre && PreapprovedFetchAllows(h, p) {
					return true
				}
				if hostAllowedFetch(h, baseAllow) {
					return true
				}
				if sess != nil && sess.IsAllowed(h) {
					return true
				}
				if _, ok := allowOnce[normalizeFetchHostKey(h)]; ok {
					return true
				}
				return false
			}

			if !allowed(host, reqPath) {
				if prompt == nil {
					return "", fmt.Errorf("host %q is not allowlisted (set CODIENT_FETCH_ALLOW_HOSTS)", host)
				}
				switch prompt(ctx, host, raw) {
				case FetchHostDeny:
					return "", fmt.Errorf("fetch denied for host %q", host)
				case FetchHostAllowOnce:
					allowOnce[normalizeFetchHostKey(host)] = struct{}{}
				case FetchHostAllowSession:
					if sess != nil {
						sess.Add(host)
					}
				case FetchHostAllowAlways:
					if sess != nil {
						sess.Add(host)
					}
					if persist != nil {
						_ = persist(host)
					}
				default:
					return "", fmt.Errorf("fetch denied for host %q", host)
				}
			}

			if err := limiter.Wait(ctx); err != nil {
				return "", fmt.Errorf("rate limit: %w", err)
			}
			return fetchURL(ctx, raw, allowed, limit, timeout)
		},
	})
}

func urlPathForFetch(u *url.URL) string {
	if u == nil {
		return "/"
	}
	p := u.Path
	if p == "" {
		return "/"
	}
	return p
}

func fetchURL(ctx context.Context, raw string, allowHostPath func(host, path string) bool, maxBytes int, timeout time.Duration) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid URL")
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("only https URLs are allowed")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host")
	}
	if ip := net.ParseIP(host); ip != nil && isDisallowedFetchIP(ip) {
		return "", fmt.Errorf("refusing to fetch disallowed IP %q", host)
	}
	if !allowHostPath(host, urlPathForFetch(u)) {
		return "", fmt.Errorf("host %q is not allowlisted", host)
	}
	if timeout <= 0 {
		timeout = defaultFetchTimeoutSec * time.Second
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https URL forbidden")
			}
			h := req.URL.Hostname()
			if ip := net.ParseIP(h); ip != nil && isDisallowedFetchIP(ip) {
				return fmt.Errorf("redirect to disallowed IP %q", h)
			}
			if !allowHostPath(h, urlPathForFetch(req.URL)) {
				return fmt.Errorf("redirect to non-allowlisted host %q", h)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "codient-fetch/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body []byte
	body, err = io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	truncated := len(body) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}
	if !utf8.Valid(body) {
		return "", fmt.Errorf("response is not valid UTF-8 (refusing to return binary)")
	}
	s := string(body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		s = htmlToMarkdown(s)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "HTTP %s\nContent-Type: %s\n", resp.Status, resp.Header.Get("Content-Type"))
	if truncated {
		fmt.Fprintf(&out, "[truncated: exceeded max_bytes=%d]\n", maxBytes)
	}
	out.WriteString("\n")
	out.WriteString(s)
	return out.String(), nil
}

// HostAllowedFetch reports whether host matches allowlist entries (suffix rules; disallowed IPs never match).
func HostAllowedFetch(host string, allowed []string) bool {
	return hostAllowedFetch(host, allowed)
}

func hostAllowedFetch(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedFetchIP(ip) {
			return false
		}
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

func isDisallowedFetchIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// IPv4-mapped / documentation / etc.
		if ip4[0] == 0 {
			return true
		}
	}
	return false
}
