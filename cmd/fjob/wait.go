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
	queueNameOption := cliString(fs, "queue-name", "")
	runID := cliString(fs, "run-id", "")
	timeout := cliDuration(fs, "timeout", 0)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 || *runID == "" {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("wait"))
		return 1
	}
	if *timeout < 0 {
		fmt.Fprintln(os.Stderr, "--timeout must be >= 0")
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
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
		return 1
	}
	runDir := filepath.Join(paths.runsDir, *runID)
	deadline := time.Time{}
	if *timeout > 0 {
		deadline = time.Now().Add(*timeout)
	}
	for {
		summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
		if err == nil {
			printRunCompletion(paths, *runID, summary)
			return summary.ExitCode
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "timed out waiting for run %s\n", *runID)
			return 1
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
	message := fmt.Sprintf("%s\n  Queue: %s\n  Run: %s\n  Exit code: %d\n  Success: %d\n  Failed: %d\n  Directory: %s\n",
		title,
		paths.queueName, runID, summary.ExitCode, successCount, failedCount, runDir)
	if failedCount > 0 {
		message += fmt.Sprintf("\nInspect run:\n  fjob show --basedir %s --queue-name %s --run-id %s\n\nSee failed job output below.\n\nFailed job output:\n%s",
			paths.baseDir, paths.queueName, runID, failedJobHints(paths, runID, summary.Results))
		message += fmt.Sprintf("\nRerun failed jobs:\n  fjob run --basedir %s --queue-name %s --failed\n", paths.baseDir, paths.queueName)
	}
	return message
}
