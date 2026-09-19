package main

import (
	"io"
	"os"
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
