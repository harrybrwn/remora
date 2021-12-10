# Build Image
FROM golang:1.16.10-alpine as builder
RUN apk update && apk --upgrade add git protoc && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.27.1 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1.0

COPY go.mod /app/build/go.mod
COPY go.sum /app/build/go.sum

WORKDIR /app/build
RUN go mod download

COPY . /app/src
WORKDIR /app/src

RUN go generate ./web && \
    CGO_ENABLED=0 go build     \
        -o /app/bin/remora     \
        -trimpath              \
        -ldflags "-w -s \
            -X 'github.com/harrybrwn/remora/cmd.date=$(date -R)' \
            -X 'github.com/harrybrwn/remora/cmd.version=docker-build' \
            -X 'github.com/harrybrwn/remora/cmd.sourcehash=$(./scripts/sourcehash.sh -e './cmd/deploy/*')' \
            -X 'github.com/harrybrwn/remora/cmd.commit=$(git rev-parse HEAD)'" \
        ./cmd/remora && \
    CGO_ENABLED=0 go build     \
        -o /app/bin/remora-api \
        -trimpath              \
        -ldflags "-w -s"       \
        ./cmd/api

# API Image
FROM alpine:3.14 as api
COPY --from=builder /app/bin/remora-api /usr/bin/
ENTRYPOINT ["remora-api"]

# Main Image
FROM alpine:3.14 as remora
COPY --from=builder /app/bin/remora /usr/bin/remora
RUN mkdir -p -m 3777 /var/local/remora
WORKDIR /
ENTRYPOINT ["remora"]
