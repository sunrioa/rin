package modscaffold

import (
	"fmt"
	"sort"
)

const (
	HostFabric        = "fabric"
	HostBepInExMono   = "bepinex-mono"
	HostBepInExIL2CPP = "bepinex-il2cpp"
	HostLuanti        = "luanti"
)

// RuntimePin records a host dependency that the generated project keeps
// fixed instead of resolving a floating "latest" version.
type RuntimePin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// HostDescriptor describes one supported scaffold target.
type HostDescriptor struct {
	ID                    string
	Name                  string
	Language              string
	TemplateStatus        string
	RequiresNamespace     bool
	RequiresGameHook      bool
	RealHostValidation    string
	RuntimePins           []RuntimePin
	UnixVerifyCommands    []string
	WindowsVerifyCommands []string
}

var hostCatalog = map[string]HostDescriptor{
	HostFabric: {
		ID:                 HostFabric,
		Name:               "Fabric dedicated-server Mod",
		Language:           "Java",
		TemplateStatus:     "build-validated",
		RequiresNamespace:  true,
		RealHostValidation: "required",
		RuntimePins: []RuntimePin{
			{Name: "minecraft", Version: "1.21.1"},
			{Name: "java", Version: "21"},
			{Name: "fabric-loader", Version: "0.16.14"},
			{Name: "fabric-api", Version: "0.116.14+1.21.1"},
			{Name: "fabric-loom", Version: "1.11.8"},
			{Name: "gradle", Version: "8.14.3"},
		},
		UnixVerifyCommands: []string{
			"./gradlew clean build --no-daemon",
		},
		WindowsVerifyCommands: []string{
			`.\gradlew.bat clean build --no-daemon`,
		},
	},
	HostBepInExMono: {
		ID:                 HostBepInExMono,
		Name:               "BepInEx 6 Unity Mono plugin",
		Language:           "C#",
		TemplateStatus:     "preview-build-validated",
		RequiresNamespace:  true,
		RequiresGameHook:   true,
		RealHostValidation: "required",
		RuntimePins: []RuntimePin{
			{Name: "bepinex", Version: "6.0.0-be.785"},
			{Name: "target-framework", Version: "netstandard2.0"},
			{Name: "unityengine.modules", Version: "5.6.1"},
			{Name: "system.text.json", Version: "8.0.6"},
		},
		UnixVerifyCommands: []string{
			"dotnet restore MOD.Core.Tests/MOD.Core.Tests.csproj --locked-mode",
			"dotnet build MOD.Core.Tests/MOD.Core.Tests.csproj -c Release --no-restore --nologo",
			"dotnet exec MOD.Core.Tests/bin/Release/net6.0/MOD.Core.Tests.dll",
			"dotnet restore MOD.Mono/MOD.Mono.csproj --locked-mode",
			"dotnet build MOD.Mono/MOD.Mono.csproj -c Release --no-restore --nologo",
		},
		WindowsVerifyCommands: []string{
			"dotnet restore MOD.Core.Tests\\MOD.Core.Tests.csproj --locked-mode",
			"dotnet build MOD.Core.Tests\\MOD.Core.Tests.csproj -c Release --no-restore --nologo",
			"dotnet exec MOD.Core.Tests\\bin\\Release\\net6.0\\MOD.Core.Tests.dll",
			"dotnet restore MOD.Mono\\MOD.Mono.csproj --locked-mode",
			"dotnet build MOD.Mono\\MOD.Mono.csproj -c Release --no-restore --nologo",
		},
	},
	HostBepInExIL2CPP: {
		ID:                 HostBepInExIL2CPP,
		Name:               "BepInEx 6 Unity IL2CPP plugin",
		Language:           "C#",
		TemplateStatus:     "preview-build-validated",
		RequiresNamespace:  true,
		RequiresGameHook:   true,
		RealHostValidation: "required",
		RuntimePins: []RuntimePin{
			{Name: "bepinex", Version: "6.0.0-be.785"},
			{Name: "target-framework", Version: "net6.0"},
		},
		UnixVerifyCommands: []string{
			"dotnet restore MOD.Core.Tests/MOD.Core.Tests.csproj --locked-mode",
			"dotnet build MOD.Core.Tests/MOD.Core.Tests.csproj -c Release --no-restore --nologo",
			"dotnet exec MOD.Core.Tests/bin/Release/net6.0/MOD.Core.Tests.dll",
			"dotnet restore MOD.IL2CPP/MOD.IL2CPP.csproj --locked-mode",
			"dotnet build MOD.IL2CPP/MOD.IL2CPP.csproj -c Release --no-restore --nologo",
		},
		WindowsVerifyCommands: []string{
			"dotnet restore MOD.Core.Tests\\MOD.Core.Tests.csproj --locked-mode",
			"dotnet build MOD.Core.Tests\\MOD.Core.Tests.csproj -c Release --no-restore --nologo",
			"dotnet exec MOD.Core.Tests\\bin\\Release\\net6.0\\MOD.Core.Tests.dll",
			"dotnet restore MOD.IL2CPP\\MOD.IL2CPP.csproj --locked-mode",
			"dotnet build MOD.IL2CPP\\MOD.IL2CPP.csproj -c Release --no-restore --nologo",
		},
	},
	HostLuanti: {
		ID:                 HostLuanti,
		Name:               "Luanti server Mod",
		Language:           "Lua",
		TemplateStatus:     "harness-validated",
		RealHostValidation: "required",
		RuntimePins: []RuntimePin{
			{Name: "lua-api", Version: "5.1-compatible"},
		},
		UnixVerifyCommands: []string{
			"lua test_state.lua",
		},
		WindowsVerifyCommands: []string{
			"lua .\\test_state.lua",
		},
	},
}

// Hosts returns a stable, caller-owned list of supported targets.
func Hosts() []HostDescriptor {
	ids := make([]string, 0, len(hostCatalog))
	for id := range hostCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hosts := make([]HostDescriptor, 0, len(ids))
	for _, id := range ids {
		hosts = append(hosts, cloneHost(hostCatalog[id]))
	}
	return hosts
}

func lookupHost(id string) (HostDescriptor, error) {
	host, ok := hostCatalog[id]
	if !ok {
		return HostDescriptor{}, fmt.Errorf(
			"unsupported host %q (use -list-hosts to see supported values)", id)
	}
	return cloneHost(host), nil
}

func cloneHost(host HostDescriptor) HostDescriptor {
	host.RuntimePins = append([]RuntimePin(nil), host.RuntimePins...)
	host.UnixVerifyCommands = append([]string(nil), host.UnixVerifyCommands...)
	host.WindowsVerifyCommands = append([]string(nil), host.WindowsVerifyCommands...)
	return host
}
