package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/timeline"
)

func TestTaskTimelineFixturesAreBoundedPublicEvidence(t *testing.T) {
	var fixtures struct {
		ContractVersion string        `json:"contract_version"`
		InternalAgent   timeline.Page `json:"internal_agent"`
		ExternalMCP     timeline.Page `json:"external_mcp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(TaskTimelineFixtures()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixtures); err != nil {
		t.Fatalf("decode task timeline fixtures: %v", err)
	}
	if fixtures.ContractVersion != timeline.ContractVersion {
		t.Fatalf("fixture contract = %q", fixtures.ContractVersion)
	}
	for name, page := range map[string]timeline.Page{
		"internal_agent": fixtures.InternalAgent,
		"external_mcp":   fixtures.ExternalMCP,
	} {
		if page.ContractVersion != timeline.ContractVersion || page.TaskID == "" ||
			len(page.Events) == 0 || page.NextCursor == "" || page.More || page.Truncated {
			t.Fatalf("%s fixture is incomplete: %#v", name, page)
		}
		last := uint64(0)
		records := make([]timeline.Record, 0, len(page.Events))
		for _, event := range page.Events {
			sequence, err := timeline.ParseCursor(event.Cursor)
			if err != nil || sequence <= last || event.TaskID != page.TaskID {
				t.Fatalf("%s event cursor is invalid: %#v, %v", name, event, err)
			}
			last = sequence
			records = append(records, timeline.Record{Sequence: sequence, Event: event})
		}
		if sequence, err := timeline.ParseCursor(page.NextCursor); err != nil || sequence != last {
			t.Fatalf("%s next cursor = %q, %v", name, page.NextCursor, err)
		}
		if _, err := timeline.BuildPage(timeline.Snapshot{
			TaskID: page.TaskID, Goal: page.Goal, GoalDigest: page.GoalDigest,
			Status: page.Status, LatestSequence: last, Records: records,
		}, timeline.Query{TaskID: page.TaskID, Limit: timeline.MaximumLimit}); err != nil {
			t.Fatalf("%s fixture violates timeline contract: %v", name, err)
		}
	}
	payload := strings.ToLower(string(TaskTimelineFixtures()))
	for _, forbidden := range []string{
		"api_key", "authorization", "bearer ", "prompt_text", "chain_of_thought",
		"memory_content", "skill_content",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("timeline fixture contains forbidden field %q", forbidden)
		}
	}
}

func TestTaskTimelineFixturesReturnsDefensiveCopy(t *testing.T) {
	first := TaskTimelineFixtures()
	first[0] = 'x'
	if bytes.Equal(first, TaskTimelineFixtures()) {
		t.Fatal("TaskTimelineFixtures returned shared storage")
	}
}
