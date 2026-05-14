package quacktype

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
)

func TestLogicalTypeRoundTrip(t *testing.T) {
	cases := []LogicalType{
		Of(LogicalTypeIDBoolean),
		Of(LogicalTypeIDInteger),
		Of(LogicalTypeIDBigInt),
		Of(LogicalTypeIDVarchar),
		Decimal(10, 2),
		Decimal(38, 18),
		List(Of(LogicalTypeIDInteger)),
		Array(Of(LogicalTypeIDDouble), 3),
		Struct([]ChildType{
			{Name: "id", Type: Of(LogicalTypeIDInteger)},
			{Name: "name", Type: Of(LogicalTypeIDVarchar)},
		}),
		Enum([]string{"red", "green", "blue"}),
	}
	for _, c := range cases {
		w := codec.NewBinaryWriter(0)
		EncodeLogicalType(w, c)
		if w.Err() != nil {
			t.Fatalf("encode %v: %v", c, w.Err())
		}
		r := codec.NewBinaryReader(w.Bytes())
		got := DecodeLogicalType(r)
		if r.Err() != nil {
			t.Fatalf("decode %v: %v", c, r.Err())
		}
		if !reflect.DeepEqual(got, c) {
			t.Errorf("round-trip mismatch:\n  want %#v\n  got  %#v", c, got)
		}
	}
}

func TestPhysicalTypeMapping(t *testing.T) {
	cases := []struct {
		t    LogicalType
		want PhysicalType
	}{
		{Of(LogicalTypeIDBoolean), PhysicalTypeBool},
		{Of(LogicalTypeIDTinyInt), PhysicalTypeInt8},
		{Of(LogicalTypeIDUTinyInt), PhysicalTypeUint8},
		{Of(LogicalTypeIDInteger), PhysicalTypeInt32},
		{Of(LogicalTypeIDBigInt), PhysicalTypeInt64},
		{Of(LogicalTypeIDDouble), PhysicalTypeDouble},
		{Of(LogicalTypeIDVarchar), PhysicalTypeVarchar},
		{Of(LogicalTypeIDHugeInt), PhysicalTypeInt128},
		{Of(LogicalTypeIDUUID), PhysicalTypeInt128},
		{Decimal(4, 2), PhysicalTypeInt16},
		{Decimal(9, 2), PhysicalTypeInt32},
		{Decimal(18, 2), PhysicalTypeInt64},
		{Decimal(38, 2), PhysicalTypeInt128},
	}
	for _, c := range cases {
		if got := GetPhysicalType(c.t); got != c.want {
			t.Errorf("GetPhysicalType(%s): got %d want %d", c.t.ID, got, c.want)
		}
	}
}

func TestPhysicalTypeIsConstantSize(t *testing.T) {
	for _, p := range []PhysicalType{
		PhysicalTypeBool, PhysicalTypeInt32, PhysicalTypeInt64, PhysicalTypeDouble,
		PhysicalTypeInt128, PhysicalTypeInterval,
	} {
		if !p.IsConstantSize() {
			t.Errorf("%d should be constant-size", p)
		}
	}
	for _, p := range []PhysicalType{
		PhysicalTypeVarchar, PhysicalTypeStruct, PhysicalTypeList, PhysicalTypeArray,
	} {
		if p.IsConstantSize() {
			t.Errorf("%d should not be constant-size", p)
		}
	}
}

func TestAnyTypeRoundTripsCastScore(t *testing.T) {
	original := LogicalType{
		ID:       LogicalTypeIDAny,
		TypeInfo: AnyTypeInfo{TargetType: Of(LogicalTypeIDInteger), CastScore: big.NewInt(42)},
	}
	w := codec.NewBinaryWriter(0)
	EncodeLogicalType(w, original)
	r := codec.NewBinaryReader(w.Bytes())
	got := DecodeLogicalType(r)
	gotInfo, ok := got.TypeInfo.(AnyTypeInfo)
	if !ok {
		t.Fatalf("expected AnyTypeInfo, got %T", got.TypeInfo)
	}
	if gotInfo.CastScore.Int64() != 42 {
		t.Errorf("CastScore: got %s want 42", gotInfo.CastScore.String())
	}
}
