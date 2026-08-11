GO ?= go
PYTHON ?= python3
NODE ?= node
NPM ?= npm
DOTNET ?= dotnet
JAVAC ?= javac
JAVA ?= java
LUA ?= lua
VERSION ?= 0.7.0

.PHONY: fmt test verify contract-check test-go test-adapters test-unreal test-luanti test-sdks test-sdk-sidecar test-sdk-python test-sdk-javascript test-sdk-csharp test-sdk-java test-sdk-lua test-terminal-story race vet build

fmt:
	$(GO) fmt ./...

test: test-go test-adapters test-unreal

verify: contract-check vet race test-adapters test-unreal test-luanti test-sdks test-terminal-story

contract-check:
	$(GO) test ./controlplane ./agentapi -run 'Contract|OpenAPI'

test-go:
	$(GO) test ./...

test-adapters:
	$(PYTHON) -m unittest discover -s adapters/renpy -p 'test_*.py'

test-unreal:
	$(PYTHON) -m unittest tools.test_verify_unreal
	$(PYTHON) tools/verify_unreal.py

test-luanti:
	$(PYTHON) -m unittest tools.test_verify_luanti

test-sdks: test-sdk-python test-sdk-javascript test-sdk-csharp test-sdk-java test-sdk-lua

test-sdk-sidecar:
	mkdir -p .cache/sdk-sidecar
	CGO_ENABLED=0 $(GO) build -o .cache/sdk-sidecar/rin ./cmd/rin
	$(PYTHON) -m unittest tools.test_sdk_sidecar_corpus
	$(PYTHON) tools/run_sdk_sidecar_corpus.py \
		--rin .cache/sdk-sidecar/rin \
		--python $(PYTHON) \
		--node $(NODE) \
		--dotnet $(DOTNET) \
		--javac $(JAVAC) \
		--java $(JAVA) \
		--lua $(LUA)

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
	$(GO) test ./examples/adapters/story ./examples/terminal-story
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
