package io.github.sunrioa.rin;

import java.util.Map;

/** Host-independent freshness check immediately before applying a Proposal. */
public final class ProposalFreshness {
    public enum Decision { FRESH, STALE }

    private ProposalFreshness() { }

    public static Decision evaluate(
            Map<String, ?> sessionState,
            Map<String, ?> proposal) {
        String proposalId = identifier(proposal.get("id"));
        Map<?, ?> proposals = object(sessionState.get("proposals"));
        Map<?, ?> retained = object(proposals.get(proposalId));
        if (!"pending".equals(retained.get("status"))) return Decision.STALE;

        long worldRevision = integer(proposal.get("based_on_world_revision"), 0);
        if (worldRevision > 0) {
            return integer(sessionState.get("world_revision"), -1) == worldRevision
                    ? Decision.FRESH
                    : Decision.STALE;
        }
        return integer(sessionState.get("revision"), -1)
                        == integer(proposal.get("created_revision"), -2)
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

    private static long integer(Object value, long fallback) {
        if (!(value instanceof Number number)) return fallback;
        double floating = number.doubleValue();
        long integral = number.longValue();
        return Double.isFinite(floating) && floating == integral
                ? integral
                : fallback;
    }
}
