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
)

const pagerLineLimit = 24

func cmdShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	runIDOption := cliString(fs, "run-id", "")
	jobIDOption := cliString(fs, "job-id", "")
	failedOnly := cliBool(fs, "failed", false)
	showRunsList := cliBool(fs, "runs", false)
	showLogs := cliBool(fs, "logs", false)
	showFailedLogs := cliBool(fs, "failed-logs", false)
	noPager := cliBool(fs, "no-pager", false)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("show"))
		return 1
	}

	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	queueName, err := resolveQueueName(baseDir, *queueNameOption)
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
	runID, err := selectRunID(paths, *runIDOption)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *jobIDOption != "" {
		return showWithPager(!*noPager, func(writer io.Writer) int {
			return showJob(writer, paths, runID, *jobIDOption)
		})
	}
	if *showLogs || *showFailedLogs {
		return showWithPager(!*noPager, func(writer io.Writer) int {
			return showRunLogs(writer, paths, runID, *showFailedLogs)
		})
	}
	return showRun(paths, runID, *failedOnly)
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
	if diff, err := compareQueueWithRun(paths.queueFile, filepath.Join(runDir, "commands.json")); err == nil && diff.HasChanges() {
		fmt.Printf("\n%s\n", yellow("Queue differs from this run:"))
		fmt.Printf("  Added: %d\n  Removed: %d\n  Changed: %d\n", diff.Added, diff.Removed, diff.Changed)
	}
	jobSpecs := loadRunJobSpecs(runDir)
	resultByID := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		resultByID[result.ID] = result
	}

	entries, err := os.ReadDir(runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read run directory: %v\n", err)
		return 1
	}
	fmt.Println("\n" + cyan("Jobs:"))
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %s", "JOB ID", "NAME", "DEPENDS ON", "STATUS", "BACKEND", "SUBMITTED", "FINISHED", "COMMAND")))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		jobID := entry.Name()
		name := readJobName(filepath.Join(runDir, jobID))
		if name == "" {
			name = jobSpecs[jobID].Name
		}
		if name == "" {
			name = "-"
		}
		dependsOn := strings.Join(jobSpecs[jobID].DependsOn, ",")
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
			if result, ok := resultByID[jobSpecs[jobID].ID]; ok {
				status = result.ExitCode
				statusOK = true
				blocked = strings.HasPrefix(result.Error, "blocked")
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
			if blocked {
				statusText = yellow("blocked")
			} else if status != 0 {
				statusText = red(strconv.Itoa(status))
			}
			fmt.Printf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %s\n", jobID, name, dependsOn, statusText, backendText, submittedAt, finishedAt, command)
		} else {
			fmt.Printf("%-12s %-15s %-20s %-10s %-30s %-24s %-24s %s\n", jobID, name, dependsOn, yellow("running"), backendText, submittedAt, finishedAt, command)
		}
	}
	fmt.Printf("\n%s\n", cyan("Output directory: "+runDir))
	fmt.Printf("\nTo clear all saved run logs:\n  fjob clear --basedir %s --queue-name %s\n", paths.baseDir, paths.queueName)
	return 0
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
	if left.ID != right.ID || left.Name != right.Name || left.Backend != right.Backend {
		return false
	}
	if !slicesEqual(left.Command, right.Command) || !slicesEqual(left.SbatchOptions, right.SbatchOptions) || !slicesEqual(left.DependsOn, right.DependsOn) {
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
			fmt.Printf("Queue: %s\nNo runs found.\n", paths.queueName)
			return 0
		}
		fmt.Fprintf(os.Stderr, "failed to read runs directory: %v\n", err)
		return 1
	}

	type runInfo struct {
		id         string
		startedAt  string
		finishedAt string
		exitCode   int
		statusText string
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
				r.startedAt = summary.StartedAt
				r.finishedAt = summary.FinishedAt
				r.exitCode = summary.ExitCode
				r.hasSummary = true
				if summary.ExitCode == 0 {
					r.statusText = green("0 (success)")
				} else {
					r.statusText = red(fmt.Sprintf("%d (failed)", summary.ExitCode))
				}
			}
		}
		if !r.hasSummary {
			r.statusText = yellow("running")
		}
		runs = append(runs, r)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].modTime > runs[j].modTime
	})

	fmt.Printf("%s\n%s\n", cyan("Queue: "+paths.queueName), cyan("Runs directory: "+paths.runsDir))
	fmt.Println("\n" + cyan("Runs:"))
	fmt.Printf("%s\n", cyan(fmt.Sprintf("%-36s %-15s %-24s %-24s", "RUN ID", "EXIT CODE", "STARTED", "FINISHED")))
	for _, r := range runs {
		started := r.startedAt
		if started == "" {
			started = "-"
		}
		finished := r.finishedAt
		if finished == "" {
			finished = "-"
		}
		fmt.Printf("%-36s %-15s %-24s %-24s\n", r.id, r.statusText, started, finished)
	}
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

func showJob(writer io.Writer, paths pathSet, runID, jobID string) int {
	jobDir := filepath.Join(paths.runsDir, runID, jobID)
	runDir := filepath.Dir(jobDir)
	if info, err := os.Stat(jobDir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "job %q not found in run %q\n", jobID, runID)
		return 1
	}
	fmt.Fprintf(writer, "%s\n%s\n%s\n", cyan("Queue: "+paths.queueName), cyan("Run: "+runID), cyan("Job: "+jobID))
	jobSpecs := loadRunJobSpecs(runDir)
	name := readJobName(jobDir)
	if name == "" {
		name = jobSpecs[jobID].Name
	}
	if name != "" {
		fmt.Fprintf(writer, "Name: %s\n", name)
	}
	if dependencies := jobSpecs[jobID].DependsOn; len(dependencies) > 0 {
		fmt.Fprintf(writer, "Depends on: %s\n", strings.Join(dependencies, ", "))
	}
	fmt.Fprintf(writer, "Submitted: %s\n", readSubmittedAt(runDir, jobID))
	fmt.Fprintf(writer, "Finished: %s\n", readFinishedAt(runDir, jobID))
	if status, ok := readJobStatus(filepath.Join(jobDir, "status")); ok {
		if status == 0 {
			fmt.Fprintf(writer, "%s\n", green(fmt.Sprintf("Status: %d", status)))
		} else {
			fmt.Fprintf(writer, "%s\n", red(fmt.Sprintf("Status: %d", status)))
		}
	} else if slurm, ok := loadSlurmStatus(filepath.Join(jobDir, "status.json")); ok {
		status := fmt.Sprintf("Status: %s (exit code %d)", slurm.Phase, slurm.ExitCode)
		if slurm.Phase == "finished" && slurm.ExitCode == 0 {
			fmt.Fprintf(writer, "%s\n", green(status))
		} else if slurm.Phase == "finished" {
			fmt.Fprintf(writer, "%s\n", red(status))
		} else {
			fmt.Fprintf(writer, "%s\n", yellow(status))
		}
	} else {
		summary, err := loadRunSummary(filepath.Join(runDir, "summary.json"))
		if err == nil {
			for _, result := range summary.Results {
				if result.ID == jobSpecs[jobID].ID && strings.HasPrefix(result.Error, "blocked") {
					fmt.Fprintf(writer, "%s\n", yellow("Status: blocked (dependency failed)"))
				}
			}
		}
	}
	fmt.Fprintf(writer, "Command: %s\n", readCommand(filepath.Join(jobDir, "command")))
	fmt.Fprintf(writer, "Output: %s\n\n", filepath.Join(jobDir, "output"))
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
		command := readCommand(filepath.Join(jobDir, "command"))
		if command == "" {
			command = readJSONCommand(filepath.Join(jobDir, "command.json"))
		}

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
	return 0
}
