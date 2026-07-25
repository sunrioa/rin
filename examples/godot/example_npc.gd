extends Node

const ACTOR_ID := "npc.mira"
const THINK_EVERY_TICKS := 5
const RinClientScript = preload("res://rin_client.gd")
const RinWorkflowScript = preload("res://rin_workflow.gd")

@onready var rin: Node = $RinClient

var workflow = RinWorkflowScript.new()
var turn_running := false


func _ready() -> void:
	if not workflow.open(rin, "default", _build_create_request):
		push_error("Rin workflow state rejected: " + workflow.last_error)
		return
	print("Rin Godot example ready. Call ask_npc_to_respond() for one turn.")


func _exit_tree() -> void:
	workflow.shutdown()


func ask_npc_to_respond() -> void:
	if turn_running:
		push_warning("A Rin NPC turn is already running.")
		return
	if workflow.session_id().is_empty():
		push_error("Rin workflow is unavailable.")
		return
	turn_running = true
	await _run_turn()
	turn_running = false


func _run_turn() -> void:
	var created: Dictionary = await rin.create_session(workflow.create_request())
	if not created.get("ok", false):
		push_warning("Create unavailable; only a coordinator-confirmed fallback may apply.")
	if not await workflow.drain_outbox():
		push_warning("Outcome remains queued: " + workflow.last_error)
		return
	if not workflow.has_attempt():
		var operation_id := workflow.next_operation_id()
		var tick := workflow.next_tick()
		if operation_id.is_empty() or tick < 0:
			push_error("Rin operation identity or tick is exhausted.")
			return
		var attempt := workflow.begin(
			_build_propose_request(operation_id, tick + 1),
			_build_observe_request(operation_id, tick),
			"wait",
		)
		if attempt.is_empty():
			push_error("Pending Turn was not persisted: " + workflow.last_error)
			return
	var result := await workflow.resume()
	var proposal = result.get("proposal")
	if not proposal is Dictionary:
		push_warning("Pending Turn remains unresolved: " + workflow.last_error)
		return
	var planned := _plan_action(proposal.get("action", {}))
	if result.get("committable", false):
		var state_result: Dictionary = await rin.state({
			"protocol_version": RinClientScript.PROTOCOL_VERSION,
			"session_id": workflow.session_id(),
		})
		if (
			not state_result.get("ok", false)
			or RinWorkflowScript.proposal_freshness(
				state_result.get("data", {}),
				proposal,
			) != "fresh"
		):
			planned = {
				"action_id": str(proposal.get("action", {}).get("id", "")),
				"accepted": false,
				"outcome": "The game rejected a stale or unverifiable proposal.",
			}
	var attempt := workflow.current_attempt()
	var occurrence_tick := maxi(
		workflow.next_tick(),
		maxi(int(attempt.get("request", {}).get("tick", 0)), int(proposal.get("tick", 0))),
	)
	var outcome := _build_outcome(
		str(attempt["operation_id"]),
		str(proposal.get("id", "")) if result.get("committable", false) else "",
		occurrence_tick,
		planned,
	)
	var apply := func(_operation_id: String) -> bool:
		if planned["accepted"]:
			print("Mira: " + str(planned["outcome"]))
		return true
	if not workflow.complete(attempt, outcome, apply):
		push_error("Applied action remains pending persistence: " + workflow.last_error)
		return
	if not await workflow.drain_outbox():
		push_warning("Outcome remains queued: " + workflow.last_error)


func _build_create_request(session_id: String, seed: int) -> Dictionary:
	return {
		"protocol_version": RinClientScript.PROTOCOL_VERSION,
		"request_id": "create." + session_id,
		"session_id": session_id,
		"binding": {
			"game_id": "godot-example",
			"content_id": "base",
			"content_version": "0.6.0",
			"content_hash": "sha256:" + "0".repeat(64),
		},
		"seed": seed,
		"features": ["outcome-reporting-v1"],
		"actors": [{
			"id": ACTOR_ID,
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
			"think_every_ticks": THINK_EVERY_TICKS,
			"enabled": true,
		}],
	}


func _build_observe_request(operation_id: String, tick: int) -> Dictionary:
	return {
		"protocol_version": RinClientScript.PROTOCOL_VERSION,
		"session_id": workflow.session_id(),
		"request_id": "observe." + operation_id,
		"event_id": "event." + operation_id,
		"tick": tick,
		"observer_ids": [ACTOR_ID],
		"source": "godot-example",
		"kind": "dialogue",
		"summary": "The player asked Mira what to do next.",
		"tags": ["conversation", "player-request"],
		"importance": 3,
	}


func _build_propose_request(operation_id: String, tick: int) -> Dictionary:
	return {
		"protocol_version": RinClientScript.PROTOCOL_VERSION,
		"session_id": workflow.session_id(),
		"request_id": "propose." + operation_id,
		"actor_id": ACTOR_ID,
		"tick": tick,
		"intent": "Choose one bounded response to the player.",
		"tags": ["conversation"],
		"candidate_actions": [
			{"id": "talk", "kind": "dialogue", "description": "ask one honest question"},
			{"id": "wait", "kind": "wait", "description": "stay silent for now"},
			{"id": "refuse", "kind": "refuse", "description": "decline an unsafe request"},
		],
	}


func _plan_action(action: Variant) -> Dictionary:
	var action_id := str(action.get("id", "")) if action is Dictionary else ""
	var lines := {
		"talk": "What outcome matters most to you?",
		"wait": "Let us observe one more cycle.",
		"refuse": "I cannot help with an unsafe action.",
	}
	if not lines.has(action_id):
		return {
			"action_id": action_id,
			"accepted": false,
			"outcome": "The game rejected an action outside its allowlist.",
		}
	return {"action_id": action_id, "accepted": true, "outcome": lines[action_id]}


func _build_outcome(
	operation_id: String,
	proposal_id: String,
	tick: int,
	applied: Dictionary,
) -> Dictionary:
	var fallback := {
		"protocol_version": RinClientScript.PROTOCOL_VERSION,
		"session_id": workflow.session_id(),
		"request_id": "fallback." + operation_id,
		"event_id": "outcome." + operation_id,
		"tick": tick,
		"observer_ids": [ACTOR_ID],
		"source": "godot-example",
		"kind": "action_outcome",
		"summary": str(applied["outcome"]),
		"tags": ["outcome", "degraded-report"],
		"importance": 3,
	}
	if proposal_id.is_empty():
		return {
			"key": operation_id,
			"kind": "observe",
			"request": fallback,
		}
	return {
		"key": operation_id,
		"kind": "commit",
		"request": {
			"protocol_version": RinClientScript.PROTOCOL_VERSION,
			"session_id": workflow.session_id(),
			"request_id": "commit." + operation_id,
			"proposal_id": proposal_id,
			"event_id": "outcome." + operation_id,
			"tick": tick,
			"accepted": applied["accepted"],
			"outcome": applied["outcome"],
			"tags": ["godot-example", "conversation"],
		},
		"fallback_observe": fallback,
	}
