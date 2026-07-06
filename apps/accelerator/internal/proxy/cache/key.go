package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
)

// volatileTopLevel keys stripped before hashing (non-semantic for result identity).
var volatileTopLevel = map[string]struct{}{
	"$db":                  {},
	"$clusterTime":         {},
	"$readPreference":      {},
	"lsid":                 {},
	"txnNumber":            {},
	"autocommit":           {},
	"$comment":             {},
	"comment":              {},
	"maxTimeMS":            {},
	"readConcern":          {},
	"$readConcern":         {},
	"apiVersion":           {},
	"apiStrict":            {},
	"apiDeprecationErrors": {},
}

// CacheKey builds a deterministic Redis key for a tenant + connection + namespace + command.
// connectionID isolates caches when an org has multiple source Mongo clusters.
func CacheKey(tenantID, connectionID, db, coll, cmdName string, cmd bson.Raw, cacheKeyVersion int) (string, error) {
	norm, err := NormalizeCommand(cmd)
	if err != nil {
		return "", err
	}
	serialized, err := bson.Marshal(norm)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(append(append(serialized, '|'), []byte(strings.ToLower(cmdName)+"|v"+itoa(cacheKeyVersion))...))
	digest := hex.EncodeToString(h[:])
	if connectionID == "" {
		connectionID = "_"
	}
	key := fmt.Sprintf("nance:tenant:{%s}:conn:%s:ns:%s.%s:cmd:%s:v%d", tenantID, connectionID, db, coll, digest, cacheKeyVersion)
	if os.Getenv("NANCE_DEBUG_CACHE_KEYS") == "1" {
		fmt.Fprintf(os.Stderr, "nance cache key tenant=%s conn=%s ns=%s.%s cmd=%s key=%s\n", tenantID, connectionID, db, coll, cmdName, key)
	}
	return key, nil
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// NormalizeCommand returns a canonical bson.D for hashing.
func NormalizeCommand(raw bson.Raw) (bson.D, error) {
	var doc bson.D
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return normalizeValue(doc).(bson.D), nil
}

func normalizeValue(v any) any {
	switch x := v.(type) {
	case bson.D:
		// Filter volatile keys and sort remaining by key name.
		filtered := make(bson.D, 0, len(x))
		for _, e := range x {
			if _, skip := volatileTopLevel[e.Key]; skip {
				continue
			}
			// Also strip comment inside nested? handled recursively only for top volatile set
			filtered = append(filtered, bson.E{Key: e.Key, Value: normalizeValue(e.Value)})
		}
		sort.SliceStable(filtered, func(i, j int) bool {
			return filtered[i].Key < filtered[j].Key
		})
		return filtered
	case bson.M:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(bson.D, 0, len(keys))
		for _, k := range keys {
			out = append(out, bson.E{Key: k, Value: normalizeValue(x[k])})
		}
		return out
	case bson.A:
		out := make(bson.A, len(x))
		for i, el := range x {
			out[i] = normalizeValue(el)
		}
		return out
	case []any:
		out := make(bson.A, len(x))
		for i, el := range x {
			out[i] = normalizeValue(el)
		}
		return out
	case int32:
		return int64(x) // canonicalize numerics for key stability
	case int:
		return int64(x)
	case float32:
		return float64(x)
	default:
		return x
	}
}

// IsCacheableCommand returns whether the command kind can participate in caching.
func IsCacheableCommand(cmdName string) bool {
	switch strings.ToLower(cmdName) {
	case "find", "aggregate", "count", "estimateddocumentcount", "distinct":
		return true
	default:
		return false
	}
}

// ShouldBypassCache returns true when transaction / change-stream / mutating stages forbid caching.
func ShouldBypassCache(cmdName string, raw bson.Raw, isTxn bool) (bypass bool, reason string) {
	if isTxn {
		return true, "transaction"
	}
	if _, err := raw.LookupErr("txnNumber"); err == nil {
		return true, "transaction"
	}
	lname := strings.ToLower(cmdName)
	if lname == "explain" {
		return true, "explain"
	}
	if lname == "aggregate" {
		// Reject pipelines with $out / $merge / $changeStream
		if hasForbiddenAggStage(raw) {
			return true, "agg_stage"
		}
	}
	if !IsCacheableCommand(cmdName) {
		return true, "command"
	}
	return false, ""
}

func hasForbiddenAggStage(raw bson.Raw) bool {
	val, err := raw.LookupErr("pipeline")
	if err != nil {
		return false
	}
	rawArr, ok := val.ArrayOK()
	if !ok {
		return false
	}
	vals, err := rawArr.Values()
	if err != nil {
		return false
	}
	for _, v := range vals {
		doc, ok := v.DocumentOK()
		if !ok {
			continue
		}
		els, _ := doc.Elements()
		for _, el := range els {
			k := el.Key()
			if k == "$out" || k == "$merge" || k == "$changeStream" {
				return true
			}
		}
	}
	return false
}

// CacheKeyWithGeneration appends a generation segment so bumps invalidate logically.
func CacheKeyWithGeneration(tenantID, connectionID, db, coll, cmdName string, cmd bson.Raw, cacheKeyVersion int, generation int64) (string, error) {
	base, err := CacheKey(tenantID, connectionID, db, coll, cmdName, cmd, cacheKeyVersion)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:g%d", base, generation), nil
}
