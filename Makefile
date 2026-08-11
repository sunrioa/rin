GO ?= go
PYTHON ?= python3
NODE ?= node
DOTNET ?= dotnet
JAVAC ?= javac
JAVA ?= java
LUA ?= lua
VERSION ?= 0.7.0

.PHONY: fmt test verify contract-check test-go test-sdks test-sdk-python test-sdk-javascript test-sdk-csharp test-sdk-java test-sdk-lua test-terminal-story race vet build

fmt:
	$(GO) fmt ./...

test: test-go test-terminal-story

verify: contract-check vet race test-sdks test-terminal-story

contract-check:
	$(GO) test ./controlplane ./agentapi ./host -run 'Contract|OpenAPI|Fixture'

test-go:
	$(GO) test ./...

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
	$(JAVA) --add-modules jdk.httpserver -cp .cache/java-sdk io.github.sunrioa.rin.RinSdkTest

test-sdk-lua:
	$(LUA) sdk/lua/test_client.lua

test-terminal-story:
	$(GO) test ./examples/adapters/grid ./examples/adapters/story ./examples/terminal-story
	$(GO) run ./examples/terminal-story --line "The light in this photograph feels familiar." --topic festival --task prepare-exhibit --json

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/rin ./cmd/rin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/rin-control ./cmd/rin-control
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/rin-mcp ./cmd/rin-mcp
