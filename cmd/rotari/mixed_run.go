package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func executeMixedRun(paths pathSet, runID, runName string, localConcurrency, batchMaxActive, retry int, requestedExecutor string, executorOptions []string, selection string, jobIDs []string, referenceRunID string, partialArray bool, progress func(JobResult, int, int, int, int), onStart func(JobSpec)) int {
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		printErrorf("failed to load queue: %v", err)
		return 1
	}
	jobs := queueToJobs(queue.Commands)
	if len(jobs) == 0 {
		printErrorf("queue '%s' has no valid commands", paths.queueName)
		return 1
	}
	if err := validateDependencies(jobs); err != nil {
		printErrorf("invalid dependencies: %v", err)
		return 1
	}
	defaultExecutor := requestedExecutor
	if defaultExecutor == "" {
		defaultExecutor = queue.DefaultExecutor
	}
	if defaultExecutor == "" {
		defaultExecutor = "local"
	}
	for index := range jobs {
		if jobs[index].Executor == "" {
			jobs[index].Executor = defaultExecutor
		}
	}
	prepareJobEnvironments(paths, runID, jobs, runName, localConcurrency, batchMaxActive, retry, executorOptions)

	plan, err := planRerunSelection(paths, queue, selection, jobIDs, referenceRunID, partialArray)
	if err != nil {
		printErrorf("failed to prepare job selection: %v", err)
		return 1
	}
	expandArrayPlan(queue.Commands, jobs, plan.Execute)
	for index := range queue.Commands {
		command := &queue.Commands[index]
		if origin, ok := plan.CarriedOrigins[command.ID]; ok {
			command.Origin = origin
		}
		if command.Array == nil {
			continue
		}
		for _, task := range arrayTaskIDs(command.Array) {
			taskID := fmt.Sprintf("%s-%d", command.ID, task)
			if origin, ok := plan.CarriedOrigins[taskID]; ok {
				if command.TaskOrigins == nil {
					command.TaskOrigins = make(map[string]*JobOrigin)
				}
				command.TaskOrigins[taskID] = origin
			}
		}
	}

	runDir := filepath.Join(paths.runsDir, runID)
	if err := os.MkdirAll(runDir, stateDirMode()); err != nil {
		return 1
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		return 1
	}

	finalResults := make(map[string]JobResult, len(jobs))
	for id, result := range plan.CarriedResults {
		finalResults[id] = result
	}
	executable := make([]JobSpec, 0, len(jobs))
	for _, job := range jobs {
		if plan.Execute[job.ID] {
			executable = append(executable, job)
		}
	}
	pending := append([]JobSpec(nil), executable...)
	jobsByName := make(map[string]JobSpec, len(jobs))
	for _, job := range jobs {
		if job.Name != "" {
			jobsByName[job.Name] = job
		}
	}
	for attempt := 0; (retry == -1 || attempt <= retry) && len(pending) > 0; attempt++ {
		pendingByID := make(map[string]bool, len(pending))
		for _, job := range pending {
			pendingByID[job.ID] = true
		}
		// Resolve dependency waves within this attempt: a job whose
		// dependency finishes in an earlier wave of the same attempt must
		// run in this attempt too, instead of waiting for the next retry.
		unresolved := pending
		var attemptResults []JobResult
		for {
			blocked := make([]JobSpec, 0)
			ready := make([]JobSpec, 0, len(unresolved))
			stillUnresolved := make([]JobSpec, 0, len(unresolved))
			for _, job := range unresolved {
				blockedBy := ""
				readyForRun := true
				for _, dependency := range job.DependsOn {
					dependencyJob := jobsByName[dependency]
					result, done := finalResults[dependencyJob.ID]
					if !done || (result.ExitCode != 0 && pendingByID[dependencyJob.ID]) {
						readyForRun = false
						continue
					}
					if result.ExitCode != 0 {
						blockedBy = dependency
						break
					}
				}
				if blockedBy != "" {
					blocked = append(blocked, job)
				} else if readyForRun {
					ready = append(ready, job)
				} else {
					stillUnresolved = append(stillUnresolved, job)
				}
			}
			for _, job := range blocked {
				finalResults[job.ID] = JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: "blocked by failed dependency"}
			}
			if len(ready) == 0 {
				break
			}
			waveResults := executeMixedAttempt(runDir, queue, ready, localConcurrency, batchMaxActive, requestedExecutor, executorOptions, onStart)
			for _, result := range waveResults {
				finalResults[result.ID] = result
			}
			attemptResults = append(attemptResults, waveResults...)
			unresolved = stillUnresolved
		}
		nextPending := make([]JobSpec, 0, len(jobs))
		for _, job := range pending {
			result, ok := finalResults[job.ID]
			if !ok || (result.ExitCode != 0 && attempt < retry) {
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
				progressResult := result
				if result.ExitCode != 0 && !jobIsPending(pending, result.ID) {
					progressResult.Error = "final-failure"
				}
				progress(progressResult, completed, len(jobs), succeeded, failed)
			}
		}
	}
	for _, job := range pending {
		if _, ok := finalResults[job.ID]; !ok {
			finalResults[job.ID] = JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: "blocked by failed dependency"}
		}
	}

	summary := RunSummary{RunID: runID, RunName: runName, Status: "finished", StartedAt: nowRFC3339(), FinishedAt: nowRFC3339(), Results: make([]JobResult, 0, len(jobs))}
	for _, job := range jobs {
		result, ok := finalResults[job.ID]
		if !ok {
			// Neither executed nor carried forward: leave it unfinished.
			continue
		}
		summary.Results = append(summary.Results, result)
		if result.ExitCode != 0 {
			summary.ExitCode = 1
		}
	}
	summary.Status = runStatus(summary.ExitCode)
	if err := writeJSON(filepath.Join(runDir, "summary.json"), summary); err != nil {
		return 1
	}
	return summary.ExitCode
}

func expandArrayPlan(commands []QueuedCommand, jobs []JobSpec, execute map[string]bool) {
	for _, command := range commands {
		if command.Array == nil || !execute[command.ID] {
			continue
		}
		delete(execute, command.ID)
		for _, job := range jobs {
			if job.ArrayGroup == command.ID {
				execute[job.ID] = true
			}
		}
	}
}

func prepareJobEnvironments(paths pathSet, runID string, jobs []JobSpec, runName string, localConcurrency, batchConcurrency, retry int, executorOptions []string) {
	runDir := filepath.Join(paths.runsDir, runID)
	cwd := ""
	if data, err := os.ReadFile(filepath.Join(runDir, "context.json")); err == nil {
		var context RunContext
		if json.Unmarshal(data, &context) == nil {
			cwd = context.CWD
		}
	}
	bin, _ := os.Executable()
	for index := range jobs {
		job := &jobs[index]
		jobDir := filepath.Join(runDir, job.ID)
		environment := []string{
			envBaseDir + "=" + paths.baseDir,
			envProjectName + "=" + paths.queueName,
			envRunID + "=" + runID,
			envJobID + "=" + job.ID,
			envExecutor + "=" + job.Executor,
			envBin + "=" + bin,
			envRunDir + "=" + runDir,
			envJobDir + "=" + jobDir,
			envCWD + "=" + cwd,
		}
		if job.Name != "" {
			environment = append(environment, envJobName+"="+job.Name)
		}
		if job.ArrayTaskID != nil {
			environment = append(environment,
				fmt.Sprintf("%s=%d", envArrayTaskID, *job.ArrayTaskID),
				fmt.Sprintf("%s=%d", envArrayFirst, job.ArrayFirst),
				fmt.Sprintf("%s=%d", envArrayLast, job.ArrayLast),
				fmt.Sprintf("%s=%d", envArraySize, job.ArraySize),
			)
		}
		if runName != "" {
			environment = append(environment, envRunName+"="+runName)
		}
		environment = append(environment,
			fmt.Sprintf("%s=%d", envRunLocalConc, localConcurrency),
			fmt.Sprintf("%s=%d", envRunBatchConc, batchConcurrency),
			fmt.Sprintf("%s=%d", envRunRetry, retry),
		)
		if len(executorOptions) > 0 {
			environment = append(environment, envExecutorOpts+"="+strings.Join(executorOptions, " "))
		}
		for _, name := range propagatedEnvironmentVariables {
			if _, exists := environmentEntry(environment, name); exists {
				continue
			}
			if value, exists := os.LookupEnv(name); exists {
				environment = append(environment, name+"="+value)
			}
		}
		job.Environment = mergeEnvironment(job.Environment, environment)
	}
}

func environmentEntry(environment []string, name string) (string, bool) {
	for _, entry := range environment {
		if strings.HasPrefix(entry, name+"=") {
			return entry, true
		}
	}
	return "", false
}

func jobIsPending(jobs []JobSpec, jobID string) bool {
	for _, job := range jobs {
		if job.ID == jobID {
			return true
		}
	}
	return false
}

func removeFinishedJobs(jobs []JobSpec, results map[string]JobResult) []JobSpec {
	remaining := make([]JobSpec, 0, len(jobs))
	for _, job := range jobs {
		if _, done := results[job.ID]; !done {
			remaining = append(remaining, job)
		}
	}
	return remaining
}

func executeMixedAttempt(runDir string, queue Queue, jobs []JobSpec, localConcurrency, batchMaxActive int, requestedExecutor string, executorOptions []string, onStart func(JobSpec)) []JobResult {
	defaultExecutor := requestedExecutor
	if defaultExecutor == "" {
		defaultExecutor = queue.DefaultExecutor
	}
	if defaultExecutor == "" {
		defaultExecutor = "local"
	}
	grouped := make(map[string][]JobSpec)
	for _, job := range jobs {
		jobExecutor := job.Executor
		if jobExecutor == "" {
			jobExecutor = defaultExecutor
		}
		grouped[jobExecutor] = append(grouped[jobExecutor], job)
	}

	results := make(chan JobResult, len(jobs))
	var workers sync.WaitGroup
	for executorName, executorJobs := range grouped {
		executor, ok := lookupExecutor(executorName)
		if !ok {
			for _, job := range executorJobs {
				results <- JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: fmt.Sprintf("unsupported executor: %s", executorName)}
			}
			continue
		}
		workers.Add(1)
		if executorName == "local" {
			go runLocalLane(&workers, runDir, executor, executorJobs, localConcurrency, results, onStart)
		} else {
			go runBatchLane(&workers, runDir, queue, executor, executorJobs, batchMaxActive, executorOptions, results, onStart)
		}
	}
	workers.Wait()
	close(results)
	collected := make([]JobResult, 0, len(jobs))
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

// runLocalLane runs jobs concurrently up to concurrency, used for the local
// executor where jobs are cheap OS subprocesses rather than scheduler batches.
func runLocalLane(workers *sync.WaitGroup, runDir string, executor JobExecutor, jobs []JobSpec, concurrency int, results chan<- JobResult, onStart func(JobSpec)) {
	defer workers.Done()
	sem := make(chan struct{}, concurrency)
	var jobsWait sync.WaitGroup
	for _, job := range jobs {
		jobsWait.Add(1)
		go func(job JobSpec) {
			defer jobsWait.Done()
			sem <- struct{}{}
			handle, err := executor.Submit(runDir, job, nil)
			if err != nil {
				results <- JobResult{ID: job.ID, Command: job.Command, ExitCode: 1, Error: err.Error()}
			} else {
				if onStart != nil {
					onStart(job)
				}
				results <- executor.Wait(runDir, handle)
			}
			<-sem
		}(job)
	}
	jobsWait.Wait()
}

// runBatchLane submits jobs to a scheduler-style executor (Slurm, PBS, ...) in
// waves of at most maxActive concurrently-tracked jobs.
func runBatchLane(workers *sync.WaitGroup, runDir string, queue Queue, executor JobExecutor, jobs []JobSpec, maxActive int, executorOptions []string, results chan<- JobResult, onStart func(JobSpec)) {
	defer workers.Done()
	for start := 0; start < len(jobs); {
		if jobs[start].ArrayGroup != "" {
			end := start + 1
			for end < len(jobs) && jobs[end].ArrayGroup == jobs[start].ArrayGroup {
				end++
			}
			if submitter, ok := executor.(ArraySubmitter); ok && completeArrayGroup(jobs[start:end], jobs[start].ArrayFirst, jobs[start].ArrayLast) {
				options := jobs[start].ExecutorOptions
				if len(options) == 0 {
					options = executorOptions
				}
				if len(options) == 0 {
					options = queue.DefaultExecutorOptions
				}
				handles, err := submitter.SubmitArray(runDir, jobs[start:end], options)
				if err != nil {
					for _, job := range jobs[start:end] {
						results <- JobResult{ID: job.ID, ExitCode: 1, Error: err.Error()}
					}
				} else {
					if onStart != nil {
						for _, job := range jobs[start:end] {
							onStart(job)
						}
					}
					for _, handle := range handles {
						results <- executor.Wait(runDir, handle)
					}
				}
				start = end
				continue
			}
		}
		end := start + maxActive
		if end > len(jobs) {
			end = len(jobs)
		}
		handles := make([]JobHandle, 0, end-start)
		for _, job := range jobs[start:end] {
			jobDir := filepath.Join(runDir, job.ID)
			if jobCancellationRequested(jobDir) {
				results <- recordCancelledJob(jobDir, job)
				continue
			}
			options := job.ExecutorOptions
			if len(options) == 0 {
				options = executorOptions
			}
			if len(options) == 0 {
				options = queue.DefaultExecutorOptions
			}
			handle, err := executor.Submit(runDir, job, options)
			if err != nil {
				results <- JobResult{ID: job.ID, ExitCode: 1, Error: err.Error()}
				continue
			}
			if onStart != nil {
				onStart(job)
			}
			handles = append(handles, handle)
		}
		for _, handle := range handles {
			result := executor.Wait(runDir, handle)
			if result.ExitCode != 0 {
				fmt.Printf("%s\n", colorKeyValueMessage(fmt.Sprintf("fail job=%s exit=%d command=%s", result.ID, result.ExitCode, strings.Join(result.Command, " ")), red))
			}
			results <- result
		}
		start = end
	}
}

func completeArrayGroup(jobs []JobSpec, first, last int) bool {
	if len(jobs) != last-first+1 {
		return false
	}
	seen := make(map[int]bool, len(jobs))
	for _, job := range jobs {
		if job.ArrayTaskID == nil || *job.ArrayTaskID < first || *job.ArrayTaskID > last || seen[*job.ArrayTaskID] {
			return false
		}
		seen[*job.ArrayTaskID] = true
	}
	return len(seen) == len(jobs)
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
