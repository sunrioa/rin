package privatefile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sunrioa/rin/internal/privatefile"
)

func TestWriteJSONBoundedRejectsOversizeWithoutReplacingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := privatefile.WriteJSON(path, map[string]string{"state": "original"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := privatefile.WriteJSONBounded(
		path, map[string]string{"state": "replacement"}, 8,
	); err == nil {
		t.Fatal("oversized private JSON was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected private JSON replaced the previous file")
	}
}

func TestReadJSONRejectsDuplicateObjectNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"state":"first","state":"last"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := privatefile.ReadJSON(path, 1<<20, &value); err == nil {
		t.Fatal("private JSON with duplicate object names was accepted")
	}
}

func TestReadJSONRejectsOversizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"state":"too-large"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := privatefile.ReadJSON(path, 8, &value); err == nil {
		t.Fatal("oversized private JSON was accepted")
	}
}

func TestWriteRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available on Windows CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.WriteJSON(
		filepath.Join(link, "state.json"), map[string]bool{"safe": true},
	); err == nil {
		t.Fatal("private JSON followed a symlink parent")
	}
}
