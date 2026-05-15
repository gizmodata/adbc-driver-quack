// Hermetic regression tests for bulk-ingest mode handling: DDL
// generation and SetOption parsing. These need no Quack server and run
// under plain `go test ./...`. End-to-end coverage of each mode against
// a live server lives in python/tests/test_smoke.py.
package quack

import (
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
)

func intSchema() *arrow.Schema {
	return arrow.NewSchema([]arrow.Field{
		{Name: "int64s", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
	}, nil)
}

func TestBuildCreateTableSQLModes(t *testing.T) {
	cases := []struct {
		name        string
		ifNotExists bool
		orReplace   bool
		temporary   bool
		want        string
	}{
		{
			name: "create",
			want: `CREATE TABLE "bulk_ingest" ("int64s" BIGINT)`,
		},
		{
			name:      "replace",
			orReplace: true,
			want:      `CREATE OR REPLACE TABLE "bulk_ingest" ("int64s" BIGINT)`,
		},
		{
			name:        "create_append",
			ifNotExists: true,
			want:        `CREATE TABLE IF NOT EXISTS "bulk_ingest" ("int64s" BIGINT)`,
		},
		{
			name:      "temporary",
			temporary: true,
			want:      `CREATE TEMPORARY TABLE "bulk_ingest" ("int64s" BIGINT)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCreateTableSQL("", "bulk_ingest", intSchema(), tc.ifNotExists, tc.orReplace, tc.temporary)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DDL mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestBuildCreateTableSQLSchemaAndQuoting(t *testing.T) {
	// Schema qualification + identifiers needing quote-escaping.
	s := arrow.NewSchema([]arrow.Field{
		{Name: `od"d`, Type: arrow.PrimitiveTypes.Int32, Nullable: true},
		{Name: "not null col", Type: arrow.BinaryTypes.String, Nullable: false},
	}, nil)
	got, err := buildCreateTableSQL("my schema", "tbl", s, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `CREATE TABLE "my schema"."tbl" ("od""d" INTEGER, "not null col" VARCHAR NOT NULL)`
	if got != want {
		t.Fatalf("DDL mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildCreateTableSQLErrors(t *testing.T) {
	if _, err := buildCreateTableSQL("", "", intSchema(), false, false, false); err == nil {
		t.Fatal("expected error for empty target table")
	}
	bad := arrow.NewSchema([]arrow.Field{
		{Name: "d", Type: &arrow.DurationType{Unit: arrow.Second}, Nullable: true},
	}, nil)
	if _, err := buildCreateTableSQL("", "t", bad, false, false, false); err == nil {
		t.Fatal("expected error for unsupported arrow type")
	}
}

// duckDBTypeForArrow must stay in lockstep with logicalTypeForArrow:
// every type creatable in DDL must also be ingestable by the encoder.
func TestDuckDBTypeForArrow(t *testing.T) {
	cases := []struct {
		dt   arrow.DataType
		want string
	}{
		{arrow.FixedWidthTypes.Boolean, "BOOLEAN"},
		{arrow.PrimitiveTypes.Int8, "TINYINT"},
		{arrow.PrimitiveTypes.Int16, "SMALLINT"},
		{arrow.PrimitiveTypes.Int32, "INTEGER"},
		{arrow.PrimitiveTypes.Int64, "BIGINT"},
		{arrow.PrimitiveTypes.Uint8, "UTINYINT"},
		{arrow.PrimitiveTypes.Uint16, "USMALLINT"},
		{arrow.PrimitiveTypes.Uint32, "UINTEGER"},
		{arrow.PrimitiveTypes.Uint64, "UBIGINT"},
		{arrow.PrimitiveTypes.Float32, "FLOAT"},
		{arrow.PrimitiveTypes.Float64, "DOUBLE"},
		{arrow.BinaryTypes.String, "VARCHAR"},
		{arrow.BinaryTypes.LargeString, "VARCHAR"},
		{arrow.BinaryTypes.Binary, "BLOB"},
		{arrow.FixedWidthTypes.Date32, "DATE"},
		{&arrow.TimestampType{Unit: arrow.Microsecond}, "TIMESTAMP"},
		{&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, "TIMESTAMP WITH TIME ZONE"},
		{&arrow.Decimal128Type{Precision: 38, Scale: 4}, "DECIMAL(38, 4)"},
	}
	for _, tc := range cases {
		got, err := duckDBTypeForArrow(tc.dt)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.dt, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.dt, got, tc.want)
		}
	}
}

func TestSetOptionIngestMode(t *testing.T) {
	s := &statementImpl{}

	// Default: no mode set means "create" is applied at ingest time.
	if s.ingestMode != "" {
		t.Fatalf("expected empty default ingestMode, got %q", s.ingestMode)
	}

	for _, m := range []string{
		adbc.OptionValueIngestModeCreate,
		adbc.OptionValueIngestModeAppend,
		adbc.OptionValueIngestModeReplace,
		adbc.OptionValueIngestModeCreateAppend,
	} {
		if err := s.SetOption(adbc.OptionKeyIngestMode, m); err != nil {
			t.Fatalf("SetOption(mode=%q) unexpected error: %v", m, err)
		}
		if s.ingestMode != m {
			t.Fatalf("ingestMode = %q, want %q", s.ingestMode, m)
		}
	}

	err := s.SetOption(adbc.OptionKeyIngestMode, "adbc.ingest.mode.bogus")
	if err == nil {
		t.Fatal("expected error for unknown ingest mode")
	}
	if ae, ok := err.(adbc.Error); !ok || ae.Code != adbc.StatusInvalidArgument {
		t.Fatalf("expected StatusInvalidArgument adbc.Error, got %#v", err)
	}
}

func TestSetOptionIngestTemporary(t *testing.T) {
	s := &statementImpl{}
	if err := s.SetOption(adbc.OptionValueIngestTemporary, adbc.OptionValueEnabled); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !s.ingestTemporary {
		t.Fatal("ingestTemporary should be true after enabling")
	}
	if err := s.SetOption(adbc.OptionValueIngestTemporary, adbc.OptionValueDisabled); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.ingestTemporary {
		t.Fatal("ingestTemporary should be false after disabling")
	}
	if err := s.SetOption(adbc.OptionValueIngestTemporary, "yes"); err == nil {
		t.Fatal("expected error for non-boolean temporary value")
	}
}
