package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func TestOutcomeStoreRetainsExactRequestAndDoesNotReapply(t *testing.T) {
	t.Parallel()

	var requestIDs []string
	attempts := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var commit protocol.CommitRequest
		if err := json.NewDecoder(request.Body).Decode(&commit); err != nil {
			return nil, err
		}
		requestIDs = append(requestIDs, commit.RequestID)
		attempts++
		body := `{"ok":true,"data":{"session_id":"session.example","revision":1,"duplicate":false}}`
		if attempts == 1 {
			body = `{"ok":false,"error":{"code":"temporary","message":"retry"}}`
		}
		return jsonResponse(body), nil
	})

	store := newGameOutcomeStore()
	store.currentTick = func() int64 { return 17 }
	operationID := "turn.example.1"
	action := protocol.ActionSpec{ID: "talk"}
	firstPlan := planGameAction(action)
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.example",
		RequestID:       "commit." + operationID,
		ProposalID:      "proposal.example",
		EventID:         "outcome." + operationID,
		Accepted:        firstPlan.accepted,
		Outcome:         firstPlan.outcome,
	}
	report := newCommitReport(operationID, "npc.mira", commit)
	transactionCalls := 0
	store.authoritativeTransaction = func(mutate func(*gameTransaction) error) error {
		transactionCalls++
		err := runInMemoryGameTransaction(mutate)
		if err == nil && (len(store.applied) != 1 || len(store.pending) != 1) {
			t.Fatal("authoritative transaction did not publish marker and Outbox together")
		}
		return err
	}
	applyCalls := 0
	first, err := store.applyAndEnqueue(
		operationID,
		firstPlan,
		report,
		func(*gameTransaction) error {
			applyCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("apply and enqueue: %v", err)
	}
	second, err := store.applyAndEnqueue(
		operationID,
		planGameAction(protocol.ActionSpec{ID: "unknown"}),
		pendingReport{},
		func(*gameTransaction) error {
			applyCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("re-enter apply and enqueue: %v", err)
	}
	if first != second {
		t.Fatalf("re-entered operation was applied again: first=%+v second=%+v", first, second)
	}
	if transactionCalls != 1 || applyCalls != 1 {
		t.Fatalf("transaction calls=%d apply calls=%d, want 1 each", transactionCalls, applyCalls)
	}
	pending := store.pending[operationID]
	if pending.commit.Tick != 17 || pending.fallback.Tick != 17 {
		t.Fatalf("occurrence tick commit=%d fallback=%d, want 17", pending.commit.Tick, pending.fallback.Tick)
	}
	c := client{
		baseURL: "http://rin.example",
		http:    &http.Client{Timeout: time.Second, Transport: transport},
	}

	if err := store.flush(&c); err == nil {
		t.Fatal("first flush succeeded, want simulated report failure")
	}
	if store.pending[operationID].kind != "commit" {
		t.Fatal("temporary failure converted Commit instead of retaining it")
	}
	if err := store.flush(&c); err != nil {
		t.Fatalf("retry flush: %v", err)
	}
	if len(store.pending) != 0 {
		t.Fatalf("pending count after acknowledgement = %d, want 0", len(store.pending))
	}
	if len(requestIDs) != 2 || requestIDs[0] != commit.RequestID || requestIDs[1] != commit.RequestID {
		t.Fatalf("retry request IDs = %v, want two copies of %q", requestIDs, commit.RequestID)
	}
}

func TestAuthoritativeTransactionFailureDoesNotApplyOrEnqueue(t *testing.T) {
	t.Parallel()

	store := newGameOutcomeStore()
	store.authoritativeTransaction = func(func(*gameTransaction) error) error {
		return errors.New("transaction unavailable")
	}
	effect := false
	_, err := store.applyAndEnqueue(
		"turn.failed.1",
		appliedOutcome{accepted: true, outcome: "planned"},
		newCommitReport(
			"turn.failed.1",
			"npc.mira",
			protocol.CommitRequest{RequestID: "commit.turn.failed.1"},
		),
		func(*gameTransaction) error {
			effect = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("applyAndEnqueue succeeded, want transaction error")
	}
	if effect || len(store.applied) != 0 || len(store.pending) != 0 {
		t.Fatalf(
			"failed transaction leaked state: effect=%t applied=%d pending=%d",
			effect,
			len(store.applied),
			len(store.pending),
		)
	}
}

func TestEffectPanicRollsBackEffectMarkerAndOutbox(t *testing.T) {
	t.Parallel()

	store := newGameOutcomeStore()
	effect := false
	_, err := store.applyAndEnqueue(
		"turn.panic.1",
		appliedOutcome{accepted: true, outcome: "planned"},
		newCommitReport(
			"turn.panic.1",
			"npc.mira",
			protocol.CommitRequest{RequestID: "commit.turn.panic.1"},
		),
		func(tx *gameTransaction) error {
			tx.onRollback(func() { effect = false })
			effect = true
			panic("effect failed")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("applyAndEnqueue error = %v, want recovered effect panic", err)
	}
	if effect || len(store.applied) != 0 || len(store.pending) != 0 {
		t.Fatalf(
			"effect panic leaked state: effect=%t applied=%d pending=%d",
			effect,
			len(store.applied),
			len(store.pending),
		)
	}
}

func TestIrrecoverableCommitAtomicallyConvertsToSafeObserve(t *testing.T) {
	t.Parallel()

	var paths []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case "/v1/action/commit":
			return jsonResponse(
				`{"ok":false,"error":{"code":"unknown_proposal","message":"gone"}}`,
			), nil
		case "/v1/session/observe":
			var observe protocol.ObserveRequest
			if err := json.NewDecoder(request.Body).Decode(&observe); err != nil {
				return nil, err
			}
			if observe.EventID != "outcome.turn.1" || observe.Tick != 23 {
				t.Errorf("Observe occurrence = %q@%d, want outcome.turn.1@23", observe.EventID, observe.Tick)
			}
			if len(observe.Facts) != 0 {
				t.Errorf("degraded Observe contains unsafe facts: %+v", observe.Facts)
			}
			return jsonResponse(`{"ok":true,"data":{"session_id":"session.example","revision":4,"duplicate":false}}`), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})
	store := newGameOutcomeStore()
	store.currentTick = func() int64 { return 23 }
	conversions := 0
	store.persistReportConversion = func(operationID string, replacement pendingReport) error {
		conversions++
		if operationID != "turn.1" || replacement.kind != "observe" {
			t.Fatalf("conversion = %q/%q", operationID, replacement.kind)
		}
		if store.pending[operationID].kind != "commit" {
			t.Fatal("in-memory Commit changed before durable conversion succeeded")
		}
		return nil
	}
	_, err := store.applyAndEnqueue(
		"turn.1",
		appliedOutcome{accepted: true, outcome: "applied"},
		newCommitReport("turn.1", "npc.mira", protocol.CommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.example",
			RequestID:       "commit.turn.1",
			ProposalID:      "proposal.1",
			EventID:         "outcome.turn.1",
			Accepted:        true,
			Outcome:         "applied",
			GoalUpdates: []protocol.GoalUpdate{{
				GoalID: "goal.connect", ProgressDelta: 1,
			}},
		}),
		func(*gameTransaction) error { return nil },
	)
	if err != nil {
		t.Fatalf("apply and enqueue: %v", err)
	}
	c := client{
		baseURL: "http://rin.example",
		http:    &http.Client{Timeout: time.Second, Transport: transport},
	}
	if err := store.flush(&c); err != nil {
		t.Fatalf("flush converted report: %v", err)
	}
	if conversions != 1 || len(store.pending) != 0 {
		t.Fatalf("conversions=%d pending=%d, want 1/0", conversions, len(store.pending))
	}
	if strings.Join(paths, ",") != "/v1/action/commit,/v1/session/observe" {
		t.Fatalf("report paths = %v", paths)
	}
}

func TestAcknowledgementMustPersistBeforeEviction(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":true,"data":{"session_id":"session.example","revision":1,"duplicate":false}}`), nil
	})
	store := newGameOutcomeStore()
	store.pending["turn.ack.1"] = pendingReport{
		kind: "observe",
		observe: protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.example",
			RequestID:       "reconcile.turn.ack.1",
		},
	}
	store.persistReportAck = func(string) error { return errors.New("disk full") }
	c := client{
		baseURL: "http://rin.example",
		http:    &http.Client{Timeout: time.Second, Transport: transport},
	}
	if err := store.flush(&c); err == nil {
		t.Fatal("flush succeeded despite durable acknowledgement failure")
	}
	if _, ok := store.pending["turn.ack.1"]; !ok {
		t.Fatal("report evicted before durable acknowledgement")
	}
}

func TestMutationSuccessRequiresMatchingSessionAndRetainsOutbox(t *testing.T) {
	t.Parallel()

	const sessionID = "session.expected"
	for _, endpoint := range []struct {
		name   string
		report pendingReport
	}{
		{
			name: "Commit",
			report: pendingReport{
				kind: "commit",
				commit: protocol.CommitRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "commit.turn.identity.1",
				},
			},
		},
		{
			name: "Observe",
			report: pendingReport{
				kind: "observe",
				observe: protocol.ObserveRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "observe.turn.identity.1",
				},
			},
		},
	} {
		endpoint := endpoint
		for _, response := range []struct {
			name string
			body string
		}{
			{name: "null data", body: `{"ok":true,"data":null}`},
			{name: "empty data", body: `{"ok":true,"data":{}}`},
			{
				name: "wrong session",
				body: `{"ok":true,"data":{"session_id":"session.other","revision":1}}`,
			},
		} {
			response := response
			t.Run(endpoint.name+"/"+response.name, func(t *testing.T) {
				t.Parallel()
				const operationID = "turn.identity.1"
				store := newGameOutcomeStore()
				store.pending[operationID] = endpoint.report
				acknowledgements := 0
				store.persistReportAck = func(string) error {
					acknowledgements++
					return nil
				}
				c := client{
					baseURL: "http://rin.example",
					http: &http.Client{
						Timeout: time.Second,
						Transport: roundTripFunc(
							func(*http.Request) (*http.Response, error) {
								return jsonResponse(response.body), nil
							},
						),
					},
				}
				err := store.flush(&c)
				var apiErr *apiError
				if !errors.As(err, &apiErr) ||
					apiErr.Code != "invalid_response" ||
					apiErr.Status != http.StatusOK {
					t.Fatalf("flush error = %v, want 2xx invalid_response", err)
				}
				if _, retained := store.pending[operationID]; !retained {
					t.Fatal("invalid success response evicted the exact Outbox report")
				}
				if acknowledgements != 0 {
					t.Fatalf(
						"invalid success response persisted %d acknowledgements",
						acknowledgements,
					)
				}
			})
		}
	}
}

func TestSessionGetSuccessRequiresMatchingSessionAndRetainsAttempt(t *testing.T) {
	t.Parallel()

	for _, response := range []struct {
		name string
		body string
	}{
		{name: "null data", body: `{"ok":true,"data":null}`},
		{name: "empty data", body: `{"ok":true,"data":{}}`},
		{
			name: "wrong session",
			body: `{"ok":true,"data":{"session_id":"session.other","revision":1}}`,
		},
	} {
		response := response
		t.Run(response.name, func(t *testing.T) {
			t.Parallel()
			store := newGameOutcomeStore()
			store.runID = time.Unix(0, 0).UTC().Format(exampleRunIDLayout)
			store.create = exampleCreateRequest(store.runID)
			attempt, err := store.retainProposalAttempt()
			if err != nil {
				t.Fatalf("retain Proposal Attempt: %v", err)
			}
			proposal := protocol.ActionProposal{
				ID:              "proposal.identity.1",
				SessionID:       attempt.Request.SessionID,
				RequestID:       attempt.Request.RequestID,
				ActorID:         attempt.Request.ActorID,
				Tick:            attempt.Request.Tick,
				CreatedRevision: 1,
				Action:          attempt.Request.CandidateActions[0],
				Status:          "pending",
			}
			applyCalls := 0
			store.applyEffect = func(*gameTransaction, protocol.ActionSpec) {
				applyCalls++
			}
			c := client{
				baseURL: "http://rin.example",
				http: &http.Client{
					Timeout: time.Second,
					Transport: roundTripFunc(
						func(request *http.Request) (*http.Response, error) {
							switch request.URL.Path {
							case "/v1/agent/propose":
								return dataResponse(t, protocol.ProposalResult{
									Proposal: proposal,
								}), nil
							case "/v1/session/get":
								return jsonResponse(response.body), nil
							default:
								t.Fatalf("unexpected request %s", request.URL.Path)
								return nil, nil
							}
						},
					),
				},
			}
			err = store.resolveProposalAttempt(&c, attempt)
			var apiErr *apiError
			if !errors.As(err, &apiErr) ||
				apiErr.Code != "invalid_response" ||
				apiErr.Status != http.StatusOK {
				t.Fatalf("resolve error = %v, want 2xx invalid_response", err)
			}
			if store.proposalAttempt != attempt || !attempt.Submitted {
				t.Fatal("invalid Session GET success abandoned the exact submitted Attempt")
			}
			if applyCalls != 0 || len(store.applied) != 0 || len(store.pending) != 0 {
				t.Fatalf(
					"invalid Session GET generated authority: calls=%d applied=%d pending=%d",
					applyCalls,
					len(store.applied),
					len(store.pending),
				)
			}
		})
	}
}

func TestPostRenameErrorsKeepMemoryAlignedWithReplacedState(t *testing.T) {
	t.Parallel()

	t.Run("authoritative transaction does not roll back", func(t *testing.T) {
		value := false
		err := runPersistedGameTransaction(
			func(tx *gameTransaction) error {
				tx.onRollback(func() { value = false })
				value = true
				return nil
			},
			func() error {
				return &stateFileReplacedError{err: errors.New("directory fsync")}
			},
		)
		if err == nil || !stateFileWasReplaced(err) {
			t.Fatalf("transaction error = %v, want post-Rename durability error", err)
		}
		if !value {
			t.Fatal("post-Rename error rolled memory back behind replaced disk state")
		}
	})

	t.Run("pre-Rename persistence error rolls back", func(t *testing.T) {
		value := false
		err := runPersistedGameTransaction(
			func(tx *gameTransaction) error {
				tx.onRollback(func() { value = false })
				value = true
				return nil
			},
			func() error { return errors.New("temporary file write") },
		)
		if err == nil {
			t.Fatal("transaction succeeded despite pre-Rename persistence error")
		}
		if value {
			t.Fatal("pre-Rename persistence error did not roll memory back")
		}
	})

	t.Run("durable store blocks every later operation", func(t *testing.T) {
		store := newGameOutcomeStore()
		store.runID, store.create = newExampleRun(time.Now())
		store.authoritativeTransaction = func(
			mutate func(*gameTransaction) error,
		) error {
			return store.runPersistedMutation(mutate, func() error {
				return &stateFileReplacedError{err: errors.New("directory fsync")}
			})
		}
		if _, err := store.retainProposalAttempt(); err == nil ||
			!stateFileWasReplaced(err) {
			t.Fatalf("retain error = %v, want post-Rename durability error", err)
		}
		if store.proposalAttempt == nil || store.durabilityBlocked == nil {
			t.Fatal("post-Rename mutation did not retain state and close durability gate")
		}
		networkCalls := 0
		c := client{
			baseURL: "http://rin.example",
			http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
				func(*http.Request) (*http.Response, error) {
					networkCalls++
					return nil, errors.New("must not be reached")
				},
			)},
		}
		if err := store.runExampleInvocation(&c); err == nil ||
			!strings.Contains(err.Error(), "durability_unconfirmed") {
			t.Fatalf("blocked invocation error = %v", err)
		}
		if _, err := store.retainProposalAttempt(); err == nil ||
			!strings.Contains(err.Error(), "durability_unconfirmed") {
			t.Fatalf("blocked mutation error = %v", err)
		}
		if err := store.flush(&c); err == nil ||
			!strings.Contains(err.Error(), "durability_unconfirmed") {
			t.Fatalf("blocked flush error = %v", err)
		}
		if networkCalls != 0 {
			t.Fatalf("durability-blocked instance made %d network calls", networkCalls)
		}
	})

	t.Run("Outbox acknowledgement evicts aligned memory", func(t *testing.T) {
		store := newGameOutcomeStore()
		store.pending["turn.ack.1"] = pendingReport{
			kind: "observe",
			observe: protocol.ObserveRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       "session.example",
				RequestID:       "reconcile.turn.ack.1",
			},
		}
		store.persistReportAck = func(string) error {
			return &stateFileReplacedError{err: errors.New("directory fsync")}
		}
		networkCalls := 0
		c := client{
			baseURL: "http://rin.example",
			http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
				func(*http.Request) (*http.Response, error) {
					networkCalls++
					return jsonResponse(`{"ok":true,"data":{"session_id":"session.example","revision":1}}`), nil
				},
			)},
		}
		if err := store.flush(&c); err == nil || !stateFileWasReplaced(err) {
			t.Fatalf("flush error = %v, want post-Rename durability error", err)
		}
		if len(store.pending) != 0 {
			t.Fatal("post-Rename acknowledgement left memory behind replaced disk state")
		}
		if err := store.flush(&c); err == nil ||
			!strings.Contains(err.Error(), "durability_unconfirmed") {
			t.Fatalf("second flush error = %v, want closed durability gate", err)
		}
		if networkCalls != 1 {
			t.Fatalf("closed durability gate allowed %d network calls", networkCalls)
		}
	})

	t.Run("Commit conversion aligns memory", func(t *testing.T) {
		store := newGameOutcomeStore()
		store.pending["turn.convert.1"] = pendingReport{
			kind: "commit",
			commit: protocol.CommitRequest{
				RequestID: "commit.turn.convert.1",
			},
			fallback: protocol.ObserveRequest{
				RequestID: "reconcile.turn.convert.1",
			},
		}
		store.persistReportConversion = func(string, pendingReport) error {
			return &stateFileReplacedError{err: errors.New("directory fsync")}
		}
		c := client{
			baseURL: "http://rin.example",
			http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
				func(*http.Request) (*http.Response, error) {
					return apiErrorResponse(http.StatusNotFound, "unknown_proposal"), nil
				},
			)},
		}
		if err := store.flush(&c); err == nil || !stateFileWasReplaced(err) {
			t.Fatalf("flush error = %v, want post-Rename durability error", err)
		}
		if store.pending["turn.convert.1"].kind != "observe" {
			t.Fatal("post-Rename conversion left memory behind replaced disk state")
		}
		if store.durabilityBlocked == nil {
			t.Fatal("post-Rename conversion did not close durability gate")
		}
	})
}

func TestInjectedDirectorySyncErrorsReplaceStateAndBlockDurability(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		syncErr error
	}{
		{name: "EINVAL", syncErr: syscall.EINVAL},
		{name: "ENOTSUP", syncErr: syscall.ENOTSUP},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			statePath := filepath.Join(t.TempDir(), "game-state.json")
			store := newGameOutcomeStore()
			store.runID = time.Unix(0, 0).UTC().Format(exampleRunIDLayout)
			store.create = exampleCreateRequest(store.runID)
			syncCalls := 0
			store.authoritativeTransaction = func(
				mutate func(*gameTransaction) error,
			) error {
				return store.runPersistedMutation(mutate, func() error {
					return persistGameOutcomeStateWithDirectorySync(
						statePath,
						store.snapshot(),
						func(path string) error {
							syncCalls++
							if path != filepath.Dir(statePath) {
								t.Fatalf("unexpected sync path %q", path)
							}
							return test.syncErr
						},
					)
				})
			}

			_, err := store.retainProposalAttempt()
			if err == nil ||
				!stateFileWasReplaced(err) ||
				!errors.Is(err, test.syncErr) {
				t.Fatalf("retain error = %v, want post-Rename %v", err, test.syncErr)
			}
			if syncCalls != 1 {
				t.Fatalf("directory sync calls = %d, want 1", syncCalls)
			}
			if store.proposalAttempt == nil || store.durabilityBlocked == nil {
				t.Fatal("post-Rename sync error did not align memory and block durability")
			}
			if _, statErr := os.Stat(statePath); statErr != nil {
				t.Fatalf("Rename did not replace state before sync error: %v", statErr)
			}
			if _, err := store.retainProposalAttempt(); err == nil ||
				!strings.Contains(err.Error(), "durability_unconfirmed") {
				t.Fatalf("later mutation error = %v, want closed durability gate", err)
			}
			if syncCalls != 1 {
				t.Fatalf("blocked mutation performed another %d sync calls", syncCalls)
			}
		})
	}
}

func TestFirstStateDirectoryCreationSyncsEveryCreatedParent(t *testing.T) {
	t.Parallel()

	t.Run("syncs parent chain before final Rename directory", func(t *testing.T) {
		root := t.TempDir()
		stateDirectory := filepath.Join(root, "new-parent", "new-leaf")
		statePath := filepath.Join(stateDirectory, "game-state.json")
		var synced []string
		if err := persistGameOutcomeStateWithDirectorySync(
			statePath,
			persistedGameOutcomeState{Version: gameOutcomeStateVersion},
			func(path string) error {
				synced = append(synced, path)
				return nil
			},
		); err != nil {
			t.Fatalf("persist first state: %v", err)
		}
		want := []string{
			root,
			filepath.Join(root, "new-parent"),
			root,
			root,
			stateDirectory,
		}
		if !reflect.DeepEqual(synced, want) {
			t.Fatalf("directory sync order = %v, want %v", synced, want)
		}
	})

	t.Run("failed cleanup leaves a durable journal for reconstructed retry", func(t *testing.T) {
		root := t.TempDir()
		stateParent := filepath.Join(root, "new-parent")
		stateDirectory := filepath.Join(stateParent, "new-leaf")
		statePath := filepath.Join(stateDirectory, "game-state.json")
		blockerPath := filepath.Join(stateDirectory, "concurrent-entry")
		var firstSynced []string
		err := persistGameOutcomeStateWithDirectorySync(
			statePath,
			persistedGameOutcomeState{Version: gameOutcomeStateVersion},
			func(path string) error {
				firstSynced = append(firstSynced, path)
				if path == stateParent {
					if writeErr := os.WriteFile(
						blockerPath,
						[]byte("keep"),
						0o600,
					); writeErr != nil {
						t.Fatalf("create cleanup blocker: %v", writeErr)
					}
					return syscall.ENOTSUP
				}
				return nil
			},
		)
		if err == nil ||
			!errors.Is(err, syscall.ENOTSUP) ||
			!strings.Contains(err.Error(), "clean up newly created state directories") ||
			stateFileWasReplaced(err) {
			t.Fatalf(
				"parent sync/cleanup error = %v, want explicit pre-Rename ENOTSUP",
				err,
			)
		}
		if !reflect.DeepEqual(firstSynced, []string{root, stateParent}) {
			t.Fatalf(
				"first syncs = %v, want journal confirmation then %s",
				firstSynced,
				stateParent,
			)
		}
		if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("state file exists after parent sync failure: %v", statErr)
		}
		if _, statErr := os.Stat(blockerPath); statErr != nil {
			t.Fatalf("failed cleanup did not leave its non-empty directory: %v", statErr)
		}
		absoluteStatePath, err := filepath.Abs(statePath)
		if err != nil {
			t.Fatalf("absolute state path: %v", err)
		}
		journalPath := filepath.Join(
			root,
			stateDirectorySyncJournalName(absoluteStatePath),
		)
		if _, statErr := os.Stat(journalPath); statErr != nil {
			t.Fatalf("durable parent-sync journal was not retained: %v", statErr)
		}

		// This is a fresh call with no in-memory plan. It must rediscover the
		// journal from disk and replay the original chain before writing state.
		var retrySynced []string
		if err := persistGameOutcomeStateWithDirectorySync(
			statePath,
			persistedGameOutcomeState{Version: gameOutcomeStateVersion},
			func(path string) error {
				if len(retrySynced) < 2 {
					if _, statErr := os.Stat(statePath); !errors.Is(
						statErr,
						os.ErrNotExist,
					) {
						t.Fatalf(
							"state file appeared before pending parent syncs: %v",
							statErr,
						)
					}
				}
				retrySynced = append(retrySynced, path)
				return nil
			},
		); err != nil {
			t.Fatalf("same-path retry: %v", err)
		}
		wantRetry := []string{
			root,
			stateParent,
			root,
			root,
			stateDirectory,
		}
		if !reflect.DeepEqual(retrySynced, wantRetry) {
			t.Fatalf(
				"same-path retry syncs = %v, want original parent chain %v",
				retrySynced,
				wantRetry,
			)
		}
		if _, statErr := os.Stat(journalPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("completed parent-sync journal was not cleared: %v", statErr)
		}
	})

	t.Run("malformed and bounded journals fail closed", func(t *testing.T) {
		for _, test := range []struct {
			name string
			body func(string, string) []byte
		}{
			{
				name: "malformed",
				body: func(string, string) []byte {
					return []byte(`{"version":1,"state_path":`)
				},
			},
			{
				name: "oversized",
				body: func(string, string) []byte {
					return []byte(strings.Repeat(
						"x",
						maxStateDirectorySyncJournalBytes+1,
					))
				},
			},
			{
				name: "too many parents",
				body: func(statePath, root string) []byte {
					parents := make(
						[]string,
						maxStateDirectorySyncJournalParents+1,
					)
					for index := range parents {
						parents[index] = root
					}
					payload, err := json.Marshal(
						persistedStateDirectorySyncJournal{
							Version:   stateDirectorySyncJournalVersion,
							StatePath: statePath,
							Parents:   parents,
						},
					)
					if err != nil {
						t.Fatalf("encode oversized parent journal: %v", err)
					}
					return payload
				},
			},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				root := t.TempDir()
				statePath := filepath.Join(
					root,
					"new-parent",
					"new-leaf",
					"game-state.json",
				)
				absoluteStatePath, err := filepath.Abs(statePath)
				if err != nil {
					t.Fatalf("absolute state path: %v", err)
				}
				journalPath := filepath.Join(
					root,
					stateDirectorySyncJournalName(absoluteStatePath),
				)
				if err := os.WriteFile(
					journalPath,
					test.body(absoluteStatePath, root),
					0o600,
				); err != nil {
					t.Fatalf("write corrupt journal: %v", err)
				}
				syncCalls := 0
				err = persistGameOutcomeStateWithDirectorySync(
					statePath,
					persistedGameOutcomeState{
						Version: gameOutcomeStateVersion,
					},
					func(string) error {
						syncCalls++
						return nil
					},
				)
				if err == nil {
					t.Fatal("corrupt parent-sync journal did not fail closed")
				}
				if syncCalls != 0 {
					t.Fatalf("corrupt journal performed %d directory syncs", syncCalls)
				}
				if _, statErr := os.Stat(statePath); !errors.Is(
					statErr,
					os.ErrNotExist,
				) {
					t.Fatalf("corrupt journal allowed state write: %v", statErr)
				}
			})
		}
	})

	t.Run("unconfirmed journal never authorizes directory creation", func(t *testing.T) {
		root := t.TempDir()
		stateDirectory := filepath.Join(root, "new-parent", "new-leaf")
		statePath := filepath.Join(stateDirectory, "game-state.json")
		for attempt := 1; attempt <= 2; attempt++ {
			err := persistGameOutcomeStateWithDirectorySync(
				statePath,
				persistedGameOutcomeState{Version: gameOutcomeStateVersion},
				func(string) error {
					return syscall.EINVAL
				},
			)
			if err == nil ||
				!errors.Is(err, syscall.EINVAL) ||
				!strings.Contains(err.Error(), "confirm") {
				t.Fatalf("attempt %d journal confirmation error = %v", attempt, err)
			}
			if _, statErr := os.Stat(stateDirectory); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf(
					"attempt %d unconfirmed journal authorized MkdirAll: %v",
					attempt,
					statErr,
				)
			}
		}
	})
}

func TestDurableOutcomeStoreRestoresAndDrainsExactOutbox(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	store, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	store.currentTick = func() int64 { return 37 }
	attempt, err := store.retainProposalAttempt()
	if err != nil {
		t.Fatalf("retain Proposal Attempt: %v", err)
	}
	operationID := attempt.OperationID
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       store.create.SessionID,
		RequestID:       "commit." + operationID,
		ProposalID:      "proposal.restart.1",
		EventID:         "outcome." + operationID,
		Accepted:        true,
		Outcome:         "applied once",
		Tags:            []string{"conversation"},
	}
	applyCalls := 0
	_, err = store.applyAndEnqueueAttempt(
		operationID,
		appliedOutcome{accepted: true, outcome: commit.Outcome},
		newCommitReport(operationID, "npc.mira", commit),
		attempt,
		attempt.Request.Tick,
		func(*gameTransaction) error {
			applyCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("apply and durably enqueue: %v", err)
	}

	restored, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore durable store: %v", err)
	}
	pending, ok := restored.pending[operationID]
	if !ok {
		t.Fatal("restart lost the pending authoritative report")
	}
	if pending.kind != "commit" ||
		pending.commit.RequestID != commit.RequestID ||
		pending.commit.Tick != 37 ||
		pending.fallback.Tick != 37 {
		t.Fatalf("restored report changed: %+v", pending)
	}
	_, err = restored.applyAndEnqueue(
		operationID,
		appliedOutcome{accepted: false, outcome: "must not replace"},
		pendingReport{},
		func(*gameTransaction) error {
			applyCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("re-enter restored operation: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("restored operation reapplied %d times, want exactly once", applyCalls)
	}

	requests := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		var got protocol.CommitRequest
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			return nil, err
		}
		if got.RequestID != commit.RequestID || got.Tick != 37 {
			t.Fatalf("retried Commit changed: %+v", got)
		}
		if requests == 1 {
			return nil, errors.New("response lost")
		}
		return dataResponse(t, protocol.MutationResult{
			SessionID: commit.SessionID,
			Revision:  2,
			Duplicate: true,
		}), nil
	})
	c := client{
		baseURL: "http://rin.example",
		http:    &http.Client{Timeout: time.Second, Transport: transport},
	}
	if err := restored.flush(&c); err == nil {
		t.Fatal("ambiguous first flush succeeded")
	}
	afterFailure, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore after failed flush: %v", err)
	}
	if afterFailure.pending[operationID].commit.RequestID != commit.RequestID {
		t.Fatal("failed flush did not retain the exact Commit durably")
	}
	if err := afterFailure.flush(&c); err != nil {
		t.Fatalf("drain restored Outbox: %v", err)
	}
	afterAck, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore after acknowledgement: %v", err)
	}
	if len(afterAck.pending) != 0 {
		t.Fatalf("acknowledged Outbox restored %d reports, want 0", len(afterAck.pending))
	}
	if _, ok := afterAck.applied[operationID]; !ok {
		t.Fatal("acknowledgement removed the authoritative applied marker")
	}
}

func TestDurableOutcomeStoreFailsClosedOnCorruptState(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	if err := os.WriteFile(statePath, []byte(`{"version":1,"applied":`), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	if _, err := newDurableGameOutcomeStore(statePath); err == nil {
		t.Fatal("corrupt state was treated as a clean first run")
	}
}

func TestProposalFreshnessAndInvalidFallback(t *testing.T) {
	t.Parallel()

	proposal := protocol.ActionProposal{
		ID:              "proposal.1",
		SessionID:       "session.1",
		RequestID:       "propose.1",
		ActorID:         "npc.mira",
		Tick:            4,
		CreatedRevision: 7,
		Action:          protocol.ActionSpec{ID: "wait", Kind: "wait"},
		Status:          "pending",
	}
	state := protocol.SessionState{
		Revision:  7,
		Proposals: map[string]protocol.ActionProposal{proposal.ID: proposal},
	}
	if !proposalIsFresh(state, proposal) {
		t.Fatal("unchanged pending proposal reported stale")
	}
	state.Revision = 8
	if proposalIsFresh(state, proposal) {
		t.Fatal("non-arbitrated proposal remained fresh after revision changed")
	}
	proposal.BasedOnWorldRevision = 3
	state.Proposals[proposal.ID] = proposal
	state.WorldRevision = 3
	if !proposalIsFresh(state, proposal) {
		t.Fatal("arbitrated proposal did not use matching world revision")
	}
	forgedBase := proposal
	forgedBase.BasedOnWorldRevision = 4
	state.WorldRevision = 4
	if proposalIsFresh(state, forgedBase) {
		t.Fatal("response revision base overrode the server-retained Proposal base")
	}
	state.WorldRevision = 3
	forgedAction := proposal
	forgedAction.Action = protocol.ActionSpec{ID: "talk", Kind: "dialogue"}
	if proposalIsFresh(state, forgedAction) {
		t.Fatal("response action differed from the server-retained Proposal")
	}
	state.Proposals[proposal.ID] = protocol.ActionProposal{ID: proposal.ID, Status: "accepted"}
	if proposalIsFresh(state, proposal) {
		t.Fatal("resolved proposal reported fresh")
	}

	request := protocol.ProposeRequest{
		RequestID: "propose.1",
		CandidateActions: []protocol.ActionSpec{{
			ID: "wait", Kind: "wait",
		}},
	}
	if _, err := authoredFallback(request, "missing"); err == nil ||
		!strings.Contains(err.Error(), "invalid_fallback") {
		t.Fatalf("invalid fallback error = %v", err)
	}
}

func TestRetrySameRequestKeepsCreatePayloadStable(t *testing.T) {
	t.Parallel()

	var payloads []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, string(body))
		if len(payloads) == 1 {
			return nil, errors.New("response lost")
		}
		return jsonResponse(`{"ok":true,"data":{"revision":1,"duplicate":true}}`), nil
	})
	c := client{
		baseURL: "http://rin.example",
		http:    &http.Client{Timeout: time.Second, Transport: transport},
	}
	create := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.run.1",
		SessionID:       "session.run.1",
		Binding: protocol.Binding{
			GameID: "game", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Seed:     42,
		Features: []string{protocol.FeatureOutcomeReporting},
		Actors: []protocol.ActorSeed{{
			ID: "npc.mira", Kind: "npc", DisplayName: "Mira", Enabled: true,
		}},
	}
	err := retrySameRequest(2, func() error {
		return c.post("/v1/session/create", create, &protocol.MutationResult{})
	})
	if err != nil {
		t.Fatalf("retry create: %v", err)
	}
	if len(payloads) != 2 || payloads[0] != payloads[1] {
		t.Fatalf("create retry payloads differ: %q != %q", payloads[0], payloads[1])
	}
}

func TestColdFallbackRestartsWithExactCreateThenDrainsObserve(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	first, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	stableCreate := first.create
	first.currentTick = func() int64 { return 19 }
	firstClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(*http.Request) (*http.Response, error) {
				return nil, errors.New("create never reached Rin")
			},
		)},
	}
	if err := first.runExampleInvocation(&firstClient); err != nil {
		t.Fatalf("first-ever cold fallback: %v", err)
	}
	if first.proposalAttempt != nil ||
		len(first.applied) != 1 ||
		len(first.pending) != 1 {
		t.Fatalf(
			"cold fallback state attempt=%+v applied=%d pending=%d",
			first.proposalAttempt,
			len(first.applied),
			len(first.pending),
		)
	}
	var retainedObserve protocol.ObserveRequest
	for _, report := range first.pending {
		retainedObserve = report.observe
	}

	restarted, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restart durable store: %v", err)
	}
	var paths []string
	recoveryClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				paths = append(paths, request.URL.Path)
				switch request.URL.Path {
				case "/v1/session/create":
					var got protocol.CreateSessionRequest
					if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
						return nil, err
					}
					if !reflect.DeepEqual(got, stableCreate) {
						t.Fatalf("restarted Create changed: got=%+v want=%+v", got, stableCreate)
					}
					return dataResponse(t, protocol.MutationResult{
						SessionID: stableCreate.SessionID,
						Revision:  1,
					}), nil
				case "/v1/session/observe":
					var got protocol.ObserveRequest
					if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
						return nil, err
					}
					if !reflect.DeepEqual(got, retainedObserve) {
						t.Fatalf("restarted Observe changed: got=%+v want=%+v", got, retainedObserve)
					}
					return dataResponse(t, protocol.MutationResult{
						SessionID: stableCreate.SessionID,
						Revision:  2,
					}), nil
				default:
					t.Fatalf("unexpected recovery request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := restarted.runExampleInvocation(&recoveryClient); err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	if strings.Join(paths, ",") != "/v1/session/create,/v1/session/observe" {
		t.Fatalf("recovery order = %v, want Create then Observe", paths)
	}
	afterRecovery, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore after recovery: %v", err)
	}
	if afterRecovery.create.SessionID != stableCreate.SessionID ||
		len(afterRecovery.pending) != 0 {
		t.Fatalf(
			"recovery lost identity or retained Outbox: session=%q pending=%d",
			afterRecovery.create.SessionID,
			len(afterRecovery.pending),
		)
	}
}

func TestAmbiguousProposeRestartsExactAttemptAndAppliesOnce(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	first, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	var proposalPayloads [][]byte
	firstClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/session/create":
					return dataResponse(t, protocol.MutationResult{
						SessionID: first.create.SessionID,
						Revision:  1,
					}), nil
				case "/v1/agent/propose":
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					proposalPayloads = append(proposalPayloads, body)
					return nil, errors.New("response lost after durable Proposal")
				default:
					t.Fatalf("unexpected first-run request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := first.runExampleInvocation(&firstClient); err == nil ||
		!strings.Contains(err.Error(), "proposal_outcome_unknown") {
		t.Fatalf("ambiguous Proposal error = %v", err)
	}
	if first.proposalAttempt == nil || !first.proposalAttempt.Submitted ||
		len(first.applied) != 0 || len(first.pending) != 0 {
		t.Fatalf(
			"ambiguous Proposal was abandoned: attempt=%+v applied=%d pending=%d",
			first.proposalAttempt,
			len(first.applied),
			len(first.pending),
		)
	}

	restarted, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restart durable store: %v", err)
	}
	attempt := restarted.proposalAttempt
	action := attempt.Request.CandidateActions[0]
	proposal := protocol.ActionProposal{
		ID:              "proposal.recovered",
		SessionID:       attempt.Request.SessionID,
		RequestID:       attempt.Request.RequestID,
		ActorID:         attempt.Request.ActorID,
		Tick:            attempt.Request.Tick,
		CreatedRevision: 2,
		Action:          action,
		Status:          "pending",
	}
	state := protocol.SessionState{
		ProtocolVersion: protocol.Version,
		SessionID:       attempt.Request.SessionID,
		Revision:        proposal.CreatedRevision,
		Proposals: map[string]protocol.ActionProposal{
			proposal.ID: proposal,
		},
	}
	applyCalls := 0
	restarted.applyEffect = func(*gameTransaction, protocol.ActionSpec) {
		applyCalls++
	}
	secondClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/session/create":
					return dataResponse(t, protocol.MutationResult{
						SessionID: attempt.Request.SessionID,
						Revision:  1,
					}), nil
				case "/v1/agent/propose":
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					proposalPayloads = append(proposalPayloads, body)
					return dataResponse(t, protocol.ProposalResult{Proposal: proposal}), nil
				case "/v1/session/get":
					return dataResponse(t, state), nil
				case "/v1/action/commit":
					return nil, errors.New("Commit response lost")
				default:
					t.Fatalf("unexpected second-run request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := restarted.runExampleInvocation(&secondClient); err == nil ||
		!strings.Contains(err.Error(), "authoritative report remains queued") {
		t.Fatalf("second-run Commit error = %v", err)
	}
	if len(proposalPayloads) != 2 ||
		!reflect.DeepEqual(proposalPayloads[0], proposalPayloads[1]) {
		t.Fatalf("recovered Proposal payload changed: %q != %q", proposalPayloads[0], proposalPayloads[1])
	}
	if applyCalls != 1 || restarted.proposalAttempt != nil ||
		len(restarted.applied) != 1 || len(restarted.pending) != 1 {
		t.Fatalf(
			"recovered application calls=%d attempt=%+v applied=%d pending=%d",
			applyCalls,
			restarted.proposalAttempt,
			len(restarted.applied),
			len(restarted.pending),
		)
	}

	draining, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore applied outcome: %v", err)
	}
	draining.applyEffect = func(*gameTransaction, protocol.ActionSpec) {
		applyCalls++
	}
	drainClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/session/create", "/v1/action/commit":
					return dataResponse(t, protocol.MutationResult{
						SessionID: attempt.Request.SessionID,
						Revision:  3,
					}), nil
				default:
					t.Fatalf("unexpected drain request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := draining.runExampleInvocation(&drainClient); err != nil {
		t.Fatalf("drain restored Commit: %v", err)
	}
	if applyCalls != 1 || len(draining.pending) != 0 {
		t.Fatalf("restart reapplied effect: calls=%d pending=%d", applyCalls, len(draining.pending))
	}
}

func TestMismatchedProposalIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	request := protocol.ProposeRequest{
		SessionID: "session.stable",
		RequestID: "propose.stable.1",
		ActorID:   "npc.mira",
		Tick:      9,
	}
	valid := protocol.ActionProposal{
		ID:        "proposal.1",
		SessionID: request.SessionID,
		RequestID: request.RequestID,
		ActorID:   request.ActorID,
		Tick:      request.Tick,
		Action:    protocol.ActionSpec{ID: "wait"},
	}
	cases := map[string]protocol.ActionProposal{
		"session": func() protocol.ActionProposal {
			value := valid
			value.SessionID = "session.other"
			return value
		}(),
		"request": func() protocol.ActionProposal {
			value := valid
			value.RequestID = "propose.other"
			return value
		}(),
		"actor": func() protocol.ActionProposal {
			value := valid
			value.ActorID = "npc.other"
			return value
		}(),
		"tick": func() protocol.ActionProposal {
			value := valid
			value.Tick++
			return value
		}(),
	}
	for name, proposal := range cases {
		proposal := proposal
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateProposalIdentity(request, proposal); err == nil ||
				!strings.Contains(err.Error(), "invalid_proposal_identity") {
				t.Fatalf("identity mismatch error = %v", err)
			}
		})
	}

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	store, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	applyCalls := 0
	store.applyEffect = func(*gameTransaction, protocol.ActionSpec) {
		applyCalls++
	}
	integrationClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(httpRequest *http.Request) (*http.Response, error) {
				switch httpRequest.URL.Path {
				case "/v1/session/create":
					return dataResponse(t, protocol.MutationResult{
						SessionID: store.create.SessionID,
						Revision:  1,
					}), nil
				case "/v1/agent/propose":
					var retained protocol.ProposeRequest
					if err := json.NewDecoder(httpRequest.Body).Decode(&retained); err != nil {
						return nil, err
					}
					return dataResponse(t, protocol.ProposalResult{
						Proposal: protocol.ActionProposal{
							ID:        "proposal.mismatch",
							SessionID: retained.SessionID,
							RequestID: "wrong-request",
							ActorID:   retained.ActorID,
							Tick:      retained.Tick,
							Action:    retained.CandidateActions[0],
						},
					}), nil
				default:
					t.Fatalf("unexpected identity-test request %s", httpRequest.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := store.runExampleInvocation(&integrationClient); err == nil ||
		!strings.Contains(err.Error(), "invalid_proposal_identity") {
		t.Fatalf("mismatched Proposal result = %v", err)
	}
	if applyCalls != 0 || store.proposalAttempt == nil ||
		!store.proposalAttempt.Submitted ||
		len(store.applied) != 0 ||
		len(store.pending) != 0 {
		t.Fatalf(
			"identity mismatch did not fail closed: calls=%d attempt=%+v applied=%d pending=%d",
			applyCalls,
			store.proposalAttempt,
			len(store.applied),
			len(store.pending),
		)
	}
	restored, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restore mismatched Proposal Attempt: %v", err)
	}
	if restored.proposalAttempt == nil ||
		restored.proposalAttempt.Request.RequestID != store.proposalAttempt.Request.RequestID {
		t.Fatal("identity mismatch abandoned the exact durable Proposal Attempt")
	}
}

func TestAuthoritativeTickHighWaterSurvivesCleanRestart(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	store, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	store.currentTick = func() int64 { return 500 }
	firstAttempt, err := store.retainProposalAttempt()
	if err != nil {
		t.Fatalf("retain first attempt: %v", err)
	}
	if firstAttempt.Request.Tick != 500 {
		t.Fatalf("first attempt tick = %d, want 500", firstAttempt.Request.Tick)
	}
	if err := store.completeColdFallback(firstAttempt); err != nil {
		t.Fatalf("complete first operation: %v", err)
	}
	observeClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(*http.Request) (*http.Response, error) {
				return dataResponse(t, protocol.MutationResult{
					SessionID: store.create.SessionID,
					Revision:  1,
				}), nil
			},
		)},
	}
	if err := store.flush(&observeClient); err != nil {
		t.Fatalf("drain first operation: %v", err)
	}

	restarted, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("clean restart: %v", err)
	}
	restarted.currentTick = func() int64 { return 0 }
	nextAttempt, err := restarted.retainProposalAttempt()
	if err != nil {
		t.Fatalf("retain next attempt: %v", err)
	}
	if nextAttempt.Request.Tick != 501 {
		t.Fatalf(
			"clean-restart Proposal tick = %d, want persisted high-water + 1",
			nextAttempt.Request.Tick,
		)
	}
}

func TestProposalTerminalClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		status          int
		code            string
		wantError       string
		wantAttempt     bool
		wantApplication bool
	}{
		{
			name:   "invalid request is confirmed no Proposal",
			status: http.StatusBadRequest, code: "invalid_request",
			wantApplication: true,
		},
		{
			name:   "state change retires old request ID",
			status: http.StatusConflict, code: "state_changed",
		},
		{
			name:   "no safe action is confirmed no Proposal",
			status: http.StatusUnprocessableEntity, code: "no_safe_action",
			wantApplication: true,
		},
		{
			name:   "server failure is ambiguous",
			status: http.StatusInternalServerError, code: "internal_error",
			wantError: "proposal_outcome_unknown", wantAttempt: true,
		},
		{
			name:   "request timeout is ambiguous",
			status: http.StatusRequestTimeout, code: "request_timeout",
			wantError: "proposal_outcome_unknown", wantAttempt: true,
		},
		{
			name:   "explicit unknown is ambiguous",
			status: http.StatusConflict, code: "proposal_outcome_unknown",
			wantError: "proposal_outcome_unknown", wantAttempt: true,
		},
		{
			name:   "missing session fails closed",
			status: http.StatusNotFound, code: "session_not_found",
			wantError: "proposal_failed_closed", wantAttempt: true,
		},
		{
			name:   "request conflict fails closed",
			status: http.StatusConflict, code: "request_id_conflict",
			wantError: "proposal_failed_closed", wantAttempt: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			statePath := filepath.Join(t.TempDir(), "game-state.json")
			store, err := newDurableGameOutcomeStore(statePath)
			if err != nil {
				t.Fatalf("create durable store: %v", err)
			}
			applyCalls := 0
			store.applyEffect = func(*gameTransaction, protocol.ActionSpec) {
				applyCalls++
			}
			requests := make(map[string]int)
			c := client{
				baseURL: "http://rin.example",
				http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
					func(request *http.Request) (*http.Response, error) {
						requests[request.URL.Path]++
						switch request.URL.Path {
						case "/v1/session/create":
							return dataResponse(t, protocol.MutationResult{
								SessionID: store.create.SessionID,
								Revision:  1,
							}), nil
						case "/v1/agent/propose":
							return apiErrorResponse(test.status, test.code), nil
						case "/v1/session/observe":
							return dataResponse(t, protocol.MutationResult{
								SessionID: store.create.SessionID,
								Revision:  2,
							}), nil
						default:
							t.Fatalf("unexpected request %s", request.URL.Path)
							return nil, nil
						}
					},
				)},
			}
			runErr := store.runExampleInvocation(&c)
			if test.wantError == "" {
				if runErr != nil {
					t.Fatalf("run: %v", runErr)
				}
			} else if runErr == nil ||
				!strings.Contains(runErr.Error(), test.wantError) {
				t.Fatalf("run error = %v, want %q", runErr, test.wantError)
			}
			if (store.proposalAttempt != nil) != test.wantAttempt {
				t.Fatalf("retained Attempt = %+v, want retained=%t", store.proposalAttempt, test.wantAttempt)
			}
			if applyCalls != boolInt(test.wantApplication) {
				t.Fatalf("application calls = %d, want %d", applyCalls, boolInt(test.wantApplication))
			}
			if test.wantApplication && requests["/v1/session/observe"] != 1 {
				t.Fatalf("safe terminal did not reconcile fallback: requests=%v", requests)
			}
			if !test.wantApplication && requests["/v1/session/observe"] != 0 {
				t.Fatalf("failed-closed terminal emitted unreachable Observe: requests=%v", requests)
			}
		})
	}
}

func TestCreateIdentityErrorsDoNotGenerateUnreachableFallback(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status int
		code   string
	}{
		{name: "invalid request", status: http.StatusBadRequest, code: "invalid_request"},
		{name: "session collision", status: http.StatusConflict, code: "session_exists"},
		{name: "unauthorized", status: http.StatusUnauthorized, code: "unauthorized"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			statePath := filepath.Join(t.TempDir(), "game-state.json")
			store, err := newDurableGameOutcomeStore(statePath)
			if err != nil {
				t.Fatalf("create durable store: %v", err)
			}
			c := client{
				baseURL: "http://rin.example",
				http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
					func(request *http.Request) (*http.Response, error) {
						if request.URL.Path != "/v1/session/create" {
							t.Fatalf("identity failure emitted %s", request.URL.Path)
						}
						return apiErrorResponse(test.status, test.code), nil
					},
				)},
			}
			if err := store.runExampleInvocation(&c); err == nil ||
				!strings.Contains(err.Error(), "failed closed") {
				t.Fatalf("Create identity error = %v", err)
			}
			if store.proposalAttempt != nil ||
				len(store.applied) != 0 ||
				len(store.pending) != 0 {
				t.Fatalf(
					"Create identity error generated authority: attempt=%+v applied=%d pending=%d",
					store.proposalAttempt,
					len(store.applied),
					len(store.pending),
				)
			}
		})
	}
}

func TestStateChangedUsesNewRequestIDOnNextTurn(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	store, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("create durable store: %v", err)
	}
	var requestIDs []string
	stateChangedClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/session/create":
					return dataResponse(t, protocol.MutationResult{
						SessionID: store.create.SessionID,
						Revision:  1,
					}), nil
				case "/v1/agent/propose":
					var propose protocol.ProposeRequest
					if err := json.NewDecoder(request.Body).Decode(&propose); err != nil {
						return nil, err
					}
					requestIDs = append(requestIDs, propose.RequestID)
					return apiErrorResponse(http.StatusConflict, "state_changed"), nil
				default:
					t.Fatalf("unexpected state-changed request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := store.runExampleInvocation(&stateChangedClient); err != nil {
		t.Fatalf("retire state-changed Attempt: %v", err)
	}
	if store.proposalAttempt != nil || len(store.applied) != 0 || len(store.pending) != 0 {
		t.Fatalf("state_changed produced authority: attempt=%+v applied=%d pending=%d", store.proposalAttempt, len(store.applied), len(store.pending))
	}

	restarted, err := newDurableGameOutcomeStore(statePath)
	if err != nil {
		t.Fatalf("restart after state_changed: %v", err)
	}
	secondClient := client{
		baseURL: "http://rin.example",
		http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/session/create":
					return dataResponse(t, protocol.MutationResult{
						SessionID: restarted.create.SessionID,
						Revision:  1,
					}), nil
				case "/v1/agent/propose":
					var propose protocol.ProposeRequest
					if err := json.NewDecoder(request.Body).Decode(&propose); err != nil {
						return nil, err
					}
					requestIDs = append(requestIDs, propose.RequestID)
					return nil, errors.New("ambiguous second Proposal")
				default:
					t.Fatalf("unexpected second-turn request %s", request.URL.Path)
					return nil, nil
				}
			},
		)},
	}
	if err := restarted.runExampleInvocation(&secondClient); err == nil ||
		!strings.Contains(err.Error(), "proposal_outcome_unknown") {
		t.Fatalf("second turn error = %v", err)
	}
	if len(requestIDs) != 2 || requestIDs[0] == requestIDs[1] {
		t.Fatalf("state_changed request IDs = %v, want a new ID", requestIDs)
	}
}

func TestRepeatedOfflineRoundsRecoverWithoutPermanentFallbackGate(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "game-state.json")
	var stableCreate protocol.CreateSessionRequest
	for round := uint64(1); round <= 2; round++ {
		store, err := newDurableGameOutcomeStore(statePath)
		if err != nil {
			t.Fatalf("round %d load: %v", round, err)
		}
		if round == 1 {
			stableCreate = store.create
		} else if !reflect.DeepEqual(store.create, stableCreate) {
			t.Fatalf("round %d changed stable Create", round)
		}
		offlineClient := client{
			baseURL: "http://rin.example",
			http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
				func(*http.Request) (*http.Response, error) {
					return nil, errors.New("offline")
				},
			)},
		}
		if err := store.runExampleInvocation(&offlineClient); err != nil {
			t.Fatalf("round %d offline fallback: %v", round, err)
		}
		if store.operationSequence != round ||
			store.proposalAttempt != nil ||
			len(store.pending) != 1 {
			t.Fatalf(
				"round %d fallback state sequence=%d attempt=%+v pending=%d",
				round,
				store.operationSequence,
				store.proposalAttempt,
				len(store.pending),
			)
		}

		recovery, err := newDurableGameOutcomeStore(statePath)
		if err != nil {
			t.Fatalf("round %d recovery load: %v", round, err)
		}
		var paths []string
		recoveryClient := client{
			baseURL: "http://rin.example",
			http: &http.Client{Timeout: time.Second, Transport: roundTripFunc(
				func(request *http.Request) (*http.Response, error) {
					paths = append(paths, request.URL.Path)
					switch request.URL.Path {
					case "/v1/session/create", "/v1/session/observe":
						return dataResponse(t, protocol.MutationResult{
							SessionID: stableCreate.SessionID,
							Revision:  round,
						}), nil
					default:
						t.Fatalf("round %d unexpected recovery request %s", round, request.URL.Path)
						return nil, nil
					}
				},
			)},
		}
		if err := recovery.runExampleInvocation(&recoveryClient); err != nil {
			t.Fatalf("round %d recover: %v", round, err)
		}
		if strings.Join(paths, ",") != "/v1/session/create,/v1/session/observe" {
			t.Fatalf("round %d recovery order = %v", round, paths)
		}
	}
}

func TestRestoreRejectsCrossInvariantCorruptionBeforeNetworking(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(t.TempDir(), "source.json")
	store, err := newDurableGameOutcomeStore(sourcePath)
	if err != nil {
		t.Fatalf("create source store: %v", err)
	}
	store.currentTick = func() int64 { return 12 }
	attempt, err := store.retainProposalAttempt()
	if err != nil {
		t.Fatalf("retain source Attempt: %v", err)
	}
	attemptState := store.snapshot()
	outcome := appliedOutcome{accepted: true, outcome: "applied once"}
	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       store.create.SessionID,
		RequestID:       "commit." + attempt.OperationID,
		ProposalID:      "proposal.restore.1",
		EventID:         "outcome." + attempt.OperationID,
		Accepted:        outcome.accepted,
		Outcome:         outcome.outcome,
		Tags:            []string{"conversation"},
	}
	if _, err := store.applyAndEnqueueAttempt(
		attempt.OperationID,
		outcome,
		newCommitReport(attempt.OperationID, attempt.Request.ActorID, commit),
		attempt,
		attempt.Request.Tick,
		func(*gameTransaction) error { return nil },
	); err != nil {
		t.Fatalf("create source Outbox: %v", err)
	}
	outboxState := store.snapshot()
	operationID := attempt.OperationID
	persistedMarker := outboxState.Applied[operationID]
	if persistedMarker.ProposalID != commit.ProposalID ||
		persistedMarker.OccurrenceTick !=
			outboxState.Pending[operationID].Commit.Tick {
		t.Fatalf(
			"applied marker did not independently bind Proposal/tick: %+v",
			persistedMarker,
		)
	}
	offlinePath := filepath.Join(t.TempDir(), "offline.json")
	offlineStore, err := newDurableGameOutcomeStore(offlinePath)
	if err != nil {
		t.Fatalf("create offline source store: %v", err)
	}
	offlineStore.currentTick = func() int64 { return 15 }
	offlineAttempt, err := offlineStore.retainProposalAttempt()
	if err != nil {
		t.Fatalf("retain offline source Attempt: %v", err)
	}
	if err := offlineStore.completeColdFallback(offlineAttempt); err != nil {
		t.Fatalf("complete offline source fallback: %v", err)
	}
	offlineState := offlineStore.snapshot()
	offlineOperationID := offlineAttempt.OperationID

	tests := []struct {
		name   string
		source persistedGameOutcomeState
		mutate func(*persistedGameOutcomeState)
	}{
		{
			name: "run and Create identity diverge", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) { state.RunID += ".other" },
		},
		{
			name: "Create request fails protocol validation", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) {
				state.Create.ProtocolVersion = "unsupported"
			},
		},
		{
			name: "Create lacks outcome reporting", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) { state.Create.Features = nil },
		},
		{
			name: "Attempt operation is noncanonical", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) {
				state.ProposalAttempt.OperationID = "turn.other.1"
			},
		},
		{
			name: "Attempt request fails protocol validation", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) {
				state.ProposalAttempt.Request.CandidateActions[0].Kind = ""
			},
		},
		{
			name: "Attempt exceeds tick high-water", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) {
				state.ProposalAttempt.Request.Tick = state.LastAuthoritativeTick + 1
			},
		},
		{
			name: "Attempt fallback identity diverges", source: attemptState,
			mutate: func(state *persistedGameOutcomeState) {
				state.ProposalAttempt.Fallback.RequestID = "propose.wrong"
			},
		},
		{
			name: "pending request ID is not bound to key", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.RequestID = "commit.turn.wrong.1"
				state.Pending[operationID] = report
			},
		},
		{
			name: "pending event IDs diverge", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Fallback.EventID = "outcome.turn.wrong.1"
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit replaces Proposal ID with another valid ID", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.ProposalID = "proposal.valid-but-replaced"
				state.Pending[operationID] = report
			},
		},
		{
			name: "marker replaces Proposal ID with another valid ID", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				marker := state.Applied[operationID]
				marker.ProposalID = "proposal.valid-but-replaced"
				state.Applied[operationID] = marker
			},
		},
		{
			name: "marker replaces occurrence tick with another valid tick", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				marker := state.Applied[operationID]
				if marker.OccurrenceTick > 0 {
					marker.OccurrenceTick--
				} else {
					marker.OccurrenceTick++
					state.LastAuthoritativeTick++
				}
				state.Applied[operationID] = marker
			},
		},
		{
			name: "Commit injects a valid Fact", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.Facts = []protocol.Fact{{
					SubjectID:  "npc.mira",
					Predicate:  "mood",
					Object:     "calm",
					Confidence: 100,
				}}
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit injects a valid goal update", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.GoalUpdates = []protocol.GoalUpdate{{
					GoalID: "goal.connect", ProgressDelta: 1,
				}}
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit injects a tag", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.Tags = append(report.Commit.Tags, "injected")
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit fallback injects a summary prefix", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Fallback.Summary = "Injected. " + report.Fallback.Summary
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit fallback changes source", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Fallback.Source = "injected"
				state.Pending[operationID] = report
			},
		},
		{
			name: "Commit fallback injects observer", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Fallback.ObserverIDs = append(
					report.Fallback.ObserverIDs,
					"npc.other",
				)
				state.Pending[operationID] = report
			},
		},
		{
			name: "offline Observe injects a valid Fact", source: offlineState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[offlineOperationID]
				report.Observe.Facts = []protocol.Fact{{
					SubjectID:  "npc.mira",
					Predicate:  "mood",
					Object:     "calm",
					Confidence: 100,
				}}
				state.Pending[offlineOperationID] = report
			},
		},
		{
			name: "offline Observe injects a tag", source: offlineState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[offlineOperationID]
				report.Observe.Tags = append(report.Observe.Tags, "injected")
				state.Pending[offlineOperationID] = report
			},
		},
		{
			name: "offline Observe injects a summary prefix", source: offlineState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[offlineOperationID]
				report.Observe.Summary = "Injected. " + report.Observe.Summary
				state.Pending[offlineOperationID] = report
			},
		},
		{
			name: "offline Observe changes source", source: offlineState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[offlineOperationID]
				report.Observe.Source = "injected"
				state.Pending[offlineOperationID] = report
			},
		},
		{
			name: "pending ticks diverge", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Fallback.Tick++
				state.Pending[operationID] = report
			},
		},
		{
			name: "Outbox exceeds tick high-water", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				state.LastAuthoritativeTick--
			},
		},
		{
			name: "marker outcome differs from Commit", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				marker := state.Applied[operationID]
				marker.Outcome = "different"
				state.Applied[operationID] = marker
			},
		},
		{
			name: "Commit fails protocol validation", source: outboxState,
			mutate: func(state *persistedGameOutcomeState) {
				report := state.Pending[operationID]
				report.Commit.ProposalID = ""
				state.Pending[operationID] = report
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := clonePersistedState(t, test.source)
			test.mutate(&state)
			statePath := filepath.Join(t.TempDir(), "corrupt.json")
			if err := persistGameOutcomeState(statePath, state); err != nil {
				t.Fatalf("write corrupt state fixture: %v", err)
			}
			if _, err := newDurableGameOutcomeStore(statePath); err == nil {
				t.Fatal("cross-invariant corruption restored as authoritative state")
			}
		})
	}
}

func clonePersistedState(
	t *testing.T,
	state persistedGameOutcomeState,
) persistedGameOutcomeState {
	t.Helper()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state clone: %v", err)
	}
	var cloned persistedGameOutcomeState
	if err := json.Unmarshal(payload, &cloned); err != nil {
		t.Fatalf("unmarshal state clone: %v", err)
	}
	return cloned
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func dataResponse(t *testing.T, data any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(struct {
		OK   bool `json:"ok"`
		Data any  `json:"data"`
	}{OK: true, Data: data})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	return jsonResponse(string(payload))
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func apiErrorResponse(status int, code string) *http.Response {
	response := jsonResponse(
		`{"ok":false,"error":{"code":"` + code + `","message":"terminal"}}`,
	)
	response.StatusCode = status
	return response
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
