package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/shared"
)

const (
	defaultSearchResults    = 5
	maxSearchResults        = 10
	defaultSearchTimeoutSec = 30
)

// SearchOptions configures the web_search tool.
type SearchOptions struct {
	MaxResults  int          // Default 5, max 10.
	TimeoutSec  int          // Per-request timeout (default 30s).
	RateLimiter *RateLimiter // Optional rate limiter shared with fetch_url.
}

func registerWebSearch(r *Registry, opts *SearchOptions, fetch *FetchOptions) {
	if opts == nil {
		return
	}

	fetchEnabled := fetch != nil && (len(fetch.AllowHosts) > 0 || fetch.PromptUnknownHost != nil || fetch.IncludePreapproved)
	desc := "Search the web for documentation, error messages, API references, or library usage. " +
		"Returns a numbered list of results with title, URL, and snippet. " +
		"Prefer this over guessing about unfamiliar libraries or APIs. " +
		"Uses embedded metasearch (multiple engines, merged results)."
	if fetchEnabled {
		desc += " You may chain with fetch_url (allowlisted hosts only) to read full page text from a result URL."
	} else {
		desc += " Summarize from snippets and links; fetch_url is not enabled in this session—do not call it."
	}

	maxN := opts.MaxResults
	if maxN < 1 {
		maxN = defaultSearchResults
	}
	if maxN > maxSearchResults {
		maxN = maxSearchResults
	}
	timeout := time.Duration(opts.TimeoutSec) * time.Second
	if timeout < time.Second {
		timeout = defaultSearchTimeoutSec * time.Second
	}
	limiter := opts.RateLimiter

	r.Register(Tool{
		Name:        "web_search",
		Description: desc,
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Number of results to return (default 5, max 10).",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Query      string `json:"query"`
				MaxResults *int   `json:"max_results"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			n := maxN
			if p.MaxResults != nil && *p.MaxResults > 0 {
				n = *p.MaxResults
				if n > maxSearchResults {
					n = maxSearchResults
				}
			}
			q := strings.TrimSpace(p.Query)
			if err := limiter.Wait(ctx); err != nil {
				return "", fmt.Errorf("rate limit: %w", err)
			}
			return searchMuxSearch(ctx, q, n, timeout)
		},
	})
}

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

func formatSearchResults(query string, results []searchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n\n", query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
		if i < len(results)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
