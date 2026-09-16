package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestResolveQueueNamePriority(t *testing.T) {
	const envName = "ROTARI_QUEUE_NAME"
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
	got, err := resolveQueueName(baseDir, "from-option")
	if err != nil || got != "from-option" {
		t.Fatalf("option priority: got %q, err %v", got, err)
	}
	got, err = resolveQueueName(baseDir, "")
	if err != nil || got != "from-env" {
		t.Fatalf("environment priority: got %q, err %v", got, err)
	}
	if err := os.Unsetenv(envName); err != nil {
		t.Fatal(err)
	}
	got, err = resolveQueueName(baseDir, "")
	if err != nil || got != defaultQueueName {
		t.Fatalf("default priority: got %q, want %q, err %v", got, defaultQueueName, err)
	}

	// Test automatically selecting a single queue if only one exists
	q1Dir := filepath.Join(baseDir, "queues", "q1")
	if err := os.MkdirAll(q1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	got, err = resolveQueueName(baseDir, "")
	if err != nil || got != "q1" {
		t.Fatalf("auto select single queue: got %q, want q1, err %v", got, err)
	}

	// Test returning an error if multiple queues exist and none is specified
	q2Dir := filepath.Join(baseDir, "queues", "q2")
	if err := os.MkdirAll(q2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	_, err = resolveQueueName(baseDir, "")
	if err == nil {
		t.Fatal("expected error for multiple queues when queue-name is empty, got nil")
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

func TestEnqueueCommandPersistsStableJobID(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "old"}, "", nil, "job", nil); err != nil {
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
	queue.Commands[0].Command = []string{"echo", "new"}
	jobs := queueToJobs(queue.Commands)
	if len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("changed command ID = %q, want %q", jobs[0].ID, id)
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

	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "new"}, "", nil, "new-job", nil); err != nil {
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
	if got != "'--basedir[state directory]:DIR:'" {
		t.Fatalf("zsh argument = %q, want value name in specification", got)
	}
}

func TestCompletionScriptsContainCommandOptions(t *testing.T) {
	for _, option := range []string{"--basedir", "--queue-name", "--failed-logs", "--no-pager", "--job-name"} {
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
	if !strings.Contains(generateBashCompletion(), "__complete queue-name") {
		t.Error("Bash completion does not dynamically complete queue names")
	}
	if !strings.Contains(generateZshCompletion(), "_rotari_queue_names") {
		t.Error("Zsh completion does not dynamically complete queue names")
	}
}

func TestCompleteQueueNames(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "queues", "z-last"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "queues", "a-first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "queues", "not-a-queue"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdComplete([]string{"queue-name", "--basedir", baseDir})
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
		t.Fatalf("queue-name completion = %q, want sorted queue names", output)
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
	_, err = planRerunSelection(paths, Queue{Commands: []QueuedCommand{{ID: "alpha"}}}, "failed", nil, "")
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
	meta := defaultMeta()
	meta.LastRunID = "run-1"
	if err := writeJSON(paths.metaFile, meta); err != nil {
		t.Fatal(err)
	}

	plan, err := planRerunSelection(paths, queue, "failed", nil, "")
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
	if origin == nil || origin.RunID != "run-1" || origin.JobID != "alpha" || origin.Status != "success" {
		t.Fatalf("origin = %#v, want run-1/alpha success", origin)
	}
}

func TestDeleteRemovesOnlySelectedRun(t *testing.T) {
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

	plan, err := planRerunSelection(paths, queue, "job-id", []string{"beta", "alpha"}, "")
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
	queueDir := filepath.Join(baseDir, "queues", "default")
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(baseDir, "retry-marker")
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID:      "retry",
		Command: []string{"/bin/sh", "-c", fmt.Sprintf("if [ -f %q ]; then exit 0; else touch %q; exit 1; fi", marker, marker)},
	}}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "retry-run", "", 1, 1, 1, "", nil, "", nil, "", nil); code != 0 {
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

func TestExecuteMixedRunPersistsRunName(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{
		ID: "named-job", Command: []string{"sh", "-c", "exit 0"}, Name: "named-job",
	}}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "named-run", "nightly-build", 1, 1, 0, "", nil, "", nil, "", nil); code != 0 {
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
		Results: []JobResult{{ID: "job-1", ExitCode: 1}},
	})
	for _, want := range []string{"nightly (run-1)", "Failed: 1", "rotari show", "rotari retry"} {
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
		[]string{"-p gpu"}, false, "", []string{"prepare"}, false, []string{"./train-v2"})
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{
		{ID: "job1-id", Command: []string{"/bin/sh", "-c", "exit 1"}, Name: "job1"},
		{ID: "job2-id", Command: []string{"/bin/sh", "-c", "echo ok"}, Name: "job2", DependsOn: []string{"job1"}},
	}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "blocked-run", "", 1, 1, 0, "", nil, "", nil, "", nil); code != 1 {
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
	if code := executeMixedRun(paths, "run-1", "", 1, 1, 0, "", nil, "", nil, "", nil); code != 1 {
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
	if code := executeMixedRun(paths, "run-2", "", 1, 1, 0, "", nil, "failed", nil, "", nil); code != 1 {
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
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

func TestCancelJobsCancelsSelectedLocalJob(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run-1")
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "30")
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		t.Fatal(err)
	}

	message, err := finishCancelMessage("Cancel requested", paths, "default", "run-1", false)
	if err != nil {
		t.Fatalf("finishCancelMessage returned error: %v", err)
	}
	if !strings.Contains(message, "Inspect status") {
		t.Fatalf("message = %q, want inspect status hint", message)
	}
	if !strings.Contains(message, "rotari show --basedir") {
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

func TestRunServerSyncWithDisconnectReturnsAfterSocketEOF(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "queues", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{Commands: []QueuedCommand{{ID: "slow-id", Command: []string{"sleep", "3"}, Name: "slow"}}}); err != nil {
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
		_, _, _ = runServerSyncWithDisconnect(serverConn, baseDir, "default", "", 1, 1, 0, "", nil, "", nil, "", func(serverResponse) {})
	}()

	time.Sleep(100 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runServerSyncWithDisconnect did not return after disconnect")
	}
}
