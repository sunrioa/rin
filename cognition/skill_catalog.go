package cognition

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
)

func OpenDefaultSkillCatalog(
	dataDir string,
	builtin []Skill,
) (*SkillCatalog, *DirectorySkillProvider, error) {
	inline, err := NewLocalSkillProvider(builtin)
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Join(dataDir, "skills")
	installed, err := OpenDirectorySkillProvider(
		filepath.Join(root, "installed"), "installed", true,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open installed skills: %w", err)
	}
	learned, err := OpenDirectorySkillProvider(
		filepath.Join(root, "learned"), "learned", true,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open learned skills: %w", err)
	}
	catalog, err := NewSkillCatalog(inline, installed, learned)
	if err != nil {
		return nil, nil, err
	}
	return catalog, learned, nil
}

// SkillCatalog presents multiple storage providers as one deterministic,
// read-only catalog. Providers keep ownership of loading and persistence.
type SkillCatalog struct {
	providers []SkillProvider
}

func NewSkillCatalog(providers ...SkillProvider) (*SkillCatalog, error) {
	filtered := make([]SkillProvider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			filtered = append(filtered, provider)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("skill catalog requires at least one provider")
	}
	return &SkillCatalog{providers: filtered}, nil
}

func (catalog *SkillCatalog) ListSkills(
	ctx context.Context,
	query SkillQuery,
) ([]SkillSummary, error) {
	limit := query.Limit
	if limit == 0 {
		limit = 32
	}
	if limit > 128 {
		return nil, errors.New("skill query limit must not exceed 128")
	}
	providerQuery := query
	providerQuery.Limit = 128
	merged := make(map[string]SkillSummary)
	for _, source := range catalog.providers {
		summaries, err := source.ListSkills(ctx, providerQuery)
		if err != nil {
			return nil, err
		}
		for _, summary := range summaries {
			key := providerKey(summary.SkillID, summary.Version)
			if current, exists := merged[key]; exists && current.Digest != summary.Digest {
				return nil, ErrProviderConflict
			}
			merged[key] = cloneSkillSummary(summary)
		}
	}
	result := make([]SkillSummary, 0, len(merged))
	for _, summary := range merged {
		result = append(result, summary)
	}
	slices.SortFunc(result, func(left, right SkillSummary) int {
		if left.SkillID != right.SkillID {
			return compareString(left.SkillID, right.SkillID)
		}
		return compareString(left.Version, right.Version)
	})
	if len(result) > int(limit) {
		result = result[:limit]
	}
	return result, nil
}

func (catalog *SkillCatalog) DescribeSkill(
	ctx context.Context,
	skillID, version string,
) (Skill, error) {
	var found *Skill
	for _, source := range catalog.providers {
		skill, err := source.DescribeSkill(ctx, skillID, version)
		if errors.Is(err, ErrProviderNotFound) {
			continue
		}
		if err != nil {
			return Skill{}, err
		}
		if found != nil && found.Digest != skill.Digest {
			return Skill{}, ErrProviderConflict
		}
		cloned := cloneSkill(skill)
		found = &cloned
	}
	if found == nil {
		return Skill{}, ErrProviderNotFound
	}
	return *found, nil
}

func (catalog *SkillCatalog) Health(ctx context.Context) ProviderHealth {
	for _, source := range catalog.providers {
		health := source.Health(ctx)
		if !health.Available {
			return ProviderHealth{Degraded: true, Code: "skill_provider_unavailable"}
		}
	}
	return ProviderHealth{Available: true}
}

func (catalog *SkillCatalog) Reload(ctx context.Context) error {
	for _, source := range catalog.providers {
		reloadable, ok := source.(interface{ Reload(context.Context) error })
		if ok {
			if err := reloadable.Reload(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
