package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// JobHandle carries what a JobExecutor needs to wait for a previously submitted job.
type JobHandle struct {
	Job    JobSpec
	Native string //executor-specific job id (e.g. Slurm job id); empty when unused
}

// JobExecutor abstracts a job execution executor (e.g. "local", "slurm"). Adding a
// new scheduler (PBS, LSF, ...) means implementing this interface and
// registering it in the init() below.
type JobExecutor interface {
	Name() string
	Submit(runDir string, job JobSpec, options []string) (JobHandle, error)
	Wait(runDir string, handle JobHandle) JobResult
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

// executorRegistry is initialized eagerly (rather than in an init func) so
// that other package-level vars, such as cliCommandSpecs, can depend on
// executorNames() during their own initialization.
var executorRegistry = map[string]JobExecutor{
	"local": localExecutor{},
	"slurm": slurmExecutor{},
	"pbs":   pbsExecutor{},
	"lsf":   lsfExecutor{},
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
