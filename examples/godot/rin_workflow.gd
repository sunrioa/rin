class_name RinWorkflow
extends RefCounted

const PROTOCOL_VERSION := "rin.protocol/v2"
const HostContract = preload("res://rin_host_contract.gd")
const MAX_BYTES := 1024 * 1024
const MAX_OUTCOMES := 64
const MAX_SAFE_INTEGER := 9007199254740991
static var _slot_owners: Dictionary = {}
var _client: Variant
var _path := ""
var _state: Dictionary = {}
var _new_operation := ""
var _busy := false
var _cancel_requested := false
var last_error := ""


func open(
	client: Variant,
	slot: String,
	create_factory: Callable,
	world_id: String,
) -> bool:
	if (
		client == null
		or not create_factory.is_valid()
		or not _safe_slot(slot)
		or not HostContract.valid_id(world_id)
	):
		return _fail("invalid_workflow_options")
	_client = client
	_path = "user://rin/%s.json" % slot
	if FileAccess.file_exists(_path):
		var loaded := _load_file(_path)
		if loaded.is_empty() or not _valid_state(loaded):
			return _fail("invalid_workflow_state")
		DirAccess.remove_absolute(_path + ".tmp")
		DirAccess.remove_absolute(_path + ".bak")
		_state = loaded
		return _open_authority(create_factory, world_id)
	var temporary := _path + ".tmp"
	var backup := _path + ".bak"
	if FileAccess.file_exists(backup):
		var recovery_path := temporary if FileAccess.file_exists(temporary) else backup
		var recovered := _load_file(recovery_path)
		if recovered.is_empty() or not _valid_state(recovered):
			return _fail("invalid_workflow_state")
		if DirAccess.rename_absolute(recovery_path, _path) != OK:
			return _fail("workflow_replace_failed")
		DirAccess.remove_absolute(backup)
		DirAccess.remove_absolute(temporary)
		_state = recovered
		return _open_authority(create_factory, world_id)
	if FileAccess.file_exists(temporary):
		var interrupted := _load_file(temporary)
		if interrupted.is_empty() or not _valid_state(interrupted):
			return _fail("invalid_workflow_state")
		if DirAccess.rename_absolute(temporary, _path) != OK:
			return _fail("workflow_replace_failed")
		_state = interrupted
		return _open_authority(create_factory, world_id)
	var run_id := _new_run_id()
	var session_id := "godot." + run_id
	var create_request = create_factory.call(session_id, _seed_from_run_id(run_id))
	if not create_request is Dictionary or not _valid_id(create_request.get("request_id")):
		return _fail("invalid_create_request")
	if str(create_request.get("session_id", "")) != session_id:
		return _fail("invalid_create_request")
	var initialized := {
		"version": 2,
		"run_id": run_id,
		"world_id": world_id,
		"sequence": 0,
		"last_tick": 0,
		"host_epoch": 0,
		"world_epoch": 1,
		"timeline_epoch": 0,
		"create_request": create_request.duplicate(true),
		"attempt": null,
		"active_run": null,
		"outcomes": [],
	}
	if not _claim_slot():
		return false
	if not _persist(initialized):
		_release_slot()
		return false
	if not _begin_authority_lifetime():
		_release_slot()
		return false
	return true


func create_request() -> Dictionary:
	return _state.get("create_request", {}).duplicate(true)


func session_id() -> String:
	return str(_state.get("create_request", {}).get("session_id", ""))


func epoch() -> Dictionary:
	return {
		"session_id": session_id(),
		"world_id": str(_state.get("world_id", "")),
		"host": int(_state.get("host_epoch", 0)),
		"world": int(_state.get("world_epoch", 0)),
		"timeline": int(_state.get("timeline_epoch", 0)),
	}


func advance_epoch(timeline_changed: bool) -> bool:
	if _busy:
		return _fail("workflow_busy")
	var candidate := _state.duplicate(true)
	if (
		int(candidate.get("world_epoch", 0)) >= MAX_SAFE_INTEGER
		or (
			timeline_changed
			and int(candidate.get("timeline_epoch", 0)) >= MAX_SAFE_INTEGER
		)
	):
		return _fail("epoch_exhausted")
	candidate["world_epoch"] = int(candidate["world_epoch"]) + 1
	if timeline_changed:
		candidate["timeline_epoch"] = int(candidate["timeline_epoch"]) + 1
	return _persist(candidate)


func has_attempt() -> bool:
	return _state.get("attempt") is Dictionary


func current_attempt() -> Dictionary:
	var attempt = _state.get("attempt")
	return attempt.duplicate(true) if attempt is Dictionary else {}


func next_operation_id() -> String:
	if has_attempt() or int(_state.get("sequence", 0)) >= MAX_SAFE_INTEGER:
		return ""
	return "%s.%d" % [str(_state.get("run_id", "")), int(_state["sequence"]) + 1]


func next_tick() -> int:
	var current := int(_state.get("last_tick", 0))
	if current >= MAX_SAFE_INTEGER:
		return -1
	return maxi(Time.get_ticks_msec(), current + 1)


func begin(propose: Dictionary, observe: Dictionary) -> Dictionary:
	if _busy or has_attempt():
		_fail("workflow_busy")
		return {}
	var sequence := int(_state.get("sequence", 0)) + 1
	if sequence > MAX_SAFE_INTEGER:
		_fail("sequence_exhausted")
		return {}
	var operation_id := "%s.%d" % [str(_state["run_id"]), sequence]
	var expected_session := session_id()
	if (
		not _valid_id(operation_id)
		or str(propose.get("session_id", "")) != expected_session
		or str(propose.get("request_id", "")) != "propose." + operation_id
		or not _valid_id(propose.get("actor_id"))
		or not _safe_integer(propose.get("tick"))
		or str(observe.get("session_id", "")) != expected_session
		or str(observe.get("request_id", "")) != "observe." + operation_id
		or str(observe.get("event_id", "")) != "event." + operation_id
		or not _safe_integer(observe.get("tick"))
		or int(observe["tick"]) <= int(_state["last_tick"])
		or not HostContract.valid_turn(propose, observe, expected_session)
		or not HostContract.semantic_equal(
			propose.get("decision_window", {}).get("epoch"),
			epoch(),
		)
	):
		_fail("workflow_identity_mismatch")
		return {}
	var attempt := {
		"version": 1,
		"operation_id": operation_id,
		"sequence": sequence,
		"request": propose.duplicate(true),
		"observe": observe.duplicate(true),
		"job_id": "",
	}
	var candidate := _state.duplicate(true)
	candidate["sequence"] = sequence
	candidate["last_tick"] = int(observe["tick"])
	candidate["attempt"] = attempt
	if not _persist(candidate):
		return {}
	_new_operation = operation_id
	return attempt.duplicate(true)


func resume() -> Dictionary:
	if _busy or not has_attempt():
		return _error("workflow_busy" if _busy else "attempt_missing")
	_busy = true
	_cancel_requested = false
	var attempt: Dictionary = _state["attempt"].duplicate(true)
	var observed: Dictionary = await _client.observe(attempt["observe"].duplicate(true))
	if not observed.get("ok", false):
		_busy = false
		return _error(str(observed.get("error_code", "observe_failed")))
	var operation_id := str(attempt["operation_id"])
	var save_job := func(job_id: String) -> bool:
		return _save_job(operation_id, job_id)
	var result: Dictionary = await _client.propose(
		attempt["request"].duplicate(true),
		_is_cancelled,
		str(attempt["job_id"]),
		save_job,
	)
	_new_operation = ""
	_busy = false
	if result.get("proposal") is Dictionary:
		var proposal: Dictionary = result["proposal"]
		var offered := HostContract.resolve_offer(attempt["request"], proposal)
		if offered.is_empty():
			last_error = "proposal_binding_mismatch"
			return _error(last_error)
		proposal = proposal.duplicate(true)
		proposal["action"] = offered
		proposal["decision_window"] = attempt["request"]["decision_window"].duplicate(true)
		result["proposal"] = proposal
	else:
		last_error = str(result.get("error_code", "proposal_unresolved"))
	return result


func shutdown() -> void:
	_cancel_requested = true
	_release_slot()


func _is_cancelled() -> bool:
	return _cancel_requested


func complete(
	attempt: Dictionary,
	proposal: Dictionary,
	outcome: Dictionary,
	apply: Callable,
) -> bool:
	if _busy or not apply.is_valid() or not _matching_attempt(_state.get("attempt"), attempt):
		return _fail("invalid_settlement")
	if _state.get("outcomes", []).size() >= MAX_OUTCOMES:
		return _fail("outcome_outbox_full")
	if (
		HostContract.resolve_offer(attempt.get("request"), proposal).is_empty()
		or not _valid_outcome(outcome)
		or str(outcome.get("key", "")) != str(attempt["operation_id"])
		or str(outcome.get("request", {}).get("report", {}).get("proposal_id", ""))
			!= str(proposal.get("id", ""))
		or not HostContract.outcome_matches_proposal(
			outcome,
			proposal,
			str(attempt["operation_id"]),
		)
	):
		return _fail("invalid_settlement")
	if str(outcome["request"].get("session_id", "")) != session_id():
		return _fail("invalid_settlement")
	_busy = true
	var report: Dictionary = outcome["request"]["report"]
	if str(report.get("decision", "")) == "accepted":
		var running := _state.duplicate(true)
		running["active_run"] = {
			"operation_id": str(attempt["operation_id"]),
			"proposal": proposal.duplicate(true),
			"recovery_outcome": HostContract.interrupted_outcome(outcome),
		}
		if not _persist(running):
			_busy = false
			return false
	var applied = apply.call(str(attempt["operation_id"]))
	if applied == false:
		_busy = false
		return _fail("game_apply_failed")
	var candidate := _state.duplicate(true)
	var outcomes: Array = candidate["outcomes"]
	outcomes.append(outcome.duplicate(true))
	candidate["attempt"] = null
	candidate["active_run"] = null
	candidate["last_tick"] = maxi(
		int(candidate["last_tick"]),
		int(outcome["request"].get("tick", 0)),
	)
	var saved := _persist(candidate)
	_busy = false
	if not saved:
		return false
	return true


func drain_outbox() -> bool:
	if _busy:
		return _fail("workflow_busy")
	_busy = true
	while not _state["outcomes"].is_empty():
		var entry: Dictionary = _state["outcomes"][0].duplicate(true)
		var response: Dictionary = await _client.report_action(entry["request"].duplicate(true))
		if not response.get("ok", false):
			_busy = false
			return _fail(str(response.get("error_code", "report_failed")))
		var result = response.get("data")
		if (
			not result is Dictionary
			or str(result.get("session_id", ""))
				!= str(entry["request"].get("session_id", ""))
		):
			_busy = false
			return _fail("invalid_outbox_ack")
		var acknowledged := _state.duplicate(true)
		acknowledged["outcomes"].remove_at(0)
		if not _persist(acknowledged):
			_busy = false
			return false
	_busy = false
	return true


static func proposal_freshness(state: Dictionary, proposal: Dictionary) -> String:
	return HostContract.proposal_freshness(state, proposal)


func _open_authority(create_factory: Callable, world_id: String) -> bool:
	if str(_state.get("world_id", "")) != world_id:
		return _fail("workflow_binding_mismatch")
	var expected = create_factory.call(
		session_id(),
		_seed_from_run_id(str(_state.get("run_id", ""))),
	)
	if (
		not expected is Dictionary
		or not HostContract.semantic_equal(expected, _state.get("create_request"))
	):
		return _fail("workflow_binding_mismatch")
	if not _claim_slot():
		return false
	if not _begin_authority_lifetime():
		_release_slot()
		return false
	return true


func _claim_slot() -> bool:
	var retained = _slot_owners.get(_path)
	if retained is WeakRef and retained.get_ref() != null:
		return _fail("workflow_slot_in_use")
	_slot_owners[_path] = weakref(self)
	return true


func _release_slot() -> void:
	var retained = _slot_owners.get(_path)
	if retained is WeakRef and retained.get_ref() == self:
		_slot_owners.erase(_path)


func _begin_authority_lifetime() -> bool:
	if (
		int(_state.get("host_epoch", 0)) >= MAX_SAFE_INTEGER
		or int(_state.get("timeline_epoch", 0)) >= MAX_SAFE_INTEGER
	):
		return _fail("epoch_exhausted")
	var candidate := _state.duplicate(true)
	candidate["host_epoch"] = int(candidate["host_epoch"]) + 1
	candidate["timeline_epoch"] = int(candidate["timeline_epoch"]) + 1
	var active = candidate.get("active_run")
	if active is Dictionary:
		if candidate["outcomes"].size() >= MAX_OUTCOMES:
			return _fail("outcome_outbox_full")
		candidate["outcomes"].append(active["recovery_outcome"].duplicate(true))
		candidate["attempt"] = null
		candidate["active_run"] = null
	return _persist(candidate)


func _save_job(operation_id: String, job_id: String) -> bool:
	if not _valid_id(job_id):
		return _fail("invalid_job")
	var current = _state.get("attempt")
	if not current is Dictionary or str(current.get("operation_id", "")) != operation_id:
		return _fail("attempt_changed")
	if str(current.get("job_id", "")) == job_id:
		return true
	var candidate := _state.duplicate(true)
	candidate["attempt"]["job_id"] = job_id
	return _persist(candidate)


func _persist(candidate: Dictionary) -> bool:
	if not _valid_state(candidate):
		return _fail("invalid_workflow_state")
	var encoded := JSON.stringify(candidate)
	if encoded.to_utf8_buffer().size() > MAX_BYTES:
		return _fail("workflow_state_too_large")
	var directory_error := DirAccess.make_dir_recursive_absolute("user://rin")
	if directory_error not in [OK, ERR_ALREADY_EXISTS]:
		return _fail("workflow_directory_failed")
	var temporary := _path + ".tmp"
	var file := FileAccess.open(temporary, FileAccess.WRITE)
	if file == null:
		return _fail("workflow_write_failed")
	file.store_string(encoded)
	file.flush()
	var file_error := file.get_error()
	file.close()
	if file_error != OK:
		return _fail("workflow_write_failed")
	var backup := _path + ".bak"
	if FileAccess.file_exists(backup):
		return _fail("workflow_backup_exists")
	if FileAccess.file_exists(_path):
		if DirAccess.rename_absolute(_path, backup) != OK:
			return _fail("workflow_backup_failed")
	if DirAccess.rename_absolute(temporary, _path) != OK:
		if FileAccess.file_exists(backup):
			DirAccess.rename_absolute(backup, _path)
		return _fail("workflow_replace_failed")
	DirAccess.remove_absolute(backup)
	_state = candidate
	last_error = ""
	return true


func _load_file(path: String) -> Dictionary:
	var file := FileAccess.open(path, FileAccess.READ)
	if file == null or file.get_length() > MAX_BYTES:
		return {}
	var text := file.get_as_text()
	file.close()
	var parsed = JSON.parse_string(text)
	return parsed if parsed is Dictionary else {}


func _valid_state(value: Variant) -> bool:
	if not value is Dictionary:
		return false
	var state: Dictionary = value
	if (
		int(state.get("version", 0)) != 2
		or not _valid_id(state.get("run_id"))
		or str(state["run_id"]).length() != 32
		or not HostContract.valid_id(state.get("world_id"))
		or not _safe_integer(state.get("sequence"))
		or not _safe_integer(state.get("last_tick"))
		or not _safe_integer(state.get("host_epoch"))
		or not _safe_integer(state.get("world_epoch"))
		or int(state["world_epoch"]) <= 0
		or not _safe_integer(state.get("timeline_epoch"))
		or not state.get("create_request") is Dictionary
		or not _valid_id(state["create_request"].get("session_id"))
		or not _valid_id(state["create_request"].get("request_id"))
		or not state.get("outcomes") is Array
		or state["outcomes"].size() > MAX_OUTCOMES
	):
		return false
	var attempt = state.get("attempt")
	if attempt != null and not _valid_attempt(attempt, state):
		return false
	var active = state.get("active_run")
	if active != null and not _valid_active_run(active, attempt, state):
		return false
	var outcome_keys := {}
	for entry in state["outcomes"]:
		if not _valid_outcome(
			entry,
			str(state["create_request"]["session_id"]),
		) or outcome_keys.has(str(entry.get("key", ""))):
			return false
		outcome_keys[str(entry["key"])] = true
		if str(entry["request"].get("session_id", "")) != str(state["create_request"]["session_id"]):
			return false
	if active is Dictionary and outcome_keys.has(str(active.get("operation_id", ""))):
		return false
	return true


func _valid_attempt(value: Variant, state: Dictionary) -> bool:
	if not value is Dictionary:
		return false
	var attempt: Dictionary = value
	return (
		int(attempt.get("version", 0)) == 1
		and _valid_id(attempt.get("operation_id"))
		and _safe_integer(attempt.get("sequence"))
		and int(attempt["sequence"]) == int(state["sequence"])
		and attempt.get("request") is Dictionary
		and str(attempt["request"].get("session_id", "")) == str(state["create_request"]["session_id"])
		and _valid_id(attempt["request"].get("request_id"))
		and _valid_id(attempt["request"].get("actor_id"))
		and _safe_integer(attempt["request"].get("tick"))
		and HostContract.valid_turn(
			attempt["request"],
			attempt["observe"],
			str(state["create_request"]["session_id"]),
		)
		and attempt.get("observe") is Dictionary
		and str(attempt["observe"].get("session_id", "")) == str(state["create_request"]["session_id"])
		and _valid_id(attempt["observe"].get("request_id"))
		and _valid_id(attempt["observe"].get("event_id"))
		and _safe_integer(attempt["observe"].get("tick"))
		and typeof(attempt.get("job_id")) == TYPE_STRING
		and (str(attempt["job_id"]).is_empty() or _valid_id(attempt["job_id"]))
	)


func _valid_outcome(value: Variant, expected_session: String = "") -> bool:
	return HostContract.valid_outcome(
		value,
		session_id() if expected_session.is_empty() else expected_session,
	)


func _valid_active_run(
	value: Variant,
	attempt: Variant,
	state: Dictionary,
) -> bool:
	if not value is Dictionary or not attempt is Dictionary:
		return false
	var active: Dictionary = value
	return (
		str(active.get("operation_id", "")) == str(attempt.get("operation_id", ""))
		and active.get("proposal") is Dictionary
		and not HostContract.resolve_offer(
			attempt.get("request"),
			active["proposal"],
		).is_empty()
		and HostContract.valid_outcome(
			active.get("recovery_outcome"),
			str(state["create_request"]["session_id"]),
			str(attempt["operation_id"]),
		)
		and HostContract.outcome_matches_proposal(
			active["recovery_outcome"],
			active["proposal"],
			str(attempt["operation_id"]),
		)
		and str(
			active.get("recovery_outcome", {})
				.get("request", {})
				.get("report", {})
				.get("outcome", {})
				.get("status", "")
		) == "outcome-unknown"
	)


func _matching_attempt(left: Variant, right: Variant) -> bool:
	return (
		left is Dictionary
		and right is Dictionary
		and str(left.get("operation_id", "")) == str(right.get("operation_id", ""))
		and str(left.get("job_id", "")) == str(right.get("job_id", ""))
		and str(left.get("request", {}).get("request_id", ""))
			== str(right.get("request", {}).get("request_id", ""))
	)


static func _valid_id(value: Variant) -> bool:
	if typeof(value) != TYPE_STRING:
		return false
	var text: String = value
	if text.is_empty() or text.length() > 96:
		return false
	var expression := RegEx.new()
	return expression.compile("^[A-Za-z0-9][A-Za-z0-9._-]*$") == OK and expression.search(text) != null


static func _safe_integer(value: Variant) -> bool:
	if typeof(value) != TYPE_INT and typeof(value) != TYPE_FLOAT:
		return false
	var number := float(value)
	return is_finite(number) and number >= 0.0 and number <= MAX_SAFE_INTEGER and floor(number) == number


static func _safe_slot(value: String) -> bool:
	if value.is_empty() or value.length() > 48:
		return false
	var expression := RegEx.new()
	return expression.compile("^[A-Za-z0-9][A-Za-z0-9._-]*$") == OK and expression.search(value) != null


static func _new_run_id() -> String:
	return Crypto.new().generate_random_bytes(16).hex_encode()


static func _seed_from_run_id(run_id: String) -> int:
	return run_id.substr(0, 12).hex_to_int()


func _error(code: String) -> Dictionary:
	last_error = code
	return {"ok": false, "error_code": code}


func _fail(code: String) -> bool:
	last_error = code
	return false
