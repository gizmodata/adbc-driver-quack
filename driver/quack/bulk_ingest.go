package quack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// Bulk-ingest state lives on statementImpl. ADBC's standard pattern is:
//   stmt.SetOption(ADBC_INGEST_OPTION_TARGET_TABLE, "tbl")
//   stmt.BindStream(ctx, reader)   // or stmt.Bind(ctx, batch)
//   stmt.ExecuteUpdate(ctx)         // dispatches to executeIngest

// extendStatementImpl adds the missing ingest fields. Implemented as
// extra methods on statementImpl in this file to keep driver.go's
// public surface readable.

// We keep the bound batch/reader on the same statementImpl struct. The
// fields are defined here via a method so the import graph stays clean.

func (s *statementImpl) bindBatch(rec arrow.Record) {
	rec.Retain()
	if s.bound != nil {
		s.bound.Release()
	}
	s.bound = rec
}

func (s *statementImpl) bindStream(rr array.RecordReader) {
	rr.Retain()
	if s.boundStream != nil {
		s.boundStream.Release()
	}
	s.boundStream = rr
}

func (s *statementImpl) clearBound() {
	if s.bound != nil {
		s.bound.Release()
		s.bound = nil
	}
	if s.boundStream != nil {
		s.boundStream.Release()
		s.boundStream = nil
	}
}

func (s *statementImpl) executeIngest(ctx context.Context) (int64, error) {
	if s.targetTable == "" {
		return -1, errStatus(adbc.StatusInvalidState, "ingest: no target table set")
	}
	if s.bound == nil && s.boundStream == nil {
		return -1, errStatus(adbc.StatusInvalidState, "ingest: no record/stream bound")
	}
	defer s.clearBound()

	var schema *arrow.Schema
	if s.bound != nil {
		schema = s.bound.Schema()
	} else {
		schema = s.boundStream.Schema()
	}
	cols := make([]ingestColumn, schema.NumFields())
	for i, f := range schema.Fields() {
		c, err := duckColumnForField(f)
		if err != nil {
			return -1, errStatus(adbc.StatusNotImplemented, "ingest: column %q: %v", f.Name, err)
		}
		cols[i] = c
	}
	target, err := s.resolveIngestTarget(ctx)
	if err != nil {
		return -1, err
	}
	if err := s.prepareIngestTarget(ctx, schema, target); err != nil {
		return -1, err
	}
	return s.streamIngest(ctx, cols, target)
}

// ingestBatchBytes is the target size of one SEND_DATA request: several
// 2048-row chunks per round trip, small enough to stay well inside any
// proxy's body limit.
const ingestBatchBytes = 4 << 20

// streamIngest runs
//
//	INSERT INTO <target> SELECT * FROM scan_data_from_quack_client(<stream>, NULL::STRUCT(...), ordered := true)
//
// and streams the bound rows into it with SEND_DATA, the way DuckDB's own
// quack client inserts into an attached table. The statement is planned
// before any data moves (PREPARE with inline_rows = 0), the terminal
// SEND_DATA carries the batch count so a lost batch fails the statement,
// and the inserted row count comes back as the statement's result.
func (s *statementImpl) streamIngest(ctx context.Context, cols []ingestColumn, target ingestTarget) (int64, error) {
	sess := s.conn.sess
	streamID := newStreamID()
	proto := make([]string, len(cols))
	for i, c := range cols {
		proto[i] = fmt.Sprintf("col%d %s", i, c.sql)
	}
	insert := fmt.Sprintf("INSERT INTO %s SELECT * FROM scan_data_from_quack_client(%s, NULL::STRUCT(%s), ordered := true)",
		target.name(), quoteLiteral(streamID), strings.Join(proto, ", "))
	cur, err := sess.prepare(ctx, insert, message.SomeIndex(0))
	if err != nil {
		return -1, ingestError(err)
	}
	defer cur.close()

	fail := func(err error) (int64, error) {
		// Release the server-side statement promptly: it is waiting for
		// batches that will never come.
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = sess.cancelQuery(cctx, cur.queryUUID)
		cancel()
		return -1, err
	}

	var (
		pending      []message.DataChunk
		pendingBytes int64
		batches      uint64
	)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		batches++
		err := sess.sendData(ctx, streamID, message.SomeIndex(batches), message.OptionalIndex{}, pending)
		pending, pendingBytes = nil, 0
		return err
	}
	pump := func(rec arrow.Record) error {
		chunks, size, err := encodeRecordChunks(rec, cols)
		if err != nil {
			return errStatus(adbc.StatusInvalidArgument, "ingest: %v", err)
		}
		for i := range chunks {
			pending = append(pending, chunks[i])
			pendingBytes += int64(len(chunks[i].Raw))
			if pendingBytes >= ingestBatchBytes {
				if err := flush(); err != nil {
					return ingestError(err)
				}
			}
		}
		_ = size
		return nil
	}
	if s.bound != nil {
		if err := pump(s.bound); err != nil {
			return fail(err)
		}
	}
	if s.boundStream != nil {
		for s.boundStream.Next() {
			if err := pump(s.boundStream.Record()); err != nil {
				return fail(err)
			}
		}
		if err := s.boundStream.Err(); err != nil {
			return fail(errStatus(adbc.StatusIO, "ingest stream: %v", err))
		}
	}
	if err := flush(); err != nil {
		return fail(ingestError(err))
	}
	if err := sess.sendData(ctx, streamID, message.OptionalIndex{}, message.SomeIndex(batches), nil); err != nil {
		return fail(ingestError(err))
	}
	chunks, err := cur.drainAll(ctx)
	if err != nil {
		return -1, ingestError(err)
	}
	// PREPARE answered before the INSERT bound its result (it was waiting
	// for our batches), so the cursor has no column names: the result is
	// DuckDB's one-row "Count".
	names := cur.columnNamesSlice()
	if len(names) == 0 {
		names = []string{"Count"}
	}
	return countFromChunks(names, chunks), nil
}

// ingestError maps a failure of the ingest INSERT. A Binder error there
// means the table exists (a missing table is a Catalog error, NOT_FOUND)
// but cannot take the bound columns, which ADBC reports as ALREADY_EXISTS.
// The server can raise it from PREPARE, a SEND_DATA or the final FETCH:
// PREPARE answers as soon as the client-data scan binds, before the INSERT
// checks its columns.
func ingestError(err error) error {
	var se *transport.ErrServerError
	if errors.As(err, &se) && strings.EqualFold(se.ExceptionType, "Binder") {
		return adbc.Error{Code: adbc.StatusAlreadyExists, SqlState: sqlState("42S01"),
			Msg: "ingest: the bound data does not match the existing table: " + se.Error()}
	}
	return fromTransportError(err)
}

// ingestTarget is the fully qualified table an ingest writes.
type ingestTarget struct {
	catalog, schema, table string
	temporary              bool
}

func (t ingestTarget) name() string {
	return quoteIdent(t.catalog) + "." + quoteIdent(t.schema) + "." + quoteIdent(t.table)
}

// resolveIngestTarget qualifies the target fully. An unqualified name is
// ambiguous in DuckDB once a temporary table of the same name exists (it
// resolves to the temporary one), so a non-temporary ingest names the
// session's current catalog and schema, and a temporary one temp.main.
func (s *statementImpl) resolveIngestTarget(ctx context.Context) (ingestTarget, error) {
	t := ingestTarget{catalog: s.targetCatalog, schema: s.targetSchema, table: s.targetTable, temporary: s.ingestTemporary}
	if t.temporary {
		if t.catalog != "" || t.schema != "" {
			return t, errStatus(adbc.StatusInvalidArgument,
				"ingest: a temporary table cannot have a target catalog or schema (DuckDB keeps it in temp.main)")
		}
		t.catalog, t.schema = "temp", "main"
		return t, nil
	}
	if t.catalog != "" && t.schema != "" {
		return t, nil
	}
	result, err := s.conn.sess.drainPrepared(ctx, "SELECT current_database(), current_schema()")
	if err != nil {
		return t, fromTransportError(err)
	}
	for _, ch := range result.Chunks {
		if ch.RowCount == 1 && len(ch.Columns) == 2 {
			if t.catalog == "" {
				t.catalog, _ = ch.Columns[0].GetObject(0).(string)
			}
			if t.schema == "" {
				t.schema, _ = ch.Columns[1].GetObject(0).(string)
			}
		}
	}
	if t.catalog == "" || t.schema == "" {
		return t, errStatus(adbc.StatusInternal, "ingest: could not resolve the current catalog and schema")
	}
	return t, nil
}

func newStreamID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isDMLStatement reports whether sql (after leading comments and
// whitespace, and a WITH clause) is INSERT/UPDATE/DELETE/MERGE, whose
// one-row "Count" result is a rows-affected count rather than data.
func isDMLStatement(sql string) bool {
	s := strings.TrimSpace(sql)
	for {
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return false
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimSpace(s[i+2:])
				continue
			}
			return false
		}
		break
	}
	upper := strings.ToUpper(s)
	for _, kw := range []string{"INSERT", "UPDATE", "DELETE", "MERGE"} {
		if strings.HasPrefix(upper, kw) && (len(upper) == len(kw) || !isIdentChar(upper[len(kw)])) {
			return true
		}
	}
	if strings.HasPrefix(upper, "WITH") {
		// WITH ... INSERT/UPDATE/DELETE: look for the DML keyword after the CTEs.
		for _, kw := range []string{") INSERT", ") UPDATE", ") DELETE", ") MERGE", ")INSERT", ")UPDATE", ")DELETE", ")MERGE"} {
			if strings.Contains(upper, kw) {
				return true
			}
		}
	}
	return false
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// countFromChunks reads the rows-affected count from a DML result: DuckDB
// answers INSERT/UPDATE/DELETE with one BIGINT column named "Count".
// Anything else (DDL, a query, RETURNING rows) reports -1, "unknown", per
// ADBC.
func countFromChunks(names []string, chunks []message.DataChunk) int64 {
	if len(names) != 1 || !strings.EqualFold(names[0], "Count") {
		return -1
	}
	var total int64
	seen := false
	for _, c := range chunks {
		if c.RowCount == 0 {
			continue
		}
		if len(c.Columns) != 1 {
			return -1
		}
		for r := 0; r < c.RowCount; r++ {
			switch v := c.Columns[0].GetObject(r).(type) {
			case int64:
				total += v
			case int32:
				total += int64(v)
			case uint64:
				total += int64(v)
			default:
				return -1
			}
			seen = true
		}
	}
	if !seen {
		return -1
	}
	return total
}

// prepareIngestTarget runs the table DDL (if any) implied by the ingest
// mode before the rows stream, mirroring DuckDB's own ADBC driver:
//
//	create        → CREATE TABLE              (error if it exists)
//	append        → no DDL                    (append fails if missing)
//	replace       → CREATE OR REPLACE TABLE   (drop + recreate)
//	create_append → CREATE TABLE IF NOT EXISTS (create only if missing)
//
// The ADBC default when no mode is set is "create".
func (s *statementImpl) prepareIngestTarget(ctx context.Context, schema *arrow.Schema, target ingestTarget) error {
	mode := s.ingestMode
	if mode == "" {
		mode = adbc.OptionValueIngestModeCreate
	}
	if mode == adbc.OptionValueIngestModeAppend {
		return nil
	}
	catalog, dbSchema := target.catalog, target.schema
	if target.temporary {
		catalog, dbSchema = "", "" // CREATE TEMPORARY TABLE puts it in temp.main
	}
	ddl, err := buildCreateTableSQL(
		catalog, dbSchema, target.table, schema,
		mode == adbc.OptionValueIngestModeCreateAppend, // IF NOT EXISTS
		mode == adbc.OptionValueIngestModeReplace,      // OR REPLACE
		target.temporary,
	)
	if err != nil {
		return errStatus(adbc.StatusInvalidArgument, "ingest: %v", err)
	}
	if _, err := s.conn.sess.drainPrepared(ctx, ddl); err != nil {
		return fromTransportError(err)
	}
	return nil
}

// buildCreateTableSQL renders a DuckDB CREATE TABLE statement from an
// Arrow schema. orReplace and ifNotExists are mutually exclusive (the
// ingest modes never set both); DuckDB rejects the combination anyway.
func buildCreateTableSQL(catalog, schema, table string, s *arrow.Schema, ifNotExists, orReplace, temporary bool) (string, error) {
	if table == "" {
		return "", fmt.Errorf("no target table set")
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if orReplace {
		b.WriteString("OR REPLACE ")
	}
	if temporary {
		b.WriteString("TEMPORARY ")
	}
	b.WriteString("TABLE ")
	if ifNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	if catalog != "" && !temporary {
		b.WriteString(quoteIdent(catalog))
		b.WriteByte('.')
	}
	if schema != "" && !temporary {
		b.WriteString(quoteIdent(schema))
		b.WriteByte('.')
	}
	b.WriteString(quoteIdent(table))
	b.WriteString(" (")
	for i, f := range s.Fields() {
		if i > 0 {
			b.WriteString(", ")
		}
		col, err := duckColumnForField(f)
		if err != nil {
			return "", fmt.Errorf("column %q: %w", f.Name, err)
		}
		colType := col.sql
		b.WriteString(quoteIdent(f.Name))
		b.WriteByte(' ')
		b.WriteString(colType)
		if !f.Nullable {
			b.WriteString(" NOT NULL")
		}
	}
	b.WriteString(")")
	return b.String(), nil
}

// quoteIdent double-quotes a SQL identifier, escaping embedded quotes so
// table/column names with spaces, keywords, or quotes are handled safely.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// duckDBTypeForArrow is the SQL type a CREATE TABLE gives an Arrow column.
func duckDBTypeForArrow(dt arrow.DataType) (string, error) {
	c, err := duckColumnForArrow(dt)
	if err != nil {
		return "", err
	}
	return c.sql, nil
}
