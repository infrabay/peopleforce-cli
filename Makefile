SPEC_URL ?= https://dash.readme.com/api/v1/api-registry/i2q3a11mfv5ssl0
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS   = -X github.com/3bagels/peopleforce-cli/internal/command.Version=$(VERSION) \
            -X github.com/3bagels/peopleforce-cli/internal/command.Commit=$(COMMIT)

.PHONY: build generate check-generated test vet install update-spec clean

build: ## Build ./peopleforce
	go build -ldflags "$(LDFLAGS)" -o peopleforce ./cmd/peopleforce

generate: ## Recompile the registry from the spec + overrides.yaml
	go run ./internal/gen

# The golden snapshot and registry.gen.go are written by the same run, so the
# in-repo test comparing them cannot notice that both are stale. Only
# regenerating and diffing against git catches a spec or overrides edit that
# never reached the shipped binary.
check-generated: ## Fail if the registry is stale w.r.t. the spec + overrides
	@go run ./internal/gen
	@git diff --quiet -- internal/registry/registry.gen.go testdata/golden/commands.json || { \
		echo "ERROR: generated files are out of date — run 'make generate' and commit the result."; \
		git --no-pager diff --stat -- internal/registry/registry.gen.go testdata/golden/commands.json; \
		exit 1; }
	@echo "generated files are up to date"

test: vet check-generated
	go test ./...

vet:
	go vet ./...

install: ## Install into GOPATH/bin
	go install -ldflags "$(LDFLAGS)" ./cmd/peopleforce

# The ReadMe registry URL hash changes between spec versions — grab the
# current one from the install command shown at https://developer.peopleforce.io
# and pass it: make update-spec SPEC_URL=https://dash.readme.com/api/v1/api-registry/<hash>
update-spec: ## Fetch the latest OpenAPI spec, regenerate, test
	curl -fsS "$(SPEC_URL)" -o internal/spec/peopleforce-openapi.json
	$(MAKE) generate
	$(MAKE) test
	@echo "Review the diff of testdata/golden/commands.json to see what changed."

clean:
	rm -f peopleforce
