local rin = dofile("sdk/lua/rin.lua")

local last_request
local function fetch(request, callback)
    last_request = request
    local accepted = request.url:match("/v1/jobs/propose$") or request.url:match("/v1/generation/jobs$")
    callback({ status = accepted and 202 or 200, body = "{}", headers = { ["Content-Length"] = "2" } })
end

local client, config_error = rin.new({
    token = "fixture",
    http_fetch = fetch,
    json_encode = function() return "{}" end,
    json_decode = function() return { ok = true, data = { status = "ok" } } end,
})
assert(client, config_error and config_error.message)

local cases = {
    { function(done) client:health(done) end, "GET", "/health" },
    { function(done) client:create_session({}, done) end, "POST", "/v1/session/create" },
    { function(done) client:observe({}, done) end, "POST", "/v1/session/observe" },
    { function(done) client:propose({}, done) end, "POST", "/v1/agent/propose" },
    { function(done) client:submit_proposal_job({}, done) end, "POST", "/v1/jobs/propose" },
    { function(done) client:get_proposal_job("job.fixture", done) end, "GET", "/v1/jobs/job.fixture" },
    { function(done) client:cancel_proposal_job("job.fixture", done) end, "DELETE", "/v1/jobs/job.fixture" },
    { function(done) client:submit_generation_job({}, done) end, "POST", "/v1/generation/jobs" },
    { function(done) client:get_generation_job("job.fixture", done) end, "GET", "/v1/generation/jobs/job.fixture" },
    { function(done) client:cancel_generation_job("job.fixture", done) end, "DELETE", "/v1/generation/jobs/job.fixture" },
    { function(done) client:commit({}, done) end, "POST", "/v1/action/commit" },
    { function(done) client:commit_batch({}, done) end, "POST", "/v1/action/commit-batch" },
    { function(done) client:set_actor_activity({}, done) end, "POST", "/v1/session/activity" },
    { function(done) client:arbitrate({}, done) end, "POST", "/v1/world/arbitrate" },
    { function(done) client:state({}, done) end, "POST", "/v1/session/get" },
    { function(done) client:snapshot({}, done) end, "POST", "/v1/session/snapshot" },
    { function(done) client:restore({}, done) end, "POST", "/v1/session/restore" },
    { function(done) client:timeline({}, done) end, "POST", "/v1/session/timeline" },
    { function(done) client:replay({}, done) end, "POST", "/v1/session/replay" },
    { function(done) client:due_agents({}, done) end, "POST", "/v1/scheduler/due" },
}

for _, test in ipairs(cases) do
    test[1](function(data, err) assert(data and not err) end)
    assert(last_request.method == test[2], "wrong method for " .. test[3])
    assert(last_request.url:sub(-#test[3]) == test[3], "wrong path for " .. test[3])
    assert(last_request.headers.Authorization == "Bearer fixture")
    assert(last_request.follow_redirects == false)
end

client:get_proposal_job(string.char(228, 189, 156, 228, 184, 154), function(data, err)
    assert(not data and err.code == "invalid_identifier")
end)

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
    json_decode = function() return { ok = true, data = { status = "running" } } end,
    schedule = function(seconds, callback) clock = clock + seconds; callback() end,
    now = function() return clock end,
}))
polling_client:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "job_timeout")
end)
assert(canceled, "timed-out job was not canceled")

print("Rin Lua SDK tests passed")
