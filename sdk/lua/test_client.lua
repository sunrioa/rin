local rin = dofile(rawget(_G, "RIN_SDK_TEST_PATH") or "sdk/lua/rin.lua")

assert(rin.VERSION == "0.7.0", "client version projection is stale")
assert(rin.CONTRACT_VERSION == "rin.control/v2", "Control contract drifted")
assert(rin.DEFAULT_BASE_URL == "http://127.0.0.1:7375", "Control port drifted")
assert(rin.DEFAULT_MAX_RESPONSE_BYTES == 8 * 1024 * 1024, "response limit drifted")

local token = "control-fixture-token-32-bytes!!"
local mode = "normal"
local last_request
local last_encoded

local function json_encode(value)
    last_encoded = value
    return "{}"
end

local function json_decode(body)
    if body == "invalid" then error("invalid JSON") end
    if body == "scalar" then return true end
    if body == "api-error" then return { code = "stale", error = "world changed" } end
    if body == "wrong-contract" then return { contract_version = "rin.control/v1" } end
    if body == "info" then
        return { contract_version = "rin.control/v2", principal = { id = "principal.fixture" } }
    end
    if body == "list" then return { { id = "fixture" } } end
    return { status = "ok" }
end

local function fetch(request, callback)
    last_request = request
    if mode == "transport_timeout" then
        callback(nil, { code = "transport_timeout", message = "untrusted" })
        return
    end
    local status = mode == "api_error" and 409 or mode == "redirect" and 302 or 200
    local body
    if mode == "api_error" then body = "api-error"
    elseif mode == "invalid_json" then body = "invalid"
    elseif mode == "scalar" then body = "scalar"
    elseif mode == "wrong_contract" then body = "wrong-contract"
    elseif mode == "oversized" then body = string.rep("x", 2048)
    elseif request.url:match("/control/v2/info$") then body = "info"
    elseif request.url:match("/control/v2/worlds$") or request.url:match("/control/v2/actors$") then body = "list"
    else body = "ok" end
    local content_type = mode == "wrong_content_type" and "text/plain" or "application/json; charset=utf-8"
    callback({
        status = status,
        body = body,
        headers = {
            ["Content-Type"] = content_type,
            ["Content-Length"] = tostring(#body),
        },
    })
end

local client, config_error = rin.new({
    token = token,
    http_fetch = fetch,
    json_encode = json_encode,
    json_decode = json_decode,
})
assert(client, config_error and config_error.message)

local function invoke(operation)
    local called, result, err = false, nil, nil
    operation(function(value, failure)
        assert(not called, "callback was delivered more than once")
        called, result, err = true, value, failure
    end)
    assert(called, "fixture transport did not finish synchronously")
    return result, err
end

local actor = { host_id = "host.fixture", world_id = "world.fixture", actor_id = "actor.fixture" }
local world = { host_id = "host.fixture", world_id = "world.fixture" }
local operation = { operation_id = "operation.fixture" }
local cases = {
    { function(done) client:info(done) end, "GET", "/control/v2/info", nil },
    { function(done) client:list_worlds(done) end, "POST", "/control/v2/worlds", true },
    { function(done) client:list_actors(world, done) end, "POST", "/control/v2/actors", world },
    { function(done) client:get_actor(actor, done) end, "POST", "/control/v2/actor", actor },
    { function(done) client:wait_actor(actor, done) end, "POST", "/control/v2/wait-actor", actor },
    { function(done) client:observe_actor(actor, done) end, "POST", "/control/v2/observe", actor },
    { function(done) client:list_capabilities(actor, done) end, "POST", "/control/v2/capabilities", actor },
    { function(done) client:describe_capability(actor, done) end, "POST", "/control/v2/capability", actor },
    { function(done) client:acquire_controller(actor, done) end, "POST", "/control/v2/controllers/acquire", actor },
    { function(done) client:renew_controller(actor, done) end, "POST", "/control/v2/controllers/renew", actor },
    { function(done) client:release_controller(actor, done) end, "POST", "/control/v2/controllers/release", actor },
    { function(done) client:get_controller(actor, done) end, "POST", "/control/v2/controllers/get", actor },
    { function(done) client:submit_action(actor, done) end, "POST", "/control/v2/actions/submit", actor },
    { function(done) client:confirm_action(operation, done) end, "POST", "/control/v2/actions/confirm", operation },
    { function(done) client:get_operation(operation, done) end, "POST", "/control/v2/operations/get", operation },
    { function(done) client:wait_operation(operation, done) end, "POST", "/control/v2/operations/wait", operation },
    { function(done) client:cancel_operation(operation, done) end, "POST", "/control/v2/operations/cancel", operation },
    { function(done) client:set_emergency_stop(actor, done) end, "POST", "/control/v2/emergency-stop", actor },
}

for _, test in ipairs(cases) do
    local result, err = invoke(test[1])
    assert(result and not err, "Control route failed: " .. test[3])
    assert(last_request.method == test[2], "wrong method for " .. test[3])
    assert(last_request.url == rin.DEFAULT_BASE_URL .. test[3], "wrong URL for " .. test[3])
    assert(last_request.headers.Authorization == "Bearer " .. token, "missing bearer token")
    assert(last_request.headers["User-Agent"] == "rin-control-lua/" .. rin.VERSION, "wrong user agent")
    assert(last_request.follow_redirects == false, "redirects must stay disabled")
    if test[2] == "POST" then
        assert(last_request.body == "{}", "POST body was not encoded")
        if test[4] ~= true then assert(last_encoded == test[4], "payload identity changed") end
    else
        assert(last_request.body == nil, "GET request unexpectedly had a body")
    end
end

local failure_modes = {
    { "wrong_contract", function(done) client:info(done) end, "control_contract_mismatch" },
    { "api_error", function(done) client:get_actor(actor, done) end, "stale" },
    { "wrong_content_type", function(done) client:get_actor(actor, done) end, "invalid_response" },
    { "redirect", function(done) client:get_actor(actor, done) end, "redirect_rejected" },
    { "invalid_json", function(done) client:get_actor(actor, done) end, "invalid_response" },
    { "scalar", function(done) client:get_actor(actor, done) end, "invalid_response" },
    { "transport_timeout", function(done) client:get_actor(actor, done) end, "transport_timeout" },
}
for _, test in ipairs(failure_modes) do
    mode = test[1]
    local result, err = invoke(test[2])
    assert(not result and err and err.code == test[3], "wrong failure for " .. mode)
end
mode = "normal"

local bounded = assert(rin.new({
    token = token,
    max_response_bytes = 1024,
    http_fetch = fetch,
    json_encode = json_encode,
    json_decode = json_decode,
}))
mode = "oversized"
local result, err = invoke(function(done) bounded:get_actor(actor, done) end)
assert(not result and err.code == "response_too_large", "oversized response was accepted")
mode = "normal"

local transport_calls = 0
local strict = assert(rin.new({
    token = token,
    http_fetch = function(request, done)
        transport_calls = transport_calls + 1
        fetch(request, done)
    end,
    json_encode = json_encode,
    json_decode = json_decode,
}))
local cyclic = {}
cyclic.self = cyclic
local sparse = { [1] = "one", [3] = "three" }
local mixed = { [1] = "one", named = "value" }
local invalid_payloads = {
    { "array-root" },
    { unsafe = 9007199254740992 },
    { number = 0 / 0 },
    { number = math.huge },
    cyclic,
    sparse,
    mixed,
    { text = string.char(0xff) },
}
for _, payload in ipairs(invalid_payloads) do
    local value, payload_error = invoke(function(done) strict:get_actor(payload, done) end)
    assert(not value and payload_error.code == "invalid_request", "invalid JSON payload was accepted")
end
assert(transport_calls == 0, "invalid payload reached the transport")

local bad_encoder = assert(rin.new({
    token = token,
    http_fetch = fetch,
    json_encode = function() return "[]" end,
    json_decode = json_decode,
}))
result, err = invoke(function(done) bad_encoder:get_actor(actor, done) end)
assert(not result and err.code == "invalid_request", "array JSON root was accepted")

for _, base_url in ipairs({
    "https://127.0.0.1:7375",
    "http://example.com:7375",
    "http://127.0.0.1",
    "http://127.0.0.1:7375/path",
    "http://user@127.0.0.1:7375",
}) do
    local invalid, invalid_error = rin.new({
        base_url = base_url,
        token = token,
        http_fetch = fetch,
        json_encode = json_encode,
        json_decode = json_decode,
    })
    assert(not invalid and invalid_error.code == "invalid_base_url", "unsafe base URL was accepted")
end

local invalid, invalid_error = rin.new({
    token = "short",
    http_fetch = fetch,
    json_encode = json_encode,
    json_decode = json_decode,
})
assert(not invalid and invalid_error.code == "invalid_token", "short token was accepted")

print("Lua Control V2 SDK tests passed.")
