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

Requires Go 1.24+.

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

Precedence: `--api-key` flag > `PEOPLEFORCE_API_KEY` > config file profile
(`--profile` / `PEOPLEFORCE_PROFILE`). `PEOPLEFORCE_API_URL` overrides the
base URL.

## Output contract (for agents)

- **stdout**: data only. JSON by default, always `{"data": ..., "meta": {...}}`.
  Lists carry pagination in `meta` (`page`, `pages`, `count`, `items`).
- **stderr**: diagnostics only; errors are structured
  `{"error": {"type", "status", "message", "detail"}}`.
- **Exit codes**: `0` ok · `2` usage · `3` auth · `4` not found ·
  `5` validation · `6` rate-limited · `7` server error · `8` network.
- Built-in filtering (no external jq): `--jq '.data[] | {id}'`, `--fields id,email`,
  `--raw`/`-r` for unquoted string output (like `jq -r`); with both, `--jq` wins.
- Other formats: `--output table` (humans), `--output ndjson` (streaming).
- Never interactive without a TTY. Destructive operations (deletes,
  `employees terminate`) require `--yes`; every mutation supports `--dry-run`.
- Lists: `--page N` (page size is fixed server-side) or `--all`
  (auto-paginates, capped by `--max-pages`, default 20; empty results are `[]`,
  never `null`).
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
