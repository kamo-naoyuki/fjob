package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// localExecutor runs jobs as plain subprocesses on the current host.
type localExecutor struct{}

func (localExecutor) Name() string { return "local" }

func (localExecutor) Submit(runDir string, job JobSpec, options []string) (JobHandle, error) {
	return JobHandle{Job: job}, nil
}

func (localExecutor) Wait(runDir string, handle JobHandle) JobResult {
	return runOneJob(runDir, handle.Job)
}

func (localExecutor) Suspend(jobDir string) error {
	return localExecutor{}.signal(jobDir, syscall.SIGSTOP)
}

func (localExecutor) Resume(jobDir string) error {
	return localExecutor{}.signal(jobDir, syscall.SIGCONT)
}

func (localExecutor) Cancel(jobDir string) error {
	return localExecutor{}.signal(jobDir, syscall.SIGTERM)
}

func (localExecutor) signal(jobDir string, sig syscall.Signal) error {
	pidData, err := os.ReadFile(filepath.Join(jobDir, "pid"))
	if err != nil {
		return fmt.Errorf("job is not running")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || !processAlive(pid) {
		return fmt.Errorf("job is not running")
	}
	return syscall.Kill(pid, sig)
}
