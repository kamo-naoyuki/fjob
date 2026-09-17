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
