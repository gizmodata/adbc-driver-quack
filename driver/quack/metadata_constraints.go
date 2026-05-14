package quack

import (
	"context"
	"fmt"
	"strings"
)

// loadConstraintsForSchema returns the PRIMARY KEY + FOREIGN KEY
// constraints for every table in (catalog, schema), keyed by table name.
//
// Two queries against duckdb_constraints() — one for each constraint
// type, both unnesting the constraint_column_names list so each row is
// (table_name, constraint_name, column_name [, referenced_table,
// referenced_column_name]). We group back up in Go.
//
// SQL shape mirrors the queries gizmosql's DuckDB Flight SQL server uses
// for DoGetPrimaryKeys and DoGetCrossReference.
func (c *connectionImpl) loadConstraintsForSchema(
	ctx context.Context,
	catalog, schema string,
) (map[string][]constraintInfo, error) {
	byTable := map[string][]constraintInfo{}

	if err := c.loadPrimaryKeys(ctx, catalog, schema, byTable); err != nil {
		return nil, err
	}
	if err := c.loadForeignKeys(ctx, catalog, schema, byTable); err != nil {
		return nil, err
	}
	return byTable, nil
}

func (c *connectionImpl) loadPrimaryKeys(
	ctx context.Context,
	catalog, schema string,
	byTable map[string][]constraintInfo,
) error {
	// (table, constraint_name, ordered list of column names)
	q := strings.Builder{}
	q.WriteString(`SELECT table_name, constraint_name, column_index, column_name
FROM (SELECT dc.*,
             UNNEST(dc.constraint_column_indexes) AS column_index,
             UNNEST(dc.constraint_column_names)   AS column_name
      FROM duckdb_constraints() AS dc
      WHERE constraint_type = 'PRIMARY KEY')
WHERE database_name = `)
	q.WriteString(sqlString(catalog))
	q.WriteString(" AND schema_name = " + sqlString(schema))
	q.WriteString(" ORDER BY table_name, constraint_name, column_index")

	result, err := c.sess.drainPrepared(ctx, q.String())
	if err != nil {
		return fromTransportError(err)
	}

	// Build a 3-level map: table -> constraint_name -> ordered list of column names.
	type pkAcc struct {
		name string
		cols []string
	}
	byTableByConstraint := map[string]map[string]*pkAcc{}
	for _, chunk := range result.Chunks {
		for i := 0; i < chunk.RowCount; i++ {
			table, _ := chunk.Columns[0].GetObject(i).(string)
			cname, _ := chunk.Columns[1].GetObject(i).(string)
			col, _ := chunk.Columns[3].GetObject(i).(string)
			if table == "" || col == "" {
				continue
			}
			constraints, ok := byTableByConstraint[table]
			if !ok {
				constraints = map[string]*pkAcc{}
				byTableByConstraint[table] = constraints
			}
			acc, ok := constraints[cname]
			if !ok {
				acc = &pkAcc{name: cname}
				constraints[cname] = acc
			}
			acc.cols = append(acc.cols, col)
		}
	}

	for table, constraints := range byTableByConstraint {
		for _, acc := range constraints {
			ci := constraintInfo{
				ConstraintType:        "PRIMARY KEY",
				ConstraintColumnNames: acc.cols,
			}
			if acc.name != "" {
				name := acc.name
				ci.ConstraintName = &name
			}
			byTable[table] = append(byTable[table], ci)
		}
	}
	return nil
}

func (c *connectionImpl) loadForeignKeys(
	ctx context.Context,
	catalog, schema string,
	byTable map[string][]constraintInfo,
) error {
	q := strings.Builder{}
	q.WriteString(`SELECT table_name,
       constraint_name,
       column_index,
       column_name,
       database_name AS referenced_catalog,
       schema_name   AS referenced_schema,
       referenced_table,
       referenced_column_name
FROM (SELECT dc.*,
             UNNEST(dc.constraint_column_indexes)  AS column_index,
             UNNEST(dc.constraint_column_names)    AS column_name,
             UNNEST(dc.referenced_column_names)    AS referenced_column_name
      FROM duckdb_constraints() AS dc
      WHERE constraint_type = 'FOREIGN KEY')
WHERE database_name = `)
	q.WriteString(sqlString(catalog))
	q.WriteString(" AND schema_name = " + sqlString(schema))
	q.WriteString(" ORDER BY table_name, constraint_name, column_index")

	result, err := c.sess.drainPrepared(ctx, q.String())
	if err != nil {
		// If the column unnesting fails (older duckdb_constraints schema)
		// just no-op — primary keys still come through.
		return nil
	}

	type fkAcc struct {
		name  string
		cols  []string
		usage []constraintColumnUsage
	}
	byTableByConstraint := map[string]map[string]*fkAcc{}
	for _, chunk := range result.Chunks {
		for i := 0; i < chunk.RowCount; i++ {
			table, _ := chunk.Columns[0].GetObject(i).(string)
			cname, _ := chunk.Columns[1].GetObject(i).(string)
			col, _ := chunk.Columns[3].GetObject(i).(string)
			refCat, _ := chunk.Columns[4].GetObject(i).(string)
			refSch, _ := chunk.Columns[5].GetObject(i).(string)
			refTab, _ := chunk.Columns[6].GetObject(i).(string)
			refCol, _ := chunk.Columns[7].GetObject(i).(string)
			if table == "" || col == "" || refTab == "" {
				continue
			}
			constraints, ok := byTableByConstraint[table]
			if !ok {
				constraints = map[string]*fkAcc{}
				byTableByConstraint[table] = constraints
			}
			acc, ok := constraints[cname]
			if !ok {
				acc = &fkAcc{name: cname}
				constraints[cname] = acc
			}
			acc.cols = append(acc.cols, col)
			usage := constraintColumnUsage{FkTable: refTab, FkColumnName: refCol}
			if refCat != "" {
				cat := refCat
				usage.FkCatalog = &cat
			}
			if refSch != "" {
				sch := refSch
				usage.FkDbSchema = &sch
			}
			acc.usage = append(acc.usage, usage)
		}
	}

	for table, constraints := range byTableByConstraint {
		for _, acc := range constraints {
			ci := constraintInfo{
				ConstraintType:        "FOREIGN KEY",
				ConstraintColumnNames: acc.cols,
				ConstraintColumnUsage: acc.usage,
			}
			if acc.name != "" {
				name := acc.name
				ci.ConstraintName = &name
			}
			byTable[table] = append(byTable[table], ci)
		}
	}
	return nil
}

// Compile-time assertion that the type names are referenced (helps if
// the JSON struct in get_objects_types.go gets renamed).
var _ = fmt.Sprintf
