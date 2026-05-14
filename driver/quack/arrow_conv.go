// Package quack is the Quack-protocol ADBC driver implementation
// against the apache/arrow-adbc Go framework. It returns Apache Arrow
// RecordBatches from a remote DuckDB server speaking Quack.
package quack

import (
	"fmt"
	"math/big"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// arrowType returns the arrow.DataType that corresponds to the Quack
// logical type.
func arrowType(t quacktype.LogicalType) arrow.DataType {
	switch t.ID {
	case quacktype.LogicalTypeIDBoolean:
		return arrow.FixedWidthTypes.Boolean
	case quacktype.LogicalTypeIDTinyInt:
		return arrow.PrimitiveTypes.Int8
	case quacktype.LogicalTypeIDSmallInt:
		return arrow.PrimitiveTypes.Int16
	case quacktype.LogicalTypeIDInteger:
		return arrow.PrimitiveTypes.Int32
	case quacktype.LogicalTypeIDBigInt:
		return arrow.PrimitiveTypes.Int64
	case quacktype.LogicalTypeIDUTinyInt:
		return arrow.PrimitiveTypes.Uint8
	case quacktype.LogicalTypeIDUSmallInt:
		return arrow.PrimitiveTypes.Uint16
	case quacktype.LogicalTypeIDUInteger:
		return arrow.PrimitiveTypes.Uint32
	case quacktype.LogicalTypeIDUBigInt:
		return arrow.PrimitiveTypes.Uint64
	case quacktype.LogicalTypeIDFloat:
		return arrow.PrimitiveTypes.Float32
	case quacktype.LogicalTypeIDDouble:
		return arrow.PrimitiveTypes.Float64
	case quacktype.LogicalTypeIDVarchar, quacktype.LogicalTypeIDChar,
		quacktype.LogicalTypeIDStringLiteral, quacktype.LogicalTypeIDBigNum,
		quacktype.LogicalTypeIDEnum:
		return arrow.BinaryTypes.String
	case quacktype.LogicalTypeIDBlob, quacktype.LogicalTypeIDBit, quacktype.LogicalTypeIDGeometry:
		return arrow.BinaryTypes.Binary
	case quacktype.LogicalTypeIDDate:
		return arrow.FixedWidthTypes.Date32
	case quacktype.LogicalTypeIDTime:
		return arrow.FixedWidthTypes.Time64us
	case quacktype.LogicalTypeIDTimeNS:
		return arrow.FixedWidthTypes.Time64ns
	case quacktype.LogicalTypeIDTimestamp, quacktype.LogicalTypeIDTimestampSec,
		quacktype.LogicalTypeIDTimestampMS, quacktype.LogicalTypeIDTimestampNS:
		return arrow.FixedWidthTypes.Timestamp_us
	case quacktype.LogicalTypeIDTimestampTZ:
		return &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
	case quacktype.LogicalTypeIDDecimal:
		if d, ok := t.TypeInfo.(quacktype.DecimalTypeInfo); ok {
			return &arrow.Decimal128Type{Precision: int32(d.Width), Scale: int32(d.Scale)}
		}
		return &arrow.Decimal128Type{Precision: 38, Scale: 0}
	case quacktype.LogicalTypeIDUUID:
		return &arrow.FixedSizeBinaryType{ByteWidth: 16}
	case quacktype.LogicalTypeIDHugeInt, quacktype.LogicalTypeIDUHugeInt:
		// No native 128-bit arrow int; represent as string.
		return arrow.BinaryTypes.String
	case quacktype.LogicalTypeIDInterval:
		return arrow.FixedWidthTypes.MonthDayNanoInterval
	}
	// Fallback for anything else (STRUCT/LIST/ARRAY/MAP nested types — not
	// yet implemented in the converter).
	return arrow.BinaryTypes.String
}

// SchemaFromColumns builds an arrow.Schema from Quack column names + types.
func SchemaFromColumns(names []string, types []quacktype.LogicalType) *arrow.Schema {
	fields := make([]arrow.Field, len(names))
	for i, n := range names {
		fields[i] = arrow.Field{
			Name:     n,
			Type:     arrowType(types[i]),
			Nullable: true,
		}
	}
	return arrow.NewSchema(fields, nil)
}

// RecordFromChunk converts a Quack DataChunk into an arrow.Record.
//
// Caller is responsible for releasing the returned record.
func RecordFromChunk(allocator memory.Allocator, names []string, chunk message.DataChunk) (arrow.Record, error) {
	schema := SchemaFromColumns(names, chunk.Types)
	if allocator == nil {
		allocator = memory.NewGoAllocator()
	}
	builder := array.NewRecordBuilder(allocator, schema)
	defer builder.Release()

	for col, vec := range chunk.Columns {
		if err := buildColumn(builder.Field(col), chunk.Types[col], vec); err != nil {
			return nil, fmt.Errorf("column %q (%s): %w", names[col], chunk.Types[col].ID, err)
		}
	}
	return builder.NewRecord(), nil
}

func buildColumn(b array.Builder, t quacktype.LogicalType, vec message.DecodedVector) error {
	n := vec.Size()
	switch builder := b.(type) {
	case *array.BooleanBuilder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(vec.GetObject(i).(bool))
			}
		}
	case *array.Int8Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(toInt8(vec.GetObject(i)))
			}
		}
	case *array.Int16Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(toInt16(vec.GetObject(i)))
			}
		}
	case *array.Int32Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(toInt32(vec.GetObject(i)))
			}
		}
	case *array.Int64Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(toInt64(vec.GetObject(i)))
			}
		}
	case *array.Uint8Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(uint8(toInt64(vec.GetObject(i))))
			}
		}
	case *array.Uint16Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(uint16(toInt64(vec.GetObject(i))))
			}
		}
	case *array.Uint32Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(uint32(toInt64(vec.GetObject(i))))
			}
		}
	case *array.Uint64Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(uint64(toInt64(vec.GetObject(i))))
			}
		}
	case *array.Float32Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(float32(toFloat64(vec.GetObject(i))))
			}
		}
	case *array.Float64Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(toFloat64(vec.GetObject(i)))
			}
		}
	case *array.StringBuilder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				builder.Append(fmt.Sprintf("%v", vec.GetObject(i)))
			}
		}
	case *array.BinaryBuilder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else {
				v := vec.GetObject(i)
				if b, ok := v.([]byte); ok {
					builder.Append(b)
				} else {
					builder.Append([]byte(fmt.Sprintf("%v", v)))
				}
			}
		}
	case *array.Date32Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else if v, ok := vec.GetObject(i).(time.Time); ok {
				epoch := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
				builder.Append(arrow.Date32(int32(v.Sub(epoch).Hours() / 24)))
			} else {
				builder.AppendNull()
			}
		}
	case *array.Time64Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else if v, ok := vec.GetObject(i).(time.Time); ok {
				vUTC := v.UTC()
				micros := int64(time.Duration(vUTC.Hour())*time.Hour+
					time.Duration(vUTC.Minute())*time.Minute+
					time.Duration(vUTC.Second())*time.Second+
					time.Duration(vUTC.Nanosecond())) / int64(time.Microsecond)
				builder.Append(arrow.Time64(micros))
			} else {
				builder.AppendNull()
			}
		}
	case *array.TimestampBuilder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else if v, ok := vec.GetObject(i).(time.Time); ok {
				builder.Append(arrow.Timestamp(v.UnixMicro()))
			} else {
				builder.AppendNull()
			}
		}
	case *array.Decimal128Builder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else if r, ok := vec.GetObject(i).(*big.Rat); ok {
				dt, _ := b.Type().(*arrow.Decimal128Type)
				scaleFactor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dt.Scale)), nil)
				num := new(big.Int).Mul(r.Num(), scaleFactor)
				unscaled := new(big.Int).Quo(num, r.Denom())
				builder.Append(decimal128.FromBigInt(unscaled))
			} else {
				builder.AppendNull()
			}
		}
	case *array.FixedSizeBinaryBuilder:
		for i := 0; i < n; i++ {
			if vec.IsNull(i) {
				builder.AppendNull()
			} else if s, ok := vec.GetObject(i).(string); ok && len(s) == 36 {
				bytes := uuidStringToBytes(s)
				builder.Append(bytes)
			} else {
				builder.AppendNull()
			}
		}
	default:
		// Fallback — try string-format the value.
		if sb, ok := b.(*array.StringBuilder); ok {
			for i := 0; i < n; i++ {
				if vec.IsNull(i) {
					sb.AppendNull()
				} else {
					sb.Append(fmt.Sprintf("%v", vec.GetObject(i)))
				}
			}
			return nil
		}
		return fmt.Errorf("arrowconv: builder %T not yet handled (type %s)", b, t.ID)
	}
	return nil
}

func uuidStringToBytes(s string) []byte {
	out := make([]byte, 0, 16)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			continue
		}
		var nib byte
		switch {
		case c >= '0' && c <= '9':
			nib = c - '0'
		case c >= 'a' && c <= 'f':
			nib = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			nib = c - 'A' + 10
		}
		if i%2 == 0 && len(out) > 0 && len(out) < 16 {
			out[len(out)-1] |= nib
		} else {
			if len(out) >= 16 {
				break
			}
			out = append(out, nib<<4)
		}
	}
	for len(out) < 16 {
		out = append(out, 0)
	}
	return out
}

func toInt8(v interface{}) int8 {
	switch x := v.(type) {
	case int8:
		return x
	case int16:
		return int8(x)
	case int32:
		return int8(x)
	case int64:
		return int8(x)
	}
	return 0
}

func toInt16(v interface{}) int16 {
	switch x := v.(type) {
	case int8:
		return int16(x)
	case int16:
		return x
	case int32:
		return int16(x)
	case int64:
		return int16(x)
	}
	return 0
}

func toInt32(v interface{}) int32 {
	switch x := v.(type) {
	case int8:
		return int32(x)
	case int16:
		return int32(x)
	case int32:
		return x
	case int64:
		return int32(x)
	case uint16:
		return int32(x)
	case uint32:
		return int32(x)
	}
	return 0
}

func toInt64(v interface{}) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	}
	return 0
}

func toFloat64(v interface{}) float64 {
	switch x := v.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	}
	return float64(toInt64(v))
}
