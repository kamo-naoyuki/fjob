package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const pagerLineLimit = 24

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "project-name", "")
	runIDOption := cliString(fs, "run-id", "")
	jobIDOption := cliString(fs, "job-id", "")
	failedOnly := cliBool(fs, "failed", false)
	showRunsList := cliBool(fs, "runs", false)
	showLogs := cliBool(fs, "logs", false)
	showFailedLogs := cliBool(fs, "failed-logs", false)
	followLogs := cliBool(fs, "follow", false)
	noPager := cliBool(fs, "no-pager", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("show"))
		return 1
	}
	baseDir, queueName, err := resolveExistingRunTarget(*basedir, *queueNameOption, *runIDOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	paths, err := resolvePaths(baseDir, queueName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve paths: %v\n", err)
		return 1
	}
	if *showRunsList {
		return showRuns(paths)
	}
	selectedRunID := *runIDOption
	if selectedRunID == "" {
		state, stateRunID, err := inspectProjectRunState(paths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to check project state: %v\n", err)
			return 1
		}
		if state == projectRunning {
			selectedRunID = stateRunID
		} else if state == projectInterrupted {
			selectedRunID = stateRunID
			printInterruptedRunNotice(paths, stateRunID)
		} else {
			queue, err := loadQueue(paths.queueFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to load queue: %v\n", err)
				return 1
			}
			if len(queue.Commands) > 0 {
				if *showLogs || *showFailedLogs || *failedOnly {
					fmt.Fprintln(os.Stderr, "logs and failed filters require --run-id")
					return 1
				}
				if *jobIDOption != "" {
					return showQueueJob(paths, queue, *jobIDOption)
				}
				return showQueue(paths, queue)
			}
		}
	}
	runID, err := selectRunID(paths, selectedRunID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *jobIDOption != "" {
		running, err := isRunning(paths.lockFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to check queue state: %v\n", err)
			return 1
		}
		if shouldFollowLogs(*followLogs, running, isTerminal(os.Stdout)) {
			return followJobLog(os.Stdout, paths, runID, *jobIDOption)
		}
		return showWithPager(!*noPager, func(writer io.Writer) int {
			return showJob(writer, paths, runID, *jobIDOption)
		})
	}
	if *followLogs {
		fmt.Fprintln(os.Stderr, "--follow requires --job-id")
		return 1
	}
	if *showLogs || *showFailedLogs {
		return showWithPager(!*noPager, func(writer io.Writer) int {
			return showRunLogs(writer, paths, runID, *showFailedLogs)
		})
	}
	return showRun(paths, runID, *failedOnly)
}

func shouldFollowLogs(explicit bool, queueRunning bool, isTTY bool) bool {
	if explicit {
		return true
	}
	return queueRunning && isTTY
}

func showWithPager(usePager bool, show func(io.Writer) int) int {
	if !usePager || !isTerminal(os.Stdout) {
		return show(os.Stdout)
	}

	writer := &pagerWriter{output: os.Stdout}
	exitCode := show(writer)
	if err := writer.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exitCode == 0 {
			return 1
		}
	}
	return exitCode
}

type pagerWriter struct {
	output   io.Writer
	buffer   bytes.Buffer
	newlines int
	input    io.WriteCloser
	command  *exec.Cmd
}

func (writer *pagerWriter) Write(data []byte) (int, error) {
	if writer.input != nil {
		return writer.input.Write(data)
	}
	if writer.command != nil {
		return writer.output.Write(data)
	}

	for offset, value := range data {
		if value == '\n' {
			writer.newlines++
		}
		if writer.newlines > pagerLineLimit {
			writer.buffer.Write(data[:offset+1])
			if err := writer.startPager(); err != nil {
				return 0, err
			}
			if offset+1 == len(data) {
				return len(data), nil
			}
			written, err := writer.Write(data[offset+1:])
			return offset + 1 + written, err
		}
	}
	writer.buffer.Write(data)
	return len(data), nil
}

func (writer *pagerWriter) Close() error {
	if writer.input == nil && writer.command == nil && exceedsPagerLineLimit(writer.buffer.Bytes(), writer.newlines) {
		if err := writer.startPager(); err != nil {
			if _, writeErr := writer.output.Write(writer.buffer.Bytes()); writeErr != nil {
				return fmt.Errorf("%v; failed to write output directly: %w", err, writeErr)
			}
			writer.buffer.Reset()
			return nil
		}
	}
	if writer.input == nil {
		_, err := writer.output.Write(writer.buffer.Bytes())
		return err
	}
	if err := writer.input.Close(); err != nil {
		return err
	}
	if err := writer.command.Wait(); err != nil {
		return fmt.Errorf("pager %q failed: %w", writer.command.Args[0], err)
	}
	return nil
}

func exceedsPagerLineLimit(data []byte, newlines int) bool {
	return newlines > pagerLineLimit || newlines == pagerLineLimit && len(data) > 0 && data[len(data)-1] != '\n'
}

func (writer *pagerWriter) startPager() error {
	args := strings.Fields(os.Getenv("PAGER"))
	if len(args) == 0 {
		args = []string{"less", "-R"}
	}
	command := exec.Command(args[0], args[1:]...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to start pager: %w", err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("failed to start pager %q: %w", args[0], err)
	}
	writer.input = input
	writer.command = command
	if _, err := writer.input.Write(writer.buffer.Bytes()); err != nil {
		return err
	}
	writer.buffer.Reset()
	return nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
				return "", fmt.Errorf("project %q has no runs (runs_dir=%s)", paths.queueName, paths.runsDir)
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
			return "", fmt.Errorf("project %q has no runs (runs_dir=%s)", paths.queueName, paths.runsDir)
		}
		sort.Slice(runIDs, func(i, j int) bool {
			left, _ := os.Stat(filepath.Join(paths.runsDir, runIDs[i]))
			right, _ := os.Stat(filepath.Join(paths.runsDir, runIDs[j]))
			return left.ModTime().After(right.ModTime())
		})
		return runIDs[0], nil
	}
}

func writeShowTargetHeader(writer io.Writer, paths pathSet) {
	fmt.Fprintf(writer, "%s %s\n%s %s\n", cyan("Base directory:"), paths.baseDir, cyan("Project:"), paths.queueName)
}

func printInterruptedRunNotice(paths pathSet, runID string) {
	fmt.Printf("%s\n", yellow(fmt.Sprintf("Run %s appears to have been interrupted.", runID)))
	fmt.Printf("Recover the queue before modifying or running it:\n  rotari unlock --basedir %s --project-name %s --run-id %s\n\n",
		shellQuote(paths.baseDir), shellQuote(paths.queueName), shellQuote(runID))
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

	writeShowTargetHeader(os.Stdout, paths)
	fmt.Printf("%s %s\n", cyan("Run:"), formatRunLabel(runID, summary.RunName))
	if summary.RunName != "" {
		fmt.Printf("%s %s\n", cyan("Run name:"), summary.RunName)
	}
	fmt.Printf("%s %s\n", cyan("Directory:"), runDir)
	if summaryOK {
		fmt.Printf("%s %s\n%s %s\n%s %s\n%s %d\n", cyan("Status:"), summary.Status, cyan("Started:"), summary.StartedAt, cyan("Finished:"), summary.FinishedAt, cyan("Exit code:"), summary.ExitCode)
	}
	fmt.Printf("%s %s\n", cyan("Output directory:"), runDir)
	queue, queueErr := loadQueue(paths.queueFile)
	if queueErr == nil && len(queue.Commands) > 0 {
		if diff, err := compareQueueWithRun(paths.queueFile, filepath.Join(runDir, "commands.json")); err == nil && diff.HasChanges() {
			fmt.Printf("\n%s\n", yellow("Queue differs from this run:"))
			fmt.Printf("  Added: %d\n  Removed: %d\n  Changed: %d\n", diff.Added, diff.Removed, diff.Changed)
		}
	}
	jobSpecs := loadRunJobSpecs(runDir)
	runQueue, runQueueErr := loadQueue(filepath.Join(runDir, "commands.json"))
	resultByID := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		resultByID[result.ID] = result
	}
	jobIDs := make([]string, 0, len(runQueue.Commands))
	originByID := make(map[string]*JobOrigin, len(runQueue.Commands))
	if runQueueErr == nil {
		for _, command := range runQueue.Commands {
			jobIDs = append(jobIDs, command.ID)
			originByID[command.ID] = command.Origin
		}
	} else {
		entries, err := os.ReadDir(runDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to read run directory: %v\n", err)
			return 1
		}
		for _, entry := range entries {
			if entry.IsDir() {
				jobIDs = append(jobIDs, entry.Name())
			}
		}
	}
	fmt.Println("\n" + cyan("Jobs:"))
	changeHints := make([]JobSpec, 0)
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %-24s %s", "JOB ID", "NAME", "DEPENDS ON", "STATUS", "EXECUTOR", "SUBMITTED", "FINISHED", "HOSTS", "COMMAND")))
	for _, jobID := range jobIDs {
		jobSpec := jobSpecs[jobID]
		name := readJobName(filepath.Join(runDir, jobID))
		if name == "" {
			name = jobSpec.Name
		}
		if name == "" {
			name = "-"
		}
		dependsOn := strings.Join(jobSpec.DependsOn, ",")
		if dependsOn == "" {
			dependsOn = "-"
		}
		status, statusOK := readJobStatus(filepath.Join(runDir, jobID, "status"))
		blocked := false
		if !statusOK {
			if slurm, ok := loadSlurmStatus(filepath.Join(runDir, jobID, "status.json")); ok && slurmStatusTerminal(slurm.Phase) {
				status = slurm.ExitCode
				statusOK = true
			}
		}
		if !statusOK {
			if result, ok := resultByID[jobSpec.ID]; ok {
				status = result.ExitCode
				statusOK = true
				blocked = strings.HasPrefix(result.Error, "blocked")
			}
		}
		executorText := queueExecutorText(runQueue, jobSpec)
		if failedOnly && (!statusOK || status == 0) {
			continue
		}
		if statusOK && status != 0 {
			changeHints = append(changeHints, jobSpec)
		}
		hosts := "-"
		if result, ok := resultByID[jobSpec.ID]; ok && len(result.Hosts) > 0 {
			hosts = strings.Join(result.Hosts, ",")
		} else if slurm, ok := loadSlurmStatus(filepath.Join(runDir, jobID, "status.json")); ok && len(slurm.Hosts) > 0 {
			hosts = strings.Join(slurm.Hosts, ",")
		}
		command := readJSONCommand(filepath.Join(runDir, jobID, "command.json"))
		if command == "" {
			command = strings.Join(jobSpec.Command, " ")
		}
		submittedAt, finishedAt := readShowJobTimestamps(runDir, jobID, originByID[jobID])
		if statusOK {
			statusText := green(strconv.Itoa(status))
			if blocked {
				statusText = yellow("blocked")
			} else if status != 0 {
				statusText = red(strconv.Itoa(status))
			}
			fmt.Printf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %-24s %s\n", jobID, name, dependsOn, statusText, executorText, submittedAt, finishedAt, hosts, command)
		} else {
			fmt.Printf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %-24s %s\n", jobID, name, dependsOn, yellow("running"), executorText, submittedAt, finishedAt, hosts, command)
		}
	}
	printChangeHints(paths, runID, runQueue, changeHints)
	fmt.Printf("\n%s\n  rotari delete --run-id %s\n", cyan("To delete this run's saved logs:"), runID)
	return 0
}

func printChangeHints(paths pathSet, runID string, queue Queue, jobs []JobSpec) {
	if len(jobs) == 0 {
		return
	}
	hasSlurm := false
	hasDependencies := false
	selector := "--job-id JOB_ID"
	if len(jobs) == 1 && jobs[0].Name != "" {
		selector = "--job-name " + jobs[0].Name
	}
	for _, job := range jobs {
		executor := job.Executor
		if executor == "" {
			executor = queue.DefaultExecutor
		}
		if executor == "slurm" {
			hasSlurm = true
		}
		if len(job.DependsOn) > 0 {
			hasDependencies = true
		}
	}
	fmt.Println("\n" + cyan("Change:"))
	fmt.Println("  " + cyan("e.g., Replace the command:"))
	fmt.Printf("    rotari change --run-id %s %s -- <new-command ...>\n", runID, selector)
	if hasSlurm {
		fmt.Println("  " + cyan("e.g., Replace the executor options:"))
		fmt.Printf("    rotari change --run-id %s %s --executor-option=\"<options>\"\n", runID, selector)
	}
	if hasDependencies {
		fmt.Println("  " + cyan("e.g., Replace the dependencies:"))
		fmt.Printf("    rotari change --run-id %s %s --depends-on <job-name>\n", runID, selector)
	}
	fmt.Println("\n" + cyan("Rerun:"))
	fmt.Printf("    rotari retry --basedir %s --project-name %s\n", paths.baseDir, paths.queueName)
}

func showQueue(paths pathSet, queue Queue) int {
	jobs := queueToJobs(queue.Commands)
	writeShowTargetHeader(os.Stdout, paths)
	fmt.Printf("%s\n\n", cyan("Showing jobs queued for the next run"))
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-12s %-15s %-20s %-30s %s", "JOB ID", "NAME", "DEPENDS ON", "EXECUTOR", "COMMAND")))
	for _, job := range jobs {
		name := job.Name
		if name == "" {
			name = "-"
		}
		dependsOn := strings.Join(job.DependsOn, ",")
		if dependsOn == "" {
			dependsOn = "-"
		}
		executorText := queueExecutorText(queue, job)
		fmt.Printf("%-12s %-15s %-20s %-30s %s\n", job.ID, name, dependsOn, executorText, strings.Join(job.Command, " "))
	}
	fmt.Printf("\n%s\n  rotari run --basedir %s --project-name %s\n", cyan("To execute these jobs:"), shellQuote(paths.baseDir), shellQuote(paths.queueName))
	return 0
}

func showQueueJob(paths pathSet, queue Queue, jobID string) int {
	for _, job := range queueToJobs(queue.Commands) {
		if job.ID != jobID {
			continue
		}
		writeShowTargetHeader(os.Stdout, paths)
		fmt.Printf("%s %s\n", cyan("Job:"), job.ID)
		if job.Name != "" {
			fmt.Printf("Name: %s\n", job.Name)
		}
		fmt.Printf("Executor: %s\n", queueExecutorText(queue, job))
		if len(job.DependsOn) > 0 {
			fmt.Printf("Depends on: %s\n", strings.Join(job.DependsOn, ", "))
		}
		fmt.Printf("Command: %s\n", strings.Join(job.Command, " "))
		return 0
	}
	fmt.Fprintf(os.Stderr, "job %q not found in current queue\n", jobID)
	return 1
}

func queueExecutorText(queue Queue, job JobSpec) string {
	executor := job.Executor
	options := job.ExecutorOptions
	if executor == "" {
		executor = queue.DefaultExecutor
	}
	if executor == "" {
		executor = "local"
	}
	if executor == "slurm" && len(options) == 0 {
		options = queue.DefaultExecutorOptions
	}
	executorText := colorExecutor(executor)
	if len(options) > 0 {
		executorText += " (" + strings.Join(options, " ") + ")"
	}
	return executorText
}

type queueRunDiff struct {
	Added   int
	Removed int
	Changed int
}

func (diff queueRunDiff) HasChanges() bool {
	return diff.Added != 0 || diff.Removed != 0 || diff.Changed != 0
}

func compareQueueWithRun(queuePath, runCommandsPath string) (queueRunDiff, error) {
	current, err := loadQueue(queuePath)
	if err != nil {
		return queueRunDiff{}, err
	}
	runData, err := os.ReadFile(runCommandsPath)
	if err != nil {
		return queueRunDiff{}, err
	}
	var runQueue Queue
	if err := json.Unmarshal(runData, &runQueue); err != nil {
		return queueRunDiff{}, err
	}

	currentJobs := queueToJobs(current.Commands)
	runJobs := queueToJobs(runQueue.Commands)
	currentByID := make(map[string]JobSpec, len(currentJobs))
	for _, job := range currentJobs {
		currentByID[job.ID] = job
	}
	runByID := make(map[string]JobSpec, len(runJobs))
	for _, job := range runJobs {
		runByID[job.ID] = job
	}
	var diff queueRunDiff
	for id, currentJob := range currentByID {
		runJob, ok := runByID[id]
		if !ok {
			diff.Added++
		} else if !sameJobSpec(currentJob, runJob) {
			diff.Changed++
		}
	}
	for id := range runByID {
		if _, ok := currentByID[id]; !ok {
			diff.Removed++
		}
	}
	return diff, nil
}

func sameJobSpec(left, right JobSpec) bool {
	if left.ID != right.ID || left.Name != right.Name || left.Executor != right.Executor {
		return false
	}
	if !slicesEqual(left.Command, right.Command) || !slicesEqual(left.ExecutorOptions, right.ExecutorOptions) || !slicesEqual(left.DependsOn, right.DependsOn) {
		return false
	}
	return true
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func showRuns(paths pathSet) int {
	entries, err := os.ReadDir(paths.runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeShowTargetHeader(os.Stdout, paths)
			fmt.Println("No runs found.")
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to read runs directory: %v\n", err)
		return 1
	}

	type runInfo struct {
		id         string
		name       string
		startedAt  string
		finishedAt string
		exitCode   int
		status     string
		hasSummary bool
		modTime    int64
	}

	var runs []runInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		runDir := filepath.Join(paths.runsDir, runID)
		info, err := entry.Info()
		modTime := int64(0)
		if err == nil {
			modTime = info.ModTime().UnixNano()
		}

		r := runInfo{id: runID, modTime: modTime}
		summaryData, err := os.ReadFile(filepath.Join(runDir, "summary.json"))
		if err == nil {
			var summary RunSummary
			if json.Unmarshal(summaryData, &summary) == nil {
				r.name = summary.RunName
				r.startedAt = summary.StartedAt
				r.finishedAt = summary.FinishedAt
				r.exitCode = summary.ExitCode
				r.status = summary.Status
				if r.status == "" {
					r.status = runStatus(summary.ExitCode)
				}
				r.hasSummary = true
			}
		}
		if !r.hasSummary {
			r.status = "running"
		}
		runs = append(runs, r)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].modTime > runs[j].modTime
	})

	writeShowTargetHeader(os.Stdout, paths)
	fmt.Printf("%s %s\n", cyan("Runs directory:"), paths.runsDir)
	fmt.Println("\n" + cyan("Runs:"))
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-36s %-24s %-12s %-12s %-24s %-24s", "RUN ID", "NAME", "STATUS", "EXIT CODE", "STARTED", "FINISHED")))
	for _, r := range runs {
		started := r.startedAt
		if started == "" {
			started = "-"
		}
		finished := r.finishedAt
		if finished == "" {
			finished = "-"
		}
		statusText := r.status
		if statusText == "finished" {
			statusText = green(statusText)
		} else if statusText == "failed" {
			statusText = red(statusText)
		} else {
			statusText = yellow(statusText)
		}
		exitCode := "-"
		if r.hasSummary {
			exitCode = strconv.Itoa(r.exitCode)
		}
		name := r.name
		if name == "" {
			name = "-"
		}
		fmt.Printf("%-36s %-24s %-12s %-12s %-24s %-24s\n", r.id, name, statusText, exitCode, started, finished)
	}
	return 0
}

func colorExecutor(executor string) string {
	switch executor {
	case "local":
		return green(executor)
	case "slurm":
		return cyan(executor)
	default:
		return yellow(executor)
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

func readShowJobTimestamps(runDir, jobID string, origin *JobOrigin) (string, string) {
	submittedAt := readSubmittedAt(runDir, jobID)
	finishedAt := readFinishedAt(runDir, jobID)
	if origin == nil {
		return submittedAt, finishedAt
	}
	if submittedAt == "-" {
		submittedAt = origin.SubmittedAt
		if submittedAt == "" {
			submittedAt = readSubmittedAt(filepath.Join(filepath.Dir(runDir), origin.RunID), origin.JobID)
		}
	}
	if finishedAt == "-" {
		finishedAt = origin.FinishedAt
		if finishedAt == "" {
			finishedAt = readFinishedAt(filepath.Join(filepath.Dir(runDir), origin.RunID), origin.JobID)
		}
	}
	return submittedAt, finishedAt
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

// loadRunOrigin returns the Origin recorded for jobID in runDir's
// commands.json, if any. Carried-forward jobs (see planRerunSelection) are
// not re-executed, so their output only exists under the origin run/job.
func loadRunOrigin(runDir, jobID string) *JobOrigin {
	queue, err := loadQueue(filepath.Join(runDir, "commands.json"))
	if err != nil {
		return nil
	}
	for _, command := range queue.Commands {
		if command.ID == jobID {
			return command.Origin
		}
	}
	return nil
}

func showJob(writer io.Writer, paths pathSet, runID, jobID string) int {
	jobDir := filepath.Join(paths.runsDir, runID, jobID)
	runDir := filepath.Dir(jobDir)
	if info, err := os.Stat(jobDir); err != nil || !info.IsDir() {
		if origin := loadRunOrigin(runDir, jobID); origin != nil {
			fmt.Fprintf(writer, "%s carried forward from run %s (no re-execution)\n\n", cyan("Note:"), origin.RunID)
			return showJob(writer, paths, origin.RunID, origin.JobID)
		}
		fmt.Fprintf(os.Stderr, "job %q not found in run %q\n", jobID, runID)
		return 1
	}
	writeShowTargetHeader(writer, paths)
	fmt.Fprintf(writer, "%s %s\n%s %s\n", cyan("Run:"), runID, cyan("Job:"), jobID)
	jobSpecs := loadRunJobSpecs(runDir)
	name := readJobName(jobDir)
	if name == "" {
		name = jobSpecs[jobID].Name
	}
	if name != "" {
		fmt.Fprintf(writer, "%s %s\n", cyan("Name:"), name)
	}
	executor, options := jobSpecs[jobID].Executor, jobSpecs[jobID].ExecutorOptions
	if executor == "" {
		executor = "local"
	}
	fmt.Fprintf(writer, "%s %s\n", cyan("Executor:"), executor)
	if len(options) > 0 {
		fmt.Fprintf(writer, "%s %s\n", cyan("Executor options:"), strings.Join(options, " "))
	}
	if dependencies := jobSpecs[jobID].DependsOn; len(dependencies) > 0 {
		fmt.Fprintf(writer, "%s %s\n", cyan("Depends on:"), strings.Join(dependencies, ", "))
	}
	fmt.Fprintf(writer, "%s %s\n", cyan("Submitted:"), readSubmittedAt(runDir, jobID))
	fmt.Fprintf(writer, "%s %s\n", cyan("Finished:"), readFinishedAt(runDir, jobID))
	if summary, err := loadRunSummary(filepath.Join(runDir, "summary.json")); err == nil {
		for _, result := range summary.Results {
			if result.ID == jobSpecs[jobID].ID {
				hosts := strings.Join(result.Hosts, ",")
				if hosts == "" {
					hosts = "-"
				}
				fmt.Fprintf(writer, "%s %s\n", cyan("Hosts:"), hosts)
				break
			}
		}
	}
	if status, ok := readJobStatus(filepath.Join(jobDir, "status")); ok {
		if status == 0 {
			fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), green(strconv.Itoa(status)))
		} else {
			fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), red(strconv.Itoa(status)))
		}
	} else if slurm, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json")); ok {
		status := fmt.Sprintf("Status: %s (exit code %d)", slurm.Phase, slurm.ExitCode)
		if slurm.Phase == "finished" && slurm.ExitCode == 0 {
			fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), green(strings.TrimPrefix(status, "Status: ")))
		} else if slurm.Phase == "finished" {
			fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), red(strings.TrimPrefix(status, "Status: ")))
		} else {
			fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), yellow(strings.TrimPrefix(status, "Status: ")))
		}
	} else {
		summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
		if err == nil {
			for _, result := range summary.Results {
				if result.ID == jobSpecs[jobID].ID && strings.HasPrefix(result.Error, "blocked") {
					fmt.Fprintf(writer, "%s %s\n", cyan("Status:"), yellow("blocked (dependency failed)"))
				}
			}
		}
	}
	command := readJSONCommand(filepath.Join(jobDir, "command.json"))
	fmt.Fprintf(writer, "%s %s\n", cyan("Command:"), command)
	fmt.Fprintf(writer, "%s %s\n\n", cyan("Output:"), filepath.Join(jobDir, "output"))
	output, err := os.ReadFile(filepath.Join(jobDir, "output"))
	if err == nil {
		fmt.Fprint(writer, string(output))
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

func readJobName(jobDir string) string {
	data, err := os.ReadFile(filepath.Join(jobDir, "name"))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func showRunLogs(writer io.Writer, paths pathSet, runID string, failedOnly bool) int {
	runDir := filepath.Join(paths.runsDir, runID)
	entries, err := os.ReadDir(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read run directory: %v\n", err)
		return 1
	}
	jobSpecs := loadRunJobSpecs(runDir)
	writeShowTargetHeader(writer, paths)
	fmt.Fprintf(writer, "%s %s\n\n", cyan("Run:"), runID)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		jobID := entry.Name()
		jobDir := filepath.Join(runDir, jobID)

		status, statusOK := readJobStatus(filepath.Join(jobDir, "status"))
		if !statusOK {
			if slurm, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json")); ok && slurmStatusTerminal(slurm.Phase) {
				status = slurm.ExitCode
				statusOK = true
			}
		}

		if failedOnly && (!statusOK || status == 0) {
			continue
		}

		name := readJobName(jobDir)
		if name == "" {
			name = jobSpecs[jobID].Name
		}
		command := readJSONCommand(filepath.Join(jobDir, "command.json"))

		header := fmt.Sprintf("=== Job: %s", jobID)
		if name != "" {
			header += fmt.Sprintf(" (Name: %s)", name)
		}
		if statusOK {
			if status == 0 {
				header += fmt.Sprintf(" [Status: %d (success)]", status)
			} else {
				header += fmt.Sprintf(" [Status: %d (failed)]", status)
			}
		} else {
			header += " [Status: running]"
		}
		header += " ==="
		fmt.Fprintln(writer, cyan(header))
		fmt.Fprintf(writer, "Command: %s\n", command)
		fmt.Fprintf(writer, "Output path: %s\n", filepath.Join(jobDir, "output"))

		output, err := os.ReadFile(filepath.Join(jobDir, "output"))
		if err == nil && len(output) > 0 {
			fmt.Fprintln(writer, "--- Log Output ---")
			fmt.Fprint(writer, string(output))
			if !strings.HasSuffix(string(output), "\n") {
				fmt.Fprintln(writer)
			}
		} else {
			fmt.Fprintln(writer, "(No output log)")
		}
		fmt.Fprintln(writer)
	}

	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		seen[entry.Name()] = true
	}
	queue, queueErr := loadQueue(filepath.Join(runDir, "commands.json"))
	if queueErr == nil {
		for _, command := range queue.Commands {
			if seen[command.ID] || command.Origin == nil {
				continue
			}
			// Carried forward from a previous run: it was not re-executed
			// here, so read its output from the origin run/job instead.
			if failedOnly && command.Origin.Status != "failed" {
				continue
			}
			header := fmt.Sprintf("=== Job: %s", command.ID)
			if command.Name != "" {
				header += fmt.Sprintf(" (Name: %s)", command.Name)
			}
			header += fmt.Sprintf(" [carried forward from run %s: %s] ===", command.Origin.RunID, command.Origin.Status)
			fmt.Fprintln(writer, cyan(header))
			fmt.Fprintf(writer, "Command: %s\n", strings.Join(command.Command, " "))
			originDir := filepath.Join(paths.runsDir, command.Origin.RunID, command.Origin.JobID)
			fmt.Fprintf(writer, "Output path: %s\n", filepath.Join(originDir, "output"))
			output, err := os.ReadFile(filepath.Join(originDir, "output"))
			if err == nil && len(output) > 0 {
				fmt.Fprintln(writer, "--- Log Output ---")
				fmt.Fprint(writer, string(output))
				if !strings.HasSuffix(string(output), "\n") {
					fmt.Fprintln(writer)
				}
			} else {
				fmt.Fprintln(writer, "(No output log)")
			}
			fmt.Fprintln(writer)
		}
	}
	return 0
}

func followJobLog(writer io.Writer, paths pathSet, runID, jobID string) int {
	jobDir := filepath.Join(paths.runsDir, runID, jobID)
	outputPath := filepath.Join(jobDir, "output")
	output, err := os.ReadFile(outputPath)
	if err != nil {
		if origin := loadRunOrigin(filepath.Join(paths.runsDir, runID), jobID); origin != nil {
			// Carried forward: it already finished under the origin run, so
			// there is nothing new to follow, just print its output once.
			fmt.Fprintf(writer, "%s carried forward from run %s (no re-execution)\n\n", cyan("Note:"), origin.RunID)
			originOutput, readErr := os.ReadFile(filepath.Join(paths.runsDir, origin.RunID, origin.JobID, "output"))
			if readErr == nil {
				_, _ = writer.Write(originOutput)
			}
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to read job output: %v\n", err)
		return 1
	}
	if _, err := writer.Write(output); err != nil {
		return 1
	}
	offset := int64(len(output))
	for {
		data, err := os.ReadFile(outputPath)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "failed to read job output: %v\n", err)
				return 1
			}
			continue
		}
		if len(data) > int(offset) {
			if _, err := writer.Write(data[offset:]); err != nil {
				return 1
			}
			offset = int64(len(data))
		}
		statusPath := filepath.Join(jobDir, "status")
		status, statusOK := readJobStatus(statusPath)
		if !statusOK {
			if slurm, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json")); ok && slurmStatusTerminal(slurm.Phase) {
				status = slurm.ExitCode
				statusOK = true
			}
		}
		if statusOK && status != 0 {
			return 0
		}
		if statusOK && status == 0 {
			return 0
		}
		time.Sleep(250 * time.Millisecond)
	}
}
