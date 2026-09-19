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
	workingDirectory := cliString(fs, "working-directory", "")
	clearWorkingDirectory := cliBool(fs, "clear-working-directory", false)
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
		(len(fs.Args()) == 0 && *executor == "" && len(executorOptions) == 0 && !*clearExecutorOptions && *workingDirectory == "" && !*clearWorkingDirectory && len(environment) == 0 && !*clearEnvironment &&
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
	message, err := changeBatchWithWorkingDirectory(baseDir, queueName, *runID, *jobID, *jobName, *executor,
		executorOptions, *clearExecutorOptions, environment, *clearEnvironment, *workingDirectory, *clearWorkingDirectory, *setJobName, dependsOn, *clearDependsOn, fs.Args())
	if err != nil {
		printError(err)
		return 1
	}
	fmt.Println(colorKeyValueMessage(message, green))
	return 0
}

func changeBatch(baseDir, queueName, requestedRunID, requestedJobID, requestedJobName, executor string,
	executorOptions []string, clearExecutorOptions bool, environment []string, clearEnvironment bool, setJobName string, dependsOn []string,
	clearDependsOn bool, command []string) (string, error) {
	return changeBatchWithWorkingDirectory(baseDir, queueName, requestedRunID, requestedJobID, requestedJobName, executor,
		executorOptions, clearExecutorOptions, environment, clearEnvironment, "", false, setJobName, dependsOn, clearDependsOn, command)
}

type changeMutation struct {
	executor              string
	executorOptions       []string
	clearExecutorOptions  bool
	environment           []string
	clearEnvironment      bool
	workingDirectory      string
	clearWorkingDirectory bool
	setJobName            string
	dependsOn             []string
	clearDependsOn        bool
	command               []string
}

func changeBatchWithWorkingDirectory(baseDir, queueName, requestedRunID, requestedJobID, requestedJobName, executor string,
	executorOptions []string, clearExecutorOptions bool, environment []string, clearEnvironment bool, workingDirectory string, clearWorkingDirectory bool, setJobName string, dependsOn []string,
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
	if err := ensureProjectIdleForPaths(paths, "change"); err != nil {
		return "", err
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
	jobIndex, err := selectChangeJob(jobs, requestedJobID, requestedJobName)
	if err != nil {
		return "", err
	}
	mutation := changeMutation{executor: executor, executorOptions: executorOptions, clearExecutorOptions: clearExecutorOptions,
		environment: environment, clearEnvironment: clearEnvironment, workingDirectory: workingDirectory,
		clearWorkingDirectory: clearWorkingDirectory, setJobName: setJobName, dependsOn: dependsOn,
		clearDependsOn: clearDependsOn, command: command}
	if err := applyChangeMutation(queue, jobIndex, mutation); err != nil {
		return "", err
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

func selectChangeJob(jobs []JobSpec, requestedJobID, requestedJobName string) (int, error) {
	jobIndex := -1
	for index, job := range jobs {
		if (requestedJobID == "" || job.ID != requestedJobID) &&
			(requestedJobName == "" || job.Name != requestedJobName) {
			continue
		}
		if jobIndex != -1 {
			return -1, fmt.Errorf("job selector matches multiple jobs")
		}
		jobIndex = index
	}
	if jobIndex == -1 {
		return -1, fmt.Errorf("job not found")
	}
	return jobIndex, nil
}

func applyChangeMutation(queue Queue, jobIndex int, mutation changeMutation) error {
	changed := &queue.Commands[jobIndex]
	if mutation.executor != "" {
		changed.Executor = mutation.executor
	}
	if len(mutation.executorOptions) > 0 || mutation.clearExecutorOptions {
		changed.ExecutorOptions = append([]string(nil), mutation.executorOptions...)
	}
	if len(mutation.environment) > 0 || mutation.clearEnvironment {
		changed.Environment = append([]string(nil), mutation.environment...)
	}
	if mutation.workingDirectory != "" || mutation.clearWorkingDirectory {
		changed.WorkingDirectory = mutation.workingDirectory
	}
	if mutation.setJobName != "" && mutation.setJobName != changed.Name {
		if err := validateChangeRename(queue, jobIndex, mutation.setJobName); err != nil {
			return err
		}
		changed.Name = mutation.setJobName
	}
	if len(mutation.command) > 0 {
		changed.Command = append([]string(nil), mutation.command...)
	}
	if len(mutation.dependsOn) > 0 || mutation.clearDependsOn {
		changed.DependsOn = append([]string(nil), mutation.dependsOn...)
	}
	return nil
}

func validateChangeRename(queue Queue, jobIndex int, newName string) error {
	jobs := queueToJobs(queue.Commands)
	for index, job := range jobs {
		if index != jobIndex && job.Name == newName {
			return fmt.Errorf("job name %q is already in use", newName)
		}
	}
	oldName := jobs[jobIndex].Name
	for index, command := range queue.Commands {
		if index == jobIndex {
			continue
		}
		for _, dependency := range command.DependsOn {
			if dependency == oldName {
				return fmt.Errorf("job %q is referenced by dependency; rename is not allowed", oldName)
			}
		}
	}
	return nil
}

func loadChangeSnapshot(paths pathSet, requestedRunID string) (Queue, error) {
	runID, err := selectRunID(paths, requestedRunID)
	if err != nil {
		return Queue{}, err
	}
	runDir, err := validatedRunDir(paths, runID)
	if err != nil {
		return Queue{}, err
	}
	data, err := os.ReadFile(filepath.Join(runDir, "commands.json")) // NOSONAR: runDir is produced by validatedRunDir.
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
