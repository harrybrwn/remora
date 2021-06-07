GOFLAGS=
MAIN=./cmd/remora

build:
	go build $(GOFLAGS) $(MAIN)

install:
	go install $(GOFLAGS) $(MAIN)

clean:
	$(RM) ./diktyo
