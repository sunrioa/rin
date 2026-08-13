package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/timeline"
)

func TestParseTaskTimelineOptionsSupportsDocumentedOrder(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key == "RIN_CONTROL_URL" {
			return "http://localhost:9000", true
		}
		return "", false
	}
	options, err := parseTaskTimelineOptions([]string{
		"task.collect.logs", "--follow", "--json", "--limit", "32", "--wait", "5s",
	}, io.Discard, lookup)
	if err != nil || options.taskID != "task.collect.logs" || !options.follow ||
		!options.json || options.limit != 32 || options.wait != 5*time.Second ||
		options.controlURL != "http://localhost:9000" {
		t.Fatalf("options = %#v, %v", options, err)
	}
}

func TestParseTaskTimelineOptionsRejectsBusyFollow(t *testing.T) {
	_, err := parseTaskTimelineOptions(
		[]string{"task.collect.logs", "--follow", "--wait", "0"},
		io.Discard,
		func(string) (string, bool) { return "", false },
	)
	if err == nil || !strings.Contains(err.Error(), "greater than zero") {
		t.Fatalf("busy follow error = %v", err)
	}
}

func TestStreamTaskTimelinePaginatesWithoutRepeatingEvidence(t *testing.T) {
	client := &fakeTimelineClient{pages: []timeline.Page{
		timelinePage("task.cli", "tl1:1", true, "task.created"),
		timelinePage("task.cli", "tl1:2", false, "operation.succeeded"),
	}}
	var output bytes.Buffer
	err := streamTaskTimeline(context.Background(), client, taskTimelineOptions{
		taskID: "task.cli", limit: 1,
	}, &output)
	if err != nil || client.getCalls != 2 || client.waitCalls != 0 ||
		strings.Count(output.String(), "task.created") != 1 ||
		strings.Count(output.String(), "operation.succeeded") != 1 {
		t.Fatalf("output=%q client=%#v err=%v", output.String(), client, err)
	}
}

func TestStreamTaskTimelineFollowUsesWaitCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeTimelineClient{
		pages: []timeline.Page{timelinePage("task.cli.follow", "tl1:1", false, "task.created")},
		updates: []timeline.Update{{
			Timeline: timelinePage("task.cli.follow", "tl1:2", false, "operation.running"),
			Changed:  true,
		}},
		afterUpdate: cancel,
	}
	var output bytes.Buffer
	err := streamTaskTimeline(ctx, client, taskTimelineOptions{
		taskID: "task.cli.follow", limit: 64, follow: true, wait: time.Second,
	}, &output)
	if !errors.Is(err, context.Canceled) || client.waitCalls != 2 ||
		client.waitInputs[0].AfterCursor != "tl1:1" ||
		client.waitInputs[1].AfterCursor != "tl1:2" {
		t.Fatalf("output=%q client=%#v err=%v", output.String(), client, err)
	}
}

type fakeTimelineClient struct {
	pages       []timeline.Page
	updates     []timeline.Update
	getCalls    int
	waitCalls   int
	waitInputs  []timeline.WaitInput
	afterUpdate context.CancelFunc
}

func (client *fakeTimelineClient) GetTaskTimeline(
	_ context.Context,
	_ timeline.Query,
) (timeline.Page, error) {
	if client.getCalls >= len(client.pages) {
		return timeline.Page{}, errors.New("unexpected timeline page request")
	}
	page := client.pages[client.getCalls]
	client.getCalls++
	return page, nil
}

func (client *fakeTimelineClient) WaitTaskTimeline(
	ctx context.Context,
	input timeline.WaitInput,
) (timeline.Update, error) {
	client.waitInputs = append(client.waitInputs, input)
	index := client.waitCalls
	client.waitCalls++
	if index < len(client.updates) {
		update := client.updates[index]
		if client.afterUpdate != nil {
			client.afterUpdate()
		}
		return update, nil
	}
	<-ctx.Done()
	return timeline.Update{}, ctx.Err()
}

func timelinePage(taskID, cursor string, more bool, kind string) timeline.Page {
	return timeline.Page{
		ContractVersion: timeline.ContractVersion, TaskID: taskID,
		Events: []timeline.Event{{
			EventID: taskID + ".event", Cursor: cursor, TaskID: taskID,
			EventKind: kind, OccurredAtUnixMillis: 1_000,
		}},
		NextCursor: cursor, More: more,
	}
}
