# Local upgrade: implemented scope and technical limitations

Last reconciled with `Google_Maps_Scraper_Local_Improvement_Specification.md`:
2026-08-20.

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
- The dashboard charts enabled-proxy latency buckets and per-pool reliability
  percentages. A time-series latency history is not kept.
- The storage tile reports database, export, and log sizes plus remaining free
  disk. Screenshot storage is always zero because nothing captures screenshots.

### 04: New Scrape Wizard

- No business-category taxonomy or curated category picker; the combination
  generator takes free-text category lines instead.
- Include/exclude keyword filters, a category x location combination generator
  with a locations file (TXT/CSV, first column), and reusable named keyword
  sets are implemented. Filters apply on an explicit step and everything
  resolves to plain query lines, so engine compatibility is untouched.
- Drawing happens on the Map Explorer and reaches the wizard as a saved-area
  snapshot; there is no map canvas inside the wizard itself.
- No per-field selection UI. Extended Maps fields (popular times, images,
  reservations, ordering links, menus, owner information, reviews) are retained
  inside `businesses.raw_json` but are not individually selectable, normalized
  into columns, or exportable as separate fields. `input_id` is stored but is
  not offered as an export column.
- No job-engine pre-scrape filters for rating, review count, included/excluded
  categories, open/closed status, claimed status, or business-name conditions.
  Equivalent filters can be applied in Results after collection.
- No per-request font or video blocking. Image blocking, the low-disk pause,
  failure screenshots, crawler URL include/exclude patterns, and an
  application-level memory ceiling all ship; see the browser-level resource
  control entry below for what remains and why.

### 05: Map Explorer

- Heat layers exist for result density (screen-space buckets over the loaded
  results), for failed and empty coverage cells, and for duplicate-heavy cells
  (per-cell duplicate counts aggregated from task checkpoints), all rendered
  with vendored Leaflet primitives only.
- Individual cells cannot be resized or grouped. Cell geometry is derived
  deterministically from one cell-size input; cells can be selected, excluded,
  and rescraped, but not edited.
- Assigning a saved template's keywords to a selected cell set is implemented,
  but there is no per-area keyword mapping inside a single job.
- The area label is a label only. There is no geocoding of text place names.

### 06-07: jobs and live monitor

- Job rename, archive/restore and operator notes are implemented as metadata
  that never touches lifecycle state; archiving is refused while a job is still
  active. Ownership labels, job tags and folders still have no routes, and the
  `folder` column is never written.
- Live controls are implemented: add runtime, change concurrency, switch the
  proxy pool (including to a direct connection), and retry-current all store a
  durable, audited request that the worker applies at the next safe task
  boundary — a change is never instant mid-task, and the UI says so.
- Exact scraper version, owner, proxy performance, retries, warnings, and errors
  on the job detail page are shown as not recorded.
- The pipeline view renders generic stage status. Per-stage counters (keyword
  expansion counts, cells excluded, fields parsed, pages visited, HTTP status,
  merges, conflicts) are not emitted by the engine.
- Eleven of the thirteen live monitor values are genuinely sampled and durable.
  The website queue depth and active page count are not reported.
- Task failures are classified into browser-failure, proxy-failure,
  website-timeout, parsing-failure, and task-failed event types. Rate-limit
  events specifically cannot be produced because the engine emits no
  block/rate-limit signal (see the genuinely-infeasible list).
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
- Inline editing covers the reviewed flag, per-record notes, and — through the
  record drawer — manual edits of name, phone, website, and category, each
  requiring a reason and recorded with operator, date, and previous value as
  manual-edit provenance. Other fields are not editable, and there is no
  spreadsheet-style cell editing in the table itself.
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
- Suspicious placeholder phones, websites, and emails are flagged at import
  without dropping data. Email/website domain mismatch is used only as a
  relevance signal, not surfaced as a flag. Social URL cleanup removes
  share/intent forms, fragments, and common tracking parameters.

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
  signatures. Homepage screenshots are captured on request through headless
  Playwright when the driver is installed (the Docker image installs it; the
  capture is skipped honestly when absent and never fails a task), stored under
  the screenshots directory and shown in the record drawer. Error-page
  screenshots, website-visible postal address extraction, accessibility and
  performance audits, cookie inspection, and certificate-date reporting are
  absent. Certificate-error detection is substring matching over the fetch
  error and has no dedicated test.
- Quality scoring is configurable and explainable per component, with versioned
  rules. Score-change history and alternative ranking profiles are absent.
- Provenance covers core normalized fields, every raw source observation, and
  website-sourced contacts. Four of seven source types are emitted
  (`google_maps_csv`, `website_homepage`, `website_contact`, `website_about`);
  the footer source type is never written. Manual edits populate the operator
  and edit-reason columns. Source grid cell is not populated for real jobs.
  Field-by-field rollback is not implemented.
- Change tracking records new businesses, changed fields, website active/
  inactive transitions, domain redirects, and newly discovered contacts, and
  change data can be included in exports. Version retention is configurable and
  executed (each business always keeps its newest snapshot). Rescan modes exist:
  a job can be marked new-only or new-and-changed, imports classify every
  business as new/changed/unchanged, businesses missing from a rescan are
  flagged possibly_removed with a not_seen_in_rescan change row (evidence,
  never deletion) and restored as reappeared when seen again, and each import
  records a summary event. Detection happens at import — the scraper still
  visits listings, because a listing must be fetched to know it changed.
  Threshold alerts are absent.

### 17-18: schedules and reusable configurations

- Schedule overlap policy supports queue, skip, and replace (replace cancels
  the still-active job through the ordinary lifecycle control).
- Scheduler-level retries exist: up to ten extra attempts with a bounded
  backoff, tracked per run. Auto-export after a completed run is available in
  every advertised export format; a completion webhook is not wired to
  schedules (webhooks fire on export creation).
- Schedules inherit the job's rescan mode through the template configuration;
  there is no run-on-start toggle.
- Old schedule runs are pruned per-schedule by a configurable retention window.
  Log-file retention remains an operator-triggered System action.
- Templates store the full implemented job configuration with tags, folder,
  description, pin, use count, and last use, and support JSON import/export,
  duplicate, and delete. Five starter templates and six example saved views
  seed once into an empty workspace. Parameter placeholders, average result
  count, average duration, and a dedicated rename action are absent.

### 19-21: proxies, adaptive performance, checkpoints

- Proxy import is pasted text only. Tests report Maps reachability and latency
  but call no IP-geolocation service, so exit IP and country remain unknown, and
  the slow/rate-limited/auth-failed/offline status taxonomy is not implemented.
- Sticky-per-query and sticky-per-cell strategies pin each task to one proxy
  by a stable hash, and pools carry an optional per-proxy task cap. Caps and
  failure attribution require sticky assignment; non-sticky pools keep their
  whole-list rotation, where per-proxy accounting is not possible. A
  least-recently-used strategy is a deferred optional feature. Disabled proxies
  can be batch-retested per pool (up to 50 at a time) and healthy ones are
  re-enabled; the retest is operator-triggered, not automatic.
- Adaptive concurrency reacts to CPU, available memory, free disk, and the
  recent task failure rate: a window where at least half the attempts failed
  halves the budget, and only a fully clean window recovers one step, so decay
  always outpaces recovery. Every change is recorded with its reason. Block
  rate specifically is not measurable (the engine emits no block callback),
  and adaptation adjusts worker concurrency rather than browser count or pages
  per browser.
- Browser crashes are classified, recorded as browser-failure events, and every
  retry constructs a fresh browser context, so a crash never fails the job by
  itself. When a sticky pool's last usable proxy fails or caps out, the job
  pauses as proxies_unavailable and resumes recoverably (resume is an operator
  action, not automatic polling of the pool). Adaptive website timeout for the
  enrichment crawler is a deferred optional feature.
- Per-query and per-grid-cell continuation is implemented and tested.
  Per-listing continuation is not. The monitor's Checkpoint card now renders the
  recovery state, the last checkpoint time, and the completed/running/remaining/
  failed task counts.
- Lease deadlines are stored with one-second granularity, so a reclaim can lag a
  lapsed lease by up to a second. The production lease is 90 seconds with a
  20-second heartbeat, which makes that irrelevant in practice.

### 22: Export Centre

- Parquet is a deferred optional format (see the deferred list below).
- PostgreSQL/MySQL output is a portable transaction of `INSERT` statements, not
  a native server backup or restore archive.
- The SQLite export is a standalone portable subset file, not a copy of the
  workspace database.

### 23-25: API, integrations, and optional AI

- POST /api/v1/jobs/validate provides create-identical dry-run validation, and
  schedules have update (PUT) and run-history endpoints.
- OpenAPI JSON and the Redoc page are served and linked, and cURL/Python/
  JavaScript/Go examples are shown, but neither the document nor the examples
  are exercised by a test.
- Integrations cover local webhooks, a watch folder, and allowlisted post-run
  command hooks. Direct database sync to local PostgreSQL, MySQL/MariaDB, or
  another SQLite file is not implemented; those appear only as export formats.
  Google Sheets sync and custom plugin hooks are deferred optional features
  (see the deferred list below).
- Optional local AI is implemented for the supported task set and stays
  disabled by default; its status/assist handlers are route-tested. Only the
  keyword-variation and result-filter tasks are exercised end-to-end in the
  UI; the remaining assist tasks are reachable through the documented API.
- This remains a loopback-trust API. API keys and the local rate limiter add
  defence in depth, not a remote multi-user boundary.

### 26-28: storage, system, and settings

- SQLite/WAL/FTS5 is the only supported web database. The repository's separate
  CLI database modes are preserved, but the local UI has no PostgreSQL
  deployment or migration path.
- Retention settings are executed: a pass at worker start (and on demand via
  the API) prunes manual backups beyond the configured count, version
  snapshots beyond their window, and — when the storage cap is exceeded — the
  oldest completed exports. Pre-migration safety copies, job CSVs, and the
  database itself are never retention candidates.
- Storage paths for data, exports, screenshots, logs, backups, and temporary
  files are configurable and path-contained. The map-tile cache path is fixed
  and there is no browser-profile directory setting.
- The version panel shows Go, OS, SQLite, schema, module, and the browser
  automation (Playwright driver module) version.
- Restart worker and online restore are deliberately absent. Restore is an
  offline procedure: stop the container, preserve the current database, verify
  the chosen backup, and replace `jobs.db`. A live SQLite file swap under active
  workers is unsafe. Proxy restores also require the adjacent
  `.proxy-master-key`; the key is never embedded into database downloads.
- The self-test checks database readability/writability, output directories,
  memory, disk, the scheduler heartbeat, browser-runtime presence (driver
  directory, honestly labelled — only a real scrape proves a launch), and
  optionally Maps reachability plus one enabled proxy's credentials. It never
  launches a browser.
- Scraping defaults include a default location label/coordinates and a default
  proxy pool alongside the existing engine defaults.
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
- Optional local login exists: a bcrypt-hashed password stored in settings,
  in-memory sessions with a configurable timeout (a restart signs everyone
  out by design), per-address login rate limiting, and full session
  invalidation on any credential change. While enabled, pages require a
  session and API requests require an API key. The cookie is HttpOnly and
  SameSite=Strict but not Secure, because the local app serves plain HTTP.
  Backups are plain SQLite copies with a SHA-256 checksum; encrypted backups
  are not implemented. Do not expose this build to an untrusted network
  without a reverse proxy and TLS.
- The app includes semantic landmarks, labels, a skip link, focus indicators,
  textual states, ARIA live progress, keyboard navigation, a command palette,
  the full suggested shortcut set, scalable layout, and reduced-motion CSS. It
  does not include an audited WCAG conformance report, a spreadsheet keyboard
  model, skeleton loaders, or tooltips for every advanced term.
- Onboarding verifies database integrity, data-directory existence, HTTP
  binding, and free disk capacity (warning under 2 GB), and can run a
  writable-directory/Maps test. It does not launch a browser or exercise
  proxies.

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

## Deliberate deviations: the capability ships by another route

Each line below appears unticked in the checklist because the specification
named a particular *mechanism*. The capability itself ships; the mechanism
differs, deliberately, and the equivalent is named here.

- **Post-run shell command / Python script, and in-process plugin hooks.**
  Ruled out on security grounds, not effort: executing operator-supplied
  processes from a locally served web UI creates a code-execution surface that
  a single request-forgery or template mistake turns into remote code
  execution on the host. The automation need is met by the signed outbound
  webhook (HMAC shared secret, retries with backoff, delivery history) plus the
  local REST API, which is exactly how a self-hosted n8n or Activepieces
  instance drives this application in both directions.
- **HTMX, Alpine.js, Tailwind, Tabulator, Apache ECharts.** The UI is
  server-rendered Go templates with vanilla JavaScript and a documented token +
  component design system in `web/static/css/app.css`. It delivers what those
  libraries were recommended for - progressive enhancement, modal/form state,
  a consistent visual system, a dense sortable/filterable/groupable data table
  with column management, and charts drawn as inline SVG/CSS - with no runtime
  CDN (the strict CSP forbids one), no build tooling, and no npm.
- **robfig/cron, Excelize.** The local scheduler (`web/schedules.go`) and the
  native XLSX writer (`web/export_xlsx.go`) provide the same capability without
  the dependency.
- **Go slog / Zerolog.** Structured, redacted job events are persisted to
  SQLite and served to the Job Monitor, which is the observability the
  recommendation stood for; process logging stays on the standard library.
- **Client-side virtual scrolling.** Indexed server-side pagination with
  configurable page size is the chosen responsiveness design; it keeps large
  workspaces responsive without holding the result set in the browser.
- **PostgreSQL as the application workspace.** SQLite with WAL is the default
  and covers local scale; the CLI's PostgreSQL run modes remain available for
  the scraper itself. Porting every repository method is a second persistence
  implementation, not a missing feature.

## Genuinely infeasible locally, or external-only

- **Google Sheets sync.** Requires Google OAuth credentials and an external
  service. Exports (CSV, JSON, JSONL, XLSX, Parquet, SQLite), the watch folder,
  the webhook, and the local database destinations cover the same workflow
  offline.
- **Site-whisper deep website auditing, mailbox-level email verification, and
  the CRM/Lead-Engine pipeline.** These stay in their own repositories per the
  GBP specification's DO-NOT-BUILD table. Website-status classification itself
  is fully local, so none of the eight statuses depends on an external service.
  The DiscoveredCompany endpoint and its export stay dormant behind the
  off-by-default `prospect.future_integrations` setting.
- **`domain_age_days`** needs WHOIS data from an external service.
- **Exit-IP geolocation** for proxies needs an external geolocation service or
  a licensed database. Note the narrower, honest boundary that remains inside
  the exit-IP feature: an HTTP CONNECT tunnel carries no outbound-address field
  in the protocol, so for HTTP/HTTPS proxies the stored address is the endpoint
  this host dialled rather than a confirmed exit; SOCKS5 can report the bound
  address, and the source of the value is stored beside it.
- **Per-listing checkpoint cursors inside a single task.** The engine commits a
  task's results as one unit; the merge-deduplicated task restart makes the gap
  harmless in practice.
- **Browser-level resource controls beyond image blocking** - font and video
  blocking, and a *hard per-browser* memory cap enforced by the browser itself.
  Image blocking and the low-disk pause both ship. Font and video blocking, and
  a browser-enforced memory limit, would each require extra Chromium launch
  arguments or a route interceptor: `scrapemateapp` builds the launch args
  internally and exposes only `DisableImages`, with no hook for either, so the
  only way to add them is to fork the upstream engine, which the compatibility
  constraint rules out.

  What *does* ship is the application-level memory ceiling
  (`JobData.MemoryCeilingBytes`, wizard field `memory_ceiling_mb`). It caps how
  much work this application asks for rather than how much the browser may
  allocate: while the sampled host memory in use is at or above the ceiling the
  adaptive supervisor pins the run to one worker and one browser with one page,
  vetoes every recovery step, and records an `adaptive-performance` worker
  event naming the ceiling and the measurement. Zero means no ceiling, and it
  can only ever lower a budget. Enforcement requires the job's adaptive
  resource safeguards to be on, because that supervisor is what applies it.
  This is a real, useful bound on the run's footprint, but it is not the same
  guarantee as a per-process RSS limit: it throttles demand and cannot stop a
  single browser process that is already running from growing.
- **Large, stable residential/mobile proxy networks, CAPTCHA solving,
  high-confidence mailbox verification, and commercial company/person
  databases.** External paid services by nature.

## Corrections applied on 2026-08-22

The previous revision of this document asserted several boundaries that later
releases removed. They are corrected here rather than left to mislead:

- **Parquet export** now ships (`web/export_parquet.go`, typed columns).
- **Adaptive website timeout** ships, opt-in, and only ever shortens the
  per-request budget.
- **Error-page screenshots** ship alongside homepage screenshots.
- **Least-recently-used proxy rotation** ships (`proxies.last_used_at`).
- **Block-rate measurement** ships: it is computed from the refusal events the
  worker genuinely records, against that day's finished tasks, and feeds both
  the dashboard trend and the adaptive concurrency decision.
- **A curated business-category vocabulary** ships with the wizard's category
  picker and reusable category groups.
- **Encrypted backups** ship through the maintenance repository.
- **Collection-skipping incremental modes** ship (`new_only`, `new_changed`,
  `volatile_fields`, and the stale-contacts re-enrichment mode).
- The claim that "the post-run command hook covers scripted extension today"
  was simply wrong: no such hook existed, and it is now a deliberate security
  decision documented above.

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
`update-info`), the template JSON import/export routes, the enrichment change
and discovery records, the adaptive memory branch, the map keyword-group
assignment action, and the export-preset repository methods.
