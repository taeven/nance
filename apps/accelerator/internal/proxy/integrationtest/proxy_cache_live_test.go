//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/crypto"
	"github.com/taeven/nance/accelerator/internal/model"
	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
	"github.com/taeven/nance/accelerator/internal/proxy/cachedcursor"
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	proxyconfig "github.com/taeven/nance/accelerator/internal/proxy/config"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/policy"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/ratelimit"
	"github.com/taeven/nance/accelerator/internal/proxy/server"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"golang.org/x/crypto/bcrypt"
)

// Live integration against localhost Mongo (:27017) and Redis (:6379).
//
//	cd apps/accelerator
//	go test -tags=integration -count=1 ./internal/proxy/integrationtest/ -v -timeout 2m

const (
	testTenantID = "itest"
	testConnID   = "conn_itest"
	testTokenRaw = "integration-test-token-secret-001"
	realMongoURI = "mongodb://root:example@127.0.0.1:27017/?authSource=admin"
	testDB       = "nance_itest"
	testColl     = "orders"
	redisDB      = 15 // isolate from other local redis data
)

func requireLocalServices(t *testing.T) {
	t.Helper()
	for _, addr := range []string{"127.0.0.1:27017", "127.0.0.1:6379"} {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Skipf("local service %s not available: %v (start with: cd apps/accelerator && make dev-up)", addr, err)
		}
		_ = c.Close()
	}
}

func freeListenAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestProxyCache_LiveMongoRedis(t *testing.T) {
	requireLocalServices(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- Seed backend Mongo directly ---
	backend, err := mongo.Connect(ctx, options.Client().ApplyURI(realMongoURI))
	if err != nil {
		t.Fatalf("connect backend mongo: %v", err)
	}
	defer backend.Disconnect(context.Background())
	if err := backend.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("ping backend: %v", err)
	}
	bdb := backend.Database(testDB)
	_ = bdb.Collection(testColl).Drop(ctx)
	seedDocs := []any{
		bson.M{"_id": "o1", "status": "open", "n": 1},
		bson.M{"_id": "o2", "status": "open", "n": 2},
		bson.M{"_id": "o3", "status": "closed", "n": 3},
	}
	if _, err := bdb.Collection(testColl).InsertMany(ctx, seedDocs); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		_ = bdb.Collection(testColl).Drop(context.Background())
	})

	// --- Control-plane state in memory ---
	ms := store.NewMemoryStore()
	now := time.Now().UTC()
	if err := ms.CreateTenant(ctx, &model.Tenant{
		ID: testTenantID, Name: "Integration", Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	masterKey := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	crypt := &crypto.Config{MasterKey: masterKey}
	ct, nonce, dek, err := crypt.Encrypt([]byte(realMongoURI), testTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.CreateConnection(ctx, &model.Connection{
		ID: testConnID, TenantID: testTenantID, Name: "local",
		URICiphertext: ct, Nonce: nonce, DEKVersion: dek,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ms.UpsertCachePolicy(ctx, &model.CachePolicy{
		ConnectionID: testConnID, TenantID: testTenantID,
		DefaultTtlSeconds: 120, CacheKeyVersion: 1,
		Collections: map[string]model.CollectionPolicy{},
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(testTokenRaw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.CreateToken(ctx, &model.Token{
		ID: "tok_itest", TenantID: testTenantID, ConnectionID: testConnID,
		Description: "itest", CreatedAt: now,
	}, string(hash)); err != nil {
		t.Fatal(err)
	}

	// --- Redis on dedicated logical DB ---
	rs, err := cache.NewRedisStore(ctx, cache.Options{Addr: "127.0.0.1:6379", DB: redisDB})
	if err != nil {
		t.Fatalf("redis: %v", err)
	}
	defer rs.Close()
	if err := rs.Ping(ctx); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	coord := cache.NewCoordinator(rs)
	pol := policy.NewEngine(ms, slog.Default(), time.Hour)
	pol.SetForTest(testConnID, &model.CachePolicy{
		ConnectionID: testConnID, TenantID: testTenantID,
		DefaultTtlSeconds: 120, CacheKeyVersion: 1,
		Collections: map[string]model.CollectionPolicy{},
	})

	statsTracker := cachestats.NewTracker()
	listen := freeListenAddr(t)
	pcfg := &proxyconfig.Config{
		ListenAddr:            listen,
		HealthAddr:            freeListenAddr(t),
		MaxConnsPerTenant:     50,
		BackendMaxPoolSize:    10,
		BackendConnectTimeout: 10 * time.Second,
		CursorIdleTimeout:     5 * time.Minute,
		CacheEnabled:          true,
		RedisAddr:             "127.0.0.1:6379",
		RedisDB:               redisDB,
		PolicyRefreshInterval: time.Hour,
		TenantQPS:             100000,
		TenantBurst:           100000,
		CachedCursorMaxBytes:  64 << 20,
		DrainTimeout:          5 * time.Second,
	}

	validator := auth.NewValidator(ms)
	pools := pool.NewManager(ms, crypt, pcfg, slog.Default())
	pools.StartIdleEviction(ctx)
	defer pools.Stop()
	cursors := cursor.NewRegistry(5 * time.Minute)
	cachedCursors := cachedcursor.NewStore(5*time.Minute, 64<<20)
	limiter := ratelimit.New(pcfg.TenantQPS, pcfg.TenantBurst)

	srv := server.New(pcfg, slog.Default(), validator, pools, cursors, server.Options{
		Cache:         coord,
		Policies:      pol,
		CacheStats:    statsTracker,
		CachedCursors: cachedCursors,
		Limiter:       limiter,
		Store:         ms,
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()
	// Wait until listening
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", listen, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	proxyURI := fmt.Sprintf(
		"mongodb://%s:%s@%s/%s?authMechanism=PLAIN&authSource=$external&directConnection=true",
		testTenantID, testTokenRaw, listen, testDB,
	)
	t.Logf("proxy listen=%s tenant=%s", listen, testTenantID)

	// --- Client via proxy ---
	cli, err := mongo.Connect(ctx, options.Client().
		ApplyURI(proxyURI).
		SetDirect(true).
		SetRetryWrites(false).
		SetRetryReads(false))
	if err != nil {
		t.Fatalf("proxy connect: %v", err)
	}
	defer cli.Disconnect(context.Background())
	if err := cli.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("proxy ping: %v", err)
	}

	// 1) Bypass read (real collection) — use Find (CountDocuments can hit session/lsid edge cases on the proxy)
	bypassColl := cli.Database(testDB).Collection(testColl)
	var bypassOpen []bson.M
	bcur, err := bypassColl.Find(ctx, bson.M{"status": "open"})
	if err != nil {
		t.Fatalf("bypass find: %v", err)
	}
	if err := bcur.All(ctx, &bypassOpen); err != nil {
		t.Fatalf("bypass decode: %v", err)
	}
	_ = bcur.Close(ctx)
	if len(bypassOpen) != 2 {
		t.Fatalf("bypass open count=%d want 2", len(bypassOpen))
	}
	t.Logf("bypass Find(status=open) = %d docs", len(bypassOpen))

	// 2) Cache path: first find = miss, second = hit
	cacheColl := cli.Database(testDB).Collection(testColl + "_cache")
	filter := bson.M{"status": "open"}

	var first []bson.M
	cur, err := cacheColl.Find(ctx, filter)
	if err != nil {
		t.Fatalf("cache find #1: %v", err)
	}
	if err := cur.All(ctx, &first); err != nil {
		t.Fatalf("cache decode #1: %v", err)
	}
	_ = cur.Close(ctx)
	if len(first) != 2 {
		t.Fatalf("cache find #1 got %d docs want 2", len(first))
	}
	t.Logf("cache find #1 returned %d docs", len(first))

	time.Sleep(150 * time.Millisecond)

	var second []bson.M
	cur2, err := cacheColl.Find(ctx, filter)
	if err != nil {
		t.Fatalf("cache find #2: %v", err)
	}
	if err := cur2.All(ctx, &second); err != nil {
		t.Fatalf("cache decode #2: %v", err)
	}
	_ = cur2.Close(ctx)
	if len(second) != 2 {
		t.Fatalf("cache find #2 got %d docs want 2", len(second))
	}
	t.Logf("cache find #2 returned %d docs", len(second))

	snap := statsTracker.SnapshotTenant(testTenantID)
	collSnap := statsTracker.SnapshotCollection(testTenantID, testDB, testColl)
	t.Logf("tenant cache stats: hits=%d misses=%d ratio=%.2f", snap.Hits, snap.Misses, snap.HitRatio)
	t.Logf("collection %s.%s stats: hits=%d misses=%d", testDB, testColl, collSnap.Hits, collSnap.Misses)

	if snap.Misses < 1 && collSnap.Misses < 1 {
		t.Fatalf("expected at least 1 cache miss, got tenant hits=%d misses=%d", snap.Hits, snap.Misses)
	}
	if snap.Hits < 1 && collSnap.Hits < 1 {
		t.Fatalf("expected at least 1 cache hit after second find on %s_cache (tenant hits=%d misses=%d)",
			testColl, snap.Hits, snap.Misses)
	}

	// 3) Backend update visible via proxy bypass (not via stale cache — we only check bypass)
	_, err = bdb.Collection(testColl).UpdateOne(ctx, bson.M{"_id": "o1"}, bson.M{"$set": bson.M{"n": 99}})
	if err != nil {
		t.Fatalf("backend update: %v", err)
	}
	var viaProxy bson.M
	if err := bypassColl.FindOne(ctx, bson.M{"_id": "o1"}).Decode(&viaProxy); err != nil {
		t.Fatalf("proxy bypass findOne: %v", err)
	}
	if !numEq(viaProxy["n"], 99) {
		t.Fatalf("proxy bypass n=%v (%T) want 99", viaProxy["n"], viaProxy["n"])
	}
	t.Logf("proxy bypass saw updated n=%v", viaProxy["n"])

	t.Log("PASS: auth, bypass read, cache miss+hit, proxy consistency OK")
	cancel()
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
	}
}

func numEq(v any, want int64) bool {
	switch x := v.(type) {
	case int32:
		return int64(x) == want
	case int64:
		return x == want
	case float64:
		return int64(x) == want
	case int:
		return int64(x) == want
	default:
		return false
	}
}
