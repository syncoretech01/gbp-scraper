# Audit — wizard, map, incremental, scheduling, saved searches

Branch `spec/wizard-map-incremental`. Sections audited: 04 New Scrape Wizard,
05 Interactive Map Explorer, 16 Change Tracking and Incremental Scraping,
17 Scheduling, 18 Saved Searches and Templates.

Classes: **1** implemented-and-verified · **2** implemented-but-unverified (test
added, now verified) · **3** unfinished-but-feasible (built in this pass) ·
**4** intentionally-optional-or-deviated · **5** genuinely-infeasible.

Migration used: **none**. Everything durable reuses storage that already
exists — `JobData` JSON (fields, filters, parameters, template link), the
`schedules.configuration` JSON blob (incremental mode), the `settings` table
(category groups), and `json_extract` over `jobs.data` (template metrics). The
reserved version 16 was deliberately not taken: `validateMigrationMetadata`
requires a contiguous 1..N migration history, so a branch adding only 16 while
14 and 15 live on other branches would fail its own migration validation.

---

## 04 New Scrape Wizard

### Step 1 — Business search

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Single keyword, multiple keywords, and upstream-supported direct Google Maps URLs through the query input. | 1 | `web/app_scrapes.go:wizardKeywords`; `TestCreateScrapeFromWizardQueuesGridJob`. |
| Business category picker and reusable category groups. | 3 | **Built.** `web/categories.go` embeds a 130-entry Maps category vocabulary grouped into nine sectors, plus `ListCategoryGroups`/`SaveCategoryGroup`/`DeleteCategoryGroup`/`TouchCategoryGroupUse` persisted in the existing `settings` table. Routes `GET /api/v1/business-categories` and `GET|POST /api/v1/category-groups` (`web/job_collection_api.go`). UI is a searchable, sector-filtered chip picker in step 1 that fills the combination generator, hydrated by `app-wizard.js:loadCategoryCatalogue` and hidden when the endpoint does not answer. Test `TestScrapeFieldAndCategoryRoutesAreReachable`. |
| Include and exclude keywords. | 1 | `app-wizard.js:applyKeywordFilters`, step-1 disclosure. |
| Upload keywords from CSV or TXT; paste one query per line. | 1 | `web/app_scrapes.go:readKeywordUpload`. |
| Generate category × location combinations automatically. | 1 | `app-wizard.js:generateCombinations`. |
| Preview/count generated query lines before launch. | 1 | `app-wizard.js:updatePreview`, `[data-query-preview]`. |
| Detect and remove exact case-insensitive duplicates; fuzzy warning documented as unavailable. | 1 | `wizardKeywords` dedupe; asserted by `TestCreateScrapeFromWizardQueuesGridJob`. |
| Save keyword sets for reuse. | 1 | `web/keyword_sets.go`, `web/keyword_sets_test.go`. |

### Step 2 — Location and geographic scope

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Configure a location label and exact coordinates. | 1 | `parseWizardJob` lat/lon parsing; `TestCreateScrapeFromWizardQueuesGridJob`. |
| Select multiple cities or upload locations from a file. | 1 | `app-wizard.js:loadTextFile` + `[data-combo-locations-file]`. |
| Draw a circle, polygon, or bounding box on a map. | 1 | `web/maps.go:ParseMapGeometry`; `TestParseMapGeometrySupportsPolygonMultiPolygonCircleAndBBox`. |
| Set radius, zoom, bounding box, and grid-cell size with explicit mode semantics. | 1 | `JobData.Validate` in `web/job.go`; `web/job_test.go`. |
| Preview estimated grid-cell and task counts. | 2→1 | `web/app_read_pages.go:buildMapPage` sets `Estimate.Cells/Queries/Tasks`. **Test added:** `TestMapPageReportsCellQueryAndTaskEstimate` asserts tasks = queries × cells. |
| Remove individual cells or define excluded areas. | 2→1 | Already end to end: `app-map.js:removeSelectedCells` writes `excluded_cells` into the saved area's GeoJSON properties, `web/maps.go:MapGeometry.ExcludedCellIDs` validates them, and `runner/webrunner/checkpoint_runner.go:63-78` skips them at seed generation. **Test added:** `TestPlanningGridNumbersCellsDenselyFromOne` covers the deterministic identity exclusion depends on; existing `TestMapGeometryReturnsStableExcludedCells` covers the round trip. |
| Save geographic areas for future jobs. | 1 | `TestSavedAreaFlowsIntoNewScrapeSnapshot`. |

### Step 3 — Data fields

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Choose which Maps fields to retain, display, and export. | 3 | **Built.** `web/job_fields.go` is a catalogue of 27 fields, each checked against `gmaps.Entry.CsvHeaders` (read-only) and the normalized `businesses` table. `JobData.Fields` stores the selection; `NormalizeJobFieldKeys` validates it and stores nothing for a complete selection so an unchanged job stays byte-identical. The selection is made real by `Service.SaveJobFieldExportPreset`, which materialises it as a repeatable export profile built through the export builder's own validator (`exportProfileFromInput`). Route `GET /api/v1/scrape-fields`. **Honesty:** collection is never narrowed — Maps has no field mask and the per-job CSV schema is fixed — and both the UI notice and `JobCollectionPlan.Notices` say exactly that. Tests: `TestNormalizeJobFieldKeysValidatesAndCanonicalises`, `TestJobFieldExportColumnsFollowTheSelection`, `TestWizardFieldStepMatchesFieldCatalogue`, `TestCreateScrapeFromWizardStoresFieldSelectionAndFilters`, `TestCreateScrapeFromWizardKeepsEveryFieldByDefault`. |
| Core details: name, category, address, phone, website, domain, coordinates, rating, reviews, business status. | 3 | **Built.** All eleven are catalogue entries in group `core`, every one backed by a normalized column and a dedicated export column. `name` is `Required` and cannot be deselected. |
| Identifiers: Place ID, CID, Data ID, input ID, source query, source grid cell. | 3 | **Built.** Group `identifiers`. Five have dedicated export columns; `input_id` is honestly marked `JobFieldStorageRecord` because the export builder has no `input_id` column, and the UI says so rather than implying one exists. |
| Extended details: opening hours, popular times, descriptions, price range, images, reservations, ordering links, menus, owner information, reviews. | 3 | **Built.** Group `extended`, all ten present and all genuinely captured by the engine (verified against `gmaps/entry.go:CsvHeaders`). Four are normalized columns (`open_hours`, `popular_times`, `description`, `price_range`), six live in `businesses.raw_json`. Selecting any of them adds the `raw_json` export column instead of inventing per-field columns; the step and the catalogue's `storage`/`note` fields state which is which. |

### Step 4 — Local enrichment

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Visit the business website and detect active/inactive status. | 1 | `web/website_enrichment.go`, `web/website_enrichment_test.go`. |
| Extract emails, visible phone numbers, contact pages, about pages, social links. | 1 | `web/enrichment/`, `web/sqlite/enrichment.go`. |
| Collect page title, meta description, language, CMS, analytics, SSL, HTTP status, redirect chain, response time. | 1 | `WebsiteView` in `web/results.go`; enrichment tests. |
| Choose crawl scope: homepage / +contact / +about / max page count. | 1 | `enrichment_scope` + `enrichment_max_pages`. |
| Set page timeout, URL patterns, and whether to save screenshots on errors. | 4 | Page timeout ships (`enrichment_timeout_seconds`). Screenshots: the homepage screenshot capture that already existed in `EnrichmentOptions.CaptureScreenshot` was unreachable from the wizard and is now exposed (`enrichment_capture_screenshot`); **error-page** screenshots are not captured because the enrichment crawler is an HTTP client, not a browser session, so there is no failed page to photograph. URL include/exclude patterns are deliberately not offered: the shipped equivalent is the bounded scope selector plus `enrichment_max_pages` and `enrichment_internal_links`, which is a targeting model that cannot be made to walk an unbounded site, and the crawler's page-selection heuristics live in `web/enrichment/` outside this group's ownership. |

### Step 5 — Filters

All five lines: **class 3, built.** `web/job_filters.go` defines `JobResultFilters`
(rating range, review-count range, included/excluded categories, business status,
claimed/unclaimed, name contains / does not contain), stored on `JobData`,
validated in `JobData.Validate`, and translated into the existing bounded
result-filter language by `FilterGroup()` / `ResultsQuery()`. `Service.SaveJobCollectionView`
materialises them as a saved result view bound to the job, so the choice is
reachable from Results and Saved searches without retyping.

**Honesty:** `JobResultFilterNotice` is the single sentence every surface repeats —
"Applied to stored results after collection. Google Maps returned every listing
the plan reached and the per-job CSV keeps them all." The step-5 panel carries it
as a `notice-warning`, and `TestWizardFilterStepStatesFiltersArePostCollection`
fails if that sentence is removed from the markup.

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Rating and review-count ranges. | 3 | `appendJobNumericFilter`, `appendJobReviewFilter` → `rating`/`review_count` numeric filters. |
| Included and excluded business categories. | 3 | `jobCategoryGroup` matches primary category OR `category_member`; exclusion negates the group. |
| Open, temporarily closed, permanently closed status. | 3 | OR group over `business_status`. |
| Claimed or unclaimed listing status where available. | 3 | `claimed` boolean filter; nil never filters, and the UI states unknown ownership is excluded once narrowed. |
| Business name contains/does not contain. | 3 | `name` `contains` / `not_contains`. |
| Post-scrape filters for website, email, phone, social, city, ZIP, website status, quality score. | 1 | Existing Results filter vocabulary (`web/sqlite/results.go` column map). |

Tests: `TestJobResultFiltersRejectImpossibleBoundsAndBuildFilterGroups`,
`TestJobCollectionPlanResultsURLIsAValidResultQuery` (the generated URL is parsed
back through the very `parseResultSearch` the Results page uses),
`TestCreateScrapeFromWizardStoresFieldSelectionAndFilters`,
`TestCreateScrapeFromWizardRejectsImpossibleFilters`.

### Step 6 — Performance and browser settings

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Fast / Balanced / Deep presets. | 1 | `app-wizard.js:applyPerformancePreset`; preset radios in step 6. |
| Advanced settings (depth, concurrency, pools, runtime, records, retries, delays, fast mode, extra reviews, headfull, proxy pool). | 1 | `parseWizardJob`; `JobData.Validate`; `web/job_test.go`. |
| Resource controls: disable images, fonts, or video where safe; cap memory usage; save failure screenshots; pause on low disk space. | 5 | Two of the four ship and are verified: image blocking (`JobData.LoadImages` → `scrapemateapp.DisableImages` in `runner/webrunner/webrunner.go:690`) and the low-disk pause (`JobData.LowDiskBytes` → `StopReasonLowDisk`, `TestCheckpointedJobPausesBeforeLowDiskAndResumesOnlyPendingTask`). The other two are blocked by a named boundary: `scrapemate v1.3.0`'s browser configuration surface is exactly `Headfull`, `DisableImages`, `WithUA`, `WithRodStealth` (`scrapemateapp/config.go:179-202`) — there is no request-routing hook to block fonts or media, no per-browser memory ceiling, and no failure-screenshot callback. Reaching them would mean changing `gmaps/` (read-only by constraint) or vendoring a fork of the browser layer. The adaptive safeguard (`JobData.Adaptive`) is the shipped response to memory pressure: it reduces concurrency under load and records the reason, rather than enforcing a hard cap. |

### Step 7 — Review and estimate

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Summarize keywords, location/coverage, queries, cells, tasks, enrichment, proxy pool, output, runtime. | 1 | `app-wizard.js:updateReview`. |
| Display implemented warnings. | 1 | `[data-estimate-warning]` in `updateReview`. |
| Save configuration as a reusable template before starting. | 1 | `save_template` in `createScrapeFromWizard`. Fixed in this pass: the stored configuration now clears `TemplateID` so a new template cannot inherit another template's run history. |

A "Retained set" card was added to step 7 mirroring `JobCollectionPlan` (fields,
filters, rescan mode) with the post-collection notice, so the wizard promises
exactly what `GET /api/v1/jobs/{id}/collection-plan` will report.

---

## 05 Interactive Map Explorer

### Planning mode

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Leaflet map with OpenStreetMap tiles. | 1 | Vendored Leaflet + `/api/v1/maps/tiles/{z}/{x}/{y}`; `TestMapTileRouteValidatesCoordinatesAndServesLocalCache`. |
| Draw circles, polygons, and bounding boxes. | 1 | `TestParseMapGeometrySupportsPolygonMultiPolygonCircleAndBBox`. |
| Preview search grids, cell numbering, estimated queries, expected task count. | 2→1 | `MapGridCell.Number` (`web/maps.go:newMapGridCell`), rail estimate tiles, `app-map.js` cell tooltip renders `"Cell " + cell.number`. **Tests added:** `TestPlanningGridNumbersCellsDenselyFromOne`, `TestMapPageReportsCellQueryAndTaskEstimate`. |
| Select, remove, resize, or group cells. | 2→1 | Select: `app-map.js` cell click toggles `selectedCells`. Remove: `removeSelectedCells`/`restoreRemovedCells` → durable `excluded_cells`. Resize: the cell-size input re-previews the same area deterministically (asserted in `TestPlanningGridNumbersCellsDenselyFromOne`). Group: `queueSelectedCells("template")` queues one saved keyword group against a chosen cell set. Individual cells are not free-form editable — the grid is derived deterministically from one cell size so a cell ID stays stable across previews, which is what makes exclusion and rescrape resumable. |
| Assign different keyword groups to different areas. | 2→1 | `POST /api/v1/maps/cells/rescrape` with `action:"template"` clones the source job restricted to the selected cells with the group's keywords (`web/map_operations.go:createMapCellScrape`). One job still carries one keyword set; assigning different groups to different areas is done by queueing one job per area, which keeps per-job CSV and resume semantics intact. **Test added:** `TestMapExplorerShipsClusteringPopupsAndCellRescrapeControls` asserts the control exists and is gated on repository support; existing `TestMapResultAreaExportAndSelectedCellKeywordJob` covers the backend. |
| Import or export geographic definitions as GeoJSON. | 1 | `TestMapRoutesCRUDImportPreviewExportAndSpatialResults`. |

### Live coverage mode

Grey/Blue/Amber/Purple were already ticked; Green and Red were not, but all six
resolve from the same function.

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Grey: waiting or not searched. | 1 | `mapCellCoverageState` default branch. |
| Blue: currently running. | 1 | `RunningTasks > 0`. |
| Green: completed successfully. | 2→1 | `CompletedTasks >= TaskCount`. **Test added:** `TestLiveCoverageResolvesCompletedAndFailedCellStates`. |
| Amber: completed with partial results or warnings. | 1 | `TestPreviewMapCoverageProjectsDurableStatesAndHeatmapEvidence`. |
| Red: failed or blocked. | 2→1 | `FailedTasks`/`BlockedTasks` with no completions. **Test added:** same test, including the rail summary counters. |
| Purple: paused. | 1 | Paused job state with unfinished tasks. |

### Results mode

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Marker clustering for large datasets. | 2→1 | Vendored `leaflet.markercluster`; `app-map.js:1325` uses `markerClusterGroup({chunkedLoading:true})`. **Test added:** `TestMapExplorerShipsClusteringPopupsAndCellRescrapeControls`, which also fails on any remote-host reference. |
| Business popup with name, category, rating, reviews, website status, email, phone, links. | 2→1 | `app-map.js:resultPopup`. **Test added:** same test asserts every named field. |
| Heatmaps for density, failed, empty, duplicate-heavy cells. | 1 | `web/map_heatmap_test.go`, `web/map_duplicates_test.go`. |
| Filter markers using the same rules as the Results Explorer. | 1 | `apiMapResults` reuses `parseResultSearch`. |
| Export businesses inside a drawn area. | 1 | `TestMapResultAreaExportAndSelectedCellKeywordJob`. |
| Retry selected failed/empty cells or run a new keyword only in selected cells. | 2→1 | `POST /api/v1/maps/cells/rescrape` with `action:"retry"` (validated against real failed/blocked/empty evidence) or `"keyword"`. **Test added:** same test asserts the controls; existing map test covers the handler. |

---

## 16 Change Tracking and Incremental Scraping

### Tracked changes

| Checklist line | Class | Evidence |
| --- | --- | --- |
| New business discovered. | 2→1 | `job_businesses.is_new` and the first `business_versions` row with `change_type = 'new'`. **Test added:** `TestImportRecordsDiscoveryAndFieldLevelChanges` asserts both, and `TestIncrementalLineageFiltersResolveAgainstStoredEvidence` proves the `first_seen_job` predicate finds it. |
| Listing removed, closed, reopened, or status changed. | 1 | `flagDisappearedBusinesses` / `restoreReappearedBusinesses`; `TestRescanFlagsDisappearedAndRestoresReappearedBusinesses`. |
| Phone, website, address, category, rating, review count, opening hours, or email changed. | 2→1 | `recordBusinessVersion` diffs the normalized snapshot into per-field `business_changes` rows. **Test added:** `TestImportRecordsDiscoveryAndFieldLevelChanges` moves all eight and asserts the recorded `field_name` values. **Defect found and fixed:** the incremental volatile-field mode had been written against display labels (`phone`, `primary_category`, `open_hours`, `business_status`) that the workspace never writes; corrected to the real snapshot keys (`phones`, `category`, `structured`, `status`, …) in `web/job_collection.go:volatileBusinessFields`. |
| Website became active/inactive or redirected to another domain. | 1 | `web/sqlite/enrichment.go:1120-1148` writes `website_status` and `website_final_url` change rows with kind `updated`/`discovered`. |
| New social profile or contact information discovered. | 1 | `insertEnrichmentDiscovery` writes `email`, `phone`, and `social_profile` rows with kind `discovered` (`web/sqlite/enrichment.go:886/948/991`). |

### Incremental modes

The two stored modes existed but were **inert**: `JobData.IncrementalMode` was
written and then only echoed into the import summary event. All four modes are
now real.

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Collect only new listings. | 3 | **Built.** `incrementalLineageGroup` resolves `new_only` to `first_seen_job = <job>` in the existing discovery-history filter language, materialised as the job's saved result view. Honest notice: Maps has no "only new" query, the plan is still executed in full, and detection happens at import. Tests: `TestBuildJobCollectionPlanExpressesRescanModesAsLineageFilters`, `TestIncrementalLineageFiltersResolveAgainstStoredEvidence`. |
| Collect new and changed listings. | 3 | **Built.** `new_changed` → `first_seen_job OR changed_by_job`. Same tests. |
| Recheck only fields likely to change. | 3 | **Built** as `volatile_fields`: `changed_by_job AND (changed_field ∈ volatileBusinessFields)`. Honest notice: Maps has no partial-record fetch, so the full listing is still collected and the mode narrows what counts as a change. |
| Re-enrich only businesses whose website or contact data is missing/stale. | 3 | **Built** as `stale_contacts`, and it is the one mode that genuinely changes work done, because the website audit is local. `JobEnrichmentOptions.StaleAfterHours`/`ForceReaudit` are new and flow through `EnrichmentOptionsForJob` into `queueEnrichmentCandidates`, which already skips businesses audited inside the window. An unset window keeps the historical 24 hours exactly. The mode also forces `ForceReaudit` off, since forcing would be its opposite. Test: `TestStaleContactsModeNarrowsTheLocalAudit`. |
| Retain configurable version history and show before/after comparisons. | 1 | `business_versions` + `web/retention.go`; `web/retention_test.go`. |

---

## 17 Scheduling

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| One-time, hourly, daily, weekly, monthly, bounded five-field cron. | 1 | `NextScheduleTime`; `web/schedules_test.go`. |
| Selected days and times with IANA timezone handling. | 1 | `web/schedules_test.go`. |
| Skip, queue, or replace when the previous run is still active. | 1 | `web/sqlite/schedules_automation_test.go`. |
| Automatic retries with retry limits and backoff. | 1 | `ScheduleRetryAllowed`/`ScheduleRetryDelay`; automation tests. |
| Incremental-only mode for recurring jobs. | 3 | **Built.** `ScheduleSpec.IncrementalMode` rides inside the `schedules.configuration` JSON that already stores the spec, so no schema change. `createScheduleJob` stamps it onto every run, overriding the template's own mode; an empty value leaves the template untouched. Exposed on the schedules page (create and edit forms), in the JSON API (`incremental_mode` on `scheduleUpdateRequest` and `scheduleAPIView`, where an explicit empty string clears it), and validated by `ValidIncrementalMode`. Test: `TestScheduleStampsIncrementalModeOnEveryRun`. |
| Automatic export or local webhook after completion. | 1 | `buildScheduleAutoExport`; automation tests. |
| Retention rules for old runs, logs, screenshots, and exports. | 3 | **Built.** Retention previously deleted only the `schedule_runs` row. `PruneScheduleRuns` now also deletes the `job_events` log of the expired runs' jobs, in one transaction and log-first so the pass stays idempotent; `ExpiredScheduleRunExports` reports the completed exports of those jobs and `Service.applyScheduleRetention` deletes them through the ordinary `DeleteExport` path so the row and the file go together. **Never removed:** the job, its runtime counters, its normalized results, or its per-job CSV. Screenshots are attached to businesses rather than runs, so deleting them with a run would destroy data about a business that still exists; they remain governed by the workspace storage cap (`web/retention.go`) and the System maintenance `screenshots` cleanup, which is the deliberate equivalent. Test: `TestScheduleRetentionPrunesRunLogsAndReportsExports`, including a second no-op pass. |
| Missed-run queue-one or skip handling after the machine was offline. | 1 | `validScheduleMissedPolicy`; `web/schedules_test.go`. |

---

## 18 Saved Searches and Templates

| Checklist line | Class | Evidence |
| --- | --- | --- |
| Save complete implemented job configurations. | 1 | `save_template` path in `createScrapeFromWizard`. |
| Duplicate, rename, tag, folders, pin, notes. | 1 | `web/app_reusable.go`, `web/template_rename_test.go`. |
| Export and import validated templates as JSON without inline proxy credentials. | 1 | `web/app_reusable.go` import/export; existing tests. |
| Parameterised templates such as one category applied to many cities. | 3 | **Built.** `web/job_parameters.go` stores a category set × location set and a bounded `{category}`/`{location}` pattern on `JobData.Parameters`. Expansion happens at **job-creation** time (`ApplyJobParameters`), not at save time, so widening a template's location list changes every future run without editing query text. Wired into both the wizard (`parameter_*` fields, with a live preview through `POST /api/v1/templates/parameters/preview`) and the scheduler (`createScheduleJob`). Bounded at 500 values per list and 5,000 expanded queries. Tests: `TestCreateScrapeFromWizardExpandsParameters`, `TestScheduleExpandsParameterisedTemplateOnEveryRun` (which also asserts the widened template expands to six queries on the next run). |
| Track last run, use count, average result count, and average duration. | 3 | **Built.** Last run and use count already existed. Average result count and average duration needed a job→template link, which is now `JobData.TemplateID`, stamped by the wizard and the scheduler. `repo.ScrapeTemplateMetrics` derives run count, average distinct businesses per run, average wall duration, and last run via `json_extract(jobs.data, '$.template_id')` joined to `job_runtime` — no migration. Served by `GET /api/v1/templates/{id}/metrics` and rendered on the saved-searches card. A template with no recorded run says so instead of inventing an average. Test: `TestScrapeTemplateMetricsDeriveRunHistoryFromJobs`, including that another template's jobs do not contaminate the average. |
| Starter templates. | 1 | `web/starter_content_test.go`. |

---

## Constraint check

- `gmaps/`, `exiter/`, `deduper/`: untouched.
- CLI flags and run modes: untouched.
- REST: additive routes only; no existing route or response shape changed.
- Legacy job statuses and nanosecond-integer durations: preserved
  (`ScrapeTemplateMetrics.AverageDuration` is a `time.Duration`, serialized as
  a nanosecond integer like every other duration in this API).
- Per-job CSV schema: unchanged. Every new narrowing feature is explicitly
  documented as post-collection for exactly this reason.
- Resumability / idempotency / restart safety: an empty field selection, filter
  set, parameter set, or schedule incremental mode stores nothing, so an
  untouched job serializes identically to one created before this pass. The
  retention pass is idempotent and asserted as such.
- Standalone/local/free: the category vocabulary is embedded in the binary, all
  new UI is vendored-asset only, `addEventListener` only, no framework, no npm,
  no runtime CDN.
