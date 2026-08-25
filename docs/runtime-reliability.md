# Runtime reliability: the concurrency model and browser-mode safety

This document records how the scraper actually uses browsers and CPU at
runtime, the defects a controlled investigation reproduced, the most probable
reconstruction of a reported zero-row production run whose job record was never
available, and the guarantees the hardening pass added. It is operational
reference, not a feature list. Proven and reconstructed claims are labelled
separately throughout — see *Evidence standard* below.

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

## Evidence standard: what is proven, and what is reconstructed

This document mixes two kinds of statement, and they are labelled throughout.

**CONFIRMED (independently reproduced on the deployed build).**
- The concurrency defect: `planTaskPool` fans out to four task workers by
  default, each its own scrapemate app and therefore its own browser pool, so a
  default browser-mode job runs four concurrent Chromium browsers. Reproduced
  in code, in unit tests, and live — the job log announces it.
- Browser memory behaviour: ~300 MB RSS per single-process Chromium, linear in
  the number of browsers, measured in a throwaway container.
- Container OOM under memory pressure: a browser-mode run in a 1.2 GiB
  container produced `OOMKilled: true`.
- Generic failure masking: every one of those causes collapsed into a single
  `browser-failure` label before this pass.
- **The engine-shutdown hang.** A controlled 16-cell acceptance run wedged: 15
  tasks finished, the 16th parked a worker for 21 minutes, and the job sailed
  past its runtime deadline still reporting `running`. Proven by a goroutine
  dump taken from the wedged container (`SIGQUIT`), which put the worker in
  `playwright-go`'s `protocolCallback.waitResult` under
  `browserContext.Close()`, reached from scrapemate's deferred shutdown inside
  `app.Start` — a call that takes neither a context nor a timeout. See *The
  engine-shutdown hang* below.

**RECONSTRUCTED (inference, not proof).** The original failing job — three
queries, sixteen cells, forty-eight tasks, zero rows — was **not available** in
this workspace when the investigation ran. No container state, OOM record, or
event log from that specific run was recovered. The explanation below (that its
four concurrent browsers were OOM-killed, and that retries cascaded to zero rows
because the host lacked headroom and/or Google was rate-limiting the shared IP)
is the mechanism that best fits the reported symptoms **and that we reproduced
in isolation** — but it is an inferred account of that incident, not a
demonstrated one. Treat the mechanism as confirmed and its application to that
specific historical job as the most probable explanation.

## What `browser-failure` really was

The upstream scrapemate engine launches Chromium with `--single-process
--no-zygote --disable-dev-shm-usage --no-sandbox` (a read-only dependency this
repo does not fork). Measured cost: **~300 MB RSS per browser, linear**
(CONFIRMED). Four concurrent single-process browsers on a typical 8 GB host —
alongside the Go service, SQLite and any co-resident containers — drive the
machine toward RAM exhaustion, and the OOM killer terminates a browser process
(CONFIRMED: reproduced at 1.2 GiB with `OOMKilled: true`). That surfaces to the
scraper as a generic `browser-failure` (CONFIRMED).

What happens next is where proof ends. In our reproduction the run **recovered**
— the killed task was retried and the job still completed with full yield. For a
run to end at zero rows, the retries must fail too, which needs either less
headroom than we could reproduce or a simultaneous platform refusal. That is the
RECONSTRUCTED part: it is the mechanism that fits the reported symptoms, and
every ingredient of it is independently confirmed, but the specific historical
job was never available to examine.

## The engine-shutdown hang

This one was found by the acceptance matrix itself, not by the incident, and it
is fully proven.

A task normally ends when its own exiter cancels its context; the engine then
returns and tears the browser down. That teardown chain is
`scrapemateapp.Start`'s deferred `ScrapeMate.Close` → `jsFetch.Close` →
`browserContext.Close` → `protocolCallback.waitResult`, and **not one link in it
accepts a context or a deadline**. It waits for the browser to answer. A browser
that has died or stopped answering never answers.

The blast radius was the whole job, because nothing above it was bounded either:

- The worker never returns, so its `defer run.activeTasks.Add(-1)` never runs.
- Other workers see `activeTasks > 0` and idle-spin instead of concluding.
- `runTaskPool`'s `group.Wait()` never returns, so the job is never finalised.
- The runtime deadline *does* fire and calls `runCancel()` — but cancelling a
  context has no effect on a call that never looks at one, and the same cancel
  retires the supervisor, so after it the run is unobservable and uncancellable.

The fix does not fork scrapemate (a read-only dependency) and does not try to
kill the goroutine, which Go cannot do. It stops *waiting* on it. `awaitEngine`
waits without any bound while the task's context is live — a task legitimately
in progress is never cut short — and once that context is cancelled it waits at
most `engineShutdownGrace` (90s) longer. After that the task keeps the rows the
engine already wrote, hands itself back to the pool, and the wedged goroutine
and its file handle are deliberately leaked. The outcome is reported as the fine
kind `engine-shutdown-timeout` under the unchanged coarse `browser-failure`
bucket, so scheduling behaves exactly as before.

Worst-case time from runtime deadline to a terminal `partial` state is therefore
bounded at deadline + 90s, instead of unbounded.

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

6. **Bounded engine shutdown.** `awaitEngine` caps how long a task waits for the
   scrape engine after cancellation, so a wedged browser teardown can no longer
   hang the job (see above).

7. **Honest browser accounting.** The operator-facing browser count now reports
   the browsers actually open — `workers x browsers-per-worker` — instead of the
   per-app pool size configured on the parent job. That number is what an
   operator judges memory risk by, and it was wrong in both directions: a
   two-worker browser job reported one browser, and Fast mode, which launches no
   browser at all, reported one.

## Fast mode vs browser mode

**Fast mode** is a pure-HTTP stealth fetcher — no browser at all. It cannot
suffer a `browser-failure` and it reports zero browsers and zero pages, because
it opens neither.

The two modes are only comparable on a workload both accept. The product refuses
grid coverage in Fast mode and requires a radius, so the one shared shape is a
grid-free radius search. Run that way — same query, centre, radius, zoom, depth
and concurrency, differing only in mode — the measured result was:

| Arm | Wall | Unique businesses | Peak browsers |
| --- | --- | --- | --- |
| Browser | 127s | 20 | 1 |
| Fast | 2s | 20 | 0 |

**Identical yield; Fast finished in a fraction of the time.** Quote that ratio
only against this pairing: it is the same workload on both arms. Note the
browser arm ran immediately after a 48-task run with the host at ~90% CPU; an
idle-host repeat of the same grid-free browser search took 94s, so the honest
range for this workload is a browser arm of 94–127s against a Fast arm of ~2s.

The trade is coverage shape, not quality: browser mode renders JavaScript and
can walk a map viewport grid, which is the only way to sweep an area cell by
cell — and the only path exposed to browser OOM and to the teardown wedge above.
For collection that fits a radius search, **Fast mode is the more robust
default**; reach for browser mode when a grid walk is genuinely needed.

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
- Cancellation stops a job at its next safe checkpoint. Measured on the hardened
  build, a 4-cell browser job reached `cancelled` **4.0s** after the request with
  every committed row intact. The worst case is one in-flight task plus the
  engine shutdown grace, so cancellation latency is bounded, not open-ended.

## Controlled acceptance matrix

Every row below is one real run through the deployed container, one job at a
time, recorded by the harness. A–G share one bounded workload family so the
comparisons are like for like: the same query (`coffee shop`), the same centre
(37.7749, -122.4194), the same 3 km cells, enrichment off and a direct
connection unless the row says otherwise. "Browsers" is the app-reported peak,
which is now the honest count.

| Exp | Workload | Req. conc | Workers x per-task | Browsers | Tasks | Unique | Wall | Rows/min | Browser fail | Blocks | Final |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A | 1 query, 1 cell | 1 | 1 x 1 | 1 | 1/1 | 20 | 89s | 13.5 | 0 | 0 | completed |
| B | 1 query, 4 cells | 1 | 1 x 1 | 1 | 4/4 | 66 | 272s | 14.6 | 0 | 0 | completed |
| C | **same as B**, conc 2 | 2 | 2 x 1 | 1 | 4/4 | 65 | 163s | 30.6 | 0 | 0 | completed |
| D | 1 query, 16 cells | 4 | 2 x 2 | 4 | 15/16 | 214 | 475s | 27.3 | 1 | 0 | partial (task_failures) |
| E | 3 queries, 16 cells (48 tasks) | 4 | 1 x 4 | 4 | 20/48 | 379 | 1881s | 13.9 | 6 | 0 | partial (runtime_limit) |
| F1 | 1 query, radius, **browser** | 1 | 1 x 1 | 1 | 1/1 | 20 | 127s | 9.4 | 0 | 0 | completed |
| F2 | **same as F1**, **Fast** | 1 | 1 x 1 | **0** | 1/1 | 20 | 2s | 600 | 0 | 0 | completed |
| G | **same as B**, enrichment ON | 1 | 1 x 1 | 1 | 4/4 | 51 | 712s | 4.3 | 0 | 0 | completed |

Named markets, all run with the identical structure (4 cells at 3 km, browser
mode, concurrency 1) so only the market differs:

| Market | Query / location | Tasks | Unique | Wall | Rows/min | Failures | Final |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Bend, OR | `plumber` @ 44.0640,-121.3190 | 4/4 | 122 | 496s | 14.8 | 0 | completed |
| Des Moines, IA | `dentist` @ 41.5868,-93.6250 | 4/4 | 110 | 420s | 15.7 | 0 | completed |
| San Francisco, CA | `coffee shop` @ 37.7749,-122.4194 | 4/4 | 64 | 305s | 12.6 | 0 | completed |

These were chosen as sparse / medium / dense by metro size, and the measured
yield did **not** follow that ordering. The category dominates the geography: a
dense-city `coffee shop` grid saturates each viewport and overlaps heavily
between adjacent cells, while `plumber` in a small city spreads service-area
listings across the whole grid. Report them as three named real markets, not as
a density gradient.

**No market run went Partial.** All three completed with every task done and
zero failures, so the question of a thin-market Partial did not arise. The two
Partial results in the matrix have precise, different causes:

- **D — `task_failures`.** 15 of 16 cells completed; one hit the engine-shutdown
  wedge and was abandoned after its grace period. Partial is the honest label:
  one cell of the grid is genuinely missing. Its 214 businesses were committed.
- **E — `runtime_limit`.** The bounded 1800s window closed with 20 of 48 tasks
  done and 22 still pending. This is the intentional acceptance limit, not an
  execution failure; the run was still discovering at 13.9 rows/min when the
  window closed, and its 379 businesses were committed.

Two further readings worth keeping:

- **Repeatability.** MKT-dense is an unplanned exact repeat of B — same query,
  bbox, cell size, mode and concurrency, about ninety minutes apart. 66 vs 64
  unique businesses, 272s vs 305s. Two samples is not a variance envelope, but
  it does bound the noise well below the differences discussed above.
- **The teardown wedge scales with browser fan-out.** Every concurrency-1 run in
  this matrix (A, B, F1, and all three markets — seven runs) had zero browser
  failures. Both concurrency-4 runs hit the wedge: D once in 16 tasks, E six
  times in 26 concluded tasks. All ten failures across the matrix classified as
  `engine-shutdown-timeout`; not one was a Google block or rate limit.

### Enrichment off vs on

B and G are the same workload, differing only in enrichment:

| | Unique | With website | With phone | **With email** | Wall | Rows/min |
| --- | --- | --- | --- | --- | --- | --- |
| B (off) | 66 | 58 | 56 | **0** | 272s | 14.6 |
| G (on) | 51 | 43 | 44 | **25** | 712s | 4.3 |

Enrichment is the only way to get an email address at all, and it costs about
2.6x the wall clock. Note honestly that G also discovered fewer businesses (51)
than either enrichment-off sample of the same workload (66, 64). Discovery and
enrichment are separate stages — the website crawl runs after the listing walk —
so enrichment should not reduce discovery, and with only two baseline samples
this is not conclusive either way. It is recorded as observed, not explained.

### Lifecycle and recovery, re-verified

Run against a dedicated container on the hardened build:

| Check | Result |
| --- | --- |
| Cancel with committed rows | `cancelled` / `user_cancelled` in **4.0s**; 20 rows before and after |
| Timeout | `partial` / `runtime_limit`; 1 of 16 tasks done, **39 rows committed**, work correctly left unfinished |
| Hard kill / restart | Recovered to `paused` with `recovery_required`; 44 rows survived, in-flight task returned to pending (not lost, not double-counted) |
| Resume to completion | `completed` 16/16, 220 businesses, **0 duplicate identities** across every result page |

## Acceptance harness

`acceptance/` is a reusable, bounded real-world acceptance/benchmark harness
that drives one job at a time through the local HTTP API and records a
comparable per-experiment JSON (yield, rates, effective concurrency, failure
kinds, resources). See `acceptance/README.md`. It never contacts Google itself;
the container does the scraping, one job at a time.
