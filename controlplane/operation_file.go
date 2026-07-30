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
)

const (
	operationFileVersion       = "rin.control.operations/v2"
	legacyOperationFileVersion = "rin.control.operations/v1"
	operationFileName          = "operations.json"
	maxOperationFileBytes      = 64 << 20
	maxQueuedStateBytes        = 32 << 20
)

var errOperationFileTooLarge = errors.New("operation state exceeds its size limit")

type operationFile struct {
	root     string
	path     string
	lockFile *os.File
}

type persistedOperations struct {
	Version    string               `json:"version"`
	Operations []persistedOperation `json:"operations"`
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
	if info.Mode().Perm()&0o077 != 0 {
		return persistedOperations{}, fmt.Errorf(
			"%w: operation state permissions must not grant group or world access",
			ErrPersistence,
		)
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
		request:  request,
		status:   value.Status,
		attempts: value.Attempts,
		cancel:   value.Cancel,
		ack:      cloneAcknowledgement(value.Ack),
		run:      cloneRunPointer(value.Run),
		outcome:  cloneOutcomePointer(value.Outcome),
		output:   append(json.RawMessage(nil), value.Output...),
		idempotency: operationRequestKey(
			request.Principal.ID,
			request.RequestID,
		),
		createdAt: value.CreatedAt,
		updatedAt: value.UpdatedAt,
	}
	if operation.ack != nil && !operation.ack.Accepted {
		operation.rejection = *operation.ack
		operation.status = OperationRejected
	} else if operation.outcome != nil {
		operation.status = operationStatusFromRun(operation.outcome.Status)
	} else if operation.status == OperationCancelled &&
		operation.attempts == 0 &&
		operation.ack == nil {
		operation.status = OperationCancelled
	} else if operation.ack != nil && operation.ack.Accepted {
		operation.status = OperationOutcomeUnknown
	} else if legacyUnbound {
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
	}
	switch request.Kind {
	case ControlMessage, ControlDirective:
		if request.Invocation != nil || request.Offer != nil {
			return errors.New("text request must not contain an offer or invocation")
		}
		if err := validateText(
			"text",
			request.Text,
			maxControlTextBytes,
			true,
		); err != nil {
			return err
		}
		requiredScope := ScopeActorConverse
		if request.Kind == ControlDirective {
			requiredScope = ScopeActorDirect
		}
		if !hasScope(request.Principal, ScopeHostAdmin) &&
			!hasScope(request.Principal, requiredScope) {
			return errors.New("principal is missing the request scope")
		}
	case ControlOffer:
		if request.Text != "" {
			return errors.New("offer request must not contain text")
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
	default:
		return errors.New("unsupported control kind")
	}
	return nil
}

func validatePersistedOperationRelations(value persistedOperation) error {
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
	if value.Ack == nil {
		if value.Status == OperationDelivered && value.Attempts == 0 {
			return errors.New("delivered operation requires a delivery attempt")
		}
		valid := value.Status == OperationQueued ||
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
		Version:    operationFileVersion,
		Operations: make([]persistedOperation, len(operations)),
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

// Delivery counters and run progress may be lost on a crash; recovery already
// redelivers unacknowledged work or marks acknowledged work outcome-unknown.
func (service *Service) markOperationCheckpointDirtyLocked() {
	service.operationCheckpointDirty = true
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
