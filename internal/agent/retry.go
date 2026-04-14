package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
)

// callLLMWithRetry wraps the LLM call with retry logic for transient errors.
// Returns the completion, whether streaming was used for the successful call, and any error.
func (r *Runner) callLLMWithRetry(ctx context.Context, params openai.ChatCompletionNewParams, streamTo io.Writer) (*openai.ChatCompletion, bool, error) {
	maxAttempts := r.Cfg.MaxLLMRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			if backoff > 8*time.Second {
				backoff = 8 * time.Second
			}
			if r.Progress != nil {
				status := fmt.Sprintf("retry %d/%d after %s", attempt, r.Cfg.MaxLLMRetries, backoff)
				if line := FormatStatusProgressLine(r.ProgressPlain, r.ProgressMode, status); line != "" {
					fmt.Fprintf(r.Progress, "%s\n", line)
				}
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}

		res, streamed, err := r.callLLMOnce(ctx, params, streamTo)
		if err == nil {
			return res, streamed, nil
		}
		lastErr = err

		if !isTransientError(err) {
			return nil, false, err
		}
	}
	return nil, false, fmt.Errorf("after %d retries: %w", r.Cfg.MaxLLMRetries, lastErr)
}

func (r *Runner) callLLMOnce(ctx context.Context, params openai.ChatCompletionNewParams, streamTo io.Writer) (*openai.ChatCompletion, bool, error) {
	useStream := streamTo != nil
	// OpenAI-compatible local servers often return incomplete tool_calls over SSE; the accumulator
	// then sees an empty ToolCalls slice and the agent skips native tool execution. Non-streaming
	// completions usually preserve tool_calls. Opt back in with CODIENT_STREAM_WITH_TOOLS=1.
	if useStream && len(params.Tools) > 0 && r.Cfg != nil && !r.Cfg.StreamWithTools {
		useStream = false
	}
	if useStream {
		if sc, ok := r.LLM.(streamChatClient); ok {
			res, err := sc.ChatCompletionStream(ctx, params, streamTo)
			return res, true, err
		}
	}
	res, err := r.LLM.ChatCompletion(ctx, params)
	return res, false, err
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "429") {
		return true
	}
	return false
}
