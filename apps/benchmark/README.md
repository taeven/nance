# benchmark

Python **[Locust](https://locust.io/)** load tests for MongoDB and the **Nance accelerator proxy**.

Use it to set throughput/latency baselines and to compare:

- **Bypass** — reads on the real collection (`db.orders`)
- **Cache** — reads on `db.orders_cache` (proxy Redis path)
- **Direct Mongo** vs **proxy** (change only `MONGO_URI`)

**Published heavy-load A/B (direct Atlas vs proxy cache):** see [`benchmark_report.md`](./benchmark_report.md).

## Layout

```
apps/benchmark/
  locustfile.py              # Locust entry (user classes + tasks)
  nance_benchmark/           # settings + pymongo helpers
  scripts/seed.py            # seed real collection before reads
  requirements.txt
  .env.example
  benchmark_report.md        # durable heavy-load report (direct vs cache)
  results/                   # optional Locust CSV/HTML exports
```

## Setup

```bash
cd apps/benchmark
python3 -m venv .venv
source .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt
cp .env.example .env
# edit .env — set MONGO_URI
```

### Nance proxy URI

From the admin dashboard **Connection → Proxy access → Create access**, copy the URI. It should look like:

```text
mongodb://<orgId>:<token>@host:27018/?authMechanism=PLAIN&authSource=$external&directConnection=true
```

Put that in `MONGO_URI`.

## Seed data (once)

Reads need documents on the **real** collection:

```bash
python scripts/seed.py
```

## Run Locust

### Web UI (interactive)

```bash
locust -f locustfile.py
# open http://localhost:8089
# pick users/spawn rate; select user class if prompted
```

### Headless — cache vs bypass (equal mix)

```bash
# CompareUser: 50/50 find_bypass vs find_cache
locust -f locustfile.py CompareUser \
  --headless -u 50 -r 10 -t 2m \
  --csv=results/compare --html=results/compare.html
```

### Headless — bypass only (or direct Mongo)

```bash
locust -f locustfile.py BypassUser \
  --headless -u 100 -r 20 -t 3m \
  --csv=results/bypass --html=results/bypass.html
```

### Headless — cache only (proxy)

```bash
locust -f locustfile.py CacheUser \
  --headless -u 100 -r 20 -t 3m \
  --csv=results/cache --html=results/cache.html
```

### Mixed traffic (cache reads + real writes)

```bash
locust -f locustfile.py MixedUser \
  --headless -u 80 -r 15 -t 5m \
  --csv=results/mixed --html=results/mixed.html
```

### Write-only

```bash
locust -f locustfile.py WriteUser \
  --headless -u 40 -r 10 -t 2m \
  --csv=results/write --html=results/write.html
```

In the Locust UI / CSV, compare request names:

| Name | Meaning |
|------|---------|
| `find_bypass` | Real collection read |
| `find_cache` | `*_cache` read (Nance path) |
| `insert_real` | Insert into real collection |

## Environment

| Variable | Default | Purpose |
|----------|---------|---------|
| `MONGO_URI` | required | Direct or proxy connection string |
| `MONGO_DB` | `loadtest` | Database |
| `MONGO_COLLECTION` | `loadtest_docs` | **Real** collection (no `_cache` suffix) |
| `DOC_SIZE` | `512` | Write/seed payload bytes |
| `SEED_COUNT` | `5000` | Target docs for `scripts/seed.py` |
| `MONGO_DIRECT` | auto | Force `directConnection` (auto-on for PLAIN proxy URIs) |

## Interpreting results

1. Run **CompareUser** (or separate BypassUser / CacheUser runs with the same `-u` / `-t`).
2. In Locust stats, look at **RPS** and **p50/p95/p99** for `find_cache` vs `find_bypass`.
3. On a healthy proxy with warm cache, `find_cache` should show higher RPS and lower latency.
4. For **proxy overhead**, run BypassUser against proxy URI and again against direct Atlas/self-hosted URI.

## Notes

- Writes never use `*_cache`; that matches the accelerator (cache is read opt-in only).
- Do not point at production app collections unless you accept the load risk.
- Extreme `-u` can exhaust connection quotas on Atlas and on the proxy.

## Related

- [Accelerator proxy](../accelerator/README.md)
- [Admin dashboard](../admin-dashboard/README.md) — connections, TTL, tokens
- [Nance monorepo](../../README.md)
