package hostscaffold

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	modtemplates "github.com/sunrioa/rin/examples/mods"
	sdkassets "github.com/sunrioa/rin/sdk"
)

func TestEmbeddedSDKInventoryMatchesCanonicalSources(t *testing.T) {
	embedded := embeddedInventory(t, sdkassets.FS, ".")
	var expected []string
	err := filepath.WalkDir("../../sdk", func(
		name string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() &&
			(entry.Name() == "bin" || entry.Name() == "obj" || entry.Name() == "__pycache__") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel("../../sdk", name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, "java/src/main/java/") &&
			strings.HasSuffix(relative, ".java") ||
			strings.HasPrefix(relative, "csharp/Rin.Client/") &&
				(strings.HasSuffix(relative, ".cs") ||
					strings.HasSuffix(relative, ".csproj") ||
					strings.HasSuffix(relative, "packages.lock.json")) ||
			relative == "lua/rin.lua" {
			expected = append(expected, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(expected)
	if !reflect.DeepEqual(embedded, expected) {
		t.Fatalf("embedded SDK inventory drifted\nembedded: %v\nexpected: %v", embedded, expected)
	}
	for _, name := range expected {
		embeddedPayload, err := fs.ReadFile(sdkassets.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		sourcePayload, err := os.ReadFile(filepath.Join("../../sdk", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(embeddedPayload, sourcePayload) {
			t.Errorf("embedded SDK asset %s differs from its canonical source", name)
		}
	}
}

func TestEmbeddedSDKVersionMatchesClientProjections(t *testing.T) {
	expectedFragments := map[string]string{
		"java/src/main/java/io/github/sunrioa/rin/RinControlClient.java": `VERSION = "` +
			sdkassets.Version + `"`,
		"csharp/Rin.Client/RinControlClient.cs": `ClientVersion = "` +
			sdkassets.Version + `"`,
		"lua/rin.lua": `VERSION = "` + sdkassets.Version + `"`,
	}
	for name, fragment := range expectedFragments {
		payload, err := fs.ReadFile(sdkassets.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), fragment) {
			t.Errorf("embedded SDK asset %s does not identify version %s",
				name, sdkassets.Version)
		}
	}
}

func TestGeneratedRinLicenseMatchesRepositoryLicense(t *testing.T) {
	payload, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(rinLicense), payload) {
		t.Fatal("generated LICENSE-RIN.txt drifted from the repository license")
	}
}

func TestEmbeddedThirdPartyLicenseAssetsMatchReviewedDigests(t *testing.T) {
	expected := map[string]string{
		"fabric-rin-npc/LICENSE-GRADLE.txt":                                        "9536d88ea948603d18e232a13f5958d67807cd80828036b082bff171d2cf0703",
		"fabric-rin-npc/NOTICE-GRADLE.txt":                                         "c2de5fd9adc7ccc4e1382a0e1f38ec6e14f8ca6e28d3c20289fe80353b019aa1",
		"bepinex-rin-npc/third-party/LICENSE-DOTNET.txt":                           "cfc21f5e8bd655ae997eec916138b707b1d290b83272c02a95c9f821b8c87310",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-DOTNET-STANDARD-2.0.txt":  "06cf69d8c3f1170895d57ce881d3e0ab22676fc2cfa41459d035c4f699f2fa83",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-MICROSOFT-BCL-8.0.txt":    "7238e7fd468427aa3fe45b1d0cee1c3e2d93ff96692820768521e9780225d473",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-MONO.txt":                 "2b63490489a1ad5dc49eaf3146c2c7b6b8e5b2b8a815d1e234fa9e0e3ffdfc52",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-NUMERICS-VECTORS-4.4.txt": "a8bc8b3b6cababd6da43e4c776a77cceb4859eb4df06d5e5da2aabb22d19542d",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-RUNTIME-UNSAFE-6.0.txt":   "df255d595f29db06c6d462ceb7c04b33d627e98ac2f1745e1f1fb9f08eaaecb0",
		"bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-TEXT-JSON-8.0.6.txt":      "97c1a7b3da6a4c6ad516448719f45114b41a4d4c5aa300a944476e2e4f5da438",
	}
	for name, want := range expected {
		payload, err := fs.ReadFile(modtemplates.FS, name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		digest := sha256.Sum256(payload)
		if got := hex.EncodeToString(digest[:]); got != want {
			t.Errorf("%s SHA-256 = %s, want reviewed %s", name, got, want)
		}
	}
}

func TestEmbeddedModInventoryExcludesArtifactsAndMatchesSources(t *testing.T) {
	embedded := embeddedInventory(t, modtemplates.FS, ".")
	var expected []string
	err := filepath.WalkDir("../../examples/mods", func(
		name string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "build", "dist", "logs", "obj", "bin", ".gradle":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		relative, err := filepath.Rel("../../examples/mods", name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if canonicalModAsset(relative) {
			expected = append(expected, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(expected)
	if !reflect.DeepEqual(embedded, expected) {
		t.Fatalf("embedded Mod inventory drifted\nembedded: %v\nexpected: %v", embedded, expected)
	}
	for _, name := range expected {
		embeddedPayload, err := fs.ReadFile(modtemplates.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		sourcePayload, err := os.ReadFile(
			filepath.Join("../../examples/mods", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(embeddedPayload, sourcePayload) {
			t.Errorf("embedded Mod asset %s differs from its canonical source", name)
		}
	}
}

func embeddedInventory(t *testing.T, filesystem fs.FS, root string) []string {
	t.Helper()
	var result []string
	err := fs.WalkDir(filesystem, root, func(
		name string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			result = append(result, strings.TrimPrefix(name, "./"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

func canonicalModAsset(name string) bool {
	switch name {
	case "fabric-rin-npc/build.gradle",
		"fabric-rin-npc/LICENSE-GRADLE.txt",
		"fabric-rin-npc/NOTICE-GRADLE.txt",
		"fabric-rin-npc/gradle.properties",
		"fabric-rin-npc/settings.gradle",
		"fabric-rin-npc/gradlew",
		"fabric-rin-npc/gradlew.bat",
		"fabric-rin-npc/gradle/wrapper/gradle-wrapper.jar",
		"fabric-rin-npc/gradle/wrapper/gradle-wrapper.properties",
		"bepinex-rin-npc/Directory.Build.props",
		"bepinex-rin-npc/NuGet.config",
		"bepinex-rin-npc/package_bepinex.py",
		"bepinex-rin-npc/RinNpc.BepInEx.sln",
		"luanti-rin-npc/init.lua",
		"luanti-rin-npc/mod.conf",
		"luanti-rin-npc/settingtypes.txt",
		"luanti-rin-npc/state.lua",
		"luanti-rin-npc/test_state.lua":
		return true
	}
	return strings.HasPrefix(name, "fabric-rin-npc/src/") ||
		strings.HasPrefix(name, "bepinex-rin-npc/third-party/") ||
		strings.HasPrefix(name, "bepinex-rin-npc/RinNpc.Core/") ||
		strings.HasPrefix(name, "bepinex-rin-npc/RinNpc.Core.Tests/") ||
		strings.HasPrefix(name, "bepinex-rin-npc/RinNpc.Mono/") ||
		strings.HasPrefix(name, "bepinex-rin-npc/RinNpc.IL2CPP/")
}
