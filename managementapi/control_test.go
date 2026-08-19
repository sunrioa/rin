package managementapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestControlManagementReadsTheAuthoritativeControlStore(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	control := controlplane.New(controlplane.Options{})
	principal := host.Principal{
		ID: "rin.console",
		GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
			controlplane.ScopeHostAdmin, controlplane.ScopeOperationCancel,
		},
	}
	if err := service.ConfigureControl(control, principal); err != nil {
		t.Fatal(err)
	}
	runtime, err := service.RuntimeSnapshot(context.Background())
	if err != nil || len(runtime.Worlds) != 0 || len(runtime.Actors) != 0 {
		t.Fatalf("runtime = %#v, %v", runtime, err)
	}
	operations, err := service.ListOperations(
		context.Background(), controlplane.ListOperationsInput{Limit: 10},
	)
	if err != nil || len(operations.Operations) != 0 {
		t.Fatalf("operations = %#v, %v", operations, err)
	}
}

func TestControlManagementRenewsAnExactActorLease(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	control := controlplane.New(controlplane.Options{})
	principal := host.Principal{
		ID: "rin.console",
		GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl,
			controlplane.ScopeHostAdmin, controlplane.ScopeOperationCancel,
		},
	}
	if err := service.ConfigureControl(control, principal); err != nil {
		t.Fatal(err)
	}
	lease, err := control.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          "host.one", InstanceID: "instance.one", LeaseTTLMillis: 30_000,
		Manifest: host.HostManifest{
			ContractVersion: host.ContractVersion, AdapterID: "adapter.test", AdapterVersion: "1.0.0",
			EngineID: "engine.test", EngineVersion: "1", Runtime: "go", Platform: "test",
			Headless: true, Authority: host.AuthorityServer,
			Deployment: host.DeploymentLoopbackSidecar, Control: host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 1,
			Durability:          host.Durability{Profile: host.DurabilityAdvisory, StableIdentity: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch := host.Epoch{SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1}
	if err := control.PublishWorld("host.one", lease.LeaseID, controlplane.WorldPublication{
		WorldID: "world.one", DisplayName: "World", Sequence: 1,
		Actors: []controlplane.ActorPublication{{
			ActorID: "actor.one", OwnerPrincipalID: principal.ID, DisplayName: "Actor",
			ObservationSeq: 1, Epoch: epoch, State: json.RawMessage(`{"status":"ready"}`),
			Authority: &controlplane.DecisionAuthority{
				Source: controlplane.DecisionExternal, ControllerPrincipalID: principal.ID,
				Revision: 1, PersonaMode: controlplane.PersonaCharacterBound,
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	target := controlplane.ActorControlTarget{HostID: "host.one", WorldID: "world.one", ActorID: "actor.one"}
	acquired, err := service.ControlActor(context.Background(), ActorControlInput{
		ActorControlTarget: target, Action: "acquire", ControllerID: "controller.console",
		LeaseTTLMillis: 5_000,
	})
	if err != nil || acquired.Lease == nil {
		t.Fatalf("acquire = %#v, %v", acquired, err)
	}
	renewed, err := service.ControlActor(context.Background(), ActorControlInput{
		ActorControlTarget: target, Action: "renew", LeaseID: acquired.Lease.LeaseID,
		LeaseTTLMillis: 10_000,
	})
	if err != nil || renewed.Lease == nil || renewed.Lease.LeaseID != acquired.Lease.LeaseID ||
		renewed.Lease.ExpiresAtUnixMillis <= acquired.Lease.ExpiresAtUnixMillis {
		t.Fatalf("renew = %#v, %v", renewed, err)
	}
}
