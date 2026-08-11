package main

import "testing"

func TestRootHelpReturnsSuccess(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatalf("run(--help): %v", err)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	if err := run([]string{"version", "extra"}); err == nil {
		t.Fatal("version accepted an extra argument")
	}
}
