# Audit — 03 Dashboard, 06 Job Management, 07 Live Job Monitor

Branch `spec/dashboard-jobs-monitor`. Reserved migration version **14**, used
(`job-ownership-labels`).

Classes: **1** implemented and verified · **2** implemented, test added ·
**3** unfinished, built in this pass · **4** deliberate equivalent ·
**5** genuinely infeasible.

Counts: **1** = 19 · **2** = 3 · **3** = 20 · **4** = 2 · **5** = 0.
Newly implemented in this pass: the 20 class-3 items; tests added for the 3
class-2 items.

---

## 03 Dashboard

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 3 | Every sub-item below is now class 1 or 2; `web/dashboard_spec_test.go` covers the five that were previously unrendered. |
| Total raw records and total unique businesses. | 1 | `web/app_pages.go:buildDashboard` → `dashboardMetrics.RawRecords/UniqueBusinesses`; `TestDashboardPageRendersRealLegacyResultMetrics`. |
| Businesses collected today, this week, and this month. | 1 | `web/sqlite/dashboard_analytics.go:DashboardAnalytics` summary row; `TestDashboardAnalyticsAggregatesSeededResults`. |
| Running, queued, paused, completed, partial, failed, and cancelled jobs. | 1 | `buildDashboard` state switch → KPI strip in `pages/dashboard.html`; `TestDashboardPageRendersRealLegacyResultMetrics`. |
| Websites, phone numbers, emails, and social profiles discovered. | 3 | Counts were computed but only percentages reached the page. Availability meters now carry `Count`/`Total` (`dashboardAvailability`) and render `data-availability="…"` rows; `TestDashboardAvailabilityCountsMatchTheDiscoveredTotals`. |
| Duplicate candidates detected and exact stable-identity records merged. | 1 | `ResultOverview` → `Metrics.Duplicates`/`DuplicateCandidates`, rendered in the KPI strip and the attention list. |
| Average places per minute and average job duration when recorded evidence exists. | 1 | `buildDashboard` runtime accumulation → `PlacesPerMinute`/`AverageDuration`, "not recorded" without evidence. |
| Proxy success rate, block rate, and number of healthy proxies. | 3 | Computed in `buildDashboard` but rendered nowhere. New "Proxy health" stat tile with `data-metric="proxy-success-rate"`, `proxy-block-rate`, `healthy-proxies`; `TestDashboardRendersDiscoveryProxyAndStorageMetrics`. |
| Local database size, export storage, screenshot storage, and remaining disk capacity. | 3 | Only database size and free disk were rendered. New "Workspace storage" table (`data-storage-breakdown`) covers database, exports, screenshots, logs, and remaining capacity; same test. |
| Results collected by date. | 1 | `analytics.CollectionByDate` → CSS column chart plus a screen-reader table in `pages/dashboard.html`. |
| Businesses by city, category, status, and rating band. | 1 | `dashboardCountPoints` breakdowns; `TestDashboardAnalyticsAggregatesSeededResults` asserts city, category, and rating labels. |
| Website, email, phone, and social-profile availability rates. | 2 | Meters already rendered; nothing proved it. `TestDashboardRendersDiscoveryProxyAndStorageMetrics` now asserts all four rows exist with their counts. |
| Job success and failure trends. | 2 | `dashboardJobTrends` already fed the "Job outcomes by day" table; `TestDashboardRendersJobTrendsSpeedAndBlockRate` now asserts it. |
| Scraping speed and block rate over time. | 3 | Only speed and a broad warning count existed; the query comment said a block rate could not be measured. `DashboardSpeedTrend` gains `BlockEvents` and `BlockRatePercent`, computed in `dashboardSpeedTrends` from refusal-typed events (`proxy-failure`, `rate-limit`, `blocked`, `captcha`) against that day's finished tasks; rendered as Blocks and Block rate columns; same test. |
| Proxy latency and reliability distribution. | 1 | `dashboardProxyLatencyBuckets` / `dashboardProxyPoolReliability` → "Operational telemetry"; `TestDashboardAnalyticsAggregatesSeededResults`. |
| Website-active versus website-inactive results. | 3 | Previously a single footer caption. Now `dashboardPageData.WebsiteStatus` renders reachable / unreachable / never-checked as its own meter group; `TestDashboardRendersWebsiteActiveVersusInactive` plus a total-conservation check. |
| Recent activity: job name, state, progress, stage, records, unique, emails, runtime, ETA. | 3 | Was ticked but the live table showed neither emails nor runtime. Both columns added to the "Running now" table in `pages/dashboard.html`. |
| Quick actions: open, pause, resume, stop, download partial results, retry failures, duplicate configuration. | 3 | Only open/pause/resume/stop existed. Added retry, partial-CSV download, and duplicate (`dashboardJob.CanDuplicate`); `TestDashboardRecentActivityOffersEveryQuickAction` asserts all seven. |

## 06 Job Management

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 3 | Closed by the three job-detail items below plus labels. |
| Draft / Queued / Starting / Running / Paused / Cancelling / Completed / Partial / Failed / Cancelled | 1 | `web/jobruntime/state.go` with `state_test.go`; CHECK constraint in migration 4; `web/sqlite/lifecycle_test.go`. |
| Start, pause, resume, cancel, delete, archive, rename, duplicate, and restart. | 1 | `registerLifecycleRoutes` + `registerJobOrganisationRoutes`; `TestAPIJobControlReturnsCanonicalDecision`, `TestAPIDuplicateJobCreatesDraftWithoutLeakingConfiguration`. |
| Add runtime, change concurrency, switch proxy pool, and retry failed tasks. | 1 | `web/live_controls_api.go` + the "Tune" control group in `pages/job_monitor.html`, each rendered only when the server says the worker can apply it. |
| Restart safely from committed output/retry files without replacing earlier results. | 1 | `runner/webrunner/result_files.go` merge path; `result_files_test.go`, `checkpoint_task_exit_test.go`. |
| Download committed partial CSV results at any time. | 1 | `/api/v1/jobs/{id}/download`; offered from jobs, monitor, and now the dashboard. |
| Apply tags, folders, notes, and ownership labels. | 3 | Folder and notes columns and the `tags`/`job_tags` tables existed with no writer, and there was no owner column. Migration **14** adds `job_runtime.owner_label` plus lookup indexes; `web/job_labels.go`, `web/job_labels_api.go` (`POST /api/v1/jobs/{id}/labels`), `web/sqlite/job_labels.go`, editor card on the monitor, labels and folder filter on the jobs table. Tests: `TestNormalizeJobLabels*`, `TestAPIJobLabels*`, `TestJobMonitorRendersTheLabelEditorWhenStorageExists`, `TestJobsPageShowsLabelsAndFiltersByFolder`, `TestJobLabelsRoundTripAndReplaceTagSet`, `TestAllJobLabelsGroupsEveryJobAndRejectsMissingJobs`. |
| Configuration snapshot and exact scraper version. | 3 | The snapshot existed; the version was the literal string "not recorded". `web/scraper_build_version.go:ScraperVersion` resolves a link-time override, then the module version, then the embedded VCS revision (`+dirty` when the tree was modified). `Service.ResetJobLiveControls` stamps it at run start, first-writer-wins, through `web/sqlite/job_scraper_version.go`. The monitor shows the recorded identity and the local build separately and never substitutes one for the other. Tests: `TestScraperVersionAlwaysReportsAnExactIdentity`, `TestDevelopmentVersionShortensRevisionAndMarksDirtyTrees`, `TestJobScraperVersionIsStampedOnceAndReadBack`, `TestJobScraperVersionBoundsLengthAndReportsMissingJob`, `TestJobMonitorRendersRealTimeDiagnosticsAndRecordedVersion`. |
| Queries, geographic cells, completed tasks, remaining tasks, raw records, unique records, websites, emails, and duplicates. | 3 | Task and record counters existed; queries and geographic cells did not. `web/sqlite/job_pipeline_facts.go` counts distinct planned queries and source cells; rendered as `data-job-queries` / `data-job-cells`. Tests: `TestJobPipelineFactsAggregatesPlanEventsAndBusinesses`, `TestJobMonitorRendersRealTimeDiagnosticsAndRecordedVersion`. |
| Average records per minute, runtime, ETA, CPU, memory, browser processes, active pages, proxy performance, retries, warnings, and errors. | 3 | Retries rendered a hardcoded `0` until a stream frame arrived, and warnings, errors, and any per-job proxy performance were absent. `applyJobDetailCounters` fills retries, warnings, errors, social profiles, and this job's own block rate from the durable evidence; the network tile now states the block rate. Same tests. |

## 07 Live Job Monitor

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 3 | Closed by the pipeline, diagnostics, and log work below. |
| **Preparing queries:** keyword expansion, validation, duplicate removal, generated search count. | 3 | `web/job_pipeline_view.go:preparingQueryMetrics`; `TestJobMonitorPipelineRendersEveryStageWithItsNamedMetrics`, `TestJobMonitorPipelineMetricsUseDurableEvidence`. |
| **Generating grid:** cells created, cells excluded, geographic coverage, task estimate. | 3 | `generatingGridMetrics` — cells from the task plan, exclusions from the saved-area GeoJSON, estimate from the plan total. Same tests. |
| **Searching Maps:** current query, coordinates, cell, results found, speed, block rate. | 3 | `searchingMapsMetrics`, block rate from `JobPipelineFacts.BlockRatePercent`. Same tests. |
| **Extracting details:** listings opened, fields parsed, retries, browser health. | 3 | `extractingDetailMetrics` — browser health derived from `browser-failure` events and the live browser count. Same tests. |
| **Crawling websites:** current domain, pages visited, HTTP status, response time. | 3 | `crawlingWebsiteMetrics` joins `websites` through `business_sources` for pages checked, last HTTP status, and average response time. Same tests. |
| **Extracting contacts:** emails, phones, and social links discovered. | 3 | `extractingContactMetrics`; social links come from `social_profiles` via the same join, which is the only place they are stored (the per-job CSV has no social column). Same tests. |
| **Deduplicating:** raw records, matches, merges, conflicts. | 3 | `deduplicatingMetrics` — matches from the committed CSV, merges from `businesses.merged_into_id` for this job, conflicts as the unresolved remainder. Same tests. |
| **Saving/exporting:** rows committed, output files, storage usage. | 3 | `savingExportingMetrics` from `ResultStats.Rows` and `FileSizeBytes`. Same tests. |
| Pause, resume, cancel, reduce/increase concurrency, change proxy pool, add runtime, retry current task, download partial results. | 1 | `web/live_controls_api.go`; the control strip renders only server-approved actions (`pages/job_monitor.html`). |
| Show current keyword, location, cell, active proxy, browser count, pages, places per minute, CPU, RAM, database writes, website queue, ETA. | 2 | All fields were bound but places/min and ETA lived only in the hero and nothing asserted the set. Both added to the "Current activity" card and `TestJobMonitorRendersRealTimeDiagnosticsAndRecordedVersion` now asserts every named field. |
| Durable Server-Sent Events with cursor replay, plus bounded progress fallback. | 1 | `web/sse.go` with `Last-Event-ID`/`after` replay; `TestAPIJobEventsReplaysDurableEvents`, `TestEventCursorPrefersQueryAndValidates`. Note: a real defect was fixed here — `app-monitor.js` recognised a log frame by an event name (`"log"`) the server never emits, so no streamed log line ever reached the viewer. |
| Severity levels: information, warning, rate limit, proxy failure, browser failure, website timeout, parsing failure, duplicate, maximum runtime, system error. | 3 | The filter offered ten levels while the handler compared against the three stored severities, so eight matched nothing. `web/job_log_levels.go:classifyJobLogLevel` derives the level from the worker event type, then the recorded stop reason, then message phrases, then severity; the same classification is attached to streamed events (`newJobEventDTO`). Tests: `TestJobLogLevelClassificationCoversEveryDeclaredLevel` (asserts every declared level is reachable), `TestMonitorLogsFilterBySeverityClassAndCarryTargets`, `TestJobMonitorLogToolbarOffersEverySeverityAndControl`, `TestStreamedJobEventCarriesLevelAndTarget`. |
| Search, severity filters, auto-scroll control, copy details, download logs, and link errors to the affected query/cell/record. | 3 | Search now covers the redacted context; the severity filter works; the follow-live preference survives a filter submit; a copy-details control was added with an honest clipboard-refusal message; `jobLogTarget` links a line to the business, query, cell, or task it names. Tests: `TestJobLogTargetLinksErrorsToTheAffectedItem`, `TestMonitorLogsFilterBySeverityClassAndCarryTargets`, `TestJobMonitorLogToolbarOffersEverySeverityAndControl`. |

---

## Class 4 — deliberate equivalents

- **"Active proxy" (07, diagnostics line).** The worker binds a proxy per task
  inside `runner/webrunner` and never publishes which one; `proxy_task_stats`
  is keyed by proxy, not by job, and adding a per-task publish would change
  worker behaviour. The monitor states the *effective route* instead — the pool
  the next task will draw from, its healthy count, and any pending live
  override — which is the actionable form of the same fact and never prints a
  credential. `web/app_read_pages.go:buildJobMonitorPage`.
- **"HTTP status" (07, crawling websites).** Only the latest status per website
  is stored (`websites.http_status`), not a per-page history, so the stage
  reports the most recent recorded status for this job's sites together with
  the pages-checked total. Storing a per-page status log belongs to the website
  analysis section, not to the monitor.

## Class 5 — infeasible

None. Every unchecked item in these three sections was reachable with local,
free, already-stored evidence.

## Notes for the integrator

- Migration **14** (`job-ownership-labels`) is additive and idempotent: one
  `ALTER TABLE job_runtime ADD COLUMN owner_label`, three
  `CREATE INDEX IF NOT EXISTS`. `currentSchemaVersion` moves 13 → 14.
- `DashboardSpeedTrend` gains two JSON fields and `JobEvent` frames on the SSE
  stream gain `level` and `target_url` through an embedding DTO. Both are
  additive; no existing field changed name, type, or position. Legacy job
  statuses and nanosecond `time.Duration` fields are untouched.
- One new route: `POST /api/v1/jobs/{id}/labels`.
- No change to `gmaps/`, `exiter/`, `deduper/`, `runner/`, CLI flags, run
  modes, or the per-job CSV schema.
