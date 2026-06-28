package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// OrgService manages organizations (tenants), membership, and invites for dashboard users.
type OrgService struct {
	store      store.Store
	mailer     Mailer
	inviteOnly bool // NANCE_INVITE_ONLY: users join via invite only; no self-serve org create
}

func NewOrgService(s store.Store, mailer Mailer) *OrgService {
	return &OrgService{store: s, mailer: mailer}
}

// WithInviteOnly enables invite-only mode (self-hosted deployments).
func (s *OrgService) WithInviteOnly(inviteOnly bool) *OrgService {
	s.inviteOnly = inviteOnly
	return s
}

// InviteOnly reports whether self-serve organization creation is disabled.
func (s *OrgService) InviteOnly() bool {
	return s != nil && s.inviteOnly
}

var (
	// ErrOrgCreationDisabled is returned when NANCE_INVITE_ONLY is set and a user tries to create an org.
	ErrOrgCreationDisabled = errors.New("organization creation is disabled on this instance; you must be invited to join an existing organization")
)

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{1,62}[a-z0-9])?$`)

// CreateOrganization creates a tenant and adds the user as owner.
// Blocked when invite-only mode is on (callers that are platform admin should use TenantService.Create instead).
func (s *OrgService) CreateOrganization(ctx context.Context, userID, id, name string) (*model.OrganizationSummary, error) {
	if s.inviteOnly {
		return nil, ErrOrgCreationDisabled
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = slugify(name)
	}
	id = strings.ToLower(id)
	if !slugRe.MatchString(id) {
		return nil, errors.New("id must be 3-64 chars: lowercase letters, digits, _ or -")
	}
	if _, err := s.store.GetTenant(ctx, id); err == nil {
		return nil, errors.New("organization id already exists")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	t := &model.Tenant{ID: id, Name: name, Status: "active", CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateTenant(ctx, t); err != nil {
		return nil, err
	}
	if err := s.store.AddMember(ctx, id, userID, model.RoleOwner); err != nil {
		return nil, err
	}
	_ = s.store.RecordAudit(ctx, id, userID, "create_organization", map[string]string{"name": name})
	return &model.OrganizationSummary{Tenant: *t, Role: model.RoleOwner}, nil
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "org-" + cryptoRandHex(4)
	}
	if len(out) > 48 {
		out = out[:48]
	}
	// ensure uniqueness-ish
	return out + "-" + cryptoRandHex(3)
}

// ListOrganizations returns orgs the user belongs to.
func (s *OrgService) ListOrganizations(ctx context.Context, userID string) ([]*model.OrganizationSummary, error) {
	return s.store.ListOrganizationsForUser(ctx, userID)
}

// RequireMember returns membership or ErrForbidden / ErrNotMember.
func (s *OrgService) RequireMember(ctx context.Context, tenantID, userID string) (*model.OrganizationMember, error) {
	m, err := s.store.GetMember(ctx, tenantID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrForbidden
		}
		return nil, err
	}
	return m, nil
}

// RequireAdmin requires owner or admin role (can manage settings; not delete org).
func (s *OrgService) RequireAdmin(ctx context.Context, tenantID, userID string) (*model.OrganizationMember, error) {
	m, err := s.RequireMember(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if m.Role != model.RoleOwner && m.Role != model.RoleAdmin {
		return nil, ErrForbidden
	}
	return m, nil
}

// RequireOwner requires the owner role (only owners may delete the organization).
func (s *OrgService) RequireOwner(ctx context.Context, tenantID, userID string) (*model.OrganizationMember, error) {
	m, err := s.RequireMember(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if m.Role != model.RoleOwner {
		return nil, ErrForbidden
	}
	return m, nil
}

// CanManageSettings is true for owner and admin.
func CanManageSettings(role model.MemberRole) bool {
	return role == model.RoleOwner || role == model.RoleAdmin
}

// orgDeleteCodeKey scopes OTP codes for organization deletion (reuses email_verification_codes PK).
func orgDeleteCodeKey(tenantID, ownerEmail string) string {
	return "orgdelete:" + tenantID + ":" + strings.ToLower(strings.TrimSpace(ownerEmail))
}

// ListMembers lists members of an org (caller must be a member).
func (s *OrgService) ListMembers(ctx context.Context, tenantID string) ([]*model.OrganizationMember, error) {
	return s.store.ListMembers(ctx, tenantID)
}

// ListPendingInvitesForTenant lists outstanding invites for an org.
func (s *OrgService) ListPendingInvitesForTenant(ctx context.Context, tenantID string) ([]*model.OrganizationInvite, error) {
	return s.store.ListPendingInvitesForTenant(ctx, tenantID)
}

// ListPendingInvitesForUser lists invites addressed to the user's email.
func (s *OrgService) ListPendingInvitesForUser(ctx context.Context, user *model.User) ([]*model.OrganizationInvite, error) {
	return s.store.ListPendingInvitesForEmail(ctx, user.Email)
}

// InviteMember creates an invite and emails the invitee.
// inviterRole is used to enforce hierarchy: only owners may invite owners; admins may invite admin/member.
func (s *OrgService) InviteMember(ctx context.Context, tenantID, inviterID, email string, role model.MemberRole, inviterRole model.MemberRole) (*model.OrganizationInvite, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if role == "" {
		role = model.RoleMember
	}
	if role != model.RoleMember && role != model.RoleAdmin && role != model.RoleOwner {
		return nil, errors.New("invalid role")
	}
	// Role hierarchy for invites
	switch inviterRole {
	case model.RoleOwner:
		// owners may invite any role
	case model.RoleAdmin:
		if role == model.RoleOwner {
			return nil, errors.New("only owners can invite another owner")
		}
	default:
		return nil, ErrForbidden
	}
	// If user already member, reject
	if u, err := s.store.GetUserByEmail(ctx, email); err == nil {
		if _, merr := s.store.GetMember(ctx, tenantID, u.ID); merr == nil {
			return nil, ErrAlreadyMember
		}
	}

	raw, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	inv := &model.OrganizationInvite{
		ID:        "inv_" + cryptoRandHex(12),
		TenantID:  tenantID,
		Email:     email,
		Role:      role,
		InvitedBy: inviterID,
		ExpiresAt: now.Add(7 * 24 * time.Hour),
		CreatedAt: now,
		RawToken:  raw,
	}
	if t, err := s.store.GetTenant(ctx, tenantID); err == nil {
		inv.TenantName = t.Name
	}
	// Replace any prior pending invite for same email (delete then insert)
	if existing, err := s.store.ListPendingInvitesForTenant(ctx, tenantID); err == nil {
		for _, e := range existing {
			if strings.EqualFold(e.Email, email) {
				_ = s.store.DeleteInvite(ctx, e.ID)
			}
		}
	}
	if err := s.store.CreateInvite(ctx, inv, hashToken(raw)); err != nil {
		return nil, err
	}
	body := fmt.Sprintf(
		"You have been invited to join organization %q on Nance.\n\nSign in with this email and accept the invite from your organizations page.\nInvite id: %s\n",
		inv.TenantName, inv.ID,
	)
	if s.mailer != nil {
		_ = s.mailer.Send(ctx, email, "Nance organization invite", body)
	}
	_ = s.store.RecordAudit(ctx, tenantID, inviterID, "invite_member", map[string]string{"email": email, "role": string(role)})
	return inv, nil
}

// AcceptInvite accepts a pending invite for the logged-in user (email must match).
func (s *OrgService) AcceptInvite(ctx context.Context, user *model.User, inviteID string) (*model.OrganizationSummary, error) {
	inv, err := s.store.GetInviteByID(ctx, inviteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrInviteNotFound
		}
		return nil, err
	}
	if inv.AcceptedAt != nil {
		return nil, ErrInviteNotFound
	}
	if time.Now().UTC().After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	if !strings.EqualFold(inv.Email, user.Email) {
		return nil, ErrForbidden
	}
	if err := s.store.AddMember(ctx, inv.TenantID, user.ID, inv.Role); err != nil {
		return nil, err
	}
	_ = s.store.MarkInviteAccepted(ctx, inv.ID)
	t, err := s.store.GetTenant(ctx, inv.TenantID)
	if err != nil {
		return nil, err
	}
	_ = s.store.RecordAudit(ctx, inv.TenantID, user.ID, "accept_invite", map[string]string{"inviteId": inv.ID})
	return &model.OrganizationSummary{Tenant: *t, Role: inv.Role}, nil
}

// RemoveMember removes a member; cannot remove last owner.
// Admins cannot remove owners; only owners may remove other owners.
func (s *OrgService) RemoveMember(ctx context.Context, tenantID, actorID, targetUserID string, actorRole model.MemberRole) error {
	target, err := s.store.GetMember(ctx, tenantID, targetUserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotMember
		}
		return err
	}
	if target.Role == model.RoleOwner && actorRole != model.RoleOwner {
		return errors.New("only owners can remove an owner")
	}
	if target.Role == model.RoleOwner {
		members, err := s.store.ListMembers(ctx, tenantID)
		if err != nil {
			return err
		}
		owners := 0
		for _, m := range members {
			if m.Role == model.RoleOwner {
				owners++
			}
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	if err := s.store.RemoveMember(ctx, tenantID, targetUserID); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, actorID, "remove_member", map[string]string{"userId": targetUserID})
	return nil
}

// RevokeInvite deletes a pending invite.
func (s *OrgService) RevokeInvite(ctx context.Context, tenantID, actorID, inviteID string) error {
	inv, err := s.store.GetInviteByID(ctx, inviteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrInviteNotFound
		}
		return err
	}
	if inv.TenantID != tenantID {
		return ErrInviteNotFound
	}
	if err := s.store.DeleteInvite(ctx, inviteID); err != nil {
		return err
	}
	_ = s.store.RecordAudit(ctx, tenantID, actorID, "revoke_invite", map[string]string{"inviteId": inviteID})
	return nil
}

// RequestDeleteOrganization sends a verification code to the owner's email.
// Only owners may call this; admins and members are rejected.
func (s *OrgService) RequestDeleteOrganization(ctx context.Context, tenantID string, owner *model.User) error {
	if _, err := s.RequireOwner(ctx, tenantID, owner.ID); err != nil {
		return err
	}
	t, err := s.store.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTenantNotFound
		}
		return err
	}
	code, err := randomDigits(6)
	if err != nil {
		return err
	}
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	key := orgDeleteCodeKey(tenantID, owner.Email)
	expires := time.Now().UTC().Add(15 * time.Minute)
	if err := s.store.SetEmailVerificationCode(ctx, key, string(hashBytes), expires); err != nil {
		return err
	}
	body := fmt.Sprintf(
		"You requested to permanently delete organization %q (%s) on Nance.\n\n"+
			"This will remove the organization, members, invites, backends, cache policies, proxy tokens, and related data. This cannot be undone.\n\n"+
			"Verification code: %s\n\nExpires in 15 minutes. If you did not request this, ignore this email.\n",
		t.Name, t.ID, code,
	)
	if s.mailer != nil {
		_ = s.mailer.Send(ctx, owner.Email, "Confirm organization deletion — Nance", body)
	}
	_ = s.store.RecordAudit(ctx, tenantID, owner.ID, "request_delete_organization", map[string]string{"email": owner.Email})
	return nil
}

// ConfirmDeleteOrganization verifies the email code and permanently deletes the org and cascaded data.
func (s *OrgService) ConfirmDeleteOrganization(ctx context.Context, tenantID string, owner *model.User, code string) error {
	if _, err := s.RequireOwner(ctx, tenantID, owner.ID); err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if len(code) < 4 {
		return ErrInvalidCode
	}
	key := orgDeleteCodeKey(tenantID, owner.Email)
	hash, expires, attempts, err := s.store.GetEmailVerificationCode(ctx, key)
	if err != nil {
		return ErrInvalidCode
	}
	if attempts >= 8 {
		return ErrTooManyAttempts
	}
	if time.Now().UTC().After(expires) {
		_ = s.store.ClearEmailVerificationCode(ctx, key)
		return ErrInvalidCode
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)) != nil {
		_ = s.store.IncrementEmailVerificationAttempts(ctx, key)
		return ErrInvalidCode
	}
	_ = s.store.ClearEmailVerificationCode(ctx, key)

	_ = s.store.RecordAudit(ctx, tenantID, owner.ID, "delete_organization", map[string]string{"confirmed": "true"})
	if err := s.store.DeleteTenant(ctx, tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTenantNotFound
		}
		return err
	}
	return nil
}
