package tools

import (
	"reflect"
	"testing"

	"mssql-mcp/internal/config"
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
		}},
		{config.DMLRW, []string{
			"search_schema", "describe_table", "list_table", "list_databases", "list_environments",
			"profile_table", "inspect_relationships", "inspect_dependencies", "explain_query",
			"read_data", "test_connection", "validate_environment_config",
			"insert_data", "update_data", "delete_data",
		}},
		{config.DDLRW, []string{
			"search_schema", "describe_table", "list_table", "list_databases", "list_environments",
			"profile_table", "inspect_relationships", "inspect_dependencies", "explain_query",
			"read_data", "test_connection", "validate_environment_config",
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

func TestMutationTarget(t *testing.T) {
	table, where, args, err := mutationTarget("dbo.Users", "id = @id AND tenant = @tenant", map[string]any{"tenant": "a", "id": 42})
	if err != nil {
		t.Fatal(err)
	}
	if table != "[dbo].[Users]" {
		t.Fatalf("table = %q", table)
	}
	if where != "id = @p1 AND tenant = @p2" {
		t.Fatalf("where = %q", where)
	}
	if len(args) != 2 || args[0] != 42 || args[1] != "a" {
		t.Fatalf("args = %#v", args)
	}
}

func TestMutationTargetRejectsUnsafeWhere(t *testing.T) {
	bad := []string{"", "1=1; DROP TABLE Users", "id IN (SELECT id FROM x); DELETE FROM x"}
	for _, where := range bad {
		if _, _, _, err := mutationTarget("dbo.Users", where, nil); err == nil {
			t.Fatalf("expected error for where %q", where)
		}
	}
}

func TestRenumberWhere(t *testing.T) {
	got := renumberWhere("id = @p1 AND tenant = @p2", 3)
	if got != "id = @p4 AND tenant = @p5" {
		t.Fatalf("got %q", got)
	}
}
