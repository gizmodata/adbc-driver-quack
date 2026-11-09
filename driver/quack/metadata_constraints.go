package quack

import (
	"context"
)

// loadConstraintsForSchema returns the PRIMARY KEY, UNIQUE and FOREIGN KEY
// constraints of every table in (catalog, schema), keyed by table name.
//
// One query against duckdb_constraints(), reading its column-name lists
// as they are: they hold the columns in the order the constraint declares
// them (PRIMARY KEY (b, a) stays [b, a]), which ADBC's GetObjects reports.
// DuckDB foreign keys reference a table in the same schema.
func (c *connectionImpl) loadConstraintsForSchema(
	ctx context.Context,
	catalog, schema string,
) (map[string][]constraintInfo, error) {
	q := `SELECT table_name, constraint_name, constraint_type, constraint_column_names,
       referenced_table, referenced_column_names
FROM duckdb_constraints()
WHERE database_name = ` + sqlString(catalog) + ` AND schema_name = ` + sqlString(schema) + `
  AND constraint_type IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY')
ORDER BY table_name, constraint_index`
	result, err := c.sess.drainPrepared(ctx, q)
	if err != nil {
		return nil, fromTransportError(err)
	}
	strs := func(v interface{}) []string {
		items, _ := v.([]interface{})
		out := make([]string, 0, len(items))
		for _, it := range items {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	byTable := map[string][]constraintInfo{}
	for _, chunk := range result.Chunks {
		for i := 0; i < chunk.RowCount; i++ {
			table, _ := chunk.Columns[0].GetObject(i).(string)
			name, _ := chunk.Columns[1].GetObject(i).(string)
			kind, _ := chunk.Columns[2].GetObject(i).(string)
			cols := strs(chunk.Columns[3].GetObject(i))
			if table == "" || len(cols) == 0 {
				continue
			}
			ci := constraintInfo{ConstraintType: kind, ConstraintColumnNames: cols}
			if name != "" {
				n := name
				ci.ConstraintName = &n
			}
			if kind == "FOREIGN KEY" {
				refTable, _ := chunk.Columns[4].GetObject(i).(string)
				refCols := strs(chunk.Columns[5].GetObject(i))
				if refTable == "" || len(refCols) != len(cols) {
					continue
				}
				for _, rc := range refCols {
					cat, sch := catalog, schema
					ci.ConstraintColumnUsage = append(ci.ConstraintColumnUsage, constraintColumnUsage{
						FkCatalog: &cat, FkDbSchema: &sch, FkTable: refTable, FkColumnName: rc,
					})
				}
			}
			byTable[table] = append(byTable[table], ci)
		}
	}
	return byTable, nil
}
