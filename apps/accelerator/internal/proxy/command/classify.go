package command

import (
	"strings"

	"github.com/taeven/nance/accelerator/internal/proxy/wire"

	"go.mongodb.org/mongo-driver/bson"
)

// Kind categorizes commands for routing / metrics.
type Kind int

const (
	KindHandshake Kind = iota
	KindAuth
	KindRead
	KindWrite
	KindCursor
	KindTxn
	KindAdmin
	KindOther
)

// Info describes a parsed command document.
type Info struct {
	Name       string
	DB         string
	Collection string
	Kind       Kind
	IsTxn      bool
	Raw        bson.Raw
}

// Classify extracts command metadata from an OP_MSG body.
func Classify(raw bson.Raw) (Info, error) {
	name, err := wire.CommandName(raw)
	if err != nil {
		return Info{}, err
	}
	db := wire.LookupString(raw, "$db")
	if db == "" {
		db = "admin"
	}

	info := Info{
		Name:  name,
		DB:    db,
		Raw:   raw,
		IsTxn: hasTxnContext(raw),
	}

	lname := strings.ToLower(name)
	switch lname {
	case "hello", "ismaster", "isMaster":
		info.Kind = KindHandshake
	case "saslstart", "saslStart", "saslcontinue", "saslContinue", "authenticate", "logout", "getnonce":
		info.Kind = KindAuth
	case "getmore", "getMore", "killcursors", "killCursors":
		info.Kind = KindCursor
	case "aborttransaction", "abortTransaction", "committransaction", "commitTransaction",
		"endsessions", "endSessions", "refreshsessions", "refreshSessions", "startsession":
		info.Kind = KindTxn
	case "buildinfo", "buildInfo", "getcmdlineopts", "getCmdLineOpts", "ping", "whatsmyuri",
		"getlog", "getLog", "listcommands", "listCommands", "connectionstatus", "connectionStatus",
		"hostinfo", "hostInfo", "features", "getparameter", "getParameter":
		info.Kind = KindAdmin
	case "find", "aggregate", "count", "estimateddocumentcount", "estimatedDocumentCount",
		"distinct", "listcollections", "listCollections", "listindexes", "listIndexes",
		"listdatabases", "listDatabases", "collstats", "collStats", "dbstats", "dbStats",
		"currentop", "currentOp", "explain":
		info.Kind = KindRead
		info.Collection = collectionFromCommand(raw, name)
	case "insert", "update", "delete", "findandmodify", "findAndModify", "bulkwrite", "bulkWrite",
		"create", "createcollection", "createCollection", "createindexes", "createIndexes",
		"drop", "dropdatabase", "dropDatabase", "dropindexes", "dropIndexes", "renamecollection",
		"renameCollection", "createuser", "createUser":
		info.Kind = KindWrite
		info.Collection = collectionFromCommand(raw, name)
	default:
		info.Kind = KindOther
		info.Collection = collectionFromCommand(raw, name)
	}

	return info, nil
}

func hasTxnContext(raw bson.Raw) bool {
	if _, err := raw.LookupErr("txnNumber"); err == nil {
		return true
	}
	if _, err := raw.LookupErr("autocommit"); err == nil {
		return true
	}
	return false
}

func collectionFromCommand(raw bson.Raw, cmdName string) string {
	// Most commands use the command name as key with collection string as value.
	val, err := raw.LookupErr(cmdName)
	if err != nil {
		// try case-sensitive known alternates
		return ""
	}
	if s, ok := val.StringValueOK(); ok {
		return s
	}
	// int32 for some admin cmds
	return ""
}

// IsHandshake returns true for hello/isMaster (allowed before auth).
func IsHandshake(name string) bool {
	switch strings.ToLower(name) {
	case "hello", "ismaster":
		return true
	default:
		return false
	}
}

// IsAuthCommand returns true for SASL / authenticate commands.
func IsAuthCommand(name string) bool {
	switch strings.ToLower(name) {
	case "saslstart", "saslcontinue", "authenticate", "logout", "getnonce":
		return true
	default:
		return false
	}
}

// IsPreAuthAllowed returns commands permitted without tenant context.
func IsPreAuthAllowed(name string) bool {
	if IsHandshake(name) || IsAuthCommand(name) {
		return true
	}
	switch strings.ToLower(name) {
	case "ping", "buildinfo", "getcmdlineopts", "whatsmyuri", "getlog", "listcommands",
		"connectionstatus", "hostinfo", "features":
		return true
	default:
		return false
	}
}
