GO        ?= go
BIN       := bin/mail-archive
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# sqlc version for code generation. Dependabot cannot update `go run pkg@version`,
# so bump it here by hand.
SQLC      := GOTOOLCHAIN=$$($(GO) env GOVERSION) $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
TEST_DATABASE_URL ?= postgres://mailarchive:mailarchive@localhost:5432/mailarchive?sslmode=disable

.PHONY: web web-test build test test-unit lint vuln generate sqlc-check tidy docker docker-multiarch clean

## web: build the web UI (embedded by `build`)
web:
	cd web && corepack enable && pnpm install --frozen-lockfile && pnpm build

## web-test: typecheck and unit-test the web UI
web-test:
	cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

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
