package taskstate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestHTTPClientUsesSharedPlanContract(t *testing.T) {
	store, err := OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"), StoreConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := &storePlanClient{store: store}
	token := "0123456789abcdef0123456789abcdef"
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient("http://127.0.0.1:7375", token)
	if err != nil {
		t.Fatal(err)
	}
	client.client.Transport = handlerTransport{handler: handler}
	created, err := client.CreatePlan(context.Background(), httpTestDraft())
	if err != nil || created.Revision != 1 {
		t.Fatalf("created = %#v, %v", created, err)
	}
	fetched, err := client.GetPlan(context.Background(), created.PlanID)
	if err != nil || fetched.GoalDigest != created.GoalDigest {
		t.Fatalf("fetched = %#v, %v", fetched, err)
	}
	update, err := client.WaitPlan(context.Background(), WaitInput{
		PlanID: created.PlanID, AfterRevision: created.Revision, WaitMillis: 0,
	})
	if err != nil || update.Changed || update.Plan.Revision != created.Revision {
		t.Fatalf("update = %#v, %v", update, err)
	}
	_, err = client.RequestTransition(context.Background(), TransitionInput{
		PlanID: created.PlanID, ExpectedRevision: created.Revision,
		ConditionID: "condition.collected", Kind: EvidenceOperationOutcome,
		EvidenceID: "operation.missing",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("forbidden transition error = %v", err)
	}
}

type storePlanClient struct {
	store *Store
}

func (client *storePlanClient) CreatePlan(ctx context.Context, input Draft) (PlanState, error) {
	return client.store.Create(ctx, input)
}

func (client *storePlanClient) GetPlan(ctx context.Context, planID string) (PlanState, error) {
	return client.store.Get(ctx, planID)
}

func (client *storePlanClient) WaitPlan(ctx context.Context, input WaitInput) (PlanUpdate, error) {
	return client.store.Wait(ctx, input)
}

func (client *storePlanClient) RevisePlan(ctx context.Context, input ReviseInput) (PlanState, error) {
	return client.store.Revise(ctx, input)
}

func (client *storePlanClient) SetPlanStatus(ctx context.Context, input StatusInput) (PlanState, error) {
	return client.store.SetStatus(ctx, input)
}

func (client *storePlanClient) RequestTransition(context.Context, TransitionInput) (PlanState, error) {
	return PlanState{}, ErrForbidden
}

func (client *storePlanClient) SubmitStepAction(context.Context, SubmitStepActionInput) (controlplane.OperationView, error) {
	return controlplane.OperationView{}, ErrForbidden
}

type handlerTransport struct {
	handler http.Handler
}

func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func httpTestDraft() Draft {
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
	}
	return Draft{
		PlanID: "plan.http", TaskID: "task.http", SessionID: "session.one",
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		ControllerID: "controller.one", ControllerSource: ControllerExternal,
		Goal: "Collect material.", PlanningMode: PlanningAuto,
		Steps: []StepDraft{{
			StepID: "step.collect", Title: "Collect", Objective: "Collect material.",
			SuccessConditions: []PlanCondition{{
				ConditionID: "condition.collected", Kind: EvidenceOperationOutcome,
				Summary: "The Host confirms collection.",
			}},
		}},
		BasedOnEpoch: epoch, BasedOnObservationSequence: 1,
	}
}

var _ http.RoundTripper = handlerTransport{}
