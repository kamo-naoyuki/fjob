package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const jobIDLen = 9
const defaultQueueName = "default"

type Queue struct {
	DefaultExecutor        string          `json:"default_executor,omitempty"`
	DefaultExecutorOptions []string        `json:"default_executor_options,omitempty"`
	Commands               []QueuedCommand `json:"commands"`
}

type QueuedCommand struct {
	ID              string     `json:"id"`
	Command         []string   `json:"command"`
	Executor        string     `json:"executor,omitempty"`
	ExecutorOptions []string   `json:"executor_options,omitempty"`
	Name            string     `json:"name,omitempty"`
	DependsOn       []string   `json:"depends_on,omitempty"`
	Origin          *JobOrigin `json:"origin,omitempty"`
}

type JobOrigin struct {
	RunID  string `json:"run_id"`
	JobID  string `json:"job_id"`
	Status string `json:"status,omitempty"`
	CWD    string `json:"cwd,omitempty"`
}

type Meta struct {
	Phase           string `json:"phase"`
	LastRunID       string `json:"last_run_id,omitempty"`
	LastRunExitCode int    `json:"last_run_exit_code,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

type LockInfo struct {
	PID       int    `json:"pid"`
	RunID     string `json:"run_id"`
	StartedAt string `json:"started_at"`
	Host      string `json:"host,omitempty"`
}

type JobSpec struct {
	ID              string   `json:"id"`
	Command         []string `json:"command"`
	Executor        string   `json:"executor,omitempty"`
	ExecutorOptions []string `json:"executor_options,omitempty"`
	Name            string   `json:"name,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
}

type JobResult struct {
	ID       string   `json:"id"`
	ExitCode int      `json:"exit_code"`
	Error    string   `json:"error,omitempty"`
	Command  []string `json:"command,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
}

type RunSummary struct {
	RunID      string      `json:"run_id"`
	RunName    string      `json:"run_name,omitempty"`
	Status     string      `json:"status"`
	StartedAt  string      `json:"started_at"`
	FinishedAt string      `json:"finished_at"`
	ExitCode   int         `json:"exit_code"`
	Results    []JobResult `json:"results"`
}

type RunContext struct {
	CWD          string       `json:"cwd"`
	Hostname     string       `json:"hostname,omitempty"`
	StartedLoad  *LoadAverage `json:"started_load,omitempty"`
	FinishedLoad *LoadAverage `json:"finished_load,omitempty"`
}

type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

func runStatus(exitCode int) string {
	if exitCode == 0 {
		return "finished"
	}
	return "failed"
}

func formatRunLabel(runID, runName string) string {
	if runName == "" {
		return runID
	}
	return fmt.Sprintf("%s (%s)", runName, runID)
}

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 1
	}
	if args[0] == "--version" || args[0] == "version" {
		printVersion()
		return 0
	}

	switch args[0] {
	case "check":
		return cmdCheck(args[1:])
	case "cancel":
		return cmdCancel(args[1:])
	case "suspend":
		return cmdJobSignal(args[1:], "suspend")
	case "resume":
		return cmdJobSignal(args[1:], "resume")
	case "delete", "clear":
		return cmdDelete(args[1:])
	case "unlock":
		return cmdUnlock(args[1:])
	case "change":
		return cmdChange(args[1:])
	case "remove":
		return cmdRemove(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "retry":
		return cmdRetry(args[1:])
	case "add":
		return cmdAdd(args[1:])
	case "copy":
		return cmdCopy(args[1:])
	case "server":
		return cmdServer(args[1:])
	case "web":
		return cmdWeb(args[1:])
	case "completion":
		return cmdCompletion(args[1:])
	case "__complete":
		return cmdComplete(args[1:])
	case "__server":
		return cmdServerProcess(args[1:])
	case "__worker-run":
		return cmdWorkerRun(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		printUsage()
		return 1
	}
}

func printUsage() {
	fmt.Println("rotari: lightweight local job queue")
	fmt.Println("")
	fmt.Println("Usage:")
	for _, command := range cliCommandSpecs {
		fmt.Printf("  %s\n", command.Usage)
	}
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	serverRequired := cliBool(fs, "server", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("check"))
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
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
		return 1
	}
	running, err := isRunning(paths.lockFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to check queue: %v\n", err)
		return 1
	}
	if running {
		meta, metaErr := loadMeta(paths.metaFile)
		if metaErr == nil && meta.Phase == "cancelling" {
			if !waitForCancellation(paths, queueName) {
				return 1
			}
			running = false
		}
	}
	if running {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf("queue '%s' is running; new jobs are not allowed", queueName)))
		fmt.Fprintf(os.Stderr, "cancel with: rotari cancel --basedir %s --queue-name %s\n", paths.baseDir, queueName)
		return 1
	}
	if *serverRequired {
		response, err := sendServerRequest(paths.baseDir, serverRequest{Op: "ping"})
		if err != nil || !response.OK {
			fmt.Fprintf(os.Stderr, "queue '%s' is available, but server is not running\n", queueName)
			return 1
		}
		fmt.Printf("queue '%s' is available; server is running pid=%d\n", queueName, response.PID)
		return 0
	}
	fmt.Printf("%s\n", green(fmt.Sprintf("queue '%s' is available", queueName)))
	return 0
}

const cancellationWaitTimeout = 5 * time.Minute

func waitForCancellation(paths pathSet, queueName string) bool {
	fmt.Println(cyan(fmt.Sprintf("queue '%s' is cancelling", queueName)))
	fmt.Println(cyan("Waiting for jobs to stop..."))
	deadline := time.Now().Add(cancellationWaitTimeout)
	for {
		running, err := isRunning(paths.lockFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to check queue: %v\n", err)
			return false
		}
		if !running {
			fmt.Println(cyan("Cancellation complete"))
			return true
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "cancellation is still in progress")
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cmdWorkerRun(args []string) int {
	fs := flag.NewFlagSet("__worker-run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	basedir := cliString(fs, "basedir", "")
	executor := cliString(fs, "executor", "")
	var executorOptions stringSliceFlag
	cliValue(fs, &executorOptions, "executor-option")
	selection := cliString(fs, "selection", "")
	var jobIDs stringSliceFlag
	cliValue(fs, &jobIDs, "job-id")
	sourceRunID := cliString(fs, "source-run-id", "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse worker args: %v\n", err)
		return 1
	}
	left := fs.Args()
	if len(left) != 7 {
		fmt.Fprintln(os.Stderr, "usage: rotari __worker-run [--basedir DIR] <queue_name> <run_id> <run_name> <local_concurrency> <batch_max_active> <retry> <cwd>")
		return 1
	}
	queueName := left[0]
	runID := left[1]
	runName := left[2]
	localConcurrency, err := strconv.Atoi(left[3])
	batchMaxActive, batchErr := strconv.Atoi(left[4])
	retry, retryErr := strconv.Atoi(left[5])
	cwd := left[6]
	if err != nil || batchErr != nil || retryErr != nil || localConcurrency < 1 || batchMaxActive < 1 || retry < -1 {
		fmt.Fprintf(os.Stderr, "invalid run options: local=%s batch=%s retry=%s\n", left[2], left[3], left[4])
		return 1
	}

	paths, err := resolvePaths(*basedir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
		return 1
	}
	if err := writeRunContext(paths, runID, cwd); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save run context: %v\n", err)
		return 1
	}

	meta, _ := loadMeta(paths.metaFile)
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		fmt.Fprintf(os.Stderr, "failed to update metadata: %v\n", err)
		return 1
	}

	exitCode := executeMixedRun(paths, runID, runName, localConcurrency, batchMaxActive, retry, *executor, executorOptions, *selection, jobIDs, *sourceRunID, nil)
	if err := finishRunContext(paths, runID); err != nil {
		fmt.Fprintf(os.Stderr, "failed to save run context: %v\n", err)
		return 1
	}
	if err := finishRun(paths, runID, exitCode); err != nil {
		fmt.Fprintf(os.Stderr, "failed to finalize metadata: %v\n", err)
		return 1
	}

	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "failed to remove lock file: %v\n", err)
	}

	return exitCode
}

func finishRun(paths pathSet, runID string, exitCode int) error {
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()

	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return fmt.Errorf("failed to load queue: %w", err)
	}
	queue.Commands = nil
	if err := writeJSON(paths.queueFile, queue); err != nil {
		return fmt.Errorf("failed to clear queue: %w", err)
	}

	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}
	meta.Phase = "finished"
	meta.LastRunID = runID
	meta.LastRunExitCode = exitCode
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return fmt.Errorf("failed to finalize metadata: %w", err)
	}
	return nil
}

func launchAsyncRun(paths pathSet, queueName, runID, runName string, localConcurrency, batchMaxActive, retry int, executor string, executorOptions []string, selection string, jobIDs []string, sourceRunID string, cwd string) int {
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		fmt.Fprintf(os.Stderr, "queue '%s' is running; run is not allowed: %v\n", queueName, err)
		return 1
	}

	meta, _ := loadMeta(paths.metaFile)
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to update metadata: %v\n", err)
		return 1
	}
	if err := writeRunContext(paths, runID, cwd); err != nil {
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to save run context: %v\n", err)
		return 1
	}

	exe, err := os.Executable()
	if err != nil {
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to detect executable path: %v\n", err)
		return 1
	}

	childArgs := []string{"__worker-run"}
	if paths.baseDirExplicit {
		childArgs = append(childArgs, "--basedir", paths.baseDir)
	}
	if executor != "" {
		childArgs = append(childArgs, "--executor", executor)
	}
	for _, option := range executorOptions {
		childArgs = append(childArgs, "--executor-option", option)
	}
	if selection != "" {
		childArgs = append(childArgs, "--selection", selection)
	}
	for _, jobID := range jobIDs {
		childArgs = append(childArgs, "--job-id", jobID)
	}
	if sourceRunID != "" {
		childArgs = append(childArgs, "--source-run-id", sourceRunID)
	}
	childArgs = append(childArgs, queueName, runID, runName, strconv.Itoa(localConcurrency), strconv.Itoa(batchMaxActive), strconv.Itoa(retry), cwd)

	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to launch async runner: %v\n", err)
		return 1
	}

	host, err := os.Hostname()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to determine lock host: %v\n", err)
		return 1
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: cmd.Process.Pid, RunID: runID, StartedAt: nowRFC3339(), Host: host}); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to update lock with child pid: %v\n", err)
		return 1
	}

	fmt.Printf("submitted queue=%s run_id=%s pid=%d\n", queueName, runID, cmd.Process.Pid)
	return 0
}

func writeRunContext(paths pathSet, runID, cwd string) error {
	return writeJSON(filepath.Join(paths.runsDir, runID, "context.json"), captureRunContext(cwd))
}

func finishRunContext(paths pathSet, runID string) error {
	path := filepath.Join(paths.runsDir, runID, "context.json")
	context := RunContext{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &context)
	}
	context.FinishedLoad = readLoadAverage()
	return writeJSON(path, context)
}

func captureRunContext(cwd string) RunContext {
	hostname, _ := os.Hostname()
	return RunContext{CWD: cwd, Hostname: hostname, StartedLoad: readLoadAverage()}
}

func readLoadAverage() *LoadAverage {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil
	}
	one, oneErr := strconv.ParseFloat(fields[0], 64)
	five, fiveErr := strconv.ParseFloat(fields[1], 64)
	fifteen, fifteenErr := strconv.ParseFloat(fields[2], 64)
	if oneErr != nil || fiveErr != nil || fifteenErr != nil {
		return nil
	}
	return &LoadAverage{One: one, Five: five, Fifteen: fifteen}
}

func executeRun(paths pathSet, runID string, numParallel int) int {
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load queue: %v\n", err)
		return 1
	}
	if len(queue.Commands) == 0 {
		fmt.Fprintf(os.Stderr, "queue '%s' has no queued commands\n", paths.queueName)
		return 1
	}

	runDir := filepath.Join(paths.runsDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create run directory: %v\n", err)
		return 1
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write command snapshot: %v\n", err)
		return 1
	}

	jobs := queueToJobs(queue.Commands)
	if len(jobs) == 0 {
		fmt.Fprintf(os.Stderr, "queue '%s' has no valid commands\n", paths.queueName)
		return 1
	}

	startedAt := nowRFC3339()
	sem := make(chan struct{}, numParallel)
	results := make(chan JobResult, len(jobs))
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(j JobSpec) {
			defer wg.Done()
			sem <- struct{}{}
			result := runOneJob(runDir, j)
			<-sem
			results <- result
		}(job)
	}

	wg.Wait()
	close(results)

	summary := RunSummary{
		RunID:      runID,
		Status:     "finished",
		StartedAt:  startedAt,
		FinishedAt: nowRFC3339(),
		ExitCode:   0,
		Results:    make([]JobResult, 0, len(jobs)),
	}
	for r := range results {
		summary.Results = append(summary.Results, r)
		if r.ExitCode != 0 {
			summary.ExitCode = 1
		}
	}
	summary.Status = runStatus(summary.ExitCode)
	successCount := 0
	failedCount := 0
	for _, result := range summary.Results {
		if result.ExitCode == 0 {
			successCount++
		} else {
			failedCount++
		}
	}

	if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write summary: %v\n", err)
		return 1
	}

	if summary.ExitCode == 0 {
		fmt.Printf("run finished run_id=%s success=%d failed=%d dir=%s\n", runID, successCount, failedCount, runDir)
	} else {
		fmt.Printf("run finished run_id=%s success=%d failed=%d dir=%s\n", runID, successCount, failedCount, runDir)
		printFailedJobHints(paths, runID, summary.Results)
	}
	return summary.ExitCode
}

func printFailedJobHints(paths pathSet, runID string, results []JobResult) {
	if hints := failedJobHints(paths, runID, results); hints != "" {
		fmt.Print(hints)
	}
}

func failedJobHints(paths pathSet, runID string, results []JobResult) string {
	var hints strings.Builder
	seen := make(map[string]bool)
	for _, result := range results {
		if result.ExitCode == 0 || seen[result.ID] {
			continue
		}
		seen[result.ID] = true
		hosts := strings.Join(result.Hosts, ",")
		if hosts == "" {
			hosts = "-"
		}
		if hints.Len() > 0 {
			hints.WriteString("  ----\n")
		}
		fmt.Fprintf(&hints, "  Job: %s\n  Hosts: %s\n  Command: %s\n  Show output:\n    rotari show --basedir %s --queue-name %s --run-id %s --job-id %s\n",
			result.ID, hosts, strings.Join(result.Command, " "), paths.baseDir, paths.queueName, runID, result.ID)
	}
	return hints.String()
}

func runOneJob(runDir string, job JobSpec) JobResult {
	hostname, _ := os.Hostname()
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}

	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}
	if job.Name != "" {
		_ = os.WriteFile(filepath.Join(jobDir, "name"), []byte(job.Name+"\n"), 0o644)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte(nowRFC3339()+"\n"), 0o644); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}
	if jobCancellationRequested(jobDir) {
		return recordCancelledJob(jobDir, job)
	}

	logPath := filepath.Join(jobDir, "output")
	logf, err := os.Create(logPath)
	if err != nil {
		return JobResult{ID: job.ID, ExitCode: 1, Error: err.Error()}
	}
	defer logf.Close()

	if len(job.Command) == 0 {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: "empty command"}
	}

	cmd := exec.Command(job.Command[0], job.Command[1:]...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte("1\n"), 0o644)
		_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), 0o644)
		fmt.Printf("fail job=%s command=%s error=%v\n", job.ID, strings.Join(job.Command, " "), err)
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}

	_ = os.WriteFile(filepath.Join(jobDir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644)
	fmt.Printf("[%s] submit job=%s pid=%d command=%s\n", nowRFC3339(), job.ID, cmd.Process.Pid, strings.Join(job.Command, " "))

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte(strconv.Itoa(exitCode)+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), 0o644)

	if exitCode == 0 {
		fmt.Printf("%s\n", green(fmt.Sprintf("success job=%s", job.ID)))
	} else {
		fmt.Printf("%s\n", red(fmt.Sprintf("fail job=%s exit=%d command=%s", job.ID, exitCode, strings.Join(job.Command, " "))))
	}

	return JobResult{ID: job.ID, Command: job.Command, ExitCode: exitCode, Hosts: []string{hostname}}
}

func jobCancellationRequested(jobDir string) bool {
	_, err := os.Stat(filepath.Join(jobDir, "cancelled"))
	return err == nil
}

func recordCancelledJob(jobDir string, job JobSpec) JobResult {
	message := "cancelled before start"
	_ = os.MkdirAll(jobDir, 0o755)
	_ = writeJSON(filepath.Join(jobDir, "command.json"), job)
	if job.Name != "" {
		_ = os.WriteFile(filepath.Join(jobDir, "name"), []byte(job.Name+"\n"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte(nowRFC3339()+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(jobDir, "output"), []byte(message+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte("143\n"), 0o644)
	_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), 0o644)
	return JobResult{ID: job.ID, Command: job.Command, ExitCode: 143, Error: message}
}

func queueToJobs(commands []QueuedCommand) []JobSpec {
	jobs := make([]JobSpec, 0, len(commands))
	for _, queued := range commands {
		if len(queued.Command) == 0 {
			continue
		}
		jobs = append(jobs, JobSpec{
			ID: queued.ID, Command: queued.Command, Name: queued.Name,
			Executor: queued.Executor, ExecutorOptions: queued.ExecutorOptions, DependsOn: queued.DependsOn,
		})
	}
	return jobs
}

type pathSet struct {
	baseDir         string
	baseDirExplicit bool
	queueName       string
	queueDir        string
	queueFile       string
	metaFile        string
	stateLockFile   string
	lockFile        string
	runsDir         string
}

func resolvePaths(cliBaseDir, queueName string) (pathSet, error) {
	baseDir, explicit, err := resolveBaseDir(cliBaseDir)
	if err != nil {
		return pathSet{}, err
	}
	queueDir := filepath.Join(baseDir, "queues", queueName)
	return pathSet{
		baseDir:         baseDir,
		baseDirExplicit: explicit,
		queueName:       queueName,
		queueDir:        queueDir,
		queueFile:       filepath.Join(queueDir, "queue.json"),
		metaFile:        filepath.Join(queueDir, "meta.json"),
		stateLockFile:   filepath.Join(queueDir, "state.lock"),
		lockFile:        filepath.Join(queueDir, "running.lock"),
		runsDir:         filepath.Join(queueDir, "runs"),
	}, nil
}

func resolveBaseDir(cliBaseDir string) (string, bool, error) {
	if cliBaseDir != "" {
		return cliBaseDir, true, nil
	}
	if v := os.Getenv("ROTARI_BASEDIR"); v != "" {
		return v, true, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		localState := filepath.Join(cwd, ".rotari-state")
		if info, err := os.Stat(localState); err == nil && info.IsDir() {
			return localState, false, nil
		}
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "rotari"), false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	return filepath.Join(home, ".local", "state", "rotari"), false, nil
}

func resolveQueueName(baseDir string, cliQueueName string) (string, error) {
	if cliQueueName != "" {
		return cliQueueName, nil
	}
	if value := os.Getenv("ROTARI_QUEUE_NAME"); value != "" {
		return value, nil
	}
	queuesDir := filepath.Join(baseDir, "queues")
	entries, err := os.ReadDir(queuesDir)
	if err == nil {
		var available []string
		for _, entry := range entries {
			if entry.IsDir() {
				available = append(available, entry.Name())
			}
		}
		if len(available) == 1 {
			return available[0], nil
		}
		if len(available) > 1 {
			sort.Strings(available)
			var list []string
			for _, q := range available {
				list = append(list, "  - "+q)
			}
			return "", fmt.Errorf("multiple queues exist, please specify one with --queue-name or ROTARI_QUEUE_NAME:\n%s", strings.Join(list, "\n"))
		}
	}
	return defaultQueueName, nil
}

func defaultMeta() Meta {
	return Meta{Phase: "collecting", UpdatedAt: nowRFC3339()}
}

func loadMeta(path string) (Meta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultMeta(), nil
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return Meta{}, err
	}
	if m.Phase == "" {
		m = defaultMeta()
	}
	return m, nil
}

func loadQueue(path string) (Queue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Queue{}, nil
		}
		return Queue{}, err
	}
	var q Queue
	if err := json.Unmarshal(b, &q); err != nil {
		return Queue{}, err
	}
	return q, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rotari-tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

const stateLockTimeout = 30 * time.Second

func acquireStateLock(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(stateLockTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("timed out waiting %s for state lock", stateLockTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func acquireLock(lockPath string, info LockInfo) error {
	if info.Host == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("determine lock host: %w", err)
		}
		info.Host = host
	}
	running, err := isRunning(lockPath)
	if err != nil {
		return err
	}
	if running {
		return errors.New("active lock exists")
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("active lock exists")
		}
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(info)
}

func isRunning(lockPath string) (bool, error) {
	lock, err := loadLockInfo(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return false, removeErr
		}
		return false, nil
	}

	localHost, err := os.Hostname()
	if err != nil {
		return false, fmt.Errorf("determine local host: %w", err)
	}
	if lock.Host == "" || lock.Host != localHost {
		return true, nil
	}
	if lock.PID <= 0 || !processAlive(lock.PID) {
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return false, removeErr
		}
		return false, nil
	}

	return true, nil
}

func loadLockInfo(lockPath string) (LockInfo, error) {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return LockInfo{}, err
	}
	var lock LockInfo
	if err := json.Unmarshal(b, &lock); err != nil {
		return LockInfo{}, err
	}
	return lock, nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func makeRunID() string {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("failed to generate run id: %v", err))
	}
	timestamp := time.Now().UTC().Format("20060102-150405")
	return fmt.Sprintf("%s-%08x", timestamp, value)
}

func makeJobID() string {
	var value [5]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("failed to generate job id: %v", err))
	}
	return hex.EncodeToString(value[:])[:jobIDLen]
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
