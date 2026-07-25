package compat_test

import (
	"os"
	"strings"
	"testing"
)

func TestPrioritySDKsExposeCapabilityGatedWorkflowCoordinators(t *testing.T) {
	tests := []struct {
		path     string
		required []string
	}{
		{
			path: "../sdk/javascript/src/index.js",
			required: []string{
				"export class HostCapabilities",
				"export class WorkflowCoordinator",
				"resumePendingWork",
				"applyAndEnqueueOutcome",
				"host_capability_insufficient",
				"completeProposalAttempt",
			},
		},
		{
			path: "../sdk/csharp/Rin.Client/WorkflowCoordinator.cs",
			required: []string{
				"public sealed record PendingTurn",
				"public sealed class WorkflowCoordinator",
				"ResumePendingWorkAsync",
				"ApplyAndEnqueueOutcomeAsync",
				"CompleteAsync",
			},
		},
		{
			path: "../sdk/java/src/main/java/io/github/sunrioa/rin/WorkflowCoordinator.java",
			required: []string{
				"public final class WorkflowCoordinator",
				"resumePendingWork",
				"applyAndEnqueueOutcome",
				"pendingTurn.operationId()",
				"completePendingTurn",
			},
		},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range test.required {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing workflow contract %q", test.path, fragment)
			}
		}
	}
}

func TestPortableLuaSDKDoesNotOverclaimWorkflowDurability(t *testing.T) {
	for _, path := range []string{
		"../sdk/lua/README.md",
		"../sdk/lua/README.zh-CN.md",
	} {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, fragment := range []string{
			"`advisory`",
			"host-capability-profiles",
		} {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing honest Lua capability wording %q", path, fragment)
			}
		}
	}
}
