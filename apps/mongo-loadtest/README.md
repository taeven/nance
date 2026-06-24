# mongo-loadtest

Extreme MongoDB **read / write throughput** load tester for Nance.

Point it at any MongoDB connection string (Atlas, self-hosted, or a Nance accelerator proxy URI) and it will hammer a collection with concurrent readers and writers, then emit a **load testing stats report** with:

- Peak and average **read / write ops/s** and **docs/s**
- Latency percentiles (p50 / p95 / p99)
- Error rates per phase
- A **breaking point** snapshot when the database (or path to it) fails configured thresholds — e.g. sustained errors or latency blow-ups at a given concurrency and cumulative request count (`~1M reads / ~300k writes`)

## Features

| Capability | Description |
|---|---|
| Modes | `read`, `write`, `mixed`, `ramp` (increasing concurrency until break) |
| Connection | Standard MongoDB URI (`mongodb://` / `mongodb+srv://`) |
| Seeding | Auto-seeds the collection when empty so reads have data |
| Breaking point | Stops ramp (or flags constant run) when error rate / p99 / success rate thresholds trip |
| Reports | JSON + Markdown under `results/` after every run |
| Safe defaults | Credentials redacted in reports; opt-in collection drop |

## Requirements

- Go **1.22+**
- Network access to the target MongoDB deployment
- A connection string with privileges to read/write the chosen database & collection

## Quick start

```bash
cd apps/mongo-loadtest

# Install deps
go mod tidy

# Mixed read+write for 60s (default)
export MONGO_URI='mongodb+srv://user:pass@cluster.example.mongodb.net/'
go run ./cmd/loadtest -uri "$MONGO_URI" -db loadtest -collection loadtest_docs

# Or pass URI on the CLI
go run ./cmd/loadtest \
  -uri 'mongodb://localhost:27017' \
  -mode mixed \
  -duration 2m \
  -read-concurrency 200 \
  -write-concurrency 100
```

Build a binary:

```bash
go build -o bin/mongo-loadtest ./cmd/loadtest
./bin/mongo-loadtest -uri "$MONGO_URI" -mode ramp
```

## Workload modes

### `write`

Insert-only. Measures pure write throughput (`InsertMany` batches).

```bash
go run ./cmd/loadtest -uri "$MONGO_URI" -mode write \
  -write-concurrency 200 -write-batch 50 -duration 90s
```

### `read`

Read-only. Seeds the collection first if it has fewer than `-seed` documents, then runs `Find` / `FindOne` workers.

```bash
go run ./cmd/loadtest -uri "$MONGO_URI" -mode read \
  -read-concurrency 500 -read-batch 20 -seed 50000 -duration 90s
```

### `mixed` (default)

Concurrent readers **and** writers. Closest to real application pressure.

```bash
go run ./cmd/loadtest -uri "$MONGO_URI" -mode mixed \
  -read-concurrency 300 -write-concurrency 100 -duration 2m
```

### `ramp` (find the breaking point)

Starts at `-ramp-start` workers and increases by `-ramp-step` each `-ramp-step-duration` until `-ramp-max` **or** a breaking threshold trips. This is the mode to use when you want an explicit “DB broke at X reads / Y writes” answer.

```bash
go run ./cmd/loadtest -uri "$MONGO_URI" -mode ramp \
  -ramp-start 20 \
  -ramp-step 40 \
  -ramp-max 2000 \
  -ramp-step-duration 20s \
  -max-error-rate 0.05 \
  -max-p99 2s \
  -min-success-rate 0.90
```

## CLI flags

| Flag | Env | Default | Description |
|---|---|---|---|
| `-uri` | `MONGO_URI` | _(required)_ | MongoDB connection string |
| `-db` | `MONGO_DB` | `loadtest` | Database name |
| `-collection` | `MONGO_COLLECTION` | `loadtest_docs` | Collection name |
| `-mode` | | `mixed` | `read` \| `write` \| `mixed` \| `ramp` |
| `-duration` | | `60s` | Length of constant-load phases |
| `-warmup` | | `5s` | Warmup before measurement (constant modes) |
| `-read-concurrency` | | `100` | Concurrent read workers |
| `-write-concurrency` | | `50` | Concurrent write workers |
| `-read-batch` | | `10` | Docs fetched per `Find` |
| `-write-batch` | | `10` | Docs inserted per `InsertMany` |
| `-seed` | | `10000` | Min docs before read/mixed/ramp |
| `-doc-size` | | `1024` | Approx payload bytes per written doc |
| `-ramp-start` | | `10` | Ramp starting workers (read side) |
| `-ramp-max` | | `2000` | Ramp maximum workers |
| `-ramp-step` | | `50` | Workers added each ramp step |
| `-ramp-step-duration` | | `15s` | Duration of each ramp step |
| `-max-error-rate` | | `0.05` | Break if error fraction exceeds this |
| `-max-p99` | | `2s` | Break if successful-op p99 exceeds this |
| `-min-success-rate` | | `0.90` | Break if success fraction falls below this |
| `-output` | | `results` | Directory for report files |
| `-run-id` | | UTC timestamp | Run identifier used in report filenames |
| `-drop` | | `false` | Drop the target collection before the run |
| `-keep-data` | | `true` | Retain documents after the run |

## Output / stats report

When a run finishes (or is interrupted with Ctrl+C after partial progress), two files are written:

```
results/loadtest-<run-id>.json
results/loadtest-<run-id>.md
```

### What the report contains

1. **Peak throughput** — best observed read/write ops/s and docs/s across phases  
2. **Totals** — cumulative ops/docs/errors and average rates over measurement time  
3. **Per-phase breakdown** — workers, ops/s, latency percentiles, error rate  
4. **Breaking point** (if thresholds tripped):
   - Phase name and timestamp
   - Read / write worker counts at failure
   - **Cumulative read ops / write ops** at the break (e.g. `1,048,576 (1.05M)` reads / `312,400 (312.4K)` writes)
   - Sustained ops/s and p99 at the break
   - Human-readable reason (error rate, latency, success rate)

Example console footer:

```
========== LOAD TEST COMPLETE ==========
Run ID:        20260624T120501Z
Verdict:       breaking_point_detected
Peak read:     18420.3 ops/s  |  92010.1 docs/s
Peak write:    4120.8 ops/s   |  41208.0 docs/s
BREAKING POINT DETECTED
  Breaking point at phase "ramp-r520w260": ~1200 read ops/s (workers=520), ...
  At ~1.05M read ops / ~312.4K write ops cumulative
JSON report:   results/loadtest-20260624T120501Z.json
Markdown:      results/loadtest-20260624T120501Z.md
========================================
```

## How breaking point detection works

A phase is marked **broken** when any of these hold on a measurement phase (not warmup/seed):

- `error_rate > -max-error-rate` (default 5%)
- `success_rate < -min-success-rate` (default 90%)
- successful-op **p99 latency** `> -max-p99` (default 2s) for reads or writes

In **`ramp` mode**, the runner stops adding concurrency once a break is recorded.  
In constant modes (`read` / `write` / `mixed`), the phase is still completed but the report flags the break if thresholds were exceeded during measurement.

> Note: “Breaking point” here means **the load tester’s thresholds were breached** — connection errors, timeouts, elevated latency, or command failures. It is an application-level signal that the deployment (or network path) could not sustain that throughput under the chosen workload shape.

## Extreme / stress recipes

Push hard (use responsibly — this can saturate Atlas tiers, disks, and connection limits):

```bash
# Aggressive mixed constant load
go run ./cmd/loadtest -uri "$MONGO_URI" -mode mixed \
  -read-concurrency 1000 -write-concurrency 400 \
  -read-batch 50 -write-batch 100 \
  -doc-size 4096 -duration 5m -seed 200000

# Ramp until it breaks (long running)
go run ./cmd/loadtest -uri "$MONGO_URI" -mode ramp \
  -ramp-start 50 -ramp-step 50 -ramp-max 5000 \
  -ramp-step-duration 30s \
  -read-batch 25 -write-batch 25 \
  -max-error-rate 0.02 -max-p99 1s
```

Against a **Nance accelerator proxy**, use the proxy’s MongoDB listen URI as `-uri` to compare cached vs direct backend throughput.

## Project layout

```
apps/mongo-loadtest/
├── README.md                 # this file
├── go.mod
├── cmd/loadtest/main.go      # CLI entrypoint
├── internal/
│   ├── config/config.go      # flags & env
│   ├── runner/runner.go      # workers, seeding, ramp/constant phases
│   └── stats/stats.go        # collectors, breaking point, JSON/MD reports
└── results/                  # default report output (gitignored contents optional)
```

## Safety notes

- Prefer a **dedicated database/collection** (`loadtest` / `loadtest_docs` by default). Do **not** point this at production application collections unless you accept data growth and load.
- `-drop` permanently deletes the target collection — off by default.
- URI passwords are **redacted** in written reports, but the process environment and shell history may still contain secrets.
- Connection pool size is scaled from concurrency; extremely high worker counts can exhaust MongoDB `maxIncomingConnections` or Atlas connection quotas — that is often exactly the breaking point you are looking for.

## License

Part of the Nance monorepo. Use under the same terms as the parent project.
