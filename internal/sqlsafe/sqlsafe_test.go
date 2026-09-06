package sqlsafe

import (
	"database/sql"
	"strings"
	"testing"
)

func TestQuoteMultipart(t *testing.T) {
	got, err := QuoteMultipart("dbo.Users")
	if err != nil {
		t.Fatal(err)
	}
	if got != "[dbo].[Users]" {
		t.Fatalf("got %q", got)
	}
	bad := []string{"", "dbo.", "dbo.Users;DROP", "dbo.[Users]", "a.b.c.d", "with space"}
	for _, name := range bad {
		if _, err := QuoteMultipart(name); err == nil {
			t.Fatalf("QuoteMultipart(%q) expected error", name)
		}
	}
}

func TestIsReadOnlyQuery(t *testing.T) {
	yes := []string{
		"SELECT * FROM dbo.Users",
		"WITH cte AS (SELECT 1 AS n) SELECT * FROM cte",
		";WITH cte AS (SELECT 1 AS n) SELECT * FROM cte;",
		"-- comment\nSELECT 1",
		"/* comment */ SELECT 1",
		"SELECT 'DROP TABLE x' AS statement_text",
		"SELECT [delete], \"update\" FROM dbo.audit_log",
		"SELECT 1 /* DELETE FROM x /* nested */ */",
		"SELECT @@VERSION AS version",
		"SELECT @delete AS requested_action FROM #update_log",
		"SELECT * FROM t ORDER BY id OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY",
	}
	for _, q := range yes {
		if !IsReadOnlyQuery(q) {
			t.Fatalf("expected read-only: %q", q)
		}
	}
	no := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"MERGE dbo.t AS t USING dbo.s AS s ON 1=1 WHEN MATCHED THEN UPDATE SET a=1",
		"CREATE TABLE x (id int)",
		"ALTER TABLE x ADD y int",
		"DROP TABLE x",
		"TRUNCATE TABLE x",
		"EXEC dbo.proc",
		"SELECT * FROM t; DROP TABLE t",
		"SELECT * INTO archive FROM source",
		"SELECT NEXT VALUE FOR dbo.sequence",
		"SELECT 1; SELECT 2",
		"SELECT 1; WAITFOR DELAY '00:00:10'",
		"SET ROWCOUNT 1; SELECT * FROM t",
		"SELECT * FROM OPENQUERY(remote_server, 'DELETE FROM x OUTPUT deleted.*')",
		"SELECT 'unterminated",
		"; SELECT 1",
	}
	for _, q := range no {
		if IsReadOnlyQuery(q) {
			t.Fatalf("expected not read-only: %q", q)
		}
	}
}

func TestBindNamedParameters(t *testing.T) {
	args, err := BindNamedParameters(
		"SELECT * FROM t WHERE id = @id AND parent_id = @id2 OR owner_id = @ID -- @ignored\nAND note = '@also_ignored' AND id = @id",
		map[string]any{"id2": 84, "id": 42},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 {
		t.Fatalf("args = %#v", args)
	}
	first, ok := args[0].(sql.NamedArg)
	if !ok || !strings.EqualFold(first.Name, "id") || first.Value != 42 {
		t.Fatalf("first arg = %#v", args[0])
	}
	second, ok := args[1].(sql.NamedArg)
	if !ok || !strings.EqualFold(second.Name, "id2") || second.Value != 84 {
		t.Fatalf("second arg = %#v", args[1])
	}
}

func TestBindNamedParametersRejectsMismatches(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		params map[string]any
	}{
		{"missing", "SELECT * FROM t WHERE id = @id", nil},
		{"unused", "SELECT * FROM t", map[string]any{"id": 1}},
		{"invalid name", "SELECT * FROM t", map[string]any{"bad-name": 1}},
		{"case collision", "SELECT * FROM t WHERE id = @id", map[string]any{"id": 1, "ID": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BindNamedParameters(tt.query, tt.params); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestBindNamedParametersIgnoresSystemVariables(t *testing.T) {
	args, err := BindNamedParameters("SELECT @@VERSION, @@SPID", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v", args)
	}
}
