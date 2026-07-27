package compat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	requiredScenarios := []string{
		"authority_thread_nonblocking",
		"chance_transition_host_owned",
		"exact_outbox_retry",
		"idempotent_operation",
		"long_action_epoch_cancel",
		"private_observation_noninterference",
		"recovery_state_cleanup",
		"revoked_capability_rejection",
		"simultaneous_window_atomicity",
		"stale_epoch_rejection",
	}
	if contract.SchemaVersion != 1 ||
		contract.Contract != "rin.host-scenarios/v1" ||
		len(contract.Scenarios) != len(requiredScenarios) {
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
		evidenceSeen := make(map[string]struct{}, len(scenario.Evidence))
		for _, evidence := range scenario.Evidence {
			clean := filepath.ToSlash(filepath.Clean(evidence))
			if clean != evidence ||
				filepath.IsAbs(evidence) ||
				clean == ".." ||
				len(clean) > 3 && clean[:3] == "../" {
				t.Fatalf("%s evidence path is not repository-relative: %q", scenario.ID, evidence)
			}
			if _, duplicate := evidenceSeen[evidence]; duplicate {
				t.Fatalf("%s repeats evidence %q", scenario.ID, evidence)
			}
			evidenceSeen[evidence] = struct{}{}
			if !slices.Contains(
				[]string{".c", ".cs", ".gd", ".go", ".java", ".lua", ".py"},
				filepath.Ext(evidence),
			) {
				t.Fatalf("%s evidence is not executable test source: %q", scenario.ID, evidence)
			}
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
	for _, required := range requiredScenarios {
		if _, exists := seen[required]; !exists {
			t.Errorf("Host scenario contract is missing %q", required)
		}
	}
}
