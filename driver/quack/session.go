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

// prepare runs a PREPARE_REQUEST and drains every FETCH_REQUEST chunk into a
// single PreparedResult. (Future enhancement: stream chunks lazily via a Cursor
// type, matching what we did in quack-jdbc.)
func (s *session) prepare(ctx context.Context, sql string) (*PreparedResult, error) {
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
	result := &PreparedResult{
		ColumnNames: pr.ResultNames,
		ColumnTypes: pr.ResultTypes,
		Chunks:      append([]message.DataChunk(nil), pr.Results...),
	}
	more := pr.NeedsMoreFetch
	uuid := pr.ResultUUID
	for more {
		freq := message.FetchRequest{
			Hdr: message.MessageHeader{
				Type:          message.MessageTypeFetchRequest,
				ConnectionID:  s.connectionID,
				ClientQueryID: s.nextQueryID(),
			},
			ResultUUID: uuid,
		}
		fresp, err := s.transport.Send(ctx, freq)
		if err != nil {
			return nil, fmt.Errorf("session: fetch: %w", err)
		}
		fr, ok := fresp.(message.FetchResponse)
		if !ok {
			return nil, fmt.Errorf("session: expected FETCH_RESPONSE, got %T", fresp)
		}
		result.Chunks = append(result.Chunks, fr.Results...)
		more = fr.HasBatchIndex
	}
	return result, nil
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

// PreparedResult is the eager-materialized result of one PREPARE_REQUEST.
type PreparedResult struct {
	ColumnNames []string
	ColumnTypes []quacktype.LogicalType
	Chunks      []message.DataChunk
}
