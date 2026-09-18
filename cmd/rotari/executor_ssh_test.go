package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHExecutorRunsRemoteCommandAndRecordsResult(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "ssh-args")
	writeExecutable(t, binDir, "ssh", fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
cat > "$ROTARI_SSH_SCRIPT"
sh "$ROTARI_SSH_SCRIPT"
`, argumentsPath))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	scriptPath := filepath.Join(t.TempDir(), "remote.sh")
	t.Setenv("ROTARI_SSH_SCRIPT", scriptPath)

	runDir := filepath.Join(t.TempDir(), "runs", "run-1")
	job := JobSpec{ID: "job-1", Command: []string{"sh", "-c", "printf '%s' \"$ROTARI_JOB_ID\""}, Environment: []string{"ROTARI_JOB_ID=job-1"}}
	handle, err := (sshExecutor{}).Submit(runDir, job, []string{"builder@example.test", "-p 2222"})
	if err != nil {
		t.Fatal(err)
	}
	result := (sshExecutor{}).Wait(runDir, handle)
	if result.ExitCode != 0 || len(result.Hosts) != 1 || result.Hosts[0] != "builder@example.test" {
		t.Fatalf("result = %#v", result)
	}
	output, err := os.ReadFile(filepath.Join(runDir, "job-1", "output"))
	if err != nil || string(output) != "job-1" {
		t.Fatalf("output = %q, err = %v", output, err)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "-p\n2222\n--\nbuilder@example.test\nsh\n-s\n") {
		t.Fatalf("ssh arguments = %q", arguments)
	}
}

func TestSSHTargetRequiresHost(t *testing.T) {
	if _, _, err := sshTarget(nil); err == nil {
		t.Fatal("sshTarget accepted no target host")
	}
}
