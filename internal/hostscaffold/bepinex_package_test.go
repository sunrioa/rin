package hostscaffold

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"

	modtemplates "github.com/sunrioa/rin/examples/mods"
)

func TestGeneratedBepInExProjectsIncludeCanonicalPackager(t *testing.T) {
	canonical, err := fs.ReadFile(
		modtemplates.FS,
		"bepinex-rin-npc/package_bepinex.py",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{HostBepInExMono, HostBepInExIL2CPP} {
		t.Run(host, func(t *testing.T) {
			plan, err := Render(bepInExPackageOptions(host, "Example Author"))
			if err != nil {
				t.Fatal(err)
			}
			helper := bepinexRenderedFile(t, plan, "package_bepinex.py")
			if helper.Mode != 0o755 || helper.Role != "build-helper" ||
				!bytes.Equal(helper.Data, canonical) {
				t.Fatal("generated package_bepinex.py is not the canonical executable helper")
			}

			var manifest scaffoldManifest
			manifestFile := bepinexRenderedFile(t, plan, manifestPath)
			if err := json.Unmarshal(manifestFile.Data, &manifest); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(canonical)
			wantDigest := hex.EncodeToString(digest[:])
			found := false
			for _, entry := range manifest.Files {
				if entry.Path == "package_bepinex.py" {
					found = true
					if entry.Role != "build-helper" || entry.SHA256 != wantDigest {
						t.Fatalf("packager manifest entry = %+v", entry)
					}
				}
			}
			if !found {
				t.Fatal("rin-scaffold.json does not record package_bepinex.py checksum")
			}

			backend := "mono"
			if host == HostBepInExIL2CPP {
				backend = "il2cpp"
			}
			for _, path := range []string{"README.md", "README.zh-CN.md"} {
				readme := string(bepinexRenderedFile(t, plan, path).Data)
				for _, fragment := range []string{
					"python package_bepinex.py",
					"--verify-archive",
					"guide-npc-bepinex-" + backend + "-0.1.0.zip",
					"Example Author",
					"LICENSE-RIN.txt",
					"SHA-256",
				} {
					if !strings.Contains(readme, fragment) {
						t.Errorf("%s is missing %q", path, fragment)
					}
				}
			}
		})
	}
}

func TestBepInExThirdPartyNoticesAreMonoOnlyAndManifested(t *testing.T) {
	expected := []string{
		"third-party/LICENSE-DOTNET.txt",
		"third-party/THIRD-PARTY-NOTICES-DOTNET-STANDARD-2.0.txt",
		"third-party/THIRD-PARTY-NOTICES-MICROSOFT-BCL-8.0.txt",
		"third-party/THIRD-PARTY-NOTICES-MONO.txt",
		"third-party/THIRD-PARTY-NOTICES-NUMERICS-VECTORS-4.4.txt",
		"third-party/THIRD-PARTY-NOTICES-RUNTIME-UNSAFE-6.0.txt",
		"third-party/THIRD-PARTY-NOTICES-TEXT-JSON-8.0.6.txt",
	}
	mono, err := Render(bepInExPackageOptions(HostBepInExMono, ""))
	if err != nil {
		t.Fatal(err)
	}
	var manifest scaffoldManifest
	if err := json.Unmarshal(
		bepinexRenderedFile(t, mono, manifestPath).Data,
		&manifest,
	); err != nil {
		t.Fatal(err)
	}
	manifestEntries := make(map[string]manifestFile, len(manifest.Files))
	for _, entry := range manifest.Files {
		manifestEntries[entry.Path] = entry
	}
	for _, path := range expected {
		file := bepinexRenderedFile(t, mono, path)
		if file.Role != "third-party-license" {
			t.Errorf("%s role = %q", path, file.Role)
		}
		digest := sha256.Sum256(file.Data)
		entry, ok := manifestEntries[path]
		if !ok || entry.Role != "third-party-license" ||
			entry.SHA256 != hex.EncodeToString(digest[:]) {
			t.Errorf("%s is not checksum-pinned in rin-scaffold.json", path)
		}
	}

	il2cpp, err := Render(bepInExPackageOptions(HostBepInExIL2CPP, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range il2cpp.files {
		if strings.HasPrefix(file.Path, "third-party/") ||
			strings.Contains(file.Path, "DOTNET") ||
			strings.Contains(file.Path, "NOTICES-MONO") {
			t.Errorf("IL2CPP scaffold incorrectly contains Mono notice %s", file.Path)
		}
	}
	for _, path := range []string{"README.md", "README.zh-CN.md"} {
		readme := string(bepinexRenderedFile(t, il2cpp, path).Data)
		if strings.Contains(readme, "LICENSE-DOTNET") ||
			strings.Contains(readme, "THIRD-PARTY-NOTICES-MONO") {
			t.Errorf("%s incorrectly claims Mono runtime notices", path)
		}
	}
}

func TestBepInExAuthorIsXMLSafeAndNotSilentlyDiscarded(t *testing.T) {
	const author = `Example & <Maintainer>`
	plan, err := Render(bepInExPackageOptions(HostBepInExMono, author))
	if err != nil {
		t.Fatal(err)
	}
	properties := string(
		bepinexRenderedFile(t, plan, "Directory.Build.props").Data,
	)
	if !strings.Contains(
		properties,
		"<Authors>Example &amp; &lt;Maintainer&gt;</Authors>",
	) {
		t.Fatalf("Directory.Build.props did not safely retain author:\n%s", properties)
	}
	if strings.Contains(properties, "<Authors>"+author+"</Authors>") {
		t.Fatal("raw author text was injected into XML")
	}
}

func TestBepInExPackagingClaimsRemainVisibleInScaffoldingDocs(t *testing.T) {
	required := map[string][]string{
		"../../docs/host-scaffolding.md": {
			"python package_bepinex.py",
			"--verify-archive",
			"System.Text.Json",
			"game-specific Interop",
			"build and package",
		},
		"../../docs/host-scaffolding.zh-CN.md": {
			"python package_bepinex.py",
			"--verify-archive",
			"System.Text.Json",
			"游戏专属",
			"构建并打包",
		},
	}
	for path, fragments := range required {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(payload), fragment) {
				t.Errorf("%s is missing %q", path, fragment)
			}
		}
	}
}

func bepInExPackageOptions(host, author string) Options {
	return Options{
		Host:      host,
		ID:        "guide_npc",
		Name:      "Guide NPC",
		Namespace: "io.github.example",
		Author:    author,
		Version:   "0.1.0",
		Output:    "guide_npc",
	}
}

func bepinexRenderedFile(t *testing.T, plan *Plan, path string) renderedFile {
	t.Helper()
	for _, file := range plan.files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("rendered file %q not found", path)
	return renderedFile{}
}
