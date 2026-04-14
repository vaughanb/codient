package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vaughanb/searchmux"
	"github.com/vaughanb/searchmux/engines"
)

var (
	defaultMux     *searchmux.Searcher
	defaultMuxErr  error
	defaultMuxOnce sync.Once
)

func defaultSearchMux() (*searchmux.Searcher, error) {
	defaultMuxOnce.Do(func() {
		reg := searchmux.NewRegistry()
		reg.Register("google", engines.NewGoogle)
		reg.Register("duckduckgo", engines.NewDuckDuckGo)
		reg.Register("stackoverflow", engines.NewStackOverflow)
		reg.Register("github", engines.NewGitHub)
		reg.Register("pkggodev", engines.NewPkgGoDev)
		reg.Register("npm", engines.NewNpm)
		reg.Register("pypi", engines.NewPyPI)
		reg.Register("hackernews", engines.NewHackerNews)
		reg.Register("wikipedia", engines.NewWikipedia)
		cfgs := []searchmux.EngineConfig{
			{Name: "google", Enabled: true, Weight: 1.2},
			{Name: "stackoverflow", Enabled: true, Weight: 1.2},
			{Name: "github", Enabled: true, Weight: 1.2},
			{Name: "duckduckgo", Enabled: true, Weight: 1.0},
			{Name: "pkggodev", Enabled: true, Weight: 0.9},
			{Name: "npm", Enabled: true, Weight: 0.9},
			{Name: "pypi", Enabled: true, Weight: 0.9},
			{Name: "hackernews", Enabled: true, Weight: 0.7},
			{Name: "wikipedia", Enabled: true, Weight: 0.7},
		}
		cl := &http.Client{Timeout: 60 * time.Second}
		defaultMux, defaultMuxErr = searchmux.New(reg, cfgs, searchmux.WithHTTPClient(cl))
	})
	return defaultMux, defaultMuxErr
}

func searchMuxSearch(ctx context.Context, query string, n int, timeout time.Duration) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	s, err := defaultSearchMux()
	if err != nil {
		return "", fmt.Errorf("searchmux: %w", err)
	}
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sr, err := s.Search(searchCtx, searchmux.Query{Term: query, Pageno: 1})
	if err != nil {
		return "", err
	}
	limit := n
	if limit > len(sr.Results) {
		limit = len(sr.Results)
	}
	results := make([]searchResult, limit)
	for i := 0; i < limit; i++ {
		r := sr.Results[i]
		results[i] = searchResult{Title: r.Title, URL: r.URL, Snippet: r.Content}
	}
	return formatSearchResults(query, results), nil
}
