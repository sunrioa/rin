package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/release"
)

// Gateway binds one configured external principal to Control V2 tools.
type Gateway struct {
	client    ControlClient
	principal host.Principal
	server    *mcp.Server
}

// New creates a scope-bounded MCP gateway over an in-process Control Service.
func New(
	service *controlplane.Service,
	principal host.Principal,
) (*Gateway, error) {
	client, err := controlplane.NewClientService(service, principal)
	if err != nil {
		return nil, err
	}
	return NewClient(client, principal)
}

// NewClient creates a thin gateway backed by the same application contract as
// HTTP. MCP owns no Host lifecycle, policy engine, or execution state.
func NewClient(
	client ControlClient,
	principal host.Principal,
) (*Gateway, error) {
	if client == nil {
		return nil, errorsInvalid("client is required")
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, errorsInvalid("principal: " + err.Error())
	}
	if !hasControlScope(principal) {
		return nil, errorsInvalid("principal has no Control Plane scope")
	}
	gateway := &Gateway{
		client:    client,
		principal: clonePrincipal(principal),
	}
	gateway.server = mcp.NewServer(
		&mcp.Implementation{Name: "rin", Version: release.Version},
		&mcp.ServerOptions{
			Instructions: "Inspect only Host-published observations and capability specs. Acquire the actor's controller lease before submitting a typed action. The Host binds effects and Rin policy authorizes them; never invent effects, permissions, targets, or execution results. queued, delivered, accepted, and running are not completion. Report success only when execution_confirmed=true with an authoritative Host outcome. outcome-unknown is unresolved and must not be retried automatically.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	if gateway.granted(controlplane.ScopeActorRead) {
		gateway.addReadTools()
	}
	gateway.addOperationTools()
	if gateway.granted(controlplane.ScopeActorControl) {
		gateway.addControllerTools()
	}
	if gateway.granted(controlplane.ScopeActorExecute) {
		gateway.addActionTools()
	}
	if gateway.granted(controlplane.ScopeOperationCancel) {
		gateway.addCancelTool()
	}
	return gateway, nil
}

func (gateway *Gateway) Server() *mcp.Server {
	return gateway.server
}

func (gateway *Gateway) Run(ctx context.Context, transport mcp.Transport) error {
	return gateway.server.Run(ctx, transport)
}

func (gateway *Gateway) addReadTools() {
	annotations := readAnnotations()
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "list_worlds", Description: "List Host-published worlds visible to the configured principal.",
		Annotations: annotations,
	}, gateway.listWorlds)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "list_actors", Description: "List visible actors in one Host-published world.",
		Annotations: annotations,
	}, gateway.listActors)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "get_actor_state", Description: "Read one actor's current redacted summary, authority, controller, and emergency-stop state.",
		Annotations: annotations,
	}, gateway.getActorState)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "wait_actor_update", Description: "Wait up to 25 seconds for a newer actor, authority, controller, or emergency-stop cursor.",
		Annotations: annotations,
	}, gateway.waitActorUpdate)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "observe_actor", Description: "Read the latest complete V2 observation authored by the authoritative Host. Opaque Host references may be copied into an action but never fabricated.",
		Annotations: annotations,
	}, gateway.observeActor)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "list_actor_capabilities", Description: "List compact summaries of the exact typed capabilities currently published for an actor. Discovery is not authorization.",
		Annotations: annotations,
	}, gateway.listActorCapabilities)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "describe_actor_capability", Description: "Read the input, output, and effect schemas for one exact capability version and digest.",
		Annotations: annotations,
	}, gateway.describeActorCapability)
}

func (gateway *Gateway) addOperationTools() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "get_operation", Description: "Read one operation. Success requires execution_confirmed=true and a Host outcome; outcome-unknown remains unresolved.",
		Annotations: readAnnotations(),
	}, gateway.getOperation)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "wait_operation", Description: "Wait up to 25 seconds for an operation change. changed=false is no new evidence; continue while reconciliation_pending=true.",
		Annotations: readAnnotations(),
	}, gateway.waitOperation)
}

func (gateway *Gateway) addControllerTools() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "acquire_actor_control", Description: "Acquire the actor's exclusive, epoch-bound deliberative controller lease. This grants no gameplay effect by itself.",
		Annotations: writeAnnotations(false),
	}, gateway.acquireActorControl)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "renew_actor_control", Description: "Renew one exact live controller lease without changing its authority or persona mode.",
		Annotations: writeAnnotations(false),
	}, gateway.renewActorControl)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "release_actor_control", Description: "Release one exact controller lease and fence its unfinished operations.",
		Annotations: writeAnnotations(false),
	}, gateway.releaseActorControl)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "get_actor_control", Description: "Read the actor's current live controller lease.",
		Annotations: readAnnotations(),
	}, gateway.getActorControl)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "confirm_action", Description: "Approve the exact single-use policy challenge on an awaiting-confirmation operation. The Host snapshot and effect binding are rechecked before queueing.",
		Annotations: writeAnnotations(true),
	}, gateway.confirmAction)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "set_actor_emergency_stop", Description: "Latch or clear the owner-controlled actor emergency stop. Latching blocks new actions and cancels unfinished work.",
		Annotations: writeAnnotations(true),
	}, gateway.setActorEmergencyStop)
}

func (gateway *Gateway) addActionTools() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "submit_actor_action", Description: "Submit one typed ActionRequest using an exact published capability, observation, Epoch, and opaque target references. The returned operation is not proof of execution.",
		Annotations: writeAnnotations(true),
	}, gateway.submitActorAction)
}

func (gateway *Gateway) addCancelTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name: "cancel_operation", Description: "Request cancellation of one operation. Cancellation does not imply rollback.",
		Annotations: writeAnnotations(false),
	}, gateway.cancelOperation)
}

func (gateway *Gateway) listWorlds(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ ListWorldsInput,
) (*mcp.CallToolResult, ListWorldsOutput, error) {
	views, err := gateway.client.ListWorlds(ctx)
	if err != nil {
		return nil, ListWorldsOutput{}, err
	}
	output := ListWorldsOutput{Worlds: make([]World, len(views))}
	for index, view := range views {
		output.Worlds[index] = World{
			HostID: view.HostID, WorldID: view.WorldID,
			DisplayName: view.DisplayName, Sequence: view.Sequence,
			Online: view.Online, LeaseExpiresAtUnixMillis: view.LeaseExpiresAtMillis,
		}
	}
	return nil, output, nil
}

func (gateway *Gateway) listActors(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ListActorsInput,
) (*mcp.CallToolResult, ListActorsOutput, error) {
	views, err := gateway.client.ListActors(ctx, input.HostID, input.WorldID)
	if err != nil {
		return nil, ListActorsOutput{}, err
	}
	output := ListActorsOutput{Actors: make([]Actor, len(views))}
	for index, view := range views {
		output.Actors[index], err = convertActor(view)
		if err != nil {
			return nil, ListActorsOutput{}, err
		}
	}
	return nil, output, nil
}

func (gateway *Gateway) getActorState(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input GetActorStateInput,
) (*mcp.CallToolResult, GetActorStateOutput, error) {
	view, err := gateway.client.GetActor(ctx, input.HostID, input.WorldID, input.ActorID)
	if err != nil {
		return nil, GetActorStateOutput{}, err
	}
	actor, err := convertActor(view)
	return nil, GetActorStateOutput{Actor: actor}, err
}

func (gateway *Gateway) waitActorUpdate(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input WaitActorUpdateInput,
) (*mcp.CallToolResult, WaitActorUpdateOutput, error) {
	update, err := gateway.client.WaitActor(ctx, controlplane.WaitActorInput{
		HostID: input.HostID, WorldID: input.WorldID, ActorID: input.ActorID,
		AfterObservationSeq:        input.AfterObservationSeq,
		AfterAuthorityRevision:     input.AfterAuthorityRevision,
		AfterControllerLeaseID:     input.AfterControllerLeaseID,
		AfterEmergencyStopRevision: input.AfterEmergencyStopRevision,
		WaitMillis:                 input.WaitMillis,
	})
	if err != nil {
		return nil, WaitActorUpdateOutput{}, err
	}
	actor, err := convertActor(update.Actor)
	return nil, WaitActorUpdateOutput{Actor: actor, Changed: update.Changed}, err
}

func (gateway *Gateway) observeActor(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ActorTargetInput,
) (*mcp.CallToolResult, ObserveActorOutput, error) {
	observation, err := gateway.client.GetObservation(ctx, input.target())
	if err != nil {
		return nil, ObserveActorOutput{}, err
	}
	converted, err := convertObservation(observation)
	return nil, ObserveActorOutput{Observation: converted}, err
}

func (gateway *Gateway) listActorCapabilities(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ActorTargetInput,
) (*mcp.CallToolResult, ListActorCapabilitiesOutput, error) {
	snapshot, err := gateway.client.ListCapabilities(ctx, input.target())
	if err != nil {
		return nil, ListActorCapabilitiesOutput{}, err
	}
	output := ListActorCapabilitiesOutput{
		Revision:     snapshot.Revision,
		Capabilities: make([]CapabilitySummary, len(snapshot.Specs)),
	}
	for index, spec := range snapshot.Specs {
		output.Capabilities[index] = CapabilitySummary{
			Capability: spec.Capability, Description: spec.Description,
			Kind: spec.Kind, Execution: spec.Execution,
			Cancellation: spec.Cancellation, RiskFloor: spec.RiskFloor,
			RequiredScopes:  append([]string(nil), spec.RequiredScopes...),
			ExecutionBudget: spec.ExecutionBudget,
			MaxInputBytes:   spec.MaxInputBytes, MaxOutputBytes: spec.MaxOutputBytes,
			MaxEffects:              spec.MaxEffects,
			ProducesChildOperations: spec.ProducesChildOperations,
			Digest:                  spec.Digest,
		}
	}
	return nil, output, nil
}

func (gateway *Gateway) describeActorCapability(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input DescribeActorCapabilityInput,
) (*mcp.CallToolResult, DescribeActorCapabilityOutput, error) {
	spec, err := gateway.client.DescribeCapability(ctx, controlplane.DescribeCapabilityInput{
		ActorControlTarget: input.target(),
		Capability: host.CapabilityRef{
			ID: input.CapabilityID, Version: input.CapabilityVersion,
		},
	})
	if err != nil {
		return nil, DescribeActorCapabilityOutput{}, err
	}
	converted, err := convertCapabilitySpec(spec)
	return nil, DescribeActorCapabilityOutput{Capability: converted}, err
}

func (gateway *Gateway) acquireActorControl(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input AcquireActorControlInput,
) (*mcp.CallToolResult, ControllerOutput, error) {
	lease, err := gateway.client.AcquireController(ctx, controlplane.AcquireControllerInput{
		ActorControlTarget: input.target(),
		ControllerID:       input.ControllerID, LeaseTTLMillis: input.LeaseTTLMillis,
	})
	return nil, ControllerOutput{Controller: lease}, err
}

func (gateway *Gateway) renewActorControl(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input RenewActorControlInput,
) (*mcp.CallToolResult, ControllerOutput, error) {
	lease, err := gateway.client.RenewController(ctx, controlplane.RenewControllerInput{
		ActorControlTarget: input.target(), LeaseID: input.LeaseID,
		LeaseTTLMillis: input.LeaseTTLMillis,
	})
	return nil, ControllerOutput{Controller: lease}, err
}

func (gateway *Gateway) releaseActorControl(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ReleaseActorControlInput,
) (*mcp.CallToolResult, ReleaseActorControlOutput, error) {
	err := gateway.client.ReleaseController(ctx, controlplane.ReleaseControllerInput{
		ActorControlTarget: input.target(), LeaseID: input.LeaseID,
	})
	return nil, ReleaseActorControlOutput{Released: err == nil}, err
}

func (gateway *Gateway) getActorControl(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ActorTargetInput,
) (*mcp.CallToolResult, ControllerOutput, error) {
	lease, err := gateway.client.GetController(ctx, input.target())
	return nil, ControllerOutput{Controller: lease}, err
}

func (gateway *Gateway) submitActorAction(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input SubmitActorActionInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	arguments, err := encodeObject(input.Arguments)
	if err != nil {
		return nil, OperationOutput{}, err
	}
	operation, err := gateway.client.SubmitAction(ctx, controlplane.SubmitActionInput{
		HostID: input.HostID, WorldID: input.WorldID,
		Request: host.ActionRequest{
			RequestID: input.RequestID, ControllerID: input.ControllerID,
			ActorID: input.ActorID,
			Capability: host.CapabilityRef{
				ID: input.CapabilityID, Version: input.CapabilityVersion,
			},
			SpecDigest: input.SpecDigest, Arguments: arguments,
			Targets:        append([]host.HostRef(nil), input.Targets...),
			ExpectedEpoch:  input.ExpectedEpoch,
			ObservationSeq: input.ObservationSeq, TaskID: input.TaskID,
			IdempotencyKey: input.IdempotencyKey,
		},
		ParentOperationID: input.ParentOperationID,
	})
	if err != nil {
		return nil, OperationOutput{}, err
	}
	converted, err := convertOperation(operation)
	return nil, OperationOutput{Operation: converted}, err
}

func (gateway *Gateway) confirmAction(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ConfirmActionInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.ConfirmAction(ctx, input.OperationID)
	if err != nil {
		return nil, OperationOutput{}, err
	}
	converted, err := convertOperation(operation)
	return nil, OperationOutput{Operation: converted}, err
}

func (gateway *Gateway) setActorEmergencyStop(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input SetEmergencyStopInput,
) (*mcp.CallToolResult, EmergencyStopOutput, error) {
	stop, err := gateway.client.SetEmergencyStop(ctx, controlplane.SetEmergencyStopInput{
		ActorControlTarget: input.target(), Active: input.Active,
	})
	return nil, EmergencyStopOutput{EmergencyStop: stop}, err
}

func (gateway *Gateway) getOperation(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input GetOperationInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.GetOperation(ctx, input.OperationID)
	if err != nil {
		return nil, OperationOutput{}, err
	}
	converted, err := convertOperation(operation)
	return nil, OperationOutput{Operation: converted}, err
}

func (gateway *Gateway) waitOperation(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input WaitOperationInput,
) (*mcp.CallToolResult, OperationUpdateOutput, error) {
	update, err := gateway.client.WaitOperation(ctx, controlplane.WaitOperationInput{
		OperationID: input.OperationID, AfterCursor: input.AfterCursor,
		WaitMillis: input.WaitMillis,
	})
	if err != nil {
		return nil, OperationUpdateOutput{}, err
	}
	operation, err := convertOperation(update.Operation)
	return nil, OperationUpdateOutput{
		Operation: operation, Changed: update.Changed,
	}, err
}

func (gateway *Gateway) cancelOperation(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input CancelOperationInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.CancelOperation(ctx, input.OperationID)
	if err != nil {
		return nil, OperationOutput{}, err
	}
	converted, err := convertOperation(operation)
	return nil, OperationOutput{Operation: converted}, err
}

func convertObservation(value host.ObservationEnvelope) (Observation, error) {
	payload, err := decodeObject(value.Payload)
	if err != nil {
		return Observation{}, err
	}
	converted := Observation{
		ObservationID: value.ObservationID, HostID: value.HostID,
		WorldID: value.WorldID, ActorID: value.ActorID, Epoch: value.Epoch,
		Sequence: value.Sequence, ObservedAt: value.ObservedAt,
		Schema: value.Schema, Payload: payload,
		Facts:             make([]ObservationFact, len(value.Facts)),
		Resources:         make([]ObservationResource, len(value.Resources)),
		Artifacts:         append([]host.ObservationArtifact(nil), value.Artifacts...),
		ContinuationToken: value.ContinuationToken,
	}
	for index, fact := range value.Facts {
		decoded, err := decodeJSONValue(fact.Value)
		if err != nil {
			return Observation{}, err
		}
		converted.Facts[index] = ObservationFact{
			FactID: fact.FactID, Kind: fact.Kind, Subject: fact.Subject,
			Tags: append([]string(nil), fact.Tags...), Value: decoded,
		}
	}
	for index, resource := range value.Resources {
		attributes, err := decodeObject(resource.Attributes)
		if err != nil {
			return Observation{}, err
		}
		converted.Resources[index] = ObservationResource{
			Ref: resource.Ref, Kind: resource.Kind,
			Tags:      append([]string(nil), resource.Tags...),
			Ownership: resource.Ownership, Scope: resource.Scope,
			Quantity: resource.Quantity, Unit: resource.Unit,
			Attributes: attributes,
		}
	}
	return converted, nil
}

func convertCapabilitySpec(value host.CapabilitySpec) (CapabilitySpec, error) {
	input, err := convertSchema(value.Input)
	if err != nil {
		return CapabilitySpec{}, err
	}
	output, err := convertSchema(value.Output)
	if err != nil {
		return CapabilitySpec{}, err
	}
	effects, err := convertSchema(value.EffectSchema)
	if err != nil {
		return CapabilitySpec{}, err
	}
	return CapabilitySpec{
		Capability: value.Capability, Description: value.Description,
		Input: input, Output: output, EffectSchema: effects,
		Kind: value.Kind, Execution: value.Execution,
		Cancellation: value.Cancellation, RiskFloor: value.RiskFloor,
		RequiredDurability: value.RequiredDurability,
		RequiredScopes:     append([]string(nil), value.RequiredScopes...),
		ExecutionBudget:    value.ExecutionBudget,
		MaxInputBytes:      value.MaxInputBytes, MaxOutputBytes: value.MaxOutputBytes,
		MaxEffects:              value.MaxEffects,
		ProducesChildOperations: value.ProducesChildOperations,
		Digest:                  value.Digest,
	}, nil
}

func convertSchema(value host.Schema) (Schema, error) {
	document, err := decodeObject(value.Document)
	if err != nil {
		return Schema{}, err
	}
	return Schema{Dialect: value.Dialect, Document: document, SHA256: value.SHA256}, nil
}

func convertOperation(value controlplane.OperationView) (Operation, error) {
	converted := Operation{
		OperationID: value.OperationID, RequestID: value.RequestID,
		HostID: value.HostID, WorldID: value.WorldID, ActorID: value.ActorID,
		Kind:              value.Kind,
		ControllerLeaseID: value.ControllerLeaseID,
		ParentOperationID: value.ParentOperationID,
		ChildOperationIDs: append([]string(nil), value.ChildOperationIDs...),
		PolicyDecision:    value.PolicyDecision, Status: value.Status,
		Cursor: value.Cursor, Terminal: value.Terminal,
		ReconciliationPending: value.ReconciliationPending,
		ExecutionConfirmed:    value.ExecutionConfirmed,
		CancelRequested:       value.CancelRequested,
		DeliveryAttempts:      value.DeliveryAttempts, Run: value.Run,
		Outcome: value.Outcome, Output: value.Output,
		RejectionCode:    value.RejectionCode,
		RejectionMessage: value.RejectionMessage,
		CreatedAt:        value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if value.ActionRequest != nil {
		request, err := convertActionRequest(*value.ActionRequest)
		if err != nil {
			return Operation{}, err
		}
		converted.ActionRequest = &request
	}
	if value.BoundAction != nil {
		action, err := convertBoundAction(*value.BoundAction)
		if err != nil {
			return Operation{}, err
		}
		converted.BoundAction = &action
	}
	return converted, nil
}

func convertActionRequest(value host.ActionRequest) (ActionRequest, error) {
	arguments, err := decodeObject(value.Arguments)
	if err != nil {
		return ActionRequest{}, err
	}
	return ActionRequest{
		RequestID: value.RequestID, ControllerID: value.ControllerID,
		ActorID: value.ActorID, Capability: value.Capability,
		SpecDigest: value.SpecDigest, Arguments: arguments,
		Targets:       append([]host.HostRef(nil), value.Targets...),
		ExpectedEpoch: value.ExpectedEpoch, ObservationSeq: value.ObservationSeq,
		TaskID: value.TaskID, IdempotencyKey: value.IdempotencyKey,
	}, nil
}

func convertBoundAction(value host.BoundAction) (BoundAction, error) {
	arguments, err := decodeObject(value.NormalizedArguments)
	if err != nil {
		return BoundAction{}, err
	}
	effects := make([]Effect, len(value.Effects))
	for index, effect := range value.Effects {
		attributes, err := decodeObject(effect.Attributes)
		if err != nil {
			return BoundAction{}, err
		}
		effects[index] = Effect{
			EffectID: effect.EffectID, Kind: effect.Kind,
			Operation: effect.Operation, Subject: effect.Subject,
			Target: effect.Target, Tags: append([]string(nil), effect.Tags...),
			Ownership: effect.Ownership, Scope: effect.Scope,
			Quantity: effect.Quantity, Unit: effect.Unit,
			Reversible: effect.Reversible, Risk: effect.Risk,
			Attributes: attributes,
		}
	}
	return BoundAction{
		BindingID: value.BindingID, RequestID: value.RequestID,
		RequestDigest: value.RequestDigest, ControllerID: value.ControllerID,
		ActorID: value.ActorID, Capability: value.Capability,
		SpecDigest: value.SpecDigest, NormalizedArguments: arguments,
		RequestedTargets: append([]host.HostRef(nil), value.RequestedTargets...),
		ResolvedTargets:  append([]host.HostRef(nil), value.ResolvedTargets...),
		ExpectedEpoch:    value.ExpectedEpoch, ObservationSeq: value.ObservationSeq,
		TaskID: value.TaskID, IdempotencyKey: value.IdempotencyKey,
		Effects: effects, EffectDigest: value.EffectDigest,
		BoundAt: value.BoundAt, ValidUntil: value.ValidUntil,
	}, nil
}

func convertActor(view controlplane.ActorView) (Actor, error) {
	state, err := decodeObject(view.State)
	if err != nil {
		return Actor{}, err
	}
	var controller *controlplane.ControllerLease
	if view.Controller != nil {
		copyLease := *view.Controller
		controller = &copyLease
	}
	return Actor{
		HostID: view.HostID, WorldID: view.WorldID, ActorID: view.ActorID,
		OwnerPrincipalID: view.OwnerPrincipalID, DisplayName: view.DisplayName,
		ObservationSeq: view.ObservationSeq, Epoch: view.Epoch,
		DecisionAuthority: view.Authority, Controller: controller,
		EmergencyStopped:      view.EmergencyStopped,
		EmergencyStopRevision: view.EmergencyStopRevision,
		State:                 state, Online: view.Online,
		LeaseExpiresAtUnixMillis: view.LeaseExpiresAtMillis,
	}, nil
}

func encodeObject(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		return nil, fmt.Errorf("action arguments must be a JSON object")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode action arguments: %w", err)
	}
	return payload, nil
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode Host-published object: %w", err)
	}
	return value, nil
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode Host-published JSON value: %w", err)
	}
	return value, nil
}

func clonePrincipal(value host.Principal) host.Principal {
	value.GrantedScopes = append([]string(nil), value.GrantedScopes...)
	return value
}

func scopeGranted(principal host.Principal, scope string) bool {
	for _, granted := range principal.GrantedScopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func (gateway *Gateway) granted(scope string) bool {
	return scopeGranted(gateway.principal, scope) ||
		scopeGranted(gateway.principal, controlplane.ScopeHostAdmin)
}

func hasControlScope(principal host.Principal) bool {
	for _, scope := range []string{
		controlplane.ScopeActorRead,
		controlplane.ScopeActorControl,
		controlplane.ScopeActorExecute,
		controlplane.ScopeOperationCancel,
		controlplane.ScopeHostAdmin,
	} {
		if scopeGranted(principal, scope) {
			return true
		}
	}
	return false
}

func readAnnotations() *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld,
	}
}

func writeAnnotations(destructive bool) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		DestructiveHint: &destructive, IdempotentHint: true,
		OpenWorldHint: &closedWorld, ReadOnlyHint: false,
	}
}

func errorsInvalid(message string) error {
	return fmt.Errorf("invalid MCP gateway configuration: %s", message)
}
