package quack

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestBuildGetObjectsRecordReader(t *testing.T) {
	mem := memory.NewGoAllocator()
	cat1, cat2 := "memory", "system"
	sch1 := "main"
	infos := []getObjectsInfo{
		{
			CatalogName: &cat1,
			CatalogDbSchemas: []dbSchemaInfo{{
				DbSchemaName: &sch1,
				DbSchemaTables: []tableInfo{{
					TableName: "users",
					TableType: "TABLE",
					TableColumns: []columnInfo{
						{ColumnName: "id", OrdinalPosition: ptrInt32(1)},
						{ColumnName: "name", OrdinalPosition: ptrInt32(2)},
					},
					TableConstraints: []constraintInfo{},
				}},
			}},
		},
		{
			CatalogName:      &cat2,
			CatalogDbSchemas: []dbSchemaInfo{},
		},
	}

	rr, err := buildGetObjectsRecordReader(mem, infos)
	if err != nil {
		t.Fatalf("buildGetObjectsRecordReader: %v", err)
	}
	defer rr.Release()

	if !rr.Next() {
		t.Fatal("expected at least one batch")
	}
	rec := rr.Record()
	if rec.NumRows() != 2 {
		t.Errorf("expected 2 catalog rows, got %d", rec.NumRows())
	}
	catalogCol := rec.Column(0)
	if catalogCol.IsNull(0) || catalogCol.IsNull(1) {
		t.Errorf("catalog names should be non-null")
	}
}

func ptrInt32(v int32) *int32 { return &v }
