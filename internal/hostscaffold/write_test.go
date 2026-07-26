package hostscaffold

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateWritesACompletePortableTree(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "父目录 with spaces")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	options := testOptions(HostFabric)
	options.Output = "父目录 with spaces/guide_npc"
	result, err := GenerateAt(base, options)
	if err != nil {
		t.Fatal(err)
	}
	expectedRoot := filepath.Join(parent, "guide_npc")
	if result.Root != expectedRoot {
		t.Fatalf("result.Root = %q, want %q", result.Root, expectedRoot)
	}
	if _, err := os.Stat(filepath.Join(expectedRoot, incompleteMarker)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("incomplete marker remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(expectedRoot, manifestPath)); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(expectedRoot, "gradlew"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("gradlew mode = %04o, want 0755", info.Mode().Perm())
		}
	}
}

func TestGenerateNeverOverwritesExistingOutput(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "empty directory",
			setup: func(t *testing.T, target string) {
				t.Helper()
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non-empty directory",
			setup: func(t *testing.T, target string) {
				t.Helper()
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(target, "sentinel.txt"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "file",
			setup: func(t *testing.T, target string) {
				t.Helper()
				if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name  string
			setup func(*testing.T, string)
		}{
			name: "symbolic link",
			setup: func(t *testing.T, target string) {
				t.Helper()
				realTarget := target + "-real"
				if err := os.Mkdir(realTarget, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(realTarget, target); err != nil {
					t.Fatal(err)
				}
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "guide_npc")
			test.setup(t, target)
			before := snapshotTree(t, base)
			_, err := GenerateAt(base, testOptions(HostLuanti))
			if err == nil {
				t.Fatal("GenerateAt() unexpectedly overwrote existing output")
			}
			after := snapshotTree(t, base)
			if !bytes.Equal(before, after) {
				t.Fatalf("existing output changed\nbefore: %q\nafter:  %q", before, after)
			}
		})
	}
}

func TestOutputValidationRejectsEscapesAndWindowsHazards(t *testing.T) {
	base := t.TempDir()
	options := testOptions(HostLuanti)
	invalid := []string{
		"../escape",
		"nested/../../escape",
		`nested\..\escape`,
		"/absolute",
		"C:/escape",
		"nested//guide",
		"nested/./guide",
		"CON",
		"COM¹.txt",
		"guide.",
		"guide ",
		"file:stream",
		string([]byte{0xff}),
		strings.Repeat("界", maxPortablePathSegmentUTF16+1),
	}
	for _, output := range invalid {
		options.Output = output
		if _, err := GenerateAt(base, options); err == nil {
			t.Errorf("GenerateAt() accepted unsafe output %q", output)
		}
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe outputs left files behind: %v", entries)
	}
}

func TestOutputValidationRejectsCaseCollisionAndSymlinkAncestor(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "Guide_Npc"), 0o755); err != nil {
		t.Fatal(err)
	}
	options := testOptions(HostLuanti)
	if _, err := GenerateAt(base, options); err == nil ||
		!strings.Contains(err.Error(), "collides by case") {
		t.Fatalf("case collision error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	link := filepath.Join(base, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	options.Output = "linked/new_mod"
	if _, err := GenerateAt(base, options); err == nil ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink ancestor error = %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("generation escaped through an output symlink")
	}
}

func TestWriteFailureRetainsIncompleteTreeAndForeignFiles(t *testing.T) {
	base := t.TempDir()
	plan, err := Render(testOptions(HostLuanti))
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.writeAtWithHooks(base, writeHooks{
		beforeFile: func(target, relative string, index int) error {
			if index == 2 {
				unknown := filepath.Join(target, "user-created.txt")
				if writeErr := os.WriteFile(
					unknown, []byte("keep"), 0o644); writeErr != nil {
					return writeErr
				}
				return errors.New("injected write failure")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("writeAtWithHooks() error = %v", err)
	}
	target := filepath.Join(base, "guide_npc")
	unknown, readErr := os.ReadFile(filepath.Join(target, "user-created.txt"))
	if readErr != nil || string(unknown) != "keep" {
		t.Fatalf("unknown concurrent file was removed or changed: %q, %v", unknown, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, incompleteMarker)); statErr != nil {
		t.Fatalf("incomplete marker must remain beside unknown files: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, manifestPath)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed tree unexpectedly has a manifest: %v", statErr)
	}
}

func TestWriteFailureWithoutForeignFilesRetainsIncompleteTree(t *testing.T) {
	base := t.TempDir()
	plan, err := Render(testOptions(HostLuanti))
	if err != nil {
		t.Fatal(err)
	}
	_, err = plan.writeAtWithHooks(base, writeHooks{
		beforeFile: func(target, relative string, index int) error {
			if index == 1 {
				return errors.New("injected write failure")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("writeAtWithHooks() unexpectedly succeeded")
	}
	target := filepath.Join(base, "guide_npc")
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("failed tree was removed: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, incompleteMarker)); statErr != nil {
		t.Fatalf("failed tree is missing incomplete marker: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, manifestPath)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed tree unexpectedly has a manifest: %v", statErr)
	}
}

func TestConcurrentTargetReplacementIsNeverCleanedByPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming an open directory has different sharing semantics on Windows")
	}
	base := t.TempDir()
	plan, err := Render(testOptions(HostLuanti))
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(base, "owned-moved")
	replacement := filepath.Join(base, "guide_npc")
	_, err = plan.writeAtWithHooks(base, writeHooks{
		beforeFile: func(target, relative string, index int) error {
			if index != 0 {
				return nil
			}
			if err := os.Rename(target, moved); err != nil {
				return err
			}
			if err := os.Mkdir(replacement, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(
				filepath.Join(replacement, "sentinel.txt"),
				[]byte("keep"),
				0o644,
			); err != nil {
				return err
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "verify output directory") {
		t.Fatalf("writeAtWithHooks() error = %v, want replaced-directory rejection", err)
	}
	payload, readErr := os.ReadFile(filepath.Join(replacement, "sentinel.txt"))
	if readErr != nil || string(payload) != "keep" {
		t.Fatalf("replacement target was removed or changed: %q, %v", payload, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(moved, incompleteMarker)); statErr != nil {
		t.Fatalf("owned directory lost its incomplete marker: %v", statErr)
	}
}

func TestConcurrentOutputAncestorReplacementCannotMisreportResultPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("renaming an open directory has different sharing semantics on Windows")
	}
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	options := testOptions(HostLuanti)
	options.Output = "parent/guide_npc"
	plan, err := Render(options)
	if err != nil {
		t.Fatal(err)
	}
	movedParent := filepath.Join(base, "parent-owned")
	replacementTarget := filepath.Join(parent, "guide_npc")
	_, err = plan.writeAtWithHooks(base, writeHooks{
		beforeFile: func(target, relative string, index int) error {
			if index != 0 {
				return nil
			}
			if err := os.Rename(parent, movedParent); err != nil {
				return err
			}
			if err := os.Mkdir(parent, 0o755); err != nil {
				return err
			}
			if err := os.Mkdir(replacementTarget, 0o755); err != nil {
				return err
			}
			return os.WriteFile(
				filepath.Join(replacementTarget, "sentinel.txt"),
				[]byte("keep"),
				0o644,
			)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "output ancestor") {
		t.Fatalf("writeAtWithHooks() error = %v, want ancestor replacement rejection", err)
	}
	payload, readErr := os.ReadFile(filepath.Join(replacementTarget, "sentinel.txt"))
	if readErr != nil || string(payload) != "keep" {
		t.Fatalf("replacement result path was modified: %q, %v", payload, readErr)
	}
	ownedTarget := filepath.Join(movedParent, "guide_npc")
	if _, statErr := os.Stat(filepath.Join(ownedTarget, incompleteMarker)); statErr != nil {
		t.Fatalf("owned target lost its incomplete marker: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(ownedTarget, "README.md")); statErr != nil {
		t.Fatalf("writer no longer followed its held target Root: %v", statErr)
	}
}

func TestConcurrentTemplateParentSymlinkCannotEscapeTargetRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks normally requires additional privileges on Windows")
	}
	base := t.TempDir()
	outside := t.TempDir()
	options, err := normalizeOptions(testOptions(HostLuanti))
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		options: options,
		files: []renderedFile{{
			Path: "nested/file.txt",
			Mode: 0o644,
			Data: []byte("owned\n"),
			Role: "test",
		}},
	}
	_, err = plan.writeAtWithHooks(base, writeHooks{
		beforeFile: func(target, relative string, index int) error {
			nested := filepath.Join(target, "nested")
			if err := os.Rename(nested, nested+"-owned"); err != nil {
				return err
			}
			return os.Symlink(outside, nested)
		},
	})
	if err == nil {
		t.Fatal("writeAtWithHooks() accepted a concurrently replaced template parent")
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("generation escaped through a concurrent symlink: %v", entries)
	}
	if payload, readErr := os.ReadFile(
		filepath.Join(base, "guide_npc", "nested-owned", "file.txt"),
	); readErr != nil || string(payload) != "owned\n" {
		t.Fatalf("directory handle did not retain the owned target: %q, %v", payload, readErr)
	}
}

func TestOutputValidationEnforcesPortableUTF16PathBudget(t *testing.T) {
	base := t.TempDir()
	options := testOptions(HostLuanti)
	options.Output = strings.Repeat("a", maxPortableAbsoluteUTF16)
	_, err := GenerateAt(base, options)
	if err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Fatalf("overlong absolute output error = %v", err)
	}
	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("overlong output created files: %v", entries)
	}
}

func TestDotSlashOutputIsAccepted(t *testing.T) {
	base := t.TempDir()
	options := testOptions(HostLuanti)
	options.Output = "./guide_npc"
	if _, err := GenerateAt(base, options); err != nil {
		t.Fatal(err)
	}
}

func snapshotTree(t *testing.T, root string) []byte {
	t.Helper()
	var builder strings.Builder
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		builder.WriteString(relative)
		builder.WriteByte('\n')
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			payload, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			builder.Write(payload)
			builder.WriteByte('\n')
		} else if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			builder.WriteString("->")
			builder.WriteString(target)
			builder.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return []byte(builder.String())
}
