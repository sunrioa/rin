package cognition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestFilePersonaStorePersistsRevisionCheckedEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personas.json")
	store, err := cognition.OpenFilePersonaStore(path, cognition.PersonaSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cognition.OpenFilePersonaStore(path, cognition.PersonaSnapshot{}); !errors.Is(err, cognition.ErrPersonaStoreLocked) {
		t.Fatalf("second writer error = %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Profiles[0].Voice = "Warm and concise."
	updated, err := store.CompareAndSwap(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != snapshot.Revision+1 {
		t.Fatalf("updated revision = %d", updated.Revision)
	}
	if _, err := store.CompareAndSwap(context.Background(), snapshot); !errors.Is(err, cognition.ErrPersonaConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	withoutDefault := updated
	withoutDefault.Bindings = nil
	if _, err := store.CompareAndSwap(context.Background(), withoutDefault); err == nil {
		t.Fatal("persona store accepted a snapshot without a default binding")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := cognition.OpenFilePersonaStore(path, cognition.PersonaSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	profile, err := reopened.Load(context.Background(), cognition.PersonaRequest{
		ActorID: "actor.any", ControllerID: "controller.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Voice != "Warm and concise." {
		t.Fatalf("profile = %+v", profile)
	}
}
