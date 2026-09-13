package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func executeMixedRun(paths pathSet, runID string, localConcurrency, slurmMaxActive, retry int, requestedBackend string, sbatchOptions []string, progress func(JobResult, int, int, int, int)) int {
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load queue: %v\n", err)
		return 1
	}
	jobs := queueToJobs(queue.Commands)
	if len(jobs) == 0 {
		fmt.Fprintf(os.Stderr, "queue '%s' has no valid commands\n", paths.queueName)
		return 1
	}
	runDir := filepath.Join(paths.runsDir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return 1
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		return 1
	}

	finalResults := make(map[string]JobResult, len(jobs))
	pending := append([]JobSpec(nil), jobs...)
	for attempt := 0; (retry == -1 || attempt <= retry) && len(pending) > 0; attempt++ {
		attemptResults := executeMixedAttempt(runDir, queue, pending, localConcurrency, slurmMaxActive, requestedBackend, sbatchOptions)
		for _, result := range attemptResults {
			finalResults[result.ID] = result
		}
		nextPending := make([]JobSpec, 0, len(jobs))
		for _, job := range jobs {
			result, ok := finalResults[job.ID]
			if ok && result.ExitCode != 0 {
				nextPending = append(nextPending, job)
			}
		}
		pending = nextPending
		if len(pending) > 0 && (retry == -1 || attempt < retry) && progress != nil {
			completed, succeeded, failed := summarizeResults(finalResults)
			for _, job := range pending {
				progress(JobResult{ID: job.ID, Command: job.Command, Error: fmt.Sprintf("retry:%d", attempt+1)}, completed, len(jobs), succeeded, failed)
			}
		}
		if progress != nil {
			completed, succeeded, failed := summarizeResults(finalResults)
			for _, result := range attemptResults {
				progress(result, completed, len(jobs), succeeded, failed)
			}
		}
	}

	summary := RunSummary{RunID: runID, StartedAt: nowRFC3339(), FinishedAt: nowRFC3339(), Results: make([]JobResult, 0, len(jobs))}
	for _, job := range jobs {
		result := finalResults[job.ID]
		summary.Results = append(summary.Results, result)
		if result.ExitCode != 0 {
			summary.ExitCode = 1
		}
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		return 1
	}
	return summary.ExitCode
}

func executeMixedAttempt(runDir string, queue Queue, jobs []JobSpec, localConcurrency, slurmMaxActive int, requestedBackend string, sbatchOptions []string) []JobResult {
	defaultBackend := requestedBackend
	if defaultBackend == "" {
		defaultBackend = queue.DefaultBackend
	}
	if defaultBackend == "" {
		defaultBackend = "local"
	}
	localJobs := make([]JobSpec, 0)
	slurmJobs := make([]JobSpec, 0)
	for _, job := range jobs {
		jobBackend := job.Backend
		if jobBackend == "" {
			jobBackend = defaultBackend
		}
		if jobBackend == "slurm" {
			slurmJobs = append(slurmJobs, job)
		} else {
			localJobs = append(localJobs, job)
		}
	}

	results := make(chan JobResult, len(jobs))
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		sem := make(chan struct{}, localConcurrency)
		var jobsWait sync.WaitGroup
		for _, job := range localJobs {
			jobsWait.Add(1)
			go func(job JobSpec) {
				defer jobsWait.Done()
				sem <- struct{}{}
				results <- runOneJob(runDir, job)
				<-sem
			}(job)
		}
		jobsWait.Wait()
	}()
	go func() {
		defer workers.Done()
		for start := 0; start < len(slurmJobs); start += slurmMaxActive {
			end := start + slurmMaxActive
			if end > len(slurmJobs) {
				end = len(slurmJobs)
			}
			metadata := make([]slurmJobMetadata, 0, end-start)
			for _, job := range slurmJobs[start:end] {
				options := job.SbatchOptions
				if len(options) == 0 {
					options = sbatchOptions
				}
				if len(options) == 0 {
					options = queue.DefaultSbatchOptions
				}
				submitted, err := submitSlurmJob(runDir, job, options)
				if err != nil {
					results <- JobResult{ID: job.ID, ExitCode: 1, Error: err.Error()}
					continue
				}
				metadata = append(metadata, submitted)
			}
			for _, job := range metadata {
				result := waitSlurmJob(runDir, job)
				if result.ExitCode != 0 {
					fmt.Printf("%s\n", red(fmt.Sprintf("fail job=%s exit=%d command=%s", result.ID, result.ExitCode, strings.Join(result.Command, " "))))
				}
				results <- result
			}
		}
	}()
	workers.Wait()
	close(results)
	collected := make([]JobResult, 0, len(jobs))
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func summarizeResults(results map[string]JobResult) (int, int, int) {
	completed, succeeded, failed := 0, 0, 0
	for _, result := range results {
		completed++
		if result.ExitCode == 0 {
			succeeded++
		} else {
			failed++
		}
	}
	return completed, succeeded, failed
}
