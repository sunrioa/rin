package mcpconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestConfigRoundTripIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp-client.json")
	written := New("http://127.0.0.1:7375", testToken)
	if err := Write(path, written); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != written {
		t.Fatalf("loaded = %#v, want %#v", loaded, written)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("permissions = %o", info.Mode().Perm())
		}
	}
}

func TestConfigRejectsUnknownRemoteAndPublicFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-client.json")
	unknown := `{"schema_version":1,"control_url":"http://127.0.0.1:7375","token":"` +
		testToken + `","extra":true}`
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if err := Write(path, New("https://example.com", testToken)); err == nil {
		t.Fatal("remote Control URL was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("public file error = %v", err)
		}
	}
}
