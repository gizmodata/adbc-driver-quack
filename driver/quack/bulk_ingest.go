package quack

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
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

	// The bound schema is needed up front for the CREATE-family modes so
	// the table DDL is built before the first APPEND_REQUEST.
	var ddlSchema *arrow.Schema
	if s.bound != nil {
		ddlSchema = s.bound.Schema()
	} else {
		ddlSchema = s.boundStream.Schema()
	}
	if err := s.prepareIngestTarget(ctx, ddlSchema); err != nil {
		return -1, err
	}

	var total int64
	pump := func(rec arrow.Record) error {
		chunk, err := chunkFromRecord(rec)
		if err != nil {
			return err
		}
		if err := s.conn.sess.appendChunk(ctx, s.targetSchema, s.targetTable, chunk); err != nil {
			return fromTransportError(err)
		}
		total += rec.NumRows()
		return nil
	}

	if s.bound != nil {
		if err := pump(s.bound); err != nil {
			return -1, err
		}
	}
	if s.boundStream != nil {
		for s.boundStream.Next() {
			if err := pump(s.boundStream.Record()); err != nil {
				return -1, err
			}
		}
		if err := s.boundStream.Err(); err != nil {
			return -1, errStatus(adbc.StatusIO, "ingest stream: %v", err)
		}
	}
	return total, nil
}

// prepareIngestTarget runs the table DDL (if any) implied by the ingest
// mode before the append loop, mirroring DuckDB's own ADBC driver:
//
//	create        → CREATE TABLE              (error if it exists)
//	append        → no DDL                    (append fails if missing)
//	replace       → CREATE OR REPLACE TABLE   (drop + recreate)
//	create_append → CREATE TABLE IF NOT EXISTS (create only if missing)
//
// The ADBC default when no mode is set is "create".
func (s *statementImpl) prepareIngestTarget(ctx context.Context, schema *arrow.Schema) error {
	mode := s.ingestMode
	if mode == "" {
		mode = adbc.OptionValueIngestModeCreate
	}
	if mode == adbc.OptionValueIngestModeAppend {
		return nil
	}
	ddl, err := buildCreateTableSQL(
		s.targetSchema, s.targetTable, schema,
		mode == adbc.OptionValueIngestModeCreateAppend, // IF NOT EXISTS
		mode == adbc.OptionValueIngestModeReplace,      // OR REPLACE
		s.ingestTemporary,
	)
	if err != nil {
		return errStatus(adbc.StatusInvalidArgument, "ingest: %v", err)
	}
	if _, err := s.conn.sess.drainPrepared(ctx, ddl); err != nil {
		// "create" against an existing table is ALREADY_EXISTS per the
		// ADBC contract; every other DDL failure is reported as-is.
		if mode == adbc.OptionValueIngestModeCreate {
			var se *transport.ErrServerError
			if errors.As(err, &se) && strings.Contains(strings.ToLower(se.Message), "already exists") {
				return adbc.Error{Code: adbc.StatusAlreadyExists, Msg: se.Message}
			}
		}
		return fromTransportError(err)
	}
	return nil
}

// buildCreateTableSQL renders a DuckDB CREATE TABLE statement from an
// Arrow schema. orReplace and ifNotExists are mutually exclusive (the
// ingest modes never set both); DuckDB rejects the combination anyway.
func buildCreateTableSQL(schema, table string, s *arrow.Schema, ifNotExists, orReplace, temporary bool) (string, error) {
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
	if schema != "" {
		b.WriteString(quoteIdent(schema))
		b.WriteByte('.')
	}
	b.WriteString(quoteIdent(table))
	b.WriteString(" (")
	for i, f := range s.Fields() {
		if i > 0 {
			b.WriteString(", ")
		}
		colType, err := duckDBTypeForArrow(f.Type)
		if err != nil {
			return "", fmt.Errorf("column %q: %w", f.Name, err)
		}
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

// duckDBTypeForArrow is the SQL-type printer for CREATE TABLE DDL. It
// covers exactly the Arrow types logicalTypeForArrow accepts on the
// encoder side, so a creatable schema is always an ingestable one.
func duckDBTypeForArrow(dt arrow.DataType) (string, error) {
	switch dt.ID() {
	case arrow.BOOL:
		return "BOOLEAN", nil
	case arrow.INT8:
		return "TINYINT", nil
	case arrow.INT16:
		return "SMALLINT", nil
	case arrow.INT32:
		return "INTEGER", nil
	case arrow.INT64:
		return "BIGINT", nil
	case arrow.UINT8:
		return "UTINYINT", nil
	case arrow.UINT16:
		return "USMALLINT", nil
	case arrow.UINT32:
		return "UINTEGER", nil
	case arrow.UINT64:
		return "UBIGINT", nil
	case arrow.FLOAT32:
		return "FLOAT", nil
	case arrow.FLOAT64:
		return "DOUBLE", nil
	case arrow.STRING, arrow.LARGE_STRING:
		return "VARCHAR", nil
	case arrow.BINARY, arrow.LARGE_BINARY:
		return "BLOB", nil
	case arrow.DATE32:
		return "DATE", nil
	case arrow.TIMESTAMP:
		if dt.(*arrow.TimestampType).TimeZone != "" {
			return "TIMESTAMP WITH TIME ZONE", nil
		}
		return "TIMESTAMP", nil
	case arrow.DECIMAL128:
		d := dt.(*arrow.Decimal128Type)
		return fmt.Sprintf("DECIMAL(%d, %d)", d.Precision, d.Scale), nil
	}
	return "", fmt.Errorf("unsupported arrow type %s for ingest", dt)
}

// chunkFromRecord converts an arrow.Record into a Quack DataChunk.
//
// Only scalar primitive + string + binary types are supported in the
// initial pass. Decimal128 and Date32 and Timestamp_us are also
// implemented because they are common in bulk-load workloads. Nested
// types are not yet supported on the encoder side.
func chunkFromRecord(rec arrow.Record) (message.DataChunk, error) {
	schema := rec.Schema()
	rowCount := int(rec.NumRows())
	types := make([]quacktype.LogicalType, schema.NumFields())
	cols := make([]message.DecodedVector, schema.NumFields())
	for i := 0; i < schema.NumFields(); i++ {
		field := schema.Field(i)
		t, err := logicalTypeForArrow(field.Type)
		if err != nil {
			return message.DataChunk{}, fmt.Errorf("column %q: %w", field.Name, err)
		}
		types[i] = t
		v, err := decodedVectorForArrow(t, rec.Column(i), rowCount)
		if err != nil {
			return message.DataChunk{}, fmt.Errorf("column %q: %w", field.Name, err)
		}
		cols[i] = v
	}
	return message.DataChunk{RowCount: rowCount, Types: types, Columns: cols}, nil
}

func logicalTypeForArrow(dt arrow.DataType) (quacktype.LogicalType, error) {
	switch dt.ID() {
	case arrow.BOOL:
		return quacktype.Of(quacktype.LogicalTypeIDBoolean), nil
	case arrow.INT8:
		return quacktype.Of(quacktype.LogicalTypeIDTinyInt), nil
	case arrow.INT16:
		return quacktype.Of(quacktype.LogicalTypeIDSmallInt), nil
	case arrow.INT32:
		return quacktype.Of(quacktype.LogicalTypeIDInteger), nil
	case arrow.INT64:
		return quacktype.Of(quacktype.LogicalTypeIDBigInt), nil
	case arrow.UINT8:
		return quacktype.Of(quacktype.LogicalTypeIDUTinyInt), nil
	case arrow.UINT16:
		return quacktype.Of(quacktype.LogicalTypeIDUSmallInt), nil
	case arrow.UINT32:
		return quacktype.Of(quacktype.LogicalTypeIDUInteger), nil
	case arrow.UINT64:
		return quacktype.Of(quacktype.LogicalTypeIDUBigInt), nil
	case arrow.FLOAT32:
		return quacktype.Of(quacktype.LogicalTypeIDFloat), nil
	case arrow.FLOAT64:
		return quacktype.Of(quacktype.LogicalTypeIDDouble), nil
	case arrow.STRING, arrow.LARGE_STRING:
		return quacktype.Of(quacktype.LogicalTypeIDVarchar), nil
	case arrow.BINARY, arrow.LARGE_BINARY:
		return quacktype.Of(quacktype.LogicalTypeIDBlob), nil
	case arrow.DATE32:
		return quacktype.Of(quacktype.LogicalTypeIDDate), nil
	case arrow.TIMESTAMP:
		ts := dt.(*arrow.TimestampType)
		if ts.TimeZone != "" {
			return quacktype.Of(quacktype.LogicalTypeIDTimestampTZ), nil
		}
		return quacktype.Of(quacktype.LogicalTypeIDTimestamp), nil
	case arrow.DECIMAL128:
		d := dt.(*arrow.Decimal128Type)
		return quacktype.Decimal(int(d.Precision), int(d.Scale)), nil
	}
	return quacktype.LogicalType{}, fmt.Errorf("unsupported arrow type %s for ingest", dt)
}

func decodedVectorForArrow(t quacktype.LogicalType, col arrow.Array, rowCount int) (message.DecodedVector, error) {
	validity := buildValidity(col, rowCount)
	switch arr := col.(type) {
	case *array.Boolean:
		vs := make([]bool, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.BoolVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Int8:
		vs := make([]int8, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.ByteVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Int16:
		vs := make([]int16, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.ShortVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Int32:
		vs := make([]int32, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.IntVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Int64:
		vs := make([]int64, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.LongVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Uint8:
		vs := make([]int16, rowCount) // promoted
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = int16(arr.Value(i))
			}
		}
		return message.ShortVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Uint16:
		vs := make([]int32, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = int32(arr.Value(i))
			}
		}
		return message.IntVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Uint32:
		vs := make([]int64, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = int64(arr.Value(i))
			}
		}
		return message.LongVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Uint64:
		vs := make([]int64, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = int64(arr.Value(i))
			}
		}
		return message.LongVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Float32:
		vs := make([]float32, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.FloatVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.Float64:
		vs := make([]float64, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.DoubleVec{TypeRef: t, Values: vs, Validity: validity}, nil
	case *array.String:
		vs := make([]interface{}, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				vs[i] = arr.Value(i)
			}
		}
		return message.ObjectVec{TypeRef: t, Values: vs}, nil
	case *array.Binary:
		vs := make([]interface{}, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				v := arr.Value(i)
				out := make([]byte, len(v))
				copy(out, v)
				vs[i] = out
			}
		}
		return message.ObjectVec{TypeRef: t, Values: vs}, nil
	case *array.Decimal128:
		dt := arr.DataType().(*arrow.Decimal128Type)
		vs := make([]interface{}, rowCount)
		for i := 0; i < rowCount; i++ {
			if !arr.IsNull(i) {
				v := arr.Value(i)
				bi := decimal128.Num.BigInt(v)
				_ = dt
				vs[i] = bi
			}
		}
		return message.ObjectVec{TypeRef: t, Values: vs}, nil
	}
	return nil, fmt.Errorf("decodedVectorForArrow: unsupported array %T", col)
}

func buildValidity(col arrow.Array, rowCount int) []uint64 {
	if col.NullN() == 0 {
		return nil
	}
	validity := message.ValidityAllValid(rowCount)
	for i := 0; i < rowCount; i++ {
		if col.IsNull(i) {
			message.ValiditySetNull(validity, i)
		}
	}
	return validity
}
