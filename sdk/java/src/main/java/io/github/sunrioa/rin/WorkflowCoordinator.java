package io.github.sunrioa.rin;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Function;

public final class WorkflowCoordinator {
    private final RinClient client;
    private final WorkflowStore store;
    private final HostDurability durability;
    private final AtomicBoolean draining = new AtomicBoolean();
    private final AtomicBoolean resuming = new AtomicBoolean();
    private final AtomicBoolean settling = new AtomicBoolean();

    public WorkflowCoordinator(
            RinClient client,
            WorkflowStore store,
            HostDurability durability) {
        this.client = Objects.requireNonNull(client, "client");
        this.store = Objects.requireNonNull(store, "store");
        this.durability = Objects.requireNonNull(durability, "durability");
    }

    public WorkflowCoordinator(RinClient client, WorkflowStore store) {
        this(client, store, HostDurability.advisory());
    }

    public HostDurability durability() {
        return durability;
    }

    public CompletableFuture<PendingTurn> begin(
            String operationId,
            Map<String, ?> request) {
        PendingTurn pendingTurn = PendingTurn.create(operationId, request);
        return store.createPendingTurn(pendingTurn).thenApply(created -> {
            if (!Boolean.TRUE.equals(created)) {
                throw new RinConfigurationException(
                        "pending_turn_exists",
                        "A Pending Turn is already retained");
            }
            return pendingTurn;
        }).toCompletableFuture();
    }

    public CompletableFuture<ResolvedPendingTurn> resumePendingWork() {
        return resumePendingWork(Duration.ofSeconds(25), Duration.ofMillis(100));
    }

    public CompletableFuture<ResolvedPendingTurn> resumePendingWork(
            Duration deadline,
            Duration interval) {
        if (!resuming.compareAndSet(false, true)) {
            return CompletableFuture.failedFuture(
                    new RinConfigurationException(
                            "workflow_busy",
                            "Pending work is already being resumed"));
        }
        CompletionStage<ResolvedPendingTurn> result;
        try {
            result = drainOutbox()
                    .thenCompose(ignored -> store.loadPendingTurn())
                    .thenCompose(pendingTurn -> {
                        if (pendingTurn == null) {
                            return CompletableFuture.failedFuture(
                                    new RinConfigurationException(
                                            "pending_turn_missing",
                                            "No Pending Turn is available to resume"));
                        }
                        return resolve(pendingTurn, deadline, interval);
                    });
        } catch (Throwable failure) {
            resuming.set(false);
            return CompletableFuture.failedFuture(failure);
        }
        return result.whenComplete((ignored, failure) -> resuming.set(false))
                .toCompletableFuture();
    }

    public CompletableFuture<Void> applyAndEnqueueOutcome(
            PendingTurn pendingTurn,
            Map<String, ?> proposal,
            Map<String, ?> report,
            HostDurabilityProfile requiredDurability,
            Function<String, CompletionStage<Void>> apply) {
        Objects.requireNonNull(pendingTurn, "pendingTurn");
        Objects.requireNonNull(apply, "apply");
        if (!settling.compareAndSet(false, true)) {
            return CompletableFuture.failedFuture(
                    new RinConfigurationException(
                            "workflow_busy",
                            "A Pending Turn is already being settled"));
        }
        CompletionStage<Void> completion;
        try {
            durability.require(requiredDurability);
            Map<String, Object> stableProposal = PendingTurn.copyObject(proposal);
            Map<String, Object> stableReport = PendingTurn.copyObject(report);
            validateSettlement(pendingTurn, stableProposal, stableReport);

            if (durability.profile() == HostDurabilityProfile.TRANSACTIONAL_ACTION) {
                completion = store.settleTransactional(
                        pendingTurn,
                        stableProposal,
                        stableReport,
                        apply);
            } else {
                CompletionStage<Void> applied;
                applied = Objects.requireNonNull(
                        apply.apply(pendingTurn.operationId()),
                        "apply returned null");
                completion = applied.thenCompose(ignored -> store.completePendingTurn(
                        pendingTurn,
                        stableProposal,
                        stableReport));
            }
        } catch (Throwable failure) {
            settling.set(false);
            return CompletableFuture.failedFuture(failure);
        }
        return completion.whenComplete((ignored, failure) -> settling.set(false))
                .toCompletableFuture();
    }

    public CompletableFuture<Integer> drainOutbox() {
        if (!draining.compareAndSet(false, true)) {
            return CompletableFuture.failedFuture(
                    new RinConfigurationException(
                            "outbox_busy",
                            "Outcome Outbox is already being drained"));
        }
        CompletionStage<Integer> result;
        try {
            result = store.listOutcomeReports().thenCompose(entries -> {
                if (entries == null) {
                    return CompletableFuture.failedFuture(
                            new RinConfigurationException(
                                    "invalid_outbox",
                                    "Outcome Outbox returned null"));
                }
                return drainEntries(List.copyOf(entries), 0);
            });
        } catch (Throwable failure) {
            draining.set(false);
            return CompletableFuture.failedFuture(failure);
        }
        return result.whenComplete((ignored, failure) -> draining.set(false))
                .toCompletableFuture();
    }

    private CompletionStage<Integer> drainEntries(
            List<OutcomeOutboxEntry> entries,
            int index) {
        if (index >= entries.size()) return CompletableFuture.completedFuture(index);
        OutcomeOutboxEntry entry = Objects.requireNonNull(entries.get(index), "Outbox entry");
        Map<String, Object> request = entry.report();
        requireIdentifier("request_id", request.get("request_id"));
        requireIdentifier("event_id", actionReport(request).get("event_id"));
        return client.reportAction(request)
                .thenCompose(result -> {
                    if (result == null ||
                            !Objects.equals(
                                    result.get("session_id"),
                                    request.get("session_id"))) {
                        return CompletableFuture.failedFuture(
                                new RinConfigurationException(
                                        "invalid_outbox_ack",
                                        "Rin acknowledged the Outcome for another or missing Session"));
                    }
                    return store.acknowledgeOutcome(entry, result);
                })
                .thenCompose(ignored -> drainEntries(entries, index + 1));
    }

    private CompletionStage<ResolvedPendingTurn> resolve(
            PendingTurn pendingTurn,
            Duration deadline,
            Duration interval) {
        CompletionStage<Map<String, Object>> knownJob = pendingTurn.jobId().isEmpty()
                ? CompletableFuture.completedFuture(null)
                : recoverKnownJob(pendingTurn, deadline, interval);
        return knownJob.thenCompose(job -> {
            if (job != null) return CompletableFuture.completedFuture(
                    resolved(pendingTurn, job));
            return client.submitProposalJob(pendingTurn.request())
                    .thenCompose(submission -> {
                        String jobId = requiredIdentifier("job_id", submission.get("job_id"));
                        PendingTurn updated = pendingTurn.withJobId(jobId);
                        return store.savePendingTurn(updated)
                                .thenCompose(ignored -> client.waitForProposal(
                                        jobId,
                                        deadline,
                                        interval))
                                .thenApply(result -> resolved(updated, result));
                    });
        });
    }

    private CompletionStage<Map<String, Object>> recoverKnownJob(
            PendingTurn pendingTurn,
            Duration deadline,
            Duration interval) {
        return client.waitForProposal(pendingTurn.jobId(), deadline, interval)
                .handle((job, failure) -> {
                    if (failure == null) return CompletableFuture.completedFuture(job);
                    Throwable cause = unwrap(failure);
                    if (cause instanceof RinApiException api &&
                            api.code().equals("job_not_found")) {
                        return CompletableFuture.<Map<String, Object>>completedFuture(null);
                    }
                    return CompletableFuture.<Map<String, Object>>failedFuture(cause);
                }).thenCompose(Function.identity());
    }

    private static ResolvedPendingTurn resolved(
            PendingTurn pendingTurn,
            Map<String, Object> job) {
        Object value = job.get("proposal");
        if (!(value instanceof Map<?, ?> rawProposal)) {
            throw new RinProtocolException(
                    "invalid_job",
                    "Resolved Proposal does not match the Pending Turn");
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> proposal =
                PendingTurn.copyObject((Map<String, ?>) rawProposal);
        requireResolvedProposalMatches(pendingTurn, proposal);
        return new ResolvedPendingTurn(
                pendingTurn,
                proposal,
                Boolean.TRUE.equals(job.get("duplicate")));
    }

    static void requireResolvedProposalMatches(
            PendingTurn pendingTurn,
            Map<String, Object> proposal) {
        Map<String, Object> request = pendingTurn.request();
        Object action = proposal.get("action");
        Object offered = request.get("offers");
        boolean selectedAuthoredOffer = offered instanceof List<?> offers &&
                offers.stream().anyMatch(offer -> JsonValues.equivalent(offer, action));
        if (!RinClient.isProtocolIdentifier(proposal.get("id")) ||
                !JsonValues.equivalent(
                        proposal.get("session_id"), request.get("session_id")) ||
                !JsonValues.equivalent(
                        proposal.get("request_id"), request.get("request_id")) ||
                !JsonValues.equivalent(proposal.get("actor_id"), request.get("actor_id")) ||
                !JsonValues.equivalent(proposal.get("tick"), request.get("tick")) ||
                !JsonValues.equivalent(
                        proposal.get("decision_window"), request.get("decision_window")) ||
                !selectedAuthoredOffer) {
            throw new RinProtocolException(
                    "invalid_job",
                    "Resolved Proposal does not match the Pending Turn");
        }
    }

    private static void validateSettlement(
            PendingTurn pendingTurn,
            Map<String, Object> proposal,
            Map<String, Object> report) {
        requireResolvedProposalMatches(pendingTurn, proposal);
        Map<String, Object> actionReport = actionReport(report);
        if (!Objects.equals(proposal.get("session_id"), pendingTurn.request().get("session_id")) ||
                !Objects.equals(proposal.get("request_id"), pendingTurn.request().get("request_id")) ||
                !Objects.equals(report.get("session_id"), pendingTurn.request().get("session_id")) ||
                !Objects.equals(actionReport.get("proposal_id"), proposal.get("id"))) {
            throw new RinConfigurationException(
                    "workflow_identity_mismatch",
                    "Pending Turn, Proposal, and report identities do not match");
        }
        requireIdentifier("request_id", report.get("request_id"));
        requireIdentifier("event_id", actionReport.get("event_id"));
    }

    private static Map<String, Object> actionReport(Map<String, Object> request) {
        Object value = request.get("report");
        if (!(value instanceof Map<?, ?> raw)) {
            throw new RinConfigurationException(
                    "invalid_workflow",
                    "report must contain a typed action report");
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> report = (Map<String, Object>) raw;
        return report;
    }

    private static String requiredIdentifier(String field, Object value) {
        requireIdentifier(field, value);
        return (String) value;
    }

    private static void requireIdentifier(String field, Object value) {
        if (!RinClient.isProtocolIdentifier(value)) {
            throw new RinConfigurationException(
                    "invalid_workflow",
                    field + " must be a protocol identifier");
        }
    }

    private static Throwable unwrap(Throwable failure) {
        Throwable result = failure;
        while ((result instanceof CompletionException ||
                result instanceof java.util.concurrent.ExecutionException) &&
                result.getCause() != null) {
            result = result.getCause();
        }
        return result;
    }
}
