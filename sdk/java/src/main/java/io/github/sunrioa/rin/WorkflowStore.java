package io.github.sunrioa.rin;

import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletionStage;
import java.util.function.Function;

public interface WorkflowStore {
    CompletionStage<PendingTurn> loadPendingTurn();

    /** Atomically creates the Pending Turn; returns false when one exists. */
    CompletionStage<Boolean> createPendingTurn(PendingTurn pendingTurn);

    /** Updates only the matching Pending Turn with its Job identity. */
    CompletionStage<Void> savePendingTurn(PendingTurn pendingTurn);

    /**
     * In one game transaction: runs apply, records the applied operation,
     * enqueues the exact action report, and removes the matching Pending Turn.
     */
    CompletionStage<Void> settleTransactional(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> report,
            Function<String, CompletionStage<Void>> apply);

    /**
     * After advisory or operation-keyed idempotent apply, atomically records
     * the marker and action report and removes the matching Pending Turn.
     */
    CompletionStage<Void> completePendingTurn(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> report);

    CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports();

    /** Durably removes only the exact entry acknowledged by Rin. */
    CompletionStage<Void> acknowledgeOutcome(
            OutcomeOutboxEntry entry,
            Map<String, Object> result);
}
