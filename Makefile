GOFLAGS=

build:
	go build $(GOFLAGS) ./cmd/diktyo

install:
	go install $(GOFLAGS) ./cmd/diktyo

clean:
	$(RM) ./diktyo
