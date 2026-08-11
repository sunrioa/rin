package main

import (
	"strings"
	"testing"
)

func TestRootHelpReturnsSuccess(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatalf("run(--help): %v", err)
	}
}

func TestRetiredSidecarCommandsAreUnavailable(t *testing.T) {
	for _, command := range []string{"serve", "inspect"} {
		err := run([]string{command})
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("run(%s) error = %v", command, err)
		}
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	if err := run([]string{"version", "extra"}); err == nil {
		t.Fatal("version accepted an extra argument")
	}
}
