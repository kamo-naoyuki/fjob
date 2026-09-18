package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func cmdDelete(args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	runIDOption := cliString(fs, "run-id", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		printError("usage: " + cliUsage("delete"))
		return 1
	}

	baseDir, queueName, err := resolveExistingRunTarget(*basedir, *queueNameOption, *runIDOption)
	if err != nil {
		printError(err)
		return 1
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		printErrorf("failed to resolve paths: %v", err)
		return 1
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		printErrorf("failed to create queue directory: %v", err)
		return 1
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		printErrorf("failed to lock queue: %v", err)
		return 1
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		printErrorf("failed to check queue: %v", err)
		return 1
	}
	if running {
		printErrorf("project '%s' is running; clear is not allowed", queueName)
		return 1
	}

	if *runIDOption == "" {
		if err := os.RemoveAll(paths.runsDir); err != nil {
			printErrorf("failed to clear run history: %v", err)
			return 1
		}
	} else {
		if filepath.Base(*runIDOption) != *runIDOption || *runIDOption == "." || *runIDOption == ".." {
			printErrorf("run %q not found", *runIDOption)
			return 1
		}
		runDir := filepath.Join(paths.runsDir, *runIDOption)
		info, err := os.Stat(runDir)
		if err != nil || !info.IsDir() {
			printErrorf("run %q not found", *runIDOption)
			return 1
		}
		if err := os.RemoveAll(runDir); err != nil {
			printErrorf("failed to clear run %q: %v", *runIDOption, err)
			return 1
		}
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		printErrorf("failed to load metadata: %v", err)
		return 1
	}
	if *runIDOption == "" || meta.LastRunID == *runIDOption {
		meta.LastRunID = latestRunID(paths.runsDir)
		if meta.LastRunID == "" {
			meta.LastRunExitCode = 0
		}
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		printErrorf("failed to update metadata: %v", err)
		return 1
	}
	if *runIDOption == "" {
		fmt.Printf("%s\n", green(fmt.Sprintf("cleared logs project=%s directory=%s", queueName, filepath.Join(paths.projectDir, "runs"))))
	} else {
		fmt.Printf("%s\n", green(fmt.Sprintf("cleared logs project=%s run=%s", queueName, *runIDOption)))
	}
	return 0
}

func latestRunID(runsDir string) string {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return ""
	}
	latestID := ""
	var latestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || (latestID != "" && !info.ModTime().After(latestTime)) {
			continue
		}
		latestID = entry.Name()
		latestTime = info.ModTime()
	}
	return latestID
}

func clearRunHistory(baseDir, queueName, runID string) error {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("project %q is running; clear is not allowed", queueName)
	}
	if filepath.Base(runID) != runID || runID == "." || runID == ".." {
		return fmt.Errorf("run %q not found", runID)
	}
	runDir := filepath.Join(paths.runsDir, runID)
	info, err := os.Stat(runDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("run %q not found", runID)
	}
	if err := os.RemoveAll(runDir); err != nil {
		return fmt.Errorf("failed to clear run %q: %w", runID, err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return err
	}
	if meta.LastRunID == runID {
		meta.LastRunID = latestRunID(paths.runsDir)
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	return writeJSON(paths.metaFile, meta)
}
