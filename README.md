# peopleforce-cli

A command-line client for the [PeopleForce](https://peopleforce.io) HR API,
designed to be driven by AI agents (Claude Code, Codex) as comfortably as by
humans.

```bash
peopleforce employees list --status active --jq '.data[] | {id, email}'
peopleforce leave requests pending
peopleforce api call GET '/recruitment/vacancies?page=1'
```

## Install

Requires Go 1.26+.

```bash
make build          # ./peopleforce
make install        # into GOPATH/bin
```

## Auth

Get an API token in PeopleForce (Settings → Integrations → API) and either:

```bash
export PEOPLEFORCE_API_KEY="pf_..."          # recommended for agents/CI
# or persist it:
peopleforce auth login --api-key "pf_..."    # ~/.config/peopleforce/config.toml
peopleforce auth status                      # verify: source + live probe
```

An API key in argv is world-readable through `/proc/<pid>/cmdline`, shows up
in `ps` and in CI logs under `set -x`, so `--api-key -` reads it from stdin
instead (same `-` sentinel as `--input`):

```bash
pass show peopleforce | peopleforce auth login --api-key -
pass show peopleforce | peopleforce employees list --api-key -
```

`--api-key -` and `--input -` would consume the same stream, so using both is
a usage error (exit 2). There is no interactive prompt: a TTY prompt would
break the non-interactive contract agents rely on.

Precedence: `--api-key` flag > `PEOPLEFORCE_API_KEY` > config file profile
(`--profile` / `PEOPLEFORCE_PROFILE`). Naming a profile that does not exist is
an error when nothing else supplies a key, and a stderr warning when the key
came from the flag or the environment — a typo must not quietly run against
whichever tenant the environment happens to hold, but it also must not veto a
key that outranks the config file. `PEOPLEFORCE_API_URL` overrides the
base URL. `auth status` emits its JSON envelope even when it fails — a config
file it cannot parse is reported as `config_error` with
`"authenticated": false` (exit 2), which is precisely when a self-diagnosing
agent needs it.

## Output contract (for agents)

- **stdout**: data only. JSON by default, always `{"data": ..., "meta": {...}}`.
  Lists carry pagination in `meta` (`page`, `pages`, `count`, `items`).
- **stderr**: diagnostics only; errors are structured
  `{"error": {"type", "status", "message", "detail"}}`.
- **Exit codes**: `0` ok · `2` usage · `3` auth · `4` not found ·
  `5` validation · `6` rate-limited · `7` server error · `8` network ·
  `9` the request succeeded but its response could not be rendered (never
  safe to blindly re-run: a mutation already happened).
- Built-in filtering (no external jq): `--jq '.data[] | {id}'`, `--fields id,email`,
  `--raw`/`-r` for unquoted string output (like `jq -r`); with both, `--jq` wins.
- Other formats: `--output table` (humans), `--output ndjson` (streaming).
- Never interactive without a TTY. Destructive operations (deletes,
  `employees terminate`) require `--yes`; every mutation supports `--dry-run`.
- Lists: `--page N` (page size is fixed server-side) or `--all`
  (auto-paginates, capped by `--max-pages`, default 20; empty results are `[]`,
  never `null`). `--all` reports per-page progress on stderr, replaces
  `meta.page` with `meta.fetched` (the total it collected), and drops a page
  that merely replays page 1 — a backend ignoring `?page=` never inflates the
  result silently.
- An `--all` run that fails partway still writes the pages it did fetch to
  stdout, marked `"truncated": true` with `"next_page": N` in `meta`, while
  the exit code stays that of the failure (7 for a 5xx, 8 for a transport
  error) and the error itself goes to stderr. Keep that partial result and
  re-run with `--all --page N` to fetch the rest; the two concatenate. A run
  that fails on its first page writes nothing. `--page N` alongside `--all`
  is the start page and `--max-pages` caps how many pages that run fetches.
- The same `"truncated"` / `"next_page"` markers appear whenever `--max-pages`
  cuts a run short, which exits 0 because the cap is deliberate. Any `--all`
  envelope without them covered everything the endpoint had.
- Those markers live in `meta`, so they only exist under the default
  `--output json`. With `ndjson`, `table` or `--jq` there is no `meta`, and a
  run truncated by `--max-pages` also exits 0 — stdout and the exit code then
  look exactly like a complete run, and only the stderr note reveals the cap.
  Use `--output json` whenever completeness has to be verifiable.
- `--output ndjson` streams one data item per line and omits `meta` entirely;
  use the default `--output json` when you need pagination info.
- Other globals: `--timeout` (default 30s) and `--verbose` (log requests and
  retries to stderr).
- 429s are retried automatically honoring `Retry-After` in both RFC forms
  (`--max-retries`, default 3). Transient 5xx responses are retried only for
  idempotent methods — a POST is never re-sent (duplicate-write risk).
- Meta commands (`commands`, `version`, `auth status`, `api ops/describe`)
  emit the same `{"data": ...}` envelope and honor `--jq`/`--fields`/`--output`.
  Plain-text exceptions: `config path`, `agents-md`, `skill install`.

### Discovery

```bash
peopleforce commands                        # full command tree as JSON, one call
peopleforce api ops                         # all 203 API operations
peopleforce api describe GET /employees     # params & body schema of one op
peopleforce api call GET '/employees?page=2'  # raw escape hatch, still authed+normalized
```

Curated commands cover the core: employees (incl. salaries, documents, notes,
lifecycle), leave, tasks, teams, departments, divisions, locations, positions,
holidays, calendars, termination reference data, plus the multipart recruitment
uploads (`recruitment candidates create`, `recruitment candidates documents
upload` — `api call` bodies are JSON-only). Everything else is reachable via
`api call`; illegal URL bytes in its path (spaces, non-ASCII) are
percent-encoded automatically while bracket keys pass through verbatim.

Team membership is read from `teams list`: the upstream API has no
`GET /teams/{id}` and an employee record carries no team field, so the list
response is the only source. `team_lead` sits outside `team_members`, so
collect both to get everyone.

### Agent onboarding

```bash
peopleforce skill install     # writes .claude/skills/peopleforce/SKILL.md
peopleforce agents-md         # prints an AGENTS.md snippet
```

## Writing data

```bash
# typed flags for simple fields
peopleforce leave requests create --set employee_id:=7 --set leave_type_id:=2 \
  --set starts_on=2026-08-01 --set ends_on=2026-08-05

# --set key=value (string), key:=json (typed), dots nest
# custom fields are set flat by internal_name (see `peopleforce employee-fields list`)
peopleforce employees update 123 --set github=octocat --set address.city=Kyiv

# whole body from a file or stdin
peopleforce employees create --input @new-employee.json

# file uploads
peopleforce employees documents upload 42 --document @contract.pdf \
  --name "Contract" --document-folder-id 3

# preview any mutation
peopleforce employees terminate 123 --effective-from 2026-08-01 \
  --termination-type-id 1 --termination-reason-id 4 --eligible-for-rehire --dry-run

# bulk-update: many employees in one run, NDJSON report with the updated
# records inline (no follow-up GETs), exit 5 if any record failed
peopleforce employees bulk-update --input @updates.jsonl        # {"id":..,"set":{..}} per line
peopleforce employees bulk-update --input @updates.jsonl --dry-run
```

Custom fields are written as flat top-level keys by `internal_name`
(`--set github=...`) but read back under `.data.fields.<internal_name>.value`;
look up internal names with `peopleforce employee-fields list`.

## Architecture

The binary never parses OpenAPI at runtime. A build-time generator
(`internal/gen`, using [pb33f/libopenapi](https://github.com/pb33f/libopenapi))
compiles the vendored OpenAPI 3.1 spec (`internal/spec/peopleforce-openapi.json`)
plus manual curation (`internal/spec/overrides.yaml`) into a static operation
table (`internal/registry/registry.gen.go`). The cobra command tree, flags and
help are built from that table; a generic HTTP layer executes operations.

Why no SDK codegen: the upstream spec is wrong about responses in ~50 places
(missing schemas, wrong content types), so responses are passed through as
JSON and normalized by shape, not by spec. Wire-level quirks are handled
explicitly and pinned by tests:

- Literal bracket query params (`ids[]=1&ids[]=2`, `hired_on[gte]=...`) —
  never sanitized or percent-encoded (`internal/httpx/query.go`).
- The upstream path typo `/termintation_reasons/{id}` is preserved verbatim.
- Multipart uploads, `update_avatar`'s base64 data-URI JSON, DELETE with body.
- All six response envelope shapes normalize into `{data, meta}`
  (`internal/envelope`).

## Updating the API spec

```bash
make update-spec SPEC_URL=https://dash.readme.com/api/v1/api-registry/<hash>
```

The registry hash changes between spec versions; copy the current one from
the `npx api install` command shown at
[developer.peopleforce.io](https://developer.peopleforce.io). Then review the
diff of `testdata/golden/commands.json` — it shows exactly which operations,
parameters, and flags changed. New endpoints appear in `api ops`/`api call`
automatically; promote them to curated commands in
`internal/spec/overrides.yaml` when needed.

## Development

```bash
make generate   # spec + overrides.yaml → registry.gen.go + golden snapshot
make test       # go vet + unit + E2E (httptest) tests
```
