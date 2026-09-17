package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type serverRequest struct {
	Op               string   `json:"op"`
	QueueName        string   `json:"project_name,omitempty"`
	Command          []string `json:"command,omitempty"`
	LocalConcurrency int      `json:"local_concurrency,omitempty"`
	BatchMaxActive   int      `json:"batch_max_active,omitempty"`
	Retry            int      `json:"retry,omitempty"`
	RunName          string   `json:"run_name,omitempty"`
	CWD              string   `json:"cwd,omitempty"`
	Wait             bool     `json:"wait,omitempty"`
	Async            bool     `json:"async,omitempty"`
	Executor         string   `json:"executor,omitempty"`
	ExecutorOptions  []string `json:"executor_options,omitempty"`
	JobIDs           []string `json:"job_ids,omitempty"`
	Selection        string   `json:"selection,omitempty"`
	SourceRunID      string   `json:"source_run_id,omitempty"`
	JobName          string   `json:"job_name,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
}

type serverResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Protocol  int    `json:"protocol,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Progress  bool   `json:"progress,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Completed int    `json:"completed,omitempty"`
	Total     int    `json:"total,omitempty"`
	Succeeded int    `json:"succeeded,omitempty"`
	Failed    int    `json:"failed,omitempty"`
}

const serverProtocolVersion = 2

type rotariServer struct {
	listener   net.Listener
	stopped    chan struct{}
	stopOnce   sync.Once
	accessMu   sync.Mutex
	lastAccess time.Time
	activeRuns int
}

const serverIdleTimeout = time.Minute

func serverSocketPath(baseDir string) string {
	return filepath.Join(baseDir, "server.sock")
}

func serverLockPath(baseDir string) string {
	return filepath.Join(baseDir, "server.lock")
}

func serverPIDPath(baseDir string) string {
	return filepath.Join(baseDir, "server.pid")
}

func cmdServer(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("server"))
		return 1
	}

	switch args[0] {
	case "shutdown":
		return cmdServerRequest(args[1:], "shutdown")
	case "status":
		return cmdServerStatus(args[1:])
	case "list":
		return cmdServerList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown server command: %s\n", args[0])
		return 1
	}
}

func ensureServer(baseDir string) error {
	if response, err := sendServerRequest(baseDir, serverRequest{Op: "ping"}); err == nil && response.OK {
		if response.Protocol == serverProtocolVersion {
			return nil
		}
		if _, shutdownErr := sendServerRequest(baseDir, serverRequest{Op: "shutdown"}); shutdownErr != nil {
			return fmt.Errorf("server protocol mismatch (got %d, want %d); failed to stop old server: %w", response.Protocol, serverProtocolVersion, shutdownErr)
		}
		for range 40 {
			if _, pingErr := sendServerRequest(baseDir, serverRequest{Op: "ping"}); pingErr != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to detect executable path: %w", err)
	}
	child := exec.Command(exe, "__server", "--basedir", baseDir)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to detach server stdio: %w", err)
	}
	defer devNull.Close()
	child.Stdin = devNull
	child.Stdout = devNull
	child.Stderr = devNull
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	for range 40 {
		if response, err := sendServerRequest(baseDir, serverRequest{Op: "ping"}); err == nil && response.OK {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server did not become ready (pid=%d)", child.Process.Pid)
}

func cmdServerStatus(args []string) int {
	fs := flag.NewFlagSet("server status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	response, err := sendServerRequest(baseDir, serverRequest{Op: "ping"})
	if err != nil || !response.OK {
		fmt.Fprintln(os.Stderr, "server is not running")
		return 1
	}
	fmt.Printf("server is running pid=%d state=%s\n", response.PID, baseDir)
	return 0
}

func cmdServerList(args []string) int {
	fs := flag.NewFlagSet("server list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	masterdir := cliString(fs, "masterdir", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	masterDir, err := resolveMasterDir(*masterdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve master directory: %v\n", err)
		return 1
	}
	servers, err := listServers(masterDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to list servers: %v\n", err)
		return 1
	}
	fmt.Printf("master=%s\n%s\n", masterDir, formatServerList(servers))
	return 0
}

func cmdServerRequest(args []string, op string) int {
	fs := flag.NewFlagSet("server request", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	response, err := sendServerRequest(baseDir, serverRequest{Op: op})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to contact server: %v\n", err)
		return 1
	}
	if !response.OK {
		fmt.Fprintln(os.Stderr, response.Message)
		return 1
	}
	fmt.Print(colorMessage(response.Message))
	return 0
}

func cmdAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	executor := cliString(fs, "executor", "")
	var executorOptions stringSliceFlag
	cliValue(fs, &executorOptions, "executor-option")
	jobName := cliString(fs, "job-name", "")
	cliStringVar(fs, jobName, "name", "")
	var dependsOn stringSliceFlag
	cliValue(fs, &dependsOn, "depends-on")
	runAfterAdd := cliBool(fs, "run", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	left := fs.Args()
	if len(left) < 1 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("add"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveProjectName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	message, err := enqueueCommand(baseDir, queueName, left, *executor, executorOptions, *jobName, dependsOn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cyan(message))
	if *runAfterAdd {
		return cmdRun(addRunArgs(baseDir, queueName))
	}
	return 0
}

func addRunArgs(baseDir, queueName string) []string {
	return []string{"--basedir", baseDir, "--project-name", queueName}
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	runIDOption := cliString(fs, "run-id", "")
	overwriteQueue := cliBool(fs, "overwrite", false)
	runName := cliString(fs, "run-name", "")
	localConcurrency := cliInt(fs, "local-concurrency", 8)
	batchConcurrency := cliInt(fs, "batch-concurrency", 8)
	retry := cliInt(fs, "retry", 0)
	failed := cliBool(fs, "failed", false)
	unfinished := cliBool(fs, "unfinished", false)
	success := cliBool(fs, "success", false)
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	async := cliBool(fs, "async", false)
	executor := cliString(fs, "executor", "")
	var executorOptions stringSliceFlag
	cliValue(fs, &executorOptions, "executor-option")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	left := fs.Args()
	selection := resultSelection(*failed, *unfinished, *success)
	if len(jobIDs) > 0 && selection == "" {
		selection = "job-id"
	}
	if len(left) != 0 || *localConcurrency < 1 || *batchConcurrency < 1 || *retry < -1 || (*overwriteQueue && *runIDOption == "") {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("run"))
		return 1
	}
	baseDir, queueName, err := resolveExistingRunTarget(*basedir, *queueNameOption, *runIDOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureProjectIdle(baseDir, queueName, "run"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// A source run is needed whenever a selection filter is used, so the
	// filter can be matched against that run's results. An explicit
	// --run-id always repopulates the queue first ("run --run-id X"
	// behaves like "copy --run-id X --overwrite" followed by "run"). When
	// --run-id is omitted, the queue is only repopulated from the latest
	// run if it is currently empty; a non-empty queue (e.g. already
	// restored and edited via "change") is used as-is.
	sourceRunID := *runIDOption
	forceCopy := sourceRunID != ""
	if sourceRunID == "" && selection != "" {
		paths, pathErr := resolvePaths(baseDir, queueName)
		if pathErr != nil {
			fmt.Fprintln(os.Stderr, pathErr)
			return 1
		}
		meta, metaErr := loadMeta(paths.metaFile)
		if metaErr != nil {
			fmt.Fprintf(os.Stderr, "failed to load metadata: %v\n", metaErr)
			return 1
		}
		if meta.LastRunID == "" {
			fmt.Fprintf(os.Stderr, "queue %q has no previous run\n", queueName)
			return 1
		}
		sourceRunID = meta.LastRunID
		queue, queueErr := loadQueue(paths.queueFile)
		if queueErr != nil {
			fmt.Fprintf(os.Stderr, "failed to load queue: %v\n", queueErr)
			return 1
		}
		forceCopy = len(queue.Commands) == 0
	}
	if forceCopy {
		// Only prompts when the queue actually has jobs to lose; an empty
		// queue (the common "auto-copy" case) is overwritten silently.
		overwriteConfirmed, confirmErr := confirmQueueOverwrite(baseDir, queueName, false, *overwriteQueue)
		if confirmErr != nil {
			fmt.Fprintln(os.Stderr, confirmErr)
			return 1
		}
		message, copyErr := copyRunToQueue(baseDir, queueName, sourceRunID, "all", nil, false, overwriteConfirmed)
		if copyErr != nil {
			fmt.Fprintln(os.Stderr, copyErr)
			return 1
		}
		fmt.Println(cyan(message))
	}

	if err := ensureServer(baseDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to determine working directory: %v\n", err)
		return 1
	}
	request := serverRequest{
		Op: "run", QueueName: queueName, LocalConcurrency: *localConcurrency, BatchMaxActive: *batchConcurrency, Retry: *retry, Async: *async,
		RunName: *runName, Executor: *executor, ExecutorOptions: executorOptions, CWD: cwd,
		Selection: selection, JobIDs: jobIDs, SourceRunID: sourceRunID,
	}
	var response serverResponse
	if *async {
		response, err = sendServerRequest(baseDir, request)
	} else {
		response, err = sendRunRequest(baseDir, request)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to contact server: %v\n", err)
		return 1
	}
	if !response.OK {
		fmt.Fprintln(os.Stderr, response.Message)
		return 1
	}
	fmt.Print(colorMessage(response.Message))
	return response.ExitCode
}

func cmdRetry(args []string) int {
	return cmdRun(append([]string{"--failed", "--unfinished"}, args...))
}

func cmdServerProcess(args []string) int {
	fs := flag.NewFlagSet("__server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	return runServer(baseDir)
}

func runServer(baseDir string) int {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create state directory: %v\n", err)
		return 1
	}
	lease, err := os.OpenFile(serverLockPath(baseDir), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open server lock: %v\n", err)
		return 1
	}
	defer lease.Close()
	if err := syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "server is already running")
		return 1
	}

	socketPath := serverSocketPath(baseDir)
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to listen on server socket: %v\n", err)
		return 1
	}
	defer os.Remove(socketPath)
	defer os.Remove(serverPIDPath(baseDir))
	if err := os.WriteFile(serverPIDPath(baseDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write server pid: %v\n", err)
		return 1
	}
	masterDir, err := resolveMasterDir("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve master directory: %v\n", err)
		return 1
	}
	record := serverRecord{
		BaseDir: baseDir, Socket: socketPath, PID: os.Getpid(),
		StartedAt: nowRFC3339(), LastSeen: nowRFC3339(),
	}
	if err := registerServer(masterDir, record); err != nil {
		fmt.Fprintf(os.Stderr, "failed to register server: %v\n", err)
		return 1
	}
	defer unregisterServer(masterDir, baseDir)

	server := &rotariServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		<-signals
		server.stop()
	}()
	go server.idleChecker(masterDir, baseDir)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if server.isStopped() {
				return 0
			}
			continue
		}
		go server.handle(baseDir, conn)
	}
}

func (server *rotariServer) isStopped() bool {
	select {
	case <-server.stopped:
		return true
	default:
		return false
	}
}

func (server *rotariServer) stop() {
	server.stopOnce.Do(func() {
		close(server.stopped)
		_ = server.listener.Close()
	})
}

func (server *rotariServer) touch() {
	server.accessMu.Lock()
	server.lastAccess = time.Now()
	server.accessMu.Unlock()
}

func (server *rotariServer) idleChecker(masterDir, baseDir string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		_ = touchServerRecord(masterDir, baseDir)
		server.accessMu.Lock()
		idle := time.Since(server.lastAccess) >= serverIdleTimeout && server.activeRuns == 0
		server.accessMu.Unlock()
		if idle {
			server.stop()
			return
		}
		if server.isStopped() {
			return
		}
	}
}

func (server *rotariServer) handle(baseDir string, conn net.Conn) {
	defer conn.Close()
	server.touch()
	var request serverRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(serverResponse{Message: err.Error()})
		return
	}
	response := serverResponse{}
	encoder := json.NewEncoder(conn)
	switch request.Op {
	case "ping":
		response = serverResponse{OK: true, PID: os.Getpid(), Protocol: serverProtocolVersion}
	case "submit":
		message, err := enqueueCommand(baseDir, request.QueueName, request.Command, request.Executor, request.ExecutorOptions, request.JobName, request.DependsOn)
		response = serverResponse{OK: err == nil, Message: message}
		if err != nil {
			response.Message = err.Error()
		}
	case "cancel":
		message, err := cancelQueueJobs(baseDir, request.QueueName, request.JobIDs, request.Wait)
		response = serverResponse{OK: err == nil, Message: message}
		if err != nil {
			response.Message = err.Error()
		}
	case "suspend", "resume":
		message, err := controlQueueJobs(baseDir, request.QueueName, request.JobIDs, request.Op)
		response = serverResponse{OK: err == nil, Message: message}
		if err != nil {
			response.Message = err.Error()
		}
	case "run":
		var message string
		var exitCode int
		var err error
		var onDone func()
		if request.Async {
			server.beginRun()
			onDone = server.endRun
			message, err = startServerRun(baseDir, request.QueueName, request.RunName, request.LocalConcurrency, request.BatchMaxActive, request.Retry, request.Executor, request.ExecutorOptions, request.Selection, request.JobIDs, request.SourceRunID, onDone, request.CWD)
			if err != nil {
				onDone()
			}
		} else {
			server.beginRun()
			defer server.endRun()
			message, exitCode, err = runServerSyncWithDisconnect(conn, baseDir, request.QueueName, request.RunName, request.LocalConcurrency, request.BatchMaxActive, request.Retry, request.Executor, request.ExecutorOptions, request.Selection, request.JobIDs, request.SourceRunID, func(progress serverResponse) {
				_ = encoder.Encode(progress)
			}, request.CWD)
		}
		response = serverResponse{OK: err == nil, Message: message, ExitCode: exitCode}
		if err != nil {
			response.Message = err.Error()
		}
	case "shutdown":
		response = serverResponse{OK: true, Message: "server stopped"}
		server.stop()
	default:
		response.Message = "unknown server operation: " + request.Op
	}
	_ = encoder.Encode(response)
}

func runServerSyncWithDisconnect(conn net.Conn, baseDir, queueName, runName string, localConcurrency, batchMaxActive, retry int, executor string, executorOptions []string, selection string, jobIDs []string, sourceRunID string, progress func(serverResponse), cwdOverride ...string) (string, int, error) {
	cwd := ""
	if len(cwdOverride) > 0 {
		cwd = cwdOverride[0]
	}
	type result struct {
		message  string
		exitCode int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		message, exitCode, err := runServerSync(baseDir, queueName, runName, localConcurrency, batchMaxActive, retry, executor, executorOptions, selection, jobIDs, sourceRunID, progress, cwd)
		done <- result{message: message, exitCode: exitCode, err: err}
	}()
	disconnected := make(chan struct{})
	go func() {
		var buffer [1]byte
		_, err := conn.Read(buffer[:])
		if err != nil && (err == io.EOF || !errors.Is(err, os.ErrDeadlineExceeded)) {
			close(disconnected)
		}
	}()
	select {
	case result := <-done:
		return result.message, result.exitCode, result.err
	case <-disconnected:
		_, _ = cancelQueue(baseDir, queueName, false)
		result := <-done
		return result.message, result.exitCode, result.err
	}
}

func cmdCancel(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	wait := cliBool(fs, "wait", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("cancel"))
		return 1
	}
	if len(jobIDs) > 0 && *wait {
		fmt.Fprintln(os.Stderr, "--wait may not be used with --job-id")
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveProjectName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureServer(baseDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	response, err := sendServerRequest(baseDir, serverRequest{Op: "cancel", QueueName: queueName, JobIDs: jobIDs, Wait: *wait})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to contact server: %v\n", err)
		return 1
	}
	if !response.OK {
		fmt.Fprintln(os.Stderr, response.Message)
		return 1
	}
	fmt.Println(response.Message)
	return 0
}

func cmdJobSignal(args []string, operation string) int {
	fs := flag.NewFlagSet(operation, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage(operation))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveProjectName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureServer(baseDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	response, err := sendServerRequest(baseDir, serverRequest{Op: operation, QueueName: queueName, JobIDs: jobIDs})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to contact server: %v\n", err)
		return 1
	}
	if !response.OK {
		fmt.Fprintln(os.Stderr, response.Message)
		return 1
	}
	fmt.Println(response.Message)
	return 0
}

func (server *rotariServer) beginRun() {
	server.accessMu.Lock()
	server.activeRuns++
	server.lastAccess = time.Now()
	server.accessMu.Unlock()
}

func (server *rotariServer) endRun() {
	server.accessMu.Lock()
	server.activeRuns--
	server.lastAccess = time.Now()
	shouldStop := server.activeRuns == 0
	server.accessMu.Unlock()
	if shouldStop {
		server.stop()
	}
}

func (server *rotariServer) isBusy() bool {
	server.accessMu.Lock()
	defer server.accessMu.Unlock()
	return server.activeRuns > 0
}

func sendServerRequest(baseDir string, request serverRequest) (serverResponse, error) {
	conn, err := net.DialTimeout("unix", serverSocketPath(baseDir), time.Second)
	if err != nil {
		return serverResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return serverResponse{}, err
	}
	var response serverResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return serverResponse{}, err
	}
	return response, nil
}

func sendRunRequest(baseDir string, request serverRequest) (serverResponse, error) {
	conn, err := net.DialTimeout("unix", serverSocketPath(baseDir), time.Second)
	if err != nil {
		return serverResponse{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return serverResponse{}, err
	}
	decoder := json.NewDecoder(conn)
	responses := make(chan serverResponse, 1)
	decodeErrors := make(chan error, 1)
	go func() {
		for {
			var response serverResponse
			if err := decoder.Decode(&response); err != nil {
				decodeErrors <- err
				return
			}
			responses <- response
			if !response.Progress {
				return
			}
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	lastCompleted, lastSucceeded, lastFailed := -1, -1, -1
	for {
		var response serverResponse
		select {
		case <-signals:
			fmt.Println(yellow("Cancellation requested; stopping running jobs..."))
			_ = conn.Close()
			return serverResponse{OK: true, ExitCode: 130}, nil
		case err := <-decodeErrors:
			return serverResponse{}, err
		case response = <-responses:
		}
		if response.Progress {
			if response.Message != "" {
				if strings.HasPrefix(response.Message, "Job failed") {
					fmt.Printf("%s\n", red(response.Message))
				} else if strings.HasPrefix(response.Message, "Retrying job") {
					fmt.Printf("%s\n", yellow(response.Message))
				} else if strings.HasPrefix(response.Message, "Run started:") {
					fmt.Printf("%s\n", cyan(response.Message))
				} else {
					fmt.Printf("%s\n", yellow(response.Message))
				}
			} else {
				if response.Completed == lastCompleted && response.Succeeded == lastSucceeded && response.Failed == lastFailed {
					continue
				}
				fmt.Printf("%s\n", cyan(fmt.Sprintf("progress: %d/%d completed=%d failed=%d", response.Completed, response.Total, response.Succeeded, response.Failed)))
				lastCompleted, lastSucceeded, lastFailed = response.Completed, response.Succeeded, response.Failed
			}
			continue
		}
		return response, nil
	}
}

func resolveQueueExecutor(baseDir, queueName, requested string) (string, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if requested == "" {
		requested = queue.DefaultExecutor
		if requested == "" {
			requested = "local"
		}
	}
	if !isKnownExecutor(requested) {
		return "", fmt.Errorf("unsupported executor: %s", requested)
	}
	resolved := requested
	for _, queued := range queue.Commands {
		executor := queued.Executor
		if executor == "" {
			executor = requested
		}
		if !isKnownExecutor(executor) {
			return "", fmt.Errorf("unsupported executor: %s", executor)
		}
	}
	return resolved, nil
}

func cancelQueue(baseDir, queueName string, wait bool) (string, error) {
	return cancelQueueJobs(baseDir, queueName, nil, wait)
}

func controlQueueJobs(baseDir, queueName string, jobIDs []string, operation string) (string, error) {
	if operation != "suspend" && operation != "resume" {
		return "", fmt.Errorf("unsupported job operation: %s", operation)
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(paths.lockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("project %q is not running", queueName)
		}
		return "", err
	}
	var lock LockInfo
	if err := json.Unmarshal(data, &lock); err != nil {
		return "", fmt.Errorf("invalid running lock: %w", err)
	}
	runDir := filepath.Join(paths.runsDir, lock.RunID)
	allJobs := len(jobIDs) == 0
	targets := append([]string(nil), jobIDs...)
	if allJobs {
		entries, err := os.ReadDir(runDir)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				targets = append(targets, entry.Name())
			}
		}
	}
	controlled := 0
	for _, jobID := range targets {
		jobDir := filepath.Join(runDir, jobID)
		if jobFinished(jobDir) {
			if allJobs {
				continue
			}
			return "", fmt.Errorf("job %q is not running", jobID)
		}
		executor, err := jobOwnerExecutor(jobDir)
		if err != nil {
			if allJobs {
				continue
			}
			return "", fmt.Errorf("job %q is not running", jobID)
		}
		suspender, ok := executor.(Suspender)
		if !ok {
			return "", fmt.Errorf("executor %q does not support %s", executor.Name(), operation)
		}
		if operation == "resume" {
			err = suspender.Resume(jobDir)
		} else {
			err = suspender.Suspend(jobDir)
		}
		if err != nil {
			return "", fmt.Errorf("%s job %s: %w", operation, jobID, err)
		}
		controlled++
	}
	if controlled == 0 {
		return "", fmt.Errorf("no running jobs found in queue %q", queueName)
	}
	return fmt.Sprintf("%s requested\n  Project: %s\n  Run: %s\n  Jobs: %d", strings.Title(operation), queueName, lock.RunID, controlled), nil
}

func jobFinished(jobDir string) bool {
	if _, err := os.Stat(filepath.Join(jobDir, "finished_at")); err == nil {
		return true
	}
	data, err := os.ReadFile(filepath.Join(jobDir, "status.json"))
	if err != nil {
		return false
	}
	var status slurmStatus
	return json.Unmarshal(data, &status) == nil && status.Phase != "" && status.Phase != "running"
}

func cancelQueueJobs(baseDir, queueName string, jobIDs []string, wait bool) (string, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(paths.lockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("project %q is not running", queueName)
		}
		return "", err
	}
	var lock LockInfo
	if err := json.Unmarshal(data, &lock); err != nil {
		return "", fmt.Errorf("invalid running lock: %w", err)
	}
	runDir := filepath.Join(paths.runsDir, lock.RunID)
	if len(jobIDs) > 0 {
		return cancelJobs(runDir, queueName, lock.RunID, jobIDs)
	}
	if err := markQueueCancelling(paths); err != nil {
		return "", err
	}
	metadataPath := filepath.Join(runDir, "slurm_jobs.json")
	if metadata, err := os.ReadFile(metadataPath); err == nil {
		var jobs []slurmJobMetadata
		if err := json.Unmarshal(metadata, &jobs); err != nil {
			return "", fmt.Errorf("invalid Slurm metadata: %w", err)
		}
		for _, job := range jobs {
			if _, err := runSlurmCommand("scancel", job.SlurmJobID); err != nil {
				return "", fmt.Errorf("scancel %s: %w", job.SlurmJobID, err)
			}
		}
		return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Project: %s\n  Run: %s\n  Slurm jobs: %d", queueName, lock.RunID, len(jobs)), paths, queueName, lock.RunID, wait)
	}

	if lock.PID == os.Getpid() {
		cancelled := 0
		entries, _ := os.ReadDir(runDir)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pidData, err := os.ReadFile(filepath.Join(runDir, entry.Name(), "pid"))
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err == nil && syscall.Kill(pid, syscall.SIGTERM) == nil {
				cancelled++
			}
		}
		return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Project: %s\n  Run: %s\n  Local jobs: %d", queueName, lock.RunID, cancelled), paths, queueName, lock.RunID, wait)
	}
	if err := syscall.Kill(-lock.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("cancel local worker: %w", err)
	}
	return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Project: %s\n  Run: %s\n  Worker PID: %d", queueName, lock.RunID, lock.PID), paths, queueName, lock.RunID, wait)
}

func cancelJobs(runDir, queueName, runID string, jobIDs []string) (string, error) {
	requested := make(map[string]bool, len(jobIDs))
	for _, jobID := range jobIDs {
		requested[jobID] = true
	}
	var commandSnapshot Queue
	if data, err := os.ReadFile(filepath.Join(runDir, "commands.json")); err == nil {
		if err := json.Unmarshal(data, &commandSnapshot); err != nil {
			return "", fmt.Errorf("invalid command snapshot: %w", err)
		}
	}
	knownJobs := make(map[string]JobSpec)
	for _, job := range queueToJobs(commandSnapshot.Commands) {
		knownJobs[job.ID] = job
	}
	cancelled := 0
	for jobID := range requested {
		jobDir := filepath.Join(runDir, jobID)
		if executor, err := jobOwnerExecutor(jobDir); err == nil {
			canceller, ok := executor.(Canceller)
			if !ok {
				return "", fmt.Errorf("executor %q does not support cancel", executor.Name())
			}
			if err := canceller.Cancel(jobDir); err != nil {
				return "", fmt.Errorf("cancel job %s: %w", jobID, err)
			}
			cancelled++
			continue
		}
		// Job has no executor yet (Submit was never called): record the
		// cancellation so the scheduler skips it once it would be submitted.
		if _, ok := knownJobs[jobID]; !ok {
			return "", fmt.Errorf("job %q is not found", jobID)
		}
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			return "", fmt.Errorf("prepare cancellation for job %s: %w", jobID, err)
		}
		if err := os.WriteFile(filepath.Join(jobDir, "cancelled"), []byte(nowRFC3339()+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("record cancellation for job %s: %w", jobID, err)
		}
		cancelled++
	}
	return fmt.Sprintf("Cancel requested\n  Project: %s\n  Run: %s\n  Jobs: %d", queueName, runID, cancelled), nil
}

func markQueueCancelling(paths pathSet) error {
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}
	meta.Phase = "cancelling"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return fmt.Errorf("failed to mark queue as cancelling: %w", err)
	}
	return nil
}

func finishCancelMessage(message string, paths pathSet, queueName, runID string, wait bool) (string, error) {
	if wait {
		deadline := time.Now().Add(5 * time.Minute)
		for {
			running, err := isRunning(paths.lockFile)
			if err != nil {
				return "", err
			}
			if !running {
				message += "\n\nCancellation complete"
				break
			}
			if time.Now().After(deadline) {
				return "", errors.New("timed out waiting for cancellation")
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	message += fmt.Sprintf("\n\nInspect status:\n  rotari show --basedir %s --project-name %s --run-id %s", paths.baseDir, queueName, runID)
	return message, nil
}

func enqueueCommand(baseDir, queueName string, command []string, executor string, executorOptions []string, jobName string, dependsOn []string) (string, error) {
	if queueName == "" || len(command) == 0 {
		return "", errors.New("project name and command are required")
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		return "", err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", err
	}
	defer release()
	if err := ensureProjectIdleForPaths(paths, "add"); err != nil {
		return "", err
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return "", err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if executor != "" && !isKnownExecutor(executor) {
		return "", fmt.Errorf("unsupported executor: %s", executor)
	}
	if executor != "" && queue.DefaultExecutor == "" {
		queue.DefaultExecutor = executor
		queue.DefaultExecutorOptions = append([]string(nil), executorOptions...)
	}
	queue.Commands = append(queue.Commands, QueuedCommand{
		ID: makeJobID(), Command: command, Executor: executor, ExecutorOptions: executorOptions, Name: jobName, DependsOn: dependsOn,
	})
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return "", err
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return "", err
	}
	return fmt.Sprintf("submitted project=%s command=%s", queueName, joinCommand(command)), nil
}

func startServerRun(baseDir, queueName, runName string, localConcurrency, batchMaxActive, retry int, executor string, executorOptions []string, selection string, jobIDs []string, sourceRunID string, onDone func(), cwd string) (string, error) {
	_, err := resolveQueueExecutor(baseDir, queueName, executor)
	if err != nil {
		return "", err
	}
	if localConcurrency < 1 {
		return "", errors.New("local concurrency must be >= 1")
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		return "", err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", err
	}
	defer release()
	if err := ensureProjectIdleForPaths(paths, "run"); err != nil {
		return "", err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if len(queue.Commands) == 0 {
		return "", fmt.Errorf("queue %q has no queued commands", queueName)
	}
	runID := makeRunID()
	if err := launchAsyncRun(paths, queueName, runID, runName, localConcurrency, batchMaxActive, retry, executor, executorOptions, selection, jobIDs, sourceRunID, cwd, onDone); err != 0 {
		return "", errors.New("queue is already running")
	}
	runDir := filepath.Join(paths.runsDir, runID)
	return fmt.Sprintf("Run started:\n  Project: %s\n  Run: %s\n  Directory: %s\n\nCheck status:\n  rotari show --basedir %s --project-name %s --run-id %s\n\nCancel run:\n  rotari cancel --basedir %s --project-name %s",
		queueName, formatRunLabel(runID, runName), runDir, paths.baseDir, queueName, runID, paths.baseDir, queueName), nil
}

func runServerSync(baseDir, queueName, runName string, localConcurrency, batchMaxActive, retry int, executor string, executorOptions []string, selection string, jobIDs []string, sourceRunID string, progress func(serverResponse), cwd string) (string, int, error) {
	resolvedExecutor, err := resolveQueueExecutor(baseDir, queueName, executor)
	if err != nil {
		return "", 1, err
	}
	if localConcurrency < 1 {
		return "", 1, errors.New("local concurrency must be >= 1")
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", 1, err
	}
	if err := os.MkdirAll(paths.projectDir, 0o755); err != nil {
		return "", 1, err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", 1, err
	}
	if err := ensureProjectIdleForPaths(paths, "run"); err != nil {
		release()
		return "", 1, err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		release()
		return "", 1, err
	}
	if len(queue.Commands) == 0 {
		release()
		return "", 1, fmt.Errorf("queue %q has no queued commands", queueName)
	}
	runID := makeRunID()
	if err := writeRunContext(paths, runID, cwd); err != nil {
		release()
		return "", 1, err
	}
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		release()
		return "", 1, fmt.Errorf("project %q is already running", queueName)
	}
	if err := registerRun(paths, runID); err != nil {
		_ = os.Remove(paths.lockFile)
		_ = os.RemoveAll(filepath.Join(paths.runsDir, runID))
		release()
		return "", 1, fmt.Errorf("failed to register run: %w", err)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		_ = os.Remove(paths.lockFile)
		release()
		return "", 1, err
	}
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		_ = os.Remove(paths.lockFile)
		release()
		return "", 1, err
	}
	release()
	if progress != nil {
		progress(serverResponse{Progress: true, Message: fmt.Sprintf("Run started: run_id=%s", runID)})
	}

	stopLoadSampling := startRunLoadSampling(paths, runID)
	exitCode := executeMixedRun(paths, runID, runName, localConcurrency, batchMaxActive, retry, resolvedExecutor, executorOptions, selection, jobIDs, sourceRunID, func(result JobResult, completed, total, succeeded, failed int) {
		if progress != nil {
			message := ""
			if result.ExitCode != 0 && result.Error == "final-failure" {
				failureTitle := "Job failed:"
				if retry > 0 {
					failureTitle = "Job failed after retry:"
				}
				message = fmt.Sprintf("%s\n  ID: %s\n  Command: %s\n  Show output:\n    rotari show --basedir %s --project-name %s --run-id %s --job-id %s",
					failureTitle,
					result.ID, strings.Join(result.Command, " "), paths.baseDir, paths.queueName, runID, result.ID)
			} else if strings.HasPrefix(result.Error, "retry:") {
				message = fmt.Sprintf("Retrying job: attempt=%s job=%s command=%v", strings.TrimPrefix(result.Error, "retry:"), result.ID, result.Command)
			}
			progress(serverResponse{OK: true, Progress: true, Message: message, JobID: result.ID, Completed: completed, Total: total, Succeeded: succeeded, Failed: failed})
		}
	})
	stopLoadSampling()
	if err := finishRunContext(paths, runID); err != nil {
		_ = os.Remove(paths.lockFile)
		return "", 1, err
	}
	if err := finishRun(paths, runID, exitCode); err != nil {
		_ = os.Remove(paths.lockFile)
		return "", 1, err
	}
	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 1, err
	}
	data, err := os.ReadFile(filepath.Join(paths.runsDir, runID, "summary.json"))
	if err == nil {
		var summary RunSummary
		if json.Unmarshal(data, &summary) == nil {
			return formatRunCompletion(paths, runID, summary), exitCode, nil
		}
	}
	return fmt.Sprintf("Run finished:\n  Project: %s\n  Run: %s\n  Exit code: %d", queueName, runID, exitCode), exitCode, nil
}

func joinCommand(command []string) string {
	return fmt.Sprintf("%v", command)
}
