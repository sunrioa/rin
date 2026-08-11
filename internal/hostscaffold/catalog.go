package hostscaffold

import (
	"fmt"
	"sort"
)

const HostCustom = "custom"

// RuntimePin records a dependency that a generated host keeps fixed.
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
	RequiresGameHook      bool
	RealHostValidation    string
	RuntimePins           []RuntimePin
	UnixVerifyCommands    []string
	WindowsVerifyCommands []string
}

var hostCatalog = map[string]HostDescriptor{
	HostCustom: {
		ID:                    HostCustom,
		Name:                  "Custom game engine or runtime",
		TemplateStatus:        "contract-skeleton",
		RequiresGameHook:      true,
		RealHostValidation:    "required",
		UnixVerifyCommands:    []string{"rin conformance host", "rin doctor host"},
		WindowsVerifyCommands: []string{"rin.exe conformance host", "rin.exe doctor host"},
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
