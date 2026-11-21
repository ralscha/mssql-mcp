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

func TestScanRowsFormatsGUIDDecimalAndBinary(t *testing.T) {
	guidRaw := []byte{0x67, 0x45, 0x23, 0x01, 0xab, 0x89, 0xef, 0xcd, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	typed := &typedRows{
		cols:    []string{"id", "amount", "payload"},
		dbTypes: []string{"UNIQUEIDENTIFIER", "DECIMAL", "VARBINARY"},
		values:  []driver.Value{guidRaw, []byte("123.45"), []byte{0xde, 0xad, 0xbe, 0xef}},
	}
	conn := &recordingConn{customRows: typed}
	db := openRecordingDB(t, "typed-scan", conn)
	client := &Client{DB: db, Config: config.Config{QueryTimeout: time.Second}}

	rows, err := client.Query(context.Background(), "SELECT id, amount, payload FROM dbo.Items")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d", len(rows))
	}
	if rows[0]["id"] != "01234567-89AB-CDEF-0123-456789ABCDEF" {
		t.Fatalf("guid = %#v", rows[0]["id"])
	}
	if rows[0]["amount"] != "123.45" {
		t.Fatalf("amount = %#v", rows[0]["amount"])
	}
	if rows[0]["payload"] != "0xdeadbeef" {
		t.Fatalf("payload = %#v", rows[0]["payload"])
	}
}

func TestWithSessionSettingEvictsConnWhenDisableFails(t *testing.T) {
	var opened atomic.Int32
	name := fmt.Sprintf("evict-on-disable-fail-%d", recordingDriverID.Add(1))
	sql.Register(name, &factoryDriver{newConn: func() driver.Conn {
		id := opened.Add(1)
		return &recordingConn{failDisable: id == 1}
	}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	client := &Client{DB: db, Config: config.Config{MaxRowsDefault: 100, QueryTimeout: time.Second}}

	if _, err := client.QueryReadOnly(context.Background(), "SELECT * FROM dbo.Items", 10); err == nil {
		t.Fatal("expected error when restoring session setting fails")
	}
	// Subsequent query must open a fresh connection rather than reusing the poisoned one.
	if _, err := client.QueryReadOnly(context.Background(), "SELECT * FROM dbo.Items", 10); err != nil {
		t.Fatalf("second QueryReadOnly() failed: %v", err)
	}
	if opened.Load() != 2 {
		t.Fatalf("opened %d connections, want 2", opened.Load())
	}
}

func openRecordingDB(t *testing.T, name string, conn *recordingConn) *sql.DB {
	t.Helper()
	name = fmt.Sprintf("%s-%d", name, recordingDriverID.Add(1))
	sql.Register(name, &recordingDriver{conn: conn})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var recordingDriverID atomic.Uint64

type factoryDriver struct {
	newConn func() driver.Conn
}

func (d *factoryDriver) Open(string) (driver.Conn, error) {
	return d.newConn(), nil
}

type recordingDriver struct {
	conn *recordingConn
}

func (d *recordingDriver) Open(string) (driver.Conn, error) {
	return d.conn, nil
}

type recordingConn struct {
	commands    []string
	queryErr    error
	failDisable bool
	customRows  driver.Rows
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
	if c.failDisable && query == "SET ROWCOUNT 0" {
		return nil, errors.New("disable failed")
	}
	return driver.RowsAffected(0), nil
}

func (c *recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.commands = append(c.commands, query)
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	if c.customRows != nil {
		return c.customRows, nil
	}
	return &recordingRows{}, nil
}

type typedRows struct {
	cols    []string
	dbTypes []string
	values  []driver.Value
	read    bool
}

func (r *typedRows) Columns() []string { return r.cols }
func (r *typedRows) Close() error      { return nil }
func (r *typedRows) ColumnTypeDatabaseTypeName(index int) string {
	return r.dbTypes[index]
}

func (r *typedRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	copy(dest, r.values)
	return nil
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
