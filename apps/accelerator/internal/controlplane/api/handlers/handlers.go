package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	cpauth "github.com/taeven/nance/accelerator/internal/controlplane/auth"
	"github.com/taeven/nance/accelerator/internal/controlplane/service"
	"github.com/taeven/nance/accelerator/internal/model"
)

// PlatformPublic is JSON-safe instance metadata for self-hosters (no secrets).
type PlatformPublic struct {
	InviteOnly          bool   `json:"inviteOnly"`
	AllowOrgCreation    bool   `json:"allowOrgCreation"`
	AllowAdminBootstrap bool   `json:"allowAdminBootstrap"`
	ProxyPublicEndpoint string `json:"proxyPublicEndpoint"`
}

type Handlers struct {
	tenants     *service.TenantService
	connections *service.ConnectionService
	policies    *service.PolicyService
	tokens      *service.TokenService
	auth        *service.AuthService
	orgs        *service.OrgService
	platform    PlatformPublic
}

func NewHandlers(
	ts *service.TenantService,
	cs *service.ConnectionService,
	ps *service.PolicyService,
	toks *service.TokenService,
	auth *service.AuthService,
	orgs *service.OrgService,
	platform PlatformPublic,
) *Handlers {
	if orgs != nil && orgs.InviteOnly() {
		platform.InviteOnly = true
		platform.AllowOrgCreation = false
	}
	platform.AllowAdminBootstrap = true
	return &Handlers{
		tenants:     ts,
		connections: cs,
		policies:    ps,
		tokens:      toks,
		auth:        auth,
		orgs:        orgs,
		platform:    platform,
	}
}

// GetPlatformSettings returns public instance configuration for the dashboard.
func (h *Handlers) GetPlatformSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.platform)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func bearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
}

// ensureTenantAccess checks platform admin or org membership (read-only for members).
func (h *Handlers) ensureTenantAccess(r *http.Request, tenantID string) error {
	if cpauth.IsPlatformAdmin(r.Context()) {
		return nil
	}
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		return service.ErrUnauthorized
	}
	_, err := h.orgs.RequireMember(r.Context(), tenantID, u.ID)
	return err
}

// ensureTenantAdmin requires owner or admin (manage settings; not delete org).
func (h *Handlers) ensureTenantAdmin(r *http.Request, tenantID string) error {
	if cpauth.IsPlatformAdmin(r.Context()) {
		return nil
	}
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		return service.ErrUnauthorized
	}
	_, err := h.orgs.RequireAdmin(r.Context(), tenantID, u.ID)
	return err
}

// ensureTenantOwner requires owner role (delete organization).
func (h *Handlers) ensureTenantOwner(r *http.Request, tenantID string) error {
	if cpauth.IsPlatformAdmin(r.Context()) {
		return nil
	}
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		return service.ErrUnauthorized
	}
	_, err := h.orgs.RequireOwner(r.Context(), tenantID, u.ID)
	return err
}

func (h *Handlers) actorMembership(r *http.Request, tenantID string) (*model.OrganizationMember, error) {
	if cpauth.IsPlatformAdmin(r.Context()) && cpauth.UserFromContext(r.Context()) == nil {
		return &model.OrganizationMember{TenantID: tenantID, Role: model.RoleOwner}, nil
	}
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		return nil, service.ErrUnauthorized
	}
	return h.orgs.RequireMember(r.Context(), tenantID, u.ID)
}

func mapAuthErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, service.ErrOrgCreationDisabled):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrForbidden), errors.Is(err, service.ErrNotMember):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, service.ErrInvalidEmail), errors.Is(err, service.ErrInvalidCode),
		errors.Is(err, service.ErrTooManyAttempts), errors.Is(err, service.ErrInviteExpired),
		errors.Is(err, service.ErrAlreadyMember), errors.Is(err, service.ErrLastOwner):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrInviteNotFound), errors.Is(err, service.ErrTenantNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// ===== Auth handlers (public) =====

func (h *Handlers) RequestCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.auth.RequestCode(r.Context(), req.Email); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "If the email is valid, a verification code has been sent",
	})
}

func (h *Handlers) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Name is collected in onboarding after email verification, not on login.
	token, user, err := h.auth.VerifyCode(r.Context(), req.Email, req.Code, "")
	if err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":     token,
		"expiresIn": int((30 * 24 * time.Hour).Seconds()),
		"user":      user,
	})
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	_ = h.auth.Logout(r.Context(), bearerToken(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handlers) UpdateMe(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	updated, err := h.auth.UpdateProfile(r.Context(), u.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ===== Organizations (user-scoped) =====

func (h *Handlers) ListMyOrganizations(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	list, err := h.orgs.ListOrganizations(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) CreateMyOrganization(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	org, err := h.orgs.CreateOrganization(r.Context(), u.ID, req.ID, req.Name)
	if err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (h *Handlers) ListMyInvites(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	list, err := h.orgs.ListPendingInvitesForUser(r.Context(), u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	inviteID := chi.URLParam(r, "inviteId")
	org, err := h.orgs.AcceptInvite(r.Context(), u, inviteID)
	if err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

// ===== Tenant handlers =====

func (h *Handlers) CreateTenant(w http.ResponseWriter, r *http.Request) {
	// Prefer CreateMyOrganization; keep for platform admin / legacy
	if !cpauth.IsPlatformAdmin(r.Context()) {
		u := cpauth.UserFromContext(r.Context())
		if u == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		org, err := h.orgs.CreateOrganization(r.Context(), u.ID, req.ID, req.Name)
		if err != nil {
			mapAuthErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, &org.Tenant)
		return
	}
	// Platform admin (NANCE_ADMIN_TOKEN): always allowed — bootstrap first org on invite-only instances
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	t, err := h.tenants.Create(r.Context(), req.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *Handlers) GetTenant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAccess(r, id); err != nil {
		mapAuthErr(w, err)
		return
	}
	t, err := h.tenants.Get(r.Context(), id)
	if err != nil {
		if err == service.ErrTenantNotFound {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Include caller's role for UI permission gates
	out := map[string]any{
		"id":         t.ID,
		"name":       t.Name,
		"status":     t.Status,
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
	if m, err := h.actorMembership(r, id); err == nil && m != nil {
		out["role"] = m.Role
		out["canManage"] = service.CanManageSettings(m.Role)
		out["canDelete"] = m.Role == model.RoleOwner || cpauth.IsPlatformAdmin(r.Context())
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) ListTenants(w http.ResponseWriter, r *http.Request) {
	// Platform admin: all tenants. User: their organizations only.
	if cpauth.IsPlatformAdmin(r.Context()) && cpauth.UserFromContext(r.Context()) == nil {
		list, err := h.tenants.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		// admin with user session still falls through to membership list unless pure admin
		if cpauth.IsPlatformAdmin(r.Context()) {
			list, err := h.tenants.List(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, list)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	orgs, err := h.orgs.ListOrganizations(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Return tenant-shaped list for backward compatibility with existing UI list
	out := make([]*model.Tenant, 0, len(orgs))
	for _, o := range orgs {
		t := o.Tenant
		out = append(out, &t)
	}
	writeJSON(w, http.StatusOK, out)
}

// ===== Members / invites on tenant =====

func (h *Handlers) ListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	list, err := h.orgs.ListMembers(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) InviteMember(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	actor := "admin"
	actorRole := model.RoleOwner
	if u := cpauth.UserFromContext(r.Context()); u != nil {
		actor = u.ID
		if m, err := h.orgs.RequireMember(r.Context(), tenantID, u.ID); err == nil {
			actorRole = m.Role
		}
	}
	inv, err := h.orgs.InviteMember(r.Context(), tenantID, actor, req.Email, model.MemberRole(req.Role), actorRole)
	if err != nil {
		mapAuthErr(w, err)
		return
	}
	// Do not leak raw token in API response for security (email contains instructions)
	inv.RawToken = ""
	writeJSON(w, http.StatusCreated, inv)
}

func (h *Handlers) ListTenantInvites(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	list, err := h.orgs.ListPendingInvitesForTenant(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	inviteID := chi.URLParam(r, "inviteId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	actor := "admin"
	if u := cpauth.UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}
	if err := h.orgs.RevokeInvite(r.Context(), tenantID, actor, inviteID); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) RemoveMember(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	userID := chi.URLParam(r, "userId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	actor := "admin"
	actorRole := model.RoleOwner
	if u := cpauth.UserFromContext(r.Context()); u != nil {
		actor = u.ID
		if m, err := h.orgs.RequireMember(r.Context(), tenantID, u.ID); err == nil {
			actorRole = m.Role
		}
	}
	if err := h.orgs.RemoveMember(r.Context(), tenantID, actor, userID, actorRole); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RequestDeleteOrganization emails a verification code to the owner.
func (h *Handlers) RequestDeleteOrganization(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	u := cpauth.UserFromContext(r.Context())
	if u == nil && !cpauth.IsPlatformAdmin(r.Context()) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Platform admin without user session cannot receive email — require owner session.
	if u == nil {
		writeError(w, http.StatusBadRequest, "sign in as an organization owner to delete (email verification required)")
		return
	}
	if err := h.orgs.RequestDeleteOrganization(r.Context(), tenantID, u); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "A verification code was sent to your email. Enter it to permanently delete this organization.",
	})
}

// ConfirmDeleteOrganization verifies the code and deletes the org and all related data.
func (h *Handlers) ConfirmDeleteOrganization(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	u := cpauth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.orgs.ConfirmDeleteOrganization(r.Context(), tenantID, u, req.Code); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "deleted",
		"message": "Organization and all related data have been permanently removed",
	})
}

// ===== Connections (multi source Mongo per org) =====

func (h *Handlers) ListConnections(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	list, err := h.connections.List(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) CreateConnection(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	c, err := h.connections.Create(r.Context(), tenantID, req.Name, req.URI)
	if err != nil {
		if errors.Is(err, service.ErrTenantNotFound) {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		if errors.Is(err, service.ErrDuplicateName) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handlers) GetConnection(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	c, err := h.connections.Get(r.Context(), tenantID, connectionID)
	if err != nil {
		if errors.Is(err, service.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handlers) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		Name *string `json:"name"`
		URI  *string `json:"uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == nil && req.URI == nil {
		writeError(w, http.StatusBadRequest, "name or uri required")
		return
	}
	if req.Name != nil {
		if err := h.connections.Rename(r.Context(), tenantID, connectionID, *req.Name); err != nil {
			if errors.Is(err, service.ErrConnectionNotFound) {
				writeError(w, http.StatusNotFound, "connection not found")
				return
			}
			if errors.Is(err, service.ErrDuplicateName) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.URI != nil {
		if err := h.connections.UpdateURI(r.Context(), tenantID, connectionID, *req.URI); err != nil {
			if errors.Is(err, service.ErrConnectionNotFound) {
				writeError(w, http.StatusNotFound, "connection not found")
				return
			}
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	c, err := h.connections.Get(r.Context(), tenantID, connectionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handlers) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	if err := h.connections.Delete(r.Context(), tenantID, connectionID); err != nil {
		if errors.Is(err, service.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handlers) TestConnection(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	if err := h.connections.Test(r.Context(), tenantID, connectionID); err != nil {
		if errors.Is(err, service.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ===== Policy handlers =====

func (h *Handlers) GetPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	p, err := h.policies.Get(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) SetCollectionPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	dbColl := chi.URLParam(r, "dbColl")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var cp model.CollectionPolicy
	if err := json.NewDecoder(r.Body).Decode(&cp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.policies.SetCollectionPolicy(r.Context(), tenantID, dbColl, cp); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handlers) SetDefaultTTL(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		DefaultTTL int `json:"defaultTtlSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := h.policies.SetDefaults(r.Context(), tenantID, req.DefaultTTL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handlers) Invalidate(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		DB   string   `json:"db"`
		Coll string   `json:"coll"`
		Tags []string `json:"tags"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.DB == "" {
		req.DB = r.URL.Query().Get("db")
	}
	if req.Coll == "" {
		req.Coll = r.URL.Query().Get("coll")
	}
	if err := h.policies.Invalidate(r.Context(), tenantID, req.DB, req.Coll, req.Tags); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "invalidated",
		"tenantId": tenantID,
		"db":       req.DB,
		"coll":     req.Coll,
		"tags":     req.Tags,
	})
}

func (h *Handlers) SavingsReport(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenantId": tenantID,
		"note":     "Proxy exposes live counters via /metrics (nance_cache_*). Aggregate with Prometheus/Grafana.",
		"suggestedQueries": []string{
			"sum(rate(nance_cache_hits_total{tenant=\"" + tenantID + "\"}[1d]))",
			"sum(rate(nance_cache_misses_total{tenant=\"" + tenantID + "\"}[1d]))",
		},
	})
}

// ===== Tokens (proxy access for a source connection) =====

func (h *Handlers) IssueToken(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAdmin(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	issued, err := h.tokens.Issue(r.Context(), tenantID, connectionID, req.Description)
	if err != nil {
		if errors.Is(err, service.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok := issued.Token
	writeJSON(w, http.StatusCreated, map[string]any{
		"tokenId":            tok.ID,
		"rawToken":           issued.RawToken,
		"tenantId":           tok.TenantID,
		"connectionId":       tok.ConnectionID,
		"description":        tok.Description,
		"createdAt":          tok.CreatedAt,
		"proxyConnectionUri": issued.ProxyConnectionURI,
	})
}

func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantId")
	connectionID := chi.URLParam(r, "connectionId")
	if err := h.ensureTenantAccess(r, tenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	list, err := h.tokens.ListForConnection(r.Context(), tenantID, connectionID)
	if err != nil {
		if errors.Is(err, service.ErrConnectionNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) RevokeToken(w http.ResponseWriter, r *http.Request) {
	tokenID := chi.URLParam(r, "tokenId")
	tok, err := h.tokens.Get(r.Context(), tokenID)
	if err != nil {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err := h.ensureTenantAdmin(r, tok.TenantID); err != nil {
		mapAuthErr(w, err)
		return
	}
	if err := h.tokens.Revoke(r.Context(), tokenID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
