package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCmdChangeUpdatesExecutorEnvironmentAndCommandByJobID(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "old"}, "", nil, nil, "job", nil); err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	jobID := queue.Commands[0].ID

	code := cmdChange([]string{
		"--basedir", baseDir, "--project-name", "default", "--job-id", jobID,
		"--executor", "slurm", "--executor-option", "-p short",
		"--env", "TOKEN=secret", "--set-job-name", "renamed",
		"echo", "new",
	})
	if code != 0 {
		t.Fatalf("cmdChange exit code = %d, want 0", code)
	}

	queue, err = loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 {
		t.Fatalf("queue commands = %#v, want 1 job", queue.Commands)
	}
	changed := queue.Commands[0]
	if changed.Executor != "slurm" ||
		len(changed.ExecutorOptions) != 1 || changed.ExecutorOptions[0] != "-p short" ||
		len(changed.Environment) != 1 || changed.Environment[0] != "TOKEN=secret" ||
		changed.Name != "renamed" || strings.Join(changed.Command, " ") != "echo new" {
		t.Fatalf("changed command = %#v, want updated fields", changed)
	}

	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" {
		t.Fatalf("meta.Phase = %q, want collecting", meta.Phase)
	}
}

func TestCmdChangeRejectsRunningProject(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "job"}, "", nil, nil, "job", nil); err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "active-run"}); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(paths.lockFile)

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdChange([]string{"--basedir", baseDir, "--project-name", "default", "--job-name", "job", "--executor", "local"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `project "default" is running; change is not allowed`) {
		t.Fatalf("cmdChange exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdChangeRejectsAmbiguousSelector(t *testing.T) {
	baseDir := t.TempDir()
	code := cmdChange([]string{"--basedir", baseDir, "--project-name", "default", "--job-id", "a", "--job-name", "b", "--executor", "local"})
	if code != 1 {
		t.Fatalf("cmdChange exit code = %d, want 1 for ambiguous selector", code)
	}
}

func TestCmdChangeRejectsUnknownJobID(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "job"}, "", nil, nil, "job", nil); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdChange([]string{"--basedir", baseDir, "--project-name", "default", "--job-id", "missing", "--executor", "local"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "job not found") {
		t.Fatalf("cmdChange exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdChangeRejectsRenameReferencedByDependency(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "a"}, "", nil, nil, "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "b"}, "", nil, nil, "b", []string{"a"}); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdChange([]string{"--basedir", baseDir, "--project-name", "default", "--job-name", "a", "--set-job-name", "c"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `job "a" is referenced by dependency; rename is not allowed`) {
		t.Fatalf("cmdChange exit code = %d, stderr = %q", code, output)
	}
}
