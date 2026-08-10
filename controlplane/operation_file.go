package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/policy"
)

const (
	operationFileVersion         = "rin.control.operations/v4"
	previousOperationFileVersion = "rin.control.operations/v3"
	olderOperationFileVersion    = "rin.control.operations/v2"
	legacyOperationFileVersion   = "rin.control.operations/v1"
	operationFileName            = "operations.json"
	maxOperationFileBytes        = 64 << 20
	maxQueuedStateBytes          = 32 << 20
)

var errOperationFileTooLarge = errors.New("operation state exceeds its size limit")

type operationFile struct {
	root     string
	path     string
	lockFile *os.File
}

type persistedOperations struct {
	Version        string               `json:"version"`
	Operations     []persistedOperation `json:"operations"`
	Controllers    []ControllerLease    `json:"controller_leases,omitempty"`
	EmergencyStops []ActorEmergencyStop `json:"emergency_stops,omitempty"`
	PolicyState    *policy.State        `json:"policy_state,omitempty"`
}

type persistedOperation struct {
	Request   HostControlRequest   `json:"request"`
	Status    OperationStatus      `json:"status"`
	Attempts  uint32               `json:"delivery_attempts"`
	Cancel    bool                 `json:"cancel_requested"`
	Ack       *HostAcknowledgement `json:"ack,omitempty"`
	Run       *host.ActionRun      `json:"run,omitempty"`
	Outcome   *host.ActionOutcome  `json:"outcome,omitempty"`
	Output    json.RawMessage      `json:"output,omitempty"`
	CreatedAt int64                `json:"created_at_unix_millis"`
	UpdatedAt int64                `json:"updated_at_unix_millis"`
}

// OpenFile creates a Control Plane whose bounded operations survive process
// restart. Host leases and read models remain Host-owned and must be republished.
func OpenFile(root string, options Options) (*Service, error) {
	file, state, err := openOperationFile(root)
	if err != nil {
		return nil, err
	}
	service := New(options)
	service.operationFile = file
	if err := service.restoreOperations(state); err != nil {
		return nil, errors.Join(err, file.close())
	}
	return service, nil
}

func openOperationFile(
	root string,
) (*operationFile, persistedOperations, error) {
	if root == "" {
		return nil, persistedOperations{}, errors.New(
			"control plane data directory is required",
		)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, persistedOperations{}, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, persistedOperations{}, fmt.Errorf(
			"%w: create data directory: %v",
			ErrPersistence,
			err,
		)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, persistedOperations{}, fmt.Errorf(
			"%w: inspect data directory: %v",
			ErrPersistence,
			err,
		)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, persistedOperations{}, fmt.Errorf(
			"%w: data directory must be a real directory",
			ErrPersistence,
		)
	}
	if err := prepareOperationDirectoryPermissions(absolute, info); err != nil {
		return nil, persistedOperations{}, err
	}
	file := &operationFile{
		root: absolute,
		path: filepath.Join(absolute, operationFileName),
	}
	lockFile, err := acquireOperationDirectoryLock(
		filepath.Join(absolute, operationLockFileName),
	)
	if err != nil {
		return nil, persistedOperations{}, err
	}
	file.lockFile = lockFile
	state, err := file.read()
	if err != nil {
		_ = file.close()
		return nil, persistedOperations{}, err
	}
	return file, state, nil
}

func (file *operationFile) close() error {
	if file == nil || file.lockFile == nil {
		return nil
	}
	lockFile := file.lockFile
	file.lockFile = nil
	return releaseOperationDirectoryLock(lockFile)
}

func (file *operationFile) read() (persistedOperations, error) {
	info, err := os.Lstat(file.path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedOperations{
			Version:    operationFileVersion,
			Operations: []persistedOperation{},
		}, nil
	}
	if err != nil {
		return persistedOperations{}, fmt.Errorf(
			"%w: inspect operation state: %v",
			ErrPersistence,
			err,
		)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return persistedOperations{}, fmt.Errorf(
			"%w: operation state must be a regular file",
			ErrPersistence,
		)
	}
	if err := validateOperationStatePermissions(info); err != nil {
		return persistedOperations{}, err
	}
	if info.Size() > maxOperationFileBytes {
		return persistedOperations{}, fmt.Errorf(
			"%w: operation state exceeds 64 MiB",
			ErrPersistence,
		)
	}
	payload, err := os.ReadFile(file.path)
	if err != nil {
		return persistedOperations{}, fmt.Errorf(
			"%w: read operation state: %v",
			ErrPersistence,
			err,
		)
	}
	if err := jsonwire.Validate(payload); err != nil {
		return persistedOperations{}, fmt.Errorf(
			"%w: invalid operation state JSON: %v",
			ErrPersistence,
			err,
		)
	}
	var state persistedOperations
	if err := decodeSingleJSON(bytes.NewReader(payload), &state); err != nil {
		return persistedOperations{}, fmt.Errorf(
			"%w: decode operation state: %v",
			ErrPersistence,
			err,
		)
	}
	if state.Version != operationFileVersion &&
		state.Version != previousOperationFileVersion &&
		state.Version != olderOperationFileVersion &&
		state.Version != legacyOperationFileVersion {
		return persistedOperations{}, fmt.Errorf(
			"%w: unsupported operation state version %q",
			ErrPersistence,
			state.Version,
		)
	}
	return state, nil
}

func (file *operationFile) write(
	state persistedOperations,
	maximumBytes int64,
) error {
	temporary, err := os.CreateTemp(file.root, ".operations-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary state: %v", ErrPersistence, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: protect temporary state: %v", ErrPersistence, err)
	}
	encoder := json.NewEncoder(&boundedOperationWriter{
		writer:    temporary,
		remaining: maximumBytes,
	})
	if err := encoder.Encode(state); err != nil {
		_ = temporary.Close()
		if errors.Is(err, errOperationFileTooLarge) {
			return fmt.Errorf(
				"%w: operation state exceeds its configured byte budget",
				ErrCapacity,
			)
		}
		return fmt.Errorf("%w: encode operation state: %v", ErrPersistence, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("%w: sync operation state: %v", ErrPersistence, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close operation state: %v", ErrPersistence, err)
	}
	if err := replaceOperationFile(temporaryName, file.path); err != nil {
		return fmt.Errorf("%w: publish operation state: %v", ErrPersistence, err)
	}
	if err := syncOperationDirectory(file.root); err != nil {
		return fmt.Errorf("%w: sync operation directory: %v", ErrPersistence, err)
	}
	return nil
}

func (service *Service) restoreOperations(state persistedOperations) error {
	if len(state.Operations) > service.maxOperations {
		return fmt.Errorf(
			"%w: state contains %d operations above configured limit %d",
			ErrPersistence,
			len(state.Operations),
			service.maxOperations,
		)
	}
	if state.PolicyState != nil {
		if service.policyEngine == nil {
			return fmt.Errorf("%w: policy state requires a configured Policy Engine", ErrPersistence)
		}
		if err := policy.ValidateState(*state.PolicyState); err != nil {
			return fmt.Errorf("%w: validate policy state: %v", ErrPersistence, err)
		}
	}
	hasV2Action := false
	actionDecisionIDs := make(map[string]struct{})
	requiredReservationIDs := make(map[string]struct{})
	for index, persisted := range state.Operations {
		operation, err := restoreOperation(persisted, state.Version)
		if err != nil {
			return fmt.Errorf(
				"%w: operations[%d]: %v",
				ErrPersistence,
				index,
				err,
			)
		}
		operationID := operation.request.OperationID
		if _, exists := service.operations[operationID]; exists {
			return fmt.Errorf(
				"%w: duplicate operation_id %q",
				ErrPersistence,
				operationID,
			)
		}
		if _, exists := service.requests[operation.idempotency]; exists {
			return fmt.Errorf(
				"%w: duplicate principal request_id",
				ErrPersistence,
			)
		}
		service.operations[operationID] = operation
		service.requests[operation.idempotency] = operationID
		hasV2Action = hasV2Action || operation.request.Kind == ControlAction
		if operation.request.Kind == ControlAction &&
			operation.request.PolicyDecision != nil &&
			operation.request.PolicyDecision.Result == policy.Allow {
			decision := operation.request.PolicyDecision
			actionDecisionIDs[decision.DecisionID] = struct{}{}
			if len(decision.EffectiveLimits) != 0 &&
				operationPolicyReservationPending(persisted.Status) {
				requiredReservationIDs[decision.DecisionID] = struct{}{}
			}
		}
	}
	if hasV2Action && (service.policyEngine == nil || state.PolicyState == nil) {
		return fmt.Errorf("%w: V2 actions require persisted policy state", ErrPersistence)
	}
	if state.PolicyState != nil {
		reservationIDs := make(map[string]struct{}, len(state.PolicyState.Reservations))
		for _, reservation := range state.PolicyState.Reservations {
			if _, exists := actionDecisionIDs[reservation.DecisionID]; !exists {
				return fmt.Errorf(
					"%w: policy reservation %q has no matching V2 action",
					ErrPersistence,
					reservation.DecisionID,
				)
			}
			reservationIDs[reservation.DecisionID] = struct{}{}
		}
		for decisionID := range requiredReservationIDs {
			if _, exists := reservationIDs[decisionID]; !exists {
				return fmt.Errorf(
					"%w: active V2 action decision %q is missing its policy reservation",
					ErrPersistence,
					decisionID,
				)
			}
		}
	}
	for _, operation := range service.operations {
		parentID := operation.request.ParentOperationID
		if parentID == "" {
			continue
		}
		parent := service.operations[parentID]
		if parent == nil || parent.request.Kind != ControlAction ||
			operation.request.Kind != ControlAction ||
			parent.request.HostID != operation.request.HostID ||
			parent.request.WorldID != operation.request.WorldID ||
			parent.request.ActorID != operation.request.ActorID ||
			parent.request.Principal.ID != operation.request.Principal.ID ||
			controllerLeaseIDFromRequest(parent.request) !=
				controllerLeaseIDFromRequest(operation.request) {
			return fmt.Errorf("%w: invalid parent operation relation", ErrPersistence)
		}
		if len(parent.children) >= maxChildOperations {
			return fmt.Errorf("%w: parent has too many child operations", ErrPersistence)
		}
		parent.children = append(parent.children, operation.request.OperationID)
	}
	if err := validateOperationParentGraph(service.operations); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	for _, operation := range service.operations {
		slices.SortFunc(operation.children, func(left, right string) int {
			leftOperation := service.operations[left]
			rightOperation := service.operations[right]
			if leftOperation.createdAt != rightOperation.createdAt {
				if leftOperation.createdAt < rightOperation.createdAt {
					return -1
				}
				return 1
			}
			return compare(left, right)
		})
	}
	leaseIDs := make(map[string]struct{}, len(state.Controllers))
	now := service.now().UnixMilli()
	for index, lease := range state.Controllers {
		if err := validatePersistedControllerLease(lease); err != nil {
			return fmt.Errorf(
				"%w: controller_leases[%d]: %v",
				ErrPersistence,
				index,
				err,
			)
		}
		key := actorControlKey{
			hostID:  lease.HostID,
			worldID: lease.WorldID,
			actorID: lease.ActorID,
		}
		if _, exists := service.controllers[key]; exists {
			return fmt.Errorf("%w: duplicate controller actor", ErrPersistence)
		}
		if _, exists := leaseIDs[lease.LeaseID]; exists {
			return fmt.Errorf("%w: duplicate controller lease_id", ErrPersistence)
		}
		leaseIDs[lease.LeaseID] = struct{}{}
		if lease.ExpiresAtUnixMillis > now {
			service.controllers[key] = lease
		}
	}
	for index, stop := range state.EmergencyStops {
		if err := validatePersistedEmergencyStop(stop); err != nil {
			return fmt.Errorf(
				"%w: emergency_stops[%d]: %v",
				ErrPersistence,
				index,
				err,
			)
		}
		key := controlKey(stop.ActorControlTarget)
		if _, exists := service.emergencyStops[key]; exists {
			return fmt.Errorf("%w: duplicate emergency stop actor", ErrPersistence)
		}
		service.emergencyStops[key] = stop
	}
	if state.PolicyState != nil {
		if err := service.policyEngine.RestoreState(*state.PolicyState); err != nil {
			return fmt.Errorf("%w: restore policy state: %v", ErrPersistence, err)
		}
	}
	if hasV2Action {
		for _, operation := range service.operations {
			if operation.request.Kind != ControlAction ||
				operation.request.PolicyDecision == nil ||
				operation.request.PolicyDecision.Result != policy.Allow {
				continue
			}
			switch operation.status {
			case OperationSucceeded, OperationOutcomeUnknown:
				service.finalizeOperationPolicyLocked(operation, true)
			case OperationFailed, OperationCancelled, OperationInterrupted,
				OperationStale, OperationRejected:
				service.finalizeOperationPolicyLocked(operation, false)
			}
		}
		service.operationDirty = true
	}
	return nil
}

func operationPolicyReservationPending(status OperationStatus) bool {
	return status == OperationQueued || status == OperationDelivered ||
		status == OperationAccepted || status == OperationRunning
}

func validateOperationParentGraph(operations map[string]*operationState) error {
	complete := make(map[string]struct{}, len(operations))
	for operationID := range operations {
		chain := make(map[string]struct{})
		currentID := operationID
		for currentID != "" {
			if _, done := complete[currentID]; done {
				break
			}
			if _, cycle := chain[currentID]; cycle {
				return errors.New("operation parent relation contains a cycle")
			}
			chain[currentID] = struct{}{}
			current := operations[currentID]
			if current == nil {
				break
			}
			currentID = current.request.ParentOperationID
		}
		for chainID := range chain {
			complete[chainID] = struct{}{}
		}
	}
	return nil
}

func validatePersistedControllerLease(value ControllerLease) error {
	if err := validateID("lease_id", value.LeaseID); err != nil {
		return err
	}
	if err := validateID("controller_id", value.ControllerID); err != nil {
		return err
	}
	if err := validateID("principal_id", value.PrincipalID); err != nil {
		return err
	}
	if err := validateActorControlTarget(ActorControlTarget{
		HostID: value.HostID, WorldID: value.WorldID, ActorID: value.ActorID,
	}); err != nil {
		return err
	}
	if value.Source != DecisionInternal && value.Source != DecisionExternal {
		return errors.New("unsupported controller source")
	}
	if value.PersonaMode != PersonaCharacterBound &&
		value.PersonaMode != PersonaAgentAvatar {
		return errors.New("unsupported controller persona mode")
	}
	if value.Source == DecisionInternal && value.PersonaMode != PersonaCharacterBound {
		return errors.New("internal controller must be character-bound")
	}
	if value.AuthorityRevision == 0 || value.AuthorityRevision > maxJSONSafeInteger {
		return errors.New("invalid controller authority revision")
	}
	if err := value.Epoch.Validate("epoch"); err != nil {
		return err
	}
	if value.Epoch.WorldID != value.WorldID {
		return errors.New("controller epoch does not match world_id")
	}
	if value.AcquiredAtUnixMillis < 0 ||
		value.ExpiresAtUnixMillis <= value.AcquiredAtUnixMillis ||
		value.ExpiresAtUnixMillis > maxJSONSafeInteger {
		return errors.New("invalid controller lease timestamps")
	}
	return nil
}

func validatePersistedEmergencyStop(value ActorEmergencyStop) error {
	if err := validateActorControlTarget(value.ActorControlTarget); err != nil {
		return err
	}
	if value.Revision == 0 || value.Revision > maxJSONSafeInteger {
		return errors.New("invalid emergency stop revision")
	}
	if err := validateID("updated_by_principal_id", value.UpdatedByPrincipalID); err != nil {
		return err
	}
	if value.UpdatedAtUnixMillis < 0 || value.UpdatedAtUnixMillis > maxJSONSafeInteger {
		return errors.New("invalid emergency stop timestamp")
	}
	return nil
}

func restoreOperation(
	value persistedOperation,
	fileVersion string,
) (*operationState, error) {
	legacyUnbound := value.Request.Binding == nil ||
		value.Request.Invocation != nil ||
		fileVersion == legacyOperationFileVersion
	if err := validateStoredRequest(value.Request, legacyUnbound); err != nil {
		return nil, err
	}
	if !validOperationStatus(value.Status) {
		return nil, errors.New("unsupported operation status")
	}
	if value.CreatedAt < 0 ||
		value.UpdatedAt < value.CreatedAt ||
		value.UpdatedAt > maxJSONSafeInteger {
		return nil, errors.New("invalid operation timestamps")
	}
	if value.Ack != nil {
		if err := validateAcknowledgement(*value.Ack); err != nil {
			return nil, fmt.Errorf("ack: %w", err)
		}
		if value.Ack.OperationID != value.Request.OperationID {
			return nil, errors.New("ack operation_id does not match request")
		}
	}
	if value.Run != nil {
		if err := host.ValidateActionRun(*value.Run); err != nil {
			return nil, fmt.Errorf("run: %w", err)
		}
		if value.Run.OperationID != value.Request.OperationID {
			return nil, errors.New("run operation_id does not match request")
		}
	}
	if value.Outcome != nil {
		if err := host.ValidateActionOutcome(*value.Outcome); err != nil {
			return nil, fmt.Errorf("outcome: %w", err)
		}
		if value.Outcome.OperationID != value.Request.OperationID {
			return nil, errors.New("outcome operation_id does not match request")
		}
	}
	if err := validateOperationOutput(value.Output); err != nil {
		return nil, err
	}
	if err := validatePersistedOperationRelations(value); err != nil {
		return nil, err
	}

	request := cloneControlRequest(value.Request)
	operation := &operationState{
		request:     request,
		status:      value.Status,
		attempts:    value.Attempts,
		cancel:      value.Cancel,
		ack:         cloneAcknowledgement(value.Ack),
		run:         cloneRunPointer(value.Run),
		outcome:     cloneOutcomePointer(value.Outcome),
		output:      append(json.RawMessage(nil), value.Output...),
		idempotency: operationIdempotencyKey(request),
		createdAt:   value.CreatedAt,
		updatedAt:   value.UpdatedAt,
	}
	if operation.ack != nil && !operation.ack.Accepted {
		operation.rejection = *operation.ack
		operation.status = OperationRejected
	} else if operation.outcome != nil {
		operation.status = operationStatusFromRun(operation.outcome.Status)
	} else if request.Kind == ControlAction &&
		request.PolicyDecision != nil &&
		request.PolicyDecision.Result == policy.Deny {
		operation.status = OperationRejected
		operation.rejection = HostAcknowledgement{
			OperationID: request.OperationID,
			Accepted:    false,
			Code:        request.PolicyDecision.ReasonCode,
			Message:     request.PolicyDecision.HumanSummary,
		}
	} else if operation.status == OperationCancelled &&
		operation.attempts == 0 &&
		operation.ack == nil {
		operation.status = OperationCancelled
	} else if operation.ack != nil && operation.ack.Accepted {
		if !legacyUnbound && operation.run == nil &&
			value.Status == OperationAccepted {
			operation.status = OperationAccepted
		} else {
			operation.status = OperationOutcomeUnknown
		}
	} else if legacyUnbound || value.Status == OperationStale ||
		request.Kind == ControlAction {
		operation.status = OperationStale
	} else {
		operation.status = OperationQueued
	}
	return operation, nil
}

func validateStoredRequest(request HostControlRequest, allowLegacy bool) error {
	if err := validateControlTarget(
		request.RequestID,
		request.HostID,
		request.WorldID,
		request.ActorID,
	); err != nil {
		return err
	}
	if err := validateID("operation_id", request.OperationID); err != nil {
		return err
	}
	if err := host.ValidatePrincipal(request.Principal); err != nil {
		return fmt.Errorf("principal: %w", err)
	}
	if request.SubmittedAt < 0 || request.SubmittedAt > maxJSONSafeInteger {
		return errors.New("invalid submitted_at_unix_millis")
	}
	if request.Binding == nil {
		if !allowLegacy {
			return errors.New("request binding is required")
		}
	} else {
		if err := request.Binding.Epoch.Validate("binding.epoch"); err != nil {
			return fmt.Errorf("binding.epoch: %w", err)
		}
		if request.Binding.Epoch.WorldID != request.WorldID {
			return errors.New("binding epoch does not match request world")
		}
		if request.Binding.ObservationSeq == 0 ||
			request.Binding.ObservationSeq > maxJSONSafeInteger {
			return errors.New("invalid binding observation_seq")
		}
		if request.Binding.AuthorityRevision == 0 {
			if !allowLegacy {
				return errors.New("binding authority_revision is required")
			}
		} else if request.Binding.AuthorityRevision > maxJSONSafeInteger {
			return errors.New("invalid binding authority_revision")
		}
		if request.Binding.ControllerLeaseID != "" {
			if err := validateID(
				"binding.controller_lease_id",
				request.Binding.ControllerLeaseID,
			); err != nil {
				return err
			}
		}
	}
	if request.TurnID != "" {
		if err := validateID("turn_id", request.TurnID); err != nil {
			return err
		}
	}
	switch request.Kind {
	case ControlMessage, ControlDirective, ControlUtterance:
		if request.Invocation != nil || request.Offer != nil ||
			request.ActionRequest != nil || request.BoundAction != nil ||
			request.PolicyDecision != nil || request.ParentOperationID != "" ||
			(request.Binding != nil && request.Binding.ControllerLeaseID != "") {
			return errors.New("text request contains action-only fields")
		}
		if err := validateText(
			"text",
			request.Text,
			maxControlTextBytes,
			true,
		); err != nil {
			return err
		}
		if request.Kind != ControlUtterance && request.TurnID != "" {
			return errors.New("inbound text request must not contain turn_id")
		}
		requiredScope := ScopeActorConverse
		if request.Kind == ControlDirective {
			requiredScope = ScopeActorDirect
		} else if request.Kind == ControlUtterance {
			requiredScope = ScopeActorSpeak
			if request.TurnID == "" {
				return errors.New("utterance request requires turn_id")
			}
		}
		if !hasScope(request.Principal, ScopeHostAdmin) &&
			!hasScope(request.Principal, requiredScope) {
			return errors.New("principal is missing the request scope")
		}
	case ControlOffer:
		if request.Text != "" || request.ActionRequest != nil ||
			request.BoundAction != nil || request.PolicyDecision != nil ||
			request.ParentOperationID != "" ||
			(request.Binding != nil && request.Binding.ControllerLeaseID != "") {
			return errors.New("offer request contains action-only fields")
		}
		if request.Offer != nil {
			if request.Invocation != nil {
				return errors.New("offer request cannot contain both offer and invocation")
			}
			if err := host.ValidateActionOffer(*request.Offer); err != nil {
				return fmt.Errorf("offer: %w", err)
			}
			if request.Binding == nil ||
				request.Offer.ActorID != request.ActorID ||
				request.Offer.ExpectedEpoch != request.Binding.Epoch ||
				request.Offer.ObservationSeq != request.Binding.ObservationSeq {
				return errors.New("offer does not match request binding")
			}
		} else if request.Invocation != nil && allowLegacy {
			if err := host.ValidateActionInvocation(*request.Invocation); err != nil {
				return fmt.Errorf("legacy invocation: %w", err)
			}
			if request.Invocation.OperationID != request.OperationID ||
				request.Invocation.ActorID != request.ActorID ||
				request.Invocation.ExpectedEpoch.WorldID != request.WorldID {
				return errors.New("legacy invocation does not match request")
			}
		} else {
			return errors.New("offer request requires a Host-published offer")
		}
		if !hasScope(request.Principal, ScopeHostAdmin) &&
			!hasScope(request.Principal, ScopeActorExecute) {
			return errors.New("principal is missing actor.execute")
		}
	case ControlAction:
		if err := validateStoredActionRequest(request); err != nil {
			return err
		}
	default:
		return errors.New("unsupported control kind")
	}
	return nil
}

func validateStoredActionRequest(request HostControlRequest) error {
	if request.Text != "" || request.TurnID != "" || request.Offer != nil ||
		request.Invocation != nil {
		return errors.New("action request contains legacy control fields")
	}
	if request.Binding == nil || request.Binding.ControllerLeaseID == "" {
		return errors.New("action request requires a controller-bound binding")
	}
	if request.ActionRequest == nil || request.BoundAction == nil ||
		request.PolicyDecision == nil {
		return errors.New("action request requires intent, binding, and policy decision")
	}
	if err := host.ValidateActionRequest(*request.ActionRequest); err != nil {
		return fmt.Errorf("action_request: %w", err)
	}
	if err := host.ValidateBoundAction(*request.BoundAction); err != nil {
		return fmt.Errorf("bound_action: %w", err)
	}
	if err := policy.ValidateDecision(*request.PolicyDecision); err != nil {
		return fmt.Errorf("policy_decision: %w", err)
	}
	actionRequest := request.ActionRequest
	bound := request.BoundAction
	decision := request.PolicyDecision
	digest, err := host.ActionRequestDigest(*actionRequest)
	if err != nil {
		return err
	}
	if actionRequest.RequestID != request.RequestID ||
		actionRequest.ActorID != request.ActorID ||
		actionRequest.ExpectedEpoch != request.Binding.Epoch ||
		actionRequest.ObservationSeq != request.Binding.ObservationSeq ||
		bound.RequestDigest != digest || bound.RequestID != actionRequest.RequestID ||
		bound.ControllerID != actionRequest.ControllerID ||
		bound.ActorID != actionRequest.ActorID ||
		bound.Capability != actionRequest.Capability ||
		bound.SpecDigest != actionRequest.SpecDigest ||
		bound.ExpectedEpoch != actionRequest.ExpectedEpoch ||
		bound.ObservationSeq != actionRequest.ObservationSeq ||
		bound.TaskID != actionRequest.TaskID ||
		bound.IdempotencyKey != actionRequest.IdempotencyKey ||
		!slices.Equal(bound.RequestedTargets, actionRequest.Targets) {
		return errors.New("action request, binding, and control target do not match")
	}
	if decision.ControllerID != bound.ControllerID ||
		decision.ActorID != bound.ActorID ||
		decision.PrincipalID != request.Principal.ID ||
		decision.EffectDigest != bound.EffectDigest {
		return errors.New("policy decision does not match bound action")
	}
	if request.ParentOperationID != "" {
		if err := validateID("parent_operation_id", request.ParentOperationID); err != nil {
			return err
		}
		if request.ParentOperationID == request.OperationID {
			return errors.New("operation cannot be its own parent")
		}
	}
	if !hasScope(request.Principal, ScopeHostAdmin) &&
		!hasScope(request.Principal, ScopeActorExecute) {
		return errors.New("principal is missing actor.execute")
	}
	return nil
}

func validatePersistedOperationRelations(value persistedOperation) error {
	if value.Request.Kind == ControlAction {
		decision := value.Request.PolicyDecision
		if decision == nil {
			return errors.New("action operation is missing policy decision")
		}
		switch decision.Result {
		case policy.Deny:
			if value.Status != OperationRejected || value.Attempts != 0 ||
				value.Ack != nil || value.Run != nil || value.Outcome != nil ||
				len(value.Output) != 0 {
				return errors.New("denied action has execution state")
			}
			return nil
		case policy.RequireConfirmation:
			validStatus := value.Status == OperationAwaitingConfirmation ||
				value.Status == OperationCancelled || value.Status == OperationStale
			if !validStatus || value.Attempts != 0 || value.Ack != nil ||
				value.Run != nil || value.Outcome != nil || len(value.Output) != 0 {
				return errors.New("unconfirmed action has execution state")
			}
			return nil
		case policy.Allow:
			if value.Status == OperationAwaitingConfirmation {
				return errors.New("allowed action cannot await confirmation")
			}
		default:
			return errors.New("unsupported action policy result")
		}
	}
	if value.Ack != nil && value.Attempts == 0 {
		return errors.New("acknowledgement requires a delivery attempt")
	}
	if value.Ack != nil && !value.Ack.Accepted {
		if value.Status != OperationRejected ||
			value.Run != nil ||
			value.Outcome != nil ||
			len(value.Output) != 0 {
			return errors.New("rejected operation has inconsistent execution state")
		}
		return nil
	}
	if value.Ack != nil && value.Ack.Accepted {
		valid := value.Status == OperationAccepted ||
			value.Status == OperationRunning ||
			value.Status == OperationSucceeded ||
			value.Status == OperationFailed ||
			value.Status == OperationCancelled ||
			value.Status == OperationInterrupted ||
			value.Status == OperationStale ||
			value.Status == OperationOutcomeUnknown
		if !valid {
			return errors.New("accepted operation has an invalid status")
		}
	}
	if value.Run != nil || value.Outcome != nil {
		if value.Ack == nil || !value.Ack.Accepted {
			return errors.New("Host execution state requires an accepted acknowledgement")
		}
	}
	if len(value.Output) != 0 && value.Outcome == nil {
		return errors.New("operation output requires a terminal outcome")
	}
	if value.Outcome != nil &&
		value.Status != operationStatusFromRun(value.Outcome.Status) {
		return errors.New("outcome does not match operation status")
	}
	if value.Outcome != nil && value.Request.Binding != nil &&
		value.Outcome.Epoch != value.Request.Binding.Epoch {
		return errors.New("outcome epoch does not match request binding")
	}
	if value.Outcome != nil && value.Request.Binding != nil &&
		value.Outcome.WorldSeq < value.Request.Binding.ObservationSeq {
		return errors.New("outcome world sequence predates request binding")
	}
	if value.Run != nil {
		runStatus := operationStatusFromRun(value.Run.Status)
		if value.Outcome == nil && value.Status != runStatus {
			return errors.New("run does not match operation status")
		}
		if value.Outcome != nil && terminalOperationStatus(runStatus) &&
			value.Run.Status != value.Outcome.Status {
			return errors.New("terminal run conflicts with outcome")
		}
	}
	if value.Ack == nil {
		if value.Status == OperationDelivered && value.Attempts == 0 {
			return errors.New("delivered operation requires a delivery attempt")
		}
		valid := value.Status == OperationQueued ||
			value.Status == OperationAwaitingConfirmation ||
			value.Status == OperationDelivered ||
			value.Status == OperationStale ||
			(value.Status == OperationCancelled && value.Attempts == 0)
		if !valid {
			return errors.New("unacknowledged operation has an invalid status")
		}
	}
	return nil
}

func (service *Service) persistedOperationsLocked() persistedOperations {
	operations := make([]*operationState, 0, len(service.operations))
	for _, operation := range service.operations {
		operations = append(operations, operation)
	}
	slices.SortFunc(operations, func(left, right *operationState) int {
		if left.createdAt < right.createdAt {
			return -1
		}
		if left.createdAt > right.createdAt {
			return 1
		}
		return compare(left.request.OperationID, right.request.OperationID)
	})
	state := persistedOperations{
		Version:        operationFileVersion,
		Operations:     make([]persistedOperation, len(operations)),
		Controllers:    make([]ControllerLease, 0, len(service.controllers)),
		EmergencyStops: make([]ActorEmergencyStop, 0, len(service.emergencyStops)),
	}
	if service.policyEngine != nil {
		decisionIDs := make([]string, 0, len(service.operations))
		for _, operation := range service.operations {
			if operation.request.Kind == ControlAction &&
				operation.request.PolicyDecision != nil &&
				operation.request.PolicyDecision.Result == policy.Allow {
				decisionIDs = append(
					decisionIDs,
					operation.request.PolicyDecision.DecisionID,
				)
			}
		}
		checkpoint := service.policyEngine.SnapshotStateFor(decisionIDs)
		state.PolicyState = &checkpoint
	}
	for index, operation := range operations {
		state.Operations[index] = persistedOperation{
			Request:   cloneControlRequest(operation.request),
			Status:    operation.status,
			Attempts:  operation.attempts,
			Cancel:    operation.cancel,
			Ack:       cloneAcknowledgement(operation.ack),
			Run:       cloneRunPointer(operation.run),
			Outcome:   cloneOutcomePointer(operation.outcome),
			Output:    append(json.RawMessage(nil), operation.output...),
			CreatedAt: operation.createdAt,
			UpdatedAt: operation.updatedAt,
		}
	}
	for _, controller := range service.controllers {
		state.Controllers = append(state.Controllers, controller)
	}
	slices.SortFunc(state.Controllers, func(left, right ControllerLease) int {
		if left.HostID != right.HostID {
			return compare(left.HostID, right.HostID)
		}
		if left.WorldID != right.WorldID {
			return compare(left.WorldID, right.WorldID)
		}
		return compare(left.ActorID, right.ActorID)
	})
	for _, stop := range service.emergencyStops {
		state.EmergencyStops = append(state.EmergencyStops, stop)
	}
	slices.SortFunc(state.EmergencyStops, func(left, right ActorEmergencyStop) int {
		if left.HostID != right.HostID {
			return compare(left.HostID, right.HostID)
		}
		if left.WorldID != right.WorldID {
			return compare(left.WorldID, right.WorldID)
		}
		return compare(left.ActorID, right.ActorID)
	})
	return state
}

func (service *Service) persistOperationsLocked() error {
	return service.persistOperationsWithLimitLocked(maxOperationFileBytes)
}

func (service *Service) persistOperationsWithLimitLocked(
	maximumBytes int64,
) error {
	if service.closed {
		return ErrClosed
	}
	if !service.operationDirty {
		return nil
	}
	return service.writeOperationsLocked(maximumBytes)
}

func (service *Service) flushOperationsLocked() error {
	if service.closed {
		return ErrClosed
	}
	if !service.operationDirty && !service.operationCheckpointDirty {
		return nil
	}
	return service.writeOperationsLocked(maxOperationFileBytes)
}

func (service *Service) writeOperationsLocked(maximumBytes int64) error {
	if service.operationFile == nil {
		service.operationDirty = false
		service.operationCheckpointDirty = false
		return nil
	}
	service.expireControllersLocked(service.now().UnixMilli())
	if err := service.operationFile.write(
		service.persistedOperationsLocked(),
		maximumBytes,
	); err != nil {
		return err
	}
	service.operationDirty = false
	service.operationCheckpointDirty = false
	return nil
}

type boundedOperationWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *boundedOperationWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		return 0, errOperationFileTooLarge
	}
	count, err := writer.writer.Write(value)
	writer.remaining -= int64(count)
	return count, err
}

func (service *Service) markOperationsDirtyLocked() {
	service.operationDirty = true
	service.notifyLocked()
}

// Delivery counters and run progress may be lost on a crash. Recovery
// redelivers work with no execution evidence and reconciles work with a run.
func (service *Service) markOperationCheckpointDirtyLocked() {
	service.operationCheckpointDirty = true
	service.notifyLocked()
}

func cloneAcknowledgement(
	value *HostAcknowledgement,
) *HostAcknowledgement {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRunPointer(value *host.ActionRun) *host.ActionRun {
	if value == nil {
		return nil
	}
	cloned := cloneRun(*value)
	return &cloned
}

func cloneOutcomePointer(value *host.ActionOutcome) *host.ActionOutcome {
	if value == nil {
		return nil
	}
	cloned := cloneOutcome(*value)
	return &cloned
}

func validOperationStatus(value OperationStatus) bool {
	return value == OperationQueued ||
		value == OperationAwaitingConfirmation ||
		value == OperationDelivered ||
		value == OperationAccepted ||
		value == OperationRunning ||
		value == OperationSucceeded ||
		value == OperationFailed ||
		value == OperationCancelled ||
		value == OperationInterrupted ||
		value == OperationStale ||
		value == OperationOutcomeUnknown ||
		value == OperationRejected
}

func decodeSingleJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON must contain one document")
	}
	return nil
}
