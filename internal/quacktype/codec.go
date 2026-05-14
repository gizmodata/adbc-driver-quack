package quacktype

import (
	"fmt"
	"math/big"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
)

// EncodeLogicalType writes a logical type to the BinarySerializer wire format.
func EncodeLogicalType(w *codec.BinaryWriter, t LogicalType) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(t.ID)) })
		if t.TypeInfo != nil {
			obj.WriteField(101, func(o *codec.BinaryWriter) {
				obj.WriteBool(true)
				EncodeExtraTypeInfo(o, t.TypeInfo)
			})
		}
	})
}

// DecodeLogicalType reads a logical type from the wire.
func DecodeLogicalType(r *codec.BinaryReader) LogicalType {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) LogicalType {
		id := LogicalTypeID(codec.ReadRequiredField(rr, 100, func(rrr *codec.BinaryReader) int {
			return rrr.ReadULEB128Int()
		}))
		info := codec.ReadOptionalField(rr, 101, func(rrr *codec.BinaryReader) ExtraTypeInfo {
			info, _ := codec.ReadNullable(rrr, DecodeExtraTypeInfo)
			return info
		}, nil)
		return LogicalType{ID: id, TypeInfo: info}
	})
	return v
}

// EncodeExtraTypeInfo writes a single ExtraTypeInfo to the wire.
func EncodeExtraTypeInfo(w *codec.BinaryWriter, info ExtraTypeInfo) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(info.Kind())) })
		if alias := info.GetAlias(); alias != "" {
			obj.WriteField(101, func(o *codec.BinaryWriter) { o.WriteString(alias) })
		}
		switch v := info.(type) {
		case GenericTypeInfo:
			// no payload
		case DecimalTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(v.Width)) })
			obj.WriteField(201, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(v.Scale)) })
		case StringTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteString(v.Collation) })
		case ListTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { EncodeLogicalType(o, v.ChildType) })
		case StructTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { encodeChildTypes(o, v.ChildTypes) })
		case EnumTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(len(v.Values))) })
			obj.WriteField(201, func(o *codec.BinaryWriter) {
				codec.WriteList(o, v.Values, func(_ int, s string, ww *codec.BinaryWriter) { ww.WriteString(s) })
			})
		case AggregateStateTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteString(v.FunctionName) })
			obj.WriteField(201, func(o *codec.BinaryWriter) { EncodeLogicalType(o, v.ReturnType) })
			obj.WriteField(202, func(o *codec.BinaryWriter) {
				codec.WriteList(o, v.BoundArgumentTypes, func(_ int, t LogicalType, ww *codec.BinaryWriter) {
					EncodeLogicalType(ww, t)
				})
			})
		case ArrayTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { EncodeLogicalType(o, v.ChildType) })
			obj.WriteField(201, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(v.Size)) })
		case AnyTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { EncodeLogicalType(o, v.TargetType) })
			score := uint64(0)
			if v.CastScore != nil {
				score = v.CastScore.Uint64()
			}
			obj.WriteField(201, func(o *codec.BinaryWriter) { o.WriteULEB128(score) })
		case TemplateTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteString(v.Name) })
		case IntegerLiteralTypeInfo:
			// not encodable
		case GeoTypeInfo:
			obj.WriteField(200, func(o *codec.BinaryWriter) { encodeCRS(o, v.CRSDefinition) })
		case UnboundTypeInfo:
			if v.Name != "" {
				obj.WriteField(200, func(o *codec.BinaryWriter) { o.WriteString(v.Name) })
			}
			if v.Catalog != "" {
				obj.WriteField(201, func(o *codec.BinaryWriter) { o.WriteString(v.Catalog) })
			}
			if v.Schema != "" {
				obj.WriteField(202, func(o *codec.BinaryWriter) { o.WriteString(v.Schema) })
			}
		}
	})
}

// DecodeExtraTypeInfo reads a single ExtraTypeInfo from the wire.
func DecodeExtraTypeInfo(r *codec.BinaryReader) ExtraTypeInfo {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) ExtraTypeInfo {
		kind := ExtraTypeInfoType(codec.ReadRequiredField(rr, 100, func(rrr *codec.BinaryReader) int {
			return rrr.ReadULEB128Int()
		}))
		alias := codec.ReadOptionalField(rr, 101, func(rrr *codec.BinaryReader) string {
			return rrr.ReadString()
		}, "")
		// extension type metadata (field 103) — not supported, just refuse to consume non-null
		codec.ReadOptionalField(rr, 103, func(rrr *codec.BinaryReader) any {
			_, present := codec.ReadNullable(rrr, func(rrrr *codec.BinaryReader) any { return nil })
			if present {
				// unsupported — would have needed schema we don't carry
			}
			return nil
		}, nil)

		switch kind {
		case ExtraTypeInfoTypeInvalid, ExtraTypeInfoTypeGeneric:
			return GenericTypeInfo{KindValue: kind, Alias: alias}
		case ExtraTypeInfoTypeDecimal:
			width := codec.ReadOptionalField(rr, 200, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() }, 0)
			scale := codec.ReadOptionalField(rr, 201, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() }, 0)
			return DecimalTypeInfo{Width: width, Scale: scale, Alias: alias}
		case ExtraTypeInfoTypeString:
			coll := codec.ReadOptionalField(rr, 200, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
			return StringTypeInfo{Collation: coll, Alias: alias}
		case ExtraTypeInfoTypeList:
			child := codec.ReadRequiredField(rr, 200, DecodeLogicalType)
			return ListTypeInfo{ChildType: child, Alias: alias}
		case ExtraTypeInfoTypeStruct:
			children := codec.ReadOptionalField(rr, 200, decodeChildTypes, nil)
			return StructTypeInfo{ChildTypes: children, Alias: alias}
		case ExtraTypeInfoTypeEnum:
			valuesCount := codec.ReadRequiredField(rr, 200, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() })
			values := codec.ReadRequiredField(rr, 201, func(rrr *codec.BinaryReader) []string {
				return codec.ReadList(rrr, func(_ int, rrrr *codec.BinaryReader) string { return rrrr.ReadString() })
			})
			if len(values) != valuesCount {
				rr.AssertEOF() // force an error path
			}
			return EnumTypeInfo{Values: values, Alias: alias}
		case ExtraTypeInfoTypeAggregateState:
			fn := codec.ReadRequiredField(rr, 200, func(rrr *codec.BinaryReader) string { return rrr.ReadString() })
			ret := codec.ReadRequiredField(rr, 201, DecodeLogicalType)
			args := codec.ReadRequiredField(rr, 202, func(rrr *codec.BinaryReader) []LogicalType {
				return codec.ReadList(rrr, func(_ int, rrrr *codec.BinaryReader) LogicalType { return DecodeLogicalType(rrrr) })
			})
			return AggregateStateTypeInfo{FunctionName: fn, ReturnType: ret, BoundArgumentTypes: args, Alias: alias}
		case ExtraTypeInfoTypeArray:
			child := codec.ReadRequiredField(rr, 200, DecodeLogicalType)
			size := codec.ReadRequiredField(rr, 201, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() })
			return ArrayTypeInfo{ChildType: child, Size: size, Alias: alias}
		case ExtraTypeInfoTypeAny:
			target := codec.ReadRequiredField(rr, 200, DecodeLogicalType)
			score := codec.ReadRequiredField(rr, 201, func(rrr *codec.BinaryReader) *big.Int {
				return rrr.ReadULEB128BigInt()
			})
			return AnyTypeInfo{TargetType: target, CastScore: score, Alias: alias}
		case ExtraTypeInfoTypeIntegerLiteral:
			return IntegerLiteralTypeInfo{Alias: alias}
		case ExtraTypeInfoTypeTemplate:
			name := codec.ReadOptionalField(rr, 200, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
			return TemplateTypeInfo{Name: name, Alias: alias}
		case ExtraTypeInfoTypeGeo:
			crs := codec.ReadOptionalField(rr, 200, decodeCRS, "")
			return GeoTypeInfo{CRSDefinition: crs, Alias: alias}
		case ExtraTypeInfoTypeUnbound:
			name := codec.ReadOptionalField(rr, 200, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
			cat := codec.ReadOptionalField(rr, 201, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
			schema := codec.ReadOptionalField(rr, 202, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
			return UnboundTypeInfo{Name: name, Catalog: cat, Schema: schema, Alias: alias}
		}
		return GenericTypeInfo{KindValue: kind, Alias: alias}
	})
	return v
}

func encodeChildTypes(w *codec.BinaryWriter, children []ChildType) {
	codec.WriteList(w, children, func(_ int, c ChildType, ww *codec.BinaryWriter) {
		ww.WriteObject(func(pair *codec.BinaryWriter) {
			pair.WriteField(0, func(o *codec.BinaryWriter) { o.WriteString(c.Name) })
			pair.WriteField(1, func(o *codec.BinaryWriter) { EncodeLogicalType(o, c.Type) })
		})
	})
}

func decodeChildTypes(r *codec.BinaryReader) []ChildType {
	return codec.ReadList(r, func(_ int, rr *codec.BinaryReader) ChildType {
		v, _ := codec.ReadObject(rr, func(pair *codec.BinaryReader) ChildType {
			name := codec.ReadRequiredField(pair, 0, func(rrr *codec.BinaryReader) string { return rrr.ReadString() })
			t := codec.ReadRequiredField(pair, 1, DecodeLogicalType)
			return ChildType{Name: name, Type: t}
		})
		return v
	})
}

func encodeCRS(w *codec.BinaryWriter, definition string) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		if definition != "" {
			obj.WriteField(100, func(o *codec.BinaryWriter) { o.WriteString(definition) })
		}
	})
}

func decodeCRS(r *codec.BinaryReader) string {
	v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) string {
		return codec.ReadOptionalField(obj, 100, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
	})
	return v
}

// Ensures we don't accidentally unused-import fmt above (placeholder).
var _ = fmt.Sprint
