---
name: peopleforce
description: Query and manage PeopleForce HR data (employees, leave requests, tasks, teams, org structure, recruitment) via the peopleforce CLI. Use whenever the user asks about employees, vacations/leave, HR tasks, teams, departments, or anything stored in PeopleForce.
---

# PeopleForce CLI

`peopleforce` is a command-line client for the PeopleForce HR API, built for
non-interactive use by agents.

## Setup

If `peopleforce` is not on PATH, it has to be installed first — ask the user
before installing software:

```bash
brew install infrabay/tap/peopleforce                                  # macOS, Linux
go install github.com/infrabay/peopleforce-cli/cmd/peopleforce@latest  # any OS with Go
```

Prebuilt binaries for Linux, macOS and Windows are on
https://github.com/infrabay/peopleforce-cli/releases. This skill may be
newer or older than the installed binary: `peopleforce commands` describes
exactly what the installed version supports.

## Auth

Requires an API token in `PEOPLEFORCE_API_KEY` (or a config-file profile).
Verify before doing work:

```bash
peopleforce auth status   # 0 = authenticated, 3 = missing/rejected key,
                          # 2 = the config file itself could not be read
```

`auth status` prints its envelope even when it exits non-zero: an unreadable
config reports `config_error` alongside `"authenticated": false`, so the
diagnosis is machine-readable exactly when something is broken.

A key passed in argv is visible to `ps`, to `/proc/<pid>/cmdline`, and to CI
logs under `set -x`. Use the environment variable, or pipe the key in with
the `-` sentinel:

```bash
pass show peopleforce | peopleforce auth login --api-key -     # persist it
pass show peopleforce | peopleforce employees list --api-key - # one-off
```

`--api-key -` and `--input -` both consume stdin, so combining them is a
usage error (exit 2), never a race for the same stream.

401 and 403 mean different things and are worth reading carefully: 401 is a
missing or invalid key, while 403 means the key was recognized but the request
was refused — usually because API access is not enabled for that key, or the
caller's IP is outside the PeopleForce allowlist. A 403 on every endpoint with
a well-formed key is almost never the key itself, so do not send the user
hunting for a new token before checking the allowlist.

## Output contract

- stdout: data only. JSON by default, always shaped `{"data": ..., "meta": {...}}`.
  Lists put pagination in `meta` (`page`, `pages`, `count`, `items`).
- stderr: diagnostics; errors are structured `{"error": {"type", "status", "message", "detail"}}`.
- Exit codes: `0` ok · `2` usage · `3` auth · `4` not found · `5` validation ·
  `6` rate-limited · `7` server error · `8` network · `9` the response could
  not be rendered (the request already succeeded — do not blindly retry).

Trim tokens with the built-in jq (no external jq needed) or field projection:

```bash
peopleforce employees list --jq '.data[] | {id, email}'
peopleforce employees list --fields id,full_name,email
peopleforce employees get 123 --jq .data.email --raw   # -r: strings without quotes
```

When both are given, `--jq` wins and `--fields` is ignored. A `--fields`
name that matches nothing in the response is reported on stderr — the data
still renders, so check stderr rather than assuming empty objects mean empty
records. Note `--jq` produces JSON regardless of `--output`.

Other globals: `--timeout` (default 30s), `--verbose` (log requests and
retries to stderr). `--output ndjson` omits `meta`; `--all` replaces
`meta.page` with `meta.fetched`.

## Discovering commands

```bash
peopleforce commands              # entire command tree as JSON, one call
peopleforce api ops               # all ~200 API operations (method, path)
peopleforce api describe GET /employees   # params/body schema of one op
```

Curated commands cover employees, leave, tasks, teams, departments, divisions,
locations, positions, holidays, calendars, termination reference data. Every
other endpoint is reachable through the escape hatch:

```bash
peopleforce api call GET '/recruitment/vacancies?page=1'
peopleforce api call POST /working_patterns --set name="4-day week"
```

## Common recipes

```bash
# Active employees (repeatable ID filter)
peopleforce employees list --status active --ids 12 --ids 14

# Everyone hired this year, all pages, id+email only
peopleforce employees list --hired-on-gte 2026-01-01 --all --jq '.data[] | {id, email}'

# One employee with leave balances
peopleforce employees get 123
peopleforce employees leave-balances 123

# Team membership — read it from the LIST; there is no GET /teams/{id} (404),
# and an employee record has no team field (only department/division/position).
# team_lead sits OUTSIDE team_members, so include it or you lose one person.
peopleforce teams list --jq '.data[] | select(.name == "Platform") |
  [.team_lead.email] + [.team_members[].user.email] | .[]' --raw

# Pending leave requests / create a leave request
peopleforce leave requests pending
peopleforce leave requests create --set employee_id:=7 --set leave_type_id:=2 \
  --set starts_on=2026-08-01 --set ends_on=2026-08-05

# Upload a document
peopleforce employees documents upload 42 --document @contract.pdf \
  --name "Contract" --document-folder-id 3

# Bulk-update many employees in ONE run (NDJSON in, NDJSON report out)
printf '%s\n' \
  '{"id": 8321, "set": {"github": "octocat"}}' \
  '{"id": 8322, "set": {"github": "octocat"}}' \
  | peopleforce employees bulk-update --input -
# report line per record: {"id":8321,"ok":true,"status":200,"data":{...updated record...}}
# "data" carries the updated record — no follow-up GETs needed to verify.
# exit 0 = all ok, 5 = some failed; --dry-run previews the whole batch.
```

## Custom fields: write flat, read nested

Custom fields are WRITTEN as flat top-level keys by `internal_name`, but READ
back under `.data.fields.<internal_name>.value` (there is no `custom_fields`
key). Round-trip:

```bash
peopleforce employee-fields list --jq '.data[] | {internal_name, name, type}'  # find internal_name
peopleforce employees update 8321 --set github=octocat                        # write: flat key
peopleforce employees get 8321 --jq '.data.fields.github.value' --raw          # read: nested
```

Note: `employees list` returns a slim record without `fields`; use
`employees get` (or the bulk-update report's `data`) to read custom fields.

## Writing data

- Request bodies: typed flags for simple fields, `--set key=value` /
  `--set key:=json` for anything, `--input @file.json` or `--input -` (stdin)
  for whole bodies. Dots nest: `--set address.city=Kyiv`.
- Always preview mutations first with `--dry-run` (prints method/URL/body,
  sends nothing).
- Destructive operations (deletes, `employees terminate`) require `--yes`
  in non-interactive mode — there are no interactive prompts without a TTY.

## Pagination and limits

- Lists take `--page N` (page size is fixed server-side). `--all` follows
  every page (capped by `--max-pages`, default 20); empty results are `[]`.
- An `--all` run that fails partway still hands over what it collected: the
  pages already fetched are on stdout with `"truncated": true` and
  `"next_page": N` in `meta`, and the exit code is the failure's own (7, 8).
  Keep that data and re-run with `--all --page N` for the rest — the two
  results concatenate. Only a run that failed on its very first page prints
  nothing. `--output ndjson` drops `meta`, so truncation is invisible there:
  and so do `table` and `--jq`. `--max-pages` limits pages fetched by that
  run, not the page number, and a run cut short by the cap carries the same
  `"truncated"` / `"next_page"` markers while exiting 0. Under the default
  `--output json`, treat the absence of those markers — not the exit code —
  as proof a list is complete; under any other output there is no completeness
  signal on stdout at all, so use json when it matters.
- Team membership comes only from `teams list` (no GET /teams/{id}), and
  `teams create` cannot set members — use `teams members add`.
- 429 rate limits are retried automatically (honoring Retry-After) up to
  `--max-retries` (default 3); exhaustion exits with code 6. Transient 5xx
  are retried only for idempotent methods — POSTs are never re-sent.

## Notes

- Meta commands (`commands`, `version`, `auth status`, `api ops/describe`)
  use the same `{"data": ...}` envelope and honor `--jq`/`--fields`.
  Plain-text exceptions: `config path`, `agents-md`, `skill install`.
- `--set key=value` sends a string; use `key:=7` / `key:=true` for typed
  JSON values (integers, booleans, arrays, objects).
- Multipart uploads have curated commands (`employees documents upload`,
  `recruitment candidates create`, `recruitment candidates documents upload`)
  — `api call` bodies are JSON-only.
