# Local upgrade: implemented scope and technical limitations

Last reconciled with `Google_Maps_Scraper_Local_Improvement_Specification.md`:
2026-08-19.

This document is the authoritative boundary for specification items that are
not implemented in this local build. A menu item or button is not displayed
unless its backend path exists; unavailable actions are capability-gated and
render nothing. The original CLI, API data model, CSV files, and scraper engine
remain compatible; this upgrade does not claim that every idea in the product
specification is complete.

Verification state for this revision: `go build ./...`, `go vet ./...`, and
`go test -race -timeout 20m ./...` pass (the race suite in a `golang:1.26.6`
container, since `-race` needs cgo). `gofmt -s` is clean for every file this
work touched.

## Completed local foundation

The build provides the following working paths:

- Loopback-only Docker publishing, telemetry disabled by default, a persistent
  `webdata` bind mount, local onboarding, responsive application shell, light,
  dark, and system themes, keyboard navigation, command palette, focus styles,
  state text, confirmations, and empty states.
- A seven-step scrape form with pasted/TXT/CSV queries, exact query
  deduplication, San Francisco centre and bounding-box defaults, grid-size and
  task estimates, Fast/Balanced/Deep presets, and per-job depth, runtime,
  concurrency, browser pool, pages per browser, maximum records, retry count,
  retry delay, page timeout, random delay bounds, image loading, headed/headless
  browser, adaptive performance, low-disk threshold, and proxy-pool selection.
- Durable FIFO lifecycle states, compare-and-swap controls, pause/resume/cancel,
  safe retry/restart, redacted ordered events, SSE, partial outcomes, timeout
  classification, result preservation, restart recovery, and retry-safe CSV
  merging.
- Durable per-task checkpoints: one `job_tasks` row per query or grid cell, so
  an interrupted run resumes from the last completed query/cell rather than
  restarting the whole job.
- SQLite migrations with verified pre-migration copies, forward-schema refusal,
  WAL, foreign keys, FTS5, normalized business/source/version/provenance tables,
  exact identity merging, conservative fuzzy review candidates, quality and
  confidence values, indexed search/filters/sorts, saved result views, tags,
  reviewed state, notes, and auditable workflow changes.
- A Leaflet Map Explorer with vendored, embedded Leaflet, leaflet-draw, and
  leaflet-markercluster assets, circle/polygon/bounding-box drawing, grid
  preview with cell numbering, six live coverage states, marker clustering,
  saved areas, GeoJSON import/export, area export, and selected-cell rescrape.
- A durable website-enrichment worker: bounded crawl with an SSRF/DNS-rebinding
  guard, reachability, HTTP status, redirect chain, TLS state, response time,
  parked/coming-soon/placeholder detection, page metadata, contact/about page
  scopes, email/phone/social extraction with de-obfuscation, DNS/MX checks, role
  and disposable classification, relevance ranking, and CMS/tracker signatures.
- Configurable, explainable quality scoring: twelve weighted components, four
  thresholds, a closed-business exclusion rule, per-component positive and
  negative contributions, and content-addressed rule versions.
- Cross-entity global search over businesses, jobs, tags, templates, saved
  views, and exports.
- Export centre with CSV, JSON, JSONL, GeoJSON, KML, VCard, plain text,
  PostgreSQL-compatible SQL, MySQL-compatible SQL, XLSX, and a portable SQLite
  file; column choice/rename/reorder, splitting, ZIP packaging, raw/source/
  provenance/change bundles, reusable presets, atomic replacement, SHA-256,
  history, repeat download, and deletion.
- Local integrations: bounded loopback/private-address webhooks, a contained
  watch folder, and allowlisted post-run command hooks (off unless
  `GMS_ENABLE_LOCAL_COMMAND_HOOKS=1`).
- Local API keys with read-only/full permissions, request logging with secret
  masking, and a configurable local rate limit.
- Optional Ollama-backed local AI, disabled by default, restricted to loopback
  or private endpoints, with bounded task-specific prompts.
- System diagnostics: health, live resource metrics, self-test, integrity check,
  VACUUM, verified backups with checksum/download, cache clear, contained
  artifact cleanup, stop-all, a redacted diagnostics bundle, and offline update
  information.
- One-time, hourly, daily, weekly, monthly, and bounded five-field cron
  schedules with IANA time zones, queue/skip overlap policy, missed-run
  handling, manual run, and durable history.
- HTTP/HTTPS/SOCKS5 proxy pools with authenticated URLs, AES-256-GCM credentials
  under a separate local key, masked display/logs, exact import deduplication,
  on-demand Google Maps test, health counters, cooldown, automatic disabling,
  and stable/random/fastest/lowest-failure ordering.

## Requirement boundaries by specification section

### 01-03: product shell and dashboard

- The local web workspace stores data only in SQLite with WAL. Optional local
  PostgreSQL exists solely in the separate CLI `database` run modes; there is no
  PostgreSQL repository implementation for the application.
- Performance safety measures CPU, available RAM, and free disk. Browser and
  page counts are reported values, not inputs to any safety decision, and block
  rate is not measurable at all (see below).
- Block rate over time is not charted. The scraper engine emits no block or
  rate-limit callback, so no writer can produce such events. The dashboard's
  speed series therefore counts the warning and error worker events that are
  genuinely recorded (low disk, adaptive concurrency changes, task errors) and
  is labelled as warnings rather than blocks.
- Proxy latency and reliability distribution charts are not implemented; the
  dashboard shows pool totals and health counts only.
- The storage tile reports database, export, and log sizes plus remaining free
  disk. Screenshot storage is always zero because nothing captures screenshots.

### 04: New Scrape Wizard

- No business-category taxonomy, category picker, or reusable category groups.
- No include/exclude keyword expression builder; queries are plain lines.
- No category x location combination generator, and no reusable keyword-set
  entity — only whole-job templates.
- No multi-city selection and no location file upload. Drawing happens on the
  Map Explorer and reaches the wizard as a saved-area snapshot; there is no map
  canvas inside the wizard itself.
- No per-field selection UI. Extended Maps fields (popular times, images,
  reservations, ordering links, menus, owner information, reviews) are retained
  inside `businesses.raw_json` but are not individually selectable, normalized
  into columns, or exportable as separate fields. `input_id` is stored but is
  not offered as an export column.
- No job-engine pre-scrape filters for rating, review count, included/excluded
  categories, open/closed status, claimed status, or business-name conditions.
  Equivalent filters can be applied in Results after collection.
- No crawler URL include/exclude patterns. Crawl targeting is fixed to the
  contact/about page heuristics.
- No failure screenshots, no memory cap, and no per-request font or video
  blocking. Image blocking and the low-disk pause are implemented.

### 05: Map Explorer

- Heatmaps for result density, failed cells, empty cells, and duplicate-heavy
  cells are not implemented.
- Individual cells cannot be resized or grouped. Cell geometry is derived
  deterministically from one cell-size input; cells can be selected, excluded,
  and rescraped, but not edited.
- Assigning a saved template's keywords to a selected cell set is implemented,
  but there is no per-area keyword mapping inside a single job.
- The area label is a label only. There is no geocoding of text place names.

### 06-07: jobs and live monitor

- Job archive, rename, ownership, and folders have no routes. The markup is
  capability-gated and never renders.
- Live runtime extension, live concurrency change, live proxy switching, and
  current-task retry have no routes and never render. Concurrency does adapt
  automatically between tasks, but not on operator command.
- Exact scraper version, owner, proxy performance, retries, warnings, and errors
  on the job detail page are shown as not recorded.
- The pipeline view renders generic stage status. Per-stage counters (keyword
  expansion counts, cells excluded, fields parsed, pages visited, HTTP status,
  merges, conflicts) are not emitted by the engine.
- Eleven of the thirteen live monitor values are genuinely sampled and durable.
  The website queue depth and active page count are not reported.
- The log severity filter offers ten levels, but the worker emits only
  `information`, `warning`, and `error`. Rate-limit, proxy-failure, browser-
  failure, website-timeout, and parsing-failure events are not produced.
- No per-listing cursor and no durable in-run deduplication state. An
  interrupted task restarts from the beginning of that task; committed rows are
  preserved and merged rather than duplicated.
- The checkpoint interval is not configurable. A checkpoint is committed after
  every completed task, which with a concurrent pool means several commit points
  can land close together.

### 08-09: Results and filtering

- Results use indexed server pagination, not client-side virtual scrolling.
  Column resize, reorder, freeze, grouping, and selected-row copy helpers are
  implemented in the browser; the saved layout lives in local storage and is not
  part of a saved view, which stores only the search.
- Inline editing covers the reviewed flag and per-record notes. No other field
  is editable from the table.
- The normalized result row omits description, street, plus code, phone type,
  per-email type/status, the individual social platforms, ratings breakdown,
  user reviews, and popular times. Some of these exist in the detail drawer or
  in `raw_json`, but they are not table columns.
- Bulk delete is a reversible soft delete: the record stops appearing in results
  and exports but keeps its sources, versions and provenance, and a restore
  action brings it back. There is no permanent purge.
- The record drawer has no website preview or screenshot, and tags are not
  editable from it.
- None of the eight suggested example views is seeded. Saved views exist only
  once an operator creates one.

### 10-11: deduplication and normalization

- Place ID, CID, Data ID, normalized phone, domain, and normalized address are
  stable exact identity keys. Exact matches merge automatically while retaining
  all source observations and versions.
- Duplicate candidates are reviewable side by side with score and match signals,
  and an operator can merge, keep both, or ignore a pair. Merging is
  non-destructive: the merged record keeps its row, versions and provenance, its
  source observations and per-job links move to the surviving record, and a
  reversible snapshot is written to `business_merges`. Keep-both writes a
  permanent non-match rule to `dedup_rules`. What remains absent is manual
  field-by-field value selection during a merge and an undo action; a merge is
  reversible only by reading the stored snapshot.
- Preferred values are chosen automatically by recency. There is no operator
  choice by source confidence, recency, or completeness.
- Phone, email, URL, name, whitespace, legal-suffix, address, rating, and review
  normalization are implemented. Registrable-domain/public-suffix resolution,
  full international address standardisation, phone type, and category ontology
  mapping are not comprehensive. `normalizeState` handles US state names only.
- Suspicious placeholder values and mismatched domains are not flagged. Social
  URL cleanup removes share/intent forms and fragments but not query tracking
  parameters.

### 12-16: email, website analysis, scoring, provenance, changes

- The enrichment worker extracts mailto, visible-text, contact/about, and
  structured-data emails with `[at]`/`(at)` de-obfuscation, validates syntax,
  normalizes domains, performs DNS/MX checks, classifies role and personal
  patterns, detects disposable domains from a local list, ranks relevance, and
  records source page and extraction method. JavaScript de-obfuscation,
  Cloudflare decoding, configurable patterns, and SMTP/mailbox verification are
  not implemented. SMTP is intentionally not presented as ownership proof.
- Website analysis covers reachability, status, final URL, redirect chain, HTTPS
  and certificate errors, response time, parked/coming-soon/placeholder
  detection, title and meta description, contact/about/phone/email/social
  presence, mobile viewport, page size, broken internal links, mixed content,
  old copyright year, template indicators, CMS signatures, and tracker
  signatures. Screenshots are not captured anywhere; `websites.screenshot_path`
  is declared and read but never written. Website-visible postal address
  extraction, accessibility and performance audits, cookie inspection, and
  certificate-date reporting are absent. Certificate-error detection is
  substring matching over the fetch error and has no dedicated test.
- Quality scoring is configurable and explainable per component, with versioned
  rules. Score-change history and alternative ranking profiles are absent.
- Provenance covers core normalized fields, every raw source observation, and
  website-sourced contacts. Four of seven source types are emitted
  (`google_maps_csv`, `website_homepage`, `website_contact`, `website_about`);
  footer and manual-edit source types are never written, and the operator and
  edit-reason columns are read and rendered but never populated because no
  manual field edit exists. Source grid cell is not populated for real jobs.
  Field-by-field rollback is not implemented.
- Change tracking records new businesses, changed fields, website active/
  inactive transitions, domain redirects, and newly discovered contacts, and
  change data can be included in exports. Listing removal, closure, and reopen
  detection are not implemented. There are no incremental-only scrape modes of
  any kind, no threshold alerts, and no version-retention setting.

### 17-18: schedules and reusable configurations

- Schedule overlap policy supports queue and skip; `replace` is absent.
- Scheduler-level retries with limits and backoff do not exist. Retry count and
  delay are per-job runner settings.
- There is no incremental-only mode, no run-on-start toggle, and no schedule- or
  job-completion export or webhook. Integration delivery fires on manual export
  creation only.
- There are no automatic retention rules for old runs, logs, or exports.
  Artifact cleanup is an operator-triggered System action.
- Templates store the full implemented job configuration with tags, folder,
  description, pin, use count, and last use, and support JSON import/export,
  duplicate, and delete. Parameter placeholders, starter-template seeding,
  average result count, average duration, and a dedicated rename action are
  absent.

### 19-21: proxies, adaptive performance, checkpoints

- Proxy import is pasted text only. Tests report Maps reachability and latency
  but call no IP-geolocation service, so exit IP and country remain unknown, and
  the slow/rate-limited/auth-failed/offline status taxonomy is not implemented.
- Least-recently-used, sticky-per-query, sticky-per-cell rotation and per-proxy
  task caps are not implemented. Disabled-proxy batch retest is manual.
- Adaptive concurrency reacts to CPU, available memory, and free disk, and every
  automatic change is recorded as a durable redacted event visible in the
  monitor. It does **not** react to block or failure rate, has no
  stable-success-window hysteresis, and reduces scrapemate worker concurrency
  rather than browser count or pages per browser.
- Browser-process crash restart, automatic retry with a fresh context or another
  proxy, all-proxies-failed pause/resume, and adaptive website timeout are not
  implemented. `StopReasonProxiesUnavailable` is defined but never emitted.
- Per-query and per-grid-cell continuation is implemented and tested.
  Per-listing continuation is not. The monitor's Checkpoint card now renders the
  recovery state, the last checkpoint time, and the completed/running/remaining/
  failed task counts.
- Lease deadlines are stored with one-second granularity, so a reclaim can lag a
  lapsed lease by up to a second. The production lease is 90 seconds with a
  20-second heartbeat, which makes that irrelevant in practice.

### 22: Export Centre

- Parquet is not implemented and is not offered.
- PostgreSQL/MySQL output is a portable transaction of `INSERT` statements, not
  a native server backup or restore archive.
- The SQLite export is a standalone portable subset file, not a copy of the
  workspace database.

### 23-25: API, integrations, and optional AI

- There is no job validate/dry-run endpoint, and no schedule update or
  run-history endpoint.
- OpenAPI JSON and the Redoc page are served and linked, and cURL/Python/
  JavaScript/Go examples are shown, but neither the document nor the examples
  are exercised by a test.
- Integrations cover local webhooks, a watch folder, and allowlisted post-run
  command hooks. Direct database sync to local PostgreSQL, MySQL/MariaDB, or
  another SQLite file is not implemented; those appear only as export formats.
  Google Sheets sync and custom plugin hooks for enrichment, validation,
  scoring, and export are absent.
- Optional local AI is implemented for the supported task set and stays disabled
  by default. Its HTTP handlers have no test; coverage stops at prompt
  construction, endpoint validation, and the transport.
- This remains a loopback-trust API. API keys and the local rate limiter add
  defence in depth, not a remote multi-user boundary.

### 26-28: storage, system, and settings

- SQLite/WAL/FTS5 is the only supported web database. The repository's separate
  CLI database modes are preserved, but the local UI has no PostgreSQL
  deployment or migration path.
- Retention settings are validated and stored but not executed. Maximum storage,
  backup count, and version-retention days have no enforcing job.
- Storage paths for data, exports, screenshots, logs, backups, and temporary
  files are configurable and path-contained. The map-tile cache path is fixed
  and there is no browser-profile directory setting.
- Browser version is not reported. The version panel shows Go, OS, SQLite,
  schema, and module version.
- Restart worker and online restore are deliberately absent. Restore is an
  offline procedure: stop the container, preserve the current database, verify
  the chosen backup, and replace `jobs.db`. A live SQLite file swap under active
  workers is unsafe. Proxy restores also require the adjacent
  `.proxy-master-key`; the key is never embedded into database downloads.
- The self-test checks database readability/writability, output directories,
  memory, disk, the scheduler heartbeat, and optionally Maps reachability. It
  does not launch a browser and does not verify proxy credentials.
- Scraping defaults omit a default location and a default proxy pool.
- Telemetry is hard-disabled and log redaction is implemented. There is no
  clear-browser-profiles action.

### 29-30: security, accessibility, and onboarding

- The default native bind is `127.0.0.1`; Compose publishes only
  `127.0.0.1:8080`. Wildcard binds warn. The local app has a restrictive CSP,
  framing/referrer/permission headers, no-store pages, CSRF, bounded uploads and
  request bodies, safe data paths, encrypted proxy URLs, secret redaction, and
  audit records. The preserved `/legacy` UI alone retains its historical CDN and
  tile allow-list.
- Framing is denied everywhere except `/app/map`, which allows same-origin
  framing only so the Results split view can embed it. Cross-origin framing
  stays blocked on every path.
- There is no login, password hashing, session, or cookie of any kind. Backups
  are plain SQLite copies with a SHA-256 checksum; encrypted backups are not
  implemented. Do not expose this build to an untrusted network without an
  authenticated reverse proxy and TLS.
- The app includes semantic landmarks, labels, a skip link, focus indicators,
  textual states, ARIA live progress, keyboard navigation, a command palette,
  the full suggested shortcut set, scalable layout, and reduced-motion CSS. It
  does not include an audited WCAG conformance report, a spreadsheet keyboard
  model, skeleton loaders, or tooltips for every advanced term.
- Onboarding verifies database integrity, data-directory existence, and HTTP
  binding, and can run a writable-directory/Maps test. It does not launch a
  browser, exercise proxies, or benchmark CPU/RAM/disk capacity.

### 31-34: stack, roadmap, acceptance, appendices

- The recommended stack is advisory and is deliberately not matched
  component-for-component. This build uses Go templates, vanilla JavaScript,
  custom CSS, SQLite/FTS5, embedded assets, vendored Leaflet, and Docker
  Compose. Alpine.js, Tabulator, and Apache ECharts are absent; HTMX remains in
  the preserved legacy UI only.
- XLSX is a hand-written OOXML/zip writer rather than Excelize, and scheduling
  uses a bespoke bounded five-field cron evaluator rather than robfig/cron.
  Both choices avoid new third-party dependencies in a local-first build and are
  intentional; the technology lines as literally written stay unmet.
- Structured slog/zerolog logging exists only in the SaaS-side packages. The
  local web build uses the standard library logger.
- Docker Compose packaging has manual runtime evidence in the implementation
  log; there is no automated Compose test.
- Release-roadmap and appendix items are accounted for by the implemented and
  limited groups above. Suggested directory/API/config examples are not treated
  as byte-for-byte contracts where the compatible existing engine uses a
  different representation.
- Large residential/mobile proxy supply, CAPTCHA solving, high-confidence
  mailbox verification, commercial enrichment databases, cloud workers, remote
  storage, and paid geocoding/SERP/Maps APIs are intentionally outside the free
  local build.

## Deliberate behaviour decisions

- **Seed identity.** Durable checkpoints need a stable seed ID so a completed
  query or grid cell can be recognised after a restart. That determinism is
  opt-in (`runner.WithDeterministicSeedIDs`) and is requested only by the web
  runner. The file, database, and Lambda runners keep the historical random
  identity, so the `input_id` CSV column stays per-run and a repeated
  `-produce` keeps enqueuing fresh rows instead of colliding with the previous
  run's primary keys.
- **Checkpointed execution is concurrent and leased.** A bounded pool of task
  workers shares one job's durable plan. A worker owns a task only while it
  holds an unexpired lease, so two workers never run the same task, a worker
  that dies loses its lease and the task returns to the queue, and a worker
  whose lease was reclaimed cannot overwrite the new owner's result. Resume
  granularity no longer costs sequential execution.
- **Parallel tasks divide the budget rather than multiplying it.** The job's
  concurrency and browser pool are shared out between workers, so four parallel
  tasks each run at a quarter of the configured capacity. Raising the parallel
  task count buys finer resume granularity and lower latency, not extra load.
  The pool size is configurable per job and in Settings, bounded to 16.
- **CSV merges are serialised.** Task results are idempotent by business
  identity, but they rewrite one file, so exactly one merge runs at a time.
- **An interrupted task is released, not failed.** Cancellation, shutdown, and
  the low-disk pause return the task to the queue without consuming one of its
  attempts, so a restart resumes it exactly. Committed rows are kept either way.
- **One failing task does not fail the job.** A crashing task is retried up to
  its configured attempt limit while other workers continue; a run that loses
  tasks ends as partial with a task-failure reason rather than failed.
- **Cancelled enrichment work is not failed.** A task interrupted by worker
  shutdown is left in its running state so startup recovery requeues it, rather
  than being recorded as a permanent website failure.

## Field issue: 37 spreadsheet lines and a pending job

The inspected CSV for job `ba78441f-a048-4c9d-a8de-d0589e66f132` has 37 lines:
one header plus 36 data rows. All 36 Place IDs are unique. The run used three
queries, a ten-minute limit, depth 10, and synchronous website/email crawling.
Logs showed at least 60 candidates before the deadline, so the low committed
row count was a runtime/enrichment bottleneck, not spreadsheet deduplication.

The worker is single-queue/FIFO. `pending` (shown as `queued` in the upgraded
UI) means the durable job exists but no worker has claimed it yet. It normally
starts within one second when no job is active. A long-lived queued state means
another scrape is occupying the worker or the Docker process is not running;
the monitor now states which case is visible and points to System/log checks.

## Development-tooling boundary

The race-enabled test suite, `go vet`, and the production build pass. The
repository's unusually broad opinionated `golangci-lint` profile still reports
style and refactoring debt such as repeated string constants, function
complexity, value-copy performance suggestions, internal-package test layout,
exhaustive-enum annotations, and whitespace-cuddling rules. Security-relevant
dynamic SQL and type-assertion findings encountered during this implementation
were fixed. A zero-warning opinionated-lint claim is deliberately not made;
clearing the remaining report would require broad non-functional refactors
outside the verified product behavior and existing compatibility boundary.

`-race` requires cgo and a C toolchain, which the Windows development host does
not provide. The race gate therefore runs in a `golang:1.26.6` container against
the same worktree; the non-race suite runs natively.

Known test gaps that keep otherwise-working features unchecked in
`implementation-progress.md`: the maintenance endpoints
(`cache/clear`, `artifacts/cleanup`, `jobs/stop-all`, `diagnostics/download`,
`update-info`), the local AI HTTP handlers, the OpenAPI document and Redoc
route, the template JSON import/export routes, the enrichment change and
discovery records, the adaptive memory branch, and the map keyword-group
assignment action.
