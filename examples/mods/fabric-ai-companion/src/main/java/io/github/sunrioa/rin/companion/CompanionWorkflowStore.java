package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.RinConfigurationException;
import io.github.sunrioa.rin.WorkflowStore;

import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.function.Function;

final class CompanionWorkflowStore implements WorkflowStore {
    private final CompanionSavedState state;
    private final CompanionSessionState session;

    CompanionWorkflowStore(CompanionSavedState state, CompanionSessionState session) {
        this.state = state;
        this.session = session;
    }

    @Override
    public CompletionStage<PendingTurn> loadPendingTurn() {
        return CompletableFuture.completedFuture(session.pendingTurn);
    }

    @Override
    public CompletionStage<Boolean> createPendingTurn(PendingTurn pendingTurn) {
        if (session.pendingTurn != null) return CompletableFuture.completedFuture(false);
        session.pendingTurn = pendingTurn;
        state.setDirty();
        return CompletableFuture.completedFuture(true);
    }

    @Override
    public CompletionStage<Void> savePendingTurn(PendingTurn pendingTurn) {
        requireMatching(pendingTurn.operationId());
        session.pendingTurn = pendingTurn;
        state.setDirty();
        return CompletableFuture.completedFuture(null);
    }

    @Override
    public CompletionStage<Void> settleTransactional(PendingTurn pendingTurn, Map<String, Object> proposal,
                                                      Map<String, Object> report,
                                                      Function<String, CompletionStage<Void>> apply) {
        return CompletableFuture.failedFuture(new RinConfigurationException(
                "host_durability_insufficient", "Minecraft Saved Data is advisory durability"));
    }

    @Override
    public CompletionStage<Void> completePendingTurn(PendingTurn pendingTurn, Map<String, Object> proposal,
                                                      Map<String, Object> report) {
        requireMatching(pendingTurn.operationId());
        if (session.outcomes.size() >= CompanionSessionState.MAX_OUTCOMES) {
            return CompletableFuture.failedFuture(new IllegalStateException("companion outcome outbox is full"));
        }
        OutcomeOutboxEntry entry = new OutcomeOutboxEntry(pendingTurn.operationId(), report);
        OutcomeOutboxEntry previous = session.outcomes.putIfAbsent(entry.key(), entry);
        if (previous != null && !previous.equals(entry)) {
            return CompletableFuture.failedFuture(new IllegalStateException("outcome identity conflict"));
        }
        session.pendingTurn = null;
        session.pendingObserve = Map.of();
        state.setDirty();
        return CompletableFuture.completedFuture(null);
    }

    @Override
    public CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports() {
        return CompletableFuture.completedFuture(List.copyOf(session.outcomes.values()));
    }

    @Override
    public CompletionStage<Void> acknowledgeOutcome(OutcomeOutboxEntry entry, Map<String, Object> result) {
        if (!session.sessionId.equals(result.get("session_id"))) {
            return CompletableFuture.failedFuture(new IllegalStateException("outcome acknowledgement session mismatch"));
        }
        if (!session.outcomes.remove(entry.key(), entry)) {
            return CompletableFuture.failedFuture(new IllegalStateException("outcome changed before acknowledgement"));
        }
        state.setDirty();
        return CompletableFuture.completedFuture(null);
    }

    private void requireMatching(String operationId) {
        if (session.pendingTurn == null || !session.pendingTurn.operationId().equals(operationId)) {
            throw new IllegalStateException("pending turn changed before persistence");
        }
    }
}
