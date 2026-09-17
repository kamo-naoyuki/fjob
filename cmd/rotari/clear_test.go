package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteAllRunsResetsMetadata(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		if err := os.MkdirAll(filepath.Join(paths.runsDir, runID), 0o755); err != nil {
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
}
