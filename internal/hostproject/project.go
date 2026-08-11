package hostproject

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/portablepath"
)

const maxProjectFileBytes = 1 << 20

type Manifest struct {
	SchemaVersion int `json:"schema_version"`
	Generator     struct {
		Name            string `json:"name"`
		RinVersion      string `json:"rin_version"`
		ContractVersion string `json:"contract_version"`
		Deterministic   bool   `json:"deterministic"`
	} `json:"generator"`
	Project struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Runtime string `json:"runtime"`
	} `json:"project"`
	Host struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		TemplateStatus   string `json:"template_status"`
		RequiresGameHook bool   `json:"requires_game_hook"`
		RuntimePins      []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"runtime_pins"`
	} `json:"host"`
	SDK struct {
		Language string `json:"language"`
		Delivery string `json:"delivery"`
		License  string `json:"license"`
	} `json:"sdk"`
	CapabilityProfile  string `json:"capability_profile"`
	RealHostValidation string `json:"real_host_validation"`
	Files              []struct {
		Path   string `json:"path"`
		Role   string `json:"role"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

type Report struct {
	Root          string
	Manifest      Manifest
	CheckedFiles  int
	ModifiedFiles []string
	Capabilities  []host.CapabilitySpec
}

func Inspect(root string) (Report, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve host path: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return Report{}, fmt.Errorf("inspect host path: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Report{}, errors.New("host path must be a real directory")
	}
	var manifest Manifest
	if err := decodeFile(filepath.Join(absolute, "rin-scaffold.json"), &manifest); err != nil {
		return Report{}, fmt.Errorf("read rin-scaffold.json: %w", err)
	}
	if manifest.SchemaVersion != 2 || manifest.Generator.Name != "rin" ||
		!manifest.Generator.Deterministic {
		return Report{}, errors.New("unsupported or non-deterministic scaffold manifest")
	}
	if manifest.Generator.ContractVersion != host.ContractVersion {
		return Report{}, fmt.Errorf(
			"host contract %q does not match %q",
			manifest.Generator.ContractVersion, host.ContractVersion)
	}
	if manifest.Project.ID == "" || manifest.Project.Runtime == "" ||
		manifest.Host.ID != "custom" || manifest.SDK.Delivery != "contract-only" ||
		manifest.CapabilityProfile != "contract-only" ||
		manifest.RealHostValidation != "required" {
		return Report{}, errors.New("scaffold manifest has no project or host identity")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	var modifiedFiles []string
	for _, entry := range manifest.Files {
		if err := portablepath.ValidateProjectPath(
			absolute,
			entry.Path,
		); err != nil {
			return Report{}, fmt.Errorf(
				"manifest contains non-portable path %q: %w",
				entry.Path,
				err,
			)
		}
		key := strings.ToLower(entry.Path)
		if _, exists := seen[key]; exists {
			return Report{}, fmt.Errorf("manifest path %q collides on Windows", entry.Path)
		}
		seen[key] = struct{}{}
		if len(entry.SHA256) != 64 {
			return Report{}, fmt.Errorf("manifest digest for %q is invalid", entry.Path)
		}
		data, err := readBounded(filepath.Join(absolute, filepath.FromSlash(entry.Path)))
		if err != nil {
			return Report{}, fmt.Errorf("verify %q: %w", entry.Path, err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			modifiedFiles = append(modifiedFiles, entry.Path)
		}
	}
	capabilities, err := loadCapabilities(filepath.Join(absolute, "capabilities"))
	if err != nil {
		return Report{}, err
	}
	var config struct {
		SchemaVersion   int      `json:"schema_version"`
		ContractVersion string   `json:"contract_version"`
		ProjectID       string   `json:"project_id"`
		Engine          string   `json:"engine"`
		Runtime         string   `json:"runtime"`
		Durability      string   `json:"durability"`
		CapabilityPaths []string `json:"capability_paths"`
	}
	if err := decodeFile(filepath.Join(absolute, "rin-host.json"), &config); err != nil {
		return Report{}, fmt.Errorf("read rin-host.json: %w", err)
	}
	if config.SchemaVersion != 2 || config.ContractVersion != host.ContractVersion ||
		config.ProjectID != manifest.Project.ID || config.Engine != "custom" ||
		config.Runtime != manifest.Project.Runtime ||
		config.Durability != string(host.DurabilityAdvisory) ||
		len(config.CapabilityPaths) != 1 || config.CapabilityPaths[0] != "capabilities" {
		return Report{}, errors.New("rin-host.json does not match the scaffold manifest")
	}
	if len(capabilities) == 0 {
		return Report{}, errors.New("custom host must declare at least one capability")
	}
	return Report{
		Root: absolute, Manifest: manifest, CheckedFiles: len(manifest.Files),
		ModifiedFiles: modifiedFiles, Capabilities: capabilities,
	}, nil
}

func loadCapabilities(directory string) ([]host.CapabilitySpec, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read capabilities: %w", err)
	}
	var result []host.CapabilitySpec
	seen := make(map[host.CapabilityRef]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("capability %q must not be a symlink", entry.Name())
		}
		var spec host.CapabilitySpec
		if err := decodeFile(filepath.Join(directory, entry.Name()), &spec); err != nil {
			return nil, fmt.Errorf("decode capability %q: %w", entry.Name(), err)
		}
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("validate capability %q: %w", entry.Name(), err)
		}
		if previous, exists := seen[spec.Capability]; exists {
			return nil, fmt.Errorf(
				"capability %s@%s is duplicated in %q and %q",
				spec.Capability.ID, spec.Capability.Version,
				previous, entry.Name())
		}
		seen[spec.Capability] = entry.Name()
		result = append(result, spec)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Capability.ID != result[right].Capability.ID {
			return result[left].Capability.ID < result[right].Capability.ID
		}
		return result[left].Capability.Version < result[right].Capability.Version
	})
	return result, nil
}

func decodeFile(path string, value any) error {
	data, err := readBounded(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("file must contain exactly one JSON value")
	}
	return nil
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("path must be a regular file")
	}
	if info.Size() > maxProjectFileBytes {
		return nil, errors.New("file exceeds 1 MiB")
	}
	return os.ReadFile(path)
}
