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

func TestCLIDocsPageUsesCommandMetadata(t *testing.T) {
	page := cliDocsHTML("/")
	for _, want := range []string{"rotari check", "rotari completion", "--queue-name", "Generated from the command metadata"} {
		if !strings.Contains(page, want) {
			t.Fatalf("docs page does not contain %q", want)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/docs/", nil)
	recorder := httptest.NewRecorder()
	newWebHandler(t.TempDir(), "").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "rotari CLI") {
		t.Fatalf("docs response = status %d, body %q", recorder.Code, recorder.Body.String())
	}
}

func TestGenerateStaticWebIncludesCLIDocs(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.queueFile, Queue{}); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(t.TempDir(), "web")
	if err := generateStaticWeb(outputDir, baseDir, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "docs", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "rotari CLI") || !strings.Contains(string(data), "../") {
		t.Fatalf("static docs page = %q", string(data))
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

func TestLoadWebStateIncludesRunContextAndTimeline(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(paths.runsDir, "run-1")
	queue := Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"true"}}}}
	if err := writeJSON(paths.queueFile, queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "context.json"), RunContext{CWD: "/work/project", Hostname: "node-a", StartedLoad: &LoadAverage{One: 1.25, Five: 1.5, Fifteen: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "summary.json"), RunSummary{RunID: "run-1", Status: "finished", StartedAt: "2026-09-16T00:00:00Z", FinishedAt: "2026-09-16T00:00:03Z", Results: []JobResult{{ID: "job-1", ExitCode: 0}}}); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(runDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte("2026-09-16T00:00:01Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte("2026-09-16T00:00:02Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := loadWebState(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	run := state.Queues[0].Runs[0]
	if run.Context.Hostname != "node-a" || run.Context.StartedLoad == nil || run.CWD != "/work/project" {
		t.Fatalf("context = %#v, cwd = %q, want host/load/cwd", run.Context, run.CWD)
	}
	if run.Jobs[0].SubmittedAt != "2026-09-16T00:00:01Z" || run.Jobs[0].FinishedAt != "2026-09-16T00:00:02Z" {
		t.Fatalf("job timestamps = %#v, want submitted and finished timestamps", run.Jobs[0])
	}
	if len(run.Timeline) != 3 || run.Timeline[1].Running != 1 || run.Timeline[2].Finished != 1 || run.Timeline[2].Success != 1 {
		t.Fatalf("timeline = %#v, want pending, submitted, and successful finished transitions", run.Timeline)
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
	if len(queue.Commands) != 1 || queue.Commands[0].ID != "job-1" {
		t.Fatalf("queue = %#v, want one copied job with the source ID preserved", queue)
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
