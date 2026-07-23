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
local terminal_commit_errors = {
    session_not_found = true,
    unknown_proposal = true,
    proposal_resolved = true,
}
local sessions = {}
local busy = {}
local applied_operations = {}
local outcome_outbox = {}
local proposal_attempts = {}
local run_id = tostring(core.get_us_time()):gsub("[^0-9]", "")

local function game_tick()
    -- Milliseconds from Luanti's monotonic clock are used consistently for
    -- Observe and for the actual authoritative accept/reject occurrence.
    return math.floor(core.get_us_time() / 1000)
end

local function safe_id(value)
    return tostring(value):gsub("[^A-Za-z0-9._-]", "_"):sub(1, 40)
end

local function notify(name, message)
    core.chat_send_player(name, "[Rin] " .. message)
end

local function mark_session_missing(name, err)
    if tostring(err and err.code or "") ~= "session_not_found" then return false end
    local entry = sessions[name]
    if entry then entry.ready = false end
    return true
end

local function failed(name, err)
    mark_session_missing(name, err)
    busy[name] = nil
    notify(name, "Request failed: " .. tostring(err and err.code or "integration_failed"))
end

local function has_pending_outcome(name)
    for _, pending in pairs(outcome_outbox) do
        if pending.name == name then return true end
    end
    return false
end

local function persist_new_proposal_attempt(name, attempt)
    -- PRODUCTION PERSISTENCE HOOK: durably insert the complete, immutable
    -- Propose request before its first POST. Store it with run_id and sequence
    -- so a restart can resume the exact identity.
    return proposal_attempts[name] == nil and attempt.name == name
end

local function persist_proposal_job_id(name, attempt, job_id)
    -- PRODUCTION PERSISTENCE HOOK: durably attach the Job ID immediately after
    -- a 202 response and before beginning GET/DELETE reconciliation.
    return proposal_attempts[name] == attempt and type(job_id) == "string"
end

local function create_session_request(entry)
    return {
        protocol_version = rin.PROTOCOL_VERSION,
        request_id = "create." .. entry.id,
        session_id = entry.id,
        binding = {
            game_id = "luanti",
            content_id = "rin-npc-example",
            content_version = "0.1.0",
            content_hash = "sha256:" .. string.rep("0", 64),
        },
        seed = entry.seed,
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
    local entry = sessions[name]
    if not entry then
        entry = {
            id = "luanti." .. run_id .. "." .. safe_id(name),
            seed = core.get_us_time(),
            ready = false,
            creating = false,
            waiters = {},
            sequence = 0,
        }
        -- Retain the complete payload. Every retry reuses the same request_id,
        -- seed, binding, actor seed, and features.
        entry.create_request = create_session_request(entry)
        sessions[name] = entry
    end
    if entry.ready then
        callback(entry.id)
        return
    end
    table.insert(entry.waiters, callback)
    if entry.creating then return end
    entry.creating = true

    client:create_session(entry.create_request, function(_, err)
        local waiters = entry.waiters
        entry.waiters = {}
        entry.creating = false
        if err then
            -- Keep entry.create_request unchanged for an idempotent retry.
            for _, waiter in ipairs(waiters) do waiter(nil, err) end
            return
        end
        entry.ready = true
        for _, waiter in ipairs(waiters) do waiter(entry.id, nil) end
    end)
end

local function persist_outbox_acknowledgement(operation_id, acknowledged)
    -- PRODUCTION PERSISTENCE HOOK: durably delete the acknowledged Outbox
    -- entry and return false on failure. In-memory eviction happens later.
    return outcome_outbox[operation_id] == acknowledged
end

local function persist_outbox_conversion(operation_id, original, converted)
    -- PRODUCTION PERSISTENCE HOOK: atomically replace only an explicitly
    -- unrecoverable Commit with its pre-recorded safe Observe. Return false on
    -- save failure so the exact original Commit stays retryable.
    return outcome_outbox[operation_id] == original and converted.kind == "observe"
end

local function acknowledge_outcome(operation_id, pending, callback)
    -- Durable ACK/delete succeeds before in-memory eviction.
    if not persist_outbox_acknowledgement(operation_id, pending) then
        callback({ code = "outbox_ack_failed" })
        return
    end
    if outcome_outbox[operation_id] == pending then
        outcome_outbox[operation_id] = nil
    end
    callback(nil)
end

local function report_outcome(operation_id, callback)
    local pending = outcome_outbox[operation_id]
    if not pending then
        callback(nil)
        return
    end

    if pending.kind == "observe" then
        client:observe(pending.request, function(_, err)
            if err then
                mark_session_missing(pending.name, err)
                callback(err)
                return
            end
            acknowledge_outcome(operation_id, pending, callback)
        end)
        return
    end

    client:commit(pending.request, function(_, err)
        if not err then
            acknowledge_outcome(operation_id, pending, callback)
            return
        end
        mark_session_missing(pending.name, err)
        if not terminal_commit_errors[tostring(err.code)] then
            -- Temporary failures keep the exact Commit unchanged.
            callback(err)
            return
        end

        local converted = {
            name = pending.name,
            session_id = pending.session_id,
            kind = "observe",
            request = pending.degraded_observe,
            degraded_observe = pending.degraded_observe,
        }
        if not persist_outbox_conversion(operation_id, pending, converted) then
            callback({ code = "outbox_conversion_failed" })
            return
        end
        outcome_outbox[operation_id] = converted

        if tostring(err.code) == "session_not_found" then
            mark_session_missing(pending.name, err)
            -- Next entry recreates from the exact retained payload, then
            -- flushes this Observe before starting a new turn.
            callback(err)
            return
        end
        client:observe(converted.request, function(_, observe_error)
            if observe_error then
                mark_session_missing(pending.name, observe_error)
                callback(observe_error)
                return
            end
            acknowledge_outcome(operation_id, converted, callback)
        end)
    end)
end

local function flush_outcome_outbox(name, callback)
    local operation_ids = {}
    for operation_id, pending in pairs(outcome_outbox) do
        if pending.name == name then table.insert(operation_ids, operation_id) end
    end
    table.sort(operation_ids)
    local index = 1
    local function report_next(err)
        if err then callback(err); return end
        local operation_id = operation_ids[index]
        if not operation_id then callback(nil); return end
        index = index + 1
        report_outcome(operation_id, report_next)
    end
    report_next(nil)
end

local function safe_observe_request(session_id, operation_id, tick, applied)
    -- Degraded reports contain episodic memory plus an absolute fact only.
    -- They never replay relative goal/progress deltas.
    return {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = session_id,
        request_id = "fallback.observe." .. operation_id,
        -- Commit and degraded Observe describe one occurrence and deliberately
        -- share the same idempotency event ID.
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

local function commit_pending(name, session_id, operation_id, proposal_id, tick, applied)
    local degraded_observe = safe_observe_request(
        session_id, operation_id, tick, applied)
    return {
        name = name,
        session_id = session_id,
        kind = "commit",
        request = {
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = session_id,
            request_id = "commit." .. operation_id,
            proposal_id = proposal_id,
            event_id = "outcome." .. operation_id,
            tick = tick,
            accepted = applied.accepted,
            outcome = applied.outcome,
            tags = { "luanti-example", "conversation" },
        },
        degraded_observe = degraded_observe,
    }
end

local function observe_pending(name, session_id, operation_id, tick, applied)
    local observe = safe_observe_request(session_id, operation_id, tick, applied)
    return {
        name = name,
        session_id = session_id,
        kind = "observe",
        request = observe,
        degraded_observe = observe,
    }
end

local function persist_authoritative_transaction(
    operation_id, applied, pending, apply_game_state, resolved_attempt, consumed_sequence)
    -- PRODUCTION PERSISTENCE HOOK: replace this body with one fallible, atomic
    -- game/ModStorage transaction. The actual game mutation, applied marker,
    -- complete Commit plus degraded-Observe Outbox entry, retained Create
    -- request, run_id, sequence, and removal of the matching unresolved
    -- Proposal attempt must commit or roll back together. The demo removes
    -- marker/outbox state when its effect callback throws, but only a real
    -- game transaction can undo an already-partial world mutation.
    if applied_operations[operation_id] then return true end
    if resolved_attempt and proposal_attempts[resolved_attempt.name] ~= resolved_attempt then
        return false, "proposal attempt changed before the game transaction"
    end
    local sequence_entry = sessions[pending.name]
    local prior_sequence = sequence_entry and sequence_entry.sequence or 0
    applied_operations[operation_id] = applied
    outcome_outbox[operation_id] = pending
    if resolved_attempt then proposal_attempts[resolved_attempt.name] = nil end
    if consumed_sequence and sequence_entry then
        sequence_entry.sequence = math.max(sequence_entry.sequence, consumed_sequence)
    end
    local ok, err = pcall(apply_game_state)
    if not ok then
        if outcome_outbox[operation_id] == pending then
            outcome_outbox[operation_id] = nil
        end
        if applied_operations[operation_id] == applied then
            applied_operations[operation_id] = nil
        end
        if resolved_attempt and proposal_attempts[resolved_attempt.name] == nil then
            proposal_attempts[resolved_attempt.name] = resolved_attempt
        end
        if consumed_sequence and sequence_entry then
            sequence_entry.sequence = prior_sequence
        end
        return false, err
    end
    return true
end

local function proposal_is_fresh(state, proposal)
    local proposals = type(state.proposals) == "table" and state.proposals or {}
    local retained = type(proposals[proposal.id]) == "table" and proposals[proposal.id] or {}
    if tostring(retained.status or "") ~= "pending" then return "stale" end
    local raw_world_revision = proposal.based_on_world_revision
    local based_on_world_revision = tonumber(raw_world_revision)
    if raw_world_revision ~= nil and not based_on_world_revision then return "stale" end
    based_on_world_revision = based_on_world_revision or 0
    if based_on_world_revision > 0 then
        local current = tonumber(state.world_revision)
        if current and current >= 0 and current == math.floor(current) and
            based_on_world_revision == math.floor(based_on_world_revision) and
            current == based_on_world_revision then
            return "fresh"
        end
        return "stale"
    end
    local revision = tonumber(state.revision)
    local created_revision = tonumber(proposal.created_revision)
    if revision and created_revision and revision >= 0 and created_revision >= 0 and
        revision == math.floor(revision) and created_revision == math.floor(created_revision) and
        revision == created_revision then
        return "fresh"
    end
    return "stale"
end

local function outcome_pending(name, err)
    mark_session_missing(name, err)
    busy[name] = nil
    notify(name, "Handled action remains queued; no new turn may start: " ..
        tostring(err and err.code or "integration_failed"))
end

local function proposal_pending(name, err)
    mark_session_missing(name, err)
    busy[name] = nil
    notify(name, "Proposal outcome is unresolved; the same request/job will resume: " ..
        tostring(err and err.code or "proposal_outcome_unknown"))
end

local function proposal_job_matches_attempt(attempt, job)
    if type(attempt) ~= "table" or type(job) ~= "table" or
        type(attempt.request) ~= "table" then
        return false
    end
    local request = attempt.request
    return type(attempt.job_id) == "string" and attempt.job_id ~= "" and
        type(attempt.session_id) == "string" and
        type(request.request_id) == "string" and request.request_id ~= "" and
        request.session_id == attempt.session_id and
        type(job.job_id) == "string" and job.job_id == attempt.job_id and
        type(job.session_id) == "string" and job.session_id == attempt.session_id and
        type(job.request_id) == "string" and job.request_id == request.request_id
end

local function proposal_matches_attempt(attempt, proposal)
    if type(attempt) ~= "table" or type(attempt.request) ~= "table" or
        type(proposal) ~= "table" then
        return false
    end
    local request = attempt.request
    return type(proposal.id) == "string" and proposal.id ~= "" and
        type(proposal.session_id) == "string" and proposal.session_id == attempt.session_id and
        type(proposal.request_id) == "string" and proposal.request_id == request.request_id and
        type(proposal.actor_id) == "string" and proposal.actor_id == request.actor_id and
        type(proposal.tick) == "number" and type(request.tick) == "number" and
        proposal.tick == request.tick and proposal.tick == math.floor(proposal.tick)
end

local function apply_and_report_outcome(name, session_id, attempt, job, freshness)
    local proposal = type(job.proposal) == "table" and job.proposal or {}
    local action = type(proposal.action) == "table" and proposal.action or {}
    local action_id = tostring(action.id or "")
    local line = allowed_actions[action_id]
    if type(proposal.id) ~= "string" then
        proposal_pending(name, { code = "invalid_response" })
        return
    end
    local operation_id = attempt.operation_id

    core.after(0, function()
        local applied = applied_operations[operation_id]
        if not applied then
            if freshness == "unavailable" then
                applied = {
                    accepted = false,
                    outcome = "The game rejected the proposal because freshness could not be verified.",
                }
            elseif freshness ~= "fresh" then
                applied = {
                    accepted = false,
                    outcome = "The game rejected a stale proposal before applying it.",
                }
            elseif not line then
                applied = {
                    accepted = false,
                    outcome = "The game rejected an action outside its allowlist.",
                }
            else
                applied = { accepted = true, outcome = line }
            end

            -- Capture occurrence at the actual authoritative decision.
            local occurrence_tick = math.max(game_tick(), attempt.request.tick, proposal.tick)
            local pending = commit_pending(
                name, session_id, operation_id, proposal.id, occurrence_tick, applied)
            local persisted, persistence_error = persist_authoritative_transaction(
                operation_id, applied, pending, function()
                    if applied.accepted then notify(name, line) end
                end, attempt)
            if not persisted then
                proposal_pending(
                    name,
                    { code = "game_transaction_failed", message = persistence_error })
                return
            end
        end
        report_outcome(operation_id, function(err)
            busy[name] = nil
            if err then outcome_pending(name, err) else notify(name, "Outcome acknowledged.") end
        end)
    end)
end

local function apply_offline_fallback(name, session_id, turn, resolved_attempt)
    local operation_id = resolved_attempt and
        (resolved_attempt.operation_id .. ".offline") or
        (session_id .. ".offline." .. turn)
    core.after(0, function()
        local line =
            "Guide (offline): Stay safe, preserve resources, and observe before acting."
        local applied = { accepted = true, outcome = line }
        local occurrence_tick = game_tick()
        local pending = observe_pending(
            name, session_id, operation_id, occurrence_tick, applied)
        local persisted, persistence_error = persist_authoritative_transaction(
            operation_id,
            applied,
            pending,
            function() notify(name, line) end,
            resolved_attempt,
            resolved_attempt and nil or turn)
        busy[name] = nil
        if not persisted then
            notify(name, "Offline transaction failed: " .. tostring(persistence_error))
            return
        end
        notify(name, "Offline outcome is queued until Rin becomes available.")
    end)
end

local submit_proposal_attempt

local function clear_attempt_job_id(name, attempt)
    if not persist_proposal_job_id(name, attempt, "") then return false end
    attempt.job_id = ""
    return true
end

local function inspect_proposal_job(name, attempt, job, may_resubmit)
    if not proposal_job_matches_attempt(attempt, job) then
        proposal_pending(name, { code = "job_identity_mismatch" })
        return
    end
    local status = tostring(job.status or "")
    if status == "succeeded" then
        local proposal = type(job.proposal) == "table" and job.proposal or {}
        if not proposal_matches_attempt(attempt, proposal) then
            proposal_pending(name, { code = "proposal_identity_mismatch" })
            return
        end
        -- Temporary State failure is fail-closed; once an online proposal
        -- exists, authored offline fallback is forbidden.
        client:state({
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = attempt.session_id,
        }, function(state, state_error)
            mark_session_missing(name, state_error)
            apply_and_report_outcome(
                name,
                attempt.session_id,
                attempt,
                job,
                state_error and "unavailable" or proposal_is_fresh(state, proposal))
        end)
        return
    end
    if status == "failed" or status == "stale" or status == "canceled" then
        local detail = type(job.error) == "table" and job.error or {}
        local code = tostring(detail.code or ("job_" .. status))
        if code == "session_not_found" then
            mark_session_missing(name, { code = code })
        end
        if code == "proposal_outcome_unknown" then
            if may_resubmit and clear_attempt_job_id(name, attempt) then
                submit_proposal_attempt(name, attempt, false)
            else
                proposal_pending(name, { code = code })
            end
            return
        end
        -- A successful GET confirmed a terminal Job with no Proposal. The
        -- fallback and attempt removal still happen in one game transaction.
        apply_offline_fallback(name, attempt.session_id, attempt.turn, attempt)
        return
    end
    if status ~= "queued" and status ~= "running" then
        proposal_pending(name, { code = "invalid_job" })
        return
    end

    client:wait_for_proposal(attempt.job_id, nil, function(resolved, wait_error)
        if not wait_error then
            inspect_proposal_job(name, attempt, resolved, may_resubmit)
            return
        end
        -- A wait error may itself be a lost GET/DELETE response. Re-read the
        -- Job before deciding whether a terminal state permits fallback.
        client:get_proposal_job(attempt.job_id, function(confirmed, confirm_error)
            if confirm_error then
                if tostring(confirm_error.code) == "job_not_found" and may_resubmit and
                    clear_attempt_job_id(name, attempt) then
                    submit_proposal_attempt(name, attempt, false)
                    return
                end
                proposal_pending(name, confirm_error)
                return
            end
            inspect_proposal_job(name, attempt, confirmed, may_resubmit)
        end)
    end)
end

submit_proposal_attempt = function(name, attempt, may_resubmit)
    client:submit_proposal_job(attempt.request, function(queued, queue_error)
        if queue_error then
            proposal_pending(name, queue_error)
            return
        end
        local job_id = type(queued) == "table" and queued.job_id or nil
        if type(job_id) ~= "string" or job_id == "" then
            proposal_pending(name, { code = "invalid_submission" })
            return
        end
        if not persist_proposal_job_id(name, attempt, job_id) then
            proposal_pending(name, { code = "proposal_attempt_persist_failed" })
            return
        end
        attempt.job_id = job_id
        client:get_proposal_job(job_id, function(job, get_error)
            if get_error then
                proposal_pending(name, get_error)
                return
            end
            inspect_proposal_job(name, attempt, job, may_resubmit)
        end)
    end)
end

local function resume_proposal_attempt(name, attempt)
    if tostring(attempt.job_id or "") == "" then
        submit_proposal_attempt(name, attempt, true)
        return
    end
    client:get_proposal_job(attempt.job_id, function(job, get_error)
        if get_error then
            if tostring(get_error.code) == "job_not_found" and
                clear_attempt_job_id(name, attempt) then
                submit_proposal_attempt(name, attempt, false)
                return
            end
            proposal_pending(name, get_error)
            return
        end
        inspect_proposal_job(name, attempt, job, true)
    end)
end

local function request_online_turn(name, message, session_id, turn)
    -- Retained reports are flushed before Observe/Propose. Any temporary
    -- failure fails closed and prevents a new turn.
    flush_outcome_outbox(name, function(flush_error)
        if flush_error then outcome_pending(name, flush_error); return end
        local retained_attempt = proposal_attempts[name]
        if retained_attempt then
            resume_proposal_attempt(name, retained_attempt)
            return
        end
        local operation_id = session_id .. "." .. turn
        local observed_tick = game_tick()
        client:observe({
            protocol_version = rin.PROTOCOL_VERSION,
            session_id = session_id,
            request_id = "observe." .. operation_id,
            event_id = "event." .. operation_id,
            tick = observed_tick,
            observer_ids = { actor_id },
            source = "luanti-example",
            kind = "dialogue",
            summary = message,
            tags = { "conversation", "player-request" },
            importance = 3,
        }, function(_, observe_error)
            if observe_error then failed(name, observe_error); return end
            local attempt = {
                name = name,
                session_id = session_id,
                turn = turn,
                operation_id = operation_id,
                job_id = "",
                request = {
                    protocol_version = rin.PROTOCOL_VERSION,
                    session_id = session_id,
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
                },
            }
            if not persist_new_proposal_attempt(name, attempt) then
                failed(name, { code = "proposal_attempt_persist_failed" })
                return
            end
            proposal_attempts[name] = attempt
            local entry = sessions[name]
            if entry then entry.sequence = math.max(entry.sequence, turn) end
            submit_proposal_attempt(name, attempt, true)
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
        local entry = sessions[name]
        local retained_attempt = proposal_attempts[name]
        local turn = retained_attempt and retained_attempt.turn or
            ((entry and entry.sequence or 0) + 1)
        if session_error or not session_id then
            if has_pending_outcome(name) then
                outcome_pending(name, session_error)
            elseif retained_attempt then
                proposal_pending(name, session_error)
            elseif entry then
                -- Only cold-start unavailability (before any Rin proposal)
                -- uses the explicit, bounded game-authored fallback.
                apply_offline_fallback(name, entry.id, turn)
            else
                failed(name, session_error)
            end
            return
        end
        request_online_turn(name, message, session_id, turn)
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
