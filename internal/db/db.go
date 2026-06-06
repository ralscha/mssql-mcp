package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"mssql-mcp/internal/config"
	"mssql-mcp/internal/sqlsafe"

	_ "github.com/microsoft/go-mssqldb"
)

type Client struct {
	DB     *sql.DB
	Config config.Config
}

func Open(cfg config.Config) (*Client, error) {
	db, err := sql.Open("sqlserver", cfg.ConnectionString())
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetMaxIdleConns(2)
	db.SetMaxOpenConns(10)
	return &Client{DB: db, Config: cfg}, nil
}

func (c *Client) Close() error {
	if c == nil || c.DB == nil {
		return nil
	}
	return c.DB.Close()
}

func (c *Client) TimeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.Config.QueryTimeout)
}

func (c *Client) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	return ScanRows(rows)
}

func (c *Client) QueryReadOnly(ctx context.Context, query string, maxRows int) ([]map[string]any, error) {
	if !sqlsafe.IsReadOnlyQuery(query) {
		return nil, fmt.Errorf("only read-only SELECT queries are allowed")
	}
	if maxRows <= 0 || maxRows > c.Config.MaxRowsDefault {
		maxRows = c.Config.MaxRowsDefault
	}
	if !sqlsafe.NeedsRowLimit(query) {
		return c.Query(ctx, query)
	}
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET ROWCOUNT %d", maxRows)); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET ROWCOUNT 0")
	}()

	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	return ScanRows(rows)
}

func (c *Client) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	res, err := c.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func ScanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = normalize(raw[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	default:
		return x
	}
}
