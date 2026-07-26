package compat_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/protocol"
)

type sdkRouteManifest struct {
	SchemaVersion   int        `json:"schema_version"`
	ReleaseVersion  string     `json:"release_version"`
	ReleaseStatus   string     `json:"release_status"`
	ProtocolVersion string     `json:"protocol_version"`
	Operations      []sdkRoute `json:"operations"`
}

type sdkRoute struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
}

func TestGeneratedSDKRouteManifestMatchesRuntimeRouteTable(t *testing.T) {
	manifest := loadSDKRouteManifest(t)
	if manifest.SchemaVersion != 1 ||
		manifest.ReleaseVersion != protocol.ContractReleaseVersion ||
		manifest.ReleaseStatus != protocol.ContractReleaseStatus ||
		manifest.ProtocolVersion != protocol.Version {
		t.Fatalf("unexpected SDK manifest header: %+v", manifest)
	}
	runtimeRoutes := httpapi.ContractRoutes()
	if len(manifest.Operations) != len(runtimeRoutes) {
		t.Fatalf(
			"SDK manifest has %d operations, runtime has %d",
			len(manifest.Operations),
			len(runtimeRoutes),
		)
	}
	runtimeByKey := make(map[string]httpapi.ContractRoute, len(runtimeRoutes))
	for _, route := range runtimeRoutes {
		key := route.Method + " " + route.Path
		if _, duplicate := runtimeByKey[key]; duplicate {
			t.Fatalf("runtime route table contains duplicate %s", key)
		}
		runtimeByKey[key] = route
	}
	seen := make(map[string]bool, len(manifest.Operations))
	for _, operation := range manifest.Operations {
		key := operation.Method + " " + operation.Path
		if seen[key] || operation.Name == "" {
			t.Fatalf("duplicate or unnamed operation %q", key)
		}
		seen[key] = true
		runtimeRoute, exists := runtimeByKey[key]
		if !exists {
			t.Errorf("SDK route manifest contains unregistered route %s", key)
			continue
		}
		if runtimeRoute.OperationID != operation.Name ||
			runtimeRoute.SuccessStatus != operation.Status {
			t.Errorf(
				"route %s projection mismatch: manifest=%+v runtime=%+v",
				key,
				operation,
				runtimeRoute,
			)
		}
	}
}

func TestReleaseDocumentationMatchesGeneratedIdentity(t *testing.T) {
	version := protocol.ContractReleaseVersion
	status := protocol.ContractReleaseStatus
	if status == "" {
		t.Fatal("generated release status is empty")
	}
	statusLabel := strings.ToUpper(status[:1]) + status[1:]
	files := []string{
		"../README.en.md",
		"../README.md",
		"../CHANGELOG.md",
		"../CHANGELOG.zh-CN.md",
		"../ROADMAP.en.md",
		"../ROADMAP.md",
		"../SECURITY.en.md",
		"../SECURITY.md",
		"../docs/compatibility.md",
		"../docs/compatibility.zh-CN.md",
		"../docs/protocol-v2.md",
		"../docs/protocol-v2.zh-CN.md",
		"../docs/release-guide.md",
		"../docs/release-guide.zh-CN.md",
	}
	for _, path := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		if !strings.Contains(text, "`"+version+"`") ||
			!strings.Contains(text, statusLabel) {
			t.Errorf(
				"%s does not identify generated release %s as %s",
				path,
				version,
				statusLabel,
			)
		}
	}
	for _, path := range []string{"../docs/release-guide.md", "../docs/release-guide.zh-CN.md"} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), "`v"+version+"`") {
			t.Errorf("%s does not identify generated release tag v%s", path, version)
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

func TestClientDefaultResponseLimitsMatchInlineTransportBudget(t *testing.T) {
	tests := []struct {
		path     string
		required string
	}{
		{"../sdk/python/src/rin_sdk/client.py", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
		{"../sdk/javascript/src/index.js", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
		{"../sdk/csharp/Rin.Client/RinClientOptions.cs", "MaxResponseBytes { get; init; } = 32 * 1024 * 1024"},
		{"../sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
		{"../sdk/lua/rin.lua", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
		{"../adapters/renpy/rin_client.py", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
		{"../examples/godot/rin_client.gd", "max_response_bytes := 33554432"},
		{"../examples/unity/RinClient.cs", "maxResponseBytes = 32 * 1024 * 1024"},
		{"../examples/mods/luanti-rin-npc/rin.lua", "DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024"},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), test.required) {
			t.Errorf("%s is missing 32 MiB default %q", test.path, test.required)
		}
	}
}

func TestSDKJobWaitersValidateReturnedIdentity(t *testing.T) {
	tests := []struct {
		path     string
		required []string
	}{
		{
			path: "../sdk/python/src/rin_sdk/client.py",
			required: []string{
				"_validate_job_identity",
				"response_job_id != expected_job_id",
				`proposal.get("session_id") != job["session_id"]`,
				"_is_nonnegative_json_safe_integer",
				"_MAX_GENERATION_CONTENT_BYTES",
			},
		},
		{
			path: "../sdk/javascript/src/index.js",
			required: []string{
				"validateJobIdentity",
				"job.job_id !== expectedJobId",
				"proposal.session_id !== job.session_id",
				"Number.isSafeInteger(proposal.tick)",
				"MAX_GENERATION_CONTENT_BYTES",
			},
		},
		{
			path: "../sdk/csharp/Rin.Client/RinClient.cs",
			required: []string{
				"ValidateJobIdentity",
				"responseJobId != expectedJobId",
				"proposalSessionId != jobSessionId",
				"TryNonnegativeJsonSafeIntegerProperty",
				"MaxGenerationContentBytes",
			},
		},
		{
			path: "../sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java",
			required: []string{
				"validateJobIdentity",
				"!id.equals(expectedJobId)",
				`Objects.equals(proposal.get("session_id"), job.get("session_id"))`,
				"isNonnegativeJsonSafeInteger",
				"MAX_GENERATION_CONTENT_BYTES",
			},
		},
		{
			path: "../sdk/lua/rin.lua",
			required: []string{
				"resolve_job(job, result_kind, expected_job_id)",
				"job.job_id ~= expected_job_id",
				"proposal.session_id ~= job.session_id",
				"is_nonnegative_json_safe_integer",
				"max_generation_content_bytes",
			},
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
				t.Errorf("%s is missing job identity guard %q", test.path, required)
			}
		}
	}

	testSources := []string{
		"../sdk/python/tests/test_client.py",
		"../sdk/javascript/test/client.test.js",
		"../sdk/csharp/Rin.Client.Tests/Program.cs",
		"../sdk/java/test/io/github/sunrioa/rin/RinClientTest.java",
		"../sdk/lua/test_client.lua",
	}
	for _, path := range testSources {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, required := range []string{"crossed", "malformed", "GET", "DELETE", "invalid_job"} {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing crossed/malformed race coverage marker %q", path, required)
			}
		}
	}
}

func TestCSharpJobStatusUsesRawJSONStrings(t *testing.T) {
	payload, err := os.ReadFile("../sdk/csharp/Rin.Client/RinClient.cs")
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, required := range []string{
		"RequiredRawJobStatus(canceledJob)",
		"RequiredRawJobStatus(job)",
		"property.ValueKind != JsonValueKind.String",
		"var status = property.GetString()",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("C# SDK is missing raw job-status guard %q", required)
		}
	}
	for _, forbidden := range []string{
		`TextProperty(canceledJob, "status"`,
		`TextProperty(job, "status"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("C# SDK normalizes decision-bearing job status through %q", forbidden)
		}
	}

	tests, err := os.ReadFile("../sdk/csharp/Rin.Client.Tests/Program.cs")
	if err != nil {
		t.Fatal(err)
	}
	testText := string(tests)
	for _, required := range []string{`canceled\\u0000`, `" canceled "`, "job_outcome_unknown"} {
		if !strings.Contains(testText, required) {
			t.Errorf("C# SDK tests are missing pseudo-status coverage marker %q", required)
		}
	}
}

func TestCSharpCIMatrixPreservesSupportedTargets(t *testing.T) {
	projects := map[string][]string{
		"../sdk/csharp/Rin.Client/Rin.Client.csproj": {
			`<TargetFrameworks Condition="'$(RinTargetFramework)' == ''">net6.0;netstandard2.0</TargetFrameworks>`,
			`<TargetFramework Condition="'$(RinTargetFramework)' != ''">$(RinTargetFramework)</TargetFramework>`,
		},
		"../sdk/csharp/Rin.Client.Tests/Rin.Client.Tests.csproj": {
			`<RinTargetFramework Condition="'$(RinTargetFramework)' == ''">net6.0</RinTargetFramework>`,
			`<TargetFramework>$(RinTargetFramework)</TargetFramework>`,
		},
	}
	for path, requiredMarkers := range projects {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, required := range requiredMarkers {
			if !strings.Contains(text, required) {
				t.Errorf("%s is missing conditional target marker %q", path, required)
			}
		}
	}

	workflow, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`framework: "net6.0"`,
		`framework: "net10.0"`,
		`-p:RinTargetFramework=${{ matrix.framework }}`,
		`sdk/csharp/Rin.Client.Tests/bin/Debug/${{ matrix.framework }}/Rin.Client.Tests.dll`,
		`-p:RinTargetFramework=netstandard2.0`,
	} {
		if !strings.Contains(string(workflow), required) {
			t.Errorf("C# CI matrix is missing %q", required)
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
			path: "../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java",
			required: []string{
				"activePlayers", "WorkflowCoordinator", "FabricNpcActions.plan",
				"FabricHostRuntime.current", "ProposalFreshness.evaluate",
				"HostDurabilityProfile.ADVISORY", "RinNpcRequests",
			},
			forbidden: []string{
				"Runtime.getRuntime().exec", "ProcessBuilder", ".join()",
				`text(proposal, "proposal_id")`, "persistOperationState",
				"rin.commit", "waitForProposal", "ProposalAttempt",
			},
		},
		{
			path: "../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricNpcActions.java",
			required: []string{
				"ALLOWED_OFFERS", "matchesProposal", "host.player",
			},
			forbidden: []string{
				"Runtime.getRuntime().exec", "ProcessBuilder", ".join()",
			},
		},
		{
			path: "../examples/mods/bepinex-rin-npc/RinNpc.Core/RinNpcRuntime.cs",
			required: []string{
				"AllowedOffers", "WorkflowCoordinator", "ProposalFreshness.Evaluate",
				"host.ApplyDialogueAsync", "HostDurabilityProfile.Advisory",
				"ApplyAndEnqueueOutcomeAsync",
			},
			forbidden: []string{
				"Process.Start", ".Result", ".Wait()", "WaitForProposalAsync",
				"CommitAsync",
			},
		},
		{
			path: "../examples/mods/bepinex-rin-npc/RinNpc.Mono/Plugin.cs",
			required: []string{
				"mainThread.Enqueue", "ApplyDialogueAsync",
				`Environment.GetEnvironmentVariable("RIN_TOKEN")`,
			},
			forbidden: []string{
				`Config.Bind("Connection", "Token"`, ".Result", ".Wait()",
			},
		},
		{
			path: "../examples/mods/luanti-rin-npc/init.lua",
			required: []string{
				"core.request_http_api", "local_origin", "allowed_actions",
				"rin.new_workflow", "workflow:begin", "workflow:resume",
				"workflow:apply_and_enqueue", "workflow:drain_outbox",
				"rin.proposal_freshness", "state_module.open",
			},
			forbidden: []string{
				"secure.trusted_mods", "request.headers.Authorization =", "os.execute",
				"proposal.proposal_id", "wait_for_proposal", "client:commit",
			},
		},
		{
			path: "../examples/mods/luanti-rin-npc/state.lua",
			required: []string{
				"function State:create_attempt", "function State:save_attempt",
				"function State:complete_attempt", "function State:list_outcomes",
				"function State:acknowledge_outcome",
			},
			forbidden: []string{"core."},
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
