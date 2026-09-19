package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatDisplayTimestampUsesJST(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	if got := formatDisplayTimestamp("2026-09-16T00:00:01Z"); got != "2026-09-16 09:00:01 JST" {
		t.Fatalf("formatDisplayTimestamp() = %q, want JST display", got)
	}
	if got := formatDisplayTimestamp("-"); got != "-" {
		t.Fatalf("formatDisplayTimestamp(-) = %q, want unchanged marker", got)
	}
}

func TestJobStatusTerminalUsesFinishedAtMarker(t *testing.T) {
	if !jobStatusTerminal(slurmStatus{Phase: "running", FinishedAt: "2026-09-18T00:00:00Z"}) {
		t.Fatal("status with finished_at was not treated as terminal")
	}
	if jobStatusTerminal(slurmStatus{Phase: "running"}) {
		t.Fatal("running status without finished_at was treated as terminal")
	}
}

func TestLoadTerminalSchedulerState(t *testing.T) {
	jobDir := t.TempDir()
	writeSchedulerStatus(jobDir, "COMPLETED")
	if status, ok := loadTerminalSchedulerState(jobDir); !ok || status != 0 {
		t.Fatalf("completed scheduler status = %d, %v", status, ok)
	}
	writeSchedulerStatus(jobDir, "RUNNING")
	if _, ok := loadTerminalSchedulerState(jobDir); ok {
		t.Fatal("running scheduler status was treated as terminal")
	}
}

func TestCmdShowDisplaysFinishedArrayTaskFromStatusJSON(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "array-run"
	task := 1
	queue := Queue{Commands: []QueuedCommand{{ID: "array", Command: []string{"true"}, Executor: "slurm", Array: &ArraySpec{First: 1, Last: 1}}}}
	if err := writeJSON(filepath.Join(paths.runsDir, runID, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(paths.runsDir, runID, "array-1")
	if err := writeJSON(filepath.Join(jobDir, "command.json"), JobSpec{ID: "array-1", Command: []string{"true"}, Executor: "slurm", ArrayTaskID: &task, ArrayFirst: 1, ArrayLast: 1}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "status.json"), slurmStatus{Phase: "running", ExitCode: 0, FinishedAt: "2026-09-18T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.metaFile), Meta{Phase: "running", LastRunID: runID}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{RunID: runID, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(paths.lockFile) })

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdShow([]string{"--basedir", baseDir, "--project-name", "demo", "--no-pager"})
	_ = writer.Close()
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "array-1") || !strings.Contains(string(output), " 0 ") {
		t.Fatalf("code=%d output=%q", code, output)
	}
}

func TestSelectRunIDUsesValidMetaLastRun(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-from-meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "newer-run"), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-from-meta"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	runID, err := selectRunID(paths, "")
	if err != nil {
		t.Fatal(err)
	}
	if runID != "run-from-meta" {
		t.Fatalf("run ID = %q, want run-from-meta", runID)
	}
}

func TestSelectRunIDFallsBackToNewestRun(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	olderPath := filepath.Join(paths.runsDir, "older-run")
	newerPath := filepath.Join(paths.runsDir, "newer-run")
	if err := os.MkdirAll(olderPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	if err := os.Chtimes(olderPath, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerPath, newer, newer); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "missing-run"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	runID, err := selectRunID(paths, "")
	if err != nil {
		t.Fatal(err)
	}
	if runID != "newer-run" {
		t.Fatalf("run ID = %q, want newer-run", runID)
	}
}

func TestSelectRunIDDoesNotFallbackForMissingRequestedRun(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "existing-run"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = selectRunID(paths, "missing-run")
	if err == nil || !strings.Contains(err.Error(), `run "missing-run" not found`) {
		t.Fatalf("error = %v, want exact missing-run error", err)
	}
}

func TestCmdShowDisplaysCurrentQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "job-1", Name: "greeting", Command: []string{"printf", "hello"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdShow([]string{"--basedir", baseDir, "--project-name", "demo", "--no-pager"})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdShow exit code = %d, want 0", code)
	}
	for _, want := range []string{"Base directory: " + baseDir, "Project: demo", "Showing jobs queued for the next run", "job-1", "greeting", "printf hello", "To execute these jobs:", "rotari run --basedir '" + baseDir + "' --project-name 'demo'"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("cmdShow output does not contain %q:\n%s", want, output)
		}
	}
}

func TestCmdShowDisplaysActiveRunBeforeQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "queued-job", Command: []string{"echo", "queued"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	runID := "active-run"
	runQueue := Queue{Commands: []QueuedCommand{{ID: "active-job", Command: []string{"echo", "active"}}}}
	if err := writeJSON(filepath.Join(paths.runsDir, runID, "commands.json"), runQueue); err != nil {
		t.Fatal(err)
	}
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID}); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(paths.lockFile)

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdShow([]string{"--basedir", baseDir, "--project-name", "demo", "--no-pager"})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdShow exit code = %d, want 0", code)
	}
	text := string(output)
	for _, want := range []string{"Run: " + runID, "active-job", "echo active"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cmdShow output does not contain %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Showing queued jobs", "queued-job", "echo queued"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("cmdShow output unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}

func TestCmdShowDisplaysInterruptedRunBeforeQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "queued-job", Command: []string{"echo", "queued"}}}}); err != nil {
		t.Fatal(err)
	}
	runID := "interrupted-run"
	if err := writeJSON(filepath.Join(paths.runsDir, runID, "commands.json"), Queue{Commands: []QueuedCommand{{ID: "run-job", Command: []string{"echo", "from-run"}}}}); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: runID, Host: host}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: runID}); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdShow([]string{"--basedir", baseDir, "--project-name", "demo", "--no-pager"})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdShow exit code = %d, want 0", code)
	}
	text := string(output)
	for _, want := range []string{"Run " + runID + " appears to have been interrupted.", "rotari unlock", "Run: " + runID, "run-job", "echo from-run"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cmdShow output does not contain %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Showing jobs queued for the next run", "To execute these jobs:", "queued-job", "echo queued"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("cmdShow output unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}

func TestCmdShowRejectsLogsForCurrentQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"true"}}}}); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdShow([]string{"--basedir", baseDir, "--project-name", "demo", "--logs"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "logs and failed filters require --run-id") {
		t.Fatalf("cmdShow exit code = %d, stderr = %q", code, output)
	}
}

func TestPagerWriterFallsBackWhenPagerCannotStart(t *testing.T) {
	t.Setenv("PAGER", filepath.Join(t.TempDir(), "missing-pager"))
	content := strings.Repeat("line\n", pagerLineLimit) + "partial"
	var output bytes.Buffer
	writer := &pagerWriter{output: &output}

	written, err := writer.Write([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if written != len(content) {
		t.Fatalf("written = %d, want %d", written, len(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if output.String() != content {
		t.Fatalf("fallback output = %q, want %q", output.String(), content)
	}
}

func TestCmdShowFailedLogsFiltersSuccessfulJobs(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	for _, job := range []struct {
		id, name, status, output string
		command                  []string
	}{
		{id: "ok-job", name: "success", status: "0\n", output: "successful output\n", command: []string{"echo", "ok"}},
		{id: "bad-job", name: "failure", status: "3\n", output: "failed output\n", command: []string{"false"}},
	} {
		jobDir := filepath.Join(paths.runsDir, runID, job.id)
		if err := writeJSON(filepath.Join(jobDir, "command.json"), JobSpec{ID: job.id, Name: job.name, Command: job.command}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, "status"), []byte(job.status), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, "output"), []byte(job.output), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdShow([]string{
		"--basedir", baseDir, "--project-name", "demo", "--run-id", runID, "--failed-logs", "--no-pager",
	})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdShow exit code = %d, want 0", code)
	}
	text := string(output)
	for _, want := range []string{"Base directory: " + baseDir, "Project: demo", "Run: " + runID, "bad-job", "failure", "Status: 3 (failed)", "Command: false", "failed output"} {
		if !strings.Contains(text, want) {
			t.Fatalf("failed logs do not contain %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"ok-job", "successful output"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("failed logs unexpectedly contain %q:\n%s", unwanted, text)
		}
	}
}

func TestShowJobDisplaysPersistedDetails(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID, jobID := "run-1", "job-1"
	runDir := filepath.Join(paths.runsDir, runID)
	jobDir := filepath.Join(runDir, jobID)
	job := JobSpec{
		ID: jobID, Name: "analysis", Command: []string{"python", "work.py"},
		Executor: "slurm", ExecutorOptions: []string{"--partition", "gpu"}, DependsOn: []string{"setup"},
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), Queue{Commands: []QueuedCommand{{
		ID: job.ID, Name: job.Name, Command: job.Command, Executor: job.Executor,
		ExecutorOptions: job.ExecutorOptions, DependsOn: job.DependsOn,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{Results: []JobResult{{ID: jobID, Hosts: []string{"node-a"}}}}); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"status": "0\n", "submitted_at": "2026-01-01T00:00:00Z\n",
		"finished_at": "2026-01-01T00:01:00Z\n", "output": "completed\n",
	} {
		if err := os.WriteFile(filepath.Join(jobDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var output bytes.Buffer
	if code := showJob(&output, paths, runID, jobID); code != 0 {
		t.Fatalf("showJob exit code = %d, want 0", code)
	}
	for _, want := range []string{
		"analysis", "slurm", "--partition gpu", "setup", "node-a", "Status: 0", "python work.py", "completed",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("showJob output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestShowJobSurfacesAccountingUnavailableFromSummary(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID, jobID := "run-1", "job-1"
	runDir := filepath.Join(paths.runsDir, runID)
	jobDir := filepath.Join(runDir, jobID)
	job := JobSpec{ID: jobID, Command: []string{"python", "work.py"}, Executor: "slurm"}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), Queue{Commands: []QueuedCommand{{
		ID: job.ID, Command: job.Command, Executor: job.Executor,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		t.Fatal(err)
	}
	// No "status" file and no "status.json" -- as when squeue and sacct were
	// both unreachable -- leaves summary.json as the only source of the result.
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{Results: []JobResult{
		{ID: jobID, ExitCode: 1, Error: "Slurm accounting result and wrapper status are unavailable"},
	}}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if code := showJob(&output, paths, runID, jobID); code != 0 {
		t.Fatalf("showJob exit code = %d, want 0", code)
	}
	if want := "Status: 1 (Slurm accounting result and wrapper status are unavailable)"; !strings.Contains(output.String(), want) {
		t.Fatalf("showJob output does not contain %q:\n%s", want, output.String())
	}
}

func TestShowJobRejectsTraversalInRunAndJobIDs(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	escapedDir := filepath.Join(filepath.Dir(paths.runsDir), "outside", "job-1")
	if err := os.MkdirAll(escapedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(escapedDir, "output"), []byte("escaped\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		runID string
		jobID string
	}{
		{runID: "../outside", jobID: "job-1"},
		{runID: "run-1", jobID: "../outside"},
	} {
		var output bytes.Buffer
		if code := showJob(&output, paths, tc.runID, tc.jobID); code == 0 {
			t.Fatalf("showJob accepted unsafe values runID=%q jobID=%q", tc.runID, tc.jobID)
		}
	}
}

func TestShowJobFollowsCarriedForwardOrigin(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	jobID := "job-1"
	currentRunDir := filepath.Join(paths.runsDir, "run-2")
	origin := &JobOrigin{RunID: "run-1", JobID: jobID, Status: "success"}
	if err := writeJSON(filepath.Join(currentRunDir, "commands.json"), Queue{Commands: []QueuedCommand{{
		ID: jobID, Name: "carried", Command: []string{"echo", "original"}, Origin: origin,
	}}}); err != nil {
		t.Fatal(err)
	}
	originJobDir := filepath.Join(paths.runsDir, origin.RunID, origin.JobID)
	if err := writeJSON(filepath.Join(originJobDir, "command.json"), JobSpec{ID: jobID, Name: "carried", Command: []string{"echo", "original"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, origin.RunID, "commands.json"), Queue{Commands: []QueuedCommand{{
		ID: jobID, Name: "carried", Command: []string{"echo", "original"},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originJobDir, "status"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originJobDir, "output"), []byte("original output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if code := showJob(&output, paths, "run-2", jobID); code != 0 {
		t.Fatalf("showJob exit code = %d, want 0", code)
	}
	for _, want := range []string{"carried forward from run run-1", "Run: run-1", "original output"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("showJob output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestIsTerminalRejectsRegularFile(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if isTerminal(regular) {
		t.Fatal("regular file was treated as a terminal")
	}
}

func TestShowQueueJobPrintsMatchingJob(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "job-1", Name: "build", Command: []string{"echo", "build"}, DependsOn: []string{"prepare"}},
	}}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := showQueueJob(paths, queue, "job-1")
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("showQueueJob exit code = %d, want 0", code)
	}
	for _, want := range []string{"Job: job-1", "Name: build", "Depends on: prepare", "Command: echo build"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("showQueueJob output does not contain %q:\n%s", want, output)
		}
	}
}

func TestShowQueueDisplaysArrayTaskColumn(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "train", Name: "train", Command: []string{"echo", "train"}, Array: &ArraySpec{First: 1, Last: 2}},
	}}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := showQueue(paths, queue)
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("showQueue exit code = %d, want 0", code)
	}
	for _, want := range []string{"TASK", "train-1", "train-2"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("showQueue output does not contain %q:\n%s", want, output)
		}
	}

	reader, writer, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code = showQueueJob(paths, queue, "train-1")
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "Array task: 1 (range 1-2)") {
		t.Fatalf("showQueueJob code=%d output does not contain array task info:\n%s", code, output)
	}
}

func TestShowQueueJobReportsMissingJob(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"echo", "build"}}}}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := showQueueJob(paths, queue, "missing")
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `job "missing" not found in current queue`) {
		t.Fatalf("showQueueJob exit code = %d, stderr = %q", code, output)
	}
}

func TestShowRunsListsRunsSortedByRecency(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	olderRun := filepath.Join(paths.runsDir, "run-old")
	newerRun := filepath.Join(paths.runsDir, "run-new")
	if err := writeJSON(filepath.Join(olderRun, "summary.json"), RunSummary{
		RunID: "run-old", Status: "finished", ExitCode: 0, StartedAt: "2026-09-16T00:00:00Z", FinishedAt: "2026-09-16T00:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(newerRun, "summary.json"), RunSummary{
		RunID: "run-new", Status: "failed", ExitCode: 1, StartedAt: "2026-09-17T00:00:00Z", FinishedAt: "2026-09-17T00:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}
	newerTime := time.Now()
	olderTime := newerTime.Add(-time.Hour)
	if err := os.Chtimes(olderRun, olderTime, olderTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerRun, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := showRuns(paths)
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("showRuns exit code = %d, want 0", code)
	}
	text := string(output)
	newIndex := strings.Index(text, "run-new")
	oldIndex := strings.Index(text, "run-old")
	if newIndex == -1 || oldIndex == -1 || newIndex > oldIndex {
		t.Fatalf("showRuns did not list run-new before run-old:\n%s", text)
	}
}

func TestShowRunsReportsNoRunsWhenDirectoryMissing(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := showRuns(paths)
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "No runs found.") {
		t.Fatalf("showRuns exit code = %d, stdout = %q", code, output)
	}
}
