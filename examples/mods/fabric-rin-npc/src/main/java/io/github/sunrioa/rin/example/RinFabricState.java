package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;
import com.google.gson.ToNumberPolicy;
import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import net.minecraft.nbt.NbtCompound;
import net.minecraft.registry.RegistryWrapper;
import net.minecraft.server.MinecraftServer;
import net.minecraft.world.PersistentState;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

final class RinFabricState extends PersistentState {
    static final int MAX_SESSIONS = 256;
    static final int MAX_OUTCOMES_PER_SESSION = 32;
    private static final int VERSION = 1;
    private static final String STORAGE_KEY = "rin_npc";
    private static final String JSON_KEY = "json";
    private static final int MAX_JSON_CHARS = 2_000_000;
    private static final Gson GSON = new GsonBuilder()
            .setObjectToNumberStrategy(ToNumberPolicy.LONG_OR_DOUBLE)
            .create();
    private static final Type<RinFabricState> TYPE = new Type<>(
            RinFabricState::new,
            RinFabricState::readNbt,
            null);

    final String worldId;
    long sequence;
    final Map<String, SessionState> sessions = new LinkedHashMap<>();

    RinFabricState() {
        this(UUID.randomUUID().toString(), 0);
    }

    private RinFabricState(String worldId, long sequence) {
        this.worldId = worldId;
        this.sequence = sequence;
    }

    static RinFabricState get(MinecraftServer server) {
        return server.getOverworld().getPersistentStateManager()
                .getOrCreate(TYPE, STORAGE_KEY);
    }

    SessionState session(String sessionId, Map<String, Object> createRequest) {
        SessionState existing = sessions.get(sessionId);
        if (existing != null) return existing;
        if (sessions.size() >= MAX_SESSIONS) {
            throw new IllegalStateException(
                    "Rin session limit reached; remove obsolete saved sessions first");
        }
        SessionState created = new SessionState(createRequest);
        sessions.put(sessionId, created);
        markDirty();
        return created;
    }

    long nextSequence() {
        sequence = Math.incrementExact(sequence);
        markDirty();
        return sequence;
    }

    @Override
    public NbtCompound writeNbt(
            NbtCompound nbt,
            RegistryWrapper.WrapperLookup registries) {
        String json = GSON.toJson(toJson());
        if (json.length() > MAX_JSON_CHARS) {
            throw new IllegalStateException("Rin saved state exceeds its bounded size");
        }
        nbt.putString(JSON_KEY, json);
        return nbt;
    }

    private Map<String, Object> toJson() {
        Map<String, Object> root = new LinkedHashMap<>();
        root.put("version", VERSION);
        root.put("world_id", worldId);
        root.put("sequence", sequence);
        Map<String, Object> encodedSessions = new LinkedHashMap<>();
        sessions.forEach((id, session) -> encodedSessions.put(id, session.toJson()));
        root.put("sessions", encodedSessions);
        return root;
    }

    static RinFabricState readNbt(
            NbtCompound nbt,
            RegistryWrapper.WrapperLookup registries) {
        try {
            String json = nbt.getString(JSON_KEY);
            if (json.isEmpty()) return new RinFabricState();
            if (json.length() > MAX_JSON_CHARS) {
                throw new IllegalStateException("Rin saved state exceeds its bounded size");
            }
            @SuppressWarnings("unchecked")
            Map<String, Object> root = GSON.fromJson(json, Map.class);
            if (root == null || integer(root.get("version")) != VERSION) {
                throw new IllegalStateException("Unsupported Rin saved state version");
            }
            String worldId = text(root.get("world_id"));
            UUID.fromString(worldId);
            long sequence = integer(root.get("sequence"));
            if (sequence < 0) throw new IllegalStateException("Rin sequence is negative");
            RinFabricState state = new RinFabricState(worldId, sequence);
            Map<String, Object> encodedSessions = object(root.get("sessions"));
            if (encodedSessions.size() > MAX_SESSIONS) {
                throw new IllegalStateException("Rin saved state has too many sessions");
            }
            encodedSessions.forEach((id, value) ->
                    state.sessions.put(id, SessionState.fromJson(object(value))));
            return state;
        } catch (RuntimeException invalid) {
            throw new IllegalStateException("Rin saved state is malformed", invalid);
        }
    }

    static final class SessionState {
        final Map<String, Object> createRequest;
        PendingTurn pendingTurn;
        Map<String, Object> pendingObserve = Map.of();
        final Map<String, OutcomeOutboxEntry> outcomes = new LinkedHashMap<>();

        SessionState(Map<String, Object> createRequest) {
            this.createRequest = immutable(createRequest);
        }

        private Map<String, Object> toJson() {
            Map<String, Object> result = new LinkedHashMap<>();
            result.put("create_request", createRequest);
            if (pendingTurn != null) {
                result.put("pending_turn", Map.of(
                        "version", pendingTurn.version(),
                        "operation_id", pendingTurn.operationId(),
                        "request", pendingTurn.request(),
                        "job_id", pendingTurn.jobId()));
                result.put("pending_observe", pendingObserve);
            }
            List<Object> encodedOutcomes = new ArrayList<>();
            outcomes.values().forEach(entry -> encodedOutcomes.add(Map.of(
                    "key", entry.key(),
                    "commit", entry.commit(),
                    "fallback_observe", entry.fallbackObserve())));
            result.put("outcomes", encodedOutcomes);
            return result;
        }

        private static SessionState fromJson(Map<String, Object> value) {
            SessionState session = new SessionState(object(value.get("create_request")));
            Map<String, Object> encodedPending = object(value.get("pending_turn"));
            if (!encodedPending.isEmpty()) {
                session.pendingTurn = new PendingTurn(
                        Math.toIntExact(integer(encodedPending.get("version"))),
                        text(encodedPending.get("operation_id")),
                        object(encodedPending.get("request")),
                        text(encodedPending.get("job_id")));
                session.pendingObserve = immutable(object(value.get("pending_observe")));
                if (session.pendingObserve.isEmpty()) {
                    throw new IllegalStateException("Pending Turn is missing its Observe");
                }
            }
            Object rawOutcomes = value.get("outcomes");
            if (rawOutcomes instanceof List<?> outcomes) {
                if (outcomes.size() > MAX_OUTCOMES_PER_SESSION) {
                    throw new IllegalStateException("Rin saved state has too many outcomes");
                }
                for (Object raw : outcomes) {
                    Map<String, Object> encoded = object(raw);
                    OutcomeOutboxEntry entry = new OutcomeOutboxEntry(
                            text(encoded.get("key")),
                            object(encoded.get("commit")),
                            object(encoded.get("fallback_observe")));
                    if (session.outcomes.putIfAbsent(entry.key(), entry) != null) {
                        throw new IllegalStateException("Rin saved state has duplicate outcomes");
                    }
                }
            }
            return session;
        }
    }

    private static Map<String, Object> immutable(Map<String, Object> value) {
        return PendingTurn.copyJsonObject(value);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value) {
        return value instanceof Map<?, ?> map
                ? (Map<String, Object>) map
                : Map.of();
    }

    private static String text(Object value) {
        return value instanceof String string ? string : "";
    }

    private static long integer(Object value) {
        if (!(value instanceof Number number)) return 0;
        double floating = number.doubleValue();
        long integral = number.longValue();
        if (!Double.isFinite(floating) || floating != integral) {
            throw new IllegalStateException("Rin saved integer is malformed");
        }
        return integral;
    }
}
