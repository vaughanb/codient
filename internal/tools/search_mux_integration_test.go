//go:build integration

package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Integration tests for the searchmux-backed web_search pipeline.
//
// These hit real search engines over the network; run with:
//
//	CODIENT_INTEGRATION=1 go test -tags=integration -run TestIntegration_SearchMux ./internal/tools/...
//
// Skipped in -short mode and when CODIENT_INTEGRATION is not set.

func skipUnlessSearchIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CODIENT_INTEGRATION") != "1" {
		t.Skip("set CODIENT_INTEGRATION=1 to run live search tests")
	}
	if testing.Short() {
		t.Skip("skipping live network call in -short mode")
	}
}

func TestIntegration_SearchMux_Init(t *testing.T) {
	skipUnlessSearchIntegration(t)
	s, err := defaultSearchMux()
	if err != nil {
		t.Fatalf("defaultSearchMux() error: %v", err)
	}
	if s == nil {
		t.Fatal("defaultSearchMux() returned nil searcher")
	}
}

func TestIntegration_SearchMux_BasicQuery(t *testing.T) {
	skipUnlessSearchIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := searchMuxSearch(ctx, "Go programming language", 5, 30*time.Second)
	if err != nil {
		t.Fatalf("searchMuxSearch error: %v", err)
	}
	if !strings.Contains(out, "Search results for") {
		t.Errorf("expected formatted header, got: %s", truncate(out, 300))
	}
	if strings.Contains(out, "No results found") {
		t.Errorf("expected at least one result for a common query, got: %s", truncate(out, 300))
	}
}

func TestIntegration_SearchMux_CodingQuery(t *testing.T) {
	skipUnlessSearchIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := searchMuxSearch(ctx, "how to parse JSON in Go", 5, 30*time.Second)
	if err != nil {
		t.Fatalf("searchMuxSearch error: %v", err)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "json") {
		t.Errorf("expected results mentioning JSON, got: %s", truncate(out, 500))
	}
}

func TestIntegration_SearchMux_MaxResultsCap(t *testing.T) {
	skipUnlessSearchIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := searchMuxSearch(ctx, "Kubernetes", 3, 30*time.Second)
	if err != nil {
		t.Fatalf("searchMuxSearch error: %v", err)
	}
	// Count numbered results (lines starting with "N. ")
	lines := strings.Split(out, "\n")
	count := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if len(l) > 2 && l[0] >= '1' && l[0] <= '9' && strings.Contains(l[:3], ".") {
			count++
		}
	}
	if count > 3 {
		t.Errorf("requested max 3 results but got %d numbered entries", count)
	}
}

func TestIntegration_SearchMux_EmptyQuery(t *testing.T) {
	skipUnlessSearchIntegration(t)
	ctx := context.Background()
	_, err := searchMuxSearch(ctx, "", 5, 10*time.Second)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

