GO ?= go
PYTHON ?= python3
VERSION ?= dev

.PHONY: fmt test test-go test-adapters race vet build

fmt:
	$(GO) fmt ./...

test: test-go test-adapters

test-go:
	$(GO) test ./...

test-adapters:
	$(PYTHON) -m unittest discover -s adapters/renpy -p 'test_*.py'

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/rin ./cmd/rin
