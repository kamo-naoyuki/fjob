package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestCmdRemoveDeletesJobByID(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "a"}, "", nil, nil, "a", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "b"}, "", nil, nil, "b", nil); err != nil {
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
	removedID := queue.Commands[0].ID

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-id", removedID})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "removed 1 job(s) from queue=default") {
		t.Fatalf("cmdRemove exit code = %d, stdout = %q", code, output)
	}

	queue, err = loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].Name != "b" {
		t.Fatalf("queue commands = %#v, want only job b remaining", queue.Commands)
	}
}

func TestCmdRemoveDeletesJobByName(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "a"}, "", nil, nil, "a", nil); err != nil {
		t.Fatal(err)
	}

	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-name", "a"})
	if code != 0 {
		t.Fatalf("cmdRemove exit code = %d, want 0", code)
	}

	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 0 {
		t.Fatalf("queue commands = %#v, want empty queue", queue.Commands)
	}
}

func TestCmdRemoveRejectsRunningProject(t *testing.T) {
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
	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-name", "job"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `project "default" is running; remove is not allowed`) {
		t.Fatalf("cmdRemove exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdRemoveRejectsWhenDependencyReferencesRemovedJob(t *testing.T) {
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
	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-name", "a"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `job "a" is referenced by dependency; remove is not allowed`) {
		t.Fatalf("cmdRemove exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdRemoveRejectsUnknownJobID(t *testing.T) {
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
	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-id", "missing"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "job not found") {
		t.Fatalf("cmdRemove exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdRemoveByIDsRejectsWhenOneIDIsMissing(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "a"}, "", nil, nil, "a", nil); err != nil {
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
	existingID := queue.Commands[0].ID

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdRemove([]string{"--basedir", baseDir, "--project-name", "default", "--job-id", existingID, "--job-id", "missing"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "one or more jobs not found") {
		t.Fatalf("cmdRemove exit code = %d, stderr = %q", code, output)
	}

	queue, err = loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 {
		t.Fatalf("queue commands = %#v, want job untouched after rejected batch", queue.Commands)
	}
}

func TestCmdRemoveRejectsInvalidUsage(t *testing.T) {
	baseDir := t.TempDir()
	cases := [][]string{
		{"--basedir", baseDir, "--project-name", "default"},
		{"--basedir", baseDir, "--project-name", "default", "--job-id", "a", "--job-name", "b"},
		{"--basedir", baseDir, "--project-name", "default", "--job-name", "a", "extra"},
	}
	for _, args := range cases {
		if code := cmdRemove(args); code != 1 {
			t.Fatalf("cmdRemove(%v) exit code = %d, want 1", args, code)
		}
	}
}
