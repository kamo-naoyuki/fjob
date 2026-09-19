package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// runOneJob wraps local commands in the same self-reporting statusWrapperScript
// used by scheduler executors, so status.json stays authoritative even if the
// coordinator process dies before it can call cmd.Wait() itself.
func TestRunOneJobSelfReportsStatusJSON(t *testing.T) {
	runDir := t.TempDir()
	job := JobSpec{ID: "job-1", Command: []string{"sh", "-c", "exit 3"}}
	result := runOneJob(runDir, job)
	if result.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", result.ExitCode)
	}
	status, ok := loadSlurmStatus(filepath.Join(runDir, "job-1", "status.json"))
	if !ok {
		t.Fatal("expected local job to self-report status.json like scheduler executors")
	}
	if status.Phase != "finished" || status.ExitCode != 3 {
		t.Fatalf("status.json = %+v, want phase=finished exit_code=3", status)
	}
}

func TestLocalJobWrapperSelfReportsStatusEvenIfCoordinatorNeverWaits(t *testing.T) {
	jobDir := t.TempDir()
	wrapperPath := filepath.Join(jobDir, "local-wrapper.sh")
	wrapper := statusWrapperScript([]string{"sh", "-c", "exit 7"}, jobDir, nil, "")
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", wrapperPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Simulate the coordinator dying before it calls cmd.Wait(); reap the
	// process afterward only to avoid leaking a zombie in this test.
	pid := cmd.Process.Pid
	t.Cleanup(func() { _, _ = syscall.Wait4(pid, nil, 0, nil) })

	deadline := time.Now().Add(2 * time.Second)
	var status slurmStatus
	var ok bool
	for time.Now().Before(deadline) {
		status, ok = loadSlurmStatus(filepath.Join(jobDir, "status.json"))
		if ok && jobStatusTerminal(status) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok || !jobStatusTerminal(status) {
		t.Fatalf("status.json was not self-reported by the orphaned job: %+v", status)
	}
	if status.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", status.ExitCode)
	}
}
