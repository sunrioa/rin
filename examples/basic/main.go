// Command basic is a deliberately small development quickstart.
//
// Production games should use examples/recovery or a priority SDK because
// stable identities, Proposal Attempts, applied markers, and the Outcome
// Outbox must survive process loss.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sunrioa/rin/protocol"
)

const maxResponseBytes = 1 << 20

type quickClient struct {
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
	client, err := newQuickClient(*address, os.Getenv("RIN_TOKEN"))
	must(err)
	must(runQuickstart(client, os.Stdout, time.Now().UTC().UnixNano()))
}

func newQuickClient(address, token string) (*quickClient, error) {
	parsed, err := url.Parse(strings.TrimRight(address, "/"))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("invalid Rin URL")
	}
	loopback := parsed.Hostname() == "localhost" ||
		net.ParseIP(parsed.Hostname()).IsLoopback()
	if parsed.Scheme != "https" && !(loopback && parsed.Scheme == "http") {
		return nil, errors.New("remote Rin requires HTTPS")
	}
	if !loopback && token == "" {
		return nil, errors.New("remote Rin requires RIN_TOKEN")
	}
	return &quickClient{
		baseURL: strings.TrimRight(address, "/"),
		token:   token,
		http:    &http.Client{Timeout: 5 * time.Second},
	}, nil
}

func runQuickstart(client *quickClient, output io.Writer, unique int64) error {
	suffix := fmt.Sprintf("%d", unique)
	sessionID := "quick.session." + suffix
	create := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "quick.create." + suffix,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID:         "rin-quickstart",
			ContentID:      "base",
			ContentVersion: "1",
			ContentHash:    "quickstart-only",
		},
		Actors: []protocol.ActorSeed{{
			ID:              "npc.mira",
			Kind:            "npc",
			DisplayName:     "Mira",
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
	var created protocol.MutationResult
	if err := client.post("/v2/session/create", create, &created); err != nil {
		return fmt.Errorf("create Session: %w", err)
	}

	observe := protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "quick.observe." + suffix,
		EventID:         "quick.event." + suffix,
		Tick:            1,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "player",
		Kind:            "dialogue",
		Summary:         "The player greeted Mira.",
		Importance:      2,
		Epoch: protocol.Epoch{
			SessionID: sessionID,
			WorldID:   "world.quickstart",
			Host:      1,
			World:     1,
			Timeline:  1,
		},
		ObservationSeq: 1,
	}
	var observed protocol.MutationResult
	if err := client.post("/v2/session/observe", observe, &observed); err != nil {
		return fmt.Errorf("observe event: %w", err)
	}
	_, err := fmt.Fprintf(
		output,
		"created %s at revision %d; observed revision %d\n",
		sessionID,
		created.Revision,
		observed.Revision,
	)
	return err
}

func (client *quickClient) post(path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(
		http.MethodPost,
		client.baseURL+path,
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if client.token != "" {
		request.Header.Set("Authorization", "Bearer "+client.token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("Rin response exceeded 1 MiB")
	}
	var decoded envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("Rin returned invalid JSON")
	}
	if response.StatusCode != http.StatusOK || !decoded.OK {
		if decoded.Error != nil {
			return fmt.Errorf("%s: %s", decoded.Error.Code, decoded.Error.Message)
		}
		return fmt.Errorf("Rin returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(decoded.Data, output); err != nil {
		return errors.New("Rin returned invalid response data")
	}
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "rin quickstart:", err)
		os.Exit(1)
	}
}
