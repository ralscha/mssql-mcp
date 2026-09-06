package db

import (
	"context"
	"database/sql"
	"errors"
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

func (c *Client) QueryReadOnly(ctx context.Context, query string, maxRows int, args ...any) ([]map[string]any, error) {
	if err := sqlsafe.ValidateReadOnlyQuery(query); err != nil {
		return nil, fmt.Errorf("invalid read-only query: %w", err)
	}
	if c.Config.MaxRowsDefault <= 0 {
		return nil, fmt.Errorf("maximum row limit must be positive")
	}
	if maxRows <= 0 || maxRows > c.Config.MaxRowsDefault {
		maxRows = c.Config.MaxRowsDefault
	}

	var result []map[string]any
	err := c.WithSessionSetting(ctx, fmt.Sprintf("SET ROWCOUNT %d", maxRows), "SET ROWCOUNT 0", func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		result, err = ScanRows(rows)
		return err
	})
	return result, err
}

// WithSessionSetting applies a connection-scoped setting for one operation and
// reliably restores it before the connection is returned to the pool.
func (c *Client) WithSessionSetting(ctx context.Context, enable, disable string, fn func(*sql.Conn) error) (err error) {
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close database connection: %w", closeErr))
		}
	}()
	if _, err := conn.ExecContext(ctx, enable); err != nil {
		return fmt.Errorf("enable database session setting: %w", err)
	}
	defer func() {
		resetTimeout := min(c.Config.QueryTimeout, 5*time.Second)
		if resetTimeout <= 0 {
			resetTimeout = 5 * time.Second
		}
		resetCtx, cancel := context.WithTimeout(context.Background(), resetTimeout)
		defer cancel()
		if _, resetErr := conn.ExecContext(resetCtx, disable); resetErr != nil {
			err = errors.Join(err, fmt.Errorf("restore database session setting: %w", resetErr))
		}
	}()
	return fn(conn)
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
	cols = uniqueColumnNames(cols)
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

// uniqueColumnNames prevents duplicate SELECT labels from silently overwriting
// values when a row is represented as a map.
func uniqueColumnNames(columns []string) []string {
	reserved := make(map[string]bool, len(columns))
	for _, column := range columns {
		reserved[column] = true
	}
	used := make(map[string]bool, len(columns))
	nextSuffix := make(map[string]int, len(columns))
	result := make([]string, len(columns))
	for i, column := range columns {
		if !used[column] {
			result[i] = column
			used[column] = true
			continue
		}
		suffix := max(nextSuffix[column], 2)
		for {
			candidate := fmt.Sprintf("%s_%d", column, suffix)
			suffix++
			if !used[candidate] && !reserved[candidate] {
				result[i] = candidate
				used[candidate] = true
				nextSuffix[column] = suffix
				break
			}
		}
	}
	return result
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
