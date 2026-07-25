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
     * enqueues the exact Commit, and removes the matching Pending Turn.
     */
    CompletionStage<Void> settleTransactional(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> commit,
            Function<String, CompletionStage<Void>> apply);

    /**
     * After advisory or operation-keyed idempotent apply, atomically records
     * the marker and Commit and removes the matching Pending Turn.
     */
    CompletionStage<Void> completePendingTurn(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> commit);

    CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports();

    /**
     * Atomically replaces an unrecoverable Commit with its pre-recorded,
     * absolute-fact Observe fallback.
     */
    default CompletionStage<OutcomeOutboxEntry> replaceOutcomeWithFallback(
            OutcomeOutboxEntry entry) {
        return java.util.concurrent.CompletableFuture.failedFuture(
                new RinConfigurationException(
                        "outcome_fallback_unsupported",
                        "Workflow Store cannot persist an Outcome fallback conversion"));
    }

    /** Durably removes only the exact entry acknowledged by Rin. */
    CompletionStage<Void> acknowledgeOutcome(
            OutcomeOutboxEntry entry,
            Map<String, Object> result);
}
