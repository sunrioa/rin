package managementapi

import (
	"context"
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
