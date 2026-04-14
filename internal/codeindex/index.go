// Package codeindex builds and queries a file-level embedding index for semantic code search.
package codeindex

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Embedder abstracts the embedding API so the index is testable without a live server.
type Embedder interface {
	CreateEmbedding(ctx context.Context, model string, inputs []string) ([][]float64, error)
}

// Entry is one indexed file.
type Entry struct {
	Path    string
	ModTime time.Time
	Vector  []float64
}

// Result is a single search hit.
type Result struct {
	Path    string
	Score   float64
	Preview string
}

// Index holds the file-level embedding index for a workspace.
type Index struct {
	mu        sync.RWMutex
	workspace string
	model     string
	embedder  Embedder
	entries   []Entry
	ready     chan struct{}
	disabled  bool
	buildErr  error
}

// New creates an Index. Call BuildOrUpdate in a goroutine to populate it.
func New(workspace string, embedder Embedder, model string) *Index {
	return &Index{
		workspace: workspace,
		model:     model,
		embedder:  embedder,
		ready:     make(chan struct{}),
	}
}

// Ready returns a channel that is closed when the initial build completes.
func (idx *Index) Ready() <-chan struct{} {
	return idx.ready
}

// BuildErr returns the error from the most recent build, if any.
func (idx *Index) BuildErr() error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.buildErr
}

// BuildOrUpdate walks the workspace, diffs against the persisted index, embeds
// new/changed files, and saves the result. It closes idx.Ready() when done.
func (idx *Index) BuildOrUpdate(ctx context.Context) {
	defer func() {
		select {
		case <-idx.ready:
		default:
			close(idx.ready)
		}
	}()

	existing, _ := loadIndex(idx.workspace, idx.model)

	docs, err := walkWorkspace(idx.workspace)
	if err != nil {
		idx.mu.Lock()
		idx.buildErr = fmt.Errorf("walk: %w", err)
		idx.mu.Unlock()
		return
	}

	byPath := make(map[string]Entry, len(existing))
	for _, e := range existing {
		byPath[e.Path] = e
	}

	var toEmbed []document
	var kept []Entry
	docPaths := make(map[string]struct{}, len(docs))

	for _, d := range docs {
		docPaths[d.Path] = struct{}{}
		if e, ok := byPath[d.Path]; ok && e.ModTime.Equal(d.ModTime) && len(e.Vector) > 0 {
			kept = append(kept, e)
		} else {
			toEmbed = append(toEmbed, d)
		}
	}

	if len(toEmbed) > 0 {
		texts := make([]string, len(toEmbed))
		for i, d := range toEmbed {
			texts[i] = d.Text
		}
		vectors, err := idx.embedder.CreateEmbedding(ctx, idx.model, texts)
		if err != nil {
			idx.mu.Lock()
			idx.buildErr = fmt.Errorf("embedding: %w", err)
			if len(kept) > 0 {
				idx.entries = kept
			}
			idx.mu.Unlock()
			return
		}
		for i, d := range toEmbed {
			if i < len(vectors) && len(vectors[i]) > 0 {
				kept = append(kept, Entry{
					Path:    d.Path,
					ModTime: d.ModTime,
					Vector:  vectors[i],
				})
			}
		}
	}

	idx.mu.Lock()
	idx.entries = kept
	idx.buildErr = nil
	idx.mu.Unlock()

	_ = saveIndex(idx.workspace, idx.model, kept)
}

// Len returns the number of indexed files.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// Query embeds the query string and returns the top-K most similar files.
// It blocks until the index is ready (or ctx is cancelled).
func (idx *Index) Query(ctx context.Context, query string, topK int) ([]Result, error) {
	select {
	case <-idx.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	idx.mu.RLock()
	disabled := idx.disabled
	buildErr := idx.buildErr
	entries := idx.entries
	idx.mu.RUnlock()

	if disabled {
		return nil, fmt.Errorf("semantic search is disabled (embedding API not available)")
	}
	if buildErr != nil {
		return nil, fmt.Errorf("index build failed: %w", buildErr)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	qVec, err := idx.embedder.CreateEmbedding(ctx, idx.model, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(qVec) == 0 || len(qVec[0]) == 0 {
		return nil, fmt.Errorf("empty query embedding")
	}

	type scored struct {
		entry Entry
		score float64
	}
	results := make([]scored, 0, len(entries))
	for _, e := range entries {
		s := cosineSimilarity(qVec[0], e.Vector)
		results = append(results, scored{entry: e, score: s})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	if topK > len(results) {
		topK = len(results)
	}

	out := make([]Result, topK)
	for i := 0; i < topK; i++ {
		preview := filePreview(idx.workspace, results[i].entry.Path, 20)
		out[i] = Result{
			Path:    results[i].entry.Path,
			Score:   results[i].score,
			Preview: preview,
		}
	}
	return out, nil
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

func filePreview(workspace, rel string, maxLines int) string {
	abs := filepath.Join(workspace, rel)
	data, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}
