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
# in-repo test comparing them cannot notice that both are stale. Regenerating
# and checking whether the output actually changed is what catches a spec or
# overrides edit that never reached the shipped binary.
#
# Compares against the files on disk rather than against git, so it works with
# uncommitted work in progress: it answers "do these artifacts match their
# inputs", which is the property that matters, not "are they committed".
check-generated: ## Fail if the registry is stale w.r.t. the spec + overrides
	@tmp=$$(mktemp -d); \
	cp internal/registry/registry.gen.go $$tmp/registry.gen.go; \
	cp testdata/golden/commands.json $$tmp/commands.json; \
	go run ./internal/gen; \
	if cmp -s $$tmp/registry.gen.go internal/registry/registry.gen.go && \
	   cmp -s $$tmp/commands.json testdata/golden/commands.json; then \
		rm -rf $$tmp; echo "generated files are up to date"; \
	else \
		rm -rf $$tmp; \
		echo "ERROR: generated files were stale — they have just been regenerated."; \
		echo "Review the diff and commit it, then re-run."; \
		exit 1; \
	fi

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
