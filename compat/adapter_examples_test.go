package compat_test

import (
	"os"
	"strings"
	"testing"
)

func TestEngineAdaptersKeepNetworkWorkOffAuthorityThreads(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		required  []string
		forbidden []string
	}{
		{
			name: "godot",
			path: "../examples/godot/rin_client.gd",
			required: []string{
				"await request.request_completed",
				"request.max_redirects = 0",
				"request.body_size_limit = max_response_bytes",
				`OS.get_environment(token_environment)`,
				`token.contains("\r")`,
				"func report_action(",
				"func report_action_batch(",
			},
			forbidden: []string{
				"OS.execute", "FileAccess.open", "Thread.wait_to_finish",
				"@export var token :=",
			},
		},
		{
			name: "unity",
			path: "../examples/unity/RinClient.cs",
			required: []string{
				"UnityWebRequest",
				"yield return request.SendWebRequest()",
				"request.redirectLimit = 0",
				"BoundedDownloadHandler",
				`tokenEnvironment = "RIN_TOKEN"`,
				"Environment.GetEnvironmentVariable(tokenEnvironment)",
				"public IEnumerator ReportAction(",
			},
			forbidden: []string{
				"Thread.Sleep", ".Wait()", "Process.Start",
				"[SerializeField] private string token =",
			},
		},
		{
			name: "renpy",
			path: "../adapters/renpy/rin_client.py",
			required: []string{
				"class _NoRedirectHandler",
				"class BackgroundProposalRegistry",
				"def report_action(",
				"def report_action_batch(",
				"_validate_generation_job_identity",
			},
			forbidden: []string{"import requests", "subprocess", "os.system"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertSourceMarkers(t, test.path, test.required, test.forbidden)
		})
	}
}

func TestEngineAdaptersUseTypedHostLifecycle(t *testing.T) {
	tests := map[string][]string{
		"../examples/godot/example_npc.gd": {
			`"decision_window"`,
			`"offers"`,
			"immediate_action_report(",
			`"kind": "report"`,
		},
		"../examples/godot/rin_client.gd": {
			"static func action_offer(",
			"static func immediate_action_report(",
			`report["invocation"]`,
			`report["run"]`,
			`report["outcome"]`,
		},
		"../examples/unity/RinUnityWorkflow.cs": {
			"host.CaptureTurn(",
			"RinUnityActionGate",
			"host.BeginAction(",
			"BuildReport(",
			"reportOutbox",
		},
		"../examples/unity/RinUnityActionGate.cs": {
			"IRinUnityHost",
			"IRinUnityAction BeginAction(",
			"ReplaceAuthority(",
			"outcome-unknown",
		},
		"../adapters/renpy/rin_client.py": {
			`"decision_window"`,
			`"offers"`,
			`"offer_id"`,
			`"source": "sidecar"`,
		},
	}
	for path, required := range tests {
		assertSourceMarkers(t, path, required, nil)
	}

	forbidden := []string{
		"outcome-reporting-v1",
		"ActionSpec",
		"CommitRequest",
		"BatchCommit",
		"committable",
		"/v1/action/commit",
		"action_specs",
	}
	for _, path := range []string{
		"../examples/godot/rin_client.gd",
		"../examples/godot/rin_workflow.gd",
		"../examples/godot/example_npc.gd",
		"../examples/unity/RinClient.cs",
		"../examples/unity/RinUnityWorkflow.cs",
		"../adapters/renpy/rin_client.py",
	} {
		assertSourceMarkers(t, path, nil, forbidden)
	}
}

func TestUnityWorkflowPersistsBeforeNetworkAndDrainsBeforeNextTurn(t *testing.T) {
	const path = "../examples/unity/RinUnityWorkflow.cs"
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, required := range []string{
		"authoritativeStateReady = RestoreAuthoritativeState();",
		"private const int StateSchemaVersion = 3;",
		"private PendingTurnState pendingTurn;",
		"private ActiveRunState activeRun;",
		"private readonly Dictionary<string, AppliedMarker> applied",
		"private readonly List<ReportOutboxEntry> reportOutbox",
		"BeginAuthorityLifetime()",
		"actionGate.ReplaceAuthority(",
		"offer_arguments_json",
		"arguments_json",
		"AdvanceEpoch(bool timelineChanged)",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("%s is missing durable workflow rule %q", path, required)
		}
	}
	persistAt := strings.Index(text, "if (PersistCurrentState()) return true;")
	observeAt := strings.Index(text, "yield return rin.Observe(pendingTurn.observation")
	proposeAt := strings.Index(text, "yield return rin.Propose(pendingTurn.request")
	if persistAt < 0 || observeAt <= persistAt || proposeAt <= observeAt {
		t.Error("Unity must persist a complete Pending Turn before Observe and Propose")
	}
	drainAt := strings.Index(text, "yield return DrainOutbox(value => drained = value);")
	createAt := strings.Index(text, "if (pendingTurn == null && !CreatePendingTurn())")
	if drainAt < 0 || createAt <= drainAt {
		t.Error("Unity must drain reports before allocating another Pending Turn")
	}
}

func TestUnityReferenceIsInstallableRestartableAndThin(t *testing.T) {
	files := map[string][]string{
		"../examples/unity/package.json": {
			`"name": "io.github.sunrioa.rin.unity"`,
			`"version": "0.7.0"`,
			`"unity": "2021.3"`,
			"Rin Protocol v2",
		},
		"../tools/unity-harness/Program.cs": {
			"restart minted a new identity",
			"domain reload did not advance Host generation",
			"scene load did not advance World generation",
			"late action callback revived a terminal run",
			"interrupted action did not enter the Outbox",
			"backup recovery changed identity",
			"write failure published a new identity",
			"malformed state was accepted",
			"arguments became a JSON string",
		},
		"../examples/unity/RinNpcExample.cs": {
			"IRinUnityHost",
			"workflow.RequestTurn()",
			"CaptureTurn(",
			"BeginAction(",
			"RinNavMeshAction.Begin(",
		},
		"../examples/unity/RinNavMeshAction.cs": {
			"NavMeshAgent",
			"SetDestination(",
			"ResetPath()",
			"outcome-unknown",
		},
		"../examples/unity/RinUnityStateFile.cs": {
			"stream.Flush(true)",
			"File.Replace(temporary, path, backup)",
			"MaxStateBytes",
		},
	}
	for path, required := range files {
		assertSourceMarkers(t, path, required, nil)
	}
	payload, err := os.ReadFile("../examples/unity/RinNpcExample.cs")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(payload), "\n") + 1; lines > 100 {
		t.Errorf("Unity host grew to %d lines; want at most 100", lines)
	}
}

func TestGodotReferenceDelegatesToPersistentWorkflow(t *testing.T) {
	files := map[string][]string{
		"../examples/godot/project.godot": {
			`run/main_scene="res://main.tscn"`,
		},
		"../examples/godot/rin_workflow.gd": {
			"const MAX_BYTES := 1024 * 1024",
			`_path = "user://rin/%s.json" % slot`,
			"func begin(",
			"func resume()",
			"func complete(",
			"func drain_outbox()",
			"_client.report_action(",
			"file.flush()",
			"DirAccess.rename_absolute(temporary, _path)",
		},
		"../examples/godot/example_npc.gd": {
			"workflow.open(",
			"workflow.begin(",
			"workflow.resume()",
			"workflow.complete(",
			"workflow.drain_outbox()",
		},
	}
	for path, required := range files {
		assertSourceMarkers(t, path, required, nil)
	}
	assertSourceMarkers(t, "../examples/godot/example_npc.gd", nil, []string{
		"FileAccess.", "_proposal_attempts", "_report_outbox",
	})
}

func TestModExamplesDelegateWorkflowAndKeepGameAuthority(t *testing.T) {
	tests := map[string][]string{
		"../examples/mods/bepinex-rin-npc/RinNpc.Core/RinNpcRuntime.cs": {
			"WorkflowCoordinator",
			"ApplyAndEnqueueOutcomeAsync",
			"HostDurabilityProfile.Advisory",
			"HostActions.ImmediateReport",
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.Core/BepInExWorkflowState.cs": {
			"IWorkflowStore",
			"OutcomeOutboxEntry",
			"Flush(flushToDisk: true)",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java": {
			"WorkflowCoordinator",
			"FabricHostRuntime.current",
			"preparePendingTurn",
			"HostDurabilityProfile.ADVISORY",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricHostRuntime.java": {
			"ServerLifecycleEvents.SERVER_STARTED",
			"server.isOnThread()",
			"server.isDedicated()",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricNpcActions.java": {
			"ALLOWED_OFFERS",
			"matchesProposal",
			"host.player",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricWorkflowStore.java": {
			"implements WorkflowStore",
			"savePendingTurn",
			"OutcomeOutboxEntry",
			"acknowledgeOutcome",
		},
		"../examples/mods/luanti-rin-npc/init.lua": {
			"core.request_http_api",
			"rin.new_workflow",
			"workflow:begin",
			"workflow:resume",
			"workflow:apply_and_enqueue",
			"workflow:drain_outbox",
		},
	}
	for path, required := range tests {
		assertSourceMarkers(t, path, required, []string{
			"Process.Start", "Runtime.getRuntime().exec", "os.execute",
			"CommitRequest", "outcome-reporting-v1",
		})
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

func TestWindowsSidecarLauncherStaysLocalAndLiteral(t *testing.T) {
	assertSourceMarkers(t, "../tools/start-rin.ps1", []string{
		`$Address = "127.0.0.1:7374"`,
		"Test-Path -LiteralPath $Rin",
		"[System.IO.Path]::GetFullPath($DataDirectory)",
		"& $Rin serve --addr $Address --data $data",
		"exit $LASTEXITCODE",
	}, nil)
}

func assertSourceMarkers(
	t *testing.T,
	path string,
	required []string,
	forbidden []string,
) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, marker := range required {
		if !strings.Contains(text, marker) {
			t.Errorf("%s is missing %q", path, marker)
		}
	}
	for _, marker := range forbidden {
		if strings.Contains(text, marker) {
			t.Errorf("%s contains obsolete or unsafe pattern %q", path, marker)
		}
	}
}
