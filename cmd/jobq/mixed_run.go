package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

func executeMixedRun(paths pathSet, runID string, localConcurrency, slurmMaxActive int, requestedBackend string, sbatchOptions []string) int {
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
	workers.Add(1)
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

	workers.Add(1)
	go func() {
		defer workers.Done()
		for start := 0; start < len(slurmJobs); start += slurmMaxActive {
			end := start + slurmMaxActive
			if end > len(slurmJobs) {
				end = len(slurmJobs)
			}
			batch := slurmJobs[start:end]
			metadata := make([]slurmJobMetadata, 0, len(batch))
			for _, job := range batch {
				options := job.SbatchOptions
				if len(options) == 0 {
					options = sbatchOptions
				}
				if len(options) == 0 {
					options = queue.DefaultSbatchOptions
				}
				submitted, submitErr := submitSlurmJob(runDir, job, options)
				if submitErr != nil {
					results <- JobResult{ID: job.ID, ExitCode: 1, Error: submitErr.Error()}
					continue
				}
				metadata = append(metadata, submitted)
			}
			for _, job := range metadata {
				results <- waitSlurmJob(runDir, job)
			}
		}
	}()

	workers.Wait()
	close(results)
	summary := RunSummary{RunID: runID, StartedAt: nowRFC3339(), FinishedAt: nowRFC3339(), Results: make([]JobResult, 0, len(jobs))}
	for result := range results {
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
