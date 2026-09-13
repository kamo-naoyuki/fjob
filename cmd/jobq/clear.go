package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func cmdClear(args []string) int {
	fs := flag.NewFlagSet("clear", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("clear"))
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create queue directory: %v\n", err)
		return 1
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to lock queue: %v\n", err)
		return 1
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to check queue: %v\n", err)
		return 1
	}
	if running {
		fmt.Fprintf(os.Stderr, "queue '%s' is running; clear is not allowed\n", queueName)
		return 1
	}

	if err := os.RemoveAll(paths.runsDir); err != nil {
		fmt.Fprintf(os.Stderr, "failed to clear run history: %v\n", err)
		return 1
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load metadata: %v\n", err)
		return 1
	}
	meta.LastRunID = ""
	meta.LastRunExitCode = 0
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		fmt.Fprintf(os.Stderr, "failed to update metadata: %v\n", err)
		return 1
	}
	fmt.Printf("%s\n", green(fmt.Sprintf("cleared logs queue=%s directory=%s", queueName, filepath.Join(paths.queueDir, "runs"))))
	return 0
}
