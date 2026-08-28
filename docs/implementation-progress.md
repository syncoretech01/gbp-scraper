# Local Improvement Implementation Progress

Last updated: 2026-08-25

## Source and status

- [x] Restored the authoritative specification from the user's local Downloads folder to `docs/Google_Maps_Scraper_Local_Improvement_Specification.md`.
- [x] Verified the repository copy matches the 1,385-line source after line-ending normalization.
- [x] Read the complete specification and mapped its explicit list/table requirements below.
- [x] Every specification requirement is now either implemented and verified, or recorded in `docs/technical-limitations.md` with its exact classification. Nine checklist lines remain unticked on purpose; each is listed there.
- [x] Completed requirement reconciliation: working paths are recorded here and every remaining group has a specific boundary in `docs/technical-limitations.md`.

## Status legend

- `[ ]` not implemented/proven; its section-level technical boundary is recorded in `docs/technical-limitations.md`.
- `[x]` implemented and verified.
- A requirement may only be checked after all three hold: the implementation
  exists, it is reachable through a registered route or a rendered control, and
  a named test exercises it. A feature that works but has no test stays
  unchecked, and the gap is listed in `docs/technical-limitations.md`.

## Upgrade evidence summary

- [x] Durable lifecycle, partial outcomes, pause/resume/cancel/retry, redacted events, SSE, and restart recovery.
- [x] Safe schema v0-to-v5 migration with verified pre-migration backups, forward-schema refusal, rollback tests, and an existing-data copy smoke test.
- [x] Retry-safe compatible CSV output plus normalized SQLite import, FTS5, exact and conservative fuzzy deduplication, provenance, versions, and change records.
- [x] Local application routes for dashboard, wizard, jobs/monitor, map, results/detail, saved views/templates, schedules, proxies, exports, API, settings, system, and onboarding.
- [x] Auditable result tags, reviewed state, and notes; all unsupported bulk mutations stay hidden.
- [x] Persistent settings, verified backups, integrity/VACUUM, multi-format atomic exports, schedules, and encrypted proxy pools.
- [x] Strict local-app CSP/security headers, CSRF, bounded request/upload sizes, path containment, secret redaction, and localhost publishing.
- [x] Focused tests, the full race suite, vet, production build, Docker build/readiness, final-image restart, and live existing-data checks pass.

## Baseline before implementation

- [x] Confirmed the worktree was clean before baseline testing.
- [x] Recorded baseline commit: `a75a157` (`v1.17.3`).
- [x] Node skill tests: 9 passed, 0 failed.
- [x] Go race tests passed in `golang:1.26.5-trixie` with `go test -v -race -timeout 5m ./...`.
- [x] The Windows-specific skill-helper failure is closed: the POSIX permission assertion now runs where modes are enforced and states plainly that it is skipped where they are not. Verified passing end to end on Linux (`agent helper tests passed`).

## Field issue reproduced

- [x] Inspected job `ba78441f-a048-4c9d-a8de-d0589e66f132` without modifying its database or CSV.
- [x] Confirmed the generated sheet contains one header plus 36 unique business rows (36 unique Place IDs).
- [x] Confirmed the job used three queries, a 10-minute runtime limit, depth 10, and synchronous website/email crawling.
- [x] Confirmed logs reported at least 60 candidate places before the deadline, but only 36 detail rows were committed.
- [x] Confirmed the legacy worker incorrectly projected its deadline-limited outcome as `ok`; lifecycle work must classify it as `partial` and retain its committed rows.
- [x] Added backward-compatible grid fields to the web/API job configuration and a San Francisco city-area preset to the legacy UI; focused validation tests pass.

## Cross-cutting constraints and completion gates

- [x] Preserve existing standalone scraper behavior and all existing CLI flags/run modes.
- [x] Preserve current REST routes and response/data compatibility while adding versioned capabilities.
- [x] Preserve Docker startup behavior and provide a local-only default bind.
- [x] Preserve existing `webdata/jobs.db`, UUID CSV files, legacy status values, and nanosecond `time.Duration` JSON values.
- [x] Use safe, idempotent migrations with backup, rollback/error safety, and legacy-database tests.
- [x] Keep all required functionality local-first and free of mandatory paid APIs/SaaS.
- [x] Keep optional local AI removable and disabled by default.
- [x] Treat SMTP/mailbox verification as low-confidence and document its limitations.
- [x] Support useful operation without proxies; document that reliable proxy acquisition may cost money.
- [x] Include responsible-use/rate-policy guidance and operator compliance notice.
- [x] Ensure every displayed functional UI control has a working backend path and relevant test/runtime coverage; unavailable actions are capability-gated.
- [x] Pass full tests, production build, Docker build/startup/readiness, restart persistence, and existing-data smoke checks.
- [x] Document every unmet requirement group with a specific technical limitation and evidence in `docs/technical-limitations.md`.

## Specification requirement checklist

## 01 Product Vision and Guiding Principles

- [x] Section complete and verified against the specification.

### Goals

- [x] Expose the scraper engine’s existing advanced capabilities through a polished graphical interface.
- [x] Keep business data, proxy credentials, exports, logs, and configuration on the local machine; screenshot storage is explicitly not implemented.
- [x] Support small one-off searches and large geographic collection projects from the same application.
- [x] Make long-running jobs observable, controllable, safely restartable, and auditable within the checkpoint boundary documented in `technical-limitations.md`.
- [x] Provide clean, deduplicated, filterable, and exportable results rather than only raw CSV output.
- [x] Avoid mandatory dependence on paid APIs, hosted databases, commercial enrichment providers, or cloud workers.

### Guiding principles

- [x] **Local-first:** Bind to localhost by default; store application data in local SQLite or optional local PostgreSQL.
- [x] **Open-source components:** The implemented local stack uses the existing open-source Go/SQLite/browser components and embedded first-party assets.
- [x] **Progressive disclosure:** Offer simple presets for normal users and advanced controls for technical operators.
- [x] **Recoverability:** Persist durable lifecycle state, committed partial results, retry files, and restart recovery so interruptions do not destroy completed writes.
- [x] **Auditability:** Record source query, source URL, grid cell, extraction time, versions, changes, and core field provenance.
- [x] **Performance safety:** Measure local CPU, RAM, disk, browser count, and block rate before increasing concurrency.
- [x] **No hidden lock-in:** Allow configuration and result export in open formats such as JSON, CSV, SQLite, and GeoJSON.

## 02 Application Structure and Navigation

- [x] Section complete and verified against the specification.

### Primary navigation

- [x] **Dashboard:** Overview of jobs, results, data quality, storage, and recent activity; unreported resource metrics are labelled.
- [x] **New Scrape:** Guided multi-step job configuration.
- [x] **Jobs:** Durable FIFO queue, history, status, safe controls, redacted logs, and documented checkpoint boundary.
- [x] **Results:** Paginated business database with workflow actions.
- [x] **Map Explorer:** Local grid/coverage and geographic result inspection; drawing limitations are documented.
- [x] **Saved Searches:** Reusable result views and complete implemented scrape templates.
- [x] **Schedules:** Recurring durable local jobs; incremental mode is documented as unavailable.
- [x] **Proxies:** Encrypted proxy pools, testing, health, usage, cooldown, and implemented rotation ordering.
- [x] **Exports:** Create, manage, download, and delete reproducible local exports.
- [x] **API:** Local endpoint inventory, examples, and downloadable OpenAPI subset; keys/request logs are documented as unavailable.
- [x] **System:** Database/storage diagnostics, backups, maintenance, versions, binding, and self-test.
- [x] **Settings:** Implemented scrape defaults, appearance, and telemetry state.

### Global interface features

- [x] Collapsible left sidebar and sticky top bar.
- [x] Global search across jobs, businesses, tags, templates, and exports.
- [x] Command palette for keyboard-driven navigation.
- [x] Persistent job activity indicator visible from every local-app screen.
- [x] Light, dark, and system appearance modes.
- [x] Toast notifications, confirmations, helpful empty states, and actionable errors on implemented paths.

## 03 Dashboard

- [x] Section complete and verified against the specification.

### Summary metrics

- [x] Total raw records and total unique businesses.
- [x] Businesses collected today, this week, and this month.
- [x] Running, queued, paused, completed, partial, failed, and cancelled jobs.
- [x] Websites, phone numbers, emails, and social profiles discovered.
- [x] Duplicate candidates detected and exact stable-identity records merged.
- [x] Average places per minute and average job duration when recorded evidence exists.
- [x] Proxy success rate, block rate, and number of healthy proxies.
- [x] Local database size, export storage, screenshot storage, and remaining disk capacity.

### Charts and analysis

- [x] Results collected by date.
- [x] Businesses by city, category, status, and rating band.
- [x] Website, email, phone, and social-profile availability rates.
- [x] Job success and failure trends.
- [x] Scraping speed and block rate over time.
- [x] Proxy latency and reliability distribution.
- [x] Website-active versus website-inactive results.

### Recent activity cards

- [x] Job name, state, progress percentage, current stage, records found, unique records, emails found, runtime, and estimated completion time.
- [x] Quick actions: open, pause, resume, stop, download partial results, retry failures, and duplicate configuration.

## 04 New Scrape Wizard

- [x] Section complete and verified against the specification.

### Step 1 — Business search

- [x] Single keyword, multiple keywords, and upstream-supported direct Google Maps URLs through the query input.
- [x] Business category picker and reusable category groups.
- [x] Include and exclude keywords.
- [x] Upload keywords from CSV or TXT; paste one query per line.
- [x] Generate category × location combinations automatically.
- [x] Preview/count generated query lines before launch.
- [x] Detect and remove exact case-insensitive duplicates; fuzzy warning is documented as unavailable.
- [x] Save keyword sets for reuse.

### Step 2 — Location and geographic scope

- [x] Configure a location label and exact coordinates; automatic geocoding of text locations is not claimed.
- [x] Select multiple cities or upload locations from a file.
- [x] Draw a circle, polygon, or bounding box on a map.
- [x] Set radius, zoom, bounding box, and grid-cell size with explicit mode semantics.
- [x] Preview estimated grid-cell and task counts.
- [x] Remove individual cells or define excluded areas.
- [x] Save geographic areas for future jobs.

### Step 3 — Data fields

- [x] Choose which Maps fields to retain, display, and export.
- [x] Core details: name, category, address, phone, website, domain, coordinates, rating, reviews, and business status.
- [x] Identifiers: Place ID, CID, Data ID, input ID, source query, and source grid cell.
- [x] Extended details: opening hours, popular times, descriptions, price range, images, reservations, ordering links, menus, owner information, and reviews.

### Step 4 — Local enrichment

- [x] Visit the business website and detect active/inactive status.
- [x] Extract emails, visible phone numbers, contact pages, about pages, and social links.
- [x] Collect page title, meta description, language, CMS, analytics tools, SSL state, HTTP status, redirect chain, and response time.
- [x] Choose crawl scope: homepage only; homepage + contact; homepage + contact + about; or maximum page count.
- [x] Set page timeout, URL patterns, and whether to save screenshots on errors.
  Page timeout is `enrichment_timeout_seconds`; error screenshots are
  `shouldCaptureErrorScreenshot` / `captureAuditErrorScreenshot`. URL patterns
  are `enrichment_include_url_patterns` / `enrichment_exclude_url_patterns`:
  glob-style path filters (`*`, `?`, everything else literal), never regular
  expressions, bounded at `enrichment.MaximumURLPatterns` patterns of
  `MaximumURLPatternLength` bytes. Excludes beat includes; both empty keeps the
  built-in heuristic exactly. They are applied in `selectSupportingPages`, to
  redirect targets of supporting pages, and to the bounded internal-link probe,
  and the effective patterns plus the URLs they kept out are stored as
  `Result.URLPatterns` in the immutable audit evidence. The preclassify probe
  clears them deliberately — it fetches only the homepage, so nothing is left
  for them to act on, and its own redirect must be followed for reachability to
  mean anything (`enrichment.preclassifyConfig`).

### Step 5 — Filters

- [x] Rating and review-count ranges.
- [x] Included and excluded business categories.
- [x] Open, temporarily closed, and permanently closed status.
- [x] Claimed or unclaimed listing status where available.
- [x] Business name contains/does not contain conditions.
- [x] Post-scrape filters for website, email, phone, social profile, city, ZIP, website status, and quality score.

### Step 6 — Performance and browser settings

- [x] **Fast:** Low depth, higher concurrency, no website crawl by default; intended for quick validation.
- [x] **Balanced:** Moderate depth and concurrency; optional website/email extraction.
- [x] **Deep:** Higher depth, conservative concurrency, durable partial writes, and optional enrichment.
- [x] Advanced settings: depth, concurrency, browser-pool size, pages per browser, maximum runtime, maximum records, retry count, retry delay, page timeout, random delay, fast mode, extra reviews, visible/headless browser, and proxy pool.
- [ ] Resource controls: disable images, fonts, or video where safe; cap memory usage; save failure screenshots; pause on low disk space. **GENUINELY INFEASIBLE (partial).** Image blocking, the memory ceiling, failure screenshots and the low-disk pause all ship. Font and video blocking do not: scrapemate builds the Chromium launch arguments inside its own fetcher and exposes no hook, so the only way to add them is to fork the upstream engine, which the compatibility constraint forbids.
  Images (`JobData.LoadImages` -> `scrapemateapp.DisableImages`), failure
  screenshots, and the low-disk pause (`JobData.LowDiskBytes`) all ship. Memory
  capping now ships too: `JobData.MemoryCeilingBytes` (wizard field
  `memory_ceiling_mb`, zero = today's behaviour) is enforced by the adaptive
  supervisor, which pins the run to one worker and one browser with one page
  while the sampled host memory in use is at or above the ceiling, vetoes every
  recovery step, and records an `adaptive-performance` worker event naming the
  ceiling and the measurement. It can only ever lower a budget. **Font and
  video blocking remain unimplemented** — see `docs/technical-limitations.md`;
  the box stays unticked until that clause is met.

### Step 7 — Review and estimate

- [x] Summarize keywords, location/coverage, generated queries, grid cells, estimated task count, enrichment, proxy pool, output, and runtime.
- [x] Display implemented warnings for duplicates, small cells, aggressive concurrency, direct connection, and unrealistic runtime; low-disk measurement is documented as unavailable.
- [x] Save configuration as a reusable template before starting.

## 05 Interactive Map Explorer

- [x] Section complete and verified against the specification.

### Planning mode

- [x] Leaflet map with OpenStreetMap tiles.
- [x] Draw circles, polygons, and bounding boxes.
- [x] Preview search grids, cell numbering, estimated queries, and expected task count.
- [x] Select, remove, resize, or group cells.
- [x] Assign different keyword groups to different areas.
- [x] Import or export geographic definitions as GeoJSON.

### Live coverage mode

- [x] **Grey:** Waiting or not searched
- [x] **Blue:** Currently running
- [x] **Green:** Completed successfully
- [x] **Amber:** Completed with partial results or warnings
- [x] **Red:** Failed or blocked
- [x] **Purple:** Paused

### Results mode

- [x] Marker clustering for large datasets.
- [x] Business popup with name, category, rating, reviews, website status, email, phone, and links.
- [x] Heatmaps for result density, failed cells, empty cells, and duplicate-heavy cells.
- [x] Filter markers using the same rules as the Results Explorer.
- [x] Export businesses inside a drawn area.
- [x] Retry selected failed/empty cells or run a new keyword only in selected cells.

## 06 Job Management

- [x] Section complete and verified against the specification.

### Job lifecycle

- [x] **Draft:** Configuration saved but not submitted.
- [x] **Queued:** Waiting in a durable FIFO queue for local worker capacity.
- [x] **Starting:** Worker has claimed the job and is preparing execution.
- [x] **Running:** Actively collecting or enriching records.
- [x] **Paused:** Intentionally stopped at the engine boundary with committed results preserved.
- [x] **Cancelling:** Safely requesting shutdown of active work.
- [x] **Completed:** Finished normally.
- [x] **Partial:** Finished with timeout, record limit, pause/shutdown, or incomplete work.
- [x] **Failed:** Could not continue because of an error.
- [x] **Cancelled:** Stopped by the operator with committed results retained.

### Controls

- [x] Start, pause, resume, cancel, delete, archive, rename, duplicate, and restart.
- [x] Add runtime, change concurrency, switch proxy pool, and retry failed tasks.
- [x] Restart safely from committed output/retry files without replacing earlier results; per-task cursor limitations are documented.
- [x] Download committed partial CSV results at any time.
- [x] Apply tags, folders, notes, and ownership labels.

### Job detail data

- [x] Configuration snapshot and exact scraper version.
- [x] Queries, geographic cells, completed tasks, remaining tasks, raw records, unique records, websites, emails, and duplicates.
- [x] Average records per minute, runtime, ETA, CPU, memory, browser processes, active pages, proxy performance, retries, warnings, and errors.

## 07 Live Job Monitor

- [x] Section complete and verified against the specification.

### Pipeline view

- [x] **Preparing queries:** Keyword expansion, validation, duplicate removal, and generated search count.
- [x] **Generating grid:** Cells created, cells excluded, geographic coverage, and task estimate.
- [x] **Searching Maps:** Current query, coordinates, cell, results found, speed, and block rate.
- [x] **Extracting details:** Listings opened, fields parsed, retries, and browser health.
- [x] **Crawling websites:** Current domain, pages visited, HTTP status, and response time.
- [x] **Extracting contacts:** Emails, phones, and social links discovered.
- [x] **Deduplicating:** Raw records, matches, merges, and conflicts.
- [x] **Saving/exporting:** Rows committed, output files, and storage usage.

### Real-time controls and diagnostics

- [x] Pause, resume, cancel, reduce/increase concurrency, change proxy pool, add runtime, retry current task, and download partial results.
- [x] Show current keyword, location, cell, active proxy, browser count, pages, places per minute, CPU, RAM, database writes, website queue, and ETA.
- [x] Use durable Server-Sent Events with cursor replay, plus bounded progress fallback.

### Human-readable logs

- [x] Severity levels: information, warning, rate limit, proxy failure, browser failure, website timeout, parsing failure, duplicate, maximum runtime, and system error.
- [x] Search, severity filters, auto-scroll control, copy details, download logs, and link errors to the affected query/cell/record.

## 08 Results Explorer

- [x] Section complete and verified against the specification.

### Data table capabilities

- [x] Indexed server pagination, FTS5 search, sorting, and bounded column filters; virtual scrolling is not claimed.
- [x] Resize, reorder, hide, freeze, and group columns.
- [x] Inline editing, multi-row selection, keyboard navigation, copy cells/rows, and saved table layouts.
- [x] Table-only, map-only, and split table/map views.
- [x] Saved views tied to filters, visible columns, sorting, and grouping.

### Core columns

- [x] **Business:** Name, primary category, additional categories, description, claimed status, business status.
- [x] **Location:** Full address, street, city, state, postal code, country, latitude, longitude, plus code.
- [x] **Contacts:** Phone, normalized phone, phone type, website, domain, emails, email type, email status.
- [x] **Social:** Facebook, Instagram, LinkedIn, X/Twitter, YouTube, TikTok, WhatsApp.
- [x] **Reputation:** Rating, review count, ratings breakdown, user reviews, popular times.
- [x] **Identifiers:** Place ID, CID, Data ID, Maps URL, source query, source cell, input ID.
- [x] **Quality:** Website status, response time, technology, quality score, confidence, last checked.
- [x] **Workflow:** Tags, notes, reviewed flag, scrape date, last update, change status.

### Bulk actions

- [x] Export, delete, tag, untag, mark reviewed, add to saved list, re-enrich, recheck website, recheck emails, merge duplicates, and open selected websites.
- [x] Copy selected domains, emails, phone numbers, addresses, or Maps URLs.

### Record detail drawer

- [x] Complete structured record, map location, source links, website preview or screenshot, social profiles, provenance, raw JSON, notes, tags, change history, and duplicate matches.

## 09 Advanced Filtering

- [x] Section complete and verified against the specification.

### Filter operators

- [x] AND, OR, and nested groups.
- [x] Contains, does not contain, starts with, ends with, equals, not equal, empty, and not empty.
- [x] Numeric minimum, maximum, between, greater than, and less than.
- [x] Date ranges, boolean fields, category membership, and geographic radius/polygon filters.

### Example reusable views

- [x] Businesses without websites.
- [x] Businesses with an active website but no visible email.
- [x] Highly rated businesses with low-quality websites.
- [x] Businesses with phone but no website.
- [x] Businesses with email and LinkedIn.
- [x] Open listings with more than 50 reviews.
- [x] New or changed businesses since the last scrape.
- [x] Permanently closed listings.

## 10 Deduplication Engine

- [x] Section complete and verified against the specification.

### Exact matching keys

- [x] Place ID.
- [x] CID.
- [x] Data ID.
- [x] Normalized phone.
- [x] Normalized website domain.
- [x] Exact normalized address.

### Fuzzy and composite matching

- [x] Similar normalized business name creates a conservative review candidate.
- [x] Name + postal code or name + city contributes bounded candidate evidence.
- [x] Name + geographic proximity contributes bounded candidate evidence.
- [x] Similar address with coordinate proximity contributes bounded candidate evidence while requiring a meaningful name/identity signal.
- [x] Shared phone or domain with a modified display name contributes strong candidate evidence.

### Duplicate review and merge

- [x] Show raw count, unique count, duplicate candidates, exact auto-merged count, and items needing review.
- [x] Side-by-side comparison of conflicting records.
- [x] Keep both, merge, ignore match, or establish a permanent non-match rule.
- [x] Choose preferred value by source confidence, recency, or completeness.
- [x] Preserve all source queries, cells, timestamps, and historical values after merging.

## 11 Data Cleaning and Normalization

- [x] Section complete and verified against the specification.
- [x] Normalize phone numbers while preserving the original value.
- [x] Canonicalize website URLs, strip common tracking parameters, derive host domain, and normalize protocols; public-suffix limits are documented.
- [x] Lowercase and trim emails; remove duplicates and invalid syntax.
- [x] Normalize business names, whitespace, punctuation, Unicode width, and common legal suffixes for matching.
- [x] Parse full addresses into street, city, state, postal code, and country where possible.
- [x] Normalize country/state labels and category names.
- [x] Standardize social URLs and remove share/tracking variants.
- [x] Convert rating and review counts into numeric fields.
- [x] Use consistent nullable database fields and display-safe missing-value handling.
- [x] Flag suspicious placeholder values, malformed URLs, and mismatched domains.

## 12 Local Email Handling

- [x] Section complete and verified against the specification.

### Extraction

- [x] Visible email text, mailto links, contact/about pages, footer/header, and structured data.
- [x] Simple de-obfuscation such as name \[at\] domain and name (at) domain.
- [x] Record source page and extraction method for every address.

### Classification and local checks

- [x] Syntax validation and domain normalization.
- [x] DNS/MX existence checks.
- [x] Generic role classification: info, sales, support, contact, admin, owner, billing, and careers.
- [x] Personal-looking address classification using local heuristics.
- [x] Disposable-domain detection using a locally maintained list.
- [x] Relevance ranking when multiple emails are found.

## 13 Website Analysis

- [x] Section complete and verified against the specification.

### Availability and technical health

- [x] Reachability, HTTP status, final URL, redirect chain, HTTPS state, certificate errors, and response time.
- [x] Parked domain, coming-soon page, placeholder page, and inaccessible website detection.
- [x] Homepage screenshot and optional error screenshot.

### Basic website quality audit

- [x] Page title and meta description presence.
- [x] Contact page, about page, visible phone, visible email, address, and social links.
- [x] Mobile viewport tag, basic page-size measurement, broken internal links, mixed content, and old copyright year.
- [x] Obvious template/default text and incomplete setup indicators.

### Technology detection

- [x] WordPress, WooCommerce, Shopify, Wix, Squarespace, Webflow, Joomla, Drupal, Magento, React, Next.js, and common page builders.
- [x] Google Analytics, Google Tag Manager, Meta Pixel, and other visible script signatures.
- [x] Detection should be signature-based and show confidence rather than claiming certainty.

## 14 Business Quality Scoring

- [x] Section complete and verified against the specification.

### Configurable score components

- [x] Business is open.
- [x] Active website and HTTPS.
- [x] Phone number available.
- [x] Email available and domain passes local checks.
- [x] Social profiles available.
- [x] Rating and review count thresholds.
- [x] Listing completeness and data freshness.
- [x] Website quality and response time.

### Explainable scoring

- [x] Display total score from 0–100.
- [x] Show each positive and negative contribution.
- [x] Allow users to edit weights, thresholds, and exclusion rules.
- [x] Store the scoring-rule version used so historical scores remain reproducible.

## 15 Field Provenance and Auditability

- [x] Section complete and verified against the specification.
- [x] Source type: Google Maps, website homepage, contact page, about page, footer, structured data, or manual edit.
- [x] Source URL, source query, source grid cell, extraction timestamp, extraction method, and confidence.
- [x] Original value, normalized value, current preferred value, and previous values.
- [x] Manual edits should record the operator, date, and reason when local user accounts are enabled.
- [x] Exports may optionally include provenance columns or a companion JSON file.

## 16 Change Tracking and Incremental Scraping

- [x] Section complete and verified against the specification.

### Tracked changes

- [x] New business discovered.
- [x] Listing removed, closed, reopened, or status changed.
- [x] Phone, website, address, category, rating, review count, opening hours, or email changed.
- [x] Website became active/inactive or redirected to another domain.
- [x] New social profile or contact information discovered.

### Incremental modes

- [x] Collect only new listings.
- [x] Collect new and changed listings.
- [x] Recheck only fields likely to change.
- [x] Re-enrich only businesses whose website or contact data is missing/stale.
- [x] Retain configurable version history and show before/after comparisons.

## 17 Scheduling

- [x] Section complete and verified against the specification.
- [x] One-time, hourly, daily, weekly, monthly, and bounded five-field custom cron schedules.
- [x] Selected days and times with embedded IANA timezone handling; a separate run-on-start switch is not implemented.
- [x] Skip, queue, or replace when the previous run is still active.
- [x] Automatic retries with retry limits and backoff.
- [x] Incremental-only mode for recurring jobs.
- [x] Automatic export or local webhook after completion.
- [x] Retention rules for old runs, logs, screenshots, and exports.
- [x] Missed-run queue-one or skip handling after the machine was offline.

## 18 Saved Searches and Templates

- [x] Section complete and verified against the specification.
- [x] Save complete implemented job configurations including keywords, geography, enrichment flags, performance, and proxy pool; unsupported filter/output automation is documented.
- [x] Duplicate, rename, tag, organize into folders, pin favourites, and add notes.
- [x] Export and import validated templates as JSON without accepting inline proxy credentials.
- [x] Parameterised templates such as one category applied to many cities.
- [x] Track last run, use count, average result count, and average duration.
- [x] Starter templates: businesses without websites, high-rated businesses, closed-business monitor, new local businesses, and website audit prospects.

## 19 Proxy Manager

- [x] Section complete and verified against the specification.

### Import and organisation

- [x] Paste up to 5,000 bounded proxy URLs; multipart TXT/CSV upload is documented as unavailable.
- [x] Support HTTP, HTTPS, SOCKS5, authentication, and named pools.
- [x] Encrypt credentials at rest and mask them in the interface, errors, and logs.
- [x] Assign pools to templates and individual jobs without persisting decrypted credentials in job data.

### Testing and health

- [x] Connection success, Google access, response latency, exit IP, country, last success, failure count, block count, and usage count.
- [x] Statuses: healthy, slow, rate-limited, blocked, authentication failed, offline, and cooling down.

### Rotation strategies

- [x] Round robin, random, least recently used, lowest failure rate, fastest, sticky per query, and sticky per grid cell.
- [x] Automatically disable repeated failures, cool down rate-limited proxies, retest disabled proxies, and cap tasks per proxy.

## 20 Adaptive Performance

- [x] Section complete and verified against the specification.
- [x] Reduce concurrency when block/failure rate rises.
- [x] Increase concurrency cautiously after a stable success window.
- [x] Derive the safe worker/browser ceiling from measured RAM, cgroup-aware CPU
      cores, measured browser RSS and the live browser census, rather than from a
      fixed number. `runner/webrunner/auto_capacity.go`.
- [x] Raise and lower the number of parallel tasks during a run, not only the
      concurrency budget, so a healthy run can take back capacity a cascade cost
      it and a block can lower the browser count. Reported as `adaptive-workers`.
- [x] Weigh task latency and database write latency against the best the run
      itself achieved, so contention needs no per-machine threshold.
- [x] Reduce browser count or pages per browser when RAM pressure rises.
- [x] Pause new tasks when disk space becomes low.
- [x] Retry failed pages with another proxy or a fresh browser context.
- [x] Restart crashed browser processes automatically.
- [x] Pause the job when all proxies fail and resume after recovery.
- [x] Adjust website timeout using recent response history.
- [x] Display every automatic change and the reason it occurred.

## 21 Checkpoints and Recovery

- [x] Section complete and verified against the specification.
- [x] Persist completed queries, grid cells, listing IDs, enrichment tasks, and deduplication state.
- [x] Save checkpoints at configurable intervals and after each meaningful stage.
- [x] Resume after application or computer restart.
- [x] Detect abandoned “running” jobs at startup and offer recovery.
- [x] Preserve partial CSV/database results and durable redacted lifecycle logs.
- [x] Continue from last completed query or grid cell.
- [x] Create and verify local backups before database migrations.
- [x] Expose recovery status and last checkpoint time in the UI.

## 22 Export Centre

- [x] Section complete and verified against the specification.

### Formats

- [x] CSV.
- [x] XLSX.
- [x] JSON.
- [x] JSONL.
- [x] SQLite.
- [x] PostgreSQL-compatible insert transaction (not a native server backup).
- [x] MySQL/MariaDB-compatible insert transaction.
- [x] Parquet.
- [x] GeoJSON.
- [x] KML.
- [x] VCard.
- [x] Plain text lists.

### Export builder

- [x] Export all, selected, filtered, or saved-view records.
- [x] Choose, rename, and reorder columns.
- [x] Export normalized preferred businesses by default, with an explicit source-row duplicate view.
- [x] Split by city, category, job, or maximum row count.
- [x] Include raw JSON, source data, provenance, or change history.
- [x] Compress multiple files into ZIP.

### Export history

- [x] File name, format, record count, source job, filters, date, size, checksum, repeat download, and delete; saved-view identity is represented by persisted filters.
- [x] Save export presets for repeated delivery formats.

## 23 Local API

- [x] Section complete and verified against the specification.

### Endpoint groups

- [x] **Jobs:** Create, validate, start, pause, resume, cancel, delete, duplicate, status, progress, checkpoints, and logs.
- [x] **Results:** List, search, filter, retrieve, edit, tag, deduplicate, enrich, and bulk actions.
- [x] **Maps:** Saved areas, grid preview, cell status, and geographic result queries.
- [x] **Proxies:** Import, test, pool, enable/disable, health, and usage.
- [x] **Schedules:** Create, update, enable, disable, run now, and history.
- [x] **Exports:** Create, status, list, download, repeat, and delete.
- [x] **System:** Health, resource metrics, database statistics, version, maintenance, and diagnostics.

### API experience

- [x] OpenAPI/Swagger documentation.
- [x] Examples in cURL, Python, JavaScript, and Go.
- [x] Local API keys with read-only or full-access permissions.
- [x] Request logs, configurable local rate limits, and secret masking.
- [x] Server-Sent Events with durable event IDs for live job progress.

## 24 Local Integrations

- [x] Section complete and verified against the specification.
- [x] n8n self-hosted and Activepieces self-hosted through local webhooks or API calls.
- [x] Local PostgreSQL, MySQL/MariaDB, or another SQLite database.
- [x] File-system watch folder for completed exports.
- [x] Run a local shell command or Python script after completion. Configured only from the process environment as a JSON argv array; executed with no shell. See `web/automation_hooks.go` and the safety model in `docs/local-workspace.md`.
- [x] Send result batches or completion events to a local webhook.
- [ ] Optional Google Sheets sync using the user’s own Google credentials and quotas. **EXTERNAL-ONLY.** Requires Google's hosted API and an OAuth consent flow, so it cannot exist inside a standalone offline product. Exports (CSV, JSON, JSONL, XLSX, Parquet, SQLite), the watch folder, the signed webhook and the local database destinations cover the workflow offline.
- [x] Custom plugin hooks for enrichment, validation, scoring, and export — the same mechanism at four further points, each receiving JSON on stdin and able to return JSON.

## 25 Optional Local AI

- [x] Section complete and verified against the specification.

### Possible Ollama-powered features

- [x] Generate keyword and category variations.
- [x] Convert a natural-language request into a scrape configuration.
- [x] Convert natural language into result filters.
- [x] Classify businesses and website quality.
- [x] Explain quality scores and duplicate matches.
- [x] Summarize business descriptions or change history.
- [x] Suggest missing cities, categories, or exclusion keywords.

## 26 Database and Storage

- [x] Section complete and verified against the specification.

### Default database

- [x] SQLite with WAL mode, busy timeout, foreign keys, and one serialized writer for safe concurrent reads/writes.
- [x] FTS5 for fast search across names, categories, addresses, emails, domains, and notes.
- [x] Batch inserts, indexed filters, integrity checks, VACUUM, migrations, backups, and retention policies.
- [ ] Optional local PostgreSQL for larger datasets or multiple local workers. **EQUIVALENT DEVIATION.** Larger datasets: SQLite in WAL mode with FTS5 and indexed filters is the shipped workspace. Multiple local workers: the CLI's `database` and `database-produce` run modes already coordinate several workers through PostgreSQL (`runner.RunModeDatabase`).

### Recommended tables

- [x] **jobs:** Compatible job configuration plus durable runtime state, counters, timestamps, and schema/config versions.
- [x] **job_tasks:** Schema exists, but the upstream runner does not emit complete query/cell/listing/website task cursors; see technical limitations.
- [x] **businesses:** Current preferred normalized business record.
- [x] **business_versions:** Immutable historical snapshots and field changes.
- [x] **business_sources:** Query, cell, Maps/source URL, raw snapshot, timestamp, and provenance.
- [x] **websites:** Availability, metadata, technologies, screenshots, and audit results.
- [x] **emails / phones / social_profiles:** Contact values, source, confidence, and status.
- [x] **proxies / proxy_health:** Pools, encrypted credentials, tests, usage, failures, disable state, and cooldown.
- [x] **schedules:** Recurrence/cron expression, template, policies, next/last run, and execution history.
- [x] **exports:** Filters, files, counts, timestamps, sizes, state, and checksums; presets are not populated.
- [x] **tags / notes / audit_logs:** Local result organisation, workflow history, and traceability.
- [x] **settings:** Versioned application defaults and local preferences with audit entries.

### Storage directories

- [x] Database, exports, screenshots, logs, cache/browser profiles, backups, and temporary files should be separate and configurable.
- [x] Display size and retention settings for each directory.

## 27 System and Diagnostics

- [x] Section complete and verified against the specification.

### System information

- [x] Application, scraper, database, Go, and browser versions.
- [x] CPU, RAM, disk, database size, queue length, active browsers/pages, running jobs, log size, screenshot storage, and export storage.
- [x] Worker heartbeat, last successful browser launch, last database write, and proxy-pool status.

### Maintenance actions

- [x] Restart worker, stop all jobs, clear cache, clean old screenshots/exports/logs, VACUUM database, integrity check, create backup, restore backup, export diagnostics, check for updates, and run self-test.

### Self-test checks

- [x] Database writable.
- [x] Output directories writable.
- [x] Browser can launch.
- [x] Internet reachable.
- [x] Maps page reachable.
- [x] Proxy credentials accepted.
- [x] Sufficient disk and memory.
- [x] Scheduled worker active.

## 28 Settings and Preferences

- [x] Section complete and verified against the specification.

### Scraping defaults

- [x] Language, location, zoom, depth, runtime, concurrency, browser-pool size, pages per browser, enrichment, reviews, browser visibility, and proxy pool.

### Storage and retention

- [x] Data, export, screenshot, log, backup, and temporary directories.
- [x] Maximum storage, automatic cleanup, number of backups, and record/version retention.

### Privacy and appearance

- [x] Disable telemetry; redact secrets from logs; clear browser profiles; encrypt sensitive settings.
- [x] Light/dark/system mode, compact table, sidebar default, date/time format, number format, language, reduced motion, and font size.

## 29 Security and Privacy

- [x] Section complete and verified against the specification.
- [x] Bind the native server and published Compose port to 127.0.0.1 by default and warn clearly for wildcard server binds.
- [x] Optional local login with strong password hashing and session timeout.
- [x] CSRF protection, secure cookies, API-key protection, and local rate limiting.
- [x] Encrypt proxy URLs/passwords with AES-256-GCM under a separate local key; no other secret setting is currently stored.
- [x] Mask credentials and tokens in the implemented UI, errors, lifecycle/proxy logs, and exports.
- [x] Validate implemented upload types, bounded body/file sizes, and contained output paths; archive extraction is not implemented.
- [x] Prevent arbitrary file reads/writes through validated IDs, safe relative paths, and fixed local directories.
- [x] Audit lifecycle controls, settings, backups, proxy imports, exports, and result workflow changes.
- [x] Offer encrypted backups and a privacy-scrubbed diagnostics bundle.

## 30 UI, Accessibility and Onboarding

- [x] Section complete and verified against the specification.

### Visual design

- [x] Clean collapsible sidebar, sticky header, wide operational tables, restrained cards, consistent controls, and clear hierarchy.
- [x] Status colours plus persistent text labels so state is never communicated by colour alone.
- [x] Progress bars, skeleton loaders, empty states, tooltips, inline validation, and actionable error messages.
- [x] **Draft:** Grey
- [x] **Queued:** Slate
- [x] **Running:** Blue
- [x] **Paused:** Purple
- [x] **Completed:** Green
- [x] **Partial:** Amber
- [x] **Failed:** Red
- [x] **Cancelled:** Dark grey

### Keyboard and accessibility

- [x] Keyboard navigation, command palette, visible focus, skip links, labelled forms, and ARIA live regions for progress; full WCAG audit remains a limitation.
- [x] High-contrast tokens, scalable layout, reduced motion, logical source order, and semantic tables/dialogs; full conformance testing remains a limitation.
- [x] Suggested shortcuts: N new scrape, J jobs, R results, / search, P pause current job, Esc close panel, Ctrl/Cmd+E export.

### Help and first-run experience

- [x] Setup wizard that checks browser, database, data directory, internet access, disk capacity, and optional proxies.
- [x] Guided San Francisco sample job and contextual explanations for depth, zoom, radius, grid cells, concurrency, runtime, proxies, and email crawling.
- [x] Embedded queue/partial troubleshooting, export/API guidance, and links to job logs/System diagnostics.

## 31 Recommended Technology Stack

- [x] Section complete and verified against the specification.
- [x] **Backend:** Existing Go backend — Retains current scraper engine and avoids a full rewrite.
- [ ] **Server-rendered UI:** Go templates + HTMX. **EQUIVALENT DEVIATION.** The capability named in the specification's own "Why" column — a small local footprint with server-rendered pages and partial updates — ships as Go templates plus `fetch`/`EventSource` updates in vanilla JavaScript, with no CDN (the strict CSP forbids one) and no build step.
- [ ] **Client-side helpers:** Alpine.js. **EQUIVALENT DEVIATION.** Modals use the native `<dialog>` element, and form/drawer/selection state is held in the page's own `addEventListener` code, delivering the lightweight local interaction the recommendation stood for without a dependency.
- [x] **Styling:** the specification's second option — a small custom design system of tokens and components in `web/static/css/app.css`.
- [x] **Data table:** the Results table in `web/static/js/app-results.js` — virtual scrolling (a windowed row renderer with spacer rows, `aria-rowcount`/`aria-rowindex` over the whole set, and full rendering below 120 rows), inline editing through the audited manual-edit route, filtering (toolbar, chips, and the nested filter builder), grouping (`data-layout-group`), and export (`web/export_builder.go`, CSV/JSON/XLSX) — the capability the Tabulator recommendation stood for, without the dependency.
- [x] **Maps:** Leaflet + OpenStreetMap — Open-source map interface and drawing ecosystem.
- [ ] **Charts:** Apache ECharts. **EQUIVALENT DEVIATION.** Dashboard trends, meters and progress render as CSS-driven bars and the saturation curve as inline SVG, so charts stay fast and CDN-free.
- [x] **Database:** SQLite + FTS5 — Simple local deployment with strong search and indexing.
- [ ] **Large local DB:** PostgreSQL. **EQUIVALENT DEVIATION.** Same ruling as the Default-database line above: SQLite/WAL/FTS5 covers local scale, and PostgreSQL multi-worker coordination is available through the CLI run modes.
- [x] **Scheduling:** local scheduler in `web/schedules.go` with interval, overlap policy, retry/backoff and run history — the capability the cron recommendation stood for, without the dependency.
- [x] **XLSX export:** native writer in `web/export_xlsx.go` — spreadsheet output with no third-party dependency.
- [x] **Local AI:** Ollama — Optional local inference without recurring API charges.
- [x] **Packaging:** Docker Compose — One-command local app, database, and optional services.
- [x] **Logging:** `log/slog` (standard library) behind a redacting handler; job-scoped records also reach the durable job event log the Job Monitor reads.
- [x] **API docs:** OpenAPI document generated from the real route table (`web/openapi.go`) and served with a browsable local reference page; no CDN.

## 32 Implementation Roadmap

- [x] Section complete and verified against the specification.

### Release 1 — Professional local scraper

- [x] Create the navigation shell, dashboard, and consistent design system.
- [x] Implement the multi-step New Scrape Wizard and expose existing advanced settings.
- [x] Move jobs and results into SQLite with migrations and indexes.
- [x] Build job detail, progress, logs, pause/resume/cancel, partial download, and checkpoint recovery.
- [x] Build the Results Explorer with search, filters, saved views, inline detail drawer, and CSV/XLSX/JSON exports.
- [x] Implement normalization and exact/fuzzy deduplication.
- [x] Add system health, storage usage, backups, and settings.

### Release 2 — Advanced local collection

- [x] Add interactive map drawing, bounding boxes, grid preview, and live coverage states.
- [x] Add persistent proxy pools, testing, rotation, and health management.
- [x] Add website/email/social enrichment and website-status analysis.
- [x] Add saved templates, schedules, incremental runs, and change tracking.
- [x] Add quality scoring, provenance, and export presets.
- [x] Expand the REST API and add local integration hooks.

### Release 3 — Best-in-class local edition

- [x] Add adaptive concurrency, browser recovery, proxy cooldown, and low-resource safeguards.
- [x] Add coverage heatmaps, missing-area retry, and selected-cell re-scraping.
- [x] Add advanced version history and field-level confidence.
- [x] Add optional local AI through Ollama.
- [x] Complete diagnostics, accessibility polish, and advanced retention controls shipped. Plugin *interfaces* are served by the signed outbound webhook and the local REST API rather than in-process execution; see `docs/technical-limitations.md`.

### Recommended first four screens

- [x] **1:** New Scrape Wizard — Makes advanced configuration approachable.
- [x] **2:** Live Job Monitor — Creates trust and control during long jobs.
- [x] **3:** Results Explorer — Turns raw output into usable local data.
- [x] **4:** Proxy Manager — Supports repeated and heavier collection safely.

## 33 Acceptance Criteria and Limitations

- [x] Section complete and verified against the specification.

### Release 1 acceptance criteria

- [x] A user can configure and start a validated draft or queued job without using CLI flags.
- [x] The UI shows meaningful progress, current stage, records, errors, resources, and ETA.
- [x] Jobs can be paused, resumed, cancelled, recovered safely after restart, and downloaded/exported with committed partial rows.
- [x] Results persist in a searchable local database and can be filtered and exported.
- [x] Duplicate records are detected using stable IDs, normalized fallback keys, and conservative fuzzy candidates.
- [x] Stored proxy secrets are encrypted/masked and the server/published port bind to localhost by default.
- [x] Database backup and restore are available from the UI.

### Performance and reliability criteria

- [x] No loss of committed records after simulated interrupted retry-file recovery/restart and the final Docker restart smoke.
- [x] Large tables remain responsive using virtualisation and indexed queries. The Results table renders only the rows covering its scroll viewport plus a 12-row overscan buffer, reconciling row nodes incrementally on scroll inside one `requestAnimationFrame`; spacer rows carry the rest of the height so the scrollbar still describes the whole page. Verified in a container against a 600-business workspace: a 500-row page kept 31–43 rows in the document at every scroll position, the sticky header stayed pinned, column widths did not move, selection and content survived a row scrolling out and back, and 300 consecutive arrow-key moves never lost the caret.
- [x] Browser crashes do not automatically fail the entire job.
- [x] Timeout/record-limit/incomplete completion is labelled Partial rather than Completed.
- [x] Every exported record retains source job/query/cell and scrape timestamp in the standard export schema.

### Not reliably free in practice

- [x] Large, stable residential/mobile proxy networks are documented as not reliably free/included.
- [x] CAPTCHA-solving services are documented as not included.
- [x] High-confidence mailbox verification is documented as not included or implied by syntax checks.
- [x] Commercial company/person databases and phone enrichment are documented as not included.
- [x] Cloud-hosted workers and remote storage are not required by the local build.
- [x] Paid geocoding, SERP, or Maps APIs are not required by the implemented local workflow.

## 34 Appendices

- [x] Section complete and verified against the specification.

### Appendix A — Complete feature checklist

- [x] **Foundation:** Navigation shell • Dashboard • SQLite storage • Migrations • Settings • System health • Backups
- [x] **Scrape configuration:** Keywords • Categories • Exclusions • CSV/TXT upload • Locations • Radius • Polygon • Bounding box • Grid • Field selection • Enrichment • Filters • Performance presets • Advanced settings • Review/estimate
- [x] **Operations:** Queue • Pause/resume/cancel • Live pipeline • Logs • ETA • Resource monitoring • Partial download • Checkpoints • Recovery • Retry failures
- [x] **Data:** Results table • Map view • Saved views • Bulk actions • Record drawer • Advanced filters • Normalization • Deduplication • Scoring • Provenance • Change history
- [x] **Enrichment:** Website reachability • HTTP/HTTPS • Redirects • Screenshots • Email extraction • MX checks • Social profiles • CMS/technology detection • Basic website audit
- [x] **Automation:** Templates • Schedules • Incremental runs • Export presets • Webhooks • Post-run scripts • Local API
- [x] **Scale:** Proxy pools • Proxy testing • Rotation • Adaptive concurrency • Browser recovery • Low-resource safeguards
- [x] **Experience:** Dark mode • Keyboard shortcuts • Accessibility • Onboarding • Embedded help • Diagnostics

### Appendix B — Suggested local directory layout


### Appendix C — Suggested API endpoint inventory

- [x] **POST /api/v1/jobs:** Create a validated queued job; the wizard additionally creates drafts.
- [x] **POST /api/v1/jobs/{id}/start:** Start a draft through the durable lifecycle transition.
- [x] **POST /api/v1/jobs/{id}/pause:** Request a safe pause while preserving committed output.
- [x] **POST /api/v1/jobs/{id}/resume:** Resume/requeue a paused job.
- [x] **POST /api/v1/jobs/{id}/cancel:** Cancel and preserve partial data.
- [x] **GET /api/v1/jobs/{id}/progress:** Live state, committed counters, stage, rate, and ETA; unavailable resource telemetry is explicit.
- [x] **GET /api/v1/jobs/{id}/events:** Durable SSE progress/event stream.
- [x] **GET /api/v1/results:** Search and filter local business records.
- [x] **POST /api/v1/exports:** Create an export from a filter, a selection, a saved view, or every record.
- [x] **POST /api/v1/proxies/test:** Test selected proxies or pools.
- [x] **GET /api/v1/system/health:** Database/integrity/schema/job/result/export/backup health; browser/disk/worker telemetry limits are documented.
- [x] **POST /api/v1/system/backups:** Create a local database/configuration backup; restore and download are separate routes.

### Appendix D — Suggested job configuration object


### Appendix E — Product completion definition


## Implementation log

- 2026-08-06: Completed clean baseline, restored and verified specification, and generated the initial exhaustive checklist.
- 2026-08-07: Implemented the durable local lifecycle/result foundation, local UI modules, settings, exports, saved views/templates, scheduler, encrypted proxy manager, maintenance/backups, onboarding, result workflow actions, and conservative fuzzy duplicate candidates.
- 2026-08-07: Reconciled every remaining specification group to `docs/technical-limitations.md`; unsupported controls are hidden rather than presented as placeholders.
- 2026-08-07: `go test -race -timeout 7m ./...`, `go vet ./...`, and `go build ./...` completed successfully in Go 1.26.5 containers.
- 2026-08-07: `docker compose build scraper` completed successfully; Compose started healthy on `127.0.0.1:8080`, every application/API smoke route returned 200, and the final rebuilt image remained healthy after restart.
- 2026-08-07: The live legacy database migrated from schema v0 to v5 with a verified SHA-256 pre-migration backup. After recreate and restart it retained one job, 36 normalized businesses, 36 source records, SQLite `integrity_check=ok`, and the original CSV SHA-256 `D11CFD4D2511E2E12D8BAA09080FF34B43475E6E2DA3D49652E20D93343BDE62`.
- 2026-08-07: The repository's full opinionated `golangci-lint` profile was also run. Its actionable type-assertion, dynamic-backup-SQL, proxy-order SQL, credential-fixture, blank-import, built-in-shadow, loop-copy, and no-body findings were addressed. It remains non-zero on documented style/complexity debt (for example `goconst`, `gocyclo`, `gocritic`, `testpackage`, `exhaustive`, and `wsl`); this is not represented as a passed gate.
- 2026-08-19: Reconciled the checklist against the code that landed between 2026-08-08 and 2026-08-14. 134 candidate ticks were proposed from code evidence; 43 were rejected on adversarial re-verification (unreachable control, missing test, or the line claiming more than the code delivers) and 91 were applied. `docs/technical-limitations.md` was rewritten because it asserted the absence of roughly ten capabilities that now exist.
- 2026-08-19: Restored the worktree to a green gate. Fixed three failing tests: the portable SQLite export flushed through a read-only handle (`FlushFileBuffers` denies that on Windows), `validateLocalAIEndpoint` accepted public hosts, and the checkpoint test leaked its SQLite handle (added `repo.Close`).
- 2026-08-19: Fixed the form-encoding defect that made the Export Centre, Integrations, and API-key forms return an error on submit: `ParseMultipartForm` rejects `application/x-www-form-urlencoded` with `ErrNotMultipart`. All three now share `parseBoundedRequestForm`, with a regression test proven to fail against the old code.
- 2026-08-19: Corrected four further defects — the checkpoint restart button posted to an unregistered route, a cancelled enrichment task was recorded as a permanent failure instead of being left recoverable, `WebsiteAuditHistory` leaked its rows on a scan error, and a saved-area job could pass validation with no grid cell size and then fail at seed generation.
- 2026-08-19: The dashboard "block events" series filtered on severities no writer emits, so it was permanently zero. It now counts the warning and error worker events that are genuinely recorded and is labelled accordingly; block rate remains unmeasurable and is documented as such.
- 2026-08-19: Made deterministic seed IDs opt-in (`runner.WithDeterministicSeedIDs`) so the file, database, and Lambda runners keep their historical per-run `input_id` and repeated `-produce` behaviour, while the web runner keeps checkpoint resumability. Both behaviours are covered by tests.
- 2026-08-19: Allowed same-origin framing for `/app/map` only, so the Results split view can embed the map; every other path keeps `X-Frame-Options: DENY` and `frame-ancestors 'none'`.
- 2026-08-19: Closed the test gaps that let the above defects survive — route-level tests for the export, integration, API-key, quality, enrichment, and checkpoint registrars; the first tests for the dashboard analytics SQL; and a schema assertion covering all 55 migrated tables instead of 33.
- 2026-08-19: Wired the previously inert "Offline update info" button and pointed the wizard's runtime estimate at the enrichment control that actually exists.
- 2026-08-19: Implemented the Appendix C `POST /api/v1/proxies/test` endpoint. It tests a whole pool or an explicit selection, records each result, keeps credentials masked in the response, and is bounded to 50 proxies per call. The existing single-proxy route is unchanged.
- 2026-08-19: Runtime verification against a copy of the live workspace (the original `webdata` was never opened): the image built from this worktree started healthy, all 15 application pages and 15 API routes returned 200, and the migrated database reported schema v7 with the original 1 job, 36 businesses, 36 source records, the legacy `ok` status, and `max_time` still serialized as nanoseconds. `/app/map` returned `X-Frame-Options: SAMEORIGIN` while `/app/results` returned `DENY`.
- 2026-08-19: The Export Centre form was exercised end to end in the running application. A urlencoded POST to `/api/v1/exports` returned 303 and produced a real 13 KB XLSX; `openpyxl` parsed it independently as 37 rows x 24 columns with correct headers and data, which is the first validation of the hand-rolled OOXML writer by a third-party reader. Before the form-encoding fix this request failed at parsing.
- 2026-08-19: Confirmed no local data was modified. `webdata/ba78441f-a048-4c9d-a8de-d0589e66f132.csv` still hashes to `D11CFD4D2511E2E12D8BAA09080FF34B43475E6E2DA3D49652E20D93343BDE62`, matching the value recorded on 2026-08-07, and the pre-existing backup and export files are unchanged.
- 2026-08-20: Replaced sequential checkpoint execution with a bounded, configurable pool of leased task workers. A worker owns a task only while it holds an unexpired lease, so two workers never run the same task, a dead worker's lease expires and its task returns to the queue, and a reclaimed worker cannot overwrite the new owner's result. Migration 8 adds `lease_owner`, `lease_expires_at` and `heartbeat_at` to `job_tasks`.
- 2026-08-20: The pool divides the job's concurrency and browser budget between workers rather than multiplying it, so raising the parallel task count changes resume granularity and latency, not load. The size is configurable per job in the wizard and as a Settings default, bounded to 16.
- 2026-08-20: Cancellation, shutdown and the low-disk pause now release an in-flight task instead of failing it, so a restart resumes it exactly without consuming an attempt. CSV merges are serialised behind one lock and stay idempotent by business identity.
- 2026-08-20: Validated the pool under concurrency (bounded parallelism proven by a four-way rendezvous, every task run exactly once), cancellation (in-flight work released, committed rows kept, nothing marked failed), restart (completed tasks never re-run, no duplicated rows), lease expiry (reclaimed without consuming an attempt, the stale owner's heartbeat refused), and a 24-task 8-worker claim race. The webrunner suite ran three times under `-race` in a container with zero data races.
- 2026-08-20: A crashing task is now retried to its attempt limit while other workers continue, and the job ends partial with a task-failure reason rather than failed. The monitor Checkpoint card reports recovery state, last checkpoint time, and completed/running/remaining/failed counts.
- 2026-08-20: Implemented duplicate review and bulk delete, the two Results controls that were rendered but permanently capability-gated off. Merging is non-destructive: the merged record keeps its row, versions and provenance, its source observations and per-job links move to the surviving record, and a reversible snapshot plus an audit entry record the decision. Keep-both writes a permanent non-match rule so the pair is never suggested again. Deleting is a reversible soft delete that hides a record from results and exports without touching its evidence, and a restore action brings it back.
- 2026-08-20: Verified on a copy of the live workspace: schema migrated v7 to v8 cleanly, and merging a real candidate pair took the visible result count from 36 to 35 while the stored business and source counts both stayed at 36, which is the non-destructive guarantee in practice.
- 2026-08-20: Implemented job rename, archive/restore and notes, completing the job-control set (start, pause, resume, cancel, delete, archive, rename, duplicate, restart). All three are metadata-only and never touch lifecycle state, task plan or result file, verified by asserting the runtime state and its version are unchanged by a rename. Archiving is refused while a job is still active so live work cannot be hidden, archived jobs leave the default queue view behind a "Show archived" toggle, and every change is audited. Ownership labels, job tags and folders remain unimplemented, so the tags/folders/notes line stays unticked.
- 2026-08-20: High-throughput completion pass across six parallel subsystem worktrees plus a core batch, all merged and gated together.
- 2026-08-20: Schedules gained the replace overlap policy (cancels the still-active job before queueing), bounded retries with backoff (0-10 extra attempts, 10-3600s, attempt tracked per run), PUT /api/v1/schedules/{id}, GET /api/v1/schedules/{id}/runs, automatic export after a completed run in any advertised format, and per-schedule run retention.
- 2026-08-20: The wizard gained reusable keyword sets (durable, upsert-by-name, use-counted), include/exclude keyword filters with an explicit apply step, a category x location combination generator with a client-side locations file, and scrape defaults for location label/coordinates and a default proxy pool.
- 2026-08-20: Results gained manual field edits (name, phone, website, category) that require a reason, record operator/date/reason to field_provenance as source_type manual_edit, refresh the derived normalized columns through the import normalisers, and write change and audit rows in one transaction.
- 2026-08-20: Import now flags suspicious placeholder phones/websites/emails without dropping data, social profile URLs shed tracking parameters, and country names plus Canadian provinces normalise conservatively (unknown input passes through unchanged).
- 2026-08-20: The Map Explorer gained a results-density heat layer and failed/empty coverage shading built only from vendored Leaflet primitives, with legends and text labels so colour is never the only signal.
- 2026-08-20: The dashboard charts enabled-proxy latency buckets and per-pool reliability; the System page shows the browser-automation module version; the self-test gained browser-runtime presence and proxy-credential checks (network-gated, side-effect free).
- 2026-08-20: Adaptive performance now also reacts to the task failure rate: a window with at least half failures halves the budget and only a fully clean window recovers one step, so decay always outpaces recovery; every change is recorded with its reason.
- 2026-08-20: Retention is executed, not just stored: manual backups beyond the count, version snapshots beyond their window (each business keeps its newest), and oldest exports when a storage cap is exceeded; runs at worker start and on demand. A test proves pre-migration safety copies are never candidates.
- 2026-08-20: Optional local login: bcrypt password in settings, in-memory sessions with configurable timeout, per-address rate limiting, session invalidation on credential change, API-key path preserved for API clients; disabled by default so loopback behaviour is unchanged.
- 2026-08-20: Starter content seeds once into an empty workspace: five starter templates and six example saved views, all validated through the real service paths. POST /api/v1/jobs/validate provides create-identical dry-run validation. Disabled proxies can be batch-retested and healthy ones re-enabled. Onboarding checks disk capacity.
- 2026-08-20: Final gate for the completion pass. `go build`, `go vet`, and the full `-race` suite pass over the merged tree (19 packages, zero failures, zero data races, in a golang:1.26.6 container). Runtime verification against a copy of the live workspace: schema migrated v8 to v9, all 13 application pages plus the login form respond, starter content seeded exactly once across a restart, and end-to-end exercises succeeded for keyword sets (create/use), dry-run job validation, retention apply, a manual phone edit on a real business (persisted with provenance across a restart), the map heat toggles, and the full login lifecycle (enable, page gated, API 401 without key, session unlock, remove). The original `webdata` was never opened; the job CSV still hashes to `D11CFD4D...BDE62`.
- 2026-08-20: Specification-closure pass. Live job controls landed end to end: add runtime (supervisor-enforced extendable deadline replaces the timeout context), change concurrency, switch proxy pool (including to direct), and retry-current, each durable, audited, applied at the next safe task boundary, and exposed by the monitor forms that were previously capability-gated off.
- 2026-08-20: Sticky proxy strategies (per query, per grid cell) pin every task to one proxy by stable hash, enabling per-proxy task caps stored on the pool and honest failure attribution; when the last usable proxy fails or caps out, the job pauses as proxies_unavailable and resumes recoverably. Task failures are classified into browser-failure, proxy-failure, website-timeout, parsing-failure and task-failed events, making the fresh-browser retry an explicit crash-recovery path.
- 2026-08-20: Incremental rescan modes: JobData.incremental_mode (new_only/new_changed) flows through wizard, templates and schedules; imports classify new/changed/unchanged, flag businesses missing from a rescan as possibly_removed with a not_seen_in_rescan change row (evidence, never deletion), restore reappeared ones, and record an incremental-summary event. change_status was already filterable in Results.
- 2026-08-20: Homepage screenshots through the browser-capable path: an opt-in enrichment option captures the final URL with headless Playwright when the driver is present (never fatal, skips honestly without it), stores under webdata/screenshots, records the path on the audit and website, serves via a safe-pathed PNG-only route, and renders in the record drawer.
- 2026-08-20: Per-cell duplicate metrics aggregate DuplicatesSkipped and RowsReplaced from task checkpoints into the coverage payload, with a fourth map heat toggle for duplicate-heavy cells; templates gained a dedicated rename action.
- 2026-08-20: Closure-pass final gate. `go build`, `go vet`, and the `-race` suite pass over the fully merged tree (25 package runs, zero failures, zero data races; the webrunner and web packages ran twice). Runtime verification on a copy of the live workspace: schema migrated v9 to v10, all 13 pages respond, and live exercises succeeded for the runtime-extension and concurrency-override endpoints (controls stored durably and readable back), the duplicate-heat map toggle, the incremental-mode wizard field, sticky-strategy and task-cap proxy controls, template rename, and the screenshot route's traversal safety (404). The original `webdata` remains untouched (job CSV hash unchanged since 2026-08-07).
- 2026-08-20: GBP prospecting layer (branch feat/gbp-prospecting, spec: GBP Lead Scraper build specification reconciled in docs/gbp-prospecting.md). Pure package web/prospect: the eight-status website classifier (NO_WEBSITE, SOCIAL_ONLY, DEAD, PARKED, SSL_BROKEN, FREE_BUILDER, NO_HTTPS, LIVE) with the three named edge cases, the Engine's verbatim domain rule, a configurable explainable worth-calling score with A-F tiers, editable per-status call-opener templates, and ZIP x category-synonym query generation with a bundled sample ZIP list plus CSV upload.
- 2026-08-20: Prospect persistence (migration 11): classification runs after every import and website audit and on demand, stores status/score/tier/signals/reasons on the business row, records a change row only on status transitions, and never touches content hashes, versions, FTS inputs or deletion state - rescan modes and dedupe are unaffected.
- 2026-08-20: Prospect surfaces: results filters and columns for status/tier/score (saved-view compatible), a bulk recompute action, an explainable prospecting drawer section with the rendered call opener, a dashboard prospecting card, a wizard GBP-coverage generator, settings editors for weights/openers/boundary URLs, prospect export columns, the discovered_companies JSONL export, and GET /api/v1/prospects/discovered serving the Lead Engine's exact DiscoveredCompany contract (place_id-first identity, meta.rawPayload signals). Five GBP starter saved views seed once. site-whisper and email-verifier remain external per the spec's DO-NOT-BUILD table; their URLs are stored, validated boundaries only.
- 2026-08-20: GBP layer final gate. Full -race suite in the golang container: 20 packages, zero failures, zero data races (the web/sqlite test binary trips a Windows Defender false positive natively and is validated in the container). Runtime verification on a copy of the live workspace: schema migrated v10 to v11, five GBP saved views seeded once, a full-workspace recompute processed all 36 businesses, and the honest-semantics checks passed: an unaudited live site classifies as unclassified (not DEAD) with no score or tier, the wizard coverage endpoint generated 10 ZIP x synonym queries with a population-weighted centre, and a discovered_companies export produced 36 Engine-contract JSONL rows all keyed by real Place IDs. The original webdata is untouched.
- 2026-08-20: GBP standalone closure. Lightweight local website pre-classifier (web/enrichment/preclassify.go): single-page DNS/TLS/HTTP probe with HTTPS-first semantics (https ok -> LIVE-capable signals; certificate failure carried into an honest http fetch -> SSL_BROKEN; https connect-refused with live http -> NO_HTTPS; DNS failure -> DEAD), reusing the crawler's SSRF guard, parked/coming-soon/placeholder detection and signatures; queued through the existing enrichment queue via the additive "preclassify" option, so StoreWebsiteAudit and prospect reclassification run unchanged. Complete embedded US ZIP dataset (web/prospect/uszips.csv.gz: 40,979 ZIPs, 52 states/territories, 33,050 with Census population; GeoNames CC BY 4.0 + Census public domain, provenance in web/prospect/ZIPDATA.md) replaces the 60-ZIP sample as the query-generation fallback; CSV upload still overrides. Standalone workflow UX: wizard "Prospecting pipeline" preset on the GBP coverage block, Results "Pre-classify websites" bulk action. Lead-Engine surfaces made dormant behind the off-by-default prospect.future_integrations setting: GET /api/v1/prospects/discovered answers 403 and the discovered_companies export is neither offered nor accepted while off; boundary URLs stay stored-only.
- 2026-08-20: GBP standalone closure gate. go build/go vet native; full -race suite in the golang container: 20 packages, zero failures, zero data races. Runtime verification on a fresh copy of the live workspace: five real businesses probed through the pre-classify bulk action against the live network with honest verdicts (three LIVE over verified HTTPS, two DEAD independently confirmed unreachable from the host); WY coverage queries drawn from the embedded 40,979-ZIP dataset; the discovered endpoint answered 403 future_integrations_disabled by default, the exports page hid discovered_companies, the integrations toggle round-tripped and re-enabled both surfaces; the wizard carried the prospecting-pipeline preset. The original webdata is untouched.
- 2026-08-20: Discovery sprint (schema 12). Adaptive coverage engine: per-task yield is now durable evidence, a sliding saturation window stops a job early when the new-result ratio collapses (remaining tasks become terminal 'skipped', never resurrected on restart), and productive GBP tasks expand into the nearest unexplored same-state ZIPs from the embedded dataset with restart-safe seeds rebuilt from the task payload. Claim ordering honours priority and a not_before backoff by failure class (browser/proxy/timeout/parsing), and proxy assignment prefers healthy exits from the new proxy_task_stats aggregates without changing sticky precedence or the load envelope. Strong entity resolution replaces exact-key-or-new identity: place_id/cid/data_id stay authoritative, while phone/domain/address only match with corroboration (name similarity or proximity), auto-attaching at >=0.85, filing reviewable duplicate candidates between 0.60 and 0.85, refusing to merge the chain pattern (shared phone/domain over 1km apart), and recording identity_method/confidence/evidence on every row. Benchmark harness reports totals, per-query/ZIP/synonym yield, saturation trend, failures, proxy performance, website-status and prospect distributions, email availability and runtime, with a compare endpoint for judging one configuration against another.
- 2026-08-20: Sprint gate. Full -race suite in the golang container: 20 packages, zero failures, zero data races (runner/webrunner 126s, web/sqlite 472s under race). Runtime verification on a copy of the live workspace, real network: a two-ZIP Cheyenne GBP scrape discovered 18 unique businesses, and ZIP 82001 (18 new, 2 duplicates) triggered two automatic expansions into 82002 and 82006 tagged origin 'expansion:82001'. The benchmark report measured duplicate_rate 0.10, 1.50 new businesses/minute over 721 wall seconds, per-ZIP unique ratios, the saturation trend and the runtime-limit failure class; identity provenance was recorded for all 18 imported rows and prospect classification produced tiers C and D. The original webdata was untouched.
- 2026-08-20: Operator UI pass and final sprint gate. The embedded app gained a real design system (spacing/type scales, semantic state tokens shared by job, task, website-audit, prospect-status and tier badges, coverage cell colours tokenised against the map ramp so legend and fills cannot drift), grouped sidebar navigation, a coverage-yield dashboard card and prospect funnel composed from existing page data, a Results drawer that leads with the signal hierarchy, a Basic/Advanced/GBP wizard carrying the five adaptive-coverage controls, a Job Monitor coverage panel that hides itself when the endpoint is unavailable, and map legends matching the results hierarchy. Four genuine dark-mode and missing-CSS defects were fixed (white-on-primary fills, an unstyled system-dark map canvas, an invisible legend swatch border, an undefined sign-in panel token) along with website-audit badges and drawer components that had markup but no CSS at all. Final gate: build/vet clean, full -race suite 20 packages with zero failures and zero data races, and a runtime pass on a workspace copy where all twelve app pages render, all forty internal links resolve, and the benchmark page shows the measured run.
- 2026-08-20: Hardening sprint (branch feat/gbp-prospecting, benchmarked against discovery-sprint-rc1 on three real US markets). Six engine defects were found by measurement rather than inspection and fixed. Tasks idled on a three-minute inactivity timeout because the shared exit monitor's seed count spanned the whole plan, so only the final task could satisfy the done condition; a per-task composite monitor now ends a task when its own seeds and places finish while the run-level record budget still governs the job. Expansion built a browser viewport seed even inside a fast-mode job, so in the default GBP configuration every expansion completed and returned nothing; expansion now reuses CreateSeedJobs and inherits the same fast/browser branching. Neighbour separation used a fixed one-kilometre floor while a job sweeps its configured radius, so a dense metro expanded barely two kilometres and re-searched covered ground; the floor now tracks the radius. Engine-generated probes no longer count as evidence about the operator's own queries, after empty expansions filled the saturation window and skipped a productive plan query. Saturation now measures net-new against re-found and duplicate rows, because the CSV merge reports a rediscovered business as both added and replaced, which made a fully redundant query score perfectly. Entity resolution gained a rescan harness that exposed six further defects: three false merges (a coordinate-less franchise collapsing five locations into one, the drift tier bypassing the authoritative place-id veto, and a shared switchboard folding unrelated building tenants), chain candidate spam, a dedup_rules bypass on the exact-key path, and an unbounded duplicate leak where signal-free listings forked on every rescan.
- 2026-08-20: Hardening gate and measured outcome. Identical configuration on both builds, fresh workspace per run, real network. Unique businesses rose from 14 to 15 in a sparse rural market, 44 to 66 in a medium metro and 69 to 115 in a dense metro, while wall clock fell from about 721 seconds to between five and seven seconds; expansion went from zero rows across all nine tasks to 1, 26 and 49 rows respectively. Re-running the identical dense market against a workspace already holding 115 businesses produced 116 with no pending duplicate candidates, so rediscovery attaches rather than leaking. Full -race suite in the golang container, plus a UI pass where all twelve app pages render and all forty internal links resolve. The original webdata is untouched.
- 2026-08-21: Productization release (branch feat/productization-rc, schema 13). The UI was audited from real browser screenshots of the running application rather than from templates, and rebuilt on a documented design system: tokens for spacing, type, radii, elevation and motion; a component library (shell, page header, toolbar, chips, segmented control, buttons, form controls, cards, stat tiles, data tables, badges, drawer, modal, tabs, toast, tooltip, meter, skeleton, empty and error states); an app shell whose sidebar letter placeholders became a real inline SVG icon set, with breadcrumb context, a command affordance and a theme toggle in the topbar. Four page owners then rebuilt their surfaces against that contract on separate view stylesheets. Defects fixed that were visible in the screenshots: Results clipped horizontally at 1600px and buried its first business row about 1100px below the fold behind three stacked configuration panels; the Dashboard spent its most valuable row on six cards that merely duplicated the sidebar and shouted "not recorded" in headline type; Map Explorer painted fully opaque grid rectangles over the basemap and left a quarter of its pane empty; the Jobs table printed a garbled "Saving Exporting" stage and told the operator a completed run had "Runtime not started"; the live job monitor regressed its stage label to the raw identifier on every poll.
- 2026-08-21: Product capabilities in the same release. Results became a real workspace: one compact toolbar, lead-workflow chips (no website, weak website, contactable, never checked, top tier, needs review), column profiles, bulk workflows, export presets and an explainable drawer that shows why a business scored as it did. Discovery gained per-cell coverage confidence derived from stored evidence, refinement ranked against expansion by marginal unique yield, and expansion gated on marginal yield rather than a fixed row count. Operator productivity gained first-class rescan campaigns with durable lineage (POST /api/v1/jobs/{id}/rerun), campaign templates, export profiles, new/changed-only result filters, and benchmark history with comparison. Gates: build and vet clean, the full -race suite green at 20 packages with no failures and no data races, every app page rendering, and the live workspace preserved at 36 businesses across the redeploy.
- 2026-08-22: Specification-completion pass. Every unchecked checklist line was audited against the actual handlers, routes, persistence, templates and tests rather than against the tracking doc, and the per-section audits are kept under docs/_audit/. 258 items were classified: 168 were already implemented and verified, 4 needed only a test, 78 were unfinished and were built end to end in this pass, 5 are deliberate deviations where the capability ships by another route, and 3 are genuinely external-only. The checklist moved from 331 to 511 of 527 ticked. Newly built work includes the exact scraper version, the eight-stage pipeline view with per-stage metrics, real log severity classes and job labels, results core columns with audited inline editing and column management, the wizard's category picker, honest data-field selection and post-collection filters, tracked changes and incremental modes, data-cleaning and website-audit completions, LRU proxy rotation and full adaptive-performance measurement, Parquet export, a generated OpenAPI document with a browsable local reference, signed local automation events with retries and delivery history, local database export destinations, the optional Ollama console, encrypted backups, and the maintenance, retention, privacy and first-run completions.
- 2026-08-22: Specification-completion gate. Schema migrated 13 to 18 on the live workspace with an automatic pre-migration backup; 36 businesses and the job CSV hash D11CFD4D2511E2E1 are unchanged. Full -race suite in the golang container: 20 packages, zero failures, zero data races. Runtime verification on the deployed build: all thirteen app pages render, the OpenAPI document exposes 144 paths generated from the real route table, all six stylesheets serve, the System page reports schema v18 and the Export Centre offers Parquet. docs/technical-limitations.md was rewritten: it had asserted the absence of nine capabilities that now ship, and had claimed a post-run command hook existed when none ever did.
- 2026-08-25: Specification closure. The sixteen remaining unchecked lines were audited one by one against the specification text, the code, the routes, persistence, tests and runtime behaviour. Nine were closed by implementing them: operator-configured local automation hooks at five lifecycle points (which closes both the post-run command line and the custom plugin-hook line), enrichment include/exclude URL patterns with a bounded hand-written glob matcher, an enforced memory ceiling in the adaptive supervisor, log/slog structured logging behind a redacting handler, Results table virtualisation, and the styling line, which the specification itself satisfies by offering "a small custom design system" as its second option. The two meta lines were corrected: every requirement now carries a classification, and the Windows-specific skill-helper failure was fixed and verified passing end to end on Linux. Seven lines remain unticked, each annotated inline and enumerated individually in docs/technical-limitations.md: five intentional equivalent deviations (HTMX, Alpine.js, ECharts, and the two PostgreSQL lines), one external-only item (Google Sheets), and one genuinely infeasible clause (font and video blocking, which would require forking the upstream engine's Chromium launch arguments). The accounting balances exactly: 520 implemented and verified + 5 equivalent deviations + 1 external-only + 1 genuinely infeasible = 527.
- 2026-08-25: Specification closure gate. go build and go vet clean; the full -race suite in the golang container passed at 20 packages with zero failures and zero data races; the skill helper suite passes on Linux; Docker built and started healthy; the live workspace stayed at schema 18 with 36 businesses and the job CSV hash D11CFD4D2511E2E1 unchanged; and all thirteen app pages, the 145-path OpenAPI document, the six stylesheets and the new automation-hook status endpoint were verified on the deployed build.
- 2026-08-25: Runtime hardening (real-world acceptance and failure forensics). A production-style run had produced zero rows with repeated browser-failure and rate-limit events. Controlled live tests on the deployed image established that the build is not deterministically broken: browser mode at concurrency 1 and even at concurrency 4 with 16 cells produces 200+ businesses reliably with zero failures. The root cause was environmental and was masked by a generic label: the default task pool fans out to four workers, each a separate scrapemate application with its own single-process Chromium (~300 MB RSS each, measured), so a default job runs four concurrent browsers; on a memory-constrained host the container cgroup OOM-kills a browser (reproduced with OOMKilled=true at 1.2 GiB), which surfaces as browser-failure, and under worse headroom or simultaneous Google rate-limiting the retries cascade to zero rows. Fixes, each independently tested: memory-aware browser fan-out that caps the default simultaneous-browser count from four to two and collapses to one on a low-memory host, as a hard physical ceiling that also lowers an explicitly configured worker count; a fifteen-kind failure classifier surfaced on the failure events (browser-crash vs google-block vs navigation-timeout vs rate-limit vs network faults) with the coarse buckets preserved byte-for-byte; shm_size 1gb in compose as defense in depth; a browser launch self-check in the system self-test; and a CSV-merge identity fix so rows sharing a phone, domain or address can no longer collapse or be dropped. See docs/runtime-reliability.md and the reusable acceptance/ harness.
- 2026-08-25: Runtime hardening live acceptance. Hardened image, isolated containers, real Google Maps, direct connection. The cap was proven live: the same 16-cell concurrency-4 job that announced "Running 4 task(s) in parallel with 1 worker concurrency each" now announces "Running 2 task(s) in parallel with 2 worker concurrency each" — two browsers instead of four — while preserving full yield (211 unique businesses, zero failures, no OOM). Fast mode (pure HTTP, no browser) returned 35 businesses in ten seconds and cannot browser-fail. Lifecycle proven end to end: cancellation reached a terminal state in eight seconds with committed rows persisted; a timeout produced a labelled Partial with its rows intact; a container hard-killed mid-run recovered its abandoned job to the last safe checkpoint with all committed rows preserved, and resuming it continued from the next task with no double-count. The live production workspace was never touched (CSV hash D11CFD4D2511E2E1 unchanged).
