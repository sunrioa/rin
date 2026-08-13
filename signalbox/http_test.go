package signalbox_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/signalbox"
)

func TestHTTPHostPublishAndActorRead(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	now := time.UnixMilli(1_000)
	control := controlplane.New(controlplane.Options{Now: func() time.Time { return now }})
	defer control.Close()
	lease, err := control.RegisterHost(testRegistration())
	if err != nil {
		t.Fatal(err)
	}
	epoch := host.Epoch{SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1}
	if err := control.PublishWorld("host.one", lease.LeaseID, testPublication(epoch, controlplane.DecisionExternal)); err != nil {
		t.Fatal(err)
	}
	reader, err := controlplane.NewClientService(control, host.Principal{
		ID: "player.one", GrantedScopes: []string{controlplane.ScopeActorRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := signalbox.NewStore(signalbox.StoreConfig{Now: func() time.Time { return now }})
	defer store.Close()
	service, _ := signalbox.NewService(store, control, reader)
	handler, err := signalbox.NewHTTPHandler(service, signalbox.HTTPOptions{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	callSignalHTTP(t, handler, token, "/signals/v1/host/settings", signalbox.HostSettingsInput{
		HostID: "host.one", LeaseID: lease.LeaseID, WorldID: "world.one", ActorID: "actor.one",
		Settings: signalbox.Settings{Enabled: true, MaxPending: 8},
	}, &signalbox.Settings{})
	var result signalbox.PublishResult
	callSignalHTTP(t, handler, token, "/signals/v1/host/publish", signalbox.HostPublishInput{
		HostID: "host.one", LeaseID: lease.LeaseID,
		Signal: signalbox.Signal{
			SchemaVersion: signalbox.SchemaVersion,
			SignalID:      "signal.one", HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
			Kind: "test.player.hurt", Summary: "The player was hurt.", Epoch: epoch,
			ObservationSequence: 2, ExpiresAtUnixMillis: now.Add(time.Second).UnixMilli(),
		},
	}, &result)
	if !result.Accepted {
		t.Fatalf("publish = %#v", result)
	}
	var page signalbox.Page
	callSignalHTTP(t, handler, token, "/signals/v1/list", signalbox.ListInput{Target: signalbox.Target{
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
	}}, &page)
	if len(page.Signals) != 1 {
		t.Fatalf("list = %#v", page)
	}
}

func callSignalHTTP(t *testing.T, handler http.Handler, token, path string, input, output any) {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status = %d body = %s", path, response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), output); err != nil {
		t.Fatal(err)
	}
}
