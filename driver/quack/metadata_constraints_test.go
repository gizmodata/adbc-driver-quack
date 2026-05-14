package quack

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// TestBuildGetObjectsRecordReaderWithConstraints exercises the JSON
// round-trip path with non-empty table_constraints — both PRIMARY KEY
// and FOREIGN KEY constraints with column_usage rows. Validates that
// the arrow record builder accepts the nested constraint structure
// from our local types.
func TestBuildGetObjectsRecordReaderWithConstraints(t *testing.T) {
	mem := memory.NewGoAllocator()
	cat := "memory"
	sch := "main"
	pkName := "users_pk"
	fkName := "orders_users_fk"
	usersTbl := "users"

	infos := []getObjectsInfo{{
		CatalogName: &cat,
		CatalogDbSchemas: []dbSchemaInfo{{
			DbSchemaName: &sch,
			DbSchemaTables: []tableInfo{
				{
					TableName: "users",
					TableType: "TABLE",
					TableColumns: []columnInfo{
						{ColumnName: "id"},
						{ColumnName: "name"},
					},
					TableConstraints: []constraintInfo{
						{
							ConstraintName:        &pkName,
							ConstraintType:        "PRIMARY KEY",
							ConstraintColumnNames: []string{"id"},
						},
					},
				},
				{
					TableName: "orders",
					TableType: "TABLE",
					TableColumns: []columnInfo{
						{ColumnName: "order_id"},
						{ColumnName: "user_id"},
					},
					TableConstraints: []constraintInfo{
						{
							ConstraintName:        &fkName,
							ConstraintType:        "FOREIGN KEY",
							ConstraintColumnNames: []string{"user_id"},
							ConstraintColumnUsage: []constraintColumnUsage{
								{FkCatalog: &cat, FkDbSchema: &sch, FkTable: usersTbl, FkColumnName: "id"},
							},
						},
					},
				},
			},
		}},
	}}

	rr, err := buildGetObjectsRecordReader(mem, infos)
	if err != nil {
		t.Fatalf("buildGetObjectsRecordReader: %v", err)
	}
	defer rr.Release()

	if !rr.Next() {
		t.Fatal("expected at least one record batch")
	}
	rec := rr.Record()
	if rec.NumRows() != 1 {
		t.Errorf("expected 1 catalog row, got %d", rec.NumRows())
	}
	// Sanity check that the schema matches the canonical ADBC GetObjectsSchema.
	if rec.NumCols() != 2 {
		t.Errorf("expected 2 top-level columns, got %d", rec.NumCols())
	}
}
