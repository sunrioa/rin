package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/timeline"
)

const defaultTaskTimelineURL = "http://127.0.0.1:7375"

type taskTimelineOptions struct {
	taskID     string
	controlURL string
	follow     bool
	json       bool
	limit      uint32
	wait       time.Duration
}

func runTasks(
	ctx context.Context,
	arguments []string,
	output io.Writer,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
) error {
	if len(arguments) == 0 || arguments[0] == "help" ||
		arguments[0] == "-h" || arguments[0] == "--help" {
		return writeTasksHelp(output)
	}
	if arguments[0] != "timeline" {
		return fmt.Errorf("unsupported tasks command %q", arguments[0])
	}
	options, err := parseTaskTimelineOptions(arguments[1:], errorOutput, lookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	token, _ := lookupEnv("RIN_CONTROL_TOKEN")
	client, err := controlplane.NewHTTPClient(options.controlURL, token)
	if err != nil {
		return err
	}
	return streamTaskTimeline(ctx, client, options, output)
}

func parseTaskTimelineOptions(
	arguments []string,
	errorOutput io.Writer,
	lookupEnv func(string) (string, bool),
) (taskTimelineOptions, error) {
	controlURL, _ := lookupEnv("RIN_CONTROL_URL")
	if strings.TrimSpace(controlURL) == "" {
		controlURL = defaultTaskTimelineURL
	}
	flags := flag.NewFlagSet("rin tasks timeline", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	options := taskTimelineOptions{controlURL: controlURL, limit: timeline.DefaultLimit, wait: 25 * time.Second}
	flags.StringVar(&options.controlURL, "control-url", options.controlURL, "loopback rin-control base URL")
	flags.BoolVar(&options.follow, "follow", false, "wait for new timeline evidence")
	flags.BoolVar(&options.json, "json", false, "write one JSON page per line")
	limit := flags.Uint("limit", uint(options.limit), "events per page from 1 through 256")
	flags.DurationVar(&options.wait, "wait", options.wait, "follow wait from 0 through 25s")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), `Usage:
  rin tasks timeline <task-id> [options]

RIN_CONTROL_TOKEN supplies the local daemon token. Timeline output contains
public evidence and references, never prompts, credentials, or memory text.

Options:
`)
		flags.PrintDefaults()
	}

	// The documented command puts task-id before flags. Move that one positional
	// value behind flags so the standard parser can still enforce all options.
	reordered := make([]string, 0, len(arguments))
	taskID := ""
	for _, argument := range arguments {
		if !strings.HasPrefix(argument, "-") && taskID == "" {
			taskID = argument
			continue
		}
		reordered = append(reordered, argument)
	}
	if err := flags.Parse(reordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return taskTimelineOptions{}, flag.ErrHelp
		}
		return taskTimelineOptions{}, err
	}
	if flags.NArg() != 0 || strings.TrimSpace(taskID) == "" {
		return taskTimelineOptions{}, errors.New("timeline requires exactly one task-id")
	}
	if *limit == 0 || *limit > uint(timeline.MaximumLimit) {
		return taskTimelineOptions{}, fmt.Errorf("limit must be between 1 and %d", timeline.MaximumLimit)
	}
	if options.wait < 0 || options.wait > time.Duration(timeline.MaximumWaitMS)*time.Millisecond {
		return taskTimelineOptions{}, errors.New("wait must be between 0 and 25s")
	}
	if options.follow && options.wait == 0 {
		return taskTimelineOptions{}, errors.New("follow wait must be greater than zero")
	}
	options.taskID = taskID
	options.limit = uint32(*limit)
	return options, nil
}

func streamTaskTimeline(
	ctx context.Context,
	client taskTimelineClient,
	options taskTimelineOptions,
	output io.Writer,
) error {
	query := timeline.Query{TaskID: options.taskID, Limit: options.limit}
	page, err := client.GetTaskTimeline(ctx, query)
	if err != nil {
		return err
	}
	for {
		for {
			if err := writeTaskTimelinePage(output, page, options.json); err != nil {
				return err
			}
			query.AfterCursor = page.NextCursor
			if !page.More {
				break
			}
			page, err = client.GetTaskTimeline(ctx, query)
			if err != nil {
				return err
			}
		}
		if !options.follow {
			return nil
		}
		for {
			update, err := client.WaitTaskTimeline(ctx, timeline.WaitInput{
				TaskID: options.taskID, AfterCursor: query.AfterCursor, Limit: options.limit,
				WaitMillis: uint32(options.wait.Milliseconds()),
			})
			if err != nil {
				return err
			}
			if !update.Changed {
				continue
			}
			page = update.Timeline
			break
		}
	}
}

type taskTimelineClient interface {
	GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error)
	WaitTaskTimeline(context.Context, timeline.WaitInput) (timeline.Update, error)
}

func writeTaskTimelinePage(output io.Writer, page timeline.Page, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(page)
	}
	for _, event := range page.Events {
		when := time.UnixMilli(event.OccurredAtUnixMillis).Format(time.RFC3339)
		summary := event.PublicSummary
		if summary == "" {
			summary = event.ReasonCode
		}
		if _, err := fmt.Fprintf(
			output, "%s  %-28s  %s  %s\n",
			when, event.EventKind, event.Cursor, strconv.Quote(summary),
		); err != nil {
			return err
		}
	}
	return nil
}

func writeTasksHelp(output io.Writer) error {
	_, err := fmt.Fprint(output, `Usage:
  rin tasks timeline <task-id> [--follow] [--json]
`)
	return err
}
