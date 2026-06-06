package main

import (
	"context"
	"log"
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

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Print(err)
		return 1
	}
	return 0
}
