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
	Phase      string   `json:"phase"`
	ExitCode   int      `json:"exit_code,omitempty"`
	Error      string   `json:"error,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
	StartedAt  string   `json:"started_at,omitempty"`
	FinishedAt string   `json:"finished_at,omitempty"`
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

func (slurmExecutor) SubmitArray(runDir string, jobs []JobSpec, options []string) ([]JobHandle, error) {
	return submitSlurmArray(runDir, jobs, options)
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
	if output, err := runSlurmCommand("scancel", metadata.SlurmJobID); err != nil {
		return fmt.Errorf("scancel %s: %w", metadata.SlurmJobID, schedulerCommandHint("scancel", output, err))
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
	if output, err := runSlurmCommand("scontrol", command, metadata.SlurmJobID); err != nil {
		return fmt.Errorf("scontrol %s %s: %w", command, metadata.SlurmJobID, schedulerCommandHint("scontrol", output, err))
	}
	return nil
}

const slurmCommandTimeout = 30 * time.Second

var slurmAccountingWait = 60 * time.Second
var slurmPollInterval = time.Second

type stringSliceFlag []string

func (flag *stringSliceFlag) String() string {
	return strings.Join(*flag, ",")
}

func (flag *stringSliceFlag) Set(value string) error {
	*flag = append(*flag, value)
	return nil
}

func submitSlurmJob(runDir string, job JobSpec, executorOptions []string) (slurmJobMetadata, error) {
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, stateDirMode()); err != nil {
		return slurmJobMetadata{}, err
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return slurmJobMetadata{}, err
	}
	wrapperPath := filepath.Join(jobDir, "slurm-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(statusWrapperScript(job.Command, jobDir, job.Environment, job.WorkingDirectory)), stateScriptMode()); err != nil {
		return slurmJobMetadata{}, err
	}
	outputPath := filepath.Join(jobDir, "output")
	runID := filepath.Base(runDir)
	showCommand := fmt.Sprintf("rotari show --run-id %s --job-id %s", shellQuote(runID), shellQuote(job.ID))
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

func submitSlurmArray(runDir string, jobs []JobSpec, executorOptions []string) ([]JobHandle, error) {
	if len(jobs) == 0 || jobs[0].ArrayTaskID == nil {
		return nil, errors.New("empty Slurm array")
	}
	command := jobs[0].Command
	first, last := jobs[0].ArrayFirst, jobs[0].ArrayLast
	for _, job := range jobs {
		if job.ArrayTaskID == nil || job.ArrayFirst != first || job.ArrayLast != last || !sameStrings(job.Command, command) {
			return nil, errors.New("Slurm array tasks must share one command and range")
		}
		if err := os.MkdirAll(filepath.Join(runDir, job.ID), stateDirMode()); err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(runDir, job.ID, "command.json"), job); err != nil {
			return nil, err
		}
	}
	wrapperPath := filepath.Join(runDir, jobs[0].ArrayGroup+"-array-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(schedulerArrayWrapperScript(jobs, "SLURM_ARRAY_TASK_ID")), stateScriptMode()); err != nil {
		return nil, err
	}
	expandedOptions, err := expandShellOptions(executorOptions)
	if err != nil {
		return nil, err
	}
	for _, option := range expandedOptions {
		if option == "--array" || strings.HasPrefix(option, "--array=") {
			return nil, errors.New("executor options must not include --array when rotari --array is used")
		}
	}
	args := []string{
		"--parsable",
		fmt.Sprintf("--array=%d-%d", first, last),
		"--job-name=rotari-array",
		"--output=/dev/null",
		"--error=/dev/null",
	}
	args = append(args, expandedOptions...)
	args = append(args, wrapperPath)
	output, err := runSlurmCommand("sbatch", args...)
	if err != nil {
		return nil, fmt.Errorf("sbatch array: %w: %s", err, strings.TrimSpace(string(output)))
	}
	masterID := strings.TrimSpace(strings.SplitN(string(output), ";", 2)[0])
	if masterID == "" {
		return nil, errors.New("sbatch returned an empty array job id")
	}
	handles := make([]JobHandle, 0, len(jobs))
	for _, job := range jobs {
		task := *job.ArrayTaskID
		nativeID := fmt.Sprintf("%s_%d", masterID, task)
		metadata := slurmJobMetadata{Executor: "slurm", JobID: job.ID, Command: job.Command, SlurmJobID: nativeID, SubmittedAt: nowRFC3339()}
		if err := writeJSON(filepath.Join(runDir, job.ID, "job.json"), metadata); err != nil {
			return nil, err
		}
		handles = append(handles, JobHandle{Job: job, Native: nativeID})
	}
	return handles, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func rejectArraySchedulerOptions(options []string, names ...string) error {
	expanded, err := expandShellOptions(options)
	if err != nil {
		return err
	}
	for _, option := range expanded {
		for _, name := range names {
			if option == name || strings.HasPrefix(option, name+"=") {
				return fmt.Errorf("executor options must not include %s when rotari --array is used", name)
			}
		}
	}
	return nil
}

func schedulerArrayWrapperScript(jobs []JobSpec, taskVariable string) string {
	quoted := make([]string, 0, len(jobs[0].Command))
	for _, arg := range jobs[0].Command {
		quoted = append(quoted, shellQuote(arg))
	}
	caseLines := make([]string, 0, len(jobs))
	for _, job := range jobs {
		exports := make([]string, 0, len(job.Environment))
		jobDir := ""
		for _, entry := range job.Environment {
			parts := strings.SplitN(entry, "=", 2)
			if len(parts) == 2 {
				if parts[0] == envJobDir {
					jobDir = parts[1]
					continue
				}
				exports = append(exports, "export "+parts[0]+"="+shellQuote(parts[1]))
			}
		}
		if jobDir == "" {
			jobDir = filepath.Join("$ROTARI_RUN_DIR", job.ID)
		}
		changeDirectory := ""
		if job.WorkingDirectory != "" {
			changeDirectory = "        cd " + shellQuote(job.WorkingDirectory) + " || exit 1\n"
		}
		caseLines = append(caseLines, fmt.Sprintf("    %d)\n        %s\n        job_dir=%s\n        export %s=%s\n        mkdir -p \"$job_dir\" || exit 1\n%s        ;;", *job.ArrayTaskID, strings.Join(exports, "\n        "), shellQuote(jobDir), envJobDir, shellQuote(jobDir), changeDirectory))
	}
	return "#!/bin/sh\nset +e\ncase \"$" + taskVariable + "\" in\n" + strings.Join(caseLines, "\n") + "\n    *) exit 1 ;;\nesac\nexec >\"$job_dir/output\" 2>&1\nstatus_path=\"$job_dir/status.json\"\nhostname=$(hostname 2>/dev/null || true)\nwrite_status() {\n    phase=$1\n    code=$2\n    tmp=\"${status_path}.tmp.$$\"\n    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)\n    if [ \"$phase\" = \"running\" ]; then\n        printf '{\"phase\":\"running\",\"hosts\":[\"%s\"],\"started_at\":\"%s\"}\n' \"$hostname\" \"$now\" > \"$tmp\"\n    else\n        printf '{\"phase\":\"%s\",\"hosts\":[\"%s\"],\"exit_code\":%s,\"finished_at\":\"%s\"}\n' \"$phase\" \"$hostname\" \"$code\" \"$now\" > \"$tmp\"\n    fi\n    mv -f \"$tmp\" \"$status_path\"\n}\nwrite_status running 0\ntrap 'write_status cancelled 143; exit 143' TERM\ntrap 'write_status cancelled 130; exit 130' INT\n" + strings.Join(quoted, " ") + "\ncode=$?\nwrite_status finished \"$code\"\nexit \"$code\"\n"
}

func slurmArrayWrapperScript(jobs []JobSpec) string {
	return schedulerArrayWrapperScript(jobs, "SLURM_ARRAY_TASK_ID")
}

// statusWrapperScript wraps command in a shell script that records phase and
// exit code to status.json, so any poll-based executor (Slurm, PBS, ...) can
// determine the final result even if the scheduler's own accounting lags.
func statusWrapperScript(command []string, jobDir string, environment []string, workingDirectory string) string {
	statusPath := filepath.Join(jobDir, "status.json")
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, shellQuote(arg))
	}
	commandLine := strings.Join(quoted, " ")
	exports := make([]string, 0, len(environment))
	for _, entry := range environment {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			exports = append(exports, "export "+parts[0]+"="+shellQuote(parts[1]))
		}
	}
	changeDirectory := ""
	if workingDirectory != "" {
		changeDirectory = "cd " + shellQuote(workingDirectory) + " || exit 1\n"
	}
	return fmt.Sprintf(`#!/bin/sh
set +e
status_path=%s
%s
%s
hostname=$(hostname 2>/dev/null || true)
write_status() {
    phase=$1
    code=$2
    tmp="${status_path}.tmp.$$"
    now=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)
    if [ "$phase" = "running" ]; then
		printf '{"phase":"running","hosts":["%%s"],"started_at":"%%s"}\n' "$hostname" "$now" > "$tmp"
    else
		printf '{"phase":"%%s","hosts":["%%s"],"exit_code":%%s,"finished_at":"%%s"}\n' "$phase" "$hostname" "$code" "$now" > "$tmp"
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
`, shellQuote(statusPath), strings.Join(exports, "\n"), changeDirectory, commandLine)
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
			return jobResultFromStatus(job.JobID, job.Command, status)
		}
		state, err := slurmJobState(job.SlurmJobID)
		if err != nil {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: err.Error()}
		}
		if state != "" {
			writeSchedulerStatus(jobDir, state)
		}
		if state == "" {
			_, _ = os.ReadDir(jobDir)
			if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
				return jobResultFromStatus(job.JobID, job.Command, status)
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
		time.Sleep(slurmPollInterval)
	}
}

func jobResultFromStatus(jobID string, command []string, status slurmStatus) JobResult {
	return JobResult{ID: jobID, Command: command, ExitCode: status.ExitCode, Error: status.Error, Hosts: status.Hosts}
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
	state, err := slurmJobState(jobID)
	return state != "", err
}

func slurmJobState(jobID string) (string, error) {
	output, err := runSlurmCommand("squeue", "--noheader", "--jobs", jobID, "--format=%T")
	if err != nil {
		return "", fmt.Errorf("squeue: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(string(output))), nil
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
