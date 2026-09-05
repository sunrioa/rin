package agentdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/signalbox"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

func (daemon *Daemon) runSignalScheduler(
	ctx context.Context,
	store *signalbox.Store,
	control *controlplane.Service,
	personas cognition.PersonaProvider,
	principal host.Principal,
) {
	defer daemon.signalWG.Done()
	for ctx.Err() == nil {
		signals, err := store.WaitPending(ctx, 25*time.Second)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, signalbox.ErrClosed) {
				return
			}
			continue
		}
		for _, current := range signals {
			result, dispatchErr := daemon.dispatchSignal(ctx, control, personas, principal, current)
			if ctx.Err() != nil {
				return
			}
			if dispatchErr != nil {
				result.Status, result.Reason = "retry", "temporarily-unavailable"
				if errors.Is(dispatchErr, agentapi.ErrForbidden) || errors.Is(dispatchErr, controlplane.ErrForbidden) {
					result.Status, result.Reason = "dropped", "forbidden"
				}
			}
			if err := store.RecordDelivery(current, result.Status, result.Reason, result.TaskID); errors.Is(err, signalbox.ErrClosed) {
				return
			}
		}
	}
}

func (daemon *Daemon) dispatchSignal(
	ctx context.Context,
	control *controlplane.Service,
	personas cognition.PersonaProvider,
	principal host.Principal,
	current signalbox.Signal,
) (cognition.SignalHandlingResult, error) {
	actor, err := control.GetActor(
		principal, current.HostID, current.WorldID, current.ActorID,
	)
	if err != nil {
		return cognition.SignalHandlingResult{}, err
	}
	if !actor.Online {
		return cognition.SignalHandlingResult{Status: "retry", Reason: "host-offline"}, nil
	}
	if actor.Authority.Source != controlplane.DecisionInternal {
		return cognition.SignalHandlingResult{Status: "dropped", Reason: "external-authority"}, nil
	}
	if actor.Epoch != current.Epoch {
		return cognition.SignalHandlingResult{Status: "dropped", Reason: "epoch-changed"}, nil
	}
	if actor.ObservationSeq < current.ObservationSequence {
		return cognition.SignalHandlingResult{Status: "retry", Reason: "observation-pending"}, nil
	}
	controllerID := proactiveControllerID(current.ActorID)
	persona, err := personas.Load(ctx, cognition.PersonaRequest{
		ActorID: current.ActorID, ControllerID: controllerID,
	})
	if err != nil {
		return cognition.SignalHandlingResult{}, err
	}
	if !persona.Initiative.Enabled || !initiativeMatches(persona.Initiative.Triggers, current.Kind) {
		return cognition.SignalHandlingResult{Status: "dropped", Reason: "initiative-disabled"}, nil
	}
	maxActions := persona.Initiative.MaxConsecutiveActions
	if maxActions == 0 {
		maxActions = 1
	}
	goal := "Notice and respond naturally to the current game event without assuming facts beyond the latest observation: " + current.Summary
	return daemon.service.HandleActorSignal(ctx, principal, cognition.ActorSignalInput{Task: cognition.StartTaskInput{
		TaskID: proactiveTaskID(current), HostID: current.HostID, WorldID: current.WorldID,
		ActorID: current.ActorID, ControllerID: controllerID, Goal: goal,
		Tags: []string{"signal", current.Kind}, PlanningMode: taskstate.PlanningDisabled,
		Completion: cognition.TaskCompletionPolicy{Mode: cognition.CompletionModel},
		Budget: cognition.TaskBudget{
			MaxSteps: maxActions * 4, MaxModelCalls: maxActions * 4,
			MaxModelTokens: uint64(maxActions) * 32_000, MaxActions: maxActions,
		},
	}, Signal: cognition.TaskSignal{SignalContextRef: timeline.SignalContextRef{
		SignalID: current.SignalID, Kind: current.Kind, Cursor: current.Cursor,
	}, Summary: current.Summary, Epoch: current.Epoch, ObservationSequence: current.ObservationSequence, ExpiresAtUnixMillis: current.ExpiresAtUnixMillis},
		Preempt: slices.Contains(persona.Initiative.PreemptTriggers, current.Kind), CooldownMillis: persona.Initiative.CooldownMillis})
}

func proactiveTaskID(current signalbox.Signal) string {
	digest := sha256.Sum256([]byte(
		current.HostID + "\x00" + current.WorldID + "\x00" + current.ActorID + "\x00" + current.SignalID,
	))
	return "task.signal." + hex.EncodeToString(digest[:12])
}

func proactiveControllerID(actorID string) string {
	digest := sha256.Sum256([]byte(actorID))
	return "controller.signal." + hex.EncodeToString(digest[:6])
}

func initiativeMatches(triggers []string, kind string) bool {
	if len(triggers) == 0 {
		return true
	}
	for _, trigger := range triggers {
		if trigger == kind {
			return true
		}
	}
	return false
}
