package main

import (
	"os"
	"testing"
)

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
