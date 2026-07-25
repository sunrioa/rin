package io.github.sunrioa.rin;

import java.util.Map;

public record OutcomeOutboxEntry(
        String key,
        Map<String, Object> commit,
        Map<String, Object> fallbackObserve) {
    public OutcomeOutboxEntry {
        if (key == null || key.isBlank() || commit == null) {
            throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox entry is missing its key or Commit");
        }
        commit = PendingTurn.copyObject(commit);
        fallbackObserve = fallbackObserve == null
                ? Map.of()
                : PendingTurn.copyObject(fallbackObserve);
        if (commit.isEmpty() && fallbackObserve.isEmpty()) {
            throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox entry has no report request");
        }
    }

    public OutcomeOutboxEntry(String key, Map<String, Object> commit) {
        this(key, commit, Map.of());
    }

    public boolean isDegradedObserve() {
        return commit.isEmpty();
    }

    public Map<String, Object> request() {
        return isDegradedObserve() ? fallbackObserve : commit;
    }

    public OutcomeOutboxEntry asDegradedObserve() {
        if (fallbackObserve.isEmpty()) {
            throw new RinConfigurationException(
                    "outcome_fallback_missing",
                    "Outcome has no pre-recorded safe Observe fallback");
        }
        return new OutcomeOutboxEntry(key, Map.of(), fallbackObserve);
    }
}
