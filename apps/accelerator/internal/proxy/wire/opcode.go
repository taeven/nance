package wire

// MongoDB wire protocol opcodes we care about.
const (
	OpReply       = 1
	OpUpdate      = 2001
	OpInsert      = 2002
	OpQuery       = 2004
	OpGetMore     = 2005
	OpDelete      = 2006
	OpKillCursors = 2007
	OpCompressed  = 2012
	OpMsg         = 2013
)

// OP_MSG section kinds.
const (
	SectionBody     = 0
	SectionDocument = 1 // document sequence
)

// MsgHeaderSize is the fixed 16-byte message header length.
const MsgHeaderSize = 16
