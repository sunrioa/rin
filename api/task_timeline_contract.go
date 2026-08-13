package api

import _ "embed"

//go:embed task-timeline-v1-fixtures.json
var taskTimelineV1Fixtures []byte

// TaskTimelineFixtures returns a defensive copy of the shared internal-Agent
// and external-MCP task timeline baseline.
func TaskTimelineFixtures() []byte {
	return append([]byte(nil), taskTimelineV1Fixtures...)
}
