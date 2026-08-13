// Package skillapi exposes the shared inert Skill Catalog to internal and
// external controllers without enabling the internal model runtime.
package skillapi

import (
	"context"
	"errors"
	"strings"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
)

const (
	ScopeSkillRead  = "skill.read"
	ScopeSkillWrite = "skill.write"
)

var (
	ErrForbidden = errors.New("skill catalog access forbidden")
	ErrInvalid   = errors.New("skill catalog request invalid")
)

type WritableSkillProvider interface {
	Save(context.Context, cognition.Skill) error
	Reload(context.Context) error
}

type Service struct {
	catalog cognition.SkillProvider
	writer  WritableSkillProvider
}

type ListInput struct {
	Tags                  []string `json:"tags,omitempty"`
	Adapter               string   `json:"adapter,omitempty"`
	AvailableCapabilities []string `json:"available_capabilities,omitempty"`
	Limit                 uint32   `json:"limit,omitempty"`
}

type ListOutput struct {
	Skills []cognition.SkillSummary `json:"skills"`
}

type GetInput struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
}

type GetOutput struct {
	Skill cognition.Skill `json:"skill"`
}

type SaveInput struct {
	SkillID      string   `json:"skill_id"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Triggers     []string `json:"triggers,omitempty"`
	Adapters     []string `json:"adapters,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type ReloadOutput struct {
	Reloaded bool `json:"reloaded"`
}

func New(catalog cognition.SkillProvider, writer WritableSkillProvider) (*Service, error) {
	if catalog == nil || writer == nil {
		return nil, errors.New("skill catalog and writable provider are required")
	}
	return &Service{catalog: catalog, writer: writer}, nil
}

func (service *Service) List(
	ctx context.Context,
	principal host.Principal,
	input ListInput,
) (ListOutput, error) {
	if !hasScope(principal, ScopeSkillRead) {
		return ListOutput{}, ErrForbidden
	}
	skills, err := service.catalog.ListSkills(ctx, cognition.SkillQuery{
		Tags: input.Tags, Adapter: strings.TrimSpace(input.Adapter),
		AvailableCapabilities: input.AvailableCapabilities, Limit: input.Limit,
	})
	if err != nil {
		return ListOutput{}, err
	}
	return ListOutput{Skills: skills}, nil
}

func (service *Service) Get(
	ctx context.Context,
	principal host.Principal,
	input GetInput,
) (GetOutput, error) {
	if !hasScope(principal, ScopeSkillRead) {
		return GetOutput{}, ErrForbidden
	}
	skill, err := service.catalog.DescribeSkill(
		ctx, strings.TrimSpace(input.SkillID), strings.TrimSpace(input.Version),
	)
	if err != nil {
		return GetOutput{}, err
	}
	return GetOutput{Skill: skill}, nil
}

func (service *Service) Save(
	ctx context.Context,
	principal host.Principal,
	input SaveInput,
) (GetOutput, error) {
	if !hasScope(principal, ScopeSkillWrite) {
		return GetOutput{}, ErrForbidden
	}
	version := strings.TrimSpace(input.Version)
	if version == "" {
		version = "v1"
	}
	skill := cognition.Skill{SkillSummary: cognition.SkillSummary{
		SkillID: strings.TrimSpace(input.SkillID), Version: version,
		Summary: strings.TrimSpace(input.Description), Source: "learned",
		Triggers: input.Triggers, Adapters: input.Adapters, Capabilities: input.Capabilities,
	}, Instructions: strings.TrimSpace(input.Instructions)}
	if err := service.writer.Save(ctx, skill); err != nil {
		return GetOutput{}, err
	}
	stored, err := service.catalog.DescribeSkill(ctx, skill.SkillID, skill.Version)
	if err != nil {
		return GetOutput{}, err
	}
	return GetOutput{Skill: stored}, nil
}

func (service *Service) Reload(
	ctx context.Context,
	principal host.Principal,
) (ReloadOutput, error) {
	if !hasScope(principal, ScopeSkillWrite) {
		return ReloadOutput{}, ErrForbidden
	}
	var reloader interface{ Reload(context.Context) error } = service.writer
	if catalogReloader, ok := service.catalog.(interface{ Reload(context.Context) error }); ok {
		reloader = catalogReloader
	}
	if err := reloader.Reload(ctx); err != nil {
		return ReloadOutput{}, err
	}
	return ReloadOutput{Reloaded: true}, nil
}

func hasScope(principal host.Principal, expected string) bool {
	for _, scope := range principal.GrantedScopes {
		if scope == expected {
			return true
		}
	}
	return false
}
