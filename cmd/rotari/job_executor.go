package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// JobHandle carries what a JobExecutor needs to wait for a previously submitted job.
type JobHandle struct {
	Job    JobSpec
	Native string //executor-specific job id (e.g. Slurm job id); empty when unused
}

type schedulerStatus struct {
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at"`
}

func writeSchedulerStatus(jobDir, state string) {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return
	}
	_ = writeJSON(filepath.Join(jobDir, "scheduler_status.json"), schedulerStatus{State: state, UpdatedAt: nowRFC3339()})
}

func loadSchedulerStatus(jobDir string) string {
	data, err := os.ReadFile(filepath.Join(jobDir, "scheduler_status.json"))
	if err != nil {
		return ""
	}
	var status schedulerStatus
	if json.Unmarshal(data, &status) != nil {
		return ""
	}
	return status.State
}

// JobExecutor abstracts a job execution executor (e.g. "local", "slurm"). Adding a
// new scheduler (PBS, LSF, ...) means implementing this interface and
// registering it in the init() below.
type JobExecutor interface {
	Name() string
	Submit(runDir string, job JobSpec, options []string) (JobHandle, error)
	Wait(runDir string, handle JobHandle) JobResult
}

type ArraySubmitter interface {
	SubmitArray(runDir string, jobs []JobSpec, options []string) ([]JobHandle, error)
}

// Suspender is implemented by executor that can pause and resume a running
// job in place (e.g. Slurm, local processes). Executor that cannot support
// this (e.g. Kubernetes) simply do not implement it; callers must type-assert
// before using it.
type Suspender interface {
	Suspend(jobDir string) error
	Resume(jobDir string) error
}

// Canceller is implemented by executor that can cancel an already-submitted
// job. Jobs that were never submitted (no executor owns them yet) are
// cancelled outside of this interface; see cancelJobs.
type Canceller interface {
	Cancel(jobDir string) error
}

// jobOwnerExecutor determines which executor owns the job recorded in jobDir,
// based on the metadata files each executor writes on submission. Every
// scheduler-style executor writes job.json with its own "executor" name, so
// that job.json alone (not its mere existence) tells us which one to use.
func jobOwnerExecutor(jobDir string) (JobExecutor, error) {
	if data, err := os.ReadFile(filepath.Join(jobDir, "job.json")); err == nil {
		var meta struct {
			Executor string `json:"executor"`
		}
		if err := json.Unmarshal(data, &meta); err == nil {
			if executor, ok := lookupExecutor(meta.Executor); ok {
				return executor, nil
			}
		}
	}
	if _, err := os.Stat(filepath.Join(jobDir, "pid")); err == nil {
		executor, _ := lookupExecutor("local")
		return executor, nil
	}
	return nil, fmt.Errorf("job is not running")
}

// localExecutorHostMismatch reports the run's recorded hostname when the
// given job belongs to the "local" executor and this process is running on a
// different host. The local executor signals jobs by PID, which is only
// meaningful on the host that actually spawned the process; over a shared
// base directory, "job is not running" from a failed PID check on the wrong
// host is misleading, so callers should surface this instead before trying.
func localExecutorHostMismatch(executor JobExecutor, runDir string) (recordedHost string, mismatch bool) {
	if executor.Name() != "local" {
		return "", false
	}
	safeRunDir := filepath.Join(filepath.Dir(runDir), filepath.Base(runDir))
	data, err := os.ReadFile(filepath.Join(safeRunDir, "context.json"))
	if err != nil {
		return "", false
	}
	var context RunContext
	if json.Unmarshal(data, &context) != nil || context.Hostname == "" {
		return "", false
	}
	host, err := os.Hostname()
	if err != nil || strings.EqualFold(host, context.Hostname) {
		return "", false
	}
	return context.Hostname, true
}

// runningWorkerHostMismatch reports the recorded host when a run's
// running.lock belongs to a different host than this process. Whole-run
// cancel (no --job-id) signals the runner's process group by the PID stored
// in that lock; on the wrong host that PID belongs to (at best) nothing, so
// the signal harmlessly returns ESRCH and callers ignore it -- silently
// reporting success without actually cancelling anything. This mirrors the
// host check `isRunning` already applies before trusting a local PID.
func runningWorkerHostMismatch(lock LockInfo) (recordedHost string, mismatch bool) {
	if lock.Host == "" {
		return "", false
	}
	host, err := os.Hostname()
	if err != nil || strings.EqualFold(host, lock.Host) {
		return "", false
	}
	return lock.Host, true
}

// schedulerCommandHint clarifies two common causes of an opaque scheduler
// control command failure: the scheduler's client tools (scontrol/qsig/
// bstop/...) not being installed on this host -- e.g. a web/CLI host outside
// the cluster that only shares the state directory over NFS, where the raw
// "executable file not found in $PATH" is easy to mistake for the job itself
// not running -- and the command running fine but being rejected by the
// scheduler itself (e.g. suspending a job that is still queued/pending
// rather than actually running), where Go's generic "exit status 1" hides
// the scheduler's own explanation unless the command's output is folded in.
func schedulerCommandHint(binary string, output []byte, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%q is not installed on this host; run this command from a host with that scheduler's client tools (%w)", binary, err)
	}
	if text := strings.TrimSpace(string(output)); text != "" {
		return fmt.Errorf("%w: %s", err, text)
	}
	return err
}

// executorRegistry is initialized eagerly (rather than in an init func) so
// that other package-level vars, such as cliCommandSpecs, can depend on
// executorNames() during their own initialization.
var executorRegistry = map[string]JobExecutor{
	"local": localExecutor{},
	"slurm": slurmExecutor{},
	"pbs":   pbsExecutor{},
	"lsf":   lsfExecutor{},
	"ssh":   sshExecutor{},
}

func lookupExecutor(name string) (JobExecutor, bool) {
	b, ok := executorRegistry[name]
	return b, ok
}

func isKnownExecutor(name string) bool {
	_, ok := executorRegistry[name]
	return ok
}

// executorNames returns the registered executor names, sorted for stable output.
func executorNames() []string {
	names := make([]string, 0, len(executorRegistry))
	for name := range executorRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
