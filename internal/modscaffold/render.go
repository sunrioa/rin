package modscaffold

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/sunrioa/rin/protocol"
)

const manifestPath = "rin-scaffold.json"

type renderedFile struct {
	Path string
	Mode fs.FileMode
	Data []byte
	Role string
}

// PlannedFile exposes deterministic metadata without exposing mutable bytes.
type PlannedFile struct {
	Path   string
	Mode   fs.FileMode
	SHA256 string
	Role   string
}

// Plan is a fully rendered scaffold that has not touched the filesystem.
type Plan struct {
	options normalizedOptions
	files   []renderedFile
}

// Result describes a completed write.
type Result struct {
	Root      string
	Host      HostDescriptor
	ID        string
	Name      string
	Version   string
	FileCount int
}

// Render validates options and renders every file in memory.
func Render(options Options) (*Plan, error) {
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	var files []renderedFile
	switch normalized.Host {
	case HostFabric:
		files, err = renderFabric(normalized)
	case HostBepInExMono, HostBepInExIL2CPP:
		files, err = renderBepInEx(normalized)
	case HostLuanti:
		files, err = renderLuanti(normalized)
	default:
		err = fmt.Errorf("unsupported host %q", normalized.Host)
	}
	if err != nil {
		return nil, err
	}
	files = append(files,
		renderedFile{
			Path: ".editorconfig", Mode: 0o644, Role: "project-metadata",
			Data: []byte(editorConfig),
		},
		renderedFile{
			Path: ".gitignore", Mode: 0o644, Role: "project-metadata",
			Data: []byte(gitignoreFor(normalized.Host)),
		},
		renderedFile{
			Path: "LICENSE-RIN.txt", Mode: 0o644, Role: "rin-license",
			Data: []byte(rinLicense),
		},
		renderedFile{
			Path: "README.md", Mode: 0o644, Role: "documentation",
			Data: []byte(readmeEnglish(normalized)),
		},
		renderedFile{
			Path: "README.zh-CN.md", Mode: 0o644, Role: "documentation",
			Data: []byte(readmeChinese(normalized)),
		},
	)
	if err := validateRenderedFiles(files); err != nil {
		return nil, err
	}
	manifest, err := renderManifest(normalized, files)
	if err != nil {
		return nil, err
	}
	files = append(files, renderedFile{
		Path: manifestPath, Mode: 0o644, Data: manifest, Role: "scaffold-manifest",
	})
	if err := validateRenderedFiles(files); err != nil {
		return nil, err
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].Path < files[right].Path
	})
	return &Plan{options: normalized, files: files}, nil
}

// Files returns a stable, caller-owned view of the planned output.
func (plan *Plan) Files() []PlannedFile {
	result := make([]PlannedFile, 0, len(plan.files))
	for _, file := range plan.files {
		digest := sha256.Sum256(file.Data)
		result = append(result, PlannedFile{
			Path: file.Path, Mode: file.Mode,
			SHA256: hex.EncodeToString(digest[:]), Role: file.Role,
		})
	}
	return result
}

// Host returns a caller-owned description of the selected host.
func (plan *Plan) Host() HostDescriptor {
	return cloneHost(plan.options.HostDescriptor)
}

// ID returns the validated machine identifier.
func (plan *Plan) ID() string {
	return plan.options.ID
}

// Name returns the validated display name.
func (plan *Plan) Name() string {
	return plan.options.Name
}

// Version returns the generated Mod's version, distinct from the Rin version.
func (plan *Plan) Version() string {
	return plan.options.Version
}

// Output returns the requested relative output path.
func (plan *Plan) Output() string {
	return plan.options.Output
}

func validateRenderedFiles(files []renderedFile) error {
	if len(files) == 0 {
		return errors.New("scaffold rendered no files")
	}
	seen := make(map[string]string, len(files))
	for _, file := range files {
		if err := validateTemplatePath(file.Path); err != nil {
			return err
		}
		key := strings.ToLower(file.Path)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("template paths %q and %q collide on Windows", previous, file.Path)
		}
		seen[key] = file.Path
		if file.Mode != 0o644 && file.Mode != 0o755 {
			return fmt.Errorf("template file %q has unsupported mode %04o", file.Path, file.Mode)
		}
		if len(file.Data) == 0 {
			return fmt.Errorf("template file %q is empty", file.Path)
		}
	}
	return nil
}

type scaffoldManifest struct {
	SchemaVersion      int               `json:"schema_version"`
	Generator          generatorManifest `json:"generator"`
	Project            projectManifest   `json:"project"`
	Host               hostManifest      `json:"host"`
	SDK                sdkManifest       `json:"sdk"`
	CapabilityProfile  string            `json:"capability_profile"`
	RealHostValidation string            `json:"real_host_validation"`
	Files              []manifestFile    `json:"files"`
}

type generatorManifest struct {
	Name            string `json:"name"`
	RinVersion      string `json:"rin_version"`
	ProtocolVersion string `json:"protocol_version"`
	Deterministic   bool   `json:"deterministic"`
}

type projectManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Namespace   string `json:"namespace,omitempty"`
	JavaPackage string `json:"java_package,omitempty"`
	CodeName    string `json:"code_name,omitempty"`
	PluginGUID  string `json:"plugin_guid,omitempty"`
}

type hostManifest struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	TemplateStatus   string       `json:"template_status"`
	RequiresGameHook bool         `json:"requires_game_hook"`
	RuntimePins      []RuntimePin `json:"runtime_pins"`
}

type sdkManifest struct {
	Language string `json:"language"`
	Delivery string `json:"delivery"`
	License  string `json:"license"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
}

func renderManifest(options normalizedOptions, files []renderedFile) ([]byte, error) {
	entries := make([]manifestFile, 0, len(files))
	for _, file := range files {
		digest := sha256.Sum256(file.Data)
		entries = append(entries, manifestFile{
			Path: file.Path, Role: file.Role, SHA256: hex.EncodeToString(digest[:]),
		})
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
	project := projectManifest{
		ID: options.ID, Name: options.Name, Version: options.Version,
		Namespace: options.Namespace,
	}
	switch options.Host {
	case HostFabric:
		project.JavaPackage = options.JavaPackage
	case HostBepInExMono, HostBepInExIL2CPP:
		project.CodeName = options.CodeName
		project.PluginGUID = options.PluginGUID
	}
	manifest := scaffoldManifest{
		SchemaVersion: 1,
		Generator: generatorManifest{
			Name: "rin", RinVersion: options.RinVersion,
			ProtocolVersion: protocol.Version, Deterministic: true,
		},
		Project: project,
		Host: hostManifest{
			ID: options.HostDescriptor.ID, Name: options.HostDescriptor.Name,
			TemplateStatus:   options.HostDescriptor.TemplateStatus,
			RequiresGameHook: options.HostDescriptor.RequiresGameHook,
			RuntimePins:      append([]RuntimePin(nil), options.HostDescriptor.RuntimePins...),
		},
		SDK: sdkManifest{
			Language: options.HostDescriptor.Language,
			Delivery: "vendored-source",
			License:  "MIT; see LICENSE-RIN.txt",
		},
		CapabilityProfile:  "advisory",
		RealHostValidation: options.HostDescriptor.RealHostValidation,
		Files:              entries,
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode scaffold manifest: %w", err)
	}
	return append(payload, '\n'), nil
}

func replaceRequired(input, old, replacement, file string) (string, error) {
	if !strings.Contains(input, old) {
		return "", fmt.Errorf("template %s is missing required marker %q", file, old)
	}
	return strings.ReplaceAll(input, old, replacement), nil
}

func replaceCommands(commands []string, codeName string) []string {
	result := make([]string, len(commands))
	for index, command := range commands {
		result[index] = strings.ReplaceAll(command, "MOD", codeName)
	}
	return result
}

func sortedEmbeddedFiles(filesystem fs.FS, root string) ([]string, error) {
	var names []string
	err := fs.WalkDir(filesystem, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
