package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadWebStateIncludesAllQueues(t *testing.T) {
	baseDir := t.TempDir()
	for _, queueName := range []string{"build", "test"} {
		paths, err := resolvePaths(baseDir, queueName)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(paths.queueFile, Queue{}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(paths.runsDir, "run-1", "summary.json"), RunSummary{RunID: "run-1", Status: "finished"}); err != nil {
			t.Fatal(err)
		}
	}

	state, err := loadWebState(baseDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Queues) != 2 || state.Queues[0].QueueName != "build" || state.Queues[1].QueueName != "test" {
		t.Fatalf("queues = %#v, want build and test", state.Queues)
	}

	filtered, err := loadWebState(baseDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Queues) != 1 || filtered.Queues[0].QueueName != "test" {
		t.Fatalf("filtered queues = %#v, want test", filtered.Queues)
	}
}

func TestLoadWebJobsIncludesCommandMetadata(t *testing.T) {
	runDir := t.TempDir()
	queue := Queue{Commands: []QueuedCommand{{
		ID: "job-1", Name: "train", Command: []string{"python", "train.py"},
		Executor: "slurm", ExecutorOptions: []string{"-p", "gpu"}, DependsOn: []string{"prepare"},
	}}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	jobs, err := loadWebJobs(runDir, RunSummary{Results: []JobResult{{ID: "job-1", ExitCode: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %#v, want one job", jobs)
	}
	job := jobs[0]
	if job.Name != "train" || job.Executor != "slurm" || len(job.ExecutorOptions) != 2 || len(job.DependsOn) != 1 || job.Result == nil {
		t.Fatalf("job = %#v, want command metadata and result", job)
	}
}

func TestWriteRunContext(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRunContext(paths, "run-1", "/work/project"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(paths.runsDir, "run-1", "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var context RunContext
	if err := json.Unmarshal(data, &context); err != nil {
		t.Fatal(err)
	}
	if context.CWD != "/work/project" {
		t.Fatalf("cwd = %q, want /work/project", context.CWD)
	}
}

func TestWebCopyEndpointCopiesWithoutRunner(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	if err := writeJSON(filepath.Join(runDir, "commands.json"), Queue{Commands: []QueuedCommand{{ID: "job-1", Name: "failed", Command: []string{"false"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{Results: []JobResult{{ID: "job-1", ExitCode: 1}}}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/copy", strings.NewReader(`{"queue_name":"default","run_id":"run-1","selection":"failed"}`))
	recorder := httptest.NewRecorder()
	newWebHandler(baseDir, "").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Commands) != 1 || queue.Commands[0].ID == "job-1" {
		t.Fatalf("queue = %#v, want one copied job with a new ID", queue)
	}
	if _, err := os.Stat(serverSocketPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("runner socket exists after web copy: %v", err)
	}
}

func TestWebChangeEndpointUpdatesQueueJob(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"old"}}}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/change", strings.NewReader(`{"queue_name":"default","job_id":"job-1","command":["new","arg"],"executor_options":["-p","gpu"]}`))
	recorder := httptest.NewRecorder()
	newWebHandler(baseDir, "").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		t.Fatal(err)
	}
	job := queue.Commands[0]
	if len(job.Command) != 2 || job.Command[0] != "new" || len(job.ExecutorOptions) != 2 || job.ExecutorOptions[1] != "gpu" {
		t.Fatalf("job = %#v, want updated command and executor options", job)
	}
}
