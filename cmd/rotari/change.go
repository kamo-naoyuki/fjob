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
	queueNameOption := cliString(fs, "project-name", "")
	runID := cliString(fs, "run-id", "")
	jobID := cliString(fs, "job-id", "")
	jobName := cliString(fs, "job-name", "")
	executor := cliString(fs, "executor", "")
	var executorOptions stringSliceFlag
	cliValue(fs, &executorOptions, "executor-option")
	clearExecutorOptions := cliBool(fs, "clear-executor-options", false)
	var environment stringSliceFlag
	cliValue(fs, &environment, "env")
	clearEnvironment := cliBool(fs, "clear-env", false)
	setJobName := cliString(fs, "set-job-name", "")
	var dependsOn stringSliceFlag
	cliValue(fs, &dependsOn, "depends-on")
	clearDependsOn := cliBool(fs, "clear-depends-on", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if (*jobID == "" && *jobName == "") || (*jobID != "" && *jobName != "") ||
		(len(fs.Args()) == 0 && *executor == "" && len(executorOptions) == 0 && !*clearExecutorOptions && len(environment) == 0 && !*clearEnvironment &&
			*setJobName == "" && len(dependsOn) == 0 && !*clearDependsOn) ||
		(*executor != "" && !isKnownExecutor(*executor)) {
		printError("usage: " + cliUsage("change"))
		return 1
	}
	if err := validateEnvironment(environment); err != nil {
		printErrorf("invalid --env: %v", err)
		return 1
	}

	baseDir, queueName, err := resolveExistingRunTarget(*basedir, *queueNameOption, *runID)
	if err != nil {
		printError(err)
		return 1
	}
	message, err := changeBatch(baseDir, queueName, *runID, *jobID, *jobName, *executor,
		executorOptions, *clearExecutorOptions, environment, *clearEnvironment, *setJobName, dependsOn, *clearDependsOn, fs.Args())
	if err != nil {
		printError(err)
		return 1
	}
	fmt.Println(cyan(message))
	return 0
}

func changeBatch(baseDir, queueName, requestedRunID, requestedJobID, requestedJobName, executor string,
	executorOptions []string, clearExecutorOptions bool, environment []string, clearEnvironment bool, setJobName string, dependsOn []string,
	clearDependsOn bool, command []string) (string, error) {
	if err := validateEnvironment(environment); err != nil {
		return "", fmt.Errorf("invalid environment: %w", err)
	}
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
		return "", fmt.Errorf("project %q is running; change is not allowed", queueName)
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
	if executor != "" {
		changed.Executor = executor
	}
	if len(executorOptions) > 0 || clearExecutorOptions {
		changed.ExecutorOptions = append([]string(nil), executorOptions...)
	}
	if len(environment) > 0 || clearEnvironment {
		changed.Environment = append([]string(nil), environment...)
	}
	if setJobName != "" && setJobName != changed.Name {
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
