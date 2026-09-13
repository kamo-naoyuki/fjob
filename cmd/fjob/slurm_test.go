package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitSlurmJobWithFakeSlurm(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "sbatch", `#!/bin/sh
printf '12345;fake-host\n'
`)
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	runDir := t.TempDir()
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
}

func TestSlurmStatusCommandsWithFakeSlurm(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "squeue", "#!/bin/sh\nprintf 'RUNNING\\n'\n")
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
	exitCode, state, ok := slurmAccounting("12345")
	if !ok || exitCode != 1 || state != "failed" {
		t.Fatalf("slurmAccounting = %d, %q, %v; want 1, failed, true", exitCode, state, ok)
	}
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
