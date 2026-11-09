package quack

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/message"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// session is the live connection to a Quack server (protocol version 3,
// DuckDB 2.0). It owns the server-issued connection id, the HTTP
// transport, a client query-id counter, and the heartbeat that keeps the
// connection's lease alive while the ADBC connection is idle.
type session struct {
	uri          transport.QuackURI
	transport    *transport.HTTPTransport
	queryIDSeq   atomic.Uint64
	connectionID string
	closed       atomic.Bool

	serverVersion  string
	serverPlatform string
	// lease is the heartbeat timeout the server accepted.
	lease time.Duration

	heartbeatStop chan struct{}
	heartbeatDone chan struct{}
	// dead is set once the server says this connection is gone (lease
	// expired, unknown connection id, invalidated database).
	dead atomic.Pointer[error]

	// activeQuery is the uuid of the statement currently running, for
	// Cancel. Zero when none.
	activeMu    sync.Mutex
	activeQuery codec.HugeIntParts
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
	s.startHeartbeat()
	return s, nil
}

func (s *session) header(mt message.MessageType) message.MessageHeader {
	return message.MessageHeader{Type: mt, ConnectionID: s.connectionID, ClientQueryID: s.nextQueryID()}
}

func (s *session) handshake(ctx context.Context) error {
	leaseSeconds := uint64(s.uri.HeartbeatTimeout / time.Second)
	if leaseSeconds == 0 {
		leaseSeconds = 1
	}
	req := message.ConnectionRequest{
		Hdr: message.MessageHeader{
			Type:          message.MessageTypeConnectionRequest,
			ClientQueryID: s.nextQueryID(),
		},
		AuthString:               s.uri.Token,
		ClientDuckDBVersion:      "adbc-driver-quack/" + Version,
		ClientPlatform:           "go",
		MinSupportedQuackVersion: codec.QuackVersion,
		MaxSupportedQuackVersion: codec.QuackVersion,
		ClientID:                 s.uri.ClientID,
		HeartbeatTimeoutSeconds:  leaseSeconds,
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
	if cr.QuackVersion != 0 && cr.QuackVersion != codec.QuackVersion {
		return fmt.Errorf("session: server speaks Quack protocol version %d; this driver speaks version %d (DuckDB 2.0)",
			cr.QuackVersion, codec.QuackVersion)
	}
	s.connectionID = cr.Hdr.ConnectionID
	s.serverVersion = cr.ServerDuckDBVersion
	s.serverPlatform = cr.ServerPlatform
	s.lease = time.Duration(cr.HeartbeatTimeoutSeconds) * time.Second
	if s.lease <= 0 {
		s.lease = time.Duration(leaseSeconds) * time.Second
	}
	return nil
}

// startHeartbeat renews the lease about every third of it, with jitter, as
// DuckDB's own client does. Any request renews the lease too, so this only
// matters while the connection is idle, but it is cheap to always send.
func (s *session) startHeartbeat() {
	s.heartbeatStop = make(chan struct{})
	s.heartbeatDone = make(chan struct{})
	go func() {
		defer close(s.heartbeatDone)
		base := s.lease / 3
		if base < 200*time.Millisecond {
			base = 200 * time.Millisecond
		}
		for {
			jitter := time.Duration(mathrand.Int64N(int64(base/5) + 1))
			timer := time.NewTimer(base - base/10 + jitter)
			select {
			case <-s.heartbeatStop:
				timer.Stop()
				return
			case <-timer.C:
			}
			ctx, cancel := context.WithTimeout(context.Background(), s.lease)
			_, err := s.transport.Send(ctx, message.HeartbeatRequest{Hdr: s.header(message.MessageTypeHeartbeatRequest)})
			cancel()
			var se *transport.ErrServerError
			if errors.As(err, &se) {
				// The server answered and refused: the connection is gone
				// (lease expired, unknown id). Transport failures are
				// transient: the server only expires the lease after the
				// whole timeout without valid traffic.
				dead := fmt.Errorf("session: the Quack server closed this connection: %w", err)
				s.dead.Store(&dead)
				return
			}
		}
	}()
}

func (s *session) stopHeartbeat() {
	if s.heartbeatStop == nil {
		return
	}
	select {
	case <-s.heartbeatStop:
	default:
		close(s.heartbeatStop)
	}
	<-s.heartbeatDone
}

// send posts a request, failing fast when the server already said this
// connection is gone, and remembering when a response says so.
func (s *session) send(ctx context.Context, m message.QuackMessage) (message.QuackMessage, error) {
	if s.closed.Load() {
		return nil, errors.New("session: connection is closed")
	}
	if dead := s.dead.Load(); dead != nil {
		return nil, *dead
	}
	resp, err := s.transport.Send(ctx, m)
	var se *transport.ErrServerError
	if errors.As(err, &se) && se.MustInvalidate {
		dead := fmt.Errorf("session: the Quack server's database is invalidated: %w", err)
		s.dead.Store(&dead)
	}
	if err != nil && ctx.Err() != nil {
		// The caller gave up (Statement cancel, a deadline): dropping the
		// HTTP request does not stop the statement on the server, so
		// cancel it there too, off the caller's time budget.
		var query codec.HugeIntParts
		switch req := m.(type) {
		case message.PrepareRequest:
			query = req.QueryUUID
		case message.FetchRequest:
			query = req.QueryUUID
		case message.SendDataRequest:
			s.activeMu.Lock()
			query = s.activeQuery
			s.activeMu.Unlock()
		}
		if query != (codec.HugeIntParts{}) {
			go func() {
				cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _ = s.transport.Send(cctx, message.CancelRequest{Hdr: s.header(message.MessageTypeCancelRequest), QueryUUID: query})
			}()
		}
		err = fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return resp, err
}

// newQueryUUID returns a random non-zero 128-bit query id. The server
// treats {0,0} as internal traffic, so a client query never uses it.
func newQueryUUID() codec.HugeIntParts {
	var b [16]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(fmt.Sprintf("crypto/rand: %v", err))
		}
		u := codec.HugeIntParts{
			Upper: int64(binary.LittleEndian.Uint64(b[8:])),
			Lower: binary.LittleEndian.Uint64(b[:8]),
		}
		if u != (codec.HugeIntParts{}) {
			return u
		}
	}
}

func (s *session) setActive(u codec.HugeIntParts) {
	s.activeMu.Lock()
	s.activeQuery = u
	s.activeMu.Unlock()
}

func (s *session) clearActive(u codec.HugeIntParts) {
	s.activeMu.Lock()
	if s.activeQuery == u {
		s.activeQuery = codec.HugeIntParts{}
	}
	s.activeMu.Unlock()
}

// cancelActive asks the server to interrupt the running statement, if any.
func (s *session) cancelActive(ctx context.Context) error {
	s.activeMu.Lock()
	u := s.activeQuery
	s.activeMu.Unlock()
	if u == (codec.HugeIntParts{}) {
		return nil
	}
	return s.cancelQuery(ctx, u)
}

func (s *session) cancelQuery(ctx context.Context, u codec.HugeIntParts) error {
	_, err := s.send(ctx, message.CancelRequest{Hdr: s.header(message.MessageTypeCancelRequest), QueryUUID: u})
	return err
}

// prepare sends PREPARE_REQUEST for sql and opens a cursor over its result.
// inlineRows caps the rows PREPARE may answer with (unset = server default).
func (s *session) prepare(ctx context.Context, sql string, inlineRows message.OptionalIndex) (*cursor, error) {
	uuid := newQueryUUID()
	s.setActive(uuid)
	req := message.PrepareRequest{
		Hdr:        s.header(message.MessageTypePrepareRequest),
		SQL:        sql,
		QueryUUID:  uuid,
		InlineRows: inlineRows,
	}
	resp, err := s.send(ctx, req)
	if err != nil {
		s.clearActive(uuid)
		return nil, fmt.Errorf("session: prepare: %w", err)
	}
	pr, ok := resp.(message.PrepareResponse)
	if !ok {
		s.clearActive(uuid)
		return nil, fmt.Errorf("session: expected PREPARE_RESPONSE, got %T", resp)
	}
	c := &cursor{
		sess:        s,
		columnNames: pr.ResultNames,
		columnTypes: pr.ResultTypes,
		queryUUID:   pr.QueryUUID,
		buffered:    append([]message.DataChunk(nil), pr.Results...),
		needsMore:   pr.NeedsMoreFetch,
		nextBatch:   1,
	}
	if !c.needsMore {
		s.clearActive(uuid)
	}
	return c, nil
}

// cursor opens a streaming cursor over a query.
//
// Only the chunks delivered inline with PREPARE_RESPONSE are buffered
// eagerly; later batches come back one FETCH at a time as the caller
// drains the cursor via nextChunk(), so peak driver memory is bounded by
// the server's batch size, not the result size.
func (s *session) cursor(ctx context.Context, sql string) (*cursor, error) {
	return s.prepare(ctx, sql, message.OptionalIndex{})
}

// cursor is a streaming view over the batches of one query.
type cursor struct {
	sess        *session
	columnNames []string
	columnTypes []quacktype.LogicalType
	queryUUID   codec.HugeIntParts
	buffered    []message.DataChunk
	needsMore   bool
	// nextBatch is the dense FETCH batch index to ask for next (from 1,
	// counting after the batches PREPARE delivered inline).
	nextBatch uint64
	closed    bool
}

// columnNamesSlice / columnTypesSlice return the result schema. Callers
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
// when the local buffer is empty and more batches are available. Returns
// (nil, nil) when the result set is exhausted.
func (c *cursor) nextChunk(ctx context.Context) (*message.DataChunk, error) {
	if c.closed {
		return nil, nil
	}
	for len(c.buffered) == 0 && c.needsMore {
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

// fetchMore asks for batch nextBatch. A batch response carries its index
// and chunks; the terminal response carries no chunks and, when the
// server knows it, the total batch count, which must match what arrived
// (a short stream is an error, never a silently truncated result).
func (c *cursor) fetchMore(ctx context.Context) error {
	freq := message.FetchRequest{
		Hdr:        c.sess.header(message.MessageTypeFetchRequest),
		QueryUUID:  c.queryUUID,
		BatchIndex: c.nextBatch,
		AckIndex:   c.nextBatch - 1,
	}
	resp, err := c.sess.send(ctx, freq)
	if err != nil {
		c.needsMore = false
		c.sess.clearActive(c.queryUUID)
		return fmt.Errorf("cursor: fetch: %w", err)
	}
	fr, ok := resp.(message.FetchResponse)
	if !ok {
		c.needsMore = false
		c.sess.clearActive(c.queryUUID)
		return fmt.Errorf("cursor: expected FETCH_RESPONSE, got %T", resp)
	}
	if fr.BatchIndex.Valid {
		if fr.BatchIndex.Value != c.nextBatch {
			c.needsMore = false
			return fmt.Errorf("cursor: asked for batch %d, server sent batch %d", c.nextBatch, fr.BatchIndex.Value)
		}
		c.buffered = append(c.buffered, fr.Results...)
		c.nextBatch++
		return nil
	}
	// terminal response
	c.needsMore = false
	c.sess.clearActive(c.queryUUID)
	if fr.TotalBatches.Valid && fr.TotalBatches.Value != c.nextBatch-1 {
		return fmt.Errorf("cursor: result truncated: server announced %d batches, received %d",
			fr.TotalBatches.Value, c.nextBatch-1)
	}
	return nil
}

func (c *cursor) close() {
	if c.closed {
		return
	}
	c.closed = true
	c.buffered = nil
	if c.needsMore {
		// The server releases an abandoned result when the next query
		// supersedes it, or on disconnect. Nothing to send here.
		c.sess.clearActive(c.queryUUID)
	}
}

// drainedResult is a fully materialized result. Used by metadata queries
// and for the side-effect-only BEGIN/COMMIT/ROLLBACK SQL.
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

// sendData posts one SEND_DATA batch (or, with no chunks and total set,
// the terminal message) for a client data stream.
func (s *session) sendData(ctx context.Context, streamID string, batchIndex message.OptionalIndex, total message.OptionalIndex, chunks []message.DataChunk) error {
	req := message.SendDataRequest{
		Hdr:          s.header(message.MessageTypeSendDataRequest),
		StreamID:     streamID,
		BatchIndex:   batchIndex,
		TotalBatches: total,
		Chunks:       chunks,
	}
	resp, err := s.send(ctx, req)
	if err != nil {
		return fmt.Errorf("session: send data: %w", err)
	}
	if _, ok := resp.(message.SendDataResponse); !ok {
		return fmt.Errorf("session: expected SEND_DATA_RESPONSE, got %T", resp)
	}
	return nil
}

func (s *session) close(ctx context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.stopHeartbeat()
	if s.connectionID == "" {
		return nil
	}
	// Best effort, and bounded: a gone server must not hang Close.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = s.transport.Send(ctx, message.DisconnectMessage{Hdr: s.header(message.MessageTypeDisconnectMessage)})
	return nil
}

func (s *session) nextQueryID() uint64 {
	return s.queryIDSeq.Add(1) - 1
}
