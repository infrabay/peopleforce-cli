SPEC_URL ?= https://dash.readme.com/api/v1/api-registry/i2q3a11mfv5ssl0
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS   = -X github.com/3bagels/peopleforce-cli/internal/command.Version=$(VERSION) \
            -X github.com/3bagels/peopleforce-cli/internal/command.Commit=$(COMMIT)

.PHONY: build generate test vet install update-spec clean

build: ## Build ./peopleforce
	go build -ldflags "$(LDFLAGS)" -o peopleforce ./cmd/peopleforce

generate: ## Recompile the registry from the spec + overrides.yaml
	go run ./internal/gen

test: vet
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
