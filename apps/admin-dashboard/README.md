# Nance Admin Dashboard

Nuxt 3 admin UI for the [Nance accelerator control plane](../accelerator/README.md).

Operators and team members sign in with **email + one-time code**, manage **organizations** (tenants), **members/invites**, MongoDB **backends**, **cache TTL**, **proxy tokens**, and **invalidation**. Part of the [Nance monorepo](../../README.md).

## Features

| Area | What you can do |
|------|-----------------|
| **Auth** | Email OTP login/signup; **name** collected on **onboarding** after verify |
| **Organizations** | List memberships, create (unless invite-only), accept invites |
| **Roles** | **member** read-only · **admin** manage settings · **owner** + delete org |
| **Members** | Invite by email (role limits by inviter), revoke invites, remove members |
| **Connection** | Set encrypted backend URI; test connectivity |
| **Caching** | Default **60s** TTL for all `*_cache` queries; optional per-collection overrides |
| **Tokens** | Issue (raw secret once), list, revoke (admin+) |
| **Invalidate** | Explicit cache flush by real db/collection/tags (admin+) |
| **Danger zone** | Owners only: delete org with email verification code |
| **Platform** | Respects `GET /api/v1/platform` (`inviteOnly` / `allowOrgCreation`) |

### How caching works

- Clients opt in **per query** with a `_cache` suffix: `db.orders_cache.find(...)` → real `orders` + Redis (default **60s**).
- `db.orders.find(...)` always hits MongoDB.
- This UI does **not** toggle caching on/off per collection. It sets **default TTL** and optional **overrides** using the **real** collection name (`mydb.orders`, not `mydb.orders_cache`).
- Writes to the real collection invalidate that namespace (via proxy).

### Invite-only instances

If the control plane runs with `NANCE_INVITE_ONLY=true`:

- Login still works.
- **Create organization** is hidden/disabled.
- Empty state explains that an invite is required.
- Login page shows a short invite-only notice.

## Prerequisites

1. Accelerator **control plane** running (default `http://localhost:8080`) — see [accelerator README](../accelerator/README.md).
2. **Node.js 20+** and npm.

## Setup

```bash
cd apps/admin-dashboard
npm install

# Server-only (Nuxt runtimeConfig) — not exposed to the browser:
export NANCE_ACCELERATOR_URL=http://localhost:8080
# Optional fallback when no user session is forwarded (prefer real login):
# export NANCE_ADMIN_TOKEN=supersecret

npm run dev
```

Open [http://localhost:3000](http://localhost:3000).

1. Enter email → continue.  
2. Enter the **6-digit code** (control plane **logs** it when using the default log mailer).  
3. Onboarding: set your **name**.  
4. Create an organization **or** accept an invite (invite-only servers: invite only).

### Environment

| Variable | Default | Description |
|----------|---------|-------------|
| `NANCE_ACCELERATOR_URL` | `http://localhost:8080` | Control plane base URL (**server-only**) |
| `NANCE_ADMIN_TOKEN` | _(empty)_ | Optional platform admin bearer (**server-only** fallback) |

Configured in `nuxt.config.ts` as `runtimeConfig.acceleratorBaseUrl` / `acceleratorAdminToken`.

## Architecture

```
Browser  →  Nuxt server routes (/api/…)  →  Accelerator /api/v1/…
              Authorization: Bearer <user session>
              (or NANCE_ADMIN_TOKEN if no user header)
```

Session token is stored in **localStorage** (`nance_session_token`) after verify; composable `useAuth` + `useAcceleratorApi` attach it on client calls. Server proxies prefer the incoming `Authorization` header so the user identity is preserved.

### Main routes

| UI route | Purpose |
|----------|---------|
| `/login` | Email + OTP |
| `/onboarding` | Display name (required once) |
| `/` | Organizations + pending invites |
| `/tenants/:id` | Org detail: connection, caching, tokens, members, invalidate, danger zone |

## Scripts

```bash
npm run dev      # http://localhost:3000
npm run build    # production build
npm run preview  # preview production build
```

## Local workflow example

1. Start accelerator infra + control plane (+ proxy if testing data plane).  
2. `npm run dev` in this app; sign in.  
3. Create an organization (or accept invite).  
4. **Connection**: set Mongo URI; test.  
5. **Caching**: confirm default TTL (60s) or set overrides.  
6. **Tokens**: issue proxy token; use PLAIN URI against the proxy.  
7. **Members**: invite teammates; they log in with the invited email.  
8. **Owner**: Danger zone → acknowledge data loss → email code → confirm delete.

## Security notes

- Backend URIs are write-only from the UI; never returned by the API.  
- Proxy tokens appear **once** — copy immediately.  
- Prefer user sessions over a long-lived admin token in production.  
- Run the dashboard only on trusted networks or behind auth/TLS at the edge.

## Related

- [Accelerator (API + proxy)](../accelerator/README.md)  
- [mongo-loadtest](../mongo-loadtest/README.md)  
- [Monorepo root](../../README.md)
