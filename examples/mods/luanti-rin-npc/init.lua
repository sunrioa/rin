local http_api = core.request_http_api and core.request_http_api()
local modpath = core.get_modpath(core.get_current_modname())

if not http_api then
    core.log("error", "[rin_npc_example] HTTP access unavailable; add this mod to secure.http_mods")
    return
end

local function local_origin(value)
    value = tostring(value or ""):gsub("/$", "")
    if value:match("^http://127%.0%.0%.1:%d+$") or value:match("^http://localhost:%d+$") or
        value:match("^http://%[::1%]:%d+$") then
        return value
    end
    return nil
end

local base_url = local_origin(core.settings:get("rin_npc_example.base_url") or "http://127.0.0.1:7374")
if not base_url then
    core.log("error", "[rin_npc_example] base_url must be an explicit loopback HTTP origin")
    return
end

local rin = dofile(modpath .. "/rin.lua")

local function encode_json(value)
    local encoded, err = core.write_json(value)
    if not encoded then error(err or "JSON encoding failed") end
    return encoded
end

local function decode_json(value)
    local decoded, err = core.parse_json(value, nil, true)
    if decoded == nil then error(err or "JSON decoding failed") end
    return decoded
end

local function fetch(request, callback)
    if request.headers.Authorization then
        callback({})
        return
    end
    local extra_headers = {}
    local user_agent = "rin-luanti-example/0.1"
    for key, value in pairs(request.headers) do
        if key:lower() == "user-agent" then
            user_agent = value
        else
            table.insert(extra_headers, key .. ": " .. value)
        end
    end
    http_api.fetch({
        url = request.url,
        timeout = request.timeout,
        method = request.method,
        data = request.body,
        user_agent = user_agent,
        extra_headers = extra_headers,
        quiet = true,
    }, function(result)
        if not result.completed or not result.succeeded then
            callback({})
            return
        end
        callback({ status = result.code, body = result.data or "", headers = {} })
    end)
end

local client, client_error = rin.new({
    base_url = base_url,
    http_fetch = fetch,
    json_encode = encode_json,
    json_decode = decode_json,
    schedule = core.after,
    now = function() return core.get_us_time() / 1000000 end,
})
if not client then
    core.log("error", "[rin_npc_example] configuration rejected: " .. client_error.code)
    return
end

local actor_id = "npc.rin.guide"
local allowed_actions = {
    talk = "Guide: Check your supplies, then choose a route with a clear return path.",
    wait = "Guide: Let us observe one more cycle before acting.",
    refuse = "Guide: I cannot help with an action that breaks the world rules.",
}
local sessions = {}
local busy = {}
local sequence = 0
local run_id = tostring(core.get_us_time()):gsub("[^0-9]", ""):sub(-12)

local function next_turn()
    sequence = sequence + 1
    return sequence
end

local function safe_id(value)
    return tostring(value):gsub("[^A-Za-z0-9._-]", "_"):sub(1, 48)
end

local function notify(name, message)
    core.chat_send_player(name, "[Rin] " .. message)
end

local function failed(name, err)
    busy[name] = nil
    notify(name, "Request failed: " .. tostring(err and err.code or "integration_failed"))
end

local function ensure_session(name, callback)
    local existing = sessions[name]
    if existing and existing.ready then
        callback(existing.id)
        return
    end
    if existing then
        table.insert(existing.waiters, callback)
        return
    end

    local turn = next_turn()
    local entry = {
        id = "luanti." .. run_id .. "." .. safe_id(name),
        ready = false,
        waiters = { callback },
    }
    sessions[name] = entry
    client:create_session({
        protocol_version = rin.PROTOCOL_VERSION,
        request_id = "create." .. turn,
        session_id = entry.id,
        binding = {
            game_id = "luanti",
            content_id = "rin-npc-example",
            content_version = "0.1.0",
            content_hash = "sha256:" .. string.rep("0", 64),
        },
        seed = turn,
        actors = {
            {
                id = actor_id,
                kind = "npc",
                display_name = "Rin Guide",
                traits = { "observant", "careful" },
                boundaries = {
                    {
                        id = "boundary.no-griefing",
                        description = "Never suggest griefing or bypassing server rules.",
                        trigger_tags = { "unsafe" },
                        response = "refuse",
                    },
                },
                goals = {
                    {
                        id = "goal.help-player",
                        description = "Help the player make one informed choice.",
                        priority = 4,
                        preferred_actions = { "talk" },
                        progress = 0,
                        target_progress = 3,
                        status = "active",
                    },
                },
                think_every_ticks = 20,
                enabled = true,
            },
        },
    }, function(_, err)
        local waiters = entry.waiters
        entry.waiters = {}
        if err then
            sessions[name] = nil
            for _, waiter in ipairs(waiters) do waiter(nil, err) end
            return
        end
        entry.ready = true
        for _, waiter in ipairs(waiters) do waiter(entry.id, nil) end
    end)
end

local function apply_and_commit(name, session_id, turn, tick, job)
    local proposal = type(job.proposal) == "table" and job.proposal or {}
    local action = type(proposal.action) == "table" and proposal.action or {}
    local action_id = tostring(action.id or "")
    local line = allowed_actions[action_id]
    if type(proposal.proposal_id) ~= "string" then
        failed(name, { code = "invalid_response" })
        return
    end
    local accepted = line ~= nil
    local outcome = line or "The game rejected an action outside its allowlist."

    core.after(0, function()
        if accepted then notify(name, line) end
        client:commit({
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = session_id,
            request_id = "commit." .. turn,
            proposal_id = proposal.proposal_id,
            event_id = "outcome." .. turn,
            tick = tick,
            accepted = accepted,
            outcome = outcome,
            tags = { "luanti-example", "conversation" },
        }, function(_, err)
            busy[name] = nil
            if err then failed(name, err) else notify(name, "Turn committed.") end
        end)
    end)
end

local function request_turn(name, message)
    if busy[name] then
        notify(name, "A turn is already running.")
        return
    end
    busy[name] = true
    ensure_session(name, function(session_id, session_error)
        if session_error or not session_id then failed(name, session_error); return end
        local turn = next_turn()
        local tick = turn * 3
        client:observe({
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = session_id,
            request_id = "observe." .. turn,
            event_id = "event." .. turn,
            tick = tick,
            observer_ids = { actor_id },
            source = "luanti-example",
            kind = "dialogue",
            summary = message,
            tags = { "conversation", "player-request" },
            importance = 3,
        }, function(_, observe_error)
            if observe_error then failed(name, observe_error); return end
            client:submit_proposal_job({
                protocol_version = rin.PROTOCOL_VERSION,
                session_id = session_id,
                request_id = "propose." .. turn,
                actor_id = actor_id,
                tick = tick + 1,
                intent = "Choose one bounded response to the player.",
                tags = { "conversation" },
                candidate_actions = {
                    { id = "talk", kind = "dialogue", description = "offer one concrete hint" },
                    { id = "wait", kind = "wait", description = "ask the player to observe first" },
                    { id = "refuse", kind = "refuse", description = "decline an unsafe request" },
                },
            }, function(queued, queue_error)
                if queue_error then failed(name, queue_error); return end
                client:wait_for_proposal(queued.job_id, nil, function(job, job_error)
                    if job_error then failed(name, job_error); return end
                    apply_and_commit(name, session_id, turn, tick + 2, job)
                end)
            end)
        end)
    end)
end

core.register_chatcommand("rin_npc", {
    params = "[message]",
    description = "Ask the example Rin guide for one bounded action.",
    func = function(name, param)
        local message = tostring(param or ""):gsub("[%z\r\n]", " "):gsub("%s+", " "):sub(1, 300)
        if message == "" then message = "The player asked what to do next." end
        request_turn(name, message)
        return true, "Rin request started."
    end,
})
