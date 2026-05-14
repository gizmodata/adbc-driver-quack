package quack

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// ADBC option keys used by the Quack driver. They follow the
// `adbc.<vendor>.<noun>` convention used by adbc-driver-flightsql.
const (
	OptionURI         = adbc.OptionKeyURI // "adbc.uri" — the quack:// connection URL
	OptionToken       = "adbc.quack.token"
	OptionTLS         = "adbc.quack.tls"
	OptionIngestTable = adbc.OptionKeyIngestTargetTable
)

// NewDriver returns a quack ADBC driver.
func NewDriver(alloc memory.Allocator) adbc.Driver {
	if alloc == nil {
		alloc = memory.NewGoAllocator()
	}
	return &driverImpl{alloc: alloc}
}

type driverImpl struct {
	alloc memory.Allocator
}

func (d *driverImpl) NewDatabase(opts map[string]string) (adbc.Database, error) {
	return d.NewDatabaseWithContext(context.Background(), opts)
}

func (d *driverImpl) NewDatabaseWithContext(_ context.Context, opts map[string]string) (adbc.Database, error) {
	db := &databaseImpl{
		alloc: d.alloc,
		opts:  cloneMap(opts),
	}
	return db, nil
}

type databaseImpl struct {
	alloc memory.Allocator
	mu    sync.Mutex
	opts  map[string]string
}

func (d *databaseImpl) SetOptions(opts map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, v := range opts {
		d.opts[k] = v
	}
	return nil
}

func (d *databaseImpl) Open(ctx context.Context) (adbc.Connection, error) {
	d.mu.Lock()
	rawURL := d.opts[OptionURI]
	overrides := cloneMap(d.opts)
	delete(overrides, OptionURI)
	d.mu.Unlock()
	if rawURL == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "quack: missing required option %q", OptionURI)
	}
	uri, err := transport.ParseURI(rawURL, overrides)
	if err != nil {
		return nil, errStatus(adbc.StatusInvalidArgument, "quack: %v", err)
	}
	s, err := newSession(ctx, uri)
	if err != nil {
		return nil, fromTransportError(err)
	}
	return &connectionImpl{db: d, sess: s, alloc: d.alloc}, nil
}

func (d *databaseImpl) Close() error { return nil }

type connectionImpl struct {
	db    *databaseImpl
	sess  *session
	alloc memory.Allocator
}

func (c *connectionImpl) Close() error {
	return c.sess.close(context.Background())
}

func (c *connectionImpl) NewStatement() (adbc.Statement, error) {
	return &statementImpl{conn: c, alloc: c.alloc}, nil
}

// Defaulted implementations for read-path. Metadata methods are
// stubbed; future commits port DuckDB JDBC's queries here.

func (c *connectionImpl) GetInfo(_ context.Context, codes []adbc.InfoCode) (array.RecordReader, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "GetInfo")
}

func (c *connectionImpl) GetObjects(_ context.Context, depth adbc.ObjectDepth, catalog, dbSchema, tableName *string, columnName *string, tableTypes []string) (array.RecordReader, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "GetObjects")
}

func (c *connectionImpl) GetTableSchema(ctx context.Context, catalog, dbSchema *string, tableName string) (*arrow.Schema, error) {
	return c.getTableSchemaImpl(ctx, catalog, dbSchema, tableName)
}

func (c *connectionImpl) GetTableTypes(ctx context.Context) (array.RecordReader, error) {
	return c.getTableTypesImpl(ctx)
}

func (c *connectionImpl) Commit(_ context.Context) error                            { return nil }
func (c *connectionImpl) Rollback(_ context.Context) error                          { return nil }
func (c *connectionImpl) SetOption(string, string) error                            { return nil }
func (c *connectionImpl) ReadPartition(context.Context, []byte) (array.RecordReader, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "ReadPartition")
}

// statementImpl implements adbc.Statement.
type statementImpl struct {
	conn         *connectionImpl
	alloc        memory.Allocator
	sql          string
	closed       bool
	targetTable  string
	targetSchema string
	bound        arrow.Record
	boundStream  array.RecordReader
}

func (s *statementImpl) Close() error {
	s.closed = true
	s.clearBound()
	return nil
}

func (s *statementImpl) SetSqlQuery(sql string) error { s.sql = sql; return nil }
func (s *statementImpl) SetOption(key, value string) error {
	switch key {
	case adbc.OptionKeyIngestTargetTable:
		s.targetTable = value
	case adbc.OptionValueIngestTargetDBSchema:
		s.targetSchema = value
	}
	return nil
}
func (s *statementImpl) SetSubstraitPlan([]byte) error {
	return errStatus(adbc.StatusNotImplemented, "Substrait")
}
func (s *statementImpl) Prepare(_ context.Context) error { return nil }

func (s *statementImpl) ExecuteQuery(ctx context.Context) (array.RecordReader, int64, error) {
	if s.sql == "" {
		return nil, -1, errStatus(adbc.StatusInvalidState, "Statement.ExecuteQuery: no SQL set")
	}
	result, err := s.conn.sess.prepare(ctx, s.sql)
	if err != nil {
		return nil, -1, fromTransportError(err)
	}
	rows, reader, err := buildRecordReader(s.alloc, result)
	if err != nil {
		return nil, -1, errStatus(adbc.StatusInternal, "buildRecordReader: %v", err)
	}
	return reader, rows, nil
}

func (s *statementImpl) ExecuteUpdate(ctx context.Context) (int64, error) {
	// Bulk-ingest path: when a target table is set and there's bound data,
	// route to executeIngest instead of running a SQL update.
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		return s.executeIngest(ctx)
	}
	if s.sql == "" {
		return -1, errStatus(adbc.StatusInvalidState, "Statement.ExecuteUpdate: no SQL set")
	}
	result, err := s.conn.sess.prepare(ctx, s.sql)
	if err != nil {
		return -1, fromTransportError(err)
	}
	var rows int64
	for _, c := range result.Chunks {
		rows += int64(c.RowCount)
	}
	return rows, nil
}

func (s *statementImpl) GetParameterSchema() (*arrow.Schema, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "GetParameterSchema")
}

func (s *statementImpl) Bind(_ context.Context, rec arrow.Record) error {
	s.bindBatch(rec)
	return nil
}

func (s *statementImpl) BindStream(_ context.Context, rr array.RecordReader) error {
	s.bindStream(rr)
	return nil
}

func (s *statementImpl) ExecutePartitions(context.Context) (*arrow.Schema, adbc.Partitions, int64, error) {
	return nil, adbc.Partitions{}, -1, errStatus(adbc.StatusNotImplemented, "ExecutePartitions")
}

func buildRecordReader(alloc memory.Allocator, result *PreparedResult) (int64, array.RecordReader, error) {
	if alloc == nil {
		alloc = memory.NewGoAllocator()
	}
	schema := SchemaFromColumns(result.ColumnNames, result.ColumnTypes)
	records := make([]arrow.Record, 0, len(result.Chunks))
	var rows int64
	for _, chunk := range result.Chunks {
		if chunk.RowCount == 0 {
			continue
		}
		rec, err := RecordFromChunk(alloc, result.ColumnNames, chunk)
		if err != nil {
			for _, r := range records {
				r.Release()
			}
			return 0, nil, err
		}
		records = append(records, rec)
		rows += int64(chunk.RowCount)
	}
	rr, err := array.NewRecordReader(schema, records)
	if err != nil {
		for _, r := range records {
			r.Release()
		}
		return 0, nil, err
	}
	for _, r := range records {
		r.Release()
	}
	return rows, rr, nil
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func errStatus(code adbc.Status, format string, args ...interface{}) error {
	return adbc.Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

func fromTransportError(err error) error {
	if err == nil {
		return nil
	}
	var se *transport.ErrServerError
	if errors.As(err, &se) {
		return adbc.Error{Code: adbc.StatusInternal, Msg: se.Message}
	}
	return adbc.Error{Code: adbc.StatusIO, Msg: err.Error()}
}

// Compile-time interface checks.
var (
	_ adbc.Driver     = (*driverImpl)(nil)
	_ adbc.Database   = (*databaseImpl)(nil)
	_ adbc.Connection = (*connectionImpl)(nil)
	_ adbc.Statement  = (*statementImpl)(nil)
	_ message.QuackMessage = message.ConnectionRequest{} // keep imports satisfied
)
