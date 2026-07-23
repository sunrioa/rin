extends Node

const MAX_PROTOCOL_INTEGER := 9223372036854775807
const NPC_THINK_EVERY_TICKS := 5

@onready var rin: RinClient = $RinClient

var _run_id := ""
var _operation_sequence := 0
var _last_authoritative_tick := 0
var _create_request: Dictionary = {}
var _applied_operations: Dictionary = {}
var _report_outbox: Dictionary = {}
var _proposal_attempts: Dictionary = {}
var _authoritative_state_ready := false
var _turn_running := false


func _ready() -> void:
	# Recovery is a startup gate, not a best-effort background task. No new
	# identity or game turn exists until storage has either restored a complete
	# state or positively confirmed that this is a new run and saved its state.
	_authoritative_state_ready = _restore_authoritative_state()
	if not _authoritative_state_ready:
		push_error("Authoritative Rin state could not be restored; NPC turns are disabled.")


func ask_npc_to_respond() -> void:
	if not _authoritative_state_ready:
		push_error("Rin NPC turn refused until authoritative state recovery succeeds.")
		return
	if _turn_running:
		push_warning("A Rin NPC turn is already running.")
		return
	_turn_running = true
	await _run_npc_turn()
	_turn_running = false


func _run_npc_turn() -> void:
	if not _authoritative_state_ready:
		return
	var session_id := "playthrough." + _run_id
	var resuming_attempt := _proposal_attempts.has(session_id)
	# Keep this complete request stable. A lost response is retried on the next
	# turn with the same request ID and byte-equivalent game-owned fields.
	var created := await rin.create_session(_create_request.duplicate(true))
	if not created.get("ok", false):
		if resuming_attempt:
			push_warning("Rin create unavailable; the persisted Proposal attempt will fail closed.")
		else:
			# An empty Outbox and no prior Proposal attempt may proceed to an
			# explicitly authored local fallback from cold start.
			push_warning("Rin create unavailable; this turn may use the authored fallback.")
	# Every authoritative entry retries pending Commit or fallback Observe
	# reports before proposing or applying another action.
	var pending_reported := await _flush_report_outbox()
	if not pending_reported:
		return

	var attempt: Dictionary
	if resuming_attempt:
		attempt = _proposal_attempts[session_id]
		_operation_sequence = maxi(_operation_sequence, int(attempt["sequence"]))
	else:
		if _operation_sequence >= MAX_PROTOCOL_INTEGER:
			push_error("Operation sequence exhausted; no new Proposal can be identified safely.")
			return
		var new_game_tick := _allocate_fresh_proposal_tick()
		if new_game_tick < 0:
			push_error("Authoritative tick exhausted; no new Proposal was submitted.")
			return
		var next_sequence := _operation_sequence + 1
		var new_operation_id := "%s.%d" % [_run_id, next_sequence]
		var stable_request := _build_propose_request(
			session_id,
			new_operation_id,
			new_game_tick,
		)
		attempt = {
			"operation_id": new_operation_id,
			"sequence": next_sequence,
			"request": stable_request.duplicate(true),
			"fallback_action_id": "wait",
			"job_id": "",
		}
		# Persist the entire stable request, operation ID, and consumed sequence
		# before the first POST can create a durable Proposal Job.
		if not _persist_new_proposal_attempt(
			session_id,
			attempt,
			next_sequence,
			new_game_tick,
		):
			push_error("Could not durably save the Proposal attempt; nothing was submitted.")
			return
		_proposal_attempts[session_id] = attempt
		_operation_sequence = next_sequence
		_last_authoritative_tick = new_game_tick

	var operation_id := str(attempt["operation_id"])
	var request: Dictionary = attempt["request"]
	var retained_job_id: String = attempt["job_id"]
	var persist_job_id := func(job_id: String) -> bool:
		return _record_proposal_job_id(session_id, operation_id, job_id)
	var result := await rin.propose_with_fallback(
		request,
		str(attempt["fallback_action_id"]),
		Callable(),
		retained_job_id,
		persist_job_id,
		not resuming_attempt,
	)
	var proposal = result.get("proposal")
	if not proposal is Dictionary:
		return
	var proposal_tick := _read_nonnegative_protocol_tick(proposal.get("tick"))
	if proposal_tick < 0:
		push_error("Proposal tick is not an exact non-negative protocol integer.")
		return
	var planned := plan_action_in_game(proposal["action"])
	var report_entry: Dictionary
	if result.get("committable", false):
		var state_result := await rin.state({
			"protocol_version": RinClient.PROTOCOL_VERSION,
			"session_id": session_id,
		})
		if not state_result.get("ok", false):
			# We already have an online proposal. Reject it authoritatively;
			# never reinterpret a read failure as permission for a fallback.
			planned = {
				"action_id": str(proposal.get("action", {}).get("id", "")),
				"accepted": false,
				"outcome": "The game rejected the proposal because freshness could not be verified.",
			}
		else:
			var state: Dictionary = state_result.get("data", {})
			if not _proposal_is_fresh(state, proposal, request):
				planned = {
					"action_id": str(proposal.get("action", {}).get("id", "")),
					"accepted": false,
					"outcome": "The game rejected a stale proposal before applying any effect.",
				}
		report_entry = _build_commit_report_entry(
			str(request["session_id"]),
			operation_id,
			str(proposal["id"]),
			0,
			planned,
		)
	else:
		# Authored local fallbacks have no Rin Proposal to Commit. Reconcile the
		# game-owned effect as a stable Observe using these exact IDs and tick.
		report_entry = {
			"kind": "observe",
			"request": _build_fallback_observe_request(
				str(request["session_id"]),
				operation_id,
				0,
				planned,
			),
		}
	var applied := _apply_and_enqueue_authoritative_operation(
		session_id,
		operation_id,
		planned,
		report_entry,
		proposal_tick,
	)
	if applied.is_empty():
		return
	await _flush_report_outbox()


func _restore_authoritative_state() -> bool:
	var loaded = _load_authoritative_state()
	if not loaded is Dictionary:
		push_error("Authoritative state loader returned an invalid result.")
		return false
	var status := str(loaded.get("status", "error"))
	if status == "loaded":
		var state = loaded.get("state")
		if not state is Dictionary or not _hydrate_authoritative_state(state):
			push_error("Persisted authoritative state is missing, corrupt, or inconsistent.")
			return false
		return true
	if status != "not_found":
		push_error("Authoritative state load failed: " + str(loaded.get("error", "unknown")))
		return false

	# Only a positive not-found result may mint a new identity. Persist the
	# complete initialized state before publishing it to the running scene.
	var wall_clock := str(int(Time.get_unix_time_from_system() * 1000000.0))
	var new_run_id := wall_clock + "." + str(get_instance_id())
	var initialized_state := {
		"schema_version": 2,
		"run_id": new_run_id,
		"operation_sequence": 0,
		"last_authoritative_tick": 0,
		"create_request": _build_create_request(new_run_id),
		"proposal_attempts": {},
		"applied_operations": {},
		"report_outbox": {},
	}
	if not _persist_authoritative_state_initialization(initialized_state):
		push_error("Could not durably initialize authoritative state.")
		return false
	return _hydrate_authoritative_state(initialized_state)


func _load_authoritative_state() -> Dictionary:
	# PRODUCTION RESTORE HOOK: synchronously read one serialized state object and
	# return exactly one of:
	#   {"status": "loaded", "state": state}
	#   {"status": "not_found"}  # storage positively confirmed no prior state
	#   {"status": "error", "error": "..."}
	# Never translate an I/O/parse/version error into "not_found". This example
	# intentionally stays disabled until the game wires its save provider.
	return {"status": "error", "error": "restore hook not configured"}


func _persist_authoritative_state_initialization(_state: Dictionary) -> bool:
	# PRODUCTION PERSISTENCE HOOK: atomically create-if-absent the entire state
	# supplied here, including run ID, stable Create request, sequence, and
	# high-water tick. A racing existing row or any storage uncertainty must
	# return false and fail closed.
	return true


func _hydrate_authoritative_state(state: Dictionary) -> bool:
	var restored_run_id := str(state.get("run_id", ""))
	var restored_sequence := _read_nonnegative_protocol_tick(
		state.get("operation_sequence"),
	)
	var restored_last_tick := _read_nonnegative_protocol_tick(
		state.get("last_authoritative_tick"),
	)
	var restored_create = state.get("create_request")
	var restored_attempts = state.get("proposal_attempts")
	var restored_applied = state.get("applied_operations")
	var restored_outbox = state.get("report_outbox")
	if (
		int(state.get("schema_version", 0)) != 2
		or restored_run_id == ""
		or restored_sequence < 0
		or restored_last_tick < 0
		or not restored_create is Dictionary
		or not restored_attempts is Dictionary
		or not restored_applied is Dictionary
		or not restored_outbox is Dictionary
	):
		return false
	var expected_session_id := "playthrough." + restored_run_id
	var expected_create := _build_create_request(restored_run_id)
	if (
		str(restored_create.get("session_id", "")) != expected_session_id
		or str(restored_create.get("request_id", "")) != "create." + restored_run_id
		or not _semantic_values_equal(restored_create, expected_create)
	):
		return false
	for session_key in restored_attempts:
		var attempt = restored_attempts[session_key]
		if (
			typeof(session_key) != TYPE_STRING
			or not attempt is Dictionary
			or str(session_key) != expected_session_id
		):
			return false
		var request = attempt.get("request")
		var attempt_sequence := _read_nonnegative_protocol_tick(attempt.get("sequence"))
		var attempt_operation_id := str(attempt.get("operation_id", ""))
		var attempt_tick := (
			_read_nonnegative_protocol_tick(request.get("tick"))
			if request is Dictionary
			else -1
		)
		var canonical_sequence := _operation_sequence_from_id(
			attempt_operation_id,
			restored_run_id,
		)
		var expected_request := (
			_build_propose_request(
				expected_session_id,
				attempt_operation_id,
				attempt_tick,
			)
			if attempt_tick >= 0
			else {}
		)
		var attempt_job_id_value = attempt.get("job_id")
		if typeof(attempt_job_id_value) != TYPE_STRING:
			return false
		var attempt_job_id: String = attempt_job_id_value
		var expected_attempt := {
			"operation_id": attempt_operation_id,
			"sequence": attempt_sequence,
			"request": expected_request,
			"fallback_action_id": "wait",
			"job_id": attempt_job_id,
		}
		if (
			not request is Dictionary
			or attempt_operation_id == ""
			or attempt_sequence <= 0
			or attempt_sequence != restored_sequence
			or canonical_sequence != attempt_sequence
			or str(request.get("session_id", "")) != expected_session_id
			or str(request.get("request_id", "")) != "propose." + attempt_operation_id
			or not _semantic_values_equal(request, expected_request)
			or str(attempt.get("fallback_action_id", "")) != "wait"
			or (
				not attempt_job_id.is_empty()
				and not _is_valid_protocol_id(attempt_job_id)
			)
			or not _semantic_values_equal(attempt, expected_attempt)
			or attempt_tick < 0
			or attempt_tick > restored_last_tick
			or restored_applied.has(attempt_operation_id)
			or restored_outbox.has(attempt_operation_id)
		):
			return false
	for operation_id in restored_applied:
		var applied_sequence := _operation_sequence_from_id(str(operation_id), restored_run_id)
		var applied = restored_applied[operation_id]
		if (
			typeof(operation_id) != TYPE_STRING
			or applied_sequence <= 0
			or applied_sequence > restored_sequence
			or not applied is Dictionary
			or typeof(applied.get("action_id")) != TYPE_STRING
			or typeof(applied.get("accepted")) != TYPE_BOOL
			or typeof(applied.get("outcome")) != TYPE_STRING
			or not _semantic_values_equal(applied, {
				"action_id": applied.get("action_id"),
				"accepted": applied.get("accepted"),
				"outcome": applied.get("outcome"),
			})
		):
			return false
	for operation_id in restored_outbox:
		var operation_key := str(operation_id)
		var operation_sequence := _operation_sequence_from_id(
			operation_key,
			restored_run_id,
		)
		var entry = restored_outbox[operation_id]
		if (
			typeof(operation_id) != TYPE_STRING
			or operation_sequence <= 0
			or operation_sequence > restored_sequence
			or not entry is Dictionary
			or not restored_applied.has(operation_key)
		):
			return false
		var kind := str(entry.get("kind", ""))
		var request = entry.get("request")
		var request_tick := (
			_read_nonnegative_protocol_tick(request.get("tick"))
			if request is Dictionary
			else -1
		)
		if (
			(kind != "commit" and kind != "observe")
			or not request is Dictionary
			or str(request.get("session_id", "")) != expected_session_id
			or request_tick < 0
			or request_tick > restored_last_tick
		):
			return false
		var applied: Dictionary = restored_applied[operation_key]
		var expected_entry: Dictionary
		if kind == "commit":
			var fallback = entry.get("fallback_request")
			var proposal_id = request.get("proposal_id") if request is Dictionary else null
			if (
				str(request.get("request_id", "")) != "commit." + operation_key
				or str(request.get("event_id", "")) != "outcome." + operation_key
				or not _is_valid_protocol_id(proposal_id)
				or not fallback is Dictionary
				or str(fallback.get("request_id", "")) != "reconcile." + operation_key
				or str(fallback.get("session_id", "")) != str(request.get("session_id", ""))
				or str(fallback.get("event_id", "")) != str(request.get("event_id", ""))
				or _read_nonnegative_protocol_tick(fallback.get("tick")) != request_tick
				or typeof(request.get("accepted")) != TYPE_BOOL
				or request.get("accepted") != applied.get("accepted")
				or typeof(request.get("outcome")) != TYPE_STRING
				or request.get("outcome") != applied.get("outcome")
				or str(fallback.get("source", "")) != "godot-example"
				or str(fallback.get("kind", "")) != "action_outcome"
				or str(fallback.get("summary", ""))
				!= "Authoritative outcome: " + str(applied.get("outcome"))
			):
				return false
			expected_entry = _build_commit_report_entry(
				expected_session_id,
				operation_key,
				String(proposal_id),
				request_tick,
				applied,
			)
		else:
			var event_id := str(request.get("event_id", ""))
			if (
				str(request.get("request_id", "")) != "reconcile." + operation_key
				or event_id not in [
					"fallback." + operation_key,
					"outcome." + operation_key,
				]
				or str(request.get("source", "")) != "godot-example"
			):
				return false
			if event_id == "outcome." + operation_key:
				if (
					str(request.get("kind", "")) != "action_outcome"
					or str(request.get("summary", ""))
					!= "Authoritative outcome: " + str(applied.get("outcome"))
				):
					return false
				expected_entry = {
					"kind": "observe",
					"request": _build_outcome_observe_request(
						expected_session_id,
						operation_key,
						request_tick,
						applied,
					),
				}
			elif (
				str(request.get("kind", "")) != "fallback_action"
				or str(request.get("summary", ""))
				!= "Local fallback %s: %s" % [
					str(applied.get("action_id")),
					str(applied.get("outcome")),
				]
			):
				return false
			else:
				expected_entry = {
					"kind": "observe",
					"request": _build_fallback_observe_request(
						expected_session_id,
						operation_key,
						request_tick,
						applied,
					),
				}
		# Rebuild the complete canonical DTO from durable operation identity,
		# applied marker, Proposal identity, and occurrence tick. Dictionary size
		# is part of semantic equality, so injected facts/goals/tags, alternate
		# observers, or any non-canonical/default field fail restoration.
		if not _semantic_values_equal(entry, expected_entry):
			return false

	_run_id = restored_run_id
	_operation_sequence = restored_sequence
	_last_authoritative_tick = restored_last_tick
	_create_request = restored_create.duplicate(true)
	_proposal_attempts = restored_attempts.duplicate(true)
	_applied_operations = restored_applied.duplicate(true)
	_report_outbox = restored_outbox.duplicate(true)
	return true


func _build_create_request(run_id: String) -> Dictionary:
	return {
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"request_id": "create." + run_id,
		"session_id": "playthrough." + run_id,
		"binding": {
			"game_id": "example-game",
			"content_id": "base",
			"content_version": "1.0.0",
			"content_hash": "example-content-hash",
		},
		"seed": 42,
		"features": ["outcome-reporting-v1"],
		"actors": [{
			"id": "npc.mira",
			"kind": "npc",
			"display_name": "Mira",
			"traits": ["careful"],
			"goals": [{
				"id": "goal.connect",
				"description": "Build trust through specific actions.",
				"priority": 4,
				"preferred_actions": ["talk"],
				"progress": 0,
				"target_progress": 3,
				"status": "active",
			}],
			"think_every_ticks": NPC_THINK_EVERY_TICKS,
			"enabled": true,
		}],
	}


func _build_propose_request(
	session_id: String,
	operation_id: String,
	tick: int,
) -> Dictionary:
	return {
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"session_id": session_id,
		"request_id": "propose." + operation_id,
		"actor_id": "npc.mira",
		"tick": tick,
		"intent": "Choose how to respond to the player.",
		"tags": ["conversation", "trust"],
		"candidate_actions": [
			{"id": "talk", "kind": "dialogue", "description": "Ask one honest question."},
			{"id": "wait", "kind": "wait", "description": "Stay silent for now."},
		],
	}


func _build_commit_report_entry(
	session_id: String,
	operation_id: String,
	proposal_id: String,
	tick: int,
	applied: Dictionary,
) -> Dictionary:
	return {
		"kind": "commit",
		"request": {
			"protocol_version": RinClient.PROTOCOL_VERSION,
			"session_id": session_id,
			"request_id": "commit." + operation_id,
			"proposal_id": proposal_id,
			"event_id": "outcome." + operation_id,
			"tick": tick,
			"accepted": applied["accepted"],
			"outcome": applied["outcome"],
		},
		# Persist this safe degradation payload in the same transaction as the
		# Commit. It records only episodic memory: no goals, recent actions,
		# scheduler changes, or relative facts are fabricated.
		"fallback_request": _build_outcome_observe_request(
			session_id,
			operation_id,
			tick,
			applied,
		),
	}


func _build_outcome_observe_request(
	session_id: String,
	operation_id: String,
	tick: int,
	applied: Dictionary,
) -> Dictionary:
	return {
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"session_id": session_id,
		"request_id": "reconcile." + operation_id,
		"event_id": "outcome." + operation_id,
		"tick": tick,
		# This adapter owns exactly one actor; never trust a persisted or remote
		# observer identity when reconstructing an authoritative report.
		"observer_ids": ["npc.mira"],
		"source": "godot-example",
		"kind": "action_outcome",
		"summary": "Authoritative outcome: " + str(applied["outcome"]),
		"tags": ["outcome-report"],
		"importance": 3,
	}


func _build_fallback_observe_request(
	session_id: String,
	operation_id: String,
	tick: int,
	applied: Dictionary,
) -> Dictionary:
	return {
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"session_id": session_id,
		"request_id": "reconcile." + operation_id,
		"event_id": "fallback." + operation_id,
		"tick": tick,
		"observer_ids": ["npc.mira"],
		"source": "godot-example",
		"kind": "fallback_action",
		"summary": "Local fallback %s: %s" % [
			str(applied["action_id"]),
			str(applied["outcome"]),
		],
		"tags": ["fallback"],
		"importance": 3,
	}


func _is_valid_protocol_id(value: Variant) -> bool:
	if typeof(value) != TYPE_STRING:
		return false
	var text: String = value
	if text.is_empty() or text.length() > 96:
		return false
	for index in range(text.length()):
		var code := text.unicode_at(index)
		var is_letter := (code >= 65 and code <= 90) or (code >= 97 and code <= 122)
		var is_digit := code >= 48 and code <= 57
		if index == 0:
			if not is_letter and not is_digit:
				return false
		elif not is_letter and not is_digit and code not in [46, 95, 45]:
			return false
	return true


func _semantic_values_equal(left: Variant, right: Variant) -> bool:
	var left_type := typeof(left)
	var right_type := typeof(right)
	if left_type == TYPE_INT and right_type == TYPE_INT:
		return int(left) == int(right)
	if left_type in [TYPE_INT, TYPE_FLOAT] and right_type in [TYPE_INT, TYPE_FLOAT]:
		var left_number := float(left)
		var right_number := float(right)
		return (
			is_finite(left_number)
			and is_finite(right_number)
			and abs(left_number) <= 9007199254740991.0
			and abs(right_number) <= 9007199254740991.0
			and left_number == right_number
		)
	if left_type != right_type:
		return false
	if left_type == TYPE_DICTIONARY:
		if left.size() != right.size():
			return false
		for key in left:
			if not right.has(key) or not _semantic_values_equal(left[key], right[key]):
				return false
		return true
	if left_type == TYPE_ARRAY:
		if left.size() != right.size():
			return false
		for index in range(left.size()):
			if not _semantic_values_equal(left[index], right[index]):
				return false
		return true
	return left == right


func _persist_new_proposal_attempt(
	session_id: String,
	attempt: Dictionary,
	sequence: int,
	authoritative_tick: int,
) -> bool:
	if not _authoritative_state_ready:
		return false
	var request = attempt.get("request")
	if (
		not request is Dictionary
		or _operation_sequence >= MAX_PROTOCOL_INTEGER
		or sequence != _operation_sequence + 1
		or authoritative_tick <= _last_authoritative_tick
		or _operation_sequence_from_id(str(attempt.get("operation_id", "")), _run_id)
		!= sequence
		or _read_nonnegative_protocol_tick(attempt.get("sequence")) != sequence
		or str(request.get("session_id", "")) != session_id
		or str(request.get("request_id", ""))
		!= "propose." + str(attempt.get("operation_id", ""))
		or not _semantic_values_equal(
			request,
			_build_propose_request(
				session_id,
				str(attempt.get("operation_id", "")),
				authoritative_tick,
			),
		)
		or str(attempt.get("fallback_action_id", "")) != "wait"
		or _read_nonnegative_protocol_tick(request.get("tick")) != authoritative_tick
	):
		return false
	# PRODUCTION PERSISTENCE HOOK: atomically save the complete attempt and the
	# consumed game sequence and last_authoritative_tick before any online
	# submission or local fallback.
	return true


func _record_proposal_job_id(
	session_id: String,
	operation_id: String,
	job_id: String,
) -> bool:
	if not _is_valid_protocol_id(job_id):
		return false
	if not _proposal_attempts.has(session_id):
		return false
	var current: Dictionary = _proposal_attempts[session_id]
	if str(current.get("operation_id", "")) != operation_id:
		return false
	var current_job_id = current.get("job_id")
	if typeof(current_job_id) != TYPE_STRING:
		return false
	if current_job_id == job_id:
		return true
	var replacement := current.duplicate(true)
	replacement["job_id"] = job_id
	if not _persist_proposal_job_id(session_id, operation_id, job_id):
		return false
	_proposal_attempts[session_id] = replacement
	return true


func _persist_proposal_job_id(
	_session_id: String,
	_operation_id: String,
	_job_id: String,
) -> bool:
	# PRODUCTION PERSISTENCE HOOK: durably attach the 202 Job ID to the matching
	# stable attempt before the adapter starts polling it.
	return true


func _apply_and_enqueue_authoritative_operation(
	session_id: String,
	operation_id: String,
	planned: Dictionary,
	report_entry: Dictionary,
	proposal_tick: int,
) -> Dictionary:
	if not _authoritative_state_ready:
		return {}
	if _applied_operations.has(operation_id):
		# Atomic persistence guarantees the matching report entry also exists
		# until acknowledgement; never execute the game effect again.
		return _applied_operations[operation_id]
	if not _persist_authoritative_transaction(
		session_id,
		operation_id,
		planned,
		report_entry,
		proposal_tick,
	):
		push_error("Authoritative game transaction rolled back; no report was queued.")
		return {}
	return _applied_operations.get(operation_id, {})


func _persist_authoritative_transaction(
	session_id: String,
	operation_id: String,
	planned: Dictionary,
	report_entry: Dictionary,
	proposal_tick: int,
) -> bool:
	if not _authoritative_state_ready:
		return false
	# PRODUCTION PERSISTENCE HOOK: replace this whole body with one atomic game
	# transaction. The actual game-state effect, applied marker, complete
	# Commit/Observe entry (including its safe fallback), Proposal-attempt
	# deletion, run ID, sequence, and last authoritative tick must commit or
	# roll back together.
	# Engine/native exceptions must abort that transaction; fallible game
	# callbacks should return failure as below.
	if not _proposal_attempts.has(session_id):
		return false
	var proposal_attempt: Dictionary = _proposal_attempts[session_id]
	if str(proposal_attempt.get("operation_id", "")) != operation_id:
		return false
	var retained_request = proposal_attempt.get("request")
	if not retained_request is Dictionary:
		return false
	var request_tick := _read_nonnegative_protocol_tick(retained_request.get("tick"))
	if request_tick < 0 or proposal_tick < 0:
		return false
	# Engine frame counters commonly reset on process/scene restart. Never let
	# that regress the outcome below either durable causal input.
	var occurrence_tick := maxi(
		maxi(_capture_authoritative_occurrence_tick(), _last_authoritative_tick),
		maxi(request_tick, proposal_tick),
	)
	var effective_planned := planned.duplicate(true)
	if (
		effective_planned.get("accepted") == true
		and occurrence_tick > MAX_PROTOCOL_INTEGER - NPC_THINK_EVERY_TICKS
	):
		# An accepted Commit schedules npc.mira at tick + think_every_ticks.
		# Convert to an authoritative rejection before any game effect when that
		# addition would overflow int64; the resulting Commit remains valid.
		effective_planned["accepted"] = false
		effective_planned["outcome"] = (
			"The game rejected the action because the scheduler tick range is exhausted."
		)
	var persisted_report: Dictionary
	if report_entry.get("kind") == "commit":
		var commit_request = report_entry.get("request")
		if (
			not commit_request is Dictionary
			or not _is_valid_protocol_id(commit_request.get("proposal_id"))
		):
			return false
		persisted_report = _build_commit_report_entry(
			session_id,
			operation_id,
			String(commit_request["proposal_id"]),
			occurrence_tick,
			effective_planned,
		)
	elif report_entry.get("kind") == "observe":
		var observe_request = report_entry.get("request")
		if (
			not observe_request is Dictionary
			or observe_request.get("event_id") != "fallback." + operation_id
		):
			return false
		persisted_report = {
			"kind": "observe",
			"request": _build_fallback_observe_request(
				session_id,
				operation_id,
				occurrence_tick,
				effective_planned,
			),
		}
	else:
		return false
	var effect_result := _apply_planned_game_effect(effective_planned)
	var rollback: Callable = effect_result.get("rollback", Callable())
	if not effect_result.get("ok", false):
		# A fallible callback may have partially mutated game state before it
		# reported failure. Run its registered inverse before aborting.
		if rollback.is_valid():
			rollback.call()
		return false
	var previous_last_tick := _last_authoritative_tick
	_last_authoritative_tick = occurrence_tick
	_applied_operations[operation_id] = effective_planned
	_report_outbox[operation_id] = persisted_report
	# A succeeded online proposal (or confirmed-safe offline terminal) stops
	# being resumable only inside this game-authoritative transaction.
	_proposal_attempts.erase(session_id)
	if not _commit_authoritative_game_transaction(operation_id, occurrence_tick):
		_applied_operations.erase(operation_id)
		_report_outbox.erase(operation_id)
		_proposal_attempts[session_id] = proposal_attempt
		_last_authoritative_tick = previous_last_tick
		if rollback.is_valid():
			rollback.call()
		return false
	return true


func _flush_report_outbox() -> bool:
	if not _authoritative_state_ready:
		return false
	var operation_ids := _report_outbox.keys()
	operation_ids.sort()
	for operation_id in operation_ids:
		var entry: Dictionary = _report_outbox[operation_id]
		var reported: Dictionary
		if entry.get("kind") == "commit":
			reported = await rin.commit(entry["request"])
			if not reported.get("ok", false):
				var error_code := str(reported.get("error_code", "unknown"))
				if not _is_irrecoverable_commit_error(error_code):
					push_error("Commit temporarily failed; its exact request remains queued.")
					return false
				var replacement := entry.duplicate(true)
				replacement["kind"] = "observe"
				replacement["request"] = entry["fallback_request"].duplicate(true)
				replacement.erase("fallback_request")
				if not _persist_report_conversion(operation_id, replacement):
					push_error("Could not durably convert Commit; original remains queued.")
					return false
				_report_outbox[operation_id] = replacement
				entry = replacement
				reported = await rin.observe(entry["request"])
		elif entry.get("kind") == "observe":
			reported = await rin.observe(entry["request"])
		else:
			push_error("Unknown authoritative report kind; entry remains queued.")
			return false
		if not reported.get("ok", false):
			push_error("Game action already handled; the same report remains queued for retry.")
			return false
		if not _persist_report_acknowledgement(operation_id):
			push_error("Report was acknowledged but durable Outbox deletion failed; retry is safe.")
			return false
		_report_outbox.erase(operation_id)
	return true


func _persist_report_acknowledgement(_operation_id: String) -> bool:
	# PRODUCTION PERSISTENCE HOOK: durably delete this Outbox row. The caller
	# evicts its in-memory copy only after this returns true.
	return true


func _persist_report_conversion(
	_operation_id: String,
	_replacement: Dictionary,
) -> bool:
	# PRODUCTION PERSISTENCE HOOK: atomically replace the Commit row with the
	# pre-persisted Observe fallback before the in-memory cache is changed.
	return true


func _commit_authoritative_game_transaction(
	_operation_id: String,
	authoritative_tick: int,
) -> bool:
	# PRODUCTION PERSISTENCE HOOK: return false (or abort the native transaction)
	# if effect, applied marker, Outbox, run ID, sequence, and high-water tick
	# cannot all commit.
	return authoritative_tick == _last_authoritative_tick


func _capture_authoritative_occurrence_tick() -> int:
	# Read the current game clock inside the transaction at actual apply/reject.
	# Production games should inject their persisted simulation clock here.
	return maxi(0, int(Engine.get_physics_frames()))


func _allocate_fresh_proposal_tick() -> int:
	if _last_authoritative_tick >= MAX_PROTOCOL_INTEGER:
		return -1
	# Preserve a larger live simulation clock, but advance the restored durable
	# high-water by at least one when the engine clock reset or stood still.
	return maxi(
		_capture_authoritative_occurrence_tick(),
		_last_authoritative_tick + 1,
	)


func _operation_sequence_from_id(operation_id: String, run_id: String) -> int:
	var prefix := run_id + "."
	if not operation_id.begins_with(prefix):
		return -1
	var suffix := operation_id.substr(prefix.length())
	if suffix == "" or not suffix.is_valid_int():
		return -1
	var sequence := suffix.to_int()
	# Reject signs and leading zeroes as non-canonical even if they parse.
	if sequence <= 0 or str(sequence) != suffix:
		return -1
	return sequence


func _read_nonnegative_protocol_tick(value: Variant) -> int:
	if typeof(value) == TYPE_INT:
		return int(value) if int(value) >= 0 else -1
	if typeof(value) == TYPE_FLOAT:
		var number := float(value)
		# JSON-decoded floats are accepted only while their integer identity is
		# exact; larger values must be transported/decoded as native int64.
		if (
			not is_finite(number)
			or number < 0.0
			or number > 9007199254740991.0
			or floor(number) != number
		):
			return -1
		return int(number)
	return -1


func _proposal_is_fresh(
	state: Dictionary,
	proposal: Dictionary,
	stable_request: Dictionary,
) -> bool:
	var proposals = state.get("proposals", {})
	var proposal_id := str(proposal.get("id", ""))
	if proposal_id == "" or not proposals is Dictionary or not proposals.has(proposal_id):
		return false
	var retained = proposals[proposal_id]
	if (
		not retained is Dictionary
		or str(retained.get("id", "")) != proposal_id
		or str(retained.get("status", "")) != "pending"
	):
		return false
	var retained_action = retained.get("action")
	var response_action = proposal.get("action")
	var retained_tick := _read_nonnegative_protocol_tick(retained.get("tick"))
	var response_tick := _read_nonnegative_protocol_tick(proposal.get("tick"))
	var retained_revision_base := _read_nonnegative_protocol_tick(
		retained.get("based_on_revision"),
	)
	var response_revision_base := _read_nonnegative_protocol_tick(
		proposal.get("based_on_revision"),
	)
	var retained_head_hash := str(retained.get("based_on_head_hash", ""))
	var response_head_hash := str(proposal.get("based_on_head_hash", ""))
	var retained_created := _read_nonnegative_protocol_tick(
		retained.get("created_revision"),
	)
	var retained_world_base := _read_nonnegative_protocol_tick(
		retained.get("based_on_world_revision", 0),
	)
	var response_created := _read_nonnegative_protocol_tick(
		proposal.get("created_revision"),
	)
	var response_world_base := _read_nonnegative_protocol_tick(
		proposal.get("based_on_world_revision", 0),
	)
	var response_action_id := (
		str(response_action.get("id", ""))
		if response_action is Dictionary
		else ""
	)
	var stable_action: Dictionary = {}
	var candidate_actions = stable_request.get("candidate_actions")
	if candidate_actions is Array:
		for candidate in candidate_actions:
			if (
				candidate is Dictionary
				and str(candidate.get("id", "")) == response_action_id
			):
				stable_action = candidate
				break
	if (
		not retained_action is Dictionary
		or not response_action is Dictionary
		or str(retained.get("session_id", "")) == ""
		or str(retained.get("session_id", "")) != str(proposal.get("session_id", ""))
		or str(retained.get("request_id", "")) == ""
		or str(retained.get("request_id", "")) != str(proposal.get("request_id", ""))
		or str(retained.get("actor_id", "")) == ""
		or str(retained.get("actor_id", "")) != str(proposal.get("actor_id", ""))
		or retained_tick < 0
		or response_tick != retained_tick
		or str(retained_action.get("id", "")) == ""
		or str(retained_action.get("id", "")) != str(response_action.get("id", ""))
		or str(retained_action.get("kind", "")) == ""
		or str(retained_action.get("kind", "")) != str(response_action.get("kind", ""))
		or not _semantic_values_equal(retained_action, response_action)
		or stable_action.is_empty()
		or not _semantic_values_equal(stable_action, response_action)
		or retained_revision_base < 0
		or response_revision_base != retained_revision_base
		or retained_head_hash != response_head_hash
		or retained_created < 0
		or retained_world_base < 0
		or response_created != retained_created
		or response_world_base != retained_world_base
	):
		return false
	if retained_world_base > 0:
		return (
			_read_nonnegative_protocol_tick(state.get("world_revision"))
			== retained_world_base
		)
	return _read_nonnegative_protocol_tick(state.get("revision")) == retained_created


func _is_irrecoverable_commit_error(error_code: String) -> bool:
	return error_code in [
		"session_not_found",
		"unknown_proposal",
		"proposal_resolved",
		"proposal_canceled",
		"proposal_stale",
	]


func plan_action_in_game(action: Dictionary) -> Dictionary:
	var action_id := str(action.get("id", ""))
	if action_id != "talk" and action_id != "wait":
		return {
			"action_id": action_id,
			"accepted": false,
			"outcome": "The game rejected an action outside its local allowlist.",
		}
	return {
		"action_id": action_id,
		"accepted": true,
		"outcome": "The game applied the advertised action.",
	}


func _apply_planned_game_effect(planned: Dictionary) -> Dictionary:
	if not _authoritative_state_ready:
		return {"ok": false, "rollback": Callable()}
	# Replace with animation, navigation, dialogue, or combat commands owned by
	# Godot. Register a rollback before mutating and return {"ok": false} on a
	# fallible callback instead of publishing a marker or accepted Outbox.
	if planned["accepted"]:
		print("Apply game-owned action: ", planned["action_id"])
	var rollback := func() -> void:
		if planned["accepted"]:
			print("Roll back game-owned action: ", planned["action_id"])
	return {
		"ok": true,
		"rollback": rollback,
	}
