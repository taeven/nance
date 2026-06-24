package wire

import (
	"bytes"
	"encoding/binary"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestReadWriteMsgRoundTrip(t *testing.T) {
	body, err := bson.Marshal(bson.D{{Key: "hello", Value: int32(1)}, {Key: "$db", Value: "admin"}})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, 1, 0, bson.Raw(body)); err != nil {
		t.Fatal(err)
	}

	h, err := ReadHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if h.OpCode != OpMsg {
		t.Fatalf("opcode %d", h.OpCode)
	}
	msg, err := ReadMsg(&buf, h)
	if err != nil {
		t.Fatal(err)
	}
	name, err := CommandName(msg.Body)
	if err != nil {
		t.Fatal(err)
	}
	if name != "hello" {
		t.Fatalf("cmd name %q", name)
	}
	if LookupString(msg.Body, "$db") != "admin" {
		t.Fatalf("$db = %q", LookupString(msg.Body, "$db"))
	}
}

func TestCommandNameSkipsMeta(t *testing.T) {
	body, _ := bson.Marshal(bson.D{
		{Key: "$db", Value: "test"},
		{Key: "lsid", Value: bson.D{{Key: "id", Value: "x"}}},
		{Key: "find", Value: "users"},
	})
	name, err := CommandName(bson.Raw(body))
	if err != nil {
		t.Fatal(err)
	}
	if name != "find" {
		t.Fatalf("got %q", name)
	}
}

func TestReadMsgWithSequence(t *testing.T) {
	// Build a minimal OP_MSG with Kind 0 + Kind 1 (insert documents)
	cmd, _ := bson.Marshal(bson.D{{Key: "insert", Value: "c"}, {Key: "$db", Value: "d"}})
	doc1, _ := bson.Marshal(bson.D{{Key: "a", Value: 1}})
	doc2, _ := bson.Marshal(bson.D{{Key: "b", Value: 2}})

	// Kind 1 section: size + "documents\0" + docs
	ident := "documents"
	secInner := 4 + len(ident) + 1 + len(doc1) + len(doc2)
	sec := make([]byte, secInner)
	binary.LittleEndian.PutUint32(sec[0:4], uint32(secInner))
	copy(sec[4:], ident)
	sec[4+len(ident)] = 0
	off := 4 + len(ident) + 1
	copy(sec[off:], doc1)
	off += len(doc1)
	copy(sec[off:], doc2)

	payload := make([]byte, 0, 4+1+len(cmd)+1+len(sec))
	payload = append(payload, 0, 0, 0, 0) // flags
	payload = append(payload, SectionBody)
	payload = append(payload, cmd...)
	payload = append(payload, SectionDocument)
	payload = append(payload, sec...)

	total := MsgHeaderSize + len(payload)
	var frame bytes.Buffer
	h := Header{MessageLength: int32(total), RequestID: 7, ResponseTo: 0, OpCode: OpMsg}
	_ = WriteHeader(&frame, h)
	frame.Write(payload)

	rh, err := ReadHeader(&frame)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := ReadMsg(&frame, rh)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Sequences) != 1 || msg.Sequences[0].Identifier != "documents" {
		t.Fatalf("sequences: %+v", msg.Sequences)
	}
	if len(msg.Sequences[0].Documents) != 2 {
		t.Fatalf("docs %d", len(msg.Sequences[0].Documents))
	}
}
