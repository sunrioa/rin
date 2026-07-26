package hostscaffold

import (
	"fmt"
	"io/fs"
	"strings"

	modtemplates "github.com/sunrioa/rin/examples/mods"
	sdkassets "github.com/sunrioa/rin/sdk"
)

const luantiTemplateRoot = "luanti-rin-npc"

func renderLuanti(options normalizedOptions) ([]renderedFile, error) {
	names, err := sortedEmbeddedFiles(modtemplates.FS, luantiTemplateRoot)
	if err != nil {
		return nil, fmt.Errorf("enumerate Luanti template: %w", err)
	}
	files := make([]renderedFile, 0, len(names)+1)
	for _, name := range names {
		payload, readErr := fs.ReadFile(modtemplates.FS, name)
		if readErr != nil {
			return nil, fmt.Errorf("read Luanti template %s: %w", name, readErr)
		}
		relative := strings.TrimPrefix(name, luantiTemplateRoot+"/")
		payload, err = renderLuantiFile(relative, payload, options)
		if err != nil {
			return nil, err
		}
		files = append(files, renderedFile{
			Path: relative, Mode: 0o644, Data: payload, Role: "host-template",
		})
	}
	rinSDK, err := fs.ReadFile(sdkassets.FS, "lua/rin.lua")
	if err != nil {
		return nil, fmt.Errorf("read Lua SDK: %w", err)
	}
	files = append(files, renderedFile{
		Path: "rin.lua", Mode: 0o644, Data: rinSDK, Role: "vendored-rin-sdk",
	})
	return files, nil
}

func renderLuantiFile(
	relative string,
	payload []byte,
	options normalizedOptions,
) ([]byte, error) {
	if relative == "mod.conf" {
		var author string
		if options.Author != "" {
			author = "author = " + options.Author + "\n"
		}
		return []byte(
			"name = " + options.ID + "\n" +
				"title = " + options.Name + "\n" +
				"description = Server-side Rin advisory integration scaffold.\n" +
				author,
		), nil
	}
	text := string(payload)
	switch relative {
	case "init.lua":
		text = strings.ReplaceAll(text, "rin_npc_example", options.ID)
		text = strings.ReplaceAll(
			text, `"rin-luanti-example/0.7.0"`,
			luaString("rin-"+options.ID+"/"+options.Version),
		)
		text = strings.ReplaceAll(text, `"rin-npc-example"`, luaString(options.ID))
		text = strings.ReplaceAll(text, `"0.7.0"`, luaString(options.Version))
		text = strings.ReplaceAll(text, `"luanti-example"`, luaString(options.ID))
		text = strings.ReplaceAll(text, `core.register_chatcommand("rin_npc"`,
			"core.register_chatcommand("+luaString(options.ID))
		text = strings.ReplaceAll(
			text,
			`description = "Ask the example Rin guide for one bounded action.",`,
			`description = "Ask the scaffolded Rin guide for one bounded action.",`,
		)
	case "settingtypes.txt":
		text = strings.ReplaceAll(text, "rin_npc_example", options.ID)
		text = strings.ReplaceAll(
			text,
			"# Rin local origin. This example intentionally rejects remote origins and tokens.",
			"# Rin local origin. This scaffold intentionally rejects remote origins and tokens.",
		)
	case "test_state.lua":
		var err error
		text, err = replaceRequired(
			text,
			`local state_module = dofile("examples/mods/luanti-rin-npc/state.lua")`,
			`local script = arg and arg[0] or "test_state.lua"
local directory = script:match("^(.*[\\/])") or ""
local state_module = dofile(directory .. "state.lua")`,
			relative,
		)
		if err != nil {
			return nil, err
		}
	}
	return []byte(text), nil
}

func luaString(value string) string {
	return javaString(value)
}
