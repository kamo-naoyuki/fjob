package main

import (
	"fmt"
	"os"
	"os/exec"
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
	if !strings.Contains(string(arguments), "--output=/dev/null\n") || !strings.Contains(string(arguments), "--error=/dev/null\n") {
		t.Fatalf("sbatch arguments = %q, want Slurm output files disabled", arguments)
	}
	wrapper, err := os.ReadFile(filepath.Join(runDir, "array-array-wrapper.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapper), "ROTARI_ARRAY_TASK_ID='1'") || !strings.Contains(string(wrapper), "job_dir='"+filepath.Join(runDir, "array-1")+"'") || strings.Contains(string(wrapper), `job_dir="$ROTARI_JOB_DIR"`) {
		t.Fatalf("array wrapper missing task environment: %s", wrapper)
	}
}

func TestExecuteMixedRunSubmitsAndCompletesSlurmArrayTasks(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "sbatch-args")
	writeExecutable(t, binDir, "sbatch", fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %q
wrapper=
for arg in "$@"; do wrapper=$arg; done
SLURM_ARRAY_TASK_ID=1 sh "$wrapper"
SLURM_ARRAY_TASK_ID=2 sh "$wrapper"
printf '54321;fake-host\n'
`, argumentsPath))
	writeExecutable(t, binDir, "squeue", "#!/bin/sh\nexit 0\n")
	writeExecutable(t, binDir, "sacct", "#!/bin/sh\nprintf 'COMPLETED|0:0\n'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID: "array", Name: "array", Executor: "slurm", Command: []string{"sh", "-c", "exit 0"}, Array: &ArraySpec{First: 1, Last: 2},
	}}}); err != nil {
		t.Fatal(err)
	}
	if code := executeMixedRun(paths, "array-run", "", 1, 2, 0, "", nil, "", nil, "", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(arguments), "--array=1-2\n") != 1 {
		t.Fatalf("sbatch arguments = %q, want one native array submission", arguments)
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, "array-run", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 2 || summary.Results[0].ID != "array-1" || summary.Results[1].ID != "array-2" {
		t.Fatalf("summary results = %#v, want both array tasks", summary.Results)
	}
	for _, id := range []string{"array-1", "array-2"} {
		status, ok := loadSlurmStatus(filepath.Join(paths.runsDir, "array-run", id, "status.json"))
		if !ok || status.Phase != "finished" {
			t.Fatalf("task %s status = %#v, ok=%v", id, status, ok)
		}
	}
}

func TestSlurmArrayWrapperWritesFinishedTaskStatus(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		taskVariable string
	}{
		{name: "slurm", taskVariable: "SLURM_ARRAY_TASK_ID"},
		{name: "pbs", taskVariable: "PBS_ARRAY_INDEX"},
		{name: "lsf", taskVariable: "LSB_JOBINDEX"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			task := 1
			jobDir := filepath.Join(runDir, "array-1")
			job := JobSpec{
				ID: "array-1", ArrayGroup: "array", ArrayTaskID: &task, ArrayFirst: 1, ArrayLast: 1,
				Command: []string{"sh", "-c", "printf task-output; exit 0"},
				Environment: []string{
					envArrayTaskID + "=1",
					envJobDir + "=" + jobDir,
				},
			}
			wrapper := filepath.Join(runDir, "wrapper.sh")
			if err := os.WriteFile(wrapper, []byte(schedulerArrayWrapperScript([]JobSpec{job}, testCase.taskVariable)), 0o755); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("sh", wrapper)
			command.Env = append(os.Environ(), testCase.taskVariable+"=1")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("wrapper failed: %v, output=%s", err, output)
			}
			status, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json"))
			if !ok || status.Phase != "finished" || status.ExitCode != 0 {
				t.Fatalf("status = %#v, ok=%v", status, ok)
			}
			output, err := os.ReadFile(filepath.Join(jobDir, "output"))
			if err != nil || string(output) != "task-output" {
				t.Fatalf("output = %q, err=%v; want task output in job directory", output, err)
			}
		})
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

// Regression test: a whole-project cancel (no --job-id) must reach an
// already-submitted Slurm job even mid-run, before the aggregate
// slurm_jobs.json snapshot exists (it is only written once the whole run
// finishes).
func TestCancelQueueCancelsRunningSlurmJobMidRun(t *testing.T) {
	binDir := t.TempDir()
	argumentsPath := filepath.Join(t.TempDir(), "scancel-args")
	writeExecutable(t, binDir, "scancel", fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> %q\n", argumentsPath))
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
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "job-1", Command: []string{"echo", "hi"}},
		{ID: "job-2", Command: []string{"echo", "hi"}},
	}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	// job-1 is currently running under Slurm; job-2 has not been submitted
	// yet (no job directory at all). Neither has slurm_jobs.json, which is
	// only written after the whole run completes.
	job1Dir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(job1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(job1Dir, "job.json"), slurmJobMetadata{Executor: "slurm", JobID: "job-1", SlurmJobID: "12345"}); err != nil {
		t.Fatal(err)
	}

	if _, err := cancelQueue(baseDir, "default", false); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "12345\n" {
		t.Fatalf("scancel arguments = %q, want 12345", args)
	}
	if _, err := os.Stat(filepath.Join(runDir, "job-2", "cancelled")); err != nil {
		t.Fatalf("job-2 was not marked cancelled before submission: %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "cancelling" {
		t.Fatalf("meta.Phase = %q, want cancelling", meta.Phase)
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
