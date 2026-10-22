GO        ?= go
BIN       := bin/mail-archive
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# sqlc version for code generation. Dependabot cannot update `go run pkg@version`,
# so bump it here by hand.
SQLC      := GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
# gitleaks version for secret scanning. Dependabot cannot update
# `go install pkg@version` either, so bump it here by hand and rescan the full
# history (`make secrets`) after a bump.
GITLEAKS_VERSION := v8.30.1
GITLEAKS  := bin/gitleaks-$(GITLEAKS_VERSION)/gitleaks
# Git history to scan; `make secrets` scans all refs unless SECRETS_LOG_OPTS
# (git log options, e.g. a commit range) is set.
SECRETS_SOURCE ?= .
TEST_DATABASE_URL ?= postgres://mailarchive:mailarchive@localhost:5432/mailarchive?sslmode=disable

.PHONY: web web-test docs docs-screenshots build test test-unit lint vuln secrets secrets-test generate sqlc-check tidy docker docker-multiarch clean

## web: build the web UI (embedded by `build`)
web:
	cd web && corepack enable && pnpm install --frozen-lockfile && pnpm build

## web-test: typecheck and unit-test the web UI
web-test:
	cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

.PHONY: web-e2e
## web-e2e: end-to-end tests of the web UI against the demo server (needs DEMO_DATABASE_URL)
web-e2e:
	@: "$${DEMO_DATABASE_URL:?set DEMO_DATABASE_URL to a PostgreSQL URL with CREATEDB}"
	cd web && corepack enable && pnpm install --frozen-lockfile && pnpm e2e

## docs: build the documentation site into docs/.vitepress/dist
docs:
	cd docs && corepack enable && pnpm install --frozen-lockfile && pnpm build

## docs-screenshots: regenerate docs/public/screenshots (needs DEMO_DATABASE_URL)
docs-screenshots:
	scripts/docs-screenshots.sh

build:
	$(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/mail-archive

## test: all tests, including PostgreSQL integration tests
test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -race -count=1 ./...

## test-unit: tests that need no database
test-unit:
	$(GO) test -race ./...

lint:
	golangci-lint run

# govulncheck must be built with the project's toolchain (see go.mod) to load
# packages that require it.
vuln:
	GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

# gitleaks is built with the project's toolchain and verified through the Go
# checksum database, like govulncheck.
$(GITLEAKS):
	GOTOOLCHAIN=$$($(GO) env GOVERSION) GOBIN=$(CURDIR)/$(dir $(GITLEAKS)) $(GO) install \
		-ldflags=-X=github.com/zricethezav/gitleaks/v8/version.Version=$(GITLEAKS_VERSION) \
		github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)

## secrets: scan the git history (all refs by default) for committed credentials
secrets: $(GITLEAKS)
	$(GITLEAKS) git --redact --verbose --no-banner --exit-code 1 --config .gitleaks.toml \
		--log-opts="$$SECRETS_LOG_OPTS" $(SECRETS_SOURCE)

## secrets-test: self-test of the secret scan (temporary git repos, no network)
secrets-test: $(GITLEAKS)
	GITLEAKS=$(CURDIR)/$(GITLEAKS) sh scripts/secret-scan-test.sh

## generate: regenerate database code from internal/store/queries
generate:
	$(SQLC) generate

## sqlc-check: fail if generated database code is stale or queries are invalid
sqlc-check: generate
	$(SQLC) vet
	git diff --exit-code -- internal/store/db
	@test -z "$$(git ls-files --others --exclude-standard -- internal/store/db)" || { echo "untracked generated files in internal/store/db"; exit 1; }

tidy:
	$(GO) mod tidy
	$(GO) mod verify

docker:
	docker build --build-arg VERSION=$(VERSION) -t mail-archive:local .

docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t mail-archive:multiarch .

clean:
	rm -rf bin
