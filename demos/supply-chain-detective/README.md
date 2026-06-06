# Supply Chain Detective Demo

This demo starts a disposable Microsoft SQL Server with Testcontainers, seeds it with a deliberately interesting supply-chain data model, and prints an MCP configuration plus an investigation prompt for an agent.

The data is for a fictional company, Northwind Relay. It includes vendors, warehouses, products, purchase orders, shipments, inventory snapshots, quality incidents, customers, sales orders, and payments. There are several clues in the data: late shipments, repeated quality failures, stockout risk, margin pressure, and a suspicious vendor/customer overlap.

## Run

Prerequisites:

- Docker Desktop or another Docker-compatible runtime
- Go 1.26+

```bash
cd demos/supply-chain-detective
go run .
```

To use a different SQL Server image, set `MSSQL_DEMO_IMAGE`. This is handy when you already have a compatible image cached locally:

```bash
export MSSQL_DEMO_IMAGE=mcr.microsoft.com/mssql/server:2022-latest
go run .
```

The demo keeps the SQL Server container alive until you press Enter or stop the process. While it is running, copy the printed MCP server configuration into your agent client and ask it to investigate the prompt printed by the demo.

The starter also refreshes `.env` with the current Testcontainers host and port after the database has been seeded. Keep the starter process running while you run the MCP client in another terminal; stopping it removes the container and makes those connection settings stale.

The container image requires accepting the Microsoft SQL Server EULA. The demo passes `mssql.WithAcceptEULA()` explicitly.

## Run The MCP Client

In a second terminal, create a `.env` file from `.env.example` if it does not exist yet and add `OPENAI_API_KEY`. The database starter writes the `MSSQL_*` values automatically. Then run:

```bash
cd demos/supply-chain-detective
go run ./client
```

The client loads `.env`, starts `go run ./cmd/mssql-mcp` over stdio, exposes the MCP tools to Eino, and asks an Eino `ChatModelAgent` to produce the findings report. It answers all of the questions below through model-selected MCP tool calls rather than querying SQL Server directly.

The client prints a transcript as it runs: the user prompt, available MCP tools, assistant tool calls, MCP tool results, token usage metadata when available, and the final answer.

By default the client assumes the main `mssql-mcp` module is two directories above the demo module. Set `MSSQL_MCP_SERVER_DIR` if you run it from a copied or moved demo directory.

## What The Agent Should Discover

The agent has enough data to answer questions like:

- Which supplier is causing operational risk?
- Which products are near stockout despite strong demand?
- Are delayed shipments connected to quality incidents?
- Are there customers or payments that deserve extra scrutiny?
- Which tables and relationships matter for the analysis?

Useful MCP tools for the investigation:

- `list_table`
- `search_schema`
- `describe_table`
- `inspect_relationships`
- `profile_table`
- `read_data`

Start with schema discovery, then join across purchasing, logistics, inventory, sales, and finance tables.
