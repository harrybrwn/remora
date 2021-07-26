# Build Image
FROM golang:1.16.5-buster as builder

RUN apt-get update     && \
    apt-get upgrade -y
RUN apt-get install -y git protobuf-compiler
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

COPY go.mod /app/build/go.mod
COPY go.sum /app/build/go.sum

WORKDIR /app/build
RUN go mod download

# This file imports all dependancies so building it
# in an early stage caches the imported libraries.
#COPY ./cmd/buildcache ./cmd/buildcache
#RUN go build -o /tmp/buildcache ./cmd/buildcache

COPY . /app/src
WORKDIR /app/src

RUN go generate ./web
RUN CGO_ENABLED=0 go build -o /app/bin/remora ./cmd/remora

# Main Image
FROM alpine:3.14

COPY --from=builder /app/bin/remora /usr/bin/remora

RUN mkdir -p -m 3777 /var/local/diktyo
WORKDIR /
ENTRYPOINT ["remora"]
