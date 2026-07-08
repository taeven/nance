package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/taeven/nance/accelerator/internal/model"
)

// ErrDuplicate is returned when a unique constraint would be violated (e.g. connection name).
var ErrDuplicate = errors.New("duplicate")

// MemoryStore is an in-memory Store for unit tests (not production).
type MemoryStore struct {
	mu sync.Mutex

	tenants      map[string]*model.Tenant
	connections  map[string]*model.Connection // by connection id
	policies     map[string]*model.CachePolicy
	tokens       map[string]*tokenRow
	users        map[string]*model.User // by id
	usersByEmail map[string]*model.User
	emailCodes   map[string]emailCode
	sessions     map[string]sessionRow                  // by token hash
	members      map[string]map[string]model.MemberRole // tenant -> user -> role
	invites      map[string]*inviteRow
	audits       int
}

type tokenRow struct {
	tok  *model.Token
	hash string
}

type emailCode struct {
	hash     string
	expires  time.Time
	attempts int
}

type sessionRow struct {
	id        string
	userID    string
	expiresAt time.Time
}

type inviteRow struct {
	inv  *model.OrganizationInvite
	hash string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tenants:      make(map[string]*model.Tenant),
		connections:  make(map[string]*model.Connection),
		policies:     make(map[string]*model.CachePolicy),
		tokens:       make(map[string]*tokenRow),
		users:        make(map[string]*model.User),
		usersByEmail: make(map[string]*model.User),
		emailCodes:   make(map[string]emailCode),
		sessions:     make(map[string]sessionRow),
		members:      make(map[string]map[string]model.MemberRole),
		invites:      make(map[string]*inviteRow),
	}
}

func (m *MemoryStore) Close() error { return nil }

func (m *MemoryStore) CreateTenant(_ context.Context, t *model.Tenant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.tenants[t.ID] = &cp
	return nil
}

func (m *MemoryStore) GetTenant(_ context.Context, id string) (*model.Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (m *MemoryStore) ListTenants(_ context.Context) ([]*model.Tenant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryStore) DeleteTenant(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[id]; !ok {
		return ErrNotFound
	}
	delete(m.tenants, id)
	delete(m.policies, id)
	delete(m.members, id)
	for cid, c := range m.connections {
		if c.TenantID == id {
			delete(m.connections, cid)
		}
	}
	for iid, inv := range m.invites {
		if inv.inv.TenantID == id {
			delete(m.invites, iid)
		}
	}
	for tid, row := range m.tokens {
		if row.tok.TenantID == id {
			delete(m.tokens, tid)
		}
	}
	return nil
}

func (m *MemoryStore) CreateConnection(_ context.Context, c *model.Connection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.connections {
		if existing.TenantID == c.TenantID && strings.EqualFold(existing.Name, c.Name) {
			return ErrDuplicate
		}
	}
	cp := *c
	if cp.URICiphertext != nil {
		cp.URICiphertext = append([]byte(nil), c.URICiphertext...)
	}
	if cp.Nonce != nil {
		cp.Nonce = append([]byte(nil), c.Nonce...)
	}
	m.connections[c.ID] = &cp
	return nil
}

func (m *MemoryStore) UpdateConnectionURI(_ context.Context, connectionID string, ciphertext, nonce []byte, dekVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connections[connectionID]
	if !ok {
		return ErrNotFound
	}
	c.URICiphertext = append([]byte(nil), ciphertext...)
	c.Nonce = append([]byte(nil), nonce...)
	c.DEKVersion = dekVersion
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemoryStore) UpdateConnectionName(_ context.Context, connectionID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connections[connectionID]
	if !ok {
		return ErrNotFound
	}
	for _, existing := range m.connections {
		if existing.ID != connectionID && existing.TenantID == c.TenantID && strings.EqualFold(existing.Name, name) {
			return ErrDuplicate
		}
	}
	c.Name = name
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemoryStore) UpdateConnectionAutoInvalidate(_ context.Context, connectionID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connections[connectionID]
	if !ok {
		return ErrNotFound
	}
	c.AutoInvalidateOnWrite = enabled
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemoryStore) GetConnection(_ context.Context, connectionID string) (*model.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connections[connectionID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *MemoryStore) ListConnections(_ context.Context, tenantID string) ([]*model.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Connection, 0)
	for _, c := range m.connections {
		if c.TenantID == tenantID {
			cp := *c
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemoryStore) DeleteConnection(_ context.Context, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.connections[connectionID]; !ok {
		return ErrNotFound
	}
	delete(m.connections, connectionID)
	for tid, row := range m.tokens {
		if row.tok.ConnectionID == connectionID {
			delete(m.tokens, tid)
		}
	}
	return nil
}

func (m *MemoryStore) UpdateConnectionValidated(_ context.Context, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.connections[connectionID]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	c.LastValidatedAt = &now
	c.UpdatedAt = now
	return nil
}

func (m *MemoryStore) GetCachePolicy(_ context.Context, connectionID string) (*model.CachePolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.policies[connectionID]; ok {
		cp := *p
		if cp.Collections == nil {
			cp.Collections = map[string]model.CollectionPolicy{}
		}
		return &cp, nil
	}
	tenantID := ""
	if c, ok := m.connections[connectionID]; ok {
		tenantID = c.TenantID
	}
	return &model.CachePolicy{
		ConnectionID: connectionID, TenantID: tenantID, DefaultTtlSeconds: 60,
		Collections: map[string]model.CollectionPolicy{}, CacheKeyVersion: 1,
	}, nil
}

func (m *MemoryStore) UpsertCachePolicy(_ context.Context, p *model.CachePolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *p
	if cp.Collections == nil {
		cp.Collections = map[string]model.CollectionPolicy{}
	}
	cols := make(map[string]model.CollectionPolicy, len(cp.Collections))
	for k, v := range cp.Collections {
		cols[k] = v
	}
	cp.Collections = cols
	m.policies[p.ConnectionID] = &cp
	return nil
}

func (m *MemoryStore) ListAllCachePolicies(_ context.Context) ([]*model.CachePolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.CachePolicy, 0, len(m.policies))
	for _, p := range m.policies {
		cp := *p
		if cp.Collections == nil {
			cp.Collections = map[string]model.CollectionPolicy{}
		}
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryStore) CreateToken(_ context.Context, tok *model.Token, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *tok
	m.tokens[tok.ID] = &tokenRow{tok: &cp, hash: tokenHash}
	return nil
}

func (m *MemoryStore) GetTokenByID(_ context.Context, id string) (*model.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tokens[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *row.tok
	return &cp, nil
}

func (m *MemoryStore) ListTokensForTenant(_ context.Context, tenantID string) ([]*model.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Token, 0)
	for _, row := range m.tokens {
		if row.tok.TenantID == tenantID {
			cp := *row.tok
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListTokensForConnection(_ context.Context, connectionID string) ([]*model.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Token, 0)
	for _, row := range m.tokens {
		if row.tok.ConnectionID == connectionID {
			cp := *row.tok
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListActiveTokenHashes(_ context.Context, tenantID string) ([]TokenHashRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TokenHashRow, 0)
	now := time.Now().UTC()
	for _, row := range m.tokens {
		t := row.tok
		if t.TenantID != tenantID || t.RevokedAt != nil || t.ConnectionID == "" {
			continue
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(now) {
			continue
		}
		out = append(out, TokenHashRow{ID: t.ID, TokenHash: row.hash, ConnectionID: t.ConnectionID})
	}
	return out, nil
}

func (m *MemoryStore) RevokeToken(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tokens[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	row.tok.RevokedAt = &now
	return nil
}

func (m *MemoryStore) ClearTokenRevocation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.tokens[id]
	if !ok || row.tok.RevokedAt == nil {
		return ErrNotFound
	}
	row.tok.RevokedAt = nil
	return nil
}

func (m *MemoryStore) UpsertUserByEmail(_ context.Context, email, name string) (*model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.usersByEmail[email]; ok {
		if name != "" && u.Name == "" {
			u.Name = name
		}
		cp := *u
		return &cp, nil
	}
	id := "usr_mem_" + email
	now := time.Now().UTC()
	u := &model.User{ID: id, Email: email, Name: name, CreatedAt: now, UpdatedAt: now}
	m.users[id] = u
	m.usersByEmail[email] = u
	cp := *u
	return &cp, nil
}

func (m *MemoryStore) GetUserByID(_ context.Context, id string) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (m *MemoryStore) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByEmail[email]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (m *MemoryStore) UpdateUserName(_ context.Context, id, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrNotFound
	}
	u.Name = name
	u.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemoryStore) SetEmailVerificationCode(_ context.Context, email, codeHash string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emailCodes[strings.ToLower(email)] = emailCode{hash: codeHash, expires: expiresAt.UTC(), attempts: 0}
	return nil
}

func (m *MemoryStore) GetEmailVerificationCode(_ context.Context, email string) (string, time.Time, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.emailCodes[strings.ToLower(email)]
	if !ok {
		return "", time.Time{}, 0, ErrNotFound
	}
	return c.hash, c.expires, c.attempts, nil
}

func (m *MemoryStore) IncrementEmailVerificationAttempts(_ context.Context, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.emailCodes[strings.ToLower(email)]
	if !ok {
		return ErrNotFound
	}
	c.attempts++
	m.emailCodes[strings.ToLower(email)] = c
	return nil
}

func (m *MemoryStore) ClearEmailVerificationCode(_ context.Context, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.emailCodes, strings.ToLower(email))
	return nil
}

func (m *MemoryStore) CreateSession(_ context.Context, id, userID, tokenHash string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[tokenHash] = sessionRow{id: id, userID: userID, expiresAt: expiresAt.UTC()}
	return nil
}

func (m *MemoryStore) GetSessionByTokenHash(_ context.Context, tokenHash string) (string, string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[tokenHash]
	if !ok {
		return "", "", time.Time{}, ErrNotFound
	}
	return s.id, s.userID, s.expiresAt, nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, s := range m.sessions {
		if s.id == sessionID {
			delete(m.sessions, h)
		}
	}
	return nil
}

func (m *MemoryStore) DeleteSessionByTokenHash(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, tokenHash)
	return nil
}

func (m *MemoryStore) AddMember(_ context.Context, tenantID, userID string, role model.MemberRole) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[tenantID] == nil {
		m.members[tenantID] = make(map[string]model.MemberRole)
	}
	m.members[tenantID][userID] = role
	return nil
}

func (m *MemoryStore) RemoveMember(_ context.Context, tenantID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[tenantID] != nil {
		delete(m.members[tenantID], userID)
	}
	return nil
}

func (m *MemoryStore) GetMember(_ context.Context, tenantID, userID string) (*model.OrganizationMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.members[tenantID][userID]
	if !ok {
		return nil, ErrNotFound
	}
	email, name := "", ""
	if u := m.users[userID]; u != nil {
		email, name = u.Email, u.Name
	}
	return &model.OrganizationMember{
		TenantID: tenantID, UserID: userID, Email: email, Name: name, Role: role, CreatedAt: time.Now().UTC(),
	}, nil
}

func (m *MemoryStore) ListMembers(_ context.Context, tenantID string) ([]*model.OrganizationMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.OrganizationMember, 0)
	for uid, role := range m.members[tenantID] {
		email, name := "", ""
		if u := m.users[uid]; u != nil {
			email, name = u.Email, u.Name
		}
		out = append(out, &model.OrganizationMember{
			TenantID: tenantID, UserID: uid, Email: email, Name: name, Role: role,
		})
	}
	return out, nil
}

func (m *MemoryStore) ListOrganizationsForUser(_ context.Context, userID string) ([]*model.OrganizationSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.OrganizationSummary, 0)
	for tid, mems := range m.members {
		role, ok := mems[userID]
		if !ok {
			continue
		}
		t, ok := m.tenants[tid]
		if !ok {
			continue
		}
		out = append(out, &model.OrganizationSummary{Tenant: *t, Role: role})
	}
	return out, nil
}

func (m *MemoryStore) CreateInvite(_ context.Context, inv *model.OrganizationInvite, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inv
	m.invites[inv.ID] = &inviteRow{inv: &cp, hash: tokenHash}
	return nil
}

func (m *MemoryStore) GetInviteByID(_ context.Context, id string) (*model.OrganizationInvite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.invites[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *row.inv
	if t := m.tenants[cp.TenantID]; t != nil {
		cp.TenantName = t.Name
	}
	return &cp, nil
}

func (m *MemoryStore) GetPendingInviteByTokenHash(_ context.Context, tokenHash string) (*model.OrganizationInvite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.invites {
		if row.hash == tokenHash && row.inv.AcceptedAt == nil {
			cp := *row.inv
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) ListPendingInvitesForEmail(_ context.Context, email string) ([]*model.OrganizationInvite, error) {
	email = strings.ToLower(email)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	out := make([]*model.OrganizationInvite, 0)
	for _, row := range m.invites {
		inv := row.inv
		if inv.AcceptedAt != nil || !strings.EqualFold(inv.Email, email) || inv.ExpiresAt.Before(now) {
			continue
		}
		cp := *inv
		if t := m.tenants[cp.TenantID]; t != nil {
			cp.TenantName = t.Name
		}
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryStore) ListPendingInvitesForTenant(_ context.Context, tenantID string) ([]*model.OrganizationInvite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	out := make([]*model.OrganizationInvite, 0)
	for _, row := range m.invites {
		inv := row.inv
		if inv.TenantID != tenantID || inv.AcceptedAt != nil || inv.ExpiresAt.Before(now) {
			continue
		}
		cp := *inv
		out = append(out, &cp)
	}
	return out, nil
}

func (m *MemoryStore) MarkInviteAccepted(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.invites[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	row.inv.AcceptedAt = &now
	return nil
}

func (m *MemoryStore) DeleteInvite(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.invites, id)
	return nil
}

func (m *MemoryStore) RecordAudit(_ context.Context, _, _, _ string, _ any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits++
	return nil
}

// Ensure MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)
