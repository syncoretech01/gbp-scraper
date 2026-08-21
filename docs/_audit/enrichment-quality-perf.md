# Audit — enrichment, quality, and performance

Branch `spec/enrichment-quality-perf`, base `c8679f4` (tag `productization-rc1`),
reserved migration version **17**.

Sections audited: 11 Data Cleaning and Normalization, 12 Local Email Handling,
13 Website Analysis (Availability and technical health, Basic website quality
audit, Technology detection), 14 Business Quality Scoring, 19 Proxy Manager
(Testing and health, Rotation strategies), 20 Adaptive Performance,
21 Checkpoints and Recovery.

Classes: **1** implemented-and-verified · **2** implemented-but-unverified (a
test was added in this pass, so it is now verified) · **3** unfinished, built in
this pass · **4** intentionally deviated · **5** genuinely infeasible.

## Counts

| Class | Count |
| --- | --- |
| 1 implemented-and-verified | 58 |
| 2 implemented-but-unverified (tests added this pass) | 2 |
| 3 unfinished-but-feasible (built this pass) | 9 |
| 4 intentionally optional or deviated | 0 |
| 5 genuinely infeasible | 0 |
| **Total checklist lines** | **69** |

Newly implemented in this pass: **9** (plus 2 class-2 items brought to verified).
No item in these sections is infeasible or deviated.

## 11 Data Cleaning and Normalization

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: every sub-item below is implemented and covered by `web/resultimport` tests. |
| Normalize phone numbers while preserving the original value. | 1 | `resultimport.NormalizePhone` keeps `Phone.Raw` beside `Normalized`/`MatchKey`; `TestPhoneAndEmailNormalizationAndDeduplication`. |
| Canonicalize website URLs, strip common tracking parameters, derive host domain, and normalize protocols; public-suffix limits are documented. | 1 | `resultimport.NormalizeURL` + `cleanQuery`/`isTrackingParameter`/`NormalizeDomain`; `TestURLCanonicalization`. |
| Lowercase and trim emails; remove duplicates and invalid syntax. | 1 | `resultimport.normalizeEmails` with `validEmailLocal`/`validEmailDomain`; `TestPhoneAndEmailNormalizationAndDeduplication`. |
| Normalize business names, whitespace, punctuation, Unicode width, and common legal suffixes for matching. | 1 | `resultimport.NormalizeName` + `legalSuffixLength`; `TestUnicodeNormalization`. |
| Parse full addresses into street, city, state, postal code, and country where possible. | 1 | `resultimport.parseAddress`/`parseAddressText`/`splitStatePostal`; `TestAddressTextCountryDetectionStaysConservative`. |
| Normalize country/state labels and category names. | 1 | `resultimport.normalizeCountry`, `normalizeState`, `NormalizeCategory` (used at `reader.go:NormalizedCategory`); `TestCountryNamesNormalizeToISOAlpha2`, `TestCanadianProvincesNormalizeLikeUSStates`, `TestUnicodeNormalization`. |
| Standardize social URLs and remove share/tracking variants. | 1 | `enrichment.standardizeSocialProfileURL` + `isSocialShareURL`; `TestSocialProfileURLsDropTrackingParametersAndFragments`. |
| Convert rating and review counts into numeric fields. | 1 | `resultimport.parseInteger`/`parseNumber` with range guards; `TestMalformedValuesAreRetainedAndWarned`. |
| Use consistent nullable database fields and display-safe missing-value handling. | 1 | Nullable scan helpers `nullIntPointer`/`nullTimePointer`/`nullIntegerBoolPointer` in `web/sqlite`; `isMissingValue` in `resultimport`; `TestMalformedValuesAreRetainedAndWarned`. |
| Flag suspicious placeholder values, malformed URLs, and mismatched domains. | **3 — built** | Placeholders and malformed URLs already warned (`isSuspiciousPhone`/`isSuspiciousWebsite`/`isSuspiciousEmail`, `IssueInvalidURL`). Mismatched domains were missing: added `IssueDomainMismatch` with `hasMismatchedEmailDomain`/`sharesRegistrableDomain` in `web/resultimport/reader.go`, ignoring consumer mailboxes and sub-domains. Tests `TestMismatchedEmailDomainsAreFlaggedButKept`, `TestSharesRegistrableDomainComparesBothDirections`. |

## 12 Local Email Handling

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: every sub-item below is implemented and covered by `web/enrichment/email_test.go` and `crawler_test.go`. |
| Visible email text, mailto links, contact/about pages, footer/header, and structured data. | 1 | `enrichment.extractEmails` (mailto, visible text, JSON-LD) plus contact/about crawling via `discoverSupportingPages`/`selectSupportingPages`; `TestCrawlerAnalyzesBoundedWebsiteAndContacts`. |
| Simple de-obfuscation such as name \[at\] domain and name (at) domain. | 1 | `enrichment.findDeobfuscatedEmails`; `TestCrawlerAnalyzesBoundedWebsiteAndContacts`. |
| Record source page and extraction method for every address. | 1 | `enrichment.Source{PageURL,PageKind,Method}` on every `Email.Sources`; persisted to `contact_evidence`; `TestDurableWebsiteEnrichmentQueueAuditAndEvidence`. |
| Syntax validation and domain normalization. | 1 | `enrichment.AnalyzeEmails` → `ValidSyntax`, normalized `Domain`; `TestAnalyzeEmailsClassifiesChecksAndRanksDeterministically`. |
| DNS/MX existence checks. | 1 | `enrichment.lookupMX` behind the opt-in `CheckMX`; `MXStatus` taxonomy; same test. |
| Generic role classification: info, sales, support, contact, admin, owner, billing, and careers. | 1 | `enrichment.classifyRole`; same test. |
| Personal-looking address classification using local heuristics. | 1 | `enrichment.personalLooking`; same test. |
| Disposable-domain detection using a locally maintained list. | 1 | `enrichment.defaultDisposableDomains` + `disposableDomain`, extendable via config; `TestAnalyzeEmailsCustomDisposableAndCancellation`. |
| Relevance ranking when multiple emails are found. | 1 | `enrichment.emailRelevance` + deterministic `Rank`; `TestAnalyzeEmailsClassifiesChecksAndRanksDeterministically`. |

## 13 Website Analysis

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: all three subsections below are now implemented and verified. |
| Reachability, HTTP status, final URL, redirect chain, HTTPS state, certificate errors, and response time. | 1 | `enrichment.Crawler.Analyze` sets `Reachable`/`StatusCode`/`FinalURL`/`RedirectChain`/`HTTPS`/`TLSValid`/`CertificateError`/`ResponseTime`; persisted by `StoreWebsiteAudit`, rendered in `result_drawer.html`; `TestCrawlerAnalyzesBoundedWebsiteAndContacts`, `TestCrawlerRejectsUnsafeRedirect`, `TestPreclassifyRecordsCertificateErrorAndFallsBackToHTTP`. |
| Parked domain, coming-soon page, placeholder page, and inaccessible website detection. | 1 | `enrichment.detectPlaceholderSignals`; `TestExtractPageDetectsMixedContentSignaturesAndStatusHeuristics`, `TestPreclassifyDetectsParkedHomepage`, `TestPreclassifyReportsDNSFailureAsUnreachable`. |
| Homepage screenshot and optional error screenshot. | **3 — built** | The homepage capture existed (`Service.captureAuditScreenshot`). The optional error capture was missing: added `shouldCaptureErrorScreenshot`, `captureAuditErrorScreenshot`, `errorScreenshotFileName`, `repo.AttachAuditErrorScreenshot`, and `website_audits.error_screenshot_path` (migration 17). Stored on the immutable audit only, so a failing rescan never replaces the last good preview. Tests `TestShouldCaptureErrorScreenshotCoversEveryFailingState`, `TestProcessEnrichmentQueueCapturesAnErrorScreenshotForAFailingSite`, `TestProcessEnrichmentQueueSkipsTheErrorScreenshotForAHealthySite`, `TestErrorScreenshotFileNameStaysInsideTheServedNameSet`. |
| Page title and meta description presence. | 1 | `enrichment.extractPage` fills `PageResult.Title`/`MetaDescription`; shown in the drawer; `TestExtractPageDetectsMixedContentSignaturesAndStatusHeuristics`. |
| Contact page, about page, visible phone, visible email, address, and social links. | **3 — built** | Contact/about crawling, phones, emails, and socials existed but there was no postal-address extraction and no presence audit. Added `web/enrichment/address.go` (JSON-LD `PostalAddress`, schema.org microdata, the semantic `<address>` element, bounded and deduplicated) and `enrichment.ContentAudit` computed by `auditContent`, both round-tripping through the immutable `raw_result`. Surfaced through `WebsiteAuditView` and as chips + an address list in `result_drawer.html`. Tests `TestExtractAddressesReadsStructuredDataMicrodataAndAddressElements`, `TestExtractAddressesIgnoresNonPostalContent`, `TestMergeAddressesDeduplicatesAcrossPagesAndFillsGaps`, `TestAuditContentReportsWhatTheCrawlFound`. |
| Mobile viewport tag, basic page-size measurement, broken internal links, mixed content, and old copyright year. | 1 | `PageResult.MobileViewport`/`SizeBytes`/`MixedContent`/`OldCopyright` and `Crawler.checkInternalLinks`; `TestExtractPageDetectsMixedContentSignaturesAndStatusHeuristics`, `TestCrawlerHonorsPageBodyAndTimeoutCaps`. |
| Obvious template/default text and incomplete setup indicators. | 1 | `enrichment.detectPlaceholderSignals` → `TemplateIndicators`; `TestExtractPageDetectsMixedContentSignaturesAndStatusHeuristics`. |
| WordPress, WooCommerce, Shopify, Wix, Squarespace, Webflow, Joomla, Drupal, Magento, React, Next.js, and common page builders. | 1 | `enrichment.detectSignatures` technology rule table (includes Elementor, Divi, WPBakery); `TestCrawlerAnalyzesBoundedWebsiteAndContacts`. |
| Google Analytics, Google Tag Manager, Meta Pixel, and other visible script signatures. | 1 | Same function, tracker rule table (GA, GTM, Meta Pixel, Hotjar, Clarity, Matomo); same test. |
| Detection should be signature-based and show confidence rather than claiming certainty. | **2 → 1** | Every `Detection` already carried a calibrated `Confidence` and its matched `Evidence` patterns, persisted to `website_detections.confidence` and returned by `GET /api/v1/results/{id}/enrichment` — but nothing proved it and the UI showed names only. Added `WebsiteView.DetectionRows`/`WebsiteDetectionRow` and a confidence table in `result_drawer.html` fed by `attachLatestWebsiteAudits`, with the explicit "likelihood not certainty" hint. Test `TestCrawlerAnalyzesBoundedWebsiteAndContacts` asserts confidences; the detection table is rendered by the drawer markup tests. |

## 14 Business Quality Scoring

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: all twelve sub-items below are implemented and covered by `web/quality_test.go` and `web/sqlite/quality_test.go`. |
| Business is open. | 1 | `QualityRuleSet.OpenWeight` + `ExcludeClosed`; scored in `web/sqlite/quality.go`. |
| Active website and HTTPS. | 1 | `ActiveWebsiteWeight`, `HTTPSWeight` scored from the stored website audit. |
| Phone number available. | 1 | `PhoneWeight`. |
| Email available and domain passes local checks. | 1 | `EmailWeight`, evaluated against the locally validated `emails` rows (syntax, MX, disposable). |
| Social profiles available. | 1 | `SocialWeight`. |
| Rating and review count thresholds. | 1 | `RatingWeight`/`RatingThreshold`, `ReviewCountWeight`/`ReviewCountThreshold`. |
| Listing completeness and data freshness. | 1 | `CompletenessWeight`, `FreshnessWeight`/`FreshnessDays`. |
| Website quality and response time. | 1 | `WebsiteQualityWeight`, `ResponseTimeWeight`/`ResponseTimeMS`. |
| Display total score from 0–100. | 1 | `BusinessQualityReport.Score`; rendered in the drawer hero and quality card. |
| Show each positive and negative contribution. | 1 | `QualityContribution{Component,Contribution,Maximum,Passed,Reason}` persisted to `business_score_components`. |
| Allow users to edit weights, thresholds, and exclusion rules. | 1 | `Service.SaveQualityRules` + `ValidateQualityRuleSet`, exposed by `web/quality_api.go`. |
| Store the scoring-rule version used so historical scores remain reproducible. | 1 | Content-derived immutable `QualityRuleSet.Version` in `quality_rule_sets`; reported as `RuleVersion` on every report. |

## 19 Proxy Manager (Testing and health, Rotation strategies)

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup for the whole of section 19. The two subsections below are now complete; "Import and organisation" is owned elsewhere and was already ticked. Integrator to confirm. |
| Connection success, Google access, response latency, exit IP, country, last success, failure count, block count, and usage count. | **3 — built** | Success/Maps access/latency/last success/failure/block/usage already recorded by `checkProxyAccess` + `repo.RecordProxyTest`. Exit IP and country were not measured. Added `web/proxies_probe.go`: a direct RFC 1928 SOCKS5 client so the CONNECT reply's `BND.ADDR` (the server's outbound address) is available, an endpoint-address observation for every protocol, and `googleCountryHint` which reads the country domain or `gl` parameter out of the Google redirect chain the test already follows. No third-party geolocation or IP-echo service is contacted and no extra request is made. `exit_ip_source` (migration 17) records which of the two an address is, and the Proxies page states it per row. Tests `TestGoogleCountryHintReadsTheRequestGoogleActuallyServed`, `TestDialSOCKS5ReportsTheServerBoundExitAddress`, `TestDialSOCKS5NegotiatesUsernameAndPasswordAndDropsUnusableBinds`, `TestUsableExitAddressRejectsPlaceholders`, `TestExitIPPrefersTheSOCKS5BoundAddress`. |
| Statuses: healthy, slow, rate-limited, blocked, authentication failed, offline, and cooling down. | **3 — built** | The first six were produced by `checkProxyAccess` but were untyped strings and "cooling down" did not exist. Added the `ProxyStatus*` constants, `ProxyRecord.EffectiveStatus` deriving cooling-down from the live cooldown window, `proxyStatusClass` mapping each status onto an existing badge state, and a blocked-exit cooldown alongside the rate-limited one (`blockedCooldown`). Rendered on `pages/proxies.html`. Tests `TestEffectiveStatusReportsCoolingDownInsideTheWindow`, `TestProxyStatusClassMapsEveryStatusOntoTheComponentLibrary`. |
| Round robin, random, least recently used, lowest failure rate, fastest, sticky per query, and sticky per grid cell. | **3 — built** | Round robin (`liveRunState.assignTaskProxies` rotation offset), random, fastest, lowest failure, and both sticky strategies existed. Least-recently-used was missing and is now a first-class strategy: `proxies.last_used_at` (migration 17) is stamped on every resolve, `proxyStrategyOrder` sorts never-used first then oldest use, `supportedProxyStrategy` accepts it, and the import form offers it. `Last used` is a column on the Proxies page. Verified by the sqlite proxy suite plus `TestNonStickyProxyAssignmentPrefersHealthyProxies` and `TestStickyAssignmentPinsAndCapsProxies`. |
| Automatically disable repeated failures, cool down rate-limited proxies, retest disabled proxies, and cap tasks per proxy. | 1 | `repo.RecordProxyTest` auto-disables after three failures and sets the cooldown; `Server.retestDisabledProxies`; `SetProxyPoolTaskCap` + `ProxyPlan.MaxTasksPerProxy`; `TestRetestDisabledProxiesReenablesHealthyProxies`, `TestStickyAssignmentPinsAndCapsProxies`. |

## 20 Adaptive Performance

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: CPU, RAM, free disk, browser count, and block rate are now all genuinely measured and all feed the concurrency decision. |
| Reduce concurrency when block/failure rate rises. | 1 | Failure rate: `decideFailureBudget`. Block rate is now real rather than assumed: `classifyTaskFailure` gained a `blocked` class (`isBlockedFailure`: 429, rate limit, captcha, `/sorry/`, consent interstitial, 403, unusual traffic), counted into `taskPoolRun.windowBlocks` and applied by `decideBlockBudget`, which halves on a single refusal. Tests `TestDecideBlockBudgetHalvesOnAnyBlockAndRecoversSlowly`, `TestIsBlockedFailureRecognisesPlatformRefusals`, `TestAdaptTaskPoolHalvesTheBudgetOnAMeasuredBlock`. |
| Increase concurrency cautiously after a stable success window. | 1 | Recovery is one step and now gated by `recoveryHasHeadroom`, which requires zero blocks, CPU below 70%, at least 2 GiB available RAM, and a live browser count within the plan. Tests `TestRecoveryHasHeadroomRequiresEveryMeasuredDimension`, `TestAdaptTaskPoolRefusesToRecoverWhileBlocksOrPressureLast`, `TestAdaptTaskPoolRecoversWhenEveryMeasuredDimensionHasHeadroom`. |
| Reduce browser count or pages per browser when RAM pressure rises. | **3 — built** | Previously only worker concurrency moved. Added `adaptiveBrowserBudget` plus `taskPoolRun.browserBudget`/`pagesBudget`, applied per task in `executeLeasedTask` (the engine's safe reconfiguration point) and restored when the pressure clears. Also added the browser-count measurement it needs: `browserCensus`/`countManagedBrowserProcesses` attributes browser processes to this application through the parent chain, cached at 10 s so it adds no load. Tests `TestAdaptiveBrowserBudgetShrinksUnderMemoryPressure`, `TestAdaptTaskPoolShrinksTheBrowserBudgetUnderMemoryPressure`, `TestCountManagedBrowserProcessesReadsTheLocalProcessTable`, `TestHasAncestorWalksABoundedParentChain`, `TestBrowserCensusCachesBetweenSamples`, `TestIsBrowserProcessNameMatchesLaunchedBrowsers`. |
| Pause new tasks when disk space becomes low. | 1 | `executeLeasedTask` releases the task and `superviseTaskPool` stops with `StopReasonLowDisk`; `TestCheckpointedJobPausesBeforeLowDiskAndResumesOnlyPendingTask`. |
| Retry failed pages with another proxy or a fresh browser context. | 1 | `failLeasedTask` + `deferFailedTask`; each retry runs a new `runCheckpointTask` with a fresh scrapemate instance, and a proxy or blocked failure marks the exit so rotation moves on. `TestStickyAssignmentPinsAndCapsProxies`, `TestTaskFailureBackoffTable`. |
| Restart crashed browser processes automatically. | 1 | `classifyTaskFailure` → `browser-failure`, backed off by `taskFailureBackoff` and retried on a brand-new browser context; `TestClassifyTaskFailureMapsSignatures`, `TestTaskFailureBackoffTable`. |
| Pause the job when all proxies fail and resume after recovery. | 1 | `liveRunState.markProxyFailed` → `StopReasonProxiesUnavailable`, resumable from the durable plan; `TestExhaustedStickyPoolPausesTheJobAsProxiesUnavailable`. |
| Adjust website timeout using recent response history. | **2 → 1** | `enrichment.AdaptiveTimeout` + `adaptEnrichmentTimeout` + `repo.WebsiteLatencyHistory` already existed and were unit tested, but the option was reachable only through the JSON/form API — nothing in the product turned it on. Added the wizard toggle (`enrichment_adaptive_timeout`) and wired `JobEnrichmentOptions.AdaptiveTimeout` in `parseScrapeForm`. Existing tests `TestAdaptiveTimeoutShortensTheAnalyzerBudgetWhenEnabled`, `TestAdaptiveTimeoutIsOffByDefaultAndKeepsTheConfiguredBudget`, `TestAdaptiveTimeoutOptionDecodesFromJSONAndForm`. |
| Display every automatic change and the reason it occurred. | 1 | `adaptTaskPool` and `adaptBrowserBudget` write `adaptive-performance` worker events carrying the previous and new budgets, the reason, and every measurement (CPU, available RAM, free disk, browser processes, window blocks, head-room). Rendered by the Job Monitor log. |

## 21 Checkpoints and Recovery

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Section complete and verified against the specification. | 1 | Rollup: every sub-item below is implemented and tested. |
| Persist completed queries, grid cells, listing IDs, enrichment tasks, and deduplication state. | **3 — built** | Queries and grid cells (`job_tasks`) and enrichment tasks (`enrichment_tasks`) were durable, but listing identities and deduplication state lived only in the in-process `deduper`, so a restart re-visited every listing already discovered. Added the `job_listing_keys` table (migration 17), `repo.RememberJobListingKeys`/`JobListingKeys`/`CountJobListingKeys`, the `Service` wrapper in `web/checkpoint_dedup.go`, and `runner/webrunner/durable_deduper.go`, which keeps the in-memory set as the hot path and seeds it from the durable rows on resume. Identities are stored as one-way digests so the record can never become a second copy of the results. Tests `TestDurableDeduperRecordsNewListingsAndSkipsRepeats`, `TestDurableDeduperResumesFromRecordedListings`, `TestDurableDeduperFlushesOnceTheBufferFills`, `TestDurableDeduperKeepsKeysWhenTheWriteFails`, `TestDurableDeduperStartsCleanWhenTheStoreCannotBeRead`, `TestListingKeyDigestIsStableAndDoesNotStoreTheURL`, `TestJobListingKeysArePersistedIdempotentlyAndReadBack`, `TestJobListingKeysIgnoreEmptyInputAndUnknownJobs`. |
| Save checkpoints at configurable intervals and after each meaningful stage. | **3 — built** | Stage checkpoints existed (one per completed or failed task). The configurable interval did not. Added `JobData.CheckpointSeconds` (5–3600 s, default 30) with validation and `CheckpointInterval()`, a wizard field in the performance step, `repo.RecordJobIntervalCheckpoint`, `Service.RecordJobIntervalCheckpoint`, and `webrunner.recordIntervalCheckpoint` driven from the pool supervisor — which also flushes the durable listing keys. Tests `TestCheckpointIntervalIsBoundedAndDefaults`, `TestCheckpointSecondsValidationRejectsOutOfRangeValues`, `TestIntervalCheckpointBecomesTheLatestResumeBoundary`. |
| Resume after application or computer restart. | 1 | `Service.RecoverAbandonedJobs` at startup plus `PrepareJobTasks` returning only unfinished tasks; `TestCheckpointTasksResumeOnlyUnfinishedWork`, `TestCheckpointedJobPausesBeforeLowDiskAndResumesOnlyPendingTask`. |
| Detect abandoned "running" jobs at startup and offer recovery. | 1 | `repo.RecoverAbandonedJobs` + `RecoveryRequired` on `JobExecutionSnapshot`; `TestInterruptedJobReportsRecoveryStatusAndLastCheckpoint`. |
| Preserve partial CSV/database results and durable redacted lifecycle logs. | 1 | `mergeResultCSV` merges rather than replaces, `recoverResultRunFiles`/`recoverResultReplacementBackups` at startup, and every worker event is redacted through `jobruntime.RedactString`; `TestResumeCursorAndRepeatedRows`, `runner/webrunner/result_files_test.go`. |
| Continue from last completed query or grid cell. | 1 | Deterministic `JobTaskDefinition.Key` per query/cell and `ClaimNextJobTask`; `TestCheckpointTasksResumeOnlyUnfinishedWork`. |
| Create and verify local backups before database migrations. | 1 | `backupBeforeMigration` + `verifySQLiteDatabase` + `registerMigrationBackup`; `TestOnDiskMigrationFailureKeepsVersionThreeDataAndBackup`, `TestPruneManualBackupsNeverTouchesMigrationCopies`. |
| Expose recovery status and last checkpoint time in the UI. | 1 | `web.RecoveryStatusMessage` + the Checkpoint card in `pages/job_monitor.html` fed by `GET /api/v1/jobs/{id}/checkpoint`; `TestInterruptedJobReportsRecoveryStatusAndLastCheckpoint`. |

## Migration 17

Reserved for this group; additive and idempotent only.

- `proxies.last_used_at` (+ index) — least-recently-used rotation clock.
- `proxies.exit_ip_source`, `proxy_health.exit_ip_source` — how a recorded exit
  address was determined, so the interface never presents a gateway entry
  address as a confirmed exit.
- `website_audits.error_screenshot_path` — the optional error capture.
- `job_listing_keys` (+ index) — durable listing identities and deduplication
  state.

`validateMigrationMetadata` and `validateMigrationChecksums` in
`web/sqlite/migrations.go` previously required the recorded history to be a
contiguous `1..N` run. Because version numbers are reserved per work stream,
they now compare the recorded history against the declared migration list
(`declaredMigrationsThrough`), which tolerates the reserved gaps until every
stream has landed and stays correct once they have.
`assertCurrentSchema` in `migrations_test.go` was updated for the same reason.

## Notes for the integrator

- `docs/technical-limitations.md` currently states that exit IP and country are
  unknown, that the proxy status taxonomy is not implemented, that
  least-recently-used rotation is deferred, that block rate is not measurable,
  that adaptation cannot touch browser count or pages per browser, that adaptive
  website timeout and error-page screenshots are deferred, and that
  deduplication state is not persisted. Every one of those statements is now out
  of date for these sections.
- Nothing in these sections is recorded as infeasible. The only honest boundary
  that remains is *within* the exit-IP item: an HTTP CONNECT tunnel carries no
  outbound-address field, so for HTTP/HTTPS proxies the stored address is the
  dialled endpoint and is labelled as such per row. SOCKS5 reports the real
  bound address. No paid or external service is involved either way.
