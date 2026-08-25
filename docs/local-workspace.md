# Local workspace guide

This workspace is designed to keep jobs, results, logs, exports, and settings
on this computer. The default Compose configuration publishes the web UI only
on `127.0.0.1` and disables telemetry.

## Start on Windows

1. Start Docker Desktop and wait until it reports that the engine is running.
2. Open PowerShell in this repository.
3. Start or rebuild the application:

   ```powershell
   docker compose up -d --build
   ```

4. Open <http://127.0.0.1:8080/>.

The bind-mounted `webdata` directory is the durable source of truth. Rebuilding
or replacing the container does not delete `webdata/jobs.db` or the UUID-named
CSV files.

Useful lifecycle commands:

```powershell
docker compose ps
docker compose logs -f scraper
docker compose stop
docker compose start
docker compose down
```

`docker compose down` removes the container and network but not the bind-mounted
`webdata` files. Do not add `--volumes` when preserving unrelated named volumes
matters.

## Thorough San Francisco dentist search

In **New Scrape**, use the **San Francisco** preset and these starting values:

- Queries: `dentist`, `dental clinic`, and any genuinely distinct specialties
  needed for the research. Exact duplicate queries are removed automatically.
- Coverage: exhaustive grid, not Fast Mode.
- San Francisco bounding box:
  `37.708,-122.515,37.833,-122.354`.
- Grid cell: `2.5 km` for a practical first pass. Smaller cells create more
  overlapping searches and take considerably longer.
- Centre: latitude `37.7749`, longitude `-122.4194`.
- Zoom: `14` or `15`.
- Depth: `10` for the first pass; increase only after checking coverage.
- Runtime: at least `60m`; use `90m` or more for extra queries or smaller cells.
- Email/website crawl: off for the collection pass. Run enrichment afterward so
  slow or unavailable business websites cannot consume the Maps-search budget.

The radius field is a strict distance filter only in **Fast Mode**. A normal
scrape does not become city-wide merely because a large radius is entered; use
the grid/bounding-box coverage mode for a thorough city search.

## Why a job can stop with fewer rows than candidates

Google Maps search pages first discover candidate listings. Each candidate then
needs a detail task, and optional website/email crawling adds separate network
work. A runtime limit can therefore stop a run after candidates were discovered
but before every detail row was committed. The upgraded lifecycle labels this
as **Partial**, preserves every committed row, and allows a restart. It does not
mislabel a deadline-limited file as a fully completed job.

## Browser runtime, memory, and concurrency

Browser-mode scrapes (the default coverage mode) drive a real headless Chromium
per task worker. Fast Mode is a pure-HTTP path that uses **no browser at all**,
so everything in this section applies only to browser-mode runs.

### Each concurrent browser costs memory

Measured inside a container built from this repo's `Dockerfile`, one headless
Chromium rendering a heavy local page holds roughly **300 MB of RSS**, and the
cost scales linearly with the number of browsers:

| Concurrent browsers | Total browser RSS | Per browser |
| --- | --- | --- |
| 1 | ~300 MB | ~300 MB |
| 2 | ~590 MB | ~295 MB |
| 4 | ~1.19 GB | ~297 MB |
| 6 | ~1.80 GB | ~300 MB |
| 8 | ~2.35 GB | ~294 MB |

A live Google Maps tab is heavier than the synthetic page used for this
measurement — expect **~450–750 MB per browser** in a real run once map tiles,
consent frames, and network buffers are resident. Budget with the higher figure.

The task pool runs several task workers side by side, and **each worker owns its
own browser pool** (its own Chromium process). The number of simultaneous
browsers is therefore `task workers × per-task browser pool`, not one. Four task
workers at one browser each means four concurrent Chromium processes.

### Maximum safe concurrent browsers

Reserve ~1.5 GB for the OS, the Go service, and SQLite, then divide the rest by
a conservative ~700 MB per live browser:

| Host RAM | Safe concurrent browsers |
| --- | --- |
| 8 GB | 4–6 (fewer if other containers are running) |
| 16 GB | 10–14 |
| 32 GB | 20+ |

The container is not given a memory limit, so browsers compete for host RAM with
everything else on the machine. When RAM runs out, the Linux OOM killer
terminates a Chromium process; the app sees "target closed" / "browser closed"
and records a **`browser-failure`** worker event, then adaptive performance
lowers concurrency. This is why a run can fail under load while a single-browser
run of the same job succeeds. Keep concurrency within the table above for the
host, or run Fast Mode, which needs no browser.

### Shared memory (`shm_size`)

`compose.yaml` sets `shm_size: "1gb"`. Chromium keeps transport and compositor
buffers in shared memory, and the Docker default `/dev/shm` is only **64 MiB** —
measured to be exhausted by about four concurrent browsers, which surfaces as a
crashed renderer (another `browser-failure`). The scrape engine already passes
`--disable-dev-shm-usage`, which redirects those buffers to `/tmp`; the larger
`/dev/shm` is defense-in-depth for the Chromium subsystems that ignore that flag
and future-proofs the setting. It is a tmpfs cap, so only the bytes actually
used cost RAM. No `pids_limit` or file-descriptor `ulimits` are set: the runtime
already reports `Max open files = 1048576` and `Max processes = unlimited`, so
neither is the bottleneck.

### Checking the browser at startup

The **System** page self-test includes a **Launch a test browser** action (API:
`POST /api/v1/system/self-test?include_browser=true`). It starts a real headless
Chromium with the scrape engine's hardening flags and opens `about:blank`, so an
operator learns whether browser-mode scrapes can run in this environment. A
failure is reported as a warning, not a hard failure, because Fast Mode still
works without a browser; the message names the usual causes (driver missing, too
little memory, or a container `/dev/shm` that is too small). The check is opt-in
so the lightweight self-test never spawns a browser unless asked.

## Responsible use

Use conservative concurrency and comply with applicable laws, contractual
terms, robots/rate policies, privacy obligations, and outreach rules. The tool
works without proxies for modest local research. Large reliable proxy networks,
CAPTCHA solving, and high-confidence mailbox verification are not included and
may require paid third-party services. SMTP or mailbox heuristics must never be
treated as proof that a person owns or reads an address.

## Local automation hooks

The workspace can run your own program at five points: after a job finishes
(`job_completed`) and around `enrichment`, `validation`, `scoring` and
`export`. Every point is off unless you configure it.

Configure a hook by setting an environment variable on the process. The value
is a JSON array of arguments, and the first element must be an absolute path:

```
GMAPS_HOOK_JOB_COMPLETED=["/usr/local/bin/notify.sh","--source","gmaps"]
GMAPS_HOOK_SCORING=["/usr/bin/python3","/opt/hooks/score.py"]
GMAPS_HOOK_TIMEOUT_SECONDS=120
```

In Compose, add them under the service's `environment:` block.

The program receives a JSON document on stdin — `{"point", "subject_id",
"payload"}` — and the variables `GMAPS_HOOK_POINT` and `GMAPS_HOOK_SUBJECT_ID`.
If it writes a JSON object to stdout, the extension points may use it; anything
unparseable is ignored rather than failing the pipeline. Every run is bounded by
a timeout, its output is captured and truncated into the job event log, and a
non-zero exit is recorded but never fails the job.

### Why hooks are configured this way

A hook command can only come from the process environment. It is deliberately
not a setting, not a database row and not a form field, and there is no API
route that accepts a command — a regression test drives every registered route
with command-shaped payloads and asserts that none of them leaves a hook
configured. That boundary is the whole security model: because a command can
never arrive in an HTTP request, no request forgery or authentication mistake
can turn the local web UI into a way to run programs on your machine.

Commands are executed from their argument array, never through a shell, and no
scraped value is ever placed on a command line, so quoting and expansion — the
usual route to command injection — do not occur. Whoever can set these
variables already decides which binary this process is, so a hook grants them
nothing they did not already have.

If you would rather not run programs from the workspace at all, leave the
variables unset and use the signed outbound webhook instead: a self-hosted n8n
or Activepieces instance consumes it and runs your scripts there.
