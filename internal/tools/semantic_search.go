package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"codient/internal/codeindex"

	"github.com/openai/openai-go/v3/shared"
)

func registerSemanticSearch(r *Registry, idx *codeindex.Index) {
	if idx == nil {
		return
	}

	r.Register(Tool{
		Name: "semantic_search",
		Description: "Find files in the workspace related to a concept or topic using semantic similarity " +
			"(e.g. 'authentication middleware', 'database migrations', 'error handling'). " +
			"Prefer this over grep when you need to discover code by meaning rather than exact text.",
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Natural language description of what you are looking for.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Maximum results to return (default 10, max 25).",
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
			query := strings.TrimSpace(p.Query)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			topK := 10
			if p.MaxResults != nil && *p.MaxResults > 0 {
				topK = *p.MaxResults
			}
			if topK > 25 {
				topK = 25
			}

			results, err := idx.Query(ctx, query, topK)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "No results found.", nil
			}
			return formatSemanticResults(results), nil
		},
	})
}

func formatSemanticResults(results []codeindex.Result) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s (score: %.3f)\n", i+1, filepath.ToSlash(r.Path), r.Score)
		if r.Preview != "" {
			lines := strings.Split(r.Preview, "\n")
			for _, l := range lines {
				fmt.Fprintf(&b, "   %s\n", l)
			}
		}
		if i < len(results)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
