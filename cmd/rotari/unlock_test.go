package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureProjectIdleRejectsInterruptedRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: "run-1", Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	err = ensureProjectIdle(baseDir, "demo", "add")
	if err == nil || !strings.Contains(err.Error(), `project "demo" has interrupted run "run-1"; add is not allowed`) {
		t.Fatalf("ensureProjectIdle error = %v, want interrupted run error", err)
	}
	if _, err := os.Stat(paths.lockFile); !os.IsNotExist(err) {
		t.Fatalf("stale local lock was not cleaned up: %v", err)
	}
}

func TestEnsureProjectIdleReportsStillRunningJobsForInterruptedRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: "run-1", Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1", UpdatedAt: "2026-09-19T10:32:00Z"}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	finishedDir := filepath.Join(runDir, "finished-job")
	if err := os.MkdirAll(finishedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finishedDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finishedDir, "status"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runningDir := filepath.Join(runDir, "running-job")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runningDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = ensureProjectIdle(baseDir, "demo", "add")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		`1 of 2 job(s) appear to still be running`,
		"this run was still executing, with no cancellation requested, as of its last recorded update at 2026-09-19T10:32:00Z",
		"Do not recover until you have independently confirmed those jobs have actually stopped.",
		"Inspect before deciding: rotari show",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to contain %q", msg, want)
		}
	}
}

func TestEnsureProjectIdleReportsAllFinishedForInterruptedRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: "run-1", Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "cancelling", LastRunID: "run-1", UpdatedAt: "2026-09-19T10:32:00Z"}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	for _, name := range []string{"job-1", "job-2"} {
		jobDir := filepath.Join(runDir, name)
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, "command.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, "status"), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	err = ensureProjectIdle(baseDir, "demo", "add")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"all 2 job(s) report having finished",
		"a cancellation had already been requested for this run, but had not finished, as of its last recorded update at 2026-09-19T10:32:00Z",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want it to contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "Do not recover") {
		t.Fatalf("error = %q, want no still-running warning when all jobs finished", msg)
	}
}

func TestScanInterruptedRunJobStatusSkipsNonJobEntries(t *testing.T) {
	runDir := t.TempDir()
	// A stray file alongside job directories (e.g. commands.json) must not
	// be counted as a job.
	if err := os.WriteFile(filepath.Join(runDir, "commands.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without command.json isn't a job directory either.
	if err := os.MkdirAll(filepath.Join(runDir, "not-a-job"), 0o755); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "status.json"), []byte(`{"phase":"finished","exit_code":0,"finished_at":"2026-09-19T10:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := scanInterruptedRunJobStatus(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 1 || status.StillRunning != 0 {
		t.Fatalf("status = %+v, want Total=1 StillRunning=0", status)
	}
}

func TestScanInterruptedRunJobStatusTreatsNonTerminalStatusJSONAsRunning(t *testing.T) {
	runDir := t.TempDir()
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A wrapper-written status.json whose phase is still "running" (no
	// finished_at yet) must count as still running, same as a missing file.
	if err := os.WriteFile(filepath.Join(jobDir, "status.json"), []byte(`{"phase":"running","started_at":"2026-09-19T10:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := scanInterruptedRunJobStatus(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 1 || status.StillRunning != 1 {
		t.Fatalf("status = %+v, want Total=1 StillRunning=1", status)
	}
}

func TestConfirmResetOfInterruptedRunIncludesJobStatusDetail(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1", UpdatedAt: "2026-09-19T10:32:00Z"}); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(paths.runsDir, "run-1", "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if _, err := confirmResetOfInterruptedRun(strings.NewReader("no\n"), &output, paths, "run-1"); err != nil {
		t.Fatal(err)
	}
	prompt := output.String()
	for _, want := range []string{
		"1 of 1 job(s) appear to still be running",
		"this run was still executing, with no cancellation requested, as of its last recorded update at 2026-09-19T10:32:00Z",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, want it to contain %q", prompt, want)
		}
	}
}

func TestCmdResetNonInteractiveRejectionIncludesJobStatusDetail(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: "run-1", Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "cancelling", LastRunID: "run-1", UpdatedAt: "2026-09-19T10:32:00Z"}); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(paths.runsDir, "run-1", "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "command.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "status"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A pipe's read end is never a char device, so isTerminal(os.Stdin)
	// reliably reports false regardless of the test process's real stdin.
	oldStdin := os.Stdin
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = stdinReader
	defer func() { os.Stdin = oldStdin }()

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatal("cmdReset accepted an interrupted run without --recover")
	}
	msg := string(output)
	for _, want := range []string{
		"all 1 job(s) report having finished",
		"a cancellation had already been requested for this run, but had not finished, as of its last recorded update at 2026-09-19T10:32:00Z",
		"Inspect before deciding: rotari show",
		"Confirm with:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("stderr = %q, want it to contain %q", msg, want)
		}
	}
}

func TestCmdUnlockRecoversInterruptedRunWithoutLock(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdUnlock([]string{"--basedir", baseDir, "--project-name", "demo", "--run-id", "run-1"})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "recovered queue project=demo run_id=run-1") {
		t.Fatalf("cmdUnlock exit code = %d, stdout = %q", code, output)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" || meta.LastRunID != "run-1" {
		t.Fatalf("metadata = %#v, want collecting with run-1 retained", meta)
	}
}

func TestIsRunningRetainsRemoteHostLock(t *testing.T) {
	lockPath := t.TempDir() + "/running.lock"
	if err := writeJSON(lockPath, LockInfo{PID: -1, RunID: "run-1", Host: "other-host"}); err != nil {
		t.Fatal(err)
	}
	running, err := isRunning(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("remote lock was treated as stale")
	}
}

func TestIsRunningRemovesDeadLocalHostLock(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	lockPath := t.TempDir() + "/running.lock"
	if err := writeJSON(lockPath, LockInfo{PID: -1, RunID: "run-1", Host: host}); err != nil {
		t.Fatal(err)
	}
	running, err := isRunning(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatal("dead local lock was retained")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists: %v", err)
	}
}

func TestAcquireLockRejectsActiveLockWithoutReplacingIt(t *testing.T) {
	lockPath := t.TempDir() + "/running.lock"
	first := LockInfo{PID: os.Getpid(), RunID: "run-1"}
	if err := acquireLock(lockPath, first); err != nil {
		t.Fatal(err)
	}
	if err := acquireLock(lockPath, LockInfo{PID: os.Getpid(), RunID: "run-2"}); err == nil || !strings.Contains(err.Error(), "active lock exists") {
		t.Fatalf("second acquire error = %v, want active lock exists", err)
	}
	stored, err := loadLockInfo(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunID != "run-1" {
		t.Fatalf("stored lock = %+v, want original run-1 lock", stored)
	}
}
