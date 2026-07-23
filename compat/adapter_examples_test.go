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
			},
			forbidden: []string{"OS.execute", "FileAccess.open", "Thread.wait_to_finish"},
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
			path: "../examples/godot/example_npc.gd",
			required: []string{
				"_applied_operations", "_report_outbox",
				`"features": ["outcome-reporting-v1"]`,
				"_flush_report_outbox", "_persist_authoritative_transaction",
				"_persist_report_acknowledgement",
				`"request_id": "commit." + operation_id`,
				`"kind": "observe"`,
				`"request_id": "reconcile." + operation_id`,
				`"event_id": "fallback." + operation_id`,
			},
			forbidden: []string{"_outcome_outbox", "_flush_outcome_outbox", "_persist_operation_state"},
		},
		{
			path: "../examples/unity/RinNpcExample.cs",
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
			name:       "godot",
			clientPath: "../examples/godot/rin_client.gd",
			gamePath:   "../examples/godot/example_npc.gd",
			clientRequired: []string{
				"known_job_id: String",
				"persist_job_id.call(job_id)",
				`== "job_not_found"`,
				`reason == "proposal_outcome_unknown" and not recovery_post_used`,
				"recovery_post_used = true",
			},
			gameRequired: []string{
				"_proposal_attempts",
				`"request": stable_request.duplicate(true)`,
				`"sequence": next_sequence`,
				`"job_id": ""`,
				"if resuming_attempt:",
				"_operation_sequence = maxi(",
				"_persist_proposal_job_id",
				"not resuming_attempt",
				"_proposal_attempts.erase(session_id)",
				"_proposal_attempts[session_id] = proposal_attempt",
				"Engine.get_physics_frames()",
			},
			gameForbidden: []string{
				"_operation_sequence += 1",
				"var _authoritative_tick :=",
			},
			persistMarker: "_persist_new_proposal_attempt(",
			submitMarker:  "await rin.propose_with_fallback(",
		},
		{
			name:       "unity",
			clientPath: "../examples/unity/RinClient.cs",
			gamePath:   "../examples/unity/RinNpcExample.cs",
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
			name: "godot",
			path: "../examples/godot/example_npc.gd",
			required: []string{
				"_authoritative_state_ready = _restore_authoritative_state()",
				"if not _authoritative_state_ready:",
				`if status == "loaded":`,
				`if status != "not_found":`,
				`return {"status": "error", "error": "restore hook not configured"}`,
				`"schema_version": 2`,
				`"run_id": new_run_id`,
				`"operation_sequence": 0`,
				`"create_request": _build_create_request(new_run_id)`,
				`"proposal_attempts": {}`,
				`"applied_operations": {}`,
				`"report_outbox": {}`,
				"_persist_authoritative_state_initialization(initialized_state)",
				"_proposal_attempts = restored_attempts.duplicate(true)",
				"_applied_operations = restored_applied.duplicate(true)",
				"_report_outbox = restored_outbox.duplicate(true)",
				"or restored_applied.has(attempt_operation_id)",
				"or restored_outbox.has(attempt_operation_id)",
				"or not restored_applied.has(operation_key)",
				`!= "propose." + attempt_operation_id`,
				`!= "commit." + operation_key`,
				`!= "outcome." + operation_key`,
				`not _is_valid_protocol_id(proposal_id)`,
				`!= "reconcile." + operation_key`,
				`!= str(request.get("event_id", ""))`,
				`!= request_tick`,
				`"fallback." + operation_key`,
				"var request_tick := _read_nonnegative_protocol_tick(",
				"var proposal_tick := _read_nonnegative_protocol_tick(",
				"maxi(request_tick, proposal_tick)",
				"number > 9007199254740991.0",
			},
			restoreCall:           "_restore_authoritative_state()",
			firstOnlineOperation:  "await rin.create_session(",
			initializeStart:       "var new_run_id :=",
			persistInitialization: "_persist_authoritative_state_initialization(initialized_state)",
			publishInitialization: "return _hydrate_authoritative_state(initialized_state)",
		},
		{
			name: "unity",
			path: "../examples/unity/RinNpcExample.cs",
			required: []string{
				"authoritativeStateReady = RestoreAuthoritativeState();",
				"if (!authoritativeStateReady)",
				"AuthoritativeStateLoadStatus.Loaded",
				"AuthoritativeStateLoadStatus.NotFound",
				`AuthoritativeStateLoadResult.Failed("restore hook not configured")`,
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
			name: "godot",
			path: "../examples/godot/example_npc.gd",
			required: []string{
				"const MAX_PROTOCOL_INTEGER := 9223372036854775807",
				`"last_authoritative_tick": 0`,
				"state.get(\"last_authoritative_tick\")",
				"attempt_tick > restored_last_tick",
				"request_tick > restored_last_tick",
				"_last_authoritative_tick = restored_last_tick",
				"var expected_create := _build_create_request(restored_run_id)",
				"_semantic_values_equal(restored_create, expected_create)",
				"func _build_propose_request(",
				"_semantic_values_equal(request, expected_request)",
				`str(attempt.get("fallback_action_id", "")) != "wait"`,
				"func _semantic_values_equal(",
				"left.size() != right.size()",
				"_allocate_fresh_proposal_tick()",
				"_last_authoritative_tick >= MAX_PROTOCOL_INTEGER",
				"_last_authoritative_tick + 1",
				"authoritative_tick <= _last_authoritative_tick",
				`_read_nonnegative_protocol_tick(request.get("tick")) != authoritative_tick`,
				"_operation_sequence_from_id(",
				"attempt_sequence != restored_sequence",
				"canonical_sequence != attempt_sequence",
				"applied_sequence > restored_sequence",
				"operation_sequence > restored_sequence",
				"request.get(\"accepted\") != applied.get(\"accepted\")",
				"request.get(\"outcome\") != applied.get(\"outcome\")",
				`str(fallback.get("source", "")) != "godot-example"`,
				`!= "Authoritative outcome: " + str(applied.get("outcome"))`,
				`!= "Local fallback %s: %s" % [`,
				"var previous_last_tick := _last_authoritative_tick",
				"_last_authoritative_tick = occurrence_tick",
				"_last_authoritative_tick = previous_last_tick",
				"_commit_authoritative_game_transaction(operation_id, occurrence_tick)",
				`str(retained.get("session_id", "")) != str(proposal.get("session_id", ""))`,
				`str(retained.get("id", "")) != proposal_id`,
				`str(retained.get("request_id", "")) != str(proposal.get("request_id", ""))`,
				`str(retained.get("actor_id", "")) != str(proposal.get("actor_id", ""))`,
				"response_tick != retained_tick",
				`str(retained_action.get("id", "")) != str(response_action.get("id", ""))`,
				`str(retained_action.get("kind", "")) != str(response_action.get("kind", ""))`,
				"response_revision_base != retained_revision_base",
				"retained_head_hash != response_head_hash",
				"response_created != retained_created",
				"response_world_base != retained_world_base",
				"_semantic_values_equal(retained_action, response_action)",
				"_semantic_values_equal(stable_action, response_action)",
				"== retained_world_base",
				"== retained_created",
				"func _apply_planned_game_effect(",
				"if not _authoritative_state_ready:",
			},
			forbidden: []string{
				"func apply_planned_game_effect(",
				"== int(proposal.get(\"created_revision\"",
			},
			persistCall:     "if not _persist_new_proposal_attempt(",
			sequencePublish: "_operation_sequence = next_sequence",
			tickPublish:     "_last_authoritative_tick = new_game_tick",
			submitCall:      "await rin.propose_with_fallback(",
		},
		{
			name: "unity",
			path: "../examples/unity/RinNpcExample.cs",
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
			name: "godot-game",
			path: "../examples/godot/example_npc.gd",
			required: []string{
				"const NPC_THINK_EVERY_TICKS := 5",
				"_build_commit_report_entry(",
				"_build_outcome_observe_request(",
				"_build_fallback_observe_request(",
				`"observer_ids": ["npc.mira"]`,
				"not _semantic_values_equal(entry, expected_entry)",
				"not _semantic_values_equal(applied, {",
				"not _semantic_values_equal(attempt, expected_attempt)",
				"and not _is_valid_protocol_id(attempt_job_id)",
				"occurrence_tick > MAX_PROTOCOL_INTEGER - NPC_THINK_EVERY_TICKS",
				`effective_planned["accepted"] = false`,
				"_apply_planned_game_effect(effective_planned)",
			},
			forbidden: []string{
				`str(attempt.get("job_id", ""))`,
				`"observer_ids": [proposal["actor_id"]]`,
			},
			ordered: []string{
				"var occurrence_tick := maxi(",
				"occurrence_tick > MAX_PROTOCOL_INTEGER - NPC_THINK_EVERY_TICKS",
				"_apply_planned_game_effect(effective_planned)",
			},
		},
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
			path: "../examples/unity/RinNpcExample.cs",
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

func TestModExamplesOptIntoOutcomeReporting(t *testing.T) {
	tests := map[string]string{
		"../examples/mods/bepinex-rin-npc/Plugin.cs":                                                 "outcome-reporting-v1",
		"../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java": "outcome-reporting-v1",
		"../examples/mods/luanti-rin-npc/init.lua":                                                   "outcome-reporting-v1",
		"../examples/basic/main.go":                                                                  "FeatureOutcomeReporting",
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

func TestManagedModExamplesPersistAndValidateProposalAttempts(t *testing.T) {
	tests := []struct {
		name                 string
		path                 string
		required             []string
		attemptPersistMarker string
		submitMarker         string
		jobPersistMarker     string
		getMarker            string
		repostStart          string
		repostEnd            string
		terminalStart        string
		terminalEnd          string
		terminalExclusion    string
		safeFallbackMarker   string
		fallbackCallMarker   string
		transactionStart     string
		transactionEnd       string
		appliedMarker        string
		outboxMarker         string
		effectMarker         string
		clearMarker          string
	}{
		{
			name: "bepinex",
			path: "../examples/mods/bepinex-rin-npc/Plugin.cs",
			required: []string{
				"ProposalAttempt? proposalAttempt",
				"RetainNewProposalAttempt",
				"attempt.ProposeRequest",
				"PersistProposalJobId(attempt, jobId)",
				"GetProposalJobAsync(jobId)",
				"ValidateJobIdentity(attempt, jobId, currentJob)",
				"ValidateProposalIdentity",
				"attempt.ProposeTick",
				"var occurrenceTick = Math.Max(",
				"InvalidateSessionIfNotFound",
				"proposalAttempt = null",
				"string.IsNullOrWhiteSpace(proposalId)",
			},
			attemptPersistMarker: "proposalAttempt = retained;",
			submitMarker:         "rin.SubmitProposalJobAsync(",
			jobPersistMarker:     "PersistProposalJobId(attempt, jobId);",
			getMarker:            "rin.GetProposalJobAsync(jobId)",
			repostStart:          "private static bool ShouldRepostProposal",
			repostEnd:            "private static bool IsConfirmedSafeTerminal",
			terminalStart:        "private static bool IsConfirmedSafeTerminal",
			terminalEnd:          "private static RinApiException UnknownProposalOutcome",
			terminalExclusion:    "!AmbiguousProposalErrors.Contains(exception.Code)",
			safeFallbackMarker:   "when (IsConfirmedSafeTerminal(exception))",
			fallbackCallMarker:   "ProposalResolution.AuthoredFallback(exception.Code)",
			transactionStart:     "private bool PersistAuthoritativeTransaction(",
			transactionEnd:       "private bool PersistOutboxConversion(",
			appliedMarker:        "appliedOperations[operationId] = result;",
			outboxMarker:         "outcomeOutbox[operationId] = pending;",
			effectMarker:         "applyGameState();",
			clearMarker:          "proposalAttempt = null",
		},
		{
			name: "fabric",
			path: "../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java",
			required: []string{
				"ProposalAttempt proposalAttempt",
				"retainNewProposalAttempt",
				"attempt.proposeRequest",
				"persistProposalJobId(registration, attempt, jobId)",
				"getProposalJob(jobId)",
				"validateJobIdentity(attempt, jobId, currentJob)",
				"validateProposalIdentity",
				"attempt.proposeTick",
				"long occurrenceTick = Math.max(",
				"invalidateSessionIfNotFound",
				"registration.proposalAttempt = null",
			},
			attemptPersistMarker: "registration.proposalAttempt = retained;",
			submitMarker:         "rin.submitProposalJob(attempt.proposeRequest)",
			jobPersistMarker:     "persistProposalJobId(registration, attempt, jobId);",
			getMarker:            "rin.getProposalJob(jobId)",
			repostStart:          "private static boolean shouldRepostProposal",
			repostEnd:            "private static boolean isConfirmedSafeTerminal",
			terminalStart:        "private static boolean isConfirmedSafeTerminal",
			terminalEnd:          "private static RinApiException unknownProposalOutcome",
			terminalExclusion:    "!AMBIGUOUS_PROPOSAL_ERRORS.contains(apiError.code())",
			safeFallbackMarker:   "if (isConfirmedSafeTerminal(cause))",
			fallbackCallMarker:   "ProposalResolution.authoredFallback(",
			transactionStart:     "private boolean persistAuthoritativeTransaction(",
			transactionEnd:       "private CompletableFuture<Void> flushOutcomeOutbox(",
			appliedMarker:        "appliedOperations.put(operationId, result);",
			outboxMarker:         "outcomeOutbox.put(operationId, pending);",
			effectMarker:         "applyGameState.run();",
			clearMarker:          "registration.proposalAttempt = null",
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
					t.Errorf(
						"%s is missing retained Proposal contract %q",
						test.path,
						required,
					)
				}
			}

			assertBefore := func(earlier, later, contract string) {
				t.Helper()
				earlierAt := strings.Index(text, earlier)
				laterAt := strings.Index(text, later)
				if earlierAt < 0 || laterAt < 0 || earlierAt >= laterAt {
					t.Errorf("%s does not preserve %s", test.path, contract)
				}
			}
			section := func(start, end string) string {
				t.Helper()
				startAt := strings.Index(text, start)
				if startAt < 0 {
					t.Errorf("%s is missing section start %q", test.path, start)
					return ""
				}
				endAt := strings.Index(text[startAt+len(start):], end)
				if endAt < 0 {
					t.Errorf("%s is missing section end %q", test.path, end)
					return ""
				}
				return text[startAt : startAt+len(start)+endAt]
			}

			assertBefore(
				test.attemptPersistMarker,
				test.submitMarker,
				"durable Proposal Attempt before its first POST",
			)
			assertBefore(
				test.jobPersistMarker,
				test.getMarker,
				"durable Job ID before its first GET",
			)

			repostSection := section(test.repostStart, test.repostEnd)
			if !strings.Contains(repostSection, `"proposal_outcome_unknown"`) {
				t.Errorf("%s does not route proposal_outcome_unknown through same-request recovery", test.path)
			}
			terminalSection := section(test.terminalStart, test.terminalEnd)
			if !strings.Contains(terminalSection, test.terminalExclusion) {
				t.Errorf("%s can reinterpret an ambiguous Proposal result as fallback-safe", test.path)
			}
			if !strings.Contains(text, test.safeFallbackMarker) {
				t.Errorf("%s does not gate authored fallback on a confirmed safe terminal result", test.path)
			}
			if count := strings.Count(text, test.fallbackCallMarker); count != 1 {
				t.Errorf(
					"%s has %d authored-fallback call sites; want one guarded terminal path",
					test.path,
					count,
				)
			}

			transactionSection := section(test.transactionStart, test.transactionEnd)
			if !strings.Contains(transactionSection, test.clearMarker) {
				t.Errorf("%s clears its Proposal Attempt outside the authoritative transaction", test.path)
			}
			transactionIndex := func(marker string) int {
				t.Helper()
				position := strings.Index(transactionSection, marker)
				if position < 0 {
					t.Errorf("%s authoritative transaction is missing %q", test.path, marker)
				}
				return position
			}
			appliedAt := transactionIndex(test.appliedMarker)
			outboxAt := transactionIndex(test.outboxMarker)
			effectAt := transactionIndex(test.effectMarker)
			clearAt := transactionIndex(test.clearMarker)
			if appliedAt < 0 || outboxAt < appliedAt || effectAt < outboxAt || clearAt < effectAt {
				t.Errorf(
					"%s does not atomically stage marker/outbox before effect and clear the attempt afterward",
					test.path,
				)
			}
		})
	}

	t.Run("fabric-player-left-fallback-is-rejected", func(t *testing.T) {
		const path = "../examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java"
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		start := strings.Index(text, "private CompletableFuture<Void> applyAuthoredFallbackTransaction(")
		end := strings.Index(text, "private PendingOutcome commitPending(")
		if start < 0 || end <= start {
			t.Fatal("Fabric fallback transaction section is missing")
		}
		fallback := text[start:end]
		for _, required := range []string{
			"if (player == null)",
			"new AppliedAction(",
			"false,",
			"The player left before the authored fallback could be applied.",
		} {
			if !strings.Contains(fallback, required) {
				t.Errorf("%s can report an accepted authored fallback without a player/effect: missing %q", path, required)
			}
		}
		if strings.Contains(fallback, "AppliedAction applied = new AppliedAction(true, line);") {
			t.Errorf("%s unconditionally accepts an authored fallback before checking its effect target", path)
		}
	})
}

func TestLuantiExampleResumesDurableProposalAttempts(t *testing.T) {
	const path = "../examples/mods/luanti-rin-npc/init.lua"
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	required := []string{
		"local proposal_attempts = {}",
		"persist_new_proposal_attempt",
		"persist_proposal_job_id",
		"resume_proposal_attempt",
		"submit_proposal_attempt(name, attempt, false)",
		`code == "proposal_outcome_unknown"`,
		`confirm_error.code) == "job_not_found"`,
		"proposal_attempts[resolved_attempt.name] = nil",
		"proposal_attempts[resolved_attempt.name] = resolved_attempt",
		"sequence = 0,",
		"local operation_id = session_id .. \".\" .. turn",
		"((entry and entry.sequence or 0) + 1)",
		"entry.sequence = math.max(entry.sequence, turn)",
		"mark_session_missing",
		"proposal_job_matches_attempt",
		"proposal_matches_attempt",
		"math.max(game_tick(), attempt.request.tick, proposal.tick)",
	}
	for _, fragment := range required {
		if !strings.Contains(text, fragment) {
			t.Errorf("%s is missing durable Proposal-attempt contract %q", path, fragment)
		}
	}
	persistAt := strings.Index(text, "if not persist_new_proposal_attempt(name, attempt)")
	submitAt := strings.LastIndex(text, "submit_proposal_attempt(name, attempt, true)")
	if persistAt < 0 || submitAt < 0 || persistAt >= submitAt {
		t.Errorf("%s must persist the complete attempt before its first POST", path)
	}
	sequenceAt := strings.Index(text, "entry.sequence = math.max(entry.sequence, turn)")
	if sequenceAt < 0 || persistAt >= sequenceAt || sequenceAt >= submitAt {
		t.Errorf("%s must persist the attempt before consuming its per-session sequence", path)
	}
	if count := strings.Count(text, "mark_session_missing("); count < 8 {
		t.Errorf("%s marks session_not_found in only %d paths; want at least 8", path, count)
	}
	if strings.Contains(text, "client:submit_proposal_job({") {
		t.Errorf("%s submits an ephemeral request instead of the retained attempt", path)
	}
	if strings.Contains(text, "local sequence =") {
		t.Errorf("%s regressed to a collision-prone global turn sequence", path)
	}
}
