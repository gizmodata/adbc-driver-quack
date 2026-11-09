package quack

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/extensions"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/variant"
	"github.com/google/uuid"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// VARIANT over Quack.
//
// A DuckDB VARIANT travels in DuckDB's own layout: per row a STRUCT of
// keys (the object keys), children (keys_index, values_index), values
// (type_id, byte_offset) and data (a blob holding each value at its byte
// offset). The driver turns it into Arrow's canonical arrow.parquet.variant
// extension (a struct of metadata and value binaries in the Parquet Variant
// encoding), the same representation DuckDB's own Arrow export, and so
// Hardcharger, produce; and turns arrow.parquet.variant input back into
// DuckDB's layout for ingest.

// DuckDB's VariantLogicalType ids (duckdb/common/types/variant.hpp).
const (
	duckVariantNull            = 0
	duckVariantTrue            = 1
	duckVariantFalse           = 2
	duckVariantInt8            = 3
	duckVariantInt16           = 4
	duckVariantInt32           = 5
	duckVariantInt64           = 6
	duckVariantInt128          = 7
	duckVariantUInt8           = 8
	duckVariantUInt16          = 9
	duckVariantUInt32          = 10
	duckVariantUInt64          = 11
	duckVariantUInt128         = 12
	duckVariantFloat           = 13
	duckVariantDouble          = 14
	duckVariantDecimal         = 15
	duckVariantVarchar         = 16
	duckVariantBlob            = 17
	duckVariantUUID            = 18
	duckVariantDate            = 19
	duckVariantTimeMicros      = 20
	duckVariantTimeNanos       = 21
	duckVariantTimestampSec    = 22
	duckVariantTimestampMillis = 23
	duckVariantTimestampMicros = 24
	duckVariantTimestampNanos  = 25
	duckVariantTimeMicrosTZ    = 26
	duckVariantTimestampTZ     = 27
	duckVariantInterval        = 28
	duckVariantObject          = 29
	duckVariantArray           = 30
	duckVariantBignum          = 31
	duckVariantBitstring       = 32
	duckVariantGeometry        = 33
	duckVariantTimestampNanoTZ = 34
)

var duckVariantTypeNames = map[int]string{
	duckVariantInterval: "INTERVAL", duckVariantBignum: "BIGNUM", duckVariantBitstring: "BIT",
	duckVariantTimeNanos: "TIME_NS", duckVariantTimeMicrosTZ: "TIMETZ",
}

// variantLogicalType is DuckDB's VARIANT with its storage struct as type
// info, as the server sends it and expects it back.
func variantLogicalType() quacktype.LogicalType {
	u32 := quacktype.Of(quacktype.LogicalTypeIDUInteger)
	return quacktype.LogicalType{
		ID: quacktype.LogicalTypeIDVariant,
		TypeInfo: quacktype.StructTypeInfo{ChildTypes: []quacktype.ChildType{
			{Name: "keys", Type: quacktype.List(quacktype.Of(quacktype.LogicalTypeIDVarchar))},
			{Name: "children", Type: quacktype.List(quacktype.Struct([]quacktype.ChildType{
				{Name: "keys_index", Type: u32}, {Name: "values_index", Type: u32}}))},
			{Name: "values", Type: quacktype.List(quacktype.Struct([]quacktype.ChildType{
				{Name: "type_id", Type: quacktype.Of(quacktype.LogicalTypeIDUTinyInt)}, {Name: "byte_offset", Type: u32}}))},
			{Name: "data", Type: quacktype.Of(quacktype.LogicalTypeIDBlob)},
		}},
	}
}

// --- DuckDB layout -> Parquet Variant (query results) ---

func asUint64(v interface{}) (uint64, bool) {
	switch x := v.(type) {
	case int:
		return uint64(x), x >= 0
	case int32:
		return uint64(x), x >= 0
	case int64:
		return uint64(x), x >= 0
	case uint8:
		return uint64(x), true
	case uint16:
		return uint64(x), true
	case uint32:
		return uint64(x), true
	case uint64:
		return x, true
	case int16:
		return uint64(x), x >= 0
	case int8:
		return uint64(x), x >= 0
	}
	return 0, false
}

type duckVariantRow struct {
	keys     []string
	children [][2]int64 // keys_index (-1 = none), values_index
	values   [][2]uint64
	data     []byte
}

func parseDuckVariantRow(v interface{}) (*duckVariantRow, error) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("VARIANT: unexpected value %T", v)
	}
	r := &duckVariantRow{}
	for _, k := range asSlice(m["keys"]) {
		s, _ := k.(string)
		r.keys = append(r.keys, s)
	}
	for _, c := range asSlice(m["children"]) {
		cm, _ := c.(map[string]interface{})
		ki := int64(-1)
		if k, ok := asUint64(cm["keys_index"]); ok && cm["keys_index"] != nil {
			ki = int64(k)
		}
		vi, ok := asUint64(cm["values_index"])
		if !ok {
			return nil, fmt.Errorf("VARIANT: child without values_index")
		}
		r.children = append(r.children, [2]int64{ki, int64(vi)})
	}
	for _, e := range asSlice(m["values"]) {
		em, _ := e.(map[string]interface{})
		t, ok1 := asUint64(em["type_id"])
		off, ok2 := asUint64(em["byte_offset"])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("VARIANT: malformed value entry")
		}
		r.values = append(r.values, [2]uint64{t, off})
	}
	r.data, _ = m["data"].([]byte)
	if len(r.values) == 0 {
		return nil, fmt.Errorf("VARIANT: row has no values")
	}
	return r, nil
}

func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}

// uvarint reads DuckDB's VarintEncode (unsigned LEB128).
func uvarint(data []byte, pos *int) (uint64, error) {
	v, n := binary.Uvarint(data[*pos:])
	if n <= 0 {
		return 0, fmt.Errorf("VARIANT: bad varint at byte %d", *pos)
	}
	*pos += n
	return v, nil
}

func fixedAt(data []byte, off uint64, n int) ([]byte, error) {
	if off+uint64(n) > uint64(len(data)) {
		return nil, fmt.Errorf("VARIANT: value at byte %d overruns the data", off)
	}
	return data[off : off+uint64(n)], nil
}

// appendExactInt writes a Parquet Variant integer of exactly the given
// width (the variant builder would narrow it), matching DuckDB's export.
func appendExactInt(b *variant.Builder, prim variant.PrimitiveType, le []byte) error {
	buf := make([]byte, 0, 1+len(le))
	buf = append(buf, byte(prim)<<2)
	buf = append(buf, le...)
	return b.UnsafeAppendEncoded(buf)
}

func le64(v int64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

func (r *duckVariantRow) write(b *variant.Builder, vi uint64, depth int) error {
	if depth > 1000 {
		return fmt.Errorf("VARIANT: nesting too deep")
	}
	if vi >= uint64(len(r.values)) {
		return fmt.Errorf("VARIANT: values_index %d out of range", vi)
	}
	t, off := int(r.values[vi][0]), r.values[vi][1]
	d := r.data
	switch t {
	case duckVariantNull:
		return b.AppendNull()
	case duckVariantTrue:
		return b.AppendBool(true)
	case duckVariantFalse:
		return b.AppendBool(false)
	case duckVariantInt8, duckVariantInt16, duckVariantInt32, duckVariantInt64:
		width := map[int]int{duckVariantInt8: 1, duckVariantInt16: 2, duckVariantInt32: 4, duckVariantInt64: 8}[t]
		raw, err := fixedAt(d, off, width)
		if err != nil {
			return err
		}
		prim := map[int]variant.PrimitiveType{duckVariantInt8: variant.PrimitiveInt8, duckVariantInt16: variant.PrimitiveInt16,
			duckVariantInt32: variant.PrimitiveInt32, duckVariantInt64: variant.PrimitiveInt64}[t]
		return appendExactInt(b, prim, raw)
	case duckVariantUInt8:
		raw, err := fixedAt(d, off, 1)
		if err != nil {
			return err
		}
		return appendExactInt(b, variant.PrimitiveInt16, []byte{raw[0], 0})
	case duckVariantUInt16:
		raw, err := fixedAt(d, off, 2)
		if err != nil {
			return err
		}
		return appendExactInt(b, variant.PrimitiveInt32, []byte{raw[0], raw[1], 0, 0})
	case duckVariantUInt32, duckVariantUInt64, duckVariantInt128, duckVariantUInt128:
		width := map[int]int{duckVariantUInt32: 4, duckVariantUInt64: 8, duckVariantInt128: 16, duckVariantUInt128: 16}[t]
		raw, err := fixedAt(d, off, width)
		if err != nil {
			return err
		}
		n := new(big.Int)
		switch t {
		case duckVariantUInt32:
			n.SetUint64(uint64(binary.LittleEndian.Uint32(raw)))
		case duckVariantUInt64:
			n.SetUint64(binary.LittleEndian.Uint64(raw))
		case duckVariantInt128:
			n = codec.HugeIntParts{Lower: binary.LittleEndian.Uint64(raw[:8]), Upper: int64(binary.LittleEndian.Uint64(raw[8:]))}.SignedBigInt()
		default:
			n = codec.HugeIntParts{Lower: binary.LittleEndian.Uint64(raw[:8]), Upper: int64(binary.LittleEndian.Uint64(raw[8:]))}.UnsignedBigInt()
		}
		if n.IsInt64() {
			return appendExactInt(b, variant.PrimitiveInt64, le64(n.Int64()))
		}
		// Too wide for the variant's int64: a scale-0 decimal holds up to
		// 38 digits exactly.
		if len(n.String()) <= 38 || (n.Sign() < 0 && len(n.String()) <= 39) {
			return b.AppendDecimal16(0, decimal128.FromBigInt(n))
		}
		return fmt.Errorf("VARIANT: integer %s does not fit a Parquet Variant", n)
	case duckVariantFloat:
		raw, err := fixedAt(d, off, 4)
		if err != nil {
			return err
		}
		return b.AppendFloat32(math.Float32frombits(binary.LittleEndian.Uint32(raw)))
	case duckVariantDouble:
		raw, err := fixedAt(d, off, 8)
		if err != nil {
			return err
		}
		return b.AppendFloat64(math.Float64frombits(binary.LittleEndian.Uint64(raw)))
	case duckVariantDecimal:
		pos := int(off)
		width, err := uvarint(d, &pos)
		if err != nil {
			return err
		}
		scale, err := uvarint(d, &pos)
		if err != nil {
			return err
		}
		switch {
		case width <= 4:
			raw, err := fixedAt(d, uint64(pos), 2)
			if err != nil {
				return err
			}
			return b.AppendDecimal4(uint8(scale), decimal.Decimal32(int16(binary.LittleEndian.Uint16(raw))))
		case width <= 9:
			raw, err := fixedAt(d, uint64(pos), 4)
			if err != nil {
				return err
			}
			return b.AppendDecimal4(uint8(scale), decimal.Decimal32(int32(binary.LittleEndian.Uint32(raw))))
		case width <= 18:
			raw, err := fixedAt(d, uint64(pos), 8)
			if err != nil {
				return err
			}
			return b.AppendDecimal8(uint8(scale), decimal.Decimal64(int64(binary.LittleEndian.Uint64(raw))))
		default:
			raw, err := fixedAt(d, uint64(pos), 16)
			if err != nil {
				return err
			}
			return b.AppendDecimal16(uint8(scale), decimal128.New(int64(binary.LittleEndian.Uint64(raw[8:])), binary.LittleEndian.Uint64(raw[:8])))
		}
	case duckVariantVarchar, duckVariantBlob, duckVariantGeometry:
		pos := int(off)
		n, err := uvarint(d, &pos)
		if err != nil {
			return err
		}
		raw, err := fixedAt(d, uint64(pos), int(n))
		if err != nil {
			return err
		}
		if t == duckVariantVarchar {
			return b.AppendString(string(raw))
		}
		return b.AppendBinary(raw)
	case duckVariantUUID:
		raw, err := fixedAt(d, off, 16)
		if err != nil {
			return err
		}
		var u uuid.UUID
		binary.BigEndian.PutUint64(u[:8], binary.LittleEndian.Uint64(raw[8:])^(1<<63))
		binary.BigEndian.PutUint64(u[8:], binary.LittleEndian.Uint64(raw[:8]))
		return b.AppendUUID(u)
	case duckVariantDate:
		raw, err := fixedAt(d, off, 4)
		if err != nil {
			return err
		}
		return b.AppendDate(arrow.Date32(int32(binary.LittleEndian.Uint32(raw))))
	case duckVariantTimeMicros:
		raw, err := fixedAt(d, off, 8)
		if err != nil {
			return err
		}
		return b.AppendTimeMicro(arrow.Time64(int64(binary.LittleEndian.Uint64(raw))))
	case duckVariantTimestampSec, duckVariantTimestampMillis, duckVariantTimestampMicros, duckVariantTimestampTZ,
		duckVariantTimestampNanos, duckVariantTimestampNanoTZ:
		raw, err := fixedAt(d, off, 8)
		if err != nil {
			return err
		}
		v := int64(binary.LittleEndian.Uint64(raw))
		switch t {
		case duckVariantTimestampSec:
			v *= 1_000_000 // the variant has no second / millisecond timestamps: widen losslessly
		case duckVariantTimestampMillis:
			v *= 1_000
		}
		micros := t != duckVariantTimestampNanos && t != duckVariantTimestampNanoTZ
		utc := t == duckVariantTimestampTZ || t == duckVariantTimestampNanoTZ
		return b.AppendTimestamp(arrow.Timestamp(v), micros, utc)
	case duckVariantObject, duckVariantArray:
		pos := int(off)
		count, err := uvarint(d, &pos)
		if err != nil {
			return err
		}
		var first uint64
		if count > 0 {
			if first, err = uvarint(d, &pos); err != nil {
				return err
			}
		}
		if first+count > uint64(len(r.children)) {
			return fmt.Errorf("VARIANT: children out of range")
		}
		start := b.Offset()
		if t == duckVariantArray {
			offsets := make([]int, 0, count)
			for i := uint64(0); i < count; i++ {
				offsets = append(offsets, b.NextElement(start))
				if err := r.write(b, uint64(r.children[first+i][1]), depth+1); err != nil {
					return err
				}
			}
			return b.FinishArray(start, offsets)
		}
		fields := make([]variant.FieldEntry, 0, count)
		for i := uint64(0); i < count; i++ {
			c := r.children[first+i]
			if c[0] < 0 || c[0] >= int64(len(r.keys)) {
				return fmt.Errorf("VARIANT: object child without a key")
			}
			fields = append(fields, b.NextField(start, r.keys[c[0]]))
			if err := r.write(b, uint64(c[1]), depth+1); err != nil {
				return err
			}
		}
		return b.FinishObject(start, fields)
	}
	name := duckVariantTypeNames[t]
	if name == "" {
		name = fmt.Sprintf("type %d", t)
	}
	return fmt.Errorf("VARIANT holds a %s value, which a Parquet Variant cannot represent; cast it in the query (for example to VARCHAR)", name)
}

// duckVariantToParquet converts one decoded DuckDB VARIANT row.
func duckVariantToParquet(v interface{}) (variant.Value, error) {
	r, err := parseDuckVariantRow(v)
	if err != nil {
		return variant.Value{}, err
	}
	b := new(variant.Builder)
	b.SetAllowDuplicates(true)
	for _, k := range r.keys {
		b.AddKey(k) // keep DuckDB's dictionary order
	}
	if err := r.write(b, 0, 0); err != nil {
		return variant.Value{}, err
	}
	return b.Build()
}

// --- Parquet Variant -> DuckDB layout (ingest) ---

type duckVariantWriter struct {
	keys     []string
	keyIndex map[string]uint32
	children [][2]int64
	values   [][2]uint64
	data     []byte
}

func (w *duckVariantWriter) key(k string) uint32 {
	if i, ok := w.keyIndex[k]; ok {
		return i
	}
	i := uint32(len(w.keys))
	w.keys = append(w.keys, k)
	w.keyIndex[k] = i
	return i
}

func (w *duckVariantWriter) putUvarint(v uint64) {
	w.data = binary.AppendUvarint(w.data, v)
}

func (w *duckVariantWriter) add(v variant.Value, depth int) (uint64, error) {
	if depth > 1000 {
		return 0, fmt.Errorf("VARIANT: nesting too deep")
	}
	raw := v.Bytes()
	if len(raw) == 0 {
		return 0, fmt.Errorf("VARIANT: empty value")
	}
	vi := uint64(len(w.values))
	w.values = append(w.values, [2]uint64{0, uint64(len(w.data))})
	setType := func(t int) { w.values[vi][0] = uint64(t) }
	basic, info := raw[0]&3, raw[0]>>2
	switch basic {
	case 1: // short string
		setType(duckVariantVarchar)
		s := raw[1 : 1+int(info)]
		w.putUvarint(uint64(len(s)))
		w.data = append(w.data, s...)
		return vi, nil
	case 2: // object
		setType(duckVariantObject)
		obj := v.Value().(variant.ObjectValue)
		n := obj.NumElements()
		base := len(w.children)
		w.putUvarint(uint64(n))
		if n > 0 {
			w.putUvarint(uint64(base))
		}
		w.children = append(w.children, make([][2]int64, n)...)
		for i := uint32(0); i < n; i++ {
			f, err := obj.FieldAt(i)
			if err != nil {
				return 0, err
			}
			ki := w.key(f.Key)
			cvi, err := w.add(f.Value, depth+1)
			if err != nil {
				return 0, err
			}
			w.children[base+int(i)] = [2]int64{int64(ki), int64(cvi)}
		}
		return vi, nil
	case 3: // array
		setType(duckVariantArray)
		arr := v.Value().(variant.ArrayValue)
		n := arr.Len()
		base := len(w.children)
		w.putUvarint(uint64(n))
		if n > 0 {
			w.putUvarint(uint64(base))
		}
		w.children = append(w.children, make([][2]int64, n)...)
		for i := uint32(0); i < n; i++ {
			e, err := arr.Value(i)
			if err != nil {
				return 0, err
			}
			cvi, err := w.add(e, depth+1)
			if err != nil {
				return 0, err
			}
			w.children[base+int(i)] = [2]int64{-1, int64(cvi)}
		}
		return vi, nil
	}
	p := raw[1:]
	switch variant.PrimitiveType(info) {
	case variant.PrimitiveNull:
		setType(duckVariantNull)
	case variant.PrimitiveBoolTrue:
		setType(duckVariantTrue)
	case variant.PrimitiveBoolFalse:
		setType(duckVariantFalse)
	case variant.PrimitiveInt8:
		setType(duckVariantInt8)
		w.data = append(w.data, p[:1]...)
	case variant.PrimitiveInt16:
		setType(duckVariantInt16)
		w.data = append(w.data, p[:2]...)
	case variant.PrimitiveInt32:
		setType(duckVariantInt32)
		w.data = append(w.data, p[:4]...)
	case variant.PrimitiveInt64:
		setType(duckVariantInt64)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveFloat:
		setType(duckVariantFloat)
		w.data = append(w.data, p[:4]...)
	case variant.PrimitiveDouble:
		setType(duckVariantDouble)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveDecimal4, variant.PrimitiveDecimal8, variant.PrimitiveDecimal16:
		setType(duckVariantDecimal)
		// The Parquet Variant's decimal4/8/16 hold up to 9/18/38 digits;
		// DuckDB stores DECIMAL(9|18|38, scale) in int32/int64/int128.
		width, size := 9, 4
		switch variant.PrimitiveType(info) {
		case variant.PrimitiveDecimal8:
			width, size = 18, 8
		case variant.PrimitiveDecimal16:
			width, size = 38, 16
		}
		w.putUvarint(uint64(width))
		w.putUvarint(uint64(p[0]))
		w.data = append(w.data, p[1:1+size]...)
	case variant.PrimitiveDate:
		setType(duckVariantDate)
		w.data = append(w.data, p[:4]...)
	case variant.PrimitiveTimeMicrosNTZ:
		setType(duckVariantTimeMicros)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveTimestampMicros:
		setType(duckVariantTimestampTZ)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveTimestampMicrosNTZ:
		setType(duckVariantTimestampMicros)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveTimestampNanos:
		setType(duckVariantTimestampNanoTZ)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveTimestampNanosNTZ:
		setType(duckVariantTimestampNanos)
		w.data = append(w.data, p[:8]...)
	case variant.PrimitiveString, variant.PrimitiveBinary:
		if variant.PrimitiveType(info) == variant.PrimitiveString {
			setType(duckVariantVarchar)
		} else {
			setType(duckVariantBlob)
		}
		n := binary.LittleEndian.Uint32(p[:4])
		w.putUvarint(uint64(n))
		w.data = append(w.data, p[4:4+n]...)
	case variant.PrimitiveUUID:
		setType(duckVariantUUID)
		var buf [16]byte
		binary.LittleEndian.PutUint64(buf[:8], binary.BigEndian.Uint64(p[8:16]))
		binary.LittleEndian.PutUint64(buf[8:], binary.BigEndian.Uint64(p[:8])^(1<<63))
		w.data = append(w.data, buf[:]...)
	default:
		return 0, fmt.Errorf("VARIANT: unsupported Parquet Variant primitive type %d", info)
	}
	return vi, nil
}

// encodeVariantArrow writes an arrow.parquet.variant column as a DuckDB
// VARIANT vector: it builds the four storage columns as Arrow arrays and
// encodes them as the VARIANT's STRUCT.
func encodeVariantArrow(w *codec.BinaryWriter, arr arrow.Array) error {
	va, ok := arr.(*extensions.VariantArray)
	if !ok {
		// Imported through the C Data interface, the column is its storage
		// struct with the extension in field metadata: wrap it again.
		vt, err := extensions.NewVariantType(arr.DataType())
		if err != nil {
			return fmt.Errorf("cannot ingest %s as VARIANT: %w", arr.DataType(), err)
		}
		ext := array.NewExtensionArrayWithStorage(vt, arr)
		defer ext.Release()
		if va, ok = ext.(*extensions.VariantArray); !ok {
			return fmt.Errorf("cannot ingest %s as VARIANT", arr.DataType())
		}
	}
	mem := memory.DefaultAllocator
	u32 := arrow.PrimitiveTypes.Uint32
	childType := arrow.StructOf(arrow.Field{Name: "keys_index", Type: u32, Nullable: true}, arrow.Field{Name: "values_index", Type: u32, Nullable: true})
	valueType := arrow.StructOf(arrow.Field{Name: "type_id", Type: arrow.PrimitiveTypes.Uint8, Nullable: true}, arrow.Field{Name: "byte_offset", Type: u32, Nullable: true})
	storage := arrow.StructOf(
		arrow.Field{Name: "keys", Type: arrow.ListOf(arrow.BinaryTypes.String), Nullable: true},
		arrow.Field{Name: "children", Type: arrow.ListOf(childType), Nullable: true},
		arrow.Field{Name: "values", Type: arrow.ListOf(valueType), Nullable: true},
		arrow.Field{Name: "data", Type: arrow.BinaryTypes.Binary, Nullable: true},
	)
	sb := array.NewStructBuilder(mem, storage)
	defer sb.Release()
	keysB := sb.FieldBuilder(0).(*array.ListBuilder)
	childrenB := sb.FieldBuilder(1).(*array.ListBuilder)
	valuesB := sb.FieldBuilder(2).(*array.ListBuilder)
	dataB := sb.FieldBuilder(3).(*array.BinaryBuilder)
	for i := 0; i < va.Len(); i++ {
		if va.IsNull(i) {
			sb.AppendNull()
			keysB.AppendNull()
			childrenB.AppendNull()
			valuesB.AppendNull()
			dataB.AppendNull()
			continue
		}
		v, err := va.Value(i)
		if err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		dw := &duckVariantWriter{keyIndex: map[string]uint32{}}
		// Keep the Parquet metadata dictionary's key order, so a value read
		// back is byte-identical to the one written.
		meta := v.Metadata()
		for k := uint32(0); ; k++ {
			key, err := meta.KeyAt(k)
			if err != nil {
				break
			}
			dw.key(key)
		}
		if _, err := dw.add(v, 0); err != nil {
			return fmt.Errorf("row %d: %w", i, err)
		}
		sb.Append(true)
		keysB.Append(true)
		kb := keysB.ValueBuilder().(*array.StringBuilder)
		for _, k := range dw.keys {
			kb.Append(k)
		}
		childrenB.Append(true)
		cb := childrenB.ValueBuilder().(*array.StructBuilder)
		for _, c := range dw.children {
			cb.Append(true)
			if c[0] < 0 {
				cb.FieldBuilder(0).AppendNull()
			} else {
				cb.FieldBuilder(0).(*array.Uint32Builder).Append(uint32(c[0]))
			}
			cb.FieldBuilder(1).(*array.Uint32Builder).Append(uint32(c[1]))
		}
		valuesB.Append(true)
		vb := valuesB.ValueBuilder().(*array.StructBuilder)
		for _, e := range dw.values {
			vb.Append(true)
			vb.FieldBuilder(0).(*array.Uint8Builder).Append(uint8(e[0]))
			vb.FieldBuilder(1).(*array.Uint32Builder).Append(uint32(e[1]))
		}
		dataB.Append(dw.data)
	}
	st := sb.NewStructArray()
	defer st.Release()
	return encodeStruct(w, variantLogicalType(), st)
}
