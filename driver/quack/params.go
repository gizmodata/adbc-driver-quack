package quack

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// Client-side parameter binding.
//
// The Quack wire protocol has no parameter markers, but ADBC consumers —
// notably DuckDB's adbc_scanner extension, which pushes filters down as
// `WHERE "col" > ?` with bound values — expect Bind/BindStream to work.
// Bound values are rendered as DuckDB SQL literals and substituted for
// each top-level `?` (outside quotes and comments) before the statement
// is sent, once per bound row.

// boundRows materializes the bound record / stream as rows of Arrow
// scalars (nil for NULL) and releases the binding.
func (s *statementImpl) boundRows() ([][]any, *arrow.Schema, error) {
	defer s.clearBound()
	var schema *arrow.Schema
	var out [][]any
	add := func(rec arrow.Record) error {
		if schema == nil {
			schema = rec.Schema()
		}
		for r := 0; r < int(rec.NumRows()); r++ {
			row := make([]any, rec.NumCols())
			for c := 0; c < int(rec.NumCols()); c++ {
				v, err := scalarAt(rec.Column(c), r)
				if err != nil {
					return fmt.Errorf("column %q: %w", rec.Schema().Field(c).Name, err)
				}
				row[c] = v
			}
			out = append(out, row)
		}
		return nil
	}
	if s.bound != nil {
		if err := add(s.bound); err != nil {
			return nil, nil, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
		}
	}
	if s.boundStream != nil {
		for s.boundStream.Next() {
			if err := add(s.boundStream.Record()); err != nil {
				return nil, nil, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
			}
		}
		if err := s.boundStream.Err(); err != nil {
			return nil, nil, errStatus(adbc.StatusIO, "bind stream: %v", err)
		}
	}
	return out, schema, nil
}

// scalarAt extracts one value as a SQL-renderable Go value.
func scalarAt(col arrow.Array, i int) (any, error) {
	if col.IsNull(i) {
		return nil, nil
	}
	switch a := col.(type) {
	case *array.Boolean:
		return a.Value(i), nil
	case *array.Int8:
		return int64(a.Value(i)), nil
	case *array.Int16:
		return int64(a.Value(i)), nil
	case *array.Int32:
		return int64(a.Value(i)), nil
	case *array.Int64:
		return a.Value(i), nil
	case *array.Uint8:
		return uint64(a.Value(i)), nil
	case *array.Uint16:
		return uint64(a.Value(i)), nil
	case *array.Uint32:
		return uint64(a.Value(i)), nil
	case *array.Uint64:
		return a.Value(i), nil
	case *array.Float32:
		return float64(a.Value(i)), nil
	case *array.Float64:
		return a.Value(i), nil
	// String / binary values are zero-copy views into the record's
	// buffers, which are released once the rows are extracted — clone.
	case *array.String:
		return strings.Clone(a.Value(i)), nil
	case *array.LargeString:
		return strings.Clone(a.Value(i)), nil
	case *array.StringView:
		return strings.Clone(a.Value(i)), nil
	case *array.Binary:
		return bytes.Clone(a.Value(i)), nil
	case *array.LargeBinary:
		return bytes.Clone(a.Value(i)), nil
	case *array.FixedSizeBinary:
		return bytes.Clone(a.Value(i)), nil
	case *array.Decimal128:
		return decimalLiteral(a.Value(i).BigInt(), a.DataType().(*arrow.Decimal128Type).Scale), nil
	case *array.Decimal256:
		return decimalLiteral(a.Value(i).BigInt(), a.DataType().(*arrow.Decimal256Type).Scale), nil
	case *array.Date32:
		return sqlKeyword("DATE '" + a.Value(i).ToTime().Format("2006-01-02") + "'"), nil
	case *array.Date64:
		return sqlKeyword("DATE '" + a.Value(i).ToTime().Format("2006-01-02") + "'"), nil
	case *array.Time32:
		return sqlKeyword("TIME '" + a.Value(i).ToTime(a.DataType().(*arrow.Time32Type).Unit).Format("15:04:05.999999999") + "'"), nil
	case *array.Time64:
		return sqlKeyword("TIME '" + a.Value(i).ToTime(a.DataType().(*arrow.Time64Type).Unit).Format("15:04:05.999999999") + "'"), nil
	case *array.Timestamp:
		dt := a.DataType().(*arrow.TimestampType)
		t := a.Value(i).ToTime(dt.Unit)
		if dt.TimeZone != "" {
			if loc, err := dt.GetZone(); err == nil {
				t = t.In(loc)
			}
			return sqlKeyword("TIMESTAMPTZ '" + t.Format("2006-01-02 15:04:05.999999999-07:00") + "'"), nil
		}
		return sqlKeyword("TIMESTAMP '" + t.UTC().Format("2006-01-02 15:04:05.999999999") + "'"), nil
	case *array.Dictionary:
		return scalarAt(a.Dictionary(), a.GetValueIndex(i))
	case *array.Null:
		return nil, nil
	}
	return nil, fmt.Errorf("cannot bind Arrow type %s", col.DataType())
}

// sqlKeyword is a pre-rendered SQL fragment (typed literal).
type sqlKeyword string

func decimalLiteral(n *big.Int, scale int32) sqlKeyword {
	s := n.String()
	if scale <= 0 {
		return sqlKeyword(s)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for len(s) <= int(scale) {
		s = "0" + s
	}
	i := len(s) - int(scale)
	out := s[:i] + "." + s[i:]
	if neg {
		out = "-" + out
	}
	return sqlKeyword(out)
}

// renderLiteral converts a scalar to a DuckDB SQL literal.
func renderLiteral(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case int64:
		return fmt.Sprint(x)
	case uint64:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprintf("%v::DOUBLE", x)
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	case []byte:
		var b strings.Builder
		b.WriteString("'")
		for _, c := range x {
			fmt.Fprintf(&b, "\\x%02X", c)
		}
		b.WriteString("'::BLOB")
		return b.String()
	case sqlKeyword:
		return string(x)
	case time.Time:
		return "TIMESTAMP '" + x.UTC().Format("2006-01-02 15:04:05.999999999") + "'"
	}
	return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", "''") + "'"
}

// substituteParams replaces each top-level `?` in sql with the
// corresponding literal from values. Question marks inside string
// literals, quoted identifiers, and comments are left alone.
func substituteParams(sql string, values []any) (string, error) {
	var b strings.Builder
	b.Grow(len(sql) + 16*len(values))
	idx := 0
	inStr, inIdent, lineComment, blockComment := false, false, false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case lineComment:
			b.WriteByte(c)
			if c == '\n' {
				lineComment = false
			}
		case blockComment:
			b.WriteByte(c)
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				b.WriteByte('/')
				i++
				blockComment = false
			}
		case inStr:
			b.WriteByte(c)
			if c == '\'' {
				inStr = false
			}
		case inIdent:
			b.WriteByte(c)
			if c == '"' {
				inIdent = false
			}
		case c == '\'':
			inStr = true
			b.WriteByte(c)
		case c == '"':
			inIdent = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			lineComment = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			blockComment = true
			b.WriteByte(c)
		case c == '?':
			if idx >= len(values) {
				return "", fmt.Errorf("statement has more parameter markers than bound values (%d)", len(values))
			}
			b.WriteString(renderLiteral(values[idx]))
			idx++
		default:
			b.WriteByte(c)
		}
	}
	if idx != len(values) {
		return "", fmt.Errorf("statement has %d parameter markers but %d values were bound", idx, len(values))
	}
	return b.String(), nil
}

// executeBoundQuery runs the statement once per bound row and returns
// the concatenated result (single-row bindings stream as usual).
func (s *statementImpl) executeBoundQuery(ctx context.Context) (array.RecordReader, int64, error) {
	rows, _, err := s.boundRows()
	if err != nil {
		return nil, -1, err
	}
	if len(rows) == 0 {
		return nil, -1, errStatus(adbc.StatusInvalidArgument, "bind: no parameter rows bound")
	}
	if len(rows) == 1 {
		sql, err := substituteParams(s.sql, rows[0])
		if err != nil {
			return nil, -1, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
		}
		if os.Getenv("ADBC_QUACK_DEBUG_SQL") != "" {
			fmt.Fprintf(os.Stderr, "[quack] bound sql: %s\n", sql)
		}
		cur, err := s.conn.sess.cursor(ctx, sql)
		if err != nil {
			return nil, -1, fromTransportError(err)
		}
		reader, err := newStreamingRecordReader(ctx, s.alloc, cur)
		if err != nil {
			cur.close()
			return nil, -1, errStatus(adbc.StatusInternal, "newStreamingRecordReader: %v", err)
		}
		return reader, -1, nil
	}
	var recs []arrow.Record
	var schema *arrow.Schema
	release := func() {
		for _, r := range recs {
			r.Release()
		}
	}
	for _, row := range rows {
		sql, err := substituteParams(s.sql, row)
		if err != nil {
			release()
			return nil, -1, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
		}
		cur, err := s.conn.sess.cursor(ctx, sql)
		if err != nil {
			release()
			return nil, -1, fromTransportError(err)
		}
		rr, err := newStreamingRecordReader(ctx, s.alloc, cur)
		if err != nil {
			cur.close()
			release()
			return nil, -1, errStatus(adbc.StatusInternal, "newStreamingRecordReader: %v", err)
		}
		if schema == nil {
			schema = rr.Schema()
		}
		for rr.Next() {
			rec := rr.RecordBatch()
			rec.Retain()
			recs = append(recs, rec)
		}
		err = rr.Err()
		rr.Release()
		if err != nil {
			release()
			return nil, -1, err
		}
	}
	out, err := array.NewRecordReader(schema, recs)
	release()
	if err != nil {
		return nil, -1, errStatus(adbc.StatusInternal, "bind: %v", err)
	}
	return out, -1, nil
}

// executeBoundUpdate runs the statement once per bound row.
func (s *statementImpl) executeBoundUpdate(ctx context.Context) (int64, error) {
	rows, _, err := s.boundRows()
	if err != nil {
		return -1, err
	}
	var total int64
	for _, row := range rows {
		sql, err := substituteParams(s.sql, row)
		if err != nil {
			return -1, errStatus(adbc.StatusInvalidArgument, "bind: %v", err)
		}
		result, err := s.conn.sess.drainPrepared(ctx, sql)
		if err != nil {
			return -1, fromTransportError(err)
		}
		for _, c := range result.Chunks {
			total += int64(c.RowCount)
		}
	}
	return total, nil
}
