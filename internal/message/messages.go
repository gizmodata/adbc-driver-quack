package message

import (
	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// MessageType is the Quack protocol message-type discriminator.
//
// The values match duckdb-quack's MessageType enum for QUACK_VERSION 3
// (DuckDB 2.0). Gaps (5, 6, 13) are retired message types.
type MessageType int

const (
	MessageTypeInvalid            MessageType = 0
	MessageTypeConnectionRequest  MessageType = 1
	MessageTypeConnectionResponse MessageType = 2
	MessageTypePrepareRequest     MessageType = 3
	MessageTypePrepareResponse    MessageType = 4
	MessageTypeFetchRequest       MessageType = 7
	MessageTypeFetchResponse      MessageType = 8
	MessageTypeSendDataRequest    MessageType = 9
	MessageTypeSuccessResponse    MessageType = 10
	MessageTypeDisconnectMessage  MessageType = 11
	MessageTypeCancelRequest      MessageType = 12
	MessageTypeSendDataResponse   MessageType = 14
	MessageTypeAcknowledgement    MessageType = 15
	MessageTypeHeartbeatRequest   MessageType = 16
	MessageTypeErrorResponse      MessageType = 100
)

// MessageHeader is the common preamble for every Quack message.
type MessageHeader struct {
	Type          MessageType
	ConnectionID  string // empty = absent
	ClientQueryID uint64 // OptionalIndexInvalid = absent
}

// HasClientQueryID reports whether the header carries a real client query id.
func (h MessageHeader) HasClientQueryID() bool {
	return h.ClientQueryID != codec.OptionalIndexInvalid
}

// QuackMessage is the sealed type for every Quack message.
type QuackMessage interface {
	Header() MessageHeader
	quackMessage()
}

// OptionalIndex is DuckDB's optional_idx: a uint64 that may be unset.
type OptionalIndex struct {
	Value uint64
	Valid bool
}

// SomeIndex returns a set OptionalIndex.
func SomeIndex(v uint64) OptionalIndex { return OptionalIndex{Value: v, Valid: true} }

type ConnectionRequest struct {
	Hdr                      MessageHeader
	AuthString               string
	ClientDuckDBVersion      string
	ClientPlatform           string
	MinSupportedQuackVersion uint64
	MaxSupportedQuackVersion uint64
	// ClientID is an opaque client identifier; empty, or at least 4 characters.
	ClientID string
	// HeartbeatTimeoutSeconds is the lease the client asks for. The server
	// rejects 0, so a request must always carry a positive value.
	HeartbeatTimeoutSeconds uint64
}

func (m ConnectionRequest) Header() MessageHeader { return m.Hdr }
func (ConnectionRequest) quackMessage()           {}

type ConnectionResponse struct {
	Hdr                 MessageHeader
	ServerDuckDBVersion string
	ServerPlatform      string
	QuackVersion        uint64
	// HeartbeatTimeoutSeconds is the lease the server accepted.
	HeartbeatTimeoutSeconds uint64
}

func (m ConnectionResponse) Header() MessageHeader { return m.Hdr }
func (ConnectionResponse) quackMessage()           {}

type PrepareRequest struct {
	Hdr       MessageHeader
	SQL       string
	QueryUUID codec.HugeIntParts
	// InlineRows caps the rows the PREPARE_RESPONSE may carry. Unset uses
	// the server's quack_prepare_inline_rows. Zero makes PREPARE answer
	// before the statement produces a row, which a statement that waits for
	// client data (an ingest) needs.
	InlineRows OptionalIndex
}

func (m PrepareRequest) Header() MessageHeader { return m.Hdr }
func (PrepareRequest) quackMessage()           {}

type PrepareResponse struct {
	Hdr            MessageHeader
	ResultTypes    []quacktype.LogicalType
	ResultNames    []string
	NeedsMoreFetch bool
	Results        []DataChunk
	QueryUUID      codec.HugeIntParts
}

func (m PrepareResponse) Header() MessageHeader { return m.Hdr }
func (PrepareResponse) quackMessage()           {}

// FetchRequest asks for one dense batch of a result. BatchIndex counts
// from 1 after the batches PREPARE delivered inline; a transport retry
// names the same index and gets the same batch. AckIndex tells the server
// the client has every batch up to and including it.
type FetchRequest struct {
	Hdr        MessageHeader
	QueryUUID  codec.HugeIntParts
	BatchIndex uint64
	AckIndex   uint64
}

func (m FetchRequest) Header() MessageHeader { return m.Hdr }
func (FetchRequest) quackMessage()           {}

// FetchResponse carries one batch (BatchIndex set, chunks in the blob that
// follows the message), or is the terminal response (no chunks,
// TotalBatches set when the server knows the total).
type FetchResponse struct {
	Hdr          MessageHeader
	ChunkCount   uint64
	TotalBatches OptionalIndex
	BatchIndex   OptionalIndex
	Results      []DataChunk
}

func (m FetchResponse) Header() MessageHeader { return m.Hdr }
func (FetchResponse) quackMessage()           {}

// SendDataRequest streams one dense batch of client rows into a
// scan_data_from_quack_client(stream_id, ...) the current statement reads.
// The terminal message has no chunks and carries TotalBatches.
type SendDataRequest struct {
	Hdr          MessageHeader
	StreamID     string
	TotalBatches OptionalIndex
	BatchIndex   OptionalIndex
	Chunks       []DataChunk
}

func (m SendDataRequest) Header() MessageHeader { return m.Hdr }
func (SendDataRequest) quackMessage()           {}

type SendDataResponse struct {
	Hdr          MessageHeader
	AcceptBudget OptionalIndex
}

func (m SendDataResponse) Header() MessageHeader { return m.Hdr }
func (SendDataResponse) quackMessage()           {}

type SuccessResponse struct {
	Hdr MessageHeader
}

func (m SuccessResponse) Header() MessageHeader { return m.Hdr }
func (SuccessResponse) quackMessage()           {}

type DisconnectMessage struct {
	Hdr MessageHeader
}

func (m DisconnectMessage) Header() MessageHeader { return m.Hdr }
func (DisconnectMessage) quackMessage()           {}

// HeartbeatRequest renews the connection's lease.
type HeartbeatRequest struct {
	Hdr MessageHeader
}

func (m HeartbeatRequest) Header() MessageHeader { return m.Hdr }
func (HeartbeatRequest) quackMessage()           {}

// CancelRequest interrupts the query with QueryUUID ({0,0} = whatever runs).
type CancelRequest struct {
	Hdr       MessageHeader
	QueryUUID codec.HugeIntParts
}

func (m CancelRequest) Header() MessageHeader { return m.Hdr }
func (CancelRequest) quackMessage()           {}

// Acknowledgement tells a server with reconnects enabled that the client
// has the whole result of QueryUUID, so it can drop its replay cache.
type Acknowledgement struct {
	Hdr       MessageHeader
	QueryUUID codec.HugeIntParts
}

func (m Acknowledgement) Header() MessageHeader { return m.Hdr }
func (Acknowledgement) quackMessage()           {}

type ErrorResponse struct {
	Hdr     MessageHeader
	Message string
	// ExceptionType is DuckDB's exception type name ("Catalog", "Parser", ...).
	ExceptionType string
	ExtraInfo     map[string]string
	// MustInvalidate is set when the server's own database is invalidated:
	// the connection is a dead end.
	MustInvalidate bool
}

func (m ErrorResponse) Header() MessageHeader { return m.Hdr }
func (ErrorResponse) quackMessage()           {}
