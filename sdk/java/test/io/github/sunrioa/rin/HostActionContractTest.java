package io.github.sunrioa.rin;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

final class HostActionContractTest {
    private static final String SPEC_DIGEST =
            "eb6781411eb3558d55c01dd710952c06111f0d1c3d0a220eef0824f6b5806f38";
    private static final String REQUEST_DIGEST =
            "8227d8c696b50aeb5c74323d3bb3c978576c617c516a72628db730d4ee1d69d7";
    private static final String EFFECT_DIGEST =
            "de4b9b1f3329993edcf4e9cdd045f1a6a558db48a025b2a0fa9597431f530619";

    private HostActionContractTest() { }

    static void run() {
        Map<String, Object> spec = HostActionContract.sealCapability(specDraft());
        require(SPEC_DIGEST.equals(spec.get("digest")),
                "Java capability digest differs from the shared Go fixture");

        Map<String, Object> epoch = epoch();
        Map<String, Object> request = request(spec, epoch);
        require(REQUEST_DIGEST.equals(HostActionContract.actionRequestDigest(request)),
                "Java ActionRequest digest differs from the shared Go fixture");
        List<Map<String, Object>> effects = effects(epoch);
        require(EFFECT_DIGEST.equals(HostActionContract.effectPreviewDigest(effects)),
                "Java Effect digest differs from the shared Go fixture");

        Map<String, Object> binding = HostActionContract.sealBinding(
                spec,
                request,
                Map.of(
                        "binding_id", "binding.move.1",
                        "resolved_targets", List.of(target(epoch)),
                        "effect_preview", effects,
                        "valid_until", Map.of("clock", "realtime", "value", 15_000L)),
                Map.of("clock", "realtime", "value", 10_000L),
                epoch,
                7L);
        require(REQUEST_DIGEST.equals(binding.get("request_digest"))
                        && EFFECT_DIGEST.equals(binding.get("effect_digest"))
                        && JsonValues.equivalent(binding.get("normalized_arguments"),
                        Map.of("count", 2L, "target", "dock")),
                "Java BoundAction did not preserve the shared fixture");

        requireRejected(() -> HostActionContract.sealBinding(
                spec, request, bindingDraft(epoch, effects, 15_000L),
                Map.of("clock", "realtime", "value", 10_000L), epoch, 8L));
        requireRejected(() -> HostActionContract.sealBinding(
                spec, request, bindingDraft(epoch, effects, 20_001L),
                Map.of("clock", "realtime", "value", 10_000L), epoch, 7L));
        List<Map<String, Object>> lowRisk = new ArrayList<>(effects);
        Map<String, Object> weakened = new LinkedHashMap<>(lowRisk.get(0));
        weakened.put("risk", "low");
        lowRisk.set(0, weakened);
        requireRejected(() -> HostActionContract.sealBinding(
                spec, request, bindingDraft(epoch, lowRisk, 15_000L),
                Map.of("clock", "realtime", "value", 10_000L), epoch, 7L));
        requireRejected(() -> HostActionContract.sealBinding(
                spec, request, bindingDraft(epoch, List.of(effects.get(0), effects.get(0)),
                        15_000L),
                Map.of("clock", "realtime", "value", 10_000L), epoch, 7L));
        Map<String, Object> nestedAttributes = new LinkedHashMap<>(effects.get(0));
        nestedAttributes.put("attributes", Map.of("nested", Map.of("unsafe", true)));
        requireRejected(() -> HostActionContract.effectPreviewDigest(
                List.of(nestedAttributes)));
    }

    private static Map<String, Object> specDraft() {
        Map<String, Object> input = HostActionContract.schema(Map.of(
                "$schema", HostActionContract.SCHEMA_DIALECT,
                "type", "object",
                "properties", Map.of(
                        "target", Map.of("type", "string", "minLength", 1L),
                        "count", Map.of("type", "integer", "minimum", 1L, "maximum", 8L)),
                "required", List.of("target", "count"),
                "additionalProperties", false));
        Map<String, Object> output = HostActionContract.schema(Map.of(
                "$schema", HostActionContract.SCHEMA_DIALECT,
                "type", "object",
                "properties", Map.of(
                        "state", Map.of("enum", List.of(
                                "reached", "unreachable", "interrupted"))),
                "required", List.of("state"),
                "additionalProperties", false));
        Map<String, Object> effect = HostActionContract.schema(Map.of(
                "$schema", HostActionContract.SCHEMA_DIALECT,
                "type", "object",
                "properties", Map.of(
                        "distance", Map.of("type", "integer", "minimum", 0L),
                        "mode", Map.of("enum", List.of("walk", "run"))),
                "required", List.of("distance", "mode"),
                "additionalProperties", false));
        Map<String, Object> draft = new LinkedHashMap<>();
        draft.put("capability", Map.of(
                "id", "rin.navigation.move-to", "version", "2.0.0"));
        draft.put("description",
                "Move through the authoritative Host navigation system.");
        draft.put("input", input);
        draft.put("output", output);
        draft.put("effect_schema", effect);
        draft.put("kind", "atomic");
        draft.put("execution", "long-running");
        draft.put("cancellation", "cooperative");
        draft.put("risk_floor", "moderate");
        draft.put("required_durability", "advisory");
        draft.put("required_scopes", List.of("rin.actor.move"));
        draft.put("execution_budget", Map.of("clock", "realtime", "value", 10_000L));
        draft.put("max_input_bytes", 1024L);
        draft.put("max_output_bytes", 1024L);
        draft.put("max_effects", 4L);
        draft.put("produces_child_operations", false);
        return draft;
    }

    private static Map<String, Object> request(
            Map<String, Object> spec,
            Map<String, Object> epoch) {
        Map<String, Object> request = new LinkedHashMap<>();
        request.put("request_id", "request.move.1");
        request.put("controller_id", "controller.external.1");
        request.put("actor_id", "actor.guide");
        request.put("capability", spec.get("capability"));
        request.put("spec_digest", spec.get("digest"));
        request.put("arguments", Map.of("target", "dock", "count", 2L));
        request.put("target_refs", List.of(target(epoch)));
        request.put("expected_epoch", epoch);
        request.put("observation_sequence", 7L);
        request.put("task_id", "task.reach.dock");
        request.put("idempotency_key", "request.move.1");
        return request;
    }

    private static Map<String, Object> bindingDraft(
            Map<String, Object> epoch,
            List<Map<String, Object>> effects,
            long deadline) {
        return Map.of(
                "binding_id", "binding.move.1",
                "resolved_targets", List.of(target(epoch)),
                "effect_preview", effects,
                "valid_until", Map.of("clock", "realtime", "value", deadline));
    }

    private static List<Map<String, Object>> effects(Map<String, Object> epoch) {
        Map<String, Object> position = new LinkedHashMap<>();
        position.put("effect_id", "effect.move.position");
        position.put("kind", "world.position");
        position.put("operation", "update");
        position.put("subject_ref", target(epoch));
        position.put("tags", List.of("world.travel", "actor.movement"));
        position.put("ownership", "actor");
        position.put("scope", "world.public");
        position.put("quantity", 12L);
        position.put("unit", "block");
        position.put("reversible", true);
        position.put("risk", "moderate");
        position.put("attributes", Map.of("mode", "walk", "distance", 12L));

        Map<String, Object> stamina = new LinkedHashMap<>();
        stamina.put("effect_id", "effect.move.stamina");
        stamina.put("kind", "actor.stamina");
        stamina.put("operation", "consume");
        stamina.put("subject_ref", target(epoch));
        stamina.put("tags", List.of("world.travel", "actor.resource"));
        stamina.put("ownership", "actor");
        stamina.put("scope", "actor.self");
        stamina.put("quantity", 1L);
        stamina.put("unit", "point");
        stamina.put("reversible", false);
        stamina.put("risk", "moderate");
        stamina.put("attributes", Map.of("mode", "walk", "distance", 12L));
        return List.of(position, stamina);
    }

    private static Map<String, Object> target(Map<String, Object> epoch) {
        return Map.of(
                "namespace", "test.world",
                "type", "location",
                "key", "dock",
                "ephemeral", false,
                "epoch", epoch);
    }

    private static Map<String, Object> epoch() {
        return Map.of(
                "session_id", "session.test",
                "world_id", "world.test",
                "host", 1L,
                "world", 2L,
                "timeline", 3L);
    }

    private static void requireRejected(Runnable action) {
        try {
            action.run();
            throw new AssertionError("Invalid Java Host action was accepted");
        } catch (IllegalArgumentException expected) {
            // Expected.
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
