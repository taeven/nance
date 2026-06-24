# Nance Accelerator

MongoDB accelerator control plane (Phase 0) + passthrough proxy data plane (Phase 1).

See [../../phase0.md](../../phase0.md) and [../../phase1.md](../../phase1.md) for plans and success criteria.

## Quick Start (Local Development)

### 1. Infrastructure

```bash
make dev-up
```

Starts Postgres (`:5432`), real MongoDB (`:27017`), and Redis (`:6379`).

### 2. Control plane (HTTP API on `:8080`)

```bash
export NANCE_MASTER_KEY="thisisexactly32byteslongforaes256!!"   # 32 bytes (or base64)
# Optional: export NANCE_ADMIN_TOKEN=supersecret   (if unset, dev allows all requests)
make run
# or: go run ./cmd/controlplane
```

### 3. Seed demo tenant + token

```bash
make seed
```

Copy the `rawToken` from the response — it is only shown once.

### 4. Data plane proxy (Mongo wire on `:27018`, health on `:9090`)

```bash
export NANCE_MASTER_KEY="thisisexactly32byteslongforaes256!!"
export DATABASE_URL="postgres://nance:nance@localhost:5432/nance?sslmode=disable"
make run-proxy
# or: go run ./cmd/proxy
```

**Port note**: compose Mongo owns `:27017`; the proxy defaults to `:27018` so both can run locally. Override with `NANCE_PROXY_LISTEN=:27017` if nothing else is on that port.

### 5. Connect through the proxy

**Phase 1 requires `authMechanism=PLAIN`** (SCRAM is not implemented yet).

Username = tenant id (`demo`), password = the `rawToken` from step 3.

```bash
# mongosh
mongosh "mongodb://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=%24external"

# Then normal MongoDB operations:
# > db.users.insertOne({ name: "alice" })
# > db.users.find().toArray()
```

Node.js example:

```js
const { MongoClient } = require("mongodb");
const uri =
  "mongodb://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=$external";
const client = new MongoClient(uri);
await client.connect();
await client.db("mydb").collection("users").insertOne({ name: "alice" });
console.log(await client.db("mydb").collection("users").find().toArray());
```

Python (pymongo):

```python
from pymongo import MongoClient
uri = "mongodb://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=$external"
client = MongoClient(uri)
client.mydb.users.insert_one({"name": "alice"})
print(list(client.mydb.users.find()))
```

Go (official driver):

```go
uri := "mongodb://demo:<rawToken>@127.0.0.1:27018/mydb?authMechanism=PLAIN&authSource=$external"
client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
```

The `mongodb+nance://` scheme from the architecture doc is the product-facing form; most drivers need `mongodb://` (or a driver option that accepts a custom scheme). Functionally the credentials and host are what matter.

## Architecture (what Phase 1 delivers)

```
App / mongosh / Compass
        │  OP_MSG (hello, saslStart PLAIN, find, insert, …)
        ▼
 Nance Proxy (:27018)  ──reads──►  Postgres (tokens, encrypted backend URIs)
        │  decrypts URI with NANCE_MASTER_KEY
        │  one pooled mongo.Client per tenant
        ▼
 Tenant's real MongoDB (:27017 in local compose)
```

- **No caching** in Phase 1 (that is Phase 2 / Redis).
- **Tenant isolation**: after PLAIN auth, each TCP connection is bound to one tenant; backend clients are never shared across tenants.
- **Connection pooling**: many app-side connections collapse to a small driver pool per tenant on the real cluster.
- **Topology lie**: `hello` / `isMaster` always reports a single writable primary (no replica-set host list).

## Important Environment Variables

### Control plane (`cmd/controlplane`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `NANCE_MASTER_KEY` | (required for backends) | 32-byte AES key for encrypting real Mongo URIs |
| `DATABASE_URL` | `postgres://nance:nance@localhost:5432/nance?sslmode=disable` | Postgres |
| `NANCE_ADMIN_TOKEN` | (empty = open in dev) | Bearer for `/api/v1/*` |
| `PORT` | `8080` | HTTP listen |
| `MIGRATIONS_DIR` | `./migrations` | SQL migrations |

### Proxy (`cmd/proxy`)

| Variable | Default | Purpose |
|----------|---------|---------|
| `NANCE_MASTER_KEY` | (required) | Decrypt backend URIs |
| `DATABASE_URL` | same as above | Token + backend lookup |
| `NANCE_PROXY_LISTEN` | `:27018` | Mongo wire TCP listen |
| `NANCE_PROXY_HEALTH_LISTEN` | `:9090` | `/healthz`, `/readyz`, `/metrics` |
| `NANCE_PROXY_MAX_CONNS_PER_TENANT` | `200` | Soft limit on client TCP conns per tenant |
| `NANCE_PROXY_BACKEND_MAX_POOL` | `50` | Driver `MaxPoolSize` toward real Mongo |
| `NANCE_PROXY_BACKEND_MIN_POOL` | `0` | Driver `MinPoolSize` |
| `NANCE_PROXY_BACKEND_CONNECT_TIMEOUT` | `10s` | Backend connect/selection timeout |
| `NANCE_PROXY_CURSOR_IDLE_TIMEOUT` | `10m` | Prune idle server-side cursor state |
| `NANCE_PROXY_ALLOW_UNAUTH` | `false` | Dev only: allow commands without auth |

## Control plane API (Phase 0, all under `/api/v1`, Bearer protected)

- `POST   /tenants` — create tenant `{ "id": "proj_abc", "name": "My Project" }`
- `GET    /tenants/{tenantId}`
- `GET    /tenants`
- `POST   /tenants/{tenantId}/backend` — `{ "uri": "mongodb://real..." }` (stored encrypted)
- `POST   /tenants/{tenantId}/backend/test` — validates connectivity (no URI leaked)
- `GET    /tenants/{tenantId}/policy`
- `PUT    /tenants/{tenantId}/policy/collections/{db.coll}` — per-collection caching rules (used in Phase 2)
- `PUT    /tenants/{tenantId}/policy/defaults`
- `POST   /tenants/{tenantId}/tokens` — returns `{ "rawToken": "...", "tokenId": "..." }` (copy rawToken)
- `GET    /tenants/{tenantId}/tokens`
- `DELETE /tokens/{tokenId}`

Health: `/healthz`, `/readyz`, `/metrics` on the control plane port; proxy exposes the same paths on `NANCE_PROXY_HEALTH_LISTEN`.

## Security Notes

- Real MongoDB connection strings are **never** stored in plaintext and never sent to clients.
- They are encrypted at rest with AES-256-GCM using `NANCE_MASTER_KEY`.
- Data-plane tokens are returned raw **once**; only a bcrypt hash is stored. The proxy validates the raw token via bcrypt on `saslStart` (PLAIN).
- One TCP connection = one tenant after successful auth.

## Known limitations (Phase 1)

- **PLAIN only** — no SCRAM-SHA-256. Clients must set `authMechanism=PLAIN&authSource=$external`.
- **Not a real replica set** — no `hosts` / `setName`; secondary targeting and change-stream edge cases may fail.
- **Cursor mapping** is implemented for `find` / `aggregate` via the Go driver; other cursor-producing paths go through `RunCommand` and may not support multi-batch `getMore` through the proxy.
- **No Redis / caching** — all reads and writes pass through to the backend.
- Legacy opcodes other than minimal `OP_QUERY` isMaster are rejected (modern drivers use `OP_MSG`).

## Build & test

```bash
make build-all
make test
make lint
```

## Next

Phase 2 layers Redis read-through caching on top of this passthrough path (see [../../phase2.md](../../phase2.md)).
