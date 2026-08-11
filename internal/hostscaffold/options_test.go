package hostscaffold

import (
	"strings"
	"testing"

	"github.com/sunrioa/rin/internal/portablepath"
)

func testOptions(string) Options {
	return Options{
		Host: "custom", Runtime: "go", ID: "guide_npc", Name: "向导 NPC",
		Author: "example author", Version: "0.1.0", Output: "guide_npc",
	}
}

func TestHostsExposeOnlyTheEngineNeutralSkeleton(t *testing.T) {
	hosts := Hosts()
	if len(hosts) != 1 || hosts[0].ID != HostCustom {
		t.Fatalf("Hosts() = %+v, want only %q", hosts, HostCustom)
	}
	if hosts[0].TemplateStatus != "contract-skeleton" ||
		hosts[0].RealHostValidation != "required" ||
		!hosts[0].RequiresGameHook {
		t.Fatalf("custom Host overstates its guarantees: %+v", hosts[0])
	}
	hosts[0].RuntimePins = append(hosts[0].RuntimePins, RuntimePin{Name: "mutated"})
	if len(Hosts()[0].RuntimePins) != 0 {
		t.Fatal("Hosts returned mutable catalog storage")
	}
}

func TestEverySupportedRuntimeIsExplicit(t *testing.T) {
	for _, runtime := range []string{"go", "javascript", "python", "csharp", "java", "lua"} {
		options := testOptions(HostCustom)
		options.Runtime = runtime
		normalized, err := normalizeOptions(options)
		if err != nil {
			t.Errorf("%s: %v", runtime, err)
			continue
		}
		if normalized.Runtime != runtime ||
			len(normalized.HostDescriptor.RuntimePins) != 1 ||
			normalized.HostDescriptor.RuntimePins[0].Version != runtime {
			t.Errorf("%s produced wrong runtime metadata: %+v", runtime, normalized)
		}
	}
}

func TestOptionValidationRejectsUnsafeValues(t *testing.T) {
	longID := "a" + strings.Repeat("b", 64)
	tests := []struct {
		name    string
		mutate  func(*Options)
		message string
	}{
		{"unknown host", func(options *Options) { options.Host = "fabric" }, "unsupported host"},
		{"missing runtime", func(options *Options) { options.Runtime = "" }, "-runtime must be"},
		{"unknown runtime", func(options *Options) { options.Runtime = "rust" }, "-runtime must be"},
		{"short id", func(options *Options) { options.ID = "a" }, "2-64"},
		{"long id", func(options *Options) { options.ID = longID }, "2-64"},
		{"uppercase id", func(options *Options) { options.ID = "Guide" }, "lowercase"},
		{"double underscore", func(options *Options) { options.ID = "guide__npc" }, "single underscores"},
		{"Windows device id", func(options *Options) { options.ID = "con" }, "reserved on Windows"},
		{"display newline", func(options *Options) { options.Name = "Guide\nInjected" }, "control characters"},
		{"display whitespace", func(options *Options) { options.Name = " Guide " }, "leading or trailing"},
		{"author newline", func(options *Options) { options.Author = "A\nB" }, "control characters"},
		{"invalid version", func(options *Options) { options.Version = "v1" }, "major.minor.patch"},
		{"large version", func(options *Options) { options.Version = "65535.0.0" }, "between 0 and 65534"},
		{"long version", func(options *Options) { options.Version = "12345678901234567.0.0" }, "at most 17"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions(HostCustom)
			test.mutate(&options)
			_, err := normalizeOptions(options)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("normalizeOptions() error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestDefaultsKeepProjectAndRinVersionsSeparate(t *testing.T) {
	options := testOptions(HostCustom)
	options.Name = ""
	options.Version = ""
	options.Output = ""
	normalized, err := normalizeOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != options.ID || normalized.Output != options.ID {
		t.Fatalf("defaults = name %q output %q", normalized.Name, normalized.Output)
	}
	if normalized.Version != "0.1.0" || normalized.Version == normalized.RinVersion {
		t.Fatalf("versions were conflated: %+v", normalized)
	}
}

func TestTemplatePathValidationUsesWindowsSemantics(t *testing.T) {
	invalid := []string{
		"", "/absolute", "../escape", "nested/../escape", `nested\escape`,
		"C:/escape", "README.md/../escape", "CON", "aux.txt", "name.",
		"name ", "stream:name", "double//separator", "COM¹.txt", "lpt²",
		string([]byte{0xff}), strings.Repeat("界", portablepath.MaxSegmentUTF16+1),
	}
	for _, candidate := range invalid {
		if err := portablepath.ValidateRelative(candidate); err == nil {
			t.Errorf("ValidateRelative(%q) unexpectedly succeeded", candidate)
		}
	}
	for _, candidate := range []string{"README.md", "src/adapter.go", "父目录/file.txt"} {
		if err := portablepath.ValidateRelative(candidate); err != nil {
			t.Errorf("ValidateRelative(%q): %v", candidate, err)
		}
	}
}
