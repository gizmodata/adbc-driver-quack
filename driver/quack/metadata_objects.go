package quack

import (
	"context"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// getObjectsImpl implements adbc.Connection.GetObjects. It walks the
// catalog/schema/table/column hierarchy using the same SQL queries
// QuackDatabaseMetaData uses on the JDBC side (which themselves are
// ports of DuckDB's own JDBC driver) and serializes the result into
// the standard ADBC GetObjects record via JSON-round-trip into
// a RecordBuilder bound to adbc.GetObjectsSchema.
//
// Table constraints (primary/foreign keys) are reported as an empty
// list for now — ADBC tools that need constraint visibility fall back
// gracefully to per-table queries against duckdb_constraints.
func (c *connectionImpl) getObjectsImpl(
	ctx context.Context,
	depth adbc.ObjectDepth,
	catalog *string,
	dbSchema *string,
	tableName *string,
	columnName *string,
	tableTypes []string,
) (array.RecordReader, error) {
	catalogs, err := c.listCatalogs(ctx, catalog)
	if err != nil {
		return nil, err
	}

	infos := make([]getObjectsInfo, 0, len(catalogs))
	for _, cat := range catalogs {
		catName := cat
		entry := getObjectsInfo{CatalogName: &catName, CatalogDbSchemas: []dbSchemaInfo{}}
		if depth != adbc.ObjectDepthCatalogs {
			schemas, err := c.listDBSchemas(ctx, cat, dbSchema)
			if err != nil {
				return nil, err
			}
			for _, sch := range schemas {
				schName := sch
				schemaEntry := dbSchemaInfo{DbSchemaName: &schName, DbSchemaTables: []tableInfo{}}
				if depth != adbc.ObjectDepthDBSchemas {
					tables, err := c.listTablesForDBSchema(ctx, cat, sch,
						tableName, columnName, tableTypes,
						depth == adbc.ObjectDepthAll || depth == adbc.ObjectDepthColumns)
					if err != nil {
						return nil, err
					}
					schemaEntry.DbSchemaTables = tables
				}
				entry.CatalogDbSchemas = append(entry.CatalogDbSchemas, schemaEntry)
			}
		}
		infos = append(infos, entry)
	}

	return buildGetObjectsRecordReader(c.alloc, infos)
}

// listCatalogs runs the same query QuackDatabaseMetaData.getCatalogs uses.
func (c *connectionImpl) listCatalogs(ctx context.Context, filter *string) ([]string, error) {
	sb := strings.Builder{}
	sb.WriteString("SELECT DISTINCT catalog_name FROM information_schema.schemata WHERE TRUE")
	appendEqualsQual(&sb, "catalog_name", filter)
	sb.WriteString(" ORDER BY catalog_name")
	return c.queryStringColumn(ctx, sb.String())
}

func (c *connectionImpl) listDBSchemas(ctx context.Context, catalog string, filter *string) ([]string, error) {
	sb := strings.Builder{}
	sb.WriteString("SELECT schema_name FROM information_schema.schemata WHERE TRUE")
	sb.WriteString(" AND catalog_name = " + sqlString(catalog))
	appendLikeQual(&sb, "schema_name", filter)
	sb.WriteString(" ORDER BY schema_name")
	return c.queryStringColumn(ctx, sb.String())
}

// listTablesForDBSchema runs a duckdb_tables() ∪ duckdb_views() query
// plus a duckdb_columns() query (when includeColumns is true) and
// stitches the results into tableInfo records.
func (c *connectionImpl) listTablesForDBSchema(
	ctx context.Context,
	catalog, schema string,
	tableFilter, columnFilter *string,
	tableTypes []string,
	includeColumns bool,
) ([]tableInfo, error) {
	sb := strings.Builder{}
	sb.WriteString(`SELECT table_name, table_type FROM (
SELECT database_name AS table_catalog, schema_name AS table_schema, table_name,
       CASE WHEN ("temporary") THEN ('LOCAL TEMPORARY') WHEN ("internal") THEN 'SYSTEM TABLE' ELSE 'TABLE' END AS table_type
FROM duckdb_tables()
UNION ALL
SELECT database_name, schema_name, view_name,
       CASE WHEN ("internal") THEN 'SYSTEM VIEW' ELSE 'VIEW' END
FROM duckdb_views()
) x WHERE table_catalog = `)
	sb.WriteString(sqlString(catalog))
	sb.WriteString(" AND table_schema = " + sqlString(schema))
	appendLikeQual(&sb, "table_name", tableFilter)
	if len(tableTypes) > 0 {
		sb.WriteString(" AND table_type IN (")
		for i, t := range tableTypes {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(sqlString(t))
		}
		sb.WriteString(")")
	}
	sb.WriteString(" ORDER BY table_name")

	tableRows, err := c.queryTableNames(ctx, sb.String())
	if err != nil {
		return nil, err
	}

	// Load the full set of constraints for the schema once (two queries)
	// rather than per-table. Most schemas have few constraints; one round
	// trip per schema is cheaper than one per table.
	var constraintsByTable map[string][]constraintInfo
	if includeColumns {
		constraintsByTable, err = c.loadConstraintsForSchema(ctx, catalog, schema)
		if err != nil {
			return nil, err
		}
	}

	out := make([]tableInfo, 0, len(tableRows))
	for _, t := range tableRows {
		info := tableInfo{
			TableName:        t.name,
			TableType:        t.tableType,
			TableColumns:     []columnInfo{},
			TableConstraints: []constraintInfo{},
		}
		if includeColumns {
			cols, err := c.listColumnsForTable(ctx, catalog, schema, t.name, columnFilter)
			if err != nil {
				return nil, err
			}
			info.TableColumns = cols
			if cs, ok := constraintsByTable[t.name]; ok {
				info.TableConstraints = cs
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *connectionImpl) listColumnsForTable(
	ctx context.Context,
	catalog, schema, table string,
	columnFilter *string,
) ([]columnInfo, error) {
	sb := strings.Builder{}
	sb.WriteString(`SELECT column_name, column_index, data_type, is_nullable, column_default, comment
FROM duckdb_columns() WHERE database_name = `)
	sb.WriteString(sqlString(catalog))
	sb.WriteString(" AND schema_name = " + sqlString(schema))
	sb.WriteString(" AND table_name = " + sqlString(table))
	appendLikeQual(&sb, "column_name", columnFilter)
	sb.WriteString(" ORDER BY column_index")

	result, err := c.sess.prepare(ctx, sb.String())
	if err != nil {
		return nil, fromTransportError(err)
	}
	out := []columnInfo{}
	for _, chunk := range result.Chunks {
		for i := 0; i < chunk.RowCount; i++ {
			col := columnInfo{}
			if v, ok := chunk.Columns[0].GetObject(i).(string); ok {
				col.ColumnName = v
			}
			if v := chunk.Columns[1].GetObject(i); v != nil {
				if n, ok := toInt32Ok(v); ok {
					ordinal := n
					col.OrdinalPosition = &ordinal
				}
			}
			if v, ok := chunk.Columns[2].GetObject(i).(string); ok {
				typeName := v
				col.XdbcTypeName = &typeName
			}
			if v := chunk.Columns[3].GetObject(i); v != nil {
				if b, ok := v.(bool); ok {
					var nullable int16
					if b {
						nullable = 1
					}
					col.XdbcNullable = &nullable
					yn := "NO"
					if b {
						yn = "YES"
					}
					col.XdbcIsNullable = &yn
				}
			}
			if v, ok := chunk.Columns[4].GetObject(i).(string); ok {
				def := v
				col.XdbcColumnDef = &def
			}
			if v, ok := chunk.Columns[5].GetObject(i).(string); ok {
				rem := v
				col.Remarks = &rem
			}
			out = append(out, col)
		}
	}
	return out, nil
}

// ---- helpers ----

type tableNameRow struct {
	name      string
	tableType string
}

func (c *connectionImpl) queryStringColumn(ctx context.Context, sql string) ([]string, error) {
	result, err := c.sess.prepare(ctx, sql)
	if err != nil {
		return nil, fromTransportError(err)
	}
	out := []string{}
	for _, chunk := range result.Chunks {
		col := chunk.Columns[0]
		for i := 0; i < chunk.RowCount; i++ {
			if s, ok := col.GetObject(i).(string); ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (c *connectionImpl) queryTableNames(ctx context.Context, sql string) ([]tableNameRow, error) {
	result, err := c.sess.prepare(ctx, sql)
	if err != nil {
		return nil, fromTransportError(err)
	}
	out := []tableNameRow{}
	for _, chunk := range result.Chunks {
		for i := 0; i < chunk.RowCount; i++ {
			name, _ := chunk.Columns[0].GetObject(i).(string)
			tt, _ := chunk.Columns[1].GetObject(i).(string)
			out = append(out, tableNameRow{name: name, tableType: tt})
		}
	}
	return out, nil
}

func appendEqualsQual(sb *strings.Builder, col string, value *string) {
	if value == nil {
		return
	}
	sb.WriteString(" AND ")
	sb.WriteString(col)
	if *value == "" {
		sb.WriteString(" IS NULL")
		return
	}
	sb.WriteString(" = ")
	sb.WriteString(sqlString(*value))
}

func appendLikeQual(sb *strings.Builder, col string, pattern *string) {
	if pattern == nil {
		return
	}
	sb.WriteString(" AND ")
	sb.WriteString(col)
	if *pattern == "" {
		sb.WriteString(" IS NULL")
		return
	}
	sb.WriteString(" LIKE ")
	sb.WriteString(sqlString(*pattern))
	sb.WriteString(" ESCAPE '\\'")
}

func toInt32Ok(v interface{}) (int32, bool) {
	switch x := v.(type) {
	case int:
		return int32(x), true
	case int8:
		return int32(x), true
	case int16:
		return int32(x), true
	case int32:
		return x, true
	case int64:
		return int32(x), true
	case uint32:
		return int32(x), true
	case uint64:
		return int32(x), true
	}
	return 0, false
}
