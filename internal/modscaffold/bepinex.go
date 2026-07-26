package modscaffold

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"strings"

	modtemplates "github.com/sunrioa/rin/examples/mods"
	sdkassets "github.com/sunrioa/rin/sdk"
)

const bepinexTemplateRoot = "bepinex-rin-npc"

func renderBepInEx(options normalizedOptions) ([]renderedFile, error) {
	backendSource := "RinNpc.Mono/"
	backendTarget := options.CodeName + ".Mono/"
	backendLabel := "Mono"
	if options.Host == HostBepInExIL2CPP {
		backendSource = "RinNpc.IL2CPP/"
		backendTarget = options.CodeName + ".IL2CPP/"
		backendLabel = "IL2CPP"
	}
	names, err := sortedEmbeddedFiles(modtemplates.FS, bepinexTemplateRoot)
	if err != nil {
		return nil, fmt.Errorf("enumerate BepInEx template: %w", err)
	}
	files := make([]renderedFile, 0, len(names)+20)
	for _, name := range names {
		relative := strings.TrimPrefix(name, bepinexTemplateRoot+"/")
		if !includeBepInExFile(relative, backendSource) {
			continue
		}
		payload, readErr := fs.ReadFile(modtemplates.FS, name)
		if readErr != nil {
			return nil, fmt.Errorf("read BepInEx template %s: %w", name, readErr)
		}
		target := remapBepInExPath(relative, options.CodeName, backendSource, backendTarget)
		payload, err = renderBepInExFile(relative, payload, options, backendLabel)
		if err != nil {
			return nil, err
		}
		mode := fs.FileMode(0o644)
		role := "host-template"
		if relative == "package_bepinex.py" {
			mode = 0o755
			role = "build-helper"
		} else if strings.HasPrefix(relative, "third-party/") {
			role = "third-party-license"
		}
		files = append(files, renderedFile{
			Path: target, Mode: mode, Data: payload, Role: role,
		})
	}
	sdkNames, err := sortedEmbeddedFiles(sdkassets.FS, "csharp/Rin.Client")
	if err != nil {
		return nil, fmt.Errorf("enumerate C# SDK: %w", err)
	}
	for _, name := range sdkNames {
		payload, readErr := fs.ReadFile(sdkassets.FS, name)
		if readErr != nil {
			return nil, fmt.Errorf("read C# SDK %s: %w", name, readErr)
		}
		target := strings.TrimPrefix(name, "csharp/")
		files = append(files, renderedFile{
			Path: target, Mode: 0o644, Data: payload, Role: "vendored-rin-sdk",
		})
	}
	return files, nil
}

func includeBepInExFile(relative, backendSource string) bool {
	return relative == "Directory.Build.props" ||
		relative == "NuGet.config" ||
		relative == "package_bepinex.py" ||
		(backendSource == "RinNpc.Mono/" &&
			strings.HasPrefix(relative, "third-party/")) ||
		strings.HasPrefix(relative, "RinNpc.Core/") ||
		strings.HasPrefix(relative, "RinNpc.Core.Tests/") ||
		strings.HasPrefix(relative, backendSource)
}

func remapBepInExPath(relative, codeName, backendSource, backendTarget string) string {
	switch {
	case strings.HasPrefix(relative, "RinNpc.Core.Tests/"):
		relative = codeName + ".Core.Tests/" +
			strings.TrimPrefix(relative, "RinNpc.Core.Tests/")
	case strings.HasPrefix(relative, "RinNpc.Core/"):
		relative = codeName + ".Core/" + strings.TrimPrefix(relative, "RinNpc.Core/")
	case strings.HasPrefix(relative, backendSource):
		relative = backendTarget + strings.TrimPrefix(relative, backendSource)
	}
	relative = strings.ReplaceAll(relative, "RinNpc.Core.Tests", codeName+".Core.Tests")
	relative = strings.ReplaceAll(relative, "RinNpc.Core", codeName+".Core")
	relative = strings.ReplaceAll(relative, "RinNpc.Mono", codeName+".Mono")
	relative = strings.ReplaceAll(relative, "RinNpc.IL2CPP", codeName+".IL2CPP")
	return relative
}

func renderBepInExFile(
	relative string,
	payload []byte,
	options normalizedOptions,
	backendLabel string,
) ([]byte, error) {
	text := string(payload)
	var err error
	if relative == "Directory.Build.props" {
		versionProperties := "<Version>" + options.Version + "</Version>"
		if options.Author != "" {
			var escaped bytes.Buffer
			if err := xml.EscapeText(&escaped, []byte(options.Author)); err != nil {
				return nil, fmt.Errorf("escape BepInEx author: %w", err)
			}
			versionProperties += "\n    <Authors>" + escaped.String() + "</Authors>"
		}
		text, err = replaceRequired(
			text, "<Version>0.7.0</Version>",
			versionProperties, relative)
		if err != nil {
			return nil, err
		}
		return []byte(text), nil
	}
	if strings.HasSuffix(relative, "packages.lock.json") {
		text = strings.ReplaceAll(
			text, `"rinnpc.core"`, csharpString(strings.ToLower(options.CodeName)+".core"))
		return []byte(text), nil
	}
	if strings.HasSuffix(relative, ".csproj") {
		text = strings.ReplaceAll(text, "RinNpc.Core.Tests", options.CodeName+".Core.Tests")
		text = strings.ReplaceAll(text, "RinNpc.Core", options.CodeName+".Core")
		text = strings.ReplaceAll(text, "RinNpc.Mono", options.CodeName+".Mono")
		text = strings.ReplaceAll(text, "RinNpc.IL2CPP", options.CodeName+".IL2CPP")
		text = strings.ReplaceAll(text, "RinNpcExample", options.CodeName)
		text = strings.ReplaceAll(
			text,
			`../../../../sdk/csharp/Rin.Client/Rin.Client.csproj`,
			`../Rin.Client/Rin.Client.csproj`,
		)
		return []byte(text), nil
	}
	if !strings.HasSuffix(relative, ".cs") {
		return payload, nil
	}
	text = strings.ReplaceAll(text, "RinNpcExample", options.CodeName)
	text = strings.ReplaceAll(text, `"rin-npc-example"`, csharpString(options.ID))
	if strings.HasSuffix(relative, "RinNpcRuntime.cs") {
		text, err = replaceRequired(
			text, `"0.7.0"`, csharpString(options.Version), relative)
		if err != nil {
			return nil, err
		}
	}
	if strings.HasSuffix(relative, "/Plugin.cs") {
		originalGUID := "io.github.sunrioa.rin.npc-example." + strings.ToLower(backendLabel)
		text, err = replaceRequired(
			text, csharpString(originalGUID),
			csharpString(options.PluginGUID+"."+strings.ToLower(backendLabel)), relative)
		if err != nil {
			return nil, err
		}
		originalName := "Rin NPC Example (" + backendLabel + ")"
		text, err = replaceRequired(
			text, csharpString(originalName),
			csharpString(options.Name+" ("+backendLabel+")"), relative)
		if err != nil {
			return nil, err
		}
		text, err = replaceRequired(
			text, `public const string PluginVersion = "0.7.0";`,
			"public const string PluginVersion = "+csharpString(options.Version)+";", relative)
		if err != nil {
			return nil, err
		}
		if backendLabel == "Mono" {
			text, err = replaceRequired(
				text, `"Example", "EnableF8Demo", true,`,
				`"Example", "EnableF8Demo", false,`, relative)
			if err != nil {
				return nil, err
			}
		}
	}
	return []byte(text), nil
}

func csharpString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
