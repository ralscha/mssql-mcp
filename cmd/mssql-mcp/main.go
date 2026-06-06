package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"mssql-mcp/internal/config"
	mssqldb "mssql-mcp/internal/db"
	"mssql-mcp/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("configuration error: %v", err)
		return 1
	}

	client, err := mssqldb.Open(cfg)
	if err != nil {
		log.Printf("database setup error: %v", err)
		return 1
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("database close error: %v", err)
		}
	}()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "mssql-mcp",
		Version: "0.1.0",
	}, &mcp.ServerOptions{
		Instructions: "MCP server for Microsoft SQL Server. Tools are registered according to MSSQL_ACCESS_LEVEL.",
	})
	tools.Register(server, client)

	if err := runServer(context.Background(), cfg, server); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

func runServer(ctx context.Context, cfg config.Config, server *mcp.Server) error {
	switch cfg.Transport {
	case config.StdioTransport:
		return server.Run(ctx, &mcp.StdioTransport{})
	case config.SSETransport:
		handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)
		mux := http.NewServeMux()
		mux.Handle(cfg.SSEPath, handler)
		log.Printf("mssql-mcp listening for SSE at http://%s%s", cfg.HTTPAddr, cfg.SSEPath)
		return http.ListenAndServe(cfg.HTTPAddr, mux)
	default:
		return cfg.Validate()
	}
}
