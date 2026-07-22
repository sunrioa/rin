package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type client struct {
	baseURL string
	token   string
	http    *http.Client
}

type envelope struct {
	OK    bool                  `json:"ok"`
	Data  json.RawMessage       `json:"data"`
	Error *protocol.ErrorDetail `json:"error"`
}

func main() {
	address := flag.String("url", "http://127.0.0.1:7374", "Rin base URL")
	flag.Parse()
	c := client{baseURL: *address, token: os.Getenv("RIN_TOKEN"), http: &http.Client{Timeout: 5 * time.Second}}
	suffix := time.Now().UTC().Format("20060102T150405.000000000")
	sessionID := "example." + suffix

	create := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + suffix,
		SessionID:       sessionID,
		Binding:         protocol.Binding{GameID: "rin-example", ContentID: "base", ContentVersion: "1.0.0", ContentHash: "example-hash"},
		Seed:            42,
		Actors: []protocol.ActorSeed{{
			ID: "npc.mira", Kind: "npc", DisplayName: "Mira", Traits: []string{"curious", "careful"}, ThinkEveryTicks: 5, Enabled: true,
			Boundaries: []protocol.Boundary{{ID: "boundary.privacy", Description: "Do not reveal private letters.", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.connect", Description: "Build trust through specific actions.", Priority: 4, PreferredActions: []string{"talk"}, TargetProgress: 3, Status: "active"}},
		}},
	}
	must(c.post("/v1/session/create", create, &protocol.MutationResult{}))

	observe := protocol.ObserveRequest{
		ProtocolVersion: protocol.Version, SessionID: sessionID, RequestID: "observe." + suffix, EventID: "event.player-waited", Tick: 1,
		ObserverIDs: []string{"npc.mira"}, Source: "game", Kind: "dialogue", Summary: "The player waited instead of demanding an answer.",
		Quote: "Take your time.", Tags: []string{"conversation", "trust"}, Importance: 4,
	}
	must(c.post("/v1/session/observe", observe, &protocol.MutationResult{}))

	propose := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: sessionID, RequestID: "propose." + suffix, ActorID: "npc.mira", Tick: 2,
		Intent: "Choose how to respond to the player.", Tags: []string{"conversation"},
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "ask one honest question"},
			{ID: "refuse", Kind: "refuse", Description: "protect a private boundary"},
			{ID: "wait", Kind: "wait", Description: "stay silent for now"},
		},
	}
	var proposed protocol.ProposalResult
	must(c.post("/v1/agent/propose", propose, &proposed))
	fmt.Printf("proposal: %s (%s)\n", proposed.Proposal.Action.Description, proposed.Proposal.Rationale)

	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version, SessionID: sessionID, RequestID: "commit." + suffix,
		ProposalID: proposed.Proposal.ID, EventID: "event.mira-responded", Tick: 2, Accepted: true,
		Outcome: "Mira asked what the player wanted remembered.", Tags: []string{"conversation"},
	}
	must(c.post("/v1/action/commit", commit, &protocol.MutationResult{}))

	var state protocol.SessionState
	must(c.post("/v1/session/get", protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: sessionID}, &state))
	fmt.Printf("session %s revision=%d memories=%d next_think_tick=%d\n", state.SessionID, state.Revision, len(state.Actors["npc.mira"].Memories), state.Actors["npc.mira"].NextThinkTick)
}

func (c client) post(path string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	var result envelope
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !result.OK {
		if result.Error == nil {
			return errors.New("Rin returned an unspecified error")
		}
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	return json.Unmarshal(result.Data, output)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
