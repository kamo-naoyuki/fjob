package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type slurmStatus struct {
	Phase      string `json:"phase"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type slurmJobMetadata struct {
	Executor    string   `json:"executor"`
	JobID       string   `json:"job_id"`
	Command     []string `json:"command"`
	SlurmJobID  string   `json:"slurm_job_id"`
	SubmittedAt string   `json:"submitted_at"`
}

// slurmExecutor submits jobs to Slurm via sbatch and tracks them through
// squeue/sacct. See submitSlurmJob and waitSlurmJob for the details.
type slurmExecutor struct{}

func (slurmExecutor) Name() string { return "slurm" }

func (slurmExecutor) Submit(runDir string, job JobSpec, options []string) (JobHandle, error) {
	metadata, err := submitSlurmJob(runDir, job, options)
	if err != nil {
		return JobHandle{}, err
	}
	return JobHandle{Job: job, Native: metadata.SlurmJobID}, nil
}

func (slurmExecutor) Wait(runDir string, handle JobHandle) JobResult {
	metadata := slurmJobMetadata{
		Executor:   "slurm",
		JobID:      handle.Job.ID,
		Command:    handle.Job.Command,
		SlurmJobID: handle.Native,
	}
	return waitSlurmJob(runDir, metadata)
}

func (slurmExecutor) Suspend(jobDir string) error {
	return slurmExecutor{}.scontrol(jobDir, "suspend")
}

func (slurmExecutor) Resume(jobDir string) error {
	return slurmExecutor{}.scontrol(jobDir, "resume")
}

func (slurmExecutor) Cancel(jobDir string) error {
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		return fmt.Errorf("job is not running")
	}
	var metadata slurmJobMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("invalid Slurm metadata: %w", err)
	}
	if _, err := runSlurmCommand("scancel", metadata.SlurmJobID); err != nil {
		return fmt.Errorf("scancel %s: %w", metadata.SlurmJobID, err)
	}
	return nil
}

func (slurmExecutor) scontrol(jobDir, command string) error {
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		return fmt.Errorf("job is not running")
	}
	var metadata slurmJobMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("invalid Slurm metadata: %w", err)
	}
	if _, err := runSlurmCommand("scontrol", command, metadata.SlurmJobID); err != nil {
		return fmt.Errorf("scontrol %s %s: %w", command, metadata.SlurmJobID, err)
	}
	return nil
}

const slurmAccountingWait = 60 * time.Second
const slurmCommandTimeout = 30 * time.Second

type stringSliceFlag []string

func (flag *stringSliceFlag) String() string {
	return strings.Join(*flag, ",")
}

func (flag *stringSliceFlag) Set(value string) error {
	*flag = append(*flag, value)
	return nil
}

func executeSlurmRun(paths pathSet, runID string, maxActive int, executorOptions []string) (int, int, int) {
	if maxActive < 1 {
		fmt.Fprintln(os.Stderr, "slurm max active must be >= 1")
		return 1, 0, 0
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load queue: %v\n", err)
		return 1, 0, 0
	}
	jobs := queueToJobs(queue.Commands)
	if len(jobs) == 0 {
		fmt.Fprintf(os.Stderr, "queue '%s' has no valid commands\n", paths.queueName)
		return 1, 0, 0
	}
	runDir := filepath.Join(paths.runsDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create run directory: %v\n", err)
		return 1, 0, 0
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write command snapshot: %v\n", err)
		return 1, 0, 0
	}

	metadata := make([]slurmJobMetadata, 0, len(jobs))
	results := make([]JobResult, 0, len(jobs))
	for start := 0; start < len(jobs); start += maxActive {
		end := start + maxActive
		if end > len(jobs) {
			end = len(jobs)
		}
		batch := jobs[start:end]
		batchMetadata := make([]slurmJobMetadata, 0, len(batch))
		for _, job := range batch {
			jobOptions := executorOptions
			if len(jobOptions) == 0 {
				jobOptions = job.ExecutorOptions
			}
			if len(jobOptions) == 0 {
				jobOptions = queue.DefaultExecutorOptions
			}
			jobMetadata, err := submitSlurmJob(runDir, job, jobOptions)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to submit job %s: %v\n", job.ID, err)
				return 1, 0, 0
			}
			metadata = append(metadata, jobMetadata)
			batchMetadata = append(batchMetadata, jobMetadata)
		}
		for _, job := range batchMetadata {
			results = append(results, waitSlurmJob(runDir, job))
		}
	}
	if err := writeJSON(filepath.Join(runDir, "slurm_jobs.json"), metadata); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write Slurm metadata: %v\n", err)
		return 1, 0, 0
	}

	summary := RunSummary{
		RunID:      runID,
		Status:     "finished",
		StartedAt:  nowRFC3339(),
		FinishedAt: nowRFC3339(),
		ExitCode:   0,
		Results:    results,
	}
	for _, result := range results {
		if result.ExitCode != 0 {
			summary.ExitCode = 1
		}
	}
	summary.Status = runStatus(summary.ExitCode)
	successCount := 0
	failedCount := 0
	for _, result := range results {
		if result.ExitCode == 0 {
			successCount++
		} else {
			failedCount++
		}
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write summary: %v\n", err)
		return 1, successCount, failedCount
	}
	if summary.ExitCode != 0 {
		printFailedJobHints(paths, runID, results)
	}
	return summary.ExitCode, successCount, failedCount
}

func prepareSlurmRun(baseDir, queueName string) (pathSet, string, Meta, func(), error) {
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		return pathSet{}, "", Meta{}, nil, err
	}
	if err := os.MkdirAll(paths.queueDir, 0o755); err != nil {
		return pathSet{}, "", Meta{}, nil, err
	}
	release, err := acquireStateLock(paths.stateLockFile)
	if err != nil {
		return pathSet{}, "", Meta{}, nil, err
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		release()
		return pathSet{}, "", Meta{}, nil, err
	}
	if len(queue.Commands) == 0 {
		release()
		return pathSet{}, "", Meta{}, nil, fmt.Errorf("queue %q has no queued commands", queueName)
	}
	runID := makeRunID()
	if err := acquireLock(paths.lockFile, LockInfo{PID: os.Getpid(), RunID: runID, StartedAt: nowRFC3339()}); err != nil {
		release()
		return pathSet{}, "", Meta{}, nil, fmt.Errorf("queue %q is already running", queueName)
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		_ = os.Remove(paths.lockFile)
		release()
		return pathSet{}, "", Meta{}, nil, err
	}
	meta.Phase = "running"
	meta.LastRunID = runID
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		_ = os.Remove(paths.lockFile)
		release()
		return pathSet{}, "", Meta{}, nil, err
	}
	return paths, runID, meta, release, nil
}

func finishSlurmRun(paths pathSet, runID string, meta Meta, exitCode int) error {
	meta.Phase = "finished"
	meta.LastRunID = runID
	meta.LastRunExitCode = exitCode
	meta.UpdatedAt = nowRFC3339()
	if err := writeJSON(paths.metaFile, meta); err != nil {
		return err
	}
	if err := os.Remove(paths.lockFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func runSlurmServerSync(baseDir, queueName string, maxActive int, executorOptions []string) (string, int, error) {
	paths, runID, meta, release, err := prepareSlurmRun(baseDir, queueName)
	if err != nil {
		return "", 1, err
	}
	release()
	exitCode, successCount, failedCount := executeSlurmRun(paths, runID, maxActive, executorOptions)
	if err := finishSlurmRun(paths, runID, meta, exitCode); err != nil {
		return "", 1, err
	}
	runDir := filepath.Join(paths.runsDir, runID)
	message := fmt.Sprintf("Run finished:\n  Queue: %s\n  Run: %s\n  Exit code: %d\n  Success: %d\n  Failed: %d\n  Directory: %s", queueName, runID, exitCode, successCount, failedCount, runDir)
	if summaryData, err := os.ReadFile(filepath.Join(runDir, "summary.json")); err == nil {
		var summary RunSummary
		if json.Unmarshal(summaryData, &summary) == nil {
			message = formatRunCompletion(paths, runID, summary)
		}
	}
	return message, exitCode, nil
}

func startSlurmServerRun(baseDir, queueName string, maxActive int, executorOptions []string, onDone func()) (string, error) {
	paths, runID, meta, release, err := prepareSlurmRun(baseDir, queueName)
	if err != nil {
		return "", err
	}
	release()
	go func() {
		exitCode, _, _ := executeSlurmRun(paths, runID, maxActive, executorOptions)
		_ = finishSlurmRun(paths, runID, meta, exitCode)
		if onDone != nil {
			onDone()
		}
	}()
	runDir := filepath.Join(paths.runsDir, runID)
	return fmt.Sprintf("Run started (Slurm):\n  Queue: %s\n  Run: %s\n  Directory: %s\n\nCheck status:\n  rotari show --basedir %s --queue-name %s --run-id %s\n\nCancel run:\n  rotari cancel --basedir %s --queue-name %s",
		queueName, runID, runDir, paths.baseDir, queueName, runID, paths.baseDir, queueName), nil
}

func submitSlurmJob(runDir string, job JobSpec, executorOptions []string) (slurmJobMetadata, error) {
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return slurmJobMetadata{}, err
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return slurmJobMetadata{}, err
	}
	wrapperPath := filepath.Join(jobDir, "slurm-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(statusWrapperScript(job.Command, jobDir)), 0o755); err != nil {
		return slurmJobMetadata{}, err
	}
	outputPath := filepath.Join(jobDir, "output")
	queueDir := filepath.Dir(filepath.Dir(runDir))
	baseDir := filepath.Dir(filepath.Dir(queueDir))
	queueName := filepath.Base(queueDir)
	runID := filepath.Base(runDir)
	showCommand := fmt.Sprintf("rotari show --basedir %s --queue-name %s --run-id %s --job-id %s",
		shellQuote(baseDir), shellQuote(queueName), shellQuote(runID), shellQuote(job.ID))
	args := []string{"--parsable", "--job-name=" + showCommand, "--output=" + outputPath, "--error=" + outputPath}
	expandedOptions, err := expandShellOptions(executorOptions)
	if err != nil {
		return slurmJobMetadata{}, err
	}
	args = append(args, expandedOptions...)
	args = append(args, wrapperPath)
	output, err := runSlurmCommand("sbatch", args...)
	if err != nil {
		return slurmJobMetadata{}, fmt.Errorf("sbatch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	slurmJobID := strings.TrimSpace(strings.SplitN(string(output), ";", 2)[0])
	if slurmJobID == "" {
		return slurmJobMetadata{}, errors.New("sbatch returned an empty job id")
	}
	metadata := slurmJobMetadata{
		Executor: "slurm", JobID: job.ID, Command: job.Command,
		SlurmJobID: slurmJobID, SubmittedAt: nowRFC3339(),
	}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), metadata); err != nil {
		return slurmJobMetadata{}, err
	}
	fmt.Printf("[%s] submit job=%s slurm_job_id=%s command=%s\n", metadata.SubmittedAt, job.ID, slurmJobID, strings.Join(job.Command, " "))
	return metadata, nil
}

// statusWrapperScript wraps command in a shell script that records phase and
// exit code to status.json, so any poll-based executor (Slurm, PBS, ...) can
// determine the final result even if the scheduler's own accounting lags.
func statusWrapperScript(command []string, jobDir string) string {
	statusPath := filepath.Join(jobDir, "status.json")
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, shellQuote(arg))
	}
	commandLine := strings.Join(quoted, " ")
	return fmt.Sprintf(`#!/bin/sh
set +e
status_path=%s
write_status() {
    phase=$1
    code=$2
    tmp="${status_path}.tmp.$$"
    now=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)
    if [ "$phase" = "running" ]; then
        printf '{"phase":"running","started_at":"%%s"}\n' "$now" > "$tmp"
    else
        printf '{"phase":"%%s","exit_code":%%s,"finished_at":"%%s"}\n' "$phase" "$code" "$now" > "$tmp"
    fi
    mv -f "$tmp" "$status_path"
}
write_status running 0
trap 'write_status cancelled 143; exit 143' TERM
trap 'write_status cancelled 130; exit 130' INT
trap 'write_status cancelled 131; exit 131' QUIT
%s
code=$?
write_status finished "$code"
exit "$code"
`, shellQuote(statusPath), commandLine)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// expandShellOptions splits shell-quoted option strings (e.g. "-p short --cpus-per-task=2")
// into individual CLI arguments. Shared by any executor that accepts free-form option strings.
func expandShellOptions(options []string) ([]string, error) {
	expanded := make([]string, 0, len(options))
	for _, option := range options {
		words, err := splitShellWords(option)
		if err != nil {
			return nil, fmt.Errorf("invalid executor option %q: %w", option, err)
		}
		expanded = append(expanded, words...)
	}
	return expanded, nil
}

func splitShellWords(input string) ([]string, error) {
	var words []string
	var word strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false
	hasContent := false

	flush := func() {
		if hasContent {
			words = append(words, word.String())
			word.Reset()
			hasContent = false
		}
	}

	for _, char := range input {
		if escaped {
			word.WriteRune(char)
			hasContent = true
			escaped = false
			continue
		}
		if inSingleQuote {
			if char == '\'' {
				inSingleQuote = false
			} else {
				word.WriteRune(char)
				hasContent = true
			}
			continue
		}
		if inDoubleQuote {
			switch char {
			case '"':
				inDoubleQuote = false
			case '\\':
				escaped = true
			default:
				word.WriteRune(char)
				hasContent = true
			}
			continue
		}
		switch {
		case char == '\\':
			escaped = true
		case char == '\'':
			inSingleQuote = true
			hasContent = true
		case char == '"':
			inDoubleQuote = true
			hasContent = true
		case char == ' ' || char == '\t' || char == '\n':
			flush()
		default:
			word.WriteRune(char)
			hasContent = true
		}
	}

	if escaped {
		return nil, errors.New("trailing escape")
	}
	if inSingleQuote || inDoubleQuote {
		return nil, errors.New("unterminated quote")
	}
	flush()
	return words, nil
}

func waitSlurmJob(runDir string, job slurmJobMetadata) JobResult {
	jobDir := filepath.Join(runDir, job.JobID)
	statusPath := filepath.Join(jobDir, "status.json")
	var accountingDeadline time.Time
	for {
		if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: status.ExitCode, Error: status.Error}
		}
		active, err := slurmJobActive(job.SlurmJobID)
		if err != nil {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: err.Error()}
		}
		if !active {
			_, _ = os.ReadDir(jobDir)
			if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: status.ExitCode, Error: status.Error}
			}
			if accountingDeadline.IsZero() {
				accountingDeadline = time.Now().Add(slurmAccountingWait)
			}
			if exitCode, state, ok := slurmAccounting(job.SlurmJobID); ok {
				_ = writeJSON(statusPath, slurmStatus{Phase: state, ExitCode: exitCode, FinishedAt: nowRFC3339()})
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: exitCode, Error: state}
			}
			if time.Now().After(accountingDeadline) {
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: "Slurm accounting result and wrapper status are unavailable"}
			}
		}
		time.Sleep(time.Second)
	}
}

func loadSlurmStatus(path string) (slurmStatus, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return slurmStatus{}, false
	}
	var status slurmStatus
	if json.Unmarshal(data, &status) != nil {
		return slurmStatus{}, false
	}
	return status, true
}

func slurmJobActive(jobID string) (bool, error) {
	output, err := runSlurmCommand("squeue", "--noheader", "--jobs", jobID, "--format=%T")
	if err != nil {
		return false, fmt.Errorf("squeue: %w", err)
	}
	return strings.TrimSpace(string(output)) != "", nil
}

func slurmAccounting(jobID string) (int, string, bool) {
	output, err := runSlurmCommand("sacct", "--noheader", "--parsable2", "--allocations", "--jobs", jobID, "--format=State,ExitCode")
	if err != nil {
		return 0, "", false
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), "|")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		state := strings.SplitN(fields[0], "+", 2)[0]
		exitCode := parseSlurmExitCode(fields[1])
		if state == "COMPLETED" {
			exitCode = 0
		}
		return exitCode, strings.ToLower(state), true
	}
	return 0, "", false
}

func runSlurmCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), slurmCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", name, slurmCommandTimeout)
	}
	return output, err
}

func parseSlurmExitCode(value string) int {
	value = strings.SplitN(value, ":", 2)[0]
	code, err := strconv.Atoi(value)
	if err != nil {
		return 1
	}
	return code
}
