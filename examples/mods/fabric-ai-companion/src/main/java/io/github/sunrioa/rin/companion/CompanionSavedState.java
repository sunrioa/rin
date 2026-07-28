package io.github.sunrioa.rin.companion;

import com.mojang.serialization.Codec;
import net.minecraft.resources.Identifier;
import net.minecraft.util.datafix.DataFixTypes;
import net.minecraft.world.level.saveddata.SavedData;
import net.minecraft.world.level.saveddata.SavedDataType;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

final class CompanionSavedState extends SavedData {
    static final int MAX_SESSIONS = 256;
    static final int MAX_JSON_CHARS = 2_000_000;
    private static final int VERSION = 1;
    static final Codec<CompanionSavedState> CODEC = Codec.STRING.xmap(CompanionSavedState::fromJson, CompanionSavedState::toJson);
    static final SavedDataType<CompanionSavedState> TYPE = new SavedDataType<>(Identifier.fromNamespaceAndPath("rin_ai_companion", "state"), CompanionSavedState::new, CODEC, DataFixTypes.SAVED_DATA_COMMAND_STORAGE);
    final UUID worldId;
    long hostGeneration;
    long timelineGeneration;
    long sequence;
    final Map<String, CompanionSessionState> sessions = new LinkedHashMap<>();
    CompanionSavedState() { this(UUID.randomUUID()); }
    CompanionSavedState(UUID worldId) { this.worldId = worldId; }

    String toJson() {
        if (sessions.size() > MAX_SESSIONS || sessions.values().stream().anyMatch(session -> session.outcomes.size() > CompanionSessionState.MAX_OUTCOMES)) throw new IllegalStateException("companion saved state exceeds its limits");
        Map<String, Object> root = new LinkedHashMap<>();
        root.put("version", VERSION); root.put("world_id", worldId.toString()); root.put("host_generation", hostGeneration); root.put("timeline_generation", timelineGeneration); root.put("sequence", sequence);
        Map<String, Object> encodedSessions = new LinkedHashMap<>(); sessions.forEach((id, session) -> encodedSessions.put(id, session.toJson())); root.put("sessions", encodedSessions);
        String json = new GsonJsonCodec().encode(root); if (json.length() > MAX_JSON_CHARS) throw new IllegalStateException("companion saved state is too large"); return json;
    }

    static CompanionSavedState fromJson(String json) {
        if (json == null || json.length() > MAX_JSON_CHARS) throw new IllegalStateException("companion saved state is too large");
        Map<String, Object> root = new GsonJsonCodec().decodeObject(json);
        if (!root.keySet().equals(Set.of("version", "world_id", "host_generation", "timeline_generation", "sequence", "sessions")) || integer(root.get("version")) != VERSION) throw new IllegalStateException("companion saved state has an invalid shape");
        final UUID worldId; try { worldId = UUID.fromString(text(root.get("world_id"))); } catch (IllegalArgumentException exception) { throw new IllegalStateException("companion world id is malformed", exception); }
        CompanionSavedState state = new CompanionSavedState(worldId); state.hostGeneration = generation(root.get("host_generation")); state.timelineGeneration = generation(root.get("timeline_generation")); state.sequence = nonNegative(root.get("sequence"));
        Map<String, Object> encodedSessions = object(root.get("sessions")); if (encodedSessions.size() > MAX_SESSIONS) throw new IllegalStateException("too many companion sessions");
        encodedSessions.forEach((id, value) -> { CompanionSessionState session = CompanionSessionState.fromJson(object(value)); if (!id.equals(session.sessionId)) throw new IllegalStateException("session id mismatch"); if (state.sessions.putIfAbsent(id, session) != null) throw new IllegalStateException("duplicate session"); });
        return state;
    }
    private static long generation(Object value) { long number = nonNegative(value); if (number <= 0) throw new IllegalStateException("invalid companion generation"); return number; }
    private static long nonNegative(Object value) { if (!(value instanceof Number number) || number.doubleValue() != number.longValue() || number.longValue() < 0) throw new IllegalStateException("invalid companion integer"); return number.longValue(); }
    private static int integer(Object value) { return Math.toIntExact(nonNegative(value)); }
    private static String text(Object value) { if (value instanceof String text && !text.isBlank()) return text; throw new IllegalStateException("invalid companion text"); }
    @SuppressWarnings("unchecked") private static Map<String, Object> object(Object value) { if (!(value instanceof Map<?, ?> map)) throw new IllegalStateException("invalid companion object"); return (Map<String, Object>) map; }
}
