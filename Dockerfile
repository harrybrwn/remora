# Build Image
FROM golang:1.16.5-buster as builder

RUN apt-get update     && \
    apt-get upgrade -y && \
    apt-get install -y git protobuf-compiler && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

COPY go.mod /app/build/go.mod
COPY go.sum /app/build/go.sum

WORKDIR /app/build
RUN go mod download

COPY . /app/src
WORKDIR /app/src

RUN go generate ./web
RUN CGO_ENABLED=0 go build -o /app/bin/deinopis ./cmd/deinopis
RUN CGO_ENABLED=0 go build -o /app/bin/remora ./cmd/remora

# Main Image
FROM alpine:3.14
COPY --from=builder /app/bin /usr/bin

RUN mkdir -p -m 3777 /var/local/diktyo

WORKDIR /
