package agentdaemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/agentapi"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/signalbox"
)

const testAgentToken = "0123456789abcdef0123456789abcdef"

func TestOpenExposesOnlyConfiguredTaskPrincipal(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	daemon, err := Open(Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	request := httptest.NewRequest(http.MethodGet, "/agent/v1/info", nil)
	request.Header.Set("Authorization", "Bearer "+testAgentToken)
	response := httptest.NewRecorder()
	daemon.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var info agentapi.ClientInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Principal.ID != "rin.agent-client" || len(info.Principal.GrantedScopes) != 3 {
		t.Fatalf("Agent principal = %#v", info.Principal)
	}
	for _, scope := range info.Principal.GrantedScopes {
		if scope == controlplane.ScopeHostAdmin || scope == controlplane.ScopeActorExecute {
			t.Fatalf("authority scope leaked through Agent API: %q", scope)
		}
	}
}

func TestOpenAuthenticationModesDoNotProbeNetwork(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	if _, err := Open(Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
	}); err == nil {
		t.Fatal("bearer authentication without RIN_AGENT_API_KEY was accepted")
	}

	daemon, err := Open(Options{
		Config: testConfig(AuthenticationNone), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken,
	})
	if err != nil {
		t.Fatalf("no-auth local provider startup made a network-dependent failure: %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(Options{
		Config: testConfig(AuthenticationNone), DataDir: t.TempDir(),
		Control: control, HTTPToken: testAgentToken, APIKey: "unexpected-key",
	}); err == nil {
		t.Fatal("unused API key was accepted in no-auth mode")
	}
}

func TestOpenSemanticEmbeddingIsExplicitAndKeepsInjectedSQLiteOwnedByCaller(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	config := testConfig(AuthenticationBearerEnv)
	config.Memory.SemanticEmbedding = SemanticEmbeddingConfig{
		Enabled: true, BaseURL: "https://embeddings.example.test/v1", Model: "embed-test",
		Authentication: AuthenticationBearerEnv,
		AllowedDomains: []cognition.MemoryDomain{cognition.MemoryActorSemantic},
	}
	if _, err := Open(Options{
		Config: config, DataDir: t.TempDir(), Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{},
	}); err == nil {
		t.Fatal("semantic bearer authentication without an embedding key was accepted")
	}

	local, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	daemon, err := Open(Options{
		Config: config, DataDir: t.TempDir(), Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{}, EmbeddingProvider: inertEmbeddingProvider{},
		Memory: local,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Snapshot(context.Background()); err != nil {
		t.Fatalf("daemon closed caller-owned SQLite memory: %v", err)
	}

	config.Memory.SemanticEmbedding = SemanticEmbeddingConfig{}
	if _, err := Open(Options{
		Config: config, DataDir: t.TempDir(), Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{}, EmbeddingProvider: inertEmbeddingProvider{},
	}); err == nil {
		t.Fatal("an embedding provider was accepted while semantic retrieval was disabled")
	}
}

func TestCloseReleasesPersistentStoresForRestart(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	dataDirectory := t.TempDir()
	options := Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: dataDirectory,
		Control: control, HTTPToken: testAgentToken,
		GenerationProvider: inertGenerationProvider{},
	}
	first, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(options); err == nil {
		t.Fatal("a second daemon acquired the same persistent stores")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tasks.json", "memory.db"} {
		if info, err := os.Stat(filepath.Join(dataDirectory, "agent", name)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("persistent %s was not created: %v", name, err)
		}
	}
	second, err := Open(options)
	if err != nil {
		t.Fatalf("restart after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerFailureDoesNotLeakStoreLocks(t *testing.T) {
	control := controlplane.New(controlplane.Options{})
	defer control.Close()
	dataDirectory := t.TempDir()
	options := Options{
		Config: testConfig(AuthenticationBearerEnv), DataDir: dataDirectory,
		Control: control, HTTPToken: "short",
		GenerationProvider: inertGenerationProvider{},
	}
	if _, err := Open(options); err == nil {
		t.Fatal("short Agent token was accepted")
	}
	options.HTTPToken = testAgentToken
	daemon, err := Open(options)
	if err != nil {
		t.Fatalf("failed startup leaked a store lock: %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSignalSchedulerWakesOnlyInternalInitiative(t *testing.T) {
	for _, test := range []struct {
		name      string
		source    controlplane.DecisionSource
		wantTasks int
	}{
		{name: "internal", source: controlplane.DecisionInternal, wantTasks: 1},
		{name: "external", source: controlplane.DecisionExternal, wantTasks: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			control := controlplane.New(controlplane.Options{})
			defer control.Close()
			lease, err := control.RegisterHost(signalTestRegistration())
			if err != nil {
				t.Fatal(err)
			}
			epoch := host.Epoch{
				SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
			}
			if err := control.PublishWorld(
				"host.one", lease.LeaseID, signalTestPublication(epoch, test.source),
			); err != nil {
				t.Fatal(err)
			}
			signals, _ := signalbox.NewStore(signalbox.StoreConfig{})
			defer signals.Close()
			_, err = signals.Configure(
				signalbox.Target{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"},
				signalbox.Settings{Enabled: true, MaxPending: 8},
			)
			if err != nil {
				t.Fatal(err)
			}
			config := testConfig(AuthenticationBearerEnv)
			config.Personas[0].Initiative = cognition.InitiativePolicy{
				Enabled: true, MaxConsecutiveActions: 1, Triggers: []string{"test.player.hurt"},
			}
			daemon, err := Open(Options{
				Config: config, DataDir: t.TempDir(), Control: control,
				HTTPToken: testAgentToken, GenerationProvider: inertGenerationProvider{},
				Signals: signals,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer daemon.Close()
			current := signalbox.Signal{
				SignalID: "signal.one", HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
				Kind: "test.player.hurt", Summary: "The player was hurt.", Epoch: epoch,
				ObservationSequence: 2, ExpiresAtUnixMillis: time.Now().Add(time.Minute).UnixMilli(),
			}
			if result, err := signals.Publish(current); err != nil || !result.Accepted {
				t.Fatalf("publish = %#v, %v", result, err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				snapshot, err := daemon.tasks.Snapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				ready := len(snapshot.Tasks) == test.wantTasks
				if test.wantTasks == 0 {
					ready = time.Now().After(deadline)
				}
				if ready || time.Now().After(deadline) {
					if len(snapshot.Tasks) != test.wantTasks {
						t.Fatalf("tasks = %#v, want %d", snapshot.Tasks, test.wantTasks)
					}
					if test.wantTasks == 1 {
						task := snapshot.Tasks[0]
						if task.TaskID != proactiveTaskID(current) || task.Budget.MaxActions != 1 ||
							len(task.History) < 2 || task.History[1].Kind != "signal.received" ||
							task.History[1].Signal == nil || task.History[1].Signal.SignalID != current.SignalID {
							t.Fatalf("signal task = %#v", task)
						}
					}
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func signalTestRegistration() controlplane.HostRegistration {
	return controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion, HostID: "host.one",
		InstanceID: "instance.one", LeaseTTLMillis: 5_000,
		Manifest: host.HostManifest{
			ContractVersion: host.ContractVersion, AdapterID: "test.adapter", AdapterVersion: "1.0.0",
			EngineID: "test.engine", EngineVersion: "1", Runtime: "go", Platform: "test",
			Headless: true, Authority: host.AuthorityServer,
			Deployment: host.DeploymentLoopbackSidecar, Control: host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 4,
			Durability:          host.Durability{Profile: host.DurabilityAdvisory, StableIdentity: true},
		},
	}
}

func signalTestPublication(
	epoch host.Epoch,
	source controlplane.DecisionSource,
) controlplane.WorldPublication {
	authority := &controlplane.DecisionAuthority{
		Source: source, Revision: 1, PersonaMode: controlplane.PersonaCharacterBound,
	}
	if source == controlplane.DecisionExternal {
		authority.ControllerPrincipalID = "player.one"
	}
	return controlplane.WorldPublication{
		WorldID: "world.one", DisplayName: "World", Sequence: 1,
		Actors: []controlplane.ActorPublication{{
			ActorID: "actor.one", OwnerPrincipalID: "player.one", DisplayName: "Companion",
			ObservationSeq: 2, Epoch: epoch, Authority: authority,
			State: json.RawMessage(`{"status":"ready"}`),
		}},
	}
}

type inertGenerationProvider struct{}

func (inertGenerationProvider) Complete(
	context.Context,
	provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	return provider.CompletionResponse{}, context.Canceled
}

type inertEmbeddingProvider struct{}

func (inertEmbeddingProvider) Embed(
	context.Context,
	provider.EmbeddingRequest,
) (provider.EmbeddingResponse, error) {
	return provider.EmbeddingResponse{Embeddings: [][]float32{{1, 0}}}, nil
}
