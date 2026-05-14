package quack

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// session is the live connection to a Quack server. It owns the
// connection id, the HTTP transport, and a query-id counter.
type session struct {
	uri          transport.QuackURI
	transport    *transport.HTTPTransport
	queryIDSeq   atomic.Uint64
	connectionID string
	closed       atomic.Bool
}

func newSession(ctx context.Context, uri transport.QuackURI) (*session, error) {
	s := &session{
		uri:       uri,
		transport: transport.NewHTTPTransport(uri),
	}
	s.queryIDSeq.Store(1)
	if err := s.handshake(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *session) handshake(ctx context.Context) error {
	req := message.ConnectionRequest{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypeConnectionRequest,
			ClientQueryID: s.nextQueryID(),
		},
		AuthString:               s.uri.Token,
		ClientDuckDBVersion:      "adbc-driver-quack/0.0.0",
		ClientPlatform:           "go",
		MinSupportedQuackVersion: codec.QuackVersion,
		MaxSupportedQuackVersion: codec.QuackVersion,
	}
	resp, err := s.transport.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("session: handshake: %w", err)
	}
	cr, ok := resp.(message.ConnectionResponse)
	if !ok {
		return fmt.Errorf("session: expected CONNECTION_RESPONSE, got %T", resp)
	}
	if cr.Hdr.ConnectionID == "" {
		return fmt.Errorf("session: server did not return a connection id")
	}
	s.connectionID = cr.Hdr.ConnectionID
	return nil
}

// cursor opens a streaming cursor over a prepared query.
//
// Only the initial chunks delivered with PREPARE_RESPONSE are fetched
// eagerly; subsequent chunks come back lazily as the caller drains the
// cursor via nextChunk(). Peak driver memory for a million-row SELECT
// is therefore bounded by the server's batch size, not the total
// result-set row count.
func (s *session) cursor(ctx context.Context, sql string) (*cursor, error) {
	req := message.PrepareRequest{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypePrepareRequest,
			ConnectionID:  s.connectionID,
			ClientQueryID: s.nextQueryID(),
		},
		SQL: sql,
	}
	resp, err := s.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("session: prepare: %w", err)
	}
	pr, ok := resp.(message.PrepareResponse)
	if !ok {
		return nil, fmt.Errorf("session: expected PREPARE_RESPONSE, got %T", resp)
	}
	return &cursor{
		sess:        s,
		columnNames: pr.ResultNames,
		columnTypes: pr.ResultTypes,
		resultUUID:  pr.ResultUUID,
		buffered:    append([]message.DataChunk(nil), pr.Results...),
		needsMore:   pr.NeedsMoreFetch,
	}, nil
}

// cursor is a streaming view over the chunks of one prepared query.
type cursor struct {
	sess        *session
	columnNames []string
	columnTypes []quacktype.LogicalType
	resultUUID  codec.HugeIntParts
	buffered    []message.DataChunk
	needsMore   bool
	closed      bool
}

// columnNamesCopy / columnTypesCopy return the result schema. Callers
// should not mutate the returned slices.
func (c *cursor) columnNamesSlice() []string                { return c.columnNames }
func (c *cursor) columnTypesSlice() []quacktype.LogicalType { return c.columnTypes }

// peekFirstChunk returns the first buffered chunk (if any) without
// consuming it. Used by ExecuteUpdate / ExecuteQuery to detect the
// 1-row "Count" result-shape that DuckDB returns for DDL/DML.
func (c *cursor) peekFirstChunk() *message.DataChunk {
	if len(c.buffered) == 0 {
		return nil
	}
	return &c.buffered[0]
}

// nextChunk returns the next available chunk, fetching one server batch
// from the server when the local buffer is empty and more chunks are
// available. Returns (nil, nil) when the result set is exhausted.
func (c *cursor) nextChunk(ctx context.Context) (*message.DataChunk, error) {
	if c.closed {
		return nil, nil
	}
	if len(c.buffered) == 0 && c.needsMore {
		if err := c.fetchMore(ctx); err != nil {
			return nil, err
		}
	}
	if len(c.buffered) == 0 {
		return nil, nil
	}
	chunk := c.buffered[0]
	c.buffered = c.buffered[1:]
	return &chunk, nil
}

// drainAll consumes every remaining chunk and returns them in order.
// Used by ExecuteUpdate, metadata queries, and any other call site
// whose result is small enough to materialize.
func (c *cursor) drainAll(ctx context.Context) ([]message.DataChunk, error) {
	out := append([]message.DataChunk(nil), c.buffered...)
	c.buffered = nil
	for c.needsMore {
		if err := c.fetchMore(ctx); err != nil {
			return nil, err
		}
		out = append(out, c.buffered...)
		c.buffered = nil
	}
	return out, nil
}

func (c *cursor) fetchMore(ctx context.Context) error {
	freq := message.FetchRequest{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypeFetchRequest,
			ConnectionID:  c.sess.connectionID,
			ClientQueryID: c.sess.nextQueryID(),
		},
		ResultUUID: c.resultUUID,
	}
	resp, err := c.sess.transport.Send(ctx, freq)
	if err != nil {
		return fmt.Errorf("cursor: fetch: %w", err)
	}
	fr, ok := resp.(message.FetchResponse)
	if !ok {
		return fmt.Errorf("cursor: expected FETCH_RESPONSE, got %T", resp)
	}
	c.buffered = append(c.buffered, fr.Results...)
	c.needsMore = fr.HasBatchIndex
	return nil
}

func (c *cursor) close() {
	c.closed = true
	c.buffered = nil
	// Quack has no explicit "release result" message today; the server
	// releases on DISCONNECT or after the batch_index sentinel. Closing
	// the cursor locally is sufficient.
}

// drainPrepared is a convenience wrapper that runs a SQL statement and
// returns every chunk eagerly. Used by metadata queries and for the
// side-effect-only BEGIN/COMMIT/ROLLBACK SQL that has no real result.
type drainedResult struct {
	ColumnNames []string
	ColumnTypes []quacktype.LogicalType
	Chunks      []message.DataChunk
}

func (s *session) drainPrepared(ctx context.Context, sql string) (*drainedResult, error) {
	cur, err := s.cursor(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer cur.close()
	chunks, err := cur.drainAll(ctx)
	if err != nil {
		return nil, err
	}
	return &drainedResult{
		ColumnNames: cur.columnNamesSlice(),
		ColumnTypes: cur.columnTypesSlice(),
		Chunks:      chunks,
	}, nil
}

// appendChunk POSTs an APPEND_REQUEST for bulk-load.
func (s *session) appendChunk(ctx context.Context, schema, table string, chunk message.DataChunk) error {
	req := message.AppendRequest{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypeAppendRequest,
			ConnectionID:  s.connectionID,
			ClientQueryID: s.nextQueryID(),
		},
		SchemaName:  schema,
		TableName:   table,
		AppendChunk: chunk,
	}
	resp, err := s.transport.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("session: append: %w", err)
	}
	if _, ok := resp.(message.SuccessResponse); !ok {
		return fmt.Errorf("session: expected SUCCESS_RESPONSE for APPEND, got %T", resp)
	}
	return nil
}

func (s *session) close(ctx context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.connectionID == "" {
		return nil
	}
	req := message.DisconnectMessage{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypeDisconnectMessage,
			ConnectionID:  s.connectionID,
			ClientQueryID: s.nextQueryID(),
		},
	}
	_, _ = s.transport.Send(ctx, req)
	return nil
}

func (s *session) nextQueryID() uint64 {
	return s.queryIDSeq.Add(1) - 1
}
