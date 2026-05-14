package message

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// VectorType identifies the on-wire encoding of a column vector.
type VectorType int

const (
	VectorTypeFlat       VectorType = 0
	VectorTypeFSST       VectorType = 1
	VectorTypeConstant   VectorType = 2
	VectorTypeDictionary VectorType = 3
	VectorTypeSequence   VectorType = 4
)

// ErrUnsupportedVector signals an encoding we don't (yet) handle.
var ErrUnsupportedVector = errors.New("message: unsupported vector encoding")

// DecodeDataChunkWrapper reads the field-300 wrapper that surrounds an
// inline DataChunk in PREPARE_RESPONSE / FETCH_RESPONSE.
func DecodeDataChunkWrapper(r *codec.BinaryReader) DataChunk {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) DataChunk {
		return codec.ReadRequiredField(rr, 300, DecodeDataChunk)
	})
	return v
}

// DecodeDataChunk reads the body of a DataChunk.
func DecodeDataChunk(r *codec.BinaryReader) DataChunk {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) DataChunk {
		rowCount := codec.ReadRequiredField(rr, 100, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() })
		types := codec.ReadRequiredField(rr, 101, func(rrr *codec.BinaryReader) []quacktype.LogicalType {
			return codec.ReadList(rrr, func(_ int, rrrr *codec.BinaryReader) quacktype.LogicalType {
				return quacktype.DecodeLogicalType(rrrr)
			})
		})
		columns := codec.ReadRequiredField(rr, 102, func(rrr *codec.BinaryReader) []DecodedVector {
			return codec.ReadList(rrr, func(i int, rrrr *codec.BinaryReader) DecodedVector {
				if i >= len(types) {
					rrrr.AssertEOF() // force error
					return nil
				}
				return DecodeVector(rrrr, types[i], rowCount)
			})
		})
		if len(columns) != len(types) {
			rr.AssertEOF()
		}
		return DataChunk{RowCount: rowCount, Types: types, Columns: columns}
	})
	return v
}

// DecodeVector reads a single column vector with a known logical type / row count.
func DecodeVector(r *codec.BinaryReader, t quacktype.LogicalType, count int) DecodedVector {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) DecodedVector {
		return decodeVectorBody(rr, t, count)
	})
	return v
}

func decodeVectorBody(r *codec.BinaryReader, t quacktype.LogicalType, count int) DecodedVector {
	vectorType := VectorType(codec.ReadOptionalField(r, 90, func(rr *codec.BinaryReader) int { return rr.ReadULEB128Int() }, int(VectorTypeFlat)))
	switch vectorType {
	case VectorTypeFlat:
		return decodeFlatVector(r, t, count)
	case VectorTypeFSST:
		// store a sentinel error in the reader's sticky path
		r.AssertEOF()
		return nil
	case VectorTypeConstant:
		// nested decode of single value, then broadcast
		single := decodeVectorBody(r, t, minOne(count))
		var value interface{}
		if single != nil && single.Size() > 0 {
			value = single.GetObject(0)
		}
		return broadcastValue(t, value, count)
	case VectorTypeDictionary:
		selection := codec.ReadRequiredField(r, 91, func(rr *codec.BinaryReader) []int32 {
			return readSelectionVector(rr, count)
		})
		dictCount := codec.ReadRequiredField(r, 92, func(rr *codec.BinaryReader) int { return rr.ReadULEB128Int() })
		dict := decodeVectorBody(r, t, dictCount)
		return projectVector(t, dict, selection)
	case VectorTypeSequence:
		start := codec.ReadRequiredField(r, 91, func(rr *codec.BinaryReader) int64 { return rr.ReadSLEB128() })
		incr := codec.ReadRequiredField(r, 92, func(rr *codec.BinaryReader) int64 { return rr.ReadSLEB128() })
		return sequenceVector(t, count, start, incr)
	}
	r.AssertEOF()
	return nil
}

func decodeFlatVector(r *codec.BinaryReader, t quacktype.LogicalType, count int) DecodedVector {
	if t.ID == quacktype.LogicalTypeIDGeometry && !r.EOF() && r.PeekFieldID() == 99 {
		codec.ReadRequiredField(r, 99, func(rr *codec.BinaryReader) int { return rr.ReadULEB128Int() })
	}
	hasValidity := codec.ReadRequiredField(r, 100, func(rr *codec.BinaryReader) bool { return rr.ReadBool() })
	var validity []uint64
	if hasValidity {
		validity = codec.ReadRequiredField(r, 101, func(rr *codec.BinaryReader) []uint64 {
			return readValidityMask(rr, count)
		})
	}

	physical := quacktype.GetPhysicalType(t)
	if physical.IsConstantSize() {
		byteLen := physical.ByteWidth() * count
		bytes := codec.ReadRequiredField(r, 102, func(rr *codec.BinaryReader) []byte { return rr.ReadBlob() })
		if len(bytes) != byteLen {
			r.AssertEOF()
			return nil
		}
		return decodeFixedFlatVector(t, physical, bytes, count, validity)
	}

	switch physical {
	case quacktype.PhysicalTypeVarchar:
		raw := codec.ReadRequiredField(r, 102, func(rr *codec.BinaryReader) [][]byte {
			return codec.ReadList(rr, func(_ int, rrr *codec.BinaryReader) []byte { return rrr.ReadStringBytes() })
		})
		values := make([]interface{}, len(raw))
		for i, b := range raw {
			if ValidityIsValid(validity, i) {
				values[i] = decodeStringLikeValue(t, b)
			}
		}
		return ObjectVec{TypeRef: t, Values: values}
	case quacktype.PhysicalTypeStruct:
		children, _ := quacktype.GetStructChildren(t)
		childVecs := codec.ReadRequiredField(r, 103, func(rr *codec.BinaryReader) []DecodedVector {
			return codec.ReadList(rr, func(i int, rrr *codec.BinaryReader) DecodedVector {
				if i >= len(children) {
					rrr.AssertEOF()
					return nil
				}
				return DecodeVector(rrr, children[i].Type, count)
			})
		})
		values := make([]interface{}, count)
		for row := 0; row < count; row++ {
			if !ValidityIsValid(validity, row) {
				continue
			}
			rowMap := make(map[string]interface{}, len(children))
			for c, child := range children {
				if c < len(childVecs) && childVecs[c] != nil {
					rowMap[child.Name] = childVecs[c].GetObject(row)
				}
			}
			values[row] = rowMap
		}
		return ObjectVec{TypeRef: t, Values: values}
	case quacktype.PhysicalTypeList:
		listSize := codec.ReadRequiredField(r, 104, func(rr *codec.BinaryReader) int { return rr.ReadULEB128Int() })
		entries := codec.ReadRequiredField(r, 105, func(rr *codec.BinaryReader) []listEntry {
			return readListEntries(rr, count)
		})
		childType, _ := quacktype.GetChildType(t)
		childVec := codec.ReadRequiredField(r, 106, func(rr *codec.BinaryReader) DecodedVector {
			return DecodeVector(rr, childType, listSize)
		})
		values := make([]interface{}, count)
		for row, e := range entries {
			if !ValidityIsValid(validity, row) {
				continue
			}
			slice := make([]interface{}, e.length)
			for k := 0; k < e.length; k++ {
				slice[k] = childVec.GetObject(e.offset + k)
			}
			values[row] = slice
		}
		return ObjectVec{TypeRef: t, Values: values}
	case quacktype.PhysicalTypeArray:
		arrSize := codec.ReadRequiredField(r, 103, func(rr *codec.BinaryReader) int { return rr.ReadULEB128Int() })
		expected, _ := quacktype.GetArraySize(t)
		if arrSize != expected {
			r.AssertEOF()
			return nil
		}
		childType, _ := quacktype.GetChildType(t)
		childVec := codec.ReadRequiredField(r, 104, func(rr *codec.BinaryReader) DecodedVector {
			return DecodeVector(rr, childType, arrSize*count)
		})
		values := make([]interface{}, count)
		for row := 0; row < count; row++ {
			if !ValidityIsValid(validity, row) {
				continue
			}
			off := row * arrSize
			slice := make([]interface{}, arrSize)
			for k := 0; k < arrSize; k++ {
				slice[k] = childVec.GetObject(off + k)
			}
			values[row] = slice
		}
		return ObjectVec{TypeRef: t, Values: values}
	}
	r.AssertEOF()
	return nil
}

// needsObjectMaterialization is the same predicate from the JDBC port:
// logical types whose Java/Go representation is not a primitive go
// through ObjectVec even for fixed-size physical types.
func needsObjectMaterialization(t quacktype.LogicalType, physical quacktype.PhysicalType) bool {
	switch t.ID {
	case quacktype.LogicalTypeIDDecimal, quacktype.LogicalTypeIDDate,
		quacktype.LogicalTypeIDTime, quacktype.LogicalTypeIDTimeNS, quacktype.LogicalTypeIDTimeTZ,
		quacktype.LogicalTypeIDTimestamp, quacktype.LogicalTypeIDTimestampSec,
		quacktype.LogicalTypeIDTimestampMS, quacktype.LogicalTypeIDTimestampNS,
		quacktype.LogicalTypeIDTimestampTZ,
		quacktype.LogicalTypeIDUUID, quacktype.LogicalTypeIDInterval,
		quacktype.LogicalTypeIDHugeInt, quacktype.LogicalTypeIDUHugeInt,
		quacktype.LogicalTypeIDEnum:
		return true
	}
	return physical == quacktype.PhysicalTypeInterval ||
		physical == quacktype.PhysicalTypeInt128 ||
		physical == quacktype.PhysicalTypeUint128
}

func decodeFixedFlatVector(t quacktype.LogicalType, physical quacktype.PhysicalType,
	bytes []byte, count int, validity []uint64) DecodedVector {
	rr := codec.NewBinaryReader(bytes)

	if needsObjectMaterialization(t, physical) {
		values := make([]interface{}, count)
		for i := 0; i < count; i++ {
			v := decodeFixedValue(rr, t, physical)
			if ValidityIsValid(validity, i) {
				values[i] = v
			}
		}
		return ObjectVec{TypeRef: t, Values: values}
	}

	switch physical {
	case quacktype.PhysicalTypeBool:
		arr := make([]bool, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedUint8() != 0
		}
		return BoolVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeInt8:
		arr := make([]int8, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedInt8()
		}
		return ByteVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeUint8:
		arr := make([]int16, count)
		for i := 0; i < count; i++ {
			arr[i] = int16(rr.ReadFixedUint8())
		}
		return ShortVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeInt16:
		arr := make([]int16, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedInt16()
		}
		return ShortVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeUint16:
		arr := make([]int32, count)
		for i := 0; i < count; i++ {
			arr[i] = int32(rr.ReadFixedUint16())
		}
		return IntVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeInt32:
		arr := make([]int32, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedInt32()
		}
		return IntVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeUint32:
		arr := make([]int64, count)
		for i := 0; i < count; i++ {
			arr[i] = int64(rr.ReadFixedUint32())
		}
		return LongVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeInt64, quacktype.PhysicalTypeUint64:
		arr := make([]int64, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedInt64()
		}
		return LongVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeFloat:
		arr := make([]float32, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedFloat32()
		}
		return FloatVec{TypeRef: t, Values: arr, Validity: validity}
	case quacktype.PhysicalTypeDouble:
		arr := make([]float64, count)
		for i := 0; i < count; i++ {
			arr[i] = rr.ReadFixedFloat64()
		}
		return DoubleVec{TypeRef: t, Values: arr, Validity: validity}
	}
	return ObjectVec{TypeRef: t, Values: make([]interface{}, count)}
}

func decodeFixedValue(r *codec.BinaryReader, t quacktype.LogicalType, physical quacktype.PhysicalType) interface{} {
	switch physical {
	case quacktype.PhysicalTypeBool:
		return r.ReadFixedUint8() != 0
	case quacktype.PhysicalTypeInt8:
		return int32(r.ReadFixedInt8())
	case quacktype.PhysicalTypeUint8:
		return decodeEnumOrInt(t, int(r.ReadFixedUint8()))
	case quacktype.PhysicalTypeInt16:
		v := r.ReadFixedInt16()
		if t.ID == quacktype.LogicalTypeIDDecimal {
			return decimalFromUnscaled(t, big.NewInt(int64(v)))
		}
		return int32(v)
	case quacktype.PhysicalTypeUint16:
		return decodeEnumOrInt(t, int(r.ReadFixedUint16()))
	case quacktype.PhysicalTypeInt32:
		v := r.ReadFixedInt32()
		if t.ID == quacktype.LogicalTypeIDDate {
			return time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(v))
		}
		if t.ID == quacktype.LogicalTypeIDDecimal {
			return decimalFromUnscaled(t, big.NewInt(int64(v)))
		}
		return v
	case quacktype.PhysicalTypeUint32:
		return decodeEnumOrInt64(t, int64(r.ReadFixedUint32()))
	case quacktype.PhysicalTypeInt64:
		return decodeInt64LogicalValue(t, r.ReadFixedInt64())
	case quacktype.PhysicalTypeUint64:
		return r.ReadFixedUint64()
	case quacktype.PhysicalTypeFloat:
		return r.ReadFixedFloat32()
	case quacktype.PhysicalTypeDouble:
		return r.ReadFixedFloat64()
	case quacktype.PhysicalTypeInt128:
		lower := r.ReadFixedUint64()
		upper := r.ReadFixedInt64()
		if t.ID == quacktype.LogicalTypeIDUUID {
			return uuidFromHugeIntParts(upper, lower)
		}
		signed := codec.HugeIntParts{Upper: upper, Lower: lower}.SignedBigInt()
		if t.ID == quacktype.LogicalTypeIDDecimal {
			return decimalFromUnscaled(t, signed)
		}
		return signed
	case quacktype.PhysicalTypeUint128:
		lower := r.ReadFixedUint64()
		upper := r.ReadFixedUint64()
		return codec.HugeIntParts{Upper: int64(upper), Lower: lower}.UnsignedBigInt()
	case quacktype.PhysicalTypeInterval:
		return IntervalValue{
			Months: r.ReadFixedInt32(),
			Days:   r.ReadFixedInt32(),
			Micros: r.ReadFixedInt64(),
		}
	}
	return nil
}

func decodeInt64LogicalValue(t quacktype.LogicalType, value int64) interface{} {
	switch t.ID {
	case quacktype.LogicalTypeIDTime:
		return time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(value) * time.Microsecond)
	case quacktype.LogicalTypeIDTimeNS:
		return time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(value))
	case quacktype.LogicalTypeIDTimeTZ:
		return value
	case quacktype.LogicalTypeIDTimestampSec:
		return time.Unix(value, 0).UTC()
	case quacktype.LogicalTypeIDTimestampMS:
		return time.UnixMilli(value).UTC()
	case quacktype.LogicalTypeIDTimestamp:
		return time.UnixMicro(value).UTC()
	case quacktype.LogicalTypeIDTimestampNS:
		return time.Unix(value/1_000_000_000, value%1_000_000_000).UTC()
	case quacktype.LogicalTypeIDTimestampTZ:
		return time.UnixMicro(value).UTC()
	case quacktype.LogicalTypeIDDecimal:
		return decimalFromUnscaled(t, big.NewInt(value))
	}
	return value
}

func decodeStringLikeValue(t quacktype.LogicalType, raw []byte) interface{} {
	switch t.ID {
	case quacktype.LogicalTypeIDBlob, quacktype.LogicalTypeIDGeometry, quacktype.LogicalTypeIDBit:
		out := make([]byte, len(raw))
		copy(out, raw)
		return out
	}
	return string(raw)
}

func decimalFromUnscaled(t quacktype.LogicalType, value *big.Int) *big.Rat {
	info, ok := t.TypeInfo.(quacktype.DecimalTypeInfo)
	if !ok {
		return new(big.Rat).SetInt(value)
	}
	denom := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(info.Scale)), nil)
	return new(big.Rat).SetFrac(value, denom)
}

func decodeEnumOrInt(t quacktype.LogicalType, idx int) interface{} {
	if t.ID != quacktype.LogicalTypeIDEnum {
		return int32(idx)
	}
	values, _ := quacktype.GetEnumValues(t)
	if idx < 0 || idx >= len(values) {
		return idx
	}
	return values[idx]
}

func decodeEnumOrInt64(t quacktype.LogicalType, idx int64) interface{} {
	if t.ID != quacktype.LogicalTypeIDEnum {
		return idx
	}
	if idx < 0 || idx > 0x7FFFFFFF {
		return idx
	}
	return decodeEnumOrInt(t, int(idx))
}

// uuidFromHugeIntParts converts DuckDB's INT128-stored UUID back to a
// hex string representation. (DuckDB XORs the sign bit of the upper 64
// bits before storage; we XOR it back.)
func uuidFromHugeIntParts(upper int64, lower uint64) string {
	display := uint64(upper) ^ (1 << 63)
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], display)
	binary.BigEndian.PutUint64(b[8:16], lower)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// broadcastValue materializes a single value across count rows. Honors
// typed vector targets when the logical/physical pair maps to a primitive.
func broadcastValue(t quacktype.LogicalType, value interface{}, count int) DecodedVector {
	physical := quacktype.GetPhysicalType(t)
	if needsObjectMaterialization(t, physical) || !physical.IsConstantSize() {
		values := make([]interface{}, count)
		if value != nil {
			for i := range values {
				values[i] = value
			}
		}
		return ObjectVec{TypeRef: t, Values: values}
	}
	if value == nil {
		// all-null validity (zeros) of the right typed primitive vector
		validity := make([]uint64, ValidityWordCount(count))
		return zeroFilledPrimitiveVector(t, physical, count, validity)
	}
	switch physical {
	case quacktype.PhysicalTypeBool:
		arr := make([]bool, count)
		b := toBool(value)
		for i := range arr {
			arr[i] = b
		}
		return BoolVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeInt8:
		arr := make([]int8, count)
		v := int8(toInt64(value))
		for i := range arr {
			arr[i] = v
		}
		return ByteVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeUint8, quacktype.PhysicalTypeInt16:
		arr := make([]int16, count)
		v := int16(toInt64(value))
		for i := range arr {
			arr[i] = v
		}
		return ShortVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeUint16, quacktype.PhysicalTypeInt32:
		arr := make([]int32, count)
		v := int32(toInt64(value))
		for i := range arr {
			arr[i] = v
		}
		return IntVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeUint32, quacktype.PhysicalTypeInt64, quacktype.PhysicalTypeUint64:
		arr := make([]int64, count)
		v := toInt64(value)
		for i := range arr {
			arr[i] = v
		}
		return LongVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeFloat:
		arr := make([]float32, count)
		v := float32(toFloat64(value))
		for i := range arr {
			arr[i] = v
		}
		return FloatVec{TypeRef: t, Values: arr}
	case quacktype.PhysicalTypeDouble:
		arr := make([]float64, count)
		v := toFloat64(value)
		for i := range arr {
			arr[i] = v
		}
		return DoubleVec{TypeRef: t, Values: arr}
	}
	values := make([]interface{}, count)
	for i := range values {
		values[i] = value
	}
	return ObjectVec{TypeRef: t, Values: values}
}

func zeroFilledPrimitiveVector(t quacktype.LogicalType, physical quacktype.PhysicalType, count int, validity []uint64) DecodedVector {
	switch physical {
	case quacktype.PhysicalTypeBool:
		return BoolVec{TypeRef: t, Values: make([]bool, count), Validity: validity}
	case quacktype.PhysicalTypeInt8:
		return ByteVec{TypeRef: t, Values: make([]int8, count), Validity: validity}
	case quacktype.PhysicalTypeUint8, quacktype.PhysicalTypeInt16:
		return ShortVec{TypeRef: t, Values: make([]int16, count), Validity: validity}
	case quacktype.PhysicalTypeUint16, quacktype.PhysicalTypeInt32:
		return IntVec{TypeRef: t, Values: make([]int32, count), Validity: validity}
	case quacktype.PhysicalTypeUint32, quacktype.PhysicalTypeInt64, quacktype.PhysicalTypeUint64:
		return LongVec{TypeRef: t, Values: make([]int64, count), Validity: validity}
	case quacktype.PhysicalTypeFloat:
		return FloatVec{TypeRef: t, Values: make([]float32, count), Validity: validity}
	case quacktype.PhysicalTypeDouble:
		return DoubleVec{TypeRef: t, Values: make([]float64, count), Validity: validity}
	}
	return ObjectVec{TypeRef: t, Values: make([]interface{}, count)}
}

// projectVector applies a dictionary selection vector to a decoded dictionary.
func projectVector(t quacktype.LogicalType, dict DecodedVector, selection []int32) DecodedVector {
	count := len(selection)
	switch d := dict.(type) {
	case BoolVec:
		arr := make([]bool, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return BoolVec{TypeRef: t, Values: arr, Validity: validity}
	case ByteVec:
		arr := make([]int8, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return ByteVec{TypeRef: t, Values: arr, Validity: validity}
	case ShortVec:
		arr := make([]int16, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return ShortVec{TypeRef: t, Values: arr, Validity: validity}
	case IntVec:
		arr := make([]int32, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return IntVec{TypeRef: t, Values: arr, Validity: validity}
	case LongVec:
		arr := make([]int64, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return LongVec{TypeRef: t, Values: arr, Validity: validity}
	case FloatVec:
		arr := make([]float32, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return FloatVec{TypeRef: t, Values: arr, Validity: validity}
	case DoubleVec:
		arr := make([]float64, count)
		var validity []uint64
		for i, idx := range selection {
			if d.IsNull(int(idx)) {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			} else {
				arr[i] = d.Values[idx]
			}
		}
		return DoubleVec{TypeRef: t, Values: arr, Validity: validity}
	}
	values := make([]interface{}, count)
	for i, idx := range selection {
		values[i] = dict.GetObject(int(idx))
	}
	return ObjectVec{TypeRef: t, Values: values}
}

func sequenceVector(t quacktype.LogicalType, count int, start, incr int64) DecodedVector {
	switch t.ID {
	case quacktype.LogicalTypeIDInteger:
		arr := make([]int32, count)
		v := start
		for i := range arr {
			arr[i] = int32(v)
			v += incr
		}
		return IntVec{TypeRef: t, Values: arr}
	case quacktype.LogicalTypeIDBigInt:
		arr := make([]int64, count)
		v := start
		for i := range arr {
			arr[i] = v
			v += incr
		}
		return LongVec{TypeRef: t, Values: arr}
	}
	values := make([]interface{}, count)
	v := start
	for i := range values {
		values[i] = decodeInt64LogicalValue(t, v)
		v += incr
	}
	return ObjectVec{TypeRef: t, Values: values}
}

// ---- helpers ----

type listEntry struct{ offset, length int }

func readSelectionVector(r *codec.BinaryReader, count int) []int32 {
	expected := count * 4
	bytes := r.ReadBlob()
	if len(bytes) != expected {
		r.AssertEOF()
		return nil
	}
	out := make([]int32, count)
	for i := 0; i < count; i++ {
		out[i] = int32(binary.LittleEndian.Uint32(bytes[i*4:]))
	}
	return out
}

func readValidityMask(r *codec.BinaryReader, count int) []uint64 {
	expected := ValidityWireByteCount(count)
	bytes := r.ReadBlob()
	if len(bytes) != expected {
		r.AssertEOF()
		return nil
	}
	return ValidityFromBytes(bytes, count)
}

func readListEntries(r *codec.BinaryReader, count int) []listEntry {
	entries := codec.ReadList(r, func(_ int, rr *codec.BinaryReader) listEntry {
		v, _ := codec.ReadObject(rr, func(obj *codec.BinaryReader) listEntry {
			off := codec.ReadRequiredField(obj, 100, func(o *codec.BinaryReader) int { return o.ReadULEB128Int() })
			ln := codec.ReadRequiredField(obj, 101, func(o *codec.BinaryReader) int { return o.ReadULEB128Int() })
			return listEntry{offset: off, length: ln}
		})
		return v
	})
	if len(entries) != count {
		r.AssertEOF()
		return nil
	}
	return entries
}

func minOne(c int) int {
	if c > 0 {
		return 1
	}
	return 0
}

func toBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	if n, ok := toInt64Ok(v); ok {
		return n != 0
	}
	return false
}

func toInt64(v interface{}) int64 {
	n, _ := toInt64Ok(v)
	return n
}

func toInt64Ok(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func toFloat64(v interface{}) float64 {
	switch x := v.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	}
	if n, ok := toInt64Ok(v); ok {
		return float64(n)
	}
	return 0
}
