package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.RinConfigurationException;
import io.github.sunrioa.rin.WorkflowStore;
import net.minecraft.server.MinecraftServer;

import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.function.Function;

final class FabricWorkflowStore implements WorkflowStore {
    private final MinecraftServer server;
    private final RinFabricState state;
    private final RinFabricState.SessionState session;

    FabricWorkflowStore(
            MinecraftServer server,
            RinFabricState state,
            RinFabricState.SessionState session) {
        this.server = server;
        this.state = state;
        this.session = session;
    }

    public CompletionStage<PendingTurn> loadPendingTurn() {
        return FabricServerTasks.call(server, () -> session.pendingTurn);
    }

    public CompletionStage<Boolean> createPendingTurn(PendingTurn pendingTurn) {
        return FabricServerTasks.call(server, () -> {
            if (session.pendingTurn != null) return false;
            session.pendingTurn = pendingTurn;
            state.markDirty();
            return true;
        });
    }

    public CompletionStage<Void> savePendingTurn(PendingTurn pendingTurn) {
        return FabricServerTasks.call(server, () -> {
            requireMatching(pendingTurn);
            session.pendingTurn = pendingTurn;
            state.markDirty();
            return null;
        });
    }

    public CompletionStage<Void> settleTransactional(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> commit,
            Function<String, CompletionStage<Void>> apply) {
        return CompletableFuture.failedFuture(new RinConfigurationException(
                "host_durability_insufficient",
                "Fabric Saved Data does not provide an atomic game-effect transaction"));
    }

    public CompletionStage<Void> completePendingTurn(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> commit) {
        return FabricServerTasks.call(server, () -> {
            requireMatching(pendingTurn);
            if (session.outcomes.size() >= RinFabricState.MAX_OUTCOMES_PER_SESSION) {
                throw new IllegalStateException("Rin outcome outbox is full");
            }
            String operationId = pendingTurn.operationId();
            OutcomeOutboxEntry entry = new OutcomeOutboxEntry(
                    operationId,
                    commit,
                    RinNpcRequests.safeObserve(commit, operationId));
            session.outcomes.putIfAbsent(operationId, entry);
            session.pendingTurn = null;
            session.pendingObserve = Map.of();
            state.markDirty();
            return null;
        });
    }

    public CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports() {
        return FabricServerTasks.call(server, () -> List.copyOf(session.outcomes.values()));
    }

    public CompletionStage<OutcomeOutboxEntry> replaceOutcomeWithFallback(
            OutcomeOutboxEntry entry) {
        return FabricServerTasks.call(server, () -> {
            OutcomeOutboxEntry retained = session.outcomes.get(entry.key());
            if (!entry.equals(retained)) {
                throw new IllegalStateException("Outcome changed before fallback conversion");
            }
            OutcomeOutboxEntry converted = entry.asDegradedObserve();
            session.outcomes.put(entry.key(), converted);
            state.markDirty();
            return converted;
        });
    }

    public CompletionStage<Void> acknowledgeOutcome(
            OutcomeOutboxEntry entry,
            Map<String, Object> result) {
        return FabricServerTasks.call(server, () -> {
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
