local module = {}
local State = {}
State.__index = State

local storage_key = "workflow_state_v1"
local maximum_bytes = 1024 * 1024
local maximum_players = 128
local maximum_outcomes = 64

local function count(table_value)
    local result = 0
    for _ in pairs(table_value or {}) do result = result + 1 end
    return result
end

local function dense_array_length(value)
    if type(value) ~= "table" then return nil end
    local entries, maximum = 0, 0
    for key in pairs(value) do
        if type(key) ~= "number" or key < 1 or key ~= math.floor(key) then return nil end
        entries = entries + 1
        maximum = math.max(maximum, key)
    end
    if entries ~= maximum then return nil end
    return entries
end

local function safe_integer(value)
    return type(value) == "number" and value >= 0 and
        value <= 9007199254740991 and value ~= math.huge and
        value == math.floor(value)
end

local function valid_id(value)
    return type(value) == "string" and #value >= 1 and #value <= 96 and
        value:match("^[A-Za-z0-9][A-Za-z0-9._-]*$") ~= nil
end

local function state_error(code, message)
    return { code = code, message = message }
end

function module.open(options)
    if type(options) ~= "table" or type(options.storage) ~= "table" or
        type(options.encode) ~= "function" or type(options.decode) ~= "function" or
        type(options.new_world_id) ~= "function" or
        type(options.hash) ~= "function" then
        return nil, state_error("invalid_state_options", "State options are incomplete")
    end
    local self = setmetatable({
        storage = options.storage,
        encode = options.encode,
        decode = options.decode,
        hash = options.hash,
        staged = {},
    }, State)
    local raw = self.storage:get_string(storage_key)
    if raw == "" then
        local world_id = options.new_world_id()
        if type(world_id) ~= "string" or not world_id:match("^[a-f0-9]+$") or
            #world_id ~= 32 then
            return nil, state_error("invalid_world_identity", "World identity is malformed")
        end
        self.state = {
            version = 1,
            world_id = world_id,
            players = {},
            outcomes = {},
        }
        local ok, err = self:_persist(self.state)
        if not ok then return nil, err end
        return self
    end
    if #raw > maximum_bytes then
        return nil, state_error("state_too_large", "Workflow state exceeds 1 MiB")
    end
    local decoded_ok, decoded = pcall(self.decode, raw)
    if not decoded_ok or type(decoded) ~= "table" then
        return nil, state_error("invalid_state", "Workflow state is not valid JSON")
    end
    self.state = decoded
    local valid, err = self:_validate()
    if not valid then return nil, err end
    return self
end

function State:_encode(value)
    local ok, encoded = pcall(self.encode, value)
    if not ok or type(encoded) ~= "string" then
        return nil, state_error("state_encode_failed", "Could not encode workflow state")
    end
    if #encoded > maximum_bytes then
        return nil, state_error("state_too_large", "Workflow state exceeds 1 MiB")
    end
    return encoded
end

function State:_copy(value)
    local encoded, encode_error = self:_encode(value)
    if not encoded then return nil, encode_error end
    local ok, copied = pcall(self.decode, encoded)
    if not ok or type(copied) ~= "table" then
        return nil, state_error("state_copy_failed", "Could not copy workflow state")
    end
    return copied
end

function State:_persist(candidate)
    local valid, validation_error = self:_validate(candidate)
    if not valid then return false, validation_error end
    local encoded, encode_error = self:_encode(candidate)
    if not encoded then return false, encode_error end
    local ok, storage_error = pcall(
        self.storage.set_string,
        self.storage,
        storage_key,
        encoded)
    if not ok then
        return false, state_error(
            "state_write_failed",
            "ModStorage rejected workflow state: " .. tostring(storage_error))
    end
    self.state = candidate
    return true
end

function State:_validate(candidate)
    local state = candidate or self.state
    local outcome_count = dense_array_length(state and state.outcomes)
    if state.version ~= 1 or type(state.world_id) ~= "string" or
        not state.world_id:match("^[a-f0-9]+$") or #state.world_id ~= 32 or
        type(state.players) ~= "table" or count(state.players) > maximum_players or
        outcome_count == nil or outcome_count > maximum_outcomes then
        return false, state_error("invalid_state", "Workflow state header is malformed")
    end
    local seen_sessions = {}
    for name, player in pairs(state.players) do
        if type(name) ~= "string" or name == "" or #name > 64 or
            type(player) ~= "table" or
            not valid_id(player.session_id) or
            seen_sessions[player.session_id] or not safe_integer(player.seed) or
            not safe_integer(player.sequence) or not safe_integer(player.last_tick) or
            type(player.create_request) ~= "table" or
            player.create_request.session_id ~= player.session_id or
            not valid_id(player.create_request.request_id) then
            return false, state_error("invalid_state", "Player workflow state is malformed")
        end
        seen_sessions[player.session_id] = true
        if player.attempt ~= nil then
            local attempt = player.attempt
            if type(attempt) ~= "table" or attempt.version ~= 1 or
                not valid_id(attempt.operation_id) or type(attempt.request) ~= "table" or
                attempt.request.session_id ~= player.session_id or
                not valid_id(attempt.request.request_id) or
                not valid_id(attempt.request.actor_id) or
                not safe_integer(attempt.request.tick) or
                type(attempt.job_id) ~= "string" or
                (attempt.job_id ~= "" and not valid_id(attempt.job_id)) or
                type(player.pending_observe) ~= "table" or
                player.pending_observe.session_id ~= player.session_id or
                not valid_id(player.pending_observe.request_id) or
                not valid_id(player.pending_observe.event_id) or
                not safe_integer(player.pending_observe.tick) then
                return false, state_error("invalid_state", "Pending Turn is malformed")
            end
        elseif player.pending_observe ~= nil then
            return false, state_error("invalid_state", "Observe exists without a Pending Turn")
        end
    end
    local seen = {}
    for _, outcome in ipairs(state.outcomes) do
        if type(outcome) ~= "table" or seen[outcome.key] or
            not valid_id(outcome.key) or type(outcome.owner) ~= "string" or
            type(state.players[outcome.owner]) ~= "table" or
            (outcome.kind ~= "report" and outcome.kind ~= "observe") or
            type(outcome.request) ~= "table" or
            not valid_id(outcome.request.session_id) or
            not valid_id(outcome.request.request_id) or
            not safe_integer(outcome.request.tick) or
            (outcome.kind == "report" and
                (type(outcome.request.report) ~= "table" or
                    not valid_id(outcome.request.report.proposal_id) or
                    not valid_id(outcome.request.report.event_id))) or
            (outcome.kind == "observe" and
                not valid_id(outcome.request.event_id)) then
            return false, state_error("invalid_state", "Outcome Outbox is malformed")
        end
        seen[outcome.key] = true
    end
    return true
end

function State:world_id()
    return self.state.world_id
end

function State:ensure_player(name, create_request)
    if type(name) ~= "string" or name == "" or #name > 64 or
        type(create_request) ~= "function" then
        return nil, state_error("invalid_player", "Player identity is malformed")
    end
    local current = self.state.players[name]
    if current then return self:_copy(current) end
    if count(self.state.players) >= maximum_players then
        return nil, state_error("player_limit", "Workflow state has reached its player limit")
    end
    local digest = self.hash(name)
    if type(digest) ~= "string" or #digest ~= 64 or
        not digest:match("^[a-f0-9]+$") then
        return nil, state_error("invalid_player_hash", "Player identity hash is malformed")
    end
    local session_id =
        "luanti." .. self.state.world_id:sub(1, 16) .. "." .. digest:sub(1, 16)
    local seed = tonumber(self.state.world_id:sub(1, 12), 16)
    local request = create_request(session_id, seed)
    if type(request) ~= "table" or request.session_id ~= session_id or
        not valid_id(request.request_id) then
        return nil, state_error("invalid_create_request", "Create request is malformed")
    end
    local candidate, copy_error = self:_copy(self.state)
    if not candidate then return nil, copy_error end
    candidate.players[name] = {
        session_id = session_id,
        seed = seed,
        sequence = 0,
        last_tick = 0,
        create_request = request,
    }
    local ok, persist_error = self:_persist(candidate)
    if not ok then return nil, persist_error end
    return self:_copy(candidate.players[name])
end

function State:get_player(name)
    local player = self.state.players[name]
    if not player then return nil end
    return self:_copy(player)
end

function State:stage_turn(name, create_request, observe)
    local player = self.state.players[name]
    if not player or player.attempt or type(create_request) ~= "table" or
        create_request.session_id ~= player.session_id or
        create_request.request_id ~= player.create_request.request_id or
        type(observe) ~= "table" or observe.session_id ~= player.session_id or
        not valid_id(observe.request_id) or not valid_id(observe.event_id) or
        type(observe.tick) ~= "number" or observe.tick < 0 or
        observe.tick ~= math.floor(observe.tick) then
        return false, state_error("invalid_workflow", "Cannot stage this Pending Turn")
    end
    self.staged[name] = {
        create_request = create_request,
        observe = observe,
    }
    return true
end

function State:load_attempt(name)
    local player = self.state.players[name]
    if not player or not player.attempt then return nil end
    return self:_copy(player.attempt)
end

function State:create_attempt(name, attempt)
    local player = self.state.players[name]
    local staged = self.staged[name]
    if not player or player.attempt then return false end
    if not staged then
        return false, state_error("workflow_context_missing", "Turn context was not staged")
    end
    local next_sequence = player.sequence + 1
    if attempt.operation_id ~= player.session_id .. "." .. next_sequence or
        attempt.request.session_id ~= player.session_id or
        type(staged.observe.tick) ~= "number" or
        staged.observe.tick <= player.last_tick then
        return false, state_error("workflow_identity_mismatch", "Pending Turn identity changed")
    end
    local candidate, copy_error = self:_copy(self.state)
    if not candidate then return false, copy_error end
    local updated = candidate.players[name]
    updated.sequence = next_sequence
    updated.last_tick = staged.observe.tick
    updated.pending_observe = staged.observe
    updated.attempt = attempt
    local ok, persist_error = self:_persist(candidate)
    if not ok then return false, persist_error end
    self.staged[name] = nil
    return true
end

local function matching_attempt(current, expected)
    return type(current) == "table" and type(expected) == "table" and
        current.version == expected.version and
        current.operation_id == expected.operation_id and
        current.request.session_id == expected.request.session_id and
        current.request.request_id == expected.request.request_id and
        (current.job_id == expected.job_id or
            current.job_id == "" or expected.job_id == "")
end

function State:save_attempt(name, attempt)
    local player = self.state.players[name]
    if not player or not matching_attempt(player.attempt, attempt) then return false end
    local candidate, copy_error = self:_copy(self.state)
    if not candidate then return false, copy_error end
    candidate.players[name].attempt = attempt
    return self:_persist(candidate)
end

function State:complete_attempt(name, attempt, outcome)
    local player = self.state.players[name]
    if not player or not matching_attempt(player.attempt, attempt) then return false end
    if #self.state.outcomes >= maximum_outcomes then
        return false, state_error("outbox_full", "Outcome Outbox is full")
    end
    local candidate, copy_error = self:_copy(self.state)
    if not candidate then return false, copy_error end
    table.insert(candidate.outcomes, outcome)
    candidate.players[name].last_tick = math.max(
        candidate.players[name].last_tick,
        tonumber(outcome.request.tick) or 0)
    candidate.players[name].attempt = nil
    candidate.players[name].pending_observe = nil
    return self:_persist(candidate)
end

function State:list_outcomes(name)
    local result = {}
    for _, outcome in ipairs(self.state.outcomes) do
        if outcome.owner == name then
            local copied, copy_error = self:_copy(outcome)
            if not copied then return nil, copy_error end
            table.insert(result, copied)
        end
    end
    table.sort(result, function(left, right) return left.key < right.key end)
    return result
end

local function same_outcome(left, right)
    return left.key == right.key and left.owner == right.owner and
        left.kind == right.kind and
        left.request.request_id == right.request.request_id
end

function State:acknowledge_outcome(name, acknowledged)
    local candidate, copy_error = self:_copy(self.state)
    if not candidate then return false, copy_error end
    for index, outcome in ipairs(candidate.outcomes) do
        if outcome.owner == name and same_outcome(outcome, acknowledged) then
            table.remove(candidate.outcomes, index)
            return self:_persist(candidate)
        end
    end
    return false, state_error("outcome_changed", "Outcome changed before acknowledgement")
end

function State:pending_observe(name)
    local player = self.state.players[name]
    if not player or not player.pending_observe then return nil end
    return self:_copy(player.pending_observe)
end

function State:has_outcomes(name)
    for _, outcome in ipairs(self.state.outcomes) do
        if outcome.owner == name then return true end
    end
    return false
end

return module
