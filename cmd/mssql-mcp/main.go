package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mssql-mcp/internal/config"
	mssqldb "mssql-mcp/internal/db"
	"mssql-mcp/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("mssql-mcp %s\n", version)
		return
	}
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "MCP server for Microsoft SQL Server. Tools are registered according to MSSQL_ACCESS_LEVEL.",
	})
	tools.Register(server, client)

	if err := runServer(ctx, cfg, server); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		return 1
	}
	return 0
}

func runServer(ctx context.Context, cfg config.Config, server *mcp.Server) error {
	switch cfg.Transport {
	case config.StdioTransport:
		return server.Run(ctx, &mcp.StdioTransport{})
	case config.HTTPTransport:
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{Stateless: true})
		mux := http.NewServeMux()
		mux.Handle(cfg.HTTPPath, handler)
		listener, err := net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			return err
		}
		httpServer := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		log.Printf("mssql-mcp listening for stateless Streamable HTTP at http://%s%s", listener.Addr(), cfg.HTTPPath)
		errCh := make(chan error, 1)
		go func() {
			errCh <- httpServer.Serve(listener)
		}()
		select {
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				_ = httpServer.Close()
				return err
			}
			err := <-errCh
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	default:
		return cfg.Validate()
	}
}
