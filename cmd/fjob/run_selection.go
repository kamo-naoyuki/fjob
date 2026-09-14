package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errNoPreviousRun = errors.New("no previous run")

func prepareRunSelection(baseDir, queueName, selection string) (int, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return 0, err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return 0, fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return 0, fmt.Errorf("failed to check queue: %w", err)
	}
	if running {
		return 0, fmt.Errorf("queue '%s' is running; selection is not allowed", queueName)
	}

	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return 0, fmt.Errorf("failed to load metadata: %w", err)
	}
	if meta.LastRunID == "" {
		return 0, fmt.Errorf("queue '%s' has no previous run: %w", queueName, errNoPreviousRun)
	}
	runDir := filepath.Join(paths.runsDir, meta.LastRunID)
	if diff, err := compareQueueWithRun(paths.queueFile, filepath.Join(runDir, "commands.json")); err == nil && diff.HasChanges() {
		fmt.Printf("Queue differs from previous run %s:\n  Added: %d\n  Removed: %d\n  Changed: %d\n", meta.LastRunID, diff.Added, diff.Removed, diff.Changed)
	}
	summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
	if err != nil {
		return 0, fmt.Errorf("failed to load run summary: %w", err)
	}
	results := make(map[string]int)
	for _, result := range summary.Results {
		results[result.ID] = result.ExitCode
	}

	snapshot, err := loadQueue(paths.queueFile)
	if err != nil {
		return 0, fmt.Errorf("failed to load queue: %w", err)
	}
	if len(snapshot.Commands) == 0 {
		data, err := os.ReadFile(filepath.Join(runDir, "commands.json"))
		if err != nil {
			return 0, fmt.Errorf("failed to load command snapshot: %w", err)
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return 0, fmt.Errorf("failed to parse command snapshot: %w", err)
		}
	}
	selected := make([]QueuedCommand, 0)
	selectedNames := make(map[string]bool)
	for _, job := range queueToJobs(snapshot.Commands) {
		exitCode, finished := results[job.ID]
		include := false
		switch selection {
		case "failed":
			include = finished && exitCode != 0
		case "success":
			include = finished && exitCode == 0
		case "unfinished":
			include = !finished
		case "nonsuccess":
			include = !finished || exitCode != 0
		default:
			return 0, fmt.Errorf("unknown run selection: %s", selection)
		}
		if include {
			selectedNames[job.Name] = true
			selected = append(selected, QueuedCommand{
				ID: job.ID, Command: job.Command, Backend: job.Backend, SbatchOptions: job.SbatchOptions, Name: job.Name, DependsOn: job.DependsOn,
			})
		}
	}
	if len(selected) == 0 {
		return 0, fmt.Errorf("last run has no jobs matching --%s", selection)
	}
	totalJobs := len(queueToJobs(snapshot.Commands))
	for index := range selected {
		dependencies := selected[index].DependsOn[:0]
		for _, dependency := range selected[index].DependsOn {
			if selectedNames[dependency] {
				dependencies = append(dependencies, dependency)
			}
		}
		selected[index].DependsOn = dependencies
	}
	selectedCount := len(selected)
	snapshot.Commands = selected
	if err := writeJSON(paths.queueFile, snapshot); err != nil {
		return 0, fmt.Errorf("failed to prepare selected jobs: %w", err)
	}
	fmt.Printf("selected jobs=%d/%d queue=%s filter=--%s run=%s\n", selectedCount, totalJobs, queueName, selection, meta.LastRunID)
	return len(selected), nil
}
