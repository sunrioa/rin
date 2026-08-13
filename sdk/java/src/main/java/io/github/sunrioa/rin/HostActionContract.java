package io.github.sunrioa.rin;

import java.math.BigDecimal;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HexFormat;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.regex.Pattern;

/**
 * Dependency-free V2 Host contract helpers for Java game adapters.
 *
 * <p>The game adapter remains responsible for parsing capability arguments
 * into engine-owned types and validating them against its published schema.
 * These helpers normalize and seal the transport contract; they never access
 * an engine object or authorize an effect.</p>
 */
public final class HostActionContract {
    public static final String CONTRACT_VERSION = "rin.host/v2";
    public static final String SCHEMA_DIALECT =
            "https://json-schema.org/draft/2020-12/schema";
    private static final long MAX_JSON_SAFE_INTEGER = 9_007_199_254_740_991L;
    private static final int MAX_SCHEMA_BYTES = 64 << 10;
    private static final int MAX_INSTANCE_BYTES = 1 << 20;
    private static final Pattern IDENTIFIER = Pattern.compile(
            "[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*");
    private static final Pattern SHA256 = Pattern.compile("[0-9a-f]{64}");
    private static final Pattern VERSION = Pattern.compile(
            "(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)"
                    + "(?:-([0-9A-Za-z.-]+))?(?:\\+([0-9A-Za-z.-]+))?");
    private static final Set<String> CLOCKS = Set.of("event", "step", "realtime");
    private static final Set<String> RISKS = Set.of("low", "moderate", "high", "critical");
    private static final Set<String> OWNERSHIP = Set.of(
            "unknown", "system", "actor", "controller", "player", "shared", "unowned");
    private static final Set<String> EFFECT_OPERATIONS = Set.of(
            "read", "create", "update", "delete", "transfer", "consume",
            "execute", "communicate");

    private HostActionContract() { }

    /** Creates a canonical, self-contained object schema and its SHA-256. */
    public static Map<String, Object> schema(Map<String, ?> document) {
        Map<String, Object> normalized = jsonObject(document, "schema.document");
        if (!SCHEMA_DIALECT.equals(normalized.get("$schema"))
                || !"object".equals(normalized.get("type"))
                || !Boolean.FALSE.equals(normalized.get("additionalProperties"))) {
            throw invalid("schema must be a closed JSON Schema 2020-12 object");
        }
        rejectExternalReferences(normalized);
        byte[] encoded = canonicalBytes(normalized);
        if (encoded.length == 0 || encoded.length > MAX_SCHEMA_BYTES) {
            throw invalid("schema document exceeds 65536 bytes");
        }
        return object(
                "dialect", SCHEMA_DIALECT,
                "document", normalized,
                "sha256", sha256(encoded));
    }

    /** Validates, normalizes, and adds the digest to one CapabilitySpec draft. */
    public static Map<String, Object> sealCapability(Map<String, ?> draft) {
        requireShape(draft, Set.of(
                "capability", "description", "input", "output", "effect_schema",
                "kind", "execution", "cancellation", "risk_floor",
                "required_durability", "execution_budget", "max_input_bytes",
                "max_output_bytes", "max_effects", "produces_child_operations"),
                Set.of("required_scopes", "digest"), "capability spec");
        Map<String, Object> capability = capabilityRef(draft.get("capability"));
        String description = text(draft.get("description"), "description", 500, false);
        Map<String, Object> input = sealedSchema(draft.get("input"));
        Map<String, Object> output = sealedSchema(draft.get("output"));
        Map<String, Object> effectSchema = sealedSchema(draft.get("effect_schema"));
        String kind = oneOf(draft.get("kind"), "kind", Set.of("atomic", "macro"));
        String execution = oneOf(draft.get("execution"), "execution",
                Set.of("immediate", "queued", "long-running"));
        String cancellation = oneOf(draft.get("cancellation"), "cancellation",
                Set.of("unsupported", "cooperative", "preemptive"));
        String riskFloor = oneOf(draft.get("risk_floor"), "risk_floor", RISKS);
        String durability = oneOf(draft.get("required_durability"),
                "required_durability",
                Set.of("advisory", "idempotent-action", "transactional-action"));
        List<String> scopes = namespacedIdentifiers(draft.get("required_scopes"),
                "required_scopes", 32, true);
        Map<String, Object> budget = duration(draft.get("execution_budget"));
        long maxInput = whole(draft.get("max_input_bytes"),
                "max_input_bytes", 1, MAX_INSTANCE_BYTES);
        long maxOutput = whole(draft.get("max_output_bytes"),
                "max_output_bytes", 1, MAX_INSTANCE_BYTES);
        long maxEffects = whole(draft.get("max_effects"), "max_effects", 1, 64);
        boolean children = bool(draft.get("produces_child_operations"),
                "produces_child_operations");
        if (kind.equals("atomic") && children) {
            throw invalid("atomic capability cannot produce child operations");
        }
        if (execution.equals("immediate") && !cancellation.equals("unsupported")) {
            throw invalid("immediate capability cannot be cancelled");
        }

        OrderedObject digestValue = ordered(
                "capability", orderedCapabilityRef(capability),
                "description", description,
                "input_sha256", input.get("sha256"),
                "output_sha256", output.get("sha256"),
                "effect_schema_sha256", effectSchema.get("sha256"),
                "kind", kind,
                "execution", execution,
                "cancellation", cancellation,
                "risk_floor", riskFloor,
                "required_durability", durability);
        if (!scopes.isEmpty()) digestValue.put("required_scopes", scopes);
        digestValue.put("execution_budget", orderedTime(budget));
        digestValue.put("max_input_bytes", maxInput);
        digestValue.put("max_output_bytes", maxOutput);
        digestValue.put("max_effects", maxEffects);
        digestValue.put("produces_child_operations", children);
        String digest = sha256(orderedBytes(digestValue));
        if (draft.containsKey("digest") && !digest.equals(draft.get("digest"))) {
            throw invalid("capability digest does not match the normalized spec");
        }

        Map<String, Object> sealed = object(
                "capability", capability,
                "description", description,
                "input", input,
                "output", output,
                "effect_schema", effectSchema,
                "kind", kind,
                "execution", execution,
                "cancellation", cancellation,
                "risk_floor", riskFloor,
                "required_durability", durability);
        if (!scopes.isEmpty()) sealed.put("required_scopes", scopes);
        sealed.put("execution_budget", budget);
        sealed.put("max_input_bytes", maxInput);
        sealed.put("max_output_bytes", maxOutput);
        sealed.put("max_effects", maxEffects);
        sealed.put("produces_child_operations", children);
        sealed.put("digest", digest);
        return sealed;
    }

    /** Computes the Go-compatible digest for one controller ActionRequest. */
    public static String actionRequestDigest(Map<String, ?> request) {
        Map<String, Object> normalized = actionRequest(request);
        return sha256(orderedBytes(orderedActionRequest(normalized)));
    }

    /** Computes the Go-compatible digest for a normalized Effect preview. */
    public static String effectPreviewDigest(List<? extends Map<String, ?>> effects) {
        if (effects == null || effects.isEmpty() || effects.size() > 64) {
            throw invalid("effect preview must contain between 1 and 64 effects");
        }
        List<Object> encoded = new ArrayList<>();
        for (Map<String, ?> effect : effects) {
            encoded.add(orderedEffect(normalizeEffect(effect, null, "low")));
        }
        encoded.sort(Comparator.comparing(value ->
                String.valueOf(((Map<?, ?>) value).get("effect_id"))));
        return sha256(orderedBytes(encoded));
    }

    /**
     * Seals one adapter-authored BindingDraft against current Host state.
     * Capability-specific argument/schema checks must run before this call.
     */
    public static Map<String, Object> sealBinding(
            Map<String, ?> sealedSpec,
            Map<String, ?> request,
            Map<String, ?> draft,
            Map<String, ?> nowValue,
            Map<String, ?> currentEpochValue,
            long currentObservationSequence) {
        Map<String, Object> spec = sealCapability(sealedSpec);
        Map<String, Object> actionRequest = actionRequest(request);
        Map<String, Object> currentEpoch = epoch(currentEpochValue, "current_epoch");
        if (!JsonValues.equivalent(actionRequest.get("expected_epoch"), currentEpoch)
                || whole(actionRequest.get("observation_sequence"),
                "observation_sequence", 1, MAX_JSON_SAFE_INTEGER)
                != currentObservationSequence) {
            throw invalid("action request belongs to a stale Host observation");
        }
        if (!JsonValues.equivalent(actionRequest.get("capability"), spec.get("capability"))
                || !actionRequest.get("spec_digest").equals(spec.get("digest"))) {
            throw invalid("action request does not match the capability spec");
        }
        byte[] arguments = canonicalBytes(actionRequest.get("arguments"));
        long inputLimit = whole(spec.get("max_input_bytes"),
                "max_input_bytes", 1, MAX_INSTANCE_BYTES);
        if (arguments.length > inputLimit) throw invalid("arguments exceed capability input limit");

        requireShape(draft, Set.of("binding_id", "effect_preview", "valid_until"),
                Set.of("resolved_targets"), "binding draft");
        String bindingId = identifier(draft.get("binding_id"), "binding_id");
        List<Map<String, Object>> requestedTargets = refs(
                actionRequest.get("target_refs"), currentEpoch, "target_refs");
        List<Map<String, Object>> resolvedTargets = refs(
                draft.get("resolved_targets"), currentEpoch, "resolved_targets");
        if (!requestedTargets.isEmpty() && resolvedTargets.isEmpty()) {
            throw invalid("targeted request must resolve at least one Host reference");
        }
        Map<String, Object> now = timepoint(nowValue, "now");
        Map<String, Object> validUntil = timepoint(draft.get("valid_until"), "valid_until");
        Map<String, Object> budget = objectValue(spec.get("execution_budget"),
                "execution_budget");
        if (!now.get("clock").equals(validUntil.get("clock"))
                || !now.get("clock").equals(budget.get("clock"))) {
            throw invalid("binding clocks do not match the capability budget");
        }
        long nowTick = whole(now.get("value"), "now.value", 0, MAX_JSON_SAFE_INTEGER);
        long deadline = whole(validUntil.get("value"),
                "valid_until.value", 0, MAX_JSON_SAFE_INTEGER);
        long budgetValue = whole(budget.get("value"),
                "execution_budget.value", 1, MAX_JSON_SAFE_INTEGER);
        if (nowTick > MAX_JSON_SAFE_INTEGER - budgetValue
                || deadline <= nowTick || deadline > nowTick + budgetValue) {
            throw invalid("valid_until must be after now and within the execution budget");
        }

        List<?> rawEffects = listValue(draft.get("effect_preview"), "effect_preview");
        int maxEffects = Math.toIntExact(whole(spec.get("max_effects"),
                "max_effects", 1, 64));
        if (rawEffects.isEmpty() || rawEffects.size() > maxEffects) {
            throw invalid("effect preview exceeds capability limit");
        }
        List<Map<String, Object>> effects = new ArrayList<>();
        for (Object raw : rawEffects) {
            effects.add(normalizeEffect(objectValue(raw, "effect"), currentEpoch,
                    (String) spec.get("risk_floor")));
        }
        effects.sort(Comparator.comparing(effect -> (String) effect.get("effect_id")));
        for (int index = 1; index < effects.size(); index++) {
            if (effects.get(index - 1).get("effect_id").equals(
                    effects.get(index).get("effect_id"))) {
                throw invalid("effect preview contains duplicate effect_id values");
            }
        }

        String requestDigest = sha256(orderedBytes(orderedActionRequest(actionRequest)));
        List<Object> orderedEffects = effects.stream()
                .map(HostActionContract::orderedEffect)
                .map(value -> (Object) value)
                .toList();
        String effectDigest = sha256(orderedBytes(orderedEffects));
        Map<String, Object> bound = object(
                "binding_id", bindingId,
                "request_id", actionRequest.get("request_id"),
                "request_digest", requestDigest,
                "controller_id", actionRequest.get("controller_id"),
                "actor_id", actionRequest.get("actor_id"),
                "capability", actionRequest.get("capability"),
                "spec_digest", actionRequest.get("spec_digest"),
                "normalized_arguments", actionRequest.get("arguments"));
        if (!requestedTargets.isEmpty()) bound.put("requested_targets", requestedTargets);
        if (!resolvedTargets.isEmpty()) bound.put("resolved_targets", resolvedTargets);
        bound.put("expected_epoch", currentEpoch);
        bound.put("observation_sequence", currentObservationSequence);
        if (actionRequest.containsKey("task_id")) {
            bound.put("task_id", actionRequest.get("task_id"));
        }
        if (actionRequest.containsKey("plan_step_ref")) {
            bound.put("plan_step_ref", actionRequest.get("plan_step_ref"));
        }
        bound.put("idempotency_key", actionRequest.get("idempotency_key"));
        bound.put("effect_preview", effects);
        bound.put("effect_digest", effectDigest);
        bound.put("bound_at", now);
        bound.put("valid_until", validUntil);
        return bound;
    }

    private static Map<String, Object> actionRequest(Map<String, ?> request) {
        requireShape(request, Set.of(
                "request_id", "controller_id", "actor_id", "capability", "spec_digest",
                "arguments", "expected_epoch", "observation_sequence", "idempotency_key"),
                Set.of("target_refs", "task_id", "plan_step_ref"), "action request");
        Map<String, Object> normalized = object(
                "request_id", identifier(request.get("request_id"), "request_id"),
                "controller_id", identifier(request.get("controller_id"), "controller_id"),
                "actor_id", identifier(request.get("actor_id"), "actor_id"),
                "capability", capabilityRef(request.get("capability")),
                "spec_digest", digest(request.get("spec_digest"), "spec_digest"),
                "arguments", jsonObject(objectValue(request.get("arguments"), "arguments"),
                        "arguments"));
        Map<String, Object> expectedEpoch = epoch(request.get("expected_epoch"),
                "expected_epoch");
        List<Map<String, Object>> targets = refs(
                request.get("target_refs"), expectedEpoch, "target_refs");
        if (!targets.isEmpty()) normalized.put("target_refs", targets);
        normalized.put("expected_epoch", expectedEpoch);
        normalized.put("observation_sequence", whole(request.get("observation_sequence"),
                "observation_sequence", 1, MAX_JSON_SAFE_INTEGER));
        if (request.containsKey("task_id")) {
            normalized.put("task_id", identifier(request.get("task_id"), "task_id"));
        }
        if (request.containsKey("plan_step_ref")) {
            if (!request.containsKey("task_id")) {
                throw invalid("plan_step_ref requires task_id");
            }
            normalized.put("plan_step_ref", planStepRef(request.get("plan_step_ref")));
        }
        normalized.put("idempotency_key",
                identifier(request.get("idempotency_key"), "idempotency_key"));
        return normalized;
    }

    private static OrderedObject orderedActionRequest(Map<String, Object> request) {
        OrderedObject value = ordered(
                "request_id", request.get("request_id"),
                "controller_id", request.get("controller_id"),
                "actor_id", request.get("actor_id"),
                "capability", orderedCapabilityRef(objectValue(
                        request.get("capability"), "capability")),
                "spec_digest", request.get("spec_digest"),
                "arguments", request.get("arguments"));
        if (request.containsKey("target_refs")) {
            value.put("target_refs", orderedRefs(listValue(
                    request.get("target_refs"), "target_refs")));
        }
        value.put("expected_epoch", orderedEpoch(objectValue(
                request.get("expected_epoch"), "expected_epoch")));
        value.put("observation_sequence", request.get("observation_sequence"));
        if (request.containsKey("task_id")) value.put("task_id", request.get("task_id"));
        if (request.containsKey("plan_step_ref")) {
            value.put("plan_step_ref", orderedPlanStepRef(objectValue(
                    request.get("plan_step_ref"), "plan_step_ref")));
        }
        value.put("idempotency_key", request.get("idempotency_key"));
        return value;
    }

    private static Map<String, Object> planStepRef(Object raw) {
        Map<String, ?> ref = objectValue(raw, "plan_step_ref");
        requireShape(ref, Set.of("plan_id", "plan_revision", "step_id"), Set.of(),
                "plan_step_ref");
        return object(
                "plan_id", identifier(ref.get("plan_id"), "plan_step_ref.plan_id"),
                "plan_revision", whole(ref.get("plan_revision"),
                        "plan_step_ref.plan_revision", 1, MAX_JSON_SAFE_INTEGER),
                "step_id", identifier(ref.get("step_id"), "plan_step_ref.step_id"));
    }

    private static OrderedObject orderedPlanStepRef(Map<String, Object> ref) {
        return ordered(
                "plan_id", ref.get("plan_id"),
                "plan_revision", ref.get("plan_revision"),
                "step_id", ref.get("step_id"));
    }

    private static Map<String, Object> normalizeEffect(
            Map<String, ?> raw,
            Map<String, Object> expectedEpoch,
            String riskFloor) {
        requireShape(raw, Set.of(
                "effect_id", "kind", "operation", "ownership", "scope",
                "reversible", "risk", "attributes"),
                Set.of("subject_ref", "target_ref", "tags", "quantity", "unit"),
                "effect");
        String risk = oneOf(raw.get("risk"), "risk", RISKS);
        if (riskRank(risk) < riskRank(riskFloor)) {
            throw invalid("effect risk is below capability risk_floor");
        }
        Map<String, Object> effect = object(
                "effect_id", identifier(raw.get("effect_id"), "effect_id"),
                "kind", namespacedIdentifier(raw.get("kind"), "kind"),
                "operation", oneOf(raw.get("operation"), "operation", EFFECT_OPERATIONS));
        if (raw.containsKey("subject_ref")) {
            effect.put("subject_ref", ref(raw.get("subject_ref"), expectedEpoch, "subject_ref"));
        }
        if (raw.containsKey("target_ref")) {
            effect.put("target_ref", ref(raw.get("target_ref"), expectedEpoch, "target_ref"));
        }
        List<String> tags = namespacedIdentifiers(raw.get("tags"), "tags", 32, true);
        if (!tags.isEmpty()) effect.put("tags", tags);
        effect.put("ownership", oneOf(raw.get("ownership"), "ownership", OWNERSHIP));
        effect.put("scope", namespacedIdentifier(raw.get("scope"), "scope"));
        if (raw.containsKey("quantity")) {
            long quantity = whole(raw.get("quantity"), "quantity", 0, MAX_JSON_SAFE_INTEGER);
            if (quantity != 0) effect.put("quantity", quantity);
        }
        if (raw.containsKey("unit")) {
            String unit = text(raw.get("unit"), "unit", 96, true);
            if (!unit.isEmpty()) effect.put("unit", identifier(unit, "unit"));
        }
        effect.put("reversible", bool(raw.get("reversible"), "reversible"));
        effect.put("risk", risk);
        Map<String, Object> attributes = scalarObject(raw.get("attributes"), "attributes");
        effect.put("attributes", attributes);
        return effect;
    }

    private static OrderedObject orderedEffect(Map<String, Object> effect) {
        OrderedObject value = ordered(
                "effect_id", effect.get("effect_id"),
                "kind", effect.get("kind"),
                "operation", effect.get("operation"));
        if (effect.containsKey("subject_ref")) {
            value.put("subject_ref", orderedRef(objectValue(
                    effect.get("subject_ref"), "subject_ref")));
        }
        if (effect.containsKey("target_ref")) {
            value.put("target_ref", orderedRef(objectValue(
                    effect.get("target_ref"), "target_ref")));
        }
        if (effect.containsKey("tags")) value.put("tags", effect.get("tags"));
        value.put("ownership", effect.get("ownership"));
        value.put("scope", effect.get("scope"));
        if (effect.containsKey("quantity")) value.put("quantity", effect.get("quantity"));
        if (effect.containsKey("unit")) value.put("unit", effect.get("unit"));
        value.put("reversible", effect.get("reversible"));
        value.put("risk", effect.get("risk"));
        value.put("attributes", effect.get("attributes"));
        return value;
    }

    private static Map<String, Object> sealedSchema(Object raw) {
        Map<String, Object> value = objectValue(raw, "schema");
        requireShape(value, Set.of("dialect", "document", "sha256"), Set.of(), "schema");
        if (!SCHEMA_DIALECT.equals(value.get("dialect"))) {
            throw invalid("unsupported schema dialect");
        }
        Map<String, Object> rebuilt = schema(objectValue(value.get("document"),
                "schema.document"));
        if (!rebuilt.get("sha256").equals(value.get("sha256"))) {
            throw invalid("schema digest does not match its canonical document");
        }
        return rebuilt;
    }

    private static Map<String, Object> capabilityRef(Object raw) {
        Map<String, Object> value = objectValue(raw, "capability");
        requireShape(value, Set.of("id", "version"), Set.of(), "capability");
        return object(
                "id", namespacedIdentifier(value.get("id"), "capability.id"),
                "version", exactVersion(value.get("version"), "capability.version"));
    }

    private static OrderedObject orderedCapabilityRef(Map<String, Object> value) {
        return ordered("id", value.get("id"), "version", value.get("version"));
    }

    private static Map<String, Object> duration(Object raw) {
        Map<String, Object> value = timepoint(raw, "execution_budget");
        if ((long) value.get("value") < 1) {
            throw invalid("execution budget must be positive");
        }
        return value;
    }

    private static Map<String, Object> timepoint(Object raw, String field) {
        Map<String, Object> value = objectValue(raw, field);
        requireShape(value, Set.of("clock", "value"), Set.of(), field);
        return object(
                "clock", oneOf(value.get("clock"), field + ".clock", CLOCKS),
                "value", whole(value.get("value"), field + ".value",
                        0, MAX_JSON_SAFE_INTEGER));
    }

    private static OrderedObject orderedTime(Map<String, Object> value) {
        return ordered("clock", value.get("clock"), "value", value.get("value"));
    }

    private static Map<String, Object> epoch(Object raw, String field) {
        Map<String, Object> value = objectValue(raw, field);
        requireShape(value, Set.of(
                "session_id", "world_id", "host", "world", "timeline"),
                Set.of(), field);
        return object(
                "session_id", identifier(value.get("session_id"), field + ".session_id"),
                "world_id", identifier(value.get("world_id"), field + ".world_id"),
                "host", whole(value.get("host"), field + ".host", 1, MAX_JSON_SAFE_INTEGER),
                "world", whole(value.get("world"), field + ".world", 1, MAX_JSON_SAFE_INTEGER),
                "timeline", whole(value.get("timeline"), field + ".timeline",
                        1, MAX_JSON_SAFE_INTEGER));
    }

    private static OrderedObject orderedEpoch(Map<String, Object> value) {
        return ordered(
                "session_id", value.get("session_id"),
                "world_id", value.get("world_id"),
                "host", value.get("host"),
                "world", value.get("world"),
                "timeline", value.get("timeline"));
    }

    private static Map<String, Object> ref(
            Object raw,
            Map<String, Object> expectedEpoch,
            String field) {
        Map<String, Object> value = objectValue(raw, field);
        requireShape(value, Set.of(
                "namespace", "type", "key", "ephemeral", "epoch"),
                Set.of(), field);
        Map<String, Object> refEpoch = epoch(value.get("epoch"), field + ".epoch");
        if (expectedEpoch != null && !JsonValues.equivalent(expectedEpoch, refEpoch)) {
            throw invalid(field + " belongs to another Host epoch");
        }
        return object(
                "namespace", namespacedIdentifier(
                        value.get("namespace"), field + ".namespace"),
                "type", identifier(value.get("type"), field + ".type"),
                "key", text(value.get("key"), field + ".key", 256, false),
                "ephemeral", bool(value.get("ephemeral"), field + ".ephemeral"),
                "epoch", refEpoch);
    }

    private static OrderedObject orderedRef(Map<String, Object> value) {
        return ordered(
                "namespace", value.get("namespace"),
                "type", value.get("type"),
                "key", value.get("key"),
                "ephemeral", value.get("ephemeral"),
                "epoch", orderedEpoch(objectValue(value.get("epoch"), "epoch")));
    }

    private static List<Map<String, Object>> refs(
            Object raw,
            Map<String, Object> expectedEpoch,
            String field) {
        if (raw == null) return List.of();
        List<?> values = listValue(raw, field);
        if (values.size() > 64) throw invalid(field + " contains more than 64 references");
        List<Map<String, Object>> result = new ArrayList<>();
        for (Object value : values) result.add(ref(value, expectedEpoch, field));
        return List.copyOf(result);
    }

    private static List<Object> orderedRefs(List<?> values) {
        List<Object> result = new ArrayList<>();
        for (Object value : values) {
            result.add(orderedRef(objectValue(value, "reference")));
        }
        return result;
    }

    private static Map<String, Object> scalarObject(Object raw, String field) {
        Map<String, Object> value = objectValue(raw, field);
        if (value.size() > 64) throw invalid(field + " contains more than 64 properties");
        Map<String, Object> result = new TreeMap<>();
        for (Map.Entry<String, Object> entry : value.entrySet()) {
            identifier(entry.getKey(), field + ".key");
            Object item = entry.getValue();
            if (!(item instanceof String || item instanceof Boolean || item instanceof Number)) {
                throw invalid(field + " values must be JSON scalars");
            }
            result.put(entry.getKey(), normalizeJson(item, field));
        }
        if (canonicalBytes(result).length > 16 << 10) {
            throw invalid(field + " exceeds 16384 bytes");
        }
        return new LinkedHashMap<>(result);
    }

    private static List<String> identifiers(
            Object raw,
            String field,
            int maximum,
            boolean sort) {
        if (raw == null) return List.of();
        List<?> values = listValue(raw, field);
        if (values.size() > maximum) throw invalid(field + " exceeds its item limit");
        List<String> result = new ArrayList<>();
        for (Object value : values) result.add(identifier(value, field));
        if (sort) result.sort(String::compareTo);
        if (new LinkedHashSet<>(result).size() != result.size()) {
            throw invalid(field + " contains duplicate values");
        }
        return List.copyOf(result);
    }

    private static List<String> namespacedIdentifiers(
            Object raw,
            String field,
            int maximum,
            boolean sort) {
        List<String> result = identifiers(raw, field, maximum, sort);
        if (result.stream().anyMatch(value -> !value.contains("."))) {
            throw invalid(field + " values must be namespaced");
        }
        return result;
    }

    private static void rejectExternalReferences(Object value) {
        if (value instanceof Map<?, ?> map) {
            for (Map.Entry<?, ?> entry : map.entrySet()) {
                if (entry.getKey().equals("$ref")
                        && entry.getValue() instanceof String ref
                        && !ref.startsWith("#")) {
                    throw invalid("external schema references are not allowed");
                }
                rejectExternalReferences(entry.getValue());
            }
        } else if (value instanceof List<?> list) {
            list.forEach(HostActionContract::rejectExternalReferences);
        }
    }

    private static void requireShape(
            Map<String, ?> value,
            Set<String> required,
            Set<String> optional,
            String field) {
        if (value == null || !value.keySet().containsAll(required)) {
            throw invalid(field + " is missing required fields");
        }
        Set<String> allowed = new LinkedHashSet<>(required);
        allowed.addAll(optional);
        if (!allowed.containsAll(value.keySet())) {
            throw invalid(field + " contains unsupported fields");
        }
    }

    private static int riskRank(String risk) {
        return switch (risk) {
            case "low" -> 0;
            case "moderate" -> 1;
            case "high" -> 2;
            case "critical" -> 3;
            default -> throw invalid("unsupported risk");
        };
    }

    private static String oneOf(Object raw, String field, Set<String> allowed) {
        String value = text(raw, field, 64, false);
        if (!allowed.contains(value)) throw invalid(field + " is unsupported");
        return value;
    }

    private static String identifier(Object raw, String field) {
        String value = text(raw, field, 96, false);
        if (!IDENTIFIER.matcher(value).matches()) throw invalid(field + " is invalid");
        return value;
    }

    private static String namespacedIdentifier(Object raw, String field) {
        String value = identifier(raw, field);
        if (!value.contains(".")) throw invalid(field + " must be namespaced");
        return value;
    }

    private static String exactVersion(Object raw, String field) {
        String value = text(raw, field, 96, false);
        java.util.regex.Matcher match = VERSION.matcher(value);
        if (!match.matches()) throw invalid(field + " must be an exact SemVer version");
        validateVersionIdentifiers(match.group(4), true, field);
        validateVersionIdentifiers(match.group(5), false, field);
        return value;
    }

    private static void validateVersionIdentifiers(
            String value,
            boolean prerelease,
            String field) {
        if (value == null || value.isEmpty()) return;
        for (String part : value.split("\\.", -1)) {
            if (part.isEmpty()) throw invalid(field + " contains an empty identifier");
            if (prerelease && part.length() > 1 && part.charAt(0) == '0'
                    && part.chars().allMatch(Character::isDigit)) {
                throw invalid(field + " contains a numeric identifier with a leading zero");
            }
        }
    }

    private static String digest(Object raw, String field) {
        String value = text(raw, field, 64, false);
        if (!SHA256.matcher(value).matches()) throw invalid(field + " is not SHA-256");
        return value;
    }

    private static String text(Object raw, String field, int maximum, boolean empty) {
        if (!(raw instanceof String value) || (!empty && value.isBlank())
                || value.getBytes(StandardCharsets.UTF_8).length > maximum
                || containsUnsafeText(value)) {
            throw invalid(field + " is invalid");
        }
        return value;
    }

    private static boolean containsUnsafeText(String value) {
        for (int index = 0; index < value.length(); index++) {
            char character = value.charAt(index);
            if (character < 0x20 || character == 0x7f
                    || character == 0x85 || character == 0x2028 || character == 0x2029) {
                return true;
            }
        }
        return false;
    }

    private static boolean bool(Object raw, String field) {
        if (raw instanceof Boolean value) return value;
        throw invalid(field + " must be boolean");
    }

    private static long whole(Object raw, String field, long minimum, long maximum) {
        if (!(raw instanceof Number number)) throw invalid(field + " must be an integer");
        BigDecimal decimal;
        try {
            decimal = new BigDecimal(number.toString());
        } catch (NumberFormatException invalid) {
            throw invalid(field + " must be finite");
        }
        long value;
        try {
            value = decimal.longValueExact();
        } catch (ArithmeticException invalid) {
            throw invalid(field + " must be an integer");
        }
        if (value < minimum || value > maximum) throw invalid(field + " is out of range");
        return value;
    }

    private static Map<String, Object> jsonObject(Map<String, ?> raw, String field) {
        return objectValue(normalizeJson(raw, field), field);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> objectValue(Object raw, String field) {
        if (!(raw instanceof Map<?, ?> map)) throw invalid(field + " must be an object");
        for (Object key : map.keySet()) {
            if (!(key instanceof String)) throw invalid(field + " contains a non-string key");
        }
        return (Map<String, Object>) map;
    }

    private static List<?> listValue(Object raw, String field) {
        if (raw instanceof List<?> list) return list;
        throw invalid(field + " must be an array");
    }

    private static Object normalizeJson(Object value, String field) {
        if (value == null || value instanceof String || value instanceof Boolean) return value;
        if (value instanceof Number number) {
            BigDecimal decimal;
            try {
                decimal = new BigDecimal(number.toString());
            } catch (NumberFormatException invalid) {
                throw invalid(field + " contains a non-finite number");
            }
            if (decimal.abs().compareTo(BigDecimal.valueOf(MAX_JSON_SAFE_INTEGER)) > 0) {
                throw invalid(field + " contains a non-interoperable number");
            }
            BigDecimal normalized = decimal.stripTrailingZeros();
            try {
                return normalized.longValueExact();
            } catch (ArithmeticException fractional) {
                return new BigDecimal(normalized.toPlainString());
            }
        }
        if (value instanceof Map<?, ?> map) {
            Map<String, Object> result = new TreeMap<>();
            for (Map.Entry<?, ?> entry : map.entrySet()) {
                if (!(entry.getKey() instanceof String key)) {
                    throw invalid(field + " contains a non-string key");
                }
                result.put(key, normalizeJson(entry.getValue(), field));
            }
            return new LinkedHashMap<>(result);
        }
        if (value instanceof List<?> list) {
            List<Object> result = new ArrayList<>();
            for (Object item : list) result.add(normalizeJson(item, field));
            return Collections.unmodifiableList(result);
        }
        throw invalid(field + " contains a non-JSON value");
    }

    private static byte[] canonicalBytes(Object value) {
        return json(value, false).getBytes(StandardCharsets.UTF_8);
    }

    private static byte[] orderedBytes(Object value) {
        return json(value, true).getBytes(StandardCharsets.UTF_8);
    }

    private static String json(Object value, boolean preserveOrdered) {
        StringBuilder result = new StringBuilder();
        appendJson(result, value, preserveOrdered);
        return result.toString();
    }

    private static void appendJson(StringBuilder result, Object value, boolean preserveOrdered) {
        if (value == null) {
            result.append("null");
        } else if (value instanceof String text) {
            appendString(result, text);
        } else if (value instanceof Boolean bool) {
            result.append(bool);
        } else if (value instanceof Number number) {
            BigDecimal decimal = new BigDecimal(number.toString());
            result.append(decimal.stripTrailingZeros().toPlainString());
        } else if (value instanceof List<?> list) {
            result.append('[');
            for (int index = 0; index < list.size(); index++) {
                if (index > 0) result.append(',');
                appendJson(result, list.get(index), preserveOrdered);
            }
            result.append(']');
        } else if (value instanceof Map<?, ?> raw) {
            Map<String, Object> map = objectValue(raw, "json");
            List<String> keys = new ArrayList<>(map.keySet());
            if (!(preserveOrdered && value instanceof OrderedObject)) {
                keys.sort(String::compareTo);
            }
            result.append('{');
            for (int index = 0; index < keys.size(); index++) {
                if (index > 0) result.append(',');
                String key = keys.get(index);
                appendString(result, key);
                result.append(':');
                appendJson(result, map.get(key), preserveOrdered);
            }
            result.append('}');
        } else {
            throw invalid("value is not JSON-compatible");
        }
    }

    private static void appendString(StringBuilder result, String value) {
        result.append('"');
        for (int index = 0; index < value.length(); index++) {
            char character = value.charAt(index);
            switch (character) {
                case '"' -> result.append("\\\"");
                case '\\' -> result.append("\\\\");
                case '\b' -> result.append("\\b");
                case '\f' -> result.append("\\f");
                case '\n' -> result.append("\\n");
                case '\r' -> result.append("\\r");
                case '\t' -> result.append("\\t");
                default -> {
                    if (character < 0x20 || character == '<' || character == '>'
                            || character == '&' || character == 0x2028 || character == 0x2029) {
                        result.append(String.format("\\u%04x", (int) character));
                    } else {
                        result.append(character);
                    }
                }
            }
        }
        result.append('"');
    }

    private static String sha256(byte[] value) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(value));
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException("SHA-256 is unavailable", impossible);
        }
    }

    private static Map<String, Object> object(Object... entries) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }

    private static OrderedObject ordered(Object... entries) {
        OrderedObject result = new OrderedObject();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }

    private static IllegalArgumentException invalid(String message) {
        return new IllegalArgumentException("Invalid Rin Host action: " + message);
    }

    private static final class OrderedObject extends LinkedHashMap<String, Object> {
    }
}
