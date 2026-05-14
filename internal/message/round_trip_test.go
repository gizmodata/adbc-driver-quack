package message

import (
	"reflect"
	"testing"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

func TestDataChunkIntegerVarcharRoundTrip(t *testing.T) {
	intType := quacktype.Of(quacktype.LogicalTypeIDInteger)
	vcharType := quacktype.Of(quacktype.LogicalTypeIDVarchar)
	chunk := DataChunk{
		RowCount: 3,
		Types:    []quacktype.LogicalType{intType, vcharType},
		Columns: []DecodedVector{
			IntVec{TypeRef: intType, Values: []int32{1, 2, 3}},
			ObjectVec{TypeRef: vcharType, Values: []interface{}{"alpha", "beta", "gamma"}},
		},
	}

	w := codec.NewBinaryWriter(0)
	EncodeDataChunkWrapper(w, chunk)
	if w.Err() != nil {
		t.Fatalf("encode: %v", w.Err())
	}
	r := codec.NewBinaryReader(w.Bytes())
	got := DecodeDataChunkWrapper(r)
	if r.Err() != nil {
		t.Fatalf("decode: %v", r.Err())
	}
	if got.RowCount != 3 {
		t.Errorf("rowCount: got %d", got.RowCount)
	}
	if _, ok := got.Columns[0].(IntVec); !ok {
		t.Errorf("column 0: expected IntVec, got %T", got.Columns[0])
	}
	for i := 0; i < 3; i++ {
		if v := got.Columns[0].GetObject(i).(int32); v != int32(i+1) {
			t.Errorf("col 0 row %d: got %v", i, v)
		}
		if s := got.Columns[1].GetObject(i).(string); s != []string{"alpha", "beta", "gamma"}[i] {
			t.Errorf("col 1 row %d: got %v", i, s)
		}
	}
}

func TestNullsRoundTripViaValidity(t *testing.T) {
	bigintType := quacktype.Of(quacktype.LogicalTypeIDBigInt)
	validity := ValidityAllValid(4)
	ValiditySetNull(validity, 1)
	ValiditySetNull(validity, 3)
	chunk := DataChunk{
		RowCount: 4,
		Types:    []quacktype.LogicalType{bigintType},
		Columns: []DecodedVector{
			LongVec{TypeRef: bigintType, Values: []int64{10, 0, 30, 0}, Validity: validity},
		},
	}
	w := codec.NewBinaryWriter(0)
	EncodeDataChunkWrapper(w, chunk)
	r := codec.NewBinaryReader(w.Bytes())
	got := DecodeDataChunkWrapper(r)
	if r.Err() != nil {
		t.Fatalf("decode: %v", r.Err())
	}
	col := got.Columns[0]
	if v := col.GetObject(0).(int64); v != 10 {
		t.Errorf("row 0: got %v", v)
	}
	if !col.IsNull(1) {
		t.Errorf("row 1 should be null")
	}
	if v := col.GetObject(2).(int64); v != 30 {
		t.Errorf("row 2: got %v", v)
	}
	if !col.IsNull(3) {
		t.Errorf("row 3 should be null")
	}
}

func TestMessageHeaderRoundTrip(t *testing.T) {
	original := PrepareRequest{
		Hdr: MessageHeader{
			Type:          MessageTypePrepareRequest,
			ConnectionID:  "CONN-XYZ",
			ClientQueryID: 7,
		},
		SQL: "SELECT 42",
	}
	bytes, err := EncodeMessage(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeMessage(bytes)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	pr, ok := decoded.(PrepareRequest)
	if !ok {
		t.Fatalf("expected PrepareRequest, got %T", decoded)
	}
	if pr.SQL != "SELECT 42" {
		t.Errorf("SQL: got %q", pr.SQL)
	}
	if pr.Hdr.ConnectionID != "CONN-XYZ" {
		t.Errorf("ConnectionID: got %q", pr.Hdr.ConnectionID)
	}
	if pr.Hdr.ClientQueryID != 7 {
		t.Errorf("ClientQueryID: got %d", pr.Hdr.ClientQueryID)
	}
}

func TestFetchRequestRoundTrip(t *testing.T) {
	uuid := codec.HugeIntParts{Upper: 0x1122334455667788, Lower: 0x99AABBCCDDEEFF00}
	original := FetchRequest{
		Hdr:        MessageHeader{Type: MessageTypeFetchRequest, ConnectionID: "c1", ClientQueryID: 2},
		ResultUUID: uuid,
	}
	bytes, err := EncodeMessage(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeMessage(bytes)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	fr, ok := decoded.(FetchRequest)
	if !ok {
		t.Fatalf("expected FetchRequest, got %T", decoded)
	}
	if !reflect.DeepEqual(fr.ResultUUID, uuid) {
		t.Errorf("UUID: got %v", fr.ResultUUID)
	}
}

func TestValidityWordCounts(t *testing.T) {
	if ValidityWordCount(64) != 1 {
		t.Errorf("wordCount(64): got %d", ValidityWordCount(64))
	}
	if ValidityWordCount(65) != 2 {
		t.Errorf("wordCount(65): got %d", ValidityWordCount(65))
	}
	if ValidityWireByteCount(1) != 8 {
		t.Errorf("wireByteCount(1): got %d", ValidityWireByteCount(1))
	}
}

func TestValidityBitOrder(t *testing.T) {
	// Bit 0 of byte 0 → row 0
	v := ValidityFromBytes([]byte{0b0000_0001}, 1)
	if !ValidityIsValid(v, 0) {
		t.Errorf("row 0 should be valid")
	}
	// Bit 1 of byte 8 → row 65
	v = ValidityFromBytes([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0b0000_0010}, 66)
	if !ValidityIsValid(v, 65) {
		t.Errorf("row 65 should be valid")
	}
	if ValidityIsValid(v, 64) {
		t.Errorf("row 64 should be null")
	}
}
