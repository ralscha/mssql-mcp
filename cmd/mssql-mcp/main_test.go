package main

import (
	"context"
	"net"
	"net/http"
	"strings"
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

func TestHTTPBearerTokenAuth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	cfg := config.Config{
		Transport:       config.HTTPTransport,
		HTTPAddr:        addr,
		HTTPPath:        "/mcp",
		HTTPBearerToken: "secret-token",
	}

	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, cfg, server)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runServer() = %v", err)
		}
	}()

	endpoint := "http://" + addr + "/mcp"
	var resp *http.Response
	for range 20 {
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTP server did not become ready: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("authenticated request was rejected with 401")
	}
}
