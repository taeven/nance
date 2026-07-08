package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/taeven/nance/accelerator/internal/controlplane/api/handlers"
	"github.com/taeven/nance/accelerator/internal/controlplane/auth"
	"github.com/taeven/nance/accelerator/internal/controlplane/service"
)

func NewServer(
	ts *service.TenantService,
	cs *service.ConnectionService,
	ps *service.PolicyService,
	toks *service.TokenService,
	authSvc *service.AuthService,
	orgSvc *service.OrgService,
	platform handlers.PlatformPublic,
) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	h := handlers.NewHandlers(ts, cs, ps, toks, authSvc, orgSvc, platform)

	// Probe endpoints without access logs (k8s/LB scrapes).
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	r.Handle("/metrics", promhttp.Handler())

	r.Group(func(r chi.Router) {
		r.Use(middleware.Logger)

		r.Route("/api/v1", func(r chi.Router) {
			// Public: instance mode for self-hosters (invite-only, etc.) + auth
			r.Get("/platform", h.GetPlatformSettings)
			r.Post("/auth/request-code", h.RequestCode)
			r.Post("/auth/verify", h.VerifyCode)

			// Authenticated routes (user session or platform admin token)
			r.Group(func(r chi.Router) {
				r.Use(auth.Middleware(authSvc))

				r.Post("/auth/logout", h.Logout)
				r.Get("/me", h.Me)
				r.Patch("/me", h.UpdateMe)
				r.Get("/me/organizations", h.ListMyOrganizations)
				r.Post("/me/organizations", h.CreateMyOrganization)
				r.Get("/me/invites", h.ListMyInvites)
				r.Post("/me/invites/{inviteId}/accept", h.AcceptInvite)

				// Tenants / organizations
				// Role hierarchy: member = read-only, admin = manage settings, owner = + delete org
				r.Post("/tenants", h.CreateTenant)
				r.Get("/tenants", h.ListTenants)
				r.Get("/tenants/{tenantId}", h.GetTenant)
				r.Post("/tenants/{tenantId}/delete/request-code", h.RequestDeleteOrganization) // owner only
				r.Post("/tenants/{tenantId}/delete/confirm", h.ConfirmDeleteOrganization)      // owner only + email code

				// Members & invites (admin+; invite role limits apply)
				r.Get("/tenants/{tenantId}/members", h.ListMembers)
				r.Post("/tenants/{tenantId}/invites", h.InviteMember)
				r.Get("/tenants/{tenantId}/invites", h.ListTenantInvites)
				r.Delete("/tenants/{tenantId}/invites/{inviteId}", h.RevokeInvite)
				r.Delete("/tenants/{tenantId}/members/{userId}", h.RemoveMember)

				// Connections (many named source Mongo URIs per org)
				r.Get("/tenants/{tenantId}/connections", h.ListConnections)
				r.Post("/tenants/{tenantId}/connections", h.CreateConnection)
				r.Get("/tenants/{tenantId}/connections/{connectionId}", h.GetConnection)
				r.Put("/tenants/{tenantId}/connections/{connectionId}", h.UpdateConnection)
				r.Delete("/tenants/{tenantId}/connections/{connectionId}", h.DeleteConnection)
				r.Post("/tenants/{tenantId}/connections/{connectionId}/test", h.TestConnection)
				// Proxy access tokens per connection
				r.Get("/tenants/{tenantId}/connections/{connectionId}/tokens", h.ListTokens)
				r.Post("/tenants/{tenantId}/connections/{connectionId}/tokens", h.IssueToken)
				r.Delete("/tokens/{tokenId}", h.RevokeToken)
				r.Post("/tokens/{tokenId}/reenable", h.ReenableToken)

				// Cache policy + invalidate (per connection)
				r.Get("/tenants/{tenantId}/connections/{connectionId}/policy", h.GetPolicy)
				r.Put("/tenants/{tenantId}/connections/{connectionId}/policy/collections/{dbColl}", h.SetCollectionPolicy)
				r.Put("/tenants/{tenantId}/connections/{connectionId}/policy/defaults", h.SetDefaultTTL)
				r.Post("/tenants/{tenantId}/connections/{connectionId}/invalidate", h.Invalidate)

				r.Get("/tenants/{tenantId}/savings", h.SavingsReport)
			})
		})
	})

	return r
}
