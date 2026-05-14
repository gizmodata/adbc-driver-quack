package quack

import (
	"context"
	"fmt"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// GetTableTypes returns the small fixed set of DuckDB table-type names.
// Mirrors the result of `org.duckdb.DuckDBDatabaseMetaData.getTableTypes()`
// in the sibling JDBC driver.
func (c *connectionImpl) getTableTypesImpl(_ context.Context) (array.RecordReader, error) {
	values := []string{"TABLE", "LOCAL TEMPORARY", "VIEW", "SYSTEM VIEW"}
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "table_type", Type: arrow.BinaryTypes.String, Nullable: false},
	}, nil)
	b := array.NewRecordBuilder(c.alloc, schema)
	defer b.Release()
	sb := b.Field(0).(*array.StringBuilder)
	for _, v := range values {
		sb.Append(v)
	}
	rec := b.NewRecord()
	defer rec.Release()
	rr, err := array.NewRecordReader(schema, []arrow.Record{rec})
	if err != nil {
		return nil, err
	}
	return rr, nil
}

// getTableSchemaImpl runs the same `duckdb_columns()` query
// QuackDatabaseMetaData uses to enumerate columns, builds an arrow.Schema
// from the result.
func (c *connectionImpl) getTableSchemaImpl(ctx context.Context, catalog, dbSchema *string, tableName string) (*arrow.Schema, error) {
	if tableName == "" {
		return nil, errStatus(adbc.StatusInvalidArgument, "GetTableSchema: tableName is required")
	}
	q := "SELECT column_name, data_type, is_nullable FROM duckdb_columns() WHERE table_name = " + sqlString(tableName)
	if catalog != nil && *catalog != "" {
		q += " AND database_name = " + sqlString(*catalog)
	}
	if dbSchema != nil && *dbSchema != "" {
		q += " AND schema_name = " + sqlString(*dbSchema)
	}
	q += " ORDER BY column_index"

	result, err := c.sess.drainPrepared(ctx, q)
	if err != nil {
		return nil, fromTransportError(err)
	}
	if len(result.ColumnTypes) < 2 {
		return nil, errStatus(adbc.StatusInternal, "GetTableSchema: unexpected metadata result shape")
	}
	fields := []arrow.Field{}
	for _, chunk := range result.Chunks {
		nameVec := chunk.Columns[0]
		typeVec := chunk.Columns[1]
		var nullableVec interface {
			IsNull(int) bool
			GetObject(int) interface{}
		} = chunk.Columns[2]
		for i := 0; i < chunk.RowCount; i++ {
			colName, _ := nameVec.GetObject(i).(string)
			dataType, _ := typeVec.GetObject(i).(string)
			nullable := true
			if !nullableVec.IsNull(i) {
				if b, ok := nullableVec.GetObject(i).(bool); ok {
					nullable = b
				}
			}
			fields = append(fields, arrow.Field{
				Name:     colName,
				Type:     arrowTypeForDuckDBName(dataType),
				Nullable: nullable,
			})
		}
	}
	if len(fields) == 0 {
		return nil, errStatus(adbc.StatusNotFound, "table %q not found", tableName)
	}
	return arrow.NewSchema(fields, nil), nil
}

// arrowTypeForDuckDBName maps DuckDB's printed type string (e.g.
// "INTEGER", "VARCHAR", "DECIMAL(10,2)") to an arrow.DataType. Mirrors
// the dataMap used in QuackDatabaseMetaData / DuckDBResultSetMetaData.
func arrowTypeForDuckDBName(s string) arrow.DataType {
	// Strip any parenthesized parameters first.
	base := s
	for i := 0; i < len(s); i++ {
		if s[i] == '(' {
			base = s[:i]
			break
		}
	}
	switch base {
	case "BOOLEAN":
		return arrow.FixedWidthTypes.Boolean
	case "TINYINT":
		return arrow.PrimitiveTypes.Int8
	case "SMALLINT", "UTINYINT":
		return arrow.PrimitiveTypes.Int16
	case "INTEGER", "USMALLINT":
		return arrow.PrimitiveTypes.Int32
	case "BIGINT", "UINTEGER":
		return arrow.PrimitiveTypes.Int64
	case "UBIGINT":
		return arrow.PrimitiveTypes.Uint64
	case "FLOAT":
		return arrow.PrimitiveTypes.Float32
	case "DOUBLE":
		return arrow.PrimitiveTypes.Float64
	case "DECIMAL":
		// Best-effort: pull precision,scale out of the original `DECIMAL(p,s)`.
		p, sc, ok := parseDecimal(s)
		if !ok {
			p, sc = 38, 0
		}
		return &arrow.Decimal128Type{Precision: int32(p), Scale: int32(sc)}
	case "VARCHAR", "CHAR":
		return arrow.BinaryTypes.String
	case "BLOB", "BIT":
		return arrow.BinaryTypes.Binary
	case "DATE":
		return arrow.FixedWidthTypes.Date32
	case "TIME":
		return arrow.FixedWidthTypes.Time64us
	case "TIMESTAMP":
		return arrow.FixedWidthTypes.Timestamp_us
	case "TIMESTAMP WITH TIME ZONE":
		return &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}
	case "UUID":
		return &arrow.FixedSizeBinaryType{ByteWidth: 16}
	}
	return arrow.BinaryTypes.String
}

func parseDecimal(s string) (precision, scale int, ok bool) {
	// expects "DECIMAL(p,s)"
	if len(s) < 11 || s[7] != '(' {
		return 0, 0, false
	}
	rest := s[8:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == ',' {
			pStr := rest[:i]
			rest2 := rest[i+1:]
			for j := 0; j < len(rest2); j++ {
				if rest2[j] == ')' {
					sStr := rest2[:j]
					_, err := fmt.Sscanf(pStr, "%d", &precision)
					if err != nil {
						return 0, 0, false
					}
					_, err = fmt.Sscanf(sStr, "%d", &scale)
					if err != nil {
						return 0, 0, false
					}
					return precision, scale, true
				}
			}
		}
	}
	return 0, 0, false
}

// sqlString renders a Go string as a single-quoted SQL literal (with
// quote doubling for escapes). Same trick as quack-jdbc's SqlLiteral.
func sqlString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
		} else {
			out = append(out, s[i])
		}
	}
	out = append(out, '\'')
	return string(out)
}

// Wire metadata to the connection. The driver-level interface methods
// already exist; these just unlock the implementations.
func (c *connectionImpl) reroute() {
	// no-op anchor for tooling
	_ = memory.NewGoAllocator
	_ = quacktype.LogicalTypeIDInvalid
}
