// Package quacktype models DuckDB's logical and physical type system as it
// appears on the Quack wire. It is a clean-room Go port of the type/
// package in the sibling JDBC driver at https://github.com/gizmodata/quack-jdbc.
package quacktype

// LogicalTypeID is DuckDB's logical type discriminator as carried on the Quack wire.
type LogicalTypeID int

const (
	LogicalTypeIDInvalid         LogicalTypeID = 0
	LogicalTypeIDSQLNull         LogicalTypeID = 1
	LogicalTypeIDUnknown         LogicalTypeID = 2
	LogicalTypeIDAny             LogicalTypeID = 3
	LogicalTypeIDUnbound         LogicalTypeID = 4
	LogicalTypeIDTemplate        LogicalTypeID = 5
	LogicalTypeIDType            LogicalTypeID = 6
	LogicalTypeIDBoolean         LogicalTypeID = 10
	LogicalTypeIDTinyInt         LogicalTypeID = 11
	LogicalTypeIDSmallInt        LogicalTypeID = 12
	LogicalTypeIDInteger         LogicalTypeID = 13
	LogicalTypeIDBigInt          LogicalTypeID = 14
	LogicalTypeIDDate            LogicalTypeID = 15
	LogicalTypeIDTime            LogicalTypeID = 16
	LogicalTypeIDTimestampSec    LogicalTypeID = 17
	LogicalTypeIDTimestampMS     LogicalTypeID = 18
	LogicalTypeIDTimestamp       LogicalTypeID = 19
	LogicalTypeIDTimestampNS     LogicalTypeID = 20
	LogicalTypeIDDecimal         LogicalTypeID = 21
	LogicalTypeIDFloat           LogicalTypeID = 22
	LogicalTypeIDDouble          LogicalTypeID = 23
	LogicalTypeIDChar            LogicalTypeID = 24
	LogicalTypeIDVarchar         LogicalTypeID = 25
	LogicalTypeIDBlob            LogicalTypeID = 26
	LogicalTypeIDInterval        LogicalTypeID = 27
	LogicalTypeIDUTinyInt        LogicalTypeID = 28
	LogicalTypeIDUSmallInt       LogicalTypeID = 29
	LogicalTypeIDUInteger        LogicalTypeID = 30
	LogicalTypeIDUBigInt         LogicalTypeID = 31
	LogicalTypeIDTimestampTZ     LogicalTypeID = 32
	LogicalTypeIDTimeTZ          LogicalTypeID = 34
	LogicalTypeIDTimeNS          LogicalTypeID = 35
	LogicalTypeIDBit             LogicalTypeID = 36
	LogicalTypeIDStringLiteral   LogicalTypeID = 37
	LogicalTypeIDIntegerLiteral  LogicalTypeID = 38
	LogicalTypeIDBigNum          LogicalTypeID = 39
	LogicalTypeIDUHugeInt        LogicalTypeID = 49
	LogicalTypeIDHugeInt         LogicalTypeID = 50
	LogicalTypeIDPointer         LogicalTypeID = 51
	LogicalTypeIDValidity        LogicalTypeID = 53
	LogicalTypeIDUUID            LogicalTypeID = 54
	LogicalTypeIDGeometry        LogicalTypeID = 60
	LogicalTypeIDStruct          LogicalTypeID = 100
	LogicalTypeIDList            LogicalTypeID = 101
	LogicalTypeIDMap             LogicalTypeID = 102
	LogicalTypeIDTable           LogicalTypeID = 103
	LogicalTypeIDEnum            LogicalTypeID = 104
	LogicalTypeIDAggregateState  LogicalTypeID = 105
	LogicalTypeIDLambda          LogicalTypeID = 106
	LogicalTypeIDUnion           LogicalTypeID = 107
	LogicalTypeIDArray           LogicalTypeID = 108
	LogicalTypeIDVariant         LogicalTypeID = 109
)

var logicalTypeIDNames = map[LogicalTypeID]string{
	LogicalTypeIDInvalid: "INVALID", LogicalTypeIDSQLNull: "SQLNULL",
	LogicalTypeIDUnknown: "UNKNOWN", LogicalTypeIDAny: "ANY",
	LogicalTypeIDUnbound: "UNBOUND", LogicalTypeIDTemplate: "TEMPLATE",
	LogicalTypeIDType: "TYPE", LogicalTypeIDBoolean: "BOOLEAN",
	LogicalTypeIDTinyInt: "TINYINT", LogicalTypeIDSmallInt: "SMALLINT",
	LogicalTypeIDInteger: "INTEGER", LogicalTypeIDBigInt: "BIGINT",
	LogicalTypeIDDate: "DATE", LogicalTypeIDTime: "TIME",
	LogicalTypeIDTimestampSec: "TIMESTAMP_SEC", LogicalTypeIDTimestampMS: "TIMESTAMP_MS",
	LogicalTypeIDTimestamp: "TIMESTAMP", LogicalTypeIDTimestampNS: "TIMESTAMP_NS",
	LogicalTypeIDDecimal: "DECIMAL", LogicalTypeIDFloat: "FLOAT",
	LogicalTypeIDDouble: "DOUBLE", LogicalTypeIDChar: "CHAR",
	LogicalTypeIDVarchar: "VARCHAR", LogicalTypeIDBlob: "BLOB",
	LogicalTypeIDInterval: "INTERVAL", LogicalTypeIDUTinyInt: "UTINYINT",
	LogicalTypeIDUSmallInt: "USMALLINT", LogicalTypeIDUInteger: "UINTEGER",
	LogicalTypeIDUBigInt: "UBIGINT", LogicalTypeIDTimestampTZ: "TIMESTAMP_TZ",
	LogicalTypeIDTimeTZ: "TIME_TZ", LogicalTypeIDTimeNS: "TIME_NS",
	LogicalTypeIDBit: "BIT", LogicalTypeIDStringLiteral: "STRING_LITERAL",
	LogicalTypeIDIntegerLiteral: "INTEGER_LITERAL", LogicalTypeIDBigNum: "BIGNUM",
	LogicalTypeIDUHugeInt: "UHUGEINT", LogicalTypeIDHugeInt: "HUGEINT",
	LogicalTypeIDPointer: "POINTER", LogicalTypeIDValidity: "VALIDITY",
	LogicalTypeIDUUID: "UUID", LogicalTypeIDGeometry: "GEOMETRY",
	LogicalTypeIDStruct: "STRUCT", LogicalTypeIDList: "LIST",
	LogicalTypeIDMap: "MAP", LogicalTypeIDTable: "TABLE",
	LogicalTypeIDEnum: "ENUM", LogicalTypeIDAggregateState: "AGGREGATE_STATE",
	LogicalTypeIDLambda: "LAMBDA", LogicalTypeIDUnion: "UNION",
	LogicalTypeIDArray: "ARRAY", LogicalTypeIDVariant: "VARIANT",
}

func (id LogicalTypeID) String() string {
	if name, ok := logicalTypeIDNames[id]; ok {
		return name
	}
	return "UNKNOWN_LOGICAL_TYPE"
}

// PhysicalType is DuckDB's physical storage layout for a column vector.
type PhysicalType int

const (
	PhysicalTypeInvalid  PhysicalType = 255
	PhysicalTypeBool     PhysicalType = 1
	PhysicalTypeUint8    PhysicalType = 2
	PhysicalTypeInt8     PhysicalType = 3
	PhysicalTypeUint16   PhysicalType = 4
	PhysicalTypeInt16    PhysicalType = 5
	PhysicalTypeUint32   PhysicalType = 6
	PhysicalTypeInt32    PhysicalType = 7
	PhysicalTypeUint64   PhysicalType = 8
	PhysicalTypeInt64    PhysicalType = 9
	PhysicalTypeFloat    PhysicalType = 11
	PhysicalTypeDouble   PhysicalType = 12
	PhysicalTypeInterval PhysicalType = 21
	PhysicalTypeList     PhysicalType = 23
	PhysicalTypeStruct   PhysicalType = 24
	PhysicalTypeArray    PhysicalType = 29
	PhysicalTypeVarchar  PhysicalType = 200
	PhysicalTypeUint128  PhysicalType = 203
	PhysicalTypeInt128   PhysicalType = 204
	PhysicalTypeUnknown  PhysicalType = 205
	PhysicalTypeBit      PhysicalType = 206
)

// IsConstantSize reports whether vectors of this physical type use fixed-width values.
func (p PhysicalType) IsConstantSize() bool {
	switch p {
	case PhysicalTypeBool, PhysicalTypeUint8, PhysicalTypeInt8,
		PhysicalTypeUint16, PhysicalTypeInt16, PhysicalTypeUint32, PhysicalTypeInt32,
		PhysicalTypeUint64, PhysicalTypeInt64, PhysicalTypeFloat, PhysicalTypeDouble,
		PhysicalTypeInterval, PhysicalTypeUint128, PhysicalTypeInt128:
		return true
	}
	return false
}

// ByteWidth returns the on-wire byte width for a constant-size physical type.
// Panics for variable-width types.
func (p PhysicalType) ByteWidth() int {
	switch p {
	case PhysicalTypeBool, PhysicalTypeUint8, PhysicalTypeInt8:
		return 1
	case PhysicalTypeUint16, PhysicalTypeInt16:
		return 2
	case PhysicalTypeUint32, PhysicalTypeInt32, PhysicalTypeFloat:
		return 4
	case PhysicalTypeUint64, PhysicalTypeInt64, PhysicalTypeDouble:
		return 8
	case PhysicalTypeInterval, PhysicalTypeUint128, PhysicalTypeInt128:
		return 16
	}
	panic("quacktype: PhysicalType is not fixed size")
}

// ExtraTypeInfoType discriminates among ExtraTypeInfo variants.
type ExtraTypeInfoType int

const (
	ExtraTypeInfoTypeInvalid         ExtraTypeInfoType = 0
	ExtraTypeInfoTypeGeneric         ExtraTypeInfoType = 1
	ExtraTypeInfoTypeDecimal         ExtraTypeInfoType = 2
	ExtraTypeInfoTypeString          ExtraTypeInfoType = 3
	ExtraTypeInfoTypeList            ExtraTypeInfoType = 4
	ExtraTypeInfoTypeStruct          ExtraTypeInfoType = 5
	ExtraTypeInfoTypeEnum            ExtraTypeInfoType = 6
	ExtraTypeInfoTypeUnbound         ExtraTypeInfoType = 7
	ExtraTypeInfoTypeAggregateState  ExtraTypeInfoType = 8
	ExtraTypeInfoTypeArray           ExtraTypeInfoType = 9
	ExtraTypeInfoTypeAny             ExtraTypeInfoType = 10
	ExtraTypeInfoTypeIntegerLiteral  ExtraTypeInfoType = 11
	ExtraTypeInfoTypeTemplate        ExtraTypeInfoType = 12
	ExtraTypeInfoTypeGeo             ExtraTypeInfoType = 13
)
