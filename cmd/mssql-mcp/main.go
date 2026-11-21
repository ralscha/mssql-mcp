package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"mssql-mcp/internal/config"
	mssqldb "mssql-mcp/internal/db"
	"mssql-mcp/internal/tools"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func buildVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("mssql-mcp %s\n", buildVersion())
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
		Version: buildVersion(),
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
		var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, &mcp.StreamableHTTPOptions{
			Stateless:                    true,
			PropagateRequestCancellation: true,
		})
		if cfg.HTTPBearerToken != "" {
			expected := []byte(cfg.HTTPBearerToken)
			verifier := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
				if subtle.ConstantTimeCompare([]byte(token), expected) != 1 {
					return nil, auth.ErrInvalidToken
				}
				return &auth.TokenInfo{UserID: "bearer"}, nil
			}
			handler = auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
				AllowMissingExpiration: true,
			})(handler)
		}
		handler = http.NewCrossOriginProtection().Handler(handler)
		if cfg.HTTPBearerToken == "" && cfg.HTTPListensBeyondLoopback() {
			log.Printf("WARNING: MSSQL_HTTP_ADDR %q accepts non-local connections without authentication; set MSSQL_HTTP_BEARER_TOKEN", cfg.HTTPAddr)
		}

		mux := http.NewServeMux()
		mux.Handle(cfg.HTTPPath, handler)
		listener, err := net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			return err
		}
		httpServer := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
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
		if err := cfg.Validate(); err != nil {
			return err
		}
		return fmt.Errorf("unsupported transport %q", cfg.Transport)
	}
}
