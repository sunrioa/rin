GO ?= go
PYTHON ?= python3
NODE ?= node
DOTNET ?= dotnet
JAVAC ?= javac
JAVA ?= java
LUA ?= lua
VERSION ?= dev

.PHONY: fmt test test-go test-adapters test-sdks test-sdk-python test-sdk-javascript test-sdk-csharp test-sdk-java test-sdk-lua race vet build

fmt:
	$(GO) fmt ./...

test: test-go test-adapters

test-go:
	$(GO) test ./...

test-adapters:
	$(PYTHON) -m unittest discover -s adapters/renpy -p 'test_*.py'

test-sdks: test-sdk-python test-sdk-javascript test-sdk-csharp test-sdk-java test-sdk-lua

test-sdk-python:
	$(PYTHON) -m unittest discover -s sdk/python/tests -p 'test_*.py'

test-sdk-javascript:
	cd sdk/javascript && $(NODE) --test

test-sdk-csharp:
	$(DOTNET) run --project sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj --nologo

test-sdk-java:
	mkdir -p .cache/java-sdk
	find sdk/java/src/main/java sdk/java/test -name '*.java' > .cache/java-sdk/sources.txt
	$(JAVAC) --add-modules jdk.httpserver -d .cache/java-sdk @.cache/java-sdk/sources.txt
	$(JAVA) --add-modules jdk.httpserver -cp .cache/java-sdk io.github.sunrioa.rin.RinClientTest

test-sdk-lua:
	$(LUA) sdk/lua/test_client.lua

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/rin ./cmd/rin
