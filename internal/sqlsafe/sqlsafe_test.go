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
	spaced, err := QuoteMultipart("dbo.Order Details")
	if err != nil {
		t.Fatal(err)
	}
	if spaced != "[dbo].[Order Details]" {
		t.Fatalf("got %q", spaced)
	}
	unicodeIdent, err := QuoteIdentifier("Unit-Price_€")
	if err != nil {
		t.Fatal(err)
	}
	if unicodeIdent != "[Unit-Price_€]" {
		t.Fatalf("got %q", unicodeIdent)
	}
	rawQuoted, err := QuoteRawIdentifier("weird]col")
	if err != nil {
		t.Fatal(err)
	}
	if rawQuoted != "[weird]]col]" {
		t.Fatalf("got %q", rawQuoted)
	}
	bad := []string{"", "dbo.", "dbo.Users;DROP", "dbo.[Users]", "otherdb.dbo.Users", "a.b.c.d", "bad--comment"}
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
		"WITH a AS (SELECT 1 AS n), b AS (SELECT 2 AS n) SELECT * FROM a UNION ALL SELECT * FROM b",
		"(SELECT 1 AS n) UNION (SELECT 2 AS n)",
		"-- comment\nSELECT 1",
		"/* comment */ SELECT 1",
		"SELECT 'DROP TABLE x' AS statement_text",
		"SELECT [delete], \"update\" FROM dbo.audit_log",
		"SELECT 1 /* DELETE FROM x /* nested */ */",
		"SELECT @@VERSION AS version",
		"SELECT @delete AS requested_action FROM #update_log",
		"SELECT * FROM t ORDER BY id OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY",
		"SELECT * FROM dbo.Orders o CROSS APPLY (SELECT TOP 1 * FROM dbo.OrderLines l WHERE l.OrderID = o.ID) sub",
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
		"SELECT 1 SELECT 2",
		"(SELECT 1) SELECT 2",
		"SELECT 1 WHERE 1=1 DISABLE TRIGGER ALL ON dbo.Users",
		"SELECT 1 WHERE 1=1 RECEIVE TOP (1) * FROM dbo.Queue",
		"SELECT 1 END CONVERSATION @handle",
		"SELECT 1 ADD SIGNATURE TO dbo.proc BY CERTIFICATE cert",
		"SELECT 1 WHERE 1=1 WHILE 1=1 SELECT 1",
		"SELECT * FROM dbo.Users WITH (TABLOCKX)",
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
		"SELECT * FROM t WHERE id = @id AND parent_id = @id2 OR owner_id = @ID -- @ignored\nAND note = '@also_ignored' AND id = @id AND col$1 = @price_usd$",
		map[string]any{"id2": float64(84), "id": 42, "price_usd$": float64(19.5)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 {
		t.Fatalf("args = %#v", args)
	}
	first, ok := args[0].(sql.NamedArg)
	if !ok || !strings.EqualFold(first.Name, "id") || first.Value != 42 {
		t.Fatalf("first arg = %#v", args[0])
	}
	second, ok := args[1].(sql.NamedArg)
	if !ok || !strings.EqualFold(second.Name, "id2") || second.Value != int64(84) {
		t.Fatalf("second arg = %#v (want int64(84))", args[1])
	}
	third, ok := args[2].(sql.NamedArg)
	if !ok || !strings.EqualFold(third.Name, "price_usd$") || third.Value != 19.5 {
		t.Fatalf("third arg = %#v", args[2])
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
		{"leading underscore", "SELECT * FROM t WHERE id = @_id", map[string]any{"_id": 1}},
		{"case collision", "SELECT * FROM t WHERE id = @id", map[string]any{"id": 1, "ID": 2}},
		{"composite slice", "SELECT * FROM t WHERE id = @id", map[string]any{"id": []any{1, 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BindNamedParameters(tt.query, tt.params); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestBindNamedParametersIgnoresSystemVariablesAndEmbeddedAt(t *testing.T) {
	args, err := BindNamedParameters("SELECT @@VERSION, @@SPID, col@1 FROM dbo.t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v", args)
	}
}

func TestLikePatternEscapesBracketWildcard(t *testing.T) {
	got := LikePattern("order_[1]*")
	if got != `order\_\[1]%` {
		t.Fatalf("LikePattern() = %q, want %q", got, `order\_\[1]%`)
	}
}
