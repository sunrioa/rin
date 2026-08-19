package managementapi

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
)

func TestServiceManagesCommonMemoryCardsWithoutLeakingActorMemory(t *testing.T) {
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
	service.now = func() time.Time { return time.UnixMilli(100) }
	ids := []string{"memory.card.one", "memory.card.two"}
	service.newID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	created, err := service.SaveMemoryCard(context.Background(), MemoryCardInput{
		Content: "The shared companion prefers concise replies.",
		Tags:    []string{"persona"}, Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.SaveMemoryCard(context.Background(), MemoryCardInput{
		MemoryID: created.MemoryID,
		Content:  "The shared companion is concise and observant.", Tags: []string{"persona"},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListMemories(context.Background(), MemoryListRequest{
		Scope: MemoryScopeCommon, Search: "observant", Tags: []string{"persona"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{listed.Records[0].MemoryID}; !reflect.DeepEqual(ids, []string{updated.MemoryID}) {
		t.Fatalf("listed ids = %v", ids)
	}
	if err := service.ForgetMemory(context.Background(), MemoryForgetInput{
		MemoryID: updated.MemoryID,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err = service.ListMemories(context.Background(), MemoryListRequest{})
	if err != nil || len(listed.Records) != 0 {
		t.Fatalf("listed after forget = %#v, %v", listed, err)
	}
}
