package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAvailableProvidersFindsAgy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	if got := AvailableProviders(); !slices.Contains(got, "antigravity") {
		t.Fatalf("agy on PATH, got %v", got)
	}
}
