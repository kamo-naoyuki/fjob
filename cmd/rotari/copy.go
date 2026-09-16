package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func confirmQueueOverwrite(baseDir, queueName string, appendJobs, overwriteJobs bool) (bool, error) {
	overwriteConfirmed := overwriteJobs
	if !appendJobs && !overwriteJobs {
		paths, pathErr := resolvePaths(baseDir, queueName)
		if pathErr != nil {
			return false, pathErr
		}
		queue, loadErr := loadQueue(paths.queueFile)
		if loadErr != nil {
			return false, loadErr
		}
		if len(queue.Commands) > 0 {
			if !isTerminal(os.Stdin) {
				return false, errors.New("queue is not empty; use --append or --overwrite")
			}
			fmt.Fprintf(os.Stderr, "queue %q contains %d jobs; overwrite it? [y/N] ", queueName, len(queue.Commands))
			answer, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
			if readErr != nil && len(answer) == 0 {
				return false, readErr
			}
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				return false, errors.New("copy cancelled")
			}
			overwriteConfirmed = true
		}
	}
	return overwriteConfirmed, nil
}

func cmdCopy(args []string) int {
	fs := flag.NewFlagSet("copy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	runID := cliString(fs, "run-id", "")
	failed := cliBool(fs, "failed", false)
	unfinished := cliBool(fs, "unfinished", false)
	success := cliBool(fs, "success", false)
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	appendJobs := cliBool(fs, "append", false)
	overwriteJobs := cliBool(fs, "overwrite", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 || *runID == "" || (*appendJobs && *overwriteJobs) {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("copy"))
		return 1
	}

	selection := resultSelection(*failed, *unfinished, *success)
	if len(jobIDs) > 0 {
		if selection == "" {
			selection = "job-id"
		}
	}
	if selection == "" {
		selection = "all"
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
	overwriteConfirmed, err := confirmQueueOverwrite(baseDir, queueName, *appendJobs, *overwriteJobs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	message, err := copyRunToQueue(baseDir, queueName, *runID, selection, jobIDs, *appendJobs, overwriteConfirmed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cyan(message))
	return 0
}

func copyRunToQueue(baseDir, queueName, runID, selection string, jobIDs []string, appendJobs bool, overwriteJobs ...bool) (string, error) {
	overwrite := len(overwriteJobs) > 0 && overwriteJobs[0]
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
		return "", fmt.Errorf("queue %q is running; copy is not allowed", queueName)
	}

	sourceRunDir := filepath.Join(paths.runsDir, runID)
	snapshot, err := loadQueue(filepath.Join(sourceRunDir, "commands.json"))
	if err != nil {
		return "", fmt.Errorf("failed to load command snapshot: %w", err)
	}
	if len(snapshot.Commands) == 0 {
		return "", errors.New("command snapshot has no jobs")
	}
	summary, summaryErr := loadRunSummary(filepath.Join(sourceRunDir, "summary.json"))
	if summaryErr != nil && selection != "all" {
		return "", fmt.Errorf("failed to load run summary: %w", summaryErr)
	}
	results := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		results[result.ID] = result
	}
	originCWD := ""
	if data, contextErr := os.ReadFile(filepath.Join(sourceRunDir, "context.json")); contextErr == nil {
		var context RunContext
		if json.Unmarshal(data, &context) == nil {
			originCWD = context.CWD
		}
	}
	requested := make(map[string]bool, len(jobIDs))
	for _, jobID := range jobIDs {
		requested[jobID] = true
	}
	selected := make([]QueuedCommand, 0, len(snapshot.Commands))
	selectedNames := make(map[string]bool)
	for _, command := range snapshot.Commands {
		result, finished := results[command.ID]
		include := false
		switch selection {
		case "all":
			include = true
		case "job-id":
			include = requested[command.ID]
		default:
			include = resultSelectionMatches(selection, finished, result.ExitCode)
		}
		if requested[command.ID] {
			include = true
			delete(requested, command.ID)
		}
		if include {
			selectedNames[command.Name] = true
			selected = append(selected, command)
		}
	}
	if len(requested) > 0 {
		missing := make([]string, 0, len(requested))
		for jobID := range requested {
			missing = append(missing, jobID)
		}
		sort.Strings(missing)
		return "", fmt.Errorf("job IDs not found in run %s: %s", runID, strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		return "", fmt.Errorf("run %s has no jobs matching selection", runID)
	}

	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", fmt.Errorf("failed to load queue: %w", err)
	}
	if len(queue.Commands) > 0 && !appendJobs && !overwrite {
		return "", fmt.Errorf("queue %q is not empty; use --append or --overwrite", queueName)
	}
	existingIDs := make(map[string]bool)
	if appendJobs {
		for _, command := range queue.Commands {
			existingIDs[command.ID] = true
		}
	}
	for index := range selected {
		sourceJobID := selected[index].ID
		dependencies := make([]string, 0, len(selected[index].DependsOn))
		for _, dependency := range selected[index].DependsOn {
			if selectedNames[dependency] {
				dependencies = append(dependencies, dependency)
			}
		}
		// Keep the source job ID so the copied job can still be matched
		// against the source run's results; only reassign on collision.
		if existingIDs[sourceJobID] {
			selected[index].ID = makeJobID()
		}
		existingIDs[selected[index].ID] = true
		selected[index].DependsOn = dependencies
		originStatus := "unfinished"
		if result, finished := results[sourceJobID]; finished {
			originStatus = "failed"
			if result.ExitCode == 0 {
				originStatus = "success"
			}
		}
		selected[index].Origin = &JobOrigin{RunID: runID, JobID: sourceJobID, Status: originStatus, CWD: originCWD, SubmittedAt: readJobTimestamp(sourceRunDir, sourceJobID, "submitted_at"), FinishedAt: readJobTimestamp(sourceRunDir, sourceJobID, "finished_at")}
	}
	if !appendJobs {
		queue.Commands = nil
	}
	queue.Commands = append(queue.Commands, selected...)
	if err := validateDependencies(queueToJobs(queue.Commands)); err != nil {
		return "", fmt.Errorf("invalid dependencies: %w", err)
	}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return "", fmt.Errorf("failed to write queue: %w", err)
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
	return fmt.Sprintf("copied jobs=%d from run=%s to queue=%s", len(selected), runID, queueName), nil
}
