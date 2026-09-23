# Everything runs in Docker. There is no host Go requirement, and adding one
# is not a prerequisite for contributing — the same instinct as the platform
# repo's "no host Node" rule.
#
# Override GO_IMAGE to test against another toolchain.

GO_IMAGE ?= golang:1.25-alpine
PKG      := github.com/The-LibreTimes/libretimes-cli
VERSION  ?= dev

# Module and build caches persist in named volumes, so a second `make test`
# does not re-download the world.
GO = docker run --rm \
	-v "$(CURDIR)":/src \
	-v lt-gomod:/go/pkg/mod \
	-v lt-gobuild:/root/.cache/go-build \
	-w /src \
	-e CGO_ENABLED=0 \
	$(GO_IMAGE)

LDFLAGS = -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Sync go.mod/go.sum
	@$(GO) go mod tidy

.PHONY: build
build: ## Build ./lt for this machine
	@$(GO) go build -trimpath -ldflags "$(LDFLAGS)" -o lt ./cmd/lt
	@echo "built ./lt"

.PHONY: test
test: ## Run the unit tests
	@$(GO) go test ./...

.PHONY: vet
vet: ## Run go vet
	@$(GO) go vet ./...

.PHONY: fmt
fmt: ## Format the tree
	@$(GO) gofmt -w -s .

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted
	@out=$$($(GO) gofmt -l -s .); \
	if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

.PHONY: check
check: fmt-check vet test ## Everything CI runs

# Proves the release matrix locally: every target from this one machine, which
# is the property that makes the release workflow a single job. darwin/arm64 in
# particular carries the ad-hoc signature Apple Silicon requires, emitted by
# Go's own linker — no codesign involved.
.PHONY: cross
cross: ## Cross-compile every release target
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  echo "  $$os/$$arch"; \
	  $(GO) env GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
	    -o dist/lt_$${os}_$${arch}$$ext ./cmd/lt || exit 1; \
	done
	@ls -la dist/

# The paths lt actually calls. The rest of the API is free to move without
# breaking this client, so watching all 177 of them would make the drift check
# noise rather than signal.
WATCHED_PATHS = \
	/libretimes/v1/collections \
	/libretimes/v1/collections/me/by-import-key \
	'/libretimes/v1/collections/{collection_id}' \
	'/libretimes/v1/collections/{collection_id}/items' \
	'/libretimes/v1/collections/{collection_id}/items/order' \
	/libretimes/v1/profiles/me \
	'/libretimes/v1/profiles/{username}' \
	/libretimes/v1/publications \
	/libretimes/v1/publications/me/by-import-key \
	'/libretimes/v1/publications/{pub_id}' \
	'/libretimes/v1/publications/{pub_id}/authors' \
	'/libretimes/v1/works/{work_id}/contents' \
	'/libretimes/v1/works/{work_id}/expressions/{expression_id}/divisions' \
	'/libretimes/v1/works/{work_id}/expressions/{expression_id}/divisions/{division_id}' \
	'/libretimes/v1/works/{work_id}/expressions/{expression_id}/divisions/{division_id}/move'

.PHONY: snapshot
snapshot: ## Refresh testdata/openapi-snapshot.json from the live API
	@curl -fsSL --max-time 60 https://api.libretimes.io/libretimes/openapi.json -o /tmp/lt-live.json
	@python3 scripts/contract.py snapshot /tmp/lt-live.json \
	  testdata/openapi-snapshot.json $(WATCHED_PATHS)

.PHONY: contract
contract: ## Check the snapshot against the live API (what contract.yml runs)
	@curl -fsSL --max-time 60 https://api.libretimes.io/libretimes/openapi.json -o /tmp/lt-live.json
	@python3 scripts/contract.py check /tmp/lt-live.json testdata/openapi-snapshot.json

.PHONY: clean
clean: ## Remove build output
	@rm -rf dist lt lt.exe
