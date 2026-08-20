# Local Improvement Implementation Progress

Last updated: 2026-08-19

## Source and status

- [x] Restored the authoritative specification from the user's local Downloads folder to `docs/Google_Maps_Scraper_Local_Improvement_Specification.md`.
- [x] Verified the repository copy matches the 1,385-line source after line-ending normalization.
- [x] Read the complete specification and mapped its explicit list/table requirements below.
- [ ] Complete implementation of every aspirational item in the specification is not claimed.
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
- [ ] Bash skill helper baseline has an existing Windows-specific failure: `proxy file permissions are not 600`.

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

- [ ] Section complete and verified against the specification.

### Goals

- [x] Expose the scraper engine’s existing advanced capabilities through a polished graphical interface.
- [x] Keep business data, proxy credentials, exports, logs, and configuration on the local machine; screenshot storage is explicitly not implemented.
- [ ] Support small one-off searches and large geographic collection projects from the same application.
- [x] Make long-running jobs observable, controllable, safely restartable, and auditable within the checkpoint boundary documented in `technical-limitations.md`.
- [x] Provide clean, deduplicated, filterable, and exportable results rather than only raw CSV output.
- [x] Avoid mandatory dependence on paid APIs, hosted databases, commercial enrichment providers, or cloud workers.

### Guiding principles

- [ ] **Local-first:** Bind to localhost by default; store application data in local SQLite or optional local PostgreSQL.
- [x] **Open-source components:** The implemented local stack uses the existing open-source Go/SQLite/browser components and embedded first-party assets.
- [ ] **Progressive disclosure:** Offer simple presets for normal users and advanced controls for technical operators.
- [x] **Recoverability:** Persist durable lifecycle state, committed partial results, retry files, and restart recovery so interruptions do not destroy completed writes.
- [x] **Auditability:** Record source query, source URL, grid cell, extraction time, versions, changes, and core field provenance.
- [ ] **Performance safety:** Measure local CPU, RAM, disk, browser count, and block rate before increasing concurrency.
- [x] **No hidden lock-in:** Allow configuration and result export in open formats such as JSON, CSV, SQLite, and GeoJSON.

## 02 Application Structure and Navigation

- [ ] Section complete and verified against the specification.

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

- [ ] Section complete and verified against the specification.

### Summary metrics

- [x] Total raw records and total unique businesses.
- [x] Businesses collected today, this week, and this month.
- [x] Running, queued, paused, completed, partial, failed, and cancelled jobs.
- [ ] Websites, phone numbers, emails, and social profiles discovered.
- [x] Duplicate candidates detected and exact stable-identity records merged.
- [x] Average places per minute and average job duration when recorded evidence exists.
- [ ] Proxy success rate, block rate, and number of healthy proxies.
- [ ] Local database size, export storage, screenshot storage, and remaining disk capacity.

### Charts and analysis

- [x] Results collected by date.
- [x] Businesses by city, category, status, and rating band.
- [ ] Website, email, phone, and social-profile availability rates.
- [ ] Job success and failure trends.
- [ ] Scraping speed and block rate over time.
- [x] Proxy latency and reliability distribution.
- [ ] Website-active versus website-inactive results.

### Recent activity cards

- [x] Job name, state, progress percentage, current stage, records found, unique records, emails found, runtime, and estimated completion time.
- [ ] Quick actions: open, pause, resume, stop, download partial results, retry failures, and duplicate configuration.

## 04 New Scrape Wizard

- [ ] Section complete and verified against the specification.

### Step 1 — Business search

- [x] Single keyword, multiple keywords, and upstream-supported direct Google Maps URLs through the query input.
- [ ] Business category picker and reusable category groups.
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
- [ ] Remove individual cells or define excluded areas.
- [x] Save geographic areas for future jobs.

### Step 3 — Data fields

- [ ] Choose which Maps fields to retain, display, and export.
- [ ] Core details: name, category, address, phone, website, domain, coordinates, rating, reviews, and business status.
- [ ] Identifiers: Place ID, CID, Data ID, input ID, source query, and source grid cell.
- [ ] Extended details: opening hours, popular times, descriptions, price range, images, reservations, ordering links, menus, owner information, and reviews.

### Step 4 — Local enrichment

- [x] Visit the business website and detect active/inactive status.
- [x] Extract emails, visible phone numbers, contact pages, about pages, and social links.
- [x] Collect page title, meta description, language, CMS, analytics tools, SSL state, HTTP status, redirect chain, and response time.
- [x] Choose crawl scope: homepage only; homepage + contact; homepage + contact + about; or maximum page count.
- [ ] Set page timeout, URL patterns, and whether to save screenshots on errors.

### Step 5 — Filters

- [ ] Rating and review-count ranges.
- [ ] Included and excluded business categories.
- [ ] Open, temporarily closed, and permanently closed status.
- [ ] Claimed or unclaimed listing status where available.
- [ ] Business name contains/does not contain conditions.
- [x] Post-scrape filters for website, email, phone, social profile, city, ZIP, website status, and quality score.

### Step 6 — Performance and browser settings

- [x] **Fast:** Low depth, higher concurrency, no website crawl by default; intended for quick validation.
- [x] **Balanced:** Moderate depth and concurrency; optional website/email extraction.
- [x] **Deep:** Higher depth, conservative concurrency, durable partial writes, and optional enrichment.
- [x] Advanced settings: depth, concurrency, browser-pool size, pages per browser, maximum runtime, maximum records, retry count, retry delay, page timeout, random delay, fast mode, extra reviews, visible/headless browser, and proxy pool.
- [ ] Resource controls: disable images, fonts, or video where safe; cap memory usage; save failure screenshots; pause on low disk space.

### Step 7 — Review and estimate

- [x] Summarize keywords, location/coverage, generated queries, grid cells, estimated task count, enrichment, proxy pool, output, and runtime.
- [x] Display implemented warnings for duplicates, small cells, aggressive concurrency, direct connection, and unrealistic runtime; low-disk measurement is documented as unavailable.
- [x] Save configuration as a reusable template before starting.

## 05 Interactive Map Explorer

- [ ] Section complete and verified against the specification.

### Planning mode

- [x] Leaflet map with OpenStreetMap tiles.
- [x] Draw circles, polygons, and bounding boxes.
- [ ] Preview search grids, cell numbering, estimated queries, and expected task count.
- [ ] Select, remove, resize, or group cells.
- [ ] Assign different keyword groups to different areas.
- [x] Import or export geographic definitions as GeoJSON.

### Live coverage mode

- [x] **Grey:** Waiting or not searched
- [x] **Blue:** Currently running
- [ ] **Green:** Completed successfully
- [x] **Amber:** Completed with partial results or warnings
- [ ] **Red:** Failed or blocked
- [x] **Purple:** Paused

### Results mode

- [ ] Marker clustering for large datasets.
- [ ] Business popup with name, category, rating, reviews, website status, email, phone, and links.
- [x] Heatmaps for result density, failed cells, empty cells, and duplicate-heavy cells.
- [x] Filter markers using the same rules as the Results Explorer.
- [x] Export businesses inside a drawn area.
- [ ] Retry selected failed/empty cells or run a new keyword only in selected cells.

## 06 Job Management

- [ ] Section complete and verified against the specification.

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
- [ ] Apply tags, folders, notes, and ownership labels.

### Job detail data

- [ ] Configuration snapshot and exact scraper version.
- [ ] Queries, geographic cells, completed tasks, remaining tasks, raw records, unique records, websites, emails, and duplicates.
- [ ] Average records per minute, runtime, ETA, CPU, memory, browser processes, active pages, proxy performance, retries, warnings, and errors.

## 07 Live Job Monitor

- [ ] Section complete and verified against the specification.

### Pipeline view

- [ ] **Preparing queries:** Keyword expansion, validation, duplicate removal, and generated search count.
- [ ] **Generating grid:** Cells created, cells excluded, geographic coverage, and task estimate.
- [ ] **Searching Maps:** Current query, coordinates, cell, results found, speed, and block rate.
- [ ] **Extracting details:** Listings opened, fields parsed, retries, and browser health.
- [ ] **Crawling websites:** Current domain, pages visited, HTTP status, and response time.
- [ ] **Extracting contacts:** Emails, phones, and social links discovered.
- [ ] **Deduplicating:** Raw records, matches, merges, and conflicts.
- [ ] **Saving/exporting:** Rows committed, output files, and storage usage.

### Real-time controls and diagnostics

- [x] Pause, resume, cancel, reduce/increase concurrency, change proxy pool, add runtime, retry current task, and download partial results.
- [ ] Show current keyword, location, cell, active proxy, browser count, pages, places per minute, CPU, RAM, database writes, website queue, and ETA.
- [x] Use durable Server-Sent Events with cursor replay, plus bounded progress fallback.

### Human-readable logs

- [ ] Severity levels: information, warning, rate limit, proxy failure, browser failure, website timeout, parsing failure, duplicate, maximum runtime, and system error.
- [ ] Search, severity filters, auto-scroll control, copy details, download logs, and link errors to the affected query/cell/record.

## 08 Results Explorer

- [ ] Section complete and verified against the specification.

### Data table capabilities

- [x] Indexed server pagination, FTS5 search, sorting, and bounded column filters; virtual scrolling is not claimed.
- [ ] Resize, reorder, hide, freeze, and group columns.
- [ ] Inline editing, multi-row selection, keyboard navigation, copy cells/rows, and saved table layouts.
- [ ] Table-only, map-only, and split table/map views.
- [ ] Saved views tied to filters, visible columns, sorting, and grouping.

### Core columns

- [ ] **Business:** Name, primary category, additional categories, description, claimed status, business status.
- [ ] **Location:** Full address, street, city, state, postal code, country, latitude, longitude, plus code.
- [ ] **Contacts:** Phone, normalized phone, phone type, website, domain, emails, email type, email status.
- [ ] **Social:** Facebook, Instagram, LinkedIn, X/Twitter, YouTube, TikTok, WhatsApp.
- [ ] **Reputation:** Rating, review count, ratings breakdown, user reviews, popular times.
- [ ] **Identifiers:** Place ID, CID, Data ID, Maps URL, source query, source cell, input ID.
- [ ] **Quality:** Website status, response time, technology, quality score, confidence, last checked.
- [ ] **Workflow:** Tags, notes, reviewed flag, scrape date, last update, change status.

### Bulk actions

- [ ] Export, delete, tag, untag, mark reviewed, add to saved list, re-enrich, recheck website, recheck emails, merge duplicates, and open selected websites.
- [ ] Copy selected domains, emails, phone numbers, addresses, or Maps URLs.

### Record detail drawer

- [ ] Complete structured record, map location, source links, website preview or screenshot, social profiles, provenance, raw JSON, notes, tags, change history, and duplicate matches.

## 09 Advanced Filtering

- [ ] Section complete and verified against the specification.

### Filter operators

- [x] AND, OR, and nested groups.
- [x] Contains, does not contain, starts with, ends with, equals, not equal, empty, and not empty.
- [x] Numeric minimum, maximum, between, greater than, and less than.
- [ ] Date ranges, boolean fields, category membership, and geographic radius/polygon filters.

### Example reusable views

- [x] Businesses without websites.
- [x] Businesses with an active website but no visible email.
- [ ] Highly rated businesses with low-quality websites.
- [x] Businesses with phone but no website.
- [ ] Businesses with email and LinkedIn.
- [x] Open listings with more than 50 reviews.
- [ ] New or changed businesses since the last scrape.
- [x] Permanently closed listings.

## 10 Deduplication Engine

- [ ] Section complete and verified against the specification.

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
- [ ] Choose preferred value by source confidence, recency, or completeness.
- [x] Preserve all source queries, cells, timestamps, and historical values after merging.

## 11 Data Cleaning and Normalization

- [ ] Section complete and verified against the specification.
- [x] Normalize phone numbers while preserving the original value.
- [x] Canonicalize website URLs, strip common tracking parameters, derive host domain, and normalize protocols; public-suffix limits are documented.
- [x] Lowercase and trim emails; remove duplicates and invalid syntax.
- [x] Normalize business names, whitespace, punctuation, Unicode width, and common legal suffixes for matching.
- [x] Parse full addresses into street, city, state, postal code, and country where possible.
- [ ] Normalize country/state labels and category names.
- [x] Standardize social URLs and remove share/tracking variants.
- [x] Convert rating and review counts into numeric fields.
- [x] Use consistent nullable database fields and display-safe missing-value handling.
- [ ] Flag suspicious placeholder values, malformed URLs, and mismatched domains.

## 12 Local Email Handling

- [ ] Section complete and verified against the specification.

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

- [ ] Section complete and verified against the specification.

### Availability and technical health

- [ ] Reachability, HTTP status, final URL, redirect chain, HTTPS state, certificate errors, and response time.
- [x] Parked domain, coming-soon page, placeholder page, and inaccessible website detection.
- [ ] Homepage screenshot and optional error screenshot.

### Basic website quality audit

- [x] Page title and meta description presence.
- [ ] Contact page, about page, visible phone, visible email, address, and social links.
- [x] Mobile viewport tag, basic page-size measurement, broken internal links, mixed content, and old copyright year.
- [x] Obvious template/default text and incomplete setup indicators.

### Technology detection

- [x] WordPress, WooCommerce, Shopify, Wix, Squarespace, Webflow, Joomla, Drupal, Magento, React, Next.js, and common page builders.
- [x] Google Analytics, Google Tag Manager, Meta Pixel, and other visible script signatures.
- [ ] Detection should be signature-based and show confidence rather than claiming certainty.

## 14 Business Quality Scoring

- [ ] Section complete and verified against the specification.

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

- [ ] Section complete and verified against the specification.
- [ ] Source type: Google Maps, website homepage, contact page, about page, footer, structured data, or manual edit.
- [ ] Source URL, source query, source grid cell, extraction timestamp, extraction method, and confidence.
- [x] Original value, normalized value, current preferred value, and previous values.
- [x] Manual edits should record the operator, date, and reason when local user accounts are enabled.
- [x] Exports may optionally include provenance columns or a companion JSON file.

## 16 Change Tracking and Incremental Scraping

- [ ] Section complete and verified against the specification.

### Tracked changes

- [ ] New business discovered.
- [x] Listing removed, closed, reopened, or status changed.
- [ ] Phone, website, address, category, rating, review count, opening hours, or email changed.
- [ ] Website became active/inactive or redirected to another domain.
- [ ] New social profile or contact information discovered.

### Incremental modes

- [ ] Collect only new listings.
- [ ] Collect new and changed listings.
- [ ] Recheck only fields likely to change.
- [ ] Re-enrich only businesses whose website or contact data is missing/stale.
- [x] Retain configurable version history and show before/after comparisons.

## 17 Scheduling

- [ ] Section complete and verified against the specification.
- [x] One-time, hourly, daily, weekly, monthly, and bounded five-field custom cron schedules.
- [x] Selected days and times with embedded IANA timezone handling; a separate run-on-start switch is not implemented.
- [x] Skip, queue, or replace when the previous run is still active.
- [x] Automatic retries with retry limits and backoff.
- [ ] Incremental-only mode for recurring jobs.
- [x] Automatic export or local webhook after completion.
- [ ] Retention rules for old runs, logs, screenshots, and exports.
- [x] Missed-run queue-one or skip handling after the machine was offline.

## 18 Saved Searches and Templates

- [ ] Section complete and verified against the specification.
- [x] Save complete implemented job configurations including keywords, geography, enrichment flags, performance, and proxy pool; unsupported filter/output automation is documented.
- [x] Duplicate, rename, tag, organize into folders, pin favourites, and add notes.
- [x] Export and import validated templates as JSON without accepting inline proxy credentials.
- [ ] Parameterised templates such as one category applied to many cities.
- [ ] Track last run, use count, average result count, and average duration.
- [x] Starter templates: businesses without websites, high-rated businesses, closed-business monitor, new local businesses, and website audit prospects.

## 19 Proxy Manager

- [ ] Section complete and verified against the specification.

### Import and organisation

- [x] Paste up to 5,000 bounded proxy URLs; multipart TXT/CSV upload is documented as unavailable.
- [x] Support HTTP, HTTPS, SOCKS5, authentication, and named pools.
- [x] Encrypt credentials at rest and mask them in the interface, errors, and logs.
- [x] Assign pools to templates and individual jobs without persisting decrypted credentials in job data.

### Testing and health

- [ ] Connection success, Google access, response latency, exit IP, country, last success, failure count, block count, and usage count.
- [ ] Statuses: healthy, slow, rate-limited, blocked, authentication failed, offline, and cooling down.

### Rotation strategies

- [ ] Round robin, random, least recently used, lowest failure rate, fastest, sticky per query, and sticky per grid cell.
- [x] Automatically disable repeated failures, cool down rate-limited proxies, retest disabled proxies, and cap tasks per proxy.

## 20 Adaptive Performance

- [ ] Section complete and verified against the specification.
- [x] Reduce concurrency when block/failure rate rises.
- [x] Increase concurrency cautiously after a stable success window.
- [ ] Reduce browser count or pages per browser when RAM pressure rises.
- [x] Pause new tasks when disk space becomes low.
- [x] Retry failed pages with another proxy or a fresh browser context.
- [x] Restart crashed browser processes automatically.
- [x] Pause the job when all proxies fail and resume after recovery.
- [ ] Adjust website timeout using recent response history.
- [x] Display every automatic change and the reason it occurred.

## 21 Checkpoints and Recovery

- [ ] Section complete and verified against the specification.
- [ ] Persist completed queries, grid cells, listing IDs, enrichment tasks, and deduplication state.
- [ ] Save checkpoints at configurable intervals and after each meaningful stage.
- [x] Resume after application or computer restart.
- [x] Detect abandoned “running” jobs at startup and offer recovery.
- [x] Preserve partial CSV/database results and durable redacted lifecycle logs.
- [x] Continue from last completed query or grid cell.
- [x] Create and verify local backups before database migrations.
- [x] Expose recovery status and last checkpoint time in the UI.

## 22 Export Centre

- [ ] Section complete and verified against the specification.

### Formats

- [x] CSV.
- [x] XLSX.
- [x] JSON.
- [x] JSONL.
- [x] SQLite.
- [x] PostgreSQL-compatible insert transaction (not a native server backup).
- [x] MySQL/MariaDB-compatible insert transaction.
- [ ] Parquet.
- [x] GeoJSON.
- [x] KML.
- [x] VCard.
- [x] Plain text lists.

### Export builder

- [ ] Export all, selected, filtered, or saved-view records.
- [x] Choose, rename, and reorder columns.
- [x] Export normalized preferred businesses by default, with an explicit source-row duplicate view.
- [ ] Split by city, category, job, or maximum row count.
- [ ] Include raw JSON, source data, provenance, or change history.
- [x] Compress multiple files into ZIP.

### Export history

- [x] File name, format, record count, source job, filters, date, size, checksum, repeat download, and delete; saved-view identity is represented by persisted filters.
- [ ] Save export presets for repeated delivery formats.

## 23 Local API

- [ ] Section complete and verified against the specification.

### Endpoint groups

- [x] **Jobs:** Create, validate, start, pause, resume, cancel, delete, duplicate, status, progress, checkpoints, and logs.
- [x] **Results:** List, search, filter, retrieve, edit, tag, deduplicate, enrich, and bulk actions.
- [x] **Maps:** Saved areas, grid preview, cell status, and geographic result queries.
- [x] **Proxies:** Import, test, pool, enable/disable, health, and usage.
- [x] **Schedules:** Create, update, enable, disable, run now, and history.
- [ ] **Exports:** Create, status, list, download, repeat, and delete.
- [x] **System:** Health, resource metrics, database statistics, version, maintenance, and diagnostics.

### API experience

- [ ] OpenAPI/Swagger documentation.
- [ ] Examples in cURL, Python, JavaScript, and Go.
- [x] Local API keys with read-only or full-access permissions.
- [x] Request logs, configurable local rate limits, and secret masking.
- [x] Server-Sent Events with durable event IDs for live job progress.

## 24 Local Integrations

- [ ] Section complete and verified against the specification.
- [ ] n8n self-hosted and Activepieces self-hosted through local webhooks or API calls.
- [ ] Local PostgreSQL, MySQL/MariaDB, or another SQLite database.
- [x] File-system watch folder for completed exports.
- [ ] Run a local shell command or Python script after completion.
- [x] Send result batches or completion events to a local webhook.
- [ ] Optional Google Sheets sync using the user’s own Google credentials and quotas.
- [ ] Custom plugin hooks for enrichment, validation, scoring, and export.

## 25 Optional Local AI

- [ ] Section complete and verified against the specification.

### Possible Ollama-powered features

- [x] Generate keyword and category variations.
- [ ] Convert a natural-language request into a scrape configuration.
- [x] Convert natural language into result filters.
- [ ] Classify businesses and website quality.
- [ ] Explain quality scores and duplicate matches.
- [ ] Summarize business descriptions or change history.
- [ ] Suggest missing cities, categories, or exclusion keywords.

## 26 Database and Storage

- [ ] Section complete and verified against the specification.

### Default database

- [x] SQLite with WAL mode, busy timeout, foreign keys, and one serialized writer for safe concurrent reads/writes.
- [x] FTS5 for fast search across names, categories, addresses, emails, domains, and notes.
- [ ] Batch inserts, indexed filters, integrity checks, VACUUM, migrations, backups, and retention policies.
- [ ] Optional local PostgreSQL for larger datasets or multiple local workers.

### Recommended tables

- [x] **jobs:** Compatible job configuration plus durable runtime state, counters, timestamps, and schema/config versions.
- [ ] **job_tasks:** Schema exists, but the upstream runner does not emit complete query/cell/listing/website task cursors; see technical limitations.
- [x] **businesses:** Current preferred normalized business record.
- [x] **business_versions:** Immutable historical snapshots and field changes.
- [x] **business_sources:** Query, cell, Maps/source URL, raw snapshot, timestamp, and provenance.
- [x] **websites:** Availability, metadata, technologies, screenshots, and audit results.
- [ ] **emails / phones / social_profiles:** Contact values, source, confidence, and status.
- [x] **proxies / proxy_health:** Pools, encrypted credentials, tests, usage, failures, disable state, and cooldown.
- [x] **schedules:** Recurrence/cron expression, template, policies, next/last run, and execution history.
- [x] **exports:** Filters, files, counts, timestamps, sizes, state, and checksums; presets are not populated.
- [x] **tags / notes / audit_logs:** Local result organisation, workflow history, and traceability.
- [x] **settings:** Versioned application defaults and local preferences with audit entries.

### Storage directories

- [ ] Database, exports, screenshots, logs, cache/browser profiles, backups, and temporary files should be separate and configurable.
- [ ] Display size and retention settings for each directory.

## 27 System and Diagnostics

- [ ] Section complete and verified against the specification.

### System information

- [x] Application, scraper, database, Go, and browser versions.
- [ ] CPU, RAM, disk, database size, queue length, active browsers/pages, running jobs, log size, screenshot storage, and export storage.
- [ ] Worker heartbeat, last successful browser launch, last database write, and proxy-pool status.

### Maintenance actions

- [ ] Restart worker, stop all jobs, clear cache, clean old screenshots/exports/logs, VACUUM database, integrity check, create backup, restore backup, export diagnostics, check for updates, and run self-test.

### Self-test checks

- [x] Database writable.
- [ ] Output directories writable.
- [ ] Browser can launch.
- [x] Internet reachable.
- [x] Maps page reachable.
- [x] Proxy credentials accepted.
- [x] Sufficient disk and memory.
- [x] Scheduled worker active.

## 28 Settings and Preferences

- [ ] Section complete and verified against the specification.

### Scraping defaults

- [x] Language, location, zoom, depth, runtime, concurrency, browser-pool size, pages per browser, enrichment, reviews, browser visibility, and proxy pool.

### Storage and retention

- [ ] Data, export, screenshot, log, backup, and temporary directories.
- [x] Maximum storage, automatic cleanup, number of backups, and record/version retention.

### Privacy and appearance

- [ ] Disable telemetry; redact secrets from logs; clear browser profiles; encrypt sensitive settings.
- [x] Light/dark/system mode, compact table, sidebar default, date/time format, number format, language, reduced motion, and font size.

## 29 Security and Privacy

- [ ] Section complete and verified against the specification.
- [x] Bind the native server and published Compose port to 127.0.0.1 by default and warn clearly for wildcard server binds.
- [x] Optional local login with strong password hashing and session timeout.
- [ ] CSRF protection, secure cookies, API-key protection, and local rate limiting.
- [x] Encrypt proxy URLs/passwords with AES-256-GCM under a separate local key; no other secret setting is currently stored.
- [x] Mask credentials and tokens in the implemented UI, errors, lifecycle/proxy logs, and exports.
- [x] Validate implemented upload types, bounded body/file sizes, and contained output paths; archive extraction is not implemented.
- [x] Prevent arbitrary file reads/writes through validated IDs, safe relative paths, and fixed local directories.
- [x] Audit lifecycle controls, settings, backups, proxy imports, exports, and result workflow changes.
- [ ] Offer encrypted backups and a privacy-scrubbed diagnostics bundle.

## 30 UI, Accessibility and Onboarding

- [ ] Section complete and verified against the specification.

### Visual design

- [x] Clean collapsible sidebar, sticky header, wide operational tables, restrained cards, consistent controls, and clear hierarchy.
- [x] Status colours plus persistent text labels so state is never communicated by colour alone.
- [ ] Progress bars, skeleton loaders, empty states, tooltips, inline validation, and actionable error messages.
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
- [ ] Suggested shortcuts: N new scrape, J jobs, R results, / search, P pause current job, Esc close panel, Ctrl/Cmd+E export.

### Help and first-run experience

- [ ] Setup wizard that checks browser, database, data directory, internet access, disk capacity, and optional proxies.
- [x] Guided San Francisco sample job and contextual explanations for depth, zoom, radius, grid cells, concurrency, runtime, proxies, and email crawling.
- [x] Embedded queue/partial troubleshooting, export/API guidance, and links to job logs/System diagnostics.

## 31 Recommended Technology Stack

- [ ] Section complete and verified against the specification.
- [x] **Backend:** Existing Go backend — Retains current scraper engine and avoids a full rewrite.
- [ ] **Server-rendered UI:** Go templates + HTMX — Small local footprint and straightforward integration.
- [ ] **Client-side helpers:** Alpine.js — Lightweight state for modals, forms, and local interactions.
- [ ] **Styling:** Tailwind CSS or a small custom design system — Fast, consistent local UI without a heavy SPA requirement.
- [ ] **Data table:** Tabulator — Virtual scrolling, editing, filtering, grouping, and export support.
- [x] **Maps:** Leaflet + OpenStreetMap — Open-source map interface and drawing ecosystem.
- [ ] **Charts:** Apache ECharts — Rich dashboards and large-data performance.
- [x] **Database:** SQLite + FTS5 — Simple local deployment with strong search and indexing.
- [ ] **Large local DB:** PostgreSQL — Optional scale and multi-worker coordination.
- [ ] **Scheduling:** robfig/cron — Mature local cron support for Go.
- [ ] **XLSX export:** Excelize — Native Go spreadsheet generation.
- [x] **Local AI:** Ollama — Optional local inference without recurring API charges.
- [ ] **Packaging:** Docker Compose — One-command local app, database, and optional services.
- [ ] **Logging:** Go slog or Zerolog — Structured logs with efficient local storage.
- [ ] **API docs:** OpenAPI / Swagger — Browsable contract and generated examples.

## 32 Implementation Roadmap

- [ ] Section complete and verified against the specification.

### Release 1 — Professional local scraper

- [x] Create the navigation shell, dashboard, and consistent design system.
- [x] Implement the multi-step New Scrape Wizard and expose existing advanced settings.
- [x] Move jobs and results into SQLite with migrations and indexes.
- [x] Build job detail, progress, logs, pause/resume/cancel, partial download, and checkpoint recovery.
- [x] Build the Results Explorer with search, filters, saved views, inline detail drawer, and CSV/XLSX/JSON exports.
- [ ] Implement normalization and exact/fuzzy deduplication.
- [x] Add system health, storage usage, backups, and settings.

### Release 2 — Advanced local collection

- [x] Add interactive map drawing, bounding boxes, grid preview, and live coverage states.
- [ ] Add persistent proxy pools, testing, rotation, and health management.
- [x] Add website/email/social enrichment and website-status analysis.
- [ ] Add saved templates, schedules, incremental runs, and change tracking.
- [ ] Add quality scoring, provenance, and export presets.
- [x] Expand the REST API and add local integration hooks.

### Release 3 — Best-in-class local edition

- [x] Add adaptive concurrency, browser recovery, proxy cooldown, and low-resource safeguards.
- [ ] Add coverage heatmaps, missing-area retry, and selected-cell re-scraping.
- [ ] Add advanced version history and field-level confidence.
- [x] Add optional local AI through Ollama.
- [ ] Add plugin interfaces, complete diagnostics, accessibility polish, and advanced retention controls.

### Recommended first four screens

- [x] **1:** New Scrape Wizard — Makes advanced configuration approachable.
- [ ] **2:** Live Job Monitor — Creates trust and control during long jobs.
- [x] **3:** Results Explorer — Turns raw output into usable local data.
- [ ] **4:** Proxy Manager — Supports repeated and heavier collection safely.

## 33 Acceptance Criteria and Limitations

- [ ] Section complete and verified against the specification.

### Release 1 acceptance criteria

- [x] A user can configure and start a validated draft or queued job without using CLI flags.
- [ ] The UI shows meaningful progress, current stage, records, errors, resources, and ETA.
- [x] Jobs can be paused, resumed, cancelled, recovered safely after restart, and downloaded/exported with committed partial rows.
- [x] Results persist in a searchable local database and can be filtered and exported.
- [x] Duplicate records are detected using stable IDs, normalized fallback keys, and conservative fuzzy candidates.
- [x] Stored proxy secrets are encrypted/masked and the server/published port bind to localhost by default.
- [ ] Database backup and restore are available from the UI.

### Performance and reliability criteria

- [x] No loss of committed records after simulated interrupted retry-file recovery/restart and the final Docker restart smoke.
- [ ] Large tables remain responsive using virtualisation and indexed queries.
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

- [ ] Section complete and verified against the specification.

### Appendix A — Complete feature checklist

- [x] **Foundation:** Navigation shell • Dashboard • SQLite storage • Migrations • Settings • System health • Backups
- [ ] **Scrape configuration:** Keywords • Categories • Exclusions • CSV/TXT upload • Locations • Radius • Polygon • Bounding box • Grid • Field selection • Enrichment • Filters • Performance presets • Advanced settings • Review/estimate
- [x] **Operations:** Queue • Pause/resume/cancel • Live pipeline • Logs • ETA • Resource monitoring • Partial download • Checkpoints • Recovery • Retry failures
- [x] **Data:** Results table • Map view • Saved views • Bulk actions • Record drawer • Advanced filters • Normalization • Deduplication • Scoring • Provenance • Change history
- [ ] **Enrichment:** Website reachability • HTTP/HTTPS • Redirects • Screenshots • Email extraction • MX checks • Social profiles • CMS/technology detection • Basic website audit
- [ ] **Automation:** Templates • Schedules • Incremental runs • Export presets • Webhooks • Post-run scripts • Local API
- [x] **Scale:** Proxy pools • Proxy testing • Rotation • Adaptive concurrency • Browser recovery • Low-resource safeguards
- [ ] **Experience:** Dark mode • Keyboard shortcuts • Accessibility • Onboarding • Embedded help • Diagnostics

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
- [ ] **POST /api/v1/results/export:** Create an export from a filter or selection.
- [x] **POST /api/v1/proxies/test:** Test selected proxies or pools.
- [x] **GET /api/v1/system/health:** Database/integrity/schema/job/result/export/backup health; browser/disk/worker telemetry limits are documented.
- [ ] **POST /api/v1/system/backup:** Create local database/configuration backup.

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
