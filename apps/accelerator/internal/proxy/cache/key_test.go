package cache

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func mustRaw(t *testing.T, doc bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCacheKey_KeyOrderIndependent(t *testing.T) {
	a := mustRaw(t, bson.D{
		{Key: "find", Value: "orders"},
		{Key: "filter", Value: bson.D{{Key: "status", Value: "shipped"}, {Key: "region", Value: "us"}}},
		{Key: "$db", Value: "mydb"},
	})
	b := mustRaw(t, bson.D{
		{Key: "find", Value: "orders"},
		{Key: "filter", Value: bson.D{{Key: "region", Value: "us"}, {Key: "status", Value: "shipped"}}},
		{Key: "$db", Value: "mydb"},
	})
	ka, err := CacheKey("t1", "c1", "mydb", "orders", "find", a, 1)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := CacheKey("t1", "c1", "mydb", "orders", "find", b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("expected same key for reordered filter keys\n%s\n%s", ka, kb)
	}
}

func TestCacheKey_CommentIgnored(t *testing.T) {
	base := bson.D{
		{Key: "find", Value: "c"},
		{Key: "filter", Value: bson.D{{Key: "a", Value: 1}}},
		{Key: "$db", Value: "d"},
	}
	withComment := append(base, bson.E{Key: "$comment", Value: "trace-xyz"})
	ka, _ := CacheKey("t", "c1", "d", "c", "find", mustRaw(t, base), 0)
	kb, _ := CacheKey("t", "c1", "d", "c", "find", mustRaw(t, withComment), 0)
	if ka != kb {
		t.Fatalf("comment must not affect key")
	}
}

func TestCacheKey_LimitAffectsKey(t *testing.T) {
	a := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "limit", Value: int32(10)}, {Key: "$db", Value: "d"}})
	b := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "limit", Value: int32(20)}, {Key: "$db", Value: "d"}})
	ka, _ := CacheKey("t", "c1", "d", "c", "find", a, 1)
	kb, _ := CacheKey("t", "c1", "d", "c", "find", b, 1)
	if ka == kb {
		t.Fatal("different limits must produce different keys")
	}
}

func TestCacheKey_TenantIsolation(t *testing.T) {
	raw := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "$db", Value: "d"}})
	ka, _ := CacheKey("tenantA", "c1", "d", "c", "find", raw, 1)
	kb, _ := CacheKey("tenantB", "c1", "d", "c", "find", raw, 1)
	if ka == kb {
		t.Fatal("tenants must not share keys")
	}
	if !strings.Contains(ka, "tenant:{tenantA}") {
		t.Fatalf("expected hash tag in key: %s", ka)
	}
}

func TestCacheKey_VersionInKey(t *testing.T) {
	raw := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "$db", Value: "d"}})
	ka, _ := CacheKey("t", "c1", "d", "c", "find", raw, 1)
	kb, _ := CacheKey("t", "c1", "d", "c", "find", raw, 2)
	if ka == kb {
		t.Fatal("cache key version must change key")
	}
}

func TestShouldBypassCache_Txn(t *testing.T) {
	raw := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "txnNumber", Value: int64(1)}})
	if ok, reason := ShouldBypassCache("find", raw, false); !ok || reason != "transaction" {
		t.Fatalf("expected txn bypass, got %v %s", ok, reason)
	}
}

func TestShouldBypassCache_AggOut(t *testing.T) {
	raw := mustRaw(t, bson.D{
		{Key: "aggregate", Value: "c"},
		{Key: "pipeline", Value: bson.A{bson.D{{Key: "$out", Value: "other"}}}},
		{Key: "$db", Value: "d"},
	})
	if ok, reason := ShouldBypassCache("aggregate", raw, false); !ok || reason != "agg_stage" {
		t.Fatalf("expected agg_stage bypass, got %v %s", ok, reason)
	}
}

func TestIsCacheableCommand(t *testing.T) {
	if !IsCacheableCommand("find") || !IsCacheableCommand("aggregate") {
		t.Fatal("reads should be cacheable")
	}
	if IsCacheableCommand("insert") {
		t.Fatal("writes must not be cacheable")
	}
}

func TestNormalize_Int32VsInt64(t *testing.T) {
	a := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "limit", Value: int32(5)}})
	b := mustRaw(t, bson.D{{Key: "find", Value: "c"}, {Key: "limit", Value: int64(5)}})
	ka, _ := CacheKey("t", "c1", "d", "c", "find", a, 0)
	kb, _ := CacheKey("t", "c1", "d", "c", "find", b, 0)
	if ka != kb {
		t.Fatal("int32 and int64 limits should normalize to same key")
	}
}
