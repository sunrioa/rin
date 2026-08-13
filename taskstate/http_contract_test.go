package taskstate_test

import (
	"bytes"
	"encoding/json"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/taskstate"
)

func TestTaskPlanOpenAPIAndFixturesMatchRuntime(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(rinapi.TaskPlanDocument(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" ||
		document["x-rin-example-fixtures"] != "task-plan-v1-fixtures.json" {
		t.Fatal("task plan OpenAPI metadata is invalid")
	}
	routes, err := rinapi.ParseTaskPlanRoutes()
	if err != nil || len(routes) != 7 {
		t.Fatalf("task plan routes = %#v, %v", routes, err)
	}
	var fixtures struct {
		ContractVersion string                    `json:"contract_version"`
		Create          taskstate.Draft           `json:"create"`
		Get             taskstate.GetPlanInput    `json:"get"`
		Wait            taskstate.WaitInput       `json:"wait"`
		Status          taskstate.StatusInput     `json:"status"`
		Transition      taskstate.TransitionInput `json:"transition"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rinapi.TaskPlanFixtures()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixtures); err != nil {
		t.Fatal(err)
	}
	if fixtures.ContractVersion != taskstate.SchemaVersion ||
		fixtures.Get.PlanID != fixtures.Create.PlanID ||
		fixtures.Wait.WaitMillis != 25_000 {
		t.Fatalf("task plan fixtures = %#v", fixtures)
	}
	if _, err := taskstate.NewPlan(fixtures.Create, 1); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
}
