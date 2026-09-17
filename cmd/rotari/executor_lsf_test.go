package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitLSFJobWithFakeLSF(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "bsub", `#!/bin/sh
cat >/dev/null
printf 'Job <123> is submitted to default queue.\n'
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	runDir := t.TempDir()
	job := JobSpec{ID: "abc123", Command: []string{"echo", "hello"}}
	metadata, err := submitLSFJob(runDir, job, []string{"-q short", "-n 2"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.LSFJobID != "123" {
		t.Fatalf("job id = %q, want 123", metadata.LSFJobID)
	}
	wrapper, err := os.ReadFile(filepath.Join(runDir, "abc123", "lsf-wrapper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapper), "#BSUB -o") || !strings.Contains(string(wrapper), "echo") {
		t.Fatalf("wrapper is missing LSF directives or command: %s", wrapper)
	}
}

func TestSubmitLSFArrayWithFakeLSF(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "bsub-array-args")
	writeExecutable(t, binDir, "bsub", fmt.Sprintf(`#!/bin/sh
cat >/dev/null
printf 'Job <123> is submitted to default queue.\n'
printf '%%s\n' "$@" > %q
`, argumentsPath))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runDir := t.TempDir()
	taskOne, taskTwo := 1, 2
	jobs := []JobSpec{
		{ID: "array-1", ArrayGroup: "array", ArrayTaskID: &taskOne, ArrayFirst: 1, ArrayLast: 2, Command: []string{"echo", "hello"}, Environment: []string{"ROTARI_ARRAY_TASK_ID=1", "ROTARI_JOB_DIR=" + filepath.Join(runDir, "array-1")}},
		{ID: "array-2", ArrayGroup: "array", ArrayTaskID: &taskTwo, ArrayFirst: 1, ArrayLast: 2, Command: []string{"echo", "hello"}, Environment: []string{"ROTARI_ARRAY_TASK_ID=2", "ROTARI_JOB_DIR=" + filepath.Join(runDir, "array-2")}},
	}
	handles, err := submitLSFArray(runDir, jobs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 2 || handles[0].Native != "123[1]" || handles[1].Native != "123[2]" {
		t.Fatalf("handles = %#v", handles)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "-J\nrotari[1-2]\n") {
		t.Fatalf("bsub arguments = %q", arguments)
	}
}

func TestLSFStatusCommandsWithFakeLSF(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "bjobs", `#!/bin/sh
if [ "$1" = "-a" ]; then
    printf 'DONE 0\n'
    exit 0
fi
printf 'PEND\n'
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	active, err := lsfJobActive("123")
	if err != nil || !active {
		t.Fatalf("lsfJobActive = %v, %v; want true, nil", active, err)
	}
	state, err := lsfJobState("123")
	if err != nil || state != "pending" {
		t.Fatalf("lsfJobState = %q, %v; want pending, nil", state, err)
	}
	exitCode, ok := lsfAccounting("123")
	if !ok || exitCode != 0 {
		t.Fatalf("lsfAccounting = %d, %v; want 0, true", exitCode, ok)
	}
}

func TestCancelJobsCancelsSelectedLSFJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "bkill-args")
	writeExecutable(t, binDir, "bkill", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argumentsPath))
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
	metadata := lsfJobMetadata{Executor: "lsf", JobID: "job-1", LSFJobID: "123"}
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
	if string(args) != "123\n" {
		t.Fatalf("bkill arguments = %q, want 123", args)
	}
}

func TestLSFExecutorSuspendsAndResumesJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "control-args")
	writeExecutable(t, binDir, "bstop", fmt.Sprintf("#!/bin/sh\nprintf 'bstop %%s\\n' \"$@\" >> %q\n", argumentsPath))
	writeExecutable(t, binDir, "bresume", fmt.Sprintf("#!/bin/sh\nprintf 'bresume %%s\\n' \"$@\" >> %q\n", argumentsPath))
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	jobDir := t.TempDir()
	if err := writeJSON(filepath.Join(jobDir, "job.json"), lsfJobMetadata{Executor: "lsf", LSFJobID: "123"}); err != nil {
		t.Fatal(err)
	}
	if err := (lsfExecutor{}).Suspend(jobDir); err != nil {
		t.Fatal(err)
	}
	if err := (lsfExecutor{}).Resume(jobDir); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "bstop 123\nbresume 123\n" {
		t.Fatalf("control arguments = %q", args)
	}
}

func TestWaitLSFJobUsesWrapperStatus(t *testing.T) {
	runDir := t.TempDir()
	job := lsfJobMetadata{Executor: "lsf", JobID: "job-1", Command: []string{"echo", "hi"}, LSFJobID: "123"}
	jobDir := filepath.Join(runDir, job.JobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "status.json"), slurmStatus{Phase: "finished", ExitCode: 9, Error: "failed"}); err != nil {
		t.Fatal(err)
	}

	result := waitLSFJob(runDir, job)
	if result.ExitCode != 9 || result.Error != "failed" {
		t.Fatalf("result = %+v, want exit 9 and failed", result)
	}
}

func TestWaitLSFJobPersistsNormalizedSchedulerStatus(t *testing.T) {
	binDir := t.TempDir()
	runDir := t.TempDir()
	job := lsfJobMetadata{Executor: "lsf", JobID: "job-1", Command: []string{"echo", "hi"}, LSFJobID: "123"}
	jobDir := filepath.Join(runDir, job.JobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(jobDir, "status.json")
	writeExecutable(t, binDir, "bjobs", fmt.Sprintf(`#!/bin/sh
printf '{"phase":"finished","exit_code":0}\n' > %q
printf 'RUN\n'
`, statusPath))
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	result := waitLSFJob(runDir, job)
	if result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("result = %+v, want successful result", result)
	}
	if state := loadSchedulerStatus(jobDir); state != "running" {
		t.Fatalf("scheduler state = %q, want running", state)
	}
}

func TestExecuteMixedRunSupportsLSFExecutor(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "bsub", `#!/bin/sh
cat >/dev/null
printf 'Job <999> is submitted to default queue.\n'
`)
	writeExecutable(t, binDir, "bjobs", `#!/bin/sh
if [ "$1" = "-a" ]; then
    printf 'DONE 0\n'
    exit 0
fi
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
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "lsf-job", Command: []string{"echo", "hi"}, Executor: "lsf"}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}

	if exitCode := executeMixedRun(paths, "run-1", "", 1, 1, 0, "", nil, "", nil, "", nil); exitCode != 0 {
		t.Fatalf("executeMixedRun exit code = %d, want 0", exitCode)
	}
}
