package service

import (
	"context"
	"strings"
	"testing"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/model"
)

func TestTenantService_CRUD(t *testing.T) {
	ms := store.NewMemoryStore()
	svc := NewTenantService(ms)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "", "x"); err == nil {
		t.Fatal("empty id")
	}
	ten, err := svc.Create(ctx, "t1", "Tenant")
	if err != nil || ten.ID != "t1" {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, "t1")
	if err != nil || got.Name != "Tenant" {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "missing"); err != ErrTenantNotFound {
		t.Fatalf("got %v", err)
	}
	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%d %v", len(list), err)
	}
}

func TestPolicyService_DefaultsAndCollection(t *testing.T) {
	ms := store.NewMemoryStore()
	svc := NewPolicyService(ms)
	ctx := context.Background()
	if err := svc.SetDefaults(ctx, "t1", 30); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Get(ctx, "t1")
	if err != nil || p.DefaultTtlSeconds != 30 {
		t.Fatalf("%+v %v", p, err)
	}
	pol := model.CollectionPolicy{Enabled: true, TTLSeconds: 10}
	if err := svc.SetCollectionPolicy(ctx, "t1", "db.c", pol); err != nil {
		t.Fatal(err)
	}
	p2, _ := svc.Get(ctx, "t1")
	if p2.Collections["db.c"].TTLSeconds != 10 {
		t.Fatalf("%+v", p2.Collections)
	}
	if err := svc.Invalidate(ctx, "t1", "db", "c", nil); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionAndTokenService(t *testing.T) {
	ms := store.NewMemoryStore()
	ctx := context.Background()
	_, _ = NewTenantService(ms).Create(ctx, "t1", "Tenant")

	// Minimal crypto: use empty Config will fail encrypt — seed connection via store directly.
	now := ctx
	_ = now
	c := &model.Connection{
		ID: "conn_a", TenantID: "t1", Name: "prod",
		URICiphertext: []byte("ct"), Nonce: []byte("n"), DEKVersion: "v1",
	}
	if err := ms.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	c2 := &model.Connection{
		ID: "conn_b", TenantID: "t1", Name: "staging",
		URICiphertext: []byte("ct2"), Nonce: []byte("n2"), DEKVersion: "v1",
	}
	if err := ms.CreateConnection(ctx, c2); err != nil {
		t.Fatal(err)
	}
	list, err := ms.ListConnections(ctx, "t1")
	if err != nil || len(list) != 2 {
		t.Fatalf("%d %v", len(list), err)
	}

	svc := NewTokenService(ms).WithProxyPublicEndpoint("proxy.example.com:27018")
	if _, err := svc.Issue(ctx, "t1", "missing", "x"); err != ErrConnectionNotFound {
		t.Fatalf("want not found, got %v", err)
	}
	issued, err := svc.Issue(ctx, "t1", "conn_a", "ci")
	if err != nil || issued.RawToken == "" || issued.Token.ConnectionID != "conn_a" {
		t.Fatal(err, issued)
	}
	if !strings.Contains(issued.ProxyConnectionURI, "proxy.example.com:27018") {
		t.Fatalf("uri: %s", issued.ProxyConnectionURI)
	}
	issuedB, err := svc.Issue(ctx, "t1", "conn_b", "other")
	if err != nil || issuedB.Token.ConnectionID != "conn_b" {
		t.Fatal(err)
	}
	listA, err := svc.ListForConnection(ctx, "t1", "conn_a")
	if err != nil || len(listA) != 1 {
		t.Fatalf("%d %v", len(listA), err)
	}
	if err := svc.Revoke(ctx, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := ms.GetTokenByID(ctx, issued.Token.ID)
	if got.RevokedAt == nil {
		t.Fatal("expected revoked")
	}
}

func TestBuildProxyConnectionURI(t *testing.T) {
	uri := BuildProxyConnectionURI("127.0.0.1:27018", "demo", "secret+token")
	if !strings.HasPrefix(uri, "mongodb://") {
		t.Fatalf("scheme: %s", uri)
	}
	if !strings.Contains(uri, "authSource") {
		t.Fatalf("missing authSource: %s", uri)
	}
	uri2 := BuildProxyConnectionURI("", "org", "tok")
	if !strings.Contains(uri2, "127.0.0.1:27018") {
		t.Fatalf("default host: %s", uri2)
	}
}
