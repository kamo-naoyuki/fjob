package main

import (
	"path/filepath"
	"testing"
)

func TestCopyRunToQueueCopiesSelectedJobsWithNewIDs(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	snapshot := Queue{Commands: []QueuedCommand{
		{ID: "prepare-id", Name: "prepare", Command: []string{"prepare"}},
		{ID: "train-id", Name: "train", Command: []string{"train"}, DependsOn: []string{"prepare"}},
		{ID: "test-id", Name: "test", Command: []string{"test"}, DependsOn: []string{"train"}},
	}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{Results: []JobResult{
		{ID: "prepare-id", ExitCode: 0},
		{ID: "train-id", ExitCode: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	message, err := copyRunToQueue(baseDir, "default", "run-1", "failed", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if message == "" {
		t.Fatal("copy message is empty")
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 {
		t.Fatalf("copied commands = %#v, want one command", queue.Commands)
	}
	copied := queue.Commands[0]
	if copied.ID == "train-id" || copied.Name != "train" || len(copied.DependsOn) != 0 {
		t.Fatalf("copied command = %#v, want a new ID and no excluded dependency", copied)
	}
	if copied.Origin == nil || copied.Origin.RunID != "run-1" || copied.Origin.JobID != "train-id" || copied.Origin.Status != "failed" {
		t.Fatalf("copied origin = %#v, want source run/job and failed status", copied.Origin)
	}
}

func TestCopyRunToQueueRequiresAppendForNonEmptyQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "existing", Command: []string{"existing"}}}}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	if err := writeJSON(filepath.Join(runDir, "commands.json"), Queue{Commands: []QueuedCommand{{ID: "source", Command: []string{"source"}}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := copyRunToQueue(baseDir, "default", "run-1", "all", nil, false); err == nil {
		t.Fatal("copy succeeded without --append")
	}
	if _, err := copyRunToQueue(baseDir, "default", "run-1", "all", nil, true); err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 2 {
		t.Fatalf("commands after append = %#v, want two commands", queue.Commands)
	}
}

func TestCopyRunToQueueCanOverwriteNonEmptyQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "existing", Command: []string{"existing"}}}}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	if err := writeJSON(filepath.Join(runDir, "commands.json"), Queue{Commands: []QueuedCommand{{ID: "source", Command: []string{"source"}}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := copyRunToQueue(baseDir, "default", "run-1", "all", nil, false, true); err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].Command[0] != "source" {
		t.Fatalf("commands after overwrite = %#v, want only copied command", queue.Commands)
	}
}
