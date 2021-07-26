# SOURCES=$(shell find . -name '*.go' -type f)
SOURCES=
GOFLAGS=
MAIN=./cmd/remora
GENERATED=web/pb/page.pb.go web/pb/page_grpc.pb.go
TOOLS=bin/deploy bin/docker

all: bin/remora $(TOOLS)

build: remora $(TOOLS)

remora: gen
	go build $(GOFLAGS) $(MAIN)

bin/remora: gen $(SOURCES)
	go build -o $@ $(GOFLAGS) $(MAIN)

bin/docker: ./cmd/docker/main.go
	go build -o $@ ./cmd/docker

bin/deploy: ./cmd/deploy/main.go
	go build -o $@ ./cmd/deploy

install: bin/remora
	install ./bin/remora ~/dev/go/bin/remora

gen: $(GENERATED)
	@if [ ! -d web/pb ]; then mkdir web/pb; fi
	go generate ./web

docker-buildx:
	bash scripts/buildx.sh

image:
	docker image build \
		-t remora:0.1  \
		-f ./Dockerfile .

docker-pi:
	docker \
		-H 192.168.0.25 \
		image build     \
		-t remora:0.1   \
		-f ./Dockerfile .

up: bin/docker bin/deploy
	./scripts/config.sh
	bin/deploy up

down: bin/deploy
	bin/deploy down

upload: bin/docker
	./upload.sh

clean:
	$(RM) ./remora
	$(RM) -r ./bin

.PHONY: install build clean up down
