package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitSlurmJobWithFakeSlurm(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "sbatch-args")
	writeExecutable(t, binDir, "sbatch", fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
printf '12345;fake-host\n'
`, argumentsPath))
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	baseDir := t.TempDir()
	runDir := filepath.Join(baseDir, "projects", "demo", "runs", "run-1")
	job := JobSpec{ID: "abc123", Command: []string{"echo", "hello"}}
	metadata, err := submitSlurmJob(runDir, job, []string{"-p short --cpus-per-task=2"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SlurmJobID != "12345" {
		t.Fatalf("job id = %q, want 12345", metadata.SlurmJobID)
	}
	if metadata.SubmittedAt == "" {
		t.Fatal("submitted_at is empty")
	}
	if !strings.Contains(metadata.Command[0], "echo") {
		t.Fatalf("unexpected command: %#v", metadata.Command)
	}
	wrapper, err := os.ReadFile(filepath.Join(runDir, "abc123", "slurm-wrapper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapper), "echo") {
		t.Fatalf("wrapper does not contain command: %s", wrapper)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantShowCommand := "--job-name=rotari show --run-id 'run-1' --job-id 'abc123'"
	if !strings.Contains(string(arguments), wantShowCommand+"\n") {
		t.Fatalf("sbatch arguments = %q, want %q", arguments, wantShowCommand)
	}
}

func TestSubmitSlurmArrayWithFakeSlurm(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "sbatch-array-args")
	writeExecutable(t, binDir, "sbatch", fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
printf '54321;fake-host\n'
`, argumentsPath))
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	runDir := filepath.Join(t.TempDir(), "runs", "run-1")
	taskOne, taskTwo := 1, 2
	jobs := []JobSpec{
		{ID: "array-1", ArrayGroup: "array", ArrayTaskID: &taskOne, ArrayFirst: 1, ArrayLast: 2, Command: []string{"echo", "hello"}, Environment: []string{"ROTARI_ARRAY_TASK_ID=1", "ROTARI_JOB_DIR=" + filepath.Join(runDir, "array-1")}},
		{ID: "array-2", ArrayGroup: "array", ArrayTaskID: &taskTwo, ArrayFirst: 1, ArrayLast: 2, Command: []string{"echo", "hello"}, Environment: []string{"ROTARI_ARRAY_TASK_ID=2", "ROTARI_JOB_DIR=" + filepath.Join(runDir, "array-2")}},
	}
	handles, err := submitSlurmArray(runDir, jobs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 2 || handles[0].Native != "54321_1" || handles[1].Native != "54321_2" {
		t.Fatalf("handles = %#v", handles)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "--array=1-2\n") {
		t.Fatalf("sbatch arguments = %q, want native array range", arguments)
	}
	wrapper, err := os.ReadFile(filepath.Join(runDir, "array-array-wrapper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapper), "ROTARI_ARRAY_TASK_ID='1'") || !strings.Contains(string(wrapper), `job_dir="$ROTARI_JOB_DIR"`) {
		t.Fatalf("array wrapper missing task environment: %s", wrapper)
	}
}

func TestPrepareSlurmRunRegistersRunLocation(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"echo", "hello"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}

	_, runID, _, release, err := prepareSlurmRun(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	release()
	t.Cleanup(func() { _ = os.Remove(paths.lockFile) })

	location, found, err := resolveRunLocation(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || location.BaseDir != baseDir || location.ProjectName != "demo" || location.RunID != runID {
		t.Fatalf("run location = %+v, %v; want prepared run target", location, found)
	}
}

func TestSlurmStatusCommandsWithFakeSlurm(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "squeue", "#!/bin/sh\nprintf 'PENDING\\n'\n")
	writeExecutable(t, binDir, "sacct", "#!/bin/sh\nprintf 'FAILED|1:0\\n'\n")
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	active, err := slurmJobActive("12345")
	if err != nil || !active {
		t.Fatalf("slurmJobActive = %v, %v; want true, nil", active, err)
	}
	state, err := slurmJobState("12345")
	if err != nil || state != "pending" {
		t.Fatalf("slurmJobState = %q, %v; want pending, nil", state, err)
	}
	exitCode, state, ok := slurmAccounting("12345")
	if !ok || exitCode != 1 || state != "failed" {
		t.Fatalf("slurmAccounting = %d, %q, %v; want 1, failed, true", exitCode, state, ok)
	}
}

func TestCancelJobsCancelsSelectedSlurmJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "scancel-args")
	writeExecutable(t, binDir, "scancel", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argumentsPath))
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
	metadata := slurmJobMetadata{Executor: "slurm", JobID: "job-1", SlurmJobID: "12345"}
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
	if string(args) != "12345\n" {
		t.Fatalf("scancel arguments = %q, want 12345", args)
	}
}

func TestControlQueueJobsControlsSelectedSlurmJob(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "scontrol-args")
	writeExecutable(t, binDir, "scontrol", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\n", argumentsPath))
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
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(paths.runsDir, "run-1", "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), slurmJobMetadata{Executor: "slurm", JobID: "job-1", SlurmJobID: "12345"}); err != nil {
		t.Fatal(err)
	}

	if _, err := controlQueueJobs(baseDir, "default", []string{"job-1"}, "suspend"); err != nil {
		t.Fatal(err)
	}
	if _, err := controlQueueJobs(baseDir, "default", []string{"job-1"}, "resume"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "suspend 12345\nresume 12345\n" {
		t.Fatalf("scontrol arguments = %q, want suspend and resume for 12345", args)
	}
}

func TestWaitSlurmJobUsesWrapperStatus(t *testing.T) {
	runDir := t.TempDir()
	job := slurmJobMetadata{Executor: "slurm", JobID: "job-1", Command: []string{"echo", "hi"}, SlurmJobID: "12345"}
	jobDir := filepath.Join(runDir, job.JobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "status.json"), slurmStatus{Phase: "finished", ExitCode: 8, Error: "failed", Hosts: []string{"compute-01", "compute-02"}}); err != nil {
		t.Fatal(err)
	}

	result := waitSlurmJob(runDir, job)
	if result.ExitCode != 8 || result.Error != "failed" || len(result.Hosts) != 2 || result.Hosts[1] != "compute-02" {
		t.Fatalf("result = %+v, want exit 8 and failed", result)
	}
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
