package main

import (
	"bufio"
	"bytes"
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
const defaultProjectName = "default"

type Queue struct {
	DefaultExecutor        string          `json:"default_executor,omitempty"`
	DefaultExecutorOptions []string        `json:"default_executor_options,omitempty"`
	Commands               []QueuedCommand `json:"commands"`
}

type QueuedCommand struct {
	ID               string     `json:"id"`
	Command          []string   `json:"command"`
	WorkingDirectory string     `json:"working_directory,omitempty"`
	Executor         string     `json:"executor,omitempty"`
	ExecutorOptions  []string   `json:"executor_options,omitempty"`
	Environment      []string   `json:"environment,omitempty"`
	Name             string     `json:"name,omitempty"`
	DependsOn        []string   `json:"depends_on,omitempty"`
	Origin           *JobOrigin `json:"origin,omitempty"`
	Array            *ArraySpec `json:"array,omitempty"`
	// TaskOrigins records, per expanded task ID (e.g. "id-1"), the origin of
	// array tasks carried forward individually (see planArrayTaskSelection);
	// Origin above only covers the whole (unexpanded) command.
	TaskOrigins map[string]*JobOrigin `json:"task_origins,omitempty"`
}

type ArraySpec struct {
	First int   `json:"first"`
	Last  int   `json:"last"`
	Tasks []int `json:"tasks,omitempty"`
}

type JobOrigin struct {
	RunID       string `json:"run_id"`
	JobID       string `json:"job_id"`
	Status      string `json:"status,omitempty"`
	CWD         string `json:"cwd,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
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
	ID               string   `json:"id"`
	Command          []string `json:"command"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	Executor         string   `json:"executor,omitempty"`
	ExecutorOptions  []string `json:"executor_options,omitempty"`
	Name             string   `json:"name,omitempty"`
	DependsOn        []string `json:"depends_on,omitempty"`
	ArrayGroup       string   `json:"array_group,omitempty"`
	ArrayTaskID      *int     `json:"array_task_id,omitempty"`
	ArrayFirst       int      `json:"array_first,omitempty"`
	ArrayLast        int      `json:"array_last,omitempty"`
	ArraySize        int      `json:"array_size,omitempty"`
	Environment      []string `json:"environment,omitempty"`
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
	LoadSamples  []LoadSample `json:"load_samples,omitempty"`
}

type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

type LoadSample struct {
	At string `json:"at"`
	LoadAverage
}

func runStatus(exitCode int) string {
	if exitCode == 0 {
		return "finished"
	}
	return "failed"
}

func parseArrayRange(value string) (ArraySpec, error) {
	values := strings.Split(value, ",")
	if len(values) == 1 && strings.TrimSpace(values[0]) == "" {
		return ArraySpec{}, fmt.Errorf("want FIRST-LAST or TASK[,TASK...]")
	}
	tasks := make([]int, 0, len(values))
	seen := make(map[int]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return ArraySpec{}, fmt.Errorf("empty task index")
		}
		parts := strings.Split(value, "-")
		if len(parts) > 2 {
			return ArraySpec{}, fmt.Errorf("invalid task range %q", value)
		}
		first, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return ArraySpec{}, fmt.Errorf("invalid task index: %w", err)
		}
		last := first
		if len(parts) == 2 {
			last, err = strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return ArraySpec{}, fmt.Errorf("invalid last index: %w", err)
			}
			if first > last {
				return ArraySpec{}, errors.New("first index must not be greater than last index")
			}
		}
		for task := first; task <= last; task++ {
			if seen[task] {
				return ArraySpec{}, fmt.Errorf("duplicate task index: %d", task)
			}
			seen[task] = true
			tasks = append(tasks, task)
		}
	}
	sort.Ints(tasks)
	array := ArraySpec{First: tasks[0], Last: tasks[len(tasks)-1]}
	if len(tasks) != array.Last-array.First+1 {
		array.Tasks = tasks
	}
	return array, nil
}

func arrayTaskIDs(array *ArraySpec) []int {
	if array == nil {
		return nil
	}
	if len(array.Tasks) > 0 {
		return array.Tasks
	}
	tasks := make([]int, 0, array.Last-array.First+1)
	for task := array.First; task <= array.Last; task++ {
		tasks = append(tasks, task)
	}
	return tasks
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
	case "reset":
		return cmdReset(args[1:])
	case "cancel":
		return cmdCancel(args[1:])
	case "suspend":
		return cmdJobSignal(args[1:], "suspend")
	case "resume":
		return cmdJobSignal(args[1:], "resume")
	case "delete":
		return cmdDelete(args[1:])
	case "gc":
		return cmdGC(args[1:])
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
	case "env":
		return cmdEnvironment(args[1:])
	case "completion":
		return cmdCompletion(args[1:])
	case "__complete":
		return cmdComplete(args[1:])
	case "__server":
		return cmdServerProcess(args[1:])
	case "__worker-run":
		return cmdWorkerRun(args[1:])
	default:
		printErrorf("unknown subcommand: %s", args[0])
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

func recoverInterruptedProject(paths pathSet, runID string, discardQueue bool) error {
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()
	state, currentRunID, err := inspectProjectRunState(paths)
	if err != nil {
		return err
	}
	if state != projectInterrupted || currentRunID != runID {
		return fmt.Errorf("project %q no longer has interrupted run %q", paths.queueName, runID)
	}
	if discardQueue {
		queue, err := loadQueue(paths.queueFile)
		if err != nil {
			return err
		}
		queue.Commands = nil
		if err := writeJSON(paths.queueFile, queue); err != nil {
			return err
		}
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return err
	}
	meta.Phase = "collecting"
	meta.UpdatedAt = nowRFC3339()
	return writeJSON(paths.metaFile, meta)
}

func formatProjectRunningError(paths pathSet, runID string) string {
	return fmt.Sprintf("%s\n  Run: %s\n\nWait for completion:\n  rotari wait --basedir %s --project-name %s --run-id %s\n\nCancel run:\n  rotari cancel --basedir %s --project-name %s\n",
		redError(fmt.Sprintf("project '%s' is running; new jobs are not allowed", paths.queueName)),
		runID, paths.baseDir, paths.queueName, runID, paths.baseDir, paths.queueName)
}

const cancellationWaitTimeout = 5 * time.Minute

func waitForCancellation(paths pathSet, projectName string) bool {
	fmt.Println(yellow(fmt.Sprintf("project '%s' is cancelling", projectName)))
	fmt.Println(yellow("Waiting for cancellation to finish..."))
	deadline := time.Now().Add(cancellationWaitTimeout)
	for {
		if finalized, err := finalizeCompletedCancellation(paths); err != nil {
			printErrorf("failed to finalize cancellation: %v", err)
			return false
		} else if finalized {
			fmt.Println(green("Cancellation complete"))
			return true
		}
		running, err := isRunning(paths.lockFile)
		if err != nil {
			printErrorf("failed to check queue: %v", err)
			return false
		}
		if !running {
			fmt.Println(green("Cancellation complete"))
			return true
		}
		if time.Now().After(deadline) {
			printError("cancellation is still in progress")
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func finalizeCompletedCancellation(paths pathSet) (bool, error) {
	lock, err := loadLockInfo(paths.lockFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	summary, err := loadRunSummary(filepath.Join(paths.runsDir, lock.RunID, "summary.json"))
	if err != nil || summary.FinishedAt == "" {
		return false, nil
	}
	if err := finishRun(paths, lock.RunID, summary.ExitCode); err != nil {
		return false, err
	}
	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
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
	partialArray := cliBool(fs, "partial-array", true)
	if err := fs.Parse(args); err != nil {
		printErrorf("failed to parse worker args: %v", err)
		return 1
	}
	left := fs.Args()
	if len(left) != 7 {
		printError("usage: rotari __worker-run [--basedir DIR] <project_name> <run_id> <run_name> <local_concurrency> <batch_max_active> <retry> <cwd>")
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
		printErrorf("invalid run options: local=%s batch=%s retry=%s", left[2], left[3], left[4])
		return 1
	}

	paths, err := resolvePaths(*basedir, queueName)
	if err != nil {
		printErrorf("failed to resolve paths: %v", err)
		return 1
	}
	if err := writeRunContext(paths, runID, cwd); err != nil {
		printErrorf("failed to save run context: %v", err)
		return 1
	}

	meta, _ := loadMeta(paths.metaFile)
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		printErrorf("failed to update metadata: %v", err)
		return 1
	}

	stopLoadSampling := startRunLoadSampling(paths, runID)
	exitCode := executeMixedRun(paths, runID, runName, localConcurrency, batchMaxActive, retry, *executor, executorOptions, *selection, jobIDs, *sourceRunID, *partialArray, nil, nil)
	stopLoadSampling()
	if err := finishRunContext(paths, runID); err != nil {
		printErrorf("failed to save run context: %v", err)
		return 1
	}
	if err := finishRun(paths, runID, exitCode); err != nil {
		printErrorf("failed to finalize metadata: %v", err)
		return 1
	}

	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		printErrorf("failed to remove lock file: %v", err)
	}

	return exitCode
}

func finishRun(paths pathSet, runID string, exitCode int) error {
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return fmt.Errorf("failed to lock queue: %w", err)
	}
	defer release()

	lock, err := loadLockInfo(paths.lockFile)
	if err != nil {
		return fmt.Errorf("failed to verify run lock: %w", err)
	}
	if lock.RunID != runID {
		return fmt.Errorf("run lock belongs to %q, not %q", lock.RunID, runID)
	}

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

func launchAsyncRun(paths pathSet, queueName, runID, runName string, localConcurrency, batchMaxActive, retry int, executor string, executorOptions []string, selection string, jobIDs []string, sourceRunID string, partialArray bool, cwd string, onDone func()) int {
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		printErrorf("project '%s' is running; run is not allowed: %v", queueName, err)
		return 1
	}

	meta, _ := loadMeta(paths.metaFile)
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to update metadata: %v", err)
		return 1
	}
	if err := writeRunContext(paths, runID, cwd); err != nil {
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to save run context: %v", err)
		return 1
	}
	if err := registerRun(paths, runID); err != nil {
		_ = os.Remove(paths.lockFile)
		_ = os.RemoveAll(filepath.Join(paths.runsDir, runID))
		printErrorf("failed to register run: %v", err)
		return 1
	}

	exe, err := os.Executable()
	if err != nil {
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to detect executable path: %v", err)
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
	childArgs = append(childArgs, "--partial-array", strconv.FormatBool(partialArray))
	childArgs = append(childArgs, queueName, runID, runName, strconv.Itoa(localConcurrency), strconv.Itoa(batchMaxActive), strconv.Itoa(retry), cwd)

	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to launch async runner: %v", err)
		return 1
	}

	host, err := os.Hostname()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to determine lock host: %v", err)
		return 1
	}
	if err := writeJSON(paths.lockFile, LockInfo{PID: cmd.Process.Pid, RunID: runID, StartedAt: nowRFC3339(), Host: host}); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(paths.lockFile)
		printErrorf("failed to update lock with child pid: %v", err)
		return 1
	}
	waitForAsyncRun(cmd, onDone)

	fmt.Printf("submitted project=%s run_id=%s pid=%d\n", queueName, runID, cmd.Process.Pid)
	return 0
}

func waitForAsyncRun(cmd *exec.Cmd, onDone func()) {
	go func() {
		_ = cmd.Wait()
		if onDone != nil {
			onDone()
		}
	}()
}

func writeRunContext(paths pathSet, runID, cwd string) error {
	context := captureRunContext(cwd)
	if context.StartedLoad != nil {
		if err := appendLoadSample(loadSamplesPath(paths, runID), LoadSample{At: nowRFC3339Nano(), LoadAverage: *context.StartedLoad}); err != nil {
			return err
		}
	}
	return writeJSON(filepath.Join(paths.runsDir, runID, "context.json"), context)
}

func finishRunContext(paths pathSet, runID string) error {
	path := filepath.Join(paths.runsDir, runID, "context.json")
	context := RunContext{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &context)
	}
	context.FinishedLoad = readLoadAverage()
	if context.FinishedLoad != nil {
		if err := appendLoadSample(loadSamplesPath(paths, runID), LoadSample{At: nowRFC3339Nano(), LoadAverage: *context.FinishedLoad}); err != nil {
			return err
		}
	}
	return writeJSON(path, context)
}

func captureRunContext(cwd string) RunContext {
	hostname, _ := os.Hostname()
	return RunContext{CWD: cwd, Hostname: hostname, StartedLoad: readLoadAverage()}
}

const loadSampleInterval = 10 * time.Second

func startRunLoadSampling(paths pathSet, runID string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(loadSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = appendRunLoadSample(paths, runID)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func loadSamplesPath(paths pathSet, runID string) string {
	return filepath.Join(paths.runsDir, runID, "load_samples.jsonl")
}

func appendLoadSample(path string, sample LoadSample) error {
	if err := os.MkdirAll(filepath.Dir(path), stateDirMode()); err != nil {
		return err
	}
	data, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, stateFileMode())
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func readLoadSamples(path string) []LoadSample {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var samples []LoadSample
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var sample LoadSample
		if json.Unmarshal(line, &sample) == nil {
			samples = append(samples, sample)
		}
	}
	return samples
}

func appendRunLoadSample(paths pathSet, runID string) error {
	load := readLoadAverage()
	if load == nil {
		return nil
	}
	return appendLoadSample(loadSamplesPath(paths, runID), LoadSample{At: nowRFC3339Nano(), LoadAverage: *load})
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
		printErrorf("failed to load queue: %v", err)
		return 1
	}
	if len(queue.Commands) == 0 {
		printErrorf("queue '%s' has no queued commands", paths.queueName)
		return 1
	}

	runDir := filepath.Join(paths.runsDir, runID)
	if err := os.MkdirAll(runDir, stateDirMode()); err != nil {
		printErrorf("failed to create run directory: %v", err)
		return 1
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		printErrorf("failed to write command snapshot: %v", err)
		return 1
	}

	jobs := queueToJobs(queue.Commands)
	if len(jobs) == 0 {
		printErrorf("queue '%s' has no valid commands", paths.queueName)
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
		printErrorf("failed to write summary: %v", err)
		return 1
	}

	completionMessage := fmt.Sprintf("run finished run_id=%s success=%d failed=%d dir=%s", runID, successCount, failedCount, runDir)
	if summary.ExitCode == 0 {
		fmt.Println(colorKeyValueMessage(completionMessage, green))
	} else {
		fmt.Println(colorKeyValueMessage(completionMessage, red))
		printFailedJobHints(runID, summary.Results)
	}
	return summary.ExitCode
}

func printFailedJobHints(runID string, results []JobResult) {
	if hints := failedJobHints(runID, results); hints != "" {
		fmt.Print(hints)
	}
}

func failedJobHints(runID string, results []JobResult) string {
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
		fmt.Fprintf(&hints, "  Job: %s\n  Hosts: %s\n  Command: %s\n  Show output:\n    rotari show --run-id %s --job-id %s\n",
			result.ID, hosts, strings.Join(result.Command, " "), runID, result.ID)
	}
	return hints.String()
}

func runOneJob(runDir string, job JobSpec) JobResult {
	hostname, _ := os.Hostname()
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, stateDirMode()); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}

	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}
	if job.Name != "" {
		_ = os.WriteFile(filepath.Join(jobDir, "name"), []byte(job.Name+"\n"), stateFileMode())
	}
	if err := os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte(nowRFC3339()+"\n"), stateFileMode()); err != nil {
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
	cmd.Dir = job.WorkingDirectory
	cmd.Env = mergeEnvironment(os.Environ(), job.Environment)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte("1\n"), stateFileMode())
		_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), stateFileMode())
		fmt.Printf("fail job=%s command=%s error=%v\n", job.ID, strings.Join(job.Command, " "), err)
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}

	_ = os.WriteFile(filepath.Join(jobDir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), stateFileMode())
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
	_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte(strconv.Itoa(exitCode)+"\n"), stateFileMode())
	_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), stateFileMode())

	if exitCode == 0 {
		fmt.Printf("%s\n", colorKeyValueMessage(fmt.Sprintf("success job=%s", job.ID), green))
	} else {
		fmt.Printf("%s\n", colorKeyValueMessage(fmt.Sprintf("fail job=%s exit=%d command=%s", job.ID, exitCode, strings.Join(job.Command, " ")), red))
	}

	return JobResult{ID: job.ID, Command: job.Command, ExitCode: exitCode, Hosts: []string{hostname}}
}

func mergeEnvironment(base, overrides []string) []string {
	values := make(map[string]string)
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range append(append([]string(nil), base...), overrides...) {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		if _, exists := values[parts[0]]; !exists {
			order = append(order, parts[0])
		}
		values[parts[0]] = parts[1]
	}
	merged := make([]string, 0, len(order))
	for _, name := range order {
		merged = append(merged, name+"="+values[name])
	}
	return merged
}

func jobCancellationRequested(jobDir string) bool {
	_, err := os.Stat(filepath.Join(jobDir, "cancelled"))
	return err == nil
}

func recordCancelledJob(jobDir string, job JobSpec) JobResult {
	message := "cancelled before start"
	_ = os.MkdirAll(jobDir, stateDirMode())
	_ = writeJSON(filepath.Join(jobDir, "command.json"), job)
	if job.Name != "" {
		_ = os.WriteFile(filepath.Join(jobDir, "name"), []byte(job.Name+"\n"), stateFileMode())
	}
	_ = os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte(nowRFC3339()+"\n"), stateFileMode())
	_ = os.WriteFile(filepath.Join(jobDir, "output"), []byte(message+"\n"), stateFileMode())
	_ = os.WriteFile(filepath.Join(jobDir, "status"), []byte("143\n"), stateFileMode())
	_ = os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte(nowRFC3339()+"\n"), stateFileMode())
	return JobResult{ID: job.ID, Command: job.Command, ExitCode: 143, Error: message}
}

func queueToJobs(commands []QueuedCommand) []JobSpec {
	jobs := make([]JobSpec, 0, len(commands))
	for _, queued := range commands {
		if len(queued.Command) == 0 {
			continue
		}
		if queued.Array == nil {
			jobs = append(jobs, JobSpec{
				ID: queued.ID, Command: queued.Command, WorkingDirectory: queued.WorkingDirectory, Name: queued.Name,
				Executor: queued.Executor, ExecutorOptions: queued.ExecutorOptions, Environment: queued.Environment, DependsOn: queued.DependsOn,
			})
			continue
		}
		for _, task := range arrayTaskIDs(queued.Array) {
			id := fmt.Sprintf("%s-%d", queued.ID, task)
			name := queued.Name
			if name != "" {
				name = fmt.Sprintf("%s[%d]", name, task)
			}
			taskID := task
			jobs = append(jobs, JobSpec{
				ID: id, Command: queued.Command, WorkingDirectory: queued.WorkingDirectory, Name: name,
				Executor: queued.Executor, ExecutorOptions: queued.ExecutorOptions, Environment: queued.Environment, DependsOn: queued.DependsOn,
				ArrayGroup: queued.ID, ArrayTaskID: &taskID, ArrayFirst: queued.Array.First, ArrayLast: queued.Array.Last, ArraySize: len(arrayTaskIDs(queued.Array)),
			})
		}
	}
	return jobs
}

type pathSet struct {
	baseDir         string
	baseDirExplicit bool
	queueName       string
	projectDir      string
	queueFile       string
	metaFile        string
	stateLockFile   string
	lockFile        string
	runsDir         string
}

func isValidProjectName(projectName string) bool {
	return projectName != "" && projectName != "." && projectName != ".." && filepath.Base(projectName) == projectName
}

func resolvePaths(cliBaseDir, projectName string) (pathSet, error) {
	if !isValidProjectName(projectName) {
		return pathSet{}, fmt.Errorf("invalid project name %q", projectName)
	}
	baseDir, explicit, err := resolveBaseDir(cliBaseDir)
	if err != nil {
		return pathSet{}, err
	}
	projectDir := filepath.Join(baseDir, "projects", projectName)
	return pathSet{
		baseDir:         baseDir,
		baseDirExplicit: explicit,
		queueName:       projectName,
		projectDir:      projectDir,
		queueFile:       filepath.Join(projectDir, "queue.json"),
		metaFile:        filepath.Join(projectDir, "meta.json"),
		stateLockFile:   filepath.Join(projectDir, "state.lock"),
		lockFile:        filepath.Join(projectDir, "running.lock"),
		runsDir:         filepath.Join(projectDir, "runs"),
	}, nil
}

func resolveBaseDir(cliBaseDir string) (string, bool, error) {
	if cliBaseDir != "" {
		return cliBaseDir, true, nil
	}
	if v := os.Getenv(envBaseDir); v != "" {
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

// privateStateEnabled controls whether the state directory tree (queues,
// runs, job output, locks) is created owner-only (0700/0600) instead of the
// default shared (0755/0644) permissions. Shared is the default because
// rotari is commonly used on shared HPC/lab filesystems where colleagues
// point each other at a job's log path.
func privateStateEnabled() bool {
	value, _ := strconv.ParseBool(os.Getenv(envPrivateState))
	return value
}

func stateDirMode() os.FileMode {
	return stateMode(0o700, 0o755)
}

func stateFileMode() os.FileMode {
	return stateMode(0o600, 0o644)
}

// stateScriptMode is for generated wrapper scripts, which must stay executable.
func stateScriptMode() os.FileMode {
	return stateMode(0o700, 0o755)
}

func stateMode(privateMode, sharedMode os.FileMode) os.FileMode {
	if privateStateEnabled() {
		return privateMode
	}
	return sharedMode
}

func resolveProjectName(baseDir string, cliProjectName string) (string, error) {
	if cliProjectName != "" {
		if !isValidProjectName(cliProjectName) {
			return "", fmt.Errorf("invalid project name %q", cliProjectName)
		}
		return cliProjectName, nil
	}
	if value := os.Getenv(envProjectName); value != "" {
		if !isValidProjectName(value) {
			return "", fmt.Errorf("invalid project name %q", value)
		}
		return value, nil
	}
	projectsDir := filepath.Join(baseDir, "projects")
	entries, err := os.ReadDir(projectsDir)
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
			return "", fmt.Errorf("multiple projects exist, please specify one with --project-name or ROTARI_PROJECT_NAME:\n%s", strings.Join(list, "\n"))
		}
	}
	return defaultProjectName, nil
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
	if err := os.MkdirAll(filepath.Dir(path), stateDirMode()); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rotari-tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(stateFileMode()); err != nil {
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
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, stateFileMode())
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

	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(lockPath), ".rotari-lock-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(stateFileMode()); err != nil {
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
	if err := os.Link(tmpName, lockPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("active lock exists")
		}
		return err
	}
	return nil
}

func isRunning(lockPath string) (bool, error) {
	lock, err := loadLockInfo(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read run lock: %w", err)
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

type projectRunState int

const (
	projectIdle projectRunState = iota
	projectRunning
	projectInterrupted
)

func inspectProjectRunState(paths pathSet) (projectRunState, string, error) {
	running, err := isRunning(paths.lockFile)
	if err != nil {
		return projectIdle, "", err
	}
	if running {
		lock, err := loadLockInfo(paths.lockFile)
		if err != nil {
			return projectIdle, "", err
		}
		return projectRunning, lock.RunID, nil
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return projectIdle, "", err
	}
	switch meta.Phase {
	case "collecting", "running", "cancelling", "finished":
	default:
		return projectIdle, "", fmt.Errorf("unknown project phase %q", meta.Phase)
	}
	if (meta.Phase == "running" || meta.Phase == "cancelling") && meta.LastRunID != "" {
		return projectInterrupted, meta.LastRunID, nil
	}
	return projectIdle, "", nil
}

func ensureProjectIdleForPaths(paths pathSet, operation string) error {
	state, runID, err := inspectProjectRunState(paths)
	if err != nil {
		return fmt.Errorf("failed to check project state: %w", err)
	}
	switch state {
	case projectRunning:
		return fmt.Errorf("project %q is running; %s is not allowed", paths.queueName, operation)
	case projectInterrupted:
		return fmt.Errorf("project %q has interrupted run %q; %s is not allowed; recover with: rotari unlock --basedir %s --project-name %s --run-id %s",
			paths.queueName, runID, operation, shellQuote(paths.baseDir), shellQuote(paths.queueName), shellQuote(runID))
	default:
		return nil
	}
}

func ensureProjectIdle(baseDir, queueName, operation string) error {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return err
	}
	return ensureProjectIdleForPaths(paths, operation)
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

func nowRFC3339Nano() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func formatDisplayTimestamp(value string) string {
	if value == "" || value == "-" {
		return value
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return timestamp.In(time.FixedZone("JST", 9*60*60)).Format("2006-01-02 15:04:05 JST")
}
