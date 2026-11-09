package quack

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal256"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// Arrow -> DuckDB for bulk ingest.
//
// Ingest sends the rows as DuckDB DataChunks (SEND_DATA), so each Arrow
// column is serialized straight into DuckDB's vector wire format. The type
// mapping mirrors DuckDB 2.0's own Arrow import (arrow_duck_schema.cpp),
// so a table ingested through Quack has the same column types as one
// ingested with DuckDB's own ADBC driver or arrow_scan.

// ingestChunkRows is DuckDB's STANDARD_VECTOR_SIZE: a table function may
// not emit a larger chunk, so ingest slices batches to it.
const ingestChunkRows = 2048

// ingestColumn is the DuckDB side of one Arrow field.
type ingestColumn struct {
	lt  quacktype.LogicalType
	sql string // the type as DuckDB SQL, for CREATE TABLE and the stream prototype
}

func extensionName(dt arrow.DataType) string {
	if ext, ok := dt.(arrow.ExtensionType); ok {
		return ext.ExtensionName()
	}
	return ""
}

// fieldExtensionName is the field's extension: its type's, or, for data
// imported through the C Data interface (which keeps an unregistered
// extension as field metadata), the ARROW:extension:name metadata.
func fieldExtensionName(f arrow.Field) string {
	if name := extensionName(f.Type); name != "" {
		return name
	}
	if i := f.Metadata.FindKey("ARROW:extension:name"); i >= 0 {
		return f.Metadata.Values()[i]
	}
	return ""
}

// duckColumnForField is duckColumnForArrow, honouring extension metadata
// on the field and on nested child fields.
func duckColumnForField(f arrow.Field) (ingestColumn, error) {
	switch fieldExtensionName(f) {
	case "arrow.uuid":
		return ingestColumn{quacktype.Of(quacktype.LogicalTypeIDUUID), "UUID"}, nil
	case "geoarrow.wkb":
		return ingestColumn{quacktype.Of(quacktype.LogicalTypeIDGeometry), "GEOMETRY"}, nil
	case "arrow.parquet.variant":
		return ingestColumn{variantLogicalType(), "VARIANT"}, nil
	}
	switch t := storageType(f.Type).(type) {
	case *arrow.ListType:
		return listColumnField(t.ElemField())
	case *arrow.LargeListType:
		return listColumnField(t.ElemField())
	case *arrow.FixedSizeListType:
		child, err := duckColumnForField(t.ElemField())
		if err != nil {
			return ingestColumn{}, err
		}
		return ingestColumn{quacktype.Array(child.lt, int(t.Len())), fmt.Sprintf("%s[%d]", child.sql, t.Len())}, nil
	case *arrow.StructType:
		if t.NumFields() == 0 {
			return ingestColumn{}, fmt.Errorf("DuckDB has no STRUCT with zero fields")
		}
		children := make([]quacktype.ChildType, t.NumFields())
		parts := make([]string, t.NumFields())
		for i, cf := range t.Fields() {
			c, err := duckColumnForField(cf)
			if err != nil {
				return ingestColumn{}, fmt.Errorf("field %q: %w", cf.Name, err)
			}
			children[i] = quacktype.ChildType{Name: cf.Name, Type: c.lt}
			parts[i] = quoteIdent(cf.Name) + " " + c.sql
		}
		return ingestColumn{quacktype.Struct(children), "STRUCT(" + strings.Join(parts, ", ") + ")"}, nil
	}
	return duckColumnForArrow(f.Type)
}

func listColumnField(elem arrow.Field) (ingestColumn, error) {
	child, err := duckColumnForField(elem)
	if err != nil {
		return ingestColumn{}, err
	}
	return ingestColumn{quacktype.List(child.lt), child.sql + "[]"}, nil
}

func storageType(dt arrow.DataType) arrow.DataType {
	if ext, ok := dt.(arrow.ExtensionType); ok {
		return ext.StorageType()
	}
	return dt
}

// duckColumnForArrow maps an Arrow type to the DuckDB column type DuckDB's
// own Arrow import would produce.
func duckColumnForArrow(dt arrow.DataType) (ingestColumn, error) {
	switch extensionName(dt) {
	case "arrow.uuid":
		return ingestColumn{quacktype.Of(quacktype.LogicalTypeIDUUID), "UUID"}, nil
	case "geoarrow.wkb":
		return ingestColumn{quacktype.Of(quacktype.LogicalTypeIDGeometry), "GEOMETRY"}, nil
	case "arrow.parquet.variant":
		return ingestColumn{variantLogicalType(), "VARIANT"}, nil
	}
	dt = storageType(dt)
	of := func(id quacktype.LogicalTypeID, sql string) (ingestColumn, error) {
		return ingestColumn{quacktype.Of(id), sql}, nil
	}
	switch t := dt.(type) {
	case *arrow.NullType:
		// DuckDB makes an untyped NULL column INTEGER (CREATE TABLE AS SELECT NULL).
		return of(quacktype.LogicalTypeIDInteger, "INTEGER")
	case *arrow.BooleanType:
		return of(quacktype.LogicalTypeIDBoolean, "BOOLEAN")
	case *arrow.Int8Type:
		return of(quacktype.LogicalTypeIDTinyInt, "TINYINT")
	case *arrow.Int16Type:
		return of(quacktype.LogicalTypeIDSmallInt, "SMALLINT")
	case *arrow.Int32Type:
		return of(quacktype.LogicalTypeIDInteger, "INTEGER")
	case *arrow.Int64Type:
		return of(quacktype.LogicalTypeIDBigInt, "BIGINT")
	case *arrow.Uint8Type:
		return of(quacktype.LogicalTypeIDUTinyInt, "UTINYINT")
	case *arrow.Uint16Type:
		return of(quacktype.LogicalTypeIDUSmallInt, "USMALLINT")
	case *arrow.Uint32Type:
		return of(quacktype.LogicalTypeIDUInteger, "UINTEGER")
	case *arrow.Uint64Type:
		return of(quacktype.LogicalTypeIDUBigInt, "UBIGINT")
	case *arrow.Float16Type, *arrow.Float32Type:
		return of(quacktype.LogicalTypeIDFloat, "FLOAT")
	case *arrow.Float64Type:
		return of(quacktype.LogicalTypeIDDouble, "DOUBLE")
	case *arrow.StringType, *arrow.LargeStringType, *arrow.StringViewType:
		return of(quacktype.LogicalTypeIDVarchar, "VARCHAR")
	case *arrow.BinaryType, *arrow.LargeBinaryType, *arrow.BinaryViewType, *arrow.FixedSizeBinaryType:
		return of(quacktype.LogicalTypeIDBlob, "BLOB")
	case *arrow.Date32Type, *arrow.Date64Type:
		return of(quacktype.LogicalTypeIDDate, "DATE")
	case *arrow.Time32Type:
		return of(quacktype.LogicalTypeIDTime, "TIME")
	case *arrow.Time64Type:
		if t.Unit == arrow.Nanosecond {
			return of(quacktype.LogicalTypeIDTimeNS, "TIME_NS")
		}
		return of(quacktype.LogicalTypeIDTime, "TIME")
	case *arrow.TimestampType:
		if t.TimeZone != "" {
			if t.Unit == arrow.Nanosecond {
				return of(quacktype.LogicalTypeIDTimestampTZNS, "TIMESTAMPTZ_NS")
			}
			return of(quacktype.LogicalTypeIDTimestampTZ, "TIMESTAMPTZ")
		}
		switch t.Unit {
		case arrow.Second:
			return of(quacktype.LogicalTypeIDTimestampSec, "TIMESTAMP_S")
		case arrow.Millisecond:
			return of(quacktype.LogicalTypeIDTimestampMS, "TIMESTAMP_MS")
		case arrow.Nanosecond:
			return of(quacktype.LogicalTypeIDTimestampNS, "TIMESTAMP_NS")
		default:
			return of(quacktype.LogicalTypeIDTimestamp, "TIMESTAMP")
		}
	case *arrow.DurationType, *arrow.MonthIntervalType, *arrow.DayTimeIntervalType, *arrow.MonthDayNanoIntervalType:
		return of(quacktype.LogicalTypeIDInterval, "INTERVAL")
	case arrow.DecimalType:
		p, s := t.GetPrecision(), t.GetScale()
		if p < 1 || p > 38 {
			return ingestColumn{}, fmt.Errorf("DECIMAL precision %d is out of DuckDB's range (1-38)", p)
		}
		if s < 0 || s > p {
			return ingestColumn{}, fmt.Errorf("DECIMAL(%d, %d): DuckDB needs 0 <= scale <= precision", p, s)
		}
		return ingestColumn{quacktype.Decimal(int(p), int(s)), fmt.Sprintf("DECIMAL(%d, %d)", p, s)}, nil
	case *arrow.DictionaryType:
		return duckColumnForArrow(t.ValueType)
	case *arrow.ListType:
		return listColumn(t.Elem())
	case *arrow.LargeListType:
		return listColumn(t.Elem())
	case *arrow.ListViewType:
		return listColumn(t.Elem())
	case *arrow.LargeListViewType:
		return listColumn(t.Elem())
	case *arrow.FixedSizeListType:
		child, err := duckColumnForArrow(t.Elem())
		if err != nil {
			return ingestColumn{}, err
		}
		return ingestColumn{quacktype.Array(child.lt, int(t.Len())), fmt.Sprintf("%s[%d]", child.sql, t.Len())}, nil
	case *arrow.MapType:
		key, err := duckColumnForArrow(t.KeyType())
		if err != nil {
			return ingestColumn{}, err
		}
		item, err := duckColumnForArrow(t.ItemType())
		if err != nil {
			return ingestColumn{}, err
		}
		entry := quacktype.Struct([]quacktype.ChildType{{Name: "key", Type: key.lt}, {Name: "value", Type: item.lt}})
		lt := quacktype.List(entry)
		lt.ID = quacktype.LogicalTypeIDMap
		return ingestColumn{lt, fmt.Sprintf("MAP(%s, %s)", key.sql, item.sql)}, nil
	case *arrow.StructType:
		if t.NumFields() == 0 {
			return ingestColumn{}, fmt.Errorf("DuckDB has no STRUCT with zero fields")
		}
		children := make([]quacktype.ChildType, t.NumFields())
		parts := make([]string, t.NumFields())
		for i, f := range t.Fields() {
			c, err := duckColumnForArrow(f.Type)
			if err != nil {
				return ingestColumn{}, fmt.Errorf("field %q: %w", f.Name, err)
			}
			children[i] = quacktype.ChildType{Name: f.Name, Type: c.lt}
			parts[i] = quoteIdent(f.Name) + " " + c.sql
		}
		return ingestColumn{quacktype.Struct(children), "STRUCT(" + strings.Join(parts, ", ") + ")"}, nil
	}
	return ingestColumn{}, fmt.Errorf("unsupported Arrow type %s for ingest", dt)
}

func listColumn(elem arrow.DataType) (ingestColumn, error) {
	child, err := duckColumnForArrow(elem)
	if err != nil {
		return ingestColumn{}, err
	}
	return ingestColumn{quacktype.List(child.lt), child.sql + "[]"}, nil
}

// encodeRecordChunks serializes rec as DataChunks of at most
// ingestChunkRows rows each.
func encodeRecordChunks(rec arrow.Record, cols []ingestColumn) ([]message.DataChunk, int64, error) {
	n := rec.NumRows()
	var out []message.DataChunk
	var bytes int64
	for start := int64(0); start < n; start += ingestChunkRows {
		end := min(start+ingestChunkRows, n)
		w := codec.NewBinaryWriter(64 * 1024)
		rows := int(end - start)
		var encErr error
		w.WriteObject(func(obj *codec.BinaryWriter) {
			obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(rows)) })
			obj.WriteField(101, func(o *codec.BinaryWriter) {
				codec.WriteList(o, cols, func(_ int, c ingestColumn, ww *codec.BinaryWriter) {
					quacktype.EncodeLogicalType(ww, c.lt)
				})
			})
			obj.WriteField(102, func(o *codec.BinaryWriter) {
				o.WriteULEB128(uint64(len(cols)))
				for i, c := range cols {
					col := array.NewSlice(rec.Column(i), start, end)
					err := encodeArrowVector(o, c.lt, col)
					col.Release()
					if err != nil && encErr == nil {
						encErr = fmt.Errorf("column %q: %w", rec.Schema().Field(i).Name, err)
					}
				}
			})
		})
		if encErr != nil {
			return nil, 0, encErr
		}
		if w.Err() != nil {
			return nil, 0, w.Err()
		}
		raw := w.Bytes()
		bytes += int64(len(raw))
		out = append(out, message.DataChunk{RowCount: rows, Raw: raw})
	}
	return out, bytes, nil
}

// encodeArrowVector writes one column vector object of arr.Len() rows.
func encodeArrowVector(w *codec.BinaryWriter, lt quacktype.LogicalType, arr arrow.Array) error {
	var err error
	w.WriteObject(func(obj *codec.BinaryWriter) {
		err = encodeArrowVectorBody(obj, lt, arr)
	})
	return err
}

// rowAccessor resolves row i of a (possibly dictionary-encoded) column to
// the array holding its value and the index inside it.
type rowAccessor struct {
	values arrow.Array
	dict   *array.Dictionary
}

func newRowAccessor(arr arrow.Array) rowAccessor {
	if ext, ok := arr.(array.ExtensionArray); ok {
		arr = ext.Storage()
	}
	if d, ok := arr.(*array.Dictionary); ok {
		values := d.Dictionary()
		if ext, ok := values.(array.ExtensionArray); ok {
			values = ext.Storage()
		}
		return rowAccessor{values: values, dict: d}
	}
	return rowAccessor{values: arr}
}

// at returns the value index for row i, or -1 for NULL.
func (a rowAccessor) at(i int) int {
	if a.dict != nil {
		if a.dict.IsNull(i) {
			return -1
		}
		j := a.dict.GetValueIndex(i)
		if a.values.IsNull(j) {
			return -1
		}
		return j
	}
	if a.values.IsNull(i) {
		return -1
	}
	return i
}

func writeValidity(w *codec.BinaryWriter, n int, valid func(i int) bool) {
	var validity []uint64
	for i := 0; i < n; i++ {
		if !valid(i) {
			if validity == nil {
				validity = message.ValidityAllValid(n)
			}
			message.ValiditySetNull(validity, i)
		}
	}
	w.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(validity != nil) })
	if validity != nil {
		w.WriteField(101, func(o *codec.BinaryWriter) { o.WriteBlob(message.ValidityToBytes(validity, n)) })
	}
}

func encodeArrowVectorBody(w *codec.BinaryWriter, lt quacktype.LogicalType, arr arrow.Array) error {
	n := arr.Len()
	if ext, ok := arr.(array.ExtensionArray); ok && lt.ID != quacktype.LogicalTypeIDVariant {
		arr = ext.Storage()
	}
	switch lt.ID {
	case quacktype.LogicalTypeIDStruct:
		return encodeStruct(w, lt, arr)
	case quacktype.LogicalTypeIDList, quacktype.LogicalTypeIDMap:
		return encodeList(w, lt, arr)
	case quacktype.LogicalTypeIDArray:
		return encodeFixedList(w, lt, arr)
	case quacktype.LogicalTypeIDVariant:
		return encodeVariantArrow(w, arr)
	}

	acc := newRowAccessor(arr)
	physical := quacktype.GetPhysicalType(lt)
	if lt.ID == quacktype.LogicalTypeIDGeometry {
		// geometry_format = WKB; without it DuckDB assumes the legacy
		// spatial-extension encoding.
		w.WriteField(99, func(o *codec.BinaryWriter) { o.WriteULEB128(1) })
	}
	writeValidity(w, n, func(i int) bool { return acc.at(i) >= 0 })

	if physical == quacktype.PhysicalTypeVarchar {
		get, err := bytesGetter(acc.values)
		if err != nil {
			return err
		}
		if lt.ID == quacktype.LogicalTypeIDGeometry {
			// DuckDB reads GEOMETRY vectors as a list of WKB strings only.
			w.WriteField(102, func(o *codec.BinaryWriter) {
				o.WriteULEB128(uint64(n))
				for i := 0; i < n; i++ {
					if j := acc.at(i); j >= 0 {
						o.WriteStringBytes(get(j))
					} else {
						o.WriteStringBytes(nil)
					}
				}
			})
			return nil
		}
		// DuckDB 2.0's split layout: lengths blob + bytes blob.
		lengths := make([]byte, 4*n)
		var data []byte
		for i := 0; i < n; i++ {
			if j := acc.at(i); j >= 0 {
				b := get(j)
				binary.LittleEndian.PutUint32(lengths[4*i:], uint32(len(b)))
				data = append(data, b...)
			}
		}
		w.WriteField(107, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(len(data))) })
		w.WriteField(108, func(o *codec.BinaryWriter) { o.WriteBlob(lengths) })
		w.WriteField(109, func(o *codec.BinaryWriter) { o.WriteBlob(data) })
		return nil
	}

	if !physical.IsConstantSize() {
		return fmt.Errorf("unsupported DuckDB type %s for ingest", lt.ID)
	}
	width := physical.ByteWidth()
	buf := make([]byte, width*n)
	put, err := fixedPutter(lt, physical, acc.values)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		if j := acc.at(i); j >= 0 {
			if err := put(buf[i*width:(i+1)*width], j); err != nil {
				return err
			}
		}
	}
	w.WriteField(102, func(o *codec.BinaryWriter) { o.WriteBlob(buf) })
	return nil
}

func bytesGetter(values arrow.Array) (func(int) []byte, error) {
	switch a := values.(type) {
	case *array.String:
		return func(i int) []byte { return []byte(a.Value(i)) }, nil
	case *array.LargeString:
		return func(i int) []byte { return []byte(a.Value(i)) }, nil
	case *array.StringView:
		return func(i int) []byte { return []byte(a.Value(i)) }, nil
	case *array.Binary:
		return a.Value, nil
	case *array.LargeBinary:
		return a.Value, nil
	case *array.BinaryView:
		return a.Value, nil
	case *array.FixedSizeBinary:
		return a.Value, nil
	}
	return nil, fmt.Errorf("cannot read %s as text/bytes", values.DataType())
}

// fixedPutter returns a function writing value j of values into a
// fixed-width DuckDB slot, converting units to DuckDB's.
func fixedPutter(lt quacktype.LogicalType, physical quacktype.PhysicalType, values arrow.Array) (func([]byte, int) error, error) {
	le := binary.LittleEndian
	put64 := func(f func(int) int64) func([]byte, int) error {
		return func(dst []byte, j int) error { le.PutUint64(dst, uint64(f(j))); return nil }
	}
	switch a := values.(type) {
	case *array.Null:
		return func([]byte, int) error { return nil }, nil
	case *array.Boolean:
		return func(dst []byte, j int) error {
			if a.Value(j) {
				dst[0] = 1
			}
			return nil
		}, nil
	case *array.Int8:
		return func(dst []byte, j int) error { dst[0] = byte(a.Value(j)); return nil }, nil
	case *array.Uint8:
		return func(dst []byte, j int) error { dst[0] = a.Value(j); return nil }, nil
	case *array.Int16:
		return func(dst []byte, j int) error { le.PutUint16(dst, uint16(a.Value(j))); return nil }, nil
	case *array.Uint16:
		return func(dst []byte, j int) error { le.PutUint16(dst, a.Value(j)); return nil }, nil
	case *array.Int32:
		return func(dst []byte, j int) error { le.PutUint32(dst, uint32(a.Value(j))); return nil }, nil
	case *array.Uint32:
		return func(dst []byte, j int) error { le.PutUint32(dst, a.Value(j)); return nil }, nil
	case *array.Int64:
		return put64(func(j int) int64 { return a.Value(j) }), nil
	case *array.Uint64:
		return func(dst []byte, j int) error { le.PutUint64(dst, a.Value(j)); return nil }, nil
	case *array.Float16:
		return func(dst []byte, j int) error { le.PutUint32(dst, math.Float32bits(a.Value(j).Float32())); return nil }, nil
	case *array.Float32:
		return func(dst []byte, j int) error { le.PutUint32(dst, math.Float32bits(a.Value(j))); return nil }, nil
	case *array.Float64:
		return func(dst []byte, j int) error { le.PutUint64(dst, math.Float64bits(a.Value(j))); return nil }, nil
	case *array.Date32:
		return func(dst []byte, j int) error { le.PutUint32(dst, uint32(a.Value(j))); return nil }, nil
	case *array.Date64:
		return func(dst []byte, j int) error {
			le.PutUint32(dst, uint32(floorDiv(int64(a.Value(j)), 86_400_000)))
			return nil
		}, nil
	case *array.Time32:
		mul := map[arrow.TimeUnit]int64{arrow.Second: 1_000_000, arrow.Millisecond: 1_000}[a.DataType().(*arrow.Time32Type).Unit]
		return put64(func(j int) int64 { return int64(a.Value(j)) * mul }), nil
	case *array.Time64:
		// TIME_NS keeps nanoseconds; TIME is microseconds.
		return put64(func(j int) int64 { return int64(a.Value(j)) }), nil
	case *array.Timestamp:
		unit := a.DataType().(*arrow.TimestampType).Unit
		mul := int64(1)
		if lt.ID == quacktype.LogicalTypeIDTimestampTZ {
			// TIMESTAMPTZ is microseconds whatever the Arrow unit was.
			switch unit {
			case arrow.Second:
				mul = 1_000_000
			case arrow.Millisecond:
				mul = 1_000
			}
		}
		return put64(func(j int) int64 { return int64(a.Value(j)) * mul }), nil
	case *array.Duration:
		unit := a.DataType().(*arrow.DurationType).Unit
		return func(dst []byte, j int) error {
			v := int64(a.Value(j))
			var micros int64
			switch unit {
			case arrow.Second:
				micros = v * 1_000_000
			case arrow.Millisecond:
				micros = v * 1_000
			case arrow.Microsecond:
				micros = v
			default:
				micros = v / 1_000
			}
			putInterval(dst, 0, 0, micros)
			return nil
		}, nil
	case *array.MonthInterval:
		return func(dst []byte, j int) error { putInterval(dst, int32(a.Value(j)), 0, 0); return nil }, nil
	case *array.DayTimeInterval:
		return func(dst []byte, j int) error {
			v := a.Value(j)
			putInterval(dst, 0, v.Days, int64(v.Milliseconds)*1_000)
			return nil
		}, nil
	case *array.MonthDayNanoInterval:
		return func(dst []byte, j int) error {
			v := a.Value(j)
			putInterval(dst, v.Months, v.Days, v.Nanoseconds/1_000)
			return nil
		}, nil
	case *array.FixedSizeBinary:
		if lt.ID != quacktype.LogicalTypeIDUUID {
			break
		}
		return func(dst []byte, j int) error {
			b := a.Value(j)
			if len(b) != 16 {
				return fmt.Errorf("arrow.uuid value has %d bytes, want 16", len(b))
			}
			// DuckDB stores a UUID as a hugeint with the top bit flipped,
			// so it sorts like the canonical string.
			upper := binary.BigEndian.Uint64(b[:8]) ^ (1 << 63)
			lower := binary.BigEndian.Uint64(b[8:])
			le.PutUint64(dst[:8], lower)
			le.PutUint64(dst[8:], upper)
			return nil
		}, nil
	case *array.Decimal32:
		return decimalPutter(physical, func(j int) *big.Int { return big.NewInt(int64(a.Value(j))) }), nil
	case *array.Decimal64:
		return decimalPutter(physical, func(j int) *big.Int { return big.NewInt(int64(a.Value(j))) }), nil
	case *array.Decimal128:
		return decimalPutter(physical, func(j int) *big.Int { return a.Value(j).BigInt() }), nil
	case *array.Decimal256:
		return decimalPutter(physical, func(j int) *big.Int { return decimal256.Num.BigInt(a.Value(j)) }), nil
	}
	return nil, fmt.Errorf("cannot ingest %s as %s", values.DataType(), lt.ID)
}

func putInterval(dst []byte, months, days int32, micros int64) {
	binary.LittleEndian.PutUint32(dst[0:], uint32(months))
	binary.LittleEndian.PutUint32(dst[4:], uint32(days))
	binary.LittleEndian.PutUint64(dst[8:], uint64(micros))
}

func decimalPutter(physical quacktype.PhysicalType, get func(int) *big.Int) func([]byte, int) error {
	le := binary.LittleEndian
	return func(dst []byte, j int) error {
		v := get(j)
		switch physical {
		case quacktype.PhysicalTypeInt16:
			le.PutUint16(dst, uint16(v.Int64()))
		case quacktype.PhysicalTypeInt32:
			le.PutUint32(dst, uint32(v.Int64()))
		case quacktype.PhysicalTypeInt64:
			le.PutUint64(dst, uint64(v.Int64()))
		default:
			parts := codec.HugeIntFromSigned(v)
			le.PutUint64(dst[:8], parts.Lower)
			le.PutUint64(dst[8:], uint64(parts.Upper))
		}
		return nil
	}
}

func encodeStruct(w *codec.BinaryWriter, lt quacktype.LogicalType, arr arrow.Array) error {
	st, ok := arr.(*array.Struct)
	if !ok {
		return fmt.Errorf("cannot ingest %s as STRUCT", arr.DataType())
	}
	children, _ := quacktype.GetStructChildren(lt)
	writeValidity(w, st.Len(), st.IsValid)
	var err error
	w.WriteField(103, func(o *codec.BinaryWriter) {
		o.WriteULEB128(uint64(len(children)))
		for i, c := range children {
			if e := encodeArrowVector(o, c.Type, st.Field(i)); e != nil && err == nil {
				err = fmt.Errorf("field %q: %w", c.Name, e)
			}
		}
	})
	return err
}

// listLike is the offsets view shared by Arrow's list, large list, list
// view and map arrays.
type listLike interface {
	arrow.Array
	ValueOffsets(i int) (start, end int64)
	ListValues() arrow.Array
}

func encodeList(w *codec.BinaryWriter, lt quacktype.LogicalType, arr arrow.Array) error {
	l, ok := arr.(listLike)
	if !ok {
		return fmt.Errorf("cannot ingest %s as %s", arr.DataType(), lt.ID)
	}
	n := l.Len()
	// Send only the child range these rows use: a 2048-row slice of a big
	// batch must not carry the whole batch's child values.
	lo, hi := int64(-1), int64(0)
	for i := 0; i < n; i++ {
		if l.IsNull(i) {
			continue
		}
		s, e := l.ValueOffsets(i)
		if e > s {
			if lo < 0 || s < lo {
				lo = s
			}
			if e > hi {
				hi = e
			}
		}
	}
	if lo < 0 {
		lo, hi = 0, 0
	}
	writeValidity(w, n, l.IsValid)
	w.WriteField(104, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(hi - lo)) })
	w.WriteField(105, func(o *codec.BinaryWriter) {
		o.WriteULEB128(uint64(n))
		for i := 0; i < n; i++ {
			var off, length int64
			if l.IsValid(i) {
				s, e := l.ValueOffsets(i)
				off, length = s-lo, e-s
				if length == 0 {
					off = 0
				}
			}
			o.WriteObject(func(ent *codec.BinaryWriter) {
				ent.WriteField(100, func(x *codec.BinaryWriter) { x.WriteULEB128(uint64(off)) })
				ent.WriteField(101, func(x *codec.BinaryWriter) { x.WriteULEB128(uint64(length)) })
			})
		}
	})
	childType, _ := quacktype.GetChildType(lt)
	child := array.NewSlice(l.ListValues(), lo, hi)
	defer child.Release()
	// For MAP, Arrow's entries struct carries (key, value) under any field
	// names; DuckDB's MAP child is STRUCT(key, value), matched by position.
	var err error
	w.WriteField(106, func(o *codec.BinaryWriter) { err = encodeArrowVector(o, childType, child) })
	return err
}

func encodeFixedList(w *codec.BinaryWriter, lt quacktype.LogicalType, arr arrow.Array) error {
	fl, ok := arr.(*array.FixedSizeList)
	if !ok {
		return fmt.Errorf("cannot ingest %s as ARRAY", arr.DataType())
	}
	size, _ := quacktype.GetArraySize(lt)
	n := fl.Len()
	start := int64(fl.Data().Offset()) * int64(size)
	child := array.NewSlice(fl.ListValues(), start, start+int64(n*size))
	defer child.Release()
	writeValidity(w, n, fl.IsValid)
	w.WriteField(103, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(size)) })
	childType, _ := quacktype.GetChildType(lt)
	var err error
	w.WriteField(104, func(o *codec.BinaryWriter) { err = encodeArrowVector(o, childType, child) })
	return err
}
