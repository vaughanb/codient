package codientcli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"

	"codient/internal/a2aserver"
	"codient/internal/agent"
	"codient/internal/agentlog"
	"codient/internal/config"
	"codient/internal/openaiclient"
	"codient/internal/prompt"
)

const codientVersion = "0.1.0"

func runA2AServer(ctx context.Context, cfg *config.Config, addr string, agentLog *agentlog.Logger) int {
	if err := cfg.RequireModel(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 2
	}

	client := openaiclient.New(cfg)
	handler := a2aserver.New(a2aserver.Config{
		Cfg: cfg,
		LLMForMode: func(_ prompt.Mode) agent.ChatClient {
			return client
		},
		Log:     agentLog,
		Version: codientVersion,
		Addr:    addr,
	})

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "a2a: failed to listen on %s: %v\n", addr, err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "codient: A2A server listening on %s\n", addr)
	fmt.Fprintf(os.Stderr, "codient: agent card at http://%s/.well-known/agent-card.json\n", addr)
	fmt.Fprintf(os.Stderr, "codient: workspace=%s model=%s\n", cfg.EffectiveWorkspace(), cfg.Model)

	server := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		server.Close()
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "a2a: %v\n", err)
		return 1
	}
	return 0
}
