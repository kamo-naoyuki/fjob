package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func cmdUnlock(args []string) int {
	fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	runID := cliString(fs, "run-id", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 || *runID == "" {
		printError("usage: " + cliUsage("unlock"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		printErrorf("failed to resolve state directory: %v", err)
		return 1
	}
	queueName, err := resolveProjectName(baseDir, *queueNameOption)
	if err != nil {
		printError(err)
		return 1
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		printErrorf("failed to resolve paths: %v", err)
		return 1
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		printErrorf("failed to lock queue: %v", err)
		return 1
	}
	defer release()
	lock, err := loadLockInfo(paths.lockFile)
	removedLock := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			printErrorf("failed to read run lock: %v", err)
			return 1
		}
	} else {
		if lock.RunID != *runID {
			printErrorf("run lock belongs to %q, not %q", lock.RunID, *runID)
			return 1
		}
		if err := os.Remove(paths.lockFile); err != nil {
			printErrorf("failed to remove run lock: %v", err)
			return 1
		}
		removedLock = true
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		printErrorf("failed to load metadata: %v", err)
		return 1
	}
	if !removedLock && (meta.Phase != "running" && meta.Phase != "cancelling" || meta.LastRunID != *runID) {
		printError("no matching interrupted run exists")
		return 1
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		printErrorf("failed to update metadata: %v", err)
		return 1
	}
	fmt.Printf("recovered queue project=%s run_id=%s\n", queueName, *runID)
	return 0
}
