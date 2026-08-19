package managementapi

import (
	"context"
	"errors"
	"strings"

	"github.com/sunrioa/rin/cognition"
)

type SkillStore interface {
	cognition.SkillWriter
	Reload(context.Context) error
	Import(context.Context, []byte) (cognition.Skill, error)
	Remove(context.Context, string, string) error
}

type SkillListInput struct {
	Adapter string `json:"adapter,omitempty"`
	Limit   uint32 `json:"limit,omitempty"`
}

type SkillListOutput struct {
	Skills []cognition.SkillSummary `json:"skills"`
}

type SkillGetInput struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
}

type SkillGetOutput struct {
	Skill cognition.Skill `json:"skill"`
}

type SkillSaveInput struct {
	SkillID      string   `json:"skill_id"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Triggers     []string `json:"triggers,omitempty"`
	Adapters     []string `json:"adapters,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type SkillImportInput struct {
	Document string `json:"document"`
}

type SkillRemoveInput struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
}

type SkillReloadOutput struct {
	Reloaded bool `json:"reloaded"`
}

func (service *Service) ConfigureSkills(
	catalog cognition.SkillProvider,
	store SkillStore,
) error {
	if catalog == nil || store == nil {
		return errors.New("skill catalog and writable store are required")
	}
	service.skills = catalog
	service.skillStore = store
	return nil
}

func (service *Service) ListSkills(
	ctx context.Context,
	input SkillListInput,
) (SkillListOutput, error) {
	if service.skills == nil {
		return SkillListOutput{}, ErrSkillsUnavailable
	}
	if input.Limit == 0 {
		input.Limit = 128
	}
	listed, err := service.skills.ListSkills(ctx, cognition.SkillQuery{
		Adapter: strings.TrimSpace(input.Adapter), Limit: input.Limit,
	})
	if err != nil {
		return SkillListOutput{}, err
	}
	return SkillListOutput{Skills: listed}, nil
}

func (service *Service) GetSkill(
	ctx context.Context,
	input SkillGetInput,
) (SkillGetOutput, error) {
	if service.skills == nil {
		return SkillGetOutput{}, ErrSkillsUnavailable
	}
	skill, err := service.skills.DescribeSkill(
		ctx, strings.TrimSpace(input.SkillID), strings.TrimSpace(input.Version),
	)
	if err != nil {
		return SkillGetOutput{}, err
	}
	return SkillGetOutput{Skill: skill}, nil
}

func (service *Service) SaveSkill(
	ctx context.Context,
	input SkillSaveInput,
) (SkillGetOutput, error) {
	if service.skills == nil || service.skillStore == nil {
		return SkillGetOutput{}, ErrSkillsUnavailable
	}
	input.SkillID = strings.TrimSpace(input.SkillID)
	input.Version = strings.TrimSpace(input.Version)
	if input.Version == "" {
		input.Version = "v1"
	}
	current, err := service.skills.DescribeSkill(ctx, input.SkillID, input.Version)
	if err == nil && current.Source != "learned" {
		return SkillGetOutput{}, errors.New(
			"built-in and installed skills are read-only; use a new id or version",
		)
	}
	if err != nil && !errors.Is(err, cognition.ErrProviderNotFound) {
		return SkillGetOutput{}, err
	}
	skill := cognition.Skill{SkillSummary: cognition.SkillSummary{
		SkillID: input.SkillID, Version: input.Version,
		Summary: strings.TrimSpace(input.Description), Source: "learned",
		Triggers: input.Triggers, Adapters: input.Adapters,
		Capabilities: input.Capabilities,
	}, Instructions: strings.TrimSpace(input.Instructions)}
	if err := service.skillStore.Save(ctx, skill); err != nil {
		return SkillGetOutput{}, err
	}
	stored, err := service.skills.DescribeSkill(ctx, input.SkillID, input.Version)
	if err != nil {
		return SkillGetOutput{}, err
	}
	return SkillGetOutput{Skill: stored}, nil
}

func (service *Service) ReloadSkills(ctx context.Context) (SkillReloadOutput, error) {
	if service.skillStore == nil {
		return SkillReloadOutput{}, ErrSkillsUnavailable
	}
	if err := service.skillStore.Reload(ctx); err != nil {
		return SkillReloadOutput{}, err
	}
	return SkillReloadOutput{Reloaded: true}, nil
}

func (service *Service) ImportSkill(
	ctx context.Context,
	input SkillImportInput,
) (SkillGetOutput, error) {
	if service.skills == nil || service.skillStore == nil {
		return SkillGetOutput{}, ErrSkillsUnavailable
	}
	if strings.TrimSpace(input.Document) == "" {
		return SkillGetOutput{}, errors.New("SKILL.md document is required")
	}
	skill, err := service.skillStore.Import(ctx, []byte(input.Document))
	if err != nil {
		return SkillGetOutput{}, err
	}
	resolved, err := service.skills.DescribeSkill(ctx, skill.SkillID, skill.Version)
	if err == nil {
		return SkillGetOutput{Skill: resolved}, nil
	}
	rollbackErr := service.skillStore.Remove(ctx, skill.SkillID, skill.Version)
	if rollbackErr != nil {
		return SkillGetOutput{}, errors.Join(err, rollbackErr)
	}
	if errors.Is(err, cognition.ErrProviderConflict) {
		return SkillGetOutput{}, errors.New(
			"imported skill conflicts with an existing skill id and version",
		)
	}
	return SkillGetOutput{}, err
}

func (service *Service) RemoveSkill(
	ctx context.Context,
	input SkillRemoveInput,
) (SkillReloadOutput, error) {
	if service.skills == nil || service.skillStore == nil {
		return SkillReloadOutput{}, ErrSkillsUnavailable
	}
	skillID := strings.TrimSpace(input.SkillID)
	version := strings.TrimSpace(input.Version)
	current, err := service.skills.DescribeSkill(ctx, skillID, version)
	if err != nil {
		return SkillReloadOutput{}, err
	}
	if current.Source != "learned" {
		return SkillReloadOutput{}, errors.New("only learned skills can be removed")
	}
	if err := service.skillStore.Remove(ctx, skillID, version); err != nil {
		return SkillReloadOutput{}, err
	}
	return SkillReloadOutput{Reloaded: true}, nil
}
