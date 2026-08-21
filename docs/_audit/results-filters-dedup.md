# Audit — 08 Results Explorer, 09 Advanced Filtering, 10 Deduplication Engine, 15 Field Provenance

Branch `spec/results-filters-dedup`. Reserved migration version **15** (used:
`web/sqlite/migrations.go`, `results-core-columns`, adds `businesses.user_reviews`).

Classes: 1 implemented-and-verified · 2 implemented-but-unverified (test added, now 1)
· 3 unfinished-but-feasible (built in this pass) · 4 intentionally-optional-or-deviated
· 5 genuinely-infeasible.

## 08 Results Explorer

### Data table capabilities

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Indexed server pagination, FTS5 search, sorting, and bounded column filters; virtual scrolling is not claimed. | 1 | `web/sqlite/results.go:SearchBusinesses` (FTS5 `businesses_fts MATCH`, bounded `resultSortSQL`, `LIMIT/OFFSET`); `web/results_api_test.go:TestParseResultSearchBoundsInput`. |
| Resize, reorder, hide, freeze, and group columns. | 2 → 1 | `web/static/js/app-results.js:startColumnResize/reorderColumns/applyColumnVisibilityAndWidths/applyFrozenColumns/groupRows`, dialog markup in `pages/results.html` (`data-column-list`, `data-layout-group`). Test added: `web/results_explorer_test.go:TestResultsTableExposesColumnAndSelectionMachinery`. |
| Inline editing, multi-row selection, keyboard navigation, copy cells/rows, and saved table layouts. | 3 (built) | Selection/keyboard/copy/named layouts already existed (`updateSelection`, `handleTableKeydown`, `copySelected`, `saveNamedLayout`). **Inline editing was missing and is now built**: editable cells (`data-edit-field` on name/category/website/phone, capability-gated), `beginInlineEdit`/`saveInlineEdit`/`cancelInlineEdit`/`undoInlineEdit` in `app-results.js`, client validation mirroring the server, POST to the audited `/api/v1/results/{id}/fields`, toolbar undo posting the previous value back. Tests: `TestResultsTableOffersInlineEditingOnlyWhenTheRepositorySupportsIt`, `TestInlineEditScriptPostsThroughTheAuditedRouteWithAnUndo`, `TestInlineEditRouteRecordsTheCorrectionAndItsUndo`. |
| Table-only, map-only, and split table/map views. | 2 → 1 | `app-results.js:setViewMode` plus the segmented control and `data-results-map-pane` iframe in `pages/results.html`; asserted in `TestResultsTableExposesColumnAndSelectionMachinery`. |
| Saved views tied to filters, visible columns, sorting, and grouping. | 3 (built) | Views previously stored only the search. Added `SavedResultView.Columns/Group` (`web/reusable.go`), persisted through the existing `saved_views.columns`/`grouping` columns (`web/sqlite/reusable.go`, no migration needed), carried on the reopen URL by `savedViewLayoutURL`, applied by `app-results.js:savedViewLayout`, and posted by `syncSavedViewLayout`. Tests: `web/sqlite/saved_view_layout_test.go`, `web/saved_view_layout_test.go`. |

### Core columns

All eight lines were partially rendered only. `BusinessResult` now carries the
missing fields (`web/results.go`), `SearchBusinesses` reads them
(`businessCoreColumnSQL` in `web/sqlite/results.go`), the table renders them as
hidden-by-default columns, and each one has an export column
(`web/export_builder.go`). Verified by
`web/sqlite/results_core_columns_test.go`,
`web/results_explorer_test.go:TestResultsTableRendersEverySpecificationCoreColumn`,
and `web/export_core_columns_test.go`.

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| **Business:** Name, primary category, additional categories, description, claimed status, business status. | 3 (built) | `description` was stored but never surfaced; now `BusinessResult.Description`, column `data-column="description"`, export key `description`. |
| **Location:** Full address, street, city, state, postal code, country, latitude, longitude, plus code. | 3 (built) | Added `Street`, `PlusCode` and the `street`, `postal`, `coords` columns; export keys `street`, `plus_code`. |
| **Contacts:** Phone, normalized phone, phone type, website, domain, emails, email type, email status. | 3 (built) | Added `PhoneType`, `Emails`, `EmailType`, `EmailStatus` (read from the `phones`/`emails` tables); columns `phone`, `emails`; export keys `phone_type`, `emails`, `email_type`, `email_status`. |
| **Social:** Facebook, Instagram, LinkedIn, X/Twitter, YouTube, TikTok, WhatsApp. | 3 (built) | Added `BusinessSocial` (`web/results.go`), read from `social_profiles`, rendered as the `social` column, exported as one column per platform. |
| **Reputation:** Rating, review count, ratings breakdown, user reviews, popular times. | 3 (built) | Added `ReviewsPerRating`, `PopularTimes` (already stored) and `UserReviews` (**migration 15** adds `businesses.user_reviews`, populated on import). Columns `ratings`, `userreviews`, `populartimes`; matching export keys. |
| **Identifiers:** Place ID, CID, Data ID, Maps URL, source query, source cell, input ID. | 3 (built) | `input_id` was stored but never exposed; added `BusinessResult.InputID`, the `identifiers` column, and the `input_id` export key. |
| **Quality:** Website status, response time, technology, quality score, confidence, last checked. | 3 (built) | Added `Technologies` and `LastCheckedAt` from the `websites` table; columns `technology`, `checked`; export keys `technologies`, `last_checked_at`. |
| **Workflow:** Tags, notes, reviewed flag, scrape date, last update, change status. | 3 (built) | Tags/reviewed/scraped/updated already rendered; added explicit `notes`, `change`, and `seen` (first/last seen) columns and the `first_seen_at`/`last_seen_at` export keys. |

### Bulk actions

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Export, delete, tag, untag, mark reviewed, add to saved list, re-enrich, recheck website, recheck emails, merge duplicates, and open selected websites. | 3 (built) | Every action but "add to saved list" existed in the selection bar (`/api/v1/results/bulk`, `/api/v1/results/duplicates/merge`, `openSelectedWebsites`, `exportSelected`). **Added saved lists**: `web/result_lists.go` (`POST /api/v1/results/lists`) tags the hand-picked selection and pins a saved view to that tag; the control is hidden unless both halves can be stored. Tests: `web/result_lists_test.go`. |
| Copy selected domains, emails, phone numbers, addresses, or Maps URLs. | 2 → 1 | `app-results.js:copySelected` plus the `data-action="copy-selected"` buttons; asserted in `TestResultsTableExposesColumnAndSelectionMachinery`. |

### Record detail drawer

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Complete structured record, map location, source links, website preview or screenshot, social profiles, provenance, raw JSON, notes, tags, change history, and duplicate matches. | 3 (built) | Everything except a real map location existed in `partials/result_drawer.html`. **Added a Location card** with the parsed street/postal/country/plus code/coordinates and a same-origin lazy iframe onto Map Explorer narrowed to this business (`businessMapEmbedURL`, `/app/map?filter_field=id&filter_operator=eq&filter_value=…`), reusing the framing already permitted by `framableLocalPath`. No remote tiles, so the strict CSP is unchanged. A record without coordinates says so instead of framing an empty map. Tests: `TestBusinessDrawerCarriesTheCompleteRecord`, `TestBusinessDrawerWithoutCoordinatesSaysSoInsteadOfFramingAMap`. |

## 09 Advanced Filtering

### Filter operators

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| AND, OR, and nested groups. | 1 | `resultFilterGroupSQL` (`web/sqlite/results.go`), `validateResultFilterGroup` (`web/results_api.go`); `web/advanced_filters_test.go:TestParseResultSearchSupportsNestedAndLegacyORFilters`. |
| Contains, does not contain, starts with, ends with, equals, not equal, empty, and not empty. | 1 | `textFilterSQL`; `web/sqlite/advanced_filters_test.go`. |
| Numeric minimum, maximum, between, greater than, and less than. | 1 | `numericFilterSQL`; `web/sqlite/advanced_filters_test.go`. |
| Date ranges, boolean fields, category membership, and geographic radius/polygon filters. | 2 → 1 | Already implemented: `dateFilterSQL` over `updated_at`/`first_seen_at`/`last_seen_at`/`scraped_at`/`last_checked_at`; boolean `reviewed`/`claimed`; `category_member` via `json_each`; `bbox`, `distance`, and `polygonFilterSQL` with `within`. Covered by `web/sqlite/advanced_filters_test.go`; the filter-field picker in `pages/results.html` exposes each one. |

### Example reusable views

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Businesses without websites. | 1 | `starterResultViews` (`web/starter_content.go`); `web/reusable_views_test.go:TestStarterViewsCoverEverySpecificationExample`. |
| Businesses with an active website but no visible email. | 1 | "Active website, no email"; same test. |
| Highly rated businesses with low-quality websites. | 3 (built) | Added "Highly rated, low-quality website" (`rating >= 4.5` AND `website not_empty` AND `quality_score <= 60`); same test. |
| Businesses with phone but no website. | 1 | "Has phone, no website"; same test. |
| Businesses with email and LinkedIn. | 3 (built) | Added "Email and LinkedIn" (`email not_empty` AND `social eq linkedin`); same test. |
| Open listings with more than 50 reviews. | 1 | "50+ reviews, open"; same test. |
| New or changed businesses since the last scrape. | 3 (built) | Added "New or changed since the last scrape" (`change_status` in `new`/`updated`, the vocabulary the importer actually writes); `TestNewOrChangedStarterViewMatchesTheImportVocabulary`. |
| Permanently closed listings. | 1 | "Permanently closed listings"; same test. |

## 10 Deduplication Engine

### Exact matching keys

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Place ID / CID / Data ID / Normalized phone / Normalized website domain / Exact normalized address. | 1 | `web/resultimport/identity.go` builds all six `IdentityKey` kinds; `web/sqlite/entity_resolution.go` matches on them; `web/sqlite/entity_resolution_test.go` (`TestPlaceIDDriftAttachesRecordsBothKeysAndStaysIdempotent`, `TestChainSharedContactPointNeverAutoMerges`, …). |

### Fuzzy and composite matching

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| All five lines (similar name, name+postcode/city, name+proximity, similar address with coordinate proximity, shared phone/domain with a modified name). | 1 | `web/resultimport/similarity.go` and the candidate scoring in `web/sqlite/entity_resolution.go`; `web/sqlite/entity_resolution_test.go:TestNearDuplicateNamesBecomeReviewCandidateAndRespectRules`, `TestMissingIdentifiersAttachOnlyWhenCorroborated`, `web/sqlite/results_test.go:TestFuzzyDuplicatesBecomeReviewCandidatesWithoutAutomaticMerge`. |

### Duplicate review and merge

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Show raw count, unique count, duplicate candidates, exact auto-merged count, and items needing review. | 1 | `ResultOverview` (`web/sqlite/results.go`) rendered in the summary rail of `pages/results.html`. |
| Side-by-side comparison of conflicting records. | 1 | Duplicates tab of `partials/result_drawer.html` (this record vs the candidate, plus the matching evidence). |
| Keep both, merge, ignore match, or establish a permanent non-match rule. | 1 | `/api/v1/results/duplicates/{merge,keep-both,ignore}`; `insertNonMatchRule` writes the permanent `dedup_rules` row; `web/sqlite/duplicate_resolution_test.go:TestKeepBothRecordsAPermanentNonMatchRule`. |
| Choose preferred value by source confidence, recency, or completeness. | 3 (built) | Added `DuplicateDecision.FieldStrategy` with service-level validation (merge only), `web/sqlite/duplicate_preferred_values.go` applying it per field (name, category, address, phone, website), moving the winning value's provenance onto the surviving record, superseding the replaced one, and writing a `business_changes` row plus the audit detail. UI: the merge form in the drawer's Duplicates tab. Tests: `web/sqlite/duplicate_preferred_values_test.go`, `web/duplicates_strategy_test.go`. |
| Preserve all source queries, cells, timestamps, and historical values after merging. | 1 | `mergeBusinessInto` moves `business_sources` and folds `job_businesses` instead of deleting; `web/sqlite/duplicate_resolution_test.go:TestMergeDuplicateKeepsEvidenceAndHidesTheMergedRow`. |

## 15 Field Provenance and Auditability

| Checklist line | Class | Evidence / justification |
| --- | --- | --- |
| Source type: Google Maps, website homepage, contact page, about page, footer, structured data, or manual edit. | 3 (built) | The repository already wrote `google_maps_csv`, `website_<page kind>`, and `manual_edit` tokens, but the drawer printed the raw token. Added `web/provenance.go` (`ProvenanceSourceTypes`, `ProvenanceSourceTypeLabel`, `ProvenanceMethodLabel`) and used it in the drawer. `structured_data` is recorded as the extraction method (`web/enrichment/types.go:MethodStructuredData`) and now renders with its own label; `website_footer` is in the vocabulary and renders correctly, although the crawler classifies pages rather than page regions today (that extractor lives in section 12/13's `web/enrichment`, not this group's code). Test: `web/provenance_test.go`. |
| Source URL, source query, source grid cell, extraction timestamp, extraction method, and confidence. | 3 (built) | All six were stored (`field_provenance`) but the drawer table showed only source, method, confidence, and time. Added a "Query and cell" column and the input id to the source timeline in `partials/result_drawer.html`. Test: `TestBusinessDrawerCarriesTheCompleteRecord`. |
| Original value, normalized value, current preferred value, and previous values. | 1 | `field_provenance.original_value/normalized_value/preferred/superseded_at`, rendered in the drawer's provenance table; `web/sqlite/results_test.go:TestOlderImportCannotReplaceNewerPreferredValues`. |
| Manual edits should record the operator, date, and reason when local user accounts are enabled. | 1 | `web/sqlite/manual_edits.go` writes provenance + `business_changes` + `audit_logs` in one transaction; `web/sqlite/manual_edits_test.go:TestManualPhoneEditUpdatesColumnProvenanceChangeAndAudit`. |
| Exports may optionally include provenance columns or a companion JSON file. | 1 | `provenance_json`, `sources_json`, `changes_json` export columns plus `ExportBuildOptions.IncludeProvenance`; `web/export_builder_test.go`. |

## Counts

- Class 1: 20
- Class 2 (test added this pass, now verified): 4
- Class 3 (built this pass): 19
- Class 4: 0
- Class 5: 0

## Schema

Migration **15** `results-core-columns` — `ALTER TABLE businesses ADD COLUMN
user_reviews TEXT NOT NULL DEFAULT '[]'`. Additive, defaulted, applied once.

Two shared migration helpers were relaxed so reserved-but-gapped version numbers
stay legal, preserving their intent: `validateMigrationMetadata` and
`validateMigrationChecksums` now compare the recorded rows against the *declared*
migration set rather than assuming a gapless 1..N sequence
(`declaredMigrationVersions`), and `assertCurrentSchema` in
`web/sqlite/migrations_test.go` counts `len(schemaMigrations)` instead of
`currentSchemaVersion`.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test ./web/ -count=1` — pass (native).
- `go test -count=1 ./web/... ./runner/... ./gmaps/... ./exiter/... ./deduper/...`
  in `golang:1.26.6-trixie` — pass (the SQLite test binary trips Windows
  Defender's documented false positive natively).
- `node --check web/static/js/app-results.js` — pass.
- `gofmt` verified on LF-normalised copies of every touched Go file (the
  checkout is CRLF, so a bare `gofmt -l` flags every file).
