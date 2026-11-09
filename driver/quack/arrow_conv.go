// Package quack is the Quack-protocol ADBC driver implementation
// against the apache/arrow-adbc Go framework. It returns Apache Arrow
// RecordBatches from a remote DuckDB server speaking Quack.
package quack

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/google/uuid"

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
	case quacktype.LogicalTypeIDBlob, quacktype.LogicalTypeIDBit:
		return arrow.BinaryTypes.Binary
	case quacktype.LogicalTypeIDGeometry:
		return newGeoArrowWKB(geoArrowMetadata(t))
	case quacktype.LogicalTypeIDVariant:
		return extensions.NewDefaultVariantType()
	case quacktype.LogicalTypeIDDate:
		return arrow.FixedWidthTypes.Date32
	case quacktype.LogicalTypeIDTime, quacktype.LogicalTypeIDTimeTZ:
		return arrow.FixedWidthTypes.Time64us
	case quacktype.LogicalTypeIDTimeNS:
		return arrow.FixedWidthTypes.Time64ns
	// Not arrow.FixedWidthTypes.Timestamp_*: those carry TimeZone "UTC",
	// which would make a naive TIMESTAMP look zoned.
	case quacktype.LogicalTypeIDTimestamp:
		return &arrow.TimestampType{Unit: arrow.Microsecond}
	case quacktype.LogicalTypeIDTimestampSec:
		return &arrow.TimestampType{Unit: arrow.Second}
	case quacktype.LogicalTypeIDTimestampMS:
		return &arrow.TimestampType{Unit: arrow.Millisecond}
	case quacktype.LogicalTypeIDTimestampNS:
		return &arrow.TimestampType{Unit: arrow.Nanosecond}
	case quacktype.LogicalTypeIDTimestampTZ:
		return &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
	case quacktype.LogicalTypeIDTimestampTZNS:
		return &arrow.TimestampType{Unit: arrow.Nanosecond, TimeZone: "UTC"}
	case quacktype.LogicalTypeIDDecimal:
		if d, ok := t.TypeInfo.(quacktype.DecimalTypeInfo); ok {
			return &arrow.Decimal128Type{Precision: int32(d.Width), Scale: int32(d.Scale)}
		}
		return &arrow.Decimal128Type{Precision: 38, Scale: 0}
	case quacktype.LogicalTypeIDUUID:
		// The canonical arrow.uuid extension (16 bytes), so a UUID stays a
		// UUID through Arrow consumers and back into an ingest.
		return extensions.NewUUIDType()
	case quacktype.LogicalTypeIDHugeInt, quacktype.LogicalTypeIDUHugeInt:
		// Arrow has no native int128; map to Decimal128(38, 0) which fits
		// any signed int128 exactly. UHUGEINT values above 10^38-1 lose
		// precision — Decimal128's max precision is 38 vs uint128's 39
		// decimal digits. Use a CAST in SQL when full unsigned range is
		// needed; SUM(INTEGER) etc. comfortably fit.
		return &arrow.Decimal128Type{Precision: 38, Scale: 0}
	case quacktype.LogicalTypeIDInterval:
		return arrow.FixedWidthTypes.MonthDayNanoInterval
	case quacktype.LogicalTypeIDList:
		child, err := quacktype.GetChildType(t)
		if err != nil {
			return arrow.BinaryTypes.String
		}
		return arrow.ListOf(arrowType(child))
	case quacktype.LogicalTypeIDArray:
		child, err := quacktype.GetChildType(t)
		if err != nil {
			return arrow.BinaryTypes.String
		}
		size, err := quacktype.GetArraySize(t)
		if err != nil || size <= 0 {
			// Fall back to a regular list if size is unset — DuckDB only
			// uses ARRAY when size > 0.
			return arrow.ListOf(arrowType(child))
		}
		return arrow.FixedSizeListOf(int32(size), arrowType(child))
	case quacktype.LogicalTypeIDStruct, quacktype.LogicalTypeIDTuple:
		children, err := quacktype.GetStructChildren(t)
		if err != nil || len(children) == 0 {
			return arrow.BinaryTypes.String
		}
		fields := make([]arrow.Field, len(children))
		for i, c := range children {
			fields[i] = arrow.Field{
				Name:     c.Name,
				Type:     arrowType(c.Type),
				Nullable: true,
			}
		}
		return arrow.StructOf(fields...)
	case quacktype.LogicalTypeIDMap:
		// DuckDB MAP is encoded as LIST<STRUCT<key, value>>. ListTypeInfo
		// carries the element type — which for MAP is a struct of (key, value).
		child, err := quacktype.GetChildType(t)
		if err != nil {
			return arrow.BinaryTypes.String
		}
		if structChildren, err := quacktype.GetStructChildren(child); err == nil && len(structChildren) == 2 {
			return arrow.MapOf(arrowType(structChildren[0].Type), arrowType(structChildren[1].Type))
		}
		// If the wire didn't carry a key/value pair, fall back to list of
		// stringified structs so the column still surfaces.
		return arrow.ListOf(arrowType(child))
	}
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
	for i := 0; i < n; i++ {
		var v interface{}
		if !vec.IsNull(i) {
			v = vec.GetObject(i)
		}
		if err := appendValue(b, t, v); err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
	}
	return nil
}

func valueMismatch(b array.Builder, t quacktype.LogicalType, v interface{}) error {
	return fmt.Errorf("arrowconv: cannot append %T to %s for %s", v, b.Type(), t.ID)
}

// appendValue appends one decoded value (nil = NULL) to b. It is used for
// top-level columns and, recursively, for the children of LIST, ARRAY,
// STRUCT and MAP values, so a nested value converts exactly like a
// top-level one. A value of an unexpected Go type is an error, never a
// silent NULL.
func appendValue(b array.Builder, t quacktype.LogicalType, v interface{}) error {
	if v == nil {
		b.AppendNull()
		return nil
	}
	switch bb := b.(type) {
	case *extensions.VariantBuilder:
		val, err := duckVariantToParquet(v)
		if err != nil {
			return err
		}
		bb.Append(val)
	case *extensions.UUIDBuilder:
		str, ok := v.(string)
		if !ok {
			return valueMismatch(b, t, v)
		}
		u, err := uuid.Parse(str)
		if err != nil {
			return fmt.Errorf("arrowconv: invalid UUID %q", str)
		}
		bb.Append(u)
	case *array.ExtensionBuilder:
		// geoarrow.wkb: WKB bytes in the binary storage.
		return appendValue(bb.StorageBuilder(), t, v)
	case *array.BooleanBuilder:
		x, ok := v.(bool)
		if !ok {
			return valueMismatch(b, t, v)
		}
		bb.Append(x)
	case *array.Int8Builder:
		bb.Append(toInt8(v))
	case *array.Int16Builder:
		bb.Append(toInt16(v))
	case *array.Int32Builder:
		bb.Append(toInt32(v))
	case *array.Int64Builder:
		bb.Append(toInt64(v))
	case *array.Uint8Builder:
		bb.Append(uint8(toInt64(v)))
	case *array.Uint16Builder:
		bb.Append(uint16(toInt64(v)))
	case *array.Uint32Builder:
		bb.Append(uint32(toInt64(v)))
	case *array.Uint64Builder:
		if x, ok := v.(uint64); ok {
			bb.Append(x)
		} else {
			bb.Append(uint64(toInt64(v)))
		}
	case *array.Float32Builder:
		bb.Append(float32(toFloat64(v)))
	case *array.Float64Builder:
		bb.Append(toFloat64(v))
	case *array.StringBuilder:
		switch x := v.(type) {
		case string:
			bb.Append(x)
		case []byte:
			bb.Append(string(x))
		default:
			bb.Append(fmt.Sprintf("%v", x))
		}
	case *array.BinaryBuilder:
		switch x := v.(type) {
		case []byte:
			bb.Append(x)
		case string:
			bb.Append([]byte(x))
		default:
			return valueMismatch(b, t, v)
		}
	case *array.Date32Builder:
		x, ok := v.(time.Time)
		if !ok {
			return valueMismatch(b, t, v)
		}
		bb.Append(arrow.Date32(floorDiv(x.Unix(), 86400)))
	case *array.Time64Builder:
		unit := bb.Type().(*arrow.Time64Type).Unit
		switch x := v.(type) {
		case time.Time:
			// TIME / TIME_NS decode as the epoch day plus the time of day,
			// so 24:00:00 survives as a full day.
			nanos := x.Sub(time.Unix(0, 0).UTC()).Nanoseconds()
			if unit == arrow.Nanosecond {
				bb.Append(arrow.Time64(nanos))
			} else {
				bb.Append(arrow.Time64(nanos / 1000))
			}
		case int64:
			// TIME_TZ: the upper 40 bits are the local time in micros, as
			// DuckDB's own Arrow export reports it.
			bb.Append(arrow.Time64(x >> 24))
		default:
			return valueMismatch(b, t, v)
		}
	case *array.TimestampBuilder:
		x, ok := v.(time.Time)
		if !ok {
			return valueMismatch(b, t, v)
		}
		switch bb.Type().(*arrow.TimestampType).Unit {
		case arrow.Second:
			bb.Append(arrow.Timestamp(x.Unix()))
		case arrow.Millisecond:
			bb.Append(arrow.Timestamp(x.UnixMilli()))
		case arrow.Nanosecond:
			bb.Append(arrow.Timestamp(x.UnixNano()))
		default:
			bb.Append(arrow.Timestamp(x.UnixMicro()))
		}
	case *array.MonthDayNanoIntervalBuilder:
		x, ok := v.(message.IntervalValue)
		if !ok {
			return valueMismatch(b, t, v)
		}
		bb.Append(arrow.MonthDayNanoInterval{Months: x.Months, Days: x.Days, Nanoseconds: x.Micros * 1000})
	case *array.Decimal128Builder:
		switch x := v.(type) {
		case *big.Rat:
			dt := bb.Type().(*arrow.Decimal128Type)
			scaleFactor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dt.Scale)), nil)
			num := new(big.Int).Mul(x.Num(), scaleFactor)
			bb.Append(decimal128.FromBigInt(new(big.Int).Quo(num, x.Denom())))
		case *big.Int:
			// HUGEINT / UHUGEINT: already an unscaled integer.
			bb.Append(decimal128.FromBigInt(x))
		default:
			return valueMismatch(b, t, v)
		}
	case *array.FixedSizeBinaryBuilder:
		s, ok := v.(string)
		if !ok {
			return valueMismatch(b, t, v)
		}
		raw, err := uuidStringToBytes(s)
		if err != nil {
			return err
		}
		bb.Append(raw)
	case *array.ListBuilder:
		items, ok := v.([]interface{})
		if !ok {
			return valueMismatch(b, t, v)
		}
		childType, _ := quacktype.GetChildType(t)
		bb.Append(true)
		for _, item := range items {
			if err := appendValue(bb.ValueBuilder(), childType, item); err != nil {
				return err
			}
		}
	case *array.FixedSizeListBuilder:
		items, ok := v.([]interface{})
		if !ok {
			return valueMismatch(b, t, v)
		}
		childType, _ := quacktype.GetChildType(t)
		bb.Append(true)
		for _, item := range items {
			if err := appendValue(bb.ValueBuilder(), childType, item); err != nil {
				return err
			}
		}
	case *array.StructBuilder:
		row, ok := v.(map[string]interface{})
		if !ok {
			return valueMismatch(b, t, v)
		}
		children, _ := quacktype.GetStructChildren(t)
		bb.Append(true)
		for ci, child := range children {
			if err := appendValue(bb.FieldBuilder(ci), child.Type, row[quacktype.StructFieldKey(children, ci)]); err != nil {
				return err
			}
		}
	case *array.MapBuilder:
		entries, ok := v.([]interface{})
		if !ok {
			return valueMismatch(b, t, v)
		}
		var keyType, valueType quacktype.LogicalType
		if entryType, err := quacktype.GetChildType(t); err == nil {
			if kv, err := quacktype.GetStructChildren(entryType); err == nil && len(kv) == 2 {
				keyType, valueType = kv[0].Type, kv[1].Type
			}
		}
		bb.Append(true)
		for _, e := range entries {
			kv, ok := e.(map[string]interface{})
			if !ok {
				return valueMismatch(b, t, e)
			}
			if err := appendValue(bb.KeyBuilder(), keyType, kv["key"]); err != nil {
				return err
			}
			if err := appendValue(bb.ItemBuilder(), valueType, kv["value"]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("arrowconv: builder %T not handled (type %s)", b, t.ID)
	}
	return nil
}

func floorDiv(a, b int64) int32 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return int32(q)
}

// uuidStringToBytes parses a canonical 8-4-4-4-12 UUID into its 16 bytes.
func uuidStringToBytes(s string) ([]byte, error) {
	clean := strings.ReplaceAll(s, "-", "")
	raw, err := hex.DecodeString(clean)
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("arrowconv: invalid UUID %q", s)
	}
	return raw, nil
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
