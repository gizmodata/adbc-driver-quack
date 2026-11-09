package quack

import (
	"context"
	"runtime/debug"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-quack/internal/message"
)

// driverInfoValues collects the static string info codes we can fill in
// without a server round-trip.
var driverInfoValues = map[adbc.InfoCode]string{
	adbc.InfoVendorName:         "DuckDB (via Quack)",
	adbc.InfoDriverName:         "ADBC Quack Driver - Go",
	adbc.InfoDriverVersion:      Version,
	adbc.InfoDriverArrowVersion: arrowGoVersion(),
}

// supportedInfoCodes is the default set we emit when the caller passes no codes.
// Covers what DBeaver, Polars, and ibis ask for during connection probe.
var supportedInfoCodes = []adbc.InfoCode{
	adbc.InfoVendorName,
	adbc.InfoVendorVersion,
	adbc.InfoVendorSql,
	adbc.InfoVendorSubstrait,
	adbc.InfoDriverName,
	adbc.InfoDriverVersion,
	adbc.InfoDriverArrowVersion,
	adbc.InfoDriverADBCVersion,
}

// getInfoImpl assembles the GetInfo standard record. Values that need a
// server query (notably VendorVersion) are fetched on demand via
// PRAGMA version.
func (c *connectionImpl) getInfoImpl(ctx context.Context, infoCodes []adbc.InfoCode) (array.RecordReader, error) {
	if len(infoCodes) == 0 {
		infoCodes = supportedInfoCodes
	}

	bldr := array.NewRecordBuilder(c.alloc, adbc.GetInfoSchema)
	defer bldr.Release()
	bldr.Reserve(len(infoCodes))

	nameBldr := bldr.Field(0).(*array.Uint32Builder)
	valueBldr := bldr.Field(1).(*array.DenseUnionBuilder)
	strBldr := valueBldr.Child(int(adbc.InfoValueStringType)).(*array.StringBuilder)
	int64Bldr := valueBldr.Child(int(adbc.InfoValueInt64Type)).(*array.Int64Builder)
	boolBldr := valueBldr.Child(int(adbc.InfoValueBooleanType)).(*array.BooleanBuilder)

	vendorVersion := ""
	for _, code := range infoCodes {
		if code == adbc.InfoVendorVersion && vendorVersion == "" {
			vendorVersion = c.fetchVendorVersion(ctx)
		}
	}

	driverVersion := driverInfoValues[adbc.InfoDriverVersion]

	for _, code := range infoCodes {
		nameBldr.Append(uint32(code))
		var (
			stringValue string
			intValue    int64
			boolValue   bool
			kind        adbc.InfoValueTypeCode
			present     bool
		)
		switch code {
		case adbc.InfoVendorName:
			stringValue, kind, present = driverInfoValues[code], adbc.InfoValueStringType, true
		case adbc.InfoVendorVersion:
			stringValue, kind, present = vendorVersion, adbc.InfoValueStringType, true
		case adbc.InfoVendorSql:
			// Quack speaks SQL — the whole protocol is "send a SQL string,
			// get DataChunks back". Substrait is unsupported on both sides.
			boolValue, kind, present = true, adbc.InfoValueBooleanType, true
		case adbc.InfoVendorSubstrait:
			boolValue, kind, present = false, adbc.InfoValueBooleanType, true
		case adbc.InfoDriverName:
			stringValue, kind, present = driverInfoValues[code], adbc.InfoValueStringType, true
		case adbc.InfoDriverVersion:
			stringValue, kind, present = driverVersion, adbc.InfoValueStringType, true
		case adbc.InfoDriverArrowVersion:
			stringValue, kind, present = driverInfoValues[code], adbc.InfoValueStringType, true
		case adbc.InfoDriverADBCVersion:
			intValue, kind, present = adbc.AdbcVersion1_1_0, adbc.InfoValueInt64Type, true
		default:
			kind = adbc.InfoValueStringType
			present = false
		}

		valueBldr.Append(int8(kind))
		switch kind {
		case adbc.InfoValueStringType:
			if present {
				strBldr.Append(stringValue)
			} else {
				strBldr.AppendNull()
			}
		case adbc.InfoValueInt64Type:
			if present {
				int64Bldr.Append(intValue)
			} else {
				int64Bldr.AppendNull()
			}
		case adbc.InfoValueBooleanType:
			if present {
				boolBldr.Append(boolValue)
			} else {
				boolBldr.AppendNull()
			}
		}
	}

	rec := bldr.NewRecord()
	defer rec.Release()
	return array.NewRecordReader(adbc.GetInfoSchema, []arrow.Record{rec})
}

// fetchVendorVersion returns the server's DuckDB version. The handshake
// reports it, so no query is needed (a query would also supersede a
// result the caller is still reading: Quack runs one statement at a time
// per connection). Falls back to PRAGMA version for a server that left
// it empty.
func (c *connectionImpl) fetchVendorVersion(ctx context.Context) string {
	if c.sess.serverVersion != "" {
		return c.sess.serverVersion
	}
	result, err := c.sess.drainPrepared(ctx, "PRAGMA version")
	if err != nil || len(result.Chunks) == 0 {
		return ""
	}
	chunk := result.Chunks[0]
	if chunk.RowCount == 0 || len(chunk.Columns) == 0 {
		return ""
	}
	v := chunk.Columns[0].GetObject(0)
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return ""
}

// Type assertion to keep imports referenced even if other build tags
// strip code paths.
var _ message.QuackMessage = message.SuccessResponse{}

// arrowGoVersion is the arrow-go module version the driver was built with
// ("v18.8.0"), from the binary's build info.
func arrowGoVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/apache/arrow-go/v18" {
				if dep.Replace != nil {
					return dep.Replace.Version
				}
				return dep.Version
			}
		}
	}
	return "v18"
}
