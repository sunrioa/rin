package compat_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

type sdkRouteManifest struct {
	SchemaVersion   int        `json:"schema_version"`
	ProtocolVersion string     `json:"protocol_version"`
	Operations      []sdkRoute `json:"operations"`
}

type sdkRoute struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
}

func TestSDKsCoverTheProtocolRouteManifest(t *testing.T) {
	manifest := loadSDKRouteManifest(t)
	if manifest.SchemaVersion != 1 || manifest.ProtocolVersion != "rin.protocol/v1" {
		t.Fatalf("unexpected SDK manifest header: %+v", manifest)
	}
	if len(manifest.Operations) != 20 {
		t.Fatalf("route manifest has %d operations, want 20", len(manifest.Operations))
	}
	seen := make(map[string]bool, len(manifest.Operations))
	for _, operation := range manifest.Operations {
		key := operation.Method + " " + operation.Path
		if seen[key] || operation.Name == "" {
			t.Fatalf("duplicate or unnamed operation %q", key)
		}
		seen[key] = true
		if operation.Status != 200 && operation.Status != 202 {
			t.Fatalf("operation %s has unexpected status %d", operation.Name, operation.Status)
		}
	}

	sdks := []struct {
		name       string
		path       string
		methodName func(string) string
	}{
		{name: "python", path: "../sdk/python/src/rin_sdk/client.py", methodName: func(value string) string { return "def " + value + "(" }},
		{name: "javascript", path: "../sdk/javascript/src/index.js", methodName: func(value string) string { return lowerCamel(value) + "(" }},
		{name: "csharp", path: "../sdk/csharp/Rin.Client/RinClient.cs", methodName: func(value string) string { return upperCamel(value) + "Async(" }},
		{name: "java", path: "../sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java", methodName: func(value string) string { return lowerCamel(value) + "(" }},
		{name: "lua", path: "../sdk/lua/rin.lua", methodName: func(value string) string { return "Client:" + value + "(" }},
	}
	for _, sdk := range sdks {
		t.Run(sdk.name, func(t *testing.T) {
			payload, err := os.ReadFile(sdk.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, operation := range manifest.Operations {
				if !strings.Contains(text, sdk.methodName(operation.Name)) {
					t.Errorf("%s is missing operation %s", sdk.path, operation.Name)
				}
				pathPrefix := strings.TrimSuffix(operation.Path, "{job_id}")
				if !strings.Contains(text, pathPrefix) {
					t.Errorf("%s is missing route %s", sdk.path, operation.Path)
				}
			}
		})
	}
}

func TestSDKRouteManifestMatchesHTTPServer(t *testing.T) {
	manifest := loadSDKRouteManifest(t)
	payload, err := os.ReadFile("../httpapi/server.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`).FindAllStringSubmatch(string(payload), -1)
	registered := make(map[string]bool, len(matches))
	for _, match := range matches {
		registered[match[1]+" "+match[2]] = true
	}
	if len(registered) != len(manifest.Operations) {
		t.Fatalf("HTTP server has %d routes, SDK manifest has %d", len(registered), len(manifest.Operations))
	}
	for _, operation := range manifest.Operations {
		key := operation.Method + " " + operation.Path
		if !registered[key] {
			t.Errorf("SDK route manifest contains unregistered route %s", key)
		}
	}
}

func TestSDKTransportSecurityGuardsRemainVisible(t *testing.T) {
	tests := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path:      "../sdk/python/src/rin_sdk/client.py",
			required:  []string{"_NoRedirect", "max_response_bytes", "remote Rin endpoints must use HTTPS", "Authorization"},
			forbidden: []string{"import requests", "verify=False", "sk-"},
		},
		{
			path:      "../sdk/javascript/src/index.js",
			required:  []string{"redirect: \"error\"", "AbortController", "maxResponseBytes", "remote Rin endpoints must use HTTPS"},
			forbidden: []string{"rejectUnauthorized: false", "sk-"},
		},
		{
			path:      "../sdk/csharp/Rin.Client/RinClient.cs",
			required:  []string{"AllowAutoRedirect = false", "ResponseHeadersRead", "maxResponseBytes", "Remote Rin endpoints must use HTTPS"},
			forbidden: []string{"DangerousAcceptAnyServerCertificateValidator", ".Result", "sk-"},
		},
		{
			path:      "../sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java",
			required:  []string{"HttpClient.Redirect.NEVER", "BoundedBodySubscriber", "maxResponseBytes", "Remote Rin endpoints must use HTTPS"},
			forbidden: []string{"HostnameVerifier", "get().join()", "sk-"},
		},
		{
			path:      "../sdk/lua/rin.lua",
			required:  []string{"follow_redirects = false", "max_response_bytes", "Remote Rin endpoints must use HTTPS", "Authorization"},
			forbidden: []string{"os.execute", "io.popen", "sk-"},
		},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, required := range test.required {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing %q", test.path, required)
			}
		}
		for _, forbidden := range test.forbidden {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains forbidden pattern %q", test.path, forbidden)
			}
		}
	}
}

func TestExampleModsPreserveGameAuthority(t *testing.T) {
	tests := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path:      "../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java",
			required:  []string{"ALLOWED_ACTIONS", "activePlayers", "waitForProposal", "server.execute", "rin.commit", "candidate_actions"},
			forbidden: []string{"Runtime.getRuntime().exec", "ProcessBuilder", ".join()"},
		},
		{
			path:      "../examples/mods/bepinex-rin-npc/Plugin.cs",
			required:  []string{"AllowedActions", "WaitForProposalAsync", "mainThread.Enqueue", "CommitAsync", "NpcActionReady"},
			forbidden: []string{"Config.Bind(\"Connection\", \"Token\"", ".Result", ".Wait()"},
		},
		{
			path:      "../examples/mods/luanti-rin-npc/init.lua",
			required:  []string{"core.request_http_api", "local_origin", "allowed_actions", "wait_for_proposal", "client:commit"},
			forbidden: []string{"secure.trusted_mods", "request.headers.Authorization =", "os.execute"},
		},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, required := range test.required {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing %q", test.path, required)
			}
		}
		for _, forbidden := range test.forbidden {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains forbidden pattern %q", test.path, forbidden)
			}
		}
	}

	sdk, err := os.ReadFile("../sdk/lua/rin.lua")
	if err != nil {
		t.Fatal(err)
	}
	vendored, err := os.ReadFile("../examples/mods/luanti-rin-npc/rin.lua")
	if err != nil {
		t.Fatal(err)
	}
	if string(sdk) != string(vendored) {
		t.Fatal("Luanti vendored rin.lua differs from sdk/lua/rin.lua")
	}
}

func lowerCamel(value string) string {
	result := upperCamel(value)
	if result == "" {
		return result
	}
	return strings.ToLower(result[:1]) + result[1:]
}

func upperCamel(value string) string {
	var result []rune
	upper := true
	for _, character := range value {
		if character == '_' || character == '-' {
			upper = true
			continue
		}
		if upper {
			result = append(result, unicode.ToUpper(character))
			upper = false
		} else {
			result = append(result, character)
		}
	}
	return string(result)
}

func loadSDKRouteManifest(t *testing.T) sdkRouteManifest {
	t.Helper()
	payload, err := os.ReadFile("../sdk/conformance/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest sdkRouteManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
