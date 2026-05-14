package quack

import (
	"context"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// streamingRecordReader pulls one DataChunk at a time from a cursor and
// converts it to an arrow.Record on demand. Implements
// array.RecordReader, satisfying the ADBC Connection.ExecuteQuery
// contract without materializing the entire result up front.
type streamingRecordReader struct {
	ctx      context.Context
	cursor   *cursor
	schema   *arrow.Schema
	alloc    memory.Allocator
	colNames []string
	current  arrow.Record
	err      error
	refs     atomic.Int64
	done     bool
}

func newStreamingRecordReader(ctx context.Context, alloc memory.Allocator, cur *cursor) (array.RecordReader, error) {
	if alloc == nil {
		alloc = memory.NewGoAllocator()
	}
	rr := &streamingRecordReader{
		ctx:      ctx,
		cursor:   cur,
		schema:   SchemaFromColumns(cur.columnNamesSlice(), cur.columnTypesSlice()),
		alloc:    alloc,
		colNames: cur.columnNamesSlice(),
	}
	rr.refs.Store(1)
	return rr, nil
}

func (r *streamingRecordReader) Retain() {
	r.refs.Add(1)
}

func (r *streamingRecordReader) Release() {
	if r.refs.Add(-1) != 0 {
		return
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	r.cursor.close()
}

func (r *streamingRecordReader) Schema() *arrow.Schema {
	return r.schema
}

func (r *streamingRecordReader) RecordBatch() arrow.RecordBatch {
	return r.current
}

// Record is the deprecated alias for RecordBatch — kept because the
// adbc framework still calls it on some Go versions.
func (r *streamingRecordReader) Record() arrow.RecordBatch {
	return r.current
}

func (r *streamingRecordReader) Err() error {
	return r.err
}

func (r *streamingRecordReader) Next() bool {
	if r.err != nil || r.done {
		return false
	}
	if r.current != nil {
		r.current.Release()
		r.current = nil
	}
	// Skip empty chunks — they're a no-op from the consumer's perspective
	// and a typical artifact of DDL/DML responses.
	for {
		chunk, err := r.cursor.nextChunk(r.ctx)
		if err != nil {
			r.err = err
			return false
		}
		if chunk == nil {
			r.done = true
			return false
		}
		if chunk.RowCount == 0 {
			continue
		}
		rec, err := RecordFromChunk(r.alloc, r.colNames, *chunk)
		if err != nil {
			r.err = err
			return false
		}
		r.current = rec
		return true
	}
}
