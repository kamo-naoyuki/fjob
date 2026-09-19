package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errNoPreviousRun = errors.New("no previous run")

func resultSelection(failed, unfinished, success bool) string {
	selections := make([]string, 0, 3)
	if failed {
		selections = append(selections, "failed")
	}
	if unfinished {
		selections = append(selections, "unfinished")
	}
	if success {
		selections = append(selections, "success")
	}
	return strings.Join(selections, ",")
}

func resultSelectionMatches(selection string, finished bool, exitCode int) bool {
	for _, filter := range strings.Split(selection, ",") {
		switch filter {
		case "failed":
			if finished && exitCode != 0 {
				return true
			}
		case "unfinished":
			if !finished {
				return true
			}
		case "success":
			if finished && exitCode == 0 {
				return true
			}
		}
	}
	return false
}

// aggregatedJobResult returns the result and finished state for a command
// ID, aggregating per-task results when array is non-nil: results are keyed
// per task (e.g. "id-1", "id-2", ...) by queueToJobs, not by the array
// command's own ID, so it is only considered finished once every task has a
// result, and any non-zero task exit code marks the whole command as failed.
func aggregatedJobResult(id string, array *ArraySpec, results map[string]JobResult) (JobResult, bool) {
	if array == nil {
		result, finished := results[id]
		return result, finished
	}
	aggregate := JobResult{ID: id}
	for _, task := range arrayTaskIDs(array) {
		result, ok := results[fmt.Sprintf("%s-%d", id, task)]
		if !ok {
			return JobResult{}, false
		}
		if result.ExitCode != 0 && aggregate.ExitCode == 0 {
			aggregate.ExitCode = result.ExitCode
			aggregate.Error = result.Error
		}
	}
	return aggregate, true
}

// rerunPlan splits a queue's commands between jobs that must be executed and
// jobs whose previous result should be carried forward into the new run
// instead of being re-executed.
type rerunPlan struct {
	// Execute holds the IDs of commands that must run normally.
	Execute map[string]bool
	// CarriedResults holds the previous JobResult for commands that are
	// skipped this run but already have a finished result to reuse.
	CarriedResults map[string]JobResult
	// CarriedOrigins holds the Origin to attach to carried commands so
	// viewers can find the original output.
	CarriedOrigins map[string]*JobOrigin
}

// planRerunSelection decides, for a run of the given queue, which jobs must
// actually execute. When selection is empty every job executes normally.
// When selection filters are provided, only matching jobs execute; jobs that
// do not match but already have a finished result in referenceRunID are
// carried forward (Origin recorded, no re-execution); jobs that neither
// match nor have a previous result are left untouched (no result recorded,
// shown as still unfinished).
//
// When partialArray is true (the default), array jobs are evaluated per
// task instead of as one unit: only tasks matching the selection (e.g. the
// failed ones) execute, and the rest carry forward their own result. When
// false, the whole array re-executes if any of its tasks match, restoring
// the older, coarser behavior.
func planRerunSelection(paths pathSet, queue Queue, selection string, jobIDs []string, referenceRunID string, partialArray bool) (rerunPlan, error) {
	plan := rerunPlan{Execute: make(map[string]bool, len(queue.Commands))}
	if selection == "" {
		for _, command := range queue.Commands {
			plan.Execute[command.ID] = true
		}
		return plan, nil
	}

	runID := referenceRunID
	if runID == "" {
		meta, err := loadMeta(paths.metaFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return rerunPlan{}, fmt.Errorf("project '%s' has no previous run: %w", paths.queueName, errNoPreviousRun)
			}
			return rerunPlan{}, fmt.Errorf("failed to load metadata: %w", err)
		}
		if meta.LastRunID == "" {
			return rerunPlan{}, fmt.Errorf("project '%s' has no previous run: %w", paths.queueName, errNoPreviousRun)
		}
		runID = meta.LastRunID
	}

	runDir := filepath.Join(paths.runsDir, runID)
	summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
	if err != nil {
		return rerunPlan{}, fmt.Errorf("failed to load run summary: %w", err)
	}
	results := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		results[result.ID] = result
	}
	originCWD := ""
	if data, contextErr := os.ReadFile(filepath.Join(runDir, "context.json")); contextErr == nil {
		var context RunContext
		if json.Unmarshal(data, &context) == nil {
			originCWD = context.CWD
		}
	}

	requested := make(map[string]bool, len(jobIDs))
	for _, jobID := range jobIDs {
		requested[jobID] = true
	}
	plan.CarriedResults = make(map[string]JobResult)
	plan.CarriedOrigins = make(map[string]*JobOrigin)
	for _, command := range queue.Commands {
		if command.Array != nil && partialArray && selection != "job-id" {
			planArrayTaskSelection(command, selection, results, runID, runDir, originCWD, &plan)
			continue
		}
		result, finished := aggregatedJobResult(command.ID, command.Array, results)
		include := false
		switch selection {
		case "job-id":
			include = requested[command.ID]
		default:
			include = resultSelectionMatches(selection, finished, result.ExitCode)
		}
		if requested[command.ID] {
			include = true
			delete(requested, command.ID)
		}
		if include {
			plan.Execute[command.ID] = true
			continue
		}
		if !finished {
			// No previous result to carry forward; leave it unfinished.
			continue
		}
		status := "failed"
		if result.ExitCode == 0 {
			status = "success"
		}
		if command.Array == nil {
			plan.CarriedResults[command.ID] = result
		} else {
			// finalResults is keyed per expanded array task (see
			// queueToJobs), not by the array command's own ID.
			for _, task := range arrayTaskIDs(command.Array) {
				taskID := fmt.Sprintf("%s-%d", command.ID, task)
				plan.CarriedResults[taskID] = results[taskID]
			}
		}
		plan.CarriedOrigins[command.ID] = &JobOrigin{
			RunID:       runID,
			JobID:       command.ID,
			Status:      status,
			CWD:         originCWD,
			SubmittedAt: readJobTimestamp(runDir, command.ID, "submitted_at"),
			FinishedAt:  readJobTimestamp(runDir, command.ID, "finished_at"),
		}
	}
	if len(requested) > 0 {
		missing := make([]string, 0, len(requested))
		for jobID := range requested {
			missing = append(missing, jobID)
		}
		sort.Strings(missing)
		return rerunPlan{}, fmt.Errorf("job IDs not found in queue: %s", strings.Join(missing, ", "))
	}
	return plan, nil
}

// planArrayTaskSelection evaluates each task of an array command on its own
// (unlike aggregatedJobResult, which treats the whole array as one unit):
// only tasks matching selection are added to Execute, and finished
// non-matching tasks carry their own result forward instead of the whole
// array re-executing. The per-task Origin (keyed by task ID, e.g. "id-1")
// lets show/web follow a carried task back to its own job directory in the
// reference run, since QueuedCommand.Origin only covers the whole command.
func planArrayTaskSelection(command QueuedCommand, selection string, results map[string]JobResult, runID, runDir, originCWD string, plan *rerunPlan) {
	for _, task := range arrayTaskIDs(command.Array) {
		taskID := fmt.Sprintf("%s-%d", command.ID, task)
		result, finished := results[taskID]
		if resultSelectionMatches(selection, finished, result.ExitCode) {
			plan.Execute[taskID] = true
			continue
		}
		if !finished {
			// No previous result to carry forward; leave it unfinished.
			continue
		}
		plan.CarriedResults[taskID] = result
		status := "failed"
		if result.ExitCode == 0 {
			status = "success"
		}
		plan.CarriedOrigins[taskID] = &JobOrigin{
			RunID:       runID,
			JobID:       taskID,
			Status:      status,
			CWD:         originCWD,
			SubmittedAt: readJobTimestamp(runDir, taskID, "submitted_at"),
			FinishedAt:  readJobTimestamp(runDir, taskID, "finished_at"),
		}
	}
}
