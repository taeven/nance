package wire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"go.mongodb.org/mongo-driver/bson"
)

// Msg is a decoded OP_MSG (opcode 2013).
type Msg struct {
	Header   Header
	FlagBits uint32
	// Body is the Kind 0 section document (command or reply).
	Body bson.Raw
	// Sequences holds Kind 1 document sequences (identifier + documents), e.g. insert payloads.
	Sequences []DocumentSequence
	// Checksum is present when flag bit 0 is set; we accept but do not validate.
	Checksum *uint32
}

// DocumentSequence is an OP_MSG Kind 1 section.
type DocumentSequence struct {
	Identifier string
	Documents  []bson.Raw
}

// ReadMsg reads one complete OP_MSG from r given an already-read header.
func ReadMsg(r io.Reader, h Header) (*Msg, error) {
	if h.OpCode != OpMsg {
		return nil, fmt.Errorf("expected OP_MSG (%d), got %d", OpMsg, h.OpCode)
	}
	bodyLen, err := h.BodyLength()
	if err != nil {
		return nil, err
	}
	if bodyLen < 4 {
		return nil, fmt.Errorf("OP_MSG body too short: %d", bodyLen)
	}

	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}

	msg := &Msg{Header: h}
	msg.FlagBits = binary.LittleEndian.Uint32(body[0:4])
	offset := 4

	// Optional checksum at the end when flag bit 0 is set.
	payloadEnd := len(body)
	if msg.FlagBits&1 != 0 {
		if len(body) < 8 {
			return nil, fmt.Errorf("OP_MSG checksum flag set but body too short")
		}
		cs := binary.LittleEndian.Uint32(body[len(body)-4:])
		msg.Checksum = &cs
		payloadEnd = len(body) - 4
	}

	for offset < payloadEnd {
		if offset >= payloadEnd {
			break
		}
		kind := body[offset]
		offset++
		switch kind {
		case SectionBody:
			if offset+4 > payloadEnd {
				return nil, fmt.Errorf("truncated Kind 0 section")
			}
			docLen := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
			if docLen < 5 || offset+docLen > payloadEnd {
				return nil, fmt.Errorf("invalid Kind 0 document length %d", docLen)
			}
			msg.Body = bson.Raw(append([]byte(nil), body[offset:offset+docLen]...))
			offset += docLen
		case SectionDocument:
			// size (int32) includes itself; then cstring identifier; then zero or more docs; ends at size boundary
			if offset+4 > payloadEnd {
				return nil, fmt.Errorf("truncated Kind 1 section size")
			}
			secSize := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
			secStart := offset
			if secSize < 5 || secStart+secSize > payloadEnd {
				return nil, fmt.Errorf("invalid Kind 1 section size %d", secSize)
			}
			secEnd := secStart + secSize
			offset += 4
			// identifier cstring
			idEnd := bytes.IndexByte(body[offset:secEnd], 0)
			if idEnd < 0 {
				return nil, fmt.Errorf("Kind 1 missing identifier terminator")
			}
			ident := string(body[offset : offset+idEnd])
			offset += idEnd + 1
			var docs []bson.Raw
			for offset < secEnd {
				if offset+4 > secEnd {
					break
				}
				dlen := int(binary.LittleEndian.Uint32(body[offset : offset+4]))
				if dlen < 5 || offset+dlen > secEnd {
					return nil, fmt.Errorf("invalid Kind 1 document length %d", dlen)
				}
				docs = append(docs, bson.Raw(append([]byte(nil), body[offset:offset+dlen]...)))
				offset += dlen
			}
			msg.Sequences = append(msg.Sequences, DocumentSequence{Identifier: ident, Documents: docs})
			offset = secEnd
		default:
			return nil, fmt.Errorf("unsupported OP_MSG section kind %d", kind)
		}
	}

	if len(msg.Body) == 0 {
		return nil, fmt.Errorf("OP_MSG missing Kind 0 body section")
	}
	return msg, nil
}

// WriteMsg encodes and writes an OP_MSG reply (single Kind 0 body, no sequences, no checksum).
func WriteMsg(w io.Writer, requestID, responseTo int32, body bson.Raw) error {
	// flagBits(4) + kind0(1) + body
	payloadLen := 4 + 1 + len(body)
	totalLen := MsgHeaderSize + payloadLen

	h := Header{
		MessageLength: int32(totalLen),
		RequestID:     requestID,
		ResponseTo:    responseTo,
		OpCode:        OpMsg,
	}
	if err := WriteHeader(w, h); err != nil {
		return err
	}

	var flags [4]byte // flagBits = 0
	if _, err := w.Write(flags[:]); err != nil {
		return err
	}
	if _, err := w.Write([]byte{SectionBody}); err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	return nil
}

// EncodeBody marshals v to bson.Raw suitable as an OP_MSG Kind 0 body.
func EncodeBody(v any) (bson.Raw, error) {
	b, err := bson.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bson.Raw(b), nil
}

// CommandName returns the first non-meta key in a command document (MongoDB convention).
// Meta keys include $db, $readPreference, lsid, txnNumber, etc.
func CommandName(raw bson.Raw) (string, error) {
	elems, err := raw.Elements()
	if err != nil {
		return "", err
	}
	for _, el := range elems {
		key := el.Key()
		if isMetaCommandKey(key) {
			continue
		}
		return key, nil
	}
	return "", fmt.Errorf("no command name in document")
}

func isMetaCommandKey(key string) bool {
	switch key {
	case "$db", "$clusterTime", "$readPreference", "$configTime", "$topologyVersion",
		"lsid", "txnNumber", "autocommit", "startTransaction", "readConcern", "writeConcern",
		"maxTimeMS", "comment", "apiVersion", "apiStrict", "apiDeprecationErrors",
		"$audit", "$client", "$configServerState", "databaseVersion", "shardVersion":
		return true
	default:
		return false
	}
}

// LookupString returns a string field from a BSON document, or "" if missing/wrong type.
func LookupString(raw bson.Raw, key string) string {
	val, err := raw.LookupErr(key)
	if err != nil {
		return ""
	}
	s, ok := val.StringValueOK()
	if !ok {
		return ""
	}
	return s
}

// LookupInt32 returns an int32 field, or 0 if missing.
func LookupInt32(raw bson.Raw, key string) int32 {
	val, err := raw.LookupErr(key)
	if err != nil {
		return 0
	}
	if i, ok := val.Int32OK(); ok {
		return i
	}
	if i, ok := val.Int64OK(); ok {
		return int32(i)
	}
	if i, ok := val.DoubleOK(); ok {
		return int32(i)
	}
	return 0
}

// LookupInt64 returns an int64 field, or 0 if missing.
func LookupInt64(raw bson.Raw, key string) int64 {
	val, err := raw.LookupErr(key)
	if err != nil {
		return 0
	}
	if i, ok := val.Int64OK(); ok {
		return i
	}
	if i, ok := val.Int32OK(); ok {
		return int64(i)
	}
	if i, ok := val.DoubleOK(); ok {
		return int64(i)
	}
	return 0
}

// StripInternalKeys returns a copy of the command without driver-internal $ keys unsuitable for RunCommand.
// We keep $db stripped (driver uses Database(name)) but pass through lsid/txnNumber etc.
func StripForRunCommand(raw bson.Raw) (bson.D, string, error) {
	var m bson.D
	if err := bson.Unmarshal(raw, &m); err != nil {
		return nil, "", err
	}
	dbName := ""
	out := make(bson.D, 0, len(m))
	for _, e := range m {
		if e.Key == "$db" {
			if s, ok := e.Value.(string); ok {
				dbName = s
			}
			continue
		}
		// $readPreference is not valid on the wire to the server via RunCommand in all cases;
		// the driver handles read preference via options. Strip it to avoid "unknown field".
		if e.Key == "$readPreference" || e.Key == "$clusterTime" || e.Key == "$configTime" ||
			e.Key == "$topologyVersion" || e.Key == "$audit" || e.Key == "$client" ||
			e.Key == "$configServerState" {
			continue
		}
		out = append(out, e)
	}
	return out, dbName, nil
}
