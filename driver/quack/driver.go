package quack

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	OptionURI   = adbc.OptionKeyURI // "adbc.uri" — the quack:// connection URL
	OptionToken = "adbc.quack.token"
	// OptionTokenEnv / OptionTokenFile name an environment variable /
	// local file to read the token from. Accepted only as ADBC options,
	// never as URL query parameters (a shared URL must not be able to
	// exfiltrate a local secret to the host it names).
	OptionTokenEnv  = "adbc.quack.token_env"
	OptionTokenFile = "adbc.quack.token_file"
	OptionTLS       = "adbc.quack.tls"
	// OptionSSLFingerprint pins the server certificate by its SHA-256
	// fingerprint instead of CA verification; implies TLS.
	OptionSSLFingerprint = "adbc.quack.ssl_fingerprint"
	// OptionHeartbeatTimeout is the connection lease, in seconds, the
	// driver asks for (default 60) and renews while idle.
	OptionHeartbeatTimeout = "adbc.quack.heartbeat_timeout_seconds"
	// OptionClientID is an opaque client identifier (empty or >= 4 chars).
	OptionClientID = "adbc.quack.client_id"
	// HTTP timeouts: plain digits are seconds, anything else is parsed
	// as a Go duration (e.g. "1.5s").
	OptionConnectTimeout = "adbc.quack.rpc.timeout_seconds.connect"
	OptionRequestTimeout = "adbc.quack.rpc.timeout_seconds.request"
	OptionIngestTable    = adbc.OptionKeyIngestTargetTable
	// OptionHTTPHeaderPrefix + <Header-Name> sets an extra HTTP header
	// sent with every request (proxy/LB auth); an empty value clears it.
	// Mirrors duckdb-quack's EXTRA_HTTP_HEADERS secret parameter. Like
	// the token_env/token_file options, accepted only as ADBC options,
	// never as URL query parameters.
	OptionHTTPHeaderPrefix = transport.HTTPHeaderPrefix
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
	db := &databaseImpl{alloc: d.alloc, opts: map[string]string{}}
	if err := db.SetOptions(opts); err != nil {
		return nil, err
	}
	return db, nil
}

// knownDatabaseOption reports whether key is a database option this
// driver reads. An unknown key is an error (ADBC: NOT_IMPLEMENTED), so a
// misspelled option fails loudly instead of being ignored.
func knownDatabaseOption(key string) bool {
	switch key {
	case OptionURI, adbc.OptionKeyUsername, adbc.OptionKeyPassword, "token", "tls":
		return true
	}
	return strings.HasPrefix(key, "adbc.quack.") || strings.HasPrefix(key, "adbc.telemetry.")
}

type databaseImpl struct {
	alloc memory.Allocator
	mu    sync.Mutex
	opts  map[string]string
}

func (d *databaseImpl) SetOptions(opts map[string]string) error {
	for k := range opts {
		if !knownDatabaseOption(k) {
			return notImplemented(k)
		}
	}
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
	// Default to ADBC's autocommit ON, matching DuckDB's own default.
	return &connectionImpl{db: d, sess: s, alloc: d.alloc, autoCommit: true}, nil
}

func (d *databaseImpl) Close() error { return nil }

type connectionImpl struct {
	db         *databaseImpl
	sess       *session
	alloc      memory.Allocator
	autoCommit bool
	txOpen     bool // true when we have issued BEGIN TRANSACTION but not yet COMMIT/ROLLBACK
	closed     bool
}

func (c *connectionImpl) Close() error {
	if c.closed {
		return errStatus(adbc.StatusInvalidState, "connection already closed")
	}
	c.closed = true
	// If a manual transaction is still open at close, roll it back rather
	// than leak it on the server.
	if c.txOpen {
		_ = c.execNoResult(context.Background(), "ROLLBACK")
		c.txOpen = false
	}
	return c.sess.close(context.Background())
}

func (c *connectionImpl) NewStatement() (adbc.Statement, error) {
	return &statementImpl{conn: c, alloc: c.alloc}, nil
}

// Read-path metadata. Implementations live in metadata_info.go +
// metadata_objects.go to keep driver.go focused on the public surface.

func (c *connectionImpl) GetInfo(ctx context.Context, codes []adbc.InfoCode) (array.RecordReader, error) {
	return c.getInfoImpl(ctx, codes)
}

func (c *connectionImpl) GetObjects(ctx context.Context, depth adbc.ObjectDepth, catalog, dbSchema, tableName *string, columnName *string, tableTypes []string) (array.RecordReader, error) {
	return c.getObjectsImpl(ctx, depth, catalog, dbSchema, tableName, columnName, tableTypes)
}

func (c *connectionImpl) GetTableSchema(ctx context.Context, catalog, dbSchema *string, tableName string) (*arrow.Schema, error) {
	return c.getTableSchemaImpl(ctx, catalog, dbSchema, tableName)
}

func (c *connectionImpl) GetTableTypes(ctx context.Context) (array.RecordReader, error) {
	return c.getTableTypesImpl(ctx)
}

// Commit ends the current manual transaction (if any) with COMMIT.
// The next statement executed with autocommit still off lazily opens a
// fresh transaction via beginTxIfNeeded, so the server never holds an
// idle transaction open between statements.
func (c *connectionImpl) Commit(ctx context.Context) error {
	if c.autoCommit {
		return errStatus(adbc.StatusInvalidState, "Commit called while autocommit is enabled")
	}
	if !c.txOpen {
		// No statement has run since the last COMMIT/ROLLBACK, so the
		// server has no transaction to commit. Be lenient.
		return nil
	}
	if err := c.execNoResult(ctx, "COMMIT"); err != nil {
		return fromTransportError(err)
	}
	c.txOpen = false
	return nil
}

// Rollback aborts the current manual transaction (if any). As with
// Commit, the next statement re-opens a transaction lazily.
func (c *connectionImpl) Rollback(ctx context.Context) error {
	if c.autoCommit {
		return errStatus(adbc.StatusInvalidState, "Rollback called while autocommit is enabled")
	}
	if !c.txOpen {
		return nil
	}
	if err := c.execNoResult(ctx, "ROLLBACK"); err != nil {
		return fromTransportError(err)
	}
	c.txOpen = false
	return nil
}

// SetOption handles connection-level options. Only autocommit toggling
// has a side effect today; other keys are recognized but otherwise no-op.
func (c *connectionImpl) SetOption(key, value string) error {
	switch key {
	case adbc.OptionKeyAutoCommit:
		switch value {
		case adbc.OptionValueEnabled:
			if c.autoCommit {
				return nil
			}
			// Disabling -> enabling: commit any pending tx, then go autocommit.
			if c.txOpen {
				if err := c.execNoResult(context.Background(), "COMMIT"); err != nil {
					return fromTransportError(err)
				}
				c.txOpen = false
			}
			c.autoCommit = true
			return nil
		case adbc.OptionValueDisabled:
			if !c.autoCommit {
				return nil
			}
			// The server-side BEGIN is deferred until the first statement
			// actually executes (beginTxIfNeeded), so a connection that
			// merely disabled autocommit doesn't pin an idle transaction.
			c.autoCommit = false
			return nil
		default:
			return errStatus(adbc.StatusInvalidArgument,
				"unknown value %q for %s; expected %q or %q",
				value, key, adbc.OptionValueEnabled, adbc.OptionValueDisabled)
		}
	case adbc.OptionKeyCurrentCatalog, adbc.OptionKeyCurrentDbSchema:
		return c.useCatalogSchema(key, value)
	}
	return notImplemented(key)
}

func (c *connectionImpl) ReadPartition(context.Context, []byte) (array.RecordReader, error) {
	return nil, errStatus(adbc.StatusNotImplemented, "ReadPartition")
}

// beginTxIfNeeded lazily opens a server-side transaction before the
// first statement executed in manual-commit mode. The server stays in
// auto-commit until a statement actually runs, so Commit with nothing
// pending is a harmless no-op instead of a server error ("cannot
// commit - no transaction is active").
func (c *connectionImpl) beginTxIfNeeded(ctx context.Context) error {
	if c.autoCommit || c.txOpen {
		return nil
	}
	if err := c.execNoResult(ctx, "BEGIN TRANSACTION"); err != nil {
		return fromTransportError(err)
	}
	c.txOpen = true
	return nil
}

// execNoResult runs a side-effectful SQL statement (BEGIN/COMMIT/ROLLBACK)
// and discards the result chunks.
func (c *connectionImpl) execNoResult(ctx context.Context, sql string) error {
	_, err := c.sess.drainPrepared(ctx, sql)
	return err
}

// statementImpl implements adbc.Statement.
type statementImpl struct {
	conn          *connectionImpl
	alloc         memory.Allocator
	sql           string
	closed        bool
	targetTable   string
	targetSchema  string
	targetCatalog string
	// ingestMode is one of adbc.OptionValueIngestMode*. Empty means the
	// ADBC default, which is "create" (create the table, then append).
	ingestMode      string
	ingestTemporary bool
	bound           arrow.Record
	boundStream     array.RecordReader
}

func (s *statementImpl) Close() error {
	if s.closed {
		return errStatus(adbc.StatusInvalidState, "statement already closed")
	}
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
	case adbc.OptionValueIngestTargetCatalog:
		s.targetCatalog = value
	case adbc.OptionKeyIngestMode:
		switch value {
		case adbc.OptionValueIngestModeCreate,
			adbc.OptionValueIngestModeAppend,
			adbc.OptionValueIngestModeReplace,
			adbc.OptionValueIngestModeCreateAppend:
			s.ingestMode = value
		default:
			return errStatus(adbc.StatusInvalidArgument, "unknown ingest mode %q", value)
		}
	case adbc.OptionValueIngestTemporary:
		switch value {
		case adbc.OptionValueEnabled:
			s.ingestTemporary = true
		case adbc.OptionValueDisabled:
			s.ingestTemporary = false
		default:
			return errStatus(adbc.StatusInvalidArgument, "invalid value %q for %s (want %q or %q)",
				value, key, adbc.OptionValueEnabled, adbc.OptionValueDisabled)
		}
	default:
		return notImplemented(key)
	}
	return nil
}
func (s *statementImpl) SetSubstraitPlan([]byte) error {
	return errStatus(adbc.StatusNotImplemented, "Substrait")
}
func (s *statementImpl) Prepare(_ context.Context) error { return nil }

func (s *statementImpl) ExecuteQuery(ctx context.Context) (array.RecordReader, int64, error) {
	if s.sql == "" && (s.targetTable == "" || (s.bound == nil && s.boundStream == nil)) {
		return nil, -1, errStatus(adbc.StatusInvalidState, "Statement.ExecuteQuery: no SQL set")
	}
	if err := s.conn.beginTxIfNeeded(ctx); err != nil {
		return nil, -1, err
	}
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		// Bulk ingest through ExecuteQuery: no result stream, just the count.
		n, err := s.executeIngest(ctx)
		return nil, n, err
	}
	if s.bound != nil || s.boundStream != nil {
		return s.executeBoundQuery(ctx)
	}
	cur, err := s.conn.sess.cursor(ctx, s.sql)
	if err != nil {
		return nil, -1, fromTransportError(err)
	}
	// A DML statement's whole result (one "Count" row) arrives with
	// PREPARE: report it as rows affected. For a query the count is
	// unknown until the reader is drained; ADBC allows -1 for that.
	affected := int64(-1)
	if !cur.needsMore && isDMLStatement(s.sql) {
		affected = countFromChunks(cur.columnNamesSlice(), cur.buffered)
	}
	reader, err := newStreamingRecordReader(ctx, s.alloc, cur)
	if err != nil {
		cur.close()
		return nil, -1, errStatus(adbc.StatusInternal, "newStreamingRecordReader: %v", err)
	}
	return reader, affected, nil
}

func (s *statementImpl) ExecuteUpdate(ctx context.Context) (int64, error) {
	if err := s.conn.beginTxIfNeeded(ctx); err != nil {
		return -1, err
	}
	// Bulk-ingest path: when a target table is set and there's bound data,
	// route to executeIngest instead of running a SQL update.
	if s.targetTable != "" && (s.bound != nil || s.boundStream != nil) {
		return s.executeIngest(ctx)
	}
	if s.sql == "" {
		return -1, errStatus(adbc.StatusInvalidState, "Statement.ExecuteUpdate: no SQL set")
	}
	if s.bound != nil || s.boundStream != nil {
		return s.executeBoundUpdate(ctx)
	}
	// DDL/DML results are small (one Count row at most), so drain eagerly.
	result, err := s.conn.sess.drainPrepared(ctx, s.sql)
	if err != nil {
		return -1, fromTransportError(err)
	}
	return countFromChunks(result.ColumnNames, result.Chunks), nil
}

// ExecuteSchema returns the result schema without running the query
// (ADBC 1.1 StatementExecuteSchema; adbc_scanner's adbc_scan needs it).
// Quack has no describe message, so the driver asks for
// `SELECT * FROM (<query>) LIMIT 0`: DuckDB plans that to an empty result,
// so none of the query's work runs, and the reply still carries the exact
// column types. Parameter markers bind as NULL.
func (s *statementImpl) ExecuteSchema(ctx context.Context) (*arrow.Schema, error) {
	if s.sql == "" {
		return nil, errStatus(adbc.StatusInvalidState, "Statement.ExecuteSchema: no SQL set")
	}
	inner, err := replaceParamMarkers(s.sql, func() (string, error) { return "NULL", nil })
	if err != nil {
		return nil, errStatus(adbc.StatusInvalidArgument, "%v", err)
	}
	inner = strings.TrimRight(strings.TrimSpace(inner), ";")
	if err := s.conn.beginTxIfNeeded(ctx); err != nil {
		return nil, err
	}
	result, err := s.conn.sess.drainPrepared(ctx, "SELECT * FROM ("+inner+"\n) LIMIT 0")
	if err != nil {
		var se *transport.ErrServerError
		if errors.As(err, &se) && (se.ExceptionType == "Parser" || strings.Contains(se.Message, "syntax error")) {
			return nil, errStatus(adbc.StatusNotImplemented,
				"ExecuteSchema: the statement is not a query, so it has no result schema to describe")
		}
		return nil, fromTransportError(err)
	}
	return SchemaFromColumns(result.ColumnNames, result.ColumnTypes), nil
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

// buildRecordReader is retained for callers (currently none) that have a
// fully-drained result and want a RecordReader. The hot path now uses
// newStreamingRecordReader via cursor.go to avoid materializing all
// chunks up front.
func buildRecordReader(alloc memory.Allocator, result *drainedResult) (int64, array.RecordReader, error) {
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

// Compile-time interface checks.
var (
	_ adbc.Driver                 = (*driverImpl)(nil)
	_ adbc.Database               = (*databaseImpl)(nil)
	_ adbc.Connection             = (*connectionImpl)(nil)
	_ adbc.Statement              = (*statementImpl)(nil)
	_ adbc.StatementExecuteSchema = (*statementImpl)(nil)
	_ message.QuackMessage        = message.ConnectionRequest{} // keep imports satisfied
)
