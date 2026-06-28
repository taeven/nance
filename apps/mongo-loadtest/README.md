# mongo-loadtest

Extreme MongoDB **read / write throughput** load tester for [Nance](../../README.md).

Point it at any MongoDB connection string — **Atlas**, self-hosted MongoDB, or a **Nance accelerator proxy** URI — and it hammers a collection with concurrent readers and writers, then writes a **stats report** (JSON + Markdown) under `results/`.

Useful for:

- Baseline capacity of a cluster  
- Comparing **direct Mongo** vs **proxy** (with/without `_cache` usage in your app)  
- Finding a **breaking point** (error rate / p99 / success thresholds under ramp concurrency)

Also see repo skill docs under `.grok/skills/` (`run-loadtest`, `analyze-results`, `compare-runs`) if you use Grok workflows.

## Features

| Capability | Description |
|------------|-------------|
| Modes | `read`, `write`, `mixed`, `ramp` (increasing concurrency until break) |
| Connection | Standard MongoDB URI (`mongodb://` / `mongodb+srv://`) |
| Seeding | Auto-seeds the collection when empty so reads have data |
| Breaking point | Stops ramp (or flags constant run) when thresholds trip |
| Reports | JSON + Markdown under `results/` after every run |
| Safe defaults | Credentials redacted in reports; opt-in collection drop |

## Requirements

- **Go 1.22+**
- Network access to the target MongoDB (or Nance proxy)
- A URI with privileges to read/write the chosen database & collection

## Quick start

```bash
cd apps/mongo-loadtest
go mod tidy

# Mixed read+write for 60s (default)
export MONGO_URI='mongodb+srv://user:pass@cluster.example.mongodb.net/'
go run ./cmd/loadtest -uri "$MONGO_URI" -db loadtest -collection loadtest_docs
```

### Against the Nance proxy (local)

Proxy requires **PLAIN** auth: username = tenant id, password = proxy token.

```bash
export MONGO_URI='mongodb://demo:<rawToken>@127.0.0.1:27018/loadtest?authMechanism=PLAIN&authSource=$external'
go run ./cmd/loadtest -uri "$MONGO_URI" -db loadtest -collection loadtest_docs
```

To exercise the **cache path**, your application (not necessarily this tool) should query `collection_cache`; this load tester talks to whatever collection name you pass with `-collection`.

## Common flags

Run `go run ./cmd/loadtest -h` for the full list. Typical knobs:

| Flag | Purpose |
|------|---------|
| `-uri` | MongoDB URI (or `MONGO_URI` env) |
| `-db` / `-collection` | Target namespace |
| `-mode` | `read`, `write`, `mixed`, `ramp` |
| `-concurrency` / duration flags | Load shape |
| Ramp / threshold flags | Breaking-point behavior |

Build a binary:

```bash
go build -o bin/mongo-loadtest ./cmd/loadtest
./bin/mongo-loadtest -uri "$MONGO_URI" -db loadtest -collection loadtest_docs
```

## Reports

After each run, inspect:

- `results/loadtest-*.json` — machine-readable metrics  
- `results/loadtest-*.md` — human summary (throughput, percentiles, phases, verdict)

Compare two runs (e.g. proxy vs direct) with your preferred diff tooling or the Grok `compare-runs` skill.

## Agent / contributor notes

See [`AGENTS.md`](./AGENTS.md) for package layout and extension rules when changing the load tester.

## Related

- [Nance monorepo](../../README.md)  
- [Accelerator proxy](../accelerator/README.md) — data plane under test  
- [Admin dashboard](../admin-dashboard/README.md) — issue tokens and configure tenants  
