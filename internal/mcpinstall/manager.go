package mcpinstall

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sunrioa/rin/internal/mcpconfig"
	"github.com/sunrioa/rin/internal/privatefile"
)

const (
	manifestSchemaVersion  = 1
	serverRegistrationName = "rin"
	maxManifestSize        = 32 << 10
	maxServerBinarySize    = 128 << 20
)

type Paths struct {
	Root     string
	Config   string
	Manifest string
	Server   string
}

type Options struct {
	Root              string
	CurrentExecutable string
	Version           string
	Runner            Runner
	Now               func() time.Time
}

type Manager struct {
	paths             Paths
	currentExecutable string
	version           string
	runner            Runner
	now               func() time.Time
	home              string
}

type InstallOptions struct {
	Agents       []AgentID
	ControlURL   string
	Token        string
	ServerSource string
	Force        bool
	Repair       bool
}

type UpdateOptions struct {
	ServerSource string
}

type UninstallOptions struct {
	Agents []AgentID
	Purge  bool
}

type AgentStatus struct {
	ID         AgentID
	Name       string
	Available  bool
	Registered bool
	Managed    bool
	Error      string
}

type Status struct {
	Installed        bool
	InstallerVersion string
	ServerPath       string
	ConfigPath       string
	BinaryPresent    bool
	BinaryCurrent    bool
	ConfigValid      bool
	ControlURL       string
	UpdatedAt        time.Time
	Agents           []AgentStatus
}

type Report struct {
	ServerPath string
	ConfigPath string
	Agents     []AgentID
	Hash       string
	Changed    bool
}

type manifest struct {
	SchemaVersion    int       `json:"schema_version"`
	ServerName       string    `json:"server_name"`
	ServerPath       string    `json:"server_path"`
	ConfigPath       string    `json:"config_path"`
	Agents           []AgentID `json:"agents"`
	BinarySHA256     string    `json:"binary_sha256"`
	InstallerVersion string    `json:"installer_version"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func New(options Options) (*Manager, error) {
	root := options.Root
	if root == "" {
		var err error
		root, err = mcpconfig.DefaultDirectory()
		if err != nil {
			return nil, err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP installer directory: %w", err)
	}
	serverName := "rin-mcp"
	if runtime.GOOS == "windows" {
		serverName += ".exe"
	}
	currentExecutable := options.CurrentExecutable
	if currentExecutable == "" {
		currentExecutable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate rin executable: %w", err)
		}
	}
	currentExecutable, err = filepath.Abs(currentExecutable)
	if err != nil {
		return nil, fmt.Errorf("resolve rin executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate user home: %w", err)
	}
	runner := options.Runner
	if runner == nil {
		runner = OSRunner{}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		paths: Paths{
			Root:     root,
			Config:   filepath.Join(root, "mcp-client.json"),
			Manifest: filepath.Join(root, "mcp-install.json"),
			Server:   filepath.Join(root, "bin", serverName),
		},
		currentExecutable: currentExecutable,
		version:           options.Version,
		runner:            runner,
		now:               now,
		home:              home,
	}, nil
}

func (manager *Manager) Detect(ctx context.Context) []AgentStatus {
	result := make([]AgentStatus, 0, len(supportedAgents))
	for _, id := range supportedAgents {
		status := AgentStatus{ID: id, Name: AgentDisplayName(id)}
		executable, err := manager.runner.LookPath(agentExecutableName(id))
		if err != nil {
			status.Error = "command not found"
			result = append(result, status)
			continue
		}
		status.Available = true
		adapter, err := newAgentAdapter(id, executable)
		if err != nil {
			status.Error = err.Error()
			result = append(result, status)
			continue
		}
		status.Registered, err = adapter.registered(ctx, manager.runner, manager.home)
		if err != nil {
			status.Error = err.Error()
		}
		result = append(result, status)
	}
	return result
}

func (manager *Manager) Install(
	ctx context.Context,
	options InstallOptions,
) (Report, error) {
	return manager.withInstallLock(func() (Report, error) {
		return manager.install(ctx, options)
	})
}

func (manager *Manager) install(
	ctx context.Context,
	options InstallOptions,
) (Report, error) {
	agents, err := normalizeAgents(options.Agents)
	if err != nil {
		return Report{}, err
	}
	if len(agents) == 0 {
		return Report{}, errors.New("select at least one MCP agent")
	}
	current, exists, err := manager.loadManifest()
	if err != nil {
		return Report{}, err
	}
	if exists {
		if err := manager.validateManifest(current); err != nil {
			return Report{}, err
		}
	} else {
		current = manager.newManifest()
	}
	adapters := make(map[AgentID]agentAdapter, len(agents))
	registrations := make(map[AgentID]bool, len(agents))
	for _, id := range agents {
		executable, err := manager.runner.LookPath(agentExecutableName(id))
		if err != nil {
			return Report{}, fmt.Errorf(
				"%s command is not installed or not on PATH",
				AgentDisplayName(id),
			)
		}
		adapter, err := newAgentAdapter(id, executable)
		if err != nil {
			return Report{}, err
		}
		registered, err := adapter.registered(ctx, manager.runner, manager.home)
		if err != nil {
			return Report{}, err
		}
		if registered && !containsAgent(current.Agents, id) && !options.Force {
			return Report{}, fmt.Errorf(
				"%s already has an unmanaged %q MCP server; use -force to replace it",
				adapter.displayName,
				serverRegistrationName,
			)
		}
		adapters[id] = adapter
		registrations[id] = registered
	}
	config, err := manager.resolveConfig(options.ControlURL, options.Token)
	if err != nil {
		return Report{}, err
	}
	source, sourceErr := manager.resolveServerSource(options.ServerSource)
	hash := current.BinarySHA256
	changed := false
	if sourceErr == nil {
		hash, changed, err = manager.installServer(source)
		if err != nil {
			return Report{}, err
		}
	} else if !exists || options.ServerSource != "" {
		return Report{}, sourceErr
	} else {
		installedHash, hashErr := hashRegularFile(manager.paths.Server)
		if hashErr != nil {
			return Report{}, errors.Join(
				sourceErr,
				fmt.Errorf("cannot reuse managed rin-mcp: %w", hashErr),
			)
		}
		if installedHash != current.BinarySHA256 {
			return Report{}, errors.Join(
				sourceErr,
				errors.New("cannot reuse managed rin-mcp: binary hash mismatch"),
			)
		}
		hash = installedHash
	}
	if err := mcpconfig.Write(manager.paths.Config, config); err != nil {
		return Report{}, fmt.Errorf("write MCP client configuration: %w", err)
	}
	current.BinarySHA256 = hash
	current.InstallerVersion = manager.version
	current.UpdatedAt = manager.now().UTC()
	if err := manager.writeManifest(current); err != nil {
		return Report{}, err
	}
	for _, id := range agents {
		adapter := adapters[id]
		registered := registrations[id]
		previouslyOwned := containsAgent(current.Agents, id)
		if registered && (options.Force || options.Repair) {
			if err := adapter.remove(ctx, manager.runner, manager.home); err != nil {
				return manager.report(current, changed), err
			}
			registered = false
		}
		if !registered {
			if !previouslyOwned {
				current.Agents = append(current.Agents, id)
				sortAgents(current.Agents)
				current.UpdatedAt = manager.now().UTC()
				if err := manager.writeManifest(current); err != nil {
					return manager.report(current, changed), err
				}
			}
			if err := adapter.add(
				ctx,
				manager.runner,
				manager.home,
				manager.paths.Server,
				manager.paths.Config,
			); err != nil {
				if previouslyOwned {
					return manager.report(current, changed), err
				}
				registeredAfterFailure, inspectErr := adapter.registered(
					ctx,
					manager.runner,
					manager.home,
				)
				var rollbackErr error
				if inspectErr == nil && !registeredAfterFailure {
					current.Agents = removeAgent(current.Agents, id)
					current.UpdatedAt = manager.now().UTC()
					rollbackErr = manager.writeManifest(current)
				}
				return manager.report(current, changed), errors.Join(
					err,
					inspectErr,
					rollbackErr,
				)
			}
		}
		current.UpdatedAt = manager.now().UTC()
		if err := manager.writeManifest(current); err != nil {
			return manager.report(current, changed), err
		}
	}
	return manager.report(current, changed), nil
}

func (manager *Manager) Update(
	ctx context.Context,
	options UpdateOptions,
) (Report, error) {
	return manager.withInstallLock(func() (Report, error) {
		return manager.update(ctx, options)
	})
}

func (manager *Manager) update(
	_ context.Context,
	options UpdateOptions,
) (Report, error) {
	current, exists, err := manager.loadManifest()
	if err != nil {
		return Report{}, err
	}
	if !exists {
		return Report{}, errors.New("Rin MCP is not installed; run rin mcp install first")
	}
	if err := manager.validateManifest(current); err != nil {
		return Report{}, err
	}
	source, err := manager.resolveServerSource(options.ServerSource)
	if err != nil {
		return Report{}, err
	}
	hash, changed, err := manager.installServer(source)
	if err != nil {
		return Report{}, err
	}
	current.BinarySHA256 = hash
	current.InstallerVersion = manager.version
	current.UpdatedAt = manager.now().UTC()
	if err := manager.writeManifest(current); err != nil {
		return Report{}, err
	}
	return manager.report(current, changed), nil
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	current, exists, err := manager.loadManifest()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Installed:  exists,
		ServerPath: manager.paths.Server,
		ConfigPath: manager.paths.Config,
	}
	if exists {
		if err := manager.validateManifest(current); err != nil {
			return Status{}, err
		}
		status.UpdatedAt = current.UpdatedAt
		status.InstallerVersion = current.InstallerVersion
	}
	if hash, err := hashRegularFile(manager.paths.Server); err == nil {
		status.BinaryPresent = true
		status.BinaryCurrent = exists && hash == current.BinarySHA256
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("inspect managed rin-mcp: %w", err)
	}
	if config, err := mcpconfig.Load(manager.paths.Config); err == nil {
		status.ConfigValid = true
		status.ControlURL = config.ControlURL
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("inspect MCP client configuration: %w", err)
	}
	status.Agents = manager.Detect(ctx)
	for index := range status.Agents {
		status.Agents[index].Managed = exists &&
			containsAgent(current.Agents, status.Agents[index].ID)
	}
	return status, nil
}

func (manager *Manager) Uninstall(
	ctx context.Context,
	options UninstallOptions,
) (Report, error) {
	return manager.withInstallLock(func() (Report, error) {
		return manager.uninstall(ctx, options)
	})
}

func (manager *Manager) uninstall(
	ctx context.Context,
	options UninstallOptions,
) (Report, error) {
	current, exists, err := manager.loadManifest()
	if err != nil {
		return Report{}, err
	}
	if !exists {
		return Report{}, errors.New("Rin MCP is not installed")
	}
	if err := manager.validateManifest(current); err != nil {
		return Report{}, err
	}
	agents := options.Agents
	if len(agents) == 0 {
		agents = append([]AgentID(nil), current.Agents...)
	}
	agents, err = normalizeAgents(agents)
	if err != nil {
		return Report{}, err
	}
	if options.Purge && len(agents) != len(current.Agents) {
		return Report{}, errors.New("purge requires removing every managed Agent")
	}
	adapters := make(map[AgentID]agentAdapter, len(agents))
	registrations := make(map[AgentID]bool, len(agents))
	for _, id := range agents {
		if !containsAgent(current.Agents, id) {
			return Report{}, fmt.Errorf(
				"%s registration is not managed by Rin",
				AgentDisplayName(id),
			)
		}
		executable, err := manager.runner.LookPath(agentExecutableName(id))
		if err != nil {
			return manager.report(current, false), fmt.Errorf(
				"%s command is required to remove its MCP registration",
				AgentDisplayName(id),
			)
		}
		adapter, err := newAgentAdapter(id, executable)
		if err != nil {
			return Report{}, err
		}
		registered, err := adapter.registered(ctx, manager.runner, manager.home)
		if err != nil {
			return Report{}, err
		}
		adapters[id] = adapter
		registrations[id] = registered
	}
	for _, id := range agents {
		adapter := adapters[id]
		registered := registrations[id]
		if registered {
			if err := adapter.remove(ctx, manager.runner, manager.home); err != nil {
				return manager.report(current, false), err
			}
		}
		current.Agents = removeAgent(current.Agents, id)
		current.UpdatedAt = manager.now().UTC()
		if err := manager.writeManifest(current); err != nil {
			return manager.report(current, false), err
		}
	}
	if options.Purge {
		if len(current.Agents) != 0 {
			return manager.report(current, false), errors.New(
				"cannot purge while managed agent registrations remain",
			)
		}
		if err := manager.purge(); err != nil {
			return manager.report(current, false), err
		}
	}
	return manager.report(current, false), nil
}

func (manager *Manager) resolveConfig(controlURL, token string) (mcpconfig.Config, error) {
	current, err := mcpconfig.Load(manager.paths.Config)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mcpconfig.Config{}, fmt.Errorf("load existing MCP configuration: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		current = mcpconfig.Config{}
	}
	if controlURL == "" {
		controlURL = current.ControlURL
	}
	if controlURL == "" {
		controlURL = "http://127.0.0.1:7375"
	}
	if token == "" {
		token = current.Token
	}
	config := mcpconfig.New(controlURL, token)
	if err := config.Validate(); err != nil {
		return mcpconfig.Config{}, err
	}
	return config, nil
}

func (manager *Manager) withInstallLock(
	operation func() (Report, error),
) (report Report, resultErr error) {
	lock, err := acquireInstallLock(manager.paths.Root)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, releaseInstallLock(lock))
	}()
	return operation()
}

func (manager *Manager) resolveServerSource(explicit string) (string, error) {
	if explicit != "" {
		return manager.validateServerSource(explicit)
	}
	name := filepath.Base(manager.paths.Server)
	candidates := []string{filepath.Join(filepath.Dir(manager.currentExecutable), name)}
	if path, err := manager.runner.LookPath(name); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil || samePath(absolute, manager.paths.Server) {
			continue
		}
		if _, err := manager.validateServerSource(absolute); err == nil {
			return absolute, nil
		}
	}
	return "", fmt.Errorf(
		"cannot locate a source rin-mcp binary; place %s beside rin or use -server PATH",
		name,
	)
}

func (manager *Manager) validateServerSource(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve rin-mcp source: %w", err)
	}
	if samePath(absolute, manager.paths.Server) {
		return "", errors.New("rin-mcp update source must differ from the managed binary")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect rin-mcp source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxServerBinarySize {
		return "", errors.New("rin-mcp source must be a bounded regular file")
	}
	return absolute, nil
}

func (manager *Manager) installServer(source string) (string, bool, error) {
	data, err := readBoundedRegularFile(source, maxServerBinarySize)
	if err != nil {
		return "", false, fmt.Errorf("read rin-mcp source: %w", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	installedHash, err := hashRegularFile(manager.paths.Server)
	if err == nil && installedHash == hash {
		return hash, false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect managed rin-mcp: %w", err)
	}
	if err := privatefile.Write(manager.paths.Server, data, 0o755); err != nil {
		return "", false, fmt.Errorf("install rin-mcp: %w", err)
	}
	return hash, true, nil
}

func (manager *Manager) newManifest() manifest {
	return manifest{
		SchemaVersion: manifestSchemaVersion,
		ServerName:    serverRegistrationName,
		ServerPath:    manager.paths.Server,
		ConfigPath:    manager.paths.Config,
		Agents:        []AgentID{},
	}
}

func (manager *Manager) loadManifest() (manifest, bool, error) {
	var current manifest
	err := privatefile.ReadJSON(manager.paths.Manifest, maxManifestSize, &current)
	if errors.Is(err, os.ErrNotExist) {
		return manifest{}, false, nil
	}
	if err != nil {
		return manifest{}, false, fmt.Errorf("load MCP installation manifest: %w", err)
	}
	return current, true, nil
}

func (manager *Manager) writeManifest(current manifest) error {
	if err := manager.validateManifest(current); err != nil {
		return err
	}
	if err := privatefile.WriteJSON(manager.paths.Manifest, current); err != nil {
		return fmt.Errorf("write MCP installation manifest: %w", err)
	}
	return nil
}

func (manager *Manager) validateManifest(current manifest) error {
	if current.SchemaVersion != manifestSchemaVersion ||
		current.ServerName != serverRegistrationName {
		return errors.New("unsupported MCP installation manifest")
	}
	if !samePath(current.ServerPath, manager.paths.Server) ||
		!samePath(current.ConfigPath, manager.paths.Config) {
		return errors.New("MCP installation manifest contains unmanaged paths")
	}
	agents, err := normalizeAgents(current.Agents)
	if err != nil || len(agents) != len(current.Agents) {
		return errors.New("MCP installation manifest contains invalid agents")
	}
	if current.BinarySHA256 != "" && len(current.BinarySHA256) != sha256.Size*2 {
		return errors.New("MCP installation manifest contains an invalid binary hash")
	}
	return nil
}

func (manager *Manager) report(current manifest, changed bool) Report {
	return Report{
		ServerPath: manager.paths.Server,
		ConfigPath: manager.paths.Config,
		Agents:     append([]AgentID(nil), current.Agents...),
		Hash:       current.BinarySHA256,
		Changed:    changed,
	}
}

func (manager *Manager) purge() error {
	for _, path := range []string{
		manager.paths.Config,
		manager.paths.Server,
		manager.paths.Manifest,
	} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to purge non-regular managed path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove managed path %s: %w", path, err)
		}
	}
	_ = os.Remove(filepath.Dir(manager.paths.Server))
	_ = os.Remove(manager.paths.Root)
	return nil
}

func hashRegularFile(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() > maxServerBinarySize {
		return "", errors.New("managed rin-mcp is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return "", errors.New("managed rin-mcp changed while opening")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxServerBinarySize+1))
	if err != nil {
		return "", err
	}
	if written > maxServerBinarySize {
		return "", errors.New("managed rin-mcp exceeds the size limit")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func readBoundedRegularFile(path string, maxBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, errors.New("file must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, errors.New("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file exceeds the size limit")
	}
	return data, nil
}

func normalizeAgents(values []AgentID) ([]AgentID, error) {
	seen := make(map[AgentID]bool, len(values))
	result := make([]AgentID, 0, len(values))
	for _, value := range values {
		id, err := ParseAgentID(string(value))
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	sortAgents(result)
	return result, nil
}

func sortAgents(values []AgentID) {
	sort.Slice(values, func(left, right int) bool {
		return agentOrder(values[left]) < agentOrder(values[right])
	})
}

func agentOrder(id AgentID) int {
	for index, supported := range supportedAgents {
		if id == supported {
			return index
		}
	}
	return len(supportedAgents)
}

func containsAgent(values []AgentID, target AgentID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeAgent(values []AgentID, target AgentID) []AgentID {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
