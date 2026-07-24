local rin = dofile("sdk/lua/rin.lua")
assert(
    rin.DEFAULT_MAX_RESPONSE_BYTES == 32 * 1024 * 1024,
    "default response limit does not match the inline transport budget"
)

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
    proposal = proposal({ tick = 1.5 }),
}), "proposal")
malformed_delete:wait_for_proposal("job.fixture", { deadline = 0.05, interval = 0.01 }, function(data, err)
    assert(not data and err.code == "invalid_job", "malformed DELETE proposal identity was accepted")
end)

print("Rin Lua SDK tests passed")
