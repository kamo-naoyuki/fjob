package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestResolveQueueNamePriority(t *testing.T) {
	const envName = "FJOB_QUEUE_NAME"
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
	const envName = "FJOB_BASEDIR"
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

	// 1. Fallback to default (home directory, etc.) if .fjob-state does not exist in current dir
	_, _, err = resolveBaseDir("")
	if err != nil {
		t.Fatal(err)
	}

	// 2. Prefer .fjob-state in current dir if it exists
	localState := filepath.Join(tempDir, ".fjob-state")
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

	// 3. FJOB_BASEDIR environment variable priority
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
	if count := strings.Count(string(data), "# fjob completion (bash)"); count != 1 {
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
	if strings.Count(content, "# fjob completion (bash)") != 1 {
		t.Fatal("Bash completion block was installed more than once")
	}
	if !strings.Contains(content, `eval "$(fjob completion bash)"`) {
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

	completion, err := os.ReadFile(filepath.Join(home, ".zfunc", "_fjob"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(completion), "#compdef fjob") {
		t.Fatal("Zsh completion header is missing")
	}
	rc, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(rc), "# fjob completion (zsh)") != 1 {
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

func TestCompareQueueWithRun(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(dir, "queue.json")
	runPath := filepath.Join(dir, "commands.json")
	if err := writeJSON(queuePath, Queue{Commands: []QueuedCommand{
		{Command: []string{"echo", "same"}, Name: "same"},
		{Command: []string{"echo", "changed"}, Name: "new-name"},
		{Command: []string{"echo", "added"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(runPath, Queue{Commands: []QueuedCommand{
		{Command: []string{"echo", "same"}, Name: "same"},
		{Command: []string{"echo", "changed"}, Name: "old-name"},
		{Command: []string{"echo", "removed"}},
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

func TestResolveQueueBackendUsesDefaultBackend(t *testing.T) {
	baseDir := t.TempDir()
	queueDir := filepath.Join(baseDir, "queues", "default")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{
		DefaultBackend: "slurm",
		Commands: []QueuedCommand{{Command: []string{"echo", "hello"}}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := resolveQueueBackend(baseDir, "default", "")
	if err != nil {
		t.Fatalf("resolveQueueBackend returned error: %v", err)
	}
	if got != "slurm" {
		t.Fatalf("resolved backend = %q, want slurm", got)
	}

	if _, err := resolveQueueBackend(baseDir, "default", "invalid"); err == nil {
		t.Fatal("resolveQueueBackend accepted unsupported backend")
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
		Command: []string{"/bin/sh", "-c", fmt.Sprintf("if [ -f %q ]; then exit 0; else touch %q; exit 1; fi", marker, marker)},
	}}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "retry-run", 1, 1, 1, "", nil, nil); code != 0 {
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
		{Command: []string{"/bin/sh", "-c", "exit 1"}, Name: "job1"},
		{Command: []string{"/bin/sh", "-c", "echo ok"}, Name: "job2", DependsOn: []string{"job1"}},
	}}); err != nil {
		t.Fatal(err)
	}

	if code := executeMixedRun(paths, "blocked-run", 1, 1, 0, "", nil, nil); code != 1 {
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

func TestServerBeginAndEndRunTracksActiveState(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &fjobServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
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
	if !strings.Contains(message, "fjob show --basedir") {
		t.Fatalf("message = %q, want show command hint", message)
	}
}

func TestFjobServerBusyStateTracksRunBoundary(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := &fjobServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
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
	baseDir, err := os.MkdirTemp("", "fjob-socket-")
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
	if err := writeJSON(filepath.Join(queueDir, "queue.json"), Queue{Commands: []QueuedCommand{{Command: []string{"sleep", "3"}, Name: "slow"}}}); err != nil {
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
		_, _, _ = runServerSyncWithDisconnect(serverConn, baseDir, "default", 1, 1, 0, "", nil, func(serverResponse) {})
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
