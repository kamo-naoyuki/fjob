package main

import (
	"flag"
	"fmt"
	"os"
)

func cmdRemove(args []string) int {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	runID := cliString(fs, "run-id", "")
	jobName := cliString(fs, "job-name", "")
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) > 0 || (*jobName == "" && len(jobIDs) == 0) || (*jobName != "" && len(jobIDs) > 0) {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("remove"))
		return 1
	}

	baseDir, queueName, err := resolveExistingRunTarget(*basedir, *queueNameOption, *runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	message, err := removeBatch(baseDir, queueName, *runID, jobIDs, *jobName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cyan(message))
	return 0
}

func removeBatch(baseDir, queueName, requestedRunID string, requestedJobIDs []string, requestedJobName string) (string, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return "", fmt.Errorf("failed to check queue: %w", err)
	}
	if running {
		return "", fmt.Errorf("project %q is running; remove is not allowed", queueName)
	}

	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", fmt.Errorf("failed to load queue: %w", err)
	}
	if len(queue.Commands) == 0 || requestedRunID != "" {
		queue, err = loadChangeSnapshot(paths, requestedRunID)
		if err != nil {
			return "", err
		}
	}

	removeIDs := make(map[string]bool, len(requestedJobIDs))
	for _, id := range requestedJobIDs {
		removeIDs[id] = true
	}
	removed := make([]QueuedCommand, 0, len(queue.Commands))
	for _, job := range queue.Commands {
		if removeIDs[job.ID] || (requestedJobName != "" && job.Name == requestedJobName) {
			removed = append(removed, job)
		}
	}
	if len(removed) == 0 {
		return "", fmt.Errorf("job not found")
	}
	if len(removed) != len(removeIDs) {
		return "", fmt.Errorf("one or more jobs not found")
	}
	removedNames := make(map[string]bool, len(removed))
	for _, job := range removed {
		if job.Name != "" {
			removedNames[job.Name] = true
		}
	}
	remaining := make([]QueuedCommand, 0, len(queue.Commands)-len(removed))
	for _, job := range queue.Commands {
		if removeIDs[job.ID] || (requestedJobName != "" && job.Name == requestedJobName) {
			continue
		}
		for _, dependency := range job.DependsOn {
			if removedNames[dependency] {
				return "", fmt.Errorf("job %q is referenced by dependency; remove is not allowed", dependency)
			}
		}
		remaining = append(remaining, job)
	}
	queue.Commands = remaining
	if err := validateDependencies(queueToJobs(queue.Commands)); err != nil {
		return "", fmt.Errorf("invalid dependencies: %w", err)
	}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return "", fmt.Errorf("failed to save removed queue: %w", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return "", fmt.Errorf("failed to load metadata: %w", err)
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return "", fmt.Errorf("failed to update metadata: %w", err)
	}
	return fmt.Sprintf("removed %d job(s) from queue=%s", len(removed), queueName), nil
}
