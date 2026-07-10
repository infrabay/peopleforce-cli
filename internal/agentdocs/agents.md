# PeopleForce CLI

This environment has `peopleforce` — a CLI for the PeopleForce HR API
(employees, leave, tasks, teams, org structure, recruitment).

- Auth: `PEOPLEFORCE_API_KEY` env var; check with `peopleforce auth status`.
- Output: JSON on stdout, always `{"data": ..., "meta": {...}}`; errors as
  JSON on stderr. Exit codes: 0 ok, 2 usage, 3 auth, 4 not found,
  5 validation, 6 rate-limit, 7 server, 8 network.
- Discover: `peopleforce commands` (full tree, one call),
  `peopleforce api ops`, `peopleforce api describe GET /employees`.
- Any endpoint without a curated command: `peopleforce api call GET '/path?query'`.
- Filter output in-process: `--jq '.data[] | {id, email}'` or `--fields id,email`.
- Lists: `--page N` or `--all` (auto-paginate, `--max-pages` cap).
- Mutations: `--set key=value`, `--set key:=json`, `--input @file.json`;
  preview with `--dry-run`; destructive ops need `--yes`.

Example:

```bash
peopleforce employees list --status active --jq '.data[] | {id, email}'
```
