# syntax=docker/dockerfile:1

# Build stages run on the native build platform; the Go stage cross-compiles,
# so arm64 images (Apple Silicon) build quickly without emulation.

# Web UI (React). Its output is embedded into the Go binary.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
COPY --from=web /src/internal/web/ui/dist ./internal/web/ui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/mail-archive ./cmd/mail-archive \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mail-archive /usr/local/bin/mail-archive
COPY --from=build --chown=65532:65532 /out/data /data
ENV MAIL_ARCHIVE_DATA_DIR=/data
VOLUME ["/data"]
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mail-archive"]
CMD ["--help"]
