package controlplane

import (
	"testing"
	"time"
)

func TestSchedulingChangeFeedTargetsWorldAndOperation(t *testing.T) {
	now := time.UnixMilli(1000000)
	service, lease, principal, actionHost := openActionPersistenceHarness(t, t.TempDir(), &now, "instance.scheduling", OpenSQLite)
	cursor := service.SchedulingChanges(0).Revision
	if err := service.PublishWorld("test.host", lease.LeaseID, worldPublication(2, "changed")); err != nil {
		t.Fatal(err)
	}
	page := service.SchedulingChanges(cursor)
	if page.All || len(page.Changes) != 1 || page.Changes[0].Target.HostID != "test.host" || page.Changes[0].Target.WorldID == "" {
		t.Fatalf("world invalidation: %#v", page)
	}
	cursor = page.Revision
	operation := commitTestOutcome(t, service, lease, principal, actionHost)
	page = service.SchedulingChanges(cursor)
	found := false
	for _, change := range page.Changes {
		found = found || change.OperationID == operation.OperationID
	}
	if page.All || !found {
		t.Fatalf("outcome invalidation missing: %#v", page)
	}
}
