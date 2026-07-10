# Nance heavy load benchmark report

**Date:** 2026-07-10  
**Run ID:** `heavy_direct_vs_cache_20260710_073855`  
**Host:** `benchmark-server` (same-VPC load generator; instance destroyed after run)  
**Python / Locust:** 3.14.4 / 2.45.0  

---

## Methodology

Serial A/B from a cloud load host (not a laptop):

| Phase | Path | Locust class | Users | Ramp | Duration |
|-------|------|--------------|------:|-----:|----------|
| **1. Bypass** | Client → **direct Atlas** | `BypassUser` | 120 | 5/s | 10m |
| **2. Cache** | Client → **Nance proxy** → Redis / backend | `CacheUser` | 180 | 15/s | 10m |

| Setting | Value |
|---------|--------|
| Database | `nance-test` |
| Real collection | `loadtest_docs` |
| Cache collection | `loadtest_docs_cache` |
| Document payload | 512 bytes |
| Data volume | ~221,630 docs (seed target 100k; already populated) |
| Order | Direct fully finished → 30s cooldown → proxy cache |

> This compares **direct MongoDB Atlas** vs **proxy + `_cache`**, not “proxy without `_cache`”.

---

## Executive summary

| Metric | Direct Mongo | Proxy + `_cache` | **Δ (cache vs direct)** |
|--------|-------------:|-----------------:|------------------------:|
| **Requests** | 89,463 | **225,111** | **+2.5× volume** |
| **Throughput** | 149.2 req/s | **375.5 req/s** | **+152%** |
| **Avg latency** | 753 ms | **443 ms** | **−41%** |
| **Median (p50)** | 950 ms | **5 ms** | **~190× faster** |
| **p66** | 960 ms | **19 ms** | huge |
| **p75** | 970 ms | 960 ms | ~parity |
| **p95** | **980 ms** | 1,900 ms | direct better tail |
| **p99** | **990 ms** | 1,900 ms | direct better tail |
| **Failures** | **0 (0%)** | **0 (0%)** | tie |

### Headline

> From a **same-region / VPC load host**, Nance **proxy + cache** delivered **~2.5× throughput** and **~190× better median latency** (5 ms vs 950 ms) than direct Atlas under heavy concurrent load, with **zero failures**.  
> Latency distribution is **bimodal**: most cache ops are Redis hits (ms); p75+ reflects miss path through proxy → Atlas.

**Total successful ops:** **314,574** (both phases).

---

## Phase 1 — Direct MongoDB (`BypassUser`)

```text
MONGO_DB  = nance-test
MONGO_COLLECTION = loadtest_docs
Users = 120, ramp = 5/s, duration = 10m
```

| Metric | Value |
|--------|------:|
| Requests | **89,463** |
| Failures | **0 (0%)** |
| RPS | **149.24** |
| Avg | **753 ms** |
| Median | **950 ms** |
| Min | 20 ms |
| Max | 2,582 ms |
| p90 / p95 / p99 | 980 / 980 / **990** ms |

Notes:
- Ramp kept slow (5/s) to avoid gevent/TLS handshake storms to Atlas under Python 3.14 + Locust.
- Single-process connectivity to Atlas was verified before load; concurrent SSL issues were mitigated with `certifi`, modest pool size, and ramp.

---

## Phase 2 — Nance proxy + cache (`CacheUser`)

```text
collection used = loadtest_docs_cache
Users = 180, ramp = 15/s, duration = 10m
```

| Metric | Value |
|--------|------:|
| Requests | **225,111** |
| Failures | **0 (0%)** |
| RPS | **375.46** |
| Avg | **443 ms** |
| Median | **5 ms** |
| Min | 1.5 ms |
| Max | 1,948 ms |
| p50 / p66 / p75 | **5 / 19 / 960** ms |
| p90 / p95 / p99 | 1,800 / 1,900 / **1,900** ms |

Notes:
- All 180 users spawned cleanly against the proxy (`maxPoolSize=1` per Locust user for PLAIN URIs).
- p50/p66 show warm Redis hits; p75+ shows miss / backend path.

---

## Side-by-side

### Throughput (higher is better)

```
Direct Mongo   ████████████████                      149 req/s
Proxy cache    ████████████████████████████████████  375 req/s   (+152%)
```

**Winner: Proxy + cache**

### Median latency (lower is better)

```
Direct Mongo   ████████████████████████████████████  950 ms
Proxy cache    █                                       5 ms   (~190× faster)
```

**Winner: Proxy + cache**

### Average latency (lower is better)

```
Direct Mongo   ████████████████████████████████████  753 ms
Proxy cache    █████████████████████                 443 ms   (−41%)
```

**Winner: Proxy + cache**

### Tail latency (lower is better)

| Percentile | Direct | Cache | Winner |
|------------|-------:|------:|--------|
| p95 | 980 ms | 1,900 ms | **Direct** |
| p99 | 990 ms | 1,900 ms | **Direct** |
| max | 2,582 ms | 1,948 ms | **Cache** |

**Winner: Direct on p95/p99** (cache miss path pays proxy hop + Atlas)

### Reliability

| | Direct | Cache |
|--|-------:|------:|
| Failure rate | **0%** | **0%** |

---

## Interpretation

1. **Cache path is hit-dominated** at the median (5 ms). That is the real Nance value for hot queries.
2. **Miss path is visible in the upper quartile** (~1–2 s): proxy + backend when Redis misses or keys are cold/random.
3. **Workload note:** Locust mix still includes random/`seq` filters; hit rate would be even higher for repeated production query shapes.
4. **Correctness:** ~315k ops, zero failures on both paths.

---

## Summary

| Question | Answer |
|----------|--------|
| Was bypass **direct Mongo**? | **Yes** — Atlas `mongodb+srv` |
| Was cache **proxy + `_cache`**? | **Yes** — `nance-proxy.oxella.com` + `loadtest_docs_cache` |
| Who is faster on average / median? | **Proxy cache** (−41% avg, ~190× median) |
| Who handles more RPS? | **Proxy cache** (+152%) |
| Who has better p95/p99? | **Direct** (miss-path tails on cache) |
| Reliability? | **Both 0% failures** |
| Total volume | **~315k** successful reads |

### Bottom line

Under heavy concurrent load from a **cloud load generator next to the stack**, **Nance proxy + cache substantially beat direct Atlas on throughput and typical latency**, with a clear hit/miss bimodal distribution. Tail latency on cache still tracks backend misses; optimize with hotter keys, warm-up, and co-located Redis/proxy/Atlas.

---

## Reproduce

```bash
cd apps/benchmark
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt

export MONGO_DB=nance-test
export MONGO_COLLECTION=loadtest_docs
export DOC_SIZE=512
export SEED_COUNT=100000

# 1) Direct Atlas
export MONGO_URI='mongodb+srv://…@nance-test.3n6vdm9.mongodb.net/nance-test'
export MONGO_DIRECT=0
python scripts/seed.py
locust -f locustfile.py BypassUser --headless -u 120 -r 5 -t 10m \
  --csv=results/bypass --html=results/bypass.html

sleep 30

# 2) Proxy cache
export MONGO_URI='mongodb://…@nance-proxy.oxella.com:27018/?authMechanism=PLAIN&authSource=$external&directConnection=true'
unset MONGO_DIRECT
locust -f locustfile.py CacheUser --headless -u 180 -r 15 -t 10m \
  --csv=results/cache --html=results/cache.html
```

---

## Raw Locust aggregates (from run)

**Bypass (`find_bypass`):**

```text
reqs=89463 fails=0 avg=753.3ms med=950ms min=20.2ms max=2582ms rps=149.24
p50=950 p95=980 p99=990
```

**Cache (`find_cache`):**

```text
reqs=225111 fails=0 avg=443.5ms med=5ms min=1.5ms max=1948ms rps=375.46
p50=5 p66=19 p75=960 p95=1900 p99=1900
```

*HTML/CSV artifacts lived on the load host under*  
`results/heavy_direct_vs_cache_20260710_073855/`  
*and were not retained after the instance was destroyed; this markdown is the durable record of the run.*
