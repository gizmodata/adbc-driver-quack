package message

import (
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// DecodedVector is one decoded column of a DataChunk.
//
// Backed by primitive slices for fixed-width numeric / boolean physical
// types to avoid per-row boxing; logical types whose materialized Java/Go
// representation isn't a primitive (DATE -> time.Time, DECIMAL ->
// *big.Rat or string, VARCHAR / BLOB / nested types, etc.) fall through
// to ObjectVec.
type DecodedVector interface {
	Type() quacktype.LogicalType
	Size() int
	IsNull(row int) bool
	GetObject(row int) interface{}
	decodedVector() // sealed within this package
}

type BoolVec struct {
	TypeRef  quacktype.LogicalType
	Values   []bool
	Validity []uint64
}

func (v BoolVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v BoolVec) Size() int                   { return len(v.Values) }
func (v BoolVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v BoolVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (BoolVec) decodedVector() {}

type ByteVec struct {
	TypeRef  quacktype.LogicalType
	Values   []int8
	Validity []uint64
}

func (v ByteVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v ByteVec) Size() int                   { return len(v.Values) }
func (v ByteVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v ByteVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (ByteVec) decodedVector() {}

type ShortVec struct {
	TypeRef  quacktype.LogicalType
	Values   []int16
	Validity []uint64
}

func (v ShortVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v ShortVec) Size() int                   { return len(v.Values) }
func (v ShortVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v ShortVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (ShortVec) decodedVector() {}

type IntVec struct {
	TypeRef  quacktype.LogicalType
	Values   []int32
	Validity []uint64
}

func (v IntVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v IntVec) Size() int                   { return len(v.Values) }
func (v IntVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v IntVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (IntVec) decodedVector() {}

type LongVec struct {
	TypeRef  quacktype.LogicalType
	Values   []int64
	Validity []uint64
}

func (v LongVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v LongVec) Size() int                   { return len(v.Values) }
func (v LongVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v LongVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (LongVec) decodedVector() {}

type FloatVec struct {
	TypeRef  quacktype.LogicalType
	Values   []float32
	Validity []uint64
}

func (v FloatVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v FloatVec) Size() int                   { return len(v.Values) }
func (v FloatVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v FloatVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (FloatVec) decodedVector() {}

type DoubleVec struct {
	TypeRef  quacktype.LogicalType
	Values   []float64
	Validity []uint64
}

func (v DoubleVec) Type() quacktype.LogicalType { return v.TypeRef }
func (v DoubleVec) Size() int                   { return len(v.Values) }
func (v DoubleVec) IsNull(row int) bool         { return ValidityIsNull(v.Validity, row) }
func (v DoubleVec) GetObject(row int) interface{} {
	if v.IsNull(row) {
		return nil
	}
	return v.Values[row]
}
func (DoubleVec) decodedVector() {}

// ObjectVec is the fallback for any logical type whose materialized form
// is not a primitive — VARCHAR/BLOB (strings + []byte), DATE/TIMESTAMP
// (time.Time), DECIMAL (string), UUID, INTERVAL, HUGEINT (math/big),
// STRUCT (map[string]interface{}), LIST/ARRAY ([]interface{}), ENUM (string).
type ObjectVec struct {
	TypeRef quacktype.LogicalType
	Values  []interface{}
}

func (v ObjectVec) Type() quacktype.LogicalType   { return v.TypeRef }
func (v ObjectVec) Size() int                     { return len(v.Values) }
func (v ObjectVec) IsNull(row int) bool           { return v.Values[row] == nil }
func (v ObjectVec) GetObject(row int) interface{} { return v.Values[row] }
func (ObjectVec) decodedVector()                  {}

// DataChunk is one batch of Quack result rows.
type DataChunk struct {
	RowCount int
	Types    []quacktype.LogicalType
	Columns  []DecodedVector
}

// IntervalValue mirrors DuckDB's INTERVAL (months + days + microseconds).
type IntervalValue struct {
	Months int32
	Days   int32
	Micros int64
}
