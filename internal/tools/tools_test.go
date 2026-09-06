package tools

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mssql-mcp/internal/config"
	mssqldb "mssql-mcp/internal/db"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolNamesForLevel(t *testing.T) {
	tests := []struct {
		level config.AccessLevel
		want  []string
	}{
		{config.ReadOnly, []string{
			"search_schema", "describe_table", "list_table", "list_databases", "list_environments",
			"profile_table", "inspect_relationships", "inspect_dependencies", "explain_query",
			"read_data", "test_connection", "validate_environment_config",
			"list_schemas", "list_views", "list_triggers", "show_create_table", "table_size",
		}},
		{config.DMLRW, []string{
			"search_schema", "describe_table", "list_table", "list_databases", "list_environments",
			"profile_table", "inspect_relationships", "inspect_dependencies", "explain_query",
			"read_data", "test_connection", "validate_environment_config",
			"list_schemas", "list_views", "list_triggers", "show_create_table", "table_size",
			"insert_data", "update_data", "delete_data",
		}},
		{config.DDLRW, []string{
			"search_schema", "describe_table", "list_table", "list_databases", "list_environments",
			"profile_table", "inspect_relationships", "inspect_dependencies", "explain_query",
			"read_data", "test_connection", "validate_environment_config",
			"list_schemas", "list_views", "list_triggers", "show_create_table", "table_size",
			"insert_data", "update_data", "delete_data",
			"create_table", "create_index", "drop_table",
		}},
	}
	for _, tt := range tests {
		got := ToolNamesForLevel(tt.level)
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("ToolNamesForLevel(%s) = %#v, want %#v", tt.level, got, tt.want)
		}
	}
}

func TestToolDescriptionsForAllTools(t *testing.T) {
	for _, name := range ToolNamesForLevel(config.DDLRW) {
		tool := (&Registry{}).tool(name)
		if strings.TrimSpace(tool.Description) == "" {
			t.Fatalf("tool %q has no description", name)
		}
	}
}

func TestToolSchemasOnlyRequireEssentialInputs(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "test"}, nil)
	Register(server, &mssqldb.Client{Config: config.Config{AccessLevel: config.DDLRW}})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "schema-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()
	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"search_schema": nil,
		"list_table":    nil,
		"profile_table": {"table"},
		"read_data":     {"query"},
		"explain_query": {"query"},
		"update_data":   {"table", "values", "where"},
		"delete_data":   {"table", "where"},
		"create_index":  {"table", "name", "columns"},
		"drop_table":    {"table"},
	}
	for _, tool := range result.Tools {
		required, checked := want[tool.Name]
		if !checked {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(schema.Required, required) {
			t.Errorf("tool %q requires %#v, want %#v", tool.Name, schema.Required, required)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("tools missing from schema response: %#v", want)
	}
}

func TestMutationTarget(t *testing.T) {
	table, where, args, err := mutationTarget("dbo.Users", "id = @id AND tenant = @tenant", map[string]any{"tenant": "a", "id": 42})
	if err != nil {
		t.Fatal(err)
	}
	if table != "[dbo].[Users]" {
		t.Fatalf("table = %q", table)
	}
	if where != "id = @id AND tenant = @tenant" {
		t.Fatalf("where = %q", where)
	}
	if len(args) != 2 || args[0] != sql.Named("id", 42) || args[1] != sql.Named("tenant", "a") {
		t.Fatalf("args = %#v", args)
	}
}

func TestMutationTargetRejectsUnsafeWhere(t *testing.T) {
	bad := []string{"", "1=1; DROP TABLE Users", "id IN (SELECT id FROM x); DELETE FROM x", "id IN (SELECT id INTO copied FROM x)"}
	for _, where := range bad {
		if _, _, _, err := mutationTarget("dbo.Users", where, nil); err == nil {
			t.Fatalf("expected error for where %q", where)
		}
	}
}

func TestMutationTargetBindsExactParameterNames(t *testing.T) {
	_, where, args, err := mutationTarget("dbo.Users", "id = @id AND parent_id = @id2 AND note = '@ignored'", map[string]any{"id": 1, "id2": 2})
	if err != nil {
		t.Fatal(err)
	}
	if where != "id = @id AND parent_id = @id2 AND note = '@ignored'" {
		t.Fatalf("where = %q", where)
	}
	if len(args) != 2 || args[0] != sql.Named("id", 1) || args[1] != sql.Named("id2", 2) {
		t.Fatalf("args = %#v", args)
	}
	if _, _, _, err := mutationTarget("dbo.Users", "id = @missing", nil); err == nil {
		t.Fatal("expected missing parameter error")
	}
	if _, _, _, err := mutationTarget("dbo.Users", "id = @__mcp_value_1", map[string]any{"__mcp_value_1": 1}); err == nil {
		t.Fatal("expected reserved parameter name error")
	}
}

func TestNormalizeSQLType(t *testing.T) {
	valid := map[string]string{
		"INT":            "int",
		"varchar(255)":   "varchar(255)",
		"NVARCHAR (MAX)": "nvarchar(max)",
		"decimal(18, 2)": "decimal(18,2)",
		"datetime2(7)":   "datetime2(7)",
		"float(53)":      "float(53)",
	}
	for input, want := range valid {
		got, err := normalizeSQLType(input)
		if err != nil {
			t.Fatalf("normalizeSQLType(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeSQLType(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{
		"", "int primary key", "varchar", "varchar(0)", "varchar(8001)",
		"nvarchar(4001)", "char(max)", "decimal(0,0)", "decimal(10,11)",
		"datetime2(8)", "float(54)", "made_up_type",
	}
	for _, input := range invalid {
		if _, err := normalizeSQLType(input); err == nil {
			t.Fatalf("normalizeSQLType(%q) expected error", input)
		}
	}
}

func TestIdentityTypeAllowed(t *testing.T) {
	for _, typ := range []string{"tinyint", "smallint", "int", "bigint", "decimal(18,0)", "numeric(9)"} {
		if !identityTypeAllowed(typ) {
			t.Fatalf("expected identity type %q to be allowed", typ)
		}
	}
	for _, typ := range []string{"varchar(10)", "decimal(18,2)", "numeric(9,1)", "float"} {
		if identityTypeAllowed(typ) {
			t.Fatalf("expected identity type %q to be rejected", typ)
		}
	}
}

func TestInsertDataUsesAtomicTransaction(t *testing.T) {
	tests := []struct {
		name         string
		failAt       int
		wantErr      bool
		wantCommit   bool
		wantRollback bool
	}{
		{name: "commit", wantCommit: true},
		{name: "rollback", failAt: 2, wantErr: true, wantRollback: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &transactionRecordingConn{failAt: tt.failAt}
			driverName := fmt.Sprintf("insert-transaction-%d", testDriverID.Add(1))
			sql.Register(driverName, &transactionRecordingDriver{conn: conn})
			database, err := sql.Open(driverName, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })

			registry := &Registry{client: &mssqldb.Client{
				DB: database,
				Config: config.Config{
					AccessLevel:  config.DMLRW,
					QueryTimeout: time.Second,
				},
			}}
			_, output, err := registry.insertData(context.Background(), nil, InsertInput{
				Table: "dbo.Items",
				Rows: []map[string]any{
					{"id": 1, "name": "first"},
					{"id": 2, "name": "second"},
				},
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if output.Executed != tt.wantCommit {
				t.Fatalf("executed = %v", output.Executed)
			}
			if conn.committed != tt.wantCommit || conn.rolledBack != tt.wantRollback {
				t.Fatalf("committed = %v, rolledBack = %v", conn.committed, conn.rolledBack)
			}
		})
	}
}

var testDriverID atomic.Uint64

type transactionRecordingDriver struct {
	conn *transactionRecordingConn
}

func (d *transactionRecordingDriver) Open(string) (driver.Conn, error) {
	return d.conn, nil
}

type transactionRecordingConn struct {
	executions int
	failAt     int
	committed  bool
	rolledBack bool
}

func (*transactionRecordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (*transactionRecordingConn) Close() error { return nil }

func (c *transactionRecordingConn) Begin() (driver.Tx, error) {
	return &transactionRecordingTx{conn: c}, nil
}

func (c *transactionRecordingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.executions++
	if c.executions == c.failAt {
		return nil, errors.New("injected insert failure")
	}
	return driver.RowsAffected(1), nil
}

type transactionRecordingTx struct {
	conn *transactionRecordingConn
}

func (tx *transactionRecordingTx) Commit() error {
	tx.conn.committed = true
	return nil
}

func (tx *transactionRecordingTx) Rollback() error {
	tx.conn.rolledBack = true
	return nil
}
