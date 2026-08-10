# Google Maps Scraper

*Complete Local Product Improvement & Feature Specification*

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Purpose</strong></p>
<p>A complete, implementation-ready catalogue of improvements that can be built with open-source libraries and run locally without recurring scraper, cloud, or enrichment subscriptions.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

| **Document type**       | Product requirements and implementation specification |
|-------------------------|-------------------------------------------------------|
| **Deployment model**    | Local machine / self-hosted Docker                    |
| **Primary constraint**  | No required paid API or SaaS dependency               |
| **Recommended backend** | Existing Go scraper and REST API                      |
| **Prepared**            | August 06, 2026                                       |

## Core outcome

> Transform the existing basic job launcher into a professional local research workspace with geographic planning, robust job control, live monitoring, searchable results, enrichment, deduplication, scheduling, change tracking, exports, and complete local data ownership.

## Contents

[1. Product vision and guiding principles](#sec01)

[2. Application structure and navigation](#sec02)

[3. Dashboard](#sec03)

[4. New Scrape Wizard](#sec04)

[5. Interactive Map Explorer](#sec05)

[6. Job Management](#sec06)

[7. Live Job Monitor](#sec07)

[8. Results Explorer](#sec08)

[9. Advanced Filtering](#sec09)

[10. Deduplication Engine](#sec10)

[11. Data Cleaning and Normalization](#sec11)

[12. Local Email Handling](#sec12)

[13. Website Analysis](#sec13)

[14. Business Quality Scoring](#sec14)

[15. Field Provenance and Auditability](#sec15)

[16. Change Tracking and Incremental Scraping](#sec16)

[17. Scheduling](#sec17)

[18. Saved Searches and Templates](#sec18)

[19. Proxy Manager](#sec19)

[20. Adaptive Performance](#sec20)

[21. Checkpoints and Recovery](#sec21)

[22. Export Centre](#sec22)

[23. Local API](#sec23)

[24. Local Integrations](#sec24)

[25. Optional Local AI](#sec25)

[26. Database and Storage](#sec26)

[27. System and Diagnostics](#sec27)

[28. Settings and Preferences](#sec28)

[29. Security and Privacy](#sec29)

[30. UI, Accessibility and Onboarding](#sec30)

[31. Recommended Technology Stack](#sec31)

[32. Implementation Roadmap](#sec32)

[33. Acceptance Criteria and Limitations](#sec33)

[Appendices](#appendix)

<a id="sec01"></a>
## 01 Product Vision and Guiding Principles

*Define what the improved application should become and the constraints every feature must respect.*

Product vision: Build a local-first Google Maps data collection and research application that is approachable for non-technical users while preserving advanced control for power users.

### Goals

- Expose the scraper engine’s existing advanced capabilities through a polished graphical interface.

- Keep business data, proxy credentials, exports, logs, screenshots, and configuration on the local machine.

- Support small one-off searches and large geographic collection projects from the same application.

- Make long-running jobs observable, controllable, resumable, and auditable.

- Provide clean, deduplicated, filterable, and exportable results rather than only raw CSV output.

- Avoid mandatory dependence on paid APIs, hosted databases, commercial enrichment providers, or cloud workers.

### Guiding principles

| **Principle**          | **Implementation meaning**                                                                         |
|------------------------|----------------------------------------------------------------------------------------------------|
| Local-first            | Bind to localhost by default; store application data in local SQLite or optional local PostgreSQL. |
| Open-source components | Use permissively licensed libraries for maps, charts, tables, scheduling, exports, and local AI.   |
| Progressive disclosure | Offer simple presets for normal users and advanced controls for technical operators.               |
| Recoverability         | Persist checkpoints and partial results so interruptions do not destroy long jobs.                 |
| Auditability           | Record source query, source URL, grid cell, extraction time, and field provenance.                 |
| Performance safety     | Measure local CPU, RAM, disk, browser count, and block rate before increasing concurrency.         |
| No hidden lock-in      | Allow configuration and result export in open formats such as JSON, CSV, SQLite, and GeoJSON.      |

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Meaning of “free”</strong></p>
<p>All features in this specification can be implemented with open-source software and run locally. Internet access, electricity, hardware, and optional high-quality residential proxies remain operational resources rather than software subscription requirements.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="sec02"></a>
## 02 Application Structure and Navigation

*Replace the single-screen form with a coherent workspace.*

### Primary navigation

| **Area**       | **Purpose**                                                                          |
|----------------|--------------------------------------------------------------------------------------|
| Dashboard      | Overview of jobs, results, data quality, system usage, and recent activity.          |
| New Scrape     | Guided multi-step job configuration.                                                 |
| Jobs           | Queue, history, status, controls, logs, and checkpoints.                             |
| Results        | Spreadsheet-style business database and bulk actions.                                |
| Map Explorer   | Draw search areas, preview grids, inspect coverage, and view results geographically. |
| Saved Searches | Reusable keywords, locations, filters, and complete templates.                       |
| Schedules      | Recurring and incremental local jobs.                                                |
| Proxies        | Proxy pools, testing, health, usage, and rotation.                                   |
| Exports        | Create, manage, and repeat local exports.                                            |
| API            | OpenAPI documentation, keys, examples, and request logs.                             |
| System         | Resource usage, diagnostics, backups, maintenance, and worker health.                |
| Settings       | Defaults, storage, appearance, privacy, and security.                                |

### Global interface features

- Collapsible left sidebar and sticky top bar.

- Global search across jobs, businesses, tags, templates, and exports.

- Command palette for keyboard-driven navigation and actions.

- Persistent job activity indicator visible from every screen.

- Light, dark, and system appearance modes.

- Toast notifications, confirmations, helpful empty states, and actionable errors.

<a id="sec03"></a>
## 03 Dashboard

*Provide an immediate operational summary instead of opening on an empty form.*

### Summary metrics

- Total raw records and total unique businesses.

- Businesses collected today, this week, and this month.

- Running, queued, paused, completed, partial, failed, and cancelled jobs.

- Websites, phone numbers, emails, and social profiles discovered.

- Duplicates detected and merged.

- Average places per minute and average job duration.

- Proxy success rate, block rate, and number of healthy proxies.

- Local database size, export storage, screenshot storage, and remaining disk capacity.

### Charts and analysis

- Results collected by date.

- Businesses by city, category, status, and rating band.

- Website, email, phone, and social-profile availability rates.

- Job success and failure trends.

- Scraping speed and block rate over time.

- Proxy latency and reliability distribution.

- Website-active versus website-inactive results.

### Recent activity cards

- Job name, state, progress percentage, current stage, records found, unique records, emails found, runtime, and estimated completion time.

- Quick actions: open, pause, resume, stop, download partial results, retry failures, and duplicate configuration.

<a id="sec04"></a>
## 04 New Scrape Wizard

*Turn the long technical form into a guided configuration flow.*

### Step 1 — Business search

- Single keyword, multiple keywords, or direct Google Maps URLs.

- Business category picker and reusable category groups.

- Include and exclude keywords.

- Upload keywords from CSV or TXT; paste one query per line.

- Generate category × location combinations automatically.

- Preview all generated queries before launch.

- Detect exact duplicates and highly similar queries.

- Save keyword sets for reuse.

### Step 2 — Location and geographic scope

- Search by city, state/province, country, ZIP/postal code, or coordinates.

- Select multiple cities or upload locations from a file.

- Draw a circle, polygon, or bounding box on a map.

- Set radius, zoom, and grid-cell size.

- Preview generated grid cells and estimated task count.

- Remove individual cells or define excluded areas.

- Save geographic areas for future jobs.

### Step 3 — Data fields

- Choose which Maps fields to retain, display, and export.

- Core details: name, category, address, phone, website, domain, coordinates, rating, reviews, and business status.

- Identifiers: Place ID, CID, Data ID, input ID, source query, and source grid cell.

- Extended details: opening hours, popular times, descriptions, price range, images, reservations, ordering links, menus, owner information, and reviews.

### Step 4 — Local enrichment

- Visit the business website and detect active/inactive status.

- Extract emails, visible phone numbers, contact pages, about pages, and social links.

- Collect page title, meta description, language, CMS, analytics tools, SSL state, HTTP status, redirect chain, and response time.

- Choose crawl scope: homepage only; homepage + contact; homepage + contact + about; or maximum page count.

- Set page timeout, URL patterns, and whether to save screenshots on errors.

### Step 5 — Filters

- Rating and review-count ranges.

- Included and excluded business categories.

- Open, temporarily closed, and permanently closed status.

- Claimed or unclaimed listing status where available.

- Business name contains/does not contain conditions.

- Post-scrape filters for website, email, phone, social profile, city, ZIP, website status, and quality score.

### Step 6 — Performance and browser settings

| **Preset** | **Behaviour**                                                                              |
|------------|--------------------------------------------------------------------------------------------|
| Fast       | Low depth, higher concurrency, no website crawl by default; intended for quick validation. |
| Balanced   | Moderate depth and concurrency; optional website/email enrichment.                         |
| Deep       | Higher depth, conservative concurrency, checkpointing, and full enrichment.                |

- Advanced settings: depth, concurrency, browser-pool size, pages per browser, maximum runtime, maximum records, retry count, retry delay, page timeout, random delay, fast mode, extra reviews, visible/headless browser, and proxy pool.

- Resource controls: disable images, fonts, or video where safe; cap memory usage; save failure screenshots; pause on low disk space.

### Step 7 — Review and estimate

- Summarize keywords, locations, generated queries, grid cells, estimated task count, enrichment options, proxy pool, output destination, and runtime limit.

- Display warnings for overlapping keywords, overly small grid cells, aggressive concurrency, missing proxies, low disk space, or unrealistic runtime.

- Save configuration as a reusable template before starting.

<a id="sec05"></a>
## 05 Interactive Map Explorer

*Make geography visible before, during, and after collection.*

### Planning mode

- Leaflet map with OpenStreetMap tiles.

- Draw circles, polygons, and bounding boxes.

- Preview search grids, cell numbering, estimated queries, and expected task count.

- Select, remove, resize, or group cells.

- Assign different keyword groups to different areas.

- Import or export geographic definitions as GeoJSON.

### Live coverage mode

| **Colour/state** | **Meaning**                                |
|------------------|--------------------------------------------|
| Grey             | Waiting or not searched                    |
| Blue             | Currently running                          |
| Green            | Completed successfully                     |
| Amber            | Completed with partial results or warnings |
| Red              | Failed or blocked                          |
| Purple           | Paused                                     |

### Results mode

- Marker clustering for large datasets.

- Business popup with name, category, rating, reviews, website status, email, phone, and links.

- Heatmaps for result density, failed cells, empty cells, and duplicate-heavy cells.

- Filter markers using the same rules as the Results Explorer.

- Export businesses inside a drawn area.

- Retry selected failed/empty cells or run a new keyword only in selected cells.

<a id="sec06"></a>
## 06 Job Management

*Create a durable job queue with controls and history.*

### Job lifecycle

| **State**  | **Description**                                                               |
|------------|-------------------------------------------------------------------------------|
| Draft      | Configuration saved but not submitted.                                        |
| Queued     | Waiting for local worker capacity.                                            |
| Starting   | Preparing queries, grid, browser, database, and proxy pool.                   |
| Running    | Actively collecting or enriching records.                                     |
| Paused     | Intentionally stopped with checkpoint preserved.                              |
| Cancelling | Safely finishing current writes and shutting down.                            |
| Completed  | Finished normally.                                                            |
| Partial    | Finished with timeout, skipped tasks, failed cells, or incomplete enrichment. |
| Failed     | Could not continue because of an error.                                       |
| Cancelled  | Stopped by the operator.                                                      |

### Controls

- Start, pause, resume, cancel, delete, archive, rename, duplicate, and restart.

- Add runtime, change concurrency, switch proxy pool, and retry failed tasks.

- Restart from the latest checkpoint rather than from the beginning.

- Download partial results at any time.

- Apply tags, folders, notes, and ownership labels.

### Job detail data

- Configuration snapshot and exact scraper version.

- Queries, geographic cells, completed tasks, remaining tasks, raw records, unique records, websites, emails, and duplicates.

- Average records per minute, runtime, ETA, CPU, memory, browser processes, active pages, proxy performance, retries, warnings, and errors.

<a id="sec07"></a>
## 07 Live Job Monitor

*Show what the scraper is doing instead of only displaying “working”.*

### Pipeline view

| **Stage**           | **Displayed information**                                                     |
|---------------------|-------------------------------------------------------------------------------|
| Preparing queries   | Keyword expansion, validation, duplicate removal, and generated search count. |
| Generating grid     | Cells created, cells excluded, geographic coverage, and task estimate.        |
| Searching Maps      | Current query, coordinates, cell, results found, speed, and block rate.       |
| Extracting details  | Listings opened, fields parsed, retries, and browser health.                  |
| Crawling websites   | Current domain, pages visited, HTTP status, and response time.                |
| Extracting contacts | Emails, phones, and social links discovered.                                  |
| Deduplicating       | Raw records, matches, merges, and conflicts.                                  |
| Saving/exporting    | Rows committed, output files, and storage usage.                              |

### Real-time controls and diagnostics

- Pause, resume, cancel, reduce/increase concurrency, change proxy pool, add runtime, retry current task, and download partial results.

- Show current keyword, location, cell, active proxy, browser count, pages, places per minute, CPU, RAM, database writes, website queue, and ETA.

- Use Server-Sent Events or WebSockets to update the page without polling delays.

### Human-readable logs

- Severity levels: information, warning, rate limit, proxy failure, browser failure, website timeout, parsing failure, duplicate, maximum runtime, and system error.

- Search, severity filters, auto-scroll control, copy details, download logs, and link errors to the affected query/cell/record.

<a id="sec08"></a>
## 08 Results Explorer

*Create a local business database rather than forcing users to work only in exported CSV files.*

### Data table capabilities

- Virtual scrolling for large datasets, pagination, full-text search, sorting, and column filters.

- Resize, reorder, hide, freeze, and group columns.

- Inline editing, multi-row selection, keyboard navigation, copy cells/rows, and saved table layouts.

- Table-only, map-only, and split table/map views.

- Saved views tied to filters, visible columns, sorting, and grouping.

### Core columns

| **Group**   | **Fields**                                                                                   |
|-------------|----------------------------------------------------------------------------------------------|
| Business    | Name, primary category, additional categories, description, claimed status, business status. |
| Location    | Full address, street, city, state, postal code, country, latitude, longitude, plus code.     |
| Contacts    | Phone, normalized phone, phone type, website, domain, emails, email type, email status.      |
| Social      | Facebook, Instagram, LinkedIn, X/Twitter, YouTube, TikTok, WhatsApp.                         |
| Reputation  | Rating, review count, ratings breakdown, user reviews, popular times.                        |
| Identifiers | Place ID, CID, Data ID, Maps URL, source query, source cell, input ID.                       |
| Quality     | Website status, response time, technology, quality score, confidence, last checked.          |
| Workflow    | Tags, notes, reviewed flag, scrape date, last update, change status.                         |

### Bulk actions

- Export, delete, tag, untag, mark reviewed, add to saved list, re-enrich, recheck website, recheck emails, merge duplicates, and open selected websites.

- Copy selected domains, emails, phone numbers, addresses, or Maps URLs.

### Record detail drawer

- Complete structured record, map location, source links, website preview or screenshot, social profiles, provenance, raw JSON, notes, tags, change history, and duplicate matches.

<a id="sec09"></a>
## 09 Advanced Filtering

*Provide a visual query builder for finding usable subsets quickly.*

### Filter operators

- AND, OR, and nested groups.

- Contains, does not contain, starts with, ends with, equals, not equal, empty, and not empty.

- Numeric minimum, maximum, between, greater than, and less than.

- Date ranges, boolean fields, category membership, and geographic radius/polygon filters.

### Example reusable views

- Businesses without websites.

- Businesses with an active website but no visible email.

- Highly rated businesses with low-quality websites.

- Businesses with phone but no website.

- Businesses with email and LinkedIn.

- Open listings with more than 50 reviews.

- New or changed businesses since the last scrape.

- Permanently closed listings.

<a id="sec10"></a>
## 10 Deduplication Engine

*Merge overlapping keyword and grid results without losing source history.*

### Exact matching keys

- Place ID.

- CID.

- Data ID.

- Normalized phone.

- Normalized website domain.

- Exact normalized address.

### Fuzzy and composite matching

- Similar normalized business name.

- Name + postal code or name + city.

- Name + geographic proximity.

- Similar address with coordinate proximity.

- Shared phone or domain with a modified display name.

### Duplicate review and merge

- Show raw count, unique count, duplicates detected, auto-merged count, and items requiring review.

- Side-by-side comparison of conflicting records.

- Keep both, merge, ignore match, or establish a permanent non-match rule.

- Choose preferred value by source confidence, recency, or completeness.

- Preserve all source queries, cells, timestamps, and historical values after merging.

<a id="sec11"></a>
## 11 Data Cleaning and Normalization

*Standardize records before filtering, scoring, and export.*

- Normalize phone numbers while preserving the original value.

- Canonicalize website URLs, strip tracking parameters, derive registrable domain, and normalize protocols.

- Lowercase and trim emails; remove duplicates and invalid syntax.

- Normalize business names, whitespace, punctuation, and common legal suffixes for matching.

- Parse full addresses into street, city, state, postal code, and country where possible.

- Normalize country/state labels and category names.

- Standardize social URLs and remove share/tracking variants.

- Convert rating and review counts into numeric fields.

- Use consistent missing-value handling.

- Flag suspicious placeholder values, malformed URLs, and mismatched domains.

<a id="sec12"></a>
## 12 Local Email Handling

*Extract and assess email addresses without requiring a paid verification API.*

### Extraction

- Visible email text, mailto links, contact/about pages, footer/header, and structured data.

- Simple de-obfuscation such as name \[at\] domain and name (at) domain.

- Record source page and extraction method for every address.

### Classification and local checks

- Syntax validation and domain normalization.

- DNS/MX existence checks.

- Generic role classification: info, sales, support, contact, admin, owner, billing, and careers.

- Personal-looking address classification using local heuristics.

- Disposable-domain detection using a locally maintained list.

- Relevance ranking when multiple emails are found.

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Accuracy limitation</strong></p>
<p>Mailbox-level verification cannot be guaranteed for free. SMTP probing is frequently blocked, can be misleading, and should be shown as a low-confidence signal rather than a definitive “valid mailbox” result.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="sec13"></a>
## 13 Website Analysis

*Turn every discovered website into a locally assessed record.*

### Availability and technical health

- Reachability, HTTP status, final URL, redirect chain, HTTPS state, certificate errors, and response time.

- Parked domain, coming-soon page, placeholder page, and inaccessible website detection.

- Homepage screenshot and optional error screenshot.

### Basic website quality audit

- Page title and meta description presence.

- Contact page, about page, visible phone, visible email, address, and social links.

- Mobile viewport tag, basic page-size measurement, broken internal links, mixed content, and old copyright year.

- Obvious template/default text and incomplete setup indicators.

### Technology detection

- WordPress, WooCommerce, Shopify, Wix, Squarespace, Webflow, Joomla, Drupal, Magento, React, Next.js, and common page builders.

- Google Analytics, Google Tag Manager, Meta Pixel, and other visible script signatures.

- Detection should be signature-based and show confidence rather than claiming certainty.

<a id="sec14"></a>
## 14 Business Quality Scoring

*Help users prioritize complete, reachable, or commercially relevant records.*

### Configurable score components

- Business is open.

- Active website and HTTPS.

- Phone number available.

- Email available and domain passes local checks.

- Social profiles available.

- Rating and review count thresholds.

- Listing completeness and data freshness.

- Website quality and response time.

### Explainable scoring

- Display total score from 0–100.

- Show each positive and negative contribution.

- Allow users to edit weights, thresholds, and exclusion rules.

- Store the scoring-rule version used so historical scores remain reproducible.

<a id="sec15"></a>
## 15 Field Provenance and Auditability

*Make each important value traceable to its source.*

- Source type: Google Maps, website homepage, contact page, about page, footer, structured data, or manual edit.

- Source URL, source query, source grid cell, extraction timestamp, extraction method, and confidence.

- Original value, normalized value, current preferred value, and previous values.

- Manual edits should record the operator, date, and reason when local user accounts are enabled.

- Exports may optionally include provenance columns or a companion JSON file.

<a id="sec16"></a>
## 16 Change Tracking and Incremental Scraping

*Compare repeat runs instead of creating disconnected snapshots.*

### Tracked changes

- New business discovered.

- Listing removed, closed, reopened, or status changed.

- Phone, website, address, category, rating, review count, opening hours, or email changed.

- Website became active/inactive or redirected to another domain.

- New social profile or contact information discovered.

### Incremental modes

- Collect only new listings.

- Collect new and changed listings.

- Recheck only fields likely to change.

- Re-enrich only businesses whose website or contact data is missing/stale.

- Retain configurable version history and show before/after comparisons.

<a id="sec17"></a>
## 17 Scheduling

*Run recurring jobs locally while the application or scheduler service remains active.*

- One-time, hourly, daily, weekly, monthly, and custom cron schedules.

- Selected days and times, timezone handling, and run-on-application-start option.

- Skip, queue, or replace when the previous run is still active.

- Automatic retries with retry limits and backoff.

- Incremental-only mode for recurring jobs.

- Automatic export or local webhook after completion.

- Retention rules for old runs, logs, screenshots, and exports.

- Missed-run handling after the machine was offline.

<a id="sec18"></a>
## 18 Saved Searches and Templates

*Make proven configurations reusable and portable.*

- Save complete configurations including keywords, geography, filters, enrichment, performance, proxy pool, and output settings.

- Duplicate, rename, tag, organize into folders, pin favourites, and add notes.

- Export and import templates as JSON.

- Parameterised templates such as one category applied to many cities.

- Track last run, use count, average result count, and average duration.

- Starter templates: businesses without websites, high-rated businesses, closed-business monitor, new local businesses, and website audit prospects.

<a id="sec19"></a>
## 19 Proxy Manager

*Replace the per-job proxy textarea with persistent pools and health data.*

### Import and organisation

- Paste proxies or upload TXT/CSV.

- Support HTTP, HTTPS, SOCKS5, authentication, and named pools.

- Mask credentials in the interface and logs.

- Assign pools to templates or individual jobs.

### Testing and health

- Connection success, Google access, response latency, exit IP, country, last success, failure count, block count, and usage count.

- Statuses: healthy, slow, rate-limited, blocked, authentication failed, offline, and cooling down.

### Rotation strategies

- Round robin, random, least recently used, lowest failure rate, fastest, sticky per query, and sticky per grid cell.

- Automatically disable repeated failures, cool down rate-limited proxies, retest disabled proxies, and cap tasks per proxy.

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Operational reality</strong></p>
<p>The manager can be implemented for free, but acquiring a large, reliable residential or mobile proxy pool is not necessarily free. The application should also support small jobs without proxies.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="sec20"></a>
## 20 Adaptive Performance

*Make the local scraper respond to blocking and resource pressure automatically.*

- Reduce concurrency when block/failure rate rises.

- Increase concurrency cautiously after a stable success window.

- Reduce browser count or pages per browser when RAM pressure rises.

- Pause new tasks when disk space becomes low.

- Retry failed pages with another proxy or a fresh browser context.

- Restart crashed browser processes automatically.

- Pause the job when all proxies fail and resume after recovery.

- Adjust website timeout using recent response history.

- Display every automatic change and the reason it occurred.

<a id="sec21"></a>
## 21 Checkpoints and Recovery

*Protect long-running local jobs against crashes, restarts, and timeouts.*

- Persist completed queries, grid cells, listing IDs, enrichment tasks, and deduplication state.

- Save checkpoints at configurable intervals and after each meaningful stage.

- Resume after application or computer restart.

- Detect abandoned “running” jobs at startup and offer recovery.

- Preserve partial CSV/database results and logs.

- Continue from last completed query or grid cell.

- Create local backups before database migrations.

- Expose recovery status and last checkpoint time in the UI.

<a id="sec22"></a>
## 22 Export Centre

*Create repeatable, configurable local exports instead of a single fixed CSV.*

### Formats

- CSV.

- XLSX.

- JSON.

- JSONL.

- SQLite.

- PostgreSQL insert/backup.

- MySQL/MariaDB-compatible SQL.

- Parquet.

- GeoJSON.

- KML.

- VCard.

- Plain text lists.

### Export builder

- Export all, selected, filtered, or saved-view records.

- Choose, rename, and reorder columns.

- Normalize and deduplicate before export.

- Split by city, category, job, or maximum row count.

- Include raw JSON, source data, provenance, or change history.

- Compress multiple files into ZIP.

### Export history

- File name, format, record count, source job/view, filters, date, size, checksum, download again, and delete.

- Save export presets for repeated delivery formats.

<a id="sec23"></a>
## 23 Local API

*Extend the existing API into full programmatic control of the local application.*

### Endpoint groups

| **Group** | **Capabilities**                                                                                            |
|-----------|-------------------------------------------------------------------------------------------------------------|
| Jobs      | Create, validate, start, pause, resume, cancel, delete, duplicate, status, progress, checkpoints, and logs. |
| Results   | List, search, filter, retrieve, edit, tag, deduplicate, enrich, and bulk actions.                           |
| Maps      | Saved areas, grid preview, cell status, and geographic result queries.                                      |
| Proxies   | Import, test, pool, enable/disable, health, and usage.                                                      |
| Schedules | Create, update, enable, disable, run now, and history.                                                      |
| Exports   | Create, status, list, download, repeat, and delete.                                                         |
| System    | Health, resource metrics, database statistics, version, maintenance, and diagnostics.                       |

### API experience

- OpenAPI/Swagger documentation.

- Examples in cURL, Python, JavaScript, and Go.

- Local API keys with read-only or full-access permissions.

- Request logs, configurable local rate limits, and secret masking.

- Server-Sent Events or WebSockets for live job progress.

<a id="sec24"></a>
## 24 Local Integrations

*Connect the application to self-hosted or user-controlled tools.*

- n8n self-hosted and Activepieces self-hosted through local webhooks or API calls.

- Local PostgreSQL, MySQL/MariaDB, or another SQLite database.

- File-system watch folder for completed exports.

- Run a local shell command or Python script after completion.

- Send result batches or completion events to a local webhook.

- Optional Google Sheets sync using the user’s own Google credentials and quotas.

- Custom plugin hooks for enrichment, validation, scoring, and export.

<a id="sec25"></a>
## 25 Optional Local AI

*Add natural-language assistance through locally hosted models rather than paid APIs.*

### Possible Ollama-powered features

- Generate keyword and category variations.

- Convert a natural-language request into a scrape configuration.

- Convert natural language into result filters.

- Classify businesses and website quality.

- Explain quality scores and duplicate matches.

- Summarize business descriptions or change history.

- Suggest missing cities, categories, or exclusion keywords.

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Optional module</strong></p>
<p>Local AI should remain removable and disabled by default. Model downloads consume disk space and inference performance depends heavily on available RAM, CPU, and GPU.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="sec26"></a>
## 26 Database and Storage

*Move from job CSV files alone to an indexed local data model.*

### Default database

- SQLite with WAL mode for safe concurrent reads/writes.

- FTS5 for fast search across names, categories, addresses, emails, domains, and notes.

- Batch inserts, indexed filters, integrity checks, VACUUM, migrations, backups, and retention policies.

- Optional local PostgreSQL for larger datasets or multiple local workers.

### Recommended tables

| **Table**                         | **Purpose**                                                                   |
|-----------------------------------|-------------------------------------------------------------------------------|
| jobs                              | Job configuration, state, statistics, timestamps, and version.                |
| job_tasks                         | Queries, grid cells, listing tasks, website tasks, attempts, and checkpoints. |
| businesses                        | Current preferred normalized business record.                                 |
| business_versions                 | Historical snapshots and changes.                                             |
| business_sources                  | Query, cell, Maps URL, website source, and provenance.                        |
| websites                          | Availability, metadata, technologies, screenshots, and audit results.         |
| emails / phones / social_profiles | Contact values, source, confidence, and status.                               |
| proxies / proxy_health            | Pools, credentials reference, tests, usage, failures, and cooldown.           |
| schedules                         | Cron expression, template, policy, and execution history.                     |
| exports                           | Preset, filters, files, counts, timestamps, and checksums.                    |
| tags / notes / audit_logs         | Local organisation, edits, and traceability.                                  |
| settings                          | Application defaults and local preferences.                                   |

### Storage directories

- Database, exports, screenshots, logs, cache/browser profiles, backups, and temporary files should be separate and configurable.

- Display size and retention settings for each directory.

<a id="sec27"></a>
## 27 System and Diagnostics

*Give operators visibility into the local environment.*

### System information

- Application, scraper, database, Go, and browser versions.

- CPU, RAM, disk, database size, queue length, active browsers/pages, running jobs, log size, screenshot storage, and export storage.

- Worker heartbeat, last successful browser launch, last database write, and proxy-pool status.

### Maintenance actions

- Restart worker, stop all jobs, clear cache, clean old screenshots/exports/logs, VACUUM database, integrity check, create backup, restore backup, export diagnostics, check for updates, and run self-test.

### Self-test checks

- Database writable.

- Output directories writable.

- Browser can launch.

- Internet reachable.

- Maps page reachable.

- Proxy credentials accepted.

- Sufficient disk and memory.

- Scheduled worker active.

<a id="sec28"></a>
## 28 Settings and Preferences

*Centralize defaults so new jobs start with sensible values.*

### Scraping defaults

- Language, location, zoom, depth, runtime, concurrency, browser-pool size, pages per browser, enrichment, reviews, browser visibility, and proxy pool.

### Storage and retention

- Data, export, screenshot, log, backup, and temporary directories.

- Maximum storage, automatic cleanup, number of backups, and record/version retention.

### Privacy and appearance

- Disable telemetry; redact secrets from logs; clear browser profiles; encrypt sensitive settings.

- Light/dark/system mode, compact table, sidebar default, date/time format, number format, language, reduced motion, and font size.

<a id="sec29"></a>
## 29 Security and Privacy

*Keep a local tool safe if it is later exposed beyond the host machine.*

- Bind to 127.0.0.1 by default and warn clearly before binding to 0.0.0.0.

- Optional local login with strong password hashing and session timeout.

- CSRF protection, secure cookies, API-key protection, and local rate limiting.

- Encrypt proxy passwords and sensitive settings at rest where practical.

- Mask credentials and tokens in the UI, diagnostics, logs, and exports.

- Validate upload types, file sizes, output paths, and archive extraction paths.

- Prevent arbitrary file reads/writes through API parameters.

- Audit important local actions and configuration changes.

- Offer encrypted backups and a privacy-scrubbed diagnostics bundle.

<a id="sec30"></a>
## 30 UI, Accessibility and Onboarding

*Make the application polished, understandable, and usable by keyboard and assistive technology.*

### Visual design

- Clean sidebar, sticky header, wide operational tables, restrained cards, consistent icons, and clear hierarchy.

- Status colours plus icons/text so state is never communicated by colour alone.

- Progress bars, skeleton loaders, empty states, tooltips, inline validation, and actionable error messages.

| **State** | **Suggested presentation** |
|-----------|----------------------------|
| Draft     | Grey                       |
| Queued    | Slate                      |
| Running   | Blue                       |
| Paused    | Purple                     |
| Completed | Green                      |
| Partial   | Amber                      |
| Failed    | Red                        |
| Cancelled | Dark grey                  |

### Keyboard and accessibility

- Keyboard navigation, command palette, visible focus, skip links, labelled forms, ARIA live regions for progress, and screen-reader-friendly errors.

- High contrast, scalable text, reduced motion, logical tab order, and accessible tables/dialogs.

- Suggested shortcuts: N new scrape, J jobs, R results, / search, P pause current job, Esc close panel, Ctrl/Cmd+E export.

### Help and first-run experience

- Setup wizard that checks browser, database, data directory, internet access, disk capacity, and optional proxies.

- Guided sample job and contextual explanations for depth, zoom, radius, grid cells, concurrency, runtime, proxies, and email crawling.

- Embedded troubleshooting, export guide, API examples, and links to local logs/diagnostics.

<a id="sec31"></a>
## 31 Recommended Technology Stack

*Use lightweight open-source components that fit the existing Go application.*

| **Area**            | **Recommendation**                           | **Why**                                                              |
|---------------------|----------------------------------------------|----------------------------------------------------------------------|
| Backend             | Existing Go backend                          | Retains current scraper engine and avoids a full rewrite.            |
| Server-rendered UI  | Go templates + HTMX                          | Small local footprint and straightforward integration.               |
| Client-side helpers | Alpine.js                                    | Lightweight state for modals, forms, and local interactions.         |
| Styling             | Tailwind CSS or a small custom design system | Fast, consistent local UI without a heavy SPA requirement.           |
| Data table          | Tabulator                                    | Virtual scrolling, editing, filtering, grouping, and export support. |
| Maps                | Leaflet + OpenStreetMap                      | Open-source map interface and drawing ecosystem.                     |
| Charts              | Apache ECharts                               | Rich dashboards and large-data performance.                          |
| Database            | SQLite + FTS5                                | Simple local deployment with strong search and indexing.             |
| Large local DB      | PostgreSQL                                   | Optional scale and multi-worker coordination.                        |
| Scheduling          | robfig/cron                                  | Mature local cron support for Go.                                    |
| XLSX export         | Excelize                                     | Native Go spreadsheet generation.                                    |
| Local AI            | Ollama                                       | Optional local inference without recurring API charges.              |
| Packaging           | Docker Compose                               | One-command local app, database, and optional services.              |
| Logging             | Go slog or Zerolog                           | Structured logs with efficient local storage.                        |
| API docs            | OpenAPI / Swagger                            | Browsable contract and generated examples.                           |

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Architecture recommendation</strong></p>
<p>A React or Next.js rewrite is not required. The application can become highly capable using the existing Go server, HTMX, Alpine.js, a modern table component, and a small design system.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="sec32"></a>
## 32 Implementation Roadmap

*Deliver value in stages while preserving a usable application at every release.*

### Release 1 — Professional local scraper

1.  Create the navigation shell, dashboard, and consistent design system.

2.  Implement the multi-step New Scrape Wizard and expose existing advanced settings.

3.  Move jobs and results into SQLite with migrations and indexes.

4.  Build job detail, progress, logs, pause/resume/cancel, partial download, and checkpoint recovery.

5.  Build the Results Explorer with search, filters, saved views, inline detail drawer, and CSV/XLSX/JSON exports.

6.  Implement normalization and exact/fuzzy deduplication.

7.  Add system health, storage usage, backups, and settings.

### Release 2 — Advanced local collection

8.  Add interactive map drawing, bounding boxes, grid preview, and live coverage states.

9.  Add persistent proxy pools, testing, rotation, and health management.

10. Add website/email/social enrichment and website-status analysis.

11. Add saved templates, schedules, incremental runs, and change tracking.

12. Add quality scoring, provenance, and export presets.

13. Expand the REST API and add local integration hooks.

### Release 3 — Best-in-class local edition

14. Add adaptive concurrency, browser recovery, proxy cooldown, and low-resource safeguards.

15. Add coverage heatmaps, missing-area retry, and selected-cell re-scraping.

16. Add advanced version history and field-level confidence.

17. Add optional local AI through Ollama.

18. Add plugin interfaces, complete diagnostics, accessibility polish, and advanced retention controls.

### Recommended first four screens

| **Priority** | **Screen**        | **Reason**                                       |
|--------------|-------------------|--------------------------------------------------|
| 1            | New Scrape Wizard | Makes advanced configuration approachable.       |
| 2            | Live Job Monitor  | Creates trust and control during long jobs.      |
| 3            | Results Explorer  | Turns raw output into usable local data.         |
| 4            | Proxy Manager     | Supports repeated and heavier collection safely. |

<a id="sec33"></a>
## 33 Acceptance Criteria and Limitations

*Define when the improved application is truly usable and where external constraints remain.*

### Release 1 acceptance criteria

- A user can configure and start a job without using CLI flags.

- The UI shows meaningful progress, current stage, records, errors, resources, and ETA.

- Jobs can be paused, resumed, cancelled, recovered after restart, and partially exported.

- Results persist in a searchable local database and can be filtered and exported.

- Duplicate records are detected using stable IDs and normalized fallback keys.

- Secrets are masked and the server binds to localhost by default.

- Database backup and restore are available from the UI.

### Performance and reliability criteria

- No loss of committed records after process interruption.

- Large tables remain responsive using virtualisation and indexed queries.

- Browser crashes do not automatically fail the entire job.

- Timeout completion is labelled Partial rather than Completed.

- Every exported record can retain source query and scrape timestamp.

### Not reliably free in practice

- Large, stable residential/mobile proxy networks.

- CAPTCHA-solving services.

- High-confidence mailbox verification.

- Commercial company/person databases and phone enrichment.

- Cloud-hosted workers and remote storage.

- Paid geocoding, SERP, or Maps APIs.

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Compliance note</strong></p>
<p>The application should include a configurable rate policy, responsible-use notice, and clear reminder that operators are responsible for complying with applicable terms, laws, privacy obligations, and outreach rules.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>

<a id="appendix"></a>
## 34 Appendices

*Reference inventories for implementation planning.*

### Appendix A — Complete feature checklist

| **Category**         | **Included capabilities**                                                                                                                                                                                     |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Foundation           | Navigation shell • Dashboard • SQLite storage • Migrations • Settings • System health • Backups                                                                                                               |
| Scrape configuration | Keywords • Categories • Exclusions • CSV/TXT upload • Locations • Radius • Polygon • Bounding box • Grid • Field selection • Enrichment • Filters • Performance presets • Advanced settings • Review/estimate |
| Operations           | Queue • Pause/resume/cancel • Live pipeline • Logs • ETA • Resource monitoring • Partial download • Checkpoints • Recovery • Retry failures                                                                   |
| Data                 | Results table • Map view • Saved views • Bulk actions • Record drawer • Advanced filters • Normalization • Deduplication • Scoring • Provenance • Change history                                              |
| Enrichment           | Website reachability • HTTP/HTTPS • Redirects • Screenshots • Email extraction • MX checks • Social profiles • CMS/technology detection • Basic website audit                                                 |
| Automation           | Templates • Schedules • Incremental runs • Export presets • Webhooks • Post-run scripts • Local API                                                                                                           |
| Scale                | Proxy pools • Proxy testing • Rotation • Adaptive concurrency • Browser recovery • Low-resource safeguards                                                                                                    |
| Experience           | Dark mode • Keyboard shortcuts • Accessibility • Onboarding • Embedded help • Diagnostics                                                                                                                     |

### Appendix B — Suggested local directory layout

gmaps-local/
├── config/
├── data/
│ ├── app.db
│ ├── backups/
│ └── migrations/
├── exports/
├── screenshots/
├── logs/
├── browser-profiles/
├── cache/
├── plugins/
└── temp/

### Appendix C — Suggested API endpoint inventory

| **Endpoint**                   | **Purpose**                                              |
|--------------------------------|----------------------------------------------------------|
| POST /api/v1/jobs              | Create a validated draft or queued job.                  |
| POST /api/v1/jobs/{id}/start   | Start a draft or paused job.                             |
| POST /api/v1/jobs/{id}/pause   | Pause safely after checkpoint.                           |
| POST /api/v1/jobs/{id}/resume  | Resume from checkpoint.                                  |
| POST /api/v1/jobs/{id}/cancel  | Cancel and preserve partial data.                        |
| GET /api/v1/jobs/{id}/progress | Live counters, stage, ETA, and resources.                |
| GET /api/v1/jobs/{id}/events   | SSE/WebSocket progress stream.                           |
| GET /api/v1/results            | Search and filter local business records.                |
| POST /api/v1/results/export    | Create an export from a filter or selection.             |
| POST /api/v1/proxies/test      | Test selected proxies or pools.                          |
| GET /api/v1/system/health      | Application, database, browser, disk, and worker health. |
| POST /api/v1/system/backup     | Create local database/configuration backup.              |

### Appendix D — Suggested job configuration object

{
"name": "San Francisco dentists",
"queries": \["dentists"\],
"area": {"type": "polygon", "geojson": {}},
"grid_cell_km": 1.0,
"depth": 5,
"concurrency": 4,
"browser_pool_size": 2,
"pages_per_browser": 2,
"max_runtime": "60m",
"enrichment": {
"website": true,
"emails": true,
"social_profiles": true,
"max_pages": 5
},
"proxy_pool_id": null,
"checkpoint_interval": "30s",
"output": \["sqlite"\]
}

### Appendix E — Product completion definition

<table>
<colgroup>
<col style="width: 100%" />
</colgroup>
<thead>
<tr class="header">
<th><p><strong>Definition of “best local edition”</strong></p>
<p>The application is complete when a user can visually define where and what to scrape, launch and control resilient jobs, understand live progress, recover from interruptions, manage clean deduplicated results, audit every important field, monitor changes over time, and export or automate the data without depending on a paid hosted platform.</p></th>
</tr>
</thead>
<tbody>
</tbody>
</table>
