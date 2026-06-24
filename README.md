# mssql-mcp

An [MCP (Model Context Protocol)](https://modelcontextprotocol.io/) server that gives AI assistants structured, read-optimized access to Microsoft SQL Server databases. It exposes schema exploration, data profiling, relationship analysis, query explanation, and safe read/write operations through a standardized MCP interface.

## Features

- **23 MCP tools** covering schema search, table description, data profiling, relationship/dependency inspection, object listing, DDL inspection, query explanation, connection testing, and tiered write access
- **Tiered access model** via `MSSQL_ACCESS_LEVEL`: `READONLY` (default), `DML-RW` (adds insert/update/delete), `DDL-RW` (adds create/drop table/index)
- **SQL-safe design** with identifier quoting, multipart name validation, and read-only query enforcement
- **Read-only query guard** that rejects mutating statements (`INSERT`, `UPDATE`, `DELETE`, `MERGE`, `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `EXEC`, etc.) on the `read_data` tool
- **Mutation confirmation** with preview mode that shows affected rows before executing writes when `MSSQL_REQUIRE_CONFIRMATION` is enabled
- **Explain plan** via `SHOWPLAN_XML` for understanding query performance
- **Connection testing** that validates connectivity and reports latency
- **Environment listing** showing current server, database, and access-level configuration

## Installation

Download the latest release for your platform from the [Releases](https://github.com/ralscha/mssql-mcp/releases) page. 

## Usage

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MSSQL_SERVER` | Yes | - | SQL Server hostname or IP |
| `MSSQL_DATABASE` | Yes | - | Database name |
| `MSSQL_USERNAME` | Yes | - | Login username |
| `MSSQL_PASSWORD` | Yes | - | Login password |
| `MSSQL_PORT` | No | `1433` | SQL Server port |
| `MSSQL_ACCESS_LEVEL` | No | `READONLY` | `READONLY`, `DML-RW`, or `DDL-RW` |
| `MSSQL_ENCRYPT` | No | `true` | SQL Server driver encryption setting |
| `MSSQL_TRUST_SERVER_CERTIFICATE` | No | `false` | Trust self-signed certificates |
| `MSSQL_CONNECTION_TIMEOUT` | No | `30` | Connection timeout in seconds |
| `MSSQL_QUERY_TIMEOUT` | No | `120` | Query timeout in seconds |
| `MSSQL_MAX_ROWS_DEFAULT` | No | `1000` | Default row limit for queries |
| `MSSQL_REQUIRE_CONFIRMATION` | No | `true` | Require confirm flag for writes |
| `MSSQL_TRANSPORT` | No | `stdio` | MCP transport: `stdio` or `sse` |
| `MSSQL_HTTP_ADDR` | No | `:8080` | HTTP listen address when `MSSQL_TRANSPORT=sse` |
| `MSSQL_SSE_PATH` | No | `/sse` | SSE endpoint path when `MSSQL_TRANSPORT=sse` |

`MSSQL_ENCRYPT` is passed through to the SQL Server driver as the `encrypt` connection parameter. Supported values include:

- `strict` - Data sent between client and server is encrypted end-to-end using [TDS 8.0](https://learn.microsoft.com/en-us/sql/relational-databases/security/networking/tds-8?view=sql-server-ver16).
- `disable` - Data sent between client and server is not encrypted.
- `false`, `optional`, `no`, `0`, `f` - Data sent between client and server is not encrypted beyond the login packet.
- `true`, `mandatory`, `yes`, `1`, `t` - Data sent between client and server is encrypted.

### Running as an MCP Server

By default, the server communicates over stdio. Configure your MCP client to launch it:

```json
{
  "mcpServers": {
    "mssql": {
      "command": "/path/to/bin/mssql-mcp",
      "env": {
        "MSSQL_SERVER": "localhost",
        "MSSQL_DATABASE": "YourDatabase",
        "MSSQL_USERNAME": "sa",
        "MSSQL_PASSWORD": "YourPassword",
        "MSSQL_ENCRYPT": "true",
        "MSSQL_TRUST_SERVER_CERTIFICATE": "true",
        "MSSQL_ACCESS_LEVEL": "READONLY"
      }
    }
  }
}
```

To serve MCP over SSE instead, set `MSSQL_TRANSPORT=sse` and run the server as an HTTP process:

```bash
MSSQL_TRANSPORT=sse \
MSSQL_HTTP_ADDR=:8080 \
MSSQL_SSE_PATH=/sse \
/path/to/bin/mssql-mcp
```

Then configure an SSE-capable MCP client to connect to:

```text
http://localhost:8080/sse
```

### Access Levels

- **`READONLY`** (default) - Schema exploration, object listing, data reading, profiling, relationship inspection, DDL inspection, query explanation, connection testing. 17 tools.
- **`DML-RW`** - All read-only tools plus `insert_data`, `update_data`, `delete_data`. 20 tools.
- **`DDL-RW`** - All DML tools plus `create_table`, `create_index`, `drop_table`. 23 tools.

Mutations (`update_data`, `delete_data`, `drop_table`) require a `"confirm": true` flag when `MSSQL_REQUIRE_CONFIRMATION` is enabled (the default). Without confirmation, the server returns a preview of the affected rows instead.

## MCP Tools

### Read-Only (READONLY)

| Tool | Description |
|------|-------------|
| `search_schema` | Search tables and columns by name pattern with pagination |
| `describe_table` | Get columns, primary keys, foreign keys, and indexes for a table |
| `list_table` | List tables, optionally filtered by schema and name |
| `list_databases` | List all databases on the server |
| `list_environments` | Show current connection and access-level configuration |
| `profile_table` | Row count, null counts, distinct counts, min/max per column, with optional data samples |
| `inspect_relationships` | List foreign keys going out of and into a table |
| `inspect_dependencies` | Find objects (views, procedures, functions) that depend on a table |
| `explain_query` | Get the XML execution plan for a read-only query |
| `read_data` | Execute a read-only SELECT query with row limits |
| `test_connection` | Ping the server and return latency and server version info |
| `validate_environment_config` | Validate that all environment variables are correctly configured |
| `list_schemas` | List database schemas and owners |
| `list_views` | List views and their definitions |
| `list_triggers` | List table triggers, events, timing, and definitions |
| `show_create_table` | Generate a CREATE TABLE statement for an existing table |
| `table_size` | Report estimated row counts and table/index size in KB |

### DML (DML-RW)

| Tool | Description |
|------|-------------|
| `insert_data` | Insert one or more rows into a table |
| `update_data` | Update rows matching a WHERE clause (with optional preview) |
| `delete_data` | Delete rows matching a WHERE clause (with optional preview) |

### DDL (DDL-RW)

| Tool | Description |
|------|-------------|
| `create_table` | Create a table with column definitions, primary keys, and identity columns |
| `create_index` | Create a standard or unique index on specified columns |
| `drop_table` | Drop a table (with optional preview/confirmation) |

## License

MIT License. See [LICENSE](LICENSE) for details.
