# Audit — 22 Export Centre, 23 Local API, 24 Local Integrations, 25 Optional Local AI, 26 Database and Storage, 27 System and Diagnostics, 28 Settings and Preferences, 29 Security and Privacy, 30 UI, Accessibility and Onboarding

Branch `spec/platform-api-system`. Reserved migration version **18** (used:
`web/sqlite/migrations.go`, `integration-deliveries-and-encrypted-backups` —
adds `integration_deliveries` with its uniqueness and recency indexes, and
`backups.encrypted`).

Classes: 1 implemented-and-verified · 2 implemented-but-unverified (test added,
now 1) · 3 unfinished-but-feasible (built in this pass) ·
4 intentionally-optional-or-deviated · 5 genuinely-infeasible.

New dependency: `github.com/parquet-go/parquet-go v0.32.0`, added for the
Parquet export format. It is a pure Go module resolved through the normal
module graph, so offline builds keep working after `go mod download`; nothing
about it requires a network at run time.

---

## 22 Export Centre

### Formats

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Parquet. | 3 (built) | `web/export_parquet.go:newParquetExportWriter` writes typed optional leaves from `exportColumnDefinitions` (double, int64, boolean, `Timestamp(Millisecond)`, JSON logical type) with zstd compression and bounded 20,000-row groups; registered in `newExportPartWriter`/`exportExtension`, verified by `verifyParquetExport`, offered in `pages/exports.html`. Round-tripped through the reader in `web/export_builder_test.go:TestConfiguredExportWritersProduceVerifiedFiles/parquet`. |

The other eleven format lines were already ticked and were re-checked against
`web/app_exports.go:exportExtension` and the writers in `export_text.go`,
`export_xlsx.go`, and `export_sqlite.go`.

### Export builder

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Export all, selected, filtered, or saved-view records. | 2 → 1 | `web/export_requests.go:resolveExportCreation` resolves `source_scope` all/filtered/saved_view/selected; the picker is `#export-scope` in `pages/exports.html`. Test added: `web/export_scope_test.go:TestExportCreationResolvesEveryRecordScope` (including refusal of an unknown saved view). |
| Split by city, category, job, or maximum row count. | 2 → 1 | `web/export_builder.go:parseExportBuildOptions` + `exportGroupFor`; XLSX row ceiling enforced in `resolveExportCreation`. Test added: `web/export_scope_test.go:TestExportDeliveryOptionsRoundTripThroughStoredConfiguration`. |
| Include raw JSON, source data, provenance, or change history. | 2 → 1 | `ExportBuildOptions.IncludeRaw/IncludeSources/IncludeProvenance/IncludeChanges` append the `raw_json`, `sources_json`, `provenance_json`, `changes_json` columns (`parseExportColumnSpec`); checkboxes in `pages/exports.html`. Same test asserts the four evidence columns survive the stored round trip. |

### Export history

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Save export presets for repeated delivery formats. | 2 → 1 | `web/exports.go:SaveExportPreset/ListExportPresets/GetExportPreset/DeleteExportPreset` over `export_presets`; created by `save_preset`/`preset_name` in `resolveExportCreation`, replayed by `presetExportCreation`, listed at `GET /api/v1/exports/presets`, rendered and runnable in `pages/exports.html`. The stored-shape contract is pinned by `TestExportDeliveryOptionsRoundTripThroughStoredConfiguration` (a preset replays exactly the stored columns, options, and filters). |

---

## 23 Local API

### Endpoint groups

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| **Exports:** Create, status, list, download, repeat, and delete. | 2 → 1 | All six registered in `web/export_requests.go:registerExportRoutes`. Test added: `web/export_api_group_test.go:TestExportsEndpointGroupIsReachableThroughTheRealHandlerChain` drives each through the real handler chain and checks each is documented in the generated catalogue. |

### API experience

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| OpenAPI/Swagger documentation. | 3 (built) | The document was a hand-written sample of eleven paths against 150+ registered routes. `web/openapi.go:openAPIDocument` now generates it from `web/openapi_catalogue.go:localAPICatalogue` (166 operations) with operationIds, summaries, path parameters, request bodies, response envelopes, tags, and security schemes; served at `GET /api/openapi.json`. `web/openapi_route_test.go:TestRouteCatalogueCoversEveryRegisteredAPIRoute` scans the package's own `HandleFunc` registrations and fails when a route is undocumented, and `TestOpenAPIDocumentCoversTheWholeRouteCatalogue` proves the document and catalogue agree exactly. The browsable page is `/app/api` (`web/app_api.go:apiReferenceGroups`, `pages/api.html`), grouped, filterable, served from the embedded FS with no CDN. |
| Examples in cURL, Python, JavaScript, and Go. | 3 (built) | `web/openapi.go:localAPIExamples` renders every operation in all four languages; they appear per endpoint on `/app/api` and as `x-codeSamples` in the OpenAPI document. Language coverage and non-emptiness are asserted per operation in `TestOpenAPIDocumentCoversTheWholeRouteCatalogue`. |

---

## 24 Local Integrations

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| n8n self-hosted and Activepieces self-hosted through local webhooks or API calls. | 3 (built) | The webhook delivered one hard-coded event, unsigned, once, with no history. Now: four event types with per-integration subscriptions (`web/integrations.go:integrationEventNames`, `validateIntegrationEvents`), a versioned envelope (`web/integrations_webhook.go:webhookEnvelope`), optional shared-secret `X-GMS-Signature: sha256=HMAC(timestamp + "." + body)` (`signWebhookPayload`), bounded geometric-backoff retries that stop on a permanent 4xx (`deliverLocalWebhook`, `webhookBackoff`, `retryableWebhookStatus`), and durable delivery history claimed before the request runs (`web/integrations_deliveries.go`, `web/sqlite/integration_deliveries.go`, schema v18). Job events are emitted by `watchTerminalJobEvents`, started from `Server.Start`. UI and history table in `pages/api.html`; `GET /api/v1/integrations/deliveries`. Tests: `web/integrations_test.go` (envelope, signature and timestamp binding, retry/permanent-rejection counts, subscription gating), `web/sqlite/integration_deliveries_test.go` (claim-once, cascade delete). |
| Local PostgreSQL, MySQL/MariaDB, or another SQLite database. | 3 (built) | New `database` integration kind (`web/integrations_database.go`). `sqlite` loads a completed csv or sqlite export into a contained local SQLite file (`loadExportIntoSQLite`, `copySQLiteExportRows`, `copyCSVExportRows`); `postgres` executes the generated `postgresql_sql` insert transaction against a loopback/private server through the already-vendored pgx (`loadExportIntoPostgres`). MySQL/MariaDB has no driver in this module graph and the product must build offline, so the shipped path there stays the generated `mysql_sql` insert transaction plus a watch folder — stated in the form's own help text. Tests: `TestSQLiteDatabaseDestinationLoadsACSVExportInsideTheDataDirectory`, `TestDatabaseDestinationValidationContainsPathsAndHidesCredentials`. |
| Run a local shell command or Python script after completion. | 4 | **Integrator ruling: out on security grounds.** Process execution driven by a locally reachable web UI turns the workspace into a code-execution surface. The shipped equivalent is the outbound local webhook plus the local REST API: an automation tool (n8n, Activepieces, a cron script) receives a signed event and calls back in. The pre-existing environment-gated command hook has been **removed** in this pass so the code matches the ruling — `IntegrationCommand`, `validateCommandConfiguration`, `commandExecutableAllowed`, `deliverCommandHook`, `boundedHookWriter`, the `os/exec` import, and the UI fields are gone, and `validateIntegrationConfiguration` now rejects the `command` kind (asserted in `TestIntegrationConfigurationValidationHidesSecretsAndRejectsUnsafeTargets`). |
| Optional Google Sheets sync using the user's own Google credentials and quotas. | 5 | **Integrator ruling.** Requires an external hosted service and an OAuth flow; it cannot work offline on one machine, which the local-first product rule forbids as a requirement. |
| Custom plugin hooks for enrichment, validation, scoring, and export. | 4 | **Integrator ruling: out on the same security grounds as command hooks.** A plugin hook is process or code execution under another name. Equivalent shipped: the outbound webhook (with subscriptions and HMAC) plus the full local REST API, which a local automation tool calls back into to enrich, validate, score, or export. |

---

## 25 Optional Local AI

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Convert a natural-language request into a scrape configuration. | 3 (built) | Prompt `scrape_configuration` existed with no way to reach it. Now offered by the Settings console (`pages/settings.html` `data-ai-console`, `web/static/js/app-ai.js`). |
| Classify businesses and website quality. | 3 (built) | Prompt `classify_business`; console assistant "Classify a business and its website quality". |
| Explain quality scores and duplicate matches. | 3 (built) | `explain_quality` existed; **`explain_duplicate` added** to `web/local_ai.go:localAIPrompt` with a matching/conflicting-evidence and merge recommendation shape. Both are console assistants. |
| Summarize business descriptions or change history. | 3 (built) | `summarize_changes` existed; **`summarize_business` added** to `localAIPrompt`. Both are console assistants. |
| Suggest missing cities, categories, or exclusion keywords. | 3 (built) | Prompt `suggest_coverage`; console assistant "Suggest missing cities, categories, or exclusions". |

All five are optional and degrade invisibly: the console ships `hidden` and is
revealed only after `GET /api/v1/ai/status` reports an enabled *and* reachable
model, so a workspace without Ollama shows no dead control and no failed request
delays the page. Requests go only to the operator's validated loopback/private
endpoint (`validateLocalAIEndpoint`, `localAIHostIsLocal`, `dialLocalIntegration`).
Model output is rendered as text nodes, never markup, and every answer is
labelled a suggestion to review. Tests:
`web/local_ai_features_test.go:TestEveryLocalAIFeatureTaskHasABoundedStructuredPrompt`,
`TestSettingsPageShipsTheLocalAIConsoleHiddenWithEveryFeature`,
`TestLocalAIDegradesWithoutBlockingWhenOllamaIsAbsent`.

---

## 26 Database and Storage

### Default database

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Batch inserts, indexed filters, integrity checks, VACUUM, migrations, backups, and retention policies. | 1 | Batch inserts: `web/sqlite/results.go:ImportResultFile` commits a whole CSV import as one `BeginTx` transaction rather than one commit per row, and the Parquet and SQLite export writers batch their own writes (`web/export_parquet.go:flush`, `web/export_sqlite.go`, `web/integrations_database.go:copyCSVExportRows` uses a prepared statement inside one transaction). Indexed filters: 61 `CREATE INDEX` statements across `web/sqlite/migrations.go` plus the `businesses_fts` FTS5 table. Integrity/VACUUM/backups: `web/sqlite/maintenance.go:RunIntegrityCheck/VacuumDatabase/CreateDatabaseBackup` behind `/api/v1/system/integrity`, `/vacuum`, `/backups`. Migrations: versioned, additive, checksum-verified, with a pre-migration backup (`migrateDatabase`, `registerMigrationBackup`) and a forward-schema refusal — covered by `web/sqlite/migrations_test.go`. Retention: `web/retention.go:ApplyRetentionPolicies` with `web/retention_test.go`. |
| Optional local PostgreSQL for larger datasets or multiple local workers. | 4 | Named equivalent already shipped: the upstream CLI keeps its PostgreSQL run modes (`database`, `database-produce` with `-dsn`, `runner.ParseConfig`), and this pass adds a **local PostgreSQL export destination** (§24). The local web workspace itself stays SQLite-only by design — the local-first rule forbids requiring a hosted or separately administered database, and moving the workspace's own persistence would break the single-writer/WAL contract every `web/sqlite/` call depends on. |

### Recommended tables

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| **job_tasks:** …the upstream runner does not emit complete query/cell/listing/website task cursors. | 4 | The table exists (`web/sqlite/migrations.go`). Filling it needs the scrape engine to emit per-task cursors, and the hard constraint for this stream is no behaviour change to `gmaps/`, `exiter/`, or `deduper/`. Named equivalents already shipped for resumability and per-task visibility: `job_checkpoints` (`web/checkpoints.go`), `job_listing_keys`, and `job_pipeline_facts`. |
| **emails / phones / social_profiles:** Contact values, source, confidence, and status. | 1 | Tables created in migration 6 (`web/sqlite/migrations.go`), extended with `valid_syntax`, `role`, `personal_likely`, `mx_status`, `mx_records`; populated by `web/sqlite/enrichment.go:839/924/968` with value, normalized value, kind/platform, source URL, and confidence; indexed on `normalized_value`. Covered by `web/sqlite/enrichment_test.go:TestDurableWebsiteEnrichmentQueueAuditAndEvidence` and read back by `web/sqlite/advanced_filters_test.go` / `results_core_columns_test.go`. |

### Storage directories

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Database, exports, screenshots, logs, cache/browser profiles, backups, and temporary files should be separate and configurable. | 3 (built) | Five directories were already configurable (`web/app_settings.go:storagePreferences`, `validateStoragePreferences`). The **browser-profile** directory was neither named nor addressable; it is now a first-class contained directory (`web/system_maintenance_actions.go:browserProfileDirectory`) with its own maintenance action, and the fixed database directory is now explained on the Settings page (`-data-folder`). Test: `web/privacy_settings_test.go:TestSettingsPageShowsDirectoryUsageAndLivePrivacyState`. |
| Display size and retention settings for each directory. | 3 (built) | `web/app_maintenance.go:systemDirectoryViews` pairs all eight directories with a measured size and the retention rule that actually governs them (age cleanup, storage cap, backup count, "never deleted"). Rendered as a table on `/app/system` and as a usage list under Storage and retention on `/app/settings`. Tests: `web/system_maintenance_actions_test.go:TestSystemPageListsEveryStorageDirectoryWithRetention`, `TestSettingsPageShowsDirectoryUsageAndLivePrivacyState`. |

---

## 27 System and Diagnostics

### System information

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| CPU, RAM, disk, database size, queue length, active browsers/pages, running jobs, log size, screenshot storage, and export storage. | 2 → 1 | `web/system_diagnostics.go:defaultLocalSystemProbe.Resources` (gopsutil CPU/memory/disk), `SystemDatabaseSnapshot` (database size, website queue, active browsers/pages), `appActivity` (running/queued jobs), `web/system_storage.go:workspaceStorageUsage` (bounded per-directory sizes). All rendered on `/app/system` and served by `GET /api/v1/system/metrics`. Existing test `web/system_diagnostics_test.go:TestSystemHealthAndMetricsUseLightweightLocalProbes`; per-directory rendering now also asserted by `TestSystemPageListsEveryStorageDirectoryWithRetention`. |
| Worker heartbeat, last successful browser launch, last database write, and proxy-pool status. | 1 | `schedulerHeartbeatView`, `SystemDatabaseSnapshot.LastBrowserAt/LastWriteAt`, `ProxyHealthy/ProxyTotal/ProxyBlocked`, all rendered in `pages/system.html`. `web/sqlite/system_diagnostics_test.go` covers the snapshot. |

### Maintenance actions

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Restart worker, stop all jobs, clear cache, clean old screenshots/exports/logs, VACUUM database, integrity check, create backup, restore backup, export diagnostics, check for updates, and run self-test. | 3 (built) | Eight already existed (`stop-all`, `cache/clear`, `artifacts/cleanup`, `vacuum`, `integrity`, `backups`, `diagnostics/download`, `update-info`, `self-test`). **Added:** *Recycle workers* (`POST /api/v1/system/worker/recycle`) pauses every active job at its next safe boundary — tearing its browser workers down — and resumes it from the durable checkpoint, without spawning a process or discarding a committed result; *Clear browser profiles* (`POST /api/v1/system/browser-profiles/clear`); *Prepare restore* (`POST /api/v1/system/backups/{id}/restore`) verifies the backup's checksum, refuses a forward schema, takes a fresh safety copy of the live database, and stages the verified file under `restore/` with the exact finishing steps. A live SQLite file cannot be swapped out from under the connections this process holds, so the final swap stays an explicit stop-and-replace step; what the action removes is the guesswork, and it never deletes or overwrites anything. All are on `/app/system`. Tests: `web/system_maintenance_actions_test.go` (CSRF refusal, containment, unknown-backup refusal, every action present on the page). |

### Self-test checks

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Output directories writable. | 1 | `web/system_diagnostics.go:checkOutputDirectoryWritable`, wired as the `output_directory` check in `apiSystemSelfTest`; covered by `TestSystemSelfTestIsOfflineByDefaultAndBoundsExplicitNetworkChecks`. |
| Browser can launch. | 1 | `browserRuntimeCheck` reports the resolved Playwright driver location and is explicit that only a real scrape proves a launch; covered by `TestSystemSelfTestReportsBrowserRuntimeAndProxyCredentialChecks`. |

---

## 28 Settings and Preferences

### Storage and retention

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Data, export, screenshot, log, backup, and temporary directories. | 3 (built) | The five configurable paths already persisted through `validateStoragePreferences` with traversal refusal (`TestValidateSettingsFormRejectsEscapingStoragePaths`). This pass adds the measured size and governing retention rule beside each one, and states plainly that the data directory is fixed by the `-data-folder` start-up flag so a running workspace can never be pointed at another database. Test: `TestSettingsPageShowsDirectoryUsageAndLivePrivacyState`. |

### Privacy and appearance

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Disable telemetry; redact secrets from logs; clear browser profiles; encrypt sensitive settings. | 3 (built) | Telemetry: the panel used to restate what the shipped Compose file sets; `web/app_settings.go:privacyStatus` now reads the live `DISABLE_TELEMETRY` state of this process and warns when it is not set (`runner/runner.go:Telemetry` is the switch it reflects). Redaction: `jobruntime.RedactString/RedactURL` across lifecycle logs, API responses, integration errors, and exports. **Clear browser profiles: newly built** as a maintenance action, with the profile size surfaced in the privacy panel. Encrypted settings: the panel now lists what is actually encrypted at rest — proxy credentials, integration webhook secrets and database DSNs (both AES-256-GCM under `.proxy-master-key`), passphrase-protected backups, and the salted-hash-only local password. Test: `web/privacy_settings_test.go:TestPrivacyStatusReportsTheLiveTelemetrySwitch`. |

---

## 29 Security and Privacy

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| CSRF protection, secure cookies, API-key protection, and local rate limiting. | 3 (built) | CSRF: `web/csrf.go:requireCSRF`, called explicitly by the handlers that mutate local state, plus `web/web.go:browserCSRFProtection`, which rejects any mutating `/api/v1/` request that carries an `Origin` header without a valid token — the browser-originated case a script cannot forge. API keys: `web/api_access.go:apiAccessMiddleware`/`authenticateAPIRequest` with read-only and full permissions and SHA-256-only storage (`TestAPIAccessMiddlewareEnforcesPermissionsAndMasksRequestLogs`, `TestLocalAPIKeyHashDoesNotContainToken`). Rate limiting: `allowAPIRequest` (`TestAPIRateLimitAndSameOriginBrowserCompatibility`). **Secure cookies were the gap**: the session cookie was `HttpOnly` + `SameSite=Strict` but never `Secure`. `web/auth_api.go:secureRequest` now sets `Secure` whenever the request arrives over TLS or through an operator's own `X-Forwarded-Proto: https` proxy, while plain loopback HTTP keeps working (a `Secure` cookie would never be sent back there). |
| Offer encrypted backups and a privacy-scrubbed diagnostics bundle. | 3 (built) | Diagnostics bundle already existed (`web/app_maintenance.go:downloadSystemDiagnostics` + `redactDiagnosticSettings`). **Encrypted backups newly built**: an optional passphrase rewrites a verified backup as an authenticated container (`web/backup_encryption.go`) — scrypt-derived AES-256-GCM key, per-chunk nonces, and the final-chunk marker inside the AAD so a truncated, reordered, or spliced file fails authentication instead of decrypting to a partial database. The same page downloads it decrypted (POST only, so the passphrase never reaches browser history or the request log), and headers are deferred until the first chunk authenticates so a wrong passphrase yields nothing. Recorded by `backups.encrypted` (schema v18, `web/sqlite/maintenance.go:MarkBackupEncrypted`). Tests: `web/backup_encryption_test.go:TestEncryptedBackupRoundTripsAndRejectsTampering`, `TestBackupPassphraseIsBounded`. |

---

## 30 UI, Accessibility and Onboarding

### Visual design

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Progress bars, skeleton loaders, empty states, tooltips, inline validation, and actionable error messages. | 1 | Progress: `.progress`/`.progress-bar` with `role="progressbar"` and full ARIA values in `pages/dashboard.html`, `pages/jobs.html`, `pages/job_monitor.html`. Skeletons: `.skeleton`/`.skeleton-text`/`.skeleton-block` in `app.css`, used by `app-results.js:skeletonBlock` for the drawer. Empty states: `.empty-state` throughout, asserted by `web/app_shell_test.go:TestEmptyMetricsRenderAsAMutedEmptyState`. Tooltips: `.tooltip` component in `app.css`, used in `pages/dashboard.html` and `partials/sidebar.html`. Inline validation: native constraint validation on every form control (`required`, `minlength`, `maxlength`, `pattern`, `min`/`max`, `type="url"`) with matching server-side bounds returning 422 and a specific message (`renderLocalAPIError`). Actionable errors: `.notice-error` bodies and the toast path in `app.js`. |

### Keyboard and accessibility

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Suggested shortcuts: N new scrape, J jobs, R results, / search, P pause current job, Esc close panel, Ctrl/Cmd+E export. | 1 | All seven are bound in the single global `keydown` handler in `web/static/js/app.js` (lines 343–376): `Escape` closes the palette and any open dialog, `Ctrl/Cmd+E` navigates to `/app/exports`, `/` focuses global search, and `n`/`j`/`r`/`p` navigate or click `[data-action="pause-job"]` — each guarded so typing in an input never triggers them. |

### Help and first-run experience

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Setup wizard that checks browser, database, data directory, internet access, disk capacity, and optional proxies. | 3 (built) | Database, data directory, disk capacity, and binding were already checked (`web/app_onboarding.go:onboardingPage`, `onboardingDiskCheck`). The browser row was an unconditional "Docker installs it" claim and there was no proxy row at all. Added `onboardingBrowserCheck` (reusing the System self-test's real Playwright probe) and `onboardingProxyCheck` (counts enabled proxies without touching the network, because a first-run page must not dial out by itself); the live self-test now checks internet reachability separately from Maps, so a blocked Maps endpoint is no longer reported as a missing connection. Existing coverage: `web/onboarding_disk_test.go`; page rendering covered by `web/operator_ui_test.go:TestEveryAppPageRendersWithZeroValuePageData`. |

---

## Section-complete lines

The nine `Section complete and verified against the specification.` lines for
sections 22–30 are **not** claimed here. Every unchecked item in these sections
is now either built and tested, or classified above with a named equivalent or a
concrete technical reason. `docs/implementation-progress.md` and
`docs/technical-limitations.md` were deliberately left untouched, per the task
instructions; the integrator owns reconciling them against this audit.
