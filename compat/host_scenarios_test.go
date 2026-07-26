package compat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHostScenarioContractHasExecutableEvidence(t *testing.T) {
	payload, err := os.ReadFile("../conformance/host-scenarios/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion int    `json:"schema_version"`
		Contract      string `json:"contract"`
		Scenarios     []struct {
			ID          string   `json:"id"`
			Requirement string   `json:"requirement"`
			Evidence    []string `json:"evidence"`
		} `json:"scenarios"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 1 ||
		contract.Contract != "rin.host-scenarios/v1" ||
		len(contract.Scenarios) < 5 {
		t.Fatalf("invalid Host scenario contract: %+v", contract)
	}
	seen := make(map[string]struct{}, len(contract.Scenarios))
	for _, scenario := range contract.Scenarios {
		if scenario.ID == "" || scenario.Requirement == "" ||
			len(scenario.Evidence) == 0 {
			t.Fatalf("incomplete Host scenario: %+v", scenario)
		}
		if _, exists := seen[scenario.ID]; exists {
			t.Fatalf("duplicate Host scenario %q", scenario.ID)
		}
		seen[scenario.ID] = struct{}{}
		for _, evidence := range scenario.Evidence {
			path := filepath.Join("..", filepath.FromSlash(evidence))
			source, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("%s evidence %s: %v", scenario.ID, evidence, readErr)
			}
			if !bytes.Contains(source, []byte(scenario.ID)) {
				t.Errorf("%s is not named by evidence %s", scenario.ID, evidence)
			}
		}
	}
}
