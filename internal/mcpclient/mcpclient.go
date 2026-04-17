// Package mcpclient manages MCP (Model Context Protocol) client connections.
// It connects to configured MCP servers, discovers their tools, and dispatches
// tool calls back through the MCP protocol.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"codient/internal/config"
	"codient/internal/sandbox"
	"codient/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Ensure Manager implements tools.MCPToolSource at compile time.
var _ tools.MCPToolSource = (*Manager)(nil)

type serverConn struct {
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// Manager holds MCP client state: one mcp.Client and zero or more server sessions.
type Manager struct {
	client  *mcp.Client
	mu      sync.RWMutex
	servers map[string]*serverConn
}

// NewManager creates a Manager with a shared MCP client.
func NewManager(version string) *Manager {
	if version == "" {
		version = "dev"
	}
	c := mcp.NewClient(&mcp.Implementation{
		Name:    "codient",
		Version: version,
	}, nil)
	return &Manager{
		client:  c,
		servers: make(map[string]*serverConn),
	}
}

// Connect opens sessions to every server in the config map.
// Servers that fail to connect are logged to warnings but do not cause an error.
func (m *Manager) Connect(ctx context.Context, servers map[string]config.MCPServerConfig) []string {
	var warnings []string
	for id, cfg := range servers {
		transport, err := transportFor(cfg)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp %s: %v", id, err))
			continue
		}
		session, err := m.client.Connect(ctx, transport, nil)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp %s: connect: %v", id, err))
			continue
		}
		toolsResult, err := session.ListTools(ctx, nil)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("mcp %s: list tools: %v", id, err))
			_ = session.Close()
			continue
		}
		m.mu.Lock()
		m.servers[id] = &serverConn{session: session, tools: toolsResult.Tools}
		m.mu.Unlock()
	}
	return warnings
}

func mergeMCPProcessEnv(extra map[string]string) []string {
	m := make(map[string]string)
	for _, e := range sandbox.ScrubOSEnviron(nil) {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func transportFor(cfg config.MCPServerConfig) (mcp.Transport, error) {
	switch {
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Env = mergeMCPProcessEnv(cfg.Env)
		return &mcp.CommandTransport{Command: cmd}, nil
	case cfg.URL != "":
		return &mcp.StreamableClientTransport{Endpoint: cfg.URL}, nil
	default:
		return nil, fmt.Errorf("server config must set command or url")
	}
}

// Tools returns all discovered tools across all connected servers.
// Implements tools.MCPToolSource.
func (m *Manager) Tools() []tools.MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []tools.MCPToolInfo
	for id, sc := range m.servers {
		for _, t := range sc.tools {
			schema, _ := inputSchemaToMap(t.InputSchema)
			out = append(out, tools.MCPToolInfo{
				ServerID:    id,
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			})
		}
	}
	return out
}

// ServerIDs returns the IDs of all connected servers.
func (m *Manager) ServerIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.servers))
	for id := range m.servers {
		ids = append(ids, id)
	}
	return ids
}

// ServerTools returns the tools for a specific server, or nil if not connected.
func (m *Manager) ServerTools(serverID string) []tools.MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sc, ok := m.servers[serverID]
	if !ok {
		return nil
	}
	out := make([]tools.MCPToolInfo, 0, len(sc.tools))
	for _, t := range sc.tools {
		schema, _ := inputSchemaToMap(t.InputSchema)
		out = append(out, tools.MCPToolInfo{
			ServerID:    serverID,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

// CallTool dispatches a tool/call to the appropriate server session.
// Implements tools.MCPToolSource.
func (m *Manager) CallTool(ctx context.Context, serverID, toolName string, argsJSON json.RawMessage) (string, error) {
	m.mu.RLock()
	sc, ok := m.servers[serverID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("MCP server %q not connected", serverID)
	}

	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}

	res, err := sc.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return "", err
	}
	return formatCallToolResult(res), nil
}

// Close shuts down all MCP server sessions.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.servers {
		_ = sc.session.Close()
	}
	m.servers = make(map[string]*serverConn)
}

// RegistryName returns the namespaced tool name for the tools.Registry.
func RegistryName(serverID, toolName string) string {
	return "mcp__" + serverID + "__" + toolName
}

// ParseRegistryName splits a namespaced name back into serverID and toolName.
// Returns empty strings if the name is not an MCP tool.
func ParseRegistryName(name string) (serverID, toolName string) {
	if !strings.HasPrefix(name, "mcp__") {
		return "", ""
	}
	rest := strings.TrimPrefix(name, "mcp__")
	idx := strings.Index(rest, "__")
	if idx < 0 {
		return "", ""
	}
	return rest[:idx], rest[idx+2:]
}

func formatCallToolResult(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s, %d bytes]", v.MIMEType, len(v.Data)))
		case *mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource: %s]", v.URI))
		case *mcp.EmbeddedResource:
			if v.Resource != nil && v.Resource.Text != "" {
				parts = append(parts, v.Resource.Text)
			} else if v.Resource != nil {
				parts = append(parts, fmt.Sprintf("[embedded resource: %s]", v.Resource.URI))
			}
		default:
			b, _ := json.Marshal(c)
			parts = append(parts, string(b))
		}
	}
	text := strings.Join(parts, "\n")
	if res.IsError {
		return "error: " + text
	}
	return text
}

// inputSchemaToMap converts the MCP tool's InputSchema (which is any) to map[string]any.
func inputSchemaToMap(schema any) (map[string]any, error) {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	if m, ok := schema.(map[string]any); ok {
		return m, nil
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
