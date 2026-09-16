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
	queueNameOption := cliString(fs, "queue-name", "")
	runID := cliString(fs, "run-id", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 || *runID == "" {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("unlock"))
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
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to lock queue: %v\n", err)
		return 1
	}
	defer release()
	lock, err := loadLockInfo(paths.lockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "no run lock exists")
			return 1
		}
		fmt.Fprintf(os.Stderr, "failed to read run lock: %v\n", err)
		return 1
	}
	if lock.RunID != *runID {
		fmt.Fprintf(os.Stderr, "run lock belongs to %q, not %q\n", lock.RunID, *runID)
		return 1
	}
	if err := os.Remove(paths.lockFile); err != nil {
		fmt.Fprintf(os.Stderr, "failed to remove run lock: %v\n", err)
		return 1
	}
	fmt.Printf("removed run lock queue=%s run_id=%s host=%s\n", queueName, lock.RunID, lock.Host)
	return 0
}
