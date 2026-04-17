package mcpclient

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func startTestServer(t *testing.T) (*mcp.InMemoryTransport, *mcp.InMemoryTransport) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v0.1"}, nil)

	type GreetInput struct {
		Name string `json:"name" jsonschema:"the name"`
	}
	type GreetOutput struct {
		Greeting string `json:"greeting"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "greet",
		Description: "Say hello",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GreetInput) (*mcp.CallToolResult, GreetOutput, error) {
		return nil, GreetOutput{Greeting: "Hello, " + input.Name + "!"}, nil
	})

	go func() {
		_ = server.Run(context.Background(), serverTransport)
	}()

	return serverTransport, clientTransport
}

func TestManager_ConnectAndListTools(t *testing.T) {
	_, clientTransport := startTestServer(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer session.Close()

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "greet" {
		t.Errorf("tool name = %q, want greet", result.Tools[0].Name)
	}
}

func TestManager_CallTool(t *testing.T) {
	_, clientTransport := startTestServer(t)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "greet",
		Arguments: map[string]any{"name": "Alice"},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if res.IsError {
		t.Fatal("CallTool returned IsError")
	}

	text := formatCallToolResult(res)
	if !strings.Contains(text, "Hello, Alice!") {
		t.Errorf("result = %q, expected to contain 'Hello, Alice!'", text)
	}
}

func TestRegistryName_RoundTrip(t *testing.T) {
	name := RegistryName("filesystem", "read_dir")
	if name != "mcp__filesystem__read_dir" {
		t.Errorf("RegistryName = %q, want mcp__filesystem__read_dir", name)
	}
	sid, tn := ParseRegistryName(name)
	if sid != "filesystem" || tn != "read_dir" {
		t.Errorf("ParseRegistryName(%q) = (%q, %q), want (filesystem, read_dir)", name, sid, tn)
	}
}

func TestParseRegistryName_NonMCP(t *testing.T) {
	sid, tn := ParseRegistryName("read_file")
	if sid != "" || tn != "" {
		t.Errorf("ParseRegistryName(read_file) = (%q, %q), want empty", sid, tn)
	}
}

func TestFormatCallToolResult_TextContent(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "line 1"},
			&mcp.TextContent{Text: "line 2"},
		},
	}
	got := formatCallToolResult(res)
	if got != "line 1\nline 2" {
		t.Errorf("formatCallToolResult = %q, want %q", got, "line 1\nline 2")
	}
}

func TestFormatCallToolResult_IsError(t *testing.T) {
	res := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "something broke"}},
		IsError: true,
	}
	got := formatCallToolResult(res)
	if !strings.HasPrefix(got, "error: ") {
		t.Errorf("expected error prefix, got %q", got)
	}
}

func TestInputSchemaToMap(t *testing.T) {
	m, err := inputSchemaToMap(nil)
	if err != nil {
		t.Fatalf("nil schema: %v", err)
	}
	if m["type"] != "object" {
		t.Errorf("nil schema type = %v, want object", m["type"])
	}

	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)
	m, err = inputSchemaToMap(raw)
	if err != nil {
		t.Fatalf("raw schema: %v", err)
	}
	if m["type"] != "object" {
		t.Errorf("raw schema type = %v, want object", m["type"])
	}
}

func TestMergeMCPProcessEnv_ScrubsAndOverrides(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "should-not-appear")
	t.Setenv("PATH", os.Getenv("PATH"))
	env := mergeMCPProcessEnv(map[string]string{"MCP_EXTRA": "ok"})
	hasExtra := false
	hasToken := false
	for _, e := range env {
		if strings.HasPrefix(e, "MCP_EXTRA=") {
			hasExtra = true
		}
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			hasToken = true
		}
	}
	if !hasExtra {
		t.Fatal("expected MCP_EXTRA in merged env")
	}
	if hasToken {
		t.Fatal("GITHUB_TOKEN should be scrubbed from subprocess env")
	}
}
