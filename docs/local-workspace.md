# Local workspace guide

This workspace is designed to keep jobs, results, logs, exports, and settings
on this computer. The default Compose configuration publishes the web UI only
on `127.0.0.1` and disables telemetry.

## Start on Windows

1. Start Docker Desktop and wait until it reports that the engine is running.
2. Open PowerShell in this repository.
3. Start or rebuild the application:

   ```powershell
   docker compose up -d --build
   ```

4. Open <http://127.0.0.1:8080/>.

The bind-mounted `webdata` directory is the durable source of truth. Rebuilding
or replacing the container does not delete `webdata/jobs.db` or the UUID-named
CSV files.

Useful lifecycle commands:

```powershell
docker compose ps
docker compose logs -f scraper
docker compose stop
docker compose start
docker compose down
```

`docker compose down` removes the container and network but not the bind-mounted
`webdata` files. Do not add `--volumes` when preserving unrelated named volumes
matters.

## Thorough San Francisco dentist search

In **New Scrape**, use the **San Francisco** preset and these starting values:

- Queries: `dentist`, `dental clinic`, and any genuinely distinct specialties
  needed for the research. Exact duplicate queries are removed automatically.
- Coverage: exhaustive grid, not Fast Mode.
- San Francisco bounding box:
  `37.708,-122.515,37.833,-122.354`.
- Grid cell: `2.5 km` for a practical first pass. Smaller cells create more
  overlapping searches and take considerably longer.
- Centre: latitude `37.7749`, longitude `-122.4194`.
- Zoom: `14` or `15`.
- Depth: `10` for the first pass; increase only after checking coverage.
- Runtime: at least `60m`; use `90m` or more for extra queries or smaller cells.
- Email/website crawl: off for the collection pass. Run enrichment afterward so
  slow or unavailable business websites cannot consume the Maps-search budget.

The radius field is a strict distance filter only in **Fast Mode**. A normal
scrape does not become city-wide merely because a large radius is entered; use
the grid/bounding-box coverage mode for a thorough city search.

## Why a job can stop with fewer rows than candidates

Google Maps search pages first discover candidate listings. Each candidate then
needs a detail task, and optional website/email crawling adds separate network
work. A runtime limit can therefore stop a run after candidates were discovered
but before every detail row was committed. The upgraded lifecycle labels this
as **Partial**, preserves every committed row, and allows a restart. It does not
mislabel a deadline-limited file as a fully completed job.

## Responsible use

Use conservative concurrency and comply with applicable laws, contractual
terms, robots/rate policies, privacy obligations, and outreach rules. The tool
works without proxies for modest local research. Large reliable proxy networks,
CAPTCHA solving, and high-confidence mailbox verification are not included and
may require paid third-party services. SMTP or mailbox heuristics must never be
treated as proof that a person owns or reads an address.
