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
		{
			path: "../sdk/lua/rin.lua",
			required: []string{
				"function rin.new_workflow",
				"function Workflow:resume",
				"function Workflow:apply_and_enqueue",
				"function Workflow:drain_outbox",
				"self.store:complete_attempt",
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

func TestPortableLuaWorkflowDoesNotOverclaimHostDurability(t *testing.T) {
	tests := []struct {
		path     string
		required []string
	}{
		{
			path: "../sdk/lua/README.md",
			required: []string{
				"`rin.new_workflow(client, store)`",
				"The SDK defines ordering and validation, not host durability.",
				"host-capability-profiles",
			},
		},
		{
			path: "../sdk/lua/README.zh-CN.md",
			required: []string{
				"`rin.new_workflow(client, store)`",
				"SDK 定义顺序和校验，不定义宿主持久性。",
				"host-capability-profiles",
			},
		},
	}
	for _, test := range tests {
		payload, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, fragment := range test.required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s is missing honest Lua capability wording %q", test.path, fragment)
			}
		}
	}
}
