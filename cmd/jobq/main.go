package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const jobIDLen = 9
const defaultQueueName = "default"

type Queue struct {
	DefaultBackend       string          `json:"default_backend,omitempty"`
	DefaultSbatchOptions []string        `json:"default_sbatch_options,omitempty"`
	Commands             []QueuedCommand `json:"commands"`
}

type QueuedCommand struct {
	Command       []string `json:"command"`
	Backend       string   `json:"backend,omitempty"`
	SbatchOptions []string `json:"sbatch_options,omitempty"`
}

func (queue *Queue) UnmarshalJSON(data []byte) error {
	var current struct {
		DefaultBackend       string          `json:"default_backend,omitempty"`
		DefaultSbatchOptions []string        `json:"default_sbatch_options,omitempty"`
		Commands             json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	var commands []QueuedCommand
	if err := json.Unmarshal(current.Commands, &commands); err != nil {
		var legacy [][]string
		if err := json.Unmarshal(current.Commands, &legacy); err != nil {
			return err
		}
		commands = make([]QueuedCommand, 0, len(legacy))
		for _, command := range legacy {
			commands = append(commands, QueuedCommand{Command: command})
		}
	}
	queue.DefaultBackend = current.DefaultBackend
	queue.DefaultSbatchOptions = current.DefaultSbatchOptions
	queue.Commands = commands
	return nil
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
}

type JobSpec struct {
	ID            string   `json:"id"`
	Command       []string `json:"command"`
	Backend       string   `json:"backend,omitempty"`
	SbatchOptions []string `json:"sbatch_options,omitempty"`
}

type JobResult struct {
	ID       string   `json:"id"`
	ExitCode int      `json:"exit_code"`
	Error    string   `json:"error,omitempty"`
	Command  []string `json:"command,omitempty"`
}

type RunSummary struct {
	RunID      string      `json:"run_id"`
	StartedAt  string      `json:"started_at"`
	FinishedAt string      `json:"finished_at"`
	ExitCode   int         `json:"exit_code"`
	Results    []JobResult `json:"results"`
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
	case "clear":
		return cmdClear(args[1:])
	case "show":
		return cmdShow(args[1:])
	case "wait":
		return cmdWait(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "submit":
		return cmdSubmit(args[1:])
	case "server":
		return cmdServer(args[1:])
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
	fmt.Println("jobq: lightweight local job queue")
	fmt.Println("  jobq version")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  jobq check [--basedir DIR] [--queue-name NAME] [--server]")
	fmt.Println("  jobq cancel [--basedir DIR] [--queue-name NAME]")
	fmt.Println("  jobq clear [--basedir DIR] [--queue-name NAME]")
	fmt.Println("  jobq show [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID] [--failed]")
	fmt.Println("  jobq wait [--basedir DIR] [--queue-name NAME] --run-id ID [--timeout DURATION]")
	fmt.Println("  jobq submit [--basedir DIR] [--queue-name NAME] [--backend BACKEND] [--sbatch-option OPTION] <command ...>")
	fmt.Println("  jobq run [--basedir DIR] [--queue-name NAME] [--local-concurrency N] [--slurm-max-active N] [--retry N] [--async]")
	fmt.Println("  jobq server <status|list|shutdown> [--basedir DIR] [--masterdir DIR]")
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := fs.String("basedir", "", "state directory")
	queueNameOption := fs.String("queue-name", "", "queue name")
	serverRequired := fs.Bool("server", false, "also require a running server")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: jobq check [--basedir DIR] [--queue-name NAME] [--server]")
		return 1
	}
	queueName := resolveQueueName(*queueNameOption)
	paths, err := resolvePaths(*basedir, queueName)
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
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf("queue '%s' is running; new jobs are not allowed", queueName)))
		fmt.Fprintf(os.Stderr, "cancel with: jobq cancel --basedir %s --queue-name %s\n", paths.baseDir, queueName)
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

func cmdWorkerRun(args []string) int {
	fs := flag.NewFlagSet("__worker-run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	basedir := fs.String("basedir", "", "state directory")
	backend := fs.String("backend", "", "execution backend")
	var sbatchOptions stringSliceFlag
	fs.Var(&sbatchOptions, "sbatch-option", "option passed to sbatch")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse worker args: %v\n", err)
		return 1
	}
	left := fs.Args()
	if len(left) != 5 {
		fmt.Fprintln(os.Stderr, "usage: jobq __worker-run [--basedir DIR] <queue_name> <run_id> <local_concurrency> <slurm_max_active> <retry>")
		return 1
	}
	queueName := left[0]
	runID := left[1]
	localConcurrency, err := strconv.Atoi(left[2])
	slurmMaxActive, slurmErr := strconv.Atoi(left[3])
	retry, retryErr := strconv.Atoi(left[4])
	if err != nil || slurmErr != nil || retryErr != nil || localConcurrency < 1 || slurmMaxActive < 1 || retry < -1 {
		fmt.Fprintf(os.Stderr, "invalid run options: local=%s slurm=%s retry=%s\n", left[2], left[3], left[4])
		return 1
	}

	paths, err := resolvePaths(*basedir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
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

	exitCode := executeMixedRun(paths, runID, localConcurrency, slurmMaxActive, retry, *backend, sbatchOptions, nil)
	meta.Phase = "finished"
	meta.LastRunID = runID
	meta.LastRunExitCode = exitCode
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		fmt.Fprintf(os.Stderr, "failed to finalize metadata: %v\n", err)
		return 1
	}

	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "failed to remove lock file: %v\n", err)
	}

	return exitCode
}

func launchAsyncRun(paths pathSet, queueName, runID string, localConcurrency, slurmMaxActive, retry int, backend string, sbatchOptions []string) int {
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
	if backend != "" {
		childArgs = append(childArgs, "--backend", backend)
	}
	for _, option := range sbatchOptions {
		childArgs = append(childArgs, "--sbatch-option", option)
	}
	childArgs = append(childArgs, queueName, runID, strconv.Itoa(localConcurrency), strconv.Itoa(slurmMaxActive), strconv.Itoa(retry))

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

	if err := writeJSON(paths.lockFile, LockInfo{PID: cmd.Process.Pid, RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		_ = cmd.Process.Kill()
		_ = os.Remove(paths.lockFile)
		fmt.Fprintf(os.Stderr, "failed to update lock with child pid: %v\n", err)
		return 1
	}

	fmt.Printf("submitted queue=%s run_id=%s pid=%d\n", queueName, runID, cmd.Process.Pid)
	return 0
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
		fmt.Fprintf(&hints, "  Job: %s\n  Command: %s\n  Show output:\n    jobq show --basedir %s --queue-name %s --run-id %s --job-id %s\n",
			result.ID, strings.Join(result.Command, " "), paths.baseDir, paths.queueName, runID, result.ID)
	}
	return hints.String()
}

func runOneJob(runDir string, job JobSpec) JobResult {
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}

	if err := os.WriteFile(filepath.Join(jobDir, "command"), []byte(strings.Join(job.Command, " ")+"\n"), 0o644); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
	}
	if err := os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte(nowRFC3339()+"\n"), 0o644); err != nil {
		return JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
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

	return JobResult{ID: job.ID, Command: job.Command, ExitCode: exitCode}
}

func queueToJobs(commands []QueuedCommand) []JobSpec {
	counter := map[string]int{}
	jobs := make([]JobSpec, 0, len(commands))
	for _, queued := range commands {
		if len(queued.Command) == 0 {
			continue
		}
		key := strings.Join(queued.Command, "\x00")
		counter[key]++
		idSeed := key
		if counter[key] > 1 {
			idSeed = idSeed + "#" + strconv.Itoa(counter[key])
		}
		sum := sha256.Sum256([]byte(idSeed))
		id := hex.EncodeToString(sum[:])[:jobIDLen]
		jobs = append(jobs, JobSpec{
			ID: id, Command: queued.Command,
			Backend: queued.Backend, SbatchOptions: queued.SbatchOptions,
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
	if v := os.Getenv("JOBQ_BASEDIR"); v != "" {
		return v, true, nil
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "jobq"), false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	return filepath.Join(home, ".local", "state", "jobq"), false, nil
}

func resolveQueueName(cliQueueName string) string {
	if cliQueueName != "" {
		return cliQueueName
	}
	if value := os.Getenv("JOBQ_QUEUE_NAME"); value != "" {
		return value
	}
	return defaultQueueName
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jobq-tmp-")
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

func acquireStateLock(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func cleanupHistory(queueDir string) error {
	entries, err := os.ReadDir(queueDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "running.lock" || name == "state.lock" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(queueDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func acquireLock(lockPath string, info LockInfo) error {
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
	b, err := os.ReadFile(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	var lock LockInfo
	if err := json.Unmarshal(b, &lock); err != nil {
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return false, removeErr
		}
		return false, nil
	}

	if lock.PID <= 0 || !processAlive(lock.PID) {
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return false, removeErr
		}
		return false, nil
	}

	return true, nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func makeRunID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("failed to generate run id: %v", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
