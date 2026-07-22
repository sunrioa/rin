GO ?= go
VERSION ?= dev

.PHONY: fmt test race vet build

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/rin ./cmd/rin
