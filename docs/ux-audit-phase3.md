# Phase 3 UX audit — the deployed application

Audited by driving the running app at `http://127.0.0.1:8080` through Chrome
DevTools Protocol: every major page captured at 1920, plus 1440 and 1280 for
the responsive pass, plus wizard steps 1–7, the result drawer, and the job
monitor. Findings below are from those screenshots and from interacting with
the real DOM — not from reading templates.

Ranking: **P0** blocks or seriously confuses the core workflow · **P1** major
usability/product issue · **P2** polish.

## What is already good (keep, do not regress)

- **The result drawer** is the strongest surface in the product: explainable
  quality score with per-factor contributions, direct contact actions
  (Call / Website / Maps / Copy phone), and Overview/Website audit/Contacts/
  Provenance/Duplicates/History tabs.
- **The Review step** (wizard step 7) is genuinely well designed: queries, grid
  cells, total tasks, estimated runtime, and four green pre-flight checks.
- **Jobs list**: tab counts (All/Active/Queued/Paused/Needs attention), clear
  state chips, and educational empty-state cards.
- **Dashboard "Needs attention"** panel is the most actionable thing on the
  home screen.
- The design system itself is coherent: one token set, consistent cards,
  buttons, and tables. This audit does **not** propose replacing it.

## P0 — blocks or seriously confuses the workflow

**P0-1. New Scrape opens in Advanced mode with seven steps.**
The mode switch (Basic / Advanced / GBP Prospecting) defaults to **Advanced**,
and the page describes itself as "seven guided steps". A first-time user is
handed Data fields, Filters, and a Performance step containing sixteen numeric
engine parameters before they can start a scrape. Advanced must remain
available, but it must not be the default path.

**P0-2. The single highest-impact choice is a checkbox at the bottom of step 6.**
"Fast HTTP mode" is rendered as one unlabelled-consequence checkbox among four,
below Memory ceiling and Checkpoint interval. Phase 2 measured Fast mode on an
identical workload at **20 businesses in 2s versus 127s in browser mode, with
identical yield and zero browser failures**. This is the first decision a user
should make, framed by what it costs them (no grid coverage, no JavaScript
rendering), not the last.

**P0-3. The Location step asks for coordinates.**
The primary inputs are "Centre latitude 37.7749" and "Centre longitude
-122.4194", alongside "Maps zoom 12", "Coverage extent (metres) 10000" and
"Grid-cell size (km) 2.5" — metres and kilometres in adjacent fields. There is
no place-name search. A normal user cannot express "dentists in Austin"
without leaving the product to look up coordinates.

**P0-4. The cost of the default configuration is invisible where it is chosen.**
The Location step's own summary reads Locations 1 · Cells 64 · Queries 2 ·
**Tasks 128**, with no time attached. The runtime estimate (~45 min) exists only
on step 7, after every decision has been made. The number that changes the
answer — grid-cell size — is three fields above a task count whose consequence
is not shown.

**P0-5. The Job Monitor is an engineering dashboard.**
On a **completed** job it renders eight pipeline stage cards, eight detail
cards and seven KPI tiles — twenty-three boxes — including "Last HTTP status",
"Queue depth", "Average response time", "Coordinates", and "Grid cell". The
string "not reported yet" appears fourteen times on a job that finished
successfully, which reads as breakage. Absent entirely: what happened in one
sentence, whether the data is safe, and what to do next.

## P1 — major usability / product issues

**P1-6. The Results default view shows four columns of nothing.**
For a freshly scraped set, SIGNAL reads "Not scored", TIER "—", and SCORE "—"
on every row, while WEBSITE reads "unknown" on every row. Four of eight columns
carry no information, and a wide unnamed column sits between CONTACT and the
row actions. The primary prospecting workspace opens on an empty analysis.

**P1-7. Results scrolls the whole shell horizontally at 1280.**
Measured: `document.scrollWidth` 1359 vs `clientWidth` 1265, offender
`table.data-table.results-table`. The table must own its overflow.

**P1-8. The table contradicts the drawer.** The row says "Not scored"; opening
that same row's drawer shows "quality 48/100 · 65% evidence confidence" with a
full factor breakdown. Two different scores share one word.

**P1-9. A wizard feature is permanently dead.** `app-wizard.js` calls
`GET /api/v1/templates`, which is **not a registered route** (only
`/api/v1/templates/{id}/…` sub-routes exist). It fails silently, so the
campaign-template picker never appears.

**P1-10. Unknown app URLs return bare unstyled text.** `404 page not found` in
the browser default font, outside the shell, with no way back.

**P1-11. Map Explorer wastes width and dulls the basemap.** The map pane ends
at x≈1465 of 1920, leaving ~415px of empty page beside it, and a translucent
grey coverage rectangle is drawn over the whole city, flattening the streets
the user is trying to read.

**P1-12. "not recorded" / "not reported yet" / "not configured" appear 46 times**
across templates and page handlers, including in prime dashboard KPI positions
("Collection speed: not recorded", "Proxy health: not recorded") and in the
Jobs table ("Stopped — end time not recorded", "Ran for not recorded").
Missing data should read as absence, not as an engineering status.

**P1-13. The Dashboard spends its best space on low-value metrics.** Of six KPI
tiles, two are "not recorded" and one is "Local Storage 14.3 MiB", which wraps
onto its own row. The large "Data availability" panel duplicates what "Needs
attention" already says more actionably.

**P1-14. Text overlap bug on the Job Monitor**: the Generating-grid card renders
"GeographMaps search near 37.7749, -122.4194 coverage" — the label "Geographic
coverage" and its value are drawn over each other.

**P1-15. Settings is one continuous 5,200px+ scroll** with no sectioning or
in-page navigation.

**P1-16. Four overlapping concurrency knobs.** "Concurrency", "Parallel tasks",
"Browser pool" and "Pages per browser" sit adjacent with no statement of how
they combine. Phase 2 proved these interact non-obviously (workers × per-worker
pool sets the real browser count); asking a user to reason about them is asking
them to model the engine.

## P2 — polish

- "Hidden steps still submit their saved defaults." — states an implementation
  detail as reassurance.
- The Jobs configuration column prints "Maps search near 37.7749, -122.4194".
- The wizard's "Saved keyword sets" block sits mid-form between the query box
  and its own advanced disclosures.
- Dashboard "Recent campaigns" is the last thing on the page despite being the
  natural continue-working entry point.
- Map "Coordinates 37.7749, -122.4194" and "Queries not configured / Tasks not
  configured" in the coverage strip.

## Cross-cutting terminology

The UI mixes user outcomes with engine internals. The vocabulary this phase
standardises on:

| Instead of | Say | Because |
| --- | --- | --- |
| Fast HTTP mode | **Fast mode** — quick, no map grid | names the trade, not the transport |
| (no name) browser path | **Thorough mode** — full map coverage | the grid walk is the point |
| Concurrency / Parallel tasks / Browser pool / Pages per browser | **Speed** preset + advanced tuning | four knobs the user cannot combine correctly |
| Cells / Tasks | **Areas to search** / **searches to run** | task is an engine word |
| Maps zoom | (hidden; derived) | never user-facing |
| Coverage extent (metres) | **Search radius (km)** | one unit |
| Runtime limit / Maximum runtime | **Stop after** | states the consequence |
| Partial | **Stopped early — results kept** | the fear is data loss |
| not recorded / not reported yet | — | absence is not a status |

## Outcome

Every P0 and every P1 in this audit was implemented and re-verified against the
running application. The evidence is a second screenshot pass over all fifteen
surfaces at 1920, plus 1280 and 1440, plus twenty-four scripted control checks
driven through the browser.

| Item | Result |
| --- | --- |
| P0-1 Advanced default | Basic is the landing mode: three steps, not seven |
| P0-2 Fast mode buried | A two-card Fast/Thorough chooser is now the first decision on step 1 |
| P0-3 Coordinates as input | Place-first, kilometres only; coordinates and zoom in a disclosure |
| P0-4 Invisible cost | Step 2 states areas, searches, estimated runtime and the stop-after limit, and warns when the estimate exceeds the limit |
| P0-5 Engineering monitor | Leads with a plain sentence, data-safety statement and next actions; internals in disclosures |
| P1-6 Dead columns | Default table carries business, category, location, website, contact and rating; an inline banner offers the ranking pass |
| P1-7 Shell overflow at 1280 | Fixed: 1280/1280 (was 1359/1265). Zero overflow on every page at 1280, 1440 and 1920 |
| P1-8 Score contradiction | Quality (record confidence) and prospect tier are now named distinctly |
| P1-9 Dead template picker | `GET /api/v1/templates` registered; picker hydrates |
| P1-10 Bare 404 | Styled in-shell page with three ways to continue |
| P1-11 Map | Fills the pane; cells outlined instead of washed over, basemap readable |
| P1-12 Placeholder strings | Removed from user-facing values across handlers and templates |
| P1-13 Dashboard vanity metrics | Four real KPIs; "Needs attention" promoted; "Continue working" added |
| P1-14 Text overlap | Fixed |
| P1-15 Settings scroll | Sectioned with in-page navigation |
| P1-16 Four concurrency knobs | Grouped under one explanation, behind "Advanced tuning" |

### One audit finding was wrong

P1-11 claimed the Map Explorer wasted ~415px because the map pane ended early.
The specialist measured the live DOM and found the stage and the Leaflet
container already filled the pane: the white band in the baseline screenshot
was **OpenStreetMap tiles that had not finished downloading within the
screenshot's wait window**, not a layout fault. The pane was still widened and
the overlay problem was real and fixed, but the original diagnosis is recorded
here as incorrect rather than quietly restated as a success.
