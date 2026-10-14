# hub: build bin/hub (darwin/arm64) for the fleet. No CGO: the Turso engine ships inside the binary and is
# loaded with purego.
VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
OUT     ?= ../bin/hub

.PHONY: build test vet check clean
build:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(OUT) .

vet:
	go vet ./...

test:
	go test ./...

check: vet test

clean:
	rm -f $(OUT)
