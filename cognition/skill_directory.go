package cognition

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

const (
	maxSkillDocumentBytes = 64 << 10
	maxDirectorySkills    = 1_024
)

type DirectorySkillProvider struct {
	mu     sync.RWMutex
	root   string
	source string
	skills map[string]Skill
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Rin struct {
			Version      any      `yaml:"version"`
			Adapters     []string `yaml:"adapters"`
			Capabilities []string `yaml:"capabilities"`
			Triggers     []string `yaml:"triggers"`
		} `yaml:"rin"`
	} `yaml:"metadata"`
}

func OpenDirectorySkillProvider(
	root, source string,
	create bool,
) (*DirectorySkillProvider, error) {
	if err := validateProviderID("source", source); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return nil, fmt.Errorf("create skill directory: %w", err)
		}
	}
	provider := &DirectorySkillProvider{root: absolute, source: source}
	if err := provider.Reload(context.Background()); err != nil {
		return nil, err
	}
	return provider, nil
}

func (provider *DirectorySkillProvider) Reload(ctx context.Context) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	entries, err := os.ReadDir(provider.root)
	if err != nil {
		return fmt.Errorf("read skill directory: %w", err)
	}
	if len(entries) > maxDirectorySkills {
		return ErrProviderCapacity
	}
	loaded := make(map[string]Skill, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := validateProviderID("skill directory", name); err != nil {
			return err
		}
		documentPath := filepath.Join(provider.root, name, "SKILL.md")
		info, err := os.Lstat(documentPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() > maxSkillDocumentBytes {
			return fmt.Errorf("skill %s has an invalid SKILL.md", name)
		}
		payload, err := os.ReadFile(documentPath)
		if err != nil {
			return err
		}
		skill, err := parseSkillDocument(payload, provider.source)
		if err != nil {
			return fmt.Errorf("skill %s: %w", name, err)
		}
		if skill.SkillID != name {
			return fmt.Errorf("skill directory %s does not match frontmatter name %s", name, skill.SkillID)
		}
		key := providerKey(skill.SkillID, skill.Version)
		if _, duplicate := loaded[key]; duplicate {
			return ErrProviderConflict
		}
		loaded[key] = skill
	}
	provider.mu.Lock()
	provider.skills = loaded
	provider.mu.Unlock()
	return nil
}

func (provider *DirectorySkillProvider) ListSkills(
	ctx context.Context,
	query SkillQuery,
) ([]SkillSummary, error) {
	provider.mu.RLock()
	skills := make([]Skill, 0, len(provider.skills))
	for _, skill := range provider.skills {
		skills = append(skills, cloneSkill(skill))
	}
	provider.mu.RUnlock()
	local, err := NewLocalSkillProvider(skills)
	if err != nil {
		return nil, err
	}
	return local.ListSkills(ctx, query)
}

func (provider *DirectorySkillProvider) DescribeSkill(
	ctx context.Context,
	skillID, version string,
) (Skill, error) {
	if err := requireMemoryContext(ctx); err != nil {
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

func (provider *DirectorySkillProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func (provider *DirectorySkillProvider) Save(ctx context.Context, skill Skill) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	skill.Source = provider.source
	sealed, err := SealSkill(skill)
	if err != nil {
		return err
	}
	payload, err := formatSkillDocument(sealed)
	if err != nil {
		return err
	}
	directory := filepath.Join(provider.root, sealed.SkillID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("skill target directory is invalid")
	}
	target := filepath.Join(directory, "SKILL.md")
	temporary, err := os.CreateTemp(directory, ".SKILL.md.*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(payload)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return err
	}
	removeTemporary = false
	return provider.Reload(ctx)
}

func parseSkillDocument(payload []byte, source string) (Skill, error) {
	frontmatter, body, err := splitSkillDocument(payload)
	if err != nil {
		return Skill{}, err
	}
	var metadata skillFrontmatter
	if err := yaml.Unmarshal(frontmatter, &metadata); err != nil {
		return Skill{}, fmt.Errorf("decode frontmatter: %w", err)
	}
	version, err := normalizeSkillVersion(metadata.Metadata.Rin.Version)
	if err != nil {
		return Skill{}, err
	}
	return SealSkill(Skill{SkillSummary: SkillSummary{
		SkillID: strings.TrimSpace(metadata.Name), Version: version,
		Summary:  strings.TrimSpace(metadata.Description),
		Triggers: metadata.Metadata.Rin.Triggers, Adapters: metadata.Metadata.Rin.Adapters,
		Capabilities: metadata.Metadata.Rin.Capabilities, Source: source,
	}, Instructions: strings.TrimSpace(string(body))})
}

func splitSkillDocument(payload []byte) ([]byte, []byte, error) {
	normalized := bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return nil, nil, errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := bytes.Index(normalized[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, nil, errors.New("SKILL.md frontmatter is not closed")
	}
	end += 4
	frontmatter := normalized[4:end]
	body := normalized[end+5:]
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, errors.New("SKILL.md instructions are empty")
	}
	return frontmatter, body, nil
}

func normalizeSkillVersion(value any) (string, error) {
	version := "v1"
	switch typed := value.(type) {
	case nil:
	case string:
		version = strings.TrimSpace(typed)
	case int:
		version = strconv.Itoa(typed)
	case uint64:
		version = strconv.FormatUint(typed, 10)
	default:
		return "", errors.New("metadata.rin.version must be a string or integer")
	}
	if version == "" {
		version = "v1"
	}
	if version[0] >= '0' && version[0] <= '9' {
		version = "v" + version
	}
	if err := validateProviderID("metadata.rin.version", version); err != nil {
		return "", err
	}
	return version, nil
}

func formatSkillDocument(skill Skill) ([]byte, error) {
	metadata := skillFrontmatter{Name: skill.SkillID, Description: skill.Summary}
	metadata.Metadata.Rin.Version = strings.TrimPrefix(skill.Version, "v")
	metadata.Metadata.Rin.Adapters = append([]string(nil), skill.Adapters...)
	metadata.Metadata.Rin.Capabilities = append([]string(nil), skill.Capabilities...)
	metadata.Metadata.Rin.Triggers = append([]string(nil), skill.Triggers...)
	frontmatter, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(frontmatter) + "---\n\n" +
		strings.TrimSpace(skill.Instructions) + "\n"), nil
}

func (provider *DirectorySkillProvider) skillIDs() []string {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	result := make([]string, 0, len(provider.skills))
	for _, skill := range provider.skills {
		result = append(result, skill.SkillID)
	}
	slices.Sort(result)
	return result
}
