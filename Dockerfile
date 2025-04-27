# syntax=docker/dockerfile

ARG ALPINE_VERSION=3.24
ARG GO_VERSION=1.24.1
ARG VERSION=dev

# Build Image
FROM golang:${GO_VERSION}-alpine AS builder
RUN --mount=type=cache,target=/var/cache/apk \
    apk update && apk --upgrade \
        add         \
        git         \
        protoc      \
        chromium && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

COPY go.mod /app/build/go.mod
COPY go.sum /app/build/go.sum

WORKDIR /app/build
RUN --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod  \
    go mod download

COPY . /app/src
WORKDIR /app/src

FROM builder AS remora-builder
RUN --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod  \
    go generate ./web && \
    CGO_ENABLED=0 go build     \
        -o /app/bin/remora     \
        -trimpath              \
        -ldflags "-w -s \
            -X 'github.com/harrybrwn/remora/cmd.date=$(date -R)' \
            -X 'github.com/harrybrwn/remora/cmd.version=${VERSION}' \
            -X 'github.com/harrybrwn/remora/cmd.sourcehash=$(./scripts/sourcehash.sh -e './cmd/deploy/*')' \
            -X 'github.com/harrybrwn/remora/cmd.commit=$(git rev-parse HEAD)'" \
        ./cmd/remora

FROM builder AS remoractl-builder
RUN --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod  \
    go generate ./... && \
    CGO_ENABLED=0 go build     \
        -o /app/bin/remoractl  \
        -trimpath              \
        -ldflags "-w -s \
            -X 'github.com/harrybrwn/remora/cmd.date=$(date -R)' \
            -X 'github.com/harrybrwn/remora/cmd.version=${VERSION}' \
            -X 'github.com/harrybrwn/remora/cmd.sourcehash=$(./scripts/sourcehash.sh -e './cmd/deploy/*')' \
            -X 'github.com/harrybrwn/remora/cmd.commit=$(git rev-parse HEAD)'" \
        ./cmd/remoractl

FROM builder AS remora-api-builder
RUN --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod  \
    CGO_ENABLED=0 go build     \
        -o /app/bin/remora-api \
        -trimpath              \
        -ldflags "-w -s \
            -X 'github.com/harrybrwn/remora/cmd.date=$(date -R)' \
            -X 'github.com/harrybrwn/remora/cmd.version=${VERSION}' \
            -X 'github.com/harrybrwn/remora/cmd.sourcehash=$(./scripts/sourcehash.sh -e './cmd/deploy/*')' \
            -X 'github.com/harrybrwn/remora/cmd.commit=$(git rev-parse HEAD)'" \
        ./cmd/api

FROM builder AS crawler-api-builder
RUN --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod  \
    CGO_ENABLED=0 go build      \
        -o /app/bin/crawler-api \
        -trimpath               \
        -ldflags "-w -s \
            -X 'github.com/harrybrwn/remora/cmd.date=$(date -R)' \
            -X 'github.com/harrybrwn/remora/cmd.version=${VERSION}' \
            -X 'github.com/harrybrwn/remora/cmd.sourcehash=$(./scripts/sourcehash.sh -e './cmd/deploy/*')' \
            -X 'github.com/harrybrwn/remora/cmd.commit=$(git rev-parse HEAD)'" \
        ./cmd/crawler-api

# API Image
FROM alpine:${ALPINE_VERSION} AS api
COPY --from=remora-api-builder /app/bin/remora-api /usr/bin/
ENTRYPOINT ["remora-api"]

# Crawler API Image
FROM alpine:${ALPINE_VERSION} AS crawler-api
RUN --mount=type=cache,target=/var/cache/apk \
    apk update && \
    apk add --upgrade chromium
COPY --from=crawler-api-builder /app/bin/crawler-api /usr/bin/
ENTRYPOINT ["crawler-api"]

# Remora API Controller
FROM alpine:${ALPINE_VERSION} AS remoractl
RUN --mount=type=cache,target=/var/cache/apk \
    apk update
COPY --from=remoractl-builder /app/bin/remoractl /usr/bin/
ENTRYPOINT ["remoractl"]

# Main Image
FROM alpine:${ALPINE_VERSION} AS remora
COPY --from=remora-builder /app/bin/remora /usr/bin/remora
RUN mkdir -p -m 3777 /var/local/remora
WORKDIR /
ENTRYPOINT ["remora"]
