package hostkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/protocol"
)

const (
	scenarioStaleEpochRejection   = "stale_epoch_rejection"
	scenarioIdempotentOperation   = "idempotent_operation"
	scenarioRevokedCapability     = "revoked_capability_rejection"
	scenarioExactOutboxRetry      = "exact_outbox_retry"
	scenarioLongActionEpochCancel = "long_action_epoch_cancel"
	scenarioRecoveryStateCleanup  = "recovery_state_cleanup"
)

func TestCoordinatorPersistsBeforeNetworkAndRetriesExactOutbox(t *testing.T) {
	t.Log(scenarioExactOutboxRetry)
	fixture := newFixture(t, host.ActionSucceeded)
	pending, err := fixture.coordinator.BeginDecision(
		context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if pending.OperationID == "" {
		t.Fatal("BeginDecision did not create a stable operation ID")
	}
	fixture.transport.beforeSubmit = func() error {
		state, loadErr := fixture.store.Load(context.Background())
		if loadErr != nil {
			return loadErr
		}
		if state.Pending == nil || state.Pending.OperationID != pending.OperationID {
			return errors.New("Pending Decision was not durable before network")
		}
		return nil
	}
	resolved, err := fixture.coordinator.ResumePendingWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Ready || resolved.Proposal == nil {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}
	record, err := fixture.coordinator.DispatchAndEnqueue(
		context.Background(),
		DispatchRequest{
			Proposal:           *resolved.Proposal,
			InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
			Summary:            "dialogue applied",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.dispatcher.used ||
		record.Run.Status != host.ActionSucceeded ||
		string(record.Output) != `{}` {
		t.Fatalf("action did not execute through authority dispatcher: %+v", record)
	}
	state, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || len(state.Actions) != 1 || len(state.Outbox) != 1 {
		t.Fatalf("unexpected settled state: %+v", state)
	}

	fixture.transport.reportError = errors.New("sidecar unavailable")
	if _, err := fixture.coordinator.DrainOutbox(context.Background()); err == nil {
		t.Fatal("DrainOutbox accepted a failed report")
	}
	retained, _ := fixture.store.Load(context.Background())
	if len(retained.Outbox) != 1 {
		t.Fatal("failed report was removed from the Outbox")
	}
	firstAttempt := fixture.transport.reports[0]
	fixture.transport.reportError = nil
	restarted, err := NewCoordinator(
		fixture.transport,
		fixture.dispatcher,
		fixture.store,
		fixture.identity,
		fixture.registry,
		fixture.executor,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := restarted.DrainOutbox(context.Background()); err != nil ||
		count != 1 {
		t.Fatalf("DrainOutbox = %d, %v", count, err)
	}
	if !reflect.DeepEqual(firstAttempt, fixture.transport.reports[1]) {
		t.Fatal("Outbox retry changed the exact Action Report")
	}
	drained, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(drained.Actions) != 0 || len(drained.Outbox) != 0 {
		t.Fatalf("acknowledged terminal action was not compacted: %+v", drained)
	}
}

func TestCoordinatorRecoversSubmitBeforeJobIDSave(t *testing.T) {
	t.Log(scenarioIdempotentOperation)
	fixture := newFixture(t, host.ActionSucceeded)
	if _, err := fixture.coordinator.BeginDecision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatal(err)
	}
	fixture.store.failNextCAS = true
	if _, err := fixture.coordinator.ResumePendingWork(
		context.Background(),
	); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("error = %v, want simulated save conflict", err)
	}
	state, _ := fixture.store.Load(context.Background())
	if state.Pending == nil || state.Pending.JobID != "" {
		t.Fatal("failed Job ID save changed retained Pending Decision")
	}
	fixture.transport.submission.Status = "succeeded"
	fixture.transport.submission.Duplicate = true
	result, err := fixture.coordinator.ResumePendingWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || fixture.transport.submissions != 2 ||
		result.Pending.JobID != "job.test" {
		t.Fatalf("submit recovery failed: %+v", result)
	}
}

func TestCoordinatorSupportsLongRunTransition(t *testing.T) {
	fixture := newFixture(t, host.ActionRunning)
	beginAndResolve(t, fixture)
	record, err := fixture.coordinator.DispatchAndEnqueue(
		context.Background(),
		DispatchRequest{
			Proposal:           fixture.proposal,
			InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
			Summary:            "movement started",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := host.Timepoint{Clock: host.ClockEvent, Value: 12}
	outcome := host.ActionOutcome{
		OperationID: record.Invocation.OperationID,
		Status:      host.ActionSucceeded, Summary: "target reached",
		Epoch: fixture.identity.value.Epoch, WorldSeq: 2, OccurredAt: finishedAt,
	}
	updated, err := fixture.coordinator.RecordTransitionAndEnqueue(
		context.Background(),
		TransitionRequest{
			OperationID: record.Invocation.OperationID,
			Run: host.ActionRun{
				OperationID: record.Invocation.OperationID,
				Status:      host.ActionSucceeded, ProgressSeq: 2, Progress: 100,
				UpdatedAt: finishedAt,
			},
			Outcome: &outcome, Output: json.RawMessage(`{}`),
			Summary: "target reached",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Outcome == nil || updated.Run.Status != host.ActionSucceeded {
		t.Fatalf("unexpected terminal transition: %+v", updated)
	}
	state, _ := fixture.store.Load(context.Background())
	if len(state.Outbox) != 2 {
		t.Fatalf("Outbox length = %d, want start and terminal reports", len(state.Outbox))
	}
	if _, err := fixture.coordinator.RecordTransitionAndEnqueue(
		context.Background(),
		TransitionRequest{
			OperationID: record.Invocation.OperationID,
			Run:         record.Run,
			Summary:     "illegal regression",
		},
	); err == nil {
		t.Fatal("terminal action regressed to running")
	}
}

func TestCoordinatorRevocationAndEpochReconciliationFailClosed(t *testing.T) {
	t.Log(scenarioStaleEpochRejection, scenarioRevokedCapability,
		scenarioLongActionEpochCancel, scenarioRecoveryStateCleanup)
	t.Run("revoked capability", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		beginAndResolve(t, fixture)
		fixture.registry.Unregister(fixture.descriptor.Capability)
		if _, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			DispatchRequest{
				Proposal:           fixture.proposal,
				InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
				Summary:            "must not execute",
			},
		); err == nil {
			t.Fatal("revoked capability executed")
		}
		if fixture.executor.executions != 0 {
			t.Fatal("executor ran after dynamic capability revocation")
		}
	})

	t.Run("stale pending decision", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		if _, err := fixture.coordinator.BeginDecision(
			context.Background(), fixture.request,
		); err != nil {
			t.Fatal(err)
		}
		fixture.advanceEpoch()
		if _, err := fixture.coordinator.ResumePendingWork(
			context.Background(),
		); !errors.Is(err, ErrStaleEpoch) {
			t.Fatalf("error = %v, want ErrStaleEpoch", err)
		}
		state, _ := fixture.store.Load(context.Background())
		if state.Pending != nil || fixture.transport.submissions != 0 {
			t.Fatal("stale Pending Decision reached the network or remained retained")
		}
	})

	t.Run("running action", func(t *testing.T) {
		fixture := newFixture(t, host.ActionRunning)
		beginAndResolve(t, fixture)
		record, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			DispatchRequest{
				Proposal:           fixture.proposal,
				InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
				Summary:            "movement started",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		fixture.advanceEpoch()
		cancelled, err := fixture.coordinator.ReconcileEpoch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if cancelled != 1 || fixture.executor.cancellations != 1 {
			t.Fatalf("cancelled=%d executor=%d", cancelled, fixture.executor.cancellations)
		}
		state, _ := fixture.store.Load(context.Background())
		if state.Actions[0].Run.Status != host.ActionStale ||
			state.Actions[0].Outcome == nil ||
			state.Actions[0].Invocation.OperationID != record.Invocation.OperationID {
			t.Fatalf("stale action was not terminalized: %+v", state.Actions[0])
		}
	})

	t.Run("removed active capability", func(t *testing.T) {
		fixture := newFixture(t, host.ActionRunning)
		beginAndResolve(t, fixture)
		if _, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			DispatchRequest{
				Proposal:           fixture.proposal,
				InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
				Summary:            "movement started",
			},
		); err != nil {
			t.Fatal(err)
		}
		fixture.registry.Unregister(fixture.descriptor.Capability)
		fixture.advanceEpoch()
		if cancelled, err := fixture.coordinator.ReconcileEpoch(
			context.Background(),
		); err != nil || cancelled != 1 {
			t.Fatalf("ReconcileEpoch = %d, %v", cancelled, err)
		}
		state, _ := fixture.store.Load(context.Background())
		if fixture.executor.cancellations != 0 ||
			state.Actions[0].Run.Status != host.ActionOutcomeUnknown {
			t.Fatalf("removed capability cancellation was invented: %+v", state.Actions[0])
		}
		if cancelled, err := fixture.coordinator.ReconcileEpoch(
			context.Background(),
		); err != nil || cancelled != 0 {
			t.Fatalf("repeated ReconcileEpoch = %d, %v", cancelled, err)
		}
		occurredAt := host.Timepoint{Clock: host.ClockEvent, Value: 12}
		outcome := host.ActionOutcome{
			OperationID: state.Actions[0].Invocation.OperationID,
			Status:      host.ActionSucceeded, Summary: "effect was found persisted",
			Epoch:    state.Actions[0].Invocation.ExpectedEpoch,
			WorldSeq: 3, OccurredAt: occurredAt,
		}
		if _, err := fixture.registry.Register(fixture.descriptor); err != nil {
			t.Fatalf("restore exact descriptor for late result validation: %v", err)
		}
		if _, err := fixture.coordinator.RecordTransitionAndEnqueue(
			context.Background(),
			TransitionRequest{
				OperationID: outcome.OperationID,
				Run: host.ActionRun{
					OperationID: outcome.OperationID, Status: host.ActionSucceeded,
					ProgressSeq: 3, Progress: 100, UpdatedAt: occurredAt,
				},
				Outcome: &outcome, Output: json.RawMessage(`{}`),
				Summary: outcome.Summary,
			},
		); err != nil {
			t.Fatalf("resolve outcome-unknown: %v", err)
		}
	})
}

func TestWorkflowStateRejectsDuplicateOperationsAndOutboxIDs(t *testing.T) {
	fixture := newFixture(t, host.ActionSucceeded)
	beginAndResolve(t, fixture)
	record, err := fixture.coordinator.DispatchAndEnqueue(
		context.Background(),
		DispatchRequest{
			Proposal:           fixture.proposal,
			InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
			Summary:            "done",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := fixture.store.Load(context.Background())
	state.Actions = append(state.Actions, record)
	if err := state.Validate(); err == nil {
		t.Fatal("duplicate Operation ID was accepted")
	}
	state.Actions = state.Actions[:1]
	state.Outbox = append(state.Outbox, state.Outbox[0])
	if err := state.Validate(); err == nil {
		t.Fatal("duplicate Outbox ID was accepted")
	}

	state, _ = fixture.store.Load(context.Background())
	mismatchedInvocation, err := state.Clone()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedInvocation.Outbox[0].Request.Report.Invocation.OfferID =
		"offer.other"
	if err := mismatchedInvocation.Validate(); err == nil {
		t.Fatal("Outbox Invocation from another action was accepted")
	}

	staleTerminalReport, err := state.Clone()
	if err != nil {
		t.Fatal(err)
	}
	staleTerminalReport.Outbox[0].Request.Report.Run.Status =
		host.ActionRunning
	staleTerminalReport.Outbox[0].Request.Report.Run.Progress = 99
	staleTerminalReport.Outbox[0].Request.Report.Outcome = nil
	if err := staleTerminalReport.Validate(); err == nil {
		t.Fatal("terminal Action without its matching terminal report was accepted")
	}
}

func TestCoordinatorPreflightsAuthorityOutputAndCapacity(t *testing.T) {
	t.Run("missing scope", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		beginAndResolve(t, fixture)
		fixture.identity.mu.Lock()
		fixture.identity.value.Principal.GrantedScopes = nil
		fixture.identity.mu.Unlock()

		if _, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			dispatchRequest(fixture),
		); err == nil {
			t.Fatal("authority accepted a principal without the required scope")
		}
		if fixture.executor.executions != 0 {
			t.Fatal("executor ran before the final scope check")
		}
	})

	t.Run("invalid report metadata", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		beginAndResolve(t, fixture)
		request := dispatchRequest(fixture)
		request.Summary = strings.Repeat("x", 1001)
		if _, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			request,
		); err == nil {
			t.Fatal("invalid report metadata reached the executor")
		}
		if fixture.executor.executions != 0 {
			t.Fatal("executor ran before report metadata validation")
		}
	})

	t.Run("invalid output becomes outcome unknown", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		beginAndResolve(t, fixture)
		fixture.executor.output = json.RawMessage(`{"unexpected":true}`)
		record, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			dispatchRequest(fixture),
		)
		if !errors.Is(err, ErrExecutionOutcomeUnknown) {
			t.Fatalf("Dispatch error = %v, want outcome unknown", err)
		}
		if fixture.executor.executions != 1 ||
			record.Run.Status != host.ActionOutcomeUnknown ||
			record.Outcome != nil {
			t.Fatalf("invalid executor result was not fenced: %+v", record)
		}
		state, loadErr := fixture.store.Load(context.Background())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if state.Pending != nil || len(state.Actions) != 1 ||
			len(state.Outbox) != 1 ||
			state.Actions[0].Run.Status != host.ActionOutcomeUnknown {
			t.Fatalf("uncertain result was not recoverable: %+v", state)
		}
	})

	t.Run("executor error becomes outcome unknown", func(t *testing.T) {
		fixture := newFixture(t, host.ActionSucceeded)
		beginAndResolve(t, fixture)
		sentinel := errors.New("executor lost its result")
		fixture.executor.executeErr = sentinel
		record, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			dispatchRequest(fixture),
		)
		if !errors.Is(err, ErrExecutionOutcomeUnknown) ||
			!errors.Is(err, sentinel) {
			t.Fatalf("Dispatch error = %v, want retained executor uncertainty", err)
		}
		if record.Run.Status != host.ActionOutcomeUnknown ||
			record.Outcome != nil ||
			len(record.Output) != 0 {
			t.Fatalf("executor error was not retained safely: %+v", record)
		}
	})

	for _, testCase := range []struct {
		name             string
		retained         int
		wantErr          error
		wantExecutions   int
		wantActionLength int
	}{
		{
			name:             "1024th retained action",
			retained:         maxActions - 1,
			wantExecutions:   1,
			wantActionLength: maxActions,
		},
		{
			name:             "1025th retained action",
			retained:         maxActions,
			wantErr:          ErrActionCapacity,
			wantActionLength: maxActions,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFixture(t, host.ActionSucceeded)
			beginAndResolve(t, fixture)
			fillActiveActions(t, fixture, testCase.retained)
			_, err := fixture.coordinator.DispatchAndEnqueue(
				context.Background(),
				dispatchRequest(fixture),
			)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Dispatch error = %v, want %v", err, testCase.wantErr)
			}
			if fixture.executor.executions != testCase.wantExecutions {
				t.Fatalf(
					"executor calls = %d, want %d",
					fixture.executor.executions,
					testCase.wantExecutions,
				)
			}
			state, loadErr := fixture.store.Load(context.Background())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(state.Actions) != testCase.wantActionLength {
				t.Fatalf(
					"retained actions = %d, want %d",
					len(state.Actions),
					testCase.wantActionLength,
				)
			}
		})
	}
}

func TestCoordinatorPreflightsEpochCancellationOutboxCapacity(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		outboxEntries     int
		wantErr           error
		wantCancellations int
	}{
		{
			name:              "1024th Outbox entry",
			outboxEntries:     maxOutboxEntries - 1,
			wantCancellations: 1,
		},
		{
			name:          "1025th Outbox entry",
			outboxEntries: maxOutboxEntries,
			wantErr:       ErrOutboxCapacity,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newFixture(t, host.ActionRunning)
			beginAndResolve(t, fixture)
			if _, err := fixture.coordinator.DispatchAndEnqueue(
				context.Background(),
				dispatchRequest(fixture),
			); err != nil {
				t.Fatal(err)
			}
			fillOutbox(t, fixture, testCase.outboxEntries)
			fixture.advanceEpoch()

			_, err := fixture.coordinator.ReconcileEpoch(context.Background())
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ReconcileEpoch error = %v, want %v", err, testCase.wantErr)
			}
			if fixture.executor.cancellations != testCase.wantCancellations {
				t.Fatalf(
					"cancellations = %d, want %d",
					fixture.executor.cancellations,
					testCase.wantCancellations,
				)
			}
		})
	}
}

func TestCoordinatorCancellationRechecksPrincipalScope(t *testing.T) {
	fixture := newFixture(t, host.ActionRunning)
	beginAndResolve(t, fixture)
	if _, err := fixture.coordinator.DispatchAndEnqueue(
		context.Background(),
		dispatchRequest(fixture),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.coordinator.DrainOutbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.identity.mu.Lock()
	fixture.identity.value.Principal.GrantedScopes = nil
	fixture.identity.mu.Unlock()
	fixture.advanceEpoch()

	reconciled, err := fixture.coordinator.ReconcileEpoch(context.Background())
	if reconciled != 1 || !errors.Is(err, ErrExecutionOutcomeUnknown) {
		t.Fatalf("ReconcileEpoch = %d, %v", reconciled, err)
	}
	if fixture.executor.cancellations != 0 {
		t.Fatal("unauthorized cancellation reached the executor")
	}
	state, loadErr := fixture.store.Load(context.Background())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(state.Actions) != 1 ||
		state.Actions[0].Run.Status != host.ActionOutcomeUnknown ||
		len(state.Outbox) != 1 {
		t.Fatalf("unauthorized cancellation did not remain recoverable: %+v", state)
	}
}

func TestCoordinatorCompactsTenThousandTerminalActions(t *testing.T) {
	fixture := newFixture(t, host.ActionSucceeded)
	for index := 0; index < 10_000; index++ {
		beginAndResolve(t, fixture)
		if _, err := fixture.coordinator.DispatchAndEnqueue(
			context.Background(),
			dispatchRequest(fixture),
		); err != nil {
			t.Fatalf("dispatch %d: %v", index+1, err)
		}
		if count, err := fixture.coordinator.DrainOutbox(
			context.Background(),
		); err != nil || count != 1 {
			t.Fatalf("drain %d = %d, %v", index+1, count, err)
		}
	}
	state, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.executor.executions != 10_000 ||
		len(state.Actions) != 0 ||
		len(state.Outbox) != 0 ||
		state.Pending != nil {
		t.Fatalf("long-running workflow remained unbounded: %+v", state)
	}
}

type testFixture struct {
	coordinator *Coordinator
	store       *memoryStore
	transport   *fakeTransport
	dispatcher  *fakeDispatcher
	identity    *fakeIdentity
	executor    *fakeExecutor
	registry    *host.Registry
	descriptor  host.CapabilityDescriptor
	request     protocol.ProposeRequest
	proposal    protocol.ActionProposal
}

func newFixture(t *testing.T, executionStatus host.ActionRunStatus) *testFixture {
	t.Helper()
	epoch := host.Epoch{
		SessionID: "session.test", WorldID: "world.test",
		Host: 1, World: 1, Timeline: 1,
	}
	now := host.Timepoint{Clock: host.ClockEvent, Value: 10}
	manifest := host.HostManifest{
		ContractVersion: host.ContractVersion,
		AdapterID:       "test.adapter", AdapterVersion: "1.0.0",
		EngineID: "test.engine", EngineVersion: "1",
		Runtime: "go", Platform: "test",
		Authority:           host.AuthorityStandalone,
		Deployment:          host.DeploymentEmbeddedOffline,
		Control:             host.ControlSemantic,
		ClockModes:          []host.ClockMode{host.ClockEvent},
		DecisionModes:       []host.DecisionMode{host.DecisionSequential},
		MaxConcurrentActors: 1,
		Durability:          host.Durability{Profile: host.DurabilityAdvisory},
	}
	registry, err := host.NewRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := host.NewSchema([]byte(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{},"additionalProperties":false}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	executionMode := host.ExecutionImmediate
	cancellationMode := host.CancellationUnsupported
	if executionStatus == host.ActionRunning {
		executionMode = host.ExecutionLongRunning
		cancellationMode = host.CancellationCooperative
	}
	descriptor, err := registry.Register(host.CapabilityDescriptor{
		Capability:  host.CapabilityRef{ID: "dialogue.say", Version: "1.0.0"},
		Description: "Show dialogue.", Input: schema, Output: schema,
		Effect: host.EffectAdvisory, Execution: executionMode,
		Risk: host.RiskLow, RequiredDurability: host.DurabilityAdvisory,
		RequiredScopes:  []string{"rin.dialogue.say"},
		ExecutionBudget: host.Duration{Clock: host.ClockEvent, Value: 10},
		MaxInputBytes:   1024, MaxOutputBytes: 1024,
		Cancellation: cancellationMode, Reversible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	window := protocol.DecisionWindow{
		ID: "window.test", Mode: host.DecisionSequential, Epoch: epoch,
		ObservationSeq: 1, OpenedAt: now,
		Deadline: host.Timepoint{Clock: host.ClockEvent, Value: 20},
		ActorIDs: []string{"actor.test"},
	}
	offer := host.ActionOffer{
		OfferID: "offer.test", DecisionWindowID: window.ID,
		ActorID: "actor.test", Capability: descriptor.Capability,
		DescriptorDigest: descriptor.Digest, Description: "Speak.",
		Arguments: json.RawMessage(`{}`), ExpectedEpoch: epoch,
		ObservationSeq: 1, Deadline: window.Deadline,
	}
	request := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: epoch.SessionID,
		RequestID: "proposal.request", ActorID: "actor.test", Tick: 10,
		Intent: "respond", DecisionWindow: window, Offers: []host.ActionOffer{offer},
	}
	proposal := protocol.ActionProposal{
		ID: "proposal.test", SessionID: request.SessionID,
		RequestID: request.RequestID, ActorID: request.ActorID, Tick: request.Tick,
		BasedOnRevision: 1, BasedOnHeadHash: fmt.Sprintf("%064d", 1),
		CreatedRevision: 2, DecisionWindow: window, Action: offer,
		Stance: "helpful", Summary: "Speak.", Rationale: "Player asked.",
		Status: "pending",
	}
	store := &memoryStore{state: EmptyState()}
	transport := &fakeTransport{
		submission: protocol.ProposalJobSubmission{
			ProtocolVersion: protocol.Version, JobID: "job.test", Status: "queued",
		},
		job: protocol.ProposalJob{
			ProtocolVersion: protocol.Version, JobID: "job.test",
			SessionID: request.SessionID, RequestID: request.RequestID,
			Status: "succeeded", Proposal: &proposal,
		},
	}
	dispatcher := &fakeDispatcher{}
	identity := &fakeIdentity{
		value: HostIdentity{
			SessionID: epoch.SessionID, Epoch: epoch, Now: now,
			Principal: host.Principal{
				ID:            "principal.test",
				GrantedScopes: []string{"rin.dialogue.say"},
			},
			Tick: 10, ObservationSeq: 1,
		},
	}
	executor := &fakeExecutor{
		status: executionStatus, dispatcher: dispatcher,
	}
	coordinator, err := NewCoordinator(
		transport, dispatcher, store, identity, registry, executor)
	if err != nil {
		t.Fatal(err)
	}
	return &testFixture{
		coordinator: coordinator, store: store, transport: transport,
		dispatcher: dispatcher, identity: identity, executor: executor,
		registry: registry, descriptor: descriptor, request: request,
		proposal: proposal,
	}
}

func beginAndResolve(t *testing.T, fixture *testFixture) {
	t.Helper()
	if _, err := fixture.coordinator.BeginDecision(
		context.Background(), fixture.request,
	); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.coordinator.ResumePendingWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready {
		t.Fatal("Proposal did not resolve")
	}
}

func dispatchRequest(fixture *testFixture) DispatchRequest {
	return DispatchRequest{
		Proposal:           fixture.proposal,
		InvocationDeadline: host.Timepoint{Clock: host.ClockEvent, Value: 15},
		Summary:            "dialogue applied",
	}
}

func fillActiveActions(
	t *testing.T,
	fixture *testFixture,
	count int,
) {
	t.Helper()
	state, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.Actions = make([]ActionRecord, 0, count)
	for index := 0; index < count; index++ {
		operationID := fmt.Sprintf("operation.capacity.%d", index+1)
		invocation, invocationErr := fixture.registry.NewInvocation(
			fixture.proposal.Action,
			operationID,
			fixture.identity.value.Now,
			host.Timepoint{Clock: host.ClockEvent, Value: 15},
			fixture.identity.value.Epoch,
		)
		if invocationErr != nil {
			t.Fatal(invocationErr)
		}
		state.Actions = append(state.Actions, ActionRecord{
			ProposalID:        fmt.Sprintf("proposal.capacity.%d", index+1),
			ProposalRequestID: fixture.request.RequestID,
			Invocation:        invocation,
			Principal:         fixture.identity.value.Principal,
			Run: host.ActionRun{
				OperationID: operationID,
				Status:      host.ActionRunning,
				ProgressSeq: 1,
				Progress:    10,
				UpdatedAt:   fixture.identity.value.Now,
			},
		})
	}
	fixture.store.replace(t, state)
}

func fillOutbox(
	t *testing.T,
	fixture *testFixture,
	count int,
) {
	t.Helper()
	state, err := fixture.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Outbox) != 1 {
		t.Fatalf("fixture Outbox length = %d, want 1", len(state.Outbox))
	}
	base := state.Outbox[0]
	for index := len(state.Outbox); index < count; index++ {
		request := base.Request
		report := request.Report
		invocation := *report.Invocation
		run := *report.Run
		report.Invocation = &invocation
		report.Run = &run
		request.RequestID = fmt.Sprintf("request.capacity.%d", index+1)
		report.EventID = fmt.Sprintf("event.capacity.%d", index+1)
		request.Report = report
		state.Outbox = append(state.Outbox, OutboxEntry{
			ID:      fmt.Sprintf("outbox.capacity.%d", index+1),
			Request: request,
		})
	}
	fixture.store.replace(t, state)
}

func (fixture *testFixture) advanceEpoch() {
	fixture.identity.mu.Lock()
	defer fixture.identity.mu.Unlock()
	fixture.identity.value.Epoch.World++
	fixture.identity.value.Now.Value++
	fixture.identity.value.Tick++
}

type memoryStore struct {
	mu          sync.Mutex
	state       WorkflowState
	failNextCAS bool
}

func (store *memoryStore) replace(t *testing.T, state WorkflowState) {
	t.Helper()
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.state = state
	store.mu.Unlock()
}

func (store *memoryStore) Load(context.Context) (WorkflowState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.state.Clone()
}

func (store *memoryStore) CompareAndSwap(
	_ context.Context,
	expected uint64,
	next WorkflowState,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failNextCAS {
		store.failNextCAS = false
		return ErrConcurrentUpdate
	}
	if store.state.Revision != expected {
		return ErrConcurrentUpdate
	}
	if next.Revision != expected+1 {
		return errors.New("next revision must increment exactly once")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	store.state = next
	return nil
}

func (store *memoryStore) CommitEffect(
	ctx context.Context,
	expected uint64,
	effect func(context.Context) (WorkflowState, error),
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.state.Revision != expected {
		return ErrConcurrentUpdate
	}
	next, err := effect(ctx)
	if err != nil {
		return err
	}
	if next.Revision != expected+1 {
		return errors.New("next revision must increment exactly once")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	store.state = next
	return nil
}

type fakeTransport struct {
	submission   protocol.ProposalJobSubmission
	job          protocol.ProposalJob
	beforeSubmit func() error
	reportError  error
	submissions  int
	reports      []protocol.ReportActionRequest
}

func (transport *fakeTransport) SubmitProposal(
	_ context.Context,
	_ protocol.ProposeRequest,
) (protocol.ProposalJobSubmission, error) {
	transport.submissions++
	if transport.beforeSubmit != nil {
		if err := transport.beforeSubmit(); err != nil {
			return protocol.ProposalJobSubmission{}, err
		}
	}
	return transport.submission, nil
}

func (transport *fakeTransport) PollProposal(
	context.Context,
	string,
) (protocol.ProposalJob, error) {
	return transport.job, nil
}

func (transport *fakeTransport) ReportAction(
	_ context.Context,
	request protocol.ReportActionRequest,
) (protocol.MutationResult, error) {
	transport.reports = append(transport.reports, request)
	if transport.reportError != nil {
		return protocol.MutationResult{}, transport.reportError
	}
	return protocol.MutationResult{SessionID: request.SessionID}, nil
}

type fakeDispatcher struct {
	inside bool
	used   bool
}

func (dispatcher *fakeDispatcher) Dispatch(
	ctx context.Context,
	operation func(context.Context) error,
) error {
	if dispatcher.inside {
		return errors.New("dispatcher is not reentrant")
	}
	dispatcher.used, dispatcher.inside = true, true
	defer func() { dispatcher.inside = false }()
	return operation(ctx)
}

type fakeIdentity struct {
	mu      sync.Mutex
	value   HostIdentity
	counter int
}

func (identity *fakeIdentity) Current(context.Context) (HostIdentity, error) {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	return identity.value, nil
}

func (identity *fakeIdentity) NewID(
	_ context.Context,
	kind IDKind,
) (string, error) {
	identity.mu.Lock()
	defer identity.mu.Unlock()
	identity.counter++
	return fmt.Sprintf("%s.%d", kind, identity.counter), nil
}

type fakeExecutor struct {
	status        host.ActionRunStatus
	dispatcher    *fakeDispatcher
	output        json.RawMessage
	executeErr    error
	executions    int
	cancellations int
}

func (executor *fakeExecutor) Execute(
	_ context.Context,
	invocation host.ActionInvocation,
) (ActionExecution, error) {
	if !executor.dispatcher.inside {
		return ActionExecution{}, errors.New("executor was called outside authority")
	}
	executor.executions++
	if executor.executeErr != nil {
		return ActionExecution{}, executor.executeErr
	}
	progress := uint32(20)
	if executor.status == host.ActionSucceeded {
		progress = 100
	}
	now := host.Timepoint{Clock: host.ClockEvent, Value: 11}
	run := host.ActionRun{
		OperationID: invocation.OperationID, Status: executor.status,
		ProgressSeq: 1, Progress: progress, UpdatedAt: now,
	}
	if !terminal(executor.status) {
		return ActionExecution{Run: run}, nil
	}
	output := executor.output
	if len(output) == 0 {
		output = json.RawMessage(`{}`)
	}
	outcome := host.ActionOutcome{
		OperationID: invocation.OperationID, Status: executor.status,
		Summary: "dialogue applied", Epoch: invocation.ExpectedEpoch,
		WorldSeq: 1, OccurredAt: now,
	}
	return ActionExecution{
		Run: run, Outcome: &outcome, Output: output,
	}, nil
}

func (executor *fakeExecutor) Cancel(
	_ context.Context,
	invocation host.ActionInvocation,
	identity HostIdentity,
) (ActionExecution, error) {
	if !executor.dispatcher.inside {
		return ActionExecution{}, errors.New("cancel was called outside authority")
	}
	executor.cancellations++
	run := host.ActionRun{
		OperationID: invocation.OperationID, Status: host.ActionStale,
		ProgressSeq: 2, Progress: 20, UpdatedAt: identity.Now,
	}
	outcome := host.ActionOutcome{
		OperationID: invocation.OperationID, Status: host.ActionStale,
		Code: "epoch.changed", Summary: "Host epoch changed.",
		Epoch: invocation.ExpectedEpoch, WorldSeq: 2, OccurredAt: identity.Now,
	}
	return ActionExecution{
		Run: run, Outcome: &outcome, Output: json.RawMessage(`{}`),
	}, nil
}
