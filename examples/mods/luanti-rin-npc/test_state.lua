local state_module = dofile("examples/mods/luanti-rin-npc/state.lua")

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
}
local state = assert(state_module.open(options))
assert(state:world_id() == string.rep("a", 32))

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
assert(restarted:complete_attempt("player-one", attempt, outcome))
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

backing.workflow_state_v1 = encode({
    version = 1,
    world_id = string.rep("a", 32),
    players = {},
    outcomes = { [2] = outcome },
})
local malformed, malformed_error = state_module.open(options)
assert(not malformed and malformed_error.code == "invalid_state",
    "sparse persisted arrays were accepted")

print("Luanti workflow state restart tests passed")
