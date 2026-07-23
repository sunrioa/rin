class_name RinClient
extends Node

## Godot 4 adapter for Rin Protocol v1. Add this node as an autoload or child.
## The coroutine-style API never blocks the render thread.

const PROTOCOL_VERSION := "rin.protocol/v1"
const TERMINAL_STATES := ["succeeded", "failed", "stale", "canceled"]
const AMBIGUOUS_PROPOSAL_ERRORS := [
	"proposal_outcome_unknown",
	"job_outcome_unknown",
	"job_cancel_unconfirmed",
	"job_timeout",
	"job_id_persistence_failed",
]
const MAX_SAFE_JSON_INTEGER := 9007199254740991

@export var base_url := "http://127.0.0.1:7374"
@export var token := ""
@export_range(1.0, 120.0, 0.5) var request_timeout_seconds := 5.0
@export_range(1.0, 300.0, 0.5) var job_deadline_seconds := 25.0
@export_range(0.05, 5.0, 0.05) var poll_interval_seconds := 0.1
@export_range(1024, 33554432, 1024) var max_response_bytes := 2097152


func _ready() -> void:
	base_url = base_url.strip_edges().trim_suffix("/")
	var error := _validate_endpoint()
	if not error.is_empty():
		push_error("Rin adapter disabled: " + error)


func health() -> Dictionary:
	return await _json_request(HTTPClient.METHOD_GET, "/health", {}, [200])


func create_session(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/create", request, [200])


func state(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/get", request, [200])


func observe(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/observe", request, [200])


func commit(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/action/commit", request, [200])


func commit_batch(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/action/commit-batch", request, [200])


func set_actor_activity(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/activity", request, [200])


func due_agents(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/scheduler/due", request, [200])


func arbitrate(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/world/arbitrate", request, [200])


func timeline(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/timeline", request, [200])


func replay(request: Dictionary) -> Dictionary:
	return await _json_request(HTTPClient.METHOD_POST, "/v1/session/replay", request, [200])


func snapshot(session_id: String) -> Dictionary:
	return await _json_request(
		HTTPClient.METHOD_POST,
		"/v1/session/snapshot",
		{"protocol_version": PROTOCOL_VERSION, "session_id": session_id},
		[200],
	)


func propose_with_fallback(
	request: Dictionary,
	fallback_action_id: String = "",
	cancel_check: Callable = Callable(),
	known_job_id: String = "",
	persist_job_id: Callable = Callable(),
	allow_offline_before_submit: bool = true,
) -> Dictionary:
	if not known_job_id.is_empty() and not _is_valid_protocol_id(known_job_id):
		return _closed_result("invalid_job")
	var validation_error := _validate_endpoint()
	if not validation_error.is_empty():
		if allow_offline_before_submit and known_job_id.is_empty():
			return _offline_result(request, fallback_action_id, "invalid_endpoint")
		if known_job_id.is_empty():
			return _closed_result("proposal_outcome_unknown")
		return _closed_result("proposal_outcome_unknown", known_job_id)

	var job_id: String = known_job_id
	var recovery_post_used := false
	if job_id.is_empty():
		var submission := await _submit_proposal(request, persist_job_id)
		if not submission.get("ok", false):
			var submission_error := str(submission.get("error_code", "transport_failed"))
			if (
				allow_offline_before_submit
				and submission_error == "transport_unavailable_before_send"
				and not submission.has("status")
			):
				# DNS/connect/TLS setup failed before an HTTP request could reach
				# Rin and no Proposal Job was created. Resumed attempts disable
				# this path even when the current transport is unavailable.
				return _offline_result(request, fallback_action_id, submission_error)
			# A timeout, connection reset, 5xx from a reverse proxy, or an
			# oversized/malformed response may hide a durable job. Never execute
			# a second, offline action after submission began.
			return _closed_result(
				"proposal_outcome_unknown",
				_valid_submission_job_id_or(submission, ""),
			)
		job_id = submission["job_id"]

	var deadline_msec := Time.get_ticks_msec() + int(job_deadline_seconds * 1000.0)
	while Time.get_ticks_msec() < deadline_msec:
		if not _is_valid_protocol_id(job_id):
			return _closed_result("invalid_job")
		if cancel_check.is_valid() and bool(cancel_check.call()):
			return await _cancel_and_resolve(
				request,
				fallback_action_id,
				job_id,
				false,
				"job_cancel_unconfirmed",
			)
		var response := await _json_request(
			HTTPClient.METHOD_GET,
			"/v1/jobs/" + job_id.uri_encode(),
			{},
			[200],
		)
		if not response.get("ok", false):
			if (
				str(response.get("error_code", "")) == "job_not_found"
				and not recovery_post_used
			):
				var recovered := await _submit_proposal(request, persist_job_id)
				recovery_post_used = true
				if not recovered.get("ok", false):
					return _closed_result(
						"proposal_outcome_unknown",
						_valid_submission_job_id_or(recovered, job_id),
					)
				job_id = recovered["job_id"]
				continue
			return _closed_result("job_outcome_unknown", job_id)
		var job = response.get("data")
		if not job is Dictionary:
			return _closed_result("invalid_job", job_id)
		if not _job_matches_request(job, job_id, request):
			return _closed_result("invalid_job_identity", job_id)
		var status_value = job.get("status")
		if typeof(status_value) != TYPE_STRING:
			return _closed_result("invalid_job", job_id)
		var status: String = status_value
		if not _job_shape_matches_status(job, status):
			return _closed_result("invalid_job", job_id)
		if status == "succeeded":
			var proposal = job.get("proposal")
			return (
				_sidecar_result(proposal, job_id)
				if proposal is Dictionary and _proposal_matches_request(proposal, request)
				else _closed_result(
					"invalid_job_identity" if proposal is Dictionary else "invalid_job",
					job_id,
				)
			)
		if status in TERMINAL_STATES:
			var reason := _terminal_error_code(job)
			if reason.is_empty():
				return _closed_result("job_outcome_unknown", job_id)
			if reason == "proposal_outcome_unknown" and not recovery_post_used:
				var recovered := await _submit_proposal(request, persist_job_id)
				recovery_post_used = true
				if not recovered.get("ok", false):
					return _closed_result(
						"proposal_outcome_unknown",
						_valid_submission_job_id_or(recovered, job_id),
					)
				job_id = recovered["job_id"]
				continue
			return _terminal_job_result(
				request,
				fallback_action_id,
				job_id,
				job,
				true,
			)
		if status != "queued" and status != "running":
			return _closed_result("invalid_job", job_id)
		await get_tree().create_timer(poll_interval_seconds).timeout

	return await _cancel_and_resolve(
		request,
		fallback_action_id,
		job_id,
		true,
		"job_outcome_unknown",
	)


func _submit_proposal(
	request: Dictionary,
	persist_job_id: Callable,
) -> Dictionary:
	var submission := await _json_request(
		HTTPClient.METHOD_POST,
		"/v1/jobs/propose",
		request,
		[202],
	)
	if not submission.get("ok", false):
		return submission
	var submission_data = submission.get("data")
	if not submission_data is Dictionary:
		return {"ok": false, "error_code": "invalid_job"}
	var job_id_value = submission_data.get("job_id")
	if not _is_valid_protocol_id(job_id_value):
		return {"ok": false, "error_code": "invalid_job"}
	var job_id: String = job_id_value
	# The game persists the accepted Job ID before polling or returning control.
	# If that durable callback fails, the stable request remains sufficient for
	# a later idempotent POST, but this invocation must fail closed.
	if persist_job_id.is_valid() and not bool(persist_job_id.call(job_id)):
		return {
			"ok": false,
			"error_code": "job_id_persistence_failed",
			"job_id": job_id,
		}
	return {"ok": true, "job_id": job_id}


func _cancel_and_resolve(
	request: Dictionary,
	fallback_action_id: String,
	job_id: String,
	allow_confirmed_terminal_fallback: bool,
	unconfirmed_reason: String,
) -> Dictionary:
	if not _is_valid_protocol_id(job_id):
		return _closed_result("invalid_job")
	var response := await _json_request(
		HTTPClient.METHOD_DELETE,
		"/v1/jobs/" + job_id.uri_encode(),
		{},
		[200],
	)
	if not response.get("ok", false):
		return _closed_result(unconfirmed_reason, job_id)
	var job = response.get("data")
	if not job is Dictionary:
		return _closed_result("invalid_job", job_id)
	if not _job_matches_request(job, job_id, request):
		return _closed_result("invalid_job_identity", job_id)
	var status_value = job.get("status")
	if typeof(status_value) != TYPE_STRING:
		return _closed_result("invalid_job", job_id)
	var status: String = status_value
	if not _job_shape_matches_status(job, status):
		return _closed_result("invalid_job", job_id)
	if status == "succeeded":
		var proposal = job.get("proposal")
		return (
			_sidecar_result(proposal, job_id)
			if proposal is Dictionary and _proposal_matches_request(proposal, request)
			else _closed_result(
				"invalid_job_identity" if proposal is Dictionary else "invalid_job",
				job_id,
			)
		)
	if status in TERMINAL_STATES:
		return _terminal_job_result(
			request,
			fallback_action_id,
			job_id,
			job,
			allow_confirmed_terminal_fallback,
		)
	if status == "queued" or status == "running":
		return _closed_result(unconfirmed_reason, job_id)
	return _closed_result("invalid_job", job_id)


func _terminal_job_result(
	request: Dictionary,
	fallback_action_id: String,
	job_id: String,
	job: Dictionary,
	allow_fallback: bool,
) -> Dictionary:
	var reason := _terminal_error_code(job)
	if reason.is_empty():
		return _closed_result("job_outcome_unknown", job_id)
	if reason in AMBIGUOUS_PROPOSAL_ERRORS:
		return _closed_result(reason, job_id)
	if allow_fallback:
		return _offline_result(request, fallback_action_id, reason, job_id)
	return _closed_result(reason, job_id, "canceled")


func _sidecar_result(proposal: Dictionary, job_id: String) -> Dictionary:
	return {
		"source": "sidecar",
		"committable": true,
		"fallback_reason": "",
		"job_id": job_id,
		"proposal": proposal.duplicate(true),
	}


func _job_matches_request(
	job: Dictionary,
	job_id: String,
	request: Dictionary,
) -> bool:
	return (
		_same_protocol_id(job.get("job_id"), job_id)
		and _same_protocol_id(job.get("session_id"), request.get("session_id"))
		and _same_protocol_id(job.get("request_id"), request.get("request_id"))
	)


func _proposal_matches_request(
	proposal: Dictionary,
	request: Dictionary,
) -> bool:
	return (
		_is_valid_protocol_id(proposal.get("id"))
		and _same_protocol_id(proposal.get("session_id"), request.get("session_id"))
		and _same_protocol_id(proposal.get("request_id"), request.get("request_id"))
		and _same_protocol_id(proposal.get("actor_id"), request.get("actor_id"))
		and _same_json_integer(proposal.get("tick"), request.get("tick"))
		and _proposal_action_matches_request(proposal.get("action"), request)
	)


func _terminal_error_code(job: Dictionary) -> String:
	var detail = job.get("error")
	if not detail is Dictionary:
		return ""
	var code = detail.get("code")
	return String(code) if _is_valid_protocol_id(code) else ""


func _job_shape_matches_status(job: Dictionary, status: String) -> bool:
	var has_proposal := job.has("proposal")
	var has_error := job.has("error")
	if status == "succeeded":
		return has_proposal and job["proposal"] is Dictionary and not has_error
	if status in ["failed", "stale", "canceled"]:
		return not has_proposal and has_error and not _terminal_error_code(job).is_empty()
	if status == "queued" or status == "running":
		return not has_proposal and not has_error
	return false


func _valid_submission_job_id_or(submission: Dictionary, fallback: String) -> String:
	var candidate = submission.get("job_id")
	if not _is_valid_protocol_id(candidate):
		return fallback
	var valid_job_id: String = candidate
	return valid_job_id


func _proposal_action_matches_request(action: Variant, request: Dictionary) -> bool:
	if not _is_valid_action_spec(action):
		return false
	var candidates = request.get("candidate_actions")
	if not candidates is Array:
		return false
	for candidate in candidates:
		if (
			_is_valid_action_spec(candidate)
			and action == candidate
		):
			return true
	return false


func _is_valid_action_spec(value: Variant) -> bool:
	if not value is Dictionary:
		return false
	if (
		not _is_valid_protocol_id(value.get("id"))
		or not _is_valid_protocol_id(value.get("kind"))
		or typeof(value.get("description")) != TYPE_STRING
	):
		return false
	var description: String = value["description"]
	if description.strip_edges().is_empty() or description.length() > 300:
		return false
	var target_ids = value.get("target_ids", [])
	if not target_ids is Array or target_ids.size() > 32:
		return false
	for target_id in target_ids:
		if not _is_valid_protocol_id(target_id):
			return false
	var parameters = value.get("parameters", {})
	if not parameters is Dictionary or parameters.size() > 32:
		return false
	for key in parameters:
		if (
			not _is_valid_protocol_id(key)
			or typeof(parameters[key]) != TYPE_STRING
			or String(parameters[key]).length() > 500
		):
			return false
	return true


func _same_protocol_id(left: Variant, right: Variant) -> bool:
	return (
		_is_valid_protocol_id(left)
		and _is_valid_protocol_id(right)
		and left == right
	)


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


func _same_json_integer(left: Variant, right: Variant) -> bool:
	var left_type := typeof(left)
	var right_type := typeof(right)
	if left_type not in [TYPE_INT, TYPE_FLOAT]:
		return false
	if right_type not in [TYPE_INT, TYPE_FLOAT]:
		return false
	if left_type == TYPE_INT and right_type == TYPE_INT:
		return left >= 0 and right >= 0 and left == right
	if (
		(left_type == TYPE_INT and (left < -MAX_SAFE_JSON_INTEGER or left > MAX_SAFE_JSON_INTEGER))
		or (
			right_type == TYPE_INT
			and (right < -MAX_SAFE_JSON_INTEGER or right > MAX_SAFE_JSON_INTEGER)
		)
	):
		return false
	var left_number := float(left)
	var right_number := float(right)
	return (
		is_finite(left_number)
		and is_finite(right_number)
		and left_number >= 0.0
		and right_number >= 0.0
		and abs(left_number) <= float(MAX_SAFE_JSON_INTEGER)
		and abs(right_number) <= float(MAX_SAFE_JSON_INTEGER)
		and floor(left_number) == left_number
		and floor(right_number) == right_number
		and left_number == right_number
	)


func _closed_result(
	reason: String,
	job_id: String = "",
	source: String = "error",
) -> Dictionary:
	return {
		"source": source,
		"committable": false,
		"fallback_reason": reason.left(96),
		"job_id": job_id,
		"proposal": null,
	}


func _json_request(
	method: HTTPClient.Method,
	path: String,
	payload: Dictionary,
	expected_statuses: Array,
) -> Dictionary:
	var endpoint_error := _validate_endpoint()
	if not endpoint_error.is_empty():
		return {"ok": false, "error_code": "invalid_endpoint"}
	var request := HTTPRequest.new()
	request.use_threads = true
	request.timeout = request_timeout_seconds
	request.body_size_limit = max_response_bytes
	request.max_redirects = 0
	add_child(request)
	var headers := PackedStringArray(["Accept: application/json"])
	if not token.is_empty():
		headers.append("Authorization: Bearer " + token)
	var body := ""
	if method == HTTPClient.METHOD_POST:
		headers.append("Content-Type: application/json")
		body = JSON.stringify(payload)
	var start_error := request.request(base_url + path, headers, method, body)
	if start_error != OK:
		request.queue_free()
		return {"ok": false, "error_code": "transport_unavailable_before_send"}
	var completed: Array = await request.request_completed
	request.queue_free()
	var transport_result: int = completed[0]
	var status: int = completed[1]
	var response_body: PackedByteArray = completed[3]
	if transport_result != HTTPRequest.RESULT_SUCCESS:
		var error_code := (
			"response_too_large"
			if transport_result == HTTPRequest.RESULT_BODY_SIZE_LIMIT_EXCEEDED
			else (
				"transport_unavailable_before_send"
				if transport_result in [
					HTTPRequest.RESULT_CANT_CONNECT,
					HTTPRequest.RESULT_CANT_RESOLVE,
					HTTPRequest.RESULT_TLS_HANDSHAKE_ERROR,
				]
				else "transport_failed"
			)
		)
		return {"ok": false, "error_code": error_code}
	if response_body.size() > max_response_bytes:
		return {"ok": false, "error_code": "response_too_large"}
	var decoded = JSON.parse_string(response_body.get_string_from_utf8())
	if not decoded is Dictionary:
		return {"ok": false, "error_code": "invalid_response"}
	if status not in expected_statuses or decoded.get("ok") != true:
		var detail: Dictionary = decoded.get("error", {})
		return {
			"ok": false,
			"error_code": str(detail.get("code", "http_error")),
			"status": status,
		}
	if not decoded.get("data") is Dictionary:
		return {"ok": false, "error_code": "invalid_response"}
	return {"ok": true, "data": decoded["data"]}


func _offline_result(
	request: Dictionary,
	fallback_action_id: String,
	reason: String,
	job_id: String = "",
) -> Dictionary:
	var candidates = request.get("candidate_actions", [])
	if not candidates is Array or candidates.is_empty():
		return {
			"source": "error",
			"committable": false,
			"fallback_reason": "invalid_request",
			"job_id": job_id,
			"proposal": null,
		}
	var selected: Dictionary
	if fallback_action_id.is_empty():
		selected = candidates[0]
	else:
		for candidate in candidates:
			if candidate is Dictionary and str(candidate.get("id", "")) == fallback_action_id:
				selected = candidate
				break
		if selected.is_empty():
			return {
				"source": "error",
				"committable": false,
				"fallback_reason": "invalid_fallback",
				"job_id": job_id,
				"proposal": null,
			}
	var kind := str(selected.get("kind", ""))
	var stance := kind if kind in ["engage", "partial", "redirect", "refuse", "wait"] else "engage"
	var fingerprint := JSON.stringify({
		"request": request,
		"action_id": selected.get("id", ""),
	}).sha256_text().left(24)
	return {
		"source": "offline",
		"committable": false,
		"fallback_reason": reason.left(96),
		"job_id": job_id,
		"proposal": {
			"id": "offline." + fingerprint,
			"session_id": str(request.get("session_id", "")),
			"request_id": str(request.get("request_id", "")),
			"actor_id": str(request.get("actor_id", "")),
			"tick": maxi(0, int(request.get("tick", 0))),
			"action": selected.duplicate(true),
			"stance": stance,
			"summary": "The game used its authored offline fallback.",
			"rationale": "The Rin Sidecar was unavailable; world state remains game-owned.",
			"policy_source": "adapter-offline",
			"status": "offline",
		},
	}


func _validate_endpoint() -> String:
	if base_url.contains("@") or base_url.contains("?") or base_url.contains("#"):
		return "base URL must not contain credentials, query, or fragment"
	if base_url.begins_with("https://"):
		var remote_pattern := RegEx.new()
		remote_pattern.compile("^https://([A-Za-z0-9.-]+)(?::([0-9]{1,5}))?$")
		var remote_match := remote_pattern.search(base_url)
		if remote_match == null:
			return "HTTPS base URL must be an origin without a path"
		var remote_host := remote_match.get_string(1).to_lower()
		if remote_host.begins_with(".") or remote_host.ends_with(".") or remote_host.contains(".."):
			return "HTTPS base URL has an invalid host"
		var remote_port := remote_match.get_string(2)
		if not remote_port.is_empty() and (int(remote_port) < 1 or int(remote_port) > 65535):
			return "HTTPS base URL has an invalid port"
		if remote_host == "localhost" or remote_host == "127.0.0.1":
			return ""
		return "" if not token.is_empty() else "remote HTTPS endpoints require a token"
	var local_pattern := RegEx.new()
	local_pattern.compile("^http://(127\\.0\\.0\\.1|localhost|\\[::1\\]):([0-9]{1,5})$")
	var local_match := local_pattern.search(base_url)
	if local_match != null:
		var port := int(local_match.get_string(2))
		if port >= 1 and port <= 65535:
			return ""
	return "HTTP is allowed only for an explicit loopback endpoint"
