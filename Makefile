VERSION=$(shell git describe --tags --abbrev=0)-$(shell git rev-parse --short HEAD)
COMMIT=$(shell git rev-parse HEAD)
HASH=$(shell ./scripts/sourcehash.sh -e './cmd/deploy/*')
DATE=$(shell date -R)
GOFLAGS=-trimpath \
		-ldflags "-w -s \
			-X 'github.com/harrybrwn/remora/cmd.version=$(VERSION)' \
			-X 'github.com/harrybrwn/remora/cmd.date=$(DATE)'       \
			-X 'github.com/harrybrwn/remora/cmd.commit=$(COMMIT)'   \
			-X 'github.com/harrybrwn/remora/cmd.sourcehash=$(HASH)'"
GENERATED=web/pb/page.pb.go web/pb/page_grpc.pb.go
TOOLS=bin/deploy bin/docker
BINDIR=./bin

all: $(BINDIR)/remora $(TOOLS)

install: $(BINDIR)/remora
	install ./bin/remora ~/dev/go/bin/remora

.PHONY: $(BINDIR)/remora
$(BINDIR)/remora: gen
	CGO_ENABLED=0 go build -o $@ $(GOFLAGS) ./cmd/remora

$(BINDIR)/docker: ./cmd/docker/main.go
	CGO_ENABLED=0 go build -o $@ ./cmd/docker

$(BINDIR)/deploy: ./cmd/deploy/main.go $(shell find ./cmd/deploy -name '*.go')
	CGO_ENABLED=0 go build -o $@ ./cmd/deploy

gen:
	go generate ./web ./cmd/remora ./internal

docs/protobuf:
	mkdir -p $@
	docker container run --rm \
		-v $(shell pwd)/docs/protobuf:/out \
		-v $(shell pwd)/protobuf:/protos pseudomuto/protoc-gen-doc

web/pb/page.pb.go web/pb/page_grpc.pb.go: protobuf/page.proto
	go generate ./web

test:
	go test -cover -v ./web ./storage ./storage/queue ./internal/... ./cmd/api

image:
	docker image build \
		-t remora:latest  \
		-f ./Dockerfile .

clean:
	$(RM) ./remora
	$(RM) -r ./bin ./internal/mock docs/protobuf

.PHONY: all gen test clean image install
