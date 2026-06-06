package sqlsafe

import "testing"

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
		"-- comment\nSELECT 1",
		"/* comment */ SELECT 1",
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
	}
	for _, q := range no {
		if IsReadOnlyQuery(q) {
			t.Fatalf("expected not read-only: %q", q)
		}
	}
}

func TestRowLimit(t *testing.T) {
	if !NeedsRowLimit("SELECT * FROM t") {
		t.Fatal("plain select should need row limit")
	}
	if NeedsRowLimit("SELECT TOP 10 * FROM t") {
		t.Fatal("TOP select should not need row limit")
	}
	batch := RowCountBatch("SELECT * FROM t;", 25)
	want := "SET ROWCOUNT 25;\nSELECT * FROM t;\nSET ROWCOUNT 0;"
	if batch != want {
		t.Fatalf("got %q, want %q", batch, want)
	}
}
