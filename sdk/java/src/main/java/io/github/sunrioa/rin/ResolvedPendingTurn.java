package io.github.sunrioa.rin;

import java.util.Map;

public record ResolvedPendingTurn(
        PendingTurn pendingTurn,
        Map<String, Object> proposal,
        boolean duplicate) {

    public ResolvedPendingTurn {
        if (pendingTurn == null || proposal == null) {
            throw new RinConfigurationException(
                    "invalid_workflow",
                    "Resolved Pending Turn is missing required data");
        }
        proposal = PendingTurn.copyObject(proposal);
    }
}

