package message

import (
	"fmt"

	"github.com/gizmodata/adbc-driver-quack/internal/codec"
	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// EncodeMessage serializes a Quack message to wire bytes.
func EncodeMessage(m QuackMessage) ([]byte, error) {
	w := codec.NewBinaryWriter(256)
	encodeHeader(w, m.Header(), messageTypeOf(m))
	encodeBody(w, m)
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
	case AppendRequest:
		return MessageTypeAppendRequest
	case SuccessResponse:
		return MessageTypeSuccessResponse
	case DisconnectMessage:
		return MessageTypeDisconnectMessage
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
		connID := codec.ReadOptionalField(rr, 2, func(rrr *codec.BinaryReader) string { return rrr.ReadString() }, "")
		queryID := codec.ReadRequiredField(rr, 3, func(rrr *codec.BinaryReader) uint64 { return rrr.ReadULEB128() })
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
			writeOptionalIndexNonZero(o, 4, v.MinSupportedQuackVersion)
			writeOptionalIndexNonZero(o, 5, v.MaxSupportedQuackVersion)
		})
	case ConnectionResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.ServerDuckDBVersion)
			writeOptionalString(o, 2, v.ServerPlatform)
			if v.HasQuackVersion {
				o.WriteField(3, func(oo *codec.BinaryWriter) { oo.WriteULEB128(v.QuackVersion) })
			}
		})
	case PrepareRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.SQL)
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
			o.WriteField(5, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.ResultUUID) })
		})
	case FetchRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			o.WriteField(1, func(oo *codec.BinaryWriter) { oo.WriteHugeInt(v.ResultUUID) })
		})
	case FetchResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			if len(v.Results) > 0 {
				o.WriteField(1, func(oo *codec.BinaryWriter) { writeChunkPointerList(oo, v.Results) })
			}
			batchIdx := v.BatchIndex
			if !v.HasBatchIndex {
				batchIdx = codec.OptionalIndexInvalid
			}
			o.WriteField(2, func(oo *codec.BinaryWriter) { oo.WriteULEB128(batchIdx) })
		})
	case AppendRequest:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.SchemaName)
			if v.TableName != "" {
				o.WriteField(2, func(oo *codec.BinaryWriter) { oo.WriteString(v.TableName) })
			}
			o.WriteField(3, func(oo *codec.BinaryWriter) {
				oo.WriteBool(true)
				EncodeDataChunkWrapper(oo, v.AppendChunk)
			})
		})
	case SuccessResponse, DisconnectMessage:
		w.WriteObject(func(o *codec.BinaryWriter) {})
	case ErrorResponse:
		w.WriteObject(func(o *codec.BinaryWriter) {
			writeOptionalString(o, 1, v.Message)
		})
	}
}

func decodeBody(r *codec.BinaryReader, header MessageHeader) QuackMessage {
	switch header.Type {
	case MessageTypeConnectionRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := ConnectionRequest{Hdr: header}
			m.AuthString = codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			m.ClientDuckDBVersion = codec.ReadOptionalField(obj, 2, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			m.ClientPlatform = codec.ReadOptionalField(obj, 3, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			m.MinSupportedQuackVersion = codec.ReadOptionalField(obj, 4, func(o *codec.BinaryReader) uint64 { return o.ReadULEB128() }, 0)
			m.MaxSupportedQuackVersion = codec.ReadOptionalField(obj, 5, func(o *codec.BinaryReader) uint64 { return o.ReadULEB128() }, 0)
			return m
		})
		return v
	case MessageTypeConnectionResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := ConnectionResponse{Hdr: header}
			m.ServerDuckDBVersion = codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			m.ServerPlatform = codec.ReadOptionalField(obj, 2, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			if obj.PeekFieldID() == 3 {
				obj.ReadFieldID()
				m.QuackVersion = obj.ReadULEB128()
				m.HasQuackVersion = true
			}
			return m
		})
		return v
	case MessageTypePrepareRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return PrepareRequest{
				Hdr: header,
				SQL: codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) string { return o.ReadString() }, ""),
			}
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
			m.ResultUUID = codec.ReadRequiredField(obj, 5, func(o *codec.BinaryReader) codec.HugeIntParts { return o.ReadHugeInt() })
			return m
		})
		return v
	case MessageTypeFetchRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return FetchRequest{
				Hdr:        header,
				ResultUUID: codec.ReadRequiredField(obj, 1, func(o *codec.BinaryReader) codec.HugeIntParts { return o.ReadHugeInt() }),
			}
		})
		return v
	case MessageTypeFetchResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := FetchResponse{Hdr: header}
			m.Results = codec.ReadOptionalField(obj, 1, readChunkPointerList, nil)
			batchIdx := codec.ReadRequiredField(obj, 2, func(o *codec.BinaryReader) uint64 { return o.ReadULEB128() })
			if batchIdx != codec.OptionalIndexInvalid {
				m.BatchIndex = batchIdx
				m.HasBatchIndex = true
			}
			return m
		})
		return v
	case MessageTypeAppendRequest:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			m := AppendRequest{Hdr: header}
			m.SchemaName = codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			m.TableName = codec.ReadOptionalField(obj, 2, func(o *codec.BinaryReader) string { return o.ReadString() }, "")
			codec.ReadOptionalField(obj, 3, func(o *codec.BinaryReader) struct{} {
				if o.ReadBool() {
					m.AppendChunk = DecodeDataChunkWrapper(o)
				}
				return struct{}{}
			}, struct{}{})
			return m
		})
		return v
	case MessageTypeSuccessResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage { return SuccessResponse{Hdr: header} })
		return v
	case MessageTypeDisconnectMessage:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage { return DisconnectMessage{Hdr: header} })
		return v
	case MessageTypeErrorResponse:
		v, _ := codec.ReadObject(r, func(obj *codec.BinaryReader) QuackMessage {
			return ErrorResponse{
				Hdr:     header,
				Message: codec.ReadOptionalField(obj, 1, func(o *codec.BinaryReader) string { return o.ReadString() }, ""),
			}
		})
		return v
	}
	return nil
}

func writeOptionalString(w *codec.BinaryWriter, fieldID uint16, value string) {
	if value != "" {
		w.WriteField(fieldID, func(o *codec.BinaryWriter) { o.WriteString(value) })
	}
}

func writeOptionalIndexNonZero(w *codec.BinaryWriter, fieldID uint16, value uint64) {
	if value != 0 {
		w.WriteField(fieldID, func(o *codec.BinaryWriter) { o.WriteULEB128(value) })
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
