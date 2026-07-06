package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
	"github.com/taeven/nance/accelerator/internal/proxy/cachedcursor"
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	"github.com/taeven/nance/accelerator/internal/proxy/command"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/policy"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/ratelimit"
	"github.com/taeven/nance/accelerator/internal/proxy/wire"
	"github.com/taeven/nance/accelerator/internal/telemetry"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ConnState is per-TCP-connection mutable state.
type ConnState struct {
	ID          int32
	Key         string // unique key for cursor scoping
	Tenant      *auth.TenantContext
	Authed      bool
	RemoteAddr  string
	AllowUnauth bool
	connIDGen   *atomic.Int32
}

// Deps bundles handler dependencies.
type Deps struct {
	Auth          *auth.Validator
	Pool          *pool.Manager
	Cursors       *cursor.Registry
	CachedCursors *cachedcursor.Store
	Cache         *cache.Coordinator
	CacheStats    *cachestats.Tracker // per-collection hit/miss (in-process, lock-free)
	Policies      *policy.Engine
	// Store loads connection settings (e.g. auto-invalidate on write). Optional.
	Store        store.Store
	Limiter      *ratelimit.Limiter
	Log          *slog.Logger
	ConnID       *atomic.Int32 // global connection id counter for hello replies
	DefaultBatch int32
}

// Handler processes one OP_MSG request and returns a reply body document.
type Handler struct {
	deps Deps
}

func New(deps Deps) *Handler {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.ConnID == nil {
		deps.ConnID = &atomic.Int32{}
	}
	return &Handler{deps: deps}
}

// Handle dispatches an OP_MSG and returns the reply BSON document (as bson.D or bson.Raw-compatible).
func (h *Handler) Handle(ctx context.Context, cs *ConnState, msg *wire.Msg) (any, error) {
	start := time.Now()
	info, err := command.Classify(msg.Body)
	if err != nil {
		return command.ErrorReply(2, "BadValue", err.Error()), nil
	}

	cmdLower := strings.ToLower(info.Name)
	tenantLabel := "unauth"
	if cs.Tenant != nil {
		tenantLabel = cs.Tenant.TenantID
	}

	defer func() {
		telemetry.ProxyCommands.WithLabelValues(tenantLabel, cmdLower).Inc()
		telemetry.ProxyCommandDuration.WithLabelValues(cmdLower).Observe(time.Since(start).Seconds())
	}()

	// Pre-auth gate
	if !cs.Authed && !cs.AllowUnauth && !command.IsPreAuthAllowed(info.Name) {
		return command.NotAuthorized(""), nil
	}

	// Phase 3: per-tenant command rate limit (post-auth only)
	if cs.Authed && cs.Tenant != nil && h.deps.Limiter != nil && !command.IsPreAuthAllowed(info.Name) {
		if !h.deps.Limiter.Allow(cs.Tenant.TenantID) {
			telemetry.ProxyRateLimited.WithLabelValues(cs.Tenant.TenantID).Inc()
			return command.ErrorReply(16500, "RateLimitExceeded", "tenant command rate limit exceeded; retry with backoff"), nil
		}
	}

	switch cmdLower {
	case "hello", "ismaster":
		return h.handleHello(cs, info.Name), nil
	case "buildinfo":
		return command.BuildInfoReply(), nil
	case "ping":
		return command.PingReply(), nil
	case "getcmdlineopts":
		return bson.D{{Key: "argv", Value: bson.A{"nance-proxy"}}, {Key: "parsed", Value: bson.D{}}, {Key: "ok", Value: float64(1)}}, nil
	case "whatsmyuri":
		return bson.D{{Key: "you", Value: cs.RemoteAddr}, {Key: "ok", Value: float64(1)}}, nil
	case "getlog":
		return bson.D{{Key: "log", Value: bson.A{}}, {Key: "ok", Value: float64(1)}}, nil
	case "listcommands":
		return bson.D{{Key: "commands", Value: bson.D{}}, {Key: "ok", Value: float64(1)}}, nil
	case "connectionstatus":
		return h.handleConnectionStatus(cs), nil
	case "hostinfo":
		return bson.D{{Key: "os", Value: bson.D{{Key: "type", Value: "Linux"}, {Key: "name", Value: "nance"}}}, {Key: "ok", Value: float64(1)}}, nil
	case "features":
		return command.OKReply(), nil
	case "saslstart":
		return h.handleSaslStart(ctx, cs, msg.Body)
	case "saslcontinue":
		// PLAIN is single-step; reject continuation
		return command.AuthFailed("SASL conversation not in progress"), nil
	case "logout":
		cs.Authed = false
		cs.Tenant = nil
		return command.OKReply(), nil
	case "authenticate":
		// Legacy MONGODB-CR style; not supported — point to PLAIN
		return command.ErrorReply(2, "BadValue", "Use authMechanism=PLAIN with tenant id as username and API token as password"), nil
	case "getnonce":
		return bson.D{{Key: "nonce", Value: "0000000000000000"}, {Key: "ok", Value: float64(1)}}, nil
	case "getmore":
		// Prefer emulated cache cursors, then backend cursor registry
		if reply, ok := h.handleCachedGetMore(ctx, cs, msg.Body); ok {
			return reply, nil
		}
		return h.handleGetMore(ctx, cs, msg.Body)
	case "killcursors":
		h.handleKillCachedCursors(cs, msg.Body)
		return h.handleKillCursors(cs, msg.Body), nil
	default:
		// Requires auth unless unauth allowed
		if !cs.Authed && !cs.AllowUnauth {
			return command.NotAuthorized(""), nil
		}
		if cs.Tenant == nil && !cs.AllowUnauth {
			return command.NotAuthorized(""), nil
		}
		return h.handlePassthrough(ctx, cs, msg, info)
	}
}

func (h *Handler) handleHello(cs *ConnState, cmdName string) bson.D {
	cid := cs.ID
	if cid == 0 {
		cid = h.deps.ConnID.Add(1)
		cs.ID = cid
	}
	return command.HelloReply(cid, cmdName)
}

// HandleHelloOnly is used for legacy OP_QUERY isMaster/hello.
func (h *Handler) HandleHelloOnly(cs *ConnState, cmdName string) bson.D {
	return h.handleHello(cs, cmdName)
}

func (h *Handler) handleConnectionStatus(cs *ConnState) bson.D {
	authUsers := bson.A{}
	if cs.Authed && cs.Tenant != nil {
		authUsers = append(authUsers, bson.D{
			{Key: "user", Value: cs.Tenant.TenantID},
			{Key: "db", Value: "$external"},
		})
	}
	return bson.D{
		{Key: "authInfo", Value: bson.D{
			{Key: "authenticatedUsers", Value: authUsers},
			{Key: "authenticatedUserRoles", Value: bson.A{}},
		}},
		{Key: "ok", Value: float64(1)},
	}
}

func (h *Handler) handleSaslStart(ctx context.Context, cs *ConnState, body bson.Raw) (any, error) {
	mech := wire.LookupString(body, "mechanism")
	if !strings.EqualFold(mech, "PLAIN") {
		return command.ErrorReply(334, "MechanismUnavailable",
			"Only PLAIN is supported in Phase 1. Use authMechanism=PLAIN&authSource=$external"), nil
	}

	payload, err := extractPayload(body)
	if err != nil || len(payload) == 0 {
		return command.AuthFailed("missing PLAIN payload"), nil
	}

	username, password, err := auth.ParsePLAINPayload(payload)
	if err != nil {
		return command.AuthFailed(""), nil
	}

	tc, err := h.deps.Auth.Authenticate(ctx, username, password)
	if err != nil {
		h.deps.Log.Info("auth failed", "user", username, "error", err)
		telemetry.ProxyAuthFailures.Inc()
		return command.AuthFailed(""), nil
	}

	cs.Tenant = tc
	cs.Authed = true
	telemetry.ProxyAuthSuccess.WithLabelValues(tc.TenantID).Inc()
	h.deps.Log.Info("auth ok", "tenant", tc.TenantID, "token_id", tc.TokenID, "remote", cs.RemoteAddr)
	return command.AuthOK(), nil
}

func extractPayload(body bson.Raw) ([]byte, error) {
	val, err := body.LookupErr("payload")
	if err != nil {
		return nil, err
	}
	// BinData subtype 0
	if subtype, data, ok := val.BinaryOK(); ok {
		_ = subtype
		return data, nil
	}
	// Sometimes base64 string (rare)
	if s, ok := val.StringValueOK(); ok {
		return base64.StdEncoding.DecodeString(s)
	}
	return nil, err
}

func (h *Handler) handlePassthrough(ctx context.Context, cs *ConnState, msg *wire.Msg, info command.Info) (any, error) {
	tenantID := ""
	connectionID := ""
	if cs.Tenant != nil {
		tenantID = cs.Tenant.TenantID
		connectionID = cs.Tenant.ConnectionID
	}
	if tenantID == "" || connectionID == "" {
		return command.NotAuthorized("no tenant context"), nil
	}

	cmdDoc, dbName, err := wire.StripForRunCommand(msg.Body)
	if err != nil {
		return command.ErrorReply(2, "BadValue", err.Error()), nil
	}
	if dbName == "" {
		dbName = info.DB
	}

	// Merge Kind 1 document sequences into insert/update/delete as appropriate
	cmdDoc = mergeDocumentSequences(cmdDoc, msg.Sequences)
	cmdLower := strings.ToLower(info.Name)
	collName := info.Collection
	if collName == "" {
		collName = fieldString(cmdDoc, info.Name)
	}

	// Developers opt into caching by querying collection+"_cache". The proxy strips
	// that suffix and talks to the real collection; non-suffixed names bypass cache entirely.
	realColl, useCache := command.ResolveCacheCollection(collName)
	if realColl != collName {
		cmdDoc = setFieldString(cmdDoc, info.Name, realColl)
		info.Collection = realColl
		collName = realColl
	}
	nsLabel := command.FormatNS(dbName, collName)

	// Phase 2: try read-through cache only when the client used the _cache suffix
	if useCache {
		if reply, handled := h.tryCacheRead(ctx, cs, tenantID, connectionID, dbName, collName, nsLabel, cmdLower, msg, info, cmdDoc); handled {
			return reply, nil
		}
	}

	client, err := h.deps.Pool.Get(ctx, connectionID)
	if err != nil {
		h.deps.Log.Error("backend pool error", "tenant", tenantID, "connection", connectionID, "error", err)
		telemetry.ProxyBackendErrors.WithLabelValues(tenantID).Inc()
		return command.ErrorReply(6, "HostUnreachable", "failed to reach tenant backend: "+err.Error()), nil
	}
	defer h.deps.Pool.Release(connectionID)

	// Cursor-producing reads: use collection helpers so we can manage getMore
	var reply any
	if cmdLower == "find" {
		reply, err = h.handleFind(ctx, cs, client, dbName, cmdDoc, info)
	} else if cmdLower == "aggregate" {
		reply, err = h.handleAggregate(ctx, cs, client, dbName, cmdDoc, info)
	} else {
		// Default: RunCommand passthrough
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		var result bson.M
		err = client.Database(dbName).RunCommand(runCtx, cmdDoc).Decode(&result)
		if err != nil {
			telemetry.ProxyBackendErrors.WithLabelValues(tenantID).Inc()
			return command.MongoErrorToReply(err), nil
		}

		// Rewrite cursor ids in replies if present (find/aggregate via RunCommand path)
		if curVal, ok := result["cursor"]; ok {
			if rewritten, did := h.maybeRewriteCursor(cs, tenantID, curVal, dbName, info.Collection); did {
				result["cursor"] = rewritten
			}
		}
		reply = mapToD(result)
	}
	if err != nil {
		return reply, err
	}

	// Populate cache only for opt-in (_cache suffix) reads that missed the coordinator path
	if useCache && info.Kind == command.KindRead && collName != "" {
		h.maybePopulateCache(ctx, tenantID, connectionID, dbName, collName, nsLabel, cmdLower, msg.Body, info, reply)
	}

	// Optional per-connection: flush cache for the written collection after a successful write.
	if info.Kind == command.KindWrite && collName != "" && !isErrorReply(reply) {
		h.maybeAutoInvalidateOnWrite(ctx, tenantID, connectionID, dbName, collName)
	}

	return reply, nil
}

// maybeAutoInvalidateOnWrite flushes cached reads for db.coll when the connection has the flag enabled.
func (h *Handler) maybeAutoInvalidateOnWrite(ctx context.Context, tenantID, connectionID, dbName, collName string) {
	if h.deps.Cache == nil || h.deps.Store == nil {
		return
	}
	conn, err := h.deps.Store.GetConnection(ctx, connectionID)
	if err != nil || conn == nil || !conn.AutoInvalidateOnWrite {
		return
	}
	// Strip accidental _cache suffix on write targets (writes should use real names).
	realColl, _ := command.ResolveCacheCollection(collName)
	if realColl == "" {
		realColl = collName
	}
	if err := h.deps.Cache.BestEffortInvalidate(ctx, tenantID, connectionID, dbName, realColl); err != nil {
		h.deps.Log.Warn("auto-invalidate on write failed",
			"tenant", tenantID, "connection", connectionID, "db", dbName, "coll", realColl, "error", err)
		return
	}
	h.deps.Log.Debug("auto-invalidated cache after write",
		"tenant", tenantID, "connection", connectionID, "db", dbName, "coll", realColl)
}

// tryCacheRead attempts a cache hit / singleflight miss populate for find/aggregate/etc.
// Caller must only invoke this when the client opted in via the "_cache" collection suffix;
// collName is the real backend collection (suffix already stripped).
// handled=true means caller should return reply directly (even if reply is an error document).
func (h *Handler) tryCacheRead(
	ctx context.Context,
	cs *ConnState,
	tenantID, connectionID, dbName, collName, nsLabel, cmdLower string,
	msg *wire.Msg,
	info command.Info,
	cmdDoc bson.D,
) (reply any, handled bool) {
	if h.deps.Cache == nil || collName == "" {
		return nil, false
	}
	if bypass, reason := cache.ShouldBypassCache(info.Name, msg.Body, info.IsTxn); bypass {
		telemetry.CacheBypass.WithLabelValues(tenantID, reason).Inc()
		return nil, false
	}
	// Policy supplies TTL / max bytes / key version; opt-in is the _cache suffix, not Enabled.
	var dec policy.Decision
	if h.deps.Policies != nil {
		dec = h.deps.Policies.Resolve(tenantID, dbName, collName)
	} else {
		dec = policy.Decision{Enabled: true, TTL: time.Duration(policy.DefaultTTLSeconds) * time.Second, MaxResultBytes: 1 << 20, CacheKeyVersion: 1}
	}

	key, err := cache.CacheKey(tenantID, connectionID, dbName, collName, info.Name, msg.Body, dec.CacheKeyVersion)
	if err != nil {
		telemetry.CacheBypass.WithLabelValues(tenantID, "bad_key").Inc()
		return nil, false
	}

	start := time.Now()
	payload, hit, err := h.deps.Cache.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		// Execute backend inside singleflight on miss
		client, err := h.deps.Pool.Get(ctx, connectionID)
		if err != nil {
			return nil, err
		}
		defer h.deps.Pool.Release(connectionID)
		var backendReply any
		switch cmdLower {
		case "find":
			backendReply, err = h.handleFind(ctx, cs, client, dbName, cmdDoc, info)
		case "aggregate":
			backendReply, err = h.handleAggregate(ctx, cs, client, dbName, cmdDoc, info)
		default:
			runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			var result bson.M
			err = client.Database(dbName).RunCommand(runCtx, cmdDoc).Decode(&result)
			if err != nil {
				return nil, err
			}
			backendReply = mapToD(result)
		}
		if err != nil {
			return nil, err
		}
		// If backend returned an error document (ok:0), do not cache
		if isErrorReply(backendReply) {
			return nil, errBackendCommand
		}
		ns, docs, ok := cache.DocsFromCursorReply(backendReply)
		if !ok {
			// For non-cursor replies (count etc.) store minimal envelope
			raw, merr := bson.Marshal(backendReply)
			if merr != nil {
				return nil, merr
			}
			docs = []bson.Raw{raw}
			ns = nsLabel
		}
		serialized, serr := cache.Serialize(ns, cmdLower, docs)
		if serr != nil {
			return nil, serr
		}
		if len(serialized) > dec.MaxResultBytes {
			telemetry.CacheBypass.WithLabelValues(tenantID, "size").Inc()
			return nil, errTooBig
		}
		h.deps.Cache.BestEffortSet(ctx, tenantID, connectionID, dbName, collName, key, serialized, dec.TTL)
		telemetry.CacheResultBytes.WithLabelValues(tenantID).Observe(float64(len(serialized)))
		telemetry.CacheMisses.WithLabelValues(tenantID, nsLabel, cmdLower).Inc()
		if h.deps.CacheStats != nil {
			h.deps.CacheStats.RecordMiss(tenantID, dbName, collName)
		}
		return serialized, nil
	})

	if err != nil {
		if errors.Is(err, cache.ErrUnavailable) {
			telemetry.CacheUnavailable.Inc()
			return nil, false // fail open
		}
		if errors.Is(err, errTooBig) || errors.Is(err, errBackendCommand) {
			return nil, false
		}
		// backend errors while loading — fall through so normal path can surface
		return nil, false
	}

	cr, derr := cache.Deserialize(payload)
	if derr != nil {
		return nil, false
	}
	if hit {
		telemetry.CacheHits.WithLabelValues(tenantID, nsLabel, cmdLower).Inc()
		if h.deps.CacheStats != nil {
			h.deps.CacheStats.RecordHit(tenantID, dbName, collName)
		}
		telemetry.CacheLatency.WithLabelValues("hit").Observe(time.Since(start).Seconds())
	} else {
		// Miss was already counted when populate ran; ensure miss counted if load returned without that path
		telemetry.CacheLatency.WithLabelValues("miss").Observe(time.Since(start).Seconds())
	}
	if cmdLower == "count" || cmdLower == "estimateddocumentcount" || cmdLower == "distinct" {
		if len(cr.Docs) == 1 {
			var m bson.M
			if err := bson.Unmarshal(cr.Docs[0], &m); err == nil {
				return mapToD(m), true
			}
		}
	}
	// Phase 3: emulate server cursors for large cached result sets
	batchSize := int(h.deps.DefaultBatch)
	if batchSize <= 0 {
		batchSize = 101
	}
	if h.deps.CachedCursors != nil && (cmdLower == "find" || cmdLower == "aggregate") {
		cid, first, _ := h.deps.CachedCursors.Register(tenantID, cs.Key, cr.NS, cr.Docs, batchSize)
		return cache.ReplyFromCacheWithCursor(cr, cid, first), true
	}
	return cache.ReplyFromCache(cr), true
}

func (h *Handler) handleCachedGetMore(_ context.Context, cs *ConnState, body bson.Raw) (any, bool) {
	if h.deps.CachedCursors == nil || !cs.Authed || cs.Tenant == nil {
		return nil, false
	}
	cursorID := wire.LookupInt64(body, "getMore")
	if cursorID == 0 {
		cursorID = wire.LookupInt64(body, "getmore")
	}
	// Cached cursor ids are in the high range; try lookup first
	batchSize := int(wire.LookupInt32(body, "batchSize"))
	ns, docs, exhausted, ok := h.deps.CachedCursors.NextBatch(cursorID, cs.Tenant.TenantID, cs.Key, batchSize)
	if !ok {
		return nil, false
	}
	batch := make(bson.A, 0, len(docs))
	for _, d := range docs {
		var m bson.M
		if err := bson.Unmarshal(d, &m); err != nil {
			batch = append(batch, d)
			continue
		}
		batch = append(batch, m)
	}
	outID := cursorID
	if exhausted {
		outID = 0
	}
	return bson.D{
		{Key: "cursor", Value: bson.D{
			{Key: "id", Value: outID},
			{Key: "ns", Value: ns},
			{Key: "nextBatch", Value: batch},
		}},
		{Key: "ok", Value: float64(1)},
	}, true
}

func (h *Handler) handleKillCachedCursors(cs *ConnState, body bson.Raw) {
	if h.deps.CachedCursors == nil || cs.Tenant == nil {
		return
	}
	var doc bson.M
	_ = bson.Unmarshal(body, &doc)
	var ids []int64
	if arr, ok := doc["cursors"].(bson.A); ok {
		for _, v := range arr {
			switch n := v.(type) {
			case int64:
				ids = append(ids, n)
			case int32:
				ids = append(ids, int64(n))
			case float64:
				ids = append(ids, int64(n))
			}
		}
	}
	h.deps.CachedCursors.KillMany(ids, cs.Tenant.TenantID, cs.Key)
}

var (
	errTooBig         = errors.New("result too large for cache")
	errBackendCommand = errors.New("backend command error")
)

func isErrorReply(reply any) bool {
	switch r := reply.(type) {
	case bson.D:
		for _, e := range r {
			if e.Key == "ok" {
				switch v := e.Value.(type) {
				case float64:
					return v == 0
				case int32:
					return v == 0
				case int:
					return v == 0
				}
			}
		}
	case bson.M:
		if v, ok := r["ok"]; ok {
			switch n := v.(type) {
			case float64:
				return n == 0
			case int32:
				return n == 0
			}
		}
	}
	return false
}

// maybePopulateCache is used when the normal passthrough path ran (cache miss path outside coordinator).
// Caller must only invoke for _cache-suffix opt-in reads; collName is the real backend collection.
func (h *Handler) maybePopulateCache(
	ctx context.Context,
	tenantID, connectionID, dbName, collName, nsLabel, cmdLower string,
	raw bson.Raw,
	info command.Info,
	reply any,
) {
	if h.deps.Cache == nil || isErrorReply(reply) {
		return
	}
	if bypass, _ := cache.ShouldBypassCache(info.Name, raw, info.IsTxn); bypass {
		return
	}
	var dec policy.Decision
	if h.deps.Policies != nil {
		dec = h.deps.Policies.Resolve(tenantID, dbName, collName)
	} else {
		dec = policy.Decision{Enabled: true, TTL: time.Duration(policy.DefaultTTLSeconds) * time.Second, MaxResultBytes: 1 << 20, CacheKeyVersion: 1}
	}
	key, err := cache.CacheKey(tenantID, connectionID, dbName, collName, info.Name, raw, dec.CacheKeyVersion)
	if err != nil {
		return
	}
	ns, docs, ok := cache.DocsFromCursorReply(reply)
	if !ok {
		rawReply, merr := bson.Marshal(reply)
		if merr != nil {
			return
		}
		docs = []bson.Raw{rawReply}
		ns = nsLabel
	}
	serialized, err := cache.Serialize(ns, cmdLower, docs)
	if err != nil || len(serialized) > dec.MaxResultBytes {
		return
	}
	h.deps.Cache.BestEffortSet(ctx, tenantID, connectionID, dbName, collName, key, serialized, dec.TTL)
}

func mergeDocumentSequences(cmd bson.D, seqs []wire.DocumentSequence) bson.D {
	if len(seqs) == 0 {
		return cmd
	}
	// Build map of identifier -> documents
	for _, seq := range seqs {
		key := seq.Identifier
		if key == "" {
			continue
		}
		docs := make(bson.A, 0, len(seq.Documents))
		for _, raw := range seq.Documents {
			var m bson.M
			if err := bson.Unmarshal(raw, &m); err != nil {
				continue
			}
			docs = append(docs, m)
		}
		// Replace or set field on command
		found := false
		for i, e := range cmd {
			if e.Key == key {
				cmd[i].Value = docs
				found = true
				break
			}
		}
		if !found {
			cmd = append(cmd, bson.E{Key: key, Value: docs})
		}
	}
	return cmd
}

func (h *Handler) handleFind(ctx context.Context, cs *ConnState, client *mongo.Client, dbName string, cmd bson.D, info command.Info) (any, error) {
	collName := info.Collection
	if collName == "" {
		collName = fieldString(cmd, "find")
	}
	filter := fieldDoc(cmd, "filter")
	if filter == nil {
		filter = bson.D{}
	}

	opts := options.Find()
	if proj := fieldDoc(cmd, "projection"); proj != nil {
		opts.SetProjection(proj)
	}
	if sort := fieldDoc(cmd, "sort"); sort != nil {
		opts.SetSort(sort)
	}
	if skip, ok := fieldInt64(cmd, "skip"); ok {
		opts.SetSkip(skip)
	}
	if limit, ok := fieldInt64(cmd, "limit"); ok && limit > 0 {
		opts.SetLimit(limit)
	}
	batchSize := int32(101)
	if bs, ok := fieldInt64(cmd, "batchSize"); ok && bs > 0 {
		batchSize = int32(bs)
		opts.SetBatchSize(batchSize)
	}
	if _, ok := fieldBool(cmd, "singleBatch"); ok {
		// handled after first batch
	}

	// Pass through lsid/txn via RunCommand if in a transaction — fall back to RunCommand
	if info.IsTxn || hasSessionFields(cmd) {
		return h.runCommandRaw(ctx, cs, client, dbName, cmd, info)
	}

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cur, err := client.Database(dbName).Collection(collName).Find(runCtx, filter, opts)
	if err != nil {
		return command.MongoErrorToReply(err), nil
	}

	return h.firstBatchFromCursor(ctx, cs, cur, dbName, collName, batchSize, fieldBoolTrue(cmd, "singleBatch"))
}

func (h *Handler) handleAggregate(ctx context.Context, cs *ConnState, client *mongo.Client, dbName string, cmd bson.D, info command.Info) (any, error) {
	collName := info.Collection
	if collName == "" {
		collName = fieldString(cmd, "aggregate")
	}
	// Collection-less aggregate (db-level) uses aggregate:1
	pipeline := fieldArray(cmd, "pipeline")
	if pipeline == nil {
		pipeline = bson.A{}
	}

	if info.IsTxn || hasSessionFields(cmd) {
		return h.runCommandRaw(ctx, cs, client, dbName, cmd, info)
	}

	opts := options.Aggregate()
	if bs, ok := fieldInt64(cmd, "cursor"); ok {
		// cursor may be a subdocument { batchSize: N }
		_ = bs
	}
	// Extract batchSize from cursor subdoc
	batchSize := int32(101)
	if curOpt := fieldDoc(cmd, "cursor"); curOpt != nil {
		for _, e := range curOpt {
			if e.Key == "batchSize" {
				switch v := e.Value.(type) {
				case int32:
					batchSize = v
				case int64:
					batchSize = int32(v)
				case int:
					batchSize = int32(v)
				}
			}
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var cur *mongo.Cursor
	var err error
	if collName == "1" || collName == "" {
		// db-level aggregate not easily supported via collection; RunCommand
		return h.runCommandRaw(ctx, cs, client, dbName, cmd, info)
	}
	cur, err = client.Database(dbName).Collection(collName).Aggregate(runCtx, pipeline, opts)
	if err != nil {
		return command.MongoErrorToReply(err), nil
	}
	return h.firstBatchFromCursor(ctx, cs, cur, dbName, collName, batchSize, false)
}

func (h *Handler) runCommandRaw(ctx context.Context, cs *ConnState, client *mongo.Client, dbName string, cmd bson.D, info command.Info) (any, error) {
	tenantID := cs.Tenant.TenantID
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	var result bson.M
	err := client.Database(dbName).RunCommand(runCtx, cmd).Decode(&result)
	if err != nil {
		telemetry.ProxyBackendErrors.WithLabelValues(tenantID).Inc()
		return command.MongoErrorToReply(err), nil
	}
	if curVal, ok := result["cursor"]; ok {
		if rewritten, did := h.maybeRewriteCursor(cs, tenantID, curVal, dbName, info.Collection); did {
			result["cursor"] = rewritten
		}
	}
	return mapToD(result), nil
}

func (h *Handler) firstBatchFromCursor(ctx context.Context, cs *ConnState, cur *mongo.Cursor, dbName, collName string, batchSize int32, singleBatch bool) (any, error) {
	tenantID := cs.Tenant.TenantID
	ns := command.FormatNS(dbName, collName)

	if batchSize <= 0 {
		batchSize = 101
	}
	firstBatch := make(bson.A, 0, batchSize)

	for int32(len(firstBatch)) < batchSize && cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			_ = cur.Close(ctx)
			return command.MongoErrorToReply(err), nil
		}
		firstBatch = append(firstBatch, doc)
	}
	if err := cur.Err(); err != nil {
		_ = cur.Close(ctx)
		return command.MongoErrorToReply(err), nil
	}

	// Keep cursor open only when the batch is full (more data likely) and not singleBatch.
	var cursorID int64
	if !singleBatch && int32(len(firstBatch)) >= batchSize {
		cursorID = h.deps.Cursors.Register(tenantID, cs.Key, ns, cur)
	} else {
		_ = cur.Close(ctx)
		cursorID = 0
	}

	return bson.D{
		{Key: "cursor", Value: bson.D{
			{Key: "id", Value: cursorID},
			{Key: "ns", Value: ns},
			{Key: "firstBatch", Value: firstBatch},
		}},
		{Key: "ok", Value: float64(1)},
	}, nil
}

func (h *Handler) handleGetMore(ctx context.Context, cs *ConnState, body bson.Raw) (any, error) {
	if !cs.Authed || cs.Tenant == nil {
		return command.NotAuthorized(""), nil
	}
	cursorID := wire.LookupInt64(body, "getMore")
	if cursorID == 0 {
		// try lowercase key from command name extraction — getMore field
		cursorID = wire.LookupInt64(body, "getmore")
	}
	batchSize := wire.LookupInt32(body, "batchSize")
	if batchSize <= 0 {
		batchSize = 101
	}

	st, ok := h.deps.Cursors.Get(cursorID, cs.Tenant.TenantID, cs.Key)
	if !ok {
		return command.ErrorReply(43, "CursorNotFound", "cursor id not found"), nil
	}

	nextBatch := make(bson.A, 0, batchSize)
	remaining := int(batchSize)
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for remaining > 0 && st.Cursor.Next(runCtx) {
		var doc bson.M
		if err := st.Cursor.Decode(&doc); err != nil {
			h.deps.Cursors.Remove(cursorID, cs.Tenant.TenantID, cs.Key)
			return command.MongoErrorToReply(err), nil
		}
		nextBatch = append(nextBatch, doc)
		remaining--
	}
	if err := st.Cursor.Err(); err != nil {
		h.deps.Cursors.Remove(cursorID, cs.Tenant.TenantID, cs.Key)
		return command.MongoErrorToReply(err), nil
	}

	outID := cursorID
	// If we got fewer than requested, cursor is likely exhausted
	if int32(len(nextBatch)) < batchSize {
		h.deps.Cursors.Remove(cursorID, cs.Tenant.TenantID, cs.Key)
		outID = 0
	}

	return bson.D{
		{Key: "cursor", Value: bson.D{
			{Key: "id", Value: outID},
			{Key: "ns", Value: st.NS},
			{Key: "nextBatch", Value: nextBatch},
		}},
		{Key: "ok", Value: float64(1)},
	}, nil
}

func (h *Handler) handleKillCursors(cs *ConnState, body bson.Raw) any {
	if !cs.Authed || cs.Tenant == nil {
		return command.NotAuthorized("")
	}
	// killCursors: { killCursors: coll, cursors: [id1, id2], $db: ... }
	var doc bson.M
	_ = bson.Unmarshal(body, &doc)
	var ids []int64
	if arr, ok := doc["cursors"].(bson.A); ok {
		for _, v := range arr {
			switch n := v.(type) {
			case int64:
				ids = append(ids, n)
			case int32:
				ids = append(ids, int64(n))
			case float64:
				ids = append(ids, int64(n))
			}
		}
	}
	h.deps.Cursors.KillMany(ids, cs.Tenant.TenantID, cs.Key)
	return bson.D{
		{Key: "cursorsKilled", Value: int64SliceToA(ids)},
		{Key: "cursorsNotFound", Value: bson.A{}},
		{Key: "cursorsAlive", Value: bson.A{}},
		{Key: "cursorsUnknown", Value: bson.A{}},
		{Key: "ok", Value: float64(1)},
	}
}

// maybeRewriteCursor replaces backend cursor id with our registered id.
// For RunCommand replies where the backend already returns a cursor, we cannot easily
// iterate the backend cursor without the driver's cursor object. In that case we pass
// through the backend cursor id only works if the client talks to the same server —
// it doesn't. So we only rewrite when we own the cursor via our registry (find/aggregate helpers).
// For pure RunCommand path, attempt to fully drain small batches is not implemented; pass through
// and hope session-bound cursors work on same backend connection — they don't across connections.
// Phase 1 limitation: prefer find/aggregate helpers for cursor safety.
func (h *Handler) maybeRewriteCursor(cs *ConnState, tenantID string, curVal any, dbName, coll string) (any, bool) {
	// Without a *mongo.Cursor we cannot manage getMore. Return as-is and document limitation.
	// Drivers using RunCommand for find are rare; official drivers use OP_MSG find which we handle.
	return curVal, false
}

// --- small BSON field helpers ---

func fieldString(cmd bson.D, key string) string {
	for _, e := range cmd {
		if e.Key == key {
			if s, ok := e.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// setFieldString sets or appends a string field on a command document (e.g. find/aggregate collection).
func setFieldString(cmd bson.D, key, value string) bson.D {
	for i, e := range cmd {
		if e.Key == key {
			cmd[i].Value = value
			return cmd
		}
	}
	return append(cmd, bson.E{Key: key, Value: value})
}

func fieldDoc(cmd bson.D, key string) bson.D {
	for _, e := range cmd {
		if e.Key == key {
			switch v := e.Value.(type) {
			case bson.D:
				return v
			case bson.M:
				d := make(bson.D, 0, len(v))
				for k, val := range v {
					d = append(d, bson.E{Key: k, Value: val})
				}
				return d
			}
		}
	}
	return nil
}

func fieldArray(cmd bson.D, key string) bson.A {
	for _, e := range cmd {
		if e.Key == key {
			if a, ok := e.Value.(bson.A); ok {
				return a
			}
		}
	}
	return nil
}

func fieldInt64(cmd bson.D, key string) (int64, bool) {
	for _, e := range cmd {
		if e.Key == key {
			switch v := e.Value.(type) {
			case int32:
				return int64(v), true
			case int64:
				return v, true
			case int:
				return int64(v), true
			case float64:
				return int64(v), true
			}
		}
	}
	return 0, false
}

func fieldBool(cmd bson.D, key string) (bool, bool) {
	for _, e := range cmd {
		if e.Key == key {
			if b, ok := e.Value.(bool); ok {
				return b, true
			}
		}
	}
	return false, false
}

func fieldBoolTrue(cmd bson.D, key string) bool {
	b, ok := fieldBool(cmd, key)
	return ok && b
}

func hasSessionFields(cmd bson.D) bool {
	for _, e := range cmd {
		if e.Key == "lsid" || e.Key == "txnNumber" {
			return true
		}
	}
	return false
}

func mapToD(m bson.M) bson.D {
	d := make(bson.D, 0, len(m))
	// Preserve ok last-ish; order doesn't matter much for replies
	if v, ok := m["ok"]; ok {
		d = append(d, bson.E{Key: "ok", Value: v})
		delete(m, "ok")
	}
	for k, v := range m {
		d = append(d, bson.E{Key: k, Value: v})
	}
	return d
}

func int64SliceToA(ids []int64) bson.A {
	a := make(bson.A, len(ids))
	for i, id := range ids {
		a[i] = id
	}
	return a
}
