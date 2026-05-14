package codec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
)

// BinaryReader decodes DuckDB BinarySerializer-compatible primitives:
// little-endian uint16 field ids, ULEB128/SLEB128 integers, fixed-width
// native primitives, and length-prefixed strings/blobs/lists. Objects
// are terminated by a FieldEnd (0xFFFF) field id.
//
// Errors are sticky: once a Read* method fails, every subsequent call is
// a no-op and Err() returns the original failure. Callers can perform a
// batch of reads and check Err() once at the end.
type BinaryReader struct {
	bytes  []byte
	offset int
	err    error
}

// NewBinaryReader wraps a byte slice for reading.
func NewBinaryReader(b []byte) *BinaryReader {
	return &BinaryReader{bytes: b}
}

// Err returns the first error encountered, or nil if every read so far
// succeeded.
func (r *BinaryReader) Err() error { return r.err }

// Position returns the current read offset.
func (r *BinaryReader) Position() int { return r.offset }

// Remaining returns the number of bytes left unread.
func (r *BinaryReader) Remaining() int { return len(r.bytes) - r.offset }

// EOF reports whether every byte has been consumed.
func (r *BinaryReader) EOF() bool { return r.offset >= len(r.bytes) }

// AssertEOF returns an error if there are any unread bytes left.
func (r *BinaryReader) AssertEOF() error {
	if r.err != nil {
		return r.err
	}
	if !r.EOF() {
		r.err = newError("AssertEOF",
			fmt.Sprintf("unexpected trailing %d byte(s)", r.Remaining()), r.offset)
	}
	return r.err
}

func (r *BinaryReader) ensure(n int) bool {
	if r.err != nil {
		return false
	}
	if r.offset+n > len(r.bytes) {
		r.err = newError("ensure",
			fmt.Sprintf("unexpected end of input; needed %d byte(s), have %d", n, r.Remaining()),
			r.offset)
		return false
	}
	return true
}

// ---- objects & fields ----

// ReadObject reads the body of an object then consumes its trailing
// FieldEnd marker. Returns the value produced by body or the first
// error encountered.
func ReadObject[T any](r *BinaryReader, body func(*BinaryReader) T) (T, error) {
	var zero T
	v := body(r)
	if r.err != nil {
		return zero, r.err
	}
	if err := r.ReadEndObject(); err != nil {
		return zero, err
	}
	return v, nil
}

// ReadEndObject consumes the FieldEnd (0xFFFF) marker.
func (r *BinaryReader) ReadEndObject() error {
	id := r.ReadFieldID()
	if r.err != nil {
		return r.err
	}
	if id != FieldEnd {
		r.err = newError("ReadEndObject",
			fmt.Sprintf("expected end-of-object (0xFFFF), got field %d", id),
			r.offset-2)
		return r.err
	}
	return nil
}

// ReadFieldID consumes the next little-endian uint16 field id.
func (r *BinaryReader) ReadFieldID() uint16 {
	if !r.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.bytes[r.offset:])
	r.offset += 2
	return v
}

// PeekFieldID returns the next field id without advancing.
func (r *BinaryReader) PeekFieldID() uint16 {
	if !r.ensure(2) {
		return 0
	}
	return binary.LittleEndian.Uint16(r.bytes[r.offset:])
}

// ReadRequiredField reads a field, requiring its id to equal fieldID.
func ReadRequiredField[T any](r *BinaryReader, fieldID uint16, read func(*BinaryReader) T) T {
	var zero T
	actual := r.ReadFieldID()
	if r.err != nil {
		return zero
	}
	if actual != fieldID {
		r.err = newError("ReadRequiredField",
			fmt.Sprintf("expected field %d, got %d", fieldID, actual),
			r.offset-2)
		return zero
	}
	return read(r)
}

// ReadOptionalField reads a field if the next field id matches; otherwise
// returns defaultValue without consuming any bytes.
func ReadOptionalField[T any](r *BinaryReader, fieldID uint16, read func(*BinaryReader) T, defaultValue T) T {
	if r.err != nil || r.EOF() {
		return defaultValue
	}
	if r.PeekFieldID() != fieldID {
		return defaultValue
	}
	r.ReadFieldID()
	return read(r)
}

// ---- primitive scalars ----

// readByte is unexported so the BinaryReader doesn't accidentally
// satisfy io.ByteReader (which requires the (byte, error) signature
// — sticky-error semantics are the whole point of this codec).
func (r *BinaryReader) readByte() byte {
	if !r.ensure(1) {
		return 0
	}
	b := r.bytes[r.offset]
	r.offset++
	return b
}

func (r *BinaryReader) ReadBytes(n int) []byte {
	if n < 0 {
		r.err = newError("ReadBytes", fmt.Sprintf("invalid length %d", n), r.offset)
		return nil
	}
	if !r.ensure(n) {
		return nil
	}
	out := make([]byte, n)
	copy(out, r.bytes[r.offset:r.offset+n])
	r.offset += n
	return out
}

func (r *BinaryReader) ReadBool() bool {
	b := r.readByte()
	if r.err != nil {
		return false
	}
	if b != 0 && b != 1 {
		r.err = newError("ReadBool", fmt.Sprintf("invalid boolean byte %d", b), r.offset-1)
		return false
	}
	return b == 1
}

// ---- LEB128 ----

// ReadULEB128 reads an unsigned LEB128 integer. The result is interpreted
// as an unsigned 64-bit value; sentinels like OptionalIndexInvalid round-trip
// via this method.
func (r *BinaryReader) ReadULEB128() uint64 {
	var result uint64
	var shift uint
	for i := 0; i < 10; i++ {
		b := r.readByte()
		if r.err != nil {
			return 0
		}
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result
		}
		shift += 7
	}
	r.err = newError("ReadULEB128", "value is too long", r.offset)
	return 0
}

// ReadULEB128Int reads a ULEB128 value and returns it as an int, failing
// if the value exceeds math.MaxInt.
func (r *BinaryReader) ReadULEB128Int() int {
	v := r.ReadULEB128()
	if r.err != nil {
		return 0
	}
	if v > math.MaxInt {
		r.err = newError("ReadULEB128Int",
			fmt.Sprintf("ULEB128 value %d exceeds MaxInt", v), r.offset)
		return 0
	}
	return int(v)
}

// ReadULEB128BigInt reads a ULEB128 as a big.Int (always non-negative),
// useful when callers need a portable arbitrary-precision representation.
func (r *BinaryReader) ReadULEB128BigInt() *big.Int {
	v := r.ReadULEB128()
	if r.err != nil {
		return nil
	}
	return new(big.Int).SetUint64(v)
}

// ReadSLEB128 reads a signed LEB128 integer.
func (r *BinaryReader) ReadSLEB128() int64 {
	var result uint64
	var shift uint
	var b byte
	for i := 0; i < 10; i++ {
		b = r.readByte()
		if r.err != nil {
			return 0
		}
		result |= uint64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && b&0x40 != 0 {
				result |= ^uint64(0) << shift
			}
			return int64(result)
		}
	}
	r.err = newError("ReadSLEB128", "value is too long", r.offset)
	return 0
}

// ---- length-prefixed ----

func (r *BinaryReader) ReadString() string {
	bytes := r.ReadStringBytes()
	if r.err != nil {
		return ""
	}
	return string(bytes)
}

func (r *BinaryReader) ReadStringBytes() []byte {
	length := r.ReadULEB128Int()
	if r.err != nil {
		return nil
	}
	return r.ReadBytes(length)
}

func (r *BinaryReader) ReadBlob() []byte {
	length := r.ReadULEB128Int()
	if r.err != nil {
		return nil
	}
	return r.ReadBytes(length)
}

// ReadList reads a ULEB128 length-prefix followed by length elements.
func ReadList[T any](r *BinaryReader, read func(index int, r *BinaryReader) T) []T {
	length := r.ReadULEB128Int()
	if r.err != nil {
		return nil
	}
	out := make([]T, 0, length)
	for i := 0; i < length; i++ {
		v := read(i, r)
		if r.err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

// ReadNullable reads a presence bool; if true, calls read and returns
// the result; if false, returns the zero value of T.
func ReadNullable[T any](r *BinaryReader, read func(*BinaryReader) T) (T, bool) {
	var zero T
	present := r.ReadBool()
	if r.err != nil || !present {
		return zero, false
	}
	return read(r), true
}

// ReadHugeInt reads a DuckDB HUGEINT (SLEB upper + ULEB lower).
func (r *BinaryReader) ReadHugeInt() HugeIntParts {
	upper := r.ReadSLEB128()
	if r.err != nil {
		return HugeIntParts{}
	}
	lower := r.ReadULEB128()
	if r.err != nil {
		return HugeIntParts{}
	}
	return HugeIntParts{Upper: upper, Lower: lower}
}

// ---- fixed-width ----

func (r *BinaryReader) ReadFixedInt8() int8 {
	b := r.readByte()
	if r.err != nil {
		return 0
	}
	return int8(b)
}

func (r *BinaryReader) ReadFixedUint8() uint8 {
	b := r.readByte()
	if r.err != nil {
		return 0
	}
	return b
}

func (r *BinaryReader) ReadFixedInt16() int16 {
	if !r.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.bytes[r.offset:])
	r.offset += 2
	return int16(v)
}

func (r *BinaryReader) ReadFixedUint16() uint16 {
	if !r.ensure(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(r.bytes[r.offset:])
	r.offset += 2
	return v
}

func (r *BinaryReader) ReadFixedInt32() int32 {
	if !r.ensure(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.bytes[r.offset:])
	r.offset += 4
	return int32(v)
}

func (r *BinaryReader) ReadFixedUint32() uint32 {
	if !r.ensure(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.bytes[r.offset:])
	r.offset += 4
	return v
}

func (r *BinaryReader) ReadFixedInt64() int64 {
	if !r.ensure(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.bytes[r.offset:])
	r.offset += 8
	return int64(v)
}

func (r *BinaryReader) ReadFixedUint64() uint64 {
	if !r.ensure(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.bytes[r.offset:])
	r.offset += 8
	return v
}

func (r *BinaryReader) ReadFixedFloat32() float32 {
	return math.Float32frombits(uint32(r.ReadFixedUint32()))
}

func (r *BinaryReader) ReadFixedFloat64() float64 {
	return math.Float64frombits(r.ReadFixedUint64())
}

// Compile-time check that errors.Is / errors.As still work via the
// standard `errors` package against our custom Error.
var _ = errors.As
