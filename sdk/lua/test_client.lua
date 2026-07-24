local rin = dofile("sdk/lua/rin.lua")
assert(rin.VERSION == "0.6.0", "client version projection is stale")
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
    local accepted = request.url:match("/v1/jobs/propose$") or request.url:match("/v1/generation/jobs$")
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
    { "create_session", function(done) client:create_session(payload, done) end, "POST", "/v1/session/create" },
    { "observe", function(done) client:observe(payload, done) end, "POST", "/v1/session/observe" },
    { "propose", function(done) client:propose(payload, done) end, "POST", "/v1/agent/propose" },
    { "submit_proposal_job", function(done) client:submit_proposal_job(payload, done) end, "POST", "/v1/jobs/propose" },
    { "get_proposal_job", function(done) client:get_proposal_job("job.fixture", done) end, "GET", "/v1/jobs/job.fixture" },
    { "cancel_proposal_job", function(done) client:cancel_proposal_job("job.fixture", done) end, "DELETE", "/v1/jobs/job.fixture" },
    { "submit_generation_job", function(done) client:submit_generation_job(payload, done) end, "POST", "/v1/generation/jobs" },
    { "get_generation_job", function(done) client:get_generation_job("job.fixture", done) end, "GET", "/v1/generation/jobs/job.fixture" },
    { "cancel_generation_job", function(done) client:cancel_generation_job("job.fixture", done) end, "DELETE", "/v1/generation/jobs/job.fixture" },
    { "commit", function(done) client:commit(payload, done) end, "POST", "/v1/action/commit" },
    { "commit_batch", function(done) client:commit_batch(payload, done) end, "POST", "/v1/action/commit-batch" },
    { "set_actor_activity", function(done) client:set_actor_activity(payload, done) end, "POST", "/v1/session/activity" },
    { "arbitrate", function(done) client:arbitrate(payload, done) end, "POST", "/v1/world/arbitrate" },
    { "state", function(done) client:state(payload, done) end, "POST", "/v1/session/get" },
    { "snapshot", function(done) client:snapshot(payload, done) end, "POST", "/v1/session/snapshot" },
    { "restore", function(done) client:restore(payload, done) end, "POST", "/v1/session/restore" },
    { "timeline", function(done) client:timeline(payload, done) end, "POST", "/v1/session/timeline" },
    { "replay", function(done) client:replay(payload, done) end, "POST", "/v1/session/replay" },
    { "due_agents", function(done) client:due_agents(payload, done) end, "POST", "/v1/scheduler/due" },
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

local manifest_file = assert(io.open("sdk/conformance/routes.json", "rb"))
local manifest = manifest_file:read("*a")
manifest_file:close()
local expected_routes = {}
for name, method, path, status in manifest:gmatch(
    '"name"%s*:%s*"([^"]+)"%s*,%s*"method"%s*:%s*"([^"]+)"%s*,' ..
        '%s*"path"%s*:%s*"([^"]+)"%s*,%s*"status"%s*:%s*(%d+)'
) do
    table.insert(expected_routes, name .. " " .. method .. " " .. path .. " " .. status)
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

local false_bodies = {}
local false_client = assert(rin.new({
    http_fetch = function(request, callback)
        table.insert(false_bodies, request.body)
        callback({ status = 200, body = "{}", headers = {} })
    end,
    json_encode = function(value)
        if value.accepted ~= nil then
            assert(value.accepted == false, "commit accepted=false changed before codec")
            return '{"accepted":false}'
        end
        assert(type(value.items) == "table" and value.items[1].accepted == false)
        return '{"items":[{"accepted":false}]}'
    end,
    json_decode = function() return { ok = true, data = {} } end,
}))
false_client:commit({ accepted = false }, function(data, err) assert(data and not err) end)
false_client:commit_batch(
    { items = { { accepted = false } } },
    function(data, err) assert(data and not err) end
)
assert(false_bodies[1]:find('"accepted":false', 1, true), "commit accepted=false was omitted")
assert(false_bodies[2]:find('"accepted":false', 1, true), "batch accepted=false was omitted")

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
    invalid_client:commit(invalid_payload, function(data, err)
        callback_called = true
        assert(not data and err.code == "invalid_request")
    end)
    assert(callback_called, "invalid JSON payload did not fail synchronously")
end
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
invalid_encoded_client:commit({}, function(data, err)
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

print("Rin Lua SDK tests passed")
