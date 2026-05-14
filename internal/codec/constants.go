// Package codec implements the binary serialization primitives used by
// DuckDB's Quack wire protocol (BinaryReader/BinaryWriter, the matching
// integer encodings, and the wire-format constants shared with the
// server).
//
// This package is a clean-room Go port of the matching codec/ package
// in the sibling JDBC driver at https://github.com/gizmodata/quack-jdbc.
package codec

// Wire-format constants for the Quack protocol.
const (
	// QuackVersion is the Quack protocol version this driver targets.
	QuackVersion uint64 = 1

	// DefaultQuackPort is the default TCP port used by Quack HTTP transport.
	DefaultQuackPort = 9494

	// QuackEndpoint is the HTTP endpoint exposed by a Quack server.
	QuackEndpoint = "/quack"

	// DuckDBMIMEType is the MIME type used for Quack request/response bodies.
	DuckDBMIMEType = "application/duckdb"

	// FieldEnd marks the end of a BinarySerializer object.
	FieldEnd uint16 = 0xFFFF

	// OptionalIndexInvalid is the sentinel ULEB128 value indicating an
	// "absent" optional 64-bit index. It is the maximum uint64.
	OptionalIndexInvalid uint64 = 0xFFFFFFFFFFFFFFFF
)
