package main

import (
	"context"
	"testing"
	"time"

	"mssql-mcp/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPServerShutsDownWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cfg := config.Config{Transport: config.HTTPTransport, HTTPAddr: "127.0.0.1:0", HTTPPath: "/mcp"}

	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, cfg, server)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServer() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP server did not shut down after cancellation")
	}
}
