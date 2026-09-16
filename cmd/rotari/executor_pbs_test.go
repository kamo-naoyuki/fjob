package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitPBSJobWithFakePBS(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "qsub", `#!/bin/sh
printf '123.headnode\n'
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	runDir := t.TempDir()
	job := JobSpec{ID: "abc123", Command: []string{"echo", "hello"}}
	metadata, err := submitPBSJob(runDir, job, []string{"-l select=1:ncpus=2"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.PBSJobID != "123.headnode" {
		t.Fatalf("job id = %q, want 123.headnode", metadata.PBSJobID)
	}
	wrapper, err := os.ReadFile(filepath.Join(runDir, "abc123", "pbs-wrapper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapper), "echo") {
		t.Fatalf("wrapper does not contain command: %s", wrapper)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "abc123", "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["executor"] != "pbs" {
		t.Fatalf("job metadata executor = %v, want pbs", raw["executor"])
	}
	if _, ok := raw["executors"]; ok {
		t.Fatal("job metadata uses obsolete executors key")
	}
}

func TestPBSStatusCommandsWithFakePBS(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "qstat", `#!/bin/sh
if [ "$1" = "-xf" ]; then
    printf '    exit_status = 1\n'
else
    exit 1
fi
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	active, err := pbsJobActive("123.headnode")
	if err != nil || active {
		t.Fatalf("pbsJobActive = %v, %v; want false, nil", active, err)
	}
	exitCode, ok := pbsAccounting("123.headnode")
	if !ok || exitCode != 1 {
		t.Fatalf("pbsAccounting = %d, %v; want 1, true", exitCode, ok)
	}
}

func TestCancelJobsCancelsSelectedPBSJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "qdel-args")
	writeExecutable(t, binDir, "qdel", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argumentsPath))
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	runDir := t.TempDir()
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := pbsJobMetadata{Executor: "pbs", JobID: "job-1", PBSJobID: "123.headnode"}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), metadata); err != nil {
		t.Fatal(err)
	}

	message, err := cancelJobs(runDir, "default", "run-1", []string{"job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "Jobs: 1") {
		t.Fatalf("message = %q, want one cancelled job", message)
	}
	args, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "123.headnode\n" {
		t.Fatalf("qdel arguments = %q, want 123.headnode", args)
	}
}

func TestPBSExecutorSuspendsAndResumesJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "qsig-args")
	writeExecutable(t, binDir, "qsig", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> %q\n", argumentsPath))
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	jobDir := t.TempDir()
	if err := writeJSON(filepath.Join(jobDir, "job.json"), pbsJobMetadata{Executor: "pbs", PBSJobID: "123.headnode"}); err != nil {
		t.Fatal(err)
	}
	if err := (pbsExecutor{}).Suspend(jobDir); err != nil {
		t.Fatal(err)
	}
	if err := (pbsExecutor{}).Resume(jobDir); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "-s\nsuspend\n123.headnode\n-s\nresume\n123.headnode\n" {
		t.Fatalf("qsig arguments = %q", args)
	}
}

func TestWaitPBSJobUsesWrapperStatus(t *testing.T) {
	runDir := t.TempDir()
	job := pbsJobMetadata{Executor: "pbs", JobID: "job-1", Command: []string{"echo", "hi"}, PBSJobID: "123.headnode"}
	jobDir := filepath.Join(runDir, job.JobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "status.json"), slurmStatus{Phase: "finished", ExitCode: 7, Error: "failed"}); err != nil {
		t.Fatal(err)
	}

	result := waitPBSJob(runDir, job)
	if result.ExitCode != 7 || result.Error != "failed" {
		t.Fatalf("result = %+v, want exit 7 and failed", result)
	}
}

func TestExecuteMixedRunSupportsPBSExecutor(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "qsub", `#!/bin/sh
shift $(($#-1))
sh "$1" >/dev/null 2>&1 &
printf '999.headnode\n'
`)
	writeExecutable(t, binDir, "qstat", `#!/bin/sh
exit 1
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "pbs-job", Command: []string{"echo", "hi"}, Executor: "pbs"}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}

	exitCode := executeMixedRun(paths, "run-1", "", 1, 1, 0, "", nil, "", nil, "", nil)
	if exitCode != 0 {
		t.Fatalf("executeMixedRun exit code = %d, want 0", exitCode)
	}
}
