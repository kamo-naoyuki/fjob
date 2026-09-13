package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestResolveQueueNamePriority(t *testing.T) {
	const envName = "JOBQ_QUEUE_NAME"
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
	const envName = "JOBQ_BASEDIR"
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

	// 1. Fallback to default (home directory, etc.) if .jobq-state does not exist in current dir
	_, _, err = resolveBaseDir("")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Prefer .jobq-state in current dir if it exists
	localState := filepath.Join(tempDir, ".jobq-state")
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

	// 3. JOBQ_BASEDIR environment variable priority
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

func TestQueueUnmarshalSupportsLegacyCommands(t *testing.T) {
	var queue Queue
	if err := json.Unmarshal([]byte(`{"commands":[["echo","hello"]]}`), &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || len(queue.Commands[0].Command) != 2 {
		t.Fatalf("unexpected queue: %#v", queue)
	}
	if queue.Commands[0].Command[0] != "echo" {
		t.Fatalf("unexpected command: %#v", queue.Commands[0].Command)
	}
}

func TestQueueToJobsPreservesName(t *testing.T) {
	jobs := queueToJobs([]QueuedCommand{{
		Command: []string{"echo", "hello"},
		Name:    "greeting",
	}})
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Name != "greeting" {
		t.Fatalf("job name = %q, want greeting", jobs[0].Name)
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
	if count := strings.Count(string(data), "# jobq completion (bash)"); count != 1 {
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
	for _, option := range []string{"--basedir", "--queue-name", "--failed-logs", "--job-name"} {
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
	if strings.Count(content, "# jobq completion (bash)") != 1 {
		t.Fatal("Bash completion block was installed more than once")
	}
	if !strings.Contains(content, `eval "$(jobq completion bash)"`) {
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

	completion, err := os.ReadFile(filepath.Join(home, ".zfunc", "_jobq"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(completion), "#compdef jobq") {
		t.Fatal("Zsh completion header is missing")
	}
	rc, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(rc), "# jobq completion (zsh)") != 1 {
		t.Fatal("Zsh completion block was installed more than once")
	}
}

func TestPrepareRunSelectionWithoutPreviousRun(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "queues", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := prepareRunSelection(baseDir, "default", "failed")
	if !errors.Is(err, errNoPreviousRun) {
		t.Fatalf("error = %v, want errNoPreviousRun", err)
	}
}
