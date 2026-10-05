# Contributing

Thanks for your interest in improving `peopleforce-cli`.

## Development

You need the Go version named in `go.mod` (the `go` command downloads it
automatically when a newer toolchain is allowed).

```sh
make build            # ./peopleforce
make test             # go vet, check-generated, go test ./...
make generate         # spec + overrides.yaml → registry.gen.go + golden snapshot
golangci-lint run     # the linter CI runs, configured by .golangci.yml
```

The tests never call the real PeopleForce API: every HTTP exchange is served
by `httptest`. Keep it that way — a test must not need an API key or network
access.

## How the code is organised

- `internal/spec/peopleforce-openapi.json` is the vendored upstream spec;
  `internal/spec/overrides.yaml` curates it (command names, flags, which
  operations are destructive).
- `internal/gen` compiles both into `internal/registry/registry.gen.go` and
  `testdata/golden/commands.json`. Never edit those two files by hand: change
  the spec or the overrides and run `make generate`. CI runs
  `make check-generated` and fails when they are stale.
- `internal/command` builds the cobra tree from the registry and executes
  operations through `internal/httpx`; `internal/envelope` normalises the
  response shapes and `internal/output` renders them.

## Pull requests

- Keep the change focused; add or update tests for behaviour changes.
- The output contract in `README.md` (stdout envelope, stderr errors, exit
  codes, `--all` truncation markers) is what agents depend on. A change to it
  needs a README update in the same pull request.
- CI runs `gofmt`, `go vet`, `go test -race`, `make check-generated`,
  `golangci-lint`, `govulncheck` and a GoReleaser snapshot build for every
  release platform. Please make sure the first five pass locally.

## Reporting issues

Please include `peopleforce version`, the command line, and the relevant
stdout/stderr output — with API keys and employee data removed.

Report security vulnerabilities privately, as described in
[`SECURITY.md`](SECURITY.md), not in a public issue.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
