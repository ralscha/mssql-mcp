package tools

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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

type toolMeta struct {
	description string
	annotations *mcp.ToolAnnotations
}

func readOnlyAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  new(false),
	}
}

func additiveAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		IdempotentHint:  false,
		DestructiveHint: new(false),
		OpenWorldHint:   new(false),
	}
}

func destructiveAnnotations(idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		IdempotentHint:  idempotent,
		DestructiveHint: new(true),
		OpenWorldHint:   new(false),
	}
}

func (r *Registry) tool(name string) *mcp.Tool {
	r.names = append(r.names, name)
	meta := toolCatalog[name]
	return &mcp.Tool{
		Name:        name,
		Description: meta.description,
		Annotations: meta.annotations,
	}
}

var toolCatalog = map[string]toolMeta{
	"search_schema":         {"Find tables and columns by name.", readOnlyAnnotations()},
	"describe_table":        {"Show columns, keys, and indexes.", readOnlyAnnotations()},
	"list_table":            {"List database tables.", readOnlyAnnotations()},
	"list_databases":        {"List SQL Server databases.", readOnlyAnnotations()},
	"list_environments":     {"Show active connection settings.", readOnlyAnnotations()},
	"profile_table":         {"Summarize table rows and columns.", readOnlyAnnotations()},
	"inspect_relationships": {"Show foreign-key relationships.", readOnlyAnnotations()},
	"inspect_dependencies":  {"Find objects depending on a table.", readOnlyAnnotations()},
	"explain_query":         {"Return the execution plan for SELECT.", readOnlyAnnotations()},
	"read_data":             {"Run a read-only SELECT query.", readOnlyAnnotations()},
	"test_connection":       {"Verify SQL Server connectivity.", readOnlyAnnotations()},
	"list_schemas":          {"List database schemas.", readOnlyAnnotations()},
	"list_views":            {"List database views.", readOnlyAnnotations()},
	"list_triggers":         {"List database triggers.", readOnlyAnnotations()},
	"show_create_table":     {"Generate CREATE TABLE DDL.", readOnlyAnnotations()},
	"table_size":            {"Show table size estimates.", readOnlyAnnotations()},
	"insert_data":           {"Insert rows into a table.", additiveAnnotations()},
	"update_data":           {"Update rows matching a filter.", destructiveAnnotations(false)},
	"delete_data":           {"Delete rows matching a filter.", destructiveAnnotations(true)},
	"create_table":          {"Create a database table.", additiveAnnotations()},
	"create_index":          {"Create an index on a table.", additiveAnnotations()},
	"drop_table":            {"Drop a database table.", destructiveAnnotations(true)},
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
	mcp.AddTool(server, r.tool("list_schemas"), r.listSchemas)
	mcp.AddTool(server, r.tool("list_views"), r.listViews)
	mcp.AddTool(server, r.tool("list_triggers"), r.listTriggers)
	mcp.AddTool(server, r.tool("show_create_table"), r.showCreateTable)
	mcp.AddTool(server, r.tool("table_size"), r.tableSize)
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

type EmptyInput struct{}

type RowsOutput struct {
	Rows []map[string]any `json:"rows"`
}

type SearchSchemaInput struct {
	Query        string `json:"query,omitempty" jsonschema:"Name pattern; * is supported as a wildcard."`
	TableOffset  int    `json:"tableOffset,omitempty" jsonschema:"Zero-based table result offset."`
	ColumnOffset int    `json:"columnOffset,omitempty" jsonschema:"Zero-based column result offset."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum results per category; defaults to 50 and is capped at 200."`
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
SELECT s.name AS [schema], tb.name AS [table], 'BASE TABLE' AS [type]
FROM sys.tables tb
JOIN sys.schemas s ON tb.schema_id = s.schema_id
WHERE s.name LIKE @p1 ESCAPE '\' OR tb.name LIKE @p1 ESCAPE '\'
ORDER BY s.name, tb.name
OFFSET @p2 ROWS FETCH NEXT @p3 ROWS ONLY`, pattern, max(in.TableOffset, 0), limit)
	if err != nil {
		return nil, SearchSchemaOutput{}, err
	}
	columns, err := r.client.Query(ctx, `
SELECT s.name AS [schema], tb.name AS [table], c.name AS [column], t.name AS [dataType]
FROM sys.columns c
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
JOIN sys.types t ON c.user_type_id = t.user_type_id
WHERE s.name LIKE @p1 ESCAPE '\' OR tb.name LIKE @p1 ESCAPE '\' OR c.name LIKE @p1 ESCAPE '\'
ORDER BY s.name, tb.name, c.column_id
OFFSET @p2 ROWS FETCH NEXT @p3 ROWS ONLY`, pattern, max(in.ColumnOffset, 0), limit)
	return nil, SearchSchemaOutput{Tables: tables, Columns: columns}, err
}

type TableInput struct {
	Table string `json:"table" jsonschema:"Table name as table or schema.table."`
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
SELECT c.name AS [name],
       t.name AS [dataType],
       CASE
         WHEN c.max_length = -1 THEN -1
         WHEN st.name IN ('nvarchar', 'nchar') THEN c.max_length / 2
         ELSE c.max_length
       END AS [maxLength],
       c.precision,
       c.scale,
       c.is_nullable AS [nullable],
       c.is_identity AS [identity],
       dc.definition AS [default]
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
LEFT JOIN sys.types st ON c.system_type_id = st.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
LEFT JOIN sys.default_constraints dc ON c.default_object_id = dc.object_id
WHERE s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo') AND tb.name = @p2
ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	if len(columns) == 0 {
		return nil, DescribeTableOutput{}, fmt.Errorf("table %q was not found", in.Table)
	}
	pks, err := r.client.Query(ctx, `
SELECT c.name AS [column], ic.key_ordinal AS [ordinal]
FROM sys.indexes i
JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
JOIN sys.tables t ON i.object_id = t.object_id
JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE i.is_primary_key = 1
  AND ic.key_ordinal > 0
  AND ic.is_included_column = 0
  AND s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo')
  AND t.name = @p2
ORDER BY ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	fks, err := r.foreignKeys(ctx, schema, table, "")
	if err != nil {
		return nil, DescribeTableOutput{}, err
	}
	indexes, err := r.client.Query(ctx, `
SELECT i.name AS [name],
       i.type_desc AS [type],
       i.is_unique AS [unique],
       c.name AS [column],
       ic.key_ordinal AS [ordinal],
       ic.is_included_column AS [included],
       ic.is_descending_key AS [descending]
FROM sys.indexes i
JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
JOIN sys.tables t ON i.object_id = t.object_id
JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo')
  AND t.name = @p2
  AND i.name IS NOT NULL
ORDER BY i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, schema, table)
	return nil, DescribeTableOutput{Columns: columns, PrimaryKeys: pks, ForeignKeys: fks, Indexes: indexes}, err
}

type ListTableInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"Exact schema name filter."`
	Filter string `json:"filter,omitempty" jsonschema:"Name pattern; * is supported as a wildcard."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 200 and is capped at 1000."`
}

func (r *Registry) listTable(ctx context.Context, _ *mcp.CallToolRequest, in ListTableInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 200, 1000)
	rows, err := r.client.Query(ctx, `
SELECT s.name AS [schema], tb.name AS [table]
FROM sys.tables tb
JOIN sys.schemas s ON tb.schema_id = s.schema_id
WHERE (@p1 = '' OR s.name = @p1)
  AND (@p2 = '' OR tb.name LIKE @p2 ESCAPE '\')
ORDER BY s.name, tb.name
OFFSET 0 ROWS FETCH NEXT @p3 ROWS ONLY`, in.Schema, patternOrEmpty(in.Filter), limit)
	return nil, RowsOutput{Rows: rows}, err
}

func (r *Registry) listDatabases(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	rows, err := r.client.Query(ctx, `SELECT name, state_desc AS [state], user_access_desc AS [userAccess] FROM sys.databases ORDER BY name`)
	return nil, RowsOutput{Rows: rows}, err
}

type EnvironmentsOutput struct {
	Environments []map[string]any `json:"environments"`
}

func (r *Registry) listEnvironments(context.Context, *mcp.CallToolRequest, EmptyInput) (*mcp.CallToolResult, EnvironmentsOutput, error) {
	env := r.client.Config.PublicSummary()
	env["name"] = "default"
	return nil, EnvironmentsOutput{Environments: []map[string]any{env}}, nil
}

type ProfileTableInput struct {
	Table          string `json:"table" jsonschema:"Table name as table or schema.table."`
	IncludeSamples bool   `json:"includeSamples,omitempty"`
	SampleSize     int    `json:"sampleSize,omitempty" jsonschema:"Sample rows to return; defaults to 10 and is capped at 100."`
}

type ProfileTableOutput struct {
	RowCount []map[string]any `json:"rowCount"`
	Columns  []map[string]any `json:"columns"`
	Samples  []map[string]any `json:"samples,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
}

func (r *Registry) profileTable(ctx context.Context, _ *mcp.CallToolRequest, in ProfileTableInput) (*mcp.CallToolResult, ProfileTableOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	cols, err := r.client.Query(ctx, `
SELECT s.name AS [schema], tb.name AS [table], c.name AS [name], t.name AS [dataType], COALESCE(st.name, t.name) AS [systemType]
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
LEFT JOIN sys.types st ON c.system_type_id = st.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
WHERE s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo') AND tb.name = @p2
ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}
	if len(cols) == 0 {
		return nil, ProfileTableOutput{}, fmt.Errorf("table %q was not found", in.Table)
	}
	resolvedSchema, _ := cols[0]["schema"].(string)
	resolvedTable, _ := cols[0]["table"].(string)
	qname, err := quoteResolvedTable(resolvedSchema, resolvedTable)
	if err != nil {
		return nil, ProfileTableOutput{}, err
	}

	const profileColumnsPerScan = 25
	summaries := make([]map[string]any, 0, len(cols))
	warnings := make([]string, 0)
	var rowCount []map[string]any

	type profileColSpec struct {
		index         int
		name          string
		dataType      any
		qcol          string
		canDistinct   bool
		canMinMax     bool
		castBitMinMax bool
	}

	specs := make([]profileColSpec, 0, len(cols))
	for i, col := range cols {
		name, _ := col["name"].(string)
		sysType, _ := col["systemType"].(string)
		qcol, err := sqlsafe.QuoteRawIdentifier(name)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipping column %q: %v", name, err))
			continue
		}
		canDistinct, canMinMax, castBit := profileCapabilities(strings.ToLower(sysType))
		if !canDistinct || !canMinMax {
			warnings = append(warnings, fmt.Sprintf("column %q (%v) does not support full distinct/min/max profiling; reporting null count", name, col["dataType"]))
		}
		specs = append(specs, profileColSpec{
			index:         i,
			name:          name,
			dataType:      col["dataType"],
			qcol:          qcol,
			canDistinct:   canDistinct,
			canMinMax:     canMinMax,
			castBitMinMax: castBit,
		})
	}

	if len(specs) == 0 {
		rowCount, err = r.client.Query(ctx, "SELECT COUNT_BIG(*) AS [count] FROM "+qname)
		if err != nil {
			return nil, ProfileTableOutput{}, err
		}
	}

	for start := 0; start < len(specs); start += profileColumnsPerScan {
		chunk := specs[start:min(start+profileColumnsPerScan, len(specs))]
		selectExprs := make([]string, 0, 1+len(chunk)*4)
		if start == 0 {
			selectExprs = append(selectExprs, "COUNT_BIG(*) AS [__row_count]")
		}
		for i, spec := range chunk {
			selectExprs = append(selectExprs, fmt.Sprintf("COUNT_BIG(CASE WHEN %s IS NULL THEN 1 END) AS [__null_%d]", spec.qcol, i))
			if spec.canDistinct {
				selectExprs = append(selectExprs, fmt.Sprintf("COUNT_BIG(DISTINCT %s) AS [__dist_%d]", spec.qcol, i))
			}
			if spec.canMinMax {
				minMaxExpr := spec.qcol
				if spec.castBitMinMax {
					minMaxExpr = "CAST(" + spec.qcol + " AS tinyint)"
				}
				selectExprs = append(selectExprs,
					fmt.Sprintf("MIN(%s) AS [__min_%d]", minMaxExpr, i),
					fmt.Sprintf("MAX(%s) AS [__max_%d]", minMaxExpr, i),
				)
			}
		}
		statsRows, err := r.client.Query(ctx, "SELECT "+strings.Join(selectExprs, ", ")+" FROM "+qname)
		if err != nil {
			return nil, ProfileTableOutput{}, err
		}
		if len(statsRows) == 0 {
			continue
		}
		stats := statsRows[0]
		if start == 0 {
			rowCount = []map[string]any{{"count": stats["__row_count"]}}
		}
		for i, spec := range chunk {
			summary := map[string]any{
				"name":      spec.name,
				"dataType":  spec.dataType,
				"nullCount": stats[fmt.Sprintf("__null_%d", i)],
			}
			if spec.canDistinct {
				summary["distinctCount"] = stats[fmt.Sprintf("__dist_%d", i)]
			} else {
				summary["distinctCount"] = nil
			}
			if spec.canMinMax {
				summary["min"] = stats[fmt.Sprintf("__min_%d", i)]
				summary["max"] = stats[fmt.Sprintf("__max_%d", i)]
			} else {
				summary["min"] = nil
				summary["max"] = nil
			}
			summaries = append(summaries, summary)
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
	return nil, ProfileTableOutput{RowCount: rowCount, Columns: summaries, Samples: samples, Warnings: warnings}, nil
}

func profileCapabilities(sysType string) (canDistinct, canMinMax, castBit bool) {
	switch sysType {
	case "text", "ntext", "image", "xml", "geography", "geometry", "sql_variant":
		return false, false, false
	case "bit":
		return true, true, true
	case "uniqueidentifier", "binary", "varbinary", "timestamp", "rowversion", "hierarchyid":
		return true, false, false
	default:
		return true, true, false
	}
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
	const schemaExpr = "COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo')"
	filter := "((ps.name = " + schemaExpr + " AND pt.name = @p2) OR (rs.name = " + schemaExpr + " AND rt.name = @p2))"
	switch direction {
	case "outbound":
		filter = "(ps.name = " + schemaExpr + " AND pt.name = @p2)"
	case "inbound":
		filter = "(rs.name = " + schemaExpr + " AND rt.name = @p2)"
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
	// Unqualified references resolve against the referencing object's schema
	// first and then dbo, so they are compared with the target's object_id.
	rows, err := r.client.Query(ctx, `
WITH target AS (
    SELECT COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo') AS schema_name,
           OBJECT_ID(QUOTENAME(COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo')) + N'.' + QUOTENAME(@p2)) AS object_id
)
SELECT DISTINCT s.name AS [schema], o.name AS [object], o.type_desc AS [type]
FROM sys.sql_expression_dependencies d
CROSS JOIN target tg
JOIN sys.objects o ON d.referencing_id = o.object_id
JOIN sys.schemas s ON o.schema_id = s.schema_id
WHERE d.referenced_id = tg.object_id
   OR (d.referenced_server_name IS NULL
       AND d.referenced_database_name IS NULL
       AND d.referenced_entity_name = @p2
       AND (d.referenced_schema_name = tg.schema_name
            OR (d.referenced_schema_name IS NULL
                AND COALESCE(OBJECT_ID(QUOTENAME(s.name) + N'.' + QUOTENAME(@p2)), OBJECT_ID(N'[dbo].' + QUOTENAME(@p2))) = tg.object_id)))
ORDER BY s.name, o.name`, schema, table)
	return nil, DependenciesOutput{Dependencies: rows}, err
}

type QueryInput struct {
	Query   string         `json:"query" jsonschema:"One T-SQL SELECT statement; named placeholders use @name syntax."`
	Params  map[string]any `json:"params,omitempty" jsonschema:"Values for every named placeholder, keyed without @. Pass integers beyond +/-2^53 as strings to keep full precision."`
	MaxRows int            `json:"maxRows,omitempty" jsonschema:"Maximum rows to return, capped by MSSQL_MAX_ROWS_DEFAULT."`
}

func (r *Registry) readData(ctx context.Context, _ *mcp.CallToolRequest, in QueryInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	args, err := sqlsafe.BindNamedParameters(in.Query, in.Params)
	if err != nil {
		return nil, RowsOutput{}, err
	}
	maxRows := bounded(in.MaxRows, r.client.Config.MaxRowsDefault, r.client.Config.MaxRowsDefault)
	rows, err := r.client.QueryReadOnly(ctx, in.Query, maxRows, args...)
	return nil, RowsOutput{Rows: rows}, err
}

type ExplainQueryInput struct {
	Query  string         `json:"query" jsonschema:"One T-SQL SELECT statement; named placeholders use @name syntax."`
	Params map[string]any `json:"params,omitempty" jsonschema:"Values for every named placeholder, keyed without @. Pass integers beyond +/-2^53 as strings to keep full precision."`
}

type ExplainOutput struct {
	Plan []map[string]any `json:"plan"`
}

func (r *Registry) explainQuery(ctx context.Context, _ *mcp.CallToolRequest, in ExplainQueryInput) (*mcp.CallToolResult, ExplainOutput, error) {
	if err := sqlsafe.ValidateReadOnlyQuery(in.Query); err != nil {
		return nil, ExplainOutput{}, fmt.Errorf("invalid read-only query: %w", err)
	}
	args, err := sqlsafe.BindNamedParameters(in.Query, in.Params)
	if err != nil {
		return nil, ExplainOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	var plan []map[string]any
	err = r.client.WithSessionSetting(ctx, "SET SHOWPLAN_XML ON", "SET SHOWPLAN_XML OFF", func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, in.Query, args...)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()
		plan, err = mssqldb.ScanRows(rows)
		return err
	})
	return nil, ExplainOutput{Plan: plan}, err
}

type ConnectionOutput struct {
	OK        bool             `json:"ok"`
	LatencyMS int64            `json:"latencyMs"`
	Config    map[string]any   `json:"config"`
	Rows      []map[string]any `json:"rows,omitempty"`
}

func (r *Registry) testConnection(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, ConnectionOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, r.client.Config.ConnectionTimeout)
	defer cancel()
	start := time.Now()
	rows, err := r.client.Query(ctx, `SELECT DB_NAME() AS [database], @@VERSION AS [version]`)
	if err != nil {
		return nil, ConnectionOutput{}, err
	}
	return nil, ConnectionOutput{OK: true, LatencyMS: time.Since(start).Milliseconds(), Config: r.client.Config.PublicSummary(), Rows: rows}, nil
}

func (r *Registry) listSchemas(ctx context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	rows, err := r.client.Query(ctx, `
SELECT s.name AS [schema],
       dp.name AS [owner],
       s.schema_id AS [schemaId]
FROM sys.schemas s
LEFT JOIN sys.database_principals dp ON s.principal_id = dp.principal_id
WHERE s.name NOT IN ('INFORMATION_SCHEMA', 'sys')
ORDER BY s.name`)
	return nil, RowsOutput{Rows: rows}, err
}

type ListDefinitionsInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"Exact schema name filter."`
	Filter string `json:"filter,omitempty" jsonschema:"Object-name pattern; * is supported as a wildcard."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 100 and is capped at 500."`
}

func (r *Registry) listViews(ctx context.Context, _ *mcp.CallToolRequest, in ListDefinitionsInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 100, 500)
	rows, err := r.client.Query(ctx, `
SELECT s.name AS [schema],
       v.name AS [view],
       OBJECT_DEFINITION(v.object_id) AS [definition]
FROM sys.views v
JOIN sys.schemas s ON v.schema_id = s.schema_id
WHERE (@p1 = '' OR s.name = @p1)
  AND (@p2 = '' OR v.name LIKE @p2 ESCAPE '\')
ORDER BY s.name, v.name
OFFSET 0 ROWS FETCH NEXT @p3 ROWS ONLY`, in.Schema, patternOrEmpty(in.Filter), limit)
	return nil, RowsOutput{Rows: rows}, err
}

func (r *Registry) listTriggers(ctx context.Context, _ *mcp.CallToolRequest, in ListDefinitionsInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 100, 500)
	rows, err := r.client.Query(ctx, `
SELECT ts.name AS [schema],
       tr.name AS [name],
       ps.name AS [tableSchema],
       parent.name AS [table],
       CASE WHEN tr.is_instead_of_trigger = 1 THEN 'INSTEAD OF' ELSE 'AFTER' END AS [timing],
       events.events AS [event],
       OBJECT_DEFINITION(tr.object_id) AS [definition]
FROM sys.triggers tr
JOIN sys.objects parent ON tr.parent_id = parent.object_id
JOIN sys.schemas ps ON parent.schema_id = ps.schema_id
JOIN sys.schemas ts ON tr.schema_id = ts.schema_id
OUTER APPLY (
    SELECT STUFF((
        SELECT ', ' + te.type_desc
        FROM sys.trigger_events te
        WHERE te.object_id = tr.object_id
        ORDER BY te.type_desc
        FOR XML PATH(''), TYPE
    ).value('.', 'nvarchar(max)'), 1, 2, '') AS events
) events
WHERE tr.parent_class_desc = 'OBJECT_OR_COLUMN'
  AND (@p1 = '' OR ps.name = @p1 OR ts.name = @p1)
  AND (@p2 = '' OR tr.name LIKE @p2 ESCAPE '\' OR parent.name LIKE @p2 ESCAPE '\')
ORDER BY ps.name, parent.name, tr.name
OFFSET 0 ROWS FETCH NEXT @p3 ROWS ONLY`, in.Schema, patternOrEmpty(in.Filter), limit)
	return nil, RowsOutput{Rows: rows}, err
}

type DDLOutput struct {
	DDL string `json:"ddl"`
}

func (r *Registry) showCreateTable(ctx context.Context, _ *mcp.CallToolRequest, in TableInput) (*mcp.CallToolResult, DDLOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	schema, table, err := splitTable(in.Table)
	if err != nil {
		return nil, DDLOutput{}, err
	}
	cols, err := r.client.Query(ctx, `
SELECT s.name AS [schema],
       tb.name AS [table],
       c.name AS [name],
       CASE
         WHEN t.is_user_defined = 1 THEN QUOTENAME(ts.name) + '.' + QUOTENAME(t.name)
         WHEN st.name IN ('varchar', 'char', 'varbinary', 'binary') THEN st.name + '(' + CASE WHEN c.max_length = -1 THEN 'MAX' ELSE CONVERT(varchar(10), c.max_length) END + ')'
         WHEN st.name IN ('nvarchar', 'nchar') THEN st.name + '(' + CASE WHEN c.max_length = -1 THEN 'MAX' ELSE CONVERT(varchar(10), c.max_length / 2) END + ')'
         WHEN st.name IN ('decimal', 'numeric') THEN st.name + '(' + CONVERT(varchar(10), c.precision) + ',' + CONVERT(varchar(10), c.scale) + ')'
         WHEN st.name IN ('datetime2', 'datetimeoffset', 'time') THEN st.name + '(' + CONVERT(varchar(10), c.scale) + ')'
         WHEN st.name = 'float' AND c.precision < 53 THEN 'float(' + CONVERT(varchar(10), c.precision) + ')'
         ELSE t.name
       END AS [type],
       c.is_nullable AS [nullable],
       dc.definition AS [defaultVal],
       ic.seed_value AS [identitySeed],
       ic.increment_value AS [identityIncrement],
       cc.definition AS [computedDefinition],
       cc.is_persisted AS [computedPersisted]
FROM sys.columns c
JOIN sys.types t ON c.user_type_id = t.user_type_id
JOIN sys.schemas ts ON t.schema_id = ts.schema_id
LEFT JOIN sys.types st ON c.system_type_id = st.user_type_id
JOIN sys.tables tb ON c.object_id = tb.object_id
JOIN sys.schemas s ON tb.schema_id = s.schema_id
LEFT JOIN sys.default_constraints dc ON c.default_object_id = dc.object_id
LEFT JOIN sys.identity_columns ic ON c.object_id = ic.object_id AND c.column_id = ic.column_id
LEFT JOIN sys.computed_columns cc ON c.object_id = cc.object_id AND c.column_id = cc.column_id
WHERE s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo') AND tb.name = @p2
ORDER BY c.column_id`, schema, table)
	if err != nil {
		return nil, DDLOutput{}, err
	}
	if len(cols) == 0 {
		return nil, DDLOutput{}, fmt.Errorf("table %q was not found", in.Table)
	}
	resolvedSchema, _ := cols[0]["schema"].(string)
	resolvedTable, _ := cols[0]["table"].(string)
	qname, err := quoteResolvedTable(resolvedSchema, resolvedTable)
	if err != nil {
		return nil, DDLOutput{}, err
	}

	colDefs := make([]string, 0, len(cols)+1)
	for _, col := range cols {
		name, _ := col["name"].(string)
		typ, _ := col["type"].(string)
		qcol, err := sqlsafe.QuoteRawIdentifier(name)
		if err != nil {
			return nil, DDLOutput{}, err
		}
		if computed, _ := col["computedDefinition"].(string); computed != "" {
			def := qcol + " AS " + computed
			if persisted, _ := col["computedPersisted"].(bool); persisted {
				def += " PERSISTED"
				if nullable, _ := col["nullable"].(bool); !nullable {
					def += " NOT NULL"
				}
			}
			colDefs = append(colDefs, def)
			continue
		}
		def := qcol + " " + typ
		if seed, ok := col["identitySeed"]; ok && seed != nil {
			increment := col["identityIncrement"]
			def += fmt.Sprintf(" IDENTITY(%v,%v)", seed, increment)
		}
		if defaultVal, _ := col["defaultVal"].(string); defaultVal != "" {
			def += " DEFAULT " + defaultVal
		}
		if nullable, _ := col["nullable"].(bool); nullable {
			def += " NULL"
		} else {
			def += " NOT NULL"
		}
		colDefs = append(colDefs, def)
	}

	pks, err := r.client.Query(ctx, `
SELECT c.name AS [column], ic.is_descending_key AS [descending]
FROM sys.indexes i
JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
JOIN sys.tables t ON i.object_id = t.object_id
JOIN sys.schemas s ON t.schema_id = s.schema_id
WHERE i.is_primary_key = 1
  AND ic.key_ordinal > 0
  AND ic.is_included_column = 0
  AND s.name = @p1
  AND t.name = @p2
ORDER BY ic.key_ordinal`, resolvedSchema, resolvedTable)
	if err != nil {
		return nil, DDLOutput{}, err
	}
	pkCols := make([]string, 0, len(pks))
	for _, pk := range pks {
		name, _ := pk["column"].(string)
		qcol, err := sqlsafe.QuoteRawIdentifier(name)
		if err != nil {
			return nil, DDLOutput{}, err
		}
		if desc, _ := pk["descending"].(bool); desc {
			qcol += " DESC"
		}
		pkCols = append(pkCols, qcol)
	}
	if len(pkCols) > 0 {
		colDefs = append(colDefs, "PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}

	ddl := "CREATE TABLE " + qname + " (\n    " + strings.Join(colDefs, ",\n    ") + "\n);"
	return nil, DDLOutput{DDL: ddl}, nil
}

func (r *Registry) tableSize(ctx context.Context, _ *mcp.CallToolRequest, in ListTableInput) (*mcp.CallToolResult, RowsOutput, error) {
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	limit := bounded(in.Limit, 200, 1000)
	rows, err := r.client.Query(ctx, `
SELECT s.name AS [schema],
       t.name AS [table],
       SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.row_count ELSE 0 END) AS [estimatedRows],
       SUM(ps.reserved_page_count) * 8 AS [totalSizeKB],
       SUM(ps.used_page_count) * 8 AS [usedSizeKB],
       SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.in_row_data_page_count + ps.lob_used_page_count + ps.row_overflow_used_page_count ELSE 0 END) * 8 AS [tableSizeKB],
       (SUM(ps.used_page_count) - SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.in_row_data_page_count + ps.lob_used_page_count + ps.row_overflow_used_page_count ELSE 0 END)) * 8 AS [indexSizeKB]
FROM sys.tables t
JOIN sys.schemas s ON t.schema_id = s.schema_id
JOIN sys.dm_db_partition_stats ps ON t.object_id = ps.object_id
WHERE (@p1 = '' OR s.name = @p1)
  AND (@p2 = '' OR t.name LIKE @p2 ESCAPE '\')
GROUP BY s.name, t.name
ORDER BY SUM(ps.reserved_page_count) DESC, s.name, t.name
OFFSET 0 ROWS FETCH NEXT @p3 ROWS ONLY`, in.Schema, patternOrEmpty(in.Filter), limit)
	return nil, RowsOutput{Rows: rows}, err
}

type InsertInput struct {
	Table string           `json:"table" jsonschema:"Table name as table or schema.table."`
	Rows  []map[string]any `json:"rows"`
}

type MutationOutput struct {
	Executed     bool             `json:"executed"`
	RowsAffected int64            `json:"rowsAffected"`
	Preview      []map[string]any `json:"preview,omitempty"`
}

type insertStatement struct {
	query string
	args  []any
}

func buildInsertStatements(table string, rows []map[string]any) ([]insertStatement, error) {
	const (
		maxParamsPerBatch = 2000 // SQL Server RPC limit is 2100 parameters.
		maxRowsPerBatch   = 1000 // SQL Server VALUES clause limit is 1000 rows.
	)

	var statements []insertStatement
	var currentKey string
	var currentQCols []string
	var batchTuples []string
	var batchArgs []any

	flush := func() {
		if len(batchTuples) == 0 {
			return
		}
		statements = append(statements, insertStatement{
			query: fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", table, strings.Join(currentQCols, ", "), strings.Join(batchTuples, ", ")),
			args:  batchArgs,
		})
		batchTuples = nil
		batchArgs = nil
	}

	for _, row := range rows {
		cols := sortedKeys(row)
		if len(cols) == 0 {
			return nil, fmt.Errorf("row cannot be empty")
		}
		if len(cols) > maxParamsPerBatch {
			return nil, fmt.Errorf("row has %d columns, exceeding maximum parameter limit of %d", len(cols), maxParamsPerBatch)
		}
		qcols := make([]string, len(cols))
		rowArgs := make([]any, len(cols))
		for i, col := range cols {
			qcol, err := sqlsafe.QuoteIdentifier(col)
			if err != nil {
				return nil, err
			}
			val, err := sqlsafe.NormalizeValue(row[col])
			if err != nil {
				return nil, fmt.Errorf("column %q: %w", col, err)
			}
			qcols[i] = qcol
			rowArgs[i] = val
		}
		key := strings.Join(qcols, "\x00")
		if key != currentKey || len(batchTuples) >= maxRowsPerBatch || len(batchArgs)+len(cols) > maxParamsPerBatch {
			flush()
			currentKey = key
			currentQCols = qcols
		}
		placeholders := make([]string, len(cols))
		base := len(batchArgs)
		for i := range cols {
			placeholders[i] = fmt.Sprintf("@p%d", base+i+1)
		}
		batchTuples = append(batchTuples, "("+strings.Join(placeholders, ", ")+")")
		batchArgs = append(batchArgs, rowArgs...)
	}
	flush()
	return statements, nil
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
	statements, err := buildInsertStatements(table, in.Rows)
	if err != nil {
		return nil, MutationOutput{}, err
	}

	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	tx, err := r.client.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var total int64
	for _, statement := range statements {
		result, err := tx.ExecContext(ctx, statement.query, statement.args...)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, MutationOutput{}, err
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: total}, nil
}

type WhereMutationInput struct {
	Table   string         `json:"table" jsonschema:"Table name as table or schema.table."`
	Values  map[string]any `json:"values"`
	Where   string         `json:"where"`
	Params  map[string]any `json:"params,omitempty" jsonschema:"Values for named placeholders in where, keyed without @. Pass integers beyond +/-2^53 as strings to keep full precision."`
	Confirm bool           `json:"confirm,omitempty"`
}

func (r *Registry) previewWhereMutation(ctx context.Context, table, where string, args []any) (MutationOutput, error) {
	countRows, err := r.client.Query(ctx, fmt.Sprintf("SELECT COUNT_BIG(*) AS [count] FROM %s WHERE (\n%s\n)", table, where), args...)
	if err != nil {
		return MutationOutput{}, err
	}
	var total int64
	if len(countRows) > 0 {
		total, _ = countRows[0]["count"].(int64)
	}
	previewLimit := max(r.client.Config.MaxRowsDefault, 1)
	preview, err := r.client.Query(ctx, fmt.Sprintf("SELECT TOP (%d) * FROM %s WHERE (\n%s\n)", previewLimit, table, where), args...)
	if err != nil {
		return MutationOutput{}, err
	}
	return MutationOutput{Executed: false, RowsAffected: total, Preview: preview}, nil
}

func (r *Registry) updateData(ctx context.Context, _ *mcp.CallToolRequest, in WhereMutationInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDML() {
		return nil, MutationOutput{}, fmt.Errorf("update_data requires DML-RW or DDL-RW")
	}
	if len(in.Values) == 0 {
		return nil, MutationOutput{}, fmt.Errorf("values is required")
	}
	cols := sortedKeys(in.Values)
	set := make([]string, len(cols))
	valueArgs := make([]any, 0, len(cols))
	for i, col := range cols {
		qcol, err := sqlsafe.QuoteIdentifier(col)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		val, err := sqlsafe.NormalizeValue(in.Values[col])
		if err != nil {
			return nil, MutationOutput{}, fmt.Errorf("column %q: %w", col, err)
		}
		name := fmt.Sprintf("%svalue_%d", reservedParamPrefix, i+1)
		set[i] = fmt.Sprintf("%s = @%s", qcol, name)
		valueArgs = append(valueArgs, sql.Named(name, val))
	}
	table, where, whereArgs, err := mutationTarget(in.Table, in.Where, in.Params)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	if r.client.Config.RequireConfirmation && !in.Confirm {
		out, err := r.previewWhereMutation(ctx, table, where, whereArgs)
		return nil, out, err
	}
	valueArgs = append(valueArgs, whereArgs...)
	n, err := r.client.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE (\n%s\n)", table, strings.Join(set, ", "), where), valueArgs...)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: n}, nil
}

type DeleteInput struct {
	Table   string         `json:"table" jsonschema:"Table name as table or schema.table."`
	Where   string         `json:"where"`
	Params  map[string]any `json:"params,omitempty" jsonschema:"Values for named placeholders in where, keyed without @. Pass integers beyond +/-2^53 as strings to keep full precision."`
	Confirm bool           `json:"confirm,omitempty"`
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
		out, err := r.previewWhereMutation(ctx, table, where, args)
		return nil, out, err
	}
	n, err := r.client.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE (\n%s\n)", table, where), args...)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: n}, nil
}

type ColumnDef struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Nullable   bool   `json:"nullable,omitempty"`
	PrimaryKey bool   `json:"primaryKey,omitempty"`
	Identity   bool   `json:"identity,omitempty"`
}

type CreateTableInput struct {
	Table   string      `json:"table" jsonschema:"Table name as table or schema.table."`
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
	seenColumns := make(map[string]struct{}, len(in.Columns))
	identityCount := 0
	for _, col := range in.Columns {
		qcol, err := sqlsafe.QuoteIdentifier(col.Name)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		columnKey := strings.ToLower(strings.TrimSpace(col.Name))
		if _, duplicate := seenColumns[columnKey]; duplicate {
			return nil, MutationOutput{}, fmt.Errorf("duplicate column %q", col.Name)
		}
		seenColumns[columnKey] = struct{}{}
		typ, err := normalizeSQLType(col.Type)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		if col.PrimaryKey && col.Nullable {
			return nil, MutationOutput{}, fmt.Errorf("primary-key column %q cannot be nullable", col.Name)
		}
		if col.Identity {
			identityCount++
			if identityCount > 1 {
				return nil, MutationOutput{}, fmt.Errorf("a table can have only one identity column")
			}
			if !identityTypeAllowed(typ) {
				return nil, MutationOutput{}, fmt.Errorf("column %q has type %q, which cannot use IDENTITY", col.Name, typ)
			}
		}
		def := qcol + " " + typ
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
	if err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: n}, nil
}

type CreateIndexInput struct {
	Table   string   `json:"table" jsonschema:"Table name as table or schema.table."`
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
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
	if err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: n}, nil
}

type DropTableInput struct {
	Table   string `json:"table" jsonschema:"Table name as table or schema.table."`
	Confirm bool   `json:"confirm,omitempty"`
}

func (r *Registry) dropTable(ctx context.Context, _ *mcp.CallToolRequest, in DropTableInput) (*mcp.CallToolResult, MutationOutput, error) {
	if !r.client.Config.AccessLevel.AllowsDDL() {
		return nil, MutationOutput{}, fmt.Errorf("drop_table requires DDL-RW")
	}
	schema, tableName, err := splitTable(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	table, err := sqlsafe.QuoteMultipart(in.Table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	ctx, cancel := r.client.TimeoutContext(ctx)
	defer cancel()
	if r.client.Config.RequireConfirmation && !in.Confirm {
		preview, err := r.client.Query(ctx, `
SELECT s.name AS [schema],
       t.name AS [table],
       COALESCE(SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.row_count ELSE 0 END), 0) AS [estimatedRows]
FROM sys.tables t
JOIN sys.schemas s ON t.schema_id = s.schema_id
LEFT JOIN sys.dm_db_partition_stats ps ON t.object_id = ps.object_id
WHERE s.name = COALESCE(NULLIF(@p1, ''), SCHEMA_NAME(), 'dbo') AND t.name = @p2
GROUP BY s.name, t.name`, schema, tableName)
		if err != nil {
			return nil, MutationOutput{}, err
		}
		if len(preview) == 0 {
			return nil, MutationOutput{}, fmt.Errorf("table %q was not found", in.Table)
		}
		estimatedRows, _ := preview[0]["estimatedRows"].(int64)
		return nil, MutationOutput{Executed: false, RowsAffected: estimatedRows, Preview: preview}, nil
	}
	n, err := r.client.Exec(ctx, "DROP TABLE "+table)
	if err != nil {
		return nil, MutationOutput{}, err
	}
	return nil, MutationOutput{Executed: true, RowsAffected: n}, nil
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

// splitTable validates a 1- or 2-part table reference. When no schema is
// supplied, schema is returned as "" so catalog queries can resolve SQL
// Server's active SCHEMA_NAME() (falling back to 'dbo').
func splitTable(name string) (string, string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", "", fmt.Errorf("table is required")
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) == 1 {
		if _, err := sqlsafe.QuoteIdentifier(parts[0]); err != nil {
			return "", "", err
		}
		return "", strings.TrimSpace(parts[0]), nil
	}
	if len(parts) == 2 {
		if _, err := sqlsafe.QuoteIdentifier(parts[0]); err != nil {
			return "", "", err
		}
		if _, err := sqlsafe.QuoteIdentifier(parts[1]); err != nil {
			return "", "", err
		}
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	return "", "", fmt.Errorf("table must be table or schema.table")
}

func quoteResolvedTable(schema, table string) (string, error) {
	qschema, err := sqlsafe.QuoteRawIdentifier(schema)
	if err != nil {
		return "", err
	}
	qtable, err := sqlsafe.QuoteRawIdentifier(table)
	if err != nil {
		return "", err
	}
	return qschema + "." + qtable, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var sqlTypeRE = regexp.MustCompile(`(?i)^([A-Z][A-Z0-9_]*)(?:\(([0-9]+|MAX)(?:,([0-9]+))?\))?$`)

var scalarSQLTypes = map[string]struct{}{
	"bigint": {}, "bit": {}, "date": {}, "datetime": {}, "geography": {},
	"geometry": {}, "hierarchyid": {}, "image": {}, "int": {}, "money": {},
	"ntext": {}, "real": {}, "smalldatetime": {}, "smallint": {}, "smallmoney": {},
	"sql_variant": {}, "sysname": {}, "text": {}, "timestamp": {}, "tinyint": {},
	"uniqueidentifier": {}, "xml": {}, "rowversion": {},
}

func normalizeSQLType(value string) (string, error) {
	typ := strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	match := sqlTypeRE.FindStringSubmatch(typ)
	if match == nil {
		return "", fmt.Errorf("invalid column type %q", value)
	}
	base := strings.ToLower(match[1])
	first, second := strings.ToUpper(match[2]), match[3]
	if _, ok := scalarSQLTypes[base]; ok {
		if first != "" {
			return "", fmt.Errorf("type %q does not accept a size", base)
		}
		return base, nil
	}

	switch base {
	case "binary", "char", "nchar":
		if err := requireLength(base, first, second, false); err != nil {
			return "", err
		}
	case "varbinary", "varchar", "nvarchar":
		if err := requireLength(base, first, second, true); err != nil {
			return "", err
		}
	case "decimal", "numeric":
		if first == "" || first == "MAX" {
			return "", fmt.Errorf("type %q requires a precision from 1 to 38", base)
		}
		precision, _ := strconv.Atoi(first)
		scale := 0
		if second != "" {
			scale, _ = strconv.Atoi(second)
		}
		if precision < 1 || precision > 38 || scale > precision {
			return "", fmt.Errorf("invalid precision or scale for type %q", base)
		}
	case "datetime2", "datetimeoffset", "time":
		if second != "" || first == "MAX" {
			return "", fmt.Errorf("invalid precision for type %q", base)
		}
		if first != "" {
			precision, _ := strconv.Atoi(first)
			if precision > 7 {
				return "", fmt.Errorf("type %q precision must be between 0 and 7", base)
			}
		}
	case "float":
		if second != "" || first == "MAX" {
			return "", fmt.Errorf("invalid precision for type %q", base)
		}
		if first != "" {
			precision, _ := strconv.Atoi(first)
			if precision < 1 || precision > 53 {
				return "", fmt.Errorf("type float precision must be between 1 and 53")
			}
		}
	default:
		return "", fmt.Errorf("unsupported column type %q", value)
	}
	return strings.ToLower(typ), nil
}

func requireLength(base, length, second string, allowMax bool) error {
	if length == "" || second != "" || (length == "MAX" && !allowMax) {
		return fmt.Errorf("type %q requires a valid length", base)
	}
	if length != "MAX" {
		n, _ := strconv.Atoi(length)
		if n < 1 || n > 8000 || ((base == "nvarchar" || base == "nchar") && n > 4000) {
			return fmt.Errorf("invalid length for type %q", base)
		}
	}
	return nil
}

func identityTypeAllowed(typ string) bool {
	base := typ
	arguments := ""
	if index := strings.IndexByte(base, '('); index >= 0 {
		arguments = strings.TrimSuffix(base[index+1:], ")")
		base = base[:index]
	}
	if (base == "decimal" || base == "numeric") && strings.Contains(arguments, ",") && !strings.HasSuffix(arguments, ",0") {
		return false
	}
	return base == "tinyint" || base == "smallint" || base == "int" || base == "bigint" ||
		base == "decimal" || base == "numeric"
}

// reservedParamPrefix names update_data's internal SET parameters. It must start
// with a letter because database/sql rejects named arguments that do not.
const reservedParamPrefix = "mcp__"

func mutationTarget(tableName, where string, params map[string]any) (string, string, []any, error) {
	table, err := sqlsafe.QuoteMultipart(tableName)
	if err != nil {
		return "", "", nil, err
	}
	where = strings.TrimSpace(where)
	if where == "" {
		return "", "", nil, fmt.Errorf("where is required")
	}
	if err := sqlsafe.ValidateWhereClause(where); err != nil {
		return "", "", nil, fmt.Errorf("where clause contains disallowed SQL: %w", err)
	}
	for name := range params {
		if strings.HasPrefix(strings.ToLower(name), reservedParamPrefix) {
			return "", "", nil, fmt.Errorf("parameter name %q uses the reserved %s prefix", name, reservedParamPrefix)
		}
	}
	args, err := sqlsafe.BindNamedParameters(where, params)
	if err != nil {
		return "", "", nil, err
	}
	return table, where, args, nil
}
