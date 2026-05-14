package codec

import (
	"encoding/binary"
	"fmt"
	"math"
)

// BinaryWriter encodes DuckDB BinarySerializer-compatible primitives.
//
// Like BinaryReader, errors are sticky: once a Write* method fails (e.g.
// from an invalid field id or out-of-range integer), every subsequent
// call is a no-op and Err() returns the original failure.
type BinaryWriter struct {
	buf []byte
	err error
}

// NewBinaryWriter creates a writer with the given initial capacity.
// Pass 0 for the default.
func NewBinaryWriter(initialCapacity int) *BinaryWriter {
	if initialCapacity <= 0 {
		initialCapacity = 1024
	}
	return &BinaryWriter{buf: make([]byte, 0, initialCapacity)}
}

// Err returns the first error encountered, or nil if every write so far
// succeeded.
func (w *BinaryWriter) Err() error { return w.err }

// Bytes returns a copy of the bytes written so far.
func (w *BinaryWriter) Bytes() []byte {
	out := make([]byte, len(w.buf))
	copy(out, w.buf)
	return out
}

// Size returns the number of bytes written so far.
func (w *BinaryWriter) Size() int { return len(w.buf) }

// ---- objects & fields ----

// WriteObject writes an object body then its trailing FieldEnd marker.
func (w *BinaryWriter) WriteObject(body func(*BinaryWriter)) {
	body(w)
	w.WriteFieldID(FieldEnd)
}

// WriteField writes a field id followed by the body.
func (w *BinaryWriter) WriteField(fieldID uint16, body func(*BinaryWriter)) {
	w.WriteFieldID(fieldID)
	body(w)
}

// WriteFieldID writes a raw little-endian uint16 field id.
func (w *BinaryWriter) WriteFieldID(fieldID uint16) {
	if w.err != nil {
		return
	}
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], fieldID)
	w.buf = append(w.buf, b[:]...)
}

// ---- primitive scalars ----

// writeByte is unexported so the BinaryWriter doesn't accidentally
// satisfy io.ByteWriter (which requires a `error` return).
func (w *BinaryWriter) writeByte(b byte) {
	if w.err != nil {
		return
	}
	w.buf = append(w.buf, b)
}

func (w *BinaryWriter) WriteBytes(b []byte) {
	if w.err != nil {
		return
	}
	w.buf = append(w.buf, b...)
}

func (w *BinaryWriter) WriteBool(v bool) {
	if v {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
}

// ---- LEB128 ----

// WriteULEB128 writes an unsigned LEB128 integer (value is interpreted as
// unsigned 64-bit). The OptionalIndexInvalid sentinel encodes as 10
// bytes via this method.
func (w *BinaryWriter) WriteULEB128(value uint64) {
	if w.err != nil {
		return
	}
	for value & ^uint64(0x7F) != 0 {
		w.buf = append(w.buf, byte(value&0x7F)|0x80)
		value >>= 7
	}
	w.buf = append(w.buf, byte(value&0x7F))
}

// WriteSLEB128 writes a signed LEB128 integer.
func (w *BinaryWriter) WriteSLEB128(value int64) {
	if w.err != nil {
		return
	}
	for {
		b := byte(value & 0x7F)
		value >>= 7
		signBitSet := b&0x40 != 0
		if (value == 0 && !signBitSet) || (value == -1 && signBitSet) {
			w.buf = append(w.buf, b)
			return
		}
		w.buf = append(w.buf, b|0x80)
	}
}

// ---- length-prefixed ----

func (w *BinaryWriter) WriteString(s string) {
	w.WriteStringBytes([]byte(s))
}

func (w *BinaryWriter) WriteStringBytes(b []byte) {
	w.WriteULEB128(uint64(len(b)))
	w.WriteBytes(b)
}

func (w *BinaryWriter) WriteBlob(b []byte) {
	w.WriteULEB128(uint64(len(b)))
	w.WriteBytes(b)
}

// WriteList writes a ULEB128 length prefix and then each element.
func WriteList[T any](w *BinaryWriter, items []T, writeItem func(index int, item T, w *BinaryWriter)) {
	w.WriteULEB128(uint64(len(items)))
	for i, item := range items {
		writeItem(i, item, w)
		if w.err != nil {
			return
		}
	}
}

// WriteNullable writes a presence bool, then writes the value if non-nil.
func WriteNullable[T any](w *BinaryWriter, value *T, writeValue func(T, *BinaryWriter)) {
	if value == nil {
		w.WriteBool(false)
		return
	}
	w.WriteBool(true)
	writeValue(*value, w)
}

// WriteHugeInt writes a DuckDB HUGEINT (SLEB upper + ULEB lower).
func (w *BinaryWriter) WriteHugeInt(p HugeIntParts) {
	w.WriteSLEB128(p.Upper)
	w.WriteULEB128(p.Lower)
}

// ---- fixed-width ----

func (w *BinaryWriter) WriteFixedInt8(v int8) {
	w.writeByte(byte(v))
}

func (w *BinaryWriter) WriteFixedUint8(v uint8) {
	w.writeByte(v)
}

func (w *BinaryWriter) WriteFixedInt16(v int16) {
	if w.err != nil {
		return
	}
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], uint16(v))
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedUint16(v uint16) {
	if w.err != nil {
		return
	}
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedInt32(v int32) {
	if w.err != nil {
		return
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(v))
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedUint32(v uint32) {
	if w.err != nil {
		return
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedInt64(v int64) {
	if w.err != nil {
		return
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(v))
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedUint64(v uint64) {
	if w.err != nil {
		return
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

func (w *BinaryWriter) WriteFixedFloat32(v float32) {
	w.WriteFixedUint32(math.Float32bits(v))
}

func (w *BinaryWriter) WriteFixedFloat64(v float64) {
	w.WriteFixedUint64(math.Float64bits(v))
}

// invalidate records an error and stops further writes.
func (w *BinaryWriter) invalidate(op, msg string) {
	if w.err == nil {
		w.err = &Error{Op: op, Msg: msg, Offset: len(w.buf)}
	}
}

// Useful sanity helper for unit tests.
func (w *BinaryWriter) checkFieldRange(op string, fieldID int) {
	if fieldID < 0 || fieldID > 0xFFFF {
		w.invalidate(op, fmt.Sprintf("invalid field id %d", fieldID))
	}
}
