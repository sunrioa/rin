extends SceneTree

const WorkflowScript = preload("res://rin_workflow.gd")
const ClientScript = preload("res://rin_client.gd")
const SLOT := "test-workflow"
const PATH := "user://rin/" + SLOT + ".json"
const WORLD_ID := "world.fixture"


class FakeClient:
	extends RefCounted

	var known_jobs: Array[String] = []
	var reports := 0
	var observes := 0
	var mutate_descriptor := false
	var report_session: Variant = "session.fixture"
	var report_requests: Array[Dictionary] = []

	func observe(_request: Dictionary) -> Dictionary:
		observes += 1
		return {"ok": true, "data": {}}

	func propose(
		request: Dictionary,
		_cancel_check: Callable,
		known_job_id: String,
		persist_job_id: Callable,
	) -> Dictionary:
		known_jobs.append(known_job_id)
		var job_id := known_job_id
		if job_id.is_empty():
			job_id = "job.fixture"
			if not persist_job_id.call(job_id):
				return {"error_code": "job_id_persistence_failed"}
		var proposal := {
			"id": "proposal.fixture",
			"session_id": request["session_id"],
			"request_id": request["request_id"],
			"actor_id": request["actor_id"],
			"tick": request["tick"],
			"decision_window": request["decision_window"].duplicate(true),
			"based_on_revision": 2,
			"based_on_head_hash": "fixture-head",
			"based_on_world_revision": 0,
			"created_revision": 2,
			"action": request["offers"][0].duplicate(true),
		}
		if mutate_descriptor:
			proposal["action"]["descriptor_digest"] = "b".repeat(64)
		return {
			"source": "sidecar",
			"job_id": job_id,
			"proposal": proposal,
		}

	func report_action(request: Dictionary) -> Dictionary:
		reports += 1
		report_requests.append(request.duplicate(true))
		return {"ok": true, "data": {"session_id": report_session}}


func _initialize() -> void:
	var epoch := {
		"session_id": "session.helper",
		"world_id": "world.helper",
		"host": 1,
		"world": 2,
		"timeline": 3,
	}
	var timepoint := {"clock": "event", "value": 8}
	var window := {
		"id": "window.helper",
		"epoch": epoch,
		"observation_seq": 7,
		"deadline": timepoint,
	}
	var offer := ClientScript.action_offer({
		"offer_id": "offer.helper",
		"actor_id": "actor.helper",
		"capability_id": "dialogue.say",
		"descriptor_digest": "a".repeat(64),
		"description": "Say one line",
		"arguments": {"line": "hello"},
	}, window)
	var report := ClientScript.immediate_action_report({
		"session_id": "session.helper",
		"request_id": "report.helper",
		"event_id": "event.helper",
		"tick": 8,
		"proposal": {"id": "proposal.helper", "action": offer},
		"operation_id": "operation.helper",
		"accepted": true,
		"summary": "applied",
		"epoch": epoch,
		"world_seq": 8,
		"occurred_at": timepoint,
	})
	_check(report["report"]["invocation"]["offer_id"] == "offer.helper",
		"host action helper changed the selected offer")
	_check(report["report"]["run"]["status"] == "succeeded",
		"host action helper omitted the terminal run")
	DirAccess.remove_absolute(PATH)
	DirAccess.remove_absolute(PATH + ".tmp")
	DirAccess.remove_absolute(PATH + ".bak")
	var client := FakeClient.new()
	var workflow := WorkflowScript.new()
	_check(
		workflow.open(client, SLOT, _create_request, WORLD_ID),
		"fresh state did not open: " + workflow.last_error,
	)
	var stable_session := workflow.session_id()
	var first_epoch := workflow.epoch()
	var duplicate_owner := WorkflowScript.new()
	_check(
		not duplicate_owner.open(client, SLOT, _create_request, WORLD_ID)
		and duplicate_owner.last_error == "workflow_slot_in_use",
		"two workflow instances acquired the same save slot",
	)
	workflow.shutdown()
	_check(DirAccess.rename_absolute(PATH, PATH + ".tmp") == OK,
		"could not stage interrupted initialization")
	var recovered_initialization := WorkflowScript.new()
	_check(recovered_initialization.open(client, SLOT, _create_request, WORLD_ID),
		"interrupted initialization did not recover")
	_check(recovered_initialization.session_id() == stable_session,
		"interrupted initialization minted a new identity")
	_check(
		int(recovered_initialization.epoch()["host"]) > int(first_epoch["host"]),
		"authority restart did not advance Host generation",
	)
	recovered_initialization.shutdown()
	var mismatched_binding := WorkflowScript.new()
	_check(
		not mismatched_binding.open(
			client,
			SLOT,
			_changed_create_request,
			WORLD_ID,
		)
		and mismatched_binding.last_error == "workflow_binding_mismatch",
		"changed content binding reused authoritative state",
	)
	_check(DirAccess.rename_absolute(PATH, PATH + ".bak") == OK,
		"could not stage interrupted replacement")
	var recovered_backup := WorkflowScript.new()
	_check(recovered_backup.open(client, SLOT, _create_request, WORLD_ID),
		"interrupted replacement did not restore backup")
	_check(recovered_backup.session_id() == stable_session,
		"backup recovery minted a new identity")
	workflow = recovered_backup
	var before_world_change := workflow.epoch()
	_check(workflow.advance_epoch(false), "world authority did not advance")
	_check(
		int(workflow.epoch()["world"]) == int(before_world_change["world"]) + 1
		and int(workflow.epoch()["timeline"]) == int(before_world_change["timeline"]),
		"world replacement changed the wrong Epoch generation",
	)
	var before_timeline_change := workflow.epoch()
	_check(workflow.advance_epoch(true), "timeline authority did not advance")
	_check(
		int(workflow.epoch()["world"]) == int(before_timeline_change["world"]) + 1
		and int(workflow.epoch()["timeline"]) == int(before_timeline_change["timeline"]) + 1,
		"timeline fork did not advance World and Timeline generations",
	)
	# Host scenario: stale_epoch_rejection.
	var stale_operation := workflow.next_operation_id()
	var stale_tick := workflow.next_tick()
	var stale_turn := _turn(workflow, stale_operation, stale_tick)
	_check(workflow.advance_epoch(false), "stale-offer fixture did not replace authority")
	_check(
		workflow.begin(stale_turn["propose"], stale_turn["observe"]).is_empty(),
		"offer from a replaced World generation was accepted",
	)
	var operation_id := workflow.next_operation_id()
	var tick := workflow.next_tick()
	var turn := _turn(workflow, operation_id, tick)
	var observe: Dictionary = turn["observe"]
	var propose: Dictionary = turn["propose"]
	var attempt := workflow.begin(propose, observe)
	_check(not attempt.is_empty(), "Pending Turn was not persisted")
	var result: Dictionary = await workflow.resume()
	_check(result.get("proposal") is Dictionary, "Proposal did not resolve")
	_check(workflow.current_attempt()["job_id"] == "job.fixture", "Job ID was not persisted")

	workflow.shutdown()
	var restarted := WorkflowScript.new()
	_check(restarted.open(client, SLOT, _create_request, WORLD_ID), "restart state did not open")
	_check(restarted.session_id() == stable_session, "Session identity changed after restart")
	var resumed: Dictionary = await restarted.resume()
	_check(resumed.get("proposal") is Dictionary, "retained Job did not resume")
	_check(client.known_jobs[-1] == "job.fixture", "restart resubmitted instead of using Job")
	var proposal: Dictionary = resumed["proposal"]
	var retained := proposal.duplicate(true)
	retained["status"] = "pending"
	_check(WorkflowScript.proposal_freshness({
		"revision": 2,
		"world_revision": 0,
		"proposals": {"proposal.fixture": retained},
	}, proposal) == "fresh", "matching Proposal was marked stale")
	var mismatched := retained.duplicate(true)
	mismatched["action"] = proposal["action"].duplicate(true)
	mismatched["action"]["descriptor_digest"] = "b".repeat(64)
	_check(WorkflowScript.proposal_freshness({
		"revision": 2,
		"world_revision": 0,
		"proposals": {"proposal.fixture": mismatched},
	}, proposal) == "stale", "mismatched retained Proposal was accepted")
	var current := restarted.current_attempt()
	var outcome := {
		"key": operation_id,
		"kind": "report",
		"request": {
			"session_id": stable_session,
			"request_id": "report." + operation_id,
			"tick": tick + 2,
			"report": {
				"proposal_id": "proposal.fixture",
				"event_id": "outcome." + operation_id,
				"decision": "rejected",
				"summary": "host rejected the offer",
			},
		},
	}
	var applied := {"count": 0}
	_check(restarted.complete(current, proposal, outcome, func(_key: String) -> bool:
		applied["count"] += 1
		return true
	), "settlement did not persist")
	_check(applied["count"] == 1, "game apply did not run exactly once in-process")

	restarted.shutdown()
	var with_outbox := WorkflowScript.new()
	_check(with_outbox.open(client, SLOT, _create_request, WORLD_ID), "outbox restart did not open")
	client.report_session = "session.other"
	_check(not await with_outbox.drain_outbox(), "wrong-Session ACK drained the Outbox")
	client.report_session = 7
	_check(not await with_outbox.drain_outbox(), "non-string Session ACK drained the Outbox")
	client.report_session = with_outbox.session_id()
	_check(await with_outbox.drain_outbox(), "report Outbox did not drain")
	_check(client.reports == 3 and client.observes >= 2, "report path was incomplete")
	with_outbox.shutdown()
	var after_ack := WorkflowScript.new()
	_check(after_ack.open(client, SLOT, _create_request, WORLD_ID), "ack restart did not open")
	_check(await after_ack.drain_outbox(), "acknowledged Outbox reappeared")
	_check(client.reports == 3, "acknowledged report was retried")

	var active_operation := after_ack.next_operation_id()
	var active_tick := after_ack.next_tick()
	var active_turn := _turn(after_ack, active_operation, active_tick)
	_check(
		not after_ack.begin(
			active_turn["propose"],
			active_turn["observe"],
		).is_empty(),
		"second Pending Turn was not persisted",
	)
	client.mutate_descriptor = true
	var rejected_binding: Dictionary = await after_ack.resume()
	_check(
		not rejected_binding.get("proposal") is Dictionary
		and after_ack.has_attempt(),
		"altered Proposal escaped exact Offer binding",
	)
	client.mutate_descriptor = false
	var active_result: Dictionary = await after_ack.resume()
	var active_proposal: Dictionary = active_result["proposal"]
	var accepted_outcome := {
		"key": active_operation,
		"kind": "report",
		"request": ClientScript.immediate_action_report({
			"session_id": stable_session,
			"request_id": "report." + active_operation,
			"event_id": "outcome." + active_operation,
			"tick": active_tick + 2,
			"proposal": active_proposal,
			"operation_id": active_operation,
			"accepted": true,
			"summary": "applied",
			"world_seq": active_tick + 2,
			"occurred_at": {"clock": "event", "value": active_tick + 2},
		}),
	}
	var altered_outcome: Dictionary = accepted_outcome.duplicate(true)
	altered_outcome["request"]["report"]["invocation"]["descriptor_digest"] = (
		"b".repeat(64)
	)
	var altered_applied := {"count": 0}
	var apply_altered := func(_key: String) -> bool:
		altered_applied["count"] += 1
		return true
	_check(
		not after_ack.complete(
			after_ack.current_attempt(),
			active_proposal,
			altered_outcome,
			apply_altered,
		)
		and altered_applied["count"] == 0,
		"altered Action Report invocation reached game code",
	)
	var active_path: String = after_ack._path
	var interrupt_terminal_write := func(_key: String) -> bool:
		after_ack._path = PATH + "/blocked/state.json"
		return true
	_check(
		not after_ack.complete(
			after_ack.current_attempt(),
			active_proposal,
			accepted_outcome,
			interrupt_terminal_write,
		),
		"failed terminal write did not retain an Active Run",
	)
	after_ack._path = active_path
	after_ack.shutdown()
	var recovered_active := WorkflowScript.new()
	_check(
		recovered_active.open(client, SLOT, _create_request, WORLD_ID),
		"Active Run restart recovery failed",
	)
	_check(
		not recovered_active.has_attempt(),
		"recovery_state_cleanup: Active Run recovery retained an executable Pending Turn",
	)
	_check(
		await recovered_active.drain_outbox(),
		"outcome-unknown recovery report did not drain",
	)
	_check(client.reports == 4, "Active Run recovery did not report exactly once")
	# Host scenario: long_action_epoch_cancel.
	var recovered_report: Dictionary = client.report_requests[-1]["report"]
	_check(
		recovered_report["outcome"]["status"] == "outcome-unknown"
		and recovered_report["run"]["status"] == "outcome-unknown"
		and recovered_report["outcome"]["epoch"]
			== recovered_report["invocation"]["expected_epoch"],
		"Active Run recovery lost its terminal status or execution Epoch",
	)

	var before_failed_begin := recovered_active.next_operation_id()
	var original_path: String = recovered_active._path
	recovered_active._path = PATH + "/blocked/state.json"
	var failed_tick := recovered_active.next_tick()
	var failed_turn := _turn(recovered_active, before_failed_begin, failed_tick)
	var failed := recovered_active.begin(failed_turn["propose"], failed_turn["observe"])
	_check(failed.is_empty(), "failed file write published Pending Turn")
	_check(
		recovered_active.next_operation_id() == before_failed_begin,
		"failed write consumed sequence",
	)
	recovered_active._path = original_path

	recovered_active.shutdown()
	var file := FileAccess.open(PATH, FileAccess.WRITE)
	_check(file != null, "could not create malformed fixture")
	file.store_string("{\"version\":999}")
	file.close()
	var malformed := WorkflowScript.new()
	_check(not malformed.open(client, SLOT, _create_request, WORLD_ID), "malformed state was accepted")
	DirAccess.remove_absolute(PATH)
	DirAccess.remove_absolute(PATH + ".tmp")
	DirAccess.remove_absolute(PATH + ".bak")
	print("Rin Godot workflow restart tests passed")
	quit(0)


func _create_request(session_id: String, seed: int) -> Dictionary:
	return {
		"request_id": "create." + session_id,
		"session_id": session_id,
		"seed": seed,
	}


func _changed_create_request(session_id: String, seed: int) -> Dictionary:
	var request := _create_request(session_id, seed)
	request["content_revision"] = "changed"
	return request


func _turn(
	workflow: Variant,
	operation_id: String,
	tick: int,
) -> Dictionary:
	var epoch: Dictionary = workflow.epoch()
	var window := {
		"id": "window." + operation_id,
		"mode": "sequential",
		"epoch": epoch,
		"observation_seq": tick,
		"opened_at": {"clock": "event", "value": tick + 1},
		"deadline": {"clock": "event", "value": tick + 2},
		"actor_ids": ["actor.fixture"],
	}
	var offer := ClientScript.action_offer({
		"offer_id": "offer.talk",
		"actor_id": "actor.fixture",
		"capability_id": "dialogue.talk",
		"descriptor_digest": "a".repeat(64),
		"description": "Say one authored line.",
		"arguments": {"line": "hello"},
	}, window)
	return {
		"observe": {
			"session_id": workflow.session_id(),
			"request_id": "observe." + operation_id,
			"event_id": "event." + operation_id,
			"tick": tick,
			"epoch": epoch,
			"observation_seq": tick,
		},
		"propose": {
			"session_id": workflow.session_id(),
			"request_id": "propose." + operation_id,
			"actor_id": "actor.fixture",
			"tick": tick + 1,
			"decision_window": window,
			"offers": [offer],
		},
	}


func _check(condition: bool, message: String) -> void:
	assert(condition, message)
