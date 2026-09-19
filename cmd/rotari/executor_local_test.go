package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalExecutorSignalRejectsMissingPID(t *testing.T) {
	err := (localExecutor{}).Cancel(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "job is not running") {
		t.Fatalf("error = %v, want job is not running", err)
	}
}

func TestLocalExecutorSignalRejectsMalformedPID(t *testing.T) {
	jobDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(jobDir, "pid"), []byte("not-a-pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := (localExecutor{}).Suspend(jobDir)
	if err == nil || !strings.Contains(err.Error(), "job is not running") {
		t.Fatalf("error = %v, want job is not running", err)
	}
}

func TestLocalExecutorSignalRejectsExitedProcess(t *testing.T) {
	command := exec.Command("sh", "-c", "exit 0")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	jobDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(jobDir, "pid"), []byte(stringPID(command.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	err := (localExecutor{}).Resume(jobDir)
	if err == nil || !strings.Contains(err.Error(), "job is not running") {
		t.Fatalf("error = %v, want job is not running", err)
	}
}

func stringPID(pid int) string {
	return fmt.Sprintf("%d\n", pid)
}
