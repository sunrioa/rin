package compat_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

type vectorFile struct {
	SchemaVersion int          `json:"schema_version"`
	Source        vectorSource `json:"source"`
	Cases         []vectorCase `json:"cases"`
}

type vectorSource struct {
	Repository  string            `json:"repository"`
	PackID      string            `json:"pack_id"`
	Version     string            `json:"version"`
	Fingerprint string            `json:"fingerprint"`
	Files       map[string]string `json:"files"`
}

type vectorCase struct {
	Name   string                        `json:"name"`
	Create protocol.CreateSessionRequest `json:"create"`
	Steps  []vectorStep                  `json:"steps"`
}

type vectorStep struct {
	Operation string `json:"operation"`

	Observe *protocol.ObserveRequest   `json:"observe,omitempty"`
	Propose *protocol.ProposeRequest   `json:"propose,omitempty"`
	Commit  *protocol.CommitRequest    `json:"commit,omitempty"`
	Due     *protocol.DueAgentsRequest `json:"due,omitempty"`

	ExpectRevision              uint64              `json:"expect_revision,omitempty"`
	ExpectActionID              string              `json:"expect_action_id,omitempty"`
	ExpectStance                string              `json:"expect_stance,omitempty"`
	ExpectPolicySource          string              `json:"expect_policy_source,omitempty"`
	ExpectBoundaryID            string              `json:"expect_boundary_id,omitempty"`
	ExpectErrorCode             string              `json:"expect_error_code,omitempty"`
	ExpectDueActorIDs           []string            `json:"expect_due_actor_ids,omitempty"`
	ExpectActorMemoryCounts     map[string]int      `json:"expect_actor_memory_counts,omitempty"`
	ExpectActorBeliefPredicates map[string][]string `json:"expect_actor_belief_predicates,omitempty"`
	ExpectRecentActionIDs       map[string][]string `json:"expect_recent_action_ids,omitempty"`
}

func TestAIGalgameCompatibilityVectors(t *testing.T) {
	payload, err := os.ReadFile("ai-galgame/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors vectorFile
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&vectors); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatal("compatibility vectors must contain exactly one JSON value")
	}
	if vectors.SchemaVersion != 1 || vectors.Source.Repository != "sunrioa/ai-galgame" || len(vectors.Cases) < 2 {
		t.Fatalf("invalid compatibility header: %+v", vectors)
	}
	assertSourceFingerprint(t, vectors.Source)
	for _, testCase := range vectors.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			runVectorCase(t, vectors.Source, testCase)
		})
	}
}

func runVectorCase(t *testing.T, source vectorSource, testCase vectorCase) {
	t.Helper()
	if testCase.Create.Binding.ContentID != source.PackID ||
		testCase.Create.Binding.ContentVersion != source.Version ||
		testCase.Create.Binding.ContentHash != source.Fingerprint {
		t.Fatal("case binding does not match source manifest")
	}
	if !protocol.HasFeature(testCase.Create.Features, protocol.FeatureMemoryArchive) ||
		!protocol.HasFeature(testCase.Create.Features, protocol.FeatureBeliefConflicts) {
		t.Fatal("ai-galgame vectors must enable the deployed cognition features")
	}
	engine, err := rinruntime.Open(store.NewMemory(), policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := engine.CreateSession(testCase.Create)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 {
		t.Fatalf("create revision=%d", created.Revision)
	}
	lastProposalID := ""
	for index := range testCase.Steps {
		step := testCase.Steps[index]
		switch step.Operation {
		case "observe":
			if step.Observe == nil {
				t.Fatal("observe step is missing request")
			}
			result, err := engine.Observe(*step.Observe)
			if err != nil {
				t.Fatal(err)
			}
			assertRevision(t, result.Revision, step.ExpectRevision)
		case "propose":
			if step.Propose == nil {
				t.Fatal("propose step is missing request")
			}
			proposal, _, err := engine.Propose(context.Background(), *step.Propose)
			if step.ExpectErrorCode != "" {
				if rinruntime.ErrorCode(err) != step.ExpectErrorCode {
					t.Fatalf("proposal error=%v code=%s", err, rinruntime.ErrorCode(err))
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			lastProposalID = proposal.ID
			if proposal.Action.ID != step.ExpectActionID || proposal.Stance != step.ExpectStance || proposal.PolicySource != step.ExpectPolicySource {
				t.Fatalf("unexpected proposal: %+v", proposal)
			}
			if proposal.BoundaryID != step.ExpectBoundaryID {
				t.Fatalf("proposal boundary_id=%q want=%q", proposal.BoundaryID, step.ExpectBoundaryID)
			}
		case "commit":
			if step.Commit == nil {
				t.Fatal("commit step is missing request")
			}
			request := *step.Commit
			if request.ProposalID == "$last" {
				request.ProposalID = lastProposalID
			}
			result, err := engine.Commit(request)
			if err != nil {
				t.Fatal(err)
			}
			assertRevision(t, result.Revision, step.ExpectRevision)
		case "due":
			if step.Due == nil {
				t.Fatal("due step is missing request")
			}
			result, err := engine.DueAgents(*step.Due)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(result.Agents))
			for _, actor := range result.Agents {
				ids = append(ids, actor.ActorID)
			}
			if !equalStrings(ids, step.ExpectDueActorIDs) {
				t.Fatalf("due actors=%v want=%v", ids, step.ExpectDueActorIDs)
			}
		case "assert_state":
			state, err := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: testCase.Create.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			assertState(t, state, step)
		default:
			t.Fatalf("unsupported vector operation %q", step.Operation)
		}
	}
}

func assertSourceFingerprint(t *testing.T, source vectorSource) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"pack_id": source.PackID,
		"version": source.Version,
		"files":   source.Files,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != source.Fingerprint {
		t.Fatalf("source fingerprint mismatch: %s", source.Fingerprint)
	}
}

func assertState(t *testing.T, state protocol.SessionState, step vectorStep) {
	t.Helper()
	for actorID, count := range step.ExpectActorMemoryCounts {
		actor := requireActor(t, state, actorID)
		if len(actor.Memories) != count {
			t.Fatalf("actor %s memories=%d want=%d", actorID, len(actor.Memories), count)
		}
	}
	for actorID, expected := range step.ExpectActorBeliefPredicates {
		actor := requireActor(t, state, actorID)
		actual := make([]string, 0, len(actor.Beliefs))
		for _, fact := range actor.Beliefs {
			actual = append(actual, fact.Predicate)
		}
		sort.Strings(actual)
		sort.Strings(expected)
		if !equalStrings(actual, expected) {
			t.Fatalf("actor %s beliefs=%v want=%v", actorID, actual, expected)
		}
	}
	for actorID, expected := range step.ExpectRecentActionIDs {
		actor := requireActor(t, state, actorID)
		actual := make([]string, 0, len(actor.RecentActions))
		for _, proposal := range actor.RecentActions {
			actual = append(actual, proposal.Action.ID)
		}
		if !equalStrings(actual, expected) {
			t.Fatalf("actor %s recent actions=%v want=%v", actorID, actual, expected)
		}
	}
}

func requireActor(t *testing.T, state protocol.SessionState, actorID string) protocol.ActorState {
	t.Helper()
	actor, exists := state.Actors[actorID]
	if !exists {
		t.Fatalf("actor %s is missing", actorID)
	}
	return actor
}

func assertRevision(t *testing.T, actual, expected uint64) {
	t.Helper()
	if expected != 0 && actual != expected {
		t.Fatalf("revision=%d want=%d", actual, expected)
	}
}

func equalStrings(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
