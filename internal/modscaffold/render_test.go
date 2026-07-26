package modscaffold

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	sdkassets "github.com/sunrioa/rin/sdk"
)

func TestRenderIsDeterministicForEveryHost(t *testing.T) {
	for _, host := range Hosts() {
		t.Run(host.ID, func(t *testing.T) {
			firstOptions := testOptions(host.ID)
			first, err := Render(firstOptions)
			if err != nil {
				t.Fatal(err)
			}
			secondOptions := testOptions(host.ID)
			secondOptions.Output = "a different destination"
			second, err := Render(secondOptions)
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
				if strings.Contains(string(left.Data), "/Users/") ||
					strings.Contains(string(left.Data), `C:\Users\`) {
					t.Errorf("%s contains an absolute workstation path", left.Path)
				}
				for _, forbidden := range []string{
					"/build/", "/obj/", "/dist/", "/.gradle/", "/logs/",
				} {
					if strings.Contains("/"+left.Path+"/", forbidden) {
						t.Errorf("generated forbidden artifact path %s", left.Path)
					}
				}
			}
		})
	}
}

func TestDisplayMetadataIsEscapedWithoutSourceInjection(t *testing.T) {
	const displayName = `Guide "NPC" \ Companion`
	fabricOptions := testOptions(HostFabric)
	fabricOptions.Name = displayName
	fabricPlan, err := Render(fabricOptions)
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(
		renderedByPath(t, fabricPlan, "src/main/resources/fabric.mod.json").Data,
		&metadata,
	); err != nil {
		t.Fatal(err)
	}
	if metadata.Name != displayName {
		t.Fatalf("Fabric display name = %q, want %q", metadata.Name, displayName)
	}

	bepInExOptions := testOptions(HostBepInExMono)
	bepInExOptions.Name = displayName
	bepInExPlan, err := Render(bepInExOptions)
	if err != nil {
		t.Fatal(err)
	}
	plugin := string(renderedByPath(t, bepInExPlan, "GuideNpc.Mono/Plugin.cs").Data)
	if !strings.Contains(plugin, `Guide \"NPC\" \\ Companion (Mono)`) {
		t.Fatalf("BepInEx display name was not escaped as a C# literal:\n%s", plugin)
	}
	if strings.Contains(plugin, "\n Companion") {
		t.Fatal("BepInEx display name escaped its string literal")
	}
}

func TestManifestChecksumsEveryNonManifestFile(t *testing.T) {
	for _, host := range Hosts() {
		t.Run(host.ID, func(t *testing.T) {
			plan, err := Render(testOptions(host.ID))
			if err != nil {
				t.Fatal(err)
			}
			manifestFile := renderedByPath(t, plan, manifestPath)
			var manifest scaffoldManifest
			if err := json.Unmarshal(manifestFile.Data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.SchemaVersion != 1 || !manifest.Generator.Deterministic {
				t.Fatalf("unexpected manifest header: %+v", manifest)
			}
			if manifest.CapabilityProfile != "advisory" ||
				manifest.RealHostValidation != "required" {
				t.Fatalf("manifest overstates host guarantees: %+v", manifest)
			}
			if len(manifest.Files) != len(plan.files)-1 {
				t.Fatalf(
					"manifest checksums %d files, want %d",
					len(manifest.Files), len(plan.files)-1)
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
		})
	}
}

func TestRenderRecordsEmbeddedSDKIdentitySeparatelyFromProjectVersion(t *testing.T) {
	options := testOptions(HostFabric)
	options.Version = "7.8.9"
	plan, err := Render(options)
	if err != nil {
		t.Fatal(err)
	}
	var manifest scaffoldManifest
	if err := json.Unmarshal(
		renderedByPath(t, plan, manifestPath).Data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Project.Version != options.Version {
		t.Fatalf("project version = %q, want %q",
			manifest.Project.Version, options.Version)
	}
	if manifest.Generator.RinVersion != sdkassets.Version {
		t.Fatalf("manifest Rin version = %q, want embedded SDK version %q",
			manifest.Generator.RinVersion, sdkassets.Version)
	}
	sdkSource := string(renderedByPath(
		t, plan, "src/main/java/io/github/sunrioa/rin/RinClient.java").Data)
	if !strings.Contains(sdkSource, `VERSION = "`+sdkassets.Version+`"`) ||
		strings.Contains(sdkSource, `VERSION = "`+options.Version+`"`) {
		t.Fatal("project version changed the vendored SDK identity")
	}
}

func TestFabricRenderIsStandaloneAndCustomized(t *testing.T) {
	plan, err := Render(testOptions(HostFabric))
	if err != nil {
		t.Fatal(err)
	}
	build := string(renderedByPath(t, plan, "build.gradle").Data)
	if strings.Contains(build, "../../../sdk/java") ||
		strings.Contains(build, "../../../LICENSE") {
		t.Fatal("Fabric build still depends on the Rin repository layout")
	}
	if !strings.Contains(build, `from("LICENSE-RIN.txt")`) {
		t.Fatal("Fabric build does not package the local Rin license")
	}
	javaPath := "src/main/java/io/github/example/guide_npc/RinNpcMod.java"
	java := string(renderedByPath(t, plan, javaPath).Data)
	if !strings.Contains(java, "package io.github.example.guide_npc;") {
		t.Fatalf("%s has the wrong package", javaPath)
	}
	metadataFile := renderedByPath(
		t, plan, "src/main/resources/fabric.mod.json")
	metadata := string(metadataFile.Data)
	for _, expected := range []string{
		`"id": "guide_npc"`,
		`"fabric-api": "0.116.14+1.21.1"`,
		`"name": "向导 NPC"`,
		`"version": "0.1.0"`,
		`"io.github.example.guide_npc.RinNpcMod"`,
	} {
		if !strings.Contains(metadata, expected) {
			t.Errorf("Fabric metadata is missing %q", expected)
		}
	}
	var metadataDocument struct {
		License string            `json:"license"`
		Depends map[string]string `json:"depends"`
	}
	if err := json.Unmarshal(metadataFile.Data, &metadataDocument); err != nil {
		t.Fatal(err)
	}
	fabricAPIPin, err := runtimePinVersion(plan.Host(), "fabric-api")
	if err != nil {
		t.Fatal(err)
	}
	if metadataDocument.License != "" || strings.Contains(metadata, `"license"`) {
		t.Fatalf("Fabric metadata selected a license for the generated Mod:\n%s", metadata)
	}
	if metadataDocument.Depends["fabric-api"] != fabricAPIPin {
		t.Fatalf("Fabric API runtime condition = %q, want catalog pin %q",
			metadataDocument.Depends["fabric-api"], fabricAPIPin)
	}
	if renderedByPath(t, plan, "gradlew").Mode != 0o755 {
		t.Fatal("gradlew is not executable")
	}
	for _, path := range []string{"LICENSE-GRADLE.txt", "NOTICE-GRADLE.txt"} {
		if file := renderedByPath(t, plan, path); file.Role != "third-party-license" {
			t.Errorf("%s role = %q, want third-party-license", path, file.Role)
		}
	}
	assertVendoredAsset(
		t, plan,
		"src/main/java/io/github/sunrioa/rin/RinClient.java",
		sdkassets.FS, "java/src/main/java/io/github/sunrioa/rin/RinClient.java",
	)
}

func TestBepInExRenderSelectsExactlyOneBackend(t *testing.T) {
	tests := []struct {
		host     string
		selected string
		excluded string
		guid     string
	}{
		{
			host: HostBepInExMono, selected: "GuideNpc.Mono/GuideNpc.Mono.csproj",
			excluded: "GuideNpc.IL2CPP/", guid: "io.github.example.guide-npc.mono",
		},
		{
			host: HostBepInExIL2CPP, selected: "GuideNpc.IL2CPP/GuideNpc.IL2CPP.csproj",
			excluded: "GuideNpc.Mono/", guid: "io.github.example.guide-npc.il2cpp",
		},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			plan, err := Render(testOptions(test.host))
			if err != nil {
				t.Fatal(err)
			}
			renderedByPath(t, plan, test.selected)
			for _, file := range plan.files {
				if strings.HasPrefix(file.Path, test.excluded) {
					t.Errorf("unexpected second backend file %s", file.Path)
				}
				if strings.Contains(string(file.Data), "../../../../sdk/csharp") {
					t.Errorf("%s still depends on the Rin repository layout", file.Path)
				}
			}
			coreProject := string(renderedByPath(
				t, plan, "GuideNpc.Core/GuideNpc.Core.csproj").Data)
			if !strings.Contains(
				coreProject, `../Rin.Client/Rin.Client.csproj`) {
				t.Fatal("Core project does not reference the vendored SDK")
			}
			pluginPath := strings.TrimSuffix(test.selected, ".csproj") + "Plugin.cs"
			pluginPath = strings.Replace(pluginPath, "/GuideNpc", "/", 1)
			if test.host == HostBepInExMono {
				pluginPath = "GuideNpc.Mono/Plugin.cs"
			} else {
				pluginPath = "GuideNpc.IL2CPP/Plugin.cs"
			}
			plugin := string(renderedByPath(t, plan, pluginPath).Data)
			if !strings.Contains(plugin, `"`+test.guid+`"`) {
				t.Errorf("plugin GUID was not customized: %s", pluginPath)
			}
			if test.host == HostBepInExMono &&
				!strings.Contains(plugin, `"Example", "EnableF8Demo", false,`) {
				t.Error("Mono demo must default to disabled")
			}
			lockPath := strings.TrimSuffix(test.selected, ".csproj") + "packages.lock.json"
			lockPath = strings.Replace(lockPath, "/GuideNpc", "/", 1)
			if test.host == HostBepInExMono {
				lockPath = "GuideNpc.Mono/packages.lock.json"
			} else {
				lockPath = "GuideNpc.IL2CPP/packages.lock.json"
			}
			lock := string(renderedByPath(t, plan, lockPath).Data)
			if !strings.Contains(lock, `"guidenpc.core"`) {
				t.Fatal("renamed Core project is missing from the lock file")
			}
			assertVendoredAsset(
				t, plan, "Rin.Client/RinClient.cs",
				sdkassets.FS, "csharp/Rin.Client/RinClient.cs",
			)
			assertVendoredAsset(
				t, plan, "Rin.Client/packages.lock.json",
				sdkassets.FS, "csharp/Rin.Client/packages.lock.json",
			)
		})
	}
}

func TestIL2CPPRuntimePinsDoNotClaimFrameworkSystemTextJsonPackage(t *testing.T) {
	for _, pin := range hostCatalog[HostBepInExIL2CPP].RuntimePins {
		if pin.Name == "system.text.json" {
			t.Fatalf("IL2CPP net6.0 incorrectly claims a System.Text.Json package pin: %+v", pin)
		}
	}
	var monoPinsSystemTextJSON bool
	for _, pin := range hostCatalog[HostBepInExMono].RuntimePins {
		if pin.Name == "system.text.json" && pin.Version == "8.0.6" {
			monoPinsSystemTextJSON = true
		}
	}
	if !monoPinsSystemTextJSON {
		t.Fatal("Mono netstandard2.0 must retain its System.Text.Json package pin")
	}
}

func TestLuantiRenderUsesPortableMetadataAndExactSDK(t *testing.T) {
	plan, err := Render(testOptions(HostLuanti))
	if err != nil {
		t.Fatal(err)
	}
	modConfig := string(renderedByPath(t, plan, "mod.conf").Data)
	if !strings.Contains(modConfig, "name = guide_npc\n") ||
		!strings.Contains(modConfig, "title = 向导 NPC\n") {
		t.Fatalf("unexpected mod.conf:\n%s", modConfig)
	}
	if strings.Contains(modConfig, "release =") {
		t.Fatal("generated Luanti mod.conf must not copy a ContentDB release number")
	}
	initLua := string(renderedByPath(t, plan, "init.lua").Data)
	for _, expected := range []string{
		`secure.http_mods`, `"guide_npc"`, `content_version = "0.1.0"`,
		`core.register_chatcommand("guide_npc"`,
	} {
		if !strings.Contains(initLua, expected) {
			t.Errorf("init.lua is missing %q", expected)
		}
	}
	testState := string(renderedByPath(t, plan, "test_state.lua").Data)
	if strings.Contains(testState, "examples/mods/") ||
		!strings.Contains(testState, `script:match("^(.*[\\/])")`) {
		t.Fatal("Luanti state test is not standalone and cross-platform")
	}
	assertVendoredAsset(t, plan, "rin.lua", sdkassets.FS, "lua/rin.lua")
}

func renderedByPath(t *testing.T, plan *Plan, name string) renderedFile {
	t.Helper()
	for _, file := range plan.files {
		if file.Path == name {
			return file
		}
	}
	t.Fatalf("rendered file %q not found", name)
	return renderedFile{}
}

func assertVendoredAsset(
	t *testing.T,
	plan *Plan,
	generatedPath string,
	filesystem fs.FS,
	assetPath string,
) {
	t.Helper()
	expected, err := fs.ReadFile(filesystem, assetPath)
	if err != nil {
		t.Fatal(err)
	}
	actual := renderedByPath(t, plan, generatedPath)
	if !bytes.Equal(actual.Data, expected) || actual.Role != "vendored-rin-sdk" {
		t.Fatalf("%s is not an exact vendored SDK asset", generatedPath)
	}
}
