package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSchedulerStatusRoundTripNormalizesState(t *testing.T) {
	jobDir := t.TempDir()
	writeSchedulerStatus(jobDir, "  RUNNING  ")

	if state := loadSchedulerStatus(jobDir); state != "running" {
		t.Fatalf("scheduler state = %q, want running", state)
	}
}

func TestWriteSchedulerStatusIgnoresEmptyState(t *testing.T) {
	jobDir := t.TempDir()
	writeSchedulerStatus(jobDir, "  ")

	if _, err := os.Stat(filepath.Join(jobDir, "scheduler_status.json")); !os.IsNotExist(err) {
		t.Fatalf("empty scheduler status created a file: %v", err)
	}
}

func TestLoadSchedulerStatusIgnoresMalformedFile(t *testing.T) {
	jobDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(jobDir, "scheduler_status.json"), []byte("{invalid}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if state := loadSchedulerStatus(jobDir); state != "" {
		t.Fatalf("scheduler state = %q, want empty state", state)
	}
}
