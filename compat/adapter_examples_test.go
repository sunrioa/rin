package compat_test

import (
	"os"
	"strings"
	"testing"
)

func TestEngineExamplesPreserveAsyncAuthorityBoundary(t *testing.T) {
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
				"HTTPClient.METHOD_DELETE",
				`_closed_result("proposal_outcome_unknown")`,
				"_cancel_and_resolve",
				"\"committable\": false",
				"\"policy_source\": \"adapter-offline\"",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
				"AMBIGUOUS_PROPOSAL_ERRORS",
				"_terminal_error_code",
				"_same_protocol_id",
				"_is_valid_action_spec",
				"left_number >= 0.0",
				`OS.get_environment(token_environment)`,
				`token.contains("\r")`,
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
				"request.redirectLimit = 0",
				"CappedDownloadHandler",
				"WaitForSecondsRealtime",
				`BuildClosedResult("proposal_outcome_unknown"`,
				"ResolveCancellation",
				"allowOfflineBeforeSubmit",
				"if (!IsConfigured)",
				"committable = false",
				"policy_source = \"adapter-offline\"",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
				"public long observed_tick",
				"public long updated_tick",
				"public long progress_accumulator",
				"public bool status_explicit",
				"public long status_updated_tick",
				"public string status_source_event_id",
				"public string outcome_event_id",
				"public long outcome_tick",
				"bool allowOfflineBeforeSubmit = true",
				"AmbiguousProposalErrors",
				"TryGetTerminalErrorCode",
				"TryReadTopLevelProtocolIdProperty",
				"ActionMatchesCandidate",
			},
			forbidden: []string{
				"Thread.Sleep",
				".Wait()",
				"Process.Start",
				"!IsConfigured || (allowOfflineBeforeSubmit && string.IsNullOrEmpty(jobId))",
				"bool allowOfflineBeforeSubmit = false",
			},
		},
		{
			name: "renpy",
			path: "../adapters/renpy/rin_client.py",
			required: []string{
				"class _NoRedirectHandler",
				"class BackgroundProposalRegistry",
				"committable\": False",
				"adapter-offline",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
				"allow_offline_before_submit",
				"_validate_generation_job_identity",
			},
			forbidden: []string{"import requests", "subprocess", "os.system"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Fatalf("%s is missing %q", test.path, required)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s contains forbidden pattern %q", test.path, forbidden)
				}
			}
		})
	}
}

func TestEngineAdaptersFailClosedForUnknownProposalOutcomes(t *testing.T) {
	tests := []struct {
		name               string
		path               string
		required           []string
		minimumOccurrences map[string]int
	}{
		{
			name: "godot",
			path: "../examples/godot/rin_client.gd",
			required: []string{
				`and not submission.has("status")`,
				`if reason == "proposal_outcome_unknown" and not recovery_post_used:`,
				"if reason in AMBIGUOUS_PROPOSAL_ERRORS:",
				"return _closed_result(reason, job_id)",
				`"status": status`,
			},
			minimumOccurrences: map[string]int{"_terminal_job_result(": 3},
		},
		{
			name: "unity",
			path: "../examples/unity/RinClient.cs",
			required: []string{
				`if (reason == "proposal_outcome_unknown" && !recoveryPostUsed)`,
				"if (AmbiguousProposalErrors.Contains(reason))",
				"return BuildClosedResult(reason, jobId);",
			},
			minimumOccurrences: map[string]int{"BuildTerminalResult(": 3},
		},
		{
			name: "renpy",
			path: "../adapters/renpy/rin_client.py",
			required: []string{
				`"proposal_outcome_unknown",`,
				"allow_offline_before_submit=False",
				"_validate_generation_job_identity(job, expected_job_id)",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Fatalf("%s is missing fail-closed contract %q", test.path, required)
				}
			}
			for fragment, minimum := range test.minimumOccurrences {
				if count := strings.Count(text, fragment); count < minimum {
					t.Fatalf(
						"%s contains %q %d times; want at least %d",
						test.path,
						fragment,
						count,
						minimum,
					)
				}
			}
		})
	}
}

func TestEngineNpcExamplesPersistAuthoritativeReportsAtomically(t *testing.T) {
	tests := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: "../examples/unity/RinUnityWorkflow.cs",
			required: []string{
				"appliedOperations", "reportOutbox",
				`features = new[] { "outcome-reporting-v1" }`,
				"FlushReportOutbox", "PersistAuthoritativeTransaction",
				"PersistReportAcknowledgement",
				`request_id = "commit." + operationId`,
				"PendingReport.Observe",
				`request_id = "reconcile." + operationId`,
				`event_id = "fallback." + operationId`,
			},
			forbidden: []string{"outcomeOutbox", "FlushOutcomeOutbox", "PersistOperationState"},
		},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range test.required {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing authoritative-report contract %q", test.path, fragment)
			}
		}
		for _, fragment := range test.forbidden {
			if strings.Contains(string(payload), fragment) {
				t.Errorf("%s contains obsolete split-persistence pattern %q", test.path, fragment)
			}
		}
	}
}

func TestEngineNpcExamplesResumeDurableProposalAttempts(t *testing.T) {
	tests := []struct {
		name           string
		clientPath     string
		gamePath       string
		clientRequired []string
		gameRequired   []string
		gameForbidden  []string
		persistMarker  string
		submitMarker   string
	}{
		{
			name:       "unity",
			clientPath: "../examples/unity/RinClient.cs",
			gamePath:   "../examples/unity/RinUnityWorkflow.cs",
			clientRequired: []string{
				"string knownJobId",
				"persistJobId(jobId)",
				`pollCall.ErrorCode == "job_not_found"`,
				`reason == "proposal_outcome_unknown" && !recoveryPostUsed`,
				"recoveryPostUsed = true",
			},
			gameRequired: []string{
				"proposalAttempts",
				"new ProposalAttempt(",
				"nextSequence",
				"if (!resuming)",
				"operationSequence = Math.Max(",
				`knownJobId: attempt.jobId`,
				"PersistProposalJobId",
				"allowOfflineBeforeSubmit: !resuming",
				"proposalAttempts.Remove(sessionId)",
				"proposalAttempts[sessionId] = proposalAttempt",
				"Time.frameCount",
			},
			gameForbidden: []string{
				"operationSequence++",
				"authoritativeGameTick",
			},
			persistMarker: "PersistNewProposalAttempt(",
			submitMarker:  "yield return rin.ProposeWithFallback(",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientPayload, err := os.ReadFile(test.clientPath)
			if err != nil {
				t.Fatal(err)
			}
			clientText := string(clientPayload)
			for _, fragment := range test.clientRequired {
				if !strings.Contains(clientText, fragment) {
					t.Errorf("%s is missing resumable-client contract %q", test.clientPath, fragment)
				}
			}

			gamePayload, err := os.ReadFile(test.gamePath)
			if err != nil {
				t.Fatal(err)
			}
			gameText := string(gamePayload)
			for _, fragment := range test.gameRequired {
				if !strings.Contains(gameText, fragment) {
					t.Errorf("%s is missing durable-attempt contract %q", test.gamePath, fragment)
				}
			}
			for _, fragment := range test.gameForbidden {
				if strings.Contains(gameText, fragment) {
					t.Errorf("%s regrows sequence during a resumed attempt via %q", test.gamePath, fragment)
				}
			}
			persistAt := strings.Index(gameText, test.persistMarker)
			submitAt := strings.Index(gameText, test.submitMarker)
			if persistAt < 0 || submitAt < 0 || persistAt >= submitAt {
				t.Errorf(
					"%s must persist a complete Proposal attempt before submitting it",
					test.gamePath,
				)
			}
		})
	}
}

func TestEngineNpcExamplesGateStartupOnAuthoritativeStateRecovery(t *testing.T) {
	tests := []struct {
		name                  string
		path                  string
		required              []string
		restoreCall           string
		firstOnlineOperation  string
		initializeStart       string
		persistInitialization string
		publishInitialization string
	}{
		{
			name: "unity",
			path: "../examples/unity/RinUnityWorkflow.cs",
			required: []string{
				"authoritativeStateReady = RestoreAuthoritativeState();",
				"if (!authoritativeStateReady)",
				"AuthoritativeStateLoadStatus.Loaded",
				"AuthoritativeStateLoadStatus.NotFound",
				"schemaVersion = 2",
				"runId = newRunId",
				"operationSequence = 0",
				"createRequest = BuildCreateRequest(newRunId)",
				"proposalAttempts = new ProposalAttemptState[0]",
				"appliedOperations = new AppliedOperationState[0]",
				"reportOutbox = new PendingReportState[0]",
				"PersistAuthoritativeStateInitialization(initialized)",
				"foreach (var entry in restoredAttempts) proposalAttempts.Add(",
				"foreach (var entry in restoredApplied) appliedOperations.Add(",
				"foreach (var entry in restoredOutbox) reportOutbox.Add(",
				"!restoredApplied.ContainsKey(saved.operationId)",
				"restoredApplied.ContainsKey(attempt.operationId)",
				"restoredOutbox.ContainsKey(attempt.operationId)",
				`!= "propose." + saved.operationId`,
				`!= "commit." + saved.operationId`,
				`!= "outcome." + saved.operationId`,
				"!RinClient.IsProtocolId(saved.commit.proposal_id)",
				`!= "reconcile." + saved.operationId`,
				"saved.fallback.session_id != saved.commit.session_id",
				"saved.fallback.event_id != saved.commit.event_id",
				"saved.fallback.tick != saved.commit.tick",
				`saved.observe.event_id != "fallback." + saved.operationId`,
				"Math.Max(proposalAttempt.request.tick, proposalTick)",
				"Math.Max(0L, (long)Time.frameCount)",
			},
			restoreCall:           "RestoreAuthoritativeState()",
			firstOnlineOperation:  "yield return rin.CreateSession(",
			initializeStart:       "var newRunId =",
			persistInitialization: "PersistAuthoritativeStateInitialization(initialized)",
			publishInitialization: "return TryHydrateAuthoritativeState(initialized)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, fragment := range test.required {
				if !strings.Contains(text, fragment) {
					t.Errorf("%s is missing recovery contract %q", test.path, fragment)
				}
			}
			restoreAt := strings.Index(text, test.restoreCall)
			onlineAt := strings.Index(text, test.firstOnlineOperation)
			if restoreAt < 0 || onlineAt < 0 || restoreAt >= onlineAt {
				t.Errorf("%s must restore authoritative state before online work", test.path)
			}
			initializeAt := strings.Index(text, test.initializeStart)
			persistAt := strings.Index(text, test.persistInitialization)
			publishAt := strings.Index(text, test.publishInitialization)
			if initializeAt < 0 || persistAt <= initializeAt || publishAt <= persistAt {
				t.Errorf(
					"%s must persist a confirmed-new identity before publishing it",
					test.path,
				)
			}
		})
	}
}

func TestEngineNpcExamplesRestoreClockIdentityAndFreshnessInvariants(t *testing.T) {
	tests := []struct {
		name            string
		path            string
		required        []string
		forbidden       []string
		persistCall     string
		sequencePublish string
		tickPublish     string
		submitCall      string
	}{
		{
			name: "unity",
			path: "../examples/unity/RinUnityWorkflow.cs",
			required: []string{
				"private long lastAuthoritativeTick;",
				"lastAuthoritativeTick = 0",
				"state.lastAuthoritativeTick < 0",
				"saved.request.tick > state.lastAuthoritativeTick",
				"saved.commit.tick > state.lastAuthoritativeTick",
				"saved.observe.tick > state.lastAuthoritativeTick",
				"lastAuthoritativeTick = state.lastAuthoritativeTick",
				"var expectedCreateRequest = BuildCreateRequest(state.runId)",
				"SemanticDtoEquals(state.createRequest, expectedCreateRequest)",
				"BuildProposeRequest(",
				"SemanticDtoEquals(",
				`saved.fallbackActionId != "wait"`,
				"type.GetFields(BindingFlags.Instance | BindingFlags.Public)",
				"TryAllocateFreshProposalTick(out var newGameTick)",
				"lastAuthoritativeTick == long.MaxValue",
				"lastAuthoritativeTick + 1",
				"authoritativeTick <= lastAuthoritativeTick",
				"attempt.request.tick != authoritativeTick",
				"TryParseOperationSequence(",
				"saved.sequence != state.operationSequence",
				"attemptOperationSequence != saved.sequence",
				"appliedOperationSequence > state.operationSequence",
				"outboxOperationSequence > state.operationSequence",
				"saved.commit.accepted != restoredApplied[saved.operationId].accepted",
				"saved.commit.outcome != restoredApplied[saved.operationId].outcome",
				"OutcomeObserveMatchesApplied(",
				`observe.source != "unity-example"`,
				`observe.summary == "Authoritative outcome: " + applied.outcome`,
				`"Local fallback " + applied.actionId + ": " + applied.outcome`,
				"var previousLastTick = lastAuthoritativeTick",
				"lastAuthoritativeTick = occurrenceTick",
				"lastAuthoritativeTick = previousLastTick",
				"CommitAuthoritativeGameTransaction(operationId, occurrenceTick)",
				"retained.session_id != proposal.session_id",
				"string.IsNullOrEmpty(retained.id)",
				"retained.request_id != proposal.request_id",
				"retained.actor_id != proposal.actor_id",
				"retained.tick != proposal.tick",
				"retained.action.id != proposal.action.id",
				"retained.action.kind != proposal.action.kind",
				"retained.based_on_revision != proposal.based_on_revision",
				"retained.based_on_head_hash != proposal.based_on_head_hash",
				"retained.based_on_world_revision != proposal.based_on_world_revision",
				"retained.created_revision != proposal.created_revision",
				"SemanticDtoEquals(retained.action, proposal.action)",
				"SemanticDtoEquals(stableAction, proposal.action)",
				"retained.has_unsupported_action_parameters",
				"proposal.has_unsupported_action_parameters",
				"state.world_revision == retained.based_on_world_revision",
				"state.revision == retained.created_revision",
			},
			forbidden: []string{
				"state.world_revision == proposal.based_on_world_revision",
				"state.revision == proposal.created_revision",
			},
			persistCall:     "if (!PersistNewProposalAttempt(",
			sequencePublish: "operationSequence = nextSequence",
			tickPublish:     "lastAuthoritativeTick = newGameTick",
			submitCall:      "yield return rin.ProposeWithFallback(",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, fragment := range test.required {
				if !strings.Contains(text, fragment) {
					t.Errorf("%s is missing restored-state invariant %q", test.path, fragment)
				}
			}
			for _, fragment := range test.forbidden {
				if strings.Contains(text, fragment) {
					t.Errorf("%s still trusts an unsafe or obsolete path %q", test.path, fragment)
				}
			}
			persistAt := strings.Index(text, test.persistCall)
			sequenceAt := strings.Index(text, test.sequencePublish)
			tickAt := strings.Index(text, test.tickPublish)
			submitAt := strings.Index(text, test.submitCall)
			if persistAt < 0 ||
				sequenceAt <= persistAt ||
				tickAt <= persistAt ||
				submitAt <= sequenceAt ||
				submitAt <= tickAt {
				t.Errorf(
					"%s must durably allocate sequence/tick before publishing them or submitting",
					test.path,
				)
			}
		})
	}
}

func TestUnityExampleRejectsUnrepresentableActionParameters(t *testing.T) {
	const path = "../examples/unity/RinClient.cs"
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{
		"ActionHasUnsupportedParameters(proposalJson)",
		`FindTopLevelPropertyValue(actionJson, "parameters") >= 0`,
		"[NonSerialized] public bool has_unsupported_action_parameters",
		"public string description;",
		"public string[] target_ids;",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("%s is missing complete-action protection %q", path, fragment)
		}
	}
	if count := strings.Count(
		text,
		"proposal.has_unsupported_action_parameters =",
	); count < 2 {
		t.Errorf(
			"%s marks unsupported parameters in only %d Proposal decode paths; want 2",
			path,
			count,
		)
	}
}

func TestEngineExamplesValidateCanonicalRecoveryJobsAndSchedulerHeadroom(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		required  []string
		forbidden []string
		ordered   []string
	}{
		{
			name: "godot-client",
			path: "../examples/godot/rin_client.gd",
			required: []string{
				"not known_job_id.is_empty() and not _is_valid_protocol_id(known_job_id)",
				"var job_id_value = submission_data.get(\"job_id\")",
				"if not _is_valid_protocol_id(job_id_value):",
				"if not _is_valid_protocol_id(job_id):",
				"func _job_shape_matches_status(",
				`var has_proposal := job.has("proposal")`,
				`var has_error := job.has("error")`,
				`if status == "succeeded":`,
				`if status in ["failed", "stale", "canceled"]:`,
				`if status == "queued" or status == "running":`,
				`return _closed_result("invalid_job", job_id)`,
			},
			forbidden: []string{
				`str(submission["job_id"])`,
				`str(submission.get("data", {}).get("job_id", ""))`,
			},
		},
		{
			name: "unity-game",
			path: "../examples/unity/RinUnityWorkflow.cs",
			required: []string{
				"private const long NpcThinkEveryTicks = 5;",
				"BuildCommitRequest(",
				"BuildOutcomeObserveRequest(",
				"BuildFallbackObserveRequest(",
				`observer_ids = new[] { "npc.mira" }`,
				"saved.commit,\n                        BuildCommitRequest(",
				"SemanticDtoEquals(saved, new ProposalAttemptState",
				"RinClient.IsProtocolId(saved.jobId)",
				"occurrenceTick > long.MaxValue - NpcThinkEveryTicks",
				"ApplyPlannedGameEffect(effectivePlanned, transaction)",
				"WithAppliedOutcome(effectivePlanned)",
			},
			ordered: []string{
				"var occurrenceTick = Math.Max(",
				"occurrenceTick > long.MaxValue - NpcThinkEveryTicks",
				"ApplyPlannedGameEffect(effectivePlanned, transaction)",
			},
		},
		{
			name: "unity-client",
			path: "../examples/unity/RinClient.cs",
			required: []string{
				"jobId.Length > 0 && !IsValidProtocolId(jobId)",
				"TryReadTopLevelProtocolIdProperty(",
				`submissionJson,`,
				`"job_id",`,
				"JobShapeMatchesStatus(job, pollCall.Text)",
				"JobShapeMatchesStatus(job, call.Text)",
				`var proposalStart = FindTopLevelPropertyValue(jobJson, "proposal")`,
				`var errorStart = FindTopLevelPropertyValue(jobJson, "error")`,
				`job.status == "queued" || job.status == "running"`,
				"public static bool IsProtocolId(string value)",
			},
			forbidden: []string{
				"if (string.IsNullOrWhiteSpace(jobId))",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, fragment := range test.required {
				if !strings.Contains(text, fragment) {
					t.Errorf("%s is missing terminal invariant %q", test.path, fragment)
				}
			}
			for _, fragment := range test.forbidden {
				if strings.Contains(text, fragment) {
					t.Errorf("%s retains unsafe terminal pattern %q", test.path, fragment)
				}
			}
			previous := -1
			for _, fragment := range test.ordered {
				index := strings.Index(text, fragment)
				if index <= previous {
					t.Errorf(
						"%s must order %q after the previous terminal guard",
						test.path,
						fragment,
					)
				}
				previous = index
			}
		})
	}
}

func TestGodotReferenceDelegatesToPersistentWorkflow(t *testing.T) {
	files := map[string][]string{
		"../examples/godot/project.godot": {
			`config/features=PackedStringArray("4.7")`,
			`run/main_scene="res://main.tscn"`,
		},
		"../examples/godot/rin_workflow.gd": {
			"const MAX_BYTES := 1024 * 1024",
			"const MAX_OUTCOMES := 64",
			`_path = "user://rin/%s.json" % slot`,
			"Crypto.new().generate_random_bytes(16)",
			`"attempt": null`,
			`"outcomes": []`,
			"func begin(",
			"func resume()",
			"func complete(",
			"func drain_outbox()",
			"func shutdown()",
			"func _save_job(",
			"TERMINAL_COMMIT_ERRORS",
			`converted["kind"] = "observe"`,
			"static func proposal_freshness(",
			"static func _semantic_equal(",
			"file.flush()",
			"DirAccess.rename_absolute(temporary, _path)",
			"_state = candidate",
		},
		"../examples/godot/tests/test_workflow.gd": {
			"Session identity changed after restart",
			"restart resubmitted instead of using Job",
			"terminal fallback did not drain",
			"failed file write published Pending Turn",
			"malformed state was accepted",
		},
	}
	for path, required := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, fragment := range required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing Godot workflow contract %q", path, fragment)
			}
		}
	}

	workflowPayload, err := os.ReadFile("../examples/godot/rin_workflow.gd")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(workflowPayload)
	beginAt := strings.Index(workflow, "func begin(")
	persistAt := strings.Index(workflow, "if not _persist(candidate):")
	submitAt := strings.Index(workflow, "var result: Dictionary = await _client.propose_with_fallback(")
	if beginAt < 0 || persistAt < 0 || submitAt < 0 ||
		persistAt <= beginAt || persistAt >= submitAt {
		t.Error("Godot Workflow must persist the complete Pending Turn before submission")
	}
	flushAt := strings.Index(workflow, "file.flush()")
	backupAt := strings.LastIndex(workflow, "DirAccess.rename_absolute(_path, backup)")
	replaceAt := strings.LastIndex(workflow, "if DirAccess.rename_absolute(temporary, _path) != OK:")
	publishAt := strings.LastIndex(workflow, "_state = candidate")
	if flushAt < 0 || backupAt <= flushAt || replaceAt <= backupAt || publishAt <= replaceAt {
		t.Error("Godot Workflow must flush, back up, replace, then publish candidate state")
	}

	hostPayload, err := os.ReadFile("../examples/godot/example_npc.gd")
	if err != nil {
		t.Fatal(err)
	}
	host := string(hostPayload)
	for _, fragment := range []string{
		"workflow.open(", "workflow.begin(", "workflow.resume()",
		"workflow.complete(", "workflow.drain_outbox()",
		`"features": ["outcome-reporting-v1"]`,
		`"request_id": "commit." + operation_id`,
		`"kind": "observe"`,
	} {
		if !strings.Contains(host, fragment) {
			t.Errorf("Godot host is missing delegated integration %q", fragment)
		}
	}
	for _, fragment := range []string{
		"FileAccess.", "propose_with_fallback", "_proposal_attempts",
		"_report_outbox", "PRODUCTION PERSISTENCE HOOK",
	} {
		if strings.Contains(host, fragment) {
			t.Errorf("Godot host reimplements SDK/storage responsibility %q", fragment)
		}
	}
	if lines := strings.Count(host, "\n") + 1; lines > 250 {
		t.Errorf("Godot host grew to %d lines; want at most 250", lines)
	}
}

func TestUnityReferenceIsInstallableRestartableAndThin(t *testing.T) {
	files := map[string][]string{
		"../examples/unity/package.json": {
			`"name": "io.github.sunrioa.rin.unity"`,
			`"unity": "2021.3"`,
		},
		"../examples/unity/RinUnityWorkflow.cs": {
			"Application.persistentDataPath",
			"private const int MaxStateBytes = 1024 * 1024;",
			"private const int MaxOutcomes = 64;",
			"RecoverInterruptedReplacement()",
			"stream.Flush(true)",
			"File.Move(statePath, backup)",
			"File.Move(temporary, statePath)",
			"PersistCurrentState()",
			"BuildTurnObserveRequest(",
			"yield return rin.Observe(attempt.observe",
			"isCanceled: () => shutdownRequested",
		},
		"../tools/unity-harness/Program.cs": {
			"restart minted a new identity",
			"backup recovery changed identity",
			"write failure published a new identity",
			"malformed state was accepted",
		},
		"../examples/unity/RinClient.cs": {
			`tokenEnvironment = "RIN_TOKEN"`,
			"Environment.GetEnvironmentVariable(tokenEnvironment)",
		},
	}
	for path, required := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, fragment := range required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing Unity package contract %q", path, fragment)
			}
		}
	}
	clientPayload, err := os.ReadFile("../examples/unity/RinClient.cs")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clientPayload), "[SerializeField] private string token =") {
		t.Error("Unity client serializes a credential into scenes or prefabs")
	}
	hostPayload, err := os.ReadFile("../examples/unity/RinNpcExample.cs")
	if err != nil {
		t.Fatal(err)
	}
	host := string(hostPayload)
	if !strings.Contains(host, "workflow.RequestTurn()") {
		t.Error("Unity host does not delegate its turn to RinUnityWorkflow")
	}
	if lines := strings.Count(host, "\n") + 1; lines > 100 {
		t.Errorf("Unity host grew to %d lines; want at most 100", lines)
	}
}

func TestWindowsSidecarLauncherStaysLocalAndLiteral(t *testing.T) {
	payload, err := os.ReadFile("../tools/start-rin.ps1")
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, required := range []string{
		`$Address = "127.0.0.1:7374"`,
		"Test-Path -LiteralPath $Rin",
		"[System.IO.Path]::GetFullPath($DataDirectory)",
		"& $Rin serve --addr $Address --data $data",
		"exit $LASTEXITCODE",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Windows Sidecar launcher is missing %q", required)
		}
	}
}

func TestModExamplesOptIntoOutcomeReporting(t *testing.T) {
	tests := map[string]string{
		"../examples/mods/bepinex-rin-npc/RinNpc.Core/RinNpcRuntime.cs":                                   "RinFeatures.OutcomeReporting",
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcRequests.java": "outcome-reporting-v1",
		"../examples/mods/luanti-rin-npc/init.lua":                                                        "outcome-reporting-v1",
		"../examples/basic/main.go":                                                                       "FeatureOutcomeReporting",
		"../examples/recovery/main.go":                                                                    "FeatureOutcomeReporting",
	}
	for path, marker := range tests {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), marker) {
			t.Errorf("%s does not opt into outcome reporting", path)
		}
	}
}

func TestBepInExDelegatesWorkflowAndSeparatesBackends(t *testing.T) {
	files := map[string][]string{
		"../examples/mods/bepinex-rin-npc/RinNpc.Core/RinNpcRuntime.cs": {
			"WorkflowCoordinator", "ProposalFreshness.Evaluate",
			"BeginAsync", "ApplyAndEnqueueOutcomeWithFallbackAsync",
			"HostProfile.Advisory", `"offer_quest"`, `"advance_quest"`,
			"store.ApplyQuestEffect(", `"quest-stage-" + store.QuestStage`,
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.Core/BepInExWorkflowState.cs": {
			"IWorkflowFallbackStore", "StageTurnContext", "ReplaceWithFallbackAsync",
			"Flush(flushToDisk: true)", "MaxOutcomes", "ApplyQuestEffect(",
			"AppliedGameOperations", "QuestStage",
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.Mono/Plugin.cs": {
			"BaseUnityPlugin", "ConcurrentQueue<Action>", "ProductIdentity",
			"shutdown.Token",
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.IL2CPP/Plugin.cs": {
			"BasePlugin", "ApplyDialogue", "ProductIdentity", "override bool Unload",
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.Mono/RinNpc.Mono.csproj": {
			"netstandard2.0", "BepInEx.Unity.Mono", "6.0.0-be.785",
		},
		"../examples/mods/bepinex-rin-npc/RinNpc.IL2CPP/RinNpc.IL2CPP.csproj": {
			"net6.0", "BepInEx.Unity.IL2CPP", "6.0.0-be.785",
		},
	}
	for path, required := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, marker := range required {
			if !strings.Contains(text, marker) {
				t.Errorf("%s is missing integration boundary %q", path, marker)
			}
		}
	}
	if _, err := os.Stat("../examples/mods/bepinex-rin-npc/Plugin.cs"); !os.IsNotExist(err) {
		t.Error("obsolete monolithic BepInEx source overlay still exists")
	}
}

func TestFabricDelegatesWorkflowAndPersistsRestartState(t *testing.T) {
	files := map[string][]string{
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java": {
			"WorkflowCoordinator", "ProposalFreshness.evaluate",
			"preparePendingTurn", "HostProfile.ADVISORY",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinFabricState.java": {
			"worldId", "PendingTurn", "OutcomeOutboxEntry",
			"MAX_OUTCOMES_PER_SESSION", "markDirty()",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricWorkflowStore.java": {
			"implements WorkflowStore", "savePendingTurn",
			"replaceOutcomeWithFallback", "acknowledgeOutcome",
		},
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcRequests.java": {
			"outcome-reporting-v1", "candidate_actions",
			"safeObserve", "content_version",
		},
	}
	for path, required := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range required {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing Fabric recovery contract %q", path, fragment)
			}
		}
	}
}

func TestLuantiExampleResumesDurableProposalAttempts(t *testing.T) {
	const initPath = "../examples/mods/luanti-rin-npc/init.lua"
	initPayload, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatal(err)
	}
	const statePath = "../examples/mods/luanti-rin-npc/state.lua"
	statePayload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const sdkPath = "../sdk/lua/rin.lua"
	sdkPayload, err := os.ReadFile(sdkPath)
	if err != nil {
		t.Fatal(err)
	}
	initText, stateText, sdkText :=
		string(initPayload), string(statePayload), string(sdkPayload)
	initRequired := []string{
		"workflow_state:stage_turn",
		"workflow:begin(name, operation_id, propose)",
		"workflow:resume(name",
		"workflow:apply_and_enqueue",
		"workflow:drain_outbox",
		"mark_session_missing",
		"rin.proposal_freshness",
	}
	for _, fragment := range initRequired {
		if !strings.Contains(initText, fragment) {
			t.Errorf("%s is missing durable workflow delegation %q", initPath, fragment)
		}
	}
	stageAt := strings.Index(initText, "workflow_state:stage_turn")
	beginAt := strings.Index(initText, "workflow:begin(name, operation_id, propose)")
	resumeAt := strings.LastIndex(initText, "resume(name)")
	if stageAt < 0 || beginAt < 0 || resumeAt < 0 ||
		stageAt >= beginAt || beginAt >= resumeAt {
		t.Errorf("%s must stage Observe, persist Pending Turn, then resume network work", initPath)
	}
	stateRequired := []string{
		`local storage_key = "workflow_state_v1"`,
		"maximum_bytes = 1024 * 1024",
		"maximum_players = 128",
		"maximum_outcomes = 64",
		"function State:create_attempt",
		"function State:save_attempt",
		"function State:complete_attempt",
		"function State:replace_outcome",
		"function State:acknowledge_outcome",
		"self.state = candidate",
	}
	for _, fragment := range stateRequired {
		if !strings.Contains(stateText, fragment) {
			t.Errorf("%s is missing durable state boundary %q", statePath, fragment)
		}
	}
	sdkRequired := []string{
		`code == "proposal_outcome_unknown"`,
		`confirm_error.code == "job_not_found"`,
		"self.store:save_attempt(key, updated)",
		"self.store:replace_outcome(key, entry, converted)",
		"self.store:acknowledge_outcome(key, entry)",
	}
	for _, fragment := range sdkRequired {
		if !strings.Contains(sdkText, fragment) {
			t.Errorf("%s is missing coordinator recovery contract %q", sdkPath, fragment)
		}
	}
	persistJobAt := strings.Index(sdkText, "self.store:save_attempt(key, updated)")
	getJobAt := strings.Index(sdkText, "self.client:get_proposal_job(job_id")
	if persistJobAt < 0 || getJobAt < 0 || persistJobAt >= getJobAt {
		t.Errorf("%s must persist the Job ID before its first GET", sdkPath)
	}
}
