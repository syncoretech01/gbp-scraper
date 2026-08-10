# Local upgrade: implemented scope and technical limitations

Last reconciled with `Google_Maps_Scraper_Local_Improvement_Specification.md`:
2026-08-07.

This document is the authoritative boundary for specification items that are
not implemented in this local build. A menu item or button is not displayed
unless its backend path exists. The original CLI, API data model, CSV files,
and scraper engine remain compatible; this upgrade does not claim that every
idea in the product specification is complete.

## Completed local foundation

The build provides the following working paths:

- Loopback-only Docker publishing, telemetry disabled by default, a persistent
  `webdata` bind mount, local onboarding, responsive application shell, light,
  dark, and system themes, keyboard navigation, command navigation, focus
  styles, state text, confirmations, and empty states.
- A seven-step scrape form with pasted/TXT/CSV queries, exact query
  deduplication, San Francisco centre and bounding-box defaults, grid-size and
  task estimates, Fast/Balanced/Deep presets, radius guidance, per-job depth,
  runtime, concurrency, browser pool, pages per browser, email/review settings,
  proxy-pool selection, draft/start, and template saving.
- Durable FIFO lifecycle states, compare-and-swap controls, pause/resume/cancel,
  safe retry/restart, redacted ordered events, SSE, partial outcomes, timeout
  classification, result preservation, restart recovery, and retry-safe CSV
  merging.
- SQLite migrations with verified pre-migration copies, forward-schema refusal,
  WAL, foreign keys, FTS5, normalized business/source/version/provenance tables,
  exact identity merging, conservative fuzzy review candidates, quality and
  confidence values, indexed search/filters/sorts, saved result views, tags,
  reviewed state, notes, and auditable workflow changes.
- Dashboard, Jobs, live monitor, dependency-free coverage/result map, Results,
  business detail/provenance/history, templates, saved views, schedules, proxy
  pools, exports, Local API, Settings, System, and onboarding/help pages.
- Verified database backups, checksum/history/download, integrity check,
  VACUUM, system/storage snapshot, and explicit offline restore instructions.
- CSV, JSON, JSONL, GeoJSON, KML, VCard, plain text, PostgreSQL-compatible SQL,
  and MySQL-compatible SQL export with filtering, pagination-safe streaming,
  atomic file replacement, SHA-256, history, repeat download, and deletion.
- One-time, hourly, daily, weekly, monthly, and bounded five-field cron schedules
  with IANA time zones, queue/skip overlap policy, missed-run handling, manual
  run, durable history, and the same ordinary job queue.
- HTTP/HTTPS/SOCKS5 proxy pools with authenticated URLs, AES-256-GCM credentials
  under a separate local key, masked display/logs, exact import deduplication,
  on-demand Google Maps test, health counters, cooldown, automatic disabling,
  and stable/random/fastest/lowest-failure ordering.

## Requirement boundaries by specification section

### 01-03: product shell and dashboard

- Global search is not cross-entity. Business FTS, job filtering, template
  search, and command navigation are separate because a ranked union index
  across unrelated records has not been designed.
- Dashboard charts are limited to collection dates and contact availability.
  City/category/rating, proxy distribution, browser-failure, website-active,
  block-rate, and long-term trend charts require persisted time-series samples
  that the scraper engine does not currently emit.
- CPU, RAM, remaining disk, browser/page counts, and proxy rates are not live
  per-job metrics. The current Scrapemate integration has no durable callback
  for these values, so the UI labels them as not reported rather than inventing
  telemetry.

### 04: New Scrape Wizard

- No category taxonomy/groups, include/exclude expression builder, fuzzy query
  warning, category-by-location generator, reusable keyword set, direct-URL
  editor, multi-city uploader, saved area, polygon/circle editor, per-cell
  deletion, or exclusion polygon exists.
- Field checkboxes are descriptive retention guidance; the upstream CSV schema
  remains compatible and is not dynamically pruned. Extended Maps fields are
  retained when the engine supplies them, but are not individually selectable.
- Website crawl scope, contact/about page limits, page timeout, URL patterns,
  screenshots, maximum-record count, retry/backoff/random delay, visible
  browser, request asset blocking, memory cap, and low-disk pause are not
  exposed because the web runner has no safe per-job implementation for them.
- Pre-scrape rating/category/status/name filters and post-scrape automation are
  not job-engine filters. Equivalent supported filters can be applied in
  Results after collection.

### 05: Map Explorer

- The map is a local, server-rendered SVG coverage/result view. It deliberately
  does not download Leaflet, OpenStreetMap tiles, or geocoding data at runtime.
- Circle/polygon drawing, geometry import/export, editable/grouped cells,
  keyword assignment per area, clustering, heatmaps, shared advanced filters,
  polygon export, and selected-cell retry are absent. They require a persisted
  geometry/task model and a bundled map renderer, not merely front-end buttons.

### 06-07: jobs and live monitor

- Rename, archive, ownership/folders/notes for jobs, live runtime extension,
  live concurrency changes, live proxy switching, current-task retry, and
  manual checkpoint selection are hidden because active Scrapemate execution
  cannot safely accept those changes.
- Checkpoint recovery is file/result and lifecycle based. Individual query,
  grid, listing, and enrichment task cursors are not emitted by the upstream
  engine, so the `job_tasks` model is not presented as complete per-task resume.
- Pipeline stage details and current keyword/cell/browser/proxy/resource values
  are inferred or labelled not reported when the engine supplies no callback.
  Logs contain durable lifecycle/import/outcome events, not every browser
  console or network event.

### 08-09: Results and filtering

- Results use indexed server pagination up to 250 rows per page, not client-side
  virtual scrolling. Column resize/reorder/freeze/group, inline field editing,
  keyboard spreadsheet editing, copy-selected helpers, saved column layouts,
  and split table/map mode are absent.
- Bulk tag/untag and reviewed/unreviewed are implemented; notes are editable per
  record. Bulk delete, selected-row export, saved lists, website/email recheck,
  enrichment, duplicate merge, and open-many-websites are hidden.
- Filters are a safe flat AND list. OR/nested groups, not-contains/not-equal,
  ends-with, between, date ranges, category sets, radius, and polygon operators
  need an explicit expression AST and spatial index and are not implemented.
- Social/contact subtypes, ratings breakdowns, popular-times presentation,
  technology columns, screenshots, and website preview are not normalized into
  the current result response even if some raw CSV JSON contains them.

### 10-11: deduplication and normalization

- Place ID, CID, Data ID, normalized phone, domain, and normalized address are
  stable exact identity keys. Exact matches merge automatically while retaining
  all source observations and versions.
- Name/address/city/postal/proximity combinations create conservative fuzzy
  candidates only. Side-by-side review, keep-both, ignore/non-match rules,
  manual field selection, merge, and undo are not exposed; destructive merge
  semantics need a separately tested reconciliation and rollback design.
- Phone, email, URL, name, whitespace, legal-suffix, address, rating, and review
  normalization are implemented. Phone type, registrable-domain/public-suffix
  resolution, full international address standardisation, social-URL cleanup,
  category ontology mapping, and all placeholder/domain-mismatch heuristics are
  not comprehensive.

### 12-16: email, website analysis, scoring, provenance, changes

- Existing website crawling extracts visible/mailto emails and the normalizer
  validates syntax, lowercases, deduplicates, classifies common role/personal
  patterns, and records source/confidence. JavaScript de-obfuscation, Cloudflare
  decoding, configurable patterns, contact/about crawl scopes, DNS/MX queries,
  disposable-domain lists, optional SMTP checks, and mailbox verification are
  not implemented. SMTP is intentionally not presented as ownership proof.
- Website status, HTTP/HTTPS fallback, redirect chain, SSL dates, page metadata,
  language, CMS, analytics, social links, screenshots, accessibility/performance
  audit, structured data, cookies, trackers, and mobile checks are not run by a
  separate durable enrichment worker.
- Quality score is a fixed, explainable-in-code completeness score with a
  confidence value. User-configurable weights, per-component UI explanations,
  penalties, ranking profiles, and score-change history are absent.
- Provenance and immutable versions cover core normalized fields and every raw
  source observation. Website-source provenance, manual-edit provenance,
  preferred-source conflict UI, and field-by-field rollback are not complete.
- Reimports record changed snapshots/fields. Incremental-only scrape modes,
  disappearance/closure confidence across repeated runs, threshold alerts,
  saved diff views, and change exports are not implemented.

### 17-18: schedules and reusable configurations

- Scheduler retries/backoff, run-on-start toggle, replace-active policy,
  incremental mode, automatic export, webhook, and retention rules are absent.
  One missed run can be queued or skipped after the application returns.
- Templates store full implemented job configuration, tags/folder/description,
  pin/use count/last use and support JSON import/export/duplicate/delete.
  Parameter placeholders, starter-template seeding, average result count,
  average duration, and a dedicated rename action are absent.

### 19-21: proxies, adaptive performance, checkpoints

- Proxy import is pasted text only, not multipart TXT/CSV. Tests report Maps
  reachability and latency but do not call an external IP-geolocation service,
  so exit IP and country remain unknown.
- Least-recently-used, sticky-query, sticky-cell, and per-proxy task caps are not
  implemented. Disabled-proxy batch retest is manual. Usage count means pool
  assignment, not a browser-level request counter.
- Adaptive concurrency, RAM-pressure browser reduction, low-disk pause,
  automatic fresh-context/proxy retry, browser-process restart policy,
  all-proxies-failed resume, and adaptive website timeout are absent because
  the runner has no safe live reconfiguration/task-requeue interface.
- Committed CSV/SQLite rows, redacted events, partial state, abandoned-running
  recovery, retry files, and migration backups are durable. Exact per-query and
  per-cell checkpoint continuation is not claimed.

### 22: Export Centre

- XLSX, a standalone SQLite subset, Parquet, ZIP, custom/reordered columns,
  grouping/splitting, raw/provenance/history bundles, selected-row export, and
  reusable export presets are not implemented and are not shown.
- PostgreSQL/MySQL output is a portable transaction of `INSERT` statements,
  not a native server backup or restore archive.

### 23-25: API, integrations, and optional AI

- The versioned API covers jobs, lifecycle/progress/events/download, normalized
  result reads/workflow updates, health/backups, settings, exports, templates,
  schedules, proxies, and onboarding actions used by the UI. The downloadable
  OpenAPI document intentionally describes the stable public subset.
- Maps/task mutation, duplicate review, enrichment, rich bulk operations,
  diagnostics archive, API key management, request logging, and local request
  rate limits are not implemented. UI mutations require CSRF; this is a
  loopback-trust API, not a remote multi-user boundary.
- n8n/Activepieces callbacks, database sync, watch folder, post-run scripts,
  local webhooks, Google Sheets, plugin hooks, and Ollama features are absent.
  Optional AI remains disabled and no paid service is mandatory.

### 26-28: storage, system, and settings

- SQLite/WAL/FTS5 is the supported web database. The repository's separate
  CLI/database modes are preserved, but the local UI has no PostgreSQL
  multi-worker deployment or migration path.
- Schema tables exist for the planned model, but website/enrichment records,
  rich social/email status, task checkpoints, retention execution, and storage
  quotas are not populated as completed features.
- Exports and backups have separate local directories. Screenshot, browser
  profile, cache, log, and temporary-directory configuration/retention are not
  exposed.
- System supports integrity, VACUUM, verified backup/download, storage/database
  counts, binding/version display, and a live database/path/Maps self-test.
  Worker restart/stop-all, cache cleanup, retention cleanup, online restore,
  diagnostics bundle, update check, browser-launch proof, CPU/RAM/disk threshold
  checks, and worker heartbeat UI are absent.
- Restore is deliberately offline: stop the container, preserve the current DB,
  verify the chosen backup, and replace `jobs.db`. A live SQLite file swap under
  active workers is unsafe. Proxy restores also require the adjacent
  `.proxy-master-key`; the key is never embedded into database downloads.
- Settings persist implemented scraper defaults and appearance. Storage paths,
  quotas/retention, backup counts, proxy default, visible-browser default,
  compact table/sidebar/date-number locale/font controls, and profile clearing
  are not implemented.

### 29-30: security, accessibility, and onboarding

- The default native bind is `127.0.0.1`; Compose publishes only
  `127.0.0.1:8080`. Wildcard binds warn. The local app has restrictive CSP,
  framing/referrer/permission headers, no-store pages, CSRF, bounded uploads and
  request bodies, safe data paths, encrypted proxy URLs, secret redaction, and
  audit records. The preserved `/legacy` UI alone retains its historical CDN
  and tile allow-list.
- No login, session cookie, API key, local rate limiter, encrypted backup,
  privacy-scrubbed diagnostics bundle, or remote-access hardening exists. No
  secure-cookie claim is relevant because this build creates no login session.
  Do not expose it to an untrusted network without an authenticated reverse
  proxy and TLS.
- The app includes semantic landmarks, labels, skip link, focus indicators,
  textual states, ARIA live progress, keyboard navigation/command palette,
  scalable layout, and reduced-motion CSS. It does not include a full audited
  WCAG conformance report, spreadsheet keyboard model, skeleton loading system,
  or tooltips for every advanced term.
- Onboarding verifies DB integrity/data-directory existence and can explicitly
  run a writable-directory/Maps test. It does not launch a second browser,
  exercise every proxy, or benchmark available CPU/RAM/disk capacity.

### 31-34: stack, roadmap, acceptance, appendices

- The recommended stack is advisory. This build uses Go templates, vanilla
  JavaScript, custom CSS, SQLite/FTS5, embedded assets, and Docker Compose. It
  does not add Alpine, Tabulator, ECharts, robfig/cron, Excelize, Ollama, or a
  new remote map dependency merely to match a technology suggestion.
- Release-roadmap and appendix items are accounted for by the implemented and
  limited groups above. Suggested directory/API/config examples are not treated
  as mandatory byte-for-byte contracts where the compatible existing engine
  uses a different representation.
- Large residential/mobile proxy supply, CAPTCHA solving, high-confidence
  mailbox verification, commercial enrichment databases, cloud workers,
  remote storage, and paid geocoding/SERP/Maps APIs are intentionally outside
  the free local build.

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

The full race-enabled test suite, `go vet`, production build, Compose image
build, readiness, migration, and final-image restart checks pass. The
repository's unusually broad opinionated `golangci-lint` profile still reports
style and refactoring debt such as repeated string constants, function
complexity, value-copy performance suggestions, internal-package test layout,
exhaustive-enum annotations, and whitespace-cuddling rules. Security-relevant
dynamic SQL/type-assertion findings encountered during this implementation were
fixed. A zero-warning opinionated-lint claim is deliberately not made; clearing
the remaining report would require broad non-functional refactors outside the
verified product behavior and existing compatibility boundary.
