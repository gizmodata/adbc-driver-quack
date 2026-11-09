// apache/arrow-adbc's Go validation suite against a live Quack server.
//
// The suite drives real SQL through the driver (database/connection/
// statement lifecycle, GetInfo, GetObjects, ingest, transactions,
// parameter binding, ExecuteSchema...), so it needs a DuckDB 2.0 Quack
// server:
//
//	python scripts/quack_test_server.py --port 9494 &
//	ADBC_QUACK_TEST_URI=quack://127.0.0.1:9494 \
//	    go test ./driver/quack -run TestValidation -v
//
// Without ADBC_QUACK_TEST_URI the test skips, so `go test ./...` stays
// hermetic; CI sets ADBC_QUACK_TEST_REQUIRE=1, which turns a missing
// server into a failure.
package quack

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/validation"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type quackQuirks struct {
	uri           string
	token         string
	alloc         memory.Allocator
	vendorVersion string
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

// BindParameter: the driver binds `?` markers client-side (Quack has no
// wire-level parameters).
func (q *quackQuirks) BindParameter(_ int) string { return "?" }

func (q *quackQuirks) SupportsBulkIngest(mode string) bool {
	switch mode {
	case adbc.OptionValueIngestModeCreate,
		adbc.OptionValueIngestModeAppend,
		adbc.OptionValueIngestModeReplace,
		adbc.OptionValueIngestModeCreateAppend:
		return true
	}
	return false
}

func (q *quackQuirks) SupportsConcurrentStatements() bool    { return true }
func (q *quackQuirks) SupportsCurrentCatalogSchema() bool    { return true }
func (q *quackQuirks) SupportsGetSetOptions() bool           { return true }
func (q *quackQuirks) SupportsExecuteSchema() bool           { return true }
func (q *quackQuirks) SupportsPartitionedData() bool         { return false }
func (q *quackQuirks) SupportsStatistics() bool              { return false }
func (q *quackQuirks) SupportsTransactions() bool            { return true }
func (q *quackQuirks) SupportsGetParameterSchema() bool      { return false }
func (q *quackQuirks) SupportsDynamicParameterBinding() bool { return true }

// SupportsErrorIngestIncompatibleSchema gates a subtest that expects
// StatusInternal when mode "create" meets an existing table. The ADBC spec
// says ALREADY_EXISTS, which is what this driver returns;
// TestIngestErrorStatuses covers the same cases with the spec's statuses.
func (q *quackQuirks) SupportsErrorIngestIncompatibleSchema() bool { return false }

func (q *quackQuirks) GetMetadata(code adbc.InfoCode) interface{} {
	switch code {
	case adbc.InfoVendorName:
		return "DuckDB (via Quack)"
	case adbc.InfoDriverName:
		return "ADBC Quack Driver - Go"
	case adbc.InfoDriverArrowVersion:
		return arrowGoVersion()
	case adbc.InfoDriverVersion:
		return Version
	case adbc.InfoDriverADBCVersion:
		return adbc.AdbcVersion1_1_0
	case adbc.InfoVendorVersion:
		return q.vendorVersion
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

	// CREATE — reuse the driver's own DDL builder so the conformance
	// suite exercises the exact CREATE path bulk-ingest uses.
	createSQL, err := buildCreateTableSQL("", "", tableName, r.Schema(), false, true, false)
	if err != nil {
		return err
	}
	if err := stmt.SetSqlQuery(createSQL); err != nil {
		return err
	}
	if _, err := stmt.ExecuteUpdate(context.Background()); err != nil {
		return err
	}

	// INSERT via bulk ingest (SEND_DATA): simpler than building a VALUES
	// clause, and it exercises the real path.
	if err := stmt.SetOption(adbc.OptionKeyIngestTargetTable, tableName); err != nil {
		return err
	}
	if err := stmt.SetOption(adbc.OptionKeyIngestMode, adbc.OptionValueIngestModeAppend); err != nil {
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

// TestValidation runs the apache/arrow-adbc generic conformance suite
// against a live Quack server. Skipped unless QUACK_VALIDATION_URI is
// set — see this file's header comment for usage.
func TestValidation(t *testing.T) {
	uri := os.Getenv("ADBC_QUACK_TEST_URI")
	if uri == "" {
		if os.Getenv("ADBC_QUACK_TEST_REQUIRE") != "" {
			t.Fatal("ADBC_QUACK_TEST_REQUIRE is set but ADBC_QUACK_TEST_URI is not: no Quack server to validate against")
		}
		t.Skip("ADBC_QUACK_TEST_URI not set; skipping the ADBC validation suite")
	}
	token := os.Getenv("ADBC_QUACK_TEST_TOKEN")
	if token == "" {
		token = "adbc-driver-quack-it-token"
	}
	q := &quackQuirks{uri: uri, token: token, alloc: memory.DefaultAllocator}
	q.vendorVersion = serverVersion(t, q)
	suite.Run(t, &validation.DatabaseTests{Quirks: q})
	suite.Run(t, &validation.ConnectionTests{Quirks: q})
	suite.Run(t, &validation.StatementTests{Quirks: q})
	t.Run("IngestErrorStatuses", func(t *testing.T) { ingestErrorStatuses(t, q) })
}

// ingestErrorStatuses: create on an existing table is ALREADY_EXISTS (the
// ADBC spec), append to a missing table is NOT_FOUND with SQLSTATE 42S02,
// and appending an incompatible schema fails.
func ingestErrorStatuses(t *testing.T, q *quackQuirks) {
	ctx := context.Background()
	db, err := NewDriver(q.alloc).NewDatabase(q.DatabaseOptions())
	require.NoError(t, err)
	defer db.Close()
	conn, err := db.Open(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, q.DropTable(conn, "ingest_status"))
	defer func() { _ = q.DropTable(conn, "ingest_status") }()

	one := arrow.NewSchema([]arrow.Field{{Name: "a", Type: arrow.PrimitiveTypes.Int64, Nullable: true}}, nil)
	two, _ := one.AddField(1, arrow.Field{Name: "b", Type: arrow.PrimitiveTypes.Int64, Nullable: true})
	batch := func(s *arrow.Schema) arrow.RecordBatch {
		b := array.NewRecordBuilder(q.alloc, s)
		defer b.Release()
		for i := range s.Fields() {
			b.Field(i).AppendNull()
		}
		return b.NewRecordBatch()
	}
	ingest := func(s *arrow.Schema, mode string) error {
		stmt, err := conn.NewStatement()
		require.NoError(t, err)
		defer stmt.Close()
		require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestTargetTable, "ingest_status"))
		require.NoError(t, stmt.SetOption(adbc.OptionKeyIngestMode, mode))
		rec := batch(s)
		defer rec.Release()
		require.NoError(t, stmt.Bind(ctx, rec))
		_, err = stmt.ExecuteUpdate(ctx)
		return err
	}
	var ae adbc.Error
	err = ingest(one, adbc.OptionValueIngestModeAppend)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusNotFound, ae.Code)
	require.Equal(t, [5]byte{'4', '2', 'S', '0', '2'}, ae.SqlState)

	require.NoError(t, ingest(one, adbc.OptionValueIngestModeCreate))
	err = ingest(one, adbc.OptionValueIngestModeCreate)
	require.ErrorAs(t, err, &ae)
	require.Equal(t, adbc.StatusAlreadyExists, ae.Code)

	err = ingest(two, adbc.OptionValueIngestModeAppend)
	require.ErrorAs(t, err, &ae)
	require.NotEqual(t, adbc.StatusOK, ae.Code)
	require.NoError(t, ingest(one, adbc.OptionValueIngestModeAppend), "the positive twin: a matching append works")
}

// serverVersion asks the server for its version, the value GetInfo must
// report as InfoVendorVersion.
func serverVersion(t *testing.T, q *quackQuirks) string {
	db, err := NewDriver(q.alloc).NewDatabase(q.DatabaseOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.(*connectionImpl).sess.serverVersion
}
