package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	_, _ = NewTenantService(ms).Create(ctx, "t1", "Tenant")
	_ = ms.CreateConnection(ctx, &model.Connection{
		ID: "conn1", TenantID: "t1", Name: "prod",
		URICiphertext: []byte("x"), Nonce: []byte("n"), DEKVersion: "v1",
	})
	if err := svc.SetDefaults(ctx, "t1", "conn1", 30); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Get(ctx, "t1", "conn1")
	if err != nil || p.DefaultTtlSeconds != 30 || p.ConnectionID != "conn1" {
		t.Fatalf("%+v %v", p, err)
	}
	pol := model.CollectionPolicy{Enabled: true, TTLSeconds: 10}
	if err := svc.SetCollectionPolicy(ctx, "t1", "conn1", "db.c", pol); err != nil {
		t.Fatal(err)
	}
	p2, _ := svc.Get(ctx, "t1", "conn1")
	if p2.Collections["db.c"].TTLSeconds != 10 {
		t.Fatalf("%+v", p2.Collections)
	}
	if err := svc.Invalidate(ctx, "t1", "conn1", "db", "c", nil); err != nil {
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

	// Within default re-enable window: can re-enable
	listRevoked, err := svc.ListForConnection(ctx, "t1", "conn_a")
	if err != nil || len(listRevoked) != 1 {
		t.Fatalf("%d %v", len(listRevoked), err)
	}
	if listRevoked[0].ReenableUntil == nil {
		t.Fatal("expected reenableUntil while in window")
	}
	restored, err := svc.Reenable(ctx, issued.Token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RevokedAt != nil {
		t.Fatal("expected re-enabled (revoked_at cleared)")
	}
	active, err := ms.ListActiveTokenHashes(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range active {
		if row.ID == issued.Token.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("re-enabled token should be active for proxy auth")
	}
}

func TestTokenReenableWindow(t *testing.T) {
	ctx := context.Background()
	ms := store.NewMemoryStore()
	if err := ms.CreateTenant(ctx, &model.Tenant{ID: "t1", Name: "T", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := ms.CreateConnection(ctx, &model.Connection{
		ID: "conn_a", TenantID: "t1", Name: "prod",
		URICiphertext: []byte("ct"), Nonce: []byte("n"), DEKVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}

	// Disabled window
	svcOff := NewTokenService(ms).WithReenableWindow(0)
	issued, err := svcOff.Issue(ctx, "t1", "conn_a", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := svcOff.Revoke(ctx, issued.Token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svcOff.Reenable(ctx, issued.Token.ID); !errors.Is(err, ErrReenableDisabled) {
		t.Fatalf("want disabled, got %v", err)
	}

	// Expired window (1ns + sleep)
	svcTiny := NewTokenService(ms).WithReenableWindow(time.Nanosecond)
	issued3, err := svcTiny.Issue(ctx, "t1", "conn_a", "z")
	if err != nil {
		t.Fatal(err)
	}
	if err := svcTiny.Revoke(ctx, issued3.Token.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := svcTiny.Reenable(ctx, issued3.Token.ID); !errors.Is(err, ErrReenableWindowExpired) {
		t.Fatalf("want window expired, got %v", err)
	}

	// Not revoked
	svc := NewTokenService(ms).WithReenableWindow(time.Minute)
	issued4, err := svc.Issue(ctx, "t1", "conn_a", "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reenable(ctx, issued4.Token.ID); !errors.Is(err, ErrTokenNotRevoked) {
		t.Fatalf("want not revoked, got %v", err)
	}
}

func TestBuildProxyConnectionURI(t *testing.T) {
	uri := BuildProxyConnectionURI("127.0.0.1:27018", "demo", "secret+token")
	if !strings.HasPrefix(uri, "mongodb://") {
		t.Fatalf("scheme: %s", uri)
	}
	if !strings.Contains(uri, "authSource=$external") {
		t.Fatalf("want literal $external, got: %s", uri)
	}
	if strings.Contains(uri, "%24") {
		t.Fatalf("authSource should not be percent-encoded: %s", uri)
	}
	if !strings.Contains(uri, "directConnection=true") {
		t.Fatalf("missing directConnection: %s", uri)
	}
	// hostname without port → default proxy port 27018
	uriHost := BuildProxyConnectionURI("nance-proxy.example.com", "org", "tok")
	if !strings.Contains(uriHost, "nance-proxy.example.com:27018") {
		t.Fatalf("default port: %s", uriHost)
	}
	uri2 := BuildProxyConnectionURI("", "org", "tok")
	if !strings.Contains(uri2, "127.0.0.1:27018") {
		t.Fatalf("default host: %s", uri2)
	}
}
