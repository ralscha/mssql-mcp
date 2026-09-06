package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"mssql-mcp/internal/config"
)

func TestQueryReadOnlyAlwaysAppliesAndRestoresHardRowLimit(t *testing.T) {
	recorder := &recordingConn{}
	db := openRecordingDB(t, "row-limit-success", recorder)
	client := &Client{DB: db, Config: config.Config{MaxRowsDefault: 100, QueryTimeout: time.Second}}

	rows, err := client.QueryReadOnly(context.Background(), "SELECT TOP 999 * FROM dbo.Items", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["value"] != int64(1) {
		t.Fatalf("rows = %#v", rows)
	}
	want := []string{"SET ROWCOUNT 25", "SELECT TOP 999 * FROM dbo.Items", "SET ROWCOUNT 0"}
	if !reflect.DeepEqual(recorder.commands, want) {
		t.Fatalf("commands = %#v, want %#v", recorder.commands, want)
	}
}

func TestQueryReadOnlyRestoresRowLimitAfterQueryFailure(t *testing.T) {
	recorder := &recordingConn{queryErr: errors.New("query failed")}
	db := openRecordingDB(t, "row-limit-failure", recorder)
	client := &Client{DB: db, Config: config.Config{MaxRowsDefault: 100, QueryTimeout: time.Second}}

	if _, err := client.QueryReadOnly(context.Background(), "SELECT * FROM dbo.Items", 25); err == nil {
		t.Fatal("expected query error")
	}
	want := []string{"SET ROWCOUNT 25", "SELECT * FROM dbo.Items", "SET ROWCOUNT 0"}
	if !reflect.DeepEqual(recorder.commands, want) {
		t.Fatalf("commands = %#v, want %#v", recorder.commands, want)
	}
}

func TestUniqueColumnNamesPreservesEveryValue(t *testing.T) {
	got := uniqueColumnNames([]string{"id", "id", "id_2", "name", "id"})
	want := []string{"id", "id_3", "id_2", "name", "id_4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueColumnNames() = %#v, want %#v", got, want)
	}
}

func openRecordingDB(t *testing.T, name string, conn *recordingConn) *sql.DB {
	t.Helper()
	name = fmt.Sprintf("%s-%d", name, atomic.AddUint64(&recordingDriverID, 1))
	sql.Register(name, &recordingDriver{conn: conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var recordingDriverID uint64

type recordingDriver struct {
	conn *recordingConn
}

func (d *recordingDriver) Open(string) (driver.Conn, error) {
	return d.conn, nil
}

type recordingConn struct {
	commands []string
	queryErr error
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}

func (c *recordingConn) Close() error { return nil }

func (c *recordingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("not implemented")
}

func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.commands = append(c.commands, query)
	return driver.RowsAffected(0), nil
}

func (c *recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.commands = append(c.commands, query)
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &recordingRows{}, nil
}

type recordingRows struct {
	read bool
}

func (*recordingRows) Columns() []string { return []string{"value"} }
func (*recordingRows) Close() error      { return nil }

func (r *recordingRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = int64(1)
	return nil
}
