package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func cmdChange(args []string) int {
	fs := flag.NewFlagSet("change", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	runID := cliString(fs, "run-id", "")
	jobID := cliString(fs, "job-id", "")
	jobName := cliString(fs, "job-name", "")
	backend := cliString(fs, "backend", "")
	var sbatchOptions stringSliceFlag
	cliValue(fs, &sbatchOptions, "sbatch-option")
	clearSbatchOptions := cliBool(fs, "clear-sbatch-options", false)
	setJobName := cliString(fs, "set-job-name", "")
	var dependsOn stringSliceFlag
	cliValue(fs, &dependsOn, "depends-on")
	clearDependsOn := cliBool(fs, "clear-depends-on", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if (*jobID == "" && *jobName == "") || (*jobID != "" && *jobName != "") ||
		(len(fs.Args()) == 0 && *backend == "" && len(sbatchOptions) == 0 && !*clearSbatchOptions &&
			*setJobName == "" && len(dependsOn) == 0 && !*clearDependsOn) ||
		(*backend != "" && *backend != "local" && *backend != "slurm") {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("change"))
		return 1
	}

	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveQueueName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	message, err := changeBatch(baseDir, queueName, *runID, *jobID, *jobName, *backend,
		sbatchOptions, *clearSbatchOptions, *setJobName, dependsOn, *clearDependsOn, fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cyan(message))
	return 0
}

func changeBatch(baseDir, queueName, requestedRunID, requestedJobID, requestedJobName, backend string,
	sbatchOptions []string, clearSbatchOptions bool, setJobName string, dependsOn []string,
	clearDependsOn bool, command []string) (string, error) {
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
		return "", fmt.Errorf("queue %q is running; change is not allowed", queueName)
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

	jobs := queueToJobs(queue.Commands)
	jobIndex := -1
	for index, job := range jobs {
		if (requestedJobID != "" && job.ID == requestedJobID) ||
			(requestedJobName != "" && job.Name == requestedJobName) {
			if jobIndex != -1 {
				return "", fmt.Errorf("job selector matches multiple jobs")
			}
			jobIndex = index
		}
	}
	if jobIndex == -1 {
		return "", fmt.Errorf("job not found")
	}
	changed := &queue.Commands[jobIndex]
	if backend != "" {
		changed.Backend = backend
	}
	if len(sbatchOptions) > 0 || clearSbatchOptions {
		changed.SbatchOptions = append([]string(nil), sbatchOptions...)
	}
	if setJobName != "" {
		for index, job := range queueToJobs(queue.Commands) {
			if index != jobIndex && job.Name == setJobName {
				return "", fmt.Errorf("job name %q is already in use", setJobName)
			}
		}
		for index := range queue.Commands {
			if index != jobIndex {
				for _, dependency := range queue.Commands[index].DependsOn {
					if dependency == changed.Name {
						return "", fmt.Errorf("job %q is referenced by dependency; rename is not allowed", changed.Name)
					}
				}
			}
		}
		changed.Name = setJobName
	}
	if len(command) > 0 {
		changed.Command = append([]string(nil), command...)
	}
	if len(dependsOn) > 0 || clearDependsOn {
		changed.DependsOn = append([]string(nil), dependsOn...)
	}
	if err := validateDependencies(queueToJobs(queue.Commands)); err != nil {
		return "", fmt.Errorf("invalid dependencies: %w", err)
	}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return "", fmt.Errorf("failed to save changed queue: %w", err)
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
	return fmt.Sprintf("changed queue=%s job=%s", queueName, jobs[jobIndex].ID), nil
}

func loadChangeSnapshot(paths pathSet, requestedRunID string) (Queue, error) {
	runID, err := selectRunID(paths, requestedRunID)
	if err != nil {
		return Queue{}, err
	}
	data, err := os.ReadFile(filepath.Join(paths.runsDir, runID, "commands.json"))
	if err != nil {
		return Queue{}, fmt.Errorf("failed to load command snapshot: %w", err)
	}
	var queue Queue
	if err := json.Unmarshal(data, &queue); err != nil {
		return Queue{}, fmt.Errorf("failed to parse command snapshot: %w", err)
	}
	if len(queue.Commands) == 0 {
		return Queue{}, errors.New("command snapshot has no jobs")
	}
	return queue, nil
}