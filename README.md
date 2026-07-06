# Nance

**Nance** is an open-source **MongoDB accelerator**: a multi-tenant control plane, a MongoDB wire-protocol proxy with optional Redis read-through caching, and tools to operate and load-test it.

Self-host the stack on your own servers, invite your team, point application clients at the proxy, and opt into caching **per query** with a `_cache` collection suffix.

## Repository layout

| Path | Description |
|------|-------------|
| [`apps/accelerator`](apps/accelerator) | Control plane (HTTP API) + data-plane **proxy** (Mongo wire protocol) |
| [`apps/admin-dashboard`](apps/admin-dashboard) | Nuxt admin UI (email OTP login, orgs, members, policies, tokens) |
| [`apps/mongo-loadtest`](apps/mongo-loadtest) | Go load tester for MongoDB or the Nance proxy (throughput, latency, breaking point) |

```
                    ┌─────────────────────┐
   Browser          │  Admin dashboard    │
   (email OTP)  ──► │  :3000 (Nuxt)       │
                    └──────────┬──────────┘
                               │  /api/* → control plane
                               ▼
                    ┌─────────────────────┐     ┌──────────────┐
                    │  Control plane      │────►│  Postgres    │
                    │  :8080              │     │  (tenants,   │
                    └──────────┬──────────┘     │   users, …)  │
                               │                └──────────────┘
          data-plane tokens    │
                               ▼
   App / driver ──► ┌─────────────────────┐     ┌──────────────┐
   Mongo URI        │  Proxy :27018       │────►│  Your Mongo  │
   PLAIN auth       │  (optional Redis    │     │  (per tenant)│
                    │   read-through)     │     └──────────────┘
                    └─────────────────────┘
                               │
                               ▼
                          Redis (cache)
```

## Core concepts

### Organizations (tenants)

An **organization** is a tenant with its own encrypted MongoDB backend URI, cache policy, proxy access tokens, and members.

**Roles**

| Role | Capabilities |
|------|----------------|
| **member** | Read-only in the dashboard (view settings, list tokens/members) |
| **admin** | Manage backend, caching, tokens, invites, invalidation — **cannot** delete the org |
| **owner** | Full control including **delete organization** (email verification code required) |

### Caching (`_cache` suffix)

- Clients **opt in per query** by using a collection name ending in `_cache` (e.g. `orders_cache`).
- The proxy strips that suffix, talks to the real collection (`orders`), and may serve/store results in Redis.
- Default TTL is **60 seconds** for all such queries; override per org or per real collection in the control plane / UI.
- Queries **without** `_cache` always hit MongoDB (no cache).
- Cache entries expire by **TTL** only unless you **manually invalidate** (dashboard / `POST …/invalidate`). Writes do **not** auto-bust the cache.

### Invite-only mode (self-hosters)

Set on the control plane:

```bash
export NANCE_INVITE_ONLY=true
```

- Users can still **sign in** with email OTP.
- They **cannot create organizations**; they only **join via invite**.
- Operators bootstrap the first org with **`NANCE_ADMIN_TOKEN`** (`POST /api/v1/tenants`), then invite owners/admins from the UI.
- Public flag: `GET /api/v1/platform` → `{ "inviteOnly": true, "allowOrgCreation": false, ... }`.

## Quick start (local)

### 1. Infra + accelerator

```bash
cd apps/accelerator
make dev-up                                          # Postgres, Mongo, Redis
export NANCE_MASTER_KEY="thisisexactly32byteslongforaes256!!"
# optional: export NANCE_ADMIN_TOKEN=supersecret
# optional: export NANCE_INVITE_ONLY=true
make run                                             # control plane :8080
# other terminal:
make run-proxy                                       # proxy :27018, health :9090
make seed                                            # demo tenant + token (uses admin bearer)
```

Details: [`apps/accelerator/README.md`](apps/accelerator/README.md).

### 2. Admin dashboard

```bash
cd apps/admin-dashboard
npm install
export NANCE_ACCELERATOR_URL=http://localhost:8080
# optional server-only fallback: NANCE_ADMIN_TOKEN=...
npm run dev                                          # http://localhost:3000
```

Sign in with email; in dev the verification code is **logged by the control plane**. Complete onboarding (name), then create or join an organization.

Details: [`apps/admin-dashboard/README.md`](apps/admin-dashboard/README.md).

### 3. Connect an app through the proxy

In the dashboard **Connection** tab: add one or more named source Mongo URIs, then **Create access** on a connection and copy the **proxy connection URI** (shown once). The token selects which source is used. Or use **PLAIN** auth manually:

- **Username** = organization / tenant id  
- **Password** = proxy access secret (`rawToken` / embedded in `proxyConnectionUri` at issuance)  
- Example:  
  `mongodb://demo:<rawToken>@127.0.0.1:27018/?authMechanism=PLAIN&authSource=$external`

Cached read example (real collection `orders`, 60s default TTL):

```js
db.orders_cache.find({ status: "open" })
// bypass cache:
db.orders.find({ status: "open" })
```

### 4. Load test (optional)

```bash
cd apps/mongo-loadtest
go run ./cmd/loadtest -uri 'mongodb://...' -db loadtest -collection loadtest_docs
```

Details: [`apps/mongo-loadtest/README.md`](apps/mongo-loadtest/README.md).

## Components at a glance

| Component | Port (default) | Role |
|-----------|----------------|------|
| Control plane | `8080` | REST API, migrations, email OTP (dev: log mailer), policies, tokens |
| Proxy | `27018` (health `9090`) | Mongo wire proxy, tenant routing, Redis cache |
| Admin dashboard | `3000` | Operator / team UI |
| Postgres | `5432` | Control plane state |
| Redis | `6379` | Read-through cache (optional but needed for cache hits) |
| Sample Mongo | `27017` | Local backend for the `demo` tenant |

## Security notes

- Backend Mongo URIs are **encrypted at rest** (`NANCE_MASTER_KEY`, AES-GCM).
- Proxy tokens are stored hashed; the **raw** secret is returned **once**.
- Dashboard sessions are bearer tokens issued after email verification.
- Prefer `NANCE_INVITE_ONLY=true` and a strong `NANCE_ADMIN_TOKEN` on public instances.
- Do not expose the control plane or dashboard to the internet without TLS and access control.

## Development

- **Go 1.22+** for accelerator and mongo-loadtest  
- **Node 20+** for admin-dashboard  
- Accelerator: `make test`, `make build-all`, `make lint` (see app README)  
- Migrations live in `apps/accelerator/migrations/` and run on control plane start  

## Continuous delivery

On every push to `main`, GitHub Actions:

1. Builds and pushes **controlplane**, **proxy**, and **dashboard** images to GHCR (`ghcr.io/taeven/nance/...`, tags include `sha-<short>`).
2. Opens a **pull request** in [`taeven/nance-deploy`](https://github.com/taeven/nance-deploy) that bumps Kustomize image tags under `deployments/nance/overlays/dev`.

Merging that PR applies the manifests to the Kubernetes cluster (VKE). Configure secret `DEPLOY_REPO_TOKEN` on this repo (PAT with write access to `nance-deploy`). Cluster credentials live only in `nance-deploy` as `KUBE_CONFIG_DATA`.

## License

This project is licensed under the [MIT License](LICENSE). Copyright (c) 2026 taeven.

The codebase is under active development; APIs and env vars may evolve — prefer the per-app READMEs for the latest flags and endpoints.

## Further reading

- [Accelerator (control plane + proxy)](apps/accelerator/README.md)
- [Admin dashboard](apps/admin-dashboard/README.md)
- [mongo-loadtest](apps/mongo-loadtest/README.md)
