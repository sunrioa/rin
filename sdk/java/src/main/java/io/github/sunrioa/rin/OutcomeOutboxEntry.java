package io.github.sunrioa.rin;

import java.util.Map;

public record OutcomeOutboxEntry(
        String key,
        Map<String, Object> report) {
    public OutcomeOutboxEntry {
        if (key == null || key.isBlank() || report == null || report.isEmpty()) {
            throw new RinConfigurationException(
                    "invalid_outbox",
                    "Outcome Outbox entry is missing its key or report");
        }
        report = PendingTurn.copyObject(report);
    }
}
