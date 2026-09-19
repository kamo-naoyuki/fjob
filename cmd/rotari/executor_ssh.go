package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type sshJobMetadata struct {
	Executor    string   `json:"executor"`
	JobID       string   `json:"job_id"`
	Command     []string `json:"command"`
	Host        string   `json:"host"`
	PID         int      `json:"pid"`
	SubmittedAt string   `json:"submitted_at"`
}

type sshExecutor struct{}

type sshProcess struct {
	command *exec.Cmd
	output  *os.File
}

var sshProcesses = struct {
	sync.Mutex
	commands map[int]sshProcess
}{commands: make(map[int]sshProcess)}

func (sshExecutor) Name() string { return "ssh" }

func (sshExecutor) Submit(runDir string, job JobSpec, options []string) (JobHandle, error) {
	host, sshOptions, err := sshTarget(options)
	if err != nil {
		return JobHandle{}, err
	}
	jobDir, err := validatedJobDir(runDir, job.ID)
	if err != nil {
		return JobHandle{}, err
	}
	if err := os.MkdirAll(jobDir, stateDirMode()); err != nil {
		return JobHandle{}, err
	}
	if err := writeJSON(filepath.Join(jobDir, "command.json"), job); err != nil {
		return JobHandle{}, err
	}
	output, err := os.Create(filepath.Join(jobDir, "output"))
	if err != nil {
		return JobHandle{}, err
	}
	cmd := exec.Command("/usr/bin/ssh", append(sshOptions, "--", host, "sh", "-s")...)
	cmd.Stdin = strings.NewReader(sshWrapperScript(job.Command, job.Environment, job.WorkingDirectory))
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		_ = output.Close()
		return JobHandle{}, fmt.Errorf("ssh %s: %w", host, err)
	}
	metadata := sshJobMetadata{Executor: "ssh", JobID: job.ID, Command: job.Command, Host: host, PID: cmd.Process.Pid, SubmittedAt: nowRFC3339()}
	if err := writeJSON(filepath.Join(jobDir, "job.json"), metadata); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = output.Close()
		return JobHandle{}, err
	}
	sshProcesses.Lock()
	sshProcesses.commands[cmd.Process.Pid] = sshProcess{command: cmd, output: output}
	sshProcesses.Unlock()
	return JobHandle{Job: job, Native: strconv.Itoa(cmd.Process.Pid)}, nil
}

func (sshExecutor) Wait(runDir string, handle JobHandle) JobResult {
	jobDir, err := validatedJobDir(runDir, handle.Job.ID)
	if err != nil {
		return JobResult{ID: handle.Job.ID, Command: handle.Job.Command, ExitCode: 1, Error: err.Error()}
	}
	metadata, err := readSSHMetadata(jobDir)
	if err != nil {
		return JobResult{ID: handle.Job.ID, Command: handle.Job.Command, ExitCode: 1, Error: err.Error()}
	}
	sshProcesses.Lock()
	process, ok := sshProcesses.commands[metadata.PID]
	sshProcesses.Unlock()
	if !ok {
		return JobResult{ID: handle.Job.ID, Command: handle.Job.Command, ExitCode: 1, Error: "SSH job is no longer managed by this process"}
	}
	err = process.command.Wait()
	_ = process.output.Close()
	sshProcesses.Lock()
	delete(sshProcesses.commands, metadata.PID)
	sshProcesses.Unlock()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		}
	}
	status := slurmStatus{Phase: "finished", ExitCode: exitCode, FinishedAt: nowRFC3339(), Hosts: []string{metadata.Host}}
	if err != nil {
		status.Error = err.Error()
	}
	_ = writeJSON(filepath.Join(jobDir, "status.json"), status)
	return jobResultFromStatus(handle.Job.ID, handle.Job.Command, status)
}

func (sshExecutor) Cancel(jobDir string) error {
	metadata, err := readSSHMetadata(jobDir)
	if err != nil {
		return err
	}
	sshProcesses.Lock()
	process, ok := sshProcesses.commands[metadata.PID]
	sshProcesses.Unlock()
	if !ok || process.command.Process == nil {
		return fmt.Errorf("SSH job is not managed by this process")
	}
	return process.command.Process.Signal(syscall.SIGTERM)
}

func readSSHMetadata(jobDir string) (sshJobMetadata, error) {
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		return sshJobMetadata{}, fmt.Errorf("job is not running")
	}
	var metadata sshJobMetadata
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Executor != "ssh" {
		return sshJobMetadata{}, fmt.Errorf("invalid SSH metadata")
	}
	return metadata, nil
}

func sshTarget(options []string) (string, []string, error) {
	expanded, err := expandShellOptions(options)
	if err != nil {
		return "", nil, err
	}
	if len(expanded) == 0 || expanded[0] == "" {
		return "", nil, fmt.Errorf("SSH executor requires its first executor option to be the target host")
	}
	if strings.HasPrefix(expanded[0], "-") {
		return "", nil, fmt.Errorf("invalid SSH target host %q", expanded[0])
	}
	return expanded[0], expanded[1:], nil
}

func sshWrapperScript(command []string, environment []string, workingDirectory string) string {
	exports := make([]string, 0, len(environment))
	for _, entry := range environment {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			exports = append(exports, "export "+parts[0]+"="+shellQuote(parts[1]))
		}
	}
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, shellQuote(arg))
	}
	changeDirectory := ""
	if workingDirectory != "" {
		changeDirectory = "cd " + shellQuote(workingDirectory) + " || exit 1\n"
	}
	return "#!/bin/sh\nset +e\n" + strings.Join(exports, "\n") + "\n" + changeDirectory + "exec " + strings.Join(quoted, " ") + "\n"
}
