package modscaffold

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	modtemplates "github.com/sunrioa/rin/examples/mods"
	sdkassets "github.com/sunrioa/rin/sdk"
)

const fabricTemplateRoot = "fabric-rin-npc"

func renderFabric(options normalizedOptions) ([]renderedFile, error) {
	names, err := sortedEmbeddedFiles(modtemplates.FS, fabricTemplateRoot)
	if err != nil {
		return nil, fmt.Errorf("enumerate Fabric template: %w", err)
	}
	files := make([]renderedFile, 0, len(names)+16)
	for _, name := range names {
		payload, readErr := fs.ReadFile(modtemplates.FS, name)
		if readErr != nil {
			return nil, fmt.Errorf("read Fabric template %s: %w", name, readErr)
		}
		relative := strings.TrimPrefix(name, fabricTemplateRoot+"/")
		target := remapFabricPackagePath(relative, options.JavaPackage)
		payload, err = renderFabricFile(relative, payload, options)
		if err != nil {
			return nil, err
		}
		mode := fs.FileMode(0o644)
		role := "host-template"
		if relative == "gradlew" {
			mode = 0o755
		}
		if relative == "LICENSE-GRADLE.txt" || relative == "NOTICE-GRADLE.txt" {
			role = "third-party-license"
		}
		files = append(files, renderedFile{
			Path: target, Mode: mode, Data: payload, Role: role,
		})
	}
	sdkNames, err := sortedEmbeddedFiles(
		sdkassets.FS, "java/src/main/java/io/github/sunrioa/rin")
	if err != nil {
		return nil, fmt.Errorf("enumerate Java SDK: %w", err)
	}
	for _, name := range sdkNames {
		payload, readErr := fs.ReadFile(sdkassets.FS, name)
		if readErr != nil {
			return nil, fmt.Errorf("read Java SDK %s: %w", name, readErr)
		}
		target := strings.TrimPrefix(name, "java/")
		files = append(files, renderedFile{
			Path: target, Mode: 0o644, Data: payload, Role: "vendored-rin-sdk",
		})
	}
	return files, nil
}

func remapFabricPackagePath(relative, javaPackage string) string {
	const mainPrefix = "src/main/java/io/github/sunrioa/rin/example/"
	const testPrefix = "src/test/java/io/github/sunrioa/rin/example/"
	packagePath := strings.ReplaceAll(javaPackage, ".", "/") + "/"
	switch {
	case strings.HasPrefix(relative, mainPrefix):
		return "src/main/java/" + packagePath + strings.TrimPrefix(relative, mainPrefix)
	case strings.HasPrefix(relative, testPrefix):
		return "src/test/java/" + packagePath + strings.TrimPrefix(relative, testPrefix)
	default:
		return relative
	}
}

func renderFabricFile(
	relative string,
	payload []byte,
	options normalizedOptions,
) ([]byte, error) {
	if relative == "src/main/resources/fabric.mod.json" {
		return renderFabricModJSON(payload, options)
	}
	if relative == "gradlew.bat" || strings.HasSuffix(relative, ".jar") {
		return payload, nil
	}
	text := string(payload)
	var err error
	switch relative {
	case "build.gradle":
		text, err = replaceRequired(
			text,
			`sourceSets {
    main {
        java {
            srcDir "../../../sdk/java/src/main/java"
        }
    }
}

`,
			"",
			relative,
		)
		if err != nil {
			return nil, err
		}
		text, err = replaceRequired(
			text, `from("../../../LICENSE")`, `from("LICENSE-RIN.txt")`, relative)
		if err != nil {
			return nil, err
		}
		text = strings.ReplaceAll(
			text, "io.github.sunrioa.rin.example", options.JavaPackage)
	case "gradle.properties":
		text, err = replaceRequired(
			text, "mod_version=0.6.0", "mod_version="+options.Version, relative)
		if err != nil {
			return nil, err
		}
		text, err = replaceRequired(
			text, "maven_group=io.github.sunrioa.rin",
			"maven_group="+options.Namespace, relative)
		if err != nil {
			return nil, err
		}
		text, err = replaceRequired(
			text, "archives_base_name=rin-fabric-npc",
			"archives_base_name="+options.ID, relative)
		if err != nil {
			return nil, err
		}
	case "settings.gradle":
		text, err = replaceRequired(
			text, `rootProject.name = "rin-fabric-npc"`,
			`rootProject.name = "`+options.ID+`"`, relative)
		if err != nil {
			return nil, err
		}
	default:
		if strings.HasSuffix(relative, ".java") {
			text = strings.ReplaceAll(
				text, "io.github.sunrioa.rin.example", options.JavaPackage)
			text = strings.ReplaceAll(text, `"rin-npc-example"`, javaString(options.ID))
			text = strings.ReplaceAll(text, `"fabric-example"`, javaString(options.ID))
			text = strings.ReplaceAll(text, `"0.6.0"`, javaString(options.Version))
			text = strings.ReplaceAll(text, `literal("rin-npc")`,
				"literal("+javaString(options.CommandName)+")")
			text = strings.ReplaceAll(text, `STORAGE_KEY = "rin_npc"`,
				"STORAGE_KEY = "+javaString(options.ID))
		}
	}
	return []byte(text), nil
}

func renderFabricModJSON(payload []byte, options normalizedOptions) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("decode Fabric metadata template: %w", err)
	}
	document["id"] = options.ID
	document["version"] = options.Version
	document["name"] = options.Name
	if options.Author == "" {
		document["authors"] = []string{}
	} else {
		document["authors"] = []string{options.Author}
	}
	document["description"] = "Server-side Rin integration scaffold for " + options.Name + "."
	// The source template is MIT-licensed Rin code, but Fabric's metadata field
	// describes the generated Mod as a whole. Leave it absent until the author
	// deliberately chooses a license for their original work.
	delete(document, "license")
	entrypoints, ok := document["entrypoints"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Fabric metadata template has invalid entrypoints")
	}
	entrypoints["main"] = []string{options.JavaPackage + ".RinNpcMod"}
	depends, ok := document["depends"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Fabric metadata template has invalid dependencies")
	}
	fabricAPIVersion, err := runtimePinVersion(options.HostDescriptor, "fabric-api")
	if err != nil {
		return nil, err
	}
	// A standalone version predicate is an exact Fabric Loader match. Minecraft
	// remains constrained separately because Fabric ignores SemVer build metadata
	// (the "+1.21.1" suffix) during dependency comparison.
	depends["fabric-api"] = fabricAPIVersion
	rendered, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Fabric metadata: %w", err)
	}
	return append(rendered, '\n'), nil
}

func runtimePinVersion(host HostDescriptor, name string) (string, error) {
	for _, pin := range host.RuntimePins {
		if pin.Name == name {
			if pin.Version == "" {
				break
			}
			return pin.Version, nil
		}
	}
	return "", fmt.Errorf("host %q is missing required runtime pin %q", host.ID, name)
}

func javaString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
