# peopleforce-cli

[![test](https://github.com/infrabay/peopleforce-cli/actions/workflows/test.yml/badge.svg)](https://github.com/infrabay/peopleforce-cli/actions/workflows/test.yml)
[![release](https://img.shields.io/github/v/release/infrabay/peopleforce-cli)](https://github.com/infrabay/peopleforce-cli/releases)
[![license](https://img.shields.io/github/license/infrabay/peopleforce-cli)](LICENSE)

A command-line client for the [PeopleForce](https://peopleforce.io) HR API,
designed to be driven by AI agents (Claude Code, Codex) as comfortably as by
humans.

> This is an independent, community project. It is not affiliated with,
> endorsed by, or supported by PeopleForce.

```bash
peopleforce employees list --status active --jq '.data[] | {id, email}'
peopleforce leave requests pending
peopleforce api call GET '/recruitment/vacancies?page=1'
```

## Install

Homebrew (macOS, Linux):

```bash
brew install infrabay/tap/peopleforce
```

With Go (the version in `go.mod` or newer):

```bash
go install github.com/infrabay/peopleforce-cli/cmd/peopleforce@latest
```

Or download a binary for Linux, macOS or Windows from
[Releases](https://github.com/infrabay/peopleforce-cli/releases) and check it
against the `*_checksums.txt` file published with it.

From source:

```bash
git clone https://github.com/infrabay/peopleforce-cli && cd peopleforce-cli
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
`"authenticated": false` (exit 2), and a probe that cannot reach the API as
`probe_error` (exit 8), which is precisely when a self-diagnosing agent needs
it.

## Output contract (for agents)

- **stdout**: data only. JSON by default, always `{"data": ..., "meta": {...}}`.
  Lists carry pagination in `meta` (`page`, `pages`, `count`, `items`).
- **stderr**: diagnostics only; errors are structured
  `{"error": {"type", "status", "message", "detail"}}` (plain sentences under
  `--output table`).
- **Exit codes**: `0` ok · `2` usage · `3` auth · `4` not found ·
  `5` validation · `6` rate-limited · `7` server error · `8` network ·
  `9` the request succeeded but its response could not be read or rendered
  (never safe to blindly re-run: a mutation already happened). A write whose
  2xx body is cut off, oversized or not JSON exits 9; the same on a GET exits
  8 (cut off) or 7 (not JSON), since retrying a read is safe. An empty body,
  such as a 204, is not an error.
- Built-in filtering (no external jq): `--jq '.data[] | {id}'`, `--fields id,email`,
  `--raw`/`-r` for unquoted string output (like `jq -r`); with both, `--jq` wins.
  An invalid `--jq`, including an unknown function, exits 2 before any
  request is sent. Integers keep every digit, beyond 2^53 too.
- Other formats: `--output table` (humans), `--output ndjson` (streaming).
- Never interactive without a TTY. Destructive operations (deletes,
  `employees terminate`) require `--yes`; every mutation supports `--dry-run`,
  including `auth login`, whose preview names the profile and config path but
  never shows the key.
- Lists: `--page N` (page size is fixed server-side) or `--all`
  (auto-paginates, capped by `--max-pages`, default 20; empty results are `[]`,
  never `null`). `--all` reports per-page progress on stderr, replaces
  `meta.page` with `meta.fetched` (the total it collected), and drops a page
  that merely replays the first page — a backend ignoring `?page=` never
  inflates the result silently. When the pagination metadata says more pages
  exist than the replay let it keep, the result is marked truncated (exit 0).
- An `--all` run that fails partway still writes the pages it did fetch to
  stdout, marked `"truncated": true` with `"next_page": N` in `meta`, while
  the exit code stays that of the failure and the error itself goes to
  stderr: 8 for a transport error, 7 for a 5xx, and 7 for a later page that
  is not a list (`null`, an object, an empty or non-JSON body) or is empty
  while its own metadata says more pages follow. Keep that partial result and
  re-run with `--all --page N` to fetch the rest; the two concatenate. A run
  whose first request fails writes nothing. `--page N` alongside `--all` is
  the start page and `--max-pages` (1 or more) caps how many pages that run
  fetches.
- The same `"truncated"` / `"next_page"` markers appear whenever `--max-pages`
  cuts a run short, which exits 0 because the cap is deliberate. Any `--all`
  envelope without them covered everything the endpoint had.
- Those markers live in `meta`, so they only exist under the default
  `--output json`. With `ndjson`, `table` or `--jq` there is no `meta`, and a
  run truncated by `--max-pages` also exits 0 — stdout and the exit code then
  look exactly like a complete run, and only the stderr note reveals the cap.
  Use `--output json` whenever completeness has to be verifiable.
- `--output ndjson` streams one compact data item per line and omits `meta`
  entirely; use the default `--output json` when you need pagination info.
- Terminal control characters in API data never reach the terminal raw: JSON,
  ndjson and `--jq` output escape them (`\u001b`, `\u009b`), and `table` and
  `--raw` strip them.
- Other globals: `--timeout` (default 30s) and `--verbose` (log requests and
  retries to stderr).
- 429s are retried automatically honoring `Retry-After` in both RFC forms
  (`--max-retries`, default 3) for waits of up to 60 seconds. A server that
  asks for longer gets no early retry: the run exits 6 and the error quotes
  its `Retry-After`. Transient 5xx responses are retried only for idempotent
  methods — a POST is never re-sent (duplicate-write risk).
- Meta commands (`commands`, `version`, `auth status`, `api ops/describe`)
  emit the same `{"data": ...}` envelope and honor `--jq`/`--fields`/`--output`.
  Plain-text exceptions: `config path`, `agents-md`, `skill install`.

### Discovery

```bash
peopleforce commands                        # full command tree as JSON, one call
peopleforce api ops                         # every API operation (~200)
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

### Agent skill

The repository ships an [Agent Skill](https://agentskills.io)
([`skills/peopleforce/SKILL.md`](skills/peopleforce/SKILL.md)) that teaches a
coding agent the auth flow, the output contract, exit codes and common
recipes. Install it the way your agent prefers.

Claude Code, as a plugin that stays up to date:

```text
/plugin marketplace add infrabay/peopleforce-cli
/plugin install peopleforce@peopleforce-cli
```

Codex, Cursor, Gemini CLI, GitHub Copilot and other agents that read Agent
Skills, with the [`skills`](https://github.com/vercel-labs/skills) installer:

```bash
npx skills add infrabay/peopleforce-cli
```

Or from the installed binary, which writes the copy matching its own version:

```bash
peopleforce skill install                  # .claude/skills/peopleforce/ in this project
peopleforce skill install --agent codex    # .agents/skills/peopleforce/ (Codex and others)
peopleforce skill install --global         # under your home directory instead
peopleforce agents-md                      # prints an AGENTS.md snippet
```

The skill only describes the CLI; the `peopleforce` binary itself still has
to be [installed](#install).

## Writing data

```bash
# typed flags for simple fields
peopleforce leave requests create --set employee_id:=7 --set leave_type_id:=2 \
  --set starts_on=2026-08-01 --set ends_on=2026-08-05

# --set key=value (string), key:=json (typed), dots nest
# custom fields are set flat by internal_name (see `peopleforce employee-fields list`)
peopleforce employees update 123 --set github=octocat --set address.city=Kyiv

# whole body from a file or stdin: exactly one JSON object, nothing after it
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

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and
[SECURITY.md](SECURITY.md) for reporting vulnerabilities.

## License

[MIT](LICENSE) © infrabay.

`internal/spec/peopleforce-openapi.json` is PeopleForce's published API
description, vendored unchanged so builds are reproducible. It belongs to
PeopleForce and is not covered by this repository's license.
