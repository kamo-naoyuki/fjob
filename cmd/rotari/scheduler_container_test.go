package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

type schedulerContainerTestConfig struct {
	executor  string
	container string
	user      string
}

func requireSchedulerContainerTest(t *testing.T) schedulerContainerTestConfig {
	t.Helper()
	if os.Getenv("ROTARI_SCHEDULER_CONTAINER_TEST") != "1" {
		t.Skip("set ROTARI_SCHEDULER_CONTAINER_TEST=1 to run scheduler container tests")
	}
	config := schedulerContainerTestConfig{
		executor:  os.Getenv("ROTARI_SCHEDULER_EXECUTOR"),
		container: os.Getenv("SCHEDULER_CONTAINER"),
		user:      os.Getenv("SCHEDULER_USER"),
	}
	if config.executor == "" || config.container == "" {
		t.Fatalf("ROTARI_SCHEDULER_EXECUTOR and SCHEDULER_CONTAINER must be set")
	}
	return config

}

func TestSchedulerContainerSlurmArrayTaskVariable(t *testing.T) {
	config := requireSchedulerContainerTest(t)
	if config.executor != "slurm" {
		t.Skip("Slurm container test")
	}

	marker := fmt.Sprintf("/state/rotari-slurm-array-%d.out", time.Now().UnixNano())
	wrapped := fmt.Sprintf("printf '%%s\\n' \"$SLURM_ARRAY_TASK_ID\" >> %s", shellQuote(marker))
	runSchedulerContainerCommand(t, config, fmt.Sprintf("rm -f %s; sbatch --parsable --array=1-2 --wrap %s", shellQuote(marker), shellQuote(wrapped)))

	assertSchedulerContainerFileLines(t, config, marker, []string{"1", "2"})
}

func TestSchedulerContainerPBSArrayTaskVariable(t *testing.T) {
	config := requireSchedulerContainerTest(t)
	if config.executor != "pbs" {
		t.Skip("PBS container test")
	}

	marker := fmt.Sprintf("/state/rotari-pbs-array-%d.out", time.Now().UnixNano())
	scriptPath := fmt.Sprintf("/state/rotari-pbs-array-%d.sh", time.Now().UnixNano())
	runSchedulerContainerCommand(t, config, fmt.Sprintf(`cat > %s <<'ROTARI_SCRIPT'
#!/bin/sh
printf '%%s\n' "$PBS_ARRAY_INDEX" >> %s
ROTARI_SCRIPT
chmod +x %s
rm -f %s
qsub -j oe -J 1-2 %s`, shellQuote(scriptPath), shellQuote(marker), shellQuote(scriptPath), shellQuote(marker), shellQuote(scriptPath)))

	assertSchedulerContainerFileLines(t, config, marker, []string{"1", "2"})
}

func assertSchedulerContainerFileLines(t *testing.T, config schedulerContainerTestConfig, path string, want []string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		output := runSchedulerContainerCommand(t, config, fmt.Sprintf("cat %s 2>/dev/null || true", shellQuote(path)))
		got := nonEmptySortedLines(output)
		if sameStrings(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("container file %s lines = %#v, want %#v", path, got, want)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func nonEmptySortedLines(output string) []string {
	lines := strings.Split(output, "\n")
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			values = append(values, line)
		}
	}
	sort.Strings(values)
	return values
}

func runSchedulerContainerCommand(t *testing.T, config schedulerContainerTestConfig, script string) string {
	t.Helper()
	args := []string{"exec"}
	if config.user != "" {
		args = append(args, "--user", config.user)
	}
	args = append(args, config.container, "sh", "-lc", script)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("docker exec timed out: %s", script)
	}
	if err != nil {
		t.Fatalf("docker exec failed: %v\nscript: %s\noutput: %s", err, script, output)
	}
	return string(output)
}
