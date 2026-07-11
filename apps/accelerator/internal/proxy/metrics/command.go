package metrics

import "strings"

// Known Mongo command names allowed as Prometheus label values.
// Unknown names map to "other" to bound cardinality.
var allowedCommands = map[string]struct{}{
	"hello": {}, "ismaster": {}, "buildinfo": {}, "ping": {},
	"getcmdlineopts": {}, "whatsmyuri": {}, "getlog": {}, "listcommands": {},
	"connectionstatus": {}, "hostinfo": {}, "features": {},
	"saslstart": {}, "saslcontinue": {}, "logout": {}, "authenticate": {}, "getnonce": {},
	"find": {}, "aggregate": {}, "count": {}, "estimateddocumentcount": {}, "distinct": {},
	"getmore": {}, "killcursors": {},
	"insert": {}, "update": {}, "delete": {}, "findandmodify": {},
	"create": {}, "createindexes": {}, "drop": {}, "dropindexes": {}, "listcollections": {},
	"listdatabases": {}, "listindexes": {}, "collstats": {}, "dbstats": {},
	"explain": {}, "endsessions": {}, "committransaction": {},
	"aborttransaction": {}, "starttransaction": {},
}

// CommandLabel returns a bounded Prometheus label for a wire command name.
func CommandLabel(name string) string {
	c := strings.ToLower(strings.TrimSpace(name))
	if c == "" {
		return "other"
	}
	if _, ok := allowedCommands[c]; ok {
		return c
	}
	return "other"
}
