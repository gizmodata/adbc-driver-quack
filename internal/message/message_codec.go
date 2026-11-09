package message

import (
	"fmt"
	"sort"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// The wire format (QUACK_VERSION 3) is: a header object, a body object,
// and, for FETCH_RESPONSE and SEND_DATA_REQUEST only, a "blob" of
// ChunkCount DataChunk objects written back to back after the body.
// Field ids and defaults mirror duckdb-quack's serialize_quack_message.cpp.

// EncodeMessage serializes a Quack message to wire bytes.
func EncodeMessage(m QuackMessage) ([]byte, error) {
	w := codec.NewBinaryWriter(256)
	encodeHeader(w, m.Header(), messageTypeOf(m))
	encodeBody(w, m)
	encodeBlob(w, m)
	if w.Err() != nil {
		return nil, w.Err()
	}
	return w.Bytes(), nil
}

// DecodeMessage parses wire bytes into a typed QuackMessage.
func DecodeMessage(bytes []byte) (QuackMessage, error) {
	r := codec.NewBinaryReader(bytes)
	header := decodeHeader(r)
	if r.Err() != nil {
		return nil, r.Err()
	}
	m := decodeBody(r, header)
	if r.Err() != nil {
		return nil, r.Err()
	}
	if m == nil {
		return nil, fmt.Errorf("message: unsupported message type %d", header.Type)
	}
	m = decodeBlob(r, m)
	if r.Err() != nil {
		return nil, r.Err()
	}
	if err := r.AssertEOF(); err != nil {
		return nil, err
	}
	return m, nil
}

func messageTypeOf(m QuackMessage) MessageType {
	switch m.(type) {
	case ConnectionRequest:
		return MessageTypeConnectionRequest
	case ConnectionResponse:
		return MessageTypeConnectionResponse
	case PrepareRequest:
		return MessageTypePrepareRequest
	case PrepareResponse:
		return MessageTypePrepareResponse
	case FetchRequest:
		return MessageTypeFetchRequest
	case FetchResponse:
		return MessageTypeFetchResponse
	case SendDataRequest:
		return MessageTypeSendDataRequest
	case SendDataResponse:
		return MessageTypeSendDataResponse
	case SuccessResponse:
		return MessageTypeSuccessResponse
	case DisconnectMessage:
		return MessageTypeDisconnectMessage
	case HeartbeatRequest:
		return MessageTypeHeartbeatRequest
	case CancelRequest:
		return MessageTypeCancelRequest
	case Acknowledgement:
		return MessageTypeAcknowledgement
	case ErrorResponse:
		return MessageTypeErrorResponse
	}
	panic(fmt.Sprintf("messageTypeOf: unknown %T", m))
}

func encodeHeader(w *codec.BinaryWriter, hdr MessageHeader, mt MessageType) {
	w.WriteObject(func(obj *codec.BinaryWriter) {
		obj.WriteField(1, func(o *codec.BinaryWriter) { o.WriteULEB128(uint64(mt)) })
		if hdr.ConnectionID != "" {
			obj.WriteField(2, func(o *codec.BinaryWriter) { o.WriteString(hdr.ConnectionID) })
		}
		queryID := hdr.ClientQueryID
		if !hdr.HasClientQueryID() {
			queryID = codec.OptionalIndexInvalid
		}
		obj.WriteField(3, func(o *codec.BinaryWriter) { o.WriteULEB128(queryID) })
	})
}

func decodeHeader(r *codec.BinaryReader) MessageHeader {
	v, _ := codec.ReadObject(r, func(rr *codec.BinaryReader) MessageHeader {
		mt := MessageType(codec.ReadRequiredField(rr, 1, func(rrr *codec.BinaryReader) int { return rrr.ReadULEB128Int() }))
		connID := codec.ReadOptionalField(rr, 2, readString, "")
		queryID := codec.ReadOptionalField(rr, 3, readULEB, codec.OptionalIndexInvalid)
		return MessageHeader{Type: mt, ConnectionID: connID, ClientQueryID: queryID}
	})
	return v
}

func encodeBody(w *codec.BinaryWriter, m QuackMessage) {
	switch v := m.(type) {
	case ConnectionRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.AuthString)
			writeOptionalString(o, 2, v.ClientDuckDBVersion)
			writeOptionalString(o, 3, v.ClientPlatform)
			writeIndexNonZero(o, 4, v.MinSupportedQuackVersion)
			writeIndexNonZero(o, 5, v.MaxSupportedQuackVersion)
			writeOptionalString(o, 6, v.ClientID)
			writeIndexNonZero(o, 7, v.HeartbeatTimeoutSeconds)
		})
	case ConnectionResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.ServerDuckDBVersion)
			writeOptionalString(o, 2, v.ServerPlatform)
			writeIndexNonZero(o, 3, v.QuackVersion)
			writeIndexNonZero(o, 4, v.HeartbeatTimeoutSeconds)
		})
	case PrepareRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.SQL)
			o.WriteField(2, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.QueryUUID) })
			writeOptionalIndex(o, 3, v.InlineRows)
		})
	case PrepareResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			if len(v.ResultTypes) > 0 {
				o.WriteField(1, func(oo *codec.BinaryWriter) {
					codec.WriteList(oo, v.ResultTypes, func(_ int, t quacktype.LogicalType, ww *codec.BinaryWriter) {
						quacktype.EncodeLogicalType(ww, t)
					})
				})
			}
			if len(v.ResultNames) > 0 {
				o.WriteField(2, func(oo *codec.BinaryWriter) {
					codec.WriteList(oo, v.ResultNames, func(_ int, s string, ww *codec.BinaryWriter) { ww.WriteString(s) })
				})
			}
			if v.NeedsMoreFetch {
				o.WriteField(3, func(oo *codec.BinaryWriter) { oo.WriteBool(true) })
			}
			if len(v.Results) > 0 {
				o.WriteField(4, func(oo *codec.BinaryWriter) { writeChunkPointerList(oo, v.Results) })
			}
			o.WriteField(5, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.QueryUUID) })
		})
	case FetchRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			o.WriteField(1, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.QueryUUID) })
			writeIndexNonZero(o, 2, v.BatchIndex)
			writeIndexNonZero(o, 3, v.AckIndex)
		})
	case FetchResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeIndexNonZero(o, 1, uint64(len(v.Results)))
			writeOptionalIndex(o, 2, v.TotalBatches)
			writeOptionalIndex(o, 3, v.BatchIndex)
		})
	case SendDataRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.StreamID)
			writeIndexNonZero(o, 2, uint64(len(v.Chunks)))
			writeOptionalIndex(o, 3, v.TotalBatches)
			writeOptionalIndex(o, 4, v.BatchIndex)
		})
	case SendDataResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			budget := codec.OptionalIndexInvalid
			if v.AcceptBudget.Valid {
				budget = v.AcceptBudget.Value
			}
			o.WriteField(1, func(oo *codec.BinaryWriter) { oo.WriteULEB128(budget) })
		})
	case SuccessResponse, DisconnectMessage, HeartbeatRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {})
	case CancelRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			o.WriteField(1, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.QueryUUID) })
		})
	case Acknowledgement:
		w.WriteObject(func(o *codec.BinaryWriter) {
			if v.QueryUUID != (codec.HugeIntParts{}) {
				o.WriteField(1, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.QueryUUID) })
			}
		})
	case ErrorResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.Message)
			writeOptionalString(o, 2, v.ExceptionType)
			if len(v.ExtraInfo) > 0 {
				keys := make([]string, 0, len(v.ExtraInfo))
				for k := range v.ExtraInfo {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				o.WriteField(3, func(oo *codec.BinaryWriter) {
					codec.WriteList(oo, keys, func(_ int, k string, ww *codec.BinaryWriter) {
						ww.WriteObject(func(pair *codec.BinaryWriter) {
							pair.WriteField(0, func(p *codec.BinaryWriter) { p.WriteString(k) })
							pair.WriteField(1, func(p *codec.BinaryWriter) { p.WriteString(v.ExtraInfo[k]) })
						})
					})
				})
			}
			if v.MustInvalidate {
				o.WriteField(4, func(oo *codec.BinaryWriter) { oo.WriteBool(true) })
			}
		})
	}
}

// encodeBlob writes the chunk blob that follows FETCH_RESPONSE and
// SEND_DATA_REQUEST bodies: each chunk is one self-delimiting object.
func encodeBlob(w *codec.BinaryWriter, m QuackMessage) {
	var chunks []DataChunk
	switch v := m.(type) {
	case FetchResponse:
		chunks = v.Results
	case SendDataRequest:
		chunks = v.Chunks
	default:
		return
	}
	for _, c := range chunks {
		EncodeDataChunk(w, c)
	}
}

func decodeBlob(r *codec.BinaryReader, m QuackMessage) QuackMessage {
	switch v := m.(type) {
	case FetchResponse:
		v.Results = readChunkBlob(r, v.ChunkCount)
		return v
	case SendDataRequest:
		count := uint64(len(v.Chunks))
		v.Chunks = readChunkBlob(r, count)
		return v
	}
	return m
}

func readChunkBlob(r *codec.BinaryReader, count uint64) []DataChunk {
	if count == 0 {
		return nil
	}
	// The count comes from the wire: cap the reservation, a false count
	// fails on the read.
	out := make([]DataChunk, 0, min(count, 1024))
	for i := uint64(0); i < count && r.Err() == nil; i++ {
		out = append(out, DecodeDataChunk(r))
	}
	return out
}

func decodeBody(r *codec.BinaryReader, header MessageHeader) QuackMessage {
	switch header.Type {
	case MessageTypeConnectionRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := ConnectionRequest{Hdr: header}
			m.AuthString = codec.ReadOptionalField(obj, 1, readString, "")
			m.ClientDuckDBVersion = codec.ReadOptionalField(obj, 2, readString, "")
			m.ClientPlatform = codec.ReadOptionalField(obj, 3, readString, "")
			m.MinSupportedQuackVersion = codec.ReadOptionalField(obj, 4, readULEB, 0)
			m.MaxSupportedQuackVersion = codec.ReadOptionalField(obj, 5, readULEB, 0)
			m.ClientID = codec.ReadOptionalField(obj, 6, readString, "")
			m.HeartbeatTimeoutSeconds = codec.ReadOptionalField(obj, 7, readULEB, 0)
			return m
		})
		return v
	case MessageTypeConnectionResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := ConnectionResponse{Hdr: header}
			m.ServerDuckDBVersion = codec.ReadOptionalField(obj, 1, readString, "")
			m.ServerPlatform = codec.ReadOptionalField(obj, 2, readString, "")
			m.QuackVersion = codec.ReadOptionalField(obj, 3, readULEB, 0)
			m.HeartbeatTimeoutSeconds = codec.ReadOptionalField(obj, 4, readULEB, 0)
			return m
		})
		return v
	case MessageTypePrepareRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := PrepareRequest{Hdr: header}
			m.SQL = codec.ReadOptionalField(obj, 1, readString, "")
			m.QueryUUID = codec.ReadRequiredField(obj, 2, readHugeInt)
			m.InlineRows = readOptionalIndexField(obj, 3)
			return m
		})
		return v
	case MessageTypePrepareResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := PrepareResponse{Hdr: header}
			m.ResultTypes = codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) []quacktype.LogicalType {
				return codec.ReadList(o, func(_ int, oo *codec.BinaryReader) quacktype.LogicalType {
					return quacktype.DecodeLogicalType(oo)
				})
			}, nil)
			m.ResultNames = codec.ReadOptionalField(obj, 2, func(o *codec.BinaryReader) []string {
				return codec.ReadList(o, func(_ int, oo *codec.BinaryReader) string { return oo.ReadString() })
			}, nil)
			m.NeedsMoreFetch = codec.ReadOptionalField(obj, 3, func(o *codec.BinaryReader) bool { return o.ReadBool() }, false)
			m.Results = codec.ReadOptionalField(obj, 4, readChunkPointerList, nil)
			m.QueryUUID = codec.ReadRequiredField(obj, 5, readHugeInt)
			return m
		})
		return v
	case MessageTypeFetchRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := FetchRequest{Hdr: header}
			m.QueryUUID = codec.ReadRequiredField(obj, 1, readHugeInt)
			m.BatchIndex = codec.ReadOptionalField(obj, 2, readULEB, 0)
			m.AckIndex = codec.ReadOptionalField(obj, 3, readULEB, 0)
			return m
		})
		return v
	case MessageTypeFetchResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := FetchResponse{Hdr: header}
			m.ChunkCount = codec.ReadOptionalField(obj, 1, readULEB, 0)
			m.TotalBatches = readOptionalIndexField(obj, 2)
			m.BatchIndex = readOptionalIndexField(obj, 3)
			return m
		})
		return v
	case MessageTypeSendDataRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := SendDataRequest{Hdr: header}
			m.StreamID = codec.ReadOptionalField(obj, 1, readString, "")
			count := codec.ReadOptionalField(obj, 2, readULEB, 0)
			m.TotalBatches = readOptionalIndexField(obj, 3)
			m.BatchIndex = readOptionalIndexField(obj, 4)
			// carry the count to decodeBlob through the slice length
			if count > 0 {
				m.Chunks = make([]DataChunk, min(count, 1<<20))
			}
			return m
		})
		return v
	case MessageTypeSendDataResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return SendDataResponse{Hdr: header, AcceptBudget: readOptionalIndexField(obj, 1)}
		})
		return v
	case MessageTypeSuccessResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage { return SuccessResponse{Hdr: header} })
		return v
	case MessageTypeDisconnectMessage:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage { return DisconnectMessage{Hdr: header} })
		return v
	case MessageTypeHeartbeatRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage { return HeartbeatRequest{Hdr: header} })
		return v
	case MessageTypeCancelRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return CancelRequest{Hdr: header, QueryUUID: codec.ReadRequiredField(obj, 1, readHugeInt)}
		})
		return v
	case MessageTypeAcknowledgement:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return Acknowledgement{Hdr: header, QueryUUID: codec.ReadOptionalField(obj, 1, readHugeInt, codec.HugeIntParts{})}
		})
		return v
	case MessageTypeErrorResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := ErrorResponse{Hdr: header}
			m.Message = codec.ReadOptionalField(obj, 1, readString, "")
			m.ExceptionType = codec.ReadOptionalField(obj, 2, readString, "")
			m.ExtraInfo = codec.ReadOptionalField(obj, 3, readStringMap, nil)
			m.MustInvalidate = codec.ReadOptionalField(obj, 4, func(o *codec.BinaryReader) bool { return o.ReadBool() }, false)
			return m
		})
		return v
	}
	return nil
}

func readString(r *codec.BinaryReader) string              { return r.ReadString() }
func readULEB(r *codec.BinaryReader) uint64                { return r.ReadULEB128() }
func readHugeInt(r *codec.BinaryReader) codec.HugeIntParts { return r.ReadHugeInt() }

// readStringMap reads a serialized unordered_map<string, string>: a list
// of {0: key, 1: value} objects.
func readStringMap(r *codec.BinaryReader) map[string]string {
	pairs := codec.ReadList(r, func(_ int, rr *codec.BinaryReader) [2]string {
		v, _ := codec.ReadObject(rr, func(obj *codec.BinaryReader) [2]string {
			k := codec.ReadOptionalField(obj, 0, readString, "")
			val := codec.ReadOptionalField(obj, 1, readString, "")
			return [2]string{k, val}
		})
		return v
	})
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		out[p[0]] = p[1]
	}
	return out
}

func readOptionalIndexField(r *codec.BinaryReader, fieldID uint16) OptionalIndex {
	v := codec.ReadOptionalField(r, fieldID, readULEB, codec.OptionalIndexInvalid)
	if v == codec.OptionalIndexInvalid {
		return OptionalIndex{}
	}
	return SomeIndex(v)
}

func writeOptionalString(w *codec.BinaryWriter, fieldID uint16, value string) {
	if value != "" {
		w.WriteField(fieldID, func(o *codec.BinaryWriter) { o.WriteString(value) })
	}
}

// writeIndexNonZero writes an idx_t property whose default is 0.
func writeIndexNonZero(w *codec.BinaryWriter, fieldID uint16, value uint64) {
	if value != 0 {
		w.WriteField(fieldID, func(o *codec.BinaryWriter) { o.WriteULEB128(value) })
	}
}

// writeOptionalIndex writes an optional_idx property whose default is unset.
func writeOptionalIndex(w *codec.BinaryWriter, fieldID uint16, value OptionalIndex) {
	if value.Valid {
		w.WriteField(fieldID, func(o *codec.BinaryWriter) { o.WriteULEB128(value.Value) })
	}
}

func writeChunkPointerList(w *codec.BinaryWriter, chunks []DataChunk) {
	codec.WriteList(w, chunks, func(_ int, c DataChunk, ww *codec.BinaryWriter) {
		ww.WriteBool(true)
		EncodeDataChunkWrapper(ww, c)
	})
}

func readChunkPointerList(r *codec.BinaryReader) []DataChunk {
	return codec.ReadList(r, func(_ int, rr *codec.BinaryReader) DataChunk {
		present := rr.ReadBool()
		if !present {
			return DataChunk{}
		}
		return DecodeDataChunkWrapper(rr)
	})
}
