extends SceneTree

const WorkflowScript = preload("res://rin_workflow.gd")
const SLOT := "test-workflow"
const PATH := "user://rin/" + SLOT + ".json"


class FakeClient:
	extends RefCounted

	var known_jobs: Array[String] = []
	var commits := 0
	var observes := 0

	func observe(_request: Dictionary) -> Dictionary:
		observes += 1
		return {"ok": true, "data": {}}

	func propose_with_fallback(
		request: Dictionary,
		_fallback_action_id: String,
		_cancel_check: Callable,
		known_job_id: String,
		persist_job_id: Callable,
		_allow_offline_before_submit: bool,
	) -> Dictionary:
		known_jobs.append(known_job_id)
		var job_id := known_job_id
		if job_id.is_empty():
			job_id = "job.fixture"
			if not persist_job_id.call(job_id):
				return {"fallback_reason": "job_id_persistence_failed"}
		return {
			"source": "sidecar",
			"committable": true,
			"job_id": job_id,
			"proposal": {
				"id": "proposal.fixture",
				"session_id": request["session_id"],
				"request_id": request["request_id"],
				"actor_id": request["actor_id"],
				"tick": request["tick"],
				"based_on_revision": 2,
				"based_on_head_hash": "fixture-head",
				"based_on_world_revision": 0,
				"created_revision": 2,
				"action": {"id": "talk", "kind": "dialogue"},
			},
		}

	func commit(_request: Dictionary) -> Dictionary:
		commits += 1
		return {"ok": false, "error_code": "unknown_proposal"}


func _initialize() -> void:
	DirAccess.remove_absolute(PATH)
	DirAccess.remove_absolute(PATH + ".tmp")
	DirAccess.remove_absolute(PATH + ".bak")
	var client := FakeClient.new()
	var workflow := WorkflowScript.new()
	_check(workflow.open(client, SLOT, _create_request), "fresh state did not open")
	var stable_session := workflow.session_id()
	_check(DirAccess.rename_absolute(PATH, PATH + ".tmp") == OK,
		"could not stage interrupted initialization")
	var recovered_initialization := WorkflowScript.new()
	_check(recovered_initialization.open(client, SLOT, _create_request),
		"interrupted initialization did not recover")
	_check(recovered_initialization.session_id() == stable_session,
		"interrupted initialization minted a new identity")
	_check(DirAccess.rename_absolute(PATH, PATH + ".bak") == OK,
		"could not stage interrupted replacement")
	var recovered_backup := WorkflowScript.new()
	_check(recovered_backup.open(client, SLOT, _create_request),
		"interrupted replacement did not restore backup")
	_check(recovered_backup.session_id() == stable_session,
		"backup recovery minted a new identity")
	workflow = recovered_backup
	var operation_id := workflow.next_operation_id()
	var tick := workflow.next_tick()
	var observe := {
		"session_id": stable_session,
		"request_id": "observe." + operation_id,
		"event_id": "event." + operation_id,
		"tick": tick,
	}
	var propose := {
		"session_id": stable_session,
		"request_id": "propose." + operation_id,
		"actor_id": "actor.fixture",
		"tick": tick + 1,
	}
	var attempt := workflow.begin(propose, observe, "wait")
	_check(not attempt.is_empty(), "Pending Turn was not persisted")
	var result: Dictionary = await workflow.resume()
	_check(result.get("committable", false), "Proposal did not resolve")
	_check(workflow.current_attempt()["job_id"] == "job.fixture", "Job ID was not persisted")

	var restarted := WorkflowScript.new()
	_check(restarted.open(client, SLOT, _create_request), "restart state did not open")
	_check(restarted.session_id() == stable_session, "Session identity changed after restart")
	var resumed: Dictionary = await restarted.resume()
	_check(resumed.get("committable", false), "retained Job did not resume")
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
	mismatched["action"] = {"id": "wait", "kind": "wait"}
	_check(WorkflowScript.proposal_freshness({
		"revision": 2,
		"world_revision": 0,
		"proposals": {"proposal.fixture": mismatched},
	}, proposal) == "stale", "mismatched retained Proposal was accepted")
	var current := restarted.current_attempt()
	var fallback := {
		"session_id": stable_session,
		"request_id": "fallback." + operation_id,
		"event_id": "outcome." + operation_id,
		"tick": tick + 2,
	}
	var outcome := {
		"key": operation_id,
		"kind": "commit",
		"request": {
			"session_id": stable_session,
			"request_id": "commit." + operation_id,
			"proposal_id": "proposal.fixture",
			"event_id": "outcome." + operation_id,
			"tick": tick + 2,
			"accepted": true,
		},
		"fallback_observe": fallback,
	}
	var applied := {"count": 0}
	_check(restarted.complete(current, outcome, func(_key: String) -> bool:
		applied["count"] += 1
		return true
	), "settlement did not persist")
	_check(applied["count"] == 1, "game apply did not run exactly once in-process")

	var with_outbox := WorkflowScript.new()
	_check(with_outbox.open(client, SLOT, _create_request), "outbox restart did not open")
	_check(await with_outbox.drain_outbox(), "terminal fallback did not drain")
	_check(client.commits == 1 and client.observes >= 3, "fallback report path was incomplete")
	var after_ack := WorkflowScript.new()
	_check(after_ack.open(client, SLOT, _create_request), "ack restart did not open")
	_check(await after_ack.drain_outbox(), "acknowledged Outbox reappeared")
	_check(client.commits == 1, "acknowledged Commit was retried")

	var before_failed_begin := after_ack.next_operation_id()
	var original_path: String = after_ack._path
	after_ack._path = PATH + "/blocked/state.json"
	var failed_tick := after_ack.next_tick()
	var failed := after_ack.begin(
		{
			"session_id": stable_session,
			"request_id": "propose." + before_failed_begin,
			"actor_id": "actor.fixture",
			"tick": failed_tick + 1,
		},
		{
			"session_id": stable_session,
			"request_id": "observe." + before_failed_begin,
			"event_id": "event." + before_failed_begin,
			"tick": failed_tick,
		},
		"wait",
	)
	_check(failed.is_empty(), "failed file write published Pending Turn")
	_check(after_ack.next_operation_id() == before_failed_begin, "failed write consumed sequence")
	after_ack._path = original_path

	var file := FileAccess.open(PATH, FileAccess.WRITE)
	_check(file != null, "could not create malformed fixture")
	file.store_string("{\"version\":999}")
	file.close()
	var malformed := WorkflowScript.new()
	_check(not malformed.open(client, SLOT, _create_request), "malformed state was accepted")
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


func _check(condition: bool, message: String) -> void:
	assert(condition, message)
