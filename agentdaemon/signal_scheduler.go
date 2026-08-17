package agentdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

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
	var cursor uint64
	for ctx.Err() == nil {
		signals, next, err := store.WaitAny(ctx, cursor, 25*time.Second)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, signalbox.ErrClosed) {
				return
			}
			continue
		}
		cursor = next
		for _, current := range signals {
			daemon.dispatchSignal(ctx, control, personas, principal, current)
		}
	}
}

func (daemon *Daemon) dispatchSignal(
	ctx context.Context,
	control *controlplane.Service,
	personas cognition.PersonaProvider,
	principal host.Principal,
	current signalbox.Signal,
) {
	actor, err := control.GetActor(
		principal, current.HostID, current.WorldID, current.ActorID,
	)
	if err != nil || !actor.Online || actor.Authority.Source != controlplane.DecisionInternal ||
		actor.Epoch != current.Epoch || actor.ObservationSeq != current.ObservationSequence {
		return
	}
	controllerID := proactiveControllerID(current.ActorID)
	persona, err := personas.Load(ctx, cognition.PersonaRequest{
		ActorID: current.ActorID, ControllerID: controllerID,
	})
	if err != nil || !persona.Initiative.Enabled ||
		!initiativeMatches(persona.Initiative.Triggers, current.Kind) {
		return
	}
	maxActions := persona.Initiative.MaxConsecutiveActions
	if maxActions == 0 {
		maxActions = 1
	}
	goal := "Notice and respond naturally to the current game event without assuming facts beyond the latest observation: " + current.Summary
	_, _ = daemon.service.StartSignalTask(ctx, principal, cognition.StartTaskInput{
		TaskID: proactiveTaskID(current), HostID: current.HostID, WorldID: current.WorldID,
		ActorID: current.ActorID, ControllerID: controllerID, Goal: goal,
		Tags: []string{"signal", current.Kind}, PlanningMode: taskstate.PlanningDisabled,
		Budget: cognition.TaskBudget{
			MaxSteps: maxActions * 4, MaxModelCalls: maxActions * 4,
			MaxModelTokens: uint64(maxActions) * 32_000, MaxActions: maxActions,
		},
	}, timeline.SignalContextRef{
		SignalID: current.SignalID, Kind: current.Kind, Cursor: current.Cursor,
	})
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
