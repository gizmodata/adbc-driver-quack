package message

import (
	"fmt"
	"math/big"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// EncodeDataChunkWrapper writes the field-300 wrapper around a DataChunk.
func EncodeDataChunkWrapper(w *codec.BinaryWriter, chunk DataChunk) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(300, func(o *codec.BinaryWriter) { EncodeDataChunk(o, chunk) })
	})
}

// EncodeDataChunk writes the body of a DataChunk.
func EncodeDataChunk(w *codec.BinaryWriter, chunk DataChunk) {
	if len(chunk.Types) != len(chunk.Columns) {
		panic("EncodeDataChunk: type count must match column count")
	}
	for i, col := range chunk.Columns {
		if col.Size() != chunk.RowCount {
			panic(fmt.Sprintf("EncodeDataChunk: column %d size %d, chunk rowCount %d", i, col.Size(), chunk.RowCount))
		}
	}
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(chunk.RowCount)) })
		obj.WriteField(101, func(o *codec.BinaryWriter) {
			codec.WriteList(o, chunk.Types, func(_ int, t quacktype.LogicalType, ww *codec.BinaryWriter) {
				quacktype.EncodeLogicalType(ww, t)
			})
		})
		obj.WriteField(102, func(o *codec.BinaryWriter) {
			codec.WriteList(o, chunk.Columns, func(i int, c DecodedVector, ww *codec.BinaryWriter) {
				EncodeVector(ww, chunk.Types[i], c)
			})
		})
	})
}

// EncodeVector writes a single column vector to the wire.
func EncodeVector(w *codec.BinaryWriter, t quacktype.LogicalType, vec DecodedVector) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		encodeFlatVectorBody(obj, t, vec)
	})
}

func encodeFlatVectorBody(w *codec.BinaryWriter, t quacktype.LogicalType, vec DecodedVector) {
	count := vec.Size()
	validity := extractValidity(vec, count)
	hasNulls := validity != nil
	w.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(hasNulls) })
	if hasNulls {
		bytes := ValidityToBytes(validity, count)
		w.WriteField(101, func(o *codec.BinaryWriter) { o.WriteBlob(bytes) })
	}
	physical := quacktype.GetPhysicalType(t)
	if physical.IsConstantSize() {
		bytes := encodeFixedBytes(t, physical, vec)
		w.WriteField(102, func(o *codec.BinaryWriter) { o.WriteBlob(bytes) })
		return
	}
	switch physical {
	case quacktype.PhysicalTypeVarchar:
		w.WriteField(102, func(o *codec.BinaryWriter) {
			o.WriteULEB128(uint64(count))
			for i := 0; i < count; i++ {
				o.WriteStringBytes(encodeStringLikeValueForWrite(t, vec.GetObject(i)))
			}
		})
	default:
		// STRUCT/LIST/ARRAY/MAP encoding intentionally not implemented yet.
		// Callers that need nested ingest should construct the wire bytes
		// directly until we round out the encoder.
		panic(fmt.Sprintf("EncodeVector: physical type %d not yet supported", physical))
	}
}

func extractValidity(vec DecodedVector, count int) []uint64 {
	switch v := vec.(type) {
	case BoolVec:
		return v.Validity
	case ByteVec:
		return v.Validity
	case ShortVec:
		return v.Validity
	case IntVec:
		return v.Validity
	case LongVec:
		return v.Validity
	case FloatVec:
		return v.Validity
	case DoubleVec:
		return v.Validity
	case ObjectVec:
		var validity []uint64
		for i, val := range v.Values {
			if val == nil {
				if validity == nil {
					validity = ValidityAllValid(count)
				}
				ValiditySetNull(validity, i)
			}
		}
		return validity
	}
	return nil
}

func encodeFixedBytes(t quacktype.LogicalType, physical quacktype.PhysicalType, vec DecodedVector) []byte {
	rows := vec.Size()
	buf := codec.NewBinaryWriter(physical.ByteWidth() * rows)
	for i := 0; i < rows; i++ {
		var v interface{}
		if !vec.IsNull(i) {
			v = vec.GetObject(i)
		}
		encodeFixedValueForWrite(buf, t, physical, v)
	}
	return buf.Bytes()
}

func encodeFixedValueForWrite(w *codec.BinaryWriter, t quacktype.LogicalType, physical quacktype.PhysicalType, value interface{}) {
	switch physical {
	case quacktype.PhysicalTypeBool:
		if toBool(value) {
			w.WriteFixedUint8(1)
		} else {
			w.WriteFixedUint8(0)
		}
	case quacktype.PhysicalTypeInt8:
		w.WriteFixedInt8(int8(toInt64(value)))
	case quacktype.PhysicalTypeUint8:
		w.WriteFixedUint8(uint8(encodeEnumOrInt(t, value, 0)))
	case quacktype.PhysicalTypeInt16:
		if t.ID == quacktype.LogicalTypeIDDecimal {
			w.WriteFixedInt16(int16(decimalUnscaled(t, value).Int64()))
		} else {
			w.WriteFixedInt16(int16(toInt64(value)))
		}
	case quacktype.PhysicalTypeUint16:
		w.WriteFixedUint16(uint16(encodeEnumOrInt(t, value, 0)))
	case quacktype.PhysicalTypeInt32:
		switch t.ID {
		case quacktype.LogicalTypeIDDate:
			w.WriteFixedInt32(int32(dateToEpochDays(value)))
		case quacktype.LogicalTypeIDDecimal:
			w.WriteFixedInt32(int32(decimalUnscaled(t, value).Int64()))
		default:
			w.WriteFixedInt32(int32(toInt64(value)))
		}
	case quacktype.PhysicalTypeUint32:
		w.WriteFixedUint32(uint32(encodeEnumOrInt(t, value, 0)))
	case quacktype.PhysicalTypeInt64:
		w.WriteFixedInt64(encodeInt64LogicalValueForWrite(t, value))
	case quacktype.PhysicalTypeUint64:
		w.WriteFixedUint64(uint64(toInt64(value)))
	case quacktype.PhysicalTypeFloat:
		w.WriteFixedFloat32(float32(toFloat64(value)))
	case quacktype.PhysicalTypeDouble:
		w.WriteFixedFloat64(toFloat64(value))
	case quacktype.PhysicalTypeInt128:
		var parts codec.HugeIntParts
		if value == nil {
			parts = codec.HugeIntParts{}
		} else if t.ID == quacktype.LogicalTypeIDUUID {
			parts = uuidToHugeIntParts(value)
		} else if t.ID == quacktype.LogicalTypeIDDecimal {
			parts = codec.HugeIntFromSigned(decimalUnscaled(t, value))
		} else if bi, ok := value.(*big.Int); ok {
			parts = codec.HugeIntFromSigned(bi)
		} else {
			parts = codec.HugeIntFromSigned(big.NewInt(toInt64(value)))
		}
		w.WriteFixedUint64(parts.Lower)
		w.WriteFixedInt64(parts.Upper)
	case quacktype.PhysicalTypeUint128:
		var v *big.Int
		if value == nil {
			v = big.NewInt(0)
		} else if bi, ok := value.(*big.Int); ok {
			v = bi
		} else {
			v = big.NewInt(toInt64(value))
		}
		mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
		lower := new(big.Int).And(v, mask).Uint64()
		upper := new(big.Int).And(new(big.Int).Rsh(v, 64), mask).Uint64()
		w.WriteFixedUint64(lower)
		w.WriteFixedUint64(upper)
	case quacktype.PhysicalTypeInterval:
		iv, _ := value.(IntervalValue)
		w.WriteFixedInt32(iv.Months)
		w.WriteFixedInt32(iv.Days)
		w.WriteFixedInt64(iv.Micros)
	}
}

func encodeInt64LogicalValueForWrite(t quacktype.LogicalType, value interface{}) int64 {
	if value == nil {
		return 0
	}
	switch t.ID {
	case quacktype.LogicalTypeIDTime:
		if tm, ok := value.(time.Time); ok {
			return durationSinceMidnight(tm) / int64(time.Microsecond)
		}
		return toInt64(value)
	case quacktype.LogicalTypeIDTimeNS:
		if tm, ok := value.(time.Time); ok {
			return durationSinceMidnight(tm)
		}
		return toInt64(value)
	case quacktype.LogicalTypeIDTimestampSec:
		if tm, ok := value.(time.Time); ok {
			return tm.Unix()
		}
	case quacktype.LogicalTypeIDTimestampMS:
		if tm, ok := value.(time.Time); ok {
			return tm.UnixMilli()
		}
	case quacktype.LogicalTypeIDTimestamp, quacktype.LogicalTypeIDTimestampTZ:
		if tm, ok := value.(time.Time); ok {
			return tm.UnixMicro()
		}
	case quacktype.LogicalTypeIDTimestampNS:
		if tm, ok := value.(time.Time); ok {
			return tm.UnixNano()
		}
	case quacktype.LogicalTypeIDTimeTZ:
		return toInt64(value)
	case quacktype.LogicalTypeIDDecimal:
		return decimalUnscaled(t, value).Int64()
	}
	return toInt64(value)
}

func durationSinceMidnight(t time.Time) int64 {
	tUTC := t.UTC()
	return int64(time.Duration(tUTC.Hour())*time.Hour +
		time.Duration(tUTC.Minute())*time.Minute +
		time.Duration(tUTC.Second())*time.Second +
		time.Duration(tUTC.Nanosecond()))
}

func dateToEpochDays(value interface{}) int64 {
	if t, ok := value.(time.Time); ok {
		epoch := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
		return int64(t.UTC().Sub(epoch).Hours() / 24)
	}
	return toInt64(value)
}

func encodeEnumOrInt(t quacktype.LogicalType, value interface{}, def int) int {
	if value == nil {
		return def
	}
	if t.ID == quacktype.LogicalTypeIDEnum {
		values, _ := quacktype.GetEnumValues(t)
		s := fmt.Sprintf("%v", value)
		for i, v := range values {
			if v == s {
				return i
			}
		}
		panic(fmt.Sprintf("EnumOrInt: unknown ENUM value %q", s))
	}
	return int(toInt64(value))
}

func decimalUnscaled(t quacktype.LogicalType, value interface{}) *big.Int {
	info, ok := t.TypeInfo.(quacktype.DecimalTypeInfo)
	if !ok {
		panic("decimalUnscaled: missing DecimalTypeInfo")
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(info.Scale)), nil)
	switch v := value.(type) {
	case *big.Rat:
		num := new(big.Int).Mul(v.Num(), factor)
		return new(big.Int).Quo(num, v.Denom())
	case *big.Int:
		return new(big.Int).Mul(v, factor)
	case string:
		// extremely lax parser: integer-only; otherwise fall back to big.Rat
		r, _, err := big.ParseFloat(v, 10, 256, big.ToNearestEven)
		if err == nil {
			rat := new(big.Rat)
			r.Rat(rat)
			num := new(big.Int).Mul(rat.Num(), factor)
			return new(big.Int).Quo(num, rat.Denom())
		}
		return new(big.Int)
	}
	if n, ok := toInt64Ok(value); ok {
		return new(big.Int).Mul(big.NewInt(n), factor)
	}
	if f, ok := toFloat64Ok(value); ok {
		rat := new(big.Rat).SetFloat64(f)
		if rat == nil {
			return new(big.Int)
		}
		num := new(big.Int).Mul(rat.Num(), factor)
		return new(big.Int).Quo(num, rat.Denom())
	}
	return new(big.Int)
}

func toFloat64Ok(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	}
	if n, ok := toInt64Ok(v); ok {
		return float64(n), true
	}
	return 0, false
}

func uuidToHugeIntParts(value interface{}) codec.HugeIntParts {
	s, ok := value.(string)
	if !ok || len(s) != 36 {
		return codec.HugeIntParts{}
	}
	hex := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			hex = append(hex, s[i])
		}
	}
	if len(hex) != 32 {
		return codec.HugeIntParts{}
	}
	upperBig, ok := new(big.Int).SetString(string(hex[:16]), 16)
	if !ok {
		return codec.HugeIntParts{}
	}
	lowerBig, ok := new(big.Int).SetString(string(hex[16:]), 16)
	if !ok {
		return codec.HugeIntParts{}
	}
	displayUpper := upperBig.Uint64()
	upper := int64(displayUpper ^ (1 << 63))
	return codec.HugeIntParts{Upper: upper, Lower: lowerBig.Uint64()}
}

func encodeStringLikeValueForWrite(t quacktype.LogicalType, value interface{}) []byte {
	if value == nil {
		return nil
	}
	if b, ok := value.([]byte); ok {
		return b
	}
	return []byte(fmt.Sprintf("%v", value))
}
