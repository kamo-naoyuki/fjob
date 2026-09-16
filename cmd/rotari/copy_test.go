package main

import (
	"path/filepath"
	"testing"
)

func TestCopyRunToQueuePreservesSourceJobIDs(t *testing.T) {
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
	if copied.ID != "train-id" || copied.Name != "train" || len(copied.DependsOn) != 0 {
		t.Fatalf("copied command = %#v, want the source ID preserved and no excluded dependency", copied)
	}
	if copied.Origin == nil || copied.Origin.RunID != "run-1" || copied.Origin.JobID != "train-id" || copied.Origin.Status != "failed" {
		t.Fatalf("copied origin = %#v, want source run/job and failed status", copied.Origin)
	}
}

func TestCopyRunToQueueReassignsIDOnCollision(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "train-id", Command: []string{"existing"}}}}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	snapshot := Queue{Commands: []QueuedCommand{
		{ID: "train-id", Name: "train", Command: []string{"train"}},
	}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), snapshot); err != nil {
		t.Fatal(err)
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
	if queue.Commands[1].ID == "train-id" {
		t.Fatal("colliding job ID was not reassigned")
	}
}

func TestCopyRunToQueueCombinesResultSelections(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	snapshot := Queue{Commands: []QueuedCommand{
		{ID: "success-id", Name: "success", Command: []string{"success"}},
		{ID: "failed-id", Name: "failed", Command: []string{"failed"}},
		{ID: "unfinished-id", Name: "unfinished", Command: []string{"unfinished"}},
	}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{Results: []JobResult{
		{ID: "success-id", ExitCode: 0},
		{ID: "failed-id", ExitCode: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := copyRunToQueue(baseDir, "default", "run-1", "failed,unfinished", []string{"success-id"}, false); err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 3 || queue.Commands[0].Name != "success" || queue.Commands[1].Name != "failed" || queue.Commands[2].Name != "unfinished" {
		t.Fatalf("combined selection = %#v, want selected filters plus job ID", queue.Commands)
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
