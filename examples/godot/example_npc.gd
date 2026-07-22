extends Node

@onready var rin: RinClient = $RinClient


func ask_npc_to_respond() -> void:
	var created := await rin.create_session({
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"request_id": "create.playthrough-1",
		"session_id": "playthrough-1",
		"binding": {
			"game_id": "example-game",
			"content_id": "base",
			"content_version": "1.0.0",
			"content_hash": "example-content-hash",
		},
		"seed": 42,
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
			"think_every_ticks": 5,
			"enabled": true,
		}],
	})
	if not created.get("ok", false):
		return
	var request := {
		"protocol_version": RinClient.PROTOCOL_VERSION,
		"session_id": "playthrough-1",
		"request_id": "propose.turn-19.mira",
		"actor_id": "npc.mira",
		"tick": 19,
		"intent": "Choose how to respond to the player.",
		"tags": ["conversation", "trust"],
		"candidate_actions": [
			{"id": "talk", "kind": "dialogue", "description": "Ask one honest question."},
			{"id": "wait", "kind": "wait", "description": "Stay silent for now."},
		],
	}
	var result := await rin.propose_with_fallback(request, "wait")
	var proposal = result.get("proposal")
	if not proposal is Dictionary:
		return
	apply_action_in_game(proposal["action"])
	if result.get("committable", false):
		await rin.commit({
			"protocol_version": RinClient.PROTOCOL_VERSION,
			"session_id": request["session_id"],
			"request_id": "commit.turn-19.mira",
			"proposal_id": proposal["id"],
			"event_id": "event.turn-19.mira",
			"tick": request["tick"],
			"accepted": true,
			"outcome": "The game applied the advertised action.",
		})


func apply_action_in_game(action: Dictionary) -> void:
	# Replace with animation, navigation, dialogue, or combat commands owned by Godot.
	print("Apply game-owned action: ", action.get("id", ""))
