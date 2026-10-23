// Command mcp serves the product API to AI agents as MCP tools over stdio.
// It is a gRPC client of a running server; every call is made as the agent
// actor, so the audit log shows which changes came from an agent.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/velovsky/insurance-product-service/internal/agenttools"
)

var version = "dev"

func main() {
	addr := flag.String("addr", envOr("GRPC_ADDR", "localhost:50051"), "address of the gRPC server")
	actor := flag.String("actor", envOr("MCP_ACTOR", "agent:claude"), "agent identity sent as x-actor; must start with \"agent:\"")
	flag.Parse()

	// stdout carries the MCP protocol, so logs go to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*addr, *actor); err != nil {
		log.Error("mcp server stopped", "error", err)
		os.Exit(1)
	}
}

func run(addr, actor string) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	srv, err := agenttools.NewServer(conn, actor, version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
