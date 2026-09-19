package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func cmdWait(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	var runIDs stringSliceFlag
	cliValue(fs, &runIDs, "run-id")
	timeout := cliDuration(fs, "timeout", 0)
	jsonOutput := cliBool(fs, "json", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	runIDs = append(runIDs, fs.Args()...)
	if len(runIDs) == 0 {
		runID, err := resolveActiveRunTarget(*basedir, *queueNameOption)
		if err != nil {
			printError(err)
			return 1
		}
		runIDs = append(runIDs, runID)
	}
	if *timeout < 0 {
		printError("--timeout must be >= 0")
		return 1
	}
	deadline := time.Time{}
	if *timeout > 0 {
		deadline = time.Now().Add(*timeout)
	}
	exitCode := 0
	for _, runID := range runIDs {
		result := waitForRun(*basedir, *queueNameOption, runID, deadline, *jsonOutput)
		if result.exitCode > exitCode {
			exitCode = result.exitCode
		}
		if result.timedOut {
			return 1
		}
	}
	return exitCode
}

func resolveActiveRunTarget(cliBaseDir, cliProjectName string) (string, error) {
	baseDir, _, err := resolveBaseDir(cliBaseDir)
	if err != nil {
		return "", err
	}
	queueName, err := resolveProjectName(baseDir, cliProjectName)
	if err != nil {
		return "", err
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	state, runID, err := inspectProjectRunState(paths)
	if err != nil {
		return "", fmt.Errorf("failed to check project state: %w", err)
	}
	if state != projectRunning || runID == "" {
		return "", fmt.Errorf("project %q has no active run", queueName)
	}
	return runID, nil
}

type waitResult struct {
	exitCode int
	timedOut bool
}

func waitForRun(basedir, queueNameOption, runID string, deadline time.Time, jsonOutput bool) waitResult {
	baseDir, queueName, err := resolveExistingRunTarget(basedir, queueNameOption, runID)
	if err != nil {
		printError(err)
		return waitResult{exitCode: 1}
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		printErrorf("failed to resolve paths: %v", err)
		return waitResult{exitCode: 1}
	}
	runDir, err := validatedRunDir(paths, runID)
	if err != nil {
		printErrorf("invalid run ID %q", runID)
		return waitResult{exitCode: 1}
	}
	for {
		summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
		if err == nil {
			if jsonOutput {
				_ = json.NewEncoder(os.Stdout).Encode(summary)
			} else {
				printRunCompletion(paths, runID, summary)
			}
			return waitResult{exitCode: summary.ExitCode}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			printErrorf("timed out waiting for run %s", runID)
			return waitResult{exitCode: 1, timedOut: true}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func loadRunSummary(path string) (RunSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunSummary{}, err
	}
	var summary RunSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return RunSummary{}, err
	}
	return summary, nil
}

func printRunCompletion(paths pathSet, runID string, summary RunSummary) {
	fmt.Print(formatRunCompletion(paths, runID, summary))
}

func formatRunCompletion(paths pathSet, runID string, summary RunSummary) string {
	successCount := 0
	failedCount := 0
	for _, result := range summary.Results {
		if result.ExitCode == 0 {
			successCount++
		} else {
			failedCount++
		}
	}
	runDir := filepath.Join(paths.runsDir, runID)
	title := "Run finished:"
	if summary.ExitCode != 0 {
		title = red("Run failed:")
	} else {
		title = green(title)
	}
	message := title + "\n" + colorLabeledDetails(fmt.Sprintf("  Project: %s\n  Run: %s\n  Status: %s\n  Exit code: %d\n  Success: %d\n  Failed: %d\n  Directory: %s\n",
		paths.queueName, formatRunLabel(runID, summary.RunName), summary.Status, summary.ExitCode, successCount, failedCount, runDir), summary.ExitCode != 0)
	if failedCount > 0 {
		message += colorLabeledDetails(fmt.Sprintf("\nInspect run:\n  rotari show --run-id %s\n\nSee failed job output below.\n\nFailed job output:\n%s\nRerun failed jobs:\n  rotari retry --basedir %s --project-name %s\n",
			runID, failedJobHints(runID, summary.Results), paths.baseDir, paths.queueName), summary.ExitCode != 0)
	}
	return message
}
