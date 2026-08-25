# Runtime reliability: the concurrency model and browser-mode safety

This document records how the scraper actually uses browsers and CPU at
runtime, why a real production run once produced zero rows, and the guarantees
the hardening pass added. It is operational reference, not a feature list.

## The concurrency model, exactly

A running job is executed by a **task pool**. The number of simultaneous
Google Maps operations is:

```
simultaneous browsers  ~=  task workers  x  per-task concurrency
```

- **Task workers** (`planTaskPool`, `runner/webrunner/task_pool.go`): how many
  grid cells / queries run side by side. Each worker is a **separate scrapemate
  application with its own browser pool** — so in browser mode each worker is at
  least one Chromium process.
- **Per-task concurrency** = `effectiveConcurrency / workers`.
- The default worker count is `defaultTaskWorkers = 4`, capped by the job's
  concurrency, the pending task count, and `MaximumJobTaskWorkers = 16`.

So a job that logs *"Running 4 task(s) in parallel with 1 worker concurrency
each"* is running **four concurrent Chromium browsers**.

## What `browser-failure` really was

The upstream scrapemate engine launches Chromium with `--single-process
--no-zygote --disable-dev-shm-usage --no-sandbox` (a read-only dependency this
repo does not fork). Measured cost: **~300 MB RSS per browser, linear**. Four
concurrent single-process browsers on a typical 8 GB host — alongside the Go
service, SQLite and any co-resident containers — drive the machine toward RAM
exhaustion, and the OOM killer terminates a browser process. That surfaces to
the scraper as a generic `browser-failure`. Normally the task is retried and
the run recovers; under worse headroom, or when Google is simultaneously
rate-limiting the shared IP, the retries also fail and the whole run cascades to
zero rows. The incident was this: **memory-driven browser OOM, masked by a
generic label, compounded by rate limiting.**

## What the hardening pass changed

1. **Memory-aware browser fan-out.** In browser mode the worker count is now
   capped by available RAM (`browserModeWorkerBudget`, reserving ~3 GiB per
   browser-mode worker, hard cap two by default, collapsing to one on a
   low-memory host). This lowers the default simultaneous-browser count from
   four to two, and is a **hard physical ceiling** — it lowers an explicitly
   configured worker count too, because launching more browsers than RAM holds
   crashes them regardless of who chose the number. Fast mode is untouched.

2. **Real failure classification.** `browser-failure` is split into fifteen
   fine kinds (browser-crash, browser-context-failure, navigation-timeout,
   google-block, rate-limit, network-dns/tls/refused, …) with a named signal,
   surfaced on the job's failure events as `failure_kind` / `failure_class` /
   `failure_signal`. The coarse buckets the adaptive controller depends on are
   unchanged. An operator can now tell an OOM crash from a Google block.

3. **Shared-memory headroom.** `compose.yaml` sets `shm_size: 1gb` as
   defense-in-depth for the Chromium code paths that still touch `/dev/shm`.

4. **A browser launch self-check** in the system self-test, so an operator
   learns at startup whether Chromium can launch in their environment.

5. **CSV-merge identity fix.** Merge dedup keyed only on authoritative
   identifiers (place_id / cid / data_id) with a full-row-hash fallback; rows
   that merely shared a phone, domain or address can no longer collapse or be
   dropped between task completions.

## Fast mode vs browser mode

**Fast mode** is a pure-HTTP stealth fetcher — no browser at all. It cannot
suffer a `browser-failure`, uses a fraction of the memory, and is roughly an
order of magnitude faster. Browser mode renders JavaScript and drives a map
viewport grid, which is heavier and is the only path exposed to browser OOM.
For most collection, **Fast mode is the more robust default**; reach for browser
mode when a grid/viewport walk is genuinely needed.

## Lifecycle and data safety

- Committed businesses are never discarded by a later failure: the per-task CSV
  merge only ever adds or replaces by authoritative identity, and an empty or
  failed task writes nothing that can drop a prior row.
- A crash-and-restart **recovers abandoned jobs to their last safe checkpoint
  and pauses them** (`RecoverAbandonedJobs`) rather than blindly re-running;
  the operator resumes, and the run continues from the next task with no
  double-count.
- Timeout and record-limit completions are labelled **Partial**, with their
  committed rows intact.
- Cancellation stops a job at its next safe checkpoint; a browser-mode task
  already in flight finishes its current cell first, so cancellation latency is
  bounded by one task plus the three-minute inactivity safety net.

## Acceptance harness

`acceptance/` is a reusable, bounded real-world acceptance/benchmark harness
that drives one job at a time through the local HTTP API and records a
comparable per-experiment JSON (yield, rates, effective concurrency, failure
kinds, resources). See `acceptance/README.md`. It never contacts Google itself;
the container does the scraping, one job at a time.
