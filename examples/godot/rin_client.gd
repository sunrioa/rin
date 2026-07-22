class_name RinClient
extends Node

## Godot 4 adapter for Rin Protocol v1. Add this node as an autoload or child.
## The coroutine-style API never blocks the render thread.

const PROTOCOL_VERSION := "rin.protocol/v1"
const TERMINAL_STATES := ["succeeded", "failed", "stale", "canceled"]

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
) -> Dictionary:
	var validation_error := _validate_endpoint()
	if not validation_error.is_empty():
		return _offline_result(request, fallback_action_id, "invalid_endpoint")

	var submission := await _json_request(
		HTTPClient.METHOD_POST,
		"/v1/jobs/propose",
		request,
		[202],
	)
	if not submission.get("ok", false):
		return _offline_result(
			request,
			fallback_action_id,
			str(submission.get("error_code", "transport_failed")),
		)
	var job_id := str(submission.get("data", {}).get("job_id", ""))
	if job_id.is_empty():
		return _offline_result(request, fallback_action_id, "invalid_submission")

	var deadline_msec := Time.get_ticks_msec() + int(job_deadline_seconds * 1000.0)
	while Time.get_ticks_msec() < deadline_msec:
		if cancel_check.is_valid() and bool(cancel_check.call()):
			await _json_request(
				HTTPClient.METHOD_DELETE,
				"/v1/jobs/" + job_id.uri_encode(),
				{},
				[200],
			)
			return {
				"source": "canceled",
				"committable": false,
				"fallback_reason": "job_canceled",
				"job_id": job_id,
				"proposal": null,
			}
		var response := await _json_request(
			HTTPClient.METHOD_GET,
			"/v1/jobs/" + job_id.uri_encode(),
			{},
			[200],
		)
		if not response.get("ok", false):
			return _offline_result(
				request,
				fallback_action_id,
				str(response.get("error_code", "transport_failed")),
				job_id,
			)
		var job: Dictionary = response.get("data", {})
		var status := str(job.get("status", ""))
		if status == "succeeded" and job.get("proposal") is Dictionary:
			return {
				"source": "sidecar",
				"committable": true,
				"fallback_reason": "",
				"job_id": job_id,
				"proposal": job["proposal"].duplicate(true),
			}
		if status in TERMINAL_STATES:
			var detail: Dictionary = job.get("error", {})
			return _offline_result(
				request,
				fallback_action_id,
				str(detail.get("code", "job_" + status)),
				job_id,
			)
		if status != "queued" and status != "running":
			return _offline_result(request, fallback_action_id, "invalid_job", job_id)
		await get_tree().create_timer(poll_interval_seconds).timeout

	await _json_request(
		HTTPClient.METHOD_DELETE,
		"/v1/jobs/" + job_id.uri_encode(),
		{},
		[200],
	)
	return _offline_result(request, fallback_action_id, "job_timeout", job_id)


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
		return {"ok": false, "error_code": "transport_failed"}
	var completed: Array = await request.request_completed
	request.queue_free()
	var transport_result: int = completed[0]
	var status: int = completed[1]
	var response_body: PackedByteArray = completed[3]
	if transport_result != HTTPRequest.RESULT_SUCCESS:
		var error_code := (
			"response_too_large"
			if transport_result == HTTPRequest.RESULT_BODY_SIZE_LIMIT_EXCEEDED
			else "transport_failed"
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
	var selected: Dictionary = candidates[0]
	if not fallback_action_id.is_empty():
		for candidate in candidates:
			if candidate is Dictionary and str(candidate.get("id", "")) == fallback_action_id:
				selected = candidate
				break
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
