# Nance Accelerator

Control plane (**HTTP API**) and data-plane **MongoDB proxy** for the Nance accelerator.

- **Control plane** (`cmd/controlplane`): tenants/orgs, users, email OTP sessions, invites, encrypted backends, cache policies, proxy tokens, platform settings (`NANCE_INVITE_ONLY`).
- **Proxy** (`cmd/proxy`): MongoDB wire protocol (OP_MSG), PLAIN auth with tenant tokens, connection pooling to each tenant’s real MongoDB, optional Redis read-through cache for `*_cache` collections.

Part of the [Nance monorepo](../../README.md). UI: [`../admin-dashboard`](../admin-dashboard). Benchmarks: [`../benchmark`](../benchmark) (Locust).

## Architecture

```
Clients (drivers)                    Operators (dashboard / curl)
        │                                      │
        │  Mongo wire :27018                   │  HTTP :8080
        ▼                                      ▼
   ┌─────────────┐                      ┌──────────────────┐
   │    Proxy    │──policies/tokens/───►│  Control plane   │
   │             │   backends (PG)      │  + migrations    │
   └──────┬──────┘                      └────────┬─────────┘
          │                                      │
          ▼                                      ▼
   Tenant MongoDB                            Postgres
   Redis (cache)                             (optional Redis for invalidate)
```

### Caching

| Client collection | Behavior |
|-------------------|----------|
| `orders_cache` | Cache **on** — proxy uses real collection `orders`, default TTL **60s** (overridable) |
| `orders` | Cache **off** — always MongoDB |

Policy in the control plane sets **default TTL** and optional **per-collection overrides** (real names like `mydb.orders`). Caching is **not** gated on an “enabled” flag anymore; the `_cache` suffix is the opt-in.

Cached results are dropped only when **TTL expires** or via **manual invalidation** (`POST /tenants/{id}/invalidate` or the dashboard). **Writes do not invalidate** the collection cache.

### Roles (organizations)

| Role | Dashboard / API |
|------|------------------|
| **member** | Read-only |
| **admin** | Manage settings; cannot delete org |
| **owner** | Full control; delete org requires email verification code |

### Invite-only (self-host)

```bash
export NANCE_INVITE_ONLY=true
```

Users cannot create organizations; they join via invite only. Bootstrap tenants with `NANCE_ADMIN_TOKEN` via `POST /api/v1/tenants`. See `GET /api/v1/platform`.

## Quick start (local)

### 1. Infrastructure

```bash
make dev-up
```

Starts Postgres (`:5432`), MongoDB (`:27017`), Redis (`:6379`) via `docker-compose.yml`.

### 2. Control plane (`:8080`)

```bash
export NANCE_MASTER_KEY="thisisexactly32byteslongforaes256!!"   # 32-byte key or base64
# Optional:
# export NANCE_ADMIN_TOKEN=supersecret
# export NANCE_INVITE_ONLY=true
# export NANCE_REQUIRE_USER_AUTH=1   # require real auth even if admin token unset
# export NANCE_REDIS_ADDR=localhost:6379
make run
# or: go run ./cmd/controlplane
```

Migrations under `migrations/` apply on startup (simple file runner).

### 3. Seed demo tenant + token (dev)

```bash
make seed
```

Uses admin bearer (`NANCE_ADMIN_TOKEN` or `dev` in the Makefile). Seed sets the demo backend, then issues proxy access. Copy `proxyConnectionUri` (or `rawToken`) — shown only once. Access requires a configured connection.

### 4. Proxy (`:27018`, health `:9090`)

```bash
export NANCE_MASTER_KEY="thisisexactly32byteslongforaes256!!"
export DATABASE_URL="postgres://nance:nance@localhost:5432/nance?sslmode=disable"
export NANCE_REDIS_ADDR=localhost:6379
export NANCE_CACHE_ENABLED=true
make run-proxy
# or: go run ./cmd/proxy
```

Compose Mongo owns `:27017`; proxy defaults to **`:27018`**. Override with `NANCE_PROXY_LISTEN`.

### 5. Connect through the proxy

**PLAIN only** (no SCRAM yet). Prefer the **`proxyConnectionUri`** returned when creating access for a connection. Equivalent manual form (username = tenant id, password = raw token):

```text
mongodb://demo:<rawToken>@127.0.0.1:27018/?authMechanism=PLAIN&authSource=$external
```

Set `NANCE_PROXY_PUBLIC_ENDPOINT` on the control plane so issued URIs use your real proxy host (default `127.0.0.1:27018`).

## Important environment variables

Both **control plane** and **proxy** load optional `.env` then `.env.local` from the **process working directory** at startup (missing files are ignored). Variables already set in the shell/container always win.

### Control plane (`cmd/controlplane`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `NANCE_MASTER_KEY` | (required for backends) | AES key for encrypting tenant Mongo URIs |
| `DATABASE_URL` | `postgres://nance:nance@localhost:5432/nance?sslmode=disable` | Postgres |
| `NANCE_ADMIN_TOKEN` | (empty = open / legacy dev mode unless restricted) | Platform admin bearer |
| `NANCE_INVITE_ONLY` | `false` | Users cannot create orgs; join via invite only |
| `NANCE_REQUIRE_USER_AUTH` | unset | If `1`, require session/admin token even when admin token is empty |
| `NANCE_PROXY_PUBLIC_ENDPOINT` | `127.0.0.1:27018` | Host[:port] embedded in issued `proxyConnectionUri` values and `GET /platform` |
| `PORT` | `8080` | HTTP listen (host uses `PORT`; bind is `:`+port) |
| `MIGRATIONS_DIR` | `./migrations` | SQL migrations |
| `NANCE_REDIS_ADDR` | | Redis `host:port` **or** full URL `redis://user:pass@host:port` / `rediss://…` (TLS) |
| `NANCE_REDIS_PASSWORD` | | Password when using `host:port` form (optional if embedded in URL) |

### Proxy (`cmd/proxy`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `NANCE_MASTER_KEY` | (required) | Decrypt backend URIs |
| `DATABASE_URL` | same as CP | Token + backend + policy lookup |
| `NANCE_PROXY_LISTEN` | `:27018` | Mongo wire TCP listen |
| `NANCE_PROXY_HEALTH_LISTEN` | `:9090` | `/healthz`, `/readyz`, `/metrics`, `/cache-stats` (per-collection hit/miss ratios, process-local) |
| `NANCE_REDIS_ADDR` | `localhost:6379` | Redis `host:port` or URL (`redis://` / `rediss://` for managed TLS) |
| `NANCE_REDIS_PASSWORD` | | Optional when not using a URL with password |
| `NANCE_CACHE_ENABLED` | | Enable cache path when Redis is configured |
| `NANCE_POLICY_REFRESH_INTERVAL` | `30s` | Reload cache policies from Postgres |
| `NANCE_PROXY_MAX_CONNS_PER_TENANT` | `200` | Soft limit client TCP conns per tenant |
| `NANCE_PROXY_BACKEND_MAX_POOL` | `50` | Driver pool toward real Mongo |
| `NANCE_PROXY_BACKEND_IDLE_TIMEOUT` | `15m` | Evict unused per-tenant `mongo.Client` after this idle period (`0` disables) |
| `NANCE_PROXY_BACKEND_IDLE_EVICT_INTERVAL` | `1m` | How often the idle reaper runs |
| `NANCE_PROXY_CURSOR_IDLE_TIMEOUT` | `10m` | Prune idle cursor state |
| `NANCE_PROXY_ALLOW_UNAUTH` | `false` | Dev only |

## Control plane API (summary)

Base path: **`/api/v1`**. Health: `/healthz`, `/readyz`, `/metrics`.

### Public

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/platform` | `{ inviteOnly, allowOrgCreation, allowAdminBootstrap, proxyPublicEndpoint }` |
| `POST` | `/auth/request-code` | `{ "email" }` — send OTP (dev: log mailer) |
| `POST` | `/auth/verify` | `{ "email", "code" }` → `{ token, user }` |

### Authenticated (user session **or** `NANCE_ADMIN_TOKEN`)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/auth/logout` | Invalidate session |
| `GET` / `PATCH` | `/me` | Current user; patch `{ "name" }` (onboarding) |
| `GET` / `POST` | `/me/organizations` | List / create org (create blocked if invite-only) |
| `GET` | `/me/invites` | Pending invites for user email |
| `POST` | `/me/invites/{id}/accept` | Accept invite |
| `GET` / `POST` | `/tenants` | List (membership-scoped) / create |
| `GET` | `/tenants/{id}` | Tenant + `role`, `canManage`, `canDelete` |
| `POST` | `/tenants/{id}/delete/request-code` | Owner: email delete confirmation code |
| `POST` | `/tenants/{id}/delete/confirm` | Owner: `{ "code" }` — cascade delete org |
| `GET` | `/tenants/{id}/members` | List members |
| `POST` / `GET` | `/tenants/{id}/invites` | Invite / list pending (admin+) |
| `DELETE` | `/tenants/{id}/invites/{inviteId}` | Revoke invite |
| `DELETE` | `/tenants/{id}/members/{userId}` | Remove member |
| `GET` / `POST` | `/tenants/{id}/connections` | List / create named source connections (`{ name, uri }`) |
| `GET` / `PUT` / `DELETE` | `/tenants/{id}/connections/{connectionId}` | Get / update (`name` and/or `uri`) / delete |
| `POST` | `/tenants/{id}/connections/{connectionId}/test` | Connectivity test |
| `GET` / `POST` | `/tenants/{id}/connections/{connectionId}/tokens` | List / create proxy access (returns `proxyConnectionUri` once) |
| `DELETE` | `/tokens/{tokenId}` | Revoke proxy access |
| `GET` | `/tenants/{id}/connections/{connectionId}/policy` | Cache policy for this connection |
| `PUT` | `/tenants/{id}/connections/{connectionId}/policy/defaults` | `{ "defaultTtlSeconds" }` |
| `PUT` | `/tenants/{id}/connections/{connectionId}/policy/collections/{db.coll}` | Per-collection TTL override |
| `POST` | `/tenants/{id}/connections/{connectionId}/invalidate` | Flush cache namespace/tags for this connection |
| `GET` | `/tenants/{id}/savings` | Metrics hints |

**Membership:** members = read; admins/owners = writes; only owners delete org (with email code). Platform admin token bypasses membership for ops/bootstrap.

## Build & test

```bash
make build-all
make test
make lint   # if configured

# Live proxy caching against compose Mongo + Redis (starts proxy in-process):
make dev-up
make test-integration
```

## Docker / K8s

- `Dockerfile.controlplane`, `Dockerfile.proxy`
- `docker-compose.yml` for local deps
- `deploy/k8s/` — sample proxy deployment and multi-region notes

## Security notes

- Real Mongo URIs are **never** stored in plaintext; encrypted with `NANCE_MASTER_KEY`.
- Proxy access: bcrypt hash in DB; **raw token + `proxyConnectionUri` returned once** at issuance (requires configured connection).
- Email OTP sessions are SHA-256 hashed in `user_sessions`.
- Prefer invite-only + admin token on shared networks; terminate TLS at ingress.

## Known limitations

- Proxy auth: **PLAIN only** (set `authMechanism=PLAIN&authSource=$external`).
- Not a real replica set topology (`hello` reports a single primary).
- Cursor mapping is strongest for `find` / `aggregate` via driver helpers.
- Legacy opcodes beyond minimal `OP_QUERY` isMaster are rejected.

## Makefile targets

| Target | Action |
|--------|--------|
| `dev-up` / `dev-down` | Compose infra up / down |
| `run` / `run-proxy` | Build + run control plane / proxy |
| `build` / `build-proxy` / `build-all` | Binaries under `bin/` |
| `seed` | Demo tenant + backend + token via curl |
| `test` | Go tests |

## Related apps

- [Admin dashboard](../admin-dashboard/README.md) — Nuxt UI on this API  
- [benchmark](../benchmark/README.md) — Locust load tests (cache vs bypass)  
- [Monorepo root](../../README.md)
