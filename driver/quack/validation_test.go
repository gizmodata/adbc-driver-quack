// Optional ADBC conformance suite wiring for the Quack driver.
//
// This test plugs the driver into apache/arrow-adbc's generic
// `validation` test framework, which exercises ~30 driver-level
// behaviors (NewDatabase / NewConnection / GetInfo / GetObjects /
// Statement lifecycle, bulk ingest paths, etc.). It is **opt-in**:
// it requires a running Quack server (because the framework's test
// methods drive real SQL through the driver) and so is gated on the
// `QUACK_VALIDATION_URI` env var. When unset, the test is skipped
// so `go test ./...` stays hermetic for routine PRs.
//
// To run locally:
//
//	# In one terminal, start a Quack server (e.g. from the python conftest):
//	QUACK_VALIDATION_URI=quack://127.0.0.1:9494 \
//	QUACK_VALIDATION_TOKEN=my-token \
//	    go test ./driver/quack -run TestValidation -v
package quack

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/validation"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/suite"
)

type quackQuirks struct {
	uri   string
	token string
	alloc memory.Allocator
}

func (q *quackQuirks) SetupDriver(t *testing.T) adbc.Driver {
	q.alloc = memory.DefaultAllocator
	return NewDriver(q.alloc)
}

func (q *quackQuirks) TearDownDriver(_ *testing.T, _ adbc.Driver) {}

func (q *quackQuirks) DatabaseOptions() map[string]string {
	opts := map[string]string{
		OptionURI: q.uri,
	}
	if q.token != "" {
		opts[OptionToken] = q.token
	}
	return opts
}

// BindParameter — Quack 1.0 has no parameter binding on the wire. The
// validation tests that need it are filtered out via
// SupportsDynamicParameterBinding=false.
func (q *quackQuirks) BindParameter(_ int) string { return "?" }

func (q *quackQuirks) SupportsBulkIngest(mode string) bool {
	// We support the APPEND_REQUEST path for append-mode ingest; create /
	// create_append are not yet implemented.
	return mode == adbc.OptionValueIngestModeAppend
}

func (q *quackQuirks) SupportsConcurrentStatements() bool          { return true }
func (q *quackQuirks) SupportsCurrentCatalogSchema() bool          { return false }
func (q *quackQuirks) SupportsGetSetOptions() bool                 { return true }
func (q *quackQuirks) SupportsExecuteSchema() bool                 { return false }
func (q *quackQuirks) SupportsPartitionedData() bool               { return false }
func (q *quackQuirks) SupportsStatistics() bool                    { return false }
func (q *quackQuirks) SupportsTransactions() bool                  { return true }
func (q *quackQuirks) SupportsGetParameterSchema() bool            { return false }
func (q *quackQuirks) SupportsDynamicParameterBinding() bool       { return false }
func (q *quackQuirks) SupportsErrorIngestIncompatibleSchema() bool { return true }

func (q *quackQuirks) GetMetadata(code adbc.InfoCode) interface{} {
	switch code {
	case adbc.InfoVendorName:
		return "DuckDB (via Quack)"
	case adbc.InfoDriverName:
		return "ADBC Quack Driver - Go"
	case adbc.InfoDriverArrowVersion:
		return "arrow-go/v18"
	case adbc.InfoVendorSql:
		return true
	case adbc.InfoVendorSubstrait:
		return false
	}
	return nil
}

func (q *quackQuirks) CreateSampleTable(tableName string, r arrow.RecordBatch) error {
	// Build an in-line `INSERT INTO ... VALUES (...)` statement using the
	// record's schema. Used by the validation suite to set up small
	// sample tables for read-back checks.
	driver := NewDriver(q.alloc)
	db, err := driver.NewDatabase(q.DatabaseOptions())
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Open(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	stmt, err := conn.NewStatement()
	if err != nil {
		return err
	}
	defer stmt.Close()

	// CREATE
	cols := []string{}
	for _, f := range r.Schema().Fields() {
		cols = append(cols, fmt.Sprintf("%s %s", f.Name, arrowToDuckDBType(f.Type)))
	}
	createSQL := fmt.Sprintf("CREATE OR REPLACE TABLE %s (%s)", tableName, strings.Join(cols, ", "))
	if err := stmt.SetSqlQuery(createSQL); err != nil {
		return err
	}
	if _, err := stmt.ExecuteUpdate(context.Background()); err != nil {
		return err
	}

	// INSERT via bulk-ingest (APPEND_REQUEST) — much simpler than building
	// the VALUES clause and exercises the real path.
	if err := stmt.SetOption(adbc.OptionKeyIngestTargetTable, tableName); err != nil {
		return err
	}
	if err := stmt.Bind(context.Background(), r); err != nil {
		return err
	}
	_, err = stmt.ExecuteUpdate(context.Background())
	return err
}

func (q *quackQuirks) SampleTableSchemaMetadata(_ string, _ arrow.DataType) arrow.Metadata {
	return arrow.Metadata{}
}

func (q *quackQuirks) DropTable(conn adbc.Connection, tableName string) error {
	stmt, err := conn.NewStatement()
	if err != nil {
		return err
	}
	defer stmt.Close()
	if err := stmt.SetSqlQuery(fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName)); err != nil {
		return err
	}
	_, err = stmt.ExecuteUpdate(context.Background())
	return err
}

func (q *quackQuirks) Catalog() string         { return "memory" }
func (q *quackQuirks) DBSchema() string        { return "main" }
func (q *quackQuirks) Alloc() memory.Allocator { return q.alloc }

// arrowToDuckDBType is the minimal type printer needed for
// CreateSampleTable. Mirrors the inverse of arrowTypeForDuckDBName in
// metadata.go.
func arrowToDuckDBType(t arrow.DataType) string {
	switch t.ID() {
	case arrow.BOOL:
		return "BOOLEAN"
	case arrow.INT8:
		return "TINYINT"
	case arrow.INT16:
		return "SMALLINT"
	case arrow.INT32:
		return "INTEGER"
	case arrow.INT64:
		return "BIGINT"
	case arrow.UINT8:
		return "UTINYINT"
	case arrow.UINT16:
		return "USMALLINT"
	case arrow.UINT32:
		return "UINTEGER"
	case arrow.UINT64:
		return "UBIGINT"
	case arrow.FLOAT32:
		return "FLOAT"
	case arrow.FLOAT64:
		return "DOUBLE"
	case arrow.STRING:
		return "VARCHAR"
	case arrow.BINARY:
		return "BLOB"
	case arrow.DATE32:
		return "DATE"
	case arrow.TIMESTAMP:
		return "TIMESTAMP"
	}
	return "VARCHAR"
}

// TestValidation runs the apache/arrow-adbc generic conformance suite
// against a live Quack server. Skipped unless QUACK_VALIDATION_URI is
// set — see this file's header comment for usage.
func TestValidation(t *testing.T) {
	uri := os.Getenv("QUACK_VALIDATION_URI")
	if uri == "" {
		t.Skip("QUACK_VALIDATION_URI not set; skipping ADBC validation suite")
	}
	q := &quackQuirks{
		uri:   uri,
		token: os.Getenv("QUACK_VALIDATION_TOKEN"),
	}
	suite.Run(t, &validation.DatabaseTests{Quirks: q})
	suite.Run(t, &validation.ConnectionTests{Quirks: q})
	suite.Run(t, &validation.StatementTests{Quirks: q})
}
