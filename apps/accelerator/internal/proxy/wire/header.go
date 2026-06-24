package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Header is the MongoDB wire protocol message header (16 bytes, little-endian).
type Header struct {
	MessageLength int32
	RequestID     int32
	ResponseTo    int32
	OpCode        int32
}

// ReadHeader reads a 16-byte header from r.
func ReadHeader(r io.Reader) (Header, error) {
	var buf [MsgHeaderSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return Header{}, err
	}
	return Header{
		MessageLength: int32(binary.LittleEndian.Uint32(buf[0:4])),
		RequestID:     int32(binary.LittleEndian.Uint32(buf[4:8])),
		ResponseTo:    int32(binary.LittleEndian.Uint32(buf[8:12])),
		OpCode:        int32(binary.LittleEndian.Uint32(buf[12:16])),
	}, nil
}

// WriteHeader writes the 16-byte header to w.
func WriteHeader(w io.Writer, h Header) error {
	var buf [MsgHeaderSize]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(h.MessageLength))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(h.RequestID))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(h.ResponseTo))
	binary.LittleEndian.PutUint32(buf[12:16], uint32(h.OpCode))
	_, err := w.Write(buf[:])
	return err
}

// BodyLength returns the number of bytes after the header for this message.
func (h Header) BodyLength() (int, error) {
	if h.MessageLength < MsgHeaderSize {
		return 0, fmt.Errorf("invalid messageLength %d (< header)", h.MessageLength)
	}
	return int(h.MessageLength - MsgHeaderSize), nil
}
