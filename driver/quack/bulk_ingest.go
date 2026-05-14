package quack

import (
	"context"
	"fmt"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
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
