package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterRunLocationIsIdempotentAndRejectsConflict(t *testing.T) {
	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	location := runLocation{BaseDir: t.TempDir(), ProjectName: "demo", RunID: "run-1"}

	if err := registerRunLocation(location); err != nil {
		t.Fatal(err)
	}
	if err := registerRunLocation(location); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
	conflict := location
	conflict.ProjectName = "other"
	if err := registerRunLocation(conflict); err == nil || !strings.Contains(err.Error(), "another location") {
		t.Fatalf("conflicting registration error = %v", err)
	}

	resolved, found, err := resolveRunLocation(location.RunID)
	if err != nil {
		t.Fatal(err)
	}
	wantBaseDir, err := filepath.Abs(location.BaseDir)
	if err != nil {
		t.Fatal(err)
	}
	if !found || resolved.BaseDir != wantBaseDir || resolved.ProjectName != location.ProjectName {
		t.Fatalf("resolved = %+v, found = %v", resolved, found)
	}
}

func TestResolveRunLocationRejectsMalformedEntry(t *testing.T) {
	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	dir := filepath.Join(masterDir, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := runLocationPath(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{invalid}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, found, err := resolveRunLocation("run-1"); err == nil || found {
		t.Fatalf("found = %v, error = %v; want malformed-entry error", found, err)
	}
}

func TestRunLocationPathRejectsUnsafeRunIDs(t *testing.T) {
	for _, runID := range []string{"", "../run-1", "nested/run-1"} {
		if _, err := runLocationPath(t.TempDir(), runID); err == nil {
			t.Fatalf("run ID %q was accepted", runID)
		}
	}
}
