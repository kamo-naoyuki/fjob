package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitForAsyncRunCallsOnDone(t *testing.T) {
	command := exec.Command("sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	waitForAsyncRun(command, func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("async run completion callback was not called")
	}
}

func TestCmdRunWithRunIDRejectsRunningProjectBeforeQueueConfirmation(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-running-source"
	if err := writeJSON(filepath.Join(paths.runsDir, runID, "commands.json"), Queue{Commands: []QueuedCommand{{ID: "source", Command: []string{"source"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "existing", Command: []string{"existing"}}}}); err != nil {
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
	code := cmdRun([]string{"--basedir", baseDir, "--project-name", "default", "--run-id", runID})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), `project "default" is running; run is not allowed`) {
		t.Fatalf("cmdRun exit code = %d, stderr = %q", code, output)
	}
	if strings.Contains(string(output), "queue is not empty") {
		t.Fatalf("cmdRun checked queue before running state: %q", output)
	}
}

func TestCmdRunOverwriteSkipsQueueConfirmation(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "existing", Command: []string{"existing"}}}}); err != nil {
		t.Fatal(err)
	}
	runID := "source-run"
	if err := os.MkdirAll(filepath.Join(paths.runsDir, runID), 0o755); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdRun([]string{"--basedir", baseDir, "--project-name", "default", "--run-id", runID, "--overwrite"})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "command snapshot has no jobs") {
		t.Fatalf("cmdRun exit code = %d, stderr = %q", code, output)
	}
	if strings.Contains(string(output), "queue is not empty") {
		t.Fatalf("cmdRun prompted instead of honoring --overwrite: %q", output)
	}
}

func TestEnqueueCommandRejectsInterruptedRunWithoutChangingQueue(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	original := Queue{Commands: []QueuedCommand{{ID: "existing", Command: []string{"existing"}}}}
	if err := writeJSON(paths.queueFile, original); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.metaFile, Meta{Phase: "running", LastRunID: "interrupted-run"}); err != nil {
		t.Fatal(err)
	}

	_, err = enqueueCommand(baseDir, "default", []string{"duplicate"}, "", nil, nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), `project "default" has interrupted run "interrupted-run"; add is not allowed`) {
		t.Fatalf("enqueueCommand error = %v, want interrupted run error", err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].ID != "existing" {
		t.Fatalf("queue changed after rejected add: %#v", queue.Commands)
	}
}

func TestCmdAddEnqueuesJob(t *testing.T) {
	baseDir := t.TempDir()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdAdd([]string{
		"--basedir", baseDir, "--project-name", "demo", "--job-name", "job",
		"--executor", "local", "--env", "TOKEN=secret", "echo", "hello",
	})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "submitted project=demo") {
		t.Fatalf("cmdAdd exit code = %d, stdout = %q", code, output)
	}

	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].Name != "job" || queue.Commands[0].Executor != "local" ||
		len(queue.Commands[0].Environment) != 1 || queue.Commands[0].Environment[0] != "TOKEN=secret" {
		t.Fatalf("queue commands = %#v, want persisted job with executor and env", queue.Commands)
	}
}

func TestCmdAddRejectsMissingCommand(t *testing.T) {
	baseDir := t.TempDir()
	if code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo"}); code != 1 {
		t.Fatalf("cmdAdd exit code = %d, want 1 for missing command", code)
	}
}

func TestCmdAddRejectsInvalidArrayRange(t *testing.T) {
	baseDir := t.TempDir()
	code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo", "--array", "not-a-range", "echo", "hello"})
	if code != 1 {
		t.Fatalf("cmdAdd exit code = %d, want 1 for invalid array range", code)
	}
}

func TestCmdAddRejectsInvalidEnv(t *testing.T) {
	baseDir := t.TempDir()
	code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo", "--env", "NOVALUE", "echo", "hello"})
	if code != 1 {
		t.Fatalf("cmdAdd exit code = %d, want 1 for invalid --env", code)
	}
}

func TestCmdAddRejectsDuplicateJobNameWithoutWriting(t *testing.T) {
	baseDir := t.TempDir()
	if code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo", "--job-name", "prepare", "echo", "one"}); code != 0 {
		t.Fatalf("first cmdAdd exit code = %d, want 0", code)
	}
	if code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo", "--job-name", "prepare", "echo", "two"}); code == 0 {
		t.Fatal("cmdAdd accepted a duplicate job name")
	}

	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].Command[len(queue.Commands[0].Command)-1] != "one" {
		t.Fatalf("queue commands = %#v, want only the first job (rejected add must not write)", queue.Commands)
	}
}

func TestCmdServerRequestFailsWithoutRunningServer(t *testing.T) {
	baseDir := t.TempDir()

	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdServerRequest([]string{"--basedir", baseDir}, "ping")
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "failed to contact server") {
		t.Fatalf("cmdServerRequest exit code = %d, stderr = %q", code, output)
	}
}

func TestCancelJobsRejectsJobTraversal(t *testing.T) {
	if _, err := cancelJobs(t.TempDir(), "default", "run-1", []string{"../outside"}); err == nil {
		t.Fatal("cancelJobs accepted unsafe job ID")
	}
}

func TestCancelJobsRejectsAbsoluteAndNestedJobIDs(t *testing.T) {
	for _, jobID := range []string{"/tmp/outside", "nested/job", "job/..", "job/.", "job/with/slash"} {
		if _, err := cancelJobs(t.TempDir(), "default", "run-1", []string{jobID}); err == nil {
			t.Fatalf("cancelJobs accepted unsafe job ID %q", jobID)
		}
	}
}

func TestCmdAddThenCmdRunExecutesLocalJobEndToEnd(t *testing.T) {
	// A short, non-nested temp dir is required: the unix socket path derived
	// from baseDir must stay under the ~108 byte sun_path limit.
	baseDir, err := os.MkdirTemp("", "rotari-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)
	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	serverDone := make(chan int, 1)
	go func() { serverDone <- runServer(baseDir) }()

	deadline := time.Now().Add(3 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		response, err := sendServerRequest(baseDir, serverRequest{Op: "ping"})
		if err == nil && response.OK {
			pingErr = nil
			break
		}
		pingErr = err
		time.Sleep(10 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("server did not become ready: %v", pingErr)
	}
	// A completed sync run drops activeRuns to zero, which makes the server
	// stop itself; no explicit shutdown request is needed here.
	defer func() {
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			t.Log("server did not stop after the run completed")
		}
	}()

	if code := cmdAdd([]string{"--basedir", baseDir, "--project-name", "demo", "echo", "hello"}); code != 0 {
		t.Fatalf("cmdAdd exit code = %d, want 0", code)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdRun([]string{"--basedir", baseDir, "--project-name", "demo"})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(output), "Run finished") {
		t.Fatalf("cmdRun exit code = %d, stdout = %q", code, output)
	}

	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "finished" || meta.LastRunExitCode != 0 {
		t.Fatalf("meta = %#v, want finished run with exit code 0", meta)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 0 {
		t.Fatalf("queue commands = %#v, want empty queue after run", queue.Commands)
	}
}

func TestServerHandlePing(t *testing.T) {
	client, serverConn := net.Pipe()
	defer client.Close()

	server := &rotariServer{stopped: make(chan struct{}), lastAccess: time.Now()}
	go server.handle(t.TempDir(), serverConn)

	if err := json.NewEncoder(client).Encode(serverRequest{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	var response serverResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.PID == 0 || response.Protocol != serverProtocolVersion {
		t.Fatalf("response = %+v, want successful ping", response)
	}
}

func TestServerHandleRejectsMalformedJSON(t *testing.T) {
	client, serverConn := net.Pipe()
	defer client.Close()

	server := &rotariServer{stopped: make(chan struct{}), lastAccess: time.Now()}
	go server.handle(t.TempDir(), serverConn)

	if _, err := client.Write([]byte("{invalid}\n")); err != nil {
		t.Fatal(err)
	}
	var response serverResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Message, "invalid character") {
		t.Fatalf("response = %+v, want JSON decoding error", response)
	}
}

func TestServerHandleRejectsUnknownOperation(t *testing.T) {
	client, serverConn := net.Pipe()
	defer client.Close()

	server := &rotariServer{stopped: make(chan struct{}), lastAccess: time.Now()}
	go server.handle(t.TempDir(), serverConn)
	if err := json.NewEncoder(client).Encode(serverRequest{Op: "unknown"}); err != nil {
		t.Fatal(err)
	}
	var response serverResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Message != "unknown server operation: unknown" {
		t.Fatalf("response = %+v, want unknown-operation error", response)
	}
}

func TestServerHandleSubmitPersistsQueue(t *testing.T) {
	baseDir := t.TempDir()
	client, serverConn := net.Pipe()
	defer client.Close()

	server := &rotariServer{stopped: make(chan struct{}), lastAccess: time.Now()}
	go server.handle(baseDir, serverConn)
	request := serverRequest{
		Op: "submit", QueueName: "demo", Command: []string{"printf", "hello"},
		JobName: "greeting", DependsOn: []string{"setup"},
	}
	if err := json.NewEncoder(client).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response serverResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("response = %+v, want successful submit", response)
	}

	paths, err := resolvePaths(baseDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].Name != "greeting" || queue.Commands[0].ID == "" {
		t.Fatalf("queue = %+v, want one persisted named command with an ID", queue)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Phase != "collecting" {
		t.Fatalf("meta phase = %q, want collecting", meta.Phase)
	}
}

func TestSendServerRequestOverUnixSocket(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-server-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })

	listener, err := net.Listen("unix", serverSocketPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			server.handle(baseDir, conn)
		}
	}()

	response, err := sendServerRequest(baseDir, serverRequest{Op: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Protocol != serverProtocolVersion {
		t.Fatalf("response = %+v, want successful ping", response)
	}
}

func TestEnsureServerReusesCompatibleServer(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-existing-server-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })
	listener, err := net.Listen("unix", serverSocketPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			server.handle(baseDir, conn)
		}
	}()

	if err := ensureServer(baseDir); err != nil {
		t.Fatalf("ensureServer returned error for compatible server: %v", err)
	}
}

func TestCmdServerStatusReportsRunningServer(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-status-server-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })
	listener, err := net.Listen("unix", serverSocketPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			server.handle(baseDir, conn)
		}
	}()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdServerStatus([]string{"--basedir", baseDir})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdServerStatus exit code = %d, want 0", code)
	}
	for _, want := range []string{"server is running", baseDir} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("server status does not contain %q: %s", want, output)
		}
	}
}

func TestCmdServerStatusReportsStoppedServer(t *testing.T) {
	baseDir := t.TempDir()
	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	code := cmdServerStatus([]string{"--basedir", baseDir})
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(string(output), "server is not running") {
		t.Fatalf("cmdServerStatus exit code = %d, stderr = %q", code, output)
	}
}

func TestCmdServerListReportsLiveServer(t *testing.T) {
	masterDir := t.TempDir()
	baseDir, err := os.MkdirTemp("", "rotari-list-server-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })
	listener, err := net.Listen("unix", serverSocketPath(baseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			server.handle(baseDir, conn)
		}
	}()
	if err := registerServer(masterDir, serverRecord{BaseDir: baseDir, PID: os.Getpid(), LastSeen: nowRFC3339()}); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := cmdServerList([]string{"--masterdir", masterDir})
	os.Stdout = oldStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("cmdServerList exit code = %d, want 0", code)
	}
	for _, want := range []string{"master=" + masterDir, "PID", baseDir} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("server list does not contain %q: %s", want, output)
		}
	}
}

func TestListServersKeepsLiveAndRemovesInvalidRecords(t *testing.T) {
	masterDir := t.TempDir()
	liveBaseDir, err := os.MkdirTemp("", "rotari-live-server-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(liveBaseDir) })
	listener, err := net.Listen("unix", serverSocketPath(liveBaseDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			server.handle(liveBaseDir, conn)
		}
	}()

	live := serverRecord{BaseDir: liveBaseDir, PID: os.Getpid(), StartedAt: nowRFC3339(), LastSeen: nowRFC3339()}
	if err := registerServer(masterDir, live); err != nil {
		t.Fatal(err)
	}
	staleBaseDir := filepath.Join(t.TempDir(), "stale")
	if err := registerServer(masterDir, serverRecord{BaseDir: staleBaseDir, PID: 999999}); err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(masterDir, "malformed.json")
	if err := os.WriteFile(malformedPath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	servers, err := listServers(masterDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].BaseDir != liveBaseDir {
		t.Fatalf("servers = %+v, want only %q", servers, liveBaseDir)
	}
	for _, path := range []string{serverRecordPath(masterDir, staleBaseDir), malformedPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid record %q was not removed: %v", path, err)
		}
	}
}

func TestServerRegistryRecordLifecycle(t *testing.T) {
	masterDir := t.TempDir()
	baseDir := t.TempDir()
	record := serverRecord{
		BaseDir: baseDir, PID: 1234, StartedAt: "2026-01-01T00:00:00Z", LastSeen: "2026-01-01T00:00:00Z",
	}
	if err := registerServer(masterDir, record); err != nil {
		t.Fatal(err)
	}
	if err := touchServerRecord(masterDir, baseDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(serverRecordPath(masterDir, baseDir))
	if err != nil {
		t.Fatal(err)
	}
	var updated serverRecord
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.LastSeen == record.LastSeen {
		t.Fatalf("last seen was not updated: %+v", updated)
	}
	formatted := formatServerList([]serverRecord{updated})
	for _, want := range []string{"PID", "LAST_SEEN", baseDir, "1234"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted server list does not contain %q:\n%s", want, formatted)
		}
	}
	if got := formatServerList(nil); got != "no running servers" {
		t.Fatalf("empty server list = %q", got)
	}
	if err := unregisterServer(masterDir, baseDir); err != nil {
		t.Fatal(err)
	}
	if err := unregisterServer(masterDir, baseDir); err != nil {
		t.Fatalf("second unregister failed: %v", err)
	}
}

func TestRunServerLifecycle(t *testing.T) {
	baseDir := t.TempDir()
	masterDir := t.TempDir()
	t.Setenv("ROTARI_MASTERDIR", masterDir)
	result := make(chan int, 1)
	go func() {
		result <- runServer(baseDir)
	}()

	deadline := time.Now().Add(3 * time.Second)
	var response serverResponse
	var err error
	for time.Now().Before(deadline) {
		response, err = sendServerRequest(baseDir, serverRequest{Op: "ping"})
		if err == nil && response.OK {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || !response.OK {
		t.Fatalf("server did not become ready: response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(serverRecordPath(masterDir, baseDir)); err != nil {
		t.Fatalf("server registry record missing: %v", err)
	}

	response, err = sendServerRequest(baseDir, serverRequest{Op: "shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("shutdown response = %+v", response)
	}
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("runServer exit code = %d, want 0", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after shutdown")
	}
	for _, path := range []string{
		serverSocketPath(baseDir), serverPIDPath(baseDir), serverRecordPath(masterDir, baseDir),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("server artifact %q remains after shutdown: %v", path, err)
		}
	}
}

func TestRunServerUsesOwnerOnlyPermissions(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-perm-server-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)
	t.Setenv("ROTARI_MASTERDIR", t.TempDir())
	result := make(chan int, 1)
	go func() {
		result <- runServer(baseDir)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err = sendServerRequest(baseDir, serverRequest{Op: "ping"})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server did not become ready: %v", err)
	}

	info, statErr := os.Stat(serverSocketPath(baseDir))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("server socket permissions = %o, want no group/other access", perm)
	}

	if _, err := sendServerRequest(baseDir, serverRequest{Op: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after shutdown")
	}
}

func TestVerifyPeerCredentialAcceptsSameUIDConnection(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "rotari-peercred-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)
	socketPath := filepath.Join(baseDir, "test.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	client, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept connection")
	}
	defer serverConn.Close()

	if err := verifyPeerCredential(serverConn); err != nil {
		t.Fatalf("verifyPeerCredential rejected same-process connection: %v", err)
	}
}
