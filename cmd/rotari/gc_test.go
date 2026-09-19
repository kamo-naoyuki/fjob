package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunRegistryGCFindsAndAppliesOnlyOrphans(t *testing.T) {
	masterDir := t.TempDir()
	baseDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	locations := []runLocation{
		{BaseDir: baseDir, ProjectName: "demo", RunID: "missing"},
		{BaseDir: baseDir, ProjectName: "demo", RunID: "live"},
	}
	for _, location := range locations {
		if err := registerRunLocation(location); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "projects", "demo", "runs", "live"), 0o755); err != nil {
		t.Fatal(err)
	}

	orphans, err := orphanRunRegistryEntries(masterDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].RunID != "missing" {
		t.Fatalf("orphans = %+v, want only missing run", orphans)
	}
	cache := runRegistryGCCache{CreatedAt: nowRFC3339(), Entries: orphans}
	if err := writeJSON(filepath.Join(masterDir, "gc.json"), cache); err != nil {
		t.Fatal(err)
	}

	if code := applyRunRegistryGC(masterDir); code != 0 {
		t.Fatalf("applyRunRegistryGC() = %d, want 0", code)
	}
	if _, found, err := resolveRunLocation("missing"); err != nil || found {
		t.Fatalf("missing entry: found=%v, err=%v; want removed", found, err)
	}
	if _, found, err := resolveRunLocation("live"); err != nil || !found {
		t.Fatalf("live entry: found=%v, err=%v; want retained", found, err)
	}
}

func TestApplyRunRegistryGCRejectsExpiredPlan(t *testing.T) {
	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	cache := runRegistryGCCache{CreatedAt: "2000-01-01T00:00:00Z"}
	if err := writeJSON(filepath.Join(masterDir, "gc.json"), cache); err != nil {
		t.Fatal(err)
	}
	if code := applyRunRegistryGC(masterDir); code == 0 {
		t.Fatal("applyRunRegistryGC() succeeded with an expired plan")
	}
}
