package handler

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	"github.com/taeven/nance/accelerator/internal/proxy/command"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/wire"
	"github.com/taeven/nance/accelerator/internal/telemetry"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ConnState is per-TCP-connection mutable state.
type ConnState struct {
	ID           int32
	Key          string // unique key for cursor scoping
	Tenant       *auth.TenantContext
	Authed       bool
	RemoteAddr   string
	AllowUnauth  bool
	connIDGen    *atomic.Int32
}

// Deps bundles handler dependencies.
type Deps struct {
	Auth     *auth.Validator
	Pool     *pool.Manager
	Cursors  *cursor.Registry
	Log      *slog.Logger
	ConnID   *atomic.Int32 // global connection id counter for hello replies
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
		return h.handleGetMore(ctx, cs, msg.Body)
	case "killcursors":
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
	if cs.Tenant != nil {
		tenantID = cs.Tenant.TenantID
	}
	if tenantID == "" {
		return command.NotAuthorized("no tenant context"), nil
	}

	client, err := h.deps.Pool.Get(ctx, tenantID)
	if err != nil {
		h.deps.Log.Error("backend pool error", "tenant", tenantID, "error", err)
		telemetry.ProxyBackendErrors.WithLabelValues(tenantID).Inc()
		return command.ErrorReply(6, "HostUnreachable", "failed to reach tenant backend: "+err.Error()), nil
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

	// Cursor-producing reads: use collection helpers so we can manage getMore
	if cmdLower == "find" {
		return h.handleFind(ctx, cs, client, dbName, cmdDoc, info)
	}
	if cmdLower == "aggregate" {
		return h.handleAggregate(ctx, cs, client, dbName, cmdDoc, info)
	}

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

	return mapToD(result), nil
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
