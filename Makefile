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
# The Carbon and IBM Plex packages of the web UI report usage to IBM after
# install. web/pnpm-workspace.yaml blocks their install scripts; this is a
# second guard for every recipe here (web, web-test, web-e2e, docs-screenshots).
export IBM_TELEMETRY_DISABLED := true
# Makes pnpm available for the web, web-e2e and docs recipes: a pnpm already on
# PATH is used as is, corepack is enabled only when there is none. Use your own
# pnpm by putting it on PATH, or skip the step with `make PNPM_SETUP=true ...`.
# scripts/makefile-pnpm-test.sh guards this.
PNPM_SETUP ?= command -v pnpm >/dev/null 2>&1 || corepack enable

.PHONY: web web-test docs docs-screenshots build test test-unit lint vuln secrets secrets-test generate sqlc-check tidy docker docker-multiarch clean

## web: build the web UI (embedded by `build`)
web:
	cd web && $(PNPM_SETUP) && pnpm install --frozen-lockfile && pnpm build

## web-test: typecheck and unit-test the web UI
web-test:
	cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

.PHONY: web-e2e
## web-e2e: end-to-end tests of the web UI against the demo server (needs DEMO_DATABASE_URL)
web-e2e:
	@: "$${DEMO_DATABASE_URL:?set DEMO_DATABASE_URL to a PostgreSQL URL with CREATEDB}"
	cd web && $(PNPM_SETUP) && pnpm install --frozen-lockfile && pnpm e2e

## docs: build the documentation site into docs/.vitepress/dist
docs:
	cd docs && $(PNPM_SETUP) && pnpm install --frozen-lockfile && pnpm build

## docs-screenshots: regenerate docs/public/screenshots (needs DEMO_DATABASE_URL)
docs-screenshots:
	scripts/docs-screenshots.sh

build:
	$(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/mail-archive

## test: all tests, including PostgreSQL integration tests
test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -race -shuffle=on -count=1 ./...

## test-unit: tests that need no database
test-unit:
	$(GO) test -race -shuffle=on ./...

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

.PHONY: review-gate review-gate-test review-gate-tools
# Review gate (scripts/review-gate.sh, docs/development/review-gate.md):
# deterministic checks on what the branch changes. Tool versions are pinned
# here and in tools/review-gate/package-lock.json (squawk); bump them by hand,
# like sqlc and gitleaks. Each Go tool is built with the project's toolchain
# into its own folder under bin/, verified through the Go checksum database.
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := bin/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
ACTIONLINT_VERSION := v1.7.12
ACTIONLINT := bin/actionlint-$(ACTIONLINT_VERSION)/actionlint
SQUAWK := tools/review-gate/node_modules/.bin/squawk
SQUAWK_STAMP := tools/review-gate/node_modules/.installed
# zizmor comes from PyPI (tools/review-gate/requirements.txt: pinned version,
# hash of every wheel) into a venv of its own under bin/.
ZIZMOR := bin/zizmor-venv/bin/zizmor
ZIZMOR_STAMP := bin/zizmor-venv/.installed
PYTHON3 ?= python3
# The branch or commit the gate compares against (CI: the pull request's base).
BASE ?= origin/main
REVIEW_GATE_ENV = GOLANGCI_LINT=$(abspath $(GOLANGCI_LINT)) ACTIONLINT=$(abspath $(ACTIONLINT)) SQUAWK=$(abspath $(SQUAWK)) ZIZMOR=$(abspath $(ZIZMOR))

$(GOLANGCI_LINT):
	GOTOOLCHAIN=$$($(GO) env GOVERSION) GOBIN=$(CURDIR)/$(dir $(GOLANGCI_LINT)) $(GO) install \
		github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(ACTIONLINT):
	GOTOOLCHAIN=$$($(GO) env GOVERSION) GOBIN=$(CURDIR)/$(dir $(ACTIONLINT)) $(GO) install \
		github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# npm ci installs exactly the lockfile (integrity hashes, no install scripts).
$(SQUAWK_STAMP): tools/review-gate/package-lock.json
	npm ci --ignore-scripts --no-audit --no-fund --prefix tools/review-gate
	touch $@

# --require-hashes: every file must match its pinned hash; --only-binary: no
# build from source. The venv is rebuilt whenever requirements.txt changes.
$(ZIZMOR_STAMP): tools/review-gate/requirements.txt
	rm -rf bin/zizmor-venv
	$(PYTHON3) -m venv bin/zizmor-venv
	bin/zizmor-venv/bin/pip install --disable-pip-version-check --quiet --no-deps --only-binary=:all: --require-hashes -r tools/review-gate/requirements.txt
	touch $@

## review-gate-tools: build or install the review gate's tools
review-gate-tools: $(GOLANGCI_LINT) $(ACTIONLINT) $(SQUAWK_STAMP) $(ZIZMOR_STAMP)

## review-gate: check what the branch changes against BASE (one line; non-zero on findings or tool errors)
review-gate: review-gate-tools
	@$(REVIEW_GATE_ENV) BASE="$(BASE)" sh scripts/review-gate.sh

## review-gate-test: self-test of the review gate (temp git repos, no network)
review-gate-test: review-gate-tools
	$(REVIEW_GATE_ENV) sh scripts/review-gate-test.sh

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
