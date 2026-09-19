package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteAllRunsResetsMetadata(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		if err := os.MkdirAll(filepath.Join(paths.runsDir, runID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := registerRun(paths, runID); err != nil {
			t.Fatal(err)
		}
	}
	meta := defaultMeta()
	meta.Phase = "finished"
	meta.LastRunID = "run-2"
	meta.LastRunExitCode = 7
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	if code := cmdDelete([]string{"--basedir", baseDir, "--project-name", "demo"}); code != 0 {
		t.Fatalf("cmdDelete exit code = %d, want 0", code)
	}
	if _, err := os.Stat(paths.runsDir); !os.IsNotExist(err) {
		t.Fatalf("runs directory remains after deleting all history: %v", err)
	}
	updated, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Phase != "collecting" || updated.LastRunID != "" || updated.LastRunExitCode != 0 {
		t.Fatalf("metadata = %+v, want reset collecting state", updated)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		if _, found, err := resolveRunLocation(runID); err != nil || found {
			t.Fatalf("registry entry for %q: found=%v, err=%v; want removed", runID, found, err)
		}
	}
}

func TestClearRunHistoryRemovesRegistryEntry(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	if err := os.MkdirAll(filepath.Join(paths.runsDir, runID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: runID}); err != nil {
		t.Fatal(err)
	}
	if err := registerRun(paths, runID); err != nil {
		t.Fatal(err)
	}

	if err := clearRunHistory(baseDir, "demo", runID); err != nil {
		t.Fatalf("clearRunHistory() error = %v", err)
	}
	if _, found, err := resolveRunLocation(runID); err != nil || found {
		t.Fatalf("registry entry: found=%v, err=%v; want removed", found, err)
	}
}

func TestCmdDeleteRejectsUnsafeProjectAndRunIDsAtCLI(t *testing.T) {
	baseDir := t.TempDir()
	for _, args := range [][]string{
		{"--basedir", baseDir, "--project-name", "../outside"},
		{"--basedir", baseDir, "--project-name", "demo", "--run-id", "../outside"},
		{"--basedir", baseDir, "--project-name", "demo", "--run-id", "nested/run-1"},
	} {
		if cmdDelete(args) == 0 {
			t.Fatalf("cmdDelete accepted unsafe CLI input: %v", args)
		}
	}

	if cmdAdd([]string{"--basedir", baseDir, "--project-name", "../outside", "echo", "ok"}) == 0 {
		t.Fatal("cmdAdd accepted unsafe project name through CLI")
	}
}
