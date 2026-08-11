package compat_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCoreProductionCodeDoesNotImportGameAdapters(t *testing.T) {
	coreRoots := []string{
		"../agentdaemon",
		"../agentapi",
		"../host",
		"../policy",
		"../controlplane",
		"../cognition",
		"../mcpbridge",
		"../runtime",
		"../protocol",
		"../sdk/hostkit",
	}
	for _, root := range coreRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range parsed.Imports {
				value, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if gameImplementationImport(value) {
					t.Errorf("core production file %s imports game implementation %q", path, value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan core dependency root %s: %v", root, err)
		}
	}
}

func gameImplementationImport(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "/examples/") ||
		strings.Contains(lower, "/adapters/") ||
		strings.Contains(lower, "/mods/") ||
		strings.Contains(lower, "minecraft") ||
		strings.Contains(lower, "fabricmc")
}
