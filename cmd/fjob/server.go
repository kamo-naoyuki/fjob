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
	QueueName        string   `json:"queue_name,omitempty"`
	Command          []string `json:"command,omitempty"`
	LocalConcurrency int      `json:"local_concurrency,omitempty"`
	SlurmMaxActive   int      `json:"slurm_max_active,omitempty"`
	Retry            int      `json:"retry,omitempty"`
	Wait             bool     `json:"wait,omitempty"`
	Async            bool     `json:"async,omitempty"`
	Backend          string   `json:"backend,omitempty"`
	SbatchOptions    []string `json:"sbatch_options,omitempty"`
	JobName          string   `json:"job_name,omitempty"`
}

type serverResponse struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	PID       int    `json:"pid,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Progress  bool   `json:"progress,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	Completed int    `json:"completed,omitempty"`
	Total     int    `json:"total,omitempty"`
	Succeeded int    `json:"succeeded,omitempty"`
	Failed    int    `json:"failed,omitempty"`
}

type fjobServer struct {
	listener   net.Listener
	stopped    chan struct{}
	stopOnce   sync.Once
	accessMu   sync.Mutex
	lastAccess time.Time
	activeRuns int
}

const serverIdleTimeout = 5 * time.Minute

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
		return nil
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

func cmdSubmit(args []string) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	backend := cliString(fs, "backend", "")
	var sbatchOptions stringSliceFlag
	cliValue(fs, &sbatchOptions, "sbatch-option")
	jobName := cliString(fs, "job-name", "")
	cliStringVar(fs, jobName, "name", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	left := fs.Args()
	if len(left) < 1 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("submit"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveQueueName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	message, err := enqueueCommand(baseDir, queueName, left, *backend, sbatchOptions, *jobName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cyan(message))
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	localConcurrency := cliInt(fs, "local-concurrency", 8)
	slurmMaxActive := cliInt(fs, "slurm-max-active", 8)
	retry := cliInt(fs, "retry", 0)
	failed := cliBool(fs, "failed", false)
	unfinished := cliBool(fs, "unfinished", false)
	success := cliBool(fs, "success", false)
	nonsuccess := cliBool(fs, "nonsuccess", false)
	async := cliBool(fs, "async", false)
	backend := cliString(fs, "backend", "")
	var sbatchOptions stringSliceFlag
	cliValue(fs, &sbatchOptions, "sbatch-option")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	left := fs.Args()
	selection := ""
	for name, enabled := range map[string]bool{"failed": *failed, "unfinished": *unfinished, "success": *success, "nonsuccess": *nonsuccess} {
		if enabled {
			if selection != "" {
				fmt.Fprintln(os.Stderr, "only one of --failed, --unfinished, --success, --nonsuccess may be used")
				return 1
			}
			selection = name
		}
	}
	if len(left) != 0 || *localConcurrency < 1 || *slurmMaxActive < 1 || *retry < -1 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("run"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveQueueName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if selection != "" {
		if _, err := prepareRunSelection(baseDir, queueName, selection); err != nil {
			if errors.Is(err, errNoPreviousRun) {
				fmt.Fprintf(os.Stderr, "queue %q has no previous run; ignoring --%s\n", queueName, selection)
				selection = ""
			} else {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				fmt.Fprintf(os.Stderr, "inspect latest run with: fjob show --basedir %s --queue-name %s\n", baseDir, queueName)
				return 1
			}
		}
	}
	if err := ensureServer(baseDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%s\n  Base directory: %s\n  Queue: %s\n", green("Run started:"), baseDir, queueName)
	request := serverRequest{
		Op: "run", QueueName: queueName, LocalConcurrency: *localConcurrency, SlurmMaxActive: *slurmMaxActive, Retry: *retry, Async: *async,
		Backend: *backend, SbatchOptions: sbatchOptions,
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

	server := &fjobServer{listener: listener, stopped: make(chan struct{}), lastAccess: time.Now()}
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

func (server *fjobServer) isStopped() bool {
	select {
	case <-server.stopped:
		return true
	default:
		return false
	}
}

func (server *fjobServer) stop() {
	server.stopOnce.Do(func() {
		close(server.stopped)
		_ = server.listener.Close()
	})
}

func (server *fjobServer) touch() {
	server.accessMu.Lock()
	server.lastAccess = time.Now()
	server.accessMu.Unlock()
}

func (server *fjobServer) idleChecker(masterDir, baseDir string) {
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

func (server *fjobServer) handle(baseDir string, conn net.Conn) {
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
		response = serverResponse{OK: true, PID: os.Getpid()}
	case "submit":
		message, err := enqueueCommand(baseDir, request.QueueName, request.Command, request.Backend, request.SbatchOptions, request.JobName)
		response = serverResponse{OK: err == nil, Message: message}
		if err != nil {
			response.Message = err.Error()
		}
	case "cancel":
		message, err := cancelQueue(baseDir, request.QueueName, request.Wait)
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
			message, err = startServerRun(baseDir, request.QueueName, request.LocalConcurrency, request.SlurmMaxActive, request.Retry, request.Backend, request.SbatchOptions, onDone)
			if err != nil && onDone != nil {
				onDone()
			}
		} else {
			server.beginRun()
			defer server.endRun()
			message, exitCode, err = runServerSyncWithDisconnect(conn, baseDir, request.QueueName, request.LocalConcurrency, request.SlurmMaxActive, request.Retry, request.Backend, request.SbatchOptions, func(progress serverResponse) {
				_ = encoder.Encode(progress)
			})
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

func runServerSyncWithDisconnect(conn net.Conn, baseDir, queueName string, localConcurrency, slurmMaxActive, retry int, backend string, sbatchOptions []string, progress func(serverResponse)) (string, int, error) {
	type result struct {
		message  string
		exitCode int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		message, exitCode, err := runServerSync(baseDir, queueName, localConcurrency, slurmMaxActive, retry, backend, sbatchOptions, progress)
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
	queueNameOption := cliString(fs, "queue-name", "")
	wait := cliBool(fs, "wait", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("cancel"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveQueueName(baseDir, *queueNameOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ensureServer(baseDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	response, err := sendServerRequest(baseDir, serverRequest{Op: "cancel", QueueName: queueName, Wait: *wait})
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

func (server *fjobServer) beginRun() {
	server.accessMu.Lock()
	server.activeRuns++
	server.lastAccess = time.Now()
	server.accessMu.Unlock()
}

func (server *fjobServer) endRun() {
	server.accessMu.Lock()
	server.activeRuns--
	server.lastAccess = time.Now()
	shouldStop := server.activeRuns == 0
	server.accessMu.Unlock()
	if shouldStop {
		server.stop()
	}
}

func (server *fjobServer) isBusy() bool {
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
	lastCompleted, lastSucceeded, lastFailed := -1, -1, -1
	for {
		var response serverResponse
		if err := decoder.Decode(&response); err != nil {
			return serverResponse{}, err
		}
		if response.Progress {
			if response.Message != "" {
				if strings.HasPrefix(response.Message, "Job failed") {
					fmt.Printf("%s\n", red(response.Message))
				} else if strings.HasPrefix(response.Message, "Retrying job") {
					fmt.Printf("%s\n", yellow(response.Message))
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

func resolveQueueBackend(baseDir, queueName, requested string) (string, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if requested == "" {
		requested = queue.DefaultBackend
		if requested == "" {
			requested = "local"
		}
	}
	if requested != "local" && requested != "slurm" {
		return "", fmt.Errorf("unsupported backend: %s", requested)
	}
	resolved := requested
	for _, queued := range queue.Commands {
		backend := queued.Backend
		if backend == "" {
			backend = requested
		}
		if backend != "local" && backend != "slurm" {
			return "", fmt.Errorf("unsupported backend: %s", backend)
		}
	}
	return resolved, nil
}

func cancelQueue(baseDir, queueName string, wait bool) (string, error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(paths.lockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("queue %q is not running", queueName)
		}
		return "", err
	}
	var lock LockInfo
	if err := json.Unmarshal(data, &lock); err != nil {
		return "", fmt.Errorf("invalid running lock: %w", err)
	}
	runDir := filepath.Join(paths.runsDir, lock.RunID)
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
		return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Queue: %s\n  Run: %s\n  Slurm jobs: %d", queueName, lock.RunID, len(jobs)), paths, queueName, lock.RunID, wait)
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
		return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Queue: %s\n  Run: %s\n  Local jobs: %d", queueName, lock.RunID, cancelled), paths, queueName, lock.RunID, wait)
	}
	if err := syscall.Kill(-lock.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", fmt.Errorf("cancel local worker: %w", err)
	}
	return finishCancelMessage(fmt.Sprintf("Cancel requested\n  Queue: %s\n  Run: %s\n  Worker PID: %d", queueName, lock.RunID, lock.PID), paths, queueName, lock.RunID, wait)
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
	message += fmt.Sprintf("\n\nInspect status:\n  fjob show --basedir %s --queue-name %s --run-id %s", paths.baseDir, queueName, runID)
	return message, nil
}

func enqueueCommand(baseDir, queueName string, command []string, backend string, sbatchOptions []string, jobName string) (string, error) {
	if queueName == "" || len(command) == 0 {
		return "", errors.New("queue name and command are required")
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		return "", err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", err
	}
	defer release()
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return "", err
	}
	if running {
		return "", fmt.Errorf("queue %q is running", queueName)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return "", err
	}
	if meta.Phase == "finished" {
		if err := cleanupHistory(paths.queueDir); err != nil {
			return "", err
		}
		meta = defaultMeta()
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if backend != "" && backend != "local" && backend != "slurm" {
		return "", fmt.Errorf("unsupported backend: %s", backend)
	}
	if backend != "" && queue.DefaultBackend == "" {
		queue.DefaultBackend = backend
		queue.DefaultSbatchOptions = append([]string(nil), sbatchOptions...)
	}
	queue.Commands = append(queue.Commands, QueuedCommand{
		Command: command, Backend: backend, SbatchOptions: sbatchOptions, Name: jobName,
	})
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return "", err
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return "", err
	}
	return fmt.Sprintf("submitted queue=%s command=%s", queueName, joinCommand(command)), nil
}

func startServerRun(baseDir, queueName string, localConcurrency, slurmMaxActive, retry int, backend string, sbatchOptions []string, onDone func()) (string, error) {
	_, err := resolveQueueBackend(baseDir, queueName, backend)
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		return "", err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return "", err
	}
	defer release()
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return "", err
	}
	if len(queue.Commands) == 0 {
		return "", fmt.Errorf("queue %q has no queued commands", queueName)
	}
	runID := makeRunID()
	if err := launchAsyncRun(paths, queueName, runID, localConcurrency, slurmMaxActive, retry, backend, sbatchOptions); err != 0 {
		return "", errors.New("queue is already running")
	}
	runDir := filepath.Join(paths.runsDir, runID)
	return fmt.Sprintf("Run started:\n  Queue: %s\n  Run: %s\n  Directory: %s\n\nCheck status:\n  fjob show --basedir %s --queue-name %s --run-id %s\n\nCancel run:\n  fjob cancel --basedir %s --queue-name %s",
		queueName, runID, runDir, paths.baseDir, queueName, runID, paths.baseDir, queueName), nil
}

func runServerSync(baseDir, queueName string, localConcurrency, slurmMaxActive, retry int, backend string, sbatchOptions []string, progress func(serverResponse)) (string, int, error) {
	resolvedBackend, err := resolveQueueBackend(baseDir, queueName, backend)
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
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		return "", 1, err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
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
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		release()
		return "", 1, fmt.Errorf("queue %q is already running", queueName)
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

	exitCode := executeMixedRun(paths, runID, localConcurrency, slurmMaxActive, retry, resolvedBackend, sbatchOptions, func(result JobResult, completed, total, succeeded, failed int) {
		if progress != nil {
			message := ""
			if result.ExitCode != 0 && retry == 0 {
				message = fmt.Sprintf("Job failed:\n  ID: %s\n  Command: %s\n  Show output:\n    fjob show --basedir %s --queue-name %s --run-id %s --job-id %s",
					result.ID, strings.Join(result.Command, " "), paths.baseDir, paths.queueName, runID, result.ID)
			} else if strings.HasPrefix(result.Error, "retry:") {
				message = fmt.Sprintf("Retrying job: attempt=%s job=%s command=%v", strings.TrimPrefix(result.Error, "retry:"), result.ID, result.Command)
			}
			progress(serverResponse{OK: true, Progress: true, Message: message, JobID: result.ID, Completed: completed, Total: total, Succeeded: succeeded, Failed: failed})
		}
	})
	meta.Phase = "finished"
	meta.LastRunID = runID
	meta.LastRunExitCode = exitCode
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
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
	return fmt.Sprintf("Run finished:\n  Queue: %s\n  Run: %s\n  Exit code: %d", queueName, runID, exitCode), exitCode, nil
}

func joinCommand(command []string) string {
	return fmt.Sprintf("%v", command)
}
