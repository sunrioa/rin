package hostscaffold

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/release"
)

func TestRenderIsDeterministicAndPortable(t *testing.T) {
	first, err := Render(testOptions(HostCustom))
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions(HostCustom)
	options.Output = "another destination"
	second, err := Render(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.files) != len(second.files) {
		t.Fatalf("file count differs: %d != %d", len(first.files), len(second.files))
	}
	for index := range first.files {
		left, right := first.files[index], second.files[index]
		if left.Path != right.Path || left.Mode != right.Mode ||
			left.Role != right.Role || !bytes.Equal(left.Data, right.Data) {
			t.Fatalf("rendered file %d is not deterministic", index)
		}
		text := string(left.Data)
		if strings.Contains(text, "/Users/") || strings.Contains(text, `C:\Users\`) {
			t.Errorf("%s contains an absolute workstation path", left.Path)
		}
	}
}

func TestManifestDescribesOnlyTheV2ContractSkeleton(t *testing.T) {
	plan, err := Render(testOptions(HostCustom))
	if err != nil {
		t.Fatal(err)
	}
	var manifest scaffoldManifest
	if err := json.Unmarshal(renderedByPath(t, plan, manifestPath).Data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || !manifest.Generator.Deterministic ||
		manifest.Generator.ContractVersion != host.ContractVersion ||
		manifest.Generator.RinVersion != release.Version {
		t.Fatalf("unexpected manifest header: %+v", manifest)
	}
	if manifest.Project.Runtime != "go" ||
		manifest.Host.ID != HostCustom ||
		manifest.Host.TemplateStatus != "contract-skeleton" ||
		manifest.SDK.Delivery != "contract-only" ||
		manifest.CapabilityProfile != "contract-only" ||
		manifest.RealHostValidation != "required" {
		t.Fatalf("manifest overstates generated behavior: %+v", manifest)
	}
	if len(manifest.Files) != len(plan.files)-1 {
		t.Fatalf("manifest checksums %d files, want %d", len(manifest.Files), len(plan.files)-1)
	}
	for _, entry := range manifest.Files {
		file := renderedByPath(t, plan, entry.Path)
		digest := sha256.Sum256(file.Data)
		if entry.SHA256 != hex.EncodeToString(digest[:]) {
			t.Errorf("%s hash mismatch", entry.Path)
		}
		if entry.Path == manifestPath {
			t.Error("manifest must not claim a circular hash of itself")
		}
	}
}

func TestCustomCapabilityIsAValidSealedV2Spec(t *testing.T) {
	plan, err := Render(testOptions(HostCustom))
	if err != nil {
		t.Fatal(err)
	}
	var spec host.CapabilitySpec
	if err := json.Unmarshal(
		renderedByPath(t, plan, "capabilities/dialogue.say.json").Data,
		&spec,
	); err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("generated capability is invalid: %v", err)
	}
	if spec.Kind != host.CapabilityAtomic ||
		spec.Execution != host.ExecutionImmediate ||
		spec.Cancellation != host.CancellationUnsupported ||
		spec.MaxEffects != 1 ||
		spec.ProducesChildOperations {
		t.Fatalf("unexpected generated capability semantics: %+v", spec)
	}
	config := string(renderedByPath(t, plan, "rin-host.json").Data)
	if !strings.Contains(config, `"contract_version": "rin.host/v2"`) ||
		strings.Contains(config, "protocol_version") {
		t.Fatalf("generated Host config is stale:\n%s", config)
	}
}

func TestGeneratedGuidanceUsesActionV2Terms(t *testing.T) {
	plan, err := Render(testOptions(HostCustom))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"README.md", "README.zh-CN.md", "src/README.md"} {
		text := string(renderedByPath(t, plan, path).Data)
		for _, forbidden := range []string{"Pending Turn", "Proposal", "7374", "rin.protocol/v2"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s still contains %q", path, forbidden)
			}
		}
		if !strings.Contains(text, "Action") {
			t.Errorf("%s does not explain the Action boundary", path)
		}
	}
}

func TestRenderRejectsInvalidTemplateInventory(t *testing.T) {
	tests := []struct {
		name  string
		files []renderedFile
	}{
		{"empty", nil},
		{"empty payload", []renderedFile{{Path: "a", Mode: 0o644}}},
		{"bad mode", []renderedFile{{Path: "a", Mode: 0o600, Data: []byte("x")}}},
		{"escape", []renderedFile{{Path: "../a", Mode: 0o644, Data: []byte("x")}}},
		{"case collision", []renderedFile{
			{Path: "README.md", Mode: 0o644, Data: []byte("x")},
			{Path: "readme.md", Mode: 0o644, Data: []byte("y")},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRenderedFiles(test.files); err == nil {
				t.Fatal("validateRenderedFiles unexpectedly succeeded")
			}
		})
	}
}

func renderedByPath(t *testing.T, plan *Plan, path string) renderedFile {
	t.Helper()
	for _, file := range plan.files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("rendered file %q not found", path)
	return renderedFile{}
}
