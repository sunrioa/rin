package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
)

type SkillSummary struct {
	SkillID  string   `json:"skill_id"`
	Version  string   `json:"version"`
	Summary  string   `json:"summary"`
	Triggers []string `json:"triggers,omitempty"`
	Source   string   `json:"source"`
	Digest   string   `json:"digest"`
}

// Skill contains inert procedural guidance. It intentionally has no scopes,
// executable entrypoint, provider credentials, or capability grant fields.
type Skill struct {
	SkillSummary
	Instructions string `json:"instructions"`
}

type SkillQuery struct {
	Tags  []string `json:"tags,omitempty"`
	Limit uint32   `json:"limit"`
}

type SkillProvider interface {
	ListSkills(context.Context, SkillQuery) ([]SkillSummary, error)
	DescribeSkill(context.Context, string, string) (Skill, error)
	Health(context.Context) ProviderHealth
}

type LocalSkillProvider struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

func NewLocalSkillProvider(skills []Skill) (*LocalSkillProvider, error) {
	provider := &LocalSkillProvider{skills: make(map[string]Skill, len(skills))}
	for index, skill := range skills {
		sealed, err := SealSkill(skill)
		if err != nil {
			return nil, fmt.Errorf("skills[%d]: %w", index, err)
		}
		key := providerKey(sealed.SkillID, sealed.Version)
		if _, exists := provider.skills[key]; exists {
			return nil, errors.New("skills contain a duplicate id and version")
		}
		provider.skills[key] = sealed
	}
	return provider, nil
}

func (provider *LocalSkillProvider) ListSkills(
	ctx context.Context,
	query SkillQuery,
) ([]SkillSummary, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if query.Limit == 0 {
		query.Limit = 32
	}
	if query.Limit > 128 {
		return nil, errors.New("skill query limit must not exceed 128")
	}
	tags, err := normalizeProviderIDs("tags", query.Tags, 32)
	if err != nil {
		return nil, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	result := make([]SkillSummary, 0, len(provider.skills))
	for _, skill := range provider.skills {
		if len(skill.Triggers) != 0 && !providerIDsIntersect(skill.Triggers, tags) {
			continue
		}
		result = append(result, cloneSkillSummary(skill.SkillSummary))
	}
	slices.SortFunc(result, func(left, right SkillSummary) int {
		if left.SkillID != right.SkillID {
			return compareString(left.SkillID, right.SkillID)
		}
		return compareString(left.Version, right.Version)
	})
	if len(result) > int(query.Limit) {
		result = result[:query.Limit]
	}
	return result, nil
}

func (provider *LocalSkillProvider) DescribeSkill(
	ctx context.Context,
	skillID, version string,
) (Skill, error) {
	if err := requireContext(ctx); err != nil {
		return Skill{}, err
	}
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	if err := validateProviderID("skill_id", skillID); err != nil {
		return Skill{}, err
	}
	if err := validateProviderID("version", version); err != nil {
		return Skill{}, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	skill, exists := provider.skills[providerKey(skillID, version)]
	if !exists {
		return Skill{}, ErrProviderNotFound
	}
	return cloneSkill(skill), nil
}

func (provider *LocalSkillProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func SealSkill(skill Skill) (Skill, error) {
	if err := validateProviderID("skill_id", skill.SkillID); err != nil {
		return Skill{}, err
	}
	if err := validateProviderID("version", skill.Version); err != nil {
		return Skill{}, err
	}
	if err := validateProviderText("summary", skill.Summary, 500, true); err != nil {
		return Skill{}, err
	}
	if err := validateProviderID("source", skill.Source); err != nil {
		return Skill{}, err
	}
	if err := validateProviderText("instructions", skill.Instructions, 16_000, true); err != nil {
		return Skill{}, err
	}
	triggers, err := normalizeProviderIDs("triggers", skill.Triggers, 32)
	if err != nil {
		return Skill{}, err
	}
	skill.Triggers = triggers
	claimed := skill.Digest
	skill.Digest = ""
	payload, err := json.Marshal(skill)
	if err != nil {
		return Skill{}, err
	}
	digest := sha256.Sum256(payload)
	skill.Digest = hex.EncodeToString(digest[:])
	if claimed != "" && claimed != skill.Digest {
		return Skill{}, errors.New("skill digest does not match content")
	}
	return skill, nil
}

func cloneSkillSummary(summary SkillSummary) SkillSummary {
	summary.Triggers = append([]string(nil), summary.Triggers...)
	return summary
}

func cloneSkill(skill Skill) Skill {
	skill.SkillSummary = cloneSkillSummary(skill.SkillSummary)
	return skill
}

func providerIDsIntersect(left, right []string) bool {
	for _, leftValue := range left {
		if slices.Contains(right, leftValue) {
			return true
		}
	}
	return false
}
