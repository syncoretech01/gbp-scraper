# GBP prospecting layer

How the **GBP Lead Scraper build specification** maps onto this repository.
The specification was written before this repository became the full local
scraper application, when the plan was a separate `gbp-lead-scraper` wrapper
repo. Its product behaviour is authoritative; its repo topology is not. Every
capability it describes is implemented **inside** the local application,
reusing the systems that already exist rather than wrapping them.

## Standalone mode is the primary mode

The application is fully standalone: no step of the GBP workflow calls the
Lead Engine, a CRM, site-whisper, or an external email verifier.

- **The complete local pipeline**: ZIP/category coverage (embedded US ZIP
  dataset, `web/prospect/uszips.csv.gz`, ~41k ZIPs with city/state/population;
  provenance and licenses in `web/prospect/ZIPDATA.md`; CSV upload still
  overrides) -> scrape -> dedupe -> **website pre-classification**
  (`web/enrichment/preclassify.go`, a bounded single-page DNS/TLS/HTTP probe
  queued through the normal enrichment queue with `"preclassify": true`) ->
  email discovery (existing enrichment) -> scoring/tier -> call opener ->
  Results filters/views -> export. The wizard's GBP coverage block carries a
  "Prospecting pipeline" preset that switches the enrichment step on; Results
  has a "Pre-classify websites" bulk action for existing rows.
- **Dormant future-integration surfaces**: `GET /api/v1/prospects/discovered`
  and the `discovered_companies` export exist for a future Lead-Engine
  integration but are disabled by default behind the
  `prospect.future_integrations` setting (Settings -> "Future integrations
  (dormant)"). While off, the endpoint answers 403
  `future_integrations_disabled` and the export format is neither offered nor
  accepted. The stored boundary URLs (email verifier, audit service) remain
  stored-only configuration; nothing is ever called.

## Where each spec section landed

| Spec section | Implementation here |
| --- | --- |
| Harvester wrapper, ZIP × synonym tiling, 120-result cap | The engine **is** this repository — no wrapper. `web/prospect` generates ZIP × category-synonym query lines (`BuildQueries`) fed into the existing multi-query wizard; each query becomes one durable checkpoint task, which is exactly the tiling the cap requires. A bundled sample ZIP list ships for demos; production coverage comes from a user CSV (`zip,city,state,latitude,longitude,population`). |
| Place-ID-first identity, idempotent dedupe | Already existed: `place_id` leads the canonical identity keys, imports are idempotent, and duplicates merge non-destructively. Nothing new was built. |
| Website-status classifier (the signature contribution) | `web/prospect.Classify` — `NO_WEBSITE`, `SOCIAL_ONLY`, `DEAD`, `PARKED`, `SSL_BROKEN`, `FREE_BUILDER`, `NO_HTTPS`, `LIVE`. Static classes (no website / social host / free builder / website-is-the-Maps-URL) conclude instantly from the business row; audit-dependent classes (`DEAD`, `PARKED`, `SSL_BROKEN`, `NO_HTTPS`, `LIVE`) conclude from the existing enrichment audit — the classifier never crawls anything itself. All three named edge cases are covered: a lingering `business.site` URL, `website == Maps listing URL`, and a resolving domain with no MX (an explicit scoring signal). |
| Email discovery, not verification | Already existed: the enrichment worker extracts mailto/visible/contact-page emails with de-obfuscation and DNS/MX checks. Verification stays in `syncore-email-verifier` (boundary below). |
| Mode-1 scoring: worth-calling score, tiers, opener | `web/prospect.Score` — a pure function over signals with configurable weights/thresholds/tier cut-offs (stored in settings, editable in the UI), explainable per-signal reasons, tiers A–F, and editable per-status call-opener templates rendered per business. Pure by design so Mode 2 can hand the same signals to the Engine's `LeadScore` pipeline untouched. |
| `DiscoveredCompany` contract | `GET /api/v1/prospects/discovered?job_id=…` returns the Engine's exact shape: `providerCompanyId` = place_id, `domain` via the Engine's verbatim `domainFromWebsite` rule, `meta.rawPayload` carrying `website_status` and the raw signals. The `discovered_companies` export format writes the same objects as JSONL. This endpoint **is** the Mode-2 provider boundary: the CRM's ~40-line `gbp-scraper.ts` adapter points `GBP_SCRAPER_URL` at this application. |
| Two output shims | Mode 1 = the existing export centre (CSV/XLSX call sheet columns include prospect status, score, tier, reasons, opener) plus `discovered_companies` JSONL. Mode 2 = the API endpoint above. Same core, two thin surfaces, as specified. |

## What was deliberately NOT built here (per the spec's DO-NOT-BUILD table)

- **Website audit / screenshots / Lighthouse** — `syncore-audit-bot` (site-whisper). This layer only classifies which URLs are worth auditing; the existing enrichment audit is the fast pre-check, not a replacement audit. A boundary setting stores the audit service URL.
- **Email verification (MX/SMTP grades)** — `syncore-email-verifier`. This layer discovers addresses only. A boundary setting stores the verifier URL.
- **Normalize/dedupe→CRM, SDR cockpit, calling, DNC/phone line-type, suppression** — `lead-engine-crm`. The contract endpoint is the only coupling.

The two boundary URLs (`prospect.email_verifier_url`, `prospect.audit_service_url`)
are validated to loopback/private addresses, stored in settings, and exposed in
the Prospecting settings card. Nothing in this repository calls them yet — they
exist so Mode-3 wiring has a configured, documented home without duplicating
either service's functionality here.

## Signals, storage, and incremental compatibility

Classification writes to `businesses`: `prospect_status`, `prospect_score`,
`prospect_tier`, `prospect_signals` (raw snapshot), `prospect_reasons`
(explainable contributions), `prospect_updated_at` (migration 11).
Classification runs automatically after every import and after every website
audit, and on demand (bulk action / API). It never touches content hashes,
versions, FTS inputs, or deletion state, so rescan modes, change detection and
dedupe behave exactly as before; a status **transition** records one
`prospect_status_changed` change row so prospect movement shows up in the
existing change history.

## Mode map (spec §2/§13 reconciled)

- **Mode 1 (standalone)** — this application as it ships: harvest, classify,
  score, call sheet exports, dashboards. No scratch Postgres, no Node wrapper,
  no AWS scaffolding needed: the durable local workspace replaces all three.
- **Mode 2 (integrated)** — the Engine calls `GET /api/v1/prospects/discovered`
  through its `gbp_scraper` provider adapter (CRM-side: one registry entry, one
  union member, one ~40-line adapter — exactly the footprint the spec names).
  Verify the two Step-4 contract questions in the spec against the live CRM
  before writing that adapter; nothing here blocks on them.
