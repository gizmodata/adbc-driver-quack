package message

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// Row and element counts come from the peer. A count the message cannot back
// with bytes must fail the reader, never size an allocation: before the check
// existed each rejected case here panicked (makeslice out of range, an index
// out of range or a nil vector) from a message of a few dozen bytes. The
// accepting twins pin what a real server sends and must keep decoding.

const hugeCount = math.MaxInt

var (
	boundsInt    = quacktype.Of(quacktype.LogicalTypeIDInteger)
	boundsBigInt = quacktype.Of(quacktype.LogicalTypeIDBigInt)
	boundsBool   = quacktype.Of(quacktype.LogicalTypeIDBoolean)
	boundsList   = quacktype.List(boundsInt)
)

func le32(values ...int32) []byte {
	out := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[4*i:], uint32(v))
	}
	return out
}

func writeSequenceBody(w *codec.BinaryWriter) {
	w.WriteField(90, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(VectorTypeSequence)) })
	w.WriteField(91, func(o *codec.BinaryWriter) { o.WriteSLEB128(0) })
	w.WriteField(92, func(o *codec.BinaryWriter) { o.WriteSLEB128(1) })
}

func writeFlatInts(w *codec.BinaryWriter, values ...int32) {
	w.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(false) })
	w.WriteField(102, func(o *codec.BinaryWriter) { o.WriteBlob(le32(values...)) })
}

// listVector is a FLAT LIST vector: list size, one (offset, length) entry per
// row, then the child vector.
func listVector(listSize int, entries [][2]int, child func(*codec.BinaryWriter)) []byte {
	w := codec.NewBinaryWriter(0)
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(false) })
		obj.WriteField(104, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(listSize)) })
		obj.WriteField(105, func(o *codec.BinaryWriter) {
			codec.WriteList(o, entries, func(_ int, e [2]int, ow *codec.BinaryWriter) {
				ow.WriteObject(func(eo *codec.BinaryWriter) {
					eo.WriteField(100, func(x *codec.BinaryWriter) { x.WriteULEB128(uint64(e[0])) })
					eo.WriteField(101, func(x *codec.BinaryWriter) { x.WriteULEB128(uint64(e[1])) })
				})
			})
		})
		if child != nil {
			obj.WriteField(106, func(o *codec.BinaryWriter) { o.WriteObject(child) })
		}
	})
	return w.Bytes()
}

func vectorBytes(body func(*codec.BinaryWriter)) []byte {
	w := codec.NewBinaryWriter(0)
	w.WriteObject(body)
	return w.Bytes()
}

func assertVectorRejected(t *testing.T, want string, typ quacktype.LogicalType, count int, wire []byte) {
	t.Helper()
	r := codec.NewBinaryReader(wire)
	v := DecodeVector(r, typ, count)
	if r.Err() == nil {
		t.Fatalf("decoded a %T of %d rows, want an error containing %q", v, count, want)
	}
	if !strings.Contains(r.Err().Error(), want) {
		t.Fatalf("error %q does not contain %q", r.Err(), want)
	}
}

// ---- rejected: a count with no bytes behind it ----

func TestSequenceVectorWithHugeRowCountIsRejected(t *testing.T) {
	assertVectorRejected(t, "vector claims 9223372036854775807 rows", boundsInt, hugeCount,
		vectorBytes(writeSequenceBody))
}

func TestConstantVectorWithHugeRowCountIsRejected(t *testing.T) {
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(90, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(VectorTypeConstant)) })
		writeFlatInts(obj, 42)
	})
	assertVectorRejected(t, "vector claims 9223372036854775807 rows", boundsInt, hugeCount, wire)
}

func TestStructWithoutChildrenAndHugeRowCountIsRejected(t *testing.T) {
	emptyStruct := quacktype.OfWithInfo(quacktype.LogicalTypeIDStruct, quacktype.StructTypeInfo{})
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(false) })
		obj.WriteField(103, func(o *codec.BinaryWriter) { o.WriteULEB128(0) })
	})
	assertVectorRejected(t, "vector claims 9223372036854775807 rows", emptyStruct, hugeCount, wire)
}

func TestDictionaryWithHugeDictionaryCountIsRejected(t *testing.T) {
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(90, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(VectorTypeDictionary)) })
		obj.WriteField(91, func(o *codec.BinaryWriter) { o.WriteBlob(le32(0)) })
		obj.WriteField(92, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(hugeCount)) })
		writeSequenceBody(obj)
	})
	assertVectorRejected(t, "vector claims 9223372036854775807 rows", boundsInt, 1, wire)
}

func TestDictionarySelectionOutOfRangeIsRejected(t *testing.T) {
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(90, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(VectorTypeDictionary)) })
		obj.WriteField(91, func(o *codec.BinaryWriter) { o.WriteBlob(le32(5)) })
		obj.WriteField(92, func(o *codec.BinaryWriter) { o.WriteULEB128(1) })
		writeFlatInts(obj, 10)
	})
	assertVectorRejected(t, "dictionary selection 5 is out of range (dictionary has 1)", boundsInt, 1, wire)
}

func TestListWithHugeChildCountIsRejected(t *testing.T) {
	wire := listVector(hugeCount, nil, writeSequenceBody)
	assertVectorRejected(t, "vector claims 9223372036854775807 rows", boundsList, 0, wire)
}

func TestListEntryLongerThanItsChildVectorIsRejected(t *testing.T) {
	wire := listVector(1, [][2]int{{0, hugeCount}}, func(w *codec.BinaryWriter) { writeFlatInts(w, 7) })
	assertVectorRejected(t, "LIST row 0 spans 9223372036854775807 child elements from 0, but the child vector has 1",
		boundsList, 1, wire)
}

func TestListEntryPastTheEndOfItsChildVectorIsRejected(t *testing.T) {
	wire := listVector(1, [][2]int{{5, 1}}, func(w *codec.BinaryWriter) { writeFlatInts(w, 7) })
	assertVectorRejected(t, "LIST row 0 spans 1 child elements from 5, but the child vector has 1",
		boundsList, 1, wire)
}

func TestListWhoseChildVectorIsMissingIsRejected(t *testing.T) {
	// The vector ends where its child (field 106) should start.
	wire := listVector(1, [][2]int{{0, 1}}, nil)
	assertVectorRejected(t, "expected field 106", boundsList, 1, wire)
}

func TestArrayWhoseChildCountOverflowsIsRejected(t *testing.T) {
	// 4 rows of 2^62 elements is 2^64 child elements: zero as an int.
	const arraySize = 1 << 62
	bigArray := quacktype.OfWithInfo(quacktype.LogicalTypeIDArray,
		quacktype.ArrayTypeInfo{ChildType: boundsInt, Size: arraySize})
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(false) })
		obj.WriteField(103, func(o *codec.BinaryWriter) { o.WriteULEB128(arraySize) })
		obj.WriteField(104, func(o *codec.BinaryWriter) { o.WriteObject(func(c *codec.BinaryWriter) { writeFlatInts(c) }) })
	})
	assertVectorRejected(t, "more child elements than a vector can hold", bigArray, 4, wire)
}

func TestChunkWithHugeRowCountIsRejected(t *testing.T) {
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(hugeCount)) })
		obj.WriteField(101, func(o *codec.BinaryWriter) { o.WriteULEB128(0) })
		obj.WriteField(102, func(o *codec.BinaryWriter) { o.WriteULEB128(0) })
	})
	r := codec.NewBinaryReader(wire)
	chunk := DecodeDataChunk(r)
	if r.Err() == nil {
		t.Fatalf("decoded a chunk of %d rows, want an error", chunk.RowCount)
	}
	if want := "DataChunk claims 9223372036854775807 rows"; !strings.Contains(r.Err().Error(), want) {
		t.Fatalf("error %q does not contain %q", r.Err(), want)
	}
}

func TestSequenceVectorJustAboveTheCapIsRejected(t *testing.T) {
	assertVectorRejected(t, "vector claims 1048577 rows", boundsBigInt, maxUnbackedVectorRows+1,
		vectorBytes(writeSequenceBody))
}

// ---- accepted: what a real server sends ----

func TestFlatVectorAboveTheCapDecodesWhenItsRowsAreOnTheWire(t *testing.T) {
	// DuckDB sends a long list's child as one FLAT vector of every element.
	rows := maxUnbackedVectorRows + 1
	data := make([]byte, rows)
	data[rows-1] = 1
	wire := vectorBytes(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteBool(false) })
		obj.WriteField(102, func(o *codec.BinaryWriter) { o.WriteBlob(data) })
	})
	r := codec.NewBinaryReader(wire)
	v := DecodeVector(r, boundsBool, rows)
	if r.Err() != nil {
		t.Fatalf("decode: %v", r.Err())
	}
	if v.Size() != rows || v.GetObject(0) != false || v.GetObject(rows-1) != true {
		t.Fatalf("size %d, first %v, last %v", v.Size(), v.GetObject(0), v.GetObject(rows-1))
	}
}

func TestSequenceVectorAtTheCapDecodes(t *testing.T) {
	rows := maxUnbackedVectorRows
	r := codec.NewBinaryReader(vectorBytes(writeSequenceBody))
	v := DecodeVector(r, boundsBigInt, rows)
	if r.Err() != nil {
		t.Fatalf("decode: %v", r.Err())
	}
	if v.Size() != rows || v.GetObject(rows-1) != int64(rows-1) {
		t.Fatalf("size %d, last %v", v.Size(), v.GetObject(rows-1))
	}
}

func TestListRowsMayShareChildElements(t *testing.T) {
	// A flattened constant LIST gives every row the same (offset, length).
	wire := listVector(2, [][2]int{{0, 2}, {0, 2}}, func(w *codec.BinaryWriter) { writeFlatInts(w, 10, 20) })
	r := codec.NewBinaryReader(wire)
	v := DecodeVector(r, boundsList, 2)
	if r.Err() != nil {
		t.Fatalf("decode: %v", r.Err())
	}
	for row := 0; row < 2; row++ {
		got := v.GetObject(row).([]interface{})
		if len(got) != 2 || got[0] != int32(10) || got[1] != int32(20) {
			t.Fatalf("row %d: %v", row, got)
		}
	}
}
