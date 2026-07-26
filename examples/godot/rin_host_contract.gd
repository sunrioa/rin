class_name RinHostContract
extends RefCounted

const MAX_SAFE_INTEGER := 9007199254740991


static func valid_id(value: Variant) -> bool:
	if typeof(value) != TYPE_STRING:
		return false
	var text: String = value
	if text.is_empty() or text.length() > 96:
		return false
	var expression := RegEx.new()
	return (
		expression.compile("^[A-Za-z0-9][A-Za-z0-9._-]*$") == OK
		and expression.search(text) != null
	)


static func safe_integer(value: Variant) -> bool:
	if typeof(value) != TYPE_INT and typeof(value) != TYPE_FLOAT:
		return false
	var number := float(value)
	return (
		is_finite(number)
		and number >= 0.0
		and number <= MAX_SAFE_INTEGER
		and floor(number) == number
	)


static func valid_epoch(value: Variant, session_id: String = "") -> bool:
	if not value is Dictionary:
		return false
	var epoch: Dictionary = value
	return (
		valid_id(epoch.get("session_id"))
		and (session_id.is_empty() or str(epoch["session_id"]) == session_id)
		and valid_id(epoch.get("world_id"))
		and safe_integer(epoch.get("host"))
		and int(epoch["host"]) > 0
		and safe_integer(epoch.get("world"))
		and int(epoch["world"]) > 0
		and safe_integer(epoch.get("timeline"))
		and int(epoch["timeline"]) > 0
	)


static func semantic_equal(left: Variant, right: Variant) -> bool:
	if (
		typeof(left) in [TYPE_INT, TYPE_FLOAT]
		and typeof(right) in [TYPE_INT, TYPE_FLOAT]
	):
		return (
			_safe_number(left)
			and _safe_number(right)
			and float(left) == float(right)
		)
	if typeof(left) != typeof(right):
		return false
	if left is Dictionary:
		if left.size() != right.size():
			return false
		for key in left:
			if not right.has(key) or not semantic_equal(left[key], right[key]):
				return false
		return true
	if left is Array:
		if left.size() != right.size():
			return false
		for index in left.size():
			if not semantic_equal(left[index], right[index]):
				return false
		return true
	return left == right


static func resolve_offer(request: Variant, proposal: Variant) -> Dictionary:
	if (
		not request is Dictionary
		or not proposal is Dictionary
		or not valid_id(proposal.get("id"))
		or str(proposal.get("session_id", "")) != str(request.get("session_id", ""))
		or str(proposal.get("request_id", "")) != str(request.get("request_id", ""))
		or str(proposal.get("actor_id", "")) != str(request.get("actor_id", ""))
		or not safe_integer(proposal.get("tick"))
		or int(proposal["tick"]) != int(request.get("tick", -1))
		or not semantic_equal(
			proposal.get("decision_window"),
			request.get("decision_window"),
		)
		or not proposal.get("action") is Dictionary
		or not request.get("offers") is Array
	):
		return {}
	for value in request["offers"]:
		if (
			value is Dictionary
			and semantic_equal(value, proposal["action"])
		):
			return value.duplicate(true)
	return {}


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
		if not semantic_equal(retained[field], proposal[field]):
			return "stale"
	if not semantic_equal(retained.get("action"), proposal.get("action")):
		return "stale"
	if int(proposal.get("based_on_world_revision", 0)) > 0:
		return (
			"fresh"
			if safe_integer(proposal.get("based_on_world_revision"))
			and safe_integer(state.get("world_revision"))
			and int(proposal["based_on_world_revision"]) > 0
			and int(proposal["based_on_world_revision"]) == int(state["world_revision"])
			else "stale"
		)
	return (
		"fresh"
		if safe_integer(proposal.get("created_revision"))
		and safe_integer(state.get("revision"))
		and int(proposal["created_revision"]) == int(state["revision"])
		else "stale"
	)


static func valid_turn(
	request: Variant,
	observe: Variant,
	session_id: String,
) -> bool:
	if (
		not request is Dictionary
		or not observe is Dictionary
		or str(request.get("session_id", "")) != session_id
		or str(observe.get("session_id", "")) != session_id
		or not valid_id(request.get("request_id"))
		or not valid_id(request.get("actor_id"))
		or not safe_integer(request.get("tick"))
		or not valid_id(observe.get("request_id"))
		or not valid_id(observe.get("event_id"))
		or not safe_integer(observe.get("tick"))
		or not request.get("decision_window") is Dictionary
		or not request.get("offers") is Array
		or request["offers"].is_empty()
		or request["offers"].size() > 32
	):
		return false
	var window: Dictionary = request["decision_window"]
	if (
		not valid_id(window.get("id"))
		or str(window.get("mode", "")) not in [
			"sequential", "simultaneous", "asynchronous"
		]
		or not valid_epoch(window.get("epoch"), session_id)
		or not safe_integer(window.get("observation_seq"))
		or not window.get("opened_at") is Dictionary
		or not window.get("deadline") is Dictionary
		or not _valid_timepoint(window["opened_at"])
		or not _valid_timepoint(window["deadline"])
		or not window.get("actor_ids") is Array
		or not str(request["actor_id"]) in window["actor_ids"]
		or not semantic_equal(observe.get("epoch"), window["epoch"])
		or not safe_integer(observe.get("observation_seq"))
		or int(observe["observation_seq"]) != int(window["observation_seq"])
	):
		return false
	var seen := {}
	for value in request["offers"]:
		if not _valid_offer(value, request["actor_id"], window):
			return false
		var offer: Dictionary = value
		if seen.has(offer["offer_id"]):
			return false
		seen[offer["offer_id"]] = true
	return true


static func valid_outcome(
	entry: Variant,
	session_id: String,
	operation_id: String = "",
) -> bool:
	if (
		not entry is Dictionary
		or not valid_id(entry.get("key"))
		or (not operation_id.is_empty() and str(entry["key"]) != operation_id)
		or str(entry.get("kind", "")) != "report"
		or not entry.get("request") is Dictionary
	):
		return false
	var request: Dictionary = entry["request"]
	if (
		str(request.get("session_id", "")) != session_id
		or not valid_id(request.get("request_id"))
		or not safe_integer(request.get("tick"))
		or not request.get("report") is Dictionary
	):
		return false
	var report: Dictionary = request["report"]
	if (
		not valid_id(report.get("proposal_id"))
		or not valid_id(report.get("event_id"))
		or str(report.get("decision", "")) not in ["accepted", "rejected"]
	):
		return false
	if report["decision"] == "rejected":
		return (
			not report.has("invocation")
			and not report.has("run")
			and not report.has("outcome")
		)
	if (
		not report.get("invocation") is Dictionary
		or not report.get("run") is Dictionary
		or not report.get("outcome") is Dictionary
	):
		return false
	var invocation: Dictionary = report["invocation"]
	var run: Dictionary = report["run"]
	var outcome: Dictionary = report["outcome"]
	var status := str(run.get("status", ""))
	return (
		str(invocation.get("operation_id", "")) == str(entry["key"])
		and str(run.get("operation_id", "")) == str(entry["key"])
		and str(outcome.get("operation_id", "")) == str(entry["key"])
		and status in [
			"succeeded", "failed", "cancelled", "interrupted", "stale",
			"outcome-unknown",
		]
		and str(outcome.get("status", "")) == status
		and valid_epoch(invocation.get("expected_epoch"), session_id)
		and semantic_equal(invocation["expected_epoch"], outcome.get("epoch"))
	)


static func interrupted_outcome(entry: Dictionary) -> Dictionary:
	var recovered := entry.duplicate(true)
	var report: Dictionary = recovered["request"]["report"]
	report["summary"] = (
		"The Godot Host restarted before the action reached a durable terminal result."
	)
	report["run"]["status"] = "outcome-unknown"
	report["run"]["progress"] = 0
	report["outcome"]["status"] = "outcome-unknown"
	report["outcome"]["summary"] = report["summary"]
	return recovered


static func outcome_matches_proposal(
	entry: Dictionary,
	proposal: Dictionary,
	operation_id: String,
) -> bool:
	var report: Dictionary = entry.get("request", {}).get("report", {})
	if str(report.get("proposal_id", "")) != str(proposal.get("id", "")):
		return false
	if str(report.get("decision", "")) == "rejected":
		return true
	if not proposal.get("action") is Dictionary:
		return false
	var invocation: Dictionary = proposal["action"].duplicate(true)
	invocation.erase("description")
	invocation["operation_id"] = operation_id
	return (
		semantic_equal(report.get("invocation"), invocation)
		and semantic_equal(
			report.get("outcome", {}).get("epoch"),
			proposal["action"].get("expected_epoch"),
		)
	)


static func _valid_offer(
	value: Variant,
	actor_id: String,
	window: Dictionary,
) -> bool:
	if not value is Dictionary:
		return false
	var offer: Dictionary = value
	return (
		valid_id(offer.get("offer_id"))
		and str(offer.get("decision_window_id", "")) == str(window["id"])
		and str(offer.get("actor_id", "")) == actor_id
		and offer.get("capability") is Dictionary
		and valid_id(offer["capability"].get("id"))
		and not str(offer["capability"].get("version", "")).is_empty()
		and _valid_digest(offer.get("descriptor_digest"))
		and not str(offer.get("description", "")).strip_edges().is_empty()
		and _valid_json_value(offer.get("arguments"))
		and semantic_equal(offer.get("expected_epoch"), window["epoch"])
		and safe_integer(offer.get("observation_seq"))
		and int(offer["observation_seq"]) == int(window["observation_seq"])
		and semantic_equal(offer.get("deadline"), window["deadline"])
	)


static func _valid_timepoint(value: Dictionary) -> bool:
	return (
		str(value.get("clock", "")) in ["event", "step", "realtime"]
		and safe_integer(value.get("value"))
	)


static func _valid_digest(value: Variant) -> bool:
	if typeof(value) != TYPE_STRING or str(value).length() != 64:
		return false
	var expression := RegEx.new()
	return (
		expression.compile("^[a-f0-9]{64}$") == OK
		and expression.search(str(value)) != null
	)


static func _valid_json_value(value: Variant) -> bool:
	if value == null or typeof(value) in [TYPE_BOOL, TYPE_STRING]:
		return true
	if typeof(value) in [TYPE_INT, TYPE_FLOAT]:
		return _safe_number(value)
	if value is Array:
		for item in value:
			if not _valid_json_value(item):
				return false
		return true
	if value is Dictionary:
		for key in value:
			if typeof(key) != TYPE_STRING or not _valid_json_value(value[key]):
				return false
		return true
	return false


static func _safe_number(value: Variant) -> bool:
	var number := float(value)
	return is_finite(number) and abs(number) <= MAX_SAFE_INTEGER
