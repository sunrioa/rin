package controlplane

// SchedulingChange identifies which readiness predicates may have changed.
// An empty ActorID denotes a world; an empty WorldID denotes a Host.
type SchedulingChange struct {
	Target      ActorControlTarget
	OperationID string
}
type SchedulingChangePage struct {
	Revision uint64
	All      bool
	Changes  []SchedulingChange
}

func (service *Service) SchedulingChanges(after uint64) SchedulingChangePage {
	service.mu.RLock()
	defer service.mu.RUnlock()
	values, revision, overflow := service.schedulingChanges.Since(after)
	return SchedulingChangePage{Revision: revision, All: overflow, Changes: values}
}
func (service *Service) notifyActorChangedLocked(target ActorControlTarget) {
	service.schedulingChanges.Append(SchedulingChange{Target: target})
	service.notifyLocked()
}
func (service *Service) recordOperationChangeLocked(id string) {
	service.schedulingChanges.Append(SchedulingChange{OperationID: id})
}
