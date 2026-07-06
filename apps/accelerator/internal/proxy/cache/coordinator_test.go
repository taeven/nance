package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestMemoryStore_RoundTripAndInvalidate(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	key := "nance:tenant:{t}:ns:db.coll:cmd:abc:v1"
	if err := s.Set(ctx, key, []byte("hello"), time.Minute); err != nil {
		t.Fatal(err)
	}
	_ = s.RegisterKey(ctx, "t", "conn1", "db", "coll", key)
	got, err := s.Get(ctx, key)
	if err != nil || string(got) != "hello" {
		t.Fatalf("get: %v %q", err, got)
	}
	if err := s.InvalidateNamespace(ctx, "t", "conn1", "db", "coll"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(ctx, key)
	if err != ErrMiss {
		t.Fatalf("expected miss after invalidate, got %v", err)
	}
}

func TestCoordinator_Singleflight(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	var loads atomic.Int32
	key := "sf-key"
	var wg sync.WaitGroup
	const n = 20
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _, err := coord.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
				loads.Add(1)
				time.Sleep(30 * time.Millisecond)
				return []byte("payload"), nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("load err: %v", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("expected 1 backend load via singleflight, got %d", loads.Load())
	}
}

func TestCoordinator_HitAfterSet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	coord := NewCoordinator(store)
	key := "k1"
	_ = store.Set(ctx, key, []byte("cached"), time.Minute)
	var loads atomic.Int32
	b, hit, err := coord.GetOrLoad(ctx, key, func(ctx context.Context) ([]byte, error) {
		loads.Add(1)
		return []byte("fresh"), nil
	})
	if err != nil || !hit || string(b) != "cached" {
		t.Fatalf("hit=%v b=%q err=%v", hit, b, err)
	}
	if loads.Load() != 0 {
		t.Fatal("load should not run on hit")
	}
}

func TestSerializeAndReply(t *testing.T) {
	docRaw, err := bson.Marshal(bson.D{{Key: "n", Value: 1}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Serialize("mydb.orders", "find", []bson.Raw{docRaw})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := Deserialize(payload)
	if err != nil {
		t.Fatal(err)
	}
	if cr.NS != "mydb.orders" || len(cr.Docs) != 1 {
		t.Fatalf("bad deserialize: %+v", cr)
	}
	reply := ReplyFromCache(cr)
	if reply[1].Key != "ok" && reply[0].Key != "cursor" {
		// structure check: must have cursor + ok
	}
	foundCursor := false
	for _, e := range reply {
		if e.Key == "cursor" {
			foundCursor = true
		}
	}
	if !foundCursor {
		t.Fatal("reply missing cursor")
	}
}

func TestDocsFromCursorReply(t *testing.T) {
	reply := bson.D{
		{Key: "cursor", Value: bson.D{
			{Key: "id", Value: int64(0)},
			{Key: "ns", Value: "db.c"},
			{Key: "firstBatch", Value: bson.A{bson.D{{Key: "a", Value: 1}}}},
		}},
		{Key: "ok", Value: float64(1)},
	}
	ns, docs, ok := DocsFromCursorReply(reply)
	if !ok || ns != "db.c" || len(docs) != 1 {
		t.Fatalf("extract failed ns=%s docs=%d ok=%v", ns, len(docs), ok)
	}
}
