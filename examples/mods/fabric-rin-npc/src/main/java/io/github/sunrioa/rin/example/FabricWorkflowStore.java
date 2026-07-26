package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.RinConfigurationException;
import io.github.sunrioa.rin.WorkflowStore;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.function.Function;

final class FabricWorkflowStore implements WorkflowStore {
    private final FabricHostRuntime host;
    private final RinFabricState state;
    private final FabricSessionState session;

    FabricWorkflowStore(
            FabricHostRuntime host,
            RinFabricState state,
            FabricSessionState session) {
        this.host = host;
        this.state = state;
        this.session = session;
    }

    public CompletionStage<PendingTurn> loadPendingTurn() {
        return host.call(() -> session.pendingTurn);
    }

    public CompletionStage<Boolean> createPendingTurn(PendingTurn pendingTurn) {
        return host.call(() -> {
            if (session.pendingTurn != null) return false;
            session.pendingTurn = pendingTurn;
            state.markDirty();
            return true;
        });
    }

    public CompletionStage<Void> savePendingTurn(PendingTurn pendingTurn) {
        return host.call(() -> {
            requireMatching(pendingTurn);
            session.pendingTurn = pendingTurn;
            state.markDirty();
            return null;
        });
    }

    public CompletionStage<Void> settleTransactional(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> report,
            Function<String, CompletionStage<Void>> apply) {
        return CompletableFuture.failedFuture(new RinConfigurationException(
                "host_durability_insufficient",
                "Fabric Saved Data does not provide an atomic game-effect transaction"));
    }

    public CompletionStage<Void> completePendingTurn(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> report) {
        return host.call(() -> {
            requireMatching(pendingTurn);
            if (session.outcomes.size() >= RinFabricState.MAX_OUTCOMES_PER_SESSION) {
                throw new IllegalStateException("Rin outcome outbox is full");
            }
            String operationId = pendingTurn.operationId();
            OutcomeOutboxEntry entry = new OutcomeOutboxEntry(
                    operationId,
                    report);
            OutcomeOutboxEntry existing =
                    session.outcomes.putIfAbsent(operationId, entry);
            if (existing != null && !existing.equals(entry)) {
                throw new IllegalStateException(
                        "Outcome identity is already bound to another report");
            }
            session.pendingTurn = null;
            session.pendingObserve = Map.of();
            state.markDirty();
            return null;
        });
    }

    public CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports() {
        return host.call(() -> List.copyOf(session.outcomes.values()));
    }

    public CompletionStage<Void> acknowledgeOutcome(
            OutcomeOutboxEntry entry,
            Map<String, Object> result) {
        return host.call(() -> {
            if (!session.outcomes.remove(entry.key(), entry)) {
                throw new IllegalStateException("Outcome changed before acknowledgement");
            }
            state.markDirty();
            return null;
        });
    }

    private void requireMatching(PendingTurn pendingTurn) {
        if (!pendingTurn.equals(session.pendingTurn)) {
            throw new IllegalStateException("Pending Turn changed before persistence");
        }
    }

}
