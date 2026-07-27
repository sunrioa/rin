local arguments = rawget(_G, "arg")
local script = arguments and arguments[0] or "test_state.lua"
local directory = script:match("^(.*[\\/])") or ""
local state_path = rawget(_G, "RIN_STATE_TEST_PATH") or directory .. "state.lua"
local rin_path = rawget(_G, "RIN_CLIENT_TEST_PATH") or directory .. "rin.lua"
local state_module = dofile(state_path)
local rin = dofile(rin_path)

local function copy(value, seen)
    if type(value) ~= "table" then return value end
    seen = seen or {}
    if seen[value] then error("cycle") end
    seen[value] = true
    local result = {}
    for key, child in pairs(value) do result[copy(key, seen)] = copy(child, seen) end
    seen[value] = nil
    return result
end

local encoded_values = {}
local next_encoded = 0
local function encode(value)
    next_encoded = next_encoded + 1
    local token = "snapshot-" .. next_encoded
    encoded_values[token] = copy(value)
    return token
end
local function decode(token)
    if not encoded_values[token] then error("invalid snapshot") end
    return copy(encoded_values[token])
end

local backing = {}
local storage = { fail_writes = false }
function storage:get_string(key) return backing[key] or "" end
function storage:set_string(key, value)
    if self.fail_writes then error("simulated ModStorage failure") end
    backing[key] = value
end

local options = {
    storage = storage,
    encode = encode,
    decode = decode,
    hash = function(value)
        if value == "player-one" then return string.rep("b", 64) end
        return string.rep("c", 64)
    end,
    new_world_id = function() return string.rep("a", 32) end,
    binding = {
        game_id = "luanti",
        content_id = "rin-npc-example",
        content_version = "0.7.0",
        content_hash = "sha256:" .. string.rep("0", 64),
    },
}
local state = assert(state_module.open(options))
assert(state:world_id() == string.rep("a", 32))
local first_epoch = assert(state:epoch("luanti.session.fixture"))

local function create_request(session_id, seed)
    return {
        protocol_version = "rin.protocol/v2",
        request_id = "create." .. session_id,
        session_id = session_id,
        seed = seed,
    }
end

local player = assert(state:ensure_player("player-one", create_request))
assert(player.session_id ==
    "luanti." .. string.rep("a", 16) .. "." .. string.rep("b", 16))
local other = assert(state:ensure_player("player one", create_request))
assert(other.session_id ~= player.session_id, "normalized player names collided")
local observe = {
    session_id = player.session_id,
    request_id = "observe." .. player.session_id,
    event_id = "event." .. player.session_id,
    tick = 10,
}
assert(state:stage_turn("player-one", player.create_request, observe))
local attempt = {
    version = 1,
    operation_id = player.session_id .. ".1",
    request = {
        session_id = player.session_id,
        request_id = "propose." .. player.session_id,
        actor_id = "actor.fixture",
        tick = 11,
    },
    job_id = "",
}
assert(state:create_attempt("player-one", attempt))
attempt.job_id = "job.fixture"
assert(state:save_attempt("player-one", attempt))

local restarted = assert(state_module.open(options))
assert(restarted:world_id() == state:world_id(), "world identity changed after restart")
local restarted_epoch = assert(restarted:epoch("luanti.session.fixture"))
assert(restarted_epoch.host > first_epoch.host and
    restarted_epoch.timeline > first_epoch.timeline,
    "server restart did not advance Host and Timeline generations")
local before_world = assert(restarted:epoch("luanti.session.fixture"))
assert(restarted:advance_epoch(false))
local after_world = assert(restarted:epoch("luanti.session.fixture"))
assert(after_world.world == before_world.world + 1 and
    after_world.timeline == before_world.timeline,
    "world replacement changed the wrong Epoch generations")
local changed_options = copy(options)
changed_options.binding.content_version = "changed"
local mismatched, mismatch_error = state_module.open(changed_options)
assert(not mismatched and mismatch_error.code == "state_binding_mismatch",
    "changed content binding reused authoritative state")
assert(restarted:load_attempt("player-one").job_id == "job.fixture")
assert(restarted:pending_observe("player-one").tick == 10)
assert(restarted:get_player("player-one").sequence == 1)
local cleared = copy(restarted:load_attempt("player-one"))
cleared.job_id = ""
assert(restarted:save_attempt("player-one", cleared))
cleared.job_id = "job.recovered"
assert(restarted:save_attempt("player-one", cleared))
attempt = cleared

local outcome = {
    key = attempt.operation_id,
    owner = "player-one",
    kind = "report",
    request = {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = player.session_id,
        request_id = "report." .. player.session_id,
        tick = 12,
        report = {
            proposal_id = "proposal.fixture",
            event_id = "outcome." .. player.session_id,
            decision = "rejected",
            summary = "host rejected the offer",
        },
    },
}
assert(restarted:settle_without_action("player-one", attempt, outcome))
local with_outcome = assert(state_module.open(options))
assert(with_outcome:load_attempt("player-one") == nil)
assert(with_outcome:get_player("player-one").last_tick == 12)
local retained = with_outcome:list_outcomes("player-one")
assert(#retained == 1 and retained[1].request.request_id == outcome.request.request_id)

assert(with_outcome:acknowledge_outcome("player-one", retained[1]))
local after_acknowledgement = assert(state_module.open(options))
assert(#after_acknowledgement:list_outcomes("player-one") == 0)

local before_failed_write = after_acknowledgement:get_player("player-one")
local next_observe = copy(observe)
next_observe.tick = 13
next_observe.request_id = "observe.second"
next_observe.event_id = "event.second"
assert(after_acknowledgement:stage_turn(
    "player-one",
    before_failed_write.create_request,
    next_observe))
local next_attempt = copy(attempt)
next_attempt.operation_id = player.session_id .. ".2"
next_attempt.request.request_id = "propose.second"
next_attempt.job_id = ""
storage.fail_writes = true
local persisted, persist_error =
    after_acknowledgement:create_attempt("player-one", next_attempt)
assert(not persisted and persist_error.code == "state_write_failed")
assert(after_acknowledgement:load_attempt("player-one") == nil)
assert(after_acknowledgement:get_player("player-one").sequence == 1)
storage.fail_writes = false

assert(after_acknowledgement:create_attempt("player-one", next_attempt))
local active_epoch = assert(after_acknowledgement:epoch(player.session_id))
local accepted_outcome = {
    key = next_attempt.operation_id,
    owner = "player-one",
    kind = "report",
    request = {
        protocol_version = rin.PROTOCOL_VERSION,
        session_id = player.session_id,
        request_id = "report.active",
        tick = 14,
        report = {
            proposal_id = "proposal.active",
            event_id = "outcome.active",
            decision = "accepted",
            summary = "applied",
            invocation = {
                operation_id = next_attempt.operation_id,
                expected_epoch = active_epoch,
            },
            run = {
                operation_id = next_attempt.operation_id,
                status = "succeeded",
                progress_seq = 1,
                progress = 100,
            },
            outcome = {
                operation_id = next_attempt.operation_id,
                status = "succeeded",
                summary = "applied",
                epoch = active_epoch,
            },
        },
    },
}
assert(after_acknowledgement:begin_action(
    "player-one", next_attempt, accepted_outcome))
-- Host scenario: long_action_epoch_cancel.
local recovered_active = assert(state_module.open(options))
assert(recovered_active:load_attempt("player-one") == nil,
    "server restart retained an executable Active Run")
local recovered_outcomes = assert(recovered_active:list_outcomes("player-one"))
assert(#recovered_outcomes == 1 and
    recovered_outcomes[1].request.report.run.status == "outcome-unknown" and
    recovered_outcomes[1].request.report.outcome.status == "outcome-unknown",
    "recovery_state_cleanup: server restart did not reconcile Active Run as outcome-unknown")

backing.rin_host_state = encode({
    version = 1,
    world_id = string.rep("a", 32),
    players = {},
    outcomes = { [2] = outcome },
})
local malformed, malformed_error = state_module.open(options)
assert(not malformed and malformed_error.code == "invalid_state",
    "sparse persisted arrays were accepted")

local binding_epoch = {
    session_id = "session.binding",
    world_id = "world.binding",
    host = 1,
    world = 1,
    timeline = 1,
}
local binding_window = {
    id = "window.binding",
    mode = "sequential",
    epoch = binding_epoch,
    observation_seq = 1,
    opened_at = { clock = "event", value = 1 },
    deadline = { clock = "event", value = 2 },
    actor_ids = { "actor.fixture" },
}
local binding_offer = rin.action_offer({
    offer_id = "offer.binding",
    actor_id = "actor.fixture",
    capability_id = "dialogue.talk",
    descriptor_digest = string.rep("a", 64),
    description = "Say one authored line.",
    arguments = { authored_action = "offer.binding" },
}, binding_window)
local binding_request = {
    session_id = binding_epoch.session_id,
    request_id = "request.binding",
    actor_id = "actor.fixture",
    tick = 2,
    decision_window = binding_window,
    offers = { binding_offer },
}
local binding_proposal = {
    id = "proposal.binding",
    session_id = binding_request.session_id,
    request_id = binding_request.request_id,
    actor_id = binding_request.actor_id,
    tick = binding_request.tick,
    decision_window = copy(binding_window),
    action = copy(binding_offer),
}
assert(rin.resolve_offered_action(binding_request, binding_proposal) ==
    binding_offer, "complete authored Offer did not resolve")
binding_proposal.action.descriptor_digest = string.rep("b", 64)
assert(rin.resolve_offered_action(binding_request, binding_proposal) == nil,
    "changed descriptor digest escaped complete Offer binding")
binding_proposal.action = copy(binding_offer)
binding_proposal.action.expected_epoch.timeline =
    binding_proposal.action.expected_epoch.timeline - 1
-- Host scenario: stale_epoch_rejection.
assert(rin.resolve_offered_action(binding_request, binding_proposal) == nil,
    "Offer from a replaced Timeline Epoch was accepted")

print("Luanti workflow state restart tests passed")
