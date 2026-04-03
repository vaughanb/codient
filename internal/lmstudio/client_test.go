package lmstudio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"codient/internal/config"
)

func testConfig(baseURL, model string) *config.Config {
	return &config.Config{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		APIKey:        "test-key",
		Model:         model,
		MaxConcurrent: 3,
		MaxToolSteps:  8,
	}
}

func TestPingModels_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"alpha","object":"model"}]}`))
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL+"/v1", "alpha"))
	if err := c.PingModels(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPingModels_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL+"/v1", "m"))
	if err := c.PingModels(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL+"/v1", "m"))
	ids, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("got %#v", ids)
	}
}

func TestChatCompletion_MockServer(t *testing.T) {
	body := `{
  "id": "c1",
  "object": "chat.completion",
  "created": 1,
  "model": "test-model",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "from-mock"},
    "finish_reason": "stop"
  }]
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL+"/v1", "test-model"))
	res, err := c.ChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model: shared.ChatModel("test-model"),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("hi"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Choices) != 1 || res.Choices[0].Message.Content != "from-mock" {
		t.Fatalf("got %+v", res.Choices)
	}
}

func TestChatCompletion_SemaphoreLimitsConcurrency(t *testing.T) {
	var mu sync.Mutex
	cur, peak := 0, 0
	delay := 30 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(delay)
		mu.Lock()
		cur--
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"."},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL+"/v1", "m")
	cfg.MaxConcurrent = 2
	client := New(cfg)

	const n = 8
	var wg sync.WaitGroup
	var errCount atomic.Int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := client.ChatCompletion(context.Background(), openai.ChatCompletionNewParams{
				Model:    shared.ChatModel("m"),
				Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("x")},
			})
			if err != nil {
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if errCount.Load() != 0 {
		t.Fatalf("%d goroutines returned errors from ChatCompletion", errCount.Load())
	}

	if peak > 2 {
		t.Fatalf("peak concurrent requests was %d, want <= 2", peak)
	}
}

func TestStreamChatCompletion_MockServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !bytes.Contains(b, []byte(`"stream":true`)) {
			http.Error(w, "expected stream", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		chunks := []string{
			`{"id":"s","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":""}]}`,
			`{"id":"s","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":""}]}`,
			`[DONE]`,
		}
		for _, ch := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ch)
			fl.Flush()
		}
	}))
	defer srv.Close()

	c := New(testConfig(srv.URL+"/v1", "m"))
	var buf bytes.Buffer
	err := c.StreamChatCompletion(context.Background(), openai.ChatCompletionNewParams{
		Model:    shared.ChatModel("m"),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "Hello" {
		t.Fatalf("got %q", buf.String())
	}
}
