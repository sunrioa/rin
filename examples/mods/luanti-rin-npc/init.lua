local http_api = core.request_http_api and core.request_http_api()
local modpath = core.get_modpath(core.get_current_modname())

if not http_api then
    core.log("error", "[rin_npc_example] HTTP unavailable; add rin_npc_example to secure.http_mods")
    return
end

local function local_origin(value)
    value = tostring(value or ""):gsub("/$", "")
    if value:match("^http://127%.0%.0%.1:%d+$") or
        value:match("^http://localhost:%d+$") or
        value:match("^http://%[::1%]:%d+$") then
        return value
    end
end

local base_url = local_origin(
    core.settings:get("rin_npc_example.base_url") or "http://127.0.0.1:7374")
if not base_url then
    core.log("error", "[rin_npc_example] base_url must be an explicit loopback HTTP origin")
    return
end

local rin = dofile(modpath .. "/rin.lua")
local state_module = dofile(modpath .. "/state.lua")

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
    if request.headers.Authorization then callback({}); return end
    local extra_headers = {}
    local user_agent = "rin-luanti-example/0.6.0"
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
        if not result.completed or not result.succeeded then callback({}); return end
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

local workflow_state, state_error = state_module.open({
    storage = core.get_mod_storage(),
    encode = encode_json,
    decode = decode_json,
    hash = function(value) return core.sha256(value) end,
    new_world_id = function()
        local entropy = tostring(core.get_us_time()) .. ":" ..
            tostring({}) .. ":" .. tostring(math.random())
        return core.sha256(entropy):sub(1, 32)
    end,
})
if not workflow_state then
    core.log("error", "[rin_npc_example] state rejected: " .. state_error.code)
    return
end

local workflow, workflow_error = rin.new_workflow(client, workflow_state)
if not workflow then
    core.log("error", "[rin_npc_example] workflow rejected: " .. workflow_error.code)
    return
end

local actor_id = "npc.rin.guide"
local allowed_actions = {
    talk = "Guide: Check your supplies, then choose a route with a clear return path.",
    wait = "Guide: Let us observe one more cycle before acting.",
    refuse = "Guide: I cannot help with an action that breaks the world rules.",
}
local ready, creating, waiters, busy = {}, {}, {}, {}

local function game_tick(player)
    return math.max(
        math.floor(core.get_us_time() / 1000),
        tonumber(player and player.last_tick or 0) + 1)
end

local function notify(name, message)
    core.chat_send_player(name, "[Rin] " .. message)
end

local function mark_session_missing(name, err)
    if tostring(err and err.code or "") == "session_not_found" then
        ready[name] = nil
        return true
    end
    return false
end

local function finish_error(name, prefix, err)
    mark_session_missing(name, err)
    busy[name] = nil
    notify(name, prefix .. tostring(err and err.code or "integration_failed"))
end

local function create_session_request(session_id, seed)
    return {
        protocol_version = rin.PROTOCOL_VERSION,
        request_id = "create." .. session_id,
        session_id = session_id,
        binding = {
            game_id = "luanti",
            content_id = "rin-npc-example",
            content_version = "0.6.0",
            content_hash = "sha256:" .. string.rep("0", 64),
        },
        seed = seed,
        features = { "outcome-reporting-v1" },
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
    }
end

local function ensure_session(name, callback)
    local player, player_error = workflow_state:ensure_player(
        name,
        create_session_request)
    if not player then callback(nil, player_error); return end
    if ready[name] then callback(player, nil); return end
    waiters[name] = waiters[name] or {}
    table.insert(waiters[name], callback)
    if creating[name] then return end
    creating[name] = true
    client:create_session(player.create_request, function(_, err)
        local queued = waiters[name] or {}
        waiters[name], creating[name] = nil, nil
        if not err then ready[name] = true end
        for _, waiter in ipairs(queued) do
            waiter(err and nil or workflow_state:get_player(name), err)
        end
    end)
end

local function safe_observe(session_id, operation_id, tick, applied)
    return {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = session_id,
        request_id = "fallback.observe." .. operation_id,
        event_id = "outcome." .. operation_id,
        tick = tick,
        observer_ids = { actor_id },
        source = "luanti-example",
        kind = "action_outcome",
        summary = applied.outcome,
        tags = { "outcome", "degraded-report" },
        importance = 3,
        facts = {
            {
                subject_id = actor_id,
                predicate = "last_action_outcome",
                object = applied.accepted and "accepted" or "rejected",
                visibility = { actor_id },
                confidence = 100,
            },
        },
    }
end

local function outcome_entry(name, operation_id, proposal_id, tick, applied)
    local player = workflow_state:get_player(name)
    local fallback = safe_observe(player.session_id, operation_id, tick, applied)
    if not proposal_id then
        return {
            key = operation_id,
            owner = name,
            kind = "observe",
            request = fallback,
            fallback_observe = fallback,
        }
    end
    return {
        key = operation_id,
        owner = name,
        kind = "commit",
        request = {
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = player.session_id,
            request_id = "commit." .. operation_id,
            proposal_id = proposal_id,
            event_id = "outcome." .. operation_id,
            tick = tick,
            accepted = applied.accepted,
            outcome = applied.outcome,
            tags = { "luanti-example", "conversation" },
        },
        fallback_observe = fallback,
    }
end

local function drain(name, callback)
    workflow:drain_outbox(name, function(count, err)
        mark_session_missing(name, err)
        callback(count, err)
    end)
end

local function settle(name, attempt, proposal, freshness)
    local action = type(proposal.action) == "table" and proposal.action or {}
    local action_id = tostring(action.id or "")
    local line = allowed_actions[action_id]
    local applied
    if freshness ~= "fresh" then
        applied = {
            accepted = false,
            outcome = "The game rejected a stale or unverifiable proposal.",
        }
    elseif not line then
        applied = {
            accepted = false,
            outcome = "The game rejected an action outside its allowlist.",
        }
    else
        applied = { accepted = true, outcome = line }
    end
    local player = workflow_state:get_player(name)
    local occurrence_tick = math.max(
        game_tick(player),
        tonumber(attempt.request.tick) or 0,
        tonumber(proposal.tick) or 0)
    local pending = outcome_entry(
        name,
        attempt.operation_id,
        proposal.id,
        occurrence_tick,
        applied)
    workflow:apply_and_enqueue(
        name,
        attempt,
        pending,
        function()
            if applied.accepted then notify(name, line) end
        end,
        function(_, err)
            if err then
                finish_error(name, "Handled action remains pending: ", err)
                return
            end
            drain(name, function(_, drain_error)
                busy[name] = nil
                if drain_error then
                    finish_error(name, "Outcome remains queued: ", drain_error)
                else
                    notify(name, "Outcome acknowledged.")
                end
            end)
        end)
end

local function apply_offline(name, player, attempt)
    local sequence = attempt and player.sequence or (player.sequence + 1)
    local operation_id = attempt and
        (attempt.operation_id .. ".offline") or
        (player.session_id .. ".offline." .. sequence)
    local line = "Guide (offline): Stay safe, preserve resources, and observe before acting."
    local applied = { accepted = true, outcome = line }
    local pending = outcome_entry(name, operation_id, nil, game_tick(player), applied)
    if attempt then
        workflow:apply_and_enqueue(
            name,
            attempt,
            pending,
            function() notify(name, line) end,
            function(_, err)
                busy[name] = nil
                if err then
                    notify(name, "Offline outcome was not persisted: " .. tostring(err.code))
                else
                    notify(name, "Offline outcome is queued until Rin becomes available.")
                end
            end)
        return
    end
    notify(name, line)
    local persisted, persist_error =
        workflow_state:complete_offline(name, sequence, pending)
    busy[name] = nil
    if not persisted then
        notify(name, "Offline outcome was not persisted: " .. tostring(persist_error.code))
    else
        notify(name, "Offline outcome is queued until Rin becomes available.")
    end
end

local function resume(name)
    local observe = workflow_state:pending_observe(name)
    if not observe then
        finish_error(name, "Pending Observe is missing: ", { code = "invalid_state" })
        return
    end
    client:observe(observe, function(_, observe_error)
        if observe_error then
            finish_error(name, "Observe remains pending: ", observe_error)
            return
        end
        workflow:resume(name, function(resolution, resume_error)
            if resume_error then
                finish_error(name, "Proposal remains unresolved: ", resume_error)
                return
            end
            if resolution.kind == "no_proposal" then
                apply_offline(name, workflow_state:get_player(name), resolution.attempt)
                return
            end
            local proposal = resolution.job.proposal
            client:state({
                protocol_version = rin.PROTOCOL_VERSION,
                session_id = resolution.attempt.request.session_id,
            }, function(session, state_error)
                mark_session_missing(name, state_error)
                settle(
                    name,
                    resolution.attempt,
                    proposal,
                    state_error and "stale" or
                        rin.proposal_freshness(session, proposal))
            end)
        end)
    end)
end

local function begin_turn(name, message, player)
    local sequence = player.sequence + 1
    local operation_id = player.session_id .. "." .. sequence
    local observed_tick = game_tick(player)
    local observe = {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = player.session_id,
        request_id = "observe." .. operation_id,
        event_id = "event." .. operation_id,
        tick = observed_tick,
        observer_ids = { actor_id },
        source = "luanti-example",
        kind = "dialogue",
        summary = message,
        tags = { "conversation", "player-request" },
        importance = 3,
    }
    local propose = {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = player.session_id,
        request_id = "propose." .. operation_id,
        actor_id = actor_id,
        tick = observed_tick + 1,
        intent = "Choose one bounded response to the player.",
        tags = { "conversation" },
        candidate_actions = {
            { id = "talk", kind = "dialogue", description = "offer one concrete hint" },
            { id = "wait", kind = "wait", description = "ask the player to observe first" },
            { id = "refuse", kind = "refuse", description = "decline an unsafe request" },
        },
    }
    local staged, stage_error =
        workflow_state:stage_turn(name, player.create_request, observe)
    if not staged then
        finish_error(name, "Turn state was not staged: ", stage_error)
        return
    end
    local attempt, begin_error = workflow:begin(name, operation_id, propose)
    if not attempt then
        finish_error(name, "Pending Turn was not persisted: ", begin_error)
        return
    end
    resume(name)
end

local function request_turn(name, message)
    if busy[name] then notify(name, "A turn is already running."); return end
    busy[name] = true
    ensure_session(name, function(player, session_error)
        if session_error or not player then
            local retained = workflow_state:load_attempt(name)
            if workflow_state:has_outcomes(name) then
                finish_error(name, "Outcome remains queued: ", session_error)
            elseif retained then
                finish_error(name, "Proposal remains unresolved: ", session_error)
            else
                local offline_player = workflow_state:get_player(name)
                if offline_player then
                    apply_offline(name, offline_player, nil)
                else
                    finish_error(name, "Session unavailable: ", session_error)
                end
            end
            return
        end
        drain(name, function(_, drain_error)
            if drain_error then
                finish_error(name, "Outcome remains queued: ", drain_error)
                return
            end
            if workflow_state:load_attempt(name) then
                resume(name)
            else
                begin_turn(name, message, player)
            end
        end)
    end)
end

core.register_chatcommand("rin_npc", {
    params = "[message]",
    description = "Ask the example Rin guide for one bounded action.",
    func = function(name, param)
        local message = tostring(param or ""):gsub("[%z\r\n]", " ")
            :gsub("%s+", " "):sub(1, 300)
        if message == "" then message = "The player asked what to do next." end
        request_turn(name, message)
        return true, "Rin request started."
    end,
})
