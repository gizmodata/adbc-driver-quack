package quacktype

import (
	"fmt"
	"math/big"
)

// LogicalType is a DuckDB logical type plus optional type-specific metadata.
// A zero TypeInfo means "no extra info".
type LogicalType struct {
	ID       LogicalTypeID
	TypeInfo ExtraTypeInfo
}

// Of creates a logical type with no extra info.
func Of(id LogicalTypeID) LogicalType {
	return LogicalType{ID: id}
}

// OfWithInfo creates a logical type with extra info.
func OfWithInfo(id LogicalTypeID, info ExtraTypeInfo) LogicalType {
	return LogicalType{ID: id, TypeInfo: info}
}

// Decimal creates a DECIMAL(width, scale) type.
func Decimal(width, scale int) LogicalType {
	return LogicalType{
		ID:       LogicalTypeIDDecimal,
		TypeInfo: DecimalTypeInfo{Width: width, Scale: scale},
	}
}

// List creates a LIST<childType>.
func List(child LogicalType) LogicalType {
	return LogicalType{
		ID:       LogicalTypeIDList,
		TypeInfo: ListTypeInfo{ChildType: child},
	}
}

// Array creates an ARRAY<childType>[size].
func Array(child LogicalType, size int) LogicalType {
	return LogicalType{
		ID:       LogicalTypeIDArray,
		TypeInfo: ArrayTypeInfo{ChildType: child, Size: size},
	}
}

// Struct creates a STRUCT(named children).
func Struct(children []ChildType) LogicalType {
	return LogicalType{
		ID:       LogicalTypeIDStruct,
		TypeInfo: StructTypeInfo{ChildTypes: children},
	}
}

// Enum creates an ENUM type with the given ordered values.
func Enum(values []string) LogicalType {
	return LogicalType{
		ID:       LogicalTypeIDEnum,
		TypeInfo: EnumTypeInfo{Values: values},
	}
}

// ChildType is a named field within a STRUCT (or similar) logical type.
type ChildType struct {
	Name string
	Type LogicalType
}

// ExtraTypeInfo is the marker interface for DuckDB ExtraTypeInfo variants.
// Use a type-switch on the concrete struct to discriminate.
type ExtraTypeInfo interface {
	Kind() ExtraTypeInfoType
	GetAlias() string
	extraTypeInfo() // sentinel — sealed within this package
}

// GenericTypeInfo carries no metadata beyond the kind/alias pair.
type GenericTypeInfo struct {
	KindValue ExtraTypeInfoType
	Alias     string
}

func (g GenericTypeInfo) Kind() ExtraTypeInfoType { return g.KindValue }
func (g GenericTypeInfo) GetAlias() string        { return g.Alias }
func (GenericTypeInfo) extraTypeInfo()            {}

// DecimalTypeInfo carries DECIMAL precision/scale.
type DecimalTypeInfo struct {
	Width int
	Scale int
	Alias string
}

func (DecimalTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeDecimal }
func (d DecimalTypeInfo) GetAlias() string      { return d.Alias }
func (DecimalTypeInfo) extraTypeInfo()          {}

// StringTypeInfo carries optional collation metadata.
type StringTypeInfo struct {
	Collation string
	Alias     string
}

func (StringTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeString }
func (s StringTypeInfo) GetAlias() string      { return s.Alias }
func (StringTypeInfo) extraTypeInfo()          {}

// ListTypeInfo carries the child type for LIST and MAP.
type ListTypeInfo struct {
	ChildType LogicalType
	Alias     string
}

func (ListTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeList }
func (l ListTypeInfo) GetAlias() string      { return l.Alias }
func (ListTypeInfo) extraTypeInfo()          {}

// StructTypeInfo carries named child fields for STRUCT.
type StructTypeInfo struct {
	ChildTypes []ChildType
	Alias      string
}

func (StructTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeStruct }
func (s StructTypeInfo) GetAlias() string      { return s.Alias }
func (StructTypeInfo) extraTypeInfo()          {}

// EnumTypeInfo carries the ordered string values for an ENUM.
type EnumTypeInfo struct {
	Values []string
	Alias  string
}

func (EnumTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeEnum }
func (e EnumTypeInfo) GetAlias() string      { return e.Alias }
func (EnumTypeInfo) extraTypeInfo()          {}

// AggregateStateTypeInfo carries the function-name + return-type + bound args.
type AggregateStateTypeInfo struct {
	FunctionName       string
	ReturnType         LogicalType
	BoundArgumentTypes []LogicalType
	Alias              string
}

func (AggregateStateTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeAggregateState }
func (a AggregateStateTypeInfo) GetAlias() string      { return a.Alias }
func (AggregateStateTypeInfo) extraTypeInfo()          {}

// ArrayTypeInfo carries the child type + fixed array size.
type ArrayTypeInfo struct {
	ChildType LogicalType
	Size      int
	Alias     string
}

func (ArrayTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeArray }
func (a ArrayTypeInfo) GetAlias() string      { return a.Alias }
func (ArrayTypeInfo) extraTypeInfo()          {}

// AnyTypeInfo carries the target type + cast score for ANY.
type AnyTypeInfo struct {
	TargetType LogicalType
	CastScore  *big.Int
	Alias      string
}

func (AnyTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeAny }
func (a AnyTypeInfo) GetAlias() string      { return a.Alias }
func (AnyTypeInfo) extraTypeInfo()          {}

// TemplateTypeInfo carries the template type's name.
type TemplateTypeInfo struct {
	Name  string
	Alias string
}

func (TemplateTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeTemplate }
func (t TemplateTypeInfo) GetAlias() string      { return t.Alias }
func (TemplateTypeInfo) extraTypeInfo()          {}

// IntegerLiteralTypeInfo is a marker for INTEGER_LITERAL ExtraTypeInfo (no payload).
type IntegerLiteralTypeInfo struct {
	Alias string
}

func (IntegerLiteralTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeIntegerLiteral }
func (i IntegerLiteralTypeInfo) GetAlias() string      { return i.Alias }
func (IntegerLiteralTypeInfo) extraTypeInfo()          {}

// GeoTypeInfo carries optional CRS definition for GEOMETRY.
type GeoTypeInfo struct {
	CRSDefinition string
	Alias         string
}

func (GeoTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeGeo }
func (g GeoTypeInfo) GetAlias() string      { return g.Alias }
func (GeoTypeInfo) extraTypeInfo()          {}

// UnboundTypeInfo carries lookup hints for an unbound type reference.
type UnboundTypeInfo struct {
	Name    string
	Catalog string
	Schema  string
	Alias   string
}

func (UnboundTypeInfo) Kind() ExtraTypeInfoType { return ExtraTypeInfoTypeUnbound }
func (u UnboundTypeInfo) GetAlias() string      { return u.Alias }
func (UnboundTypeInfo) extraTypeInfo()          {}

// GetPhysicalType returns the wire physical type for a logical type.
func GetPhysicalType(t LogicalType) PhysicalType {
	switch t.ID {
	case LogicalTypeIDBoolean:
		return PhysicalTypeBool
	case LogicalTypeIDTinyInt:
		return PhysicalTypeInt8
	case LogicalTypeIDUTinyInt:
		return PhysicalTypeUint8
	case LogicalTypeIDSmallInt:
		return PhysicalTypeInt16
	case LogicalTypeIDUSmallInt:
		return PhysicalTypeUint16
	case LogicalTypeIDSQLNull, LogicalTypeIDDate, LogicalTypeIDInteger:
		return PhysicalTypeInt32
	case LogicalTypeIDUInteger:
		return PhysicalTypeUint32
	case LogicalTypeIDBigInt, LogicalTypeIDTime, LogicalTypeIDTimeNS,
		LogicalTypeIDTimestamp, LogicalTypeIDTimestampSec, LogicalTypeIDTimestampNS,
		LogicalTypeIDTimestampMS, LogicalTypeIDTimeTZ, LogicalTypeIDTimestampTZ:
		return PhysicalTypeInt64
	case LogicalTypeIDUBigInt:
		return PhysicalTypeUint64
	case LogicalTypeIDUHugeInt:
		return PhysicalTypeUint128
	case LogicalTypeIDHugeInt, LogicalTypeIDUUID:
		return PhysicalTypeInt128
	case LogicalTypeIDFloat:
		return PhysicalTypeFloat
	case LogicalTypeIDDouble:
		return PhysicalTypeDouble
	case LogicalTypeIDDecimal:
		return getDecimalPhysicalType(t)
	case LogicalTypeIDBigNum, LogicalTypeIDVarchar, LogicalTypeIDChar,
		LogicalTypeIDBlob, LogicalTypeIDBit, LogicalTypeIDType,
		LogicalTypeIDAggregateState, LogicalTypeIDGeometry:
		return PhysicalTypeVarchar
	case LogicalTypeIDInterval:
		return PhysicalTypeInterval
	case LogicalTypeIDUnion, LogicalTypeIDVariant, LogicalTypeIDStruct:
		return PhysicalTypeStruct
	case LogicalTypeIDList, LogicalTypeIDMap:
		return PhysicalTypeList
	case LogicalTypeIDArray:
		return PhysicalTypeArray
	case LogicalTypeIDPointer:
		return PhysicalTypeUint64
	case LogicalTypeIDValidity:
		return PhysicalTypeBit
	case LogicalTypeIDEnum:
		return getEnumPhysicalType(t)
	}
	return PhysicalTypeInvalid
}

// GetChildType returns the child type for LIST, MAP, or ARRAY logical types.
func GetChildType(t LogicalType) (LogicalType, error) {
	switch info := t.TypeInfo.(type) {
	case ListTypeInfo:
		return info.ChildType, nil
	case ArrayTypeInfo:
		return info.ChildType, nil
	}
	return LogicalType{}, fmt.Errorf("quacktype: logical type %s does not have a child type", t.ID)
}

// GetStructChildren returns the named child types for STRUCT-like logical types.
func GetStructChildren(t LogicalType) ([]ChildType, error) {
	if info, ok := t.TypeInfo.(StructTypeInfo); ok {
		return info.ChildTypes, nil
	}
	if t.ID == LogicalTypeIDVariant || t.ID == LogicalTypeIDUnion {
		return nil, nil
	}
	return nil, fmt.Errorf("quacktype: logical type %s does not have struct children", t.ID)
}

// GetArraySize returns the fixed element count for an ARRAY logical type.
func GetArraySize(t LogicalType) (int, error) {
	if info, ok := t.TypeInfo.(ArrayTypeInfo); ok {
		return info.Size, nil
	}
	return 0, fmt.Errorf("quacktype: logical type %s is not an ARRAY", t.ID)
}

// GetEnumValues returns the value list for an ENUM logical type.
func GetEnumValues(t LogicalType) ([]string, error) {
	if info, ok := t.TypeInfo.(EnumTypeInfo); ok {
		return info.Values, nil
	}
	return nil, fmt.Errorf("quacktype: logical type %s is not an ENUM", t.ID)
}

func getDecimalPhysicalType(t LogicalType) PhysicalType {
	info, ok := t.TypeInfo.(DecimalTypeInfo)
	if !ok {
		return PhysicalTypeInvalid
	}
	switch {
	case info.Width <= 4:
		return PhysicalTypeInt16
	case info.Width <= 9:
		return PhysicalTypeInt32
	case info.Width <= 18:
		return PhysicalTypeInt64
	case info.Width <= 38:
		return PhysicalTypeInt128
	}
	return PhysicalTypeInvalid
}

func getEnumPhysicalType(t LogicalType) PhysicalType {
	values, err := GetEnumValues(t)
	if err != nil {
		return PhysicalTypeInvalid
	}
	n := len(values)
	switch {
	case n <= 0xFF:
		return PhysicalTypeUint8
	case n <= 0xFFFF:
		return PhysicalTypeUint16
	}
	return PhysicalTypeUint32
}
