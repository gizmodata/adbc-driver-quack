package codec

import (
	"math"
	"math/big"
	"testing"
)

func TestULEB128RoundTrip(t *testing.T) {
	cases := []uint64{
		0, 1, 127, 128, 255, 256, 16383, 16384,
		1_000_000, math.MaxInt64, math.MaxUint64,
	}
	for _, v := range cases {
		w := NewBinaryWriter(0)
		w.WriteULEB128(v)
		if w.Err() != nil {
			t.Fatalf("write %d: %v", v, w.Err())
		}
		r := NewBinaryReader(w.Bytes())
		got := r.ReadULEB128()
		if r.Err() != nil {
			t.Fatalf("read back %d: %v", v, r.Err())
		}
		if got != v {
			t.Errorf("ULEB128 %d round-trip mismatch: got %d", v, got)
		}
		if !r.EOF() {
			t.Errorf("ULEB128 %d: %d bytes left after read", v, r.Remaining())
		}
	}
}

func TestSLEB128RoundTrip(t *testing.T) {
	cases := []int64{0, 1, -1, 63, -64, 64, -65, math.MaxInt64, math.MinInt64}
	for _, v := range cases {
		w := NewBinaryWriter(0)
		w.WriteSLEB128(v)
		r := NewBinaryReader(w.Bytes())
		got := r.ReadSLEB128()
		if r.Err() != nil {
			t.Fatalf("read back %d: %v", v, r.Err())
		}
		if got != v {
			t.Errorf("SLEB128 %d round-trip mismatch: got %d", v, got)
		}
	}
}

func TestULEB128OptionalIndexSentinel(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteULEB128(OptionalIndexInvalid)
	r := NewBinaryReader(w.Bytes())
	got := r.ReadULEB128()
	if got != OptionalIndexInvalid {
		t.Errorf("sentinel round-trip: got %d, want %d", got, OptionalIndexInvalid)
	}
}

func TestFixedIntsLittleEndian(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteFixedInt32(0x01020304)
	got := w.Bytes()
	want := []byte{0x04, 0x03, 0x02, 0x01}
	for i, b := range want {
		if got[i] != b {
			t.Errorf("byte %d: got 0x%02X want 0x%02X", i, got[i], b)
		}
	}
	r := NewBinaryReader(got)
	if v := r.ReadFixedInt32(); v != 0x01020304 {
		t.Errorf("readback got 0x%X", v)
	}
}

func TestStringRoundTrip(t *testing.T) {
	const sample = "héllo 🦆"
	w := NewBinaryWriter(0)
	w.WriteString(sample)
	r := NewBinaryReader(w.Bytes())
	got := r.ReadString()
	if got != sample {
		t.Errorf("string round-trip: got %q want %q", got, sample)
	}
}

func TestObjectsTerminateWithFieldEnd(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteObject(func(obj *BinaryWriter) {
		obj.WriteField(1, func(o *BinaryWriter) { o.WriteULEB128(42) })
		obj.WriteField(2, func(o *BinaryWriter) { o.WriteString("x") })
	})
	r := NewBinaryReader(w.Bytes())
	_, err := ReadObject(r, func(rr *BinaryReader) struct{} {
		v := ReadRequiredField(rr, 1, func(rrr *BinaryReader) uint64 { return rrr.ReadULEB128() })
		s := ReadRequiredField(rr, 2, func(rrr *BinaryReader) string { return rrr.ReadString() })
		if v != 42 || s != "x" {
			t.Errorf("got v=%d s=%q", v, s)
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatalf("object: %v", err)
	}
	if !r.EOF() {
		t.Errorf("%d bytes left after object", r.Remaining())
	}
}

func TestOptionalFieldsSkipMissing(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteObject(func(obj *BinaryWriter) {
		obj.WriteField(3, func(o *BinaryWriter) { o.WriteULEB128(7) })
	})
	r := NewBinaryReader(w.Bytes())
	_, err := ReadObject(r, func(rr *BinaryReader) struct{} {
		def := ReadOptionalField(rr, 1, func(rrr *BinaryReader) string { return rrr.ReadString() }, "default")
		if def != "default" {
			t.Errorf("optional default: got %q", def)
		}
		v := ReadRequiredField(rr, 3, func(rrr *BinaryReader) uint64 { return rrr.ReadULEB128() })
		if v != 7 {
			t.Errorf("required: got %d", v)
		}
		return struct{}{}
	})
	if err != nil {
		t.Fatalf("object: %v", err)
	}
}

func TestHugeIntRoundTrip(t *testing.T) {
	// 3 * Int64 max — fits in 128-bit signed
	value := new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(3))
	parts := HugeIntFromSigned(value)
	w := NewBinaryWriter(0)
	w.WriteHugeInt(parts)
	r := NewBinaryReader(w.Bytes())
	got := r.ReadHugeInt()
	gotBig := got.SignedBigInt()
	if gotBig.Cmp(value) != 0 {
		t.Errorf("HugeInt round-trip: got %s want %s", gotBig.String(), value.String())
	}
}

func TestStickyErrorOnTruncation(t *testing.T) {
	r := NewBinaryReader([]byte{0x80, 0x80, 0x80}) // incomplete ULEB128
	_ = r.ReadULEB128()
	if r.Err() == nil {
		t.Fatal("expected sticky error for truncated ULEB128")
	}
	// Subsequent reads should be no-ops
	_ = r.ReadFixedUint8()
	_ = r.ReadString()
	if r.Err() == nil {
		t.Fatal("err should remain set")
	}
}
