package main

import (
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

const pbsAccountingWait = 60 * time.Second
const pbsCommandTimeout = 30 * time.Second

type pbsJobMetadata struct {
	Executor    string   `json:"executor"`
	JobID       string   `json:"job_id"`
	Command     []string `json:"command"`
	PBSJobID    string   `json:"pbs_job_id"`
	SubmittedAt string   `json:"submitted_at"`
}

// pbsExecutor submits jobs to a PBS/Torque scheduler via qsub and tracks them
// through qstat, mirroring the Slurm executor's submit/poll/accounting model.
type pbsExecutor struct{}

func (pbsExecutor) Name() string { return "pbs" }

func (pbsExecutor) Submit(runDir string, job JobSpec, options []string) (JobHandle, error) {
	metadata, err := submitPBSJob(runDir, job, options)
	if err != nil {
		return JobHandle{}, err
	}
	return JobHandle{Job: job, Native: metadata.PBSJobID}, nil
}

func (pbsExecutor) Wait(runDir string, handle JobHandle) JobResult {
	metadata := pbsJobMetadata{
		Executor: "pbs",
		JobID:    handle.Job.ID,
		Command:  handle.Job.Command,
		PBSJobID: handle.Native,
	}
	return waitPBSJob(runDir, metadata)
}

func (pbsExecutor) Suspend(jobDir string) error {
	return pbsExecutor{}.qsig(jobDir, "suspend")
}

func (pbsExecutor) Resume(jobDir string) error {
	return pbsExecutor{}.qsig(jobDir, "resume")
}

func (pbsExecutor) qsig(jobDir, signal string) error {
	metadata, err := readPBSMetadata(jobDir)
	if err != nil {
		return err
	}
	if _, err := runPBSCommand("qsig", "-s", signal, metadata.PBSJobID); err != nil {
		return fmt.Errorf("qsig -s %s %s: %w", signal, metadata.PBSJobID, err)
	}
	return nil
}

func (pbsExecutor) Cancel(jobDir string) error {
	metadata, err := readPBSMetadata(jobDir)
	if err != nil {
		return err
	}
	if _, err := runPBSCommand("qdel", metadata.PBSJobID); err != nil {
		return fmt.Errorf("qdel %s: %w", metadata.PBSJobID, err)
	}
	return nil
}

func readPBSMetadata(jobDir string) (pbsJobMetadata, error) {
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		return pbsJobMetadata{}, fmt.Errorf("job is not running")
	}
	var metadata pbsJobMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return pbsJobMetadata{}, fmt.Errorf("invalid PBS metadata: %w", err)
	}
	return metadata, nil
}

func submitPBSJob(runDir string, job JobSpec, options []string) (pbsJobMetadata, error) {
	jobDir := filepath.Join(runDir, job.ID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return pbsJobMetadata{}, err
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return pbsJobMetadata{}, err
	}
	wrapperPath := filepath.Join(jobDir, "pbs-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(statusWrapperScript(job.Command, jobDir)), 0o755); err != nil {
		return pbsJobMetadata{}, err
	}
	outputPath := filepath.Join(jobDir, "output")
	args := []string{"-j", "oe", "-o", outputPath}
	expandedOptions, err := expandShellOptions(options)
	if err != nil {
		return pbsJobMetadata{}, err
	}
	args = append(args, expandedOptions...)
	args = append(args, wrapperPath)
	output, err := runPBSCommand("qsub", args...)
	if err != nil {
		return pbsJobMetadata{}, fmt.Errorf("qsub: %w: %s", err, strings.TrimSpace(string(output)))
	}
	pbsJobID := strings.TrimSpace(string(output))
	if pbsJobID == "" {
		return pbsJobMetadata{}, errors.New("qsub returned an empty job id")
	}
	metadata := pbsJobMetadata{
		Executor: "pbs", JobID: job.ID, Command: job.Command,
		PBSJobID: pbsJobID, SubmittedAt: nowRFC3339(),
	}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), metadata); err != nil {
		return pbsJobMetadata{}, err
	}
	fmt.Printf("[%s] submit job=%s pbs_job_id=%s command=%s\n", metadata.SubmittedAt, job.ID, pbsJobID, strings.Join(job.Command, " "))
	return metadata, nil
}

func waitPBSJob(runDir string, job pbsJobMetadata) JobResult {
	jobDir := filepath.Join(runDir, job.JobID)
	statusPath := filepath.Join(jobDir, "status.json")
	var accountingDeadline time.Time
	for {
		if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: status.ExitCode, Error: status.Error}
		}
		active, err := pbsJobActive(job.PBSJobID)
		if err != nil {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: err.Error()}
		}
		if !active {
			if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: status.ExitCode, Error: status.Error}
			}
			if accountingDeadline.IsZero() {
				accountingDeadline = time.Now().Add(pbsAccountingWait)
			}
			if exitCode, ok := pbsAccounting(job.PBSJobID); ok {
				_ = writeJSON(statusPath, slurmStatus{Phase: "finished", ExitCode: exitCode, FinishedAt: nowRFC3339()})
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: exitCode}
			}
			if time.Now().After(accountingDeadline) {
				return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: "PBS accounting result and wrapper status are unavailable"}
			}
		}
		time.Sleep(time.Second)
	}
}

// pbsJobActive reports whether the scheduler still tracks the job. qstat
// exits non-zero once a job has been purged from its queue view.
func pbsJobActive(jobID string) (bool, error) {
	_, err := runPBSCommand("qstat", jobID)
	return err == nil, nil
}

// pbsAccounting parses "exit_status = N" out of qstat's full/history output,
// PBS's equivalent of Slurm's sacct.
func pbsAccounting(jobID string) (int, bool) {
	output, err := runPBSCommand("qstat", "-xf", jobID)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "exit_status") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		code, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		return code, true
	}
	return 0, false
}

func runPBSCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pbsCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", name, pbsCommandTimeout)
	}
	return output, err
}
