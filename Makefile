VERSION=$(shell git describe --tags --abbrev=0)-$(shell git rev-parse --short HEAD)
COMMIT=$(shell git rev-parse HEAD)
HASH=$(shell ./scripts/sourcehash.sh -e './cmd/deploy/*' -e '*_test.go')
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
PROTOS=$(shell find . -name '*.proto')
SOURCES=$(shell scripts/sourcehash.sh -l -e '*_test.go') $(PROTOS)

all: $(BINDIR)/remora $(BINDIR)/crawler-api $(TOOLS)

install: $(BINDIR)/remora $(BINDIR)/remoractl
	install ./bin/remora ~/dev/go/bin/remora
	install ./bin/remoractl ~/dev/go/bin/remoractl

.PHONY: $(BINDIR)/remora
$(BINDIR)/remora: gen
	CGO_ENABLED=0 go build $(GOFLAGS) -o $@ ./cmd/remora

$(BINDIR)/docker: $(SOURCES)
	CGO_ENABLED=0 go build $(GOFLAGS) -o $@ ./cmd/docker

$(BINDIR)/deploy: $(SOURCES)
	CGO_ENABLED=0 go build $(GOFLAGS) -o $@ ./cmd/deploy

$(BINDIR)/crawler-api: $(SOURCES)
	CGO_ENABLED=0 go build $(GOFLAGS) -o $@ ./cmd/crawler-api

$(BINDIR)/remoractl: gen
	CGO_ENABLED=0 go build $(GOFLAGS) -o $@ ./cmd/remoractl

.PHONY: gen
gen:
	go generate ./web ./cmd/remora ./internal

.PHONY: docs/protobuf
docs/protobuf:
	mkdir -p $@
	docker container run --rm \
		-v $(shell pwd)/docs/protobuf:/out \
		-v $(shell pwd)/protobuf:/protos pseudomuto/protoc-gen-doc \
		--doc_opt=markdown,docs.md

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

.PHONY: all test clean image install
