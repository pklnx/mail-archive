GO        ?= go
BIN       := bin/mail-archive
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TEST_DATABASE_URL ?= postgres://mailarchive:mailarchive@localhost:5432/mailarchive?sslmode=disable

.PHONY: build test test-unit lint vuln tidy docker docker-multiarch clean

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

tidy:
	$(GO) mod tidy
	$(GO) mod verify

docker:
	docker build --build-arg VERSION=$(VERSION) -t mail-archive:local .

docker-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t mail-archive:multiarch .

clean:
	rm -rf bin
