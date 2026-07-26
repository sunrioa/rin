package modscaffold

import (
	"strings"
	"testing"
)

func testOptions(host string) Options {
	options := Options{
		Host: host, ID: "guide_npc", Name: "向导 NPC",
		Author: "example_author", Version: "0.1.0",
		Output: "guide_npc",
	}
	if host != HostLuanti {
		options.Namespace = "io.github.example"
	}
	return options
}

func TestHostsAreStableAndExplicit(t *testing.T) {
	hosts := Hosts()
	expected := []string{
		HostBepInExIL2CPP,
		HostBepInExMono,
		HostFabric,
		HostLuanti,
	}
	if len(hosts) != len(expected) {
		t.Fatalf("Hosts() returned %d entries, want %d", len(hosts), len(expected))
	}
	for index, id := range expected {
		if hosts[index].ID != id {
			t.Errorf("Hosts()[%d].ID = %q, want %q", index, hosts[index].ID, id)
		}
		if hosts[index].RealHostValidation != "required" {
			t.Errorf("%s must retain an explicit real-host validation gate", id)
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
		{
			name: "unknown host",
			mutate: func(options *Options) {
				options.Host = "Fabric"
			},
			message: "unsupported host",
		},
		{
			name: "short id",
			mutate: func(options *Options) {
				options.ID = "a"
			},
			message: "2-64",
		},
		{
			name: "long id",
			mutate: func(options *Options) {
				options.ID = longID
			},
			message: "2-64",
		},
		{
			name: "uppercase id",
			mutate: func(options *Options) {
				options.ID = "Guide"
			},
			message: "lowercase",
		},
		{
			name: "double underscore",
			mutate: func(options *Options) {
				options.ID = "guide__npc"
			},
			message: "single underscores",
		},
		{
			name: "Windows device id",
			mutate: func(options *Options) {
				options.ID = "con"
			},
			message: "reserved on Windows",
		},
		{
			name: "display newline",
			mutate: func(options *Options) {
				options.Name = "Guide\nInjected"
			},
			message: "control characters",
		},
		{
			name: "display surrounding whitespace",
			mutate: func(options *Options) {
				options.Name = " Guide "
			},
			message: "leading or trailing",
		},
		{
			name: "missing namespace",
			mutate: func(options *Options) {
				options.Namespace = ""
			},
			message: "-namespace is required",
		},
		{
			name: "one namespace segment",
			mutate: func(options *Options) {
				options.Namespace = "example"
			},
			message: "at least two",
		},
		{
			name: "uppercase namespace",
			mutate: func(options *Options) {
				options.Namespace = "io.Example"
			},
			message: "lowercase",
		},
		{
			name: "Java keyword namespace",
			mutate: func(options *Options) {
				options.Namespace = "com.class"
			},
			message: "reserved",
		},
		{
			name: "invalid version",
			mutate: func(options *Options) {
				options.Version = "v1"
			},
			message: "major.minor.patch",
		},
		{
			name: "version component exceeds common host limit",
			mutate: func(options *Options) {
				options.Version = "65535.0.0"
			},
			message: "between 0 and 65534",
		},
		{
			name: "version exceeds portable length",
			mutate: func(options *Options) {
				options.Version = "12345678901234567.0.0"
			},
			message: "at most 17 ASCII characters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions(HostFabric)
			test.mutate(&options)
			_, err := normalizeOptions(options)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("normalizeOptions() error = %v, want fragment %q", err, test.message)
			}
		})
	}
}

func TestProjectVersionAcceptsCommonHostBoundary(t *testing.T) {
	for _, host := range Hosts() {
		options := testOptions(host.ID)
		options.Version = "65534.65534.65534"
		if _, err := normalizeOptions(options); err != nil {
			t.Errorf("%s rejected common host version boundary: %v", host.ID, err)
		}
	}
}

func TestLuantiRejectsUnusedNamespace(t *testing.T) {
	options := testOptions(HostLuanti)
	options.Namespace = "io.github.example"
	_, err := normalizeOptions(options)
	if err == nil || !strings.Contains(err.Error(), "not used") {
		t.Fatalf("normalizeOptions() error = %v, want unused namespace error", err)
	}
}

func TestLuantiAuthorMustBeAContentDBUsername(t *testing.T) {
	options := testOptions(HostLuanti)
	options.Author = "Example Author"
	_, err := normalizeOptions(options)
	if err == nil || !strings.Contains(err.Error(), "ContentDB username") {
		t.Fatalf("normalizeOptions() error = %v, want ContentDB username error", err)
	}

	options.Author = "Wuzzy_2"
	if _, err := normalizeOptions(options); err != nil {
		t.Fatalf("normalizeOptions() rejected a portable ContentDB username: %v", err)
	}
}

func TestDefaultsDoNotConflateModAndRinVersions(t *testing.T) {
	options := testOptions(HostFabric)
	options.Name = ""
	options.Version = ""
	normalized, err := normalizeOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != options.ID {
		t.Fatalf("default display name = %q, want %q", normalized.Name, options.ID)
	}
	if normalized.Version != "0.1.0" {
		t.Fatalf("default Mod version = %q, want 0.1.0", normalized.Version)
	}
	if normalized.Version == normalized.RinVersion {
		t.Fatal("default Mod version unexpectedly equals the Rin version")
	}
}

func TestTemplatePathValidationUsesWindowsSemantics(t *testing.T) {
	invalid := []string{
		"", "/absolute", "../escape", "nested/../escape", `nested\escape`,
		"C:/escape", "README.md/../escape", "CON", "aux.txt", "name.",
		"name ", "stream:name", "double//separator", "COM¹.txt", "lpt²",
		string([]byte{0xff}), strings.Repeat("界", maxPortablePathSegmentUTF16+1),
	}
	for _, candidate := range invalid {
		if err := validateTemplatePath(candidate); err == nil {
			t.Errorf("validateTemplatePath(%q) unexpectedly succeeded", candidate)
		}
	}
	valid := []string{
		"README.md",
		"src/main/java/io/github/example/Guide.java",
		"父目录/file.txt",
	}
	for _, candidate := range valid {
		if err := validateTemplatePath(candidate); err != nil {
			t.Errorf("validateTemplatePath(%q): %v", candidate, err)
		}
	}
}
