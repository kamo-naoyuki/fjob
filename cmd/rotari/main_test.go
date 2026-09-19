package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolveProjectNamePriority(t *testing.T) {
	const envName = "ROTARI_PROJECT_NAME"
	old, existed := os.LookupEnv(envName)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(envName, old)
		} else {
			_ = os.Unsetenv(envName)
		}
	})

	baseDir := t.TempDir()

	if err := os.Setenv(envName, "from-env"); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProjectName(baseDir, "from-option")
	if err != nil || got != "from-option" {
		t.Fatalf("option priority: got %q, err %v", got, err)
	}
	got, err = resolveProjectName(baseDir, "")
	if err != nil || got != "from-env" {
		t.Fatalf("environment priority: got %q, err %v", got, err)
	}
	if err := os.Unsetenv(envName); err != nil {
		t.Fatal(err)
	}
	got, err = resolveProjectName(baseDir, "")
	if err != nil || got != defaultProjectName {
		t.Fatalf("default priority: got %q, want %q, err %v", got, defaultProjectName, err)
	}

	// Test automatically selecting a single queue if only one exists
	q1Dir := filepath.Join(baseDir, "projects", "q1")
	if err := os.MkdirAll(q1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	got, err = resolveProjectName(baseDir, "")
	if err != nil || got != "q1" {
		t.Fatalf("auto select single project: got %q, want q1, err %v", got, err)
	}

	// Test returning an error if multiple projects exist and none is specified
	q2Dir := filepath.Join(baseDir, "projects", "q2")
	if err := os.MkdirAll(q2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	_, err = resolveProjectName(baseDir, "")
	if err == nil {
		t.Fatal("expected error for multiple projects when project-name is empty, got nil")
	}
}

func TestResolvePathsRejectsProjectTraversal(t *testing.T) {
	for _, projectName := range []string{"../outside", ".", "..", "nested/project"} {
		if _, err := resolvePaths(t.TempDir(), projectName); err == nil {
			t.Errorf("resolvePaths accepted unsafe project name %q", projectName)
		}
	}
}

func TestResolveProjectNameRejectsUnsafeProjectNames(t *testing.T) {
	for _, projectName := range []string{"../outside", "/tmp/outside", ".", "..", "nested/project", "subdir/..", "job/with/slash"} {
		if _, err := resolveProjectName(t.TempDir(), projectName); err == nil {
			t.Errorf("resolveProjectName accepted unsafe project name %q", projectName)
		}
	}

	const envName = "ROTARI_PROJECT_NAME"
	old, existed := os.LookupEnv(envName)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(envName, old)
		} else {
			_ = os.Unsetenv(envName)
		}
	})
	if err := os.Setenv(envName, "../outside"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveProjectName(t.TempDir(), ""); err == nil {
		t.Fatal("resolveProjectName accepted unsafe project name from environment")
	}
}

func TestResolvePathsRejectsAbsoluteAndNestedPathVariants(t *testing.T) {
	for _, projectName := range []string{"/tmp/outside", "///tmp/outside", "nested/../outside", "subdir/.", "subdir/..", "job/with/slash", "..\\outside", "nested\\project", "C:\\tmp\\outside"} {
		if _, err := resolvePaths(t.TempDir(), projectName); err == nil {
			t.Errorf("resolvePaths accepted unsafe project name %q", projectName)
		}
	}
}

func TestValidWebIDRejectsTraversalAndDotSegments(t *testing.T) {
	for _, value := range []string{"..", ".", "../outside", "nested/project", "nested\\project", "/tmp/outside", "C:\\tmp\\outside"} {
		if validWebID(value) {
			t.Fatalf("validWebID accepted unsafe value %q", value)
		}
	}
	if !validWebID("demo") || !validWebID("run-2026") || !validWebID("job-1") {
		t.Fatal("validWebID rejected a normal identifier")
	}
}

func TestResolveBaseDirPriority(t *testing.T) {
	const envName = "ROTARI_BASEDIR"
	old, existed := os.LookupEnv(envName)
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(envName, old)
		} else {
			_ = os.Unsetenv(envName)
		}
	})

	_ = os.Unsetenv(envName)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	// 1. Fallback to default (home directory, etc.) if .rotari-state does not exist in current dir
	_, _, err = resolveBaseDir("")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Prefer .rotari-state in current dir if it exists
	localState := filepath.Join(tempDir, ".rotari-state")
	if err := os.Mkdir(localState, 0755); err != nil {
		t.Fatal(err)
	}

	got, _, err := resolveBaseDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != localState {
		t.Fatalf("current dir priority: got %q, want %q", got, localState)
	}

	// 3. ROTARI_BASEDIR environment variable priority
	if err := os.Setenv(envName, "/env/basedir"); err != nil {
		t.Fatal(err)
	}
	got, _, err = resolveBaseDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/env/basedir" {
		t.Fatalf("env priority: got %q, want %q", got, "/env/basedir")
	}

	// 4. CLI option priority
	got, _, err = resolveBaseDir("/cli/basedir")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/cli/basedir" {
		t.Fatalf("cli option priority: got %q, want %q", got, "/cli/basedir")
	}
}

func TestStateModeDefaultsToSharedPermissions(t *testing.T) {
	t.Setenv(envPrivateState, "")
	if got := stateDirMode(); got != 0o755 {
		t.Fatalf("stateDirMode() = %o, want 0755 (shared by default)", got)
	}
	if got := stateFileMode(); got != 0o644 {
		t.Fatalf("stateFileMode() = %o, want 0644 (shared by default)", got)
	}
	if got := stateScriptMode(); got != 0o755 {
		t.Fatalf("stateScriptMode() = %o, want 0755 (shared by default)", got)
	}

	t.Setenv(envPrivateState, "true")
	if got := stateDirMode(); got != 0o700 {
		t.Fatalf("stateDirMode() with %s=true = %o, want 0700", envPrivateState, got)
	}
	if got := stateFileMode(); got != 0o600 {
		t.Fatalf("stateFileMode() with %s=true = %o, want 0600", envPrivateState, got)
	}
	if got := stateScriptMode(); got != 0o700 {
		t.Fatalf("stateScriptMode() with %s=true = %o, want 0700", envPrivateState, got)
	}
}

func TestCLIStringUsesRotariEnvironmentDefaults(t *testing.T) {
	t.Setenv("ROTARI_RUN_ID", "run-from-env")
	t.Setenv("ROTARI_JOB_ID", "job-from-env")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	runID := cliString(fs, "run-id", "")
	jobID := cliString(fs, "job-id", "")
	if *runID != "run-from-env" || *jobID != "job-from-env" {
		t.Fatalf("defaults = %q, %q", *runID, *jobID)
	}
	if err := fs.Parse([]string{"--run-id", "run-from-flag"}); err != nil {
		t.Fatal(err)
	}
	if *runID != "run-from-flag" {
		t.Fatalf("flag value = %q, want explicit flag to override environment", *runID)
	}
}

func TestCLICommandSpecificEnvironmentDefaults(t *testing.T) {
	t.Setenv("ROTARI_WEB_HOST", "127.0.0.2")
	t.Setenv("ROTARI_WEB_PORT", "9000")
	t.Setenv("ROTARI_WAIT_TIMEOUT", "2s")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	host := cliString(fs, "host", "127.0.0.1")
	port := cliInt(fs, "port", 8787)
	timeout := cliDuration(fs, "timeout", 0)
	if *host != "127.0.0.2" || *port != 9000 || *timeout != 2*time.Second {
		t.Fatalf("defaults = %q, %d, %s", *host, *port, *timeout)
	}
}

func TestCLIHelpShowsEnvironmentDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var output strings.Builder
	fs.SetOutput(&output)
	cliString(fs, "basedir", "")
	cliBool(fs, "overwrite", false)
	cliInt(fs, "local-concurrency", 8)

	fs.PrintDefaults()
	help := output.String()
	if !strings.Contains(help, "basedir") || !strings.Contains(help, "env: ROTARI_BASEDIR") {
		t.Fatalf("help does not show basedir environment variable: %q", help)
	}
	if strings.Contains(help, "--overwrite") && strings.Contains(help, "ROTARI_OVERWRITE") {
		t.Fatalf("help advertises unsupported overwrite environment variable: %q", help)
	}
	if !strings.Contains(help, "env: ROTARI_RUN_LOCAL_CONCURRENCY") {
		t.Fatalf("help does not show local concurrency environment variable: %q", help)
	}
}

func TestEnvironmentDefinitionsAreUniqueAndIncludeCoreVariables(t *testing.T) {
	definitions := environmentDefinitions()
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" {
			t.Fatal("environment definition has an empty name")
		}
		if seen[definition.Name] {
			t.Fatalf("duplicate environment definition %q", definition.Name)
		}
		seen[definition.Name] = true
	}
	for _, name := range []string{envBaseDir, envRunID, envJobID, envExecutor, envRunRetry, envRunAsync, envArrayTaskID, envWebPort} {
		if !seen[name] {
			t.Errorf("missing environment definition %q", name)
		}
	}
}

func TestCmdEnvironmentListsCurrentValues(t *testing.T) {
	t.Setenv(envRunID, "run-from-env")
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdEnvironment(nil)
	_ = writer.Close()
	os.Stdout = oldStdout
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if code != 0 || readErr != nil {
		t.Fatalf("cmdEnvironment = %d, read error = %v", code, readErr)
	}
	output := string(data)
	if !strings.Contains(output, "VARIABLE\tVALUE\tCLI\tJOB\tARRAY") || !strings.Contains(output, envRunID+"\trun-from-env") {
		t.Fatalf("environment output = %q", output)
	}
}

func TestResolveExistingRunTargetUsesRegistryAndRejectsConflicts(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	location := runLocation{BaseDir: baseDir, ProjectName: "demo", RunID: "run-1"}
	if err := registerRunLocation(location); err != nil {
		t.Fatal(err)
	}

	gotBaseDir, gotProject, err := resolveExistingRunTarget("", "", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotBaseDir != baseDir || gotProject != "demo" {
		t.Fatalf("target = %q, %q; want %q, demo", gotBaseDir, gotProject, baseDir)
	}
	if _, _, err := resolveExistingRunTarget(t.TempDir(), "", "run-1"); err == nil {
		t.Fatal("conflicting basedir was accepted")
	}
	if _, _, err := resolveExistingRunTarget("", "other", "run-1"); err == nil {
		t.Fatal("conflicting project was accepted")
	}
}

func TestSplitShellWords(t *testing.T) {
	got, err := splitShellWords(`-p "short queue" --constraint='fast\ node' --exclusive`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "short queue", "--constraint=fast\\ node", "--exclusive"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitShellWordsRejectsUnterminatedInput(t *testing.T) {
	for _, input := range []string{`"unterminated`, `trailing\`} {
		if _, err := splitShellWords(input); err == nil {
			t.Errorf("splitShellWords(%q) returned nil error", input)
		}
	}
}

func TestParseSlurmExitCode(t *testing.T) {
	cases := map[string]int{"0:0": 0, "1:0": 1, "2:15": 2, "invalid": 1}
	for input, want := range cases {
		if got := parseSlurmExitCode(input); got != want {
			t.Errorf("parseSlurmExitCode(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestMakeRunIDFormat(t *testing.T) {
	pattern := regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`)
	first := makeRunID()
	if !pattern.MatchString(first) {
		t.Fatalf("makeRunID() = %q, want format YYYYMMDD-HHMMSS-XXXXXXXX", first)
	}
	if first == makeRunID() {
		t.Fatal("makeRunID returned the same ID twice")
	}
}

func TestFormatProjectRunningErrorIncludesWaitAndCancelHints(t *testing.T) {
	output := formatProjectRunningError(pathSet{baseDir: "/state", queueName: "demo"}, "run-1")
	for _, want := range []string{
		"project 'demo' is running",
		"Run: run-1",
		"rotari wait --basedir /state --project-name demo --run-id run-1",
		"rotari cancel --basedir /state --project-name demo",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("formatProjectRunningError() missing %q; got %q", want, output)
		}
	}
}

func TestCmdResetClearsQueueButKeepsDefaultsAndHistory(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{DefaultExecutor: "slurm", Commands: []QueuedCommand{{ID: "queued", Command: []string{"echo", "queued"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"}); code != 0 {
		t.Fatalf("cmdReset exit code = %d, want 0", code)
	}
	gotQueue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotQueue.Commands) != 0 || gotQueue.DefaultExecutor != "slurm" {
		t.Fatalf("queue after reset = %#v, want no commands and preserved defaults", gotQueue)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-1")); err != nil {
		t.Fatalf("run history was removed: %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" {
		t.Fatalf("metadata phase = %q, want collecting", meta.Phase)
	}
}

func TestCmdResetClearsInvalidDuplicateNameQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "old-prepare", Command: []string{"echo", "old"}, Name: "prepare"},
		{ID: "new-prepare", Command: []string{"echo", "new"}, Name: "prepare"},
	}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}

	if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"}); code != 0 {
		t.Fatalf("cmdReset exit code = %d, want 0", code)
	}
	gotQueue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotQueue.Commands) != 0 {
		t.Fatalf("queue commands = %#v, want empty", gotQueue.Commands)
	}
}

func TestCmdResetRejectsRunningProject(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}

	if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"}); code == 0 {
		t.Fatal("cmdReset accepted a running project")
	}
}

func TestCmdResetRecoversInterruptedRunWithoutPrompt(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "retained", Command: []string{"retained"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo", "--recover"}); code != 0 {
		t.Fatalf("cmdReset exit code = %d, want 0", code)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 0 {
		t.Fatalf("queue commands = %#v, want empty", queue.Commands)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" {
		t.Fatalf("metadata phase = %q, want collecting", meta.Phase)
	}
}

func TestCmdResetRequiresRecoverFlagForInterruptedRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "retained", Command: []string{"retained"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"}); code == 0 {
		t.Fatal("cmdReset recovered an interrupted run without confirmation")
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 {
		t.Fatalf("queue commands = %#v, want unchanged", queue.Commands)
	}
}

func TestCmdResetRejectsUnexpectedStateWithoutDiscardingQueue(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, paths pathSet)
	}{
		{
			name: "malformed queue",
			setup: func(t *testing.T, paths pathSet) {
				t.Helper()
				if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.queueFile, []byte("not json\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed metadata",
			setup: func(t *testing.T, paths pathSet) {
				t.Helper()
				if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.metaFile, []byte("not json\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown metadata phase",
			setup: func(t *testing.T, paths pathSet) {
				t.Helper()
				if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "retained", Command: []string{"echo", "retained"}}}}); err != nil {
					t.Fatal(err)
				}
				if err := writeJSON(paths.metaFile, Meta{Phase: "unknown"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed run lock",
			setup: func(t *testing.T, paths pathSet) {
				t.Helper()
				if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.lockFile, []byte("not json\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "remote run lock",
			setup: func(t *testing.T, paths pathSet) {
				t.Helper()
				if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "retained", Command: []string{"echo", "retained"}}}}); err != nil {
					t.Fatal(err)
				}
				if err := writeJSON(paths.lockFile, LockInfo{PID: -1, RunID: "run-1", Host: "other-host"}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseDir := t.TempDir()
			paths, err := resolvePaths(baseDir, "demo")
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, paths)

			if code := cmdReset([]string{"--basedir", baseDir, "--project-name", "demo"}); code == 0 {
				t.Fatal("cmdReset accepted an unexpected project state")
			}
			if _, err := os.Stat(paths.queueFile); err == nil {
				queue, loadErr := loadQueue(paths.queueFile)
				if loadErr == nil && len(queue.Commands) == 0 {
					t.Fatal("cmdReset discarded queued commands")
				}
			}
		})
	}
}

func TestConfirmResetOfInterruptedRun(t *testing.T) {
	paths := pathSet{queueName: "demo"}
	confirmed, err := confirmResetOfInterruptedRun(strings.NewReader("yes\n"), io.Discard, paths, "run-1")
	if err != nil || !confirmed {
		t.Fatalf("confirmResetOfInterruptedRun(yes) = %v, %v", confirmed, err)
	}
	confirmed, err = confirmResetOfInterruptedRun(strings.NewReader("no\n"), io.Discard, paths, "run-1")
	if err != nil || confirmed {
		t.Fatalf("confirmResetOfInterruptedRun(no) = %v, %v", confirmed, err)
	}
}

func TestRunStatus(t *testing.T) {
	if got := runStatus(0); got != "finished" {
		t.Fatalf("runStatus(0) = %q, want finished", got)
	}
	if got := runStatus(1); got != "failed" {
		t.Fatalf("runStatus(1) = %q, want failed", got)
	}
}

func TestQueueToJobsPreservesName(t *testing.T) {
	jobs := queueToJobs([]QueuedCommand{{
		ID:      "fixed-id",
		Command: []string{"echo", "hello"},
		Name:    "greeting",
	}})
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Name != "greeting" {
		t.Fatalf("job name = %q, want greeting", jobs[0].Name)
	}
	if jobs[0].ID != "fixed-id" {
		t.Fatalf("job id = %q, want fixed-id", jobs[0].ID)
	}
}

func TestQueueToJobsExpandsArray(t *testing.T) {
	taskJobs := queueToJobs([]QueuedCommand{{
		ID: "array", Command: []string{"echo", "hello"}, Name: "train",
		Array: &ArraySpec{First: 2, Last: 4},
	}})
	if len(taskJobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(taskJobs))
	}
	for index, wantTask := range []int{2, 3, 4} {
		job := taskJobs[index]
		if job.ID != fmt.Sprintf("array-%d", wantTask) || job.ArrayTaskID == nil || *job.ArrayTaskID != wantTask || job.Name != fmt.Sprintf("train[%d]", wantTask) {
			t.Fatalf("job %d = %#v, want task %d", index, job, wantTask)
		}
	}
}

func TestParseArrayRange(t *testing.T) {
	got, err := parseArrayRange("2-4")
	if err != nil || got.First != 2 || got.Last != 4 || len(got.Tasks) != 0 {
		t.Fatalf("parseArrayRange = %#v, %v", got, err)
	}
	got, err = parseArrayRange("1,3,4")
	if err != nil || !reflect.DeepEqual(got, ArraySpec{First: 1, Last: 4, Tasks: []int{1, 3, 4}}) {
		t.Fatalf("parseArrayRange sparse = %#v, %v", got, err)
	}
	for _, value := range []string{"", "4-2", "one-2", "1,,3", "1,3,3"} {
		if _, err := parseArrayRange(value); err == nil {
			t.Errorf("parseArrayRange(%q) returned nil error", value)
		}
	}
}

func TestQueueToJobsExpandsSparseArray(t *testing.T) {
	jobs := queueToJobs([]QueuedCommand{{
		ID: "array", Command: []string{"echo", "hello"}, Array: &ArraySpec{First: 1, Last: 4, Tasks: []int{1, 3, 4}},
	}})
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}
	for index, wantTask := range []int{1, 3, 4} {
		if jobs[index].ID != fmt.Sprintf("array-%d", wantTask) || *jobs[index].ArrayTaskID != wantTask || jobs[index].ArraySize != 3 {
			t.Fatalf("job %d = %#v, want task %d with size 3", index, jobs[index], wantTask)
		}
	}
}

func TestSparseArrayUsesIndividualSubmissions(t *testing.T) {
	jobs := queueToJobs([]QueuedCommand{{
		ID: "array", Command: []string{"echo", "hello"}, Array: &ArraySpec{First: 1, Last: 4, Tasks: []int{1, 3, 4}},
	}})
	if completeArrayGroup(jobs, 1, 4) {
		t.Fatal("sparse array must not use a native contiguous scheduler array")
	}
}

func TestMergeEnvironmentOverridesValues(t *testing.T) {
	got := mergeEnvironment([]string{"PATH=/bin", "ROTARI_JOB_ID=old"}, []string{"ROTARI_JOB_ID=new", "ROTARI_TASK=value"})
	want := []string{"PATH=/bin", "ROTARI_JOB_ID=new", "ROTARI_TASK=value"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("mergeEnvironment = %#v, want %#v", got, want)
	}
}

func TestPrepareJobEnvironmentsIncludesRunOptions(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	if err := writeRunContext(paths, runID, "/work"); err != nil {
		t.Fatal(err)
	}
	jobs := []JobSpec{{ID: "job-1", Executor: "slurm"}}
	prepareJobEnvironments(paths, runID, jobs, "nightly", 2, 3, 4, []string{"-p short"})
	values := make(map[string]string)
	for _, entry := range jobs[0].Environment {
		parts := strings.SplitN(entry, "=", 2)
		values[parts[0]] = parts[1]
	}
	for name, want := range map[string]string{
		envRunName: "nightly", envRunLocalConc: "2", envRunBatchConc: "3", envRunRetry: "4", envExecutorOpts: "-p short",
	} {
		if values[name] != want {
			t.Errorf("%s = %q, want %q", name, values[name], want)
		}
	}
}

func TestEnqueueCommandPersistsStableJobID(t *testing.T) {
	baseDir := t.TempDir()
	message, err := enqueueCommand(baseDir, "default", []string{"echo", "old"}, "", nil, nil, "job", nil)
	if err != nil {
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
	if len(queue.Commands) != 1 || queue.Commands[0].ID == "" {
		t.Fatalf("queue command ID = %q, want a persisted ID", queue.Commands[0].ID)
	}
	id := queue.Commands[0].ID
	if !strings.Contains(message, "submitted project=default job_id="+id+" job_name=job command=[echo old]") {
		t.Fatalf("enqueue message = %q, want job metadata and command", message)
	}
	queue.Commands[0].Command = []string{"echo", "new"}
	jobs := queueToJobs(queue.Commands)
	if len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("changed command ID = %q, want %q", jobs[0].ID, id)
	}
}

func TestEnqueueCommandKeepsPerJobExecutorOutOfQueueDefault(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "job"}, "slurm", []string{"-p short"}, nil, "job", nil); err != nil {
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
	if queue.DefaultExecutor != "" || len(queue.Commands) != 1 || queue.Commands[0].Executor != "slurm" {
		t.Fatalf("queue = %#v, want only the job to use slurm", queue)
	}
}

func TestEnqueueCommandPersistsEnvironment(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "job"}, "", nil, []string{"TOKEN=secret", "MODE=test"}, "job", nil); err != nil {
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
	if got := strings.Join(queue.Commands[0].Environment, "\x00"); got != "TOKEN=secret\x00MODE=test" {
		t.Fatalf("environment = %q", got)
	}
	jobs := queueToJobs(queue.Commands)
	prepareJobEnvironments(paths, "run-1", jobs, "", 1, 1, 0, nil)
	values := make(map[string]string)
	for _, entry := range jobs[0].Environment {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	if values["TOKEN"] != "secret" || values["MODE"] != "test" {
		t.Fatalf("job environment = %#v", values)
	}
}

func TestValidateEnvironment(t *testing.T) {
	if err := validateEnvironment([]string{"KEY=value", "EMPTY="}); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironment([]string{"not-an-assignment"}); err == nil {
		t.Fatal("validateEnvironment accepted a missing equals sign")
	}
}

func TestAddRunArgsPreservesResolvedQueue(t *testing.T) {
	got := addRunArgs("/tmp/rotari state", "build queue")
	want := []string{"--basedir", "/tmp/rotari state", "--project-name", "build queue"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("addRunArgs() = %#v, want %#v", got, want)
	}
}

func TestEnqueueCommandKeepsFinishedRunHistory(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "new"}, "", nil, nil, "new-job", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-1", "summary.json")); err != nil {
		t.Fatalf("finished run history was removed: %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" || meta.LastRunID != "run-1" {
		t.Fatalf("meta = %#v, want collecting with run-1 history", meta)
	}
}

func TestAppendCompletionBlockIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".bashrc")
	if err := appendCompletionBlock(path, "bash", "eval completion"); err != nil {
		t.Fatal(err)
	}
	if err := appendCompletionBlock(path, "bash", "eval completion"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "# rotari completion (bash)"); count != 1 {
		t.Fatalf("completion marker count = %d, want 1", count)
	}
}

func TestZshArgumentsIncludeValueNames(t *testing.T) {
	got := zshArguments([]cliFlagSpec{{Name: "basedir", Description: "state directory", ValueName: "DIR"}})
	if got != "{-b,--basedir}'[state directory]:DIR:'" {
		t.Fatalf("zsh argument = %q, want value name in specification", got)
	}
}

func TestCLIShortOptions(t *testing.T) {
	fs := flag.NewFlagSet("short-options", flag.ContinueOnError)
	baseDir := cliString(fs, "basedir", "")
	projectName := cliString(fs, "project-name", "")
	runID := cliString(fs, "run-id", "")
	executor := cliString(fs, "executor", "")
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")

	if err := fs.Parse([]string{"-b", "/state", "-p", "build", "-r", "run-1", "-j", "job-1", "-j", "job-2", "-e", "local"}); err != nil {
		t.Fatal(err)
	}
	if *baseDir != "/state" || *projectName != "build" || *runID != "run-1" || *executor != "local" {
		t.Fatalf("short option values = %q, %q, %q, %q", *baseDir, *projectName, *runID, *executor)
	}
	if strings.Join(jobIDs, ",") != "job-1,job-2" {
		t.Fatalf("short job IDs = %q", jobIDs)
	}
}

func TestCompletionScriptsContainCommandOptions(t *testing.T) {
	for _, option := range []string{"--basedir", "-b", "--project-name", "-p", "--run-id", "-r", "--job-id", "-j", "--executor", "-e", "--failed-logs", "--no-pager", "--job-name"} {
		if !strings.Contains(generateBashCompletion(), option) {
			t.Errorf("Bash completion does not contain %s", option)
		}
		if !strings.Contains(generateZshCompletion(), option) {
			t.Errorf("Zsh completion does not contain %s", option)
		}
	}
	if !strings.Contains(generateBashCompletion(), "bash zsh install") {
		t.Error("Bash completion does not contain the install subcommand")
	}
	if !strings.Contains(generateZshCompletion(), "'install:install completion") {
		t.Error("Zsh completion does not contain the install subcommand")
	}
	if !strings.Contains(generateZshCompletion(), "compdef _rotari rotari") {
		t.Error("Zsh completion does not register rotari")
	}
	if !strings.Contains(generateBashCompletion(), "__complete project-name") {
		t.Error("Bash completion does not dynamically complete project names")
	}
	if !strings.Contains(generateZshCompletion(), "_rotari_project_names") {
		t.Error("Zsh completion does not dynamically complete project names")
	}
}

func TestCompleteProjectNames(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "projects", "z-last"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "projects", "a-first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "projects", "not-a-project"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdComplete([]string{"project-name", "--basedir", baseDir})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdComplete exit code = %d, want 0", code)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "a-first\nz-last\n" {
		t.Fatalf("project-name completion = %q, want sorted project names", output)
	}
}

func TestCompleteJobIDsForRun(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runID := "completion-run-1"
	for _, jobID := range []string{"job-b", "job-a"} {
		if err := os.MkdirAll(filepath.Join(paths.runsDir, runID, jobID), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "other-run", "job-other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registerRun(paths, runID); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdComplete([]string{"job-id", "--run-id", runID})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdComplete exit code = %d, want 0", code)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "job-a\njob-b\n" {
		t.Fatalf("job-id completion = %q, want only jobs from %s", output, runID)
	}
}

func TestShouldFollowLogs(t *testing.T) {
	if !shouldFollowLogs(true, true, true) {
		t.Fatal("explicit follow should be enabled")
	}
	if !shouldFollowLogs(false, true, true) {
		t.Fatal("single-job auto follow should activate for running jobs on TTY")
	}
	if shouldFollowLogs(false, false, true) {
		t.Fatal("auto follow should be disabled when the job is no longer running")
	}
	if shouldFollowLogs(false, true, false) {
		t.Fatal("auto follow should be disabled for non-TTY output")
	}
}

func TestShowWithPagerDisabledWritesDirectly(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()

	if code := showWithPager(false, func(writer io.Writer) int {
		_, _ = fmt.Fprint(writer, "log output")
		return 0
	}); code != 0 {
		t.Fatalf("showWithPager exit code = %d, want 0", code)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "log output" {
		t.Fatalf("output = %q, want log output", output)
	}
}

func TestShowRunIncludesCarriedJobFromCommands(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-2")
	queue := Queue{Commands: []QueuedCommand{{
		ID: "carried", Name: "carried-job", Command: []string{"echo", "done"},
		Origin: &JobOrigin{RunID: "run-1", JobID: "carried", Status: "success"},
	}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{RunID: "run-2", Status: "finished", Results: []JobResult{{ID: "carried", ExitCode: 0}}}); err != nil {
		t.Fatal(err)
	}
	sourceJobDir := filepath.Join(paths.runsDir, "run-1", "carried")
	if err := os.MkdirAll(sourceJobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceJobDir, "submitted_at"), []byte("2026-09-16T00:00:01Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceJobDir, "finished_at"), []byte("2026-09-16T00:00:02Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := showRun(paths, "run-2", false)
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("showRun exit code = %d, want 0", code)
	}
	for _, want := range []string{"carried", "carried-job", "2026-09-16 09:00:01 JST", "2026-09-16 09:00:02 JST", "echo done"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("showRun output does not contain %q:\n%s", want, output)
		}
	}
	if !strings.Contains(string(output), "rotari delete --run-id run-2") {
		t.Fatalf("showRun output does not contain short delete command:\n%s", output)
	}
	if strings.Contains(string(output), "rotari delete --basedir") {
		t.Fatalf("showRun output contains verbose delete command:\n%s", output)
	}
}

func TestPrintChangeHintsUsesRetryLabel(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	printChangeHints(pathSet{baseDir: "/tmp/rotari", queueName: "demo"}, "run-1", Queue{}, []JobSpec{{ID: "job-1"}})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	if !strings.Contains(text, "Retry:") || strings.Contains(text, "Rerun:") {
		t.Fatalf("change hints have incorrect retry label:\n%s", text)
	}
}

func TestExceedsPagerLineLimit(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		output   string
		newlines int
		want     bool
	}{
		{name: "24 complete lines", output: strings.Repeat("line\n", pagerLineLimit), newlines: pagerLineLimit},
		{name: "25 complete lines", output: strings.Repeat("line\n", pagerLineLimit+1), newlines: pagerLineLimit + 1, want: true},
		{name: "25th partial line", output: strings.Repeat("line\n", pagerLineLimit) + "line", newlines: pagerLineLimit, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := exceedsPagerLineLimit([]byte(testCase.output), testCase.newlines); got != testCase.want {
				t.Fatalf("exceedsPagerLineLimit() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestInstallCompletionForBash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := installCompletion("bash"); err != nil {
		t.Fatal(err)
	}
	if err := installCompletion("bash"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Count(content, "# rotari completion (bash)") != 1 {
		t.Fatal("Bash completion block was installed more than once")
	}
	if !strings.Contains(content, `eval "$(rotari completion bash)"`) {
		t.Fatal("Bash completion command is missing")
	}
}

func TestInstallCompletionForZsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := installCompletion("zsh"); err != nil {
		t.Fatal(err)
	}
	if err := installCompletion("zsh"); err != nil {
		t.Fatal(err)
	}

	completion, err := os.ReadFile(filepath.Join(home, ".zfunc", "_rotari"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(completion), "#compdef rotari") {
		t.Fatal("Zsh completion header is missing")
	}
	if err := os.WriteFile(filepath.Join(home, ".zfunc", "_rotari"), []byte("stale zsh completion"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installCompletion("zsh"); err != nil {
		t.Fatal(err)
	}
	completion, err = os.ReadFile(filepath.Join(home, ".zfunc", "_rotari"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(completion), "#compdef rotari") {
		t.Fatal("Zsh completion was not refreshed after stale install")
	}
	rc, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(rc), "# rotari completion (zsh)") != 1 {
		t.Fatal("Zsh completion block was installed more than once")
	}
	if !strings.Contains(string(rc), "autoload -Uz _rotari && compdef _rotari rotari") {
		t.Fatal("Zsh completion function was not registered")
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# rotari completion (zsh)\nfpath=(\"$HOME/.zfunc\" $fpath)\nautoload -Uz compinit && compinit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installCompletion("zsh"); err != nil {
		t.Fatal(err)
	}
	rc, err = os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rc), "autoload -Uz _rotari && compdef _rotari rotari") {
		t.Fatal("Existing Zsh completion block was not upgraded")
	}
}

func TestPlanRerunSelectionWithoutPreviousRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.metaFile), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = planRerunSelection(paths, Queue{Commands: []QueuedCommand{{ID: "alpha"}}}, "failed", nil, "", true)
	if !errors.Is(err, errNoPreviousRun) {
		t.Fatalf("error = %v, want errNoPreviousRun", err)
	}
}

func TestPlanRerunSelectionCarriesForwardNonMatchingResults(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "alpha", Command: []string{"echo", "alpha"}, Name: "alpha"},
		{ID: "beta", Command: []string{"echo", "beta"}, Name: "beta"},
		{ID: "gamma", Command: []string{"echo", "gamma"}, Name: "gamma"},
		{ID: "delta", Command: []string{"echo", "delta"}, Name: "delta"},
	}}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1",
		Results: []JobResult{
			{ID: "alpha", ExitCode: 0},
			{ID: "beta", ExitCode: 1},
			{ID: "gamma", ExitCode: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	alphaDir := filepath.Join(paths.runsDir, "run-1", "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "submitted_at"), []byte("2026-09-16T00:00:01Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "finished_at"), []byte("2026-09-16T00:00:02Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	plan, err := planRerunSelection(paths, queue, "failed", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Execute["beta"] || plan.Execute["alpha"] || plan.Execute["gamma"] || plan.Execute["delta"] {
		t.Fatalf("execute set = %#v, want only beta", plan.Execute)
	}
	if _, ok := plan.CarriedResults["alpha"]; !ok {
		t.Fatal("expected alpha to be carried forward")
	}
	if _, ok := plan.CarriedResults["gamma"]; !ok {
		t.Fatal("expected gamma to be carried forward")
	}
	if _, ok := plan.CarriedResults["delta"]; ok {
		t.Fatal("delta has no previous result and must not be carried forward")
	}
	origin := plan.CarriedOrigins["alpha"]
	if origin == nil || origin.RunID != "run-1" || origin.JobID != "alpha" || origin.Status != "success" || origin.SubmittedAt != "2026-09-16T00:00:01Z" || origin.FinishedAt != "2026-09-16T00:00:02Z" {
		t.Fatalf("origin = %#v, want run-1/alpha success with timestamps", origin)
	}
}

func TestPlanRerunSelectionAggregatesArrayTaskResults(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "array", Command: []string{"echo", "array"}, Name: "array", Array: &ArraySpec{First: 1, Last: 2}},
	}}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1",
		Results: []JobResult{
			{ID: "array-1", ExitCode: 0},
			{ID: "array-2", ExitCode: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	// A fully successful array job must not be re-executed by --unfinished,
	// with or without partialArray (both tasks already match "finished").
	plan, err := planRerunSelection(paths, queue, "unfinished", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Execute["array"] {
		t.Fatalf("execute set = %#v, want array carried forward, not re-executed", plan.Execute)
	}
	if _, ok := plan.CarriedResults["array-1"]; !ok {
		t.Fatal("expected array-1 to be carried forward")
	}
	if _, ok := plan.CarriedResults["array-2"]; !ok {
		t.Fatal("expected array-2 to be carried forward")
	}
	if origin := plan.CarriedOrigins["array"]; origin == nil || origin.Status != "success" {
		t.Fatalf("origin = %#v, want status=success", origin)
	}

	// With partialArray disabled, a single failed task still re-executes
	// the whole array as one unit (the pre-partial-array behavior).
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1",
		Results: []JobResult{
			{ID: "array-1", ExitCode: 0},
			{ID: "array-2", ExitCode: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err = planRerunSelection(paths, queue, "failed", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Execute["array"] {
		t.Fatal("expected array with a failed task to be re-executed as a whole when partialArray is false")
	}
}

func TestPlanRerunSelectionPartialArrayReexecutesOnlyFailedTasks(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "array", Command: []string{"echo", "array"}, Name: "array", Array: &ArraySpec{First: 1, Last: 2}},
	}}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1",
		Results: []JobResult{
			{ID: "array-1", ExitCode: 0},
			{ID: "array-2", ExitCode: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	// partialArray=true (the default): only the failed task re-executes,
	// the successful one carries its previous result forward instead.
	plan, err := planRerunSelection(paths, queue, "failed", nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Execute["array"] {
		t.Fatal("did not expect the whole array command ID to be marked for execution")
	}
	if plan.Execute["array-1"] {
		t.Fatal("array-1 already succeeded and must not be re-executed")
	}
	if !plan.Execute["array-2"] {
		t.Fatal("array-2 failed and must be re-executed")
	}
	if result, ok := plan.CarriedResults["array-1"]; !ok || result.ExitCode != 0 {
		t.Fatalf("array-1 carried result = %#v, want carried success", result)
	}
	if _, ok := plan.CarriedResults["array-2"]; ok {
		t.Fatal("array-2 is being re-executed and must not also be carried forward")
	}
}

func TestDeleteRemovesOnlySelectedRun(t *testing.T) {
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		if err := os.Mkdir(filepath.Join(paths.runsDir, runID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := registerRun(paths, runID); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: "run-2", LastRunExitCode: 1}); err != nil {
		t.Fatal(err)
	}

	if code := cmdDelete([]string{"--basedir", baseDir, "--run-id", "run-2"}); code != 0 {
		t.Fatalf("cmdDelete exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-1")); err != nil {
		t.Fatalf("run-1 was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-2")); !os.IsNotExist(err) {
		t.Fatalf("run-2 still exists, stat error = %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.LastRunID != "run-1" || meta.LastRunExitCode != 1 || meta.Phase != "collecting" {
		t.Fatalf("metadata = %#v, want latest remaining run-1", meta)
	}
	if _, found, err := resolveRunLocation("run-2"); err != nil || found {
		t.Fatalf("deleted run registry entry: found=%v, err=%v; want removed", found, err)
	}
	if _, found, err := resolveRunLocation("run-1"); err != nil || !found {
		t.Fatalf("remaining run registry entry: found=%v, err=%v; want retained", found, err)
	}
}

func TestClearCommandIsRejected(t *testing.T) {
	if code := run([]string{"clear", "--basedir", t.TempDir()}); code != 1 {
		t.Fatalf("run clear exit = %d, want 1", code)
	}
}

func TestPlanRerunSelectionByJobID(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "prepare", Command: []string{"echo", "prepare"}, Name: "prepare"},
		{ID: "alpha", Command: []string{"echo", "alpha"}, Name: "alpha", DependsOn: []string{"prepare"}},
		{ID: "beta", Command: []string{"echo", "beta"}, Name: "beta"},
	}}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	plan, err := planRerunSelection(paths, queue, "job-id", []string{"beta", "alpha"}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Execute["alpha"] || !plan.Execute["beta"] || plan.Execute["prepare"] {
		t.Fatalf("execute set = %#v, want alpha and beta only", plan.Execute)
	}
	if len(plan.CarriedResults) != 0 {
		t.Fatalf("carried results = %#v, want none (prepare has no previous result)", plan.CarriedResults)
	}
}

func TestCompareQueueWithRun(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(dir, "queue.json")
	runPath := filepath.Join(dir, "commands.json")
	if err := writeJSON(queuePath, Queue{Commands: []QueuedCommand{
		{ID: "same", Command: []string{"echo", "same"}, Name: "same"},
		{ID: "changed", Command: []string{"echo", "changed"}, Name: "new-name"},
		{ID: "added", Command: []string{"echo", "added"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(runPath, Queue{Commands: []QueuedCommand{
		{ID: "same", Command: []string{"echo", "same"}, Name: "same"},
		{ID: "changed", Command: []string{"echo", "changed"}, Name: "old-name"},
		{ID: "removed", Command: []string{"echo", "removed"}},
	}}); err != nil {
		t.Fatal(err)
	}

	diff, err := compareQueueWithRun(queuePath, runPath)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Added != 1 || diff.Removed != 1 || diff.Changed != 1 {
		t.Fatalf("diff = %#v, want added=1 removed=1 changed=1", diff)
	}
}

func TestResolveQueueExecutorUsesDefaultExecutor(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "projects", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{
		DefaultExecutor: "slurm",
		Commands:        []QueuedCommand{{ID: "hello", Command: []string{"echo", "hello"}}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := resolveQueueExecutor(baseDir, "default", "")
	if err != nil {
		t.Fatalf("resolveQueueExecutor returned error: %v", err)
	}
	if got != "slurm" {
		t.Fatalf("resolved executor = %q, want slurm", got)
	}

	if _, err := resolveQueueExecutor(baseDir, "default", "invalid"); err == nil {
		t.Fatal("resolveQueueExecutor accepted unsupported executor")
	}
}

func TestRemoveFinishedJobsDropsCompletedResults(t *testing.T) {
	jobs := []JobSpec{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	remaining := removeFinishedJobs(jobs, map[string]JobResult{"b": {ID: "b", ExitCode: 0}})
	if len(remaining) != 2 {
		t.Fatalf("remaining jobs = %d, want 2", len(remaining))
	}
	ids := map[string]bool{}
	for _, job := range remaining {
		ids[job.ID] = true
	}
	if ids["b"] {
		t.Fatal("completed job was not removed from remaining list")
	}
	for _, want := range []string{"a", "c"} {
		if !ids[want] {
			t.Fatalf("missing job %q in remaining list: %#v", want, remaining)
		}
	}
}

func TestExecuteMixedRunRetriesFailedJob(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(baseDir, "retry-marker")
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID:      "retry",
		Command: []string{"/bin/sh", "-c", fmt.Sprintf("if [ -f %q ]; then exit 0; else touch %q; exit 1; fi", marker, marker)},
	}}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "retry-run", "", 1, 1, 1, "", nil, "", nil, "", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("retry marker was not created: %v", err)
	}

	summaryPath := filepath.Join(paths.runsDir, "retry-run", "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary RunSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 1 || summary.Results[0].ExitCode != 0 {
		t.Fatalf("summary = %#v, want single successful result", summary.Results)
	}
}

type recordingExecutor struct {
	name      string
	submitted []string
}

func (executor *recordingExecutor) Name() string { return executor.name }

func (executor *recordingExecutor) Submit(_ string, job JobSpec, _ []string) (JobHandle, error) {
	executor.submitted = append(executor.submitted, job.ID)
	return JobHandle{Job: job}, nil
}

func (executor *recordingExecutor) Wait(_ string, handle JobHandle) JobResult {
	return JobResult{ID: handle.Job.ID, Command: handle.Job.Command, ExitCode: 0}
}

func TestExecuteMixedRunKeepsPerJobExecutorOverrides(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scheduler := &recordingExecutor{name: "slurm"}
	previous, existed := executorRegistry["slurm"]
	executorRegistry["slurm"] = scheduler
	t.Cleanup(func() {
		if existed {
			executorRegistry["slurm"] = previous
		} else {
			delete(executorRegistry, "slurm")
		}
	})
	queue := Queue{Commands: []QueuedCommand{
		{ID: "local-job", Command: []string{"sh", "-c", "exit 0"}},
		{ID: "slurm-job", Command: []string{"echo", "scheduler"}, Executor: "slurm"},
	}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if code := executeMixedRun(paths, "mixed-run", "", 1, 1, 0, "", nil, "", nil, "", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	if len(scheduler.submitted) != 1 || scheduler.submitted[0] != "slurm-job" {
		t.Fatalf("scheduler submissions = %#v, want only slurm-job", scheduler.submitted)
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, "mixed-run", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("summary results = %#v, want local and slurm jobs", summary.Results)
	}
}

func TestExecuteMixedRunExecutesAllArrayTasks(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID: "array", Command: []string{"sh", "-c", "exit 0"}, Array: &ArraySpec{First: 1, Last: 2},
	}}}); err != nil {
		t.Fatal(err)
	}
	if code := executeMixedRun(paths, "array-run", "", 2, 1, 0, "", nil, "", nil, "", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, "array-run", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 2 || summary.Results[0].ID == "array" || summary.Results[1].ID == "array" {
		t.Fatalf("summary results = %#v, want two array tasks", summary.Results)
	}
}

func TestExecuteMixedRunPartialArrayReexecutesOnlyFailedTask(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{
		ID: "array", Command: []string{"sh", "-c", "exit 0"}, Array: &ArraySpec{First: 1, Last: 2},
	}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1",
		Results: []JobResult{
			{ID: "array-1", ExitCode: 1},
			{ID: "array-2", ExitCode: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "run-2", "", 1, 1, 0, "", nil, "failed", nil, "run-1", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-2", "array-1")); err != nil {
		t.Fatalf("array-1 was not re-executed in run-2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-2", "array-2")); !os.IsNotExist(err) {
		t.Fatalf("array-2 should not have been re-executed, stat error = %v", err)
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, "run-2", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		results[result.ID] = result
	}
	if result, ok := results["array-1"]; !ok || result.ExitCode != 0 {
		t.Fatalf("array-1 result = %#v, want re-executed with exit 0", result)
	}
	if result, ok := results["array-2"]; !ok || result.ExitCode != 0 {
		t.Fatalf("array-2 result = %#v, want carried forward with exit 0", result)
	}

	// The carried task's origin must point at its own task ID in the
	// reference run, not the array's base command ID, so show/web can
	// follow it back to find the original output.
	newQueue, err := loadQueue(filepath.Join(paths.runsDir, "run-2", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	origin := newQueue.Commands[0].TaskOrigins["array-2"]
	if origin == nil || origin.RunID != "run-1" || origin.JobID != "array-2" || origin.Status != "success" {
		t.Fatalf("array-2 TaskOrigins = %#v, want run-1/array-2 success", origin)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1", "array-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.runsDir, "run-1", "array-2", "output"), []byte("carried output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if code := showJob(&buffer, paths, "run-2", "array-2"); code != 0 {
		t.Fatalf("showJob exit = %d, want 0", code)
	}
	if !strings.Contains(buffer.String(), "carried forward from run run-1") {
		t.Fatalf("showJob output = %q, want carried-forward note", buffer.String())
	}
}

func TestExecuteMixedRunPersistsRunName(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID: "named-job", Command: []string{"sh", "-c", "exit 0"}, Name: "named-job",
	}}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "named-run", "nightly-build", 1, 1, 0, "", nil, "", nil, "", true, nil, nil); code != 0 {
		t.Fatalf("executeMixedRun exit = %d, want 0", code)
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, "named-run", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunName != "nightly-build" {
		t.Fatalf("summary run name = %q, want nightly-build", summary.RunName)
	}
	if got := formatRunLabel(summary.RunID, summary.RunName); got != "nightly-build (named-run)" {
		t.Fatalf("run label = %q, want named-run with display name", got)
	}
}

func TestFormatRunCompletionIncludesRunNameAndFailedJobHint(t *testing.T) {
	paths, err := resolvePaths(t.TempDir(), "build")
	if err != nil {
		t.Fatal(err)
	}
	message := formatRunCompletion(paths, "run-1", RunSummary{
		RunID: "run-1", RunName: "nightly", Status: "failed", ExitCode: 1,
		Results: []JobResult{{ID: "job-1", ExitCode: 1, Hosts: []string{"compute-01"}}},
	})
	for _, want := range []string{"nightly (run-1)", "Failed: 1", "Hosts: compute-01", "rotari show", "rotari retry"} {
		if !strings.Contains(message, want) {
			t.Errorf("completion message missing %q: %s", want, message)
		}
	}
}

func TestCmdWaitRejectsNegativeTimeout(t *testing.T) {
	if code := cmdWait([]string{"--run-id", "run-1", "--timeout", "-1s"}); code != 1 {
		t.Fatalf("cmdWait exit = %d, want 1", code)
	}
}

func TestCmdCancelRejectsWaitWithJobID(t *testing.T) {
	if code := cmdCancel([]string{"--basedir", t.TempDir(), "--job-id", "job-1", "--wait"}); code != 1 {
		t.Fatalf("cmdCancel exit = %d, want 1", code)
	}
}

func TestFollowJobLogReadsAppendedOutputUntilFinished(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(paths.runsDir, "run-1", "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(jobDir, "output")
	if err := os.WriteFile(outputPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(outputPath, []byte("first\nsecond\n"), 0o644)
		_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte("0\n"), 0o644)
	}()

	var output bytes.Buffer
	if code := followJobLog(&output, paths, "run-1", "job-1"); code != 0 {
		t.Fatalf("followJobLog exit = %d, want 0", code)
	}
	if got := output.String(); got != "first\nsecond\n" {
		t.Fatalf("followed output = %q, want appended log", got)
	}
}

func TestFinishRunClearsQueueAndKeepsRunHistory(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	queue := Queue{DefaultExecutor: "slurm", Commands: []QueuedCommand{{ID: "queued", Command: []string{"echo", "queued"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, defaultMeta()); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{RunID: "run-1", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	if err := finishRun(paths, "run-1", 1); err != nil {
		t.Fatal(err)
	}

	gotQueue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotQueue.Commands) != 0 {
		t.Fatalf("queue commands = %d, want 0", len(gotQueue.Commands))
	}
	if gotQueue.DefaultExecutor != "slurm" {
		t.Fatalf("queue default executor = %q, want slurm", gotQueue.DefaultExecutor)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-1", "commands.json")); err != nil {
		t.Fatalf("run snapshot was removed: %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "finished" || meta.LastRunID != "run-1" || meta.LastRunExitCode != 1 {
		t.Fatalf("metadata = %#v, want finished run-1 exit 1", meta)
	}
}

func TestFinishRunDoesNotFinalizeAnotherRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{{ID: "queued", Command: []string{"echo", "queued"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "run-2"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{RunID: "run-2", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	if err := finishRun(paths, "run-1", 1); err == nil {
		t.Fatal("finishRun finalized a different run")
	}

	gotQueue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotQueue.Commands) != 1 {
		t.Fatalf("queue commands = %d, want 1", len(gotQueue.Commands))
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.LastRunID != "run-2" || meta.Phase != "running" {
		t.Fatalf("metadata = %#v, want active run-2", meta)
	}
}

func TestChangeBatchRestoresAndEditsPreviousRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := Queue{Commands: []QueuedCommand{
		{ID: "prepare-id", Command: []string{"echo", "prepare"}, Name: "prepare"},
		{ID: "train-id", Command: []string{"echo", "train"}, Name: "train", DependsOn: []string{"prepare"}},
	}}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	message, err := changeBatch(baseDir, "default", "", "train-id", "", "slurm",
		[]string{"-p gpu"}, false, nil, false, "", []string{"prepare"}, false, []string{"./train-v2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "job=train-id") {
		t.Fatalf("change message = %q, want train-id", message)
	}
	changed, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Commands[1].ID != "train-id" || changed.Commands[1].Executor != "slurm" ||
		changed.Commands[1].Command[0] != "./train-v2" || len(changed.Commands[1].ExecutorOptions) != 1 {
		t.Fatalf("changed queue = %#v", changed)
	}
	original, err := loadQueue(filepath.Join(paths.runsDir, "run-1", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	if original.Commands[1].Command[0] != "echo" || original.Commands[1].Executor != "" {
		t.Fatalf("snapshot was modified: %#v", original.Commands[1])
	}
}

func TestRemoveBatchRemovesJobsAndRejectsDependencies(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "prepare-id", Command: []string{"echo", "prepare"}, Name: "prepare"},
		{ID: "train-id", Command: []string{"echo", "train"}, Name: "train", DependsOn: []string{"prepare"}},
		{ID: "other-id", Command: []string{"echo", "other"}, Name: "other"},
	}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, defaultMeta()); err != nil {
		t.Fatal(err)
	}

	if _, err := removeBatch(baseDir, "default", "", []string{"other-id"}, ""); err != nil {
		t.Fatal(err)
	}
	remaining, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Commands) != 2 || remaining.Commands[0].ID != "prepare-id" || remaining.Commands[1].ID != "train-id" {
		t.Fatalf("remaining queue = %#v", remaining.Commands)
	}

	if _, err := removeBatch(baseDir, "default", "", nil, "prepare"); err == nil {
		t.Fatal("removing a job referenced by a dependency succeeded")
	}
	remaining, err = loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Commands) != 2 {
		t.Fatalf("queue changed after rejected removal: %#v", remaining.Commands)
	}
}

func TestRemoveBatchRestoresPreviousRun(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Queue{Commands: []QueuedCommand{
		{ID: "one-id", Command: []string{"echo", "one"}, Name: "one"},
		{ID: "two-id", Command: []string{"echo", "two"}, Name: "two"},
	}}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "commands.json"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "finished", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}

	message, err := removeBatch(baseDir, "default", "", []string{"one-id"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "removed 1 job") {
		t.Fatalf("remove message = %q", message)
	}
	remaining, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.Commands) != 1 || remaining.Commands[0].ID != "two-id" {
		t.Fatalf("restored queue = %#v", remaining.Commands)
	}
}

func TestExecuteMixedRunBlocksWhenDependencyFails(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{
		{ID: "job1-id", Command: []string{"/bin/sh", "-c", "exit 1"}, Name: "job1"},
		{ID: "job2-id", Command: []string{"/bin/sh", "-c", "echo ok"}, Name: "job2", DependsOn: []string{"job1"}},
	}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "blocked-run", "", 1, 1, 0, "", nil, "", nil, "", true, nil, nil); code != 1 {
		t.Fatalf("executeMixedRun exit = %d, want 1", code)
	}

	data, err := os.ReadFile(filepath.Join(paths.runsDir, "blocked-run", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary RunSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Results) != 2 {
		t.Fatalf("summary len = %d, want 2", len(summary.Results))
	}
	if summary.Results[0].ExitCode != 1 || summary.Results[1].ExitCode != 1 {
		t.Fatalf("results = %#v, want both jobs to fail", summary.Results)
	}
	if summary.Results[1].Error != "blocked by failed dependency" {
		t.Fatalf("job2 error = %q, want blocked by failed dependency", summary.Results[1].Error)
	}
}

func TestExecuteMixedRunCarriesForwardNonSelectedResults(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	queue := Queue{Commands: []QueuedCommand{
		{ID: "alpha", Command: []string{"/bin/sh", "-c", "exit 0"}, Name: "alpha"},
		{ID: "beta", Command: []string{"/bin/sh", "-c", "exit 1"}, Name: "beta"},
	}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if code := executeMixedRun(paths, "run-1", "", 1, 1, 0, "", nil, "", nil, "", true, nil, nil); code != 1 {
		t.Fatalf("first run exit = %d, want 1", code)
	}
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if code := executeMixedRun(paths, "run-2", "", 1, 1, 0, "", nil, "failed", nil, "", true, nil, nil); code != 1 {
		t.Fatalf("second run exit = %d, want 1 (beta still fails)", code)
	}

	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-2", "alpha")); !os.IsNotExist(err) {
		t.Fatalf("alpha should not have been re-executed, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.runsDir, "run-2", "beta")); err != nil {
		t.Fatalf("beta should have been re-executed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(paths.runsDir, "run-2", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary RunSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	results := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		results[result.ID] = result
	}
	if results["alpha"].ExitCode != 0 || results["beta"].ExitCode != 1 {
		t.Fatalf("summary results = %#v, want alpha carried success and beta re-run failed", results)
	}

	commandsData, err := os.ReadFile(filepath.Join(paths.runsDir, "run-2", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var commands Queue
	if err := json.Unmarshal(commandsData, &commands); err != nil {
		t.Fatal(err)
	}
	var alphaOrigin *JobOrigin
	for _, command := range commands.Commands {
		if command.ID == "alpha" {
			alphaOrigin = command.Origin
		}
	}
	if alphaOrigin == nil || alphaOrigin.RunID != "run-1" || alphaOrigin.JobID != "alpha" || alphaOrigin.Status != "success" {
		t.Fatalf("alpha origin = %#v, want run-1/alpha success", alphaOrigin)
	}
}

func TestValidateDependencies(t *testing.T) {
	valid := []JobSpec{
		{Name: "job1", Command: []string{"echo", "1"}},
		{Name: "job2", Command: []string{"echo", "2"}, DependsOn: []string{"job1"}},
	}
	if err := validateDependencies(valid); err != nil {
		t.Fatalf("valid dependencies returned error: %v", err)
	}
	cases := []struct {
		name string
		jobs []JobSpec
	}{
		{
			name: "unknown dependency",
			jobs: []JobSpec{{Name: "job2", DependsOn: []string{"missing"}}},
		},
		{
			name: "duplicate name",
			jobs: []JobSpec{{Name: "same"}, {Name: "same"}},
		},
		{
			name: "cycle",
			jobs: []JobSpec{
				{Name: "job1", DependsOn: []string{"job2"}},
				{Name: "job2", DependsOn: []string{"job1"}},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateDependencies(testCase.jobs); err == nil {
				t.Fatal("validateDependencies returned nil")
			}
		})
	}
}

func TestFinishCancelMessageWaitsUntilLockDisappears(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}
	if err := writeJSON(paths.lockFile, lock); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(paths.lockFile)
	}()

	message, err := finishCancelMessage("Cancel requested", paths, "default", "run-1", true)
	if err != nil {
		t.Fatalf("finishCancelMessage returned error: %v", err)
	}
	if !strings.Contains(message, "Cancellation complete") {
		t.Fatalf("message = %q, want cancellation complete", message)
	}
}

func TestFinalizeCompletedCancellationRemovesStaleServerLock(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "cancelling", LastRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"echo", "stale"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{
		RunID: "run-1", Status: "failed", FinishedAt: nowRFC3339(), ExitCode: 143,
	}); err != nil {
		t.Fatal(err)
	}

	finalized, err := finalizeCompletedCancellation(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !finalized {
		t.Fatal("finalizeCompletedCancellation returned false")
	}
	if _, err := os.Stat(paths.lockFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still exists, stat error = %v", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "finished" {
		t.Fatalf("meta phase = %q, want finished", meta.Phase)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 0 {
		t.Fatalf("queue commands = %d, want 0", len(queue.Commands))
	}
}

func TestCancelJobsCancelsSelectedLocalJob(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run-1")
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
	// runOneJob starts jobs as their own process group leader (Setpgid) so
	// signal(-pid) reaches the wrapped command too; mirror that here.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if err := os.WriteFile(filepath.Join(jobDir, "pid"), fmt.Appendf(nil, "%d\n", child.Process.Pid), 0o644); err != nil {
		t.Fatal(err)
	}

	message, err := cancelJobs(runDir, "default", "run-1", []string{"job-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "Jobs: 1") {
		t.Fatalf("message = %q, want one cancelled job", message)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("cancelled process exited successfully")
	}
}

func TestControlQueueJobsSuspendsAndResumesSelectedLocalJob(t *testing.T) {
	baseDir := t.TempDir()
	outputPath := filepath.Join(baseDir, "progress")
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sh", "-c", fmt.Sprintf("while :; do printf x >> %q; done", outputPath))
	// runOneJob starts jobs as their own process group leader (Setpgid) so
	// signal(-pid) reaches the wrapped command too; mirror that here.
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if err := os.WriteFile(filepath.Join(jobDir, "pid"), fmt.Appendf(nil, "%d\n", child.Process.Pid), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFileSize(t, outputPath, 1)

	if _, err := controlQueueJobs(baseDir, "default", []string{"job-1"}, "suspend"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	suspendedSize := len(data)
	time.Sleep(100 * time.Millisecond)
	data, err = os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != suspendedSize {
		t.Fatalf("suspended process continued writing: size changed from %d to %d", suspendedSize, len(data))
	}
	if _, err := controlQueueJobs(baseDir, "default", []string{"job-1"}, "resume"); err != nil {
		t.Fatal(err)
	}
	waitForFileSize(t, outputPath, suspendedSize+1)
}

func TestControlQueueJobsReportsHostMismatchForLocalJob(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "context.json"), RunContext{Hostname: "other-host"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "pid"), fmt.Appendf(nil, "%d\n", os.Getpid()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = controlQueueJobs(baseDir, "default", []string{"job-1"}, "suspend")
	if err == nil {
		t.Fatal("suspend across hosts unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "other-host") {
		t.Fatalf("error = %q, want it to mention the recorded host", err)
	}
	if _, err := cancelJobs(runDir, "default", "run-1", []string{"job-1"}); err == nil {
		t.Fatal("cancel across hosts unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), "other-host") {
		t.Fatalf("error = %q, want it to mention the recorded host", err)
	}
}

func TestCancelQueueRejectsWholeRunFromWrongHost(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	// A PID that isn't this test process's own, recorded as owned by
	// another host: the pre-fix code would signal -pid locally, get ESRCH,
	// swallow it, and report success without cancelling anything remote.
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid() + 1, RunID: "run-1", StartedAt: nowRFC3339(), Host: "other-host"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = cancelQueue(baseDir, "default", false)
	if err == nil {
		t.Fatal("cancel of a whole run on another host unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "other-host") {
		t.Fatalf("error = %q, want it to mention the recorded host", err)
	}
}

func waitForFileSize(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("file %s did not reach size %d", path, want)
}

func TestControlQueueJobsControlsAllRunningJobsAndSkipsFinishedJobs(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: "run-1", StartedAt: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}
	for _, jobID := range []string{"running-1", "running-2", "finished"} {
		if err := os.MkdirAll(filepath.Join(paths.runsDir, "run-1", jobID), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(paths.runsDir, "run-1", "finished", "finished_at"), []byte(nowRFC3339()), 0o644); err != nil {
		t.Fatal(err)
	}
	children := make([]*exec.Cmd, 0, 2)
	for _, jobID := range []string{"running-1", "running-2"} {
		child := exec.Command("sleep", "30")
		// runOneJob starts jobs as their own process group leader (Setpgid) so
		// signal(-pid) reaches the wrapped command too; mirror that here.
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
		jobDir := filepath.Join(paths.runsDir, "run-1", jobID)
		if err := os.WriteFile(filepath.Join(jobDir, "pid"), fmt.Appendf(nil, "%d\n", child.Process.Pid), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, child := range children {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})

	message, err := controlQueueJobs(baseDir, "default", nil, "suspend")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "Jobs: 2") {
		t.Fatalf("message = %q, want two controlled jobs", message)
	}
}

func TestControlQueueJobsRejectsInvalidOrUnavailableRequests(t *testing.T) {
	if _, err := controlQueueJobs(t.TempDir(), "default", nil, "pause"); err == nil {
		t.Fatal("unsupported operation succeeded")
	}
	baseDir := t.TempDir()
	if _, err := controlQueueJobs(baseDir, "default", nil, "suspend"); err == nil {
		t.Fatal("suspend without a running queue succeeded")
	}
}

func TestRunOneJobSkipsCancelledPendingJob(t *testing.T) {
	runDir := t.TempDir()
	job := JobSpec{ID: "job-1", Command: []string{"sh", "-c", "exit 0"}}
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "cancelled"), []byte("requested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runOneJob(runDir, job)
	if result.ExitCode != 143 || result.Error != "cancelled before start" {
		t.Fatalf("result = %+v, want cancelled result", result)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file exists or stat failed: %v", err)
	}
	if output, err := os.ReadFile(filepath.Join(jobDir, "output")); err != nil || !strings.Contains(string(output), "cancelled before start") {
		t.Fatalf("output = %q, err = %v", output, err)
	}
}

func TestServerBeginAndEndRunTracksActiveState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	server.beginRun()
	if server.activeRuns != 1 {
		t.Fatalf("activeRuns = %d, want 1", server.activeRuns)
	}
	server.endRun()
	if server.activeRuns != 0 {
		t.Fatalf("activeRuns = %d, want 0", server.activeRuns)
	}
	select {
	case <-server.stopped:
	default:
		t.Fatal("server.stop was not triggered when activeRuns reached 0")
	}
}

func TestFinishCancelMessageIncludesInspectHintWhenNotWaiting(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	message, err := finishCancelMessage("Cancel requested", paths, "default", "run-1", false)
	if err != nil {
		t.Fatalf("finishCancelMessage returned error: %v", err)
	}
	if !strings.Contains(message, "Inspect status") {
		t.Fatalf("message = %q, want inspect status hint", message)
	}
	if !strings.Contains(message, "rotari show --run-id run-1") {
		t.Fatalf("message = %q, want show command hint", message)
	}
}

func TestFjobServerBusyStateTracksRunBoundary(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	if server.isBusy() {
		t.Fatal("server should be idle before any run begins")
	}
	server.beginRun()
	if !server.isBusy() {
		t.Fatal("server should report busy while a run is active")
	}
	server.endRun()
	if server.isBusy() {
		t.Fatal("server should not report busy after all runs finish")
	}
}

func TestSendRunRequestReadsProgressThenFinalResponse(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", serverSocketPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(serverSocketPath(baseDir))

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request serverRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.Op != "run" {
			t.Errorf("request op = %q, want run", request.Op)
		}
		encoder := json.NewEncoder(conn)
		if err := encoder.Encode(serverResponse{Progress: true, Completed: 1, Total: 2, Succeeded: 1, Failed: 0, Message: "progress"}); err != nil {
			t.Errorf("encode progress: %v", err)
			return
		}
		if err := encoder.Encode(serverResponse{OK: true, Message: "Run finished", ExitCode: 0}); err != nil {
			t.Errorf("encode final response: %v", err)
		}
	}()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	response, err := sendRunRequest(baseDir, serverRequest{Op: "run"})
	_ = w.Close()
	output, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		t.Fatalf("sendRunRequest returned error: %v", err)
	}
	if !response.OK || response.Message != "Run finished" || response.ExitCode != 0 {
		t.Fatalf("response = %#v, want OK=true message=Run finished exit_code=0", response)
	}
	if !strings.Contains(string(output), "progress") && !strings.Contains(string(output), "progress:") {
		t.Fatalf("progress output missing; got %q", string(output))
	}
}

func TestRunServerSyncWithDisconnectCancelsRunningJob(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "projects", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{Commands: []QueuedCommand{{ID: "slow-id", Command: []string{"sleep", "30"}, Name: "slow"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "meta.json"), defaultMeta()); err != nil {
		t.Fatal(err)
	}

	client, serverConn := net.Pipe()
	defer client.Close()
	defer serverConn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = runServerSyncWithDisconnect(serverConn, baseDir, "default", "", 1, 1, 0, "", nil, "", nil, "", true, func(serverResponse) {})
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(queueDir, "runs", "*", "slow-id", "pid"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			data, err := os.ReadFile(matches[0])
			if err == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("job did not start")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("running job was not cancelled after disconnect")
	}
	if processAlive(pid) {
		t.Fatalf("job process %d is still running after disconnect", pid)
	}
}

func TestRunServerSyncWithDisconnectDetachesRunningJob(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "projects", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{Commands: []QueuedCommand{{ID: "slow-id", Command: []string{"sleep", "1"}, Name: "slow"}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "meta.json"), defaultMeta()); err != nil {
		t.Fatal(err)
	}

	client, serverConn := net.Pipe()
	defer client.Close()
	defer serverConn.Close()
	done := make(chan struct{})
	onDone := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, detached := runServerSyncWithDisconnectAndDone(serverConn, baseDir, "default", "", 1, 1, 0, "", nil, "", nil, "", true, func(serverResponse) {}, func() { close(onDone) })
		if !detached {
			t.Errorf("run was not detached")
		}
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(queueDir, "runs", "*", "slow-id", "pid"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			data, err := os.ReadFile(matches[0])
			if err == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("job did not start")
	}
	if _, err := client.Write([]byte{runDetachControl}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("detach did not return promptly")
	}
	if !processAlive(pid) {
		t.Fatalf("running job %d was stopped by detach", pid)
	}
	select {
	case <-onDone:
	case <-time.After(5 * time.Second):
		t.Fatal("onDone was not called after detached run completed")
	}
}
