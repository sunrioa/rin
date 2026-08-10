package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/examples/adapters/story"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/sdk/hostkit"
)

const (
	principalID  = "player.story.cli"
	controllerID = "controller.story.cli"
)

type options struct {
	line  string
	topic string
	task  string
	json  bool
}

type actionSummary struct {
	Capability         string                       `json:"capability"`
	Status             controlplane.OperationStatus `json:"status"`
	ExecutionConfirmed bool                         `json:"execution_confirmed"`
	Reason             string                       `json:"reason,omitempty"`
	Output             map[string]any               `json:"output,omitempty"`
}

type demoOutput struct {
	Actions []actionSummary `json:"actions"`
	State   story.State     `json:"state"`
}

func main() {
	configuration := options{}
	flag.StringVar(&configuration.line, "line", "", "line for Mira to speak")
	flag.StringVar(&configuration.topic, "topic", story.TopicFestival, "next conversation topic")
	flag.StringVar(&configuration.task, "task", "prepare-exhibit", "story task to accept")
	flag.BoolVar(&configuration.json, "json", false, "print machine-readable output")
	flag.Parse()
	if configuration.line == "" {
		configuration.line = promptLine()
	}
	if err := run(configuration); err != nil {
		fmt.Fprintln(os.Stderr, "terminal story:", err)
		os.Exit(1)
	}
}

func run(configuration options) error {
	ctx := context.Background()
	adapter, err := story.New()
	if err != nil {
		return err
	}
	coordinator, err := hostkit.NewAdapterCoordinator(ctx, adapter, inlineDispatcher{})
	if err != nil {
		return err
	}
	engine, err := story.NewPolicy()
	if err != nil {
		return err
	}
	service := controlplane.New(controlplane.Options{
		ActionHost: coordinator, PolicyEngine: engine,
	})
	defer service.Close()
	lease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          story.HostID, InstanceID: "instance.story.cli",
		Manifest: coordinator.Manifest(), LeaseTTLMillis: 60_000,
	})
	if err != nil {
		return err
	}
	authority := controlplane.DecisionAuthority{
		Source: controlplane.DecisionExternal, ControllerPrincipalID: principalID,
		Revision: 1, PersonaMode: controlplane.PersonaAgentAvatar,
	}
	if err := publishStory(ctx, service, lease, coordinator, adapter, authority); err != nil {
		return err
	}
	principal := host.Principal{
		ID: principalID,
		GrantedScopes: []string{
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeActorRead,
			controlplane.ScopeOperationCancel,
		},
	}
	controller, err := service.AcquireController(principal, controlplane.AcquireControllerInput{
		ActorControlTarget: storyTarget(), ControllerID: controllerID,
		LeaseTTLMillis: 60_000,
	})
	if err != nil {
		return err
	}
	defer service.ReleaseController(principal, storyTarget(), controller.LeaseID)

	actions := []struct {
		capability string
		arguments  json.RawMessage
	}{
		{story.CapabilitySpeak, mustJSON(map[string]any{"text": configuration.line})},
		{story.CapabilityChangeTopic, mustJSON(map[string]any{"topic": configuration.topic})},
		{story.CapabilityAcceptTask, mustJSON(map[string]any{"task": configuration.task})},
	}
	summaries := make([]actionSummary, 0, len(actions))
	for index, action := range actions {
		summary, actionErr := executeStoryAction(
			ctx, service, lease, coordinator, adapter, authority,
			principal, index+1, action.capability, action.arguments,
		)
		if actionErr != nil {
			return actionErr
		}
		summaries = append(summaries, summary)
	}
	result := demoOutput{Actions: summaries, State: adapter.State()}
	if configuration.json {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	for _, action := range summaries {
		if action.ExecutionConfirmed {
			fmt.Printf("[completed] %s\n", action.Capability)
		} else {
			fmt.Printf("[denied] %s: %s\n", action.Capability, action.Reason)
		}
	}
	for _, line := range result.State.Transcript {
		fmt.Printf("%s: %s\n", line.Speaker, line.Text)
	}
	fmt.Printf(
		"Scene: %s | Topic: %s | Relation: %d | Task: %s\n",
		result.State.Scene,
		result.State.Topic,
		result.State.Relation,
		result.State.AcceptedTask,
	)
	return nil
}

func executeStoryAction(
	ctx context.Context,
	service *controlplane.Service,
	lease controlplane.HostLease,
	coordinator *hostkit.AdapterCoordinator,
	adapter *story.Adapter,
	authority controlplane.DecisionAuthority,
	principal host.Principal,
	index int,
	capabilityID string,
	arguments json.RawMessage,
) (actionSummary, error) {
	snapshot, err := coordinator.SnapshotAction(ctx, storyTarget())
	if err != nil {
		return actionSummary{}, err
	}
	catalog := coordinator.Capabilities()
	capabilityIndex := slices.IndexFunc(catalog.Specs, func(spec host.CapabilitySpec) bool {
		return spec.Capability.ID == capabilityID
	})
	if capabilityIndex < 0 {
		return actionSummary{}, errors.New("story capability is missing")
	}
	spec := catalog.Specs[capabilityIndex]
	requestID := fmt.Sprintf("request.story.cli.%d", index)
	view, err := service.SubmitAction(ctx, principal, controlplane.SubmitActionInput{
		HostID:  story.HostID,
		WorldID: story.WorldID,
		Request: host.ActionRequest{
			RequestID: requestID, ControllerID: controllerID, ActorID: story.ActorID,
			Capability: spec.Capability, SpecDigest: spec.Digest,
			Arguments:     append(json.RawMessage(nil), arguments...),
			ExpectedEpoch: snapshot.Epoch, ObservationSeq: snapshot.ObservationSeq,
			IdempotencyKey: fmt.Sprintf("action.story.cli.%d", index),
		},
	})
	if err != nil {
		return actionSummary{}, err
	}
	if view.Status == controlplane.OperationRejected {
		return summarizeOperation(capabilityID, view), nil
	}
	pollContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	batch, err := service.PollHost(pollContext, story.HostID, lease.LeaseID, 1)
	if err != nil {
		return actionSummary{}, err
	}
	if len(batch.Requests) != 1 || batch.Requests[0].Request.OperationID != view.OperationID {
		return actionSummary{}, errors.New("story Host received an unexpected operation")
	}
	delivery := batch.Requests[0]
	if err := service.AcknowledgeHost(
		story.HostID,
		lease.LeaseID,
		controlplane.HostAcknowledgement{OperationID: view.OperationID, Accepted: true},
	); err != nil {
		return actionSummary{}, err
	}
	result, err := coordinator.ExecuteDelivery(ctx, delivery)
	if err != nil {
		return actionSummary{}, err
	}
	if result.Outcome == nil {
		return actionSummary{}, errors.New("story Adapter returned no Outcome")
	}
	if err := publishStory(ctx, service, lease, coordinator, adapter, authority); err != nil {
		return actionSummary{}, err
	}
	if err := service.ReportHostResult(
		story.HostID, lease.LeaseID, *result.Outcome, result.Output,
	); err != nil {
		return actionSummary{}, err
	}
	completed, err := service.GetOperation(principal, view.OperationID)
	if err != nil {
		return actionSummary{}, err
	}
	if !coordinator.ForgetOperation(view.OperationID) {
		return actionSummary{}, errors.New("story Adapter did not release the operation")
	}
	return summarizeOperation(capabilityID, completed), nil
}

func publishStory(
	ctx context.Context,
	service *controlplane.Service,
	lease controlplane.HostLease,
	coordinator *hostkit.AdapterCoordinator,
	adapter *story.Adapter,
	authority controlplane.DecisionAuthority,
) error {
	snapshot, err := coordinator.SnapshotAction(ctx, storyTarget())
	if err != nil {
		return err
	}
	observation, err := coordinator.Observe(ctx, host.ObservationQuery{
		QueryID: fmt.Sprintf("query.story.cli.%d", snapshot.ObservationSeq),
		HostID:  story.HostID, WorldID: story.WorldID, ActorID: story.ActorID,
		ExpectedEpoch: snapshot.Epoch, Limit: 128,
	})
	if err != nil {
		return err
	}
	state, err := json.Marshal(adapter.State())
	if err != nil {
		return err
	}
	catalog := coordinator.Capabilities()
	return service.PublishWorld(story.HostID, lease.LeaseID, controlplane.WorldPublication{
		WorldID: story.WorldID, DisplayName: "Archive Room",
		Sequence: snapshot.ObservationSeq,
		Actors: []controlplane.ActorPublication{{
			ActorID: story.ActorID, OwnerPrincipalID: principalID,
			DisplayName: "Mira", ObservationSeq: snapshot.ObservationSeq,
			Epoch: snapshot.Epoch, Authority: &authority, State: state,
			Observation: &observation, Capabilities: &catalog,
		}},
	})
}

type inlineDispatcher struct{}

func (inlineDispatcher) Dispatch(
	ctx context.Context,
	work func(context.Context) error,
) error {
	return work(ctx)
}

func storyTarget() controlplane.ActorControlTarget {
	return controlplane.ActorControlTarget{
		HostID: story.HostID, WorldID: story.WorldID, ActorID: story.ActorID,
	}
}

func summarizeOperation(
	capability string,
	view controlplane.OperationView,
) actionSummary {
	reason := view.RejectionMessage
	if reason == "" && view.Status == controlplane.OperationRejected &&
		view.PolicyDecision != nil {
		reason = view.PolicyDecision.HumanSummary
	}
	return actionSummary{
		Capability: capability, Status: view.Status,
		ExecutionConfirmed: view.ExecutionConfirmed,
		Reason:             reason, Output: cloneMap(view.Output),
	}
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	payload, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}

func mustJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func promptLine() string {
	fmt.Print("Mira will speak. Enter her next line: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(line) == 0 {
		return "The light in this photograph feels familiar."
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "The light in this photograph feels familiar."
	}
	return line
}
