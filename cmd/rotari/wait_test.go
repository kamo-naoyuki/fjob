package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdWaitReturnsCompletedRunExitCode(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	summary := RunSummary{
		RunID: runID, RunName: "nightly", Status: "failed", ExitCode: 2,
		Results: []JobResult{{ID: "job-1", ExitCode: 0}, {ID: "job-2", ExitCode: 2}},
	}
	if err := writeJSON(filepath.Join(paths.runsDir, runID, "summary.json"), summary); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdWait([]string{"--basedir", baseDir, "--project-name", "demo", "--run-id", runID})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 {
		t.Fatalf("cmdWait exit code = %d, want 2", code)
	}
	for _, want := range []string{"Run failed:", "nightly", "Success: 1", "Failed: 1"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("cmdWait output does not contain %q:\n%s", want, output)
		}
	}
}

func TestCmdWaitAcceptsMultipleRunIDs(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())

	baseDir := t.TempDir()
	pathsA, err := resolvePaths(baseDir, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	pathsB, err := resolvePaths(baseDir, "beta")
	if err != nil {
		t.Fatal(err)
	}
	runA := "run-a"
	runB := "run-b"
	if err := writeJSON(filepath.Join(pathsA.runsDir, runA, "summary.json"), RunSummary{RunID: runA, Status: "success", Results: []JobResult{{ID: "job-a", ExitCode: 0}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(pathsB.runsDir, runB, "summary.json"), RunSummary{RunID: runB, Status: "failed", ExitCode: 3, Results: []JobResult{{ID: "job-b", ExitCode: 3}}}); err != nil {
		t.Fatal(err)
	}
	if err := registerRun(pathsA, runA); err != nil {
		t.Fatal(err)
	}
	if err := registerRun(pathsB, runB); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdWait([]string{"--run-id", runA, runB})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 {
		t.Fatalf("cmdWait exit code = %d, want 3", code)
	}
	for _, want := range []string{"Project: alpha", "Project: beta", "Run: run-a", "Run: run-b"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("cmdWait output does not contain %q:\n%s", want, output)
		}
	}
}

func TestCmdWaitTimesOutForMalformedSummary(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	summaryPath := filepath.Join(paths.runsDir, runID, "summary.json")
	if err := os.MkdirAll(filepath.Dir(summaryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summaryPath, []byte("{incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdWait([]string{"--basedir", baseDir, "--project-name", "demo", "--run-id", runID, "--timeout", "1ms"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "timed out waiting for run run-1") {
		t.Fatalf("cmdWait exit code = %d, stderr = %q", code, output)
	}
}
