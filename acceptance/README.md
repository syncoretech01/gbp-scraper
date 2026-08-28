# Acceptance / benchmark harness

A bounded, in-repo, real-world acceptance and benchmark harness for the local
Google Maps scraper. It drives **one** scrape job at a time through a deployed
container's existing local HTTP API, waits for the run to finish, reads the
durable readback endpoints, and records a stable, comparable `ExperimentRecord`
(durable JSON + a human summary) per run.

It exists to turn the concurrency incident (browser mode producing zero rows and
repeated `browser-failure` / `rate-limit` events at load) into a repeatable,
measured experiment: run the same workload at rising concurrency, and diff the
records to see exactly where browser mode breaks.

## What it does and does not do

- It **only** speaks to the local application's HTTP API. It never contacts
  Google Maps itself; the container does any scraping.
- It drives one job at a time (create -> poll -> read), never in parallel.
- Its unit tests run entirely against an in-process fake HTTP server that
  returns canned JSON. `go test ./acceptance/...` touches no network and no
  live workspace. Synthetic tests prove the harness's parsing and arithmetic;
  they are **not** evidence about scraping reliability — that is the lead's live
  job.
- Pointed at a real container, it performs real scrapes. Run it only against a
  container and a target you intend to scrape.

## Build and offline inspection

```
# List the resolved experiments without running anything:
go run ./acceptance/cmd/harness -experiment all -list

# Print the exact job JSON each experiment would POST, without creating a job:
go run ./acceptance/cmd/harness -experiment D -dry-run
```

Neither `-list` nor `-dry-run` needs `-base`, so the lead can vet configuration
before spending queue time.

## Running against a deployed container

Point `-base` at a container **copy** on a spare port (never the live
workspace — see `CLAUDE.md`). Records land under `-out` (default
`acceptance-results/`), one directory per experiment plus an append-only
`records.jsonl`.

`-token` is only needed if the container enabled API keys or local login.

The single-job invariant means: run experiments **sequentially**, one job in the
container's queue at a time.

## Metrics

Recorded per run (see the schema below). Rates are fractions in `[0,1]`:

| metric | definition |
| --- | --- |
| `discovered_rows` | benchmark `total_discovered_rows` (rows added + replaced + duplicates), falling back to the file-backed row count |
| `unique_businesses` | benchmark `unique_businesses`, falling back to the progress summary / normalized results total |
| `rows_per_minute` | `discovered_rows / (wall_seconds/60)` |
| `task_success_rate` | `completed / (completed+failed+skipped)` |
| `browser_failure_rate` | `browser-failure events / (browser-failure events + finished tasks)` |
| `block_rate` | `block events / (block events + finished tasks)`; block = `blocked`+`rate-limit`+`captcha`+`proxy-failure` |
| `duplicate_rate` | benchmark `duplicate_rate` |
| `retry_count` | benchmark `retries`, falling back to the task summary |
| `final_effective_concurrency` | last `effective_concurrency` an `adaptive-performance` event settled on, else the planned budget |
| resources | **app-reported** (`GET /api/v1/system/metrics`); host-wide, not scoped to the job — labelled as such in every record |

Effective concurrency is reconstructed from the worker events
(`task-pool` + `adaptive-performance` context), and falls back to the
plain-text log messages for a build that did not attach event context. The fine
`failure_kinds` breakdown is bound defensively: a structured `failure_kinds`
field on the benchmark report (once the classification specialist lands it) is
preferred; otherwise the harness counts the failure-typed worker events;
otherwise it derives kinds from the coarse benchmark classes. Its absence is
never an error.

## Repeatability

`-repeat N` runs the same configuration N times and writes a
`repeatability.json` / `.txt` reporting the mean, population standard deviation,
range, and coefficient of variation of every headline metric — so the lead can
see how trustworthy a single result is before comparing two code versions.

## Named experiments

The catalog holds two families:

- **A..E escalation** — the same 48-task workload (3 queries over a 4x4 1 km
  grid = 16 cells, direct connection, enrichment off, 60-minute cap) run at
  browser concurrency `1, 2, 4, 8, 16`. Comparing their records finds the load
  at which browser mode breaks. `A` (concurrency 1) is the baseline the lead
  already proved good; `C` (concurrency 4) reproduces the incident's default
  `4 workers x 1`.
- **sparse / medium / dense markets** — the same fixed browser concurrency
  (default 1) over three area densities, to compare yield and quality across
  market types.

- **W width ladder** — the same 48-task workload run at a rising number of
  parallel task WORKERS (`W1`, `W2`, `W4`, `W6`) with per-task concurrency, the
  browser pool and pages-per-browser all pinned so that **one rung = N workers =
  N Maps operations = N browser processes**.

There is also a `fast` reference experiment (pure-HTTP, no browser) matching the
lead's fast-mode control.

### Why the W ladder exists alongside A..E

The A..E ladder varies **concurrency**, which the engine may spend either on
more browsers or on more pages inside one browser depending on
`pages_per_browser`. That makes it the right ladder for finding the load at
which browser mode breaks, and the wrong one for finding how wide the pool may
safely be: two rungs with the same concurrency can hold very different numbers
of browser processes.

Each task worker runs its **own** scrapemate app and therefore its **own**
browser pool, which never drops below one browser. Workers — not concurrency —
are what multiply Chromium processes and consume the memory budget. The W ladder
varies exactly that dimension and holds everything else fixed, so a rung's block
rate, browser-failure rate and peak memory are attributable to the width alone.

Run the rungs **in ascending order**, one job at a time, and stop at the first
rung whose `block_rate` or `browser_failure_rate` rises above the rung below it.
That rung is the live knee for this host and this target; the rung below it is
the safe width. Throughput bought past the knee is bought with platform
refusals, which is not throughput.

Auto capacity is left **on** for the ladder. A rung whose recorded
`final_effective_concurrency` or measured browser count comes back below its
label means the host could not afford that width — which is itself the answer,
not a failed run.

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment W1 -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment W2 -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment W4 -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment W6 -out ./acc
# or the whole ladder in order: -experiment widths
```

### The offline scheduler benchmark

The W ladder costs live Google traffic. The question "how much of a run's wall
time is the scheduler's, and how does that change with width" does not, and is
answered by `TestSchedulerThroughputBenchmark` in
`runner/webrunner/schedbench_test.go`. It drives the real pool — the real
durable plan, the real lease/claim protocol, the real CSV merge, the real SQLite
writes, the real supervisor — and replaces only the scraping engine, which
sleeps for the measured duration of each of the 180 tasks of acceptance job
`7100e95b`.

```
GMS_SCHEDBENCH=1 GMS_SCHEDBENCH_SCALE=50 GMS_SCHEDBENCH_WIDTHS=1,2,3,4,6,8 \
  go test -run TestSchedulerThroughputBenchmark -timeout 30m ./runner/webrunner/
```

Everything it reports about scheduling, contention, duplicate work and write
latency is **measured**. The per-task durations it replays are **modelled** from
one real run. It cannot say anything about the platform's block rate at width —
that is what the W ladder is for.

The default workload coordinates (Austin, TX; and the market placeholders) are
**placeholders**. Replace them with your real targets via `-queries`,
`-grid-bbox`, `-grid-cell-km`. The *shape* (3 queries x 16 cells = 48 tasks) is
what reproduces the incident.

### Exact command lines

Escalation A through E (records under `./acc/`), one at a time:

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment A -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment B -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment C -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment D -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment E -out ./acc
```

Or the whole ladder in order:

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment escalation -out ./acc
```

The three markets:

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment sparse -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment medium -out ./acc
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment dense  -out ./acc
# or all three: -experiment markets
```

Repeatability of one experiment (variance across two runs):

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment D -repeat 2 -out ./acc
```

Custom target and workload (keep the 3-query, 16-cell shape to reproduce the
incident):

```
go run ./acceptance/cmd/harness -base http://127.0.0.1:8099 -experiment escalation \
  -queries "plumber in Denver CO 80202||electrician in Denver CO 80202||hvac contractor in Denver CO 80202" \
  -grid-bbox "39.735,-105.010,39.770,-104.970" -grid-cell-km 1 -out ./acc
```

## Recorded experiment JSON schema (`acceptance/v1`)

```jsonc
{
  "schema": "acceptance/v1",
  "harness_version": "1",
  "experiment": "D",
  "label": "browser mode, concurrency 8, 48-task workload, direct",
  "config": {
    "base_url": "http://127.0.0.1:8099",
    "mode": "browser",                 // "browser" | "fast"
    "connection": "direct",            // "direct" | "proxy"
    "proxy_pool_id": "",
    "enrichment": false,
    "queries": ["plumber in Austin TX 78701", "..."],
    "query_count": 3,
    "grid_bbox": "30.250,-97.760,30.285,-97.720",
    "grid_cell_km": 1,
    "estimated_grid_cells": 16,
    "estimated_seed_tasks": 48,
    "concurrency": 8,
    "task_workers": 0,
    "browser_pool_size": 0,
    "pages_per_browser": 0,
    "runtime_limit_seconds": 3600,
    "zoom": 15, "depth": 10, "language": "en"
  },
  "run": {
    "job_id": "…",
    "harness_started_at": "RFC3339",
    "harness_ended_at": "RFC3339",
    "job_created_at_unix": 0, "job_started_at_unix": 0, "job_finished_at_unix": 0,
    "wall_seconds": 0,
    "terminal_state": "partial",       // completed | partial | failed | cancelled
    "stop_reason": "runtime_limit",
    "poll_count": 0,
    "timed_out": false,
    "error": ""
  },
  "outcomes": {
    "discovered_rows": 0,
    "unique_businesses": 0,
    "normalized_results_total": 0,
    "rows_per_minute": 0,
    "new_businesses_per_minute": 0,
    "duplicate_rate": 0,
    "duplicate_count": 0,
    "task_success_rate": 0,
    "browser_failure_rate": 0,
    "block_rate": 0,
    "retry_count": 0,
    "tasks": { "total":0,"completed":0,"failed":0,"skipped":0,"pending":0,"running":0 },
    "failure_classes": [ { "class":"browser","count":0,"retries":0,"sample":"" } ],
    "failure_kinds": { "browser-failure": 0, "blocked": 0 },
    "events_by_type": { "task-pool": 1, "adaptive-performance": 2 }
  },
  "concurrency": {
    "desired": 8,
    "planned_workers": 4,
    "per_task_concurrency": 2,
    "planned_effective": 8,
    "final_effective": 2,
    "adaptive_reductions": 2,
    "effective_workers_reported": 2,
    "source": "worker-events"          // "worker-events" | "log-messages" | "unavailable"
  },
  "resources_app_reported": {
    "label": "app-reported (GET /api/v1/system/metrics); host-wide, not scoped to this job",
    "cpu_percent": 0, "logical_cpus": 0,
    "memory_used_bytes": 0, "memory_used_percent": 0, "memory_total_bytes": 0,
    "peak_active_browsers": 0, "peak_active_pages": 0,
    "sample_count": 0, "collected_at": "RFC3339"
  },
  "recovery": {
    "checkpoint_present": true,
    "checkpoint_task_key": "…",
    "recovery_required": false,
    "tasks_remaining_at_end": 0,
    "coverage_stopped": false,
    "coverage_stop_reason": ""
  },
  "availability": {
    "progress": true, "benchmark": true, "coverage": true,
    "logs": true, "events": true, "results": true, "metrics": true
  }
}
```

`availability` records which readback endpoints answered, so a record with
zeroed metrics can be told apart from an endpoint that was simply unavailable on
that build. The record shape is identical for identical configuration, so two
code versions diff field for field.
