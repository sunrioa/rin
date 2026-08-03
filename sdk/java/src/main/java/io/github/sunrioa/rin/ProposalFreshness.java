package io.github.sunrioa.rin;

import java.util.Map;

/** Host-independent freshness check immediately before applying a Proposal. */
public final class ProposalFreshness {
    public enum Decision { FRESH, STALE }

    private static final long MAX_JSON_SAFE_INTEGER = 9_007_199_254_740_991L;

    private ProposalFreshness() { }

    public static Decision evaluate(
            Map<String, ?> sessionState,
            Map<String, ?> proposal) {
        String proposalId = identifier(proposal.get("id"));
        Map<?, ?> proposals = object(sessionState.get("proposals"));
        Map<?, ?> retained = object(proposals.get(proposalId));
        if (!"pending".equals(retained.get("status"))) return Decision.STALE;

        if (proposal.containsKey("based_on_world_revision")) {
            Long worldRevision = positiveSafeInteger(
                    proposal.get("based_on_world_revision"));
            Long currentWorldRevision = positiveSafeInteger(
                    sessionState.get("world_revision"));
            return worldRevision != null && worldRevision.equals(currentWorldRevision)
                    ? Decision.FRESH
                    : Decision.STALE;
        }
        Long currentRevision = positiveSafeInteger(sessionState.get("revision"));
        Long createdRevision = positiveSafeInteger(proposal.get("created_revision"));
        return createdRevision != null && createdRevision.equals(currentRevision)
                ? Decision.FRESH
                : Decision.STALE;
    }

    private static String identifier(Object value) {
        if (!RinClient.isProtocolIdentifier(value)) {
            throw new RinProtocolException(
                    "invalid_proposal",
                    "Proposal freshness requires a valid Proposal ID");
        }
        return (String) value;
    }

    private static Map<?, ?> object(Object value) {
        if (!(value instanceof Map<?, ?> result)) {
            throw new RinProtocolException(
                    "invalid_state",
                    "Session state is missing retained Proposals");
        }
        return result;
    }

    private static Long positiveSafeInteger(Object value) {
        if (!(value instanceof Number number)) return null;
        double floating = number.doubleValue();
        long integral = number.longValue();
        return Double.isFinite(floating) && floating == integral &&
                integral > 0 && integral <= MAX_JSON_SAFE_INTEGER
                ? Long.valueOf(integral)
                : null;
    }
}
