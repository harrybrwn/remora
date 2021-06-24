# SOURCES=$(shell find . -name '*.go' -type f)
SOURCES=
GOFLAGS=
MAIN=./cmd/remora

all: bin/remora bin/deinopis bin/deploy bin/docker

build: remora deinopis bin/deploy bin/docker

remora: gen
	go build $(GOFLAGS) $(MAIN)

deinopis: gen
	go build $(GOFLAGS) ./cmd/deinopis

bin/remora: gen $(SOURCES)
	go build -o $@ $(GOFLAGS) $(MAIN)
bin/deinopis: gen $(SOURCES)
	go build -o $@ $(GOFLAGS) ./cmd/deinopis
bin/docker: ./cmd/docker/main.go
	go build -o $@ ./cmd/docker
bin/deploy: ./cmd/deploy/main.go
	go build -o $@ ./cmd/deploy

install: bin/remora bin/deinopis
	install ./bin/remora ~/dev/go/bin/remora
	install ./bin/deinopis ~/dev/go/bin/deinopis

gen:
	@if [ ! -d web/pb ]; then mkdir web/pb; fi
	@if [ ! -d frontier/pb ]; then mkdir frontier/pb; fi
	go generate ./web

docker-build: bin/deploy
	sudo bin/deploy build

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

build-remote: bin/docker
	sudo ./bin/docker -H rpi1.local -H rpi2.local -H rpi3.local -- image build -t remora:0.1 -f ./Dockerfile .

up: bin/docker bin/deploy
	./scripts/config.sh
	bin/deploy up

down: bin/deploy
	bin/deploy down

upload: bin/docker
	./upload.sh

clean:
	$(RM) ./remora ./deinopis
	$(RM) -r ./bin

.PHONY: install build gen clean up down
