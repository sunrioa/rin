package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.google.gson.GsonBuilder;
import com.google.gson.ToNumberPolicy;
import net.minecraft.nbt.NbtCompound;
import net.minecraft.registry.RegistryWrapper;
import net.minecraft.server.MinecraftServer;
import net.minecraft.world.PersistentState;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;

final class RinFabricState extends PersistentState {
    static final int MAX_SESSIONS = 256;
    static final int MAX_OUTCOMES_PER_SESSION = 32;
    private static final int VERSION = 2;
    private static final String STORAGE_KEY = "rin_npc";
    private static final String JSON_KEY = "json";
    private static final int MAX_JSON_CHARS = 2_000_000;
    private static final Set<String> ROOT_KEYS = Set.of(
            "version", "world_id", "sequence", "host",
            "timeline", "authority", "sessions");
    private static final Gson GSON = new GsonBuilder()
            .setObjectToNumberStrategy(ToNumberPolicy.LONG_OR_DOUBLE)
            .create();
    private static final Type<RinFabricState> TYPE = new Type<>(
            RinFabricState::new,
            RinFabricState::readNbt,
            null);

    final String worldId;
    long sequence;
    long hostGeneration;
    long timelineGeneration;
    FabricHostEpoch.AuthorityKind authorityKind;
    final Map<String, FabricSessionState> sessions = new LinkedHashMap<>();

    RinFabricState() {
        this(UUID.randomUUID().toString(), 0);
    }

    private RinFabricState(String worldId, long sequence) {
        this.worldId = worldId;
        this.sequence = sequence;
    }

    FabricHostEpoch beginHost(
            long host,
            FabricHostEpoch.AuthorityKind kind) {
        Objects.requireNonNull(kind, "kind");
        if (host <= 0 || host > FabricHostEpoch.MAX_GENERATION) {
            throw new IllegalArgumentException("Host generation is not JSON-safe");
        }
        hostGeneration = host;
        timelineGeneration = increment("timeline", timelineGeneration);
        authorityKind = kind;
        markDirty();
        return new FabricHostEpoch(
                worldId,
                hostGeneration,
                1,
                timelineGeneration,
                authorityKind);
    }

    static RinFabricState get(MinecraftServer server) {
        return server.getOverworld().getPersistentStateManager()
                .getOrCreate(TYPE, STORAGE_KEY);
    }

    FabricSessionState session(String sessionId, Map<String, Object> createRequest) {
        FabricSessionState existing = sessions.get(sessionId);
        if (existing != null) {
            if (!existing.hasCreateRequest(createRequest)) {
                throw new IllegalStateException(
                        "Rin session identity is already bound to another create request");
            }
            return existing;
        }
        if (sessions.size() >= MAX_SESSIONS) {
            throw new IllegalStateException(
                    "Rin session limit reached; remove obsolete saved sessions first");
        }
        FabricSessionState created = new FabricSessionState(createRequest);
        sessions.put(sessionId, created);
        markDirty();
        return created;
    }

    long nextSequence() {
        sequence = increment("sequence", sequence);
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
        root.put("host", hostGeneration);
        root.put("timeline", timelineGeneration);
        root.put("authority", authorityKind == null ? "" : authorityKind.wireName());
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
            if (json.isEmpty()) {
                throw new IllegalStateException("Rin saved state is missing");
            }
            if (json.length() > MAX_JSON_CHARS) {
                throw new IllegalStateException("Rin saved state exceeds its bounded size");
            }
            @SuppressWarnings("unchecked")
            Map<String, Object> root = GSON.fromJson(json, Map.class);
            if (root == null || !root.keySet().equals(ROOT_KEYS)
                    || integer(root.get("version")) != VERSION) {
                throw new IllegalStateException("Unsupported Rin saved state version");
            }
            String worldId = text(root.get("world_id"));
            UUID.fromString(worldId);
            long sequence = integer(root.get("sequence"));
            if (sequence < 0 || sequence > FabricHostEpoch.MAX_GENERATION) {
                throw new IllegalStateException("Rin sequence is outside its JSON-safe range");
            }
            RinFabricState state = new RinFabricState(worldId, sequence);
            state.hostGeneration = generation("host", root.get("host"));
            state.timelineGeneration = generation("timeline", root.get("timeline"));
            state.authorityKind = FabricHostEpoch.AuthorityKind.fromWire(
                    text(root.get("authority")));
            Map<String, Object> encodedSessions =
                    object(root.get("sessions"), "sessions");
            if (encodedSessions.size() > MAX_SESSIONS) {
                throw new IllegalStateException("Rin saved state has too many sessions");
            }
            encodedSessions.forEach((id, value) -> state.sessions.put(
                    id,
                    FabricSessionState.fromJson(object(value, "sessions." + id))));
            return state;
        } catch (RuntimeException invalid) {
            throw new IllegalStateException("Rin saved state is malformed", invalid);
        }
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value, String field) {
        if (!(value instanceof Map<?, ?> map)) {
            throw new IllegalStateException(field + " must be an object");
        }
        return (Map<String, Object>) map;
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

    private static long generation(String field, Object value) {
        long generation = integer(value);
        if (generation <= 0 || generation > FabricHostEpoch.MAX_GENERATION) {
            throw new IllegalStateException(field + " generation is invalid");
        }
        return generation;
    }

    private static long increment(String field, long value) {
        if (value < 0 || value >= FabricHostEpoch.MAX_GENERATION) {
            throw new IllegalStateException(field + " exhausted its JSON-safe range");
        }
        return value + 1;
    }
}
