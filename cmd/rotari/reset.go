package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdReset(args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	recoverOption := cliBool(fs, "recover", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		printError("usage: " + cliUsage("reset"))
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
	state, runID, err := inspectProjectRunState(paths)
	if err != nil {
		printErrorf("failed to check project state: %v", err)
		return 1
	}
	if state == projectRunning {
		meta, metaErr := loadMeta(paths.metaFile)
		if metaErr == nil && meta.Phase == "cancelling" {
			if !waitForCancellation(paths, queueName) {
				return 1
			}
			state, runID, err = inspectProjectRunState(paths)
			if err != nil {
				printErrorf("failed to check project state: %v", err)
				return 1
			}
		}
	}
	if state == projectRunning {
		fmt.Fprint(os.Stderr, formatProjectRunningError(paths, runID))
		return 1
	}
	if state == projectInterrupted {
		confirmed := *recoverOption
		if !confirmed {
			if !isTerminal(os.Stdin) {
				detail, stillRunning := interruptedRunStatusDetail(paths, runID)
				message := fmt.Sprintf("project %q has interrupted run %q%s; reset requires confirmation\nInspect before deciding: rotari show --basedir %s --project-name %s --run-id %s\n",
					queueName, runID, detail, shellQuote(paths.baseDir), shellQuote(paths.queueName), shellQuote(runID))
				if stillRunning {
					message += "Do not recover until you have independently confirmed those jobs have actually stopped.\n"
				}
				message += fmt.Sprintf("Confirm with:\n  rotari reset --basedir %s --project-name %s --recover",
					shellQuote(paths.baseDir), shellQuote(paths.queueName))
				printError(message)
				return 1
			}
			confirmed, err = confirmResetOfInterruptedRun(os.Stdin, os.Stderr, paths, runID)
			if err != nil {
				printErrorf("failed to confirm reset: %v", err)
				return 1
			}
			if !confirmed {
				printError("reset cancelled")
				return 1
			}
		}
		queue, err := loadQueue(paths.queueFile)
		if err != nil {
			printErrorf("failed to load queue: %v", err)
			return 1
		}
		cleared := len(queue.Commands)
		if err := recoverInterruptedProject(paths, runID, true); err != nil {
			printErrorf("failed to recover interrupted run: %v", err)
			return 1
		}
		fmt.Printf("%s\n", colorKeyValueMessage(fmt.Sprintf("reset project=%s cleared=%d job(s); recovered interrupted run=%s", queueName, cleared, runID), yellow))
		return 0
	}
	cleared, err := resetQueueCommands(paths)
	if err != nil {
		printErrorf("failed to reset queue: %v", err)
		return 1
	}
	color := green
	if cleared > 0 {
		color = yellow
	}
	fmt.Printf("%s\n", colorKeyValueMessage(fmt.Sprintf("reset project=%s cleared=%d job(s)", queueName, cleared), color))
	return 0
}

func confirmResetOfInterruptedRun(input io.Reader, output io.Writer, paths pathSet, runID string) (bool, error) {
	detail, _ := interruptedRunStatusDetail(paths, runID)
	fmt.Fprintf(output, "project %q has interrupted run %q%s.\nConfirm all jobs have stopped and reset the queue? [y/N] ", paths.queueName, runID, detail)
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && len(answer) == 0 {
		return false, err
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}

// resetQueueCommands preserves queue defaults and run history.
func resetQueueCommands(paths pathSet) (int, error) {
	if err := os.MkdirAll(paths.projectDir, stateDirMode()); err != nil {
		return 0, fmt.Errorf("failed to create project directory: %w", err)
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return 0, fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return 0, fmt.Errorf("failed to check queue: %w", err)
	}
	if running {
		return 0, fmt.Errorf("project %q is running; reset is not allowed", paths.queueName)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return 0, fmt.Errorf("failed to load queue: %w", err)
	}
	cleared := len(queue.Commands)
	if cleared > 0 {
		queue.Commands = nil
		if err := writeJSON(paths.queueFile, queue); err != nil {
			return 0, fmt.Errorf("failed to reset queue: %w", err)
		}
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return 0, fmt.Errorf("failed to load metadata: %w", err)
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return 0, fmt.Errorf("failed to update metadata: %w", err)
	}
	return cleared, nil
}
