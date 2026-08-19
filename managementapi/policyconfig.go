package managementapi

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/policy"
)

var (
	ErrPolicyConfigUnavailable = errors.New("gameplay policy configuration management is not enabled")
	ErrInvalidPolicyConfig     = errors.New("invalid gameplay policy configuration")
	ErrPolicyConfigConflict    = errors.New("gameplay policy configuration revision conflict")
)

type PolicyConfigSnapshot struct {
	Configured bool          `json:"configured"`
	Config     policy.Config `json:"config"`
}

type PolicyConfigSaveRequest struct {
	ExpectedRevision uint64        `json:"expected_revision"`
	Config           policy.Config `json:"config"`
}

type PolicyConfigEditor interface {
	PolicyConfig(context.Context) (PolicyConfigSnapshot, error)
	SavePolicyConfig(context.Context, PolicyConfigSaveRequest) (PolicyConfigSnapshot, error)
}

func (service *Service) ConfigurePolicyConfig(editor PolicyConfigEditor) error {
	if editor == nil {
		return errors.New("gameplay policy configuration editor is required")
	}
	service.policyConfig = editor
	return nil
}

func (service *Service) PolicyConfig(ctx context.Context) (PolicyConfigSnapshot, error) {
	if service.policyConfig == nil {
		return PolicyConfigSnapshot{}, ErrPolicyConfigUnavailable
	}
	return service.policyConfig.PolicyConfig(ctx)
}

func (service *Service) SavePolicyConfig(
	ctx context.Context,
	request PolicyConfigSaveRequest,
) (PolicyConfigSnapshot, error) {
	if service.policyConfig == nil {
		return PolicyConfigSnapshot{}, ErrPolicyConfigUnavailable
	}
	return service.policyConfig.SavePolicyConfig(ctx, request)
}
