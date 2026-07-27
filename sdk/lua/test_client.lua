local rin = dofile(
    rawget(_G, "RIN_SDK_TEST_PATH") or "sdk/lua/rin.lua")
assert(rin.VERSION == "0.7.0", "client version projection is stale")
assert(
    rin.DEFAULT_MAX_RESPONSE_BYTES == 32 * 1024 * 1024,
    "default response limit does not match the inline transport budget"
)

local last_request
local last_response_status
local payload = {
    protocol_version = rin.PROTOCOL_VERSION,
    request_id = "request.fixture",
    utf8 = "雨",
}
local function fetch(request, callback)
    last_request = request
    local accepted = request.url:match("/v2/jobs/propose$") or request.url:match("/v2/generation/jobs$")
    last_response_status = accepted and 202 or 200
    callback({ status = last_response_status, body = "{}", headers = { ["Content-Length"] = "2" } })
end

local client, config_error = rin.new({
    token = "fixture",
    http_fetch = fetch,
    json_encode = function(value)
        assert(value.protocol_version == rin.PROTOCOL_VERSION)
        assert(value.request_id == "request.fixture")
        assert(value.utf8 == "雨")
        return '{"protocol_version":"' .. rin.PROTOCOL_VERSION ..
            '","request_id":"request.fixture","utf8":"雨"}'
    end,
    json_decode = function() return { ok = true, data = { status = "ok" } } end,
})
assert(client, config_error and config_error.message)

local cases = {
    { "health", function(done) client:health(done) end, "GET", "/health" },
    { "create_session", function(done) client:create_session(payload, done) end, "POST", "/v2/session/create" },
    { "observe", function(done) client:observe(payload, done) end, "POST", "/v2/session/observe" },
    { "propose", function(done) client:propose(payload, done) end, "POST", "/v2/agent/propose" },
    { "submit_proposal_job", function(done) client:submit_proposal_job(payload, done) end, "POST", "/v2/jobs/propose" },
    { "get_proposal_job", function(done) client:get_proposal_job("job.fixture", done) end, "GET", "/v2/jobs/job.fixture" },
    { "cancel_proposal_job", function(done) client:cancel_proposal_job("job.fixture", done) end, "DELETE", "/v2/jobs/job.fixture" },
    { "submit_generation_job", function(done) client:submit_generation_job(payload, done) end, "POST", "/v2/generation/jobs" },
    { "get_generation_job", function(done) client:get_generation_job("job.fixture", done) end, "GET", "/v2/generation/jobs/job.fixture" },
    { "cancel_generation_job", function(done) client:cancel_generation_job("job.fixture", done) end, "DELETE", "/v2/generation/jobs/job.fixture" },
    { "report_action", function(done) client:report_action(payload, done) end, "POST", "/v2/action/report" },
    { "report_action_batch", function(done) client:report_action_batch(payload, done) end, "POST", "/v2/action/report-batch" },
    { "set_actor_activity", function(done) client:set_actor_activity(payload, done) end, "POST", "/v2/session/activity" },
    { "arbitrate", function(done) client:arbitrate(payload, done) end, "POST", "/v2/world/arbitrate" },
    { "state", function(done) client:state(payload, done) end, "POST", "/v2/session/get" },
    { "session_stats", function(done) client:session_stats(payload, done) end, "POST", "/v2/session/stats" },
    { "archive_session", function(done) client:archive_session(payload, done) end, "POST", "/v2/session/archive" },
    { "delete_session", function(done) client:delete_session(payload, done) end, "POST", "/v2/session/delete" },
    { "snapshot", function(done) client:snapshot(payload, done) end, "POST", "/v2/session/snapshot" },
    { "restore", function(done) client:restore(payload, done) end, "POST", "/v2/session/restore" },
    { "timeline", function(done) client:timeline(payload, done) end, "POST", "/v2/session/timeline" },
    { "replay", function(done) client:replay(payload, done) end, "POST", "/v2/session/replay" },
    { "due_agents", function(done) client:due_agents(payload, done) end, "POST", "/v2/scheduler/due" },
}

local observed_routes = {}
for _, test in ipairs(cases) do
    test[2](function(data, err) assert(data and data.status == "ok" and not err) end)
    assert(last_request.method == test[3], "wrong method for " .. test[4])
    assert(last_request.url:sub(-#test[4]) == test[4], "wrong path for " .. test[4])
    assert(last_request.headers.Authorization == "Bearer fixture")
    assert(last_request.headers["User-Agent"] == "rin-lua/" .. rin.VERSION)
    assert(last_request.follow_redirects == false)
    if test[3] == "POST" then
        assert(last_request.body and last_request.body:find('"utf8":"雨"', 1, true))
    else
        assert(last_request.body == nil)
    end
    local contract_path = last_request.url:match("^https?://[^/]+(.*)$")
    contract_path = contract_path:gsub("job%.fixture", "{job_id}")
    table.insert(
        observed_routes,
        test[1] .. " " .. last_request.method .. " " .. contract_path .. " " ..
            tostring(last_response_status)
    )
end

local manifest_file = assert(io.open(
    rawget(_G, "RIN_SDK_ROUTES_PATH") or
        "sdk/conformance/routes.json",
    "rb"))
local manifest = manifest_file:read("*a")
manifest_file:close()
local expected_routes = {}
for name, method, path, status, profile in manifest:gmatch(
    '"name"%s*:%s*"([^"]+)"%s*,%s*"method"%s*:%s*"([^"]+)"%s*,' ..
        '%s*"path"%s*:%s*"([^"]+)"%s*,%s*"status"%s*:%s*(%d+)%s*,' ..
        '%s*"profile"%s*:%s*"([^"]+)"'
) do
    if profile == "transport" then
        table.insert(expected_routes, name .. " " .. method .. " " .. path .. " " .. status)
    end
end
assert(#expected_routes > 0, "sdk/conformance/routes.json contains no operations")
table.sort(observed_routes)
table.sort(expected_routes)
assert(#observed_routes == #expected_routes, "SDK route count differs from generated contract")
for index, expected in ipairs(expected_routes) do
    assert(
        observed_routes[index] == expected,
        "actual SDK request method/path/status set differs from sdk/conformance/routes.json"
    )
end

local decision_bodies = {}
local decision_client = assert(rin.new({
    http_fetch = function(request, callback)
        table.insert(decision_bodies, request.body)
        callback({ status = 200, body = "{}", headers = {} })
    end,
    json_encode = function(value)
        if value.report ~= nil then
            assert(value.report.decision == "rejected", "report decision changed before codec")
            return '{"report":{"decision":"rejected"}}'
        end
        assert(type(value.reports) == "table" and value.reports[1].decision == "rejected")
        return '{"reports":[{"decision":"rejected"}]}'
    end,
    json_decode = function() return { ok = true, data = {} } end,
}))
decision_client:report_action(
    { report = { decision = "rejected" } },
    function(data, err) assert(data and not err) end
)
decision_client:report_action_batch(
    { reports = { { decision = "rejected" } } },
    function(data, err) assert(data and not err) end
)
assert(
    decision_bodies[1]:find('"decision":"rejected"', 1, true),
    "report decision was omitted"
)
assert(
    decision_bodies[2]:find('"decision":"rejected"', 1, true),
    "batch report decision was omitted"
)

local invalid_transport_calls = 0
local invalid_codec_calls = 0
local invalid_client = assert(rin.new({
    http_fetch = function(_, callback)
        invalid_transport_calls = invalid_transport_calls + 1
        callback({ status = 200, body = "{}", headers = {} })
    end,
    json_encode = function()
        invalid_codec_calls = invalid_codec_calls + 1
        return "{}"
    end,
    json_decode = function() return { ok = true, data = {} } end,
}))
local cyclic_payload = {}
cyclic_payload.self = cyclic_payload
local non_json_key_payload = {}
non_json_key_payload[function() end] = "value"
local invalid_utf8_key_payload = {}
invalid_utf8_key_payload[string.char(0xff)] = "value"
local deep_payload = "leaf"
for _ = 1, 66 do deep_payload = { deep_payload } end
local invalid_payloads = {
    { nested = { { unsafe = 9007199254740992 } } },
    { nested = 0 / 0 },
    { nested = math.huge },
    cyclic_payload,
    { nested = deep_payload },
    { nested = { [1] = "array", named = "object" } },
    { nested = { [1] = "first", [3] = "third" } },
    { nested = non_json_key_payload },
    { nested = string.char(0xff) },
    { nested = invalid_utf8_key_payload },
    { "array root" },
}
for _, invalid_payload in ipairs(invalid_payloads) do
    local callback_called = false
    invalid_client:report_action(invalid_payload, function(data, err)
        callback_called = true
        assert(not data and err.code == "invalid_request")
    end)
    assert(callback_called, "invalid JSON payload did not fail synchronously")
end

local adapter_error_client = assert(rin.new({
    http_fetch = function(_, callback)
        callback(nil, { code = "transport_timeout", message = "untrusted" })
    end,
    json_encode = function() return "{}" end,
    json_decode = function() return {} end,
}))
adapter_error_client:health(function(data, err)
    assert(data == nil and err and err.code == "transport_timeout")
    assert(err.message == "Rin request timed out", "adapter error text crossed the trust boundary")
end)
assert(invalid_transport_calls == 0, "invalid JSON payload reached the transport")
assert(invalid_codec_calls == 0, "invalid JSON payload reached the host codec")

local invalid_encoded_transport_calls = 0
local invalid_encoded_client = assert(rin.new({
    http_fetch = function()
        invalid_encoded_transport_calls = invalid_encoded_transport_calls + 1
    end,
    json_encode = function() return string.char(0xff) end,
    json_decode = function() return { ok = true, data = {} } end,
}))
invalid_encoded_client:report_action({}, function(data, err)
    assert(not data and err.code == "invalid_request", "invalid encoded UTF-8 returned wrong error")
end)
assert(invalid_encoded_transport_calls == 0, "invalid encoded UTF-8 reached the transport")

local invalid_response_decode_calls = 0
local invalid_response_client = assert(rin.new({
    http_fetch = function(_, callback)
        callback({ status = 200, body = string.char(0xff), headers = {} })
    end,
    json_encode = function() return "{}" end,
    json_decode = function()
        invalid_response_decode_calls = invalid_response_decode_calls + 1
        return { ok = true, data = {} }
    end,
}))
invalid_response_client:health(function(data, err)
    assert(not data and err.code == "invalid_response", "invalid response UTF-8 returned wrong error")
end)
assert(invalid_response_decode_calls == 0, "invalid response UTF-8 reached the host codec")

local api_error_client = assert(rin.new({
    http_fetch = function(_, callback)
        callback({ status = 400, body = "api-error", headers = {} })
    end,
    json_encode = function() return "{}" end,
    json_decode = function(body)
        assert(body == "api-error")
        return {
            ok = false,
            error = { code = "invalid_request", message = "safe", field = "actor_id" },
        }
    end,
}))
api_error_client:health(function(data, err)
    assert(not data)
    assert(err.code == "invalid_request")
    assert(err.status == 400)
    assert(err.field == "actor_id")
end)

client:get_proposal_job(string.char(228, 189, 156, 228, 184, 154), function(data, err)
    assert(not data and err.code == "invalid_identifier")
end)

local workflow_store = {
    attempt = nil,
    active = nil,
    outcomes = {},
}
function workflow_store:load_attempt() return self.attempt end
function workflow_store:create_attempt(_, attempt)
    if self.attempt then return false end
    self.attempt = attempt
    return true
end
function workflow_store:save_attempt(_, attempt)
    assert(self.attempt and self.attempt.operation_id == attempt.operation_id)
    self.attempt = attempt
    return true
end
function workflow_store:begin_action(_, attempt, _outcome)
    assert(self.attempt == attempt and not self.active)
    self.active = attempt.operation_id
    return true
end
function workflow_store:complete_action(_, attempt, outcome)
    assert(self.attempt == attempt and self.active == attempt.operation_id)
    self.active = nil
    table.insert(self.outcomes, outcome)
    self.attempt = nil
    return true
end
function workflow_store:settle_without_action(_, attempt, outcome)
    assert(self.attempt == attempt)
    table.insert(self.outcomes, outcome)
    self.attempt = nil
    return true
end
function workflow_store:list_outcomes() return self.outcomes end
function workflow_store:acknowledge_outcome(_, entry)
    assert(self.outcomes[1] == entry)
    table.remove(self.outcomes, 1)
    return true
end

local workflow_epoch = {
    session_id = "session.workflow",
    world_id = "world.workflow",
    host = 1,
    world = 1,
    timeline = 1,
}
local workflow_window = {
    id = "window.workflow",
    mode = "sequential",
    epoch = workflow_epoch,
    observation_seq = 1,
    opened_at = { clock = "event", value = 1 },
    deadline = { clock = "event", value = 2 },
    actor_ids = { "actor.workflow" },
}
local workflow_offer = rin.action_offer({
    offer_id = "offer.workflow",
    actor_id = "actor.workflow",
    capability_id = "dialogue.talk",
    descriptor_digest = string.rep("a", 64),
    description = "Say one authored line.",
    arguments = { authored_action = "offer.workflow" },
}, workflow_window)

local workflow_requests = 0
local workflow_reports = 0
local workflow_ack_session = "session.workflow"
local workflow_client = assert(rin.new({
    http_fetch = function(request, callback)
        workflow_requests = workflow_requests + 1
        if request.url:match("/v2/jobs/propose$") then
            callback({ status = 202, body = "queued", headers = {} })
        elseif request.url:match("/v2/jobs/job%.workflow$") then
            assert(workflow_store.attempt.job_id == "job.workflow")
            callback({ status = 200, body = "succeeded", headers = {} })
        elseif request.url:match("/v2/action/report$") then
            workflow_reports = workflow_reports + 1
            callback({ status = 200, body = "reported", headers = {} })
        else
            error("unexpected workflow route " .. request.url)
        end
    end,
    json_encode = function() return "{}" end,
    json_decode = function(body)
        if body == "queued" then
            return { ok = true, data = { job_id = "job.workflow" } }
        end
        if body == "succeeded" then
            return {
                ok = true,
                data = {
                    job_id = "job.workflow",
                    session_id = "session.workflow",
                    request_id = "request.workflow",
                    status = "succeeded",
                    proposal = {
                        id = "proposal.workflow",
                        session_id = "session.workflow",
                        request_id = "request.workflow",
                        actor_id = "actor.workflow",
                        tick = 2,
                        decision_window = workflow_window,
                        action = workflow_offer,
                    },
                },
            }
        end
        return {
            ok = true,
            data = {
                session_id = workflow_ack_session,
                revision = 3,
                head_hash = string.rep("a", 64),
                duplicate = false,
            },
        }
    end,
}))
local workflow = assert(rin.new_workflow(workflow_client, workflow_store))
local workflow_attempt = assert(workflow:begin(
    "player.fixture",
    "operation.workflow",
    {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = "session.workflow",
        request_id = "request.workflow",
        actor_id = "actor.workflow",
        tick = 2,
        decision_window = workflow_window,
        offers = { workflow_offer },
    }
))
assert(workflow_requests == 0, "Pending Turn was not persisted before network")
local workflow_resolution
workflow:resume("player.fixture", function(result, err)
    assert(result and result.kind == "proposal" and not err)
    workflow_resolution = result
end)
assert(workflow_resolution, "Proposal workflow did not resolve")
local applied_operation
workflow:apply_and_enqueue(
    "player.fixture",
    workflow_resolution.attempt,
    workflow_resolution.job.proposal,
    {
        key = "operation.workflow",
        owner = "player.fixture",
        kind = "report",
        request = {
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = "session.workflow",
            request_id = "report.workflow",
            tick = 3,
            report = {
                proposal_id = "proposal.workflow",
                event_id = "event.workflow",
                decision = "rejected",
                summary = "host declined the offer",
            },
        },
    },
    function(operation_id) applied_operation = operation_id end,
    function(ok, err) assert(ok and not err) end
)
assert(applied_operation == nil, "Rejected action reached the game Apply callback")
workflow_ack_session = "session.other"
workflow:drain_outbox("player.fixture", function(count, err)
    assert(not count and err and err.code == "invalid_outbox_ack")
end)
assert(#workflow_store.outcomes == 1, "wrong-Session ACK removed the Outcome")
workflow_ack_session = nil
workflow:drain_outbox("player.fixture", function(count, err)
    assert(not count and err and err.code == "invalid_outbox_ack")
end)
assert(#workflow_store.outcomes == 1, "missing-Session ACK removed the Outcome")
local valid_outcome_session = workflow_store.outcomes[1].request.session_id
workflow_store.outcomes[1].request.session_id = nil
workflow:drain_outbox("player.fixture", function(count, err)
    assert(not count and err and err.code == "invalid_outbox")
end)
assert(#workflow_store.outcomes == 1, "malformed durable Outcome was removed")
workflow_store.outcomes[1].request.session_id = valid_outcome_session
table.insert(workflow_store.outcomes, {
    key = "operation.workflow.second",
    owner = "player.fixture",
    kind = "report",
    request = {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = "session.workflow",
        request_id = "report.workflow.second",
        tick = 3,
        report = {
            proposal_id = "proposal.workflow",
            event_id = "event.workflow.second",
            decision = "rejected",
            summary = "host declined the second offer",
        },
    },
})
workflow_ack_session = "session.workflow"
workflow:drain_outbox("player.fixture", function(count, err)
    assert(count == 2 and not err)
end)
assert(#workflow_store.outcomes == 0 and workflow_reports == 4)

workflow_store.attempt = workflow_resolution.attempt
local accepted_request = rin.immediate_action_report({
    session_id = "session.workflow",
    request_id = "report.workflow.accepted",
    event_id = "event.workflow.accepted",
    tick = 4,
    proposal = workflow_resolution.job.proposal,
    operation_id = workflow_resolution.attempt.operation_id,
    accepted = true,
    summary = "host applied the offer",
    occurred_at = { clock = "event", value = 4 },
})
local accepted_operation
workflow:apply_and_enqueue(
    "player.fixture",
    workflow_resolution.attempt,
    workflow_resolution.job.proposal,
    {
        key = workflow_resolution.attempt.operation_id,
        owner = "player.fixture",
        kind = "report",
        request = accepted_request,
    },
    function(operation_id)
        assert(workflow_store.active == operation_id,
            "Active Run was not persisted before game Apply")
        accepted_operation = operation_id
    end,
    function(ok, err) assert(ok and not err) end
)
assert(accepted_operation == workflow_resolution.attempt.operation_id)
assert(workflow_store.active == nil and #workflow_store.outcomes == 1)
workflow:drain_outbox("player.fixture", function(count, err)
    assert(count == 1 and not err)
end)
assert(#workflow_store.outcomes == 0 and workflow_reports == 5)

assert(
    rin.proposal_freshness(
        {
            revision = 2,
            proposals = { ["proposal.workflow"] = { status = "pending" } },
        },
        {
            id = "proposal.workflow",
            created_revision = 2,
        }
    ) == "fresh"
)
assert(
    rin.proposal_freshness(
        {
            revision = "2",
            proposals = { ["proposal.workflow"] = { status = 7 } },
        },
        {
            id = "proposal.workflow",
            created_revision = 2,
        }
    ) == "stale",
    "malformed freshness fields were not fail-closed"
)

local function proposal(overrides)
    local value = {
        id = "proposal.fixture",
        session_id = "session.fixture",
        request_id = "request.fixture",
        actor_id = "actor.fixture",
        tick = 7,
    }
    for key, field in pairs(overrides or {}) do value[key] = field end
    return value
end

local function proposal_job(status, overrides)
    local value = {
        job_id = "job.fixture",
        session_id = "session.fixture",
        request_id = "request.fixture",
        status = status or "running",
    }
    for key, field in pairs(overrides or {}) do value[key] = field end
    return value
end

local function generation_job(status, overrides)
    local value = {
        job_id = "job.fixture",
        request_id = "generation.fixture",
        status = status or "running",
    }
    for key, field in pairs(overrides or {}) do value[key] = field end
    return value
end

local remote, remote_error = rin.new({
    base_url = "http://models.example",
    token = "fixture",
    http_fetch = fetch,
    json_encode = function() return "{}" end,
    json_decode = function() return {} end,
})
assert(not remote and remote_error.code == "insecure_base_url")

local clock = 0
local canceled = false
local polling_client = assert(rin.new({
    http_fetch = function(request, callback)
        if request.method == "DELETE" then canceled = true end
        callback({ status = 200, body = "{}", headers = {} })
    end,
    json_encode = function() return "{}" end,
    json_decode = function() return { ok = true, data = proposal_job() } end,
    schedule = function(seconds, callback) clock = clock + seconds; callback() end,
    now = function() return clock end,
}))
polling_client:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "job_timeout")
end)
assert(canceled, "timed-out job was not canceled")

local function make_race_client(cancel_data, result_kind, get_data)
    local race_clock = 0
    local method = "GET"
    local race_client = assert(rin.new({
        http_fetch = function(request, callback)
            method = request.method
            callback({ status = 200, body = "{}", headers = {} })
        end,
        json_encode = function() return "{}" end,
        json_decode = function()
            return {
                ok = true,
                data = method == "DELETE" and cancel_data or
                    get_data or (result_kind == "generation" and generation_job() or proposal_job()),
            }
        end,
        schedule = function(seconds, callback)
            race_clock = race_clock + seconds
            callback()
        end,
        now = function() return race_clock end,
    }))
    return race_client
end

local proposal_race = make_race_client(proposal_job("succeeded", {
    proposal = proposal({ id = "proposal.race" }),
}), "proposal")
proposal_race:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(data and not err)
    assert(data.proposal.id == "proposal.race", "proposal cancellation race result was discarded")
end)

local generation_race = make_race_client(generation_job("succeeded", {
    result = { content = "finished at the deadline" },
}), "generation")
generation_race:wait_for_generation("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(data and not err)
    assert(data.result.content == "finished at the deadline", "generation cancellation race result was discarded")
end)

local terminal_cancel = make_race_client(proposal_job("stale", {
    error = { code = "proposal_stale", message = "World changed" },
}), "proposal")
terminal_cancel:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "proposal_stale", "terminal cancellation result became job_timeout")
end)

local invalid_race = make_race_client(proposal_job("succeeded"), "proposal")
invalid_race:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "invalid_job", "successful proposal without payload was accepted")
end)

local crossed_get = make_race_client(
    proposal_job("canceled"),
    "proposal",
    proposal_job("running", { job_id = "job.other" })
)
crossed_get:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "invalid_job", "crossed GET job identity was accepted")
end)

local malformed_delete = make_race_client(proposal_job("succeeded", {
    proposal = proposal({ tick = 9007199254740992 }),
}), "proposal")
malformed_delete:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "invalid_job", "malformed DELETE proposal identity was accepted")
end)

local helper_epoch = {
    session_id = "session.helper",
    world_id = "world.helper",
    host = 1,
    world = 2,
    timeline = 3,
}
local helper_timepoint = { clock = "event", value = 8 }
local helper_window = {
    id = "window.helper",
    epoch = helper_epoch,
    observation_seq = 7,
    deadline = helper_timepoint,
}
local helper_offer = rin.action_offer({
    offer_id = "offer.helper",
    actor_id = "actor.helper",
    capability_id = "dialogue.say",
    descriptor_digest = string.rep("a", 64),
    description = "Say one line",
    arguments = { line = "hello" },
}, helper_window)
local helper_report = rin.immediate_action_report({
    session_id = "session.helper",
    request_id = "report.helper",
    event_id = "event.helper",
    tick = 8,
    proposal = { id = "proposal.helper", action = helper_offer },
    operation_id = "operation.helper",
    accepted = true,
    summary = "applied",
    epoch = helper_epoch,
    world_seq = 8,
    occurred_at = helper_timepoint,
})
assert(helper_report.report.invocation.offer_id == "offer.helper")
assert(helper_report.report.run.status == "succeeded")
assert(helper_report.report.outcome.operation_id == "operation.helper")

print("Rin Lua SDK tests passed")
