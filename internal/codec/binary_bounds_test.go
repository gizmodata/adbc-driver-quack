package codec

import (
	"math"
	"strings"
	"testing"
)

func TestStringLengthPastTheEndOfInputIsRejected(t *testing.T) {
	// offset + length used to overflow, so the length passed the bounds
	// check and sized a byte slice.
	w := NewBinaryWriter(0)
	w.WriteULEB128(math.MaxInt)
	r := NewBinaryReader(w.Bytes())
	if s := r.ReadString(); r.Err() == nil {
		t.Fatalf("read a string of %d bytes, want an error", len(s))
	}
	if want := "needed 9223372036854775807 byte(s), have 0"; !strings.Contains(r.Err().Error(), want) {
		t.Fatalf("error %q does not contain %q", r.Err(), want)
	}
}

func TestStringEndingExactlyAtTheEndOfInputDecodes(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteString("quack")
	r := NewBinaryReader(w.Bytes())
	if s := r.ReadString(); s != "quack" || r.Err() != nil {
		t.Fatalf("got %q, %v", s, r.Err())
	}
	if err := r.AssertEOF(); err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedListDoesNotReserveItsClaimedLength(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteULEB128(math.MaxInt)
	r := NewBinaryReader(w.Bytes())
	got := ReadList(r, func(_ int, rr *BinaryReader) uint8 { return rr.ReadFixedUint8() })
	if r.Err() == nil {
		t.Fatalf("read a list of %d elements, want an error", len(got))
	}
	if want := "unexpected end of input"; !strings.Contains(r.Err().Error(), want) {
		t.Fatalf("error %q does not contain %q", r.Err(), want)
	}
}

func TestListLongerThanTheReservationDecodes(t *testing.T) {
	w := NewBinaryWriter(0)
	w.WriteULEB128(2048)
	for i := 0; i < 2048; i++ {
		w.WriteFixedUint8(uint8(i))
	}
	r := NewBinaryReader(w.Bytes())
	got := ReadList(r, func(_ int, rr *BinaryReader) uint8 { return rr.ReadFixedUint8() })
	if r.Err() != nil || len(got) != 2048 || got[2047] != uint8(2047%256) {
		t.Fatalf("len %d, err %v", len(got), r.Err())
	}
}
