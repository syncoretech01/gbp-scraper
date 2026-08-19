# CLAUDE.md — persistent project rules

Google Maps Scraper (`github.com/gosom/google-maps-scraper`), Go 1.26.5, plus a
local-first web application layer under `web/`. This file holds the rules that
survive between sessions. Read it before changing anything.

## 0. Hard rule: never destroy local data

The following are **irreplaceable user data**. Never delete, truncate, move,
overwrite, or "regenerate" them, and never suggest a command that would:

- `webdata/` (git-ignored) — the durable local workspace bind mount.
- `webdata/jobs.db` (+ `-wal`, `-shm`) — the live SQLite database.
- `webdata/*.csv` — UUID-named per-job result files.
- `webdata/.proxy-master-key` — AES-256-GCM key for encrypted proxy
  credentials. Without it, stored proxy secrets are unrecoverable. It is never
  embedded in a database download or backup.
- Any `*.backup-*` / pre-migration copy under `webdata/`.

Corollaries:

- `docker compose down` is fine; **never** add `--volumes`.
- Migrations must be additive and idempotent, must take a verified copy first,
  and must refuse to run against a forward (newer) schema.
- Do not `git checkout`/`reset`/`stash`/`clean` uncommitted work in this repo
  without an explicit instruction naming the files. Large amounts of unfinished
  implementation live in the worktree.
- Tests must never open the real `webdata/jobs.db`. Use `t.TempDir()`.

## 1. Source-of-truth documents

| Document | Role |
| --- | --- |
| `docs/Google_Maps_Scraper_Local_Improvement_Specification.md` | Authoritative product specification. Do not edit; it is a restored artifact. |
| `docs/implementation-progress.md` | The requirement checklist and implementation log. Update as work lands. |
| `docs/technical-limitations.md` | Authoritative boundary for spec items that are *not* implemented. Anything unimplemented must be recorded here with a specific technical reason. |
| `docs/local-workspace.md` | Operator guide for the local Docker workspace. |
| `AGENTS.md` | Go code style rules. Still applies; this file does not replace it. |
| `README.md` | Upstream CLI/user documentation. Keep flag docs accurate. |

### Checklist discipline

`docs/implementation-progress.md` uses `[ ]` / `[x]`. **Only tick `[x]` after
code, a passing test, and runtime evidence all exist.** A ticked box is a
claim that the feature genuinely works. If something is implemented but not
verified, leave it unticked and say so. If something will not be implemented,
give it a specific entry in `docs/technical-limitations.md` — never a vague one.

Both progress and limitations docs drift behind the worktree. Reconcile them
against the actual code rather than trusting them.

## 2. Compatibility constraints (non-negotiable)

Preserve all of the following. Additions must be backward compatible.

- **Scraper engine behavior.** `gmaps/`, `deduper/`, `exiter/`, `runner/` drive
  the actual scrape. New options must default to today's behavior.
- **CLI flags and run modes.** `runner.ParseConfig` and the seven run modes in
  `main.go` (`file`, `database`, `database-produce`, `install-playwright`,
  `web`, `aws-lambda`, `aws-lambda-invoker`). No flag may be removed or change
  meaning.
- **REST API.** Existing routes and response shapes stay. New capability goes
  behind new `/api/v1/...` routes. Note the legacy quirks that must not be
  "fixed": legacy job status values (`pending`, `working`, `ok`, `failed`) and
  `time.Duration` fields serialized as **nanosecond integers**.
- **CSV output schema.** The per-job UUID CSV keeps its existing header and
  column order; retry/restart must merge rather than replace.
- **Docker / local startup.** `Dockerfile`, `compose.yaml`. The published port
  stays `127.0.0.1:8080`; the native default bind stays loopback. Telemetry
  stays disabled by default (`DISABLE_TELEMETRY=1`).
- **`/legacy` UI.** The preserved historical UI keeps its own CDN/tile
  allow-list; the new local app keeps the strict CSP.

## 3. Local-first product rules

- No mandatory paid API, hosted database, SaaS, or cloud worker. Everything
  required must work offline on one machine.
- Third-party front-end assets are **vendored** under `web/static/vendor/` and
  served from the embedded FS — never a runtime CDN fetch (the CSP forbids it).
- Optional local AI (Ollama) is off by default and fully removable.
- Secrets (proxy URLs/passwords, keys) are encrypted at rest and masked in UI,
  logs, errors, and exports.
- Every visible functional control must have a working backend path. If a
  capability is unavailable, **hide the control** — do not ship a placeholder.
- SMTP/mailbox checks are low confidence and must never be presented as proof
  that an address is owned or read.

## 4. Build, test, and verification commands

Go 1.26.5 (`go.mod`). Windows notes: `make` is not installed; run the underlying
commands directly. There is no system Go on PATH either — a toolchain is
extracted at `C:\Users\DELL\golang\go`, so prefix commands with
`$env:PATH = "C:\Users\DELL\golang\go\bin;$env:PATH"`.

`-race` needs cgo and a C compiler, which this host does not have. Run the race
gate in a container instead (see below). The non-race suite runs natively.

```powershell
go build ./...                       # must pass
go vet ./...                         # must pass
go test ./...                        # fast loop
go test -race -timeout 7m ./...      # full gate (Makefile `test` target)
gofmt -l <files you changed>         # must print nothing
go test ./web/... ./web/sqlite/...   # focused local-app loop
node --test skills/google-maps-scraper/scripts/select-proxy-sponsors.test.mjs
```

`gofmt -l` over the whole tree is **not** a useful gate here: files checked out
from git carry CRLF endings and gofmt reports every one of them. Only check the
files you actually touched.

Race gate (container, since `-race` needs cgo):

```powershell
docker run --rm --memory=6g -v "e:\Development\gosom scraper:/src" -w /src `
  golang:1.26.6-trixie sh -c "go build ./... && go vet ./... && go test -count=1 -race -timeout 20m ./..."
```

Docker gate:

```powershell
docker compose build scraper
docker compose up -d
# expect healthy on http://127.0.0.1:8080/, then restart and re-check
```

**Runtime smoke tests must not touch the live workspace.** A Compose container
(`gosomscraper-scraper-1`) may already be running and holding `webdata/jobs.db`
open. Copy the data out and test a separate container against the copy on a
different port:

```powershell
docker cp gosomscraper-scraper-1:/gmapsdata/. <scratch>\webdata-copy
docker run -d --name gmaps-verify -p 127.0.0.1:8099:8080 -e DISABLE_TELEMETRY=1 `
  -v "<scratch>\webdata-copy:/gmapsdata" <image> -web -addr 0.0.0.0:8080 -data-folder /gmapsdata
```

Never point a test container at `./webdata`.

`go tool golangci-lint run ./...` uses an unusually broad opinionated profile.
Security-relevant findings (dynamic SQL, unchecked type assertions, credential
handling) must be fixed. A zero-warning claim is deliberately **not** made for
style/complexity rules (`goconst`, `gocyclo`, `gocritic`, `wsl`, …).

## 5. Architecture map

```
main.go                 run-mode factory
runner/                 CLI config + run modes
  webrunner/            local web-app runner, job queue, checkpoint runner
gmaps/                  scrape jobs: search, place detail, email
exiter/                 pipeline counters + stop conditions
deduper/                in-run dedup
web/                    local application (HTTP handlers, embedded UI)
  sqlite/               schema, migrations, all persistence
  jobruntime/           shared runtime types
  enrichment/           website crawl, email/social extraction, signatures
  resultimport/         CSV -> normalized business import
  static/               templates, css, js, vendored Leaflet
```

Conventions in `web/`: `app_*.go` render server pages, `*_api.go` register
`/api/v1` JSON routes, `web/web.go` is the single route table, and every
persistence call goes through `web/sqlite/`. Route registration helpers are
named `register<Area>Routes(mux)`.

## 6. Working rules for agents

- Audit before editing. This worktree carries large uncommitted work; assume it
  is valuable and unfinished, not disposable.
- Keep changes additive. Prefer new files/routes over rewriting existing ones.
- Match surrounding style: tabs (gofmt), error wrapping with `%w`, `ctx` first,
  early returns, godoc on exported symbols, no magic numbers.
- Do not commit or push unless explicitly asked.
- Report honestly: if a test fails or a step was skipped, say so with output.
