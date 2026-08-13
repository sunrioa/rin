package experience_test

import (
	"testing"

	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/timeline"
)

func TestProjectAcceptsOnlyAuthoritativeSuccess(t *testing.T) {
	page := timeline.Page{
		ContractVersion: timeline.ContractVersion, TaskID: "task.collect.logs",
		Goal: "Collect logs", Status: "completed",
		Events: []timeline.Event{{
			EventID: "event.one", EventKind: "operation.succeeded",
			OccurredAtUnixMillis: 10, PublicSummary: "Collected eight logs.",
			Operation: &timeline.OperationSummary{
				OperationID: "operation.one", Status: "succeeded", Terminal: true,
				ExecutionConfirmed: true, OutcomeCode: "succeeded",
			},
		}},
	}
	episode, err := experience.Project(experience.ProjectionInput{
		ControllerKind: experience.ControllerExternalMCP, Timeline: page,
		Tags: []string{"collect"},
		Corrections: []experience.Correction{{
			CorrectionID: "correction.one", OccurredAtUnixMillis: 5,
			Summary: "Use the nearby forest instead.", RelatedEventID: "event.one",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if episode.EpisodeID != page.TaskID || episode.VerifiedResult == nil ||
		!episode.VerifiedResult.Success || len(episode.Corrections) != 1 {
		t.Fatalf("episode = %#v", episode)
	}

	page.Events[0].Operation.ExecutionConfirmed = false
	if _, err := experience.Project(experience.ProjectionInput{
		ControllerKind: experience.ControllerExternalMCP, Timeline: page,
	}); err == nil {
		t.Fatal("unconfirmed success became verified experience")
	}
}

func TestProjectKeepsFailureButNeverCallsItSuccess(t *testing.T) {
	page := timeline.Page{
		ContractVersion: timeline.ContractVersion, TaskID: "task.collect.logs",
		Status: "failed",
		Events: []timeline.Event{{
			EventID: "event.failed", EventKind: "operation.failed",
			OccurredAtUnixMillis: 10,
			Operation: &timeline.OperationSummary{
				OperationID: "operation.failed", Status: "failed", Terminal: true,
				OutcomeCode: "failed",
			},
		}},
	}
	episode, err := experience.Project(experience.ProjectionInput{
		ControllerKind: experience.ControllerInternal, Timeline: page,
	})
	if err != nil {
		t.Fatal(err)
	}
	if episode.VerifiedResult == nil || episode.VerifiedResult.Success {
		t.Fatalf("failure result = %#v", episode.VerifiedResult)
	}
}

func TestProjectRejectsCorrectionForUnknownEvidence(t *testing.T) {
	_, err := experience.Project(experience.ProjectionInput{
		ControllerKind: experience.ControllerInternal,
		Timeline: timeline.Page{
			ContractVersion: timeline.ContractVersion, TaskID: "task.one",
		},
		Corrections: []experience.Correction{{
			CorrectionID: "correction.one", Summary: "Try another route.",
			RelatedEventID: "missing.event",
		}},
	})
	if err == nil {
		t.Fatal("correction with unknown evidence was accepted")
	}
}
