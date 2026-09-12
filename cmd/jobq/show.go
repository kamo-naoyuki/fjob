package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := fs.String("basedir", "", "state directory")
	queueNameOption := fs.String("queue-name", "", "queue name")
	runIDOption := fs.String("run-id", "", "run ID")
	jobIDOption := fs.String("job-id", "", "job ID")
	failedOnly := fs.Bool("failed", false, "show failed jobs only")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: jobq show [--basedir DIR] [--queue-name NAME] [--run-id ID] [--job-id ID] [--failed]")
		return 1
	}

	queueName := resolveQueueName(*queueNameOption)
	paths, err := resolvePaths(*basedir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
		return 1
	}
	runID, err := selectRunID(paths, *runIDOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *jobIDOption != "" {
		return showJob(paths, runID, *jobIDOption)
	}
	return showRun(paths, runID, *failedOnly)
}

func selectRunID(paths pathSet, requested string) (string, error) {
	if requested != "" {
		if _, err := os.Stat(filepath.Join(paths.runsDir, requested)); err != nil {
			return "", fmt.Errorf("run %q not found", requested)
		}
		return requested, nil
	}
	meta, err := loadMeta(paths.metaFile)
	if err != nil {
		return "", fmt.Errorf("failed to load metadata: %w", err)
	}
	if meta.LastRunID != "" {
		if info, err := os.Stat(filepath.Join(paths.runsDir, meta.LastRunID)); err == nil && info.IsDir() {
			return meta.LastRunID, nil
		}
	}
	{
		entries, err := os.ReadDir(paths.runsDir)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("queue %q has no runs (runs_dir=%s)", paths.queueName, paths.runsDir)
			}
			return "", fmt.Errorf("failed to read runs: %w", err)
		}
		runIDs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				runIDs = append(runIDs, entry.Name())
			}
		}
		if len(runIDs) == 0 {
			return "", fmt.Errorf("queue %q has no runs (runs_dir=%s)", paths.queueName, paths.runsDir)
		}
		sort.Slice(runIDs, func(i, j int) bool {
			left, _ := os.Stat(filepath.Join(paths.runsDir, runIDs[i]))
			right, _ := os.Stat(filepath.Join(paths.runsDir, runIDs[j]))
			return left.ModTime().After(right.ModTime())
		})
		return runIDs[0], nil
	}
}

func showRun(paths pathSet, runID string, failedOnly bool) int {
	runDir := filepath.Join(paths.runsDir, runID)
	var summary RunSummary
	summaryData, err := os.ReadFile(filepath.Join(runDir, "summary.json"))
	summaryOK := err == nil
	if err == nil {
		if err := json.Unmarshal(summaryData, &summary); err != nil {
			fmt.Fprintf(os.Stderr, "failed to read summary: %v\n", err)
			return 1
		}
	}

	fmt.Printf("%s\n%s\n%s\n", cyan("Queue: "+paths.queueName), cyan("Run: "+runID), cyan("Directory: "+runDir))
	if summaryOK {
		fmt.Printf("Started: %s\nFinished: %s\nExit code: %d\n", summary.StartedAt, summary.FinishedAt, summary.ExitCode)
	}
	jobSpecs := loadRunJobSpecs(runDir)

	entries, err := os.ReadDir(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read run directory: %v\n", err)
		return 1
	}
	fmt.Println("\n" + cyan("Jobs:"))
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-12s %-10s %-30s %-24s %-24s %s", "JOB ID", "STATUS", "BACKEND", "SUBMITTED", "FINISHED", "COMMAND")))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		jobID := entry.Name()
		status, statusOK := readJobStatus(filepath.Join(runDir, jobID, "status"))
		if !statusOK {
			if slurm, ok := loadSlurmStatus(filepath.Join(runDir, jobID, "status.json")); ok && slurmStatusTerminal(slurm.Phase) {
				status = slurm.ExitCode
				statusOK = true
			}
		}
		backend, options := jobSpecs[jobID].Backend, jobSpecs[jobID].SbatchOptions
		if backend == "" {
			backend = "local"
		}
		backendText := colorBackend(backend)
		if len(options) > 0 {
			backendText += " (" + strings.Join(options, " ") + ")"
		}
		if failedOnly && (!statusOK || status == 0) {
			continue
		}
		command := readCommand(filepath.Join(runDir, jobID, "command"))
		if command == "" {
			command = readJSONCommand(filepath.Join(runDir, jobID, "command.json"))
		}
		submittedAt := readSubmittedAt(runDir, jobID)
		finishedAt := readFinishedAt(runDir, jobID)
		if statusOK {
			statusText := green(strconv.Itoa(status))
			if status != 0 {
				statusText = red(strconv.Itoa(status))
			}
			fmt.Printf("%-12s %-10s %-30s %-24s %-24s %s\n", jobID, statusText, backendText, submittedAt, finishedAt, command)
		} else {
			fmt.Printf("%-12s %-10s %-30s %-24s %-24s %s\n", jobID, yellow("running"), backendText, submittedAt, finishedAt, command)
		}
	}
	fmt.Printf("\n%s\n", cyan("Output directory: "+runDir))
	fmt.Printf("\nTo clear all saved run logs:\n  jobq clear --basedir %s --queue-name %s\n", paths.baseDir, paths.queueName)
	return 0
}

func colorBackend(backend string) string {
	switch backend {
	case "local":
		return green(backend)
	case "slurm":
		return cyan(backend)
	default:
		return yellow(backend)
	}
}

func slurmStatusTerminal(phase string) bool {
	switch phase {
	case "finished", "completed", "failed", "cancelled", "timeout", "out_of_memory", "unknown":
		return true
	default:
		return false
	}
}

func readSubmittedAt(runDir, jobID string) string {
	data, err := os.ReadFile(filepath.Join(runDir, jobID, "submitted_at"))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	data, err = os.ReadFile(filepath.Join(runDir, jobID, "job.json"))
	if err != nil {
		return "-"
	}
	var metadata slurmJobMetadata
	if json.Unmarshal(data, &metadata) != nil || metadata.SubmittedAt == "" {
		return "-"
	}
	return metadata.SubmittedAt
}

func readFinishedAt(runDir, jobID string) string {
	data, err := os.ReadFile(filepath.Join(runDir, jobID, "finished_at"))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	data, err = os.ReadFile(filepath.Join(runDir, jobID, "status.json"))
	if err != nil {
		return "-"
	}
	var status slurmStatus
	if json.Unmarshal(data, &status) != nil || status.FinishedAt == "" {
		return "-"
	}
	return status.FinishedAt
}

func loadRunJobSpecs(runDir string) map[string]JobSpec {
	specs := make(map[string]JobSpec)
	data, err := os.ReadFile(filepath.Join(runDir, "commands.json"))
	if err == nil {
		var queue Queue
		if json.Unmarshal(data, &queue) == nil {
			for _, job := range queueToJobs(queue.Commands) {
				specs[job.ID] = job
			}
		}
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return specs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runDir, entry.Name(), "command.json"))
		if err != nil {
			continue
		}
		var job JobSpec
		if json.Unmarshal(data, &job) == nil {
			specs[entry.Name()] = job
		}
	}
	return specs
}

func showJob(paths pathSet, runID, jobID string) int {
	jobDir := filepath.Join(paths.runsDir, runID, jobID)
	runDir := filepath.Dir(jobDir)
	if info, err := os.Stat(jobDir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "job %q not found in run %q\n", jobID, runID)
		return 1
	}
	fmt.Printf("%s\n%s\n%s\n", cyan("Queue: "+paths.queueName), cyan("Run: "+runID), cyan("Job: "+jobID))
	fmt.Printf("Submitted: %s\n", readSubmittedAt(runDir, jobID))
	fmt.Printf("Finished: %s\n", readFinishedAt(runDir, jobID))
	if status, ok := readJobStatus(filepath.Join(jobDir, "status")); ok {
		if status == 0 {
			fmt.Printf("%s\n", green(fmt.Sprintf("Status: %d", status)))
		} else {
			fmt.Printf("%s\n", red(fmt.Sprintf("Status: %d", status)))
		}
	} else if slurm, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json")); ok {
		status := fmt.Sprintf("Status: %s (exit code %d)", slurm.Phase, slurm.ExitCode)
		if slurm.Phase == "finished" && slurm.ExitCode == 0 {
			fmt.Printf("%s\n", green(status))
		} else if slurm.Phase == "finished" {
			fmt.Printf("%s\n", red(status))
		} else {
			fmt.Printf("%s\n", yellow(status))
		}
	}
	fmt.Printf("Command: %s\n", readCommand(filepath.Join(jobDir, "command")))
	fmt.Printf("Output: %s\n\n", filepath.Join(jobDir, "output"))
	output, err := os.ReadFile(filepath.Join(jobDir, "output"))
	if err == nil {
		fmt.Print(string(output))
	}
	return 0
}

func readJobStatus(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return value, err == nil
}

func readCommand(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readJSONCommand(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var job JobSpec
	if json.Unmarshal(data, &job) != nil {
		return ""
	}
	return strings.Join(job.Command, " ")
}
