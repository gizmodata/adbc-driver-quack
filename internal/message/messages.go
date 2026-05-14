package message

import (
	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// MessageType is the Quack protocol message-type discriminator.
type MessageType int

const (
	MessageTypeInvalid            MessageType = 0
	MessageTypeConnectionRequest  MessageType = 1
	MessageTypeConnectionResponse MessageType = 2
	MessageTypePrepareRequest     MessageType = 3
	MessageTypePrepareResponse    MessageType = 4
	MessageTypeFetchRequest       MessageType = 7
	MessageTypeFetchResponse      MessageType = 8
	MessageTypeAppendRequest      MessageType = 9
	MessageTypeSuccessResponse    MessageType = 10
	MessageTypeDisconnectMessage  MessageType = 11
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

type ConnectionRequest struct {
	Hdr                      MessageHeader
	AuthString               string
	ClientDuckDBVersion      string
	ClientPlatform           string
	MinSupportedQuackVersion uint64
	MaxSupportedQuackVersion uint64
}

func (m ConnectionRequest) Header() MessageHeader { return m.Hdr }
func (ConnectionRequest) quackMessage()           {}

type ConnectionResponse struct {
	Hdr                 MessageHeader
	ServerDuckDBVersion string
	ServerPlatform      string
	QuackVersion        uint64
	HasQuackVersion     bool
}

func (m ConnectionResponse) Header() MessageHeader { return m.Hdr }
func (ConnectionResponse) quackMessage()           {}

type PrepareRequest struct {
	Hdr MessageHeader
	SQL string
}

func (m PrepareRequest) Header() MessageHeader { return m.Hdr }
func (PrepareRequest) quackMessage()           {}

type PrepareResponse struct {
	Hdr            MessageHeader
	ResultTypes    []quacktype.LogicalType
	ResultNames    []string
	NeedsMoreFetch bool
	Results        []DataChunk
	ResultUUID     codec.HugeIntParts
}

func (m PrepareResponse) Header() MessageHeader { return m.Hdr }
func (PrepareResponse) quackMessage()           {}

type FetchRequest struct {
	Hdr        MessageHeader
	ResultUUID codec.HugeIntParts
}

func (m FetchRequest) Header() MessageHeader { return m.Hdr }
func (FetchRequest) quackMessage()           {}

type FetchResponse struct {
	Hdr           MessageHeader
	Results       []DataChunk
	BatchIndex    uint64
	HasBatchIndex bool
}

func (m FetchResponse) Header() MessageHeader { return m.Hdr }
func (FetchResponse) quackMessage()           {}

type AppendRequest struct {
	Hdr         MessageHeader
	SchemaName  string
	TableName   string
	AppendChunk DataChunk
}

func (m AppendRequest) Header() MessageHeader { return m.Hdr }
func (AppendRequest) quackMessage()           {}

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

type ErrorResponse struct {
	Hdr     MessageHeader
	Message string
}

func (m ErrorResponse) Header() MessageHeader { return m.Hdr }
func (ErrorResponse) quackMessage()           {}
