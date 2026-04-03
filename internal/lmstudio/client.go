// Package lmstudio wraps the OpenAI-compatible LM Studio HTTP API (openai-go client + helpers).
//
// LLM_MAX_CONCURRENT in the agent layer limits how many in-flight HTTP requests hit LM Studio;
// LM Studio's own "max concurrent predictions" is a separate server-side limit—tune both together.
package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/config"
)

// Client is the narrow surface used by the agent.
type Client struct {
	oa     openai.Client
	base   string
	apiKey string
	model  shared.ChatModel
	llmSem *semaphore
}

type semaphore struct {
	ch chan struct{}
}

func newSemaphore(n int) *semaphore {
	return &semaphore{ch: make(chan struct{}, n)}
}

func (s *semaphore) acquire(ctx context.Context) error {
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *semaphore) release() {
	<-s.ch
}

// New builds an OpenAI API client pointed at LM Studio and a concurrency limiter for chat calls.
func New(cfg *config.Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	oa := openai.NewClient(
		option.WithBaseURL(base),
		option.WithAPIKey(cfg.APIKey),
	)
	return &Client{
		oa:     oa,
		base:   base,
		apiKey: cfg.APIKey,
		model:  shared.ChatModel(cfg.Model),
		llmSem: newSemaphore(cfg.MaxConcurrent),
	}
}

// Model returns the configured model id.
func (c *Client) Model() string {
	return string(c.model)
}

// PingModels GETs /v1/models relative to the configured base URL (health / discovery).
func (c *Client) PingModels(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("models endpoint: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// ChatCompletion performs a non-streaming chat completion (acquires LLM semaphore).
func (c *Client) ChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if err := c.llmSem.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.llmSem.release()
	return c.oa.Chat.Completions.New(ctx, params)
}

// StreamChatCompletion streams a chat completion with no tools; writes assistant text deltas to w.
// Acquires the same LLM semaphore as non-streaming calls.
func (c *Client) StreamChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams, w io.Writer) error {
	if err := c.llmSem.acquire(ctx); err != nil {
		return err
	}
	defer c.llmSem.release()

	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := c.oa.Chat.Completions.NewStreaming(ctx, params)
	for stream.Next() {
		chunk := stream.Current()
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				if _, err := io.WriteString(w, ch.Delta.Content); err != nil {
					return err
				}
			}
		}
	}
	return stream.Err()
}

// ChatCompletionStream streams a completion (with or without tools), writes assistant
// content deltas to w, and returns the accumulated completion (same shape as non-streaming).
// Used by the agent so long replies show tokens as they arrive while tool rounds still work.
func (c *Client) ChatCompletionStream(ctx context.Context, params openai.ChatCompletionNewParams, w io.Writer) (*openai.ChatCompletion, error) {
	if err := c.llmSem.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.llmSem.release()

	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := c.oa.Chat.Completions.NewStreaming(ctx, params)
	var acc openai.ChatCompletionAccumulator
	for stream.Next() {
		chunk := stream.Current()
		if !acc.AddChunk(chunk) {
			return nil, fmt.Errorf("chat stream: chunk accumulation failed")
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				if _, err := io.WriteString(w, ch.Delta.Content); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	out := acc.ChatCompletion
	return &out, nil
}

// ModelsResponse is a minimal parse of GET /v1/models for CLI listing.
type ModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListModels fetches model ids (optional helper for operators).
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("models: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out ModelsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}
