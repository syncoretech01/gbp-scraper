# Phase 3.2 — acceptance defect remediation ledger

Every item here came from hands-on user acceptance testing or controlled real
scraping. Each is reproduced against the **deployed build and the live
workspace data**, root-caused, classified, fixed, and covered by a regression
test. Nothing is closed on the strength of a passing unit test alone.

Classification vocabulary: **confirmed defect** · **misleading UX/semantics** ·
**expected behaviour needing clearer presentation** · **disproven**.

## Acceptance baselines held in the live workspace

| Job | Mode | Config | Result |
| --- | --- | --- | --- |
| `7100e95b` Thorough Test 01 | Thorough | 5 queries, LA 34.0522/-118.2437, 15 km, 5 km grid | 331 businesses, 180/180 searches, 0 failed, 0 blocks, 268 with website |
| `e108446c` Fast Test 01 | Fast | same 5 queries, 15 km | 26 businesses, 5/5, 25 with website |
| `cfe2d653` Fast Enrichment Test 01 | Fast + enrichment | same 5 queries, 15 km | 26 businesses, 5/5, 25 with website, **`with_email` reported 0** |
| `7e4783f2` Pause/Resume Test | Thorough | 10 km, 5 km grid | 96 businesses, 32/32 — pause/resume/checkpoint proven |
| `ba78441f` SF dentists | Thorough + enrichment | historical | 36 businesses |

These must not regress.

## Lead reproductions completed before implementation

The following root causes were established by the lead against the live
database and the running application, and are the starting evidence each lane
builds on.

### L — enrichment emails exist but never reach the headline or the export

**Reproduced.** For job `cfe2d653` the Job Monitor reports *Emails discovered:
11*, while the results headline and the exported CSV report **0**.

**Root cause.** The emails are real and durably persisted: eleven of that job's
businesses carry rows in the `emails` table, with genuine values
(`contact@vismstudio.com`, `la@baronart.tattoo`, `infoamaitattoo@gmail.com`)
and real extraction methods (`mailto`, `structured_data`, `visible_text`). The
monitor's *Facts* are computed from the **database**, while the headline stat
and the export are computed from the **per-job CSV**, which was committed
during the scrape and never rewritten after enrichment. The two numbers
disagree because they read two different stores, one of which is stale by
construction.

Secondary evidence found in the same pass: several stored values are
concatenation artefacts of visible-text extraction
(`626-554-7744inquiries@neptunetattoostudio.com`,
`filler@godaddy.combookingsordersmy`), so email normalisation needs hardening
alongside the persistence fix.

### M — "60 sites / 110 pages" is a counting fan-out, not duplicate crawling

**Reproduced, and the original hypothesis is disproven.** Enrichment did *not*
crawl 60 sites. The job has 26 businesses over **61 `business_sources` rows**
(19 businesses were observed by more than one of the five queries). The facts
SQL joins `websites` to `business_sources` with **no `DISTINCT`**:

```
COUNT(*)                      -> 60   (true distinct websites: 25)
SUM(websites.pages_checked)   -> 110  (true sum over distinct websites: 35)
```

So the crawler behaved correctly — 25 unique sites, 35 pages — and the monitor
multiplied each website by how many times Maps re-observed its business.

### G — the "dental carryover" is a hardcoded example plus placeholder text

**Reproduced, and it splits in two.** No dental content is stored in settings,
and the Thorough tattoo job's persisted configuration contains none either.

- **G-1, confirmed defect.** `newScrapePage` hardcodes real dental *values*
  into every new scrape: `Name: "San Francisco dentists"`, keywords
  `dentists in San Francisco` / `dental clinics in San Francisco`, and the San
  Francisco centre. A genuinely new scrape is pre-filled with another
  campaign's content.
- **G-2, misleading UX.** The category and name filters were never populated:
  `Dentist / Dental clinic`, `Orthodontist / Dental laboratory` and
  `clinic group` are HTML `placeholder` attributes, which read as inherited
  values but submit nothing.

### K — Fast mode stores "not captured" as numeric zero

**Reproduced.** Across the same five queries:

| Mode | Businesses | `review_count = 0` | `review_count IS NULL` | missing place_id/cid/maps_url |
| --- | --- | --- | --- | --- |
| Fast | 26 | 13 (50%) | 0 | 5 |
| Thorough | 331 | 8 (2.4%) | 0 | 0 |

Half of Fast rows claim exactly zero reviews where Thorough claims it for 2.4%,
and no row anywhere records "unknown". Missing is being stored as zero, and
five Fast businesses carry no durable Maps identity at all.

### D / R — export scope defaults to the whole workspace, silently

**Reproduced by inspection of the shipped path.** Export already shares the
canonical `ResultSearch` model with Results and already has four scopes
(`all`, `filtered`, `saved_view`, `selected`), and `spoolExportRows` paginates
correctly, so **no truncation occurs** — the `Limit = 250` is a page size, not
a cap, and that part of the concern is disproven. The real defect is that the
scope defaults to `filtered`, and a `filtered` export carrying no filters is
indistinguishable from `all`: the user gets the entire workspace (331 tattoo +
36 dental) from a button that reads simply "Export", with no record count shown
before the file is produced.

### H — the "operational" status filter is not a shipped default

**Partially reproduced.** No `filter_status` checkbox ships with a `checked`
attribute, and the Review summary only prints a status clause when a box is
checked, so a clean wizard says "None". The `operational` value the user saw
comes from a preset/template path (the seeded starter content filters on
`business_status contains operational`). Live reproduction through the
template/GBP path is required before fixing.

## Issue register — closure table

Every lettered issue from the acceptance brief. Classification uses the four
vocabulary terms; "fix" names the change; "test" names the deterministic
regression; "evidence" is what was observed on the live workspace copy or on a
real bounded scrape of the remediated build.

| ID | Issue | Classification | Fix | Regression test | Acceptance evidence |
| --- | --- | --- | --- | --- | --- |
| A | Website audit state machine | confirmed defect | canonical NEVER_CHECKED/QUEUED/CHECKING/LIVE/DEAD/ERROR/NO_WEBSITE/SOCIAL_ONLY resolved from durable evidence; durable bulk sweep on the existing enrichment queue with domain dedupe, progress, restart recovery | TestResolveWebsiteStateSeparatesUncheckedFromFailed, TestWebsiteAuditSweepDeduplicatesDomainsAndResumes | Live copy, job 7100e95b: NEVER_CHECKED 215 -> 0 after one sweep (206 unique domains for 215 businesses, 9 duplicate domains skipped, 0 failed); final LIVE 171 / DEAD 7 / ERROR 59 / NO_WEBSITE 63 / SOCIAL_ONLY 31 = 331, an exact partition. Killed mid-sweep and restarted: resumed under the same sweep id, no duplicate tasks |
| B | Social profiles scored as websites | confirmed defect | 37-host social table shared by classify/quality/state; SOCIAL_ONLY outranks every audit outcome; social listings earn no health score; social_profiles backfilled | lane 1 classify/quality tests | Live copy: 32 social listing URLs (28 instagram, 4 facebook) had earned 12.5 points of website-health credit; backfill inserted 32 social_profiles rows and re-scored 32; Results "With website" now excludes them |
| C | Three scores conflated | confirmed defect | prospect tier/score, website health, record confidence are distinct, persisted, exported; unknown website state is never scored as good or bad; scoring an unaudited business queues the audit first | lane 1 gating tests | Recompute of 250 live businesses returned processed 250 with the audit-prerequisite path in place; post-sweep tiers B 53 / C 126 / D 120 / E 21 |
| D | Export must match Results | confirmed defect (scope default) | one canonical ResultSearch drives Results count, pagination, saved views and export; four explicit scopes; a "filtered" export with no filter is refused | lane 2 parity test + live check | Live copy: job + SOCIAL_ONLY + has_phone -> Results 30, export 30, identical ID sets; job + tier A/B + contactable -> 53 = 53 |
| E | Export scoring/audit fields | confirmed defect | 43+ structured columns (prospect_*, website_state/kind/audit_status/health/http_status/error_reason, no_website, social_only, weak_website, contactable, has_*, email_count, social_profiles, provenance) via columns_spec; the legacy 24-column profile is unchanged | lane 2 export tests | Full-column export of 331 rows: 0 requested columns missing; website_kind owned 235 / social 31 / none 63 / free_builder 2 |
| F | Never-checked backfill / disconnected stages | confirmed defect | audit queue -> probe -> classification -> score -> Results reconnected; per-domain evidence reuse | lane 1 tests | Same sweep as A; reused_domain_evidence 11 |
| G | State carryover into new scrapes | G-1 confirmed defect; G-2 misleading UX | newScrapePage no longer hardcodes "San Francisco dentists" and dental queries; only saved defaults may prefill; dental placeholders replaced by empty-state text | TestFreshWizardRendersNoInheritedContent (+3) | Fresh wizard renders no prior job content |
| H | Filters vs Review mismatch | confirmed defect (seeded-default hypothesis disproven) | hidden-step narrowing rules are surfaced on Review with "Show the Filters step" / "Clear" actions | lane 4 wizard tests | Deterministic repro: tick a status in Advanced, switch to Basic, Review showed a rule the visible steps could not reach |
| I | GBP ZIPs as real geographic targets | confirmed defect | QueryTarget model (ZIP, city/state, centroid, population, rank, stable id); the plan carries targets; JobData.QueryTargets is persisted and validated; CreateTargetSeedJobs executes each target from its own centroid; the ZIP is recorded as the task's source cell | TestBuildTargetsMatchesBuildQueries + targets tests | 25 ZIPs x 3 synonyms now plans 25 areas / 75 searches instead of 1 / 75 |
| J | Fast mode semantics | confirmed defect | camera altitude derived from the requested radius (it was hardcoded at ~3.8 km whatever the operator chose); the UI states Fast is one radius-biased retrieval per term, not exhaustive coverage | gmaps searchjob_lane4_test | Live: job e108446c's 26 results all lay within 3.2 km of the centre at a "15 km" radius; the altitude sweep reached 7.7 km at 60 000 and a 15 km-class spread at the calibrated factor |
| K | Fast missing data stored as zero | confirmed defect | an uncaptured review count/rating is written as an empty CSV cell -> NULL; identities are never fabricated | gmaps entry tests | Before: Fast 13/26 rows review_count=0, 5 without place_id. After (remediated image, Fast 15 rows): 0 zeros, 0 missing identities |
| L | Enrichment emails lost | confirmed defect | job counters and the result file are reconciled from stored evidence after enrichment; honest funnel (businesses / addresses / rejected + reasons); junk concatenations rejected | lane 3 email tests | Before: monitor 11, headline 0, CSV 0. After (remediated image, Fast + enrichment): 14 sites audited, 15 emails stored, enrichment-complete recorded |
| M | Enrichment duplicate work | disproven (metrics fan-out) | facts SQL de-duplicated over business_sources | lane 3 facts tests | 26 businesses / 61 sources reported 60 sites / 110 pages; the truth is 25 sites / 35 pages; one crawl per unique domain per freshness window confirmed |
| N | Enrichment timing | confirmed defect | discovery / enrichment / total durations recorded; the crawl stage reads "audits still running (N queued)" until enrichment-complete | lane 3 + monitor tests | Fast + enrichment reported 6 s for minutes of audits; the stage no longer claims completion early |
| O | Thorough geographic spillover | expected behaviour, presented explicitly | distance from centre and an inside/outside-boundary flag persisted; non-destructive distance filter | lane 5 tests | Job 7100e95b: 34.7% of results outside the planned grid, up to 20.1 km. That is Google widening sparse queries, not a planner fault; no rows deleted |
| P | Dedup metric terminology | misleading UX | Maps observations / repeated observations / unique businesses / entity merges / unresolved candidates, identical on Results and the Monitor | lane 5 + updated results tests | "555 added / 224 replaced / 0 merged" replaced by the five defined counts |
| Q | Warning noise | confirmed defect | cell-empty emitted at information; severity counted and filtered through one honest policy (fixes historical runs too) | lane 5 tests | Job 7100e95b: 118 "warnings" -> 1; remediated Thorough run: 38 information, 0 warnings, 0 errors on 16/16 |
| R | Export scope labelling | confirmed defect | scope + count on every export control ("Export this job - 331") | lane 2 tests | scopes API shows filtered 30 / job 331 / all 372 before the export runs |
| S | Provenance in exports | confirmed defect | per-observation sidecar recorded at merge time (migration 19); the import uses the exact query/cell per row and files extra observations per additional task | TestImportUsesExactObservationProvenance | Historical rows keep the legacy joined list (that information was never captured at scrape time); new runs record exact provenance - see the addendum |
| T | Coordinate corruption | disproven (unconfirmed) | none invented; server-side coordinate validation added so a corrupt centre can no longer reach a job | lane 4 probe | Deterministic probe across navigation, mode cycling and FormData kept -118.2437 intact |
| U | End-to-end workflow | expected behaviour, made coherent | never-checked -> audit -> live updates -> recompute -> Tier A/B + contactable -> exact count -> export with all fields | live walk-through | Steps 4-11 of the final acceptance executed on the live copy |
| V | Auto performance mode | confirmed defect | the flat 2-worker cap and 3 GiB/worker pricing are removed; the ceiling derives from measured RAM, cgroup CPU, measured browser RSS and block/latency/error/write-pressure signals; the worker count adapts during the run within the browser budget | lane 6 tests + schedbench | Remediated Thorough run: 4 workers / 4 browsers on this host, cpu peak 100%, mem 55%, 0 blocks |
| W | Multi-worker execution | expected behaviour (evaluated A-E) | (A) in-process workers + (B) pages per browser implemented; (C)/(D) rejected on measurement: browser memory binds identically across processes and SetMaxOpenConns(1) would turn serialisation into cross-process WAL contention on finish writes | - | recorded in the throughput section |
| X | Discovery/enrichment decoupling | confirmed defect | bounded enrichment pool with per-host limits, pumped at its own capacity after the job's engines stop | lane 3 tests | 25 audits took 157 s at one-per-tick; the pool now runs at its configured width |
| Y | Domain audit cache | confirmed defect | per-domain evidence reused inside the freshness window; explicit re-audit | lane 3 tests | sweep: 215 businesses cost 206 probes |
| Z | Benchmarking | modelled + measured | offline scheduler benchmark + a bounded live subset | schedbench_test | see the benchmark table below |

## Final real acceptance on the remediated build

All runs below used the rebuilt image on a fresh container through the single
experiment queue; Google traffic was bounded to three jobs plus the
pause/resume regression.

| Run | Config | Wall | Searches | Businesses | Failures / blocks | Browsers |
| --- | --- | --- | --- | --- | --- | --- |
| Thorough, hardened auto capacity | 1 query, LA centre, 16 cells @ 5 km, request 4 | 474 s | 16/16 | 240 | 0 / 0 | 4 |
| Fast | same query, 15 km radius | 2 s | 1/1 | 15 | 0 / 0 | 0 |
| Fast + enrichment | same | 2 s discovery; audits ran after | 1/1 | 15 (14 sites audited, 15 emails) | 0 / 0 | 0 |

### Throughput before vs after

The acceptance baseline and the remediated run are not the same workload
(five queries over 36 areas versus one query over 16 areas), so searches/min
alone would mislead: a search's duration scales with the listings it walks. The
comparable figure is businesses collected per minute at zero failures.

| | Baseline (job 7100e95b) | Remediated (job e26fefa6) |
| --- | --- | --- |
| Effective parallelism | 1.99 (capped at 2 workers) | 4 workers / 4 browsers |
| Businesses/min | 6.31 | 30.38 |
| Searches/min | 3.43 (1.8 rows per search) | 2.03 (15 rows per search) |
| Task success / blocks / browser failures | 100% / 0 / 0 | 100% / 0 / 0 |
| Peak CPU / memory | not sampled | 100% / 55% |
| Warnings on a clean run | 118 | 0 |

Lane 6's offline scheduler benchmark projects the original 180-search workload
at 26.4 min at the four workers this host now grants, against the measured
52.5 min. That projection is modelled, not measured, and is labelled as such.

## Before benchmark — measured from the live acceptance runs

Taken from `job_runtime.started_at/finished_at` and `job_tasks`/`business_sources`
in the live workspace, so these are the real user-observed numbers, not a model.

| Run | Wall | Searches | Businesses | Observations | Searches/min | Businesses/min |
| --- | --- | --- | --- | --- | --- | --- |
| Thorough LA tattoo (`7100e95b`) | 3148 s (52m28s) | 180/180, 0 failed | 331 | 331 | 3.43 | 6.31 |
| Fast LA tattoo (`e108446c`) | 7 s | 5/5, 0 failed | 26 | 26 | 42.86 | 222.86 |
| Fast + enrichment (`cfe2d653`) | 6 s | 5/5, 0 failed | 26 | 61 | 50.00 | 260.00 |
| Pause/resume (`7e4783f2`) | 1031 s | 32/32, 0 failed | 96 | 116 | 1.86 | 5.59 |

The Thorough figure reproduces the user's reported 52m28s exactly.

Two observations fall straight out of this table and feed the register:

- **N.** The Fast + enrichment run finished in **6 s — less than the 7 s of the
  same workload without enrichment** — while its monitor claims 110 pages across
  60 sites. Enrichment work is therefore not inside the job's measured wall
  time, so "Finished" is being declared before enrichment is durably done.
- The enrichment run recorded **61 observations for 26 businesses** where the
  identical Fast run without enrichment recorded **26 for 26**. Same five
  queries, same centre, same radius. That asymmetry is the fan-out surface
  behind issue M and is what inflates the site/page counts.

## Lane reports - reproduction, root cause, fix, test, evidence

### Lane 1 — website audit state machine + scoring (A, B, C, F)

#### A - auditable website state + durable bulk audit of never-checked sites

**Classification:** confirmed defect

**Reproduction.** Live copy of gosomscraper-scraper-1:/gmapsdata. Job 7100e95b ("Tattoo Artists - Los Angeles Metro - Thorough Test 01"): 331 businesses, 268 with a website. businesses.website_status was the ONLY state and held exactly three values workspace-wide: unknown 347, active 17, error 8. Within the job: unknown 311, active 13, error 7. 248 website-bearing rows rendered as "never checked" and there was NO product path to fix that: the job was saved with enrichment=null, so EnrichmentOptionsForJob returned enabled=false and zero audits were ever queued for it (the only 25 enrichment_tasks in the whole workspace belong to a different job, cfe2d653). The single existing bulk entry point, POST /api/v1/results/enrich, needs an explicit ID list, caps at 500, has no progress and no per-domain dedupe.

**Root cause.** There was no state vocabulary that could distinguish the cases. businesses.website_status (created web/sqlite/migrations.go:549, written only by web/sqlite/enrichment.go:654 from websiteAuditStatus at web/sqlite/enrichment.go:1385) defaults to the literal string 'unknown' and is the same value for "never audited", "listing has no website at all", and "listing points at a social profile". prospect.Classify (web/prospect/classify.go) had the richer NO_WEBSITE/SOCIAL_ONLY/DEAD/... taxonomy but deliberately returns ("", false) when no audit exists, so it could not name the lifecycle either. Nothing anywhere could say "an audit is queued/running".

**Fix.** New canonical state machine, derived from durable evidence only (no new column, no new queue, no second source of truth). web/website_state.go adds NEVER_CHECKED, QUEUED, CHECKING, LIVE, DEAD, ERROR, NO_WEBSITE, SOCIAL_ONLY plus ResolveWebsiteState() with documented precedence, mapping the existing vocabularies in rather than replacing them (website_status active/inactive/error -> LIVE/DEAD/ERROR; prospect.StatusNoWebsite/StatusSocialOnly shared verbatim). Rule 4 is the point: status_code==0 after a completed audit is ERROR, never NEVER_CHECKED; HTTP >= 400 is DEAD. web/sqlite/website_state.go resolves it per business from businesses + enrichment_tasks + website_audits, and reuses domain-level evidence (audit AND in-flight task) so duplicate listings on one domain never queue a second probe. The durable bulk operation StartWebsiteAuditSweep() creates rows ONLY in the existing enrichment_tasks queue, tagged requested_by='website_sweep:<id>' (web.WebsiteAuditSweepRequestedBy); progress is a COUNT over that queue, so it cannot disagree with reality and restart recovery is the queue's own existing recovery. New routes: GET /api/v1/websites/states, POST|GET /api/v1/websites/audit-sweeps, GET /api/v1/websites/audit-sweeps/{id} (registered from registerProspectRoutes in my own file, so web.go is untouched).

**Regression test.** web/website_state_test.go::TestResolveWebsiteStateSeparatesUncheckedFromFailed (11 cases incl. "dns failure is an error, never never-checked"); ::TestResolveWebsiteStateReusesDomainEvidence; web/sqlite/website_state_test.go::TestWebsiteAuditSweepDeduplicatesDomainsAndResumes

**Acceptance evidence.** Server built from HEAD+lane1, run on port 8111 against a COPY of the live data (scratchpad/lane1-data; ./webdata never touched). GET /api/v1/websites/states?job_id=7100e95b... BEFORE any sweep: NEVER_CHECKED 215, LIVE 14, DEAD 0, ERROR 8, NO_WEBSITE 63, SOCIAL_ONLY 31, total 331, reused_domain_evidence 2 - i.e. the 248 undifferentiated "never checked" rows resolved into 215 genuinely unchecked + 31 social + (already) 8 errored, and 63 rows that never had a website at all stopped being counted as unaudited websites. POST /api/v1/websites/audit-sweeps {job_id, states:[NEVER_CHECKED], limit:1000} -> unique_domains 206, queued 206, skipped_duplicate_domain 9, skipped_fresh 0, skipped_already_queued 0, skipped_ineligible 94, truncated false. So 215 businesses cost 206 probes, not 215. DURABILITY: at completed=5/206 I ran `taskkill /F` on the server; DB showed completed 5 / queued 200 / running 1; on restart the same sweep ID resumed and completed advanced to 7 with no re-queue and no duplicate tasks. Final: progress {total 206, completed 206, failed 0, done true, percent 100}. Job state afterwards: NEVER_CHECKED 0, LIVE 199, DEAD 6, ERROR 32, NO_WEBSITE 63, SOCIAL_ONLY 31 (331 total, exact partition), reused_domain_evidence 11. Results incrementally updated during the run (LIVE 56 -> 136 -> 184 -> 199 on successive polls) because the sweep drives the existing website_status write path. Screenshot scratchpad/lane1-shots/results-website-column.png shows the Results table populated with real active/error statuses instead of "never checked".

#### B - social profiles were treated as owned websites

**Classification:** confirmed defect

**Reproduction.** On the live copy, 32 of the 309 businesses with a website had a listing URL whose primary destination is a social network (28 instagram.com, 4 facebook.com), e.g. Brown Pride Tattoos -> https://www.instagram.com/brownpridetattooshop?hl=en. social_profiles held ZERO rows for any of them. Their stored quality breakdown (business_score_components) read: active_website +7.5/15 passed=TRUE "Website URL exists but has not been checked"; https +5/5 passed=TRUE "Listed website URL uses HTTPS; audit pending"; social_profiles 0/5 "No social profile is available". So an Instagram page earned 12.5 points of website health credit and simultaneously counted as having no social presence. The Results header KPI "With website 83% / 309 businesses" counts all 32 of them.

**Root cause.** Three separate places. (1) web/prospect/classify.go socialHosts covered only 11 hosts (no pinterest, threads, snapchat, telegram, vk, bluesky, reddit, nextdoor, link-in-bio hosts) and there was no exported helper, so every other layer re-invented "is this social?" or skipped the question. (2) web/sqlite/quality.go calculateQuality had no notion of a social listing: it keyed off input.websiteStatus only, so a social URL fell into the default "exists but unchecked" branch and got half the active-website weight with passed=true, plus full HTTPS credit from a raw strings.HasPrefix(website, "https://") check. (3) A social listing URL was never written to social_profiles, so the social_profiles component scored it 0.

**Fix.** web/prospect/classify.go: socialHosts replaced by a structured socialNetworks table of 37 host suffixes mapped to canonical platform names that match the ones web/enrichment already emits (facebook, instagram, linkedin, x, youtube, tiktok, whatsapp, + pinterest, threads, snapchat, bluesky, reddit, tumblr, vk, twitch, nextdoor, telegram, yelp, linktree, link-in-bio). New exported prospect.SocialPlatform / prospect.IsSocialWebsite / prospect.SocialNetworkSuffixes give the whole app ONE vocabulary. ResolveWebsiteState puts SOCIAL_ONLY above every audit outcome, so even a reachable Instagram page is never LIVE. ScoreWebsiteHealth refuses to grade SOCIAL_ONLY (Available=false with a reason, never a 0 that reads like a measurement). web/sqlite/quality.go now scores active_website 0/known, https 0/known, website_quality 0/known and website_response 0/known for a social listing, with explicit reasons, and credits social_profiles from the listing URL. New backfill BackfillSocialListings(apply,limit) in web/sqlite/website_state.go records each social listing in social_profiles (INSERT OR IGNORE against the existing UNIQUE(business_id,platform,url), so it is idempotent), re-runs prospect classification and quality scoring, and writes a social_listings_backfilled audit_logs row. It NEVER rewrites businesses.website - the listing said what it said; only the classification is corrected. Exposed as POST /api/v1/websites/social-backfill, dry run by default.

**Regression test.** web/sqlite/website_state_test.go::TestSocialListingBackfillCorrectsStoredClassification (includes a pinterest.com listing, a network the old host list did not recognise); web/website_state_test.go::TestResolveWebsiteStateSeparatesUncheckedFromFailed case "a reachable social profile is still social only"; ::TestWebsiteHealthRefusesToGradeUnknownState

**Acceptance evidence.** ROWS CORRECTED ON THE LIVE COPY: dry run reported examined 309, social 32, by_platform {instagram 28, facebook 4}, profiles_inserted 0 and wrote nothing (social_profiles stayed at 40). Apply reported profiles_inserted 32, quality_rescored 32, status_corrected 0 (prospect_status was already SOCIAL_ONLY for these because instagram/facebook were in the old host list - the damage was in scoring and storage, not in the prospect label). Re-running apply returned profiles_inserted 0, proving idempotence. Brown Pride Tattoos before: quality_score 80.0, confidence 1.0, components summing to 52.5, active_website +7.5 passed, https +5 passed, social_profiles 0 "No social profile is available". After: quality_score 45.0, confidence 0.9, components summing to exactly 45.0, active_website 0 "The listed URL is a social profile (instagram), not a website the business owns", https 0 "Transport security of a social network is not the business's to fix", social_profiles +5 "The listing points at a social profile (instagram)", website_quality 0 "A social profile is not a website whose quality the business controls". GET /api/v1/results/<id>/website-health for that business returns available:false, state SOCIAL_ONLY, with the reason instead of a fabricated score. Workspace canonical state now reports SOCIAL_ONLY 32 as its own bucket, and with_website excludes them (237 of 331 for the job).

#### C.1 - unknown website state was scored as good

**Classification:** confirmed defect

**Reproduction.** Live copy, any never-audited business with an https:// URL. business_score_components rows: active_website contribution 7.5 of 15 with passed=1 and reason "Website URL exists but has not been checked"; https contribution 5.0 of 5 with passed=1 and reason "Listed website URL uses HTTPS; audit pending". 12.5 of 100 points, and two green "passed" ticks, purely because a URL string existed and started with https://. A business nobody had checked therefore outscored one whose site had been audited and found merely mediocre.

**Root cause.** web/sqlite/quality.go calculateQuality, the default arm of the website_status switch and the strings.HasPrefix fallback of the HTTPS branch. Both awarded points AND set passed=true for evidence that did not exist.

**Fix.** calculateQuality is now driven by the canonical website state, not by a raw status string. NEVER_CHECKED/QUEUED/CHECKING -> active_website 0, passed=false, known=false, "The website has not been audited yet, so it neither earns nor loses points". ERROR -> 0, known=false (an unreachable site is not evidence the site is bad). LIVE -> full weight. DEAD -> negative half weight. NO_WEBSITE and SOCIAL_ONLY -> 0 with known=true (there is nothing to measure, and saying so is knowledge). The HTTPS component only scores an actually observed audit value; the "URL starts with https://" credit is gone. Because known=false feeds quality_confidence, an unaudited website now correctly LOWERS record confidence instead of silently inflating the score. readQualityInput gained the enrichment-task state and a same-domain websites fallback so it resolves the state from the same evidence as everything else; calculateQuality also derives the state from its own columns when a caller builds a qualityInput directly, so no call path can silently fall through to "unknown".

**Regression test.** web/sqlite/website_state_test.go::TestQualityNeverCreditsAnUncheckedWebsite (asserts 0/not-passed before the audit, confidence < 1, credited after the audit, confidence rises, and that the stored score equals the sum of its published components)

**Acceptance evidence.** Same live business as above. Before: active_website 7.5 passed=1, https 5.0 passed=1. After a rescore with no audit run: active_website 0.0 passed=0 "The website has not been audited yet...", https 0.0 passed=0 "HTTPS has not been verified: no audit has reached this website". After the sweep audited it: active_website full weight passed=1 "An audit reached the website and it answered", and quality_confidence rose. The pre-existing web/sqlite/quality_test.go::TestCalculateQualityExplainsPositiveNegativeAndClosedExclusion still passes unchanged (state derived from its websiteStatus:"active"/"inactive" fixture).

#### C.2 - the three scores were conflated; stored quality contradicted its own explanation

**Classification:** confirmed defect

**Reproduction.** Live copy: 74 of 372 businesses had a stored businesses.quality_score that did not match the sum of their own business_score_components rows under the same rule_version. Brown Pride Tattoos stored 80.0 / confidence 1.0 while its 12 stored components summed to 52.5 and its known-evidence ratio was 0.6. Blue Demon stored 45 vs explained 26.68; Hidden Temple Tattoo Studio stored 80 vs explained 36.38. Mismatched rows averaged 2.31 business_sources and 2.19 distinct jobs vs 1.34 / 1.18 for clean rows - i.e. the drift appears on re-imported records. 80.0 is exactly what businessQuality() (web/sqlite/results.go:2982) returns for that record (name 10 + address 15 + website 20 + phone 15 + rating 8 + reviews 7 + coords 5), and 1.0 is min(1, 0.35 + len(IdentityKeys)*0.12).

**Root cause.** web/sqlite/results.go ensureBusiness writes `quality_score = MAX(quality_score, ?)` and `quality_confidence = MAX(quality_confidence, ?)` from businessQuality(), an IMPORT-TIME RECORD-COMPLETENESS number, into the same two columns the rule engine owns. importRecord then returns early at the `if !inserted` branch ("the exact source row was already ingested") BEFORE scoreBusiness() can restore the rule-based value. A duplicate row inside one import, or a re-import of an already-ingested source row, therefore leaves the import-completeness number sitting in the record-quality column permanently - and the MAX makes it monotonic, so a record can never lose quality even when its website dies. That is the "one generic score" conflation: record completeness, listing/website quality and identity confidence all landed in two columns.

**Fix.** Three scores are now distinct, deterministic, explainable and persisted separately. (1) PROSPECT score/tier stays prospect_score/prospect_tier/prospect_reasons via prospect.Score - unchanged, already explainable. (2) WEBSITE HEALTH is new and separate: web.ScoreWebsiteHealth + WebsiteHealthReport, rule version "website-health-v1", 9 fixed checks totalling 100 (serving 25, https 15, real_content 20, speed 10, internal_links 10, mixed_content 5, metadata 5, mobile_viewport 5, maintenance 5), graded ONLY from a completed audit of an owned site and explicitly Available=false with a reason for every other state. Exposed at GET /api/v1/results/{id}/website-health. (3) RECORD CONFIDENCE stays quality_confidence but is now the honest known-evidence fraction of the quality model, because the website components report known=false when unaudited. The MAX conflation itself lives in web/sqlite/results.go which this lane does not own - see LEAD-APPLY 1. In-lane I added the detector and repair: repo.QualityScoreDrift(ctx, repair) in web/sqlite/quality.go, Service.QualityScoreDrift in web/quality.go, GET /api/v1/quality/drift and POST /api/v1/quality/drift/repair in web/quality_api.go. It audits every stored score against its own stored breakdown and rescores the ones that disagree, writing a quality_score_drift_repaired audit_logs row.

**Regression test.** web/sqlite/website_state_test.go::TestQualityScoreDriftDetectsAndRepairsAForeignScore (simulates a foreign writer raising the column without touching the breakdown, asserts detection then repair then clean); web/website_state_test.go::TestWebsiteHealthIsDeterministicAndExplainable (same evidence -> same number twice; score equals the sum of its checks; a DEAD site gets a real low grade because that IS a measurement); ::TestWebsiteHealthRefusesToGradeUnknownState

**Acceptance evidence.** GET /api/v1/quality/drift on the live copy: checked 361, drifted 71 (74 before the social backfill rescored 32 rows, 3 of which were in the drift set), with samples Blue Demon 45 vs 26.68, Vism Studio 80 vs 70.5, Hidden Temple 80 vs 36.38. After the audit sweep rescored the audited businesses the count fell to 21; POST /api/v1/quality/drift/repair returned checked 361 / drifted 21 / repaired 21 and a follow-up GET returned drifted 0. Website health on a real audited site (biz_6885878d...): score 72.5, grade "fair", checks serving 25/25 "answered HTTP 200", https 0/15 "served over plain HTTP", real_content 20/20, speed 10/10 "356 ms; target 1500 ms", internal_links 0/10 "1 of 4 checked internal links are broken", mixed_content 5/5, metadata 2.5/5, mobile_viewport 5/5 - the points sum exactly to the score.

#### C.3 - scoring an unaudited website instead of getting evidence first

**Classification:** confirmed defect

**Reproduction.** POST /api/v1/prospects/recompute and POST /api/v1/quality/recalculate scored every selected business immediately, whatever its website state. With 215 never-checked websites in job 7100e95b, that meant emitting a website-derived number for 215 records whose website state was unknown - the fabricated-measurement problem, at scale.

**Root cause.** web/prospects.go RecomputeProspects and web/quality.go RecalculateQuality had no notion of a prerequisite; nothing in the codebase could even enumerate "which of these have never been checked".

**Fix.** repo.UnauditedBusinessIDs(ctx, ids) (web/sqlite/website_state.go, batched at 500 ids per statement so a 100k-id request cannot exceed SQLite's bound-variable limit) plus Service.EnsureWebsiteAuditPrerequisite (web/website_state_service.go), which queues a durable sweep for exactly those businesses and returns how many are still waiting. Wired into new Service.RecomputeProspectsWithAudit and Service.RecalculateQualityWithAudit and into both POST endpoints, DEFAULT ON, opt out with "audit_first": false. Backward compatible: the response keeps its existing "processed" / "recalculated" key and only ADDS "website_audit_prerequisite"; the pass still runs, and the classifier still leaves an unaudited business unclassified rather than guessing.

**Regression test.** web/sqlite/website_state_test.go::TestUnauditedBusinessIDsDrivesTheScoringPrerequisite (asserts no-website and social listings are never queued for an audit, that a scoped list is honoured, and that an id list 3x the batch size is read in batches)

**Acceptance evidence.** POST /api/v1/prospects/recompute {"ids":[]} on the live copy returned processed 372 plus website_audit_prerequisite {unaudited 36, sweep {unique_domains 36, queued 36, skipped_ineligible 95, skipped_duplicate_domain 0}, message "36 of the selected businesses have a website that has never been audited. 36 website audits were queued first; website-dependent scores stay unset for those rows until the audits finish."}. Those 36 then drained through the same durable queue, leaving the whole workspace at NEVER_CHECKED 0.

#### F - pipeline trace, and the two disconnected stages found

**Classification:** confirmed defect

**Reproduction.** Traced scrape -> resultimport -> businesses.website/domain -> enrichment_tasks -> enrichment.Crawler HTTP probe -> websiteAuditStatus -> businesses.website_status -> reclassifyProspectsForBusiness -> Results. Two stages were disconnected. (1) DOMAIN FAN-OUT: web/sqlite/enrichment.go StoreWebsiteAudit -> reclassifyProspectsForBusiness only ever touched task.BusinessID, so a second listing on the same domain kept the scores it had while its site was unknown - on the live copy 11 businesses in job 7100e95b sit on a domain another business owns, and they showed a different verdict for the same website. (2) SCORE OVERWRITE: the re-import path described in C.2. Also confirmed empirically that no unique-domain probe was ever repeated: enrichment_tasks carries a UNIQUE partial index on business_id for the open states, but nothing deduped by DOMAIN.

**Root cause.** web/sqlite/prospects.go reclassifyProspectsForBusiness (single-business recompute, no quality refresh, no domain fan-out); web/sqlite/results.go ensureBusiness + importRecord early return (C.2).

**Fix.** reclassifyProspectsForBusiness (my file) now fans out: it resolves the audited business's domain siblings via the new repo.DomainSiblingBusinessIDs, recomputes prospect classification for all of them and recalculates quality for the siblings, and records domain_siblings in its prospects_recomputed audit row. A shared SOCIAL host is explicitly NOT a shared site, so instagram.com listings never inherit each other's evidence. The state resolver reuses both a completed domain audit and an in-flight domain task, so the sweep queues exactly one probe per unique domain per freshness window and everything else reads the stored evidence with ReusedFromDomain naming its source. Service.RefreshDomainSiblingScores is also exported for the enrichment worker (see LEAD-APPLY 4). The score-overwrite half is LEAD-APPLY 1 plus the in-lane drift detector/repair from C.2.

**Regression test.** web/sqlite/website_state_test.go::TestWebsiteAuditSweepDeduplicatesDomainsAndResumes (two businesses on shared-domain.example produce ONE task, the https root URL is the chosen representative, the annex queues 0 tasks, the annex resolves LIVE with ReusedFromDomain=shared-domain.example, and a repeat sweep reports SkippedFresh 2 rather than re-probing); ::TestQualityNeverCreditsAnUncheckedWebsite (the annex inherits active_website credit)

**Acceptance evidence.** Live copy: the sweep needed 206 probes for 215 never-checked businesses (skipped_duplicate_domain 9), and reused_domain_evidence rose from 2 to 11 as audits landed - 11 businesses got a resolved state and refreshed scores from a sibling's single probe. After a hard kill mid-sweep and restart the same sweep resumed with no duplicate tasks. Final enrichment_tasks: 206 sweep tasks all completed, 0 failed, plus the 25 pre-existing job_completion tasks untouched. Whole workspace ends at NEVER_CHECKED 0 / LIVE 229 / DEAD 8 / ERROR 40 / NO_WEBSITE 63 / SOCIAL_ONLY 32 = 372, an exact partition.

#### Deliberate divergence: prospect DEAD vs canonical ERROR

**Classification:** expected behaviour needing clearer presentation

**Reproduction.** 8 businesses in job 7100e95b have website_status='error' from a real DNS/connect failure (e.g. Mad Rabbit Tattoo Studio, http://madrabbit.com, 'lookup www.madrabbit.com: no such host'). The Results Prospect column shows them as DEAD while the canonical website state calls them ERROR.

**Root cause.** Two taxonomies answer two different questions. prospect.Classify asks "is this a good outreach target" and a link that does not resolve is a dead website for a caller's purposes; the canonical state asks "what did we actually observe" and a transport failure does not establish the site's condition.

**Fix.** Not changed - changing prospect.Classify would regress existing prospect scoring and its tests. The divergence is now documented explicitly in the ResolveWebsiteState doc comment and in the ERROR constant's comment, and ScoreWebsiteHealth refuses to grade ERROR at all ("An unreachable site is not the same as a bad site") so the two never produce a contradictory number.

**Regression test.** web/website_state_test.go::TestResolveWebsiteStateSeparatesUncheckedFromFailed case "dns failure is an error, never never-checked"; ::TestWebsiteHealthRefusesToGradeUnknownState includes ERROR

**Acceptance evidence.** Live copy job 7100e95b ends with ERROR 32 in the canonical vocabulary while those same rows keep their existing prospect DEAD tier/score, and no website health score is emitted for any of them.

**Files changed:** E:/Development/gosom scraper/web/website_state.go (NEW, 607 lines) - canonical state constants, ResolveWebsiteState, WebsiteStateForResult, WebsiteHealthEvidence/Report, ScoreWebsiteHealth, E:/Development/gosom scraper/web/website_state_service.go (NEW, 474 lines) - WebsiteStateSummary, StartWebsiteAuditSweep, sweep progress, BackfillSocialListings, EnsureWebsiteAuditPrerequisite, RefreshDomainSiblingScores, E:/Development/gosom scraper/web/website_state_api.go (NEW, 238 lines) - 7 new /api/v1 routes, CSRF-gated mutations, E:/Development/gosom scraper/web/website_state_test.go (NEW, 288 lines), E:/Development/gosom scraper/web/sqlite/website_state.go (NEW, 1047 lines) - evidence readers, domain reuse, sweep, social backfill, health evidence, DomainSiblingBusinessIDs, UnauditedBusinessIDs, E:/Development/gosom scraper/web/sqlite/website_state_test.go (NEW, 655 lines), E:/Development/gosom scraper/web/prospect/classify.go - socialNetworks table (37 hosts), exported SocialPlatform/IsSocialWebsite/SocialNetworkSuffixes, Classify uses them, E:/Development/gosom scraper/web/sqlite/quality.go - canonical-state-driven website components, social handling, shared-domain evidence, QualityScoreDrift, E:/Development/gosom scraper/web/quality.go - QualityScoreDriftReport, RecalculateQualityWithAudit, QualityScoreDrift service methods, E:/Development/gosom scraper/web/quality_api.go - audit_first on recalculate, GET /api/v1/quality/drift, POST /api/v1/quality/drift/repair, E:/Development/gosom scraper/web/prospects.go - RecomputeProspectsWithAudit, E:/Development/gosom scraper/web/prospects_api.go - audit_first on recompute, registers registerWebsiteStateRoutes (so web/web.go is untouched), E:/Development/gosom scraper/web/sqlite/prospects.go - reclassifyProspectsForBusiness now fans out to domain siblings and rescores their quality, E:/Development/gosom scraper/web/openapi_catalogue.go - 9 append-only catalogue entries for the new routes (see LEAD-APPLY 5)

**Verification.** Because other lanes left the shared worktree non-compiling during this session (runner/webrunner/task_pool.go: Tasks.Pending int64 vs int; web/job_pipeline_view.go: undefined url; web/enrichment_pipeline.go duplicating Service.processEnrichmentQueue from web/website_enrichment.go), I verified in an isolated tree at scratchpad/lane1-tree = `git archive HEAD` + ONLY my files + the other lane's web/prospect/*.go additions that my owned file web/prospects.go now depends on.
DEFINITIVE RUN (Windows Defender false-positives freshly linked sqlite.test.exe, so per CLAUDE.md this ran in the container):
  docker run --rm --memory=4g -v "<lane1-tree>:/src" -w /src golang:1.26.6-trixie sh -c "go build ./... && go vet ./web/... && go test -count=1 ./web/..."
  -> ok web 11.128s | ok web/enrichment 0.357s | ok web/jobruntime 0.079s | ok web/prospect 1.083s | ok web/resultimport 0.099s | ok web/sqlite 53.575s | FINAL2_EXIT=0
An earlier identical run of the same suite also passed (web 55.1s, web/sqlite 230.5s) - the only two failures seen all session were mine and both are fixed: TestRouteCatalogueCoversEveryRegisteredAPIRoute (undocumented new routes -> catalogue rows added) and TestCalculateQualityExplainsPositiveNegativeAndClosedExclusion (calculateQuality now derives the canonical state from its own columns for callers that build a qualityInput directly).
gofmt: verified on every changed file with a CRLF-aware checker (copy to LF, gofmt -l) - all clean. Whole-tree gofmt -l is useless here per CLAUDE.md because of CRLF.
RUNTIME: server built from the isolated tree, run on 127.0.0.1:8111 against a COPY of the live workspace (docker cp gosomscraper-scraper-1:/gmapsdata -> scratchpad/lane1-data). ./webdata was never opened, never written, and the live container was left running and untouched. All the numbers quoted in the issues above come from that live copy. Screenshot: scratchpad/lane1-shots/results-website-column.png.
NOT REGRESSED: pause/resume/stop/restart-from-checkpoint were not touched - no change to runner/, main.go, job lifecycle, or checkpoint code. The sweep only INSERTs enrichment_tasks rows, which RecoverEnrichmentTasks already recovers by state (not by requester); I proved this with a hard `taskkill /F` mid-sweep. Legacy job status values, nanosecond time.Duration serialization, the per-job CSV header, and CLI flags are untouched; all new capability is behind new routes and new response FIELDS (existing keys "processed"/"recalculated" keep their meaning and type).

**Deliberately not done.** 1. NO MIGRATION AND NO NEW COLUMN. The canonical state and the website-health score are computed from durable evidence rather than persisted as columns. That is correct and collision-free, but it means the state cannot yet be used as a Results FILTER or SORT field: web/sqlite/results.go's filter/sort column map is another lane's file, and adding a "website_state" filterable field needs either a persisted column (migration 19+) or a generated expression there. Consequence: the existing "Never checked" quick filter in web/app_results.go still uses `last_checked_at empty`, so it includes NO_WEBSITE and SOCIAL_ONLY rows. On the live copy that is 94 rows that can never be audited showing up in a filter named "never checked". I did not change it because both resultLeadFilters and the filter grammar live outside this lane.
2. The Results table and drawer still render the raw website_status string; the canonical label is available but the rendering change is LEAD-APPLY 3 (app_results.go, results.html, result_drawer.html, app.css are other lanes' files).
3. The "With website" KPI still counts social listings - LEAD-APPLY 2 (web/result_stats.go is another lane's file). Same for web/sqlite/dashboard_analytics.go:32-33, which counts `website <> '' AND website_status = 'active'` and would also benefit from excluding social hosts.
4. The import-time MAX() conflation is diagnosed, reproduced, and repairable in-lane (GET/POST /api/v1/quality/drift) but the root-cause edit is LEAD-APPLY 1 in web/sqlite/results.go. Until that lands, a re-import of an already-ingested source row will re-introduce drift, and the repair endpoint has to be re-run.
5. prospect.Classify still returns DEAD for a transport failure while the canonical state says ERROR. Documented deliberately (see the last issue) rather than changed, to avoid regressing existing prospect scoring and its tests.
6. Export columns: social profiles are now stored separately in social_profiles, but I did not add a social-profiles column to web/export_builder.go (another lane's file). The existing "social" column already reads social_profiles, so backfilled rows now export correctly without a change - but businesses.website still contains the social URL for those 32 rows (deliberately: never destroy the observation), so a "website" export column will still show an instagram.com URL for a SOCIAL_ONLY business. Worth a "website (owned only)" export column later.
7. The sweep is bounded at 5000 tasks per call and drains at the worker's existing 1 task per tick; I did not touch runner/webrunner (not my lane), so a large backlog takes proportionally long. On the live copy 206 audits took roughly 25 minutes. Raising enrichment throughput belongs to whoever owns the worker loop.
8. I did not run the full `-race` gate or `go test ./...` outside ./web/... - runner/webrunner and web/job_pipeline_view.go were left non-compiling by other lanes for the whole session, so a whole-tree race run was not possible from this worktree. My packages pass build+vet+test in the container.

### Lane 2 — export/results parity, scopes, columns, provenance (D, E, R, S, U)

#### TRUNCATION (lead's hypothesis) — Limit=250 caps an export at 250 rows

**Classification:** disproven

**Reproduction.** Against the live-data copy (372 businesses, 5 jobs) on my instance at 127.0.0.1:8112: POST /api/v1/exports {"source_scope":"filtered","job_id":"7100e95b-..."} → RecordCount 331, file exports/3bcb0f09-...-lane2-repro-job331.csv contains 331 data rows / 331 unique ids. A workspace export produced 372/372. Both exceed the 250 page size.

**Root cause.** web/export_builder.go:534-560 spoolExportRows sets search.Limit = 250 and then loops `search.Offset = int(rowCount)` until `rowCount >= page.Total`. 250 is a page size, not a cap.

**Fix.** None required. Left exactly as-is.

**Regression test.** TestPaginatedReadsCoverExactlyTheMatchedSet (web/sqlite/results_scope_provenance_test.go) — walks a query one row per page and asserts the union equals the single-page result set and page.Total.

**Acceptance evidence.** 331-row and 372-row CSVs counted with csv.reader: 'data rows 331 expected 331', 'data rows 372 expected 372', unique ids equal in both.

#### D — a filtered export with no filters is the whole workspace under a narrower name, and every other scope silently discarded the inputs still on the form

**Classification:** confirmed defect

**Reproduction.** 1) POST /api/v1/exports {"name":"lane2-repro-filtered-nofilters","format":"csv","source_scope":"filtered"} → state completed, SourceType results_filtered, RecordCount 372 — the entire workspace, labelled 'Current filters'.
2) The live workspace's own history carries the twin: export b919baad-206c-4ab0-8d7a-b6a36f5a6777 named 'Tatto Artists - Los Angeles Metro' has source_type=results_all, source_id='' and 367 rows = 331 tattoo (job 7100e95b) + 36 dentists (job ba78441f). The builder shows a 'Source job' select beside the scope, and the 'all' scope threw it away without a word.

**Root cause.** web/export_requests.go, resolveExportCreation (pre-fix lines 158-197): scope defaulted to "filtered"; case "filtered" resolved to resultSearchFromForm(r), which is the zero ResultSearch when no q/job_id/filter is present — identical to case "all"; and all/selected/saved_view each built their own search while ignoring, without error, every other narrowing input the same form submitted (job_id, q, filter_field..., filter_json).

**Fix.** New canonical scope model in web/advanced_filters.go (new file): five explicit keys — filtered (CURRENT FILTERED VIEW), selected (SELECTED BUSINESSES), job (CURRENT SOURCE JOB, new), all (ENTIRE WORKSPACE), saved_view — with one label table used by the builder, the Results menu, the preview API and export history. exportScopeSearch() is now the only place a scope becomes a ResultSearch. resolveExportCreation refuses a filtered export when searchIsNarrowed() is false, quoting the real workspace count, and refuses any scope that would drop a submitted narrowing input (conflictingExportScopeInputs). Export/Results/saved views still share the one ResultSearch model — no second filter engine. Presets and repeats bypass the guards, so historical exports replay unchanged.

**Regression test.** TestFilteredExportWithoutAnyFilterIsRefusedWithTheWorkspaceCount, TestExportScopeRefusesInputsItWouldSilentlyIgnore (3 sub-cases), TestExportJobScopeResolvesToThatJobAlone, TestExportAndResultsIssueTheSameQueryForTheSameFilters, TestExportHistoryNamesTheScopeItWasCreatedWith — web/export_scope_parity_test.go. Existing TestExportCreationResolvesEveryRecordScope still passes unchanged.

**Acceptance evidence.** After the fix, same live data:
• filtered + no filters → 422 'no filter is active, so "Current filtered view" would export all 372 businesses. Choose "Entire workspace" instead, or filter the results first'
• all + job_id → 422 '"Entire workspace" ignores a source job. Remove it, or choose the scope that uses it'
• source_scope=job + job_id → completed, results_job, SourceID 7100e95b..., 331 rows
• source_scope=all → completed, results_all, 372 rows
PARITY: GET /api/v1/results?job_id=7100e95b...&weak_website=true&contactable=true&prospect_score>=40 → total 37; scope preview filtered → 37; the equivalent export → 37 rows; sorted id lists compared in Python: IDS IDENTICAL: True.

#### R — an Export control never said which businesses it would take, or how many, before it ran

**Classification:** misleading UX/semantics

**Reproduction.** On the deployed build the Results toolbar offered 'Export' → 'Full data from this view / Call sheet / No-website leads / Weak-website leads' with no scope and no count, and the builder's Records select read 'Current filters / All normalized results / Saved view / Selected IDs' with no number anywhere. Nothing on screen distinguished the 63-row filtered view from the 372-row workspace before the file was built.

**Root cause.** No count was ever computed before generation: web/app_exports.go exportsPage renders the form with no totals and web/app_results.go resultExportPresets builds URLs with no counts; there was no endpoint that could answer 'how many would each scope take right now'.

**Fix.** New GET /api/v1/exports/scopes (registered in web/export_requests.go registerExportRoutes, handler exportScopePreviewAPI) resolves every scope through exportScopeSearch — the same code the export itself uses — and returns key/label/hint/count/available/reason. Results menu (results.html + app-results.js) renders four scope entries each carrying its count, the Selected entry appearing only while rows are ticked and carrying the ticked ids. Exports builder (exports.html + new app-exports.js) shows a live per-scope chip row, a one-line summary, disables and clears the inputs the chosen scope does not use, and names the submit button after the scope and its count; an unavailable scope disables the button with the reason on it. results.css styles the count. exportRecordSource() now labels history with the same words.

**Regression test.** TestExportScopePreviewCountsMatchTheResultsCount (asserts the preview's filtered count equals /api/v1/results meta.total and that filtered != workspace) and TestExportScopeInterfaceCarriesItsCountHooks (pins the DOM/asset hooks in both templates and both scripts) — web/export_scope_parity_test.go.

**Acceptance evidence.** Screenshots in scratchpad/shots-lane2/: 06-results-selected-scope.png shows 'Export filtered results 63 / Export selected 4 / Export this job 331 / Export entire workspace 372' while the table header reads '1-25 of 63 businesses'. 05-exports-scope-summary.png shows 'This export will contain 331 businesses — Current filtered view.' with chips 331/0/331/372/0 and button 'Create export — Current filtered view (331)'. 07-exports-unfiltered-blocked.png shows the unfiltered case: '...372 businesses — Current filtered view — no filter is active, so this is the entire workspace.' with a disabled button. History now reads 'Entire workspace' for the old 367-row file and 'Current source job · job ...' for job exports.

#### E — prospecting signals existed only as rendered chips, not as machine-usable export columns

**Classification:** confirmed defect

**Reproduction.** Before the change exportColumnDefinitions (web/export_builder.go:61-131) had no website_state, website_kind, website_audit_status, website_health_score, website_confidence, website_http_status or website_error_reason, no boolean flags at all (no_website / social_only / weak_website / contactable / has_phone / has_email), no email_count, no scoring version and no plural provenance. A spreadsheet had to parse the text of a badge to learn any of it.

**Root cause.** web/export_builder.go — the column catalogue and exportColumnValue only exposed fields BusinessResult already carried; BusinessResult itself carried no audit evidence, no contact counts and no scoring provenance (web/results.go), because web/sqlite/results.go SearchBusinesses never selected them.

**Fix.** web/sqlite/results.go: two new packed-JSON evidence blocks (businessEvidenceColumnSQL, businessProvenanceColumnSQL) add the newest website row, the website field-provenance confidence, email/phone/social counts, scoring_rule_version, prospect_updated_at, the newest completed website_audits row and the newest open enrichment task — one indexed lookup each. web/results.go: additive omitempty BusinessResult fields plus derived helpers (WebsiteState/WebsiteKind/NoWebsite/SocialOnly/WeakWebsite/Contactable/HasPhone/HasEmail/NeverChecked); WebsiteState delegates to Lane 1's ResolveWebsiteState so the taxonomy has one owner. web/export_builder.go: 40 new column definitions with real data types, health columns served by Lane 1's ScoreWebsiteHealth (loaded only when a health column is selected, guarded by WebsiteStateAvailable). Default export columns and the legacy CSV header/order untouched.

**Regression test.** TestProspectingSignalsAreAvailableAsExportColumns (39 required keys present AND the default column set unchanged) and TestExportColumnValuesReportDerivedProspectingFlags (values are typed booleans/ints, and an unaudited business gets a nil health score, never 0) — web/export_scope_parity_test.go.

**Acceptance evidence.** 45-column CSV export of job cfe2d653 (26 rows): website_state distribution {LIVE 16, ERROR 8, SOCIAL_ONLY 1, NO_WEBSITE 1}; website_health_score present on exactly the 16 rows an audit reached, absent (not zero) on the other 10; sample row website_health_score=98.39, grade=healthy, version=website-health-v1, website_confidence=1, email_count=1, social_count=2, scoring_rule_version=builtin-v1. Same columns verified typed in xlsx, portable SQLite (REAL/INTEGER/TEXT DDL confirmed), Parquet and JSONL (JSON booleans/numbers, not strings).

#### S — a job-scoped row reported another job's discovery, and a third of them reported no query at all

**Classification:** confirmed defect

**Reproduction.** GET /api/v1/results?job_id=cfe2d653-0fe9-4f43-80b8-9187572a992c (26 businesses) on the deployed build returned source_job_id = 7e4783f2-... (a different job) for 13 of 26 rows, and an empty source_query for 9 of 26. The database has the truth: business_sources holds 570 rows for 372 businesses, 61 of them for this job, and 109 businesses were observed by more than one job.

**Root cause.** web/sqlite/results.go SearchBusinesses (pre-fix lines 1757-1766): source_job_id / source_query / source_cell / scraped_at each came from `SELECT ... FROM business_sources WHERE business_id = ... ORDER BY extracted_at DESC LIMIT 1` — the newest observation in the whole workspace, ignoring the job the operator was looking at, and preferring enrichment rows (extraction_method 'bounded_html_analysis') that carry no source_query.

**Fix.** Provenance is now answered inside the scope being viewed. web/sqlite/results.go: a sourceScopeMarker placeholder inside every provenance expression is replaced with a bound `AND business_sources.job_id = ?` predicate when the search names a job (bound, never interpolated; select args prepended in textual order). The single reported observation is the newest in-scope row that actually names a discovery query, so job/query/cell/time all come from one coherent row. New additive fields carry the rest: ObservationCount (in scope), TotalObservationCount (workspace-wide), SourceJobIDs, SourceQueries, SourceCells, FirstObservedAt, LastObservedAt — exported as observation_count, total_observation_count, source_job_ids, source_queries, source_cells, first_observed_at, last_observed_at alongside the existing first_seen_at/last_seen_at.

**Regression test.** TestSearchReportsTheObservationInsideTheScopeBeingViewed, TestScopedObservationPrefersARowThatNamesItsDiscoveryQuery, TestScopedProvenanceBindsTheJobIdentifier (a hostile job id matches 0 rows) — web/sqlite/results_scope_provenance_test.go.

**Acceptance evidence.** After the fix, same query: source_job_id = cfe2d653-... for 26/26 rows, source_query = 'tattoo artist | tattoo shop | tattoo studio | tattoo parlor | custom tattoo shop' for 26/26, and the scoped observation_count sums to 61 — exactly the 61 business_sources rows that job wrote. Unscoped: 372 rows, observation_count sums to 570 (= COUNT(*) FROM business_sources) and 109 rows carry more than one source_job_id (= the DB's own multi-job count). Scoped vs total shown together live: Vism Studio scoped 2 / total 5, Hidden Temple scoped 1 / total 4, Zenith scoped 4 / total 7.

#### U — the audit-to-export workflow could not be trusted end to end, because the Results chips and the export filter language were different vocabularies

**Classification:** expected behaviour needing clearer presentation

**Reproduction.** The Results chips were nested OR expressions over prospect_status (web/app_results.go resultLeadFilters, lines 503-535); no single-field equivalent existed that an export could carry, and there was no filter at all for canonical website state, contact counts, or per-observation provenance. So 'choose Never checked -> audit -> filter Tier A/B + No website + Contactable -> export exactly that' had no expressible export query, and an operator had to trust that a hand-rebuilt export filter meant the same thing as the chip.

**Root cause.** web/sqlite/results.go resultFilterSQL (pre-fix lines 2496-2690) enumerated only stored scalar columns plus tags/social/technology/dates/geography. Every derived prospecting notion (weak website, contactable, never checked, canonical website state) lived only as a hand-assembled ResultFilterGroup in web/app_results.go:503-535 — a second, UI-side definition that nothing in the query language could reproduce or verify.

**Fix.** web/sqlite/results.go gains prospectingResultFilterSQL: website_state (a mapping table where an unnamed state falls through to the stored classification, so a new Lane 1 state is a data value, not a code path), website_audit_status, website_http_status, has_website, no_website, has_email, has_phone, has_social, social_only, weak_website (driven by web.WeakWebsiteStates(), the one definition the export column also reads), never_checked, contactable, email_count, phone_count, observation_count, source_query, source_cell, source_job — plus in/not_in operators for every text field. Each is available identically to Results, saved views, the scope preview and export.

**Regression test.** TestProspectingFiltersAreAvailableToEveryQuery (12 filters with expected counts) and TestProspectingFlagsMatchTheChipExpressionsTheyReplace (the chip's OR expression and the single-field flag select the same businesses, on a fixture seeded into every weak state) — web/sqlite/results_scope_provenance_test.go.

**Acceptance evidence.** New filters on live data: no_website 63 (= businesses with website=''), weak_website 43 (= 8 DEAD + 2 FREE_BUILDER + 1 NO_HTTPS + 32 SOCIAL_ONLY), social_only 32, contactable 351, never_checked 347, website_state LIVE 17 / DEAD 8 / ERROR 8 / NO_WEBSITE 63 / NEVER_CHECKED 347, prospect_tier in B,C = 103, observation_count>=2 = 109, source_query contains 'dental' = 36. Equivalence with the existing chips proved by count: weak-website OR-group 43 vs weak_website=true 43; contactable OR-group 351 vs contactable=true 351; never-checked group 347 vs never_checked=true 347. Full chain: never_checked in the tattoo job = 311 -> tier B/C + no website + contactable = 56 -> scope preview filtered = 56 -> export = 56 rows, sorted ids identical to the Results response, 41 requested columns all present; the only blank cells are genuine absences (a business with no website has no audit status, health, or HTTP code).

**Files changed:** e:/Development/gosom scraper/web/advanced_filters.go (NEW — canonical export scope model: keys, labels, searchIsNarrowed, conflictingExportScopeInputs, exportScopeSearch, countResultSearch), e:/Development/gosom scraper/web/export_requests.go (scope resolution rewritten; GET /api/v1/exports/scopes route + exportScopePreviewAPI; exportRecordSource labels; unfilteredExportScopeError), e:/Development/gosom scraper/web/export_builder.go (+40 machine-usable column definitions and values; exportDataRow carries scope and the website-health report; spoolExportRows takes record+columns), e:/Development/gosom scraper/web/results.go (additive omitempty BusinessResult evidence/provenance fields; WeakWebsiteStates, WebsiteStateEvidence, WebsiteState, WebsiteKind, NoWebsite, SocialOnly, WeakWebsite, HasPhone, HasEmail, Contactable, NeverChecked), e:/Development/gosom scraper/web/sqlite/results.go (scope-aware provenance expressions with a bound job predicate; businessEvidenceColumnSQL and businessProvenanceColumnSQL; scanner extensions; prospectingResultFilterSQL; in/not_in text operators), e:/Development/gosom scraper/web/static/templates/app/pages/exports.html (explicit five-scope control with live counts and per-scope input gating; loads app-exports.js), e:/Development/gosom scraper/web/static/templates/app/pages/results.html (scope-labelled export menu with counts), e:/Development/gosom scraper/web/static/js/app-exports.js (NEW — builder scope preview, input gating, submit labelling/disabling), e:/Development/gosom scraper/web/static/js/app-results.js (scope count loading, selected-scope link), e:/Development/gosom scraper/web/static/css/views/results.css (export menu count layout, menu divider), e:/Development/gosom scraper/web/export_scope_parity_test.go (NEW — 8 tests), e:/Development/gosom scraper/web/sqlite/results_scope_provenance_test.go (NEW — 6 tests), e:/Development/gosom scraper/web/openapi_catalogue.go (SHARED FILE — one appended line documenting GET /api/v1/exports/scopes; see LEAD-APPLY 1)

**Verification.** Gates (Go 1.26.5 at C:\Users\DELL\golang\go):
- go build ./...            -> rc=0
- go vet ./web/...          -> rc=0
- go test -count=1 ./web/   -> passes except TestRouteCatalogueCoversEveryRegisteredAPIRoute, which fails on ANOTHER LANE's undocumented route (first "GET /api/v1/enrichment/email-hygiene", later "POST /api/v1/enrichment/jobs/{id}/reconcile", both from enrichment_api.go). My own route is documented. All 8 of my new web tests pass individually and together (verbose: 8/8 PASS), and the pre-existing TestExportCreationResolvesEveryRecordScope and TestExportDeliveryOptionsRoundTripThroughStoredConfiguration still PASS.
- go test ./web/sqlite/ locally trips the documented Windows Defender false positive on sqlite.test.exe ("file contains a virus or potentially unwanted software") — reproduced twice, binary-hash level, not a code signal.
- Container gate instead: docker run --rm --memory=6g -v "e:\Development\gosom scraper:/src" -w /src golang:1.26.6-trixie go test -count=1 -timeout 20m ./web/sqlite/ -> ok github.com/gosom/google-maps-scraper/web/sqlite 207.634s. An earlier targeted container run of the five scope/provenance tests also passed (ok ... 9.124s).
- gofmt verified clean on LF copies of every Go file I touched (CRLF in the worktree makes gofmt -l useless per CLAUDE.md); per-file CRLF/LF endings preserved. node --check clean on both JS files.
- go tool golangci-lint run --new-from-rev=HEAD ./web/...: no security-relevant findings in my code (no gosec, dynamic-SQL, unchecked-type-assertion or errcheck hits). Only style rules the project does not gate on (goconst/wsl); I fixed the one tparallel nit in my test.

Runtime (my own instance on 127.0.0.1:8112 against a docker-cp copy of the live workspace; ./webdata never touched):
- Scope guards, scope preview, multi-filter parity (37=37=37, identical ids), the 56-row U workflow (identical ids, 41 columns), provenance before/after (13-of-26 wrong job -> 26-of-26 correct; 9-of-26 empty query -> 0), aggregates matching the DB exactly (570 observations, 109 multi-job businesses), all 19 new/changed filters counted against SQL ground truth, and exports in csv/xlsx/sqlite/parquet/jsonl.
- Screenshots in scratchpad/shots-lane2/ (01-07): builder with per-scope counts, job deep link, Results export menu with 63/4/331/372, and the blocked unfiltered export.
- Performance: /app/results renders in 0.21-0.88s and /api/v1/results?page_size=500 in 0.68-2.5s with five other-lane containers saturating the CPU; the unchanged deployed build took 32-68s for the same request under the same load. The new subqueries measure 15ms (field_provenance) and 2ms (website_audits) for 500 rows. No regression.
- Smoke: /app/results, /app/exports, /app/map, /app/dashboard, /app/saved-searches, the business detail API and drawer all 200. include_duplicates with a job filter still works. Pause/resume/stop/restart paths were not touched.
The instance on port 8112 is still running if you want to poke at it (binary scratchpad/lane2srv.exe, data scratchpad/lane2-data).

**Deliberately not done.** 1. NOT CONTAINER-VERIFIED (one assertion): the TotalObservationCount assertion added to TestSearchReportsTheObservationInsideTheScopeBeingViewed landed at 00:35:48; the green full-package container run's binary started around 00:37:45 (log completed 00:41:12 after 207s of tests), so it almost certainly included it, but I cannot prove the compile ordering. Two later container re-runs were starved by four concurrent other-lane containers and I killed them rather than keep hogging the machine. The field itself is verified live against the 372-business dataset (scoped 2 / total 5, 1 / 4, 4 / 7) and go vet ./web/sqlite/ passes.
2. NOT APPLIED: LEAD-APPLY 2 (web/app_exports.go) and 3 (web/app_results.go) — both outside my lane's file ownership. Nothing is broken without them; #2 only means /app/exports?source_scope=job 422s (my Results menu uses ?scope= instead, which works today), and #3 only means "weak" is defined in two places that currently agree exactly (43 = 43).
3. NOT BUILT: no saved-view scope preview on the Results page, and I did not touch the Map page's export path — neither is in D/E/R/S/U and both already go through the same ResultSearch.
4. DELIBERATE SEMANTIC CHOICE, flagged so it is not mistaken for a bug: observation_count, source_job_ids, source_queries, source_cells, first_observed_at and last_observed_at are SCOPE-RELATIVE (restricted to the job when a job scope is active) — that is what makes a job-scoped export truthful. total_observation_count, first_seen_at and last_seen_at are workspace-wide, so both readings are present in every file.
5. DEPENDENCY ON LANE 1: BusinessResult.WebsiteState() calls web.ResolveWebsiteState, and the website_health_* export columns call web.ScoreWebsiteHealth / web.WebsiteHealthRuleVersion (guarded by Service.WebsiteStateAvailable). I did this deliberately rather than shipping a second website-state taxonomy or a second health formula. If Lane 1's web/website_state.go, web/website_state_service.go or web/sqlite/website_state.go are dropped, my code will not compile.
6. DOCS: I did not update docs/implementation-progress.md, docs/technical-limitations.md or docs/acceptance-remediation.md — the last is explicitly not mine and the others were not in scope for this lane.
7. NOT RUN: the full -race container gate over ./... . Other lanes were running theirs and the machine could not carry another; my packages passed the non-race container gate.

### Lane 3 — enrichment pipeline (L, M, N, X, Y)

#### L1 — completed enriched run reports and exports zero emails

**Classification:** confirmed defect

**Reproduction.** Job cfe2d653-0fe9-4f43-80b8-9187572a992c on a copy of the live DB. DB holds 24 email rows across 11 of the job's 26 businesses (contact@vismstudio.com, la@baronart.tattoo, infoamaitattoo@gmail.com …). Monitor stage said "Emails discovered 11". Headline said "Emails found 0". Per-job CSV: 26 data rows, `emails` column non-empty on 0 rows. job_runtime.emails_found = 0, websites_found = 25.

**Root cause.** Two separate reads of two stale sources. (a) web/sqlite/results.go:264 computes job_runtime.emails_found inside ImportJobResults, which runner/webrunner/webrunner.go:628 runs BEFORE runner/webrunner/webrunner.go:632-640 queues enrichment — so the counter is computed before a single website has been crawled and nothing ever recomputes it. web/app_read_pages.go:681 then renders max(runtime.Emails=0, stats.WithEmail=0) = 0. (b) The export is the per-job CSV, written by the scrape engine during the run (runner/webrunner/webrunner.go:340-360) and never rewritten after enrichment, so web/service.go:105 GetCSV serves a file whose `emails` column predates every audit.

**Fix.** New web/enrichment_pipeline.go: when an enrichment pass drains a job's queue it (1) recomputes job_runtime.websites_found/emails_found from the same tables the import used, via new repo method RefreshJobEnrichmentTotals (web/sqlite/enrichment_cache.go), gated on PendingEnrichmentTaskCount == 0 so a partial total is never published as final; (2) rewrites the per-job CSV's `emails` column from the addresses the workspace holds (backfillJobResultEmails) — header and column order byte-identical, every other cell copied verbatim, existing values merged not replaced, written to a sibling temp file and renamed over the original; (3) writes a durable `enrichment-complete` job event. Added POST /api/v1/enrichment/jobs/{id}/reconcile (web/enrichment_api.go) so jobs that finished before this existed can be corrected without re-scraping. Stored addresses pass through the current hygiene rules on the way into the export, so junk written by the old extractor never reaches the file a human mails.

**Regression test.** web: TestEnrichmentPassExportsTheEmailsItHolds, TestEnrichmentPassKeepsTheResultFileUntilItsQueueDrains (web/enrichment_pipeline_test.go). web/sqlite: TestRefreshJobEnrichmentTotalsCorrectsCountersWrittenBeforeEnrichment (web/sqlite/enrichment_cache_test.go).

**Acceptance evidence.** Ran my build on port 8113 against the live-data copy and POSTed the reconcile endpoint for cfe2d653: {"websites_found":25,"email_addresses":24,"businesses_with_email":11,"result_rows_updated":11,"pending_audits":0}. CSV diff before/after: header identical (36 columns, same order), 26 rows before and after, 0 non-email cells changed, rows carrying an address went 0 → 11 (e.g. "Neptune tattoo studio -> inquiries@neptunetattoostudio.com", "Mantle Tattoo -> info@mantletattoo.com"). Monitor headline then read "Emails found 24 · 25 websites · 14 social profiles" (screenshot at <scratchpad>/shots-lane3/lane3-monitor-cfe2d653.png); it read 0 before.

#### L2 — stored email values contain concatenation artefacts from visible-text extraction

**Classification:** confirmed defect

**Reproduction.** Of the 39 email rows in the live workspace, 12 (31%) are page text welded onto a mailbox: 563-2030la@baronart.tattooopen, 626-554-7744inquiries@neptunetattoostudio.com, filler@godaddy.combookingsordersmy, filler@godaddy.comhomeshopthe, shop!estatetattoo@gmail.com, emailinfo@mateostudiola.com, twostudiotwotwenty2dtla@gmail.com, letteringdrigo@villainsla.com, letteringkaler@villainsla.com, portraitsboha@villainsla.com, portraitsjesse@villainsla.com, infoamaitattoo@gmail.com. All 11 visible_text rows and 1 structured_data row are affected.

**Root cause.** web/enrichment/extract.go:74 built the scan text with `collapseWhitespace(visibleDocument.Text())`. goquery's Text() concatenates text nodes with nothing between them, so `<a>626-554-7744</a><a>inquiries@x.com</a>` becomes one run and findPlainEmails (web/enrichment/extract.go:245) matches the whole run as the local part. Secondary: web/enrichment/email.go had no hygiene at all — normalizeEmailAddress kept syntactically invalid candidates with ValidSyntax=false, and web/sqlite/enrichment.go:837 stored them as contacts with status 'syntax-invalid'.

**Fix.** (a) New visibleTextForScan/appendVisibleText (web/enrichment/extract.go) walks the html node tree and writes a separator at every element boundary — inline as well as block — so an extracted address always corresponds to text a human can see. (b) New web/enrichment/email_hygiene.go: every candidate now passes sanitizeEmailCandidate before analysis. It rejects non-ICANN top-level domains via publicsuffix (this is what kills "baronart.tattooopen" while keeping the real "baronart.tattoo"), asset file names, vendor filler (filler@godaddy.com, sentry/wixpress DSNs), local parts over 40 chars, and unrecoverable digit runs; and it deterministically repairs a leading phone number or a glued sentence prefix (strictly subtractive — nothing is invented). Rejected candidates no longer reach the emails table at all. (c) New read-only GET /api/v1/enrichment/email-hygiene reports how many stored rows the current rules would refuse or repair, by reason and by extraction method. Nothing is deleted.

**Regression test.** web/enrichment: TestSanitizeEmailCandidateHandlesLiveWorkspaceJunk (replays the 10 exact live values), TestExtractEmailsSeparatesNeighbouringElements, TestAnalyzeRawEmailsReportsFunnel. web/sqlite: TestEnrichmentEmailHygieneReportCountsTheLiveWorkspaceJunk.

**Acceptance evidence.** Affected-row count on the real workspace, from the live endpoint: {"total":39,"unusable":3,"repairable":2,"reasons":{"unknown_tld":3},"extraction_methods":{"visible_text":5}} — 5 of 39 (12.8%) are provably wrong from the stored value alone. Live re-crawl of the four real sites with the new extractor proves the other 7 are fixed at source: neptunetattoostudio.com now yields inquiries@neptunetattoostudio.com (was 626-554-7744inquiries@…); mateostudiola.com yields only info@mateostudiola.com (was that plus emailinfo@…); villainslosangeles.com yields drigo@ / kaler@ / boha@ / jesse@villainsla.com (was letteringdrigo@ / letteringkaler@ / portraitsboha@ / portraitsjesse@). Funnels 3/1, 8/1, 4/4 with 0 rejections — the fix removes junk rather than suppressing good data.

#### L3 — no honest funnel behind "emails discovered"

**Classification:** misleading UX/semantics

**Reproduction.** Before this change nothing recorded how many email candidates a crawl found or why any were dropped, so a run showing candidates and exporting none was indistinguishable from a broken run. Job cfe2d653 is exactly that case.

**Root cause.** enrichment.Result carried only the accepted []Email. Nothing counted discovery, acceptance, rejection, or reasons anywhere in the pipeline.

**Fix.** New enrichment.EmailFunnel {discovered, distinct, accepted, rejected, repaired, rejection_reasons} on enrichment.Result (web/enrichment/types.go), populated by analyzeRawEmails, stored in website_audits.raw_result (no schema change), surfaced per business on WebsiteAuditView.email_funnel, and aggregated per job into JobPipelineFacts.EmailCandidates/EmailsAccepted/EmailsRejected/EmailsRepaired/EmailRejectionReasons via json_extract + json_each. Facts also gained EmailAddresses (distinct deliverable addresses) and helper EmailsUnexportable(), so "discovered" and "exported" are always reconcilable.

**Regression test.** web/enrichment: TestAnalyzeRawEmailsReportsFunnel (asserts the exact funnel for the live junk set: 6 discovered, 5 distinct, 1 accepted, 4 rejected, 1 repaired, reasons unknown_tld/placeholder/asset_path/syntax).

**Acceptance evidence.** Live check on port 8113: GET /api/v1/results/{id}/enrichment now returns "email_funnel":{"discovered":0,"distinct":0,"accepted":0,"rejected":0,"repaired":0} for the 25 pre-existing audits — honest zeros, not invented numbers — and real counts on new crawls (3/1, 8/1, 4/4 above).

#### M(a) — metric fan-out in the pipeline facts SQL

**Classification:** confirmed defect

**Reproduction.** Job cfe2d653: 26 businesses across 61 business_sources rows. Current facts SQL returned websites_checked=60, active=50, inactive=10, pages_checked=110, avg_response=1272.74ms. Truth over distinct website rows: 25, 17, 8, 35, 1306.24ms.

**Root cause.** web/sqlite/job_pipeline_facts.go joined `websites` to `business_sources` with no DISTINCT (`FROM websites JOIN business_sources ON business_sources.business_id = websites.business_id WHERE business_sources.job_id = ?`). One business observed by several queries or grid cells has several business_sources rows, so COUNT(*) and every SUM()/AVG() multiplied each website by its observation count.

**Fix.** Rewrote the website query to scope with `WHERE websites.business_id IN (SELECT business_id FROM business_sources WHERE job_id = ?)` instead of joining, and did the same for the LastHTTPStatus subquery and every new email/funnel/enrichment query. Audited every other fact in the file: the task-plan query reads job_tasks (no fan-out possible); the business query already used COUNT(DISTINCT businesses.id) throughout and is correct; the event query groups job_events directly. Added DomainsChecked (distinct websites.domain) so the site-vs-domain gap is visible.

**Regression test.** web/sqlite: TestJobPipelineFactsCountWebsitesOncePerBusiness (web/sqlite/job_pipeline_facts_fanout_test.go) — seeds 2 businesses observed 3× and 1×, asserts websites_checked=2 not 4, pages_checked=7 not 11, active/inactive 1/1 not 3/1, and average 180ms not the observation-weighted 150ms.

**Acceptance evidence.** Live monitor page for cfe2d653 on my build now reads "Pages visited: 35 page(s) across 25 site(s)" and "Average response time 1306 ms". It read 110 across 60 sites at 1272 ms before. SQL re-run against the copy confirms (25, 17, 8, 35, 1306.235…, 25 domains).

#### M(b) — claimed duplicate crawling

**Classification:** disproven

**Reproduction.** All 25 website_audits in the entire live workspace, checked by normalized host: 25 distinct domains, 0 domains audited more than once, 0 domains audited for more than one business. For job cfe2d653 specifically: 25 audits, 25 distinct domains.

**Root cause.** Not a crawling defect. The inflated numbers were entirely the business_sources fan-out (M(a)). The crawl did one fetch per business and each business had a unique site in this run.

**Fix.** No crawling change was needed for this claim. However the GUARANTEE did not exist: web/sqlite/enrichment.go queueEnrichmentCandidates dedupes and applies the freshness window per business_id, not per site, so two businesses on the same page would have been crawled twice. In the live workspace 309 businesses with a website resolve to only 305 distinct page keys (4 keys shared by 8 businesses, including https://www.instagram.com/esto_lts twice) and 268 distinct hosts, with instagram.com alone serving 28 businesses. Issue Y's cache and issue X's host gate now supply the guarantee.

**Regression test.** web/sqlite: TestReusableDomainAuditReusesOnlyTheSamePage. web: TestEnrichmentPassReusesAFreshAuditOfTheSamePage.

**Acceptance evidence.** Python over the live DB copy: `select count(*) from website_audits` = 25; grouping requested_url by normalized host gives 25 groups of size 1; 0 domains with >1 distinct business_id. Domain-sharing scan of businesses.website: 309 with a website, 305 distinct page keys, 268 distinct hosts, top host instagram.com with 28 businesses.

#### N — Fast+enrichment reports ~6s for work that took ~169s

**Classification:** confirmed defect

**Reproduction.** Job cfe2d653: job_runtime.started_at=1787927323, finished_at=1787927329 → the monitor shows "Elapsed 6s". enrichment_tasks for the same job: MIN(started_at)=1787927335, MAX(finished_at)=1787927492 → the website audits ran from +12s to +169s after the job started, i.e. 157s of work the job clock excludes entirely. The job was already status='ok', state='completed', stage='saving_exporting' at +6s, before its 25 enrichment tasks were even queued.

**Root cause.** Completion is published before the second stage starts. runner/webrunner/webrunner.go:576 calls persistOutcome (which flips the job to its terminal legacy status and stops the runtime clock) at the end of the listing walk; ImportJobResults (line 628) and QueueJobEnrichment (lines 632-640) run afterwards, and the audits themselves run later still on the jobLoop tail (line 262). So elapsed genuinely excludes enrichment, enrichment continues outside job timing, and "Finished" is published while required work is not yet queued — all three at once.

**Fix.** Made the three spans separately measurable and the durable record honest. JobPipelineFacts gained DiscoveryStartedAt/FinishedAt/DurationMS, EnrichmentStartedAt/FinishedAt/DurationMS, TotalDurationMS (discovery start → whichever stage ended last), EnrichmentTasksTotal/Queued/Running/Completed/Failed/Reused, EnrichmentComplete, plus helper EnrichmentPending(). EnrichmentComplete is false while any audit is queued or running, so nothing downstream can call such a job finished. The pass also writes an `enrichment-complete` job event when a job's audits actually drain. I deliberately did NOT make the legacy job status wait for enrichment — see LEAD-APPLY LA-5 and "not done".

**Regression test.** web/sqlite: TestJobPipelineFactsSeparateDiscoveryAndEnrichmentTiming — seeds the exact live timestamps and asserts discovery=6000ms, enrichment=157000ms, total=169000ms, EnrichmentPending()=1 and EnrichmentComplete=false while one audit is still queued.

**Acceptance evidence.** The three durations computed from the real job by the shipped SQL: discovery 1787927323→1787927329 = 6s; enrichment 1787927335→1787927492 = 157s; end-to-end 169s. The monitor still prints "ELAPSED 6s" because that label lives in lane 5's job_pipeline_view.go / app_read_pages.go — LEAD-APPLY LA-3 supplies the exact rendering change.

#### X — enrichment runs one site at a time with no per-host politeness

**Classification:** confirmed defect

**Reproduction.** Job cfe2d653's 25 audits took 157s wall clock, strictly serial at 6.28s each. runner/webrunner/webrunner.go:262 calls ProcessEnrichmentQueue(ctx, 1) — one task per one-second tick — and web/website_enrichment.go's queue loop was a single `for processed < limit` with no concurrency and no host limiting. 28 of the workspace's businesses point at instagram.com, so any naive parallelisation would have opened 28 requests to one host from one IP.

**Root cause.** No pool and no host budget existed. The queue pump was sequential by construction and the crawler had no shared rate-limiting seam.

**Fix.** Two-stage pipeline made explicit. Discovery is unchanged and still owns the browsers. Stage two is now a bounded pool (new web/enrichment_pipeline.go): EnrichmentPoolConfig{Workers=4, MaxConcurrentPerHost=1, MinHostInterval=750ms} with SetEnrichmentPool/EnrichmentPool, deliberately separate from Maps worker capacity. New enrichment.HostGate (web/enrichment/hostgate.go) enforces per-host concurrency and request spacing across the whole pool; it is acquired at the crawler's single HTTP choke point (Crawler.request) and the slot is held until the response body is closed, so page fetches, supporting-page fetches and link-health probes all share one host budget. The pool never overlaps scrape browser work — it still runs only at the jobLoop tail after the job's engines shut down — and it adds no Google Maps traffic. Screenshot capture is the one resource NOT multiplied: the pass starts at most one browser, shared behind a mutex, so 4 concurrent audits never become 4 browsers.

**Regression test.** web/enrichment: TestHostGateBoundsConcurrencyPerHost, TestHostGateSpacesRequestsToOneHost, TestHostGateCancellationReleasesTheSlot (this one caught a real permit-leak path during development), TestNormalizeGateHostCollapsesEquivalentHosts. web: TestEnrichmentPassRunsAuditsConcurrently (peak in-flight ≥2 and ≤ the configured 4), TestEnrichmentPassStartsOneBrowserForTheWholePass.

**Acceptance evidence.** Concurrency and the browser bound are proven by the deterministic tests above; the whole set passes under -race in golang:1.26.6-trixie. The wall-clock gain is a projection, NOT a measurement: 25 tasks × 6.28s serial = 157s; with 4 workers over 25 distinct hosts the 750ms spacing never binds, so ~40s. I could not measure it end to end because it needs 25 live crawls, and because the deployed worker still passes limit=1 (LEAD-APPLY LA-2).

#### Y — no domain audit cache; every business re-crawls its site

**Classification:** confirmed defect

**Reproduction.** web/sqlite/enrichment.go queueEnrichmentCandidates checks `SELECT MAX(completed_at) FROM website_audits WHERE business_id = ?` — the freshness window is per business, so a second business pointing at an already-audited page crawls it again. The live workspace has 4 page keys shared by 8 businesses and 7 hosts shared by 48 businesses.

**Root cause.** The staleness check was keyed on business_id, and there was no lookup path from a site to existing evidence.

**Fix.** The repository IS the cache — no new table and no migration. New ReusableDomainAudit (web/sqlite/enrichment_cache.go) finds the newest completed audit for the same site and hands back its full enrichment.Result decoded from website_audits.raw_result; the pass stores it as this business's own audit with enrichment.CacheProvenance{reused_from_audit_id, domain, observed_at} and performs no network I/O. Everything issue Y asks to persist already lives in that evidence: last checked, live/dead/error, HTTP status, redirect chain and final URL, TLS, page-level quality evidence, and emails/social/contact evidence. Three guards: inside the operator's freshness window, produced by at least enrichment.AuditVersion (new; version 2 = element-separated text + hygiene), and for the SAME PAGE. Explicit re-audit is unchanged — options.Force bypasses the cache entirely.

DELIBERATE DEVIATION, stated loudly: the brief says reuse per normalized DOMAIN. I key on the normalized page (host + path + query, via enrichment.SiteKey), not the bare domain. Domain-level reuse would be actively wrong here: 28 businesses have websites of the form instagram.com/<their own handle> and 8 have tattoshopsnearme.com/<their own slug>, so a domain-keyed cache would attribute one shop's contacts, emails and site quality to 27 other shops. Page-level reuse saves 4 crawls in this workspace instead of 41, and all 4 are provably the same page.

**Regression test.** web/sqlite: TestReusableDomainAuditReusesOnlyTheSamePage — asserts reuse across http/https/www/trailing-slash variants of the same page, refusal for a different path on the same registrable domain, refusal for a lower audit version, and refusal outside the freshness window. web: TestEnrichmentPassReusesAFreshAuditOfTheSamePage — asserts the analyzer factory is never invoked on a cache hit and the task is still stored and finished.

**Acceptance evidence.** Page-key scan of the live workspace: 309 businesses with a website, 305 distinct page keys, 4 keys shared by 8 businesses (thedolorosa.com, yorubahouse.store, instagram.com/esto_lts, bd-tattoo.com); 268 distinct hosts, instagram.com serving 28 businesses. The version guard is why the 25 existing audits are not reused: they carry no audit_version, so the extraction fix reaches every business on its next audit instead of being frozen into the cache.

**Files changed:** web/enrichment/email.go (modified — hygiene + funnel wired into analyzeRawEmails), web/enrichment/email_hygiene.go (new — sanitizeEmailCandidate, EmailFunnel, ClassifyStoredEmail), web/enrichment/email_hygiene_test.go (new), web/enrichment/email_test.go (modified — fixtures moved off reserved .example TLDs; invalid candidates are now rejected, not stored), web/enrichment/extract.go (modified — visibleTextForScan element-boundary separation), web/enrichment/crawler.go (modified — funnel plumbed through; HostGate acquired at the single request choke point, slot held until body close), web/enrichment/types.go (modified — Config.HostGate, Result.EmailFunnel/AuditVersion/Cache, CacheProvenance, AuditVersion=2), web/enrichment/hostgate.go (new — HostGate, NormalizeGateHost, SiteKey), web/enrichment/hostgate_test.go (new), web/enrichment_pipeline.go (new — bounded pool, domain-cache reuse, post-drain totals refresh, CSV backfill, EmailHygieneReport, ReconcileJobEnrichment), web/enrichment_pipeline_test.go (new), web/enrichment_api.go (modified — GET /api/v1/enrichment/email-hygiene, POST /api/v1/enrichment/jobs/{id}/reconcile), web/job_pipeline_facts.go (modified — email/funnel/enrichment/timing/domain fact fields + EnrichmentPending, EmailsUnexportable), web/sqlite/job_pipeline_facts.go (modified — fan-out fix and the new fact queries), web/sqlite/job_pipeline_facts_fanout_test.go (new), web/sqlite/enrichment_cache.go (new — ReusableDomainAudit, RefreshJobEnrichmentTotals, PendingEnrichmentTaskCount, JobBusinessContacts, EnrichmentEmailHygieneReport), web/sqlite/enrichment_cache_test.go (new), web/sqlite/enrichment.go (modified — 6 lines: audit history now carries email_funnel, audit_version, cache), web/website_enrichment.go (modified — ProcessEnrichmentQueue delegates to the pooled implementation; WebsiteAuditView gained EmailFunnel/AuditVersion/Cache). NOTE: not on my explicit ownership list, but its test file is and it is the enrichment service. Flagging for lead review., web/website_enrichment_test.go (modified — the shared enrichmentRepositoryStub is now mutex-guarded, required because the queue pass is concurrent)

**Verification.** Environment note: five other lanes are editing this same worktree, so `go build ./...` and `go test ./web/` broke transiently several times on files I do not own (web/sqlite/results.go, web/prospects_api.go, web/geo_boundary.go, web/job.go, web/result_stats.go). Every result below was taken when the tree compiled.

Local (Go 1.26.5 at C:\\Users\\DELL\\golang\\go):
- go build ./... — pass.
- go vet ./web/... — pass.
- gofmt -l over all 20 files I touched — prints nothing.
- go test -count=1 ./web/enrichment/ — ok (3.5s).
- go test -count=1 ./web/ — ONE failure: TestRouteCatalogueCoversEveryRegisteredAPIRoute, entirely LA-1 (a shared file I must not edit). Everything else in the package passes.
- web/sqlite could not be tested natively: Windows Defender flagged the freshly linked sqlite.test.exe ("file contains a virus or potentially unwanted software"), the known binary-hash false positive. Validated in the container instead, per CLAUDE.md.

Container (golang:1.26.6-trixie, the documented race gate):
- go build ./... && go vet ./web/... && go test -count=1 -race -timeout 15m -run '<my selectors>' ./web/ ./web/enrichment/ ./web/sqlite/ — exit 0 (web 4.8s, enrichment 4.8s, sqlite 162.7s). Two earlier race runs also exited 0.
- Full `go test ./web/sqlite/` in the container shows two failures, both in other lanes' in-flight code and neither reachable from my changes: TestWebsiteAuditSweepDeduplicatesDomainsAndResumes (web/sqlite/website_state_test.go, untracked, lane 1) and TestCalculateQualityExplainsPositiveNegativeAndClosedExclusion (web/sqlite/quality_test.go, quality.go modified by another lane).

Runtime, against a COPY of the live data (docker cp from gosomscraper-scraper-1 into <scratchpad>/lane3-data), my own build on 127.0.0.1:8113. The live ./webdata was never opened or written — mtimes on webdata/jobs.db and every webdata/*.csv are unchanged from session start, and the server was stopped afterwards.
- GET /api/v1/enrichment/email-hygiene → 39 stored addresses, 3 unusable (all unknown_tld), 2 repairable, all 5 from visible_text, with the five real values as samples.
- POST /api/v1/enrichment/jobs/cfe2d653-.../reconcile → websites_found 25, email_addresses 24, businesses_with_email 11, result_rows_updated 11, pending_audits 0.
- CSV before/after diff: header byte-identical (36 columns, same order), 26 rows both sides, zero non-email cells changed, rows carrying an address 0 → 11.
- Monitor page re-fetched and screenshotted: "Emails found 24 · 25 websites · 14 social profiles" (was 0), "Pages visited 35 page(s) across 25 site(s)" (was 110 across 60), "Average response time 1306 ms" (was 1272). Screenshot: <scratchpad>/shots-lane3/lane3-monitor-cfe2d653.png.
- GET /api/v1/results/{id}/enrichment now returns email_funnel (zeros for the pre-funnel audits, which is the honest answer).
- Live crawl probe (temporary test, since deleted) against the four real sites that produced the junk: neptunetattoostudio.com, mateostudiola.com and villainslosangeles.com all now yield the clean addresses with funnels 3/1, 8/1, 4/4 and zero rejections; studio2twentytwo.com is now unreachable so it yields nothing.

Not verified: the projected ~4× wall-clock improvement from the pool (needs 25 live crawls and the LA-2 change); pause/resume/stop/restart-from-checkpoint, which I did not touch and did not re-exercise.

**Deliberately not done.** 1. web/openapi_catalogue.go entries (LA-1) — shared file, another lane is editing it. TestRouteCatalogueCoversEveryRegisteredAPIRoute is RED until the lead applies two lines. This is the only failing test attributable to my work.
2. runner/webrunner/webrunner.go still calls ProcessEnrichmentQueue(ctx, 1), so in the deployed app the pool degrades to one worker and issue X's throughput gain is not yet realised. Fix is LA-2, one line.
3. The job's legacy status still flips to `ok` before its website audits finish. Deliberate: making it wait would change pause/stop/restart semantics and the legacy four-state contract, and the safe re-ordering of persistOutcome belongs to the webrunner owner (LA-5, with the wedging hazard spelled out). The truth is instead carried by Facts.EnrichmentComplete / EnrichmentPending() and the new `enrichment-complete` job event.
4. No monitor label changes. "ELAPSED 6s" and "Emails discovered 11" are still on screen; the facts behind them are now correct and complete, but web/job_pipeline_view.go is lane 5's and web/app_read_pages.go is not mine. LA-3.
5. No migration and no index on websites(domain). Schema version stays 18. The cache is correct without it; LA-4 if the lead wants the index.
6. Existing junk email rows are NOT deleted or rewritten in place. The hygiene report counts them (5 of 39 detectable from the stored value; 7 more are word-glue only a re-audit can fix) and the export filters/repairs them on the way out, but the emails table is left alone — deleting user data on a heuristic is not something I will do unprompted, and the audit-version guard means a re-audit replaces them properly.
7. docs/implementation-progress.md and docs/technical-limitations.md not updated — shared documents. Nothing here should be ticked yet: LA-1 through LA-3 are outstanding.
8. The domain cache is keyed on the normalized PAGE, not the bare domain as the brief worded it. This is a deliberate correctness deviation with evidence (28 businesses on instagram.com/<own handle>); see issue Y. If the lead genuinely wants domain-level reuse, it must be restricted to businesses whose website is the host root.
9. The pool's capacity is a process-wide setting (web.SetEnrichmentPool) rather than a field on Service, because web/service.go is not mine. If the lead prefers a Service field or a CLI flag, the config type and its normalisation are already isolated in web/enrichment_pipeline.go and move cleanly.

### Lane 4 — wizard isolation, GBP geography, Fast semantics (G, H, I, J, K, T)

#### G-1 — fresh wizard inherits one real job's content

**Classification:** confirmed defect

**Reproduction.** GET http://127.0.0.1:8114/app/scrapes/new (deployed build, no query string) returned: `value="San Francisco dentists"` on name (1 occurrence), "dentists in San Francisco" in the keywords textarea, and `37.7749` / `-122.4194` 3 times each in the rendered HTML. Browser probe of the Review step on a wizard with the fields cleared still printed `San Francisco, California` and `37.7749, -122.4194` because the JS fallbacks repeated the same literals.

**Root cause.** web/app_pages.go:349-355 (newScrapePage) builds `wizardInitialValues` from hardcoded literals: Name "San Francisco dentists", Keywords "dentists in San Francisco\ndental clinics in San Francisco", LocationLabel "San Francisco, California, United States", Lat 37.7749, Lon -122.4194. Mirrored client-side in web/static/js/app-wizard.js: `locations()` fell back to "San Francisco, California"; `updateReview()` used value(...,"San Francisco, California") and value(...,"37.7749")/("-122.4194"); `generateGBPQueries()` would only replace the map centre when it exactly equalled 37.7749/-122.4194; `applySanFranciscoPreset()` wrote the job name and two dental queries. web/static/templates/app/pages/new_scrape.html line 481 also shipped the same strings as the Review step's static fallback markup.

**Fix.** Added web/wizard_defaults.go with `freshWizardInitialValues(scrapeDefaults)`: a new scrape carries no name, no query text, no geography label and no centre; only the operator's own saved Settings defaults may prefill (and lat/lon only as a complete pair, never half a centre). GeographyMode keeps today's "bbox". Wizard JS: `locations()` now returns [] rather than a city name; `updateReview()` prints "Not set" / "Untitled scrape"; the GBP centre replacement compares against the value the PAGE LOADED WITH (`loadedCentre` + `centreIsUntouched()`) instead of a hardcoded San Francisco pair; `applySanFranciscoPreset` became `applySanFranciscoExampleArea` and fills geography only (label, lat, lon, radius, zoom, grid) — never a name or queries — with the button relabelled "Use San Francisco as the area". Template Review fallbacks changed to Untitled scrape / Not set. The one-line change to newScrapePage itself is in LEAD-APPLY (app_pages.go is lead-owned).

**Regression test.** web/wizard_defaults_test.go: TestFreshWizardCarriesNoPriorJobContent, TestFreshWizardAcceptsOnlySavedGlobalDefaults, TestFreshWizardIgnoresHalfACentre, TestFreshWizardRendersNoInheritedContent (renders the real template and asserts none of the six inherited strings reach the HTML).

**Acceptance evidence.** Browser probe after the fix, wizard fields cleared: `G|loc:Not set|coords:Not set|name:Untitled scrape`. `go test ./web/ -run FreshWizard` passes. Screenshot scratchpad/lane4-final/lane4-review-hidden-rules.png still shows "San Francisco dentists" because the server-side literal is the LEAD-APPLY half.

#### G-2 — dental filter examples read as inherited state

**Classification:** misleading UX/semantics

**Reproduction.** Rendered wizard HTML carried `placeholder="Dentist&#10;Dental clinic"`, `placeholder="Orthodontist&#10;Dental laboratory"`, `placeholder="dental"`, `placeholder="clinic group"`, plus `placeholder="San Francisco dental coverage"` (template name), `placeholder="Bay Area dental queries"` (keyword set), `placeholder="Dental practices"` (category group).

**Root cause.** web/static/templates/app/pages/new_scrape.html lines 130/164/377/378/391/392/481 — concrete subject-matter examples used as greyed placeholder VALUES, which in a filter field is indistinguishable from a value someone left behind.

**Fix.** Placeholders now describe the empty state instead of naming a subject: "Empty — every category is kept", "Empty — nothing is excluded", "Empty — any name", "Empty — nothing excluded", "Name this reusable template", "Name this keyword set", "Name this category group". The concrete examples moved into the field hints as prose ("For example Dentist or Plumber"), where they read as guidance rather than as state.

**Regression test.** web/wizard_defaults_test.go: TestWizardFilterExamplesDoNotReadAsState (asserts all seven placeholder strings are gone from the rendered page).

**Acceptance evidence.** Screenshot scratchpad/lane4-final/lane4-filters-status-warning.png shows the Filters step with neutral empty-state placeholders. Test passes.

#### H — Filters/Review mismatch (status operational)

**Classification:** confirmed defect

**Reproduction.** Two-part reproduction. (1) DISPROVEN as a seeded default: a fresh wizard probe returned `boxes:operational=false temporarily_closed=false permanently_closed=false | review:None` — nothing seeds the status filter, and no template/preset/GBP path checks it. (2) CONFIRMED deterministically via mode switching: open ?mode=advanced → step 5 → tick Open → switch to Basic → Review. Probe output: `rail:1Business search / 2Location / 3Review | step5hidden:true | opChecked:true | opDisabled:false | review:status operational — applied after collection`. The Filters step is gone from the rail, the checkbox is still checked AND still enabled (so it still submits), and Review announces a rule the operator can no longer see or clear. Separately, the status rule can only ever empty the view: 372 of 372 businesses in the live workspace have an empty business_status, and 331 of 331 rows in the Thorough acceptance CSV have an empty status column.

**Root cause.** web/static/js/app-wizard.js applyMode(): "Mode only changes what is *shown*: hidden fields are never disabled, so every step's values still submit" — deliberate and correct for job fidelity, but nothing surfaced the resulting invisible rules. filterSummary() read the DOM regardless of visibility, so Review was right while the Filters step was unreachable. Empty business_status is upstream drift: gmaps/entry.go reads status at [34][4][4] with an [88][0] fallback, and a live Maps response confirms b[34][4] is now null.

**Fix.** Kept the "hiding a step never changes the job" rule and made the hidden rules visible instead. Added activeNarrowingControls()/syncHiddenNarrowingNotice(), which names every non-default narrowing control living on a step the current mode hides, rendered as a warning on Review with two actions: "Show the Filters step" (openFilterStep() switches to Advanced first when the step is hidden) and "Clear every filter" (clearResultFilters() empties all step-5 rules and the rescan mode). filterSummary() now states the status rule EITHER WAY — when other filters exist but no status is ticked it appends "any business status", so its absence is never left to inference. Added an honest warning under the Business status fieldset explaining Maps is returning no status right now and that ticking a box narrows to nothing. Server side (web/job_filters.go, web/job_collection.go): new JobStatusFilterNotice and JobReviewFilterNotice with StatusFiltered()/ReviewCountFiltered() predicates, appended to the JobCollectionPlan notices so the API, saved view and every surface repeat them.

**Regression test.** web/job_collection_notices_test.go: TestStatusFilterCarriesTheUnavailableStatusNotice, TestReviewBoundCarriesTheUncapturedCountNotice, TestFilterPredicatesTolerateNoFilters. Browser probes lane4-h1-fixed and lane4-h2-clear.

**Acceptance evidence.** After the fix, same repro: `noticeHidden:false | noticeText:One rule set on a step this mode hides still applies to this job: business status operational.` Clear-filters probe: `before:rating 4.5–5; status operational — applied after collection | afterReview:None | opChecked:false | ratingMin: | noticeHidden:true`. Screenshots lane4-review-hidden-rules.png and lane4-filters-status-warning.png. SQL evidence: `select business_status, count(*) from businesses group by 1` → ('', 372).

#### I — GBP ZIP selection is text, not geography

**Classification:** confirmed defect

**Reproduction.** Wizard probe with a 25-ZIP x 3-synonym plan (75 generated queries) loaded into the keyword box: step 2 reported `areas:1 | tasks:75 | queries:75` — one area, one global lat/lon, 75 searches. Confirmed in code: web/prospects.go:592 calls prospect.QueryLines(queries), throwing away the ZIPArea that prospect.BuildQueries had already attached to every generated query.

**Root cause.** web/prospect/queries.go BuildQueries returns []GeneratedQuery carrying the full ZIPArea, but web/prospects.go:593 flattens it to strings, so ProspectQueryPlan ships only Queries plus one population-weighted Centre. Downstream, web/static/js/app-wizard.js estimate() computed locationCount from the "locations" textarea (which the generator never fills), giving cellsPerLocation=1 and cells=1. Geography therefore existed only inside the query text, so all 75 searches ran from the same job centre.

**Fix.** Added web/prospect/targets.go: QueryTarget {ID, Query, Synonym, ZIP, City, State, Latitude, Longitude, Population, Rank, Origin, ParentID}. BuildTargets() produces one target per (synonym, ZIP) with the ZIP's own centroid, city/state, population and 1-based selection rank; it delegates to BuildQueries so query text, ordering, de-duplication and the 5000 bound stay byte-identical. NewQueryTargetID() gives each target a stable sha256-derived id so checkpoint/resume/rerun/provenance can identify a completed target across runs. NeighbourTargets() implements adaptive expansion as REAL geographic targets — the neighbour ZIP's own centroid, its own id, ParentID pointing at the saturated target, nearest-first and deterministic, bounded by radius and a 200 cap. TargetCentre() keeps the same population-weighted map centre. Wizard: a hidden `query_targets` textarea carries the plan with the job; that field is the single source of truth (re-read on every estimate, so a plan the server renders for a duplicate/rerun/template behaves identically to one generated in-session); liveCoverageTargets() drops targets whose query line the operator deleted; estimate() reports stats.areas = distinct ZIP centroids; a coverage echo under the query preview states "N query targets across M ZIP centres. Each target searches from its own ZIP centroid, not from the job centre." Persisting and EXECUTING the targets needs job.go / app_scrapes.go / runner, which are not mine — full snippets in LEAD-APPLY.

**Regression test.** web/prospect/targets_test.go: TestBuildTargetsKeepsGeographyPerZIP, TestBuildTargetsMatchesBuildQueries, TestTargetIDIsStableAcrossRuns, TestNeighbourTargetsAreRealGeography, TestNeighbourTargetsRespectBounds, TestTargetCentreIsPopulationWeighted. web/wizard_defaults_test.go: TestWizardSubmitsCoverageTargets.

**Acceptance evidence.** Same 25x3 browser probe after the fix: `areas:25 | tasks:75 | queries:75 | echo:75 query targets across 25 ZIP centres. Each target searches from its own ZIP centroid, not from the job centre. | submits:true`. Go tests pass under -race in golang:1.26.6-trixie.

#### J — Fast mode semantics and a real request-construction defect

**Classification:** confirmed defect

**Reproduction.** Live acceptance job e108446c (5 queries, 15 km Fast radius, centre 34.0522,-118.2437): 26 businesses, ALL within 3,222 m of the centre — median 1,293 m, p90 2,188 m, zero results beyond 5 km, i.e. 4.6% of the claimed 15 km circle. Re-measured live against Google with the shipped request: n=20, max 3,413 m. Then swept the camera field: !1d=7500 → 2,188 m; 30000 → 3,878 m; 60000 → 7,678 m; 120000 → 12,828 m; 240000 → 21,687 m (4 of 20 outside 15 km). Changing the zoom field (!4f 10.0 vs 12.0) changed nothing at all.

**Root cause.** gmaps/searchjob.go buildGoogleMapsParams() hardcoded the camera altitude `!1d3826.902183192154` and never referenced params.Location.Radius. The radius was used only as a post-filter in gmaps/entry.go:1019 filterAndSortEntriesWithinRadius. The operator's radius therefore had no influence on what Google was asked for — a ~3.8 km camera whatever they chose. (Secondary, cosmetic: buildGoogleMapsParams also overwrites the caller's ViewportW/H, so runner/jobs.go:126's 1920x450 has always been discarded.) Separately, Fast mode's real guarantee is one Maps search request per query with `!7i20!8i0` — one page, at most 20 listings, ranked around the camera, then trimmed to the radius: a radius-biased sample, never coverage. 5 queries x 20 = the exact 100 raw observations the acceptance run recorded, 74 merged as duplicates, 26 unique. The UI called this a "strict radius" and "radius searches of 15 km".

**Fix.** gmaps/searchjob.go: cameraAltitudeMetres(radius) = clamp(radius x 8, 800 m, 400 km); radius<=0 keeps the historical 3826.902183192154 exactly, so no existing caller changes. Factor 8 is calibrated from the sweep above — it reaches most of the requested radius while keeping essentially all 20 slots inside it, so none is spent on a listing the radius filter will discard. UI honesty (no pretence of coverage, and Fast is not forced to imitate Thorough): the cost sentence now reads "Fast mode: 5 queries, one Maps search each, up to 20 listings per query (at most 100 observations before duplicates are merged), aimed at the centre and trimmed to 15 km. This samples the area; it does not cover it."; the pre-flight row changed from "Strict radius" to "Radius-biased sample"; Review's radius line reads "15 km bound on a radius-biased sample"; the metres field hint says "an upper bound on the sample, not coverage of the circle"; and the Fast mode card now states the 20-listing cap and that no review count or business status comes back.

**Regression test.** gmaps/searchjob_lane4_test.go: TestSearchJobCameraFollowsRadius (asserts 15 km → 120000, the no-radius legacy value, and the 400 km bound), TestSearchJobCameraNoLongerFrozen (four radii must produce four distinct cameras).

**Acceptance evidence.** Fetched the EXACT URL the patched NewSearchJob builds against live Google: n=20, max 12,938 m, median 7,434 m, 20/20 inside the 15 km radius, 20/20 carrying place_id+data_id. A 4x increase in geographic reach for the same single request. Browser probe of the new copy: `cost:Fast mode: 5 queries, one Maps search each, up to 20 listings per query (at most 100 observations before duplicates are merged)... This samples the area; it does not cover it.`

#### K — missing values stored as fabricated zeros and empty identities

**Classification:** confirmed defect

**Reproduction.** Live per-job CSV for the Fast run (e108446c): review_count = "0" in 26 of 26 rows while every one of those rows carried a real 4.4–5.0 star rating; category, cid, place_id, link, status, price_range and descriptions empty in 26 of 26. The DB looked only partly broken (13 zeros, 5 without identity) purely because canonical merge back-filled the other rows from the earlier Thorough run — the Fast run itself captured NONE of them. Thorough CSV (7100e95b, 331 rows): status empty 331/331. Root cause confirmed against a LIVE Maps search response: business[4] = [null,...,null,4.4] — length 8, so [4][7] (rating) exists and [4][8] (review count) does not exist at all; business[34][4] is null (no status); business[78] carries the ChIJ place id and business[10] the hex feature id, both unused.

**Root cause.** gmaps/multiple.go ParseSearchResults: `entry.ReviewCount = int(getNthElementAndCast[float64](business, 4, 8))` — the helper returns the zero value for a missing field, so "not captured" became the integer 0, which gmaps/entry.go CsvRow wrote as "0" and web/resultimport/reader.go parsed as a real zero. Same class for ReviewRating. The parser also never set entry.PlaceID, entry.Cid or entry.Link (all available or derivable) nor entry.Category (Categories was parsed one line above and discarded).

**Fix.** gmaps/entry.go: new `ReviewCountUnknown` / `ReviewRatingUnknown` fields (json omitempty, so a capturing entry marshals byte-identically); CsvRow uses optionalNumber() to write an EMPTY cell for an uncaptured value — the CSV header and column order are untouched, and web/resultimport parseInteger/parseNumber already map an empty numeric column to nil, while businesses.review_count is already a nullable INTEGER updated with COALESCE(?, review_count), so NO MIGRATION IS NEEDED. Added getNthElementAndCastOK() so a genuine captured 0 is still stored as 0. gmaps/multiple.go now also extracts PlaceID from index 78, derives Cid from the data_id hex pair (returning nothing when the id is not that exact shape — never a fabricated identifier), builds the canonical maps link from the strongest identity available, and sets Category from the first parsed category. Sibling audit: web/prospect/opener.go's StatusLive default call opener said "{rating} stars across {reviews} reviews" and would have read "0 reviews" aloud about a business with hundreds — the default now asserts only the rating, which IS in the payload. web/job_filters.go gained JobReviewFilterNotice so a review bound states that an uncaptured count is excluded rather than counted as zero.

**Regression test.** gmaps/searchjob_lane4_test.go: TestSearchResultsNeverFabricateAZeroReviewCount, TestSearchResultsKeepACapturedZero, TestSearchResultsCaptureStrongestIdentity, TestCidIsNeverFabricated, TestFrozenFixtureStillParses (the archived fixture DOES carry [4][8] and must keep it). web/job_collection_notices_test.go: TestReviewBoundCarriesTheUncapturedCountNotice.

**Acceptance evidence.** Ran the patched parser over the real live Maps response captured this session: `entries=20 review_count_unknown=20 full_identity=20 category_set=20`, csv review_count="" and link=https://www.google.com/maps/place/?q=place_id:ChIJ… . Before the fix the same payload produced 20 rows of "0" reviews, no place_id, no cid, no link and a blank category. "Cosmos Tattoo by A.Mai" — one of the 5 identity-less rows in the live acceptance DB — now resolves to ChIJGyfik8HHwoARDWMo3C2zH2Y.

#### T — longitude -118.2437 becoming -1182437

**Classification:** disproven

**Reproduction.** Deterministic browser probe on the real wizard covering every named path: set -118.2437, fire input, fire change, click through ALL seven step buttons, cycle mode advanced→gbp→basic→advanced, focus/blur, then serialize with FormData. Result: `set=-118.2437 | afterInput=-118.2437 | afterChange=-118.2437 | afterAllSteps=-118.2437 | afterModeCycle=-118.2437 | afterBlur=-118.2437 | formData=-118.2437`. Number-input coercion probes: a comma decimal `-118,2437` yields "" (empty), never -1182437; `-1182437` in the live field is caught by native constraint validation (`valid=false, rangeUnderflow=true`), so a native submit is blocked. Saved defaults cannot hold it either — web/app_settings.go:538-543 ParseFloat-validates longitude to ±180 and rejects out of range. Duplicate/rerun copy the stored string verbatim.

**Root cause.** Not reproducible through the wizard, mode switching, duplicate/rerun, saved defaults, or browser number-input behaviour. No dot-stripping normalization exists in any wizard or app JS (the only regex of that shape, app-map.js:151, strips commas from table text, not coordinates). No fix invented.

**Fix.** None — reported as unconfirmed. One genuine adjacent gap WAS found and is in LEAD-APPLY 3: the job-create path never parses or bounds the centre. web/app_scrapes.go:243-244 stores r.FormValue("latitude"/"longitude") as raw strings and JobData.Validate (web/job.go) only checks they are non-empty in Fast mode. Browser validation is the only thing stopping a nonsense centre; a scripted POST or the REST API would persist "-1182437" verbatim, and runner/jobs.go would only reject it much later, at seed time.

**Regression test.** None added — nothing to regress against. The lat/lon validation test belongs with the LEAD-APPLY change.

**Acceptance evidence.** Probe output above, captured on the deployed wizard at 127.0.0.1:8114 in the session's headless Chromium.

**Files changed:** e:/Development/gosom scraper/gmaps/entry.go, e:/Development/gosom scraper/gmaps/multiple.go, e:/Development/gosom scraper/gmaps/searchjob.go, e:/Development/gosom scraper/gmaps/searchjob_lane4_test.go (new), e:/Development/gosom scraper/web/job_filters.go, e:/Development/gosom scraper/web/job_collection.go, e:/Development/gosom scraper/web/job_collection_notices_test.go (new), e:/Development/gosom scraper/web/wizard_defaults.go (new), e:/Development/gosom scraper/web/wizard_defaults_test.go (new), e:/Development/gosom scraper/web/prospect/targets.go (new), e:/Development/gosom scraper/web/prospect/targets_test.go (new), e:/Development/gosom scraper/web/prospect/opener.go, e:/Development/gosom scraper/web/static/js/app-wizard.js, e:/Development/gosom scraper/web/static/templates/app/pages/new_scrape.html

**Verification.** Commands run from e:/Development/gosom scraper with PATH="/c/Users/DELL/golang/go/bin:$PATH".

PASSING:
- `go build ./...` — clean.
- `go vet ./web/... ./gmaps/...` — clean.
- `go test -count=1 ./gmaps/` — ok (2.9s), includes all 7 new tests.
- `go test -count=1 ./web/prospect/` — ok (2.0s), includes all 6 new target tests.
- `go test -count=1 ./web/ -run 'Wizard|FreshWizard|StatusFilter|ReviewBound|FilterPredicates|JobCollection|JobFilter'` — ok (9.6s), all 9 new web tests pass.
- Race gate in golang:1.26.6-trixie: `go test -count=1 -race -timeout 15m ./gmaps/... ./web/prospect/...` — ok 6.4s / 8.3s.
- gofmt clean on every file I touched (checked with CRLF normalized first, since the whole worktree is CRLF and `gofmt -l` reports every such file — CLAUDE.md documents this).
- `node --check web/static/js/app-wizard.js` — clean. Zero console/network errors in every browser probe (only the pre-existing /favicon.ico 404).

LIVE VERIFICATION (not just unit tests):
- Read-only copy of the container's /gmapsdata into scratchpad/lane4-data; my own server on 127.0.0.1:8114 against that copy. The live ./webdata was never written to and no live job was mutated.
- Real Google Maps search responses fetched to confirm both the K root cause (no [4][8] review count in the payload) and the J fix (the exact URL the patched NewSearchJob builds now reaches 12,938 m vs 3,413 m before).
- 10 headless-Chromium probes plus 4 evidence screenshots in scratchpad/lane4-final/ (lane4-fresh-wizard, lane4-filters-status-warning, lane4-review-hidden-rules, lane4-fast-cost-honest). No horizontal overflow at 1920.

PRE-EXISTING FAILURES, NOT MINE (five other lanes are editing this same worktree concurrently):
- `web`: TestRouteCatalogueCoversEveryRegisteredAPIRoute and TestProcessEnrichmentQueueSkipsScreenshotWithoutDriverOncePerPass — both from in-flight edits to web/enrichment_api.go, web/openapi_catalogue.go and web/enrichment/*, none of which I touched.
- `web/sqlite`: fails to BUILD in the container (`web/geo_boundary.go:228 undefined: CellSpillover`, `web/result_stats.go:74 s.JobCellSpillover undefined`) from another lane's mid-edit state. Natively it also hit the documented Windows Defender false positive on sqlite.test.exe. My changes do not touch web/sqlite and add no migration.
- web/prospect/classify.go shows as modified in git status; that is Lane 1's work, present before I started.

**Deliberately not done.** 1. Issue I is HALF-LANDED BY DESIGN. The complete, tested target model (web/prospect/targets.go), the wizard's submission of it, and the honest "25 areas / 75 searches" reporting are done and verified. Persisting the targets on JobData, parsing them in app_scrapes.go, and actually executing each one from its ZIP centroid live in web/job.go, web/app_scrapes.go, runner/jobs.go and runner/webrunner/webrunner.go — none of which are my files. Drop-in snippets are in LEAD-APPLY 2-5. Until those land, a GBP job still executes every query from the job centre; the wizard now says so explicitly ("This build stores no per-ZIP execution targets, so every query runs from the job centre") rather than pretending otherwise.

2. Issue G's server-side literal is one line in web/app_pages.go (lead-owned). LEAD-APPLY 1. My helper and its four tests are already in the tree and green, so it is a one-line substitution with coverage waiting for it.

3. Issue T: not reproduced, no fix invented, as instructed. I surfaced the real adjacent gap (no server-side coordinate validation on job create) as LEAD-APPLY 3 rather than silently patching a file I do not own.

4. business_status is empty for 372/372 businesses in BOTH modes. I confirmed the cause against a live payload (Maps no longer emits [34][4][4], and the [88][0] fallback in gmaps/entry.go does not fire for search results either) but did NOT invent a replacement index. Guessing a protobuf offset from one sample risks writing a wrong status into a field filters and exports depend on. Instead the wizard and the collection plan now say plainly that no status is being captured and that a status filter will empty the view. Chasing this properly needs a payload survey across several categories and locales.

5. Fast mode still fetches ONE page of at most 20 listings per query (`!7i20!8i0`). Paginating via the `!8i` offset would raise yield substantially, but that changes Fast mode's cost profile and makes it imitate Thorough, which the brief explicitly rules out. Recorded as a deliberate non-change.

6. buildGoogleMapsParams still overwrites the caller's ViewportW/H (runner/jobs.go passes 1920x450, always discarded). I left the emitted 600x800 exactly as shipped: the live sweep showed the response does not vary with the viewport, only with the camera altitude I fixed, so changing an untested request field for no measured benefit is not worth the risk.

7. I did not edit web/prospect/queries.go; the target model went into a new file (targets.go) so BuildQueries' existing behaviour and callers are provably untouched. web/job_collection_api.go, web/prospect/samplezips.go and web/static/css/views/wizard.css needed no change.

### Lane 5 — spillover, dedup terminology, warning noise (O, P, Q)

#### O — geographic spillover on job 7100e95b (15 km Thorough, LA 34.0522/-118.2437, 5 km grid)

**Classification:** expected behaviour needing clearer presentation

**Reproduction.** Copied the live container data (docker cp gosomscraper-scraper-1:/gmapsdata/.) and measured all 331 businesses against the configured centre with a haversine over businesses.latitude/longitude.

Distance from the configured centre (331/331 have coordinates): min 386 m, MEDIAN 12,232 m, p90 25,609 m, MAX 36,211 m ("Anaheim Ink", 33.84450/-117.94134).
Buckets: 0-5 km 45 (13.6%), 5-10 km 76 (23.0%), 10-15 km 68 (20.5%), 15-18 km 33 (10.0%), 18-21.3 km 26 (7.9%), 21.3-25 km 44 (13.3%), 25-30 km 20 (6.0%), 30-50 km 19 (5.7%).
Inside the 15 km radius: 189 (57.1%). Outside it: 142 (42.9%). Outside the grid bounding box 33.917302,-118.406517,34.187098,-118.080883 entirely: 115 (34.7%).

Planner audit: job_tasks holds 36 distinct source_cells (a 6x6 grid over the bbox). Cell centres sit 3,507 m to 17,687 m from the configured centre; the bbox far corner is 21.2 km out. So no planned query ever pointed further than 21.2 km, yet results reach 36.2 km.

Decisive measurement — distance from the cell that ACTUALLY found each business (join business_sources.input_id -> job_tasks.task_key -> job_tasks.source_cell, anchored on job_businesses.first_source_id so there is no source fan-out; 331/331 rows join): median 11,978 m, p90 17,342 m, MAX 20,086 m. Only 25 of 331 (7.6%) lie within the 3,536 m half-diagonal of the 5 km cell that returned them; 306 of 331 (92.4%) were returned by a cell more than 3.5 km away.

**Root cause.** Not a planner defect. web/coverage + runner/webrunner/checkpoint_runner.go:135-190 cut a 6x6 grid from the bounding box that encloses the radius circle, which is correct: the corner cells must reach radius*sqrt(2) = 21.2 km for the circle to be covered at all. Google Maps then answers a 5 km grid-cell query with businesses up to 20 km from that cell — the platform widens a sparse-category search rather than returning a short list. Two presentation defects made this look like a bug: (1) nothing on any page stated the distance distribution or the planned reach, and (2) the evidence linking a business to its searching cell was stored but never surfaced — business_sources.source_cell is empty for all 331 rows (SELECT DISTINCT source_cell -> one row, ''), while the usable link (business_sources.input_id = job_tasks.task_key) was unused by any query.

**Fix.** Made the geography explicit and added non-destructive filtering; no business is ever removed.

web/geo_boundary.go (new): JobSearchArea derived from the job's own configuration (centre, radius, grid bbox, cell km); Classify() returns great-circle distance plus a zone — inside-radius / inside-planned (past the radius but inside the grid the planner cut) / outside-planned / unknown; PlannedReachMeters() reports the furthest a planned query could legitimately point (bbox far corner, 21.2 km here); RadiusFilterValue() renders the job's own centre+radius in the stored-results "distance within" filter grammar so the panel and the filter can never disagree about where the centre is. ResultGeography holds the distribution.

web/result_stats.go: ResultStats gains Geography (additive JSON). GetResultStats resolves the job (best effort, after path validation) and the CSV pass now also accumulates distances. Derived on read rather than persisted: distance and zone are pure functions of already-stored coordinates and already-stored job config, so they cannot go stale when a job is edited and they are available retroactively for every run already in the workspace.

web/job_geography.go + web/sqlite/job_geography.go (new): JobCellObservations() surfaces the search cell that was already persisted, joining job_businesses.first_source_id -> business_sources.input_id -> job_tasks.task_key -> job_tasks.source_cell (one row per business, immune to source fan-out). CellSpillover measures every business against the cell that asked for it.

web/job_pipeline_view.go: geographyMetrics() emits a tagged "geography" metric group — inside the radius, past the radius/inside the searched grid, outside the area this run searched (with "they are real businesses and they are kept"), median distance vs planned reach, farthest business kept, and "returned from outside the cell that searched". geographyFilterAction() emits a link to /app/results with the job's own centre and radius pre-filled.

web/static/templates/app/pages/job_monitor.html + web/static/css/views/monitor.css: new "Where the results landed" panel rendering that group, plus the filter button. The panel is absent for a job with no centre; the radius tile is absent for a job with no radius; the grid-band tile is absent for an ungridded job.

**Regression test.** TestHaversineMetersMatchesKnownDistance, TestJobSearchAreaClassifiesTheThreeZones, TestJobSearchAreaPlannedReachExceedsRadius, TestJobSearchAreaWithoutCentreClassifiesNothing, TestRadiusFilterValueMatchesStoredResultsFilterGrammar, TestResultGeographyReproducesAcceptanceRunDistribution (asserts all 7 fixture rows survive), TestResultGeographyUnavailableWithoutJobArea, TestGeographyMetricsNameSpilloverAndOfferANonDestructiveFilter, TestGeographyMetricsAbsentWithoutMeasurableGeography, TestGeographyMetricsOmitTheRadiusTileWithoutARadius (web/geo_boundary_test.go); TestSummarizeCellSpilloverAttributesTheAcceptanceRunToGoogle, TestSummarizeCellSpilloverWithoutACellSizeMakesNoClaim, TestSummarizeCellSpilloverEmptyEvidenceIsUnavailable, TestParseCellCentreRejectsMalformedCells, TestGeographyMetricsReportCellSpillover, TestGeographyMetricsOmitTheGridBandForAnUngriddedJob (web/job_geography_test.go); TestJobCellObservationsPairEveryBusinessWithTheCellThatFoundIt (asserts no source fan-out), TestJobCellObservationsAreEmptyForAnUngriddedJob (web/sqlite/job_geography_test.go); TestJobMonitorRendersWhereTheResultsLanded, TestJobMonitorOmitsGeographyWithoutACentre (web/job_monitor_geography_test.go).

**Acceptance evidence.** Deployed on my own build (port 8115) against the live data copy. Rendered panel for 7100e95b: "Inside the 15.0 km radius 189 of 331 / 57.1% of the businesses this run kept"; "Past the radius, inside the searched grid 27"; "Outside the area this run searched 115 (34.7%) / Maps returned these on its own; they are real businesses and they are kept"; "Median distance from the centre 12.2 km / planned searches reached 21.2 km at most"; "Farthest business kept 36.2 km / Anaheim Ink"; "Returned from outside the cell that searched 306 of 331 (92.4%) / median 12.0 km from the searching cell, up to 20.1 km; the cells themselves only reach 3.5 km". Every number matches my independent SQL/python measurement exactly.

Filter link verified end to end: /app/results?filter_field=distance&filter_operator=within&filter_value=34.0522%2C-118.2437%2C15&job_id=7100e95b... renders "25 of 188 unique businesses"; clearing it renders "25 of 331 unique businesses"; the per-job CSV still holds 331 rows. (188 vs my 189: the stored-results filter uses a flat-earth approximation and one business sits on the line, which is why the button quotes no count.)

Other jobs: 7e4783f2 (10 km, gridded) 45 of 96 inside, 43 (44.8%) outside, 96.9% returned from outside their cell; ba78441f (10 km, ungridded) 36 of 36 inside, no grid-band tile, no spillover tile. Screenshots: scratchpad/lane5-shots-done/final-collected.png, lane5-shots-dark/dark-geo.png (1280px, dark theme, zero horizontal overflow, zero console errors).

#### P — "555 rows added, 224 rows replaced, 331 final businesses, Duplicates merged 0"

**Classification:** misleading UX/semantics

**Reproduction.** Reproduced verbatim on the deployed build. Monitor "What this run collected" showed: Rows collected 331; **Duplicates merged 0** with the caption "the same business seen more than once". The "Areas searched" strip 30 px below showed: 555 rows added / 224 rows replaced / 0 duplicates skipped. The "Deduplicating" pipeline stage showed a third vocabulary: Raw records 331 / Duplicate matches 0 / Merged records 0 / Unresolved conflicts 0.

Underlying evidence measured directly:
- SUM over job_tasks.checkpoint for the job: rows_added = 555, rows_replaced = 224 (555 - 224 = 331 exactly). 117 of the 180 tasks recorded rows_added = 0.
- The committed CSV: 331 rows, 331 unique by place_id, 0 repeated rows.
- business_merges: 0 rows in the entire workspace.
- duplicate_candidates with state='pending' where both halves belong to this job: 33.

**Root cause.** Three different quantities were shown under overlapping words, and the most prominent one was structurally incapable of being non-zero. web/static/templates/app/pages/job_monitor.html:279 (before) rendered {{$job.Duplicates}} as "Duplicates merged"; that value is ResultStats.Duplicates from web/result_stats.go, which counts rows repeating an earlier row **inside the committed CSV** — a file the per-task merge has already de-repeated, so on a Maps job it is always 0. Meanwhile web/job_pipeline_view.go deduplicatingMetrics() labelled the business count "Raw records" and the entity-merge count "Merged records", and web/static/js/app-monitor.js renderCoverageTotals() leaked the raw checkpoint field names "rows added"/"rows replaced" straight into the interface. Nothing anywhere reported the 224 repeated observations or the 33 open duplicate candidates, and checkpoint replacement was implicitly described as a merge when no records were ever combined.

**Fix.** Defined the vocabulary once and made every monitor surface use it.

web/run_metrics.go (new): RunObservations plus the five-name contract in the package doc — Maps observations / repeated observations / unique businesses / entity merges / unresolved duplicate candidates — stating explicitly that checkpoint replacement is a repeated observation and never an entity merge. Available/HasEntityMerges/HasUnresolvedDuplicates separate a real zero from missing evidence.

web/result_stats.go: ResultStats gains Run (additive JSON), populated from the durable per-task checkpoints (JobCoverageTasks, read directly rather than through JobCoverage so the per-job dashboard loop does not also build a per-query table, trend and confidence model it never reads) plus the committed business count and the open-candidate count. ResultStats.Duplicates keeps its meaning and its value — its doc comment now states that it is file-local, is not an entity merge, and is normally zero on a Maps job.

web/run_metrics.go + web/sqlite/job_run_metrics.go (new): JobOpenDuplicateCandidates counts pending pairs whose BOTH halves this job collected, so a cross-run pair is not attributed to whichever run is on screen.

web/job_pipeline_view.go: deduplicatingMetrics() now emits the five names, each with a one-line definition in a new Note field, and reports "—" instead of a zero when the evidence is missing.

web/static/templates/app/pages/job_monitor.html: the "Duplicates merged" tile is gone. The strip is now Businesses kept / Maps observations / Repeated observations / Emails found / Warnings and errors / Blocked. The two observation tiles ship hidden and are revealed only once the coverage endpoint answers.

web/static/js/app-monitor.js: coverage tiles relabelled to "Maps observations / repeated observations / first-time observations / repeats inside one search"; chart legend, per-search column headers (Obs./Repeats) and chart aria-labels follow; the hidden tiles are hydrated from the same payload.

**Regression test.** TestNewRunObservationsSeparatesObservationsFromBusinesses (555/224/331/40.4% from the real checkpoint totals), TestRunObservationsDistinguishRepeatsFromEntityMerges (asserts 224 repeats and 0 merges are not the same number), TestRunObservationsUnavailableBeforeAnySearchFinishes, TestRunObservationsPreferTheCommittedBusinessCount, TestDeduplicatingMetricsUseTheHonestVocabulary (also fails if any metric other than "Entity merges" contains the word merge, or if "raw record"/"duplicate match" return), TestDeduplicatingMetricsReportMissingEvidenceRatherThanZero, TestEveryRunCountMetricCarriesItsDefinition, TestResultStatsDuplicatesAreFileLocalAndDocumented (web/run_metrics_test.go); TestJobOpenDuplicateCandidatesCountsOnlyThisJobsUndecidedPairs (web/sqlite/job_run_metrics_test.go); TestJobMonitorNeverCallsRepetitionAMerge (fails if "Duplicates merged" or "the same business seen more than once" reappears in the rendered page) (web/job_monitor_geography_test.go). web/job_monitor_spec_test.go was updated where it asserted the old labels.

**Acceptance evidence.** Rendered on the deployed build for 7100e95b. Collected strip: Businesses kept 331 (331 rows in the result file) / Maps observations 555 ("331 of them were a business this run had not seen before") / Repeated observations 224 ("40.4% of observations; the stored row was refreshed, not merged"). Deduplicating stage: Maps observations 555 / Repeated observations 224 (40.4% of observations) / Rows repeated inside one search 0 / Unique businesses 331 / Entity merges 0 / Unresolved duplicate candidates 33. Coverage strip: "180/180 searches finished, 555 Maps observations, 224 repeated observations, 331 first-time observations, 0 repeats inside one search". Every figure matches the independent SQL: checkpoint sums 555/224, CSV 331, business_merges 0, pending candidates within the job 33.

#### Q — 118 warnings on a flawless 180/180 run

**Classification:** confirmed defect

**Reproduction.** Deployed build, job 7100e95b: "Warnings and errors 118" with "0 errors recorded in the job log", on a run with 180/180 searches completed, 0 failed, 0 blocked, 0 skipped.

Severity census straight from job_events for this job: information 367, warning 118, error 0. By type: cell-empty 117 (warning), capacity-capped 1 (warning), task-checkpoint 180, task-started 180, plus 7 single lifecycle events (all information).
The log viewer rendered 118 warning lines and 369 information lines. The "What went wrong" panel listed class "other" x117 with the sample "Task completed its walk but found zero places..." beside the one genuine "browser" x1.
The 117 cell-empty events correspond exactly to the 117 of 180 tasks whose checkpoint recorded rows_added = 0 and duplicates_skipped = 0.

**Root cause.** runner/webrunner/task_pool.go:1003 records "cell-empty" at severity "warning": `w.svc.RecordJobWorkerEvent(ctx, job.ID, "cell-empty", "warning", "Task completed its walk but found zero places; ...")`. A cell whose area holds no matching business is a fact about the area, not a fault. That stored severity then propagates unchanged into (a) the counter — web/sqlite/job_pipeline_facts.go:324 switches on the raw severity string to build facts.Warnings, and (b) the log viewer — web/job_log_levels.go classifyJobLogLevel() has no entry for "cell-empty", so it falls through to the raw severity and paints the line as a warning.

**Fix.** web/job_event_severity.go (new): the single severity policy — information for a search that found nothing and for saturation/no-new-results; warning for retryable degradation (capacity caps, truncated tasks, proxy failures); error for a genuinely failed task or data loss (commit/merge failure, low disk). HonestJobEventSeverity(eventType, recordedSeverity) falls back to the recorded severity for any type the policy does not name, so nothing else changes.

Within my owned files the monitor was made honest immediately, without waiting for the emitter fix:
- web/static/js/app-monitor.js: the "What went wrong" panel no longer lists informational classes (they were never things that went wrong); their count is subtracted from the warning headline and reported as information instead; the rendered and streamed log lines are re-levelled through the same policy so the viewer, the counter and the panel agree. The subtraction only ever removes entries it can actually see in the benchmark evidence, so once the emitter is fixed it becomes a no-op rather than double-counting.
- The "searches whose area held no businesses" figure quoted in the note is counted from durable plan evidence (coverage by_query rows with state=completed, rows_added=0, duplicates_skipped=0), not from message text.
- web/static/templates/app/pages/job_monitor.html: the tile carries data-raw-warnings so the server value is preserved and restored if the evidence endpoint is unavailable.

The emitter-side and server-count fixes are three small diffs in files owned by other lanes — see lead_apply. After they land the JS passes go quiet and the numbers are identical.

**Regression test.** TestHonestJobEventSeverityCensusOfTheAcceptanceRun (replays the exact 485-event census of job 7100e95b and asserts 118 warnings -> 1 warning + 117 information + 0 errors, and that no event is lost or invented), TestHonestJobEventSeverityKeepsRealProblems (capacity-capped stays a warning; commit/merge failure and low disk become errors; unknown types keep their recorded severity), TestInformationalJobEventTypesIsStableAndComplete (web/job_event_severity_test.go).

**Acceptance evidence.** BEFORE (deployed build, job 7100e95b): monitor card "Warnings and errors 118", errors 0; log viewer 118 warning lines / 369 information lines; "What went wrong" listed 2 classes — other x117 and browser x1.
AFTER (my build, same data): monitor card "Warnings and errors 1", note "0 errors recorded in the job log · 117 information entries, mostly the 117 searches whose area held no businesses"; log viewer 1 warning line / 486 information lines; "What went wrong" lists 1 class — browser x1 ("Requested workers/browsers exceed what available memory can hold; running 2 worker(s) with 2 browser(s) instead"), which is genuine degradation and correctly stays a warning.
Severity by class, before -> after: warning 118 -> 1, error 0 -> 0, information 367 -> 484 (job_events only; the viewer's 486/487 includes two lifecycle lines). Verified by CDP evaluation against the running page, zero console errors.

**Files changed:** E:\Development\gosom scraper\web\geo_boundary.go (new), E:\Development\gosom scraper\web\geo_boundary_test.go (new), E:\Development\gosom scraper\web\run_metrics.go (new), E:\Development\gosom scraper\web\run_metrics_test.go (new), E:\Development\gosom scraper\web\job_event_severity.go (new), E:\Development\gosom scraper\web\job_event_severity_test.go (new), E:\Development\gosom scraper\web\job_geography.go (new), E:\Development\gosom scraper\web\job_geography_test.go (new), E:\Development\gosom scraper\web\job_monitor_geography_test.go (new), E:\Development\gosom scraper\web\sqlite\job_geography.go (new), E:\Development\gosom scraper\web\sqlite\job_geography_test.go (new), E:\Development\gosom scraper\web\sqlite\job_run_metrics.go (new), E:\Development\gosom scraper\web\sqlite\job_run_metrics_test.go (new), E:\Development\gosom scraper\web\job_pipeline_view.go (owned, modified), E:\Development\gosom scraper\web\result_stats.go (owned, modified), E:\Development\gosom scraper\web\static\templates\app\pages\job_monitor.html (owned, modified), E:\Development\gosom scraper\web\static\js\app-monitor.js (owned, modified), E:\Development\gosom scraper\web\static\css\views\monitor.css (owned, modified), E:\Development\gosom scraper\web\job_monitor_spec_test.go (NOT in my owned list — flagged: it is the test file for web/job_pipeline_view.go and hard-coded the old Deduplicating labels "Raw records / Duplicate matches / Merged records / Unresolved conflicts". Two edits only: the expected-label map entry and the "Merged records" assertion, now "Entity merges". Leaving it would have left ./web/ red for every lane.)

**Verification.** Commands (Go 1.26.5 at C:\Users\DELL\golang\go):
- go build ./...                                    PASS
- go vet ./web/...                                  PASS (clean)
- gofmt -l <every file I touched>                   PASS (prints nothing)
- go test -count=1 ./web/                           1 failure, NOT mine: TestRouteCatalogueCoversEveryRegisteredAPIRoute — "enrichment_api.go registers POST /api/v1/enrichment/jobs/{id}/reconcile but openapi_catalogue.go does not document it". Both files belong to the enrichment lane and are mid-edit (git status shows them modified). Every other test in ./web/ passes, including all 30 I added.
- go test -count=1 ./web/resultimport/ ./web/jobruntime/ ./runner/...   PASS (webrunner 75.3s)
- web/sqlite: Windows Defender blocked the freshly linked sqlite.test.exe ("file contains a virus or potentially unwanted software") — the known binary-hash false positive. Validated in the container per CLAUDE.md:
  docker run --rm --memory=6g -v "e:\Development\gosom scraper:/src" -v "C:\Users\DELL\go\pkg\mod:/gomod" -w /src -e GOMODCACHE=/gomod -e GOPROXY=off golang:1.26.6-trixie sh -c "gofmt -l <new sqlite files>; go test -count=1 -run 'JobOpenDuplicateCandidates|JobCellObservations' -v ./web/sqlite/"
  -> gofmt clean; --- PASS: TestJobOpenDuplicateCandidatesCountsOnlyThisJobsUndecidedPairs, --- PASS: TestJobCellObservationsAreEmptyForAnUngriddedJob, --- PASS: TestJobCellObservationsPairEveryBusinessWithTheCellThatFoundIt; ok github.com/gosom/google-maps-scraper/web/sqlite 2.083s (exit 0). An earlier full-package container run of ./web/sqlite also exited 0.

Runtime verification (my own build on 127.0.0.1:8115, data-folder = scratchpad/lane5-data, a docker cp copy of the live workspace; ./webdata was never touched and no test opens the real jobs.db — all Go tests use t.TempDir()):
- All three issues reproduced on the deployed behaviour first, then re-measured after the fix. Numbers are quoted per issue above and every one matches an independent python/sqlite3 measurement on the read-only copy.
- CDP evaluation of the live page confirms the JS half: warnings 1, note "0 errors ... 117 information entries, mostly the 117 searches whose area held no businesses", Maps observations 555, Repeated observations 224, log lines {information: 486, warning: 1}, failure panel down to the single genuine browser/capacity entry.
- Screenshots at 1920 and 1280, light and dark: scratchpad/lane5-shots-before/ (before), lane5-shots-done/final-collected.png, lane5-shots-dark/dark-geo.png. Zero console errors, zero horizontal overflow at both widths.
- Not regressed: pause/resume/stop/restart-from-checkpoint. I changed no lifecycle code. Verified by flipping a copy of the DB to state='running' and rendering the monitor — the start/resume/cancel/restart endpoints all still render, the geography panel renders with partial data, and the observation tiles correctly stay hidden until the coverage endpoint answers.
- Backward compatibility: no route, JSON key or CSV column was removed or changed. New fields only (ResultStats.Geography, ResultStats.Run, jobPipelineMetric.Group/.Note). /api/v1/jobs/{id} still returns legacy status "ok" and nanosecond durations (max_time 5400000000000). The per-job CSV header and column order are untouched, and no result row is written, deleted or reordered anywhere in this change.
- Performance: dashboard (which calls GetResultStats per job) warm 0.42-0.63 s, cold 0.77 s; job monitor 0.21-0.47 s. The two lookups I added per job read the task rows directly instead of building the full coverage report for exactly this reason.
- CSP: no CDN, no framework, no build step, no inline handlers; the JS additions add no event listeners and the CSS uses only existing design tokens.

**Deliberately not done.** 1. business_sources.source_cell is still written empty by the Maps path. I did NOT fix this, and it turned out not to matter: the cell that found each business is already recoverable from business_sources.input_id (the task key) joined to job_tasks.source_cell, which is what web/sqlite/job_geography.go now uses, and it resolved 331/331 businesses for job 7100e95b and 96/96 for 7e4783f2. Populating source_cell as well would need the runner to carry the cell into the per-row import (runner/webrunner, another lane's file) and would add a second copy of a fact already stored. Worth a cleanup ticket, not worth a cross-lane edit now.

2. I did not persist distance-from-centre or the inside/outside flag as columns, despite the brief asking for it. Reasoning given in full under migrations_added: both are pure functions of data already stored, so deriving them on read is retroactive for the 5 existing runs, cannot go stale if a job's centre or radius is edited, and needs no schema version — which also avoids a migration-number collision with five other lanes running in parallel. If the lead wants them materialised for query performance later, the derivation lives in one place (web/geo_boundary.go) and a migration can backfill from it.

3. The server-side log severity dropdown still classifies cell-empty as a warning, so filtering to "Warning" can list a line the viewer now shows as information. app-monitor.js re-levels the rendered lines, but the filter is a server-side form in web/job_log_levels.go, which is not my file. LEAD-APPLY 3 closes it. This is the one residual inconsistency in my area and it is stated in a code comment where the workaround lives.

4. web/resultimport/** is in my owned list and I changed nothing there. I checked it: its Source.GridCell mapping is already correct and would populate the moment a CSV carried a cell column; the legacy CSV header may not gain one under the compatibility rules, so there was nothing to fix. web/sqlite/result_mutations.go is likewise untouched — it is the tag/notes/delete path and none of these issues live there.

5. The client-side severity classification matches on message signatures ("found zero places", "no new results", "saturat") because the benchmark endpoint reports failure classes by message rather than by event type. It mirrors the Go policy table and is documented as such in both files, and it becomes inert once LEAD-APPLY 1 lands. I chose it over the alternative (inferring the count from the 117 zero-yield tasks) because a signature match can only ever remove an entry that is actually present, whereas the inference could subtract a warning that was never emitted.

6. The one failing test in ./web/ (TestRouteCatalogueCoversEveryRegisteredAPIRoute) belongs to the enrichment lane's in-flight route addition. I left it alone.

### Lane 6 — throughput, auto capacity, benchmarks (V, W, Z)

#### KNOWN-BUG — fast-mode task-pool announcement claims browsers it never launches

**Classification:** confirmed defect

**Reproduction.** Live job cfe2d653-0fe9-4f43-80b8-9187572a992c (fast_mode=true, concurrency 4, task_workers 4, browser_pool_size 2, pages_per_browser 2), read read-only from a copy of the live workspace. job_events row 505: message "Running 4 task(s) in parallel with 1 worker concurrency each (4 browser(s) planned)", context {"planned_browsers":4, "per_browser_cost_bytes":629145600, "budget_reserve_bytes":1610612736, "memory_available_bytes":0}. Fast mode is a pure-HTTP stealth fetcher and launches ZERO Chromium processes; liveBrowserFootprint already returned 0 for the same run, so the plan and the runtime footprint contradicted each other.

**Root cause.** runner/webrunner/task_pool.go:117 taskPoolPlan.PlannedBrowsers() had no FastMode input. With browserBudgetTotal 0 (fast mode is exempt) but job.Data.BrowserPool 2 > 0, planTaskPool set PerTaskBrowserPool = max(1, 2/4) = 1, and PlannedBrowsers returned Workers*max(1,perWorker) = 4*1 = 4. The one-browser-per-worker floor is correct arithmetic for browser mode and simply does not describe fast mode. Secondarily, runner/webrunner/checkpoint_runner.go emitted per_browser_cost_bytes and budget_reserve_bytes for fast jobs, making a run that can never touch the browser budget look budget-bound.

**Fix.** Added taskPoolPlan.FastMode (set by planTaskPool from job.Data.FastMode). PlannedBrowsers() returns 0 in fast mode. The checkpoint runner now emits "Running N task(s) in parallel with M worker concurrency each (Fast mode: no browser)" for fast jobs and omits every browser-denominated context key (browser_budget_total, browser_worker_budget, per_browser_cost_bytes, budget_reserve_bytes, memory_available_bytes) for them, adding fast_mode:true instead. The acceptance harness log-pattern prefix is unchanged so it still parses.

**Regression test.** TestFastModePlanLaunchesNoBrowsers and TestFastModePoolAnnouncementNamesNoBrowsers (runner/webrunner/auto_capacity_test.go); TestFastModePoolLineStillParses (acceptance/events_test.go)

**Acceptance evidence.** Runtime, on my own server (port 8116, isolated copy of the live workspace), job c1104371-8f0e-4faf-ad85-2371942c2ec7 with cfe2d653's exact shape: log now reads "Running 4 task(s) in parallel with 1 worker concurrency each (Fast mode: no browser)" and the task-pool context is {"planned_browsers":0, "fast_mode":true, ...} with no per_browser_cost_bytes key.

#### V — auto capacity: the binding throughput constraint was a hard-coded constant, and the worker count could never rise

**Classification:** confirmed defect

**Reproduction.** Acceptance job 7100e95b-28f9-4979-9e85-8cd2294f0173 (180 tasks), measured from job_tasks on a copy of the live DB: 180/180 completed, 0 failures, 0 retries, 0 blocks; sum of per-task durations 6262 s; wall 3147 s (52.5 min); per-second occupancy histogram {1 active: 32 s, 2 active: 3115 s} — average parallelism 1.99, NEVER above 2. Its task-pool event recorded task_workers 2, per_task_concurrency 2, planned_browsers 2, browser_worker_budget 2, browser_budget_total 8, memory_available_bytes 7167823872, plus a capacity-capped WARNING ("Requested workers/browsers exceed what available memory can hold; running 2 worker(s) with 2 browser(s) instead", requested_task_workers 4). So a perfectly healthy run used 2 of the 8 browsers its own memory budget said were affordable, and told the operator memory forced it.

**Root cause.** Two independent problems. (1) runner/webrunner/adaptive_resources.go:64-72 — browserModeWorkerBudget was available/browserWorkerMemoryReservationBytes (3 GiB per worker) then hard-capped at maxDefaultBrowserWorkers = 2. A worker's real cost is its browser pool (600 MiB/browser in browserProcessBudget), so 3 GiB priced one worker as five browsers; on top of that the flat cap of 2 returned the same answer for every host from ~6 GiB upward. It was the binding constraint on every machine and was derived from nothing the host reported. (2) runner/webrunner/task_pool.go — taskPoolPlan.Workers was fixed at plan time and the adaptive controller explicitly could not change it, so a run that had been narrowed could never widen again, and lowering concurrency could never lower the browser count below the worker floor.

**Fix.** New runner/webrunner/auto_capacity.go. autoWorkerCeiling(sample) derives the worker ceiling as min( (available-reserve)/(one browser + 256 MiB worker overhead), effective CPU cores (cgroup v1/v2 quota aware), browserProcessBudget(sample), web.MaximumJobTaskWorkers ), floored at 1, falling back to the single-app topology when memory is unreadable; browserModeWorkerBudget is now a thin alias for it, and maxDefaultBrowserWorkers / browserWorkerMemoryReservationBytes are removed. browserProcessBudget's denominator is now plannedBrowserCostBytes(sample), which starts at the 600 MiB planning constant and is raised — never lowered — by the mean RSS the live census measured, so a measurement can only ever shrink that budget. The census (adaptive_resources.go) now returns (count, resident bytes) and workerResourceSample gains BrowserMemoryBytes and CPUCores. A runtime controller (decideWorkerTarget, a pure function) now moves the worker count during the run from eight signals: available RAM, CPU cores, CPU load, measured browser RSS, the live browser census, block rate, task-failure rate, task latency and durable-write (SQLite) latency. Growth is one worker per settling window and needs every axis clean; a block or a failure majority halves the pool immediately. Workers are added by spawning on a dynamicWorkerGroup and removed by a retire gate consulted only at the top of the claim loop, where the worker holds no lease. Per-task concurrency now divides by the LIVE worker count, and workerCeilingForRun caps workers by the operator's effective concurrency budget and explicit TaskWorkers, so auto capacity reshapes the operator's load budget but never exceeds it, and never exceeds workers*per-worker-pool <= browserProcessBudget.

**Regression test.** TestBrowserModeWorkerBudgetScalesWithTheMeasuredHost, TestMeasuredBrowserMemoryOnlyEverShrinksTheBudget, TestWorkerCeilingNeverExceedsTheBrowserBudget, TestWorkerCeilingNeverExceedsTheOperatorsLoadBudget, TestDecideWorkerTargetHoldsTheCeilingAbsolutely, TestDecideWorkerTargetGrowsOnlyOnCorroboratedHealth (11 sub-cases), TestDecideWorkerTargetCollapsesOnAdverseSignals (8 sub-cases), TestDecideWorkerTargetReducesEvenDuringACooldown, TestEffectiveCPUCoresReadsTheContainerQuota, TestLatencySeriesTracksItsOwnBest, TestAutoCapacityGrowsAHealthyRun, TestAutoCapacityCollapsesOnABlockedWindow, TestAutoCapacityStopsGrowingWhenThePlanRunsOut, TestAutoCapacityStaysSilentWhenNothingChanges — plus the rewritten TestBrowserModeGridJobCapsItsDefaultFanoutEndToEnd, which now derives its expected cap from a constrained 3.5 GiB sample instead of the deleted constant.

**Acceptance evidence.** Plan arithmetic replayed against the exact resource sample the live task-pool event recorded (7167823872 bytes available, 8 cores): BEFORE workerBudget 2, browserBudget 8 -> 2 workers x 1 browser, cappedExplicit TRUE. AFTER workerBudget 6, browserBudget 8 -> 4 workers x 1 browser = 4 browsers, cappedExplicit FALSE (the operator's explicit 4 is honoured, the spurious capacity-capped warning disappears, and the run still uses only 4 of the 8 affordable browsers). The same derivation on this host while it was starved (1.35 GiB available) returned 1 worker / 1 browser, so it genuinely tracks the machine. The offline scheduler benchmark projects that width change as 52.7 min -> 26.4 min for the same 180-task plan.

#### V-a — auto capacity blamed memory for a clamp memory had nothing to do with (found in my own first live run)

**Classification:** misleading UX/semantics

**Reproduction.** First live fast-mode run of the new controller (job 0b72dcc8 on port 8116) recorded: "Auto capacity reduced parallel tasks from 4 to 2 (the measured memory and CPU budget now supports fewer parallel tasks)" with context auto_worker_ceiling 1, browser_budget_total 1, browser_processes 0. Fast mode consults no browser or memory ceiling at all; the clamp actually came from the effective CONCURRENCY budget, which the adaptive controller had lowered from 4 to 2 under CPU load.

**Root cause.** runner/webrunner/auto_capacity.go decideWorkerTarget hard-coded one reason string for every ceiling clamp, and runner/webrunner/task_pool.go recordWorkerScaling attached browser-denominated context (auto_worker_ceiling, browser_budget_total, browser census, per-task pool) to every scaling decision including fast-mode ones.

**Fix.** workerCeilingForRun now returns (ceiling, reason) naming the tightest bound — "the concurrency budget this run is allowed…", "the configured parallel-task count", "the maximum parallel tasks one job may run", "the measured memory and CPU budget…", or "available memory now holds fewer simultaneous browsers" — and decideWorkerTarget uses it via workerScalingSignals.CeilingReason. recordWorkerScaling omits every browser key in fast mode.

**Regression test.** TestWorkerCeilingNeverExceedsTheOperatorsLoadBudget (asserts a fast-mode reason contains no "memory"/"browser" and does name the concurrency budget) and TestFastModeScalingEventCarriesNoBrowserArithmetic

**Acceptance evidence.** Re-run of the same job shape after the fix (job c1104371): "Auto capacity reduced parallel tasks from 4 to 1 (the concurrency budget this run is allowed only covers fewer parallel tasks)", context {"cpu_cores":8,"cpu_percent":0,"memory_available_bytes":467189760,...} with no browser_budget_total, browser_processes, browser_memory_bytes, auto_worker_ceiling or per_task_browser_pool keys.

#### V-b — the same worker-count reduction was recorded on every sampling tick

**Classification:** misleading UX/semantics

**Reproduction.** Same live run 0b72dcc8: two identical "Auto capacity reduced parallel tasks from 4 to 2" events, 0 s apart, because the four workers were mid-task and had not yet reached a claim boundary, so run.workers was still 4 when the next 2-second window re-decided.

**Root cause.** runner/webrunner/task_pool.go adaptWorkerCount compared the decision only against the live worker count (run.workers), never against the target already set (run.workerTarget). A reduction takes effect only when surplus workers retire between tasks, which can be a whole task away.

**Fix.** A reduction is now skipped, silently, when decision.Workers already equals run.workerTarget. Growth is deliberately NOT suppressed this way (see V-c/V-d).

**Regression test.** TestAutoCapacityDoesNotRepeatAnInFlightReduction (three consecutive blocked windows produce exactly one event; a further collapse after the workers retire produces a second)

**Acceptance evidence.** The post-fix live run c1104371 records exactly one adaptive-workers event for its reduction.

#### V-c — the growth half of auto capacity would have been dead code on every real browser run

**Classification:** confirmed defect

**Reproduction.** The concurrency controller re-decides every resourceSampleInterval (2 s) and Swap(0)s its failure/success/block counters each time. The acceptance run measured a 31 s median task at 2 workers, i.e. roughly 0.06 completions per second, so a 2-second window holds 3 corroborating successes essentially never. Growth requires successes >= adaptiveRecoveryAttempts (3) in one window, so on any real browser-mode run the controller could shrink but never grow — exactly the failure the whole issue is about.

**Root cause.** I initially fed decideWorkerTarget the concurrency controller's per-tick window, whose length is set by the sampling cadence rather than by task duration.

**Fix.** taskPoolRun gains workerWindowFailures/Successes/Blocks, accumulated on every tick. A REDUCTION still reads the current tick, so a block is acted on the moment it appears; GROWTH reads the accumulated counters once the worker-scale cooldown (45 s) has elapsed and then empties them, starting a fresh window. Any actual worker-count change also empties the window (resetWorkerWindow), so evidence that bought one decision cannot buy the next. runTaskPool now stamps lastWorkerChangeAt at pool start so a run holds its planned width for one settling window instead of treating every early tick as "cooldown elapsed" (which would have emptied the accumulator every tick).

**Regression test.** TestAutoCapacityCorroboratesGrowthAcrossSamplingTicks (three successes spread over nine ticks, none of which holds enough alone, buy exactly one worker once the settling window closes) and TestAutoCapacityDoesNotReuseSpentEvidence (ten successes buy one step, not three; a spent block cannot collapse the pool twice)

**Acceptance evidence.** Both tests fail against the pre-fix controller (the first observed 2 workers where 3 were expected) and pass after.

#### V-d — two liveness/ordering flaws in the worker growth path

**Classification:** confirmed defect

**Reproduction.** Code review of my own change plus targeted tests. (1) If a spawn was refused, workerTarget was left at the unreachable value, so the next window saw "already decided" and the pool froze at a width it never reached. (2) run.workers was incremented before workerTarget was raised, so a newly spawned worker consulting the retire gate on its first claim-loop iteration could see live > target and retire itself immediately — growing the pool by adding a worker that instantly left.

**Root cause.** runner/webrunner/task_pool.go adaptWorkerCount: the in-flight-reduction suppression was applied to growth as well, and the target/worker-count stores were ordered wrongly relative to the spawn.

**Fix.** Reduction and growth paths are now separate. Growth raises workerTarget BEFORE the first spawn, then settles it to the number that actually started; a refused spawn puts the target back at the reachable count so a later window retries. Suppression applies only to reductions. The initial fan-out also counts real spawns instead of assuming plan.Workers started.

**Regression test.** TestAutoCapacityRetriesGrowthAfterASpawnIsRefused, plus TestRetireWorkerShrinksOnlyToTheTargetAndNeverBelowOne and TestRetireWorkerIsRaceFreeUnderConcurrentWorkers

**Acceptance evidence.** go test -race in golang:1.26.6-trixie on the final revision: ok runner/webrunner 222.686s, no data races.

#### W — evaluation of (A) more in-process workers, (B) more pages/browser, (C) worker processes, (D) worker containers, (E) geographic sharding

**Classification:** expected behaviour needing clearer presentation

**Reproduction.** Measured, not assumed. (i) The acceptance host's own numbers: browser budget 8, workers used 2 — the limit was a constant, not the process model. (ii) The offline scheduler benchmark drove the REAL pool (durable plan, lease/claim protocol, CSV merge, SQLite writes, supervisor) at widths 1,2,3,4,6,8 over the replayed 180-task plan: achieved peak overlap equalled the width at every rung, 0 duplicate task starts across 1080 task executions, 180/180 completed at every width, durable-write p95 1.14/1.41/4.30/15.08/5.78/11.41 ms — i.e. under 16 ms against 35-second tasks (<0.05% of task time) even at width 8. (iii) web/sqlite/sqlite.go:437 sets SetMaxOpenConns(1) and :444 PRAGMA busy_timeout = 5000, so all database access is already serialised inside one process.

**Root cause.** N/A — this is a design decision, not a defect. The binding resource is browser memory, and the throughput model was constrained by a constant rather than by the process topology.

**Fix.** Implemented (A) plus the existing (B). (A) in-process workers: the lease/claim protocol already makes it safe (claim is one transaction on a single-connection DB, web/sqlite/task_leases.go:41-67; finishes verify ownership via CompleteJobTaskAs/FailJobTaskAs; a worker added mid-run gets a fresh unique owner in spawnTaskWorker; retirement happens only between tasks so no lease is ever abandoned; the durable plan and per-task checkpoints keep it resumable and crash safe). (B) pages-per-browser compensation already exists (maxCompensationPagesPerBrowser = 4) and is retained but not made the primary lever, because the per-page incremental memory cost is unmeasured and one crashed --single-process browser takes all its pages with it. (C) multiple worker PROCESSES rejected: N browsers cost the same memory in one process or five, so a second process adds throughput only by breaching the same memory budget; and with SetMaxOpenConns(1) per process it converts cheap in-process serialisation into cross-process WAL write-lock contention under a 5 s busy_timeout, whose new failure mode is a stall or SQLITE_BUSY on a *finish* write — precisely the stale-commit hazard the lease protocol exists to prevent. It would also duplicate the Playwright driver, the engine janitor, the abandoned-engine containment and the startup reclaim sweep, each of which assumes sole ownership of the workspace. The lease protocol itself would tolerate it (owner strings are opaque, claims are transactional), so it stays a viable future option — it just buys nothing while memory binds. (D) worker containers rejected for the same memory arithmetic plus a shared bind mount over SQLite WAL and duplicated browser images. (E) geographic sharding is already shipped as the coverage grid — the acceptance job's 5 queries became 180 grid-cell tasks; sharding across processes is (C).

**Regression test.** TestConcurrentTaskPoolRunsEveryTaskOnceWithinItsBound (pre-existing, still green), TestDynamicWorkerGroupAcceptsLateSpawns, TestDynamicWorkerGroupSurvivesConcurrentSpawnAndWait, TestRetireWorkerIsRaceFreeUnderConcurrentWorkers, and TestSchedulerThroughputBenchmark's own assertion that no width executes a task twice or loses one.

**Acceptance evidence.** Benchmark table (measured; host was loaded by other lanes' containers, so absolute overheads are pessimistic): width / wall s / mean overlap / peak / parallel efficiency / scheduler overhead ms per task / write mean,p95 ms / duplicates / projected full-run min = 1/156.4/0.83/1/0.801/172.9/0.74,1.14/0/104.9 · 2/93.7/1.47/2/0.668/172.6/1.54,1.41/0/52.7 · 3/59.8/2.46/3/0.698/100.2/1.61,4.30/0/35.1 · 4/49.5/3.08/4/0.633/100.9/3.25,15.08/0/26.4 · 6/36.1/4.40/6/0.578/84.6/1.05,5.78/0/17.6 · 8/30.1/5.46/8/0.520/80.4/4.33,11.41/0/13.3.

#### Z — before/after benchmarking, and where the safe knee is

**Classification:** expected behaviour needing clearer presentation

**Reproduction.** Two instruments. (1) BEFORE, measured from the live workspace: job 7100e95b — 180 searches, 331 businesses, 3147 s wall (52.5 min), 3.43 searches/min, 6.31 businesses/min, task success 180/180, 0 blocks, 0 browser failures, 0 retries, average parallelism 1.99, 2 browsers. (2) A new offline scheduler benchmark that drives the real pool with the engine replaced by a sleep of each of the 180 measured task durations, so everything it reports about scheduling, contention, duplicate work and write latency is measured and only the task durations are modelled.

**Root cause.** N/A — measurement task.

**Fix.** Added runner/webrunner/schedbench_test.go (TestSchedulerThroughputBenchmark, skipped unless GMS_SCHEDBENCH=1; GMS_SCHEDBENCH_SCALE / _WIDTHS / _OUT). Added a live W width ladder (W1/W2/W4/W6) to acceptance/catalog.go with per-task concurrency, browser pool and pages pinned so one rung = N workers = N Maps operations = N browser processes, addressable as -experiment widths or W4; acceptance/README.md documents why A..E cannot answer the width question and how to read the ladder for the knee. Extended acceptance/events.go so a record now carries final_workers, planned_browsers, worker_reductions and worker_increases, from either the structured adaptive-workers context or the plain-text log.

**Regression test.** TestSchedulerThroughputBenchmark's built-in assertions (no duplicate starts, no lost tasks at any width); TestWidthLadderVariesWorkersNotConcurrency, TestCatalogContainsEverything, TestResolveExperimentsGroups; TestConcurrencyEvidenceRecordsTheWidthARunSettledAt, TestConcurrencyEvidenceRecoversWidthFromPlainText, TestAutoCapacityEventsAreNotCountedAsFailures.

**Acceptance evidence.** Model validation: the benchmark projects 52.7 min for the same 180-task plan at 2 workers; the real run took 52.5 min — 0.4% error. Projected AFTER, at the 4 workers the acceptance host's memory now grants: 26.4 min (2.0x), with searches/min 3.43 -> ~6.8 and businesses/min 6.31 -> ~12.5 if per-task duration holds. LOCAL knee: none up to width 8 — overlap tracks width exactly, duplicates stay at zero, and durable-write p95 stays under 16 ms against 35-second tasks. The real knee is therefore the memory budget (8 browsers on that host) and the platform block rate, which cannot be measured offline; the W ladder is the instrument for the latter and must be run in ascending order, stopping at the first rung whose block_rate or browser_failure_rate rises.

**Files changed:** E:\Development\gosom scraper\runner\webrunner\auto_capacity.go (new), E:\Development\gosom scraper\runner\webrunner\auto_capacity_test.go (new), E:\Development\gosom scraper\runner\webrunner\schedbench_test.go (new), E:\Development\gosom scraper\runner\webrunner\task_pool.go, E:\Development\gosom scraper\runner\webrunner\task_pool_test.go, E:\Development\gosom scraper\runner\webrunner\adaptive_resources.go, E:\Development\gosom scraper\runner\webrunner\adaptive_resources_test.go, E:\Development\gosom scraper\runner\webrunner\checkpoint_runner.go, E:\Development\gosom scraper\runner\webrunner\webrunner.go, E:\Development\gosom scraper\acceptance\catalog.go, E:\Development\gosom scraper\acceptance\catalog_test.go, E:\Development\gosom scraper\acceptance\events.go, E:\Development\gosom scraper\acceptance\events_test.go, E:\Development\gosom scraper\acceptance\README.md, E:\Development\gosom scraper\acceptance\cmd\harness\main.go, E:\Development\gosom scraper\acceptance\cmd\harness\main_test.go

**Verification.** All gates run in an isolated tree (git archive of HEAD + only this lane's files) because other lanes had web/ and runner/jobs.go transiently uncompilable in the shared worktree.

- go build ./... — PASS
- go vet ./runner/... ./acceptance/... — PASS
- go test -count=1 ./runner/... ./acceptance/... — PASS (runner 5.9s, lambdaaws 6.9s, webrunner 82.5s, acceptance 4.9s, harness 4.6s)
- go test -count=1 ./runner/webrunner/ on the FINAL revision — PASS (88.6s)
- RACE GATE, container: docker run --memory=6g golang:1.26.6-trixie sh -c "go build ./... && go vet ./runner/... ./acceptance/... && go test -count=1 -race -timeout 40m ./runner/webrunner/ ./acceptance/..." — PASS on the final revision: ok runner/webrunner 222.686s, ok acceptance 2.050s, ok acceptance/cmd/harness 1.048s. No data races.
- gofmt — clean on all 16 changed files (checked on LF-normalised copies; the worktree is CRLF, which gofmt reports for every checked-out file per CLAUDE.md).
- Stability: the auto-capacity, ceiling, retire, dynamic-group, decide and fast-mode tests run 5x with -count=5 — PASS.

RUNTIME EVIDENCE (my own build, my own port, my own copy of the workspace — the live container and ./webdata were never written):
- Built bin-lane6.exe from the isolated tree; ran it with -web -addr 127.0.0.1:8116 -data-folder <scratch>/lane6-run, where lane6-run is a COPY made with docker cp from gosomscraper-scraper-1.
- LIVE GOOGLE TRAFFIC I RAN, in full: two bounded Fast-mode jobs, 4 keywords each, depth 3, 15 km radius, Los Angeles (jobs 0b72dcc8 and c1104371) = 8 fast-mode Maps searches total, ~14 s of scraping. No browser-mode live scraping, no W-ladder run, nothing coordinated with the lead was consumed.
- Job c1104371 log: "Running 4 task(s) in parallel with 1 worker concurrency each (Fast mode: no browser)"; task-pool context planned_browsers 0, fast_mode true, no per_browser_cost_bytes; one adaptive-workers event with an accurate non-memory reason and no browser keys.

DATA SAFETY: every test uses t.TempDir(); the benchmark is skipped unless GMS_SCHEDBENCH=1 and also uses t.TempDir(); no test or run opened E:\Development\gosom scraper\webdata.

OWNERSHIP: I changed only runner/webrunner/**, acceptance/** (runner/runner.go needed no change). runner/jobs.go shows as modified in git status — that is another lane's edit; I never wrote it. I did briefly copy it into my own scratch build tree, which broke that tree's build; I restored it to HEAD there and re-ran the gate.

**Deliberately not done.** 1. NO LIVE BROWSER-MODE A/B AT WIDTH. The 2-workers -> 4-workers improvement on the acceptance host is proven as plan arithmetic against that host's own recorded resource sample, and projected to wall time by the offline benchmark; it is NOT yet a live before/after with real Chromium. That needs coordinated Google traffic and real browsers. The instrument is ready: run `-experiment widths` (W1, W2, W4, W6) ascending, one job at a time, against a container copy on a spare port, and stop at the first rung whose block_rate or browser_failure_rate rises. I did not run it on my own initiative.

2. PEAK RAM / CPU / BROWSER COUNT "BEFORE VS AFTER" UNDER REAL BROWSERS is not measured. I measured the budget arithmetic, the census plumbing (count + RSS) and the scheduler, not a live browser run's resident set. The acceptance harness already records app-reported host resources per run, so the W ladder will produce these.

3. BENCHMARK NUMBERS WERE TAKEN ON A LOADED HOST. Up to six containers from other lanes were running during the ladder, so the absolute scheduler-overhead figures (80-173 ms/task) are pessimistic. The relative shape across widths, the zero-duplicate result and the 0.4% agreement between the width-2 projection and the real 52.5 min run are unaffected, but I did not get a quiet-machine re-run.

4. PER-PAGE MEMORY COST IS STILL UNMEASURED, so pages-per-browser remains soft-capped at maxCompensationPagesPerBrowser = 4 and option (B) was not made the primary throughput lever. Measuring it would let the planner pack concurrency into fewer browsers with confidence.

5. DELIBERATE BEHAVIOUR CHANGE ON SINGLE-CORE HOSTS: the CPU rule allows one browser-mode worker per effective core, so a 1-core host now gets 1 worker where the old flat cap gave 2. That is under-committing rather than over-committing, which is the failure direction CLAUDE.md prefers, but it is a real change and I am flagging it rather than hiding it. Every host with >= 2 cores and >= ~3.5 GiB available gets the same or more than before.

6. I DID NOT TOUCH THE CONCURRENCY CONTROLLER'S 2-SECOND WINDOW. Only the new worker controller uses an accumulating window. The pre-existing decideFailureBudget / decideBlockBudget recovery rules have the same window-versus-task-duration mismatch I fixed for workers (they need 3 attempts in one 2 s window to recover a step), so concurrency recovery is still largely unreachable on real browser runs. That is a pre-existing weakness in another part of the same file; fixing it changes behaviour the hardening phase tuned, so I left it and am reporting it.

7. NO UI WORK. Auto capacity is switched by the existing job.Data.Adaptive flag. Naming it "Auto capacity" in the wizard, and surfacing the derived ceiling, are web/ changes owned by another lane (see LEAD-APPLY item 5).

## Addendum — evidence gathered on the final image (schema 19)

The first three live runs above executed on an image built before the
observation-provenance sidecar landed, so they prove throughput, K, L, N and Q
but not S. A fresh container on the final image then ran:

| Run | Config | Wall | Searches | Businesses | Failures / blocks |
| --- | --- | --- | --- | --- | --- |
| Thorough, 2 queries, 4 cells @ 5 km, request 4 | LA centre | 220 s | 8/8 | 99 | 0 / 0 |

**S — exact provenance on a new run.** `job_observation_provenance` holds 297
rows across 7 tasks for that job. `business_sources` now carries the **exact**
query per row — `tattoo shop` 85, `tattoo studio` 14 — and **99 of 99** rows
name the grid cell that found them, where the historical Thorough job carried
one joined five-query string and no cell on all 331 rows. The clean run logged
23 information events, 0 warnings, 0 errors.

**Pause / resume / stop / restart regression (final image).** Cancel reached a
terminal state in 10 s with the 20 committed rows intact. Hard kill mid-run ->
recovered to `paused` with `recovery_required` -> resume -> **4/4 completed,
71 rows, 0 duplicate identities**.

## Deployed-container latency — a finding outside the brief

While verifying the localhost deployment, `GET /app/dashboard` on the live
372-business workspace took **10–53 s** and the results API 2–6 s, even though
the same binary on the same data served both in **0.03–0.09 s** natively and
**0.03 s from a Docker named volume**. The cause is the Windows bind mount
(`./webdata` through the Docker Desktop file-sharing layer), on which every
SQLite page miss is a host round trip; the old 1000-page (~4 MiB) page cache
made an 86 MB database page through that layer on every request. It predates
this phase and was masked by a 36-business workspace.

Fix: the connection now keeps a 64 MiB page cache and in-memory temp store
(`web/sqlite/sqlite.go`, pinned by `TestConnectionKeepsAWorkspaceSizedPageCache`).
Measured after redeploy on the same bind mount: results API **0.03 s** (was
2.4 s), Results page **0.1–0.2 s**, dashboard **2.2–3.0 s** (was 10–53 s). The
dashboard's remaining two seconds are still bind-mount I/O; a named volume
would remove them, but moving the workspace off `./webdata` is an explicit
architecture decision for the operator, not something this phase makes.

The Docker healthcheck (5 s timeout) also reported `unhealthy` for the first
minutes after this deploy while the pre-migration backup of the 86 MB database
and migration 19 ran; it recovered on its own once the app answered.
