package io.github.sunrioa.rin;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * Engine-neutral constructors for fully bound offers and immediate reports.
 * The host still validates authority, epoch, deadline, targets, and game rules
 * immediately before executing the selected offer.
 */
public final class HostActions {
    private HostActions() { }

    public static Map<String, Object> offer(
            String offerId,
            String actorId,
            String capabilityId,
            String capabilityVersion,
            String descriptorDigest,
            String description,
            Map<String, ?> arguments,
            Map<String, ?> decisionWindow) {
        return offer(offerId, actorId, capabilityId, capabilityVersion,
                descriptorDigest, description, arguments, decisionWindow, null);
    }

    public static Map<String, Object> offer(
            String offerId,
            String actorId,
            String capabilityId,
            String capabilityVersion,
            String descriptorDigest,
            String description,
            Map<String, ?> arguments,
            Map<String, ?> decisionWindow,
            Map<String, ?> planning) {
        Objects.requireNonNull(decisionWindow, "decisionWindow");
        Map<String, Object> result = mapOf(
                "offer_id", offerId,
                "decision_window_id", decisionWindow.get("id"),
                "actor_id", actorId,
                "capability", mapOf("id", capabilityId, "version", capabilityVersion),
                "descriptor_digest", descriptorDigest,
                "description", description,
                "arguments", arguments,
                "expected_epoch", decisionWindow.get("epoch"),
                "observation_seq", decisionWindow.get("observation_seq"),
                "deadline", decisionWindow.get("deadline"));
        if (planning != null) result.put("planning", planning);
        return result;
    }

    public static Map<String, Object> immediateReport(
            String sessionId,
            String requestId,
            String eventId,
            long tick,
            Map<String, ?> proposal,
            String operationId,
            boolean accepted,
            String summary,
            Map<String, ?> outcomeEpoch,
            long worldSeq,
            Map<String, ?> occurredAt,
            List<String> tags) {
        Map<String, Object> report = mapOf(
                "proposal_id", proposal.get("id"),
                "event_id", eventId,
                "decision", accepted ? "accepted" : "rejected",
                "summary", summary,
                "tags", tags);
        if (accepted) {
            Map<String, Object> offer = object(proposal.get("action"));
            report.put("invocation", mapOf(
                    "operation_id", operationId,
                    "offer_id", offer.get("offer_id"),
                    "decision_window_id", offer.get("decision_window_id"),
                    "actor_id", offer.get("actor_id"),
                    "capability", offer.get("capability"),
                    "descriptor_digest", offer.get("descriptor_digest"),
                    "arguments", offer.get("arguments"),
                    "targets", offer.getOrDefault("targets", List.of()),
                    "expected_epoch", offer.get("expected_epoch"),
                    "observation_seq", offer.get("observation_seq"),
                    "deadline", offer.get("deadline")));
            report.put("run", mapOf(
                    "operation_id", operationId,
                    "status", "succeeded",
                    "progress_seq", 1L,
                    "progress", 100,
                    "updated_at", occurredAt));
            report.put("outcome", mapOf(
                    "operation_id", operationId,
                    "status", "succeeded",
                    "summary", summary,
                    "epoch", outcomeEpoch,
                    "world_seq", worldSeq,
                    "occurred_at", occurredAt));
        }
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", requestId,
                "tick", tick,
                "report", report);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value) {
        if (!(value instanceof Map<?, ?>)) {
            throw new RinConfigurationException(
                    "invalid_action_offer",
                    "Proposal action must be an object");
        }
        return (Map<String, Object>) value;
    }

    private static Map<String, Object> mapOf(Object... entries) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }
}
