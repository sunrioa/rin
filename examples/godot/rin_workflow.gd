class_name RinWorkflow
extends RefCounted

const PROTOCOL_VERSION := "rin.protocol/v1"
const MAX_BYTES := 1024 * 1024
const MAX_OUTCOMES := 64
const MAX_SAFE_INTEGER := 9007199254740991
const TERMINAL_COMMIT_ERRORS := {
	"session_not_found": true,
	"unknown_proposal": true,
	"proposal_resolved": true,
	"proposal_canceled": true,
	"proposal_stale": true,
}

var _client: Variant
var _path := ""
var _state: Dictionary = {}
var _new_operation := ""
var _busy := false
var _cancel_requested := false
var last_error := ""


func open(client: Variant, slot: String, create_factory: Callable) -> bool:
	if client == null or not create_factory.is_valid() or not _safe_slot(slot):
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
		return true
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
		return true
	if FileAccess.file_exists(temporary):
		var interrupted := _load_file(temporary)
		if interrupted.is_empty() or not _valid_state(interrupted):
			return _fail("invalid_workflow_state")
		if DirAccess.rename_absolute(temporary, _path) != OK:
			return _fail("workflow_replace_failed")
		_state = interrupted
		return true
	var run_id := _new_run_id()
	var session_id := "godot." + run_id
	var create_request = create_factory.call(session_id, _seed_from_run_id(run_id))
	if not create_request is Dictionary or not _valid_id(create_request.get("request_id")):
		return _fail("invalid_create_request")
	if str(create_request.get("session_id", "")) != session_id:
		return _fail("invalid_create_request")
	var initialized := {
		"version": 1,
		"run_id": run_id,
		"sequence": 0,
		"last_tick": 0,
		"create_request": create_request.duplicate(true),
		"attempt": null,
		"outcomes": [],
	}
	if not _persist(initialized):
		return false
	return true


func create_request() -> Dictionary:
	return _state.get("create_request", {}).duplicate(true)


func session_id() -> String:
	return str(_state.get("create_request", {}).get("session_id", ""))


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


func begin(propose: Dictionary, observe: Dictionary, fallback_action_id: String) -> Dictionary:
	if _busy or has_attempt() or not _valid_id(fallback_action_id):
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
	):
		_fail("workflow_identity_mismatch")
		return {}
	var attempt := {
		"version": 1,
		"operation_id": operation_id,
		"sequence": sequence,
		"request": propose.duplicate(true),
		"observe": observe.duplicate(true),
		"fallback_action_id": fallback_action_id,
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
	var result: Dictionary = await _client.propose_with_fallback(
		attempt["request"].duplicate(true),
		str(attempt["fallback_action_id"]),
		_is_cancelled,
		str(attempt["job_id"]),
		save_job,
		_new_operation == operation_id,
	)
	_new_operation = ""
	_busy = false
	if not result.get("proposal") is Dictionary:
		last_error = str(result.get("fallback_reason", "proposal_unresolved"))
	return result


func shutdown() -> void:
	_cancel_requested = true


func _is_cancelled() -> bool:
	return _cancel_requested


func complete(attempt: Dictionary, outcome: Dictionary, apply: Callable) -> bool:
	if _busy or not apply.is_valid() or not _matching_attempt(_state.get("attempt"), attempt):
		return _fail("invalid_settlement")
	if _state.get("outcomes", []).size() >= MAX_OUTCOMES:
		return _fail("outcome_outbox_full")
	if not _valid_outcome(outcome) or str(outcome.get("key", "")) != str(attempt["operation_id"]):
		return _fail("invalid_settlement")
	if str(outcome["request"].get("session_id", "")) != session_id():
		return _fail("invalid_settlement")
	_busy = true
	var applied = apply.call(str(attempt["operation_id"]))
	if applied == false:
		_busy = false
		return _fail("game_apply_failed")
	var candidate := _state.duplicate(true)
	var outcomes: Array = candidate["outcomes"]
	outcomes.append(outcome.duplicate(true))
	candidate["attempt"] = null
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
		var response: Dictionary
		if entry["kind"] == "commit":
			response = await _client.commit(entry["request"].duplicate(true))
			if not response.get("ok", false):
				var code := str(response.get("error_code", "commit_failed"))
				if not TERMINAL_COMMIT_ERRORS.has(code):
					_busy = false
					return _fail(code)
				var converted := entry.duplicate(true)
				converted["kind"] = "observe"
				converted["request"] = entry["fallback_observe"].duplicate(true)
				var conversion := _state.duplicate(true)
				conversion["outcomes"][0] = converted
				if not _persist(conversion):
					_busy = false
					return false
				entry = converted
				response = await _client.observe(entry["request"].duplicate(true))
		else:
			response = await _client.observe(entry["request"].duplicate(true))
		if not response.get("ok", false):
			_busy = false
			return _fail(str(response.get("error_code", "report_failed")))
		var acknowledged := _state.duplicate(true)
		acknowledged["outcomes"].remove_at(0)
		if not _persist(acknowledged):
			_busy = false
			return false
	_busy = false
	return true


static func proposal_freshness(state: Dictionary, proposal: Dictionary) -> String:
	var retained = state.get("proposals", {}).get(proposal.get("id"))
	if not retained is Dictionary or str(retained.get("status", "")) != "pending":
		return "stale"
	for field in [
		"id", "session_id", "request_id", "actor_id", "tick",
		"based_on_revision", "based_on_head_hash",
		"based_on_world_revision", "created_revision",
	]:
		if not retained.has(field) or not proposal.has(field):
			return "stale"
		if not _semantic_equal(retained[field], proposal[field]):
			return "stale"
	if not _semantic_equal(retained.get("action"), proposal.get("action")):
		return "stale"
	if int(proposal.get("based_on_world_revision", 0)) > 0:
		return (
			"fresh"
			if _safe_integer(proposal.get("based_on_world_revision"))
			and _safe_integer(state.get("world_revision"))
			and int(proposal["based_on_world_revision"]) > 0
			and int(proposal["based_on_world_revision"]) == int(state["world_revision"])
			else "stale"
		)
	return (
		"fresh"
		if _safe_integer(proposal.get("created_revision"))
		and _safe_integer(state.get("revision"))
		and int(proposal["created_revision"]) == int(state["revision"])
		else "stale"
	)


static func _semantic_equal(left: Variant, right: Variant) -> bool:
	if typeof(left) in [TYPE_INT, TYPE_FLOAT] and typeof(right) in [TYPE_INT, TYPE_FLOAT]:
		return _safe_integer(left) and _safe_integer(right) and float(left) == float(right)
	if typeof(left) != typeof(right):
		return false
	if left is Dictionary:
		if left.size() != right.size():
			return false
		for key in left:
			if not right.has(key) or not _semantic_equal(left[key], right[key]):
				return false
		return true
	if left is Array:
		if left.size() != right.size():
			return false
		for index in left.size():
			if not _semantic_equal(left[index], right[index]):
				return false
		return true
	return left == right


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
	if DirAccess.make_dir_recursive_absolute("user://rin") != OK:
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
		int(state.get("version", 0)) != 1
		or not _valid_id(state.get("run_id"))
		or str(state["run_id"]).length() != 32
		or not _safe_integer(state.get("sequence"))
		or not _safe_integer(state.get("last_tick"))
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
	for entry in state["outcomes"]:
		if not _valid_outcome(entry):
			return false
		if str(entry["request"].get("session_id", "")) != str(state["create_request"]["session_id"]):
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
		and attempt.get("observe") is Dictionary
		and str(attempt["observe"].get("session_id", "")) == str(state["create_request"]["session_id"])
		and _valid_id(attempt["observe"].get("request_id"))
		and _valid_id(attempt["observe"].get("event_id"))
		and _safe_integer(attempt["observe"].get("tick"))
		and _valid_id(attempt.get("fallback_action_id"))
		and typeof(attempt.get("job_id")) == TYPE_STRING
		and (str(attempt["job_id"]).is_empty() or _valid_id(attempt["job_id"]))
	)


func _valid_outcome(value: Variant) -> bool:
	if not value is Dictionary:
		return false
	var entry: Dictionary = value
	if (
		not _valid_id(entry.get("key"))
		or str(entry.get("kind", "")) not in ["commit", "observe"]
		or not entry.get("request") is Dictionary
		or not _valid_id(entry["request"].get("session_id"))
		or not _valid_id(entry["request"].get("request_id"))
		or not _valid_id(entry["request"].get("event_id"))
		or not _safe_integer(entry["request"].get("tick"))
	):
		return false
	if str(entry["kind"]) == "commit":
		var fallback = entry.get("fallback_observe")
		return (
			fallback is Dictionary
			and str(fallback.get("session_id", "")) == str(entry["request"]["session_id"])
			and _valid_id(fallback.get("request_id"))
			and _valid_id(fallback.get("event_id"))
			and _safe_integer(fallback.get("tick"))
		)
	return true


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
	return {"ok": false, "fallback_reason": code}


func _fail(code: String) -> bool:
	last_error = code
	return false
