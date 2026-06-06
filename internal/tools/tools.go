package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"mssql-mcp/internal/config"
	mssqldb "mssql-mcp/internal/db"
	"mssql-mcp/internal/sqlsafe"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Registry struct {
	client *mssqldb.Client
	names  []string
}

func Register(server *mcp.Server, client *mssqldb.Client) []string {
	r := &Registry{client: client}
	r.addReadOnly(server)
	if client.Config.AccessLevel.AllowsDML() {
		r.addDML(server)
	}
	if client.Config.AccessLevel.AllowsDDL() {
		r.addDDL(server)
	}
	return append([]string(nil), r.names...)
}

func ToolNamesForLevel(level config.AccessLevel) []string {
	cfg := config.Config{AccessLevel: level}
	client := &mssqldb.Client{Config: cfg}
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	return Register(server, client)
}

func (r *Registry) tool(name string) *mcp.Tool {
	r.names = append(r.names, name)
	return &mcp.Tool{Name: name}
}

func (r *Registry) addReadOnly(server *mcp.Server) {
	mcp.AddTool(server, r.tool("search_schema"), r.searchSchema)
	mcp.AddTool(server, r.tool("describe_table"), r.describeTable)
	mcp.AddTool(server, r.tool("list_table"), r.listTable)
	mcp.AddTool(server, r.tool("list_databases"), r.listDatabases)
	mcp.AddTool(server, r.tool("list_environments"), r.listEnvironments)
	mcp.AddTool(server, r.tool("profile_table"), r.profileTable)
	mcp.AddTool(server, r.tool("inspect_relationships"), r.inspectRelationships)
	mcp.AddTool(server, r.tool("inspect_dependencies"), r.inspectDependencies)
	mcp.AddTool(server, r.tool("explain_query"), r.explainQuery)
	mcp.AddTool(server, r.tool("read_data"), r.readData)
	mcp.AddTool(server, r.tool("test_connection"), r.testConnection)
	mcp.AddTool(server, r.tool("validate_environment_config"), r.validateEnvironmentConfig)
}

func (r *Registry) addDML(server *mcp.Server) {
	mcp.AddTool(server, r.tool("insert_data"), r.insertData)
	mcp.AddTool(server, r.tool("update_data"), r.updateData)
	mcp.AddTool(server, r.tool("delete_data"), r.deleteData)
}

func (r *Registry) addDDL(server *mcp.Server) {
	mcp.AddTool(server, r.tool("create_table"), r.createTable)
	mcp.AddTool(server, r.tool("create_index"), r.createIndex)
	mcp.AddTool(server, r.tool("drop_table"), r.dropTable)
}

type RowsOutput struct {
	Rows []map[string]any `json:"rows"`
}

type SearchSchemaInput struct {
	Query        string `json:"query"`
	TableOffset  int    `json:"tableOffset"`
	ColumnOffset int    `json:"columnOffset"`
	Limit        int    `json:"limit"`
}

type SearchSchemaOutput struct {
	Tables  []map[string]any `json:"tables"`
	Columns []map[string]any `json:"columns"`
}

func (r *Registry) searchSchema(ctx context.Context, _ *mcp.CallToolRequest, in SearchSchemaInput) (*mcp.CallToolResult, SearchSchemaOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 50, 200)
	pattern := sqlsafe.LikePattern(in.Query)
	tables, err := r.client.Query(ctx, `
SELECT TABLE_SCHEMA AS [schema], TABLE_NAME AS [table], TABLE_TYPE AS [type]
FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_TYPE = 'BASE TABLE' AND (TABLE_SCHEMA LIKE @p1 ESCAPE '\' OR TABLE_NAME LIKE @p1 ESCAPE '\')
ORDER BY TABLE_SCHEMA, TABLE_NAME
OFFSET @p2 ROWS FETCH NEXT @p3 ROWS ONLY`, pattern, max(in.TableOffset, 0), limit)
	if err != nil {
		return nil, SearchSchemaOutput{}, err
	}
	columns, err := r.client.Query(ctx, `
SELECT TABLE_SCHEMA AS [schema], TABLE_NAME AS [table], COLUMN_NAME AS [column], DATA_TYPE AS [dataType]
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA LIKE @p1 ESCAPE '\' OR TABLE_NAME LIKE @p1 ESCAPE '\' OR COLUMN_NAME LIKE @p1 ESCAPE '\'
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION
OFFSET @p2 ROWS FETCH NEXT @p3 ROWS ONLY`, pattern, max(in.ColumnOffset, 0), limit)
	return nil, SearchSchemaOutput{Tables: tables, Columns: columns}, err
}

type TableInput struct {
	Table string `json:"table"`
}

type DescribeTableOutput struct {
	Columns     []map[string]any `json:"columns"`
	PrimaryKeys []map[string]any `json:"primaryKeys"`
	ForeignKeys []map[string]any `json:"foreignKeys"`
	Indexes     []map[string]any `json:"indexes"`
}

func (r *Registry) describeTable(ctx context.Context, _ *mcp.CallToolRequest, in TableInput) (*mcp.CallToolResult, DescribeTableOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	columns, err := r.client.Query(ctx, `
SELECT c.name AS [name], t.name AS [dataType], c.max_length AS [maxLength], c.precision, c.scale,
       c.is_nullable AS [nullable], c.is_identity AS [identity], dc.definition AS [default]
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
LEFT JOIN sys.default_constraints dc ON c.default_object_id = dc.object_id
WHERE s.name = @p1 AND tb.name = @p2
ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	pks, err := r.client.Query(ctx, `
SELECT c.name AS [column], ic.key_ordinal AS [ordinal]
FROM sys.indexes i
JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
JOIN sys.tables t ON i.object_id = t.object_id
JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE i.is_primary_key = 1 AND s.name = @p1 AND t.name = @p2
ORDER BY ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	fks, err := r.foreignKeys(ctx, schema, table, "")
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	indexes, err := r.client.Query(ctx, `
SELECT i.name AS [name], i.type_desc AS [type], i.is_unique AS [unique], c.name AS [column], ic.key_ordinal AS [ordinal]
FROM sys.indexes i
JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
JOIN sys.tables t ON i.object_id = t.object_id
JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE s.name = @p1 AND t.name = @p2 AND i.name IS NOT NULL
ORDER BY i.name, ic.key_ordinal`, schema, table)
	return nil, DescribeTableOutput{Columns: columns, PrimaryKeys: pks, ForeignKeys: fks, Indexes: indexes}, err
}

type ListTableInput struct {
	Schema string `json:"schema"`
	Filter string `json:"filter"`
	Limit  int    `json:"limit"`
}

func (r *Registry) listTable(ctx context.Context, _ *mcp.CallToolRequest, in ListTableInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 200, 1000)
	rows, err := r.client.Query(ctx, `
SELECT TABLE_SCHEMA AS [schema], TABLE_NAME AS [table]
FROM INFORMATION_SCHEMA.TABLES
WHERE TABLE_TYPE = 'BASE TABLE'
  AND (@p1 = '' OR TABLE_SCHEMA = @p1)
  AND (@p2 = '' OR TABLE_NAME LIKE @p2 ESCAPE '\')
ORDER BY TABLE_SCHEMA, TABLE_NAME
OFFSET 0 ROWS FETCH NEXT @p3 ROWS ONLY`, in.Schema, patternOrEmpty(in.Filter), limit)
	return nil, RowsOutput{Rows: rows}, err
}

func (r *Registry) listDatabases(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	rows, err := r.client.Query(ctx, `SELECT name, state_desc AS [state], user_access_desc AS [userAccess] FROM sys.databases ORDER BY name`)
	return nil, RowsOutput{Rows: rows}, err
}

type EnvironmentsOutput struct {
	Environments []map[string]any `json:"environments"`
}

func (r *Registry) listEnvironments(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, EnvironmentsOutput, error) {
	env := r.client.Config.PublicSummary()
	env["name"] = "default"
	return nil, EnvironmentsOutput{Environments: []map[string]any{env}}, nil
}

type ProfileTableInput struct {
	Table          string `json:"table"`
	IncludeSamples bool   `json:"includeSamples"`
	SampleSize     int    `json:"sampleSize"`
}

type ProfileTableOutput struct {
	RowCount []map[string]any `json:"rowCount"`
	Columns  []map[string]any `json:"columns"`
	Samples  []map[string]any `json:"samples,omitempty"`
}

func (r *Registry) profileTable(ctx context.Context, _ *mcp.CallToolRequest, in ProfileTableInput) (*mcp.CallToolResult, ProfileTableOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	qname, err := sqlsafe.QuoteMultipart(schema + "." + table)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	rowCount, err := r.client.Query(ctx, "SELECT COUNT_BIG(*) AS [count] FROM "+qname)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	cols, err := r.client.Query(ctx, `
SELECT c.name AS [name], t.name AS [dataType]
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
WHERE s.name = @p1 AND tb.name = @p2
ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	summaries := make([]map[string]any, 0, len(cols))
	for _, col := range cols {
		name, _ := col["name"].(string)
		qcol, err := sqlsafe.QuoteIdentifier(name)
		if err != nil {
			continue
		}
		stats, err := r.client.Query(ctx, fmt.Sprintf("SELECT COUNT_BIG(*) - COUNT(%s) AS [nullCount], COUNT_BIG(DISTINCT %s) AS [distinctCount], MIN(%s) AS [min], MAX(%s) AS [max] FROM %s", qcol, qcol, qcol, qcol, qname))
		if err == nil && len(stats) > 0 {
			stats[0]["name"] = name
			stats[0]["dataType"] = col["dataType"]
			summaries = append(summaries, stats[0])
		}
	}
	var samples []map[string]any
	if in.IncludeSamples {
		sampleSize := bounded(in.SampleSize, 10, 100)
		samples, err = r.client.Query(ctx, fmt.Sprintf("SELECT TOP (%d) * FROM %s", sampleSize, qname))
		if err != nil {
			return nil, ProfileTableOutput{}, err
		}
	}
	return nil, ProfileTableOutput{RowCount: rowCount, Columns: summaries, Samples: samples}, nil
}

type RelationshipsOutput struct {
	Outbound []map[string]any `json:"outbound"`
	Inbound  []map[string]any `json:"inbound"`
}

func (r *Registry) inspectRelationships(ctx context.Context, _ *mcp.CallToolRequest, in TableInput) (*mcp.CallToolResult, RelationshipsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, RelationshipsOutput{}, err
	}
	out, err := r.foreignKeys(ctx, schema, table, "outbound")
	if err != nil {
		return nil, RelationshipsOutput{}, err
	}
	inb, err := r.foreignKeys(ctx, schema, table, "inbound")
	return nil, RelationshipsOutput{Outbound: out, Inbound: inb}, err
}

func (r *Registry) foreignKeys(ctx context.Context, schema, table, direction string) ([]map[string]any, error) {
	filter := "((ps.name = @p1 AND pt.name = @p2) OR (rs.name = @p1 AND rt.name = @p2))"
	switch direction {
	case "outbound":
		filter = "(ps.name = @p1 AND pt.name = @p2)"
	case "inbound":
		filter = "(rs.name = @p1 AND rt.name = @p2)"
	}
	return r.client.Query(ctx, `
SELECT fk.name AS [foreignKey], ps.name AS [schema], pt.name AS [table], pc.name AS [column],
       rs.name AS [referencedSchema], rt.name AS [referencedTable], rc.name AS [referencedColumn]
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fk.object_id = fkc.constraint_object_id
JOIN sys.tables pt ON fkc.parent_object_id = pt.object_id
JOIN sys.schemas ps ON pt.schema_id = ps.schema_id
JOIN sys.columns pc ON fkc.parent_object_id = pc.object_id AND fkc.parent_column_id = pc.column_id
JOIN sys.tables rt ON fkc.referenced_object_id = rt.object_id
JOIN sys.schemas rs ON rt.schema_id = rs.schema_id
JOIN sys.columns rc ON fkc.referenced_object_id = rc.object_id AND fkc.referenced_column_id = rc.column_id
WHERE `+filter+`
ORDER BY fk.name, fkc.constraint_column_id`, schema, table)
}

type DependenciesOutput struct {
	Dependencies []map[string]any `json:"dependencies"`
}

func (r *Registry) inspectDependencies(ctx context.Context, _ *mcp.CallToolRequest, in TableInput) (*mcp.CallToolResult, DependenciesOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, DependenciesOutput{}, err
	}
	rows, err := r.client.Query(ctx, `
SELECT DISTINCT s.name AS [schema], o.name AS [object], o.type_desc AS [type]
FROM sys.sql_expression_dependencies d
JOIN sys.objects o ON d.referencing_id = o.object_id
JOIN sys.schemas s ON o.schema_id = s.schema_id
WHERE d.referenced_schema_name = @p1 AND d.referenced_entity_name = @p2
ORDER BY s.name, o.name`, schema, table)
	return nil, DependenciesOutput{Dependencies: rows}, err
}

type QueryInput struct {
	Query   string `json:"query"`
	MaxRows int    `json:"maxRows"`
}

func (r *Registry) readData(ctx context.Context, _ *mcp.CallToolRequest, in QueryInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	maxRows := bounded(in.MaxRows, r.client.Config.MaxRowsDefault, r.client.Config.MaxRowsDefault)
	rows, err := r.client.QueryReadOnly(ctx, in.Query, maxRows)
	return nil, RowsOutput{Rows: rows}, err
}

type ExplainOutput struct {
	Plan []map[string]any `json:"plan"`
}

func (r *Registry) explainQuery(ctx context.Context, _ *mcp.CallToolRequest, in QueryInput) (*mcp.CallToolResult, ExplainOutput, error) {
	if !sqlsafe.IsReadOnlyQuery(in.Query) {
		return nil, ExplainOutput{}, fmt.Errorf("only read-only SELECT queries can be explained")
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	conn, err := r.client.DB.Conn(ctx)
	if err != nil {
		return nil, ExplainOutput{}, err
	}
	defer func() {
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
		return nil, ExplainOutput{}, err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET SHOWPLAN_XML OFF")
	}()
	rows, err := conn.QueryContext(ctx, in.Query)
	if err != nil {
		return nil, ExplainOutput{}, err
	}
	defer func() {
		_ = rows.Close()
	}()
	plan, err := mssqldb.ScanRows(rows)
	return nil, ExplainOutput{Plan: plan}, err
}

type ConnectionOutput struct {
	OK        bool             `json:"ok"`
	LatencyMS int64            `json:"latencyMs"`
	Config    map[string]any   `json:"config"`
	Rows      []map[string]any `json:"rows,omitempty"`
}

func (r *Registry) testConnection(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, ConnectionOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, r.client.Config.ConnectionTimeout)
	defer cancel()
	start := time.Now()
	if err := r.client.DB.PingContext(ctx); err != nil {
		return nil, ConnectionOutput{}, err
	}
	rows, err := r.client.Query(ctx, `SELECT DB_NAME() AS [database], @@VERSION AS [version]`)
	if err != nil {
		return nil, ConnectionOutput{}, err
	}
	return nil, ConnectionOutput{OK: true, LatencyMS: time.Since(start).Milliseconds(), Config: r.client.Config.PublicSummary(), Rows: rows}, nil
}

type ValidationOutput struct {
	Valid  bool           `json:"valid"`
	Config map[string]any `json:"config"`
}

func (r *Registry) validateEnvironmentConfig(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, ValidationOutput, error) {
	err := r.client.Config.Validate()
	return nil, ValidationOutput{Valid: err == nil, Config: r.client.Config.PublicSummary()}, err
}

type InsertInput struct {
	Table string           `json:"table"`
	Rows  []map[string]any `json:"rows"`
}

type MutationOutput struct {
	Executed     bool             `json:"executed"`
	RowsAffected int64            `json:"rowsAffected"`
	Preview      []map[string]any `json:"preview,omitempty"`
}

func (r *Registry) insertData(ctx context.Context, _ *mcp.CallToolRequest, in InsertInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDML() {
		return nil, MutationOutput{}, fmt.Errorf("insert_data requires DML-RW or DDL-RW")
	}
	if len(in.Rows) == 0 {
		return nil, MutationOutput{}, fmt.Errorf("rows is required")
	}
	table, err := sqlsafe.QuoteMultipart(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	var total int64
	for _, row := range in.Rows {
		cols := sortedKeys(row)
		if len(cols) == 0 {
			return nil, MutationOutput{}, fmt.Errorf("row cannot be empty")
		}
		qcols := make([]string, len(cols))
		params := make([]string, len(cols))
		args := make([]any, len(cols))
		for i, col := range cols {
			qcol, err := sqlsafe.QuoteIdentifier(col)
			if err != nil {
				return nil, MutationOutput{}, err
			}
			qcols[i] = qcol
			params[i] = fmt.Sprintf("@p%d", i+1)
			args[i] = row[col]
		}
		n, err := r.client.Exec(ctx, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(qcols, ", "), strings.Join(params, ", ")), args...)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		total += n
	}
	return nil, MutationOutput{Executed: true, RowsAffected: total}, nil
}

type WhereMutationInput struct {
	Table   string         `json:"table"`
	Values  map[string]any `json:"values"`
	Where   string         `json:"where"`
	Params  map[string]any `json:"params"`
	Confirm bool           `json:"confirm"`
}

func (r *Registry) updateData(ctx context.Context, _ *mcp.CallToolRequest, in WhereMutationInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDML() {
		return nil, MutationOutput{}, fmt.Errorf("update_data requires DML-RW or DDL-RW")
	}
	if len(in.Values) == 0 {
		return nil, MutationOutput{}, fmt.Errorf("values is required")
	}
	table, where, args, err := mutationTarget(in.Table, in.Where, in.Params)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	if r.client.Config.RequireConfirmation && !in.Confirm {
		preview, err := r.client.Query(ctx, fmt.Sprintf("SELECT TOP (%d) * FROM %s WHERE %s", r.client.Config.MaxRowsDefault, table, where), args...)
		return nil, MutationOutput{Executed: false, Preview: preview}, err
	}
	cols := sortedKeys(in.Values)
	set := make([]string, len(cols))
	allArgs := make([]any, 0, len(cols)+len(args))
	for i, col := range cols {
		qcol, err := sqlsafe.QuoteIdentifier(col)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		set[i] = fmt.Sprintf("%s = @p%d", qcol, i+1)
		allArgs = append(allArgs, in.Values[col])
	}
	allArgs = append(allArgs, args...)
	n, err := r.client.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE %s", table, strings.Join(set, ", "), renumberWhere(where, len(cols))), allArgs...)
	return nil, MutationOutput{Executed: true, RowsAffected: n}, err
}

type DeleteInput struct {
	Table   string         `json:"table"`
	Where   string         `json:"where"`
	Params  map[string]any `json:"params"`
	Confirm bool           `json:"confirm"`
}

func (r *Registry) deleteData(ctx context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDML() {
		return nil, MutationOutput{}, fmt.Errorf("delete_data requires DML-RW or DDL-RW")
	}
	table, where, args, err := mutationTarget(in.Table, in.Where, in.Params)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	if r.client.Config.RequireConfirmation && !in.Confirm {
		preview, err := r.client.Query(ctx, fmt.Sprintf("SELECT TOP (%d) * FROM %s WHERE %s", r.client.Config.MaxRowsDefault, table, where), args...)
		return nil, MutationOutput{Executed: false, Preview: preview}, err
	}
	n, err := r.client.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s", table, where), args...)
	return nil, MutationOutput{Executed: true, RowsAffected: n}, err
}

type ColumnDef struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable"`
	PrimaryKey bool   `json:"primaryKey"`
	Identity   bool   `json:"identity"`
}

type CreateTableInput struct {
	Table   string      `json:"table"`
	Columns []ColumnDef `json:"columns"`
}

func (r *Registry) createTable(ctx context.Context, _ *mcp.CallToolRequest, in CreateTableInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDDL() {
		return nil, MutationOutput{}, fmt.Errorf("create_table requires DDL-RW")
	}
	if len(in.Columns) == 0 {
		return nil, MutationOutput{}, fmt.Errorf("columns is required")
	}
	table, err := sqlsafe.QuoteMultipart(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	defs := make([]string, 0, len(in.Columns)+1)
	var pks []string
	for _, col := range in.Columns {
		qcol, err := sqlsafe.QuoteIdentifier(col.Name)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		if !validSQLType(col.Type) {
			return nil, MutationOutput{}, fmt.Errorf("invalid column type %q", col.Type)
		}
		def := qcol + " " + col.Type
		if col.Identity {
			def += " IDENTITY(1,1)"
		}
		if col.Nullable {
			def += " NULL"
		} else {
			def += " NOT NULL"
		}
		defs = append(defs, def)
		if col.PrimaryKey {
			pks = append(pks, qcol)
		}
	}
	if len(pks) > 0 {
		defs = append(defs, "PRIMARY KEY ("+strings.Join(pks, ", ")+")")
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	n, err := r.client.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", table, strings.Join(defs, ", ")))
	return nil, MutationOutput{Executed: true, RowsAffected: n}, err
}

type CreateIndexInput struct {
	Table   string   `json:"table"`
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

func (r *Registry) createIndex(ctx context.Context, _ *mcp.CallToolRequest, in CreateIndexInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDDL() {
		return nil, MutationOutput{}, fmt.Errorf("create_index requires DDL-RW")
	}
	table, err := sqlsafe.QuoteMultipart(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	name, err := sqlsafe.QuoteIdentifier(in.Name)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	if len(in.Columns) == 0 {
		return nil, MutationOutput{}, fmt.Errorf("columns is required")
	}
	cols := make([]string, len(in.Columns))
	for i, col := range in.Columns {
		cols[i], err = sqlsafe.QuoteIdentifier(col)
		if err != nil {
			return nil, MutationOutput{}, err
		}
	}
	prefix := "CREATE INDEX"
	if in.Unique {
		prefix = "CREATE UNIQUE INDEX"
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	n, err := r.client.Exec(ctx, fmt.Sprintf("%s %s ON %s (%s)", prefix, name, table, strings.Join(cols, ", ")))
	return nil, MutationOutput{Executed: true, RowsAffected: n}, err
}

type DropTableInput struct {
	Table   string `json:"table"`
	Confirm bool   `json:"confirm"`
}

func (r *Registry) dropTable(ctx context.Context, _ *mcp.CallToolRequest, in DropTableInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDDL() {
		return nil, MutationOutput{}, fmt.Errorf("drop_table requires DDL-RW")
	}
	table, err := sqlsafe.QuoteMultipart(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	if r.client.Config.RequireConfirmation && !in.Confirm {
		return nil, MutationOutput{Executed: false}, nil
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	n, err := r.client.Exec(ctx, "DROP TABLE "+table)
	return nil, MutationOutput{Executed: true, RowsAffected: n}, err
}

func bounded(value, fallback, maxValue int) int {
	if value <= 0 {
		return fallback
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func patternOrEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return sqlsafe.LikePattern(s)
}

func splitTable(name string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(name), ".")
	if len(parts) == 1 {
		if _, err := sqlsafe.QuoteIdentifier(parts[0]); err != nil {
			return "", "", err
		}
		return "dbo", parts[0], nil
	}
	if len(parts) == 2 {
		if _, err := sqlsafe.QuoteIdentifier(parts[0]); err != nil {
			return "", "", err
		}
		if _, err := sqlsafe.QuoteIdentifier(parts[1]); err != nil {
			return "", "", err
		}
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("table must be table or schema.table")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var sqlTypeRE = regexp.MustCompile(`(?i)^[A-Z][A-Z0-9 ]*(\([0-9]+(,[0-9]+)?|MAX\))?$`)

func validSQLType(s string) bool {
	return sqlTypeRE.MatchString(strings.TrimSpace(s))
}

func mutationTarget(tableName, where string, params map[string]any) (string, string, []any, error) {
	table, err := sqlsafe.QuoteMultipart(tableName)
	if err != nil {
		return "", "", nil, err
	}
	where = strings.TrimSpace(where)
	if where == "" {
		return "", "", nil, fmt.Errorf("where is required")
	}
	if strings.Contains(where, ";") || !sqlsafe.IsReadOnlyQuery("SELECT * FROM x WHERE "+where) {
		return "", "", nil, fmt.Errorf("where clause contains disallowed SQL")
	}
	keys := sortedKeys(params)
	args := make([]any, 0, len(keys))
	for i, key := range keys {
		if _, err := sqlsafe.QuoteIdentifier(key); err != nil {
			return "", "", nil, err
		}
		where = strings.ReplaceAll(where, "@"+key, fmt.Sprintf("@p%d", i+1))
		args = append(args, params[key])
	}
	return table, where, args, nil
}

func renumberWhere(where string, offset int) string {
	for i := 100; i >= 1; i-- {
		where = strings.ReplaceAll(where, fmt.Sprintf("@p%d", i), fmt.Sprintf("@p%d", i+offset))
	}
	return where
}
