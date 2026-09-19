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

func (pbsExecutor) SubmitArray(runDir string, jobs []JobSpec, options []string) ([]JobHandle, error) {
	return submitPBSArray(runDir, jobs, options)
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
	if err := os.WriteFile(wrapperPath, []byte(statusWrapperScript(job.Command, jobDir, job.Environment, job.WorkingDirectory)), 0o755); err != nil {
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

func submitPBSArray(runDir string, jobs []JobSpec, executorOptions []string) ([]JobHandle, error) {
	if len(jobs) == 0 || jobs[0].ArrayTaskID == nil {
		return nil, errors.New("empty PBS array")
	}
	command := jobs[0].Command
	first, last := jobs[0].ArrayFirst, jobs[0].ArrayLast
	for _, job := range jobs {
		if job.ArrayTaskID == nil || job.ArrayFirst != first || job.ArrayLast != last || !sameStrings(job.Command, command) {
			return nil, errors.New("PBS array tasks must share one command and range")
		}
		jobDir := filepath.Join(runDir, job.ID)
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
			return nil, err
		}
	}
	if err := rejectArraySchedulerOptions(executorOptions, "-J", "-t"); err != nil {
		return nil, err
	}
	wrapperPath := filepath.Join(runDir, jobs[0].ArrayGroup+"-pbs-array-wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(schedulerArrayWrapperScript(jobs, "PBS_ARRAY_INDEX")), 0o755); err != nil {
		return nil, err
	}
	expandedOptions, err := expandShellOptions(executorOptions)
	if err != nil {
		return nil, err
	}
	args := []string{"-j", "oe", "-o", "/dev/null", "-J", fmt.Sprintf("%d-%d", first, last)}
	args = append(args, expandedOptions...)
	args = append(args, wrapperPath)
	output, err := runPBSCommand("qsub", args...)
	if err != nil {
		return nil, fmt.Errorf("qsub array: %w: %s", err, strings.TrimSpace(string(output)))
	}
	masterID := strings.TrimSpace(string(output))
	if bracket := strings.Index(masterID, "["); bracket >= 0 {
		masterID = masterID[:bracket] + strings.TrimPrefix(masterID[strings.Index(masterID, "]")+1:], "]")
	}
	if masterID == "" {
		return nil, errors.New("qsub returned an empty array job id")
	}
	handles := make([]JobHandle, 0, len(jobs))
	for _, job := range jobs {
		taskID := *job.ArrayTaskID
		nativeID := fmt.Sprintf("%s[%d]", masterID, taskID)
		metadata := pbsJobMetadata{Executor: "pbs", JobID: job.ID, Command: job.Command, PBSJobID: nativeID, SubmittedAt: nowRFC3339()}
		if err := writeJSON(filepath.Join(runDir, job.ID, "job.json"), metadata); err != nil {
			return nil, err
		}
		handles = append(handles, JobHandle{Job: job, Native: nativeID})
	}
	return handles, nil
}

func waitPBSJob(runDir string, job pbsJobMetadata) JobResult {
	jobDir := filepath.Join(runDir, job.JobID)
	statusPath := filepath.Join(jobDir, "status.json")
	var accountingDeadline time.Time
	for {
		if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
			return jobResultFromStatus(job.JobID, job.Command, status)
		}
		state, err := pbsJobState(job.PBSJobID)
		if err != nil {
			return JobResult{ID: job.JobID, Command: job.Command, ExitCode: 1, Error: err.Error()}
		}
		if state != "" {
			writeSchedulerStatus(jobDir, state)
		}
		if state == "" {
			if status, ok := loadSlurmStatus(statusPath); ok && status.Phase == "finished" {
				return jobResultFromStatus(job.JobID, job.Command, status)
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
	state, err := pbsJobState(jobID)
	return state != "", err
}

func pbsJobState(jobID string) (string, error) {
	output, err := runPBSCommand("qstat", "-f", jobID)
	if err != nil {
		return "", nil
	}
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != "job_state" {
			continue
		}
		switch strings.TrimSpace(parts[1]) {
		case "Q":
			return "pending", nil
		case "R", "E":
			return "running", nil
		case "H":
			return "held", nil
		case "S":
			return "suspended", nil
		case "W":
			return "waiting", nil
		default:
			return strings.ToLower(strings.TrimSpace(parts[1])), nil
		}
	}
	return "", nil
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
