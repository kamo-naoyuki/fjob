package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const lsfAccountingWait = 60 * time.Second
const lsfCommandTimeout = 30 * time.Second

type lsfJobMetadata struct {
	Executor    string   `json:"executor"`
	JobID       string   `json:"job_id"`
	Command     []string `json:"command"`
	LSFJobID    string   `json:"lsf_job_id"`
	SubmittedAt string   `json:"submitted_at"`
}

// lsfExecutor submits jobs to IBM LSF via bsub and tracks them with bjobs/bhist.
type lsfExecutor struct{}

func (lsfExecutor) Name() string { return "lsf" }

func (lsfExecutor) Submit(runDir string, job JobSpec, options []string) (JobHandle, error) {
	metadata, err := submitLSFJob(runDir, job, options)
	if err != nil {
		return JobHandle{}, err
	}
	return JobHandle{Job: job, Native: metadata.LSFJobID}, nil
}

func (lsfExecutor) Wait(runDir string, handle JobHandle) JobResult {
	metadata := lsfJobMetadata{
		Executor: "lsf",
		JobID:    handle.Job.ID,
		Command:  handle.Job.Command,
		LSFJobID: handle.Native,
	}
	return waitLSFJob(runDir, metadata)
}

func (lsfExecutor) Suspend(jobDir string) error {
	return lsfExecutor{}.runControl(jobDir, "bstop")
}

func (lsfExecutor) Resume(jobDir string) error {
	return lsfExecutor{}.runControl(jobDir, "bresume")
}

func (lsfExecutor) Cancel(jobDir string) error {
	metadata, err := readLSFMetadata(jobDir)
	if err != nil {
		return err
	}
	if _, err := runLSFCommand("bkill", metadata.LSFJobID); err != nil {
		return fmt.Errorf("bkill %s: %w", metadata.LSFJobID, err)
	}
	return nil
}

func (lsfExecutor) runControl(jobDir, command string) error {
	metadata, err := readLSFMetadata(jobDir)
	if err != nil {
		return err
	}
	if _, err := runLSFCommand(command, metadata.LSFJobID); err != nil {
		return fmt.Errorf("%s %s: %w", command, metadata.LSFJobID, err)
	}
	return nil
}

func readLSFMetadata(jobDir string) (lsfJobMetadata, error) {
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		return lsfJobMetadata{}, errors.New("job is not running")
	}
	var metadata lsfJobMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return lsfJobMetadata{}, fmt.Errorf("invalid LSF metadata: %w", err)
	}
	return metadata, nil
}

func submitLSFJob(runDir string, job JobSpec, options []string) (lsfJobMetadata, error) {
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return lsfJobMetadata{}, err
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return lsfJobMetadata{}, err
	}
	outputPath := filepath.Join(jobDir, "output")
	wrapperPath := filepath.Join(jobDir, "lsf-wrapper.sh")
	wrapper := lsfWrapperScript(job.Command, jobDir, outputPath)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		return lsfJobMetadata{}, err
	}
	expandedOptions, err := expandShellOptions(options)
	if err != nil {
		return lsfJobMetadata{}, err
	}
	args := append([]string{"bsub"}, expandedOptions...)
	output, err := runLSFCommandWithInput(bytes.NewReader([]byte(wrapper)), args...)
	if err != nil {
		return lsfJobMetadata{}, fmt.Errorf("bsub: %w: %s", err, strings.TrimSpace(string(output)))
	}
	jobID, err := parseLSFJobID(string(output))
	if err != nil {
		return lsfJobMetadata{}, err
	}
	metadata := lsfJobMetadata{
		Executor: "lsf", JobID: job.ID, Command: job.Command,
		LSFJobID: jobID, SubmittedAt: nowRFC3339(),
	}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), metadata); err != nil {
		return lsfJobMetadata{}, err
	}
	fmt.Printf("[%s] submit job=%s lsf_job_id=%s command=%s\n", metadata.SubmittedAt, job.ID, jobID, strings.Join(job.Command, " "))
	return metadata, nil
}

func lsfWrapperScript(command []string, jobDir, outputPath string) string {
	return "#BSUB -o " + shellQuote(outputPath) + "\n#BSUB -e " + shellQuote(outputPath) + "\n" + statusWrapperScript(command, jobDir)
}

var lsfJobIDPattern = regexp.MustCompile(`<([0-9]+)>`)

func parseLSFJobID(output string) (string, error) {
	match := lsfJobIDPattern.FindStringSubmatch(output)
	if len(match) == 2 {
		return match[1], nil
	}
	return "", errors.New("bsub returned no job id")
}

func waitLSFJob(runDir string, job lsfJobMetadata) JobResult {
	jobDir := filepath.Join(runDir, job.JobID)
	statusPath := filepath.Join(jobDir, "status.json")
	var accountingDeadline time.Time
	for {
		if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
			return jobResultFromStatus(job.JobID, job.Command, status)
		}
		active, err := lsfJobActive(job.LSFJobID)
		if err != nil {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: err.Error()}
		}
		if !active {
			if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
				return jobResultFromStatus(job.JobID, job.Command, status)
			}
			if accountingDeadline.IsZero() {
				accountingDeadline = time.Now().Add(lsfAccountingWait)
			}
			if exitCode, ok := lsfAccounting(job.LSFJobID); ok {
				_ = writeJSON(statusPath, slurmStatus{Phase: "finished", ExitCode: exitCode, FinishedAt: nowRFC3339()})
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: exitCode}
			}
			if time.Now().After(accountingDeadline) {
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: "LSF accounting result and wrapper status are unavailable"}
			}
		}
		time.Sleep(time.Second)
	}
}

func lsfJobActive(jobID string) (bool, error) {
	_, err := runLSFCommand("bjobs", "-noheader", jobID)
	return err == nil, nil
}

func lsfAccounting(jobID string) (int, bool) {
	output, err := runLSFCommand("bjobs", "-a", "-noheader", "-o", "stat exit_code", jobID)
	if err != nil {
		output, err = runLSFCommand("bhist", "-l", jobID)
		if err != nil {
			return 0, false
		}
	}
	text := string(output)
	if strings.Contains(text, "DONE") || strings.Contains(text, "Done successfully") {
		return 0, true
	}
	if match := regexp.MustCompile(`(?:EXIT|Exited)\s*\(?([0-9]+)\)?`).FindStringSubmatch(text); len(match) == 2 {
		code, parseErr := strconv.Atoi(match[1])
		return code, parseErr == nil
	}
	if match := regexp.MustCompile(`\s([0-9]+)\s*$`).FindStringSubmatch(strings.TrimSpace(text)); len(match) == 2 {
		code, parseErr := strconv.Atoi(match[1])
		return code, parseErr == nil
	}
	return 0, false
}

func runLSFCommand(name string, args ...string) ([]byte, error) {
	return runLSFCommandWithInput(nil, append([]string{name}, args...)...)
}

func runLSFCommandWithInput(input *bytes.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lsfCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	if input != nil {
		command.Stdin = input
	}
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", args[0], lsfCommandTimeout)
	}
	return output, err
}
