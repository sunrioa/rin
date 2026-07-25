package io.github.sunrioa.rin;

import java.util.Map;

public record OutcomeOutboxEntry(String key, Map<String, Object> commit) {
    public OutcomeOutboxEntry {
        if (key == null || key.isBlank() || commit == null) {
            throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox entry is missing its key or Commit");
        }
        commit = PendingTurn.copyObject(commit);
    }
}

