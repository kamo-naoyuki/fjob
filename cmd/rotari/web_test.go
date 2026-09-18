package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWebRunGuidanceUsesRunIDOnly(t *testing.T) {
	html := webHTML()
	if !strings.Contains(html, "rotari retry --run-id '+shellQuote(runID)") {
		t.Fatal("web run guidance does not contain a run-id-only retry command")
	}
	if strings.Contains(html, "rotari retry'+basedir+' --queue-name '+shellQuote(queueName)") {
		t.Fatal("web run guidance still contains basedir and project name")
	}
	if !strings.Contains(html, "Cancel run") || !strings.Contains(html, "/api/cancel-run") {
		t.Fatal("web run page does not contain run cancellation controls")
	}
}

func TestWebHTMLIncludesEmbeddedThemeFavicons(t *testing.T) {
	html := webHTML()
	for _, want := range []string{
		`media="(prefers-color-scheme: dark)"`,
		`media="(prefers-color-scheme: light)"`,
		`data:image/svg+xml;base64,`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("web HTML does not contain %q", want)
		}
	}
}

func TestWebCancelRunRejectsStaleRunID(t *testing.T) {
	baseDir := t.TempDir()
	paths, err := resolvePaths(baseDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(paths.lockFile, LockInfo{RunID: "run-current", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/cancel-run", strings.NewReader(`{"project_name":"default","run_id":"run-old"}`))
	recorder := httptest.NewRecorder()
	newWebHandler(baseDir, "").ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("status = %d, want stale run rejection", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "is no longer running") {
		t.Fatalf("body = %q, want stale run error", recorder.Body.String())
	}
}

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
	t.Setenv(envRunID, "web-run")
	state, err = loadWebState(baseDir, "")
	if err != nil {
		t.Fatal(err)
	}
	var foundRunID bool
	for _, definition := range state.Environments {
		if definition.Name == envRunID {
			foundRunID = definition.Value == "web-run" && definition.Job && definition.Array
			break
		}
	}
	if !foundRunID {
		t.Fatalf("web environments missing current run ID: %#v", state.Environments)
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
	for _, want := range []string{"rotari check", "rotari completion", "--project-name", "Generated from the command metadata"} {
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

func TestEnvironmentPageUsesDefinitions(t *testing.T) {
	t.Setenv(envRunID, "web-run")
	page := environmentHTML("/", environmentDefinitions())
	for _, want := range []string{envRunID, envBaseDir, "State directory"} {
		if !strings.Contains(page, want) {
			t.Fatalf("environment page does not contain %q", want)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/environment/", nil)
	recorder := httptest.NewRecorder()
	newWebHandler(t.TempDir(), "").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "rotari environment variables") {
		t.Fatalf("environment response = status %d, body %q", recorder.Code, recorder.Body.String())
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
	environmentData, err := os.ReadFile(filepath.Join(outputDir, "environment", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(environmentData), "rotari environment variables") || !strings.Contains(string(environmentData), "../") {
		t.Fatalf("static environment page = %q", string(environmentData))
	}
	index, err := os.ReadFile(filepath.Join(outputDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "rewriteStaticLinks();") || !strings.Contains(string(index), "path===root||path.startsWith(root+'/')") || !strings.Contains(string(index), "new MutationObserver(rewriteStaticLinks)") {
		t.Fatal("static web page does not rewrite links before rendering")
	}
	if !strings.Contains(string(index), "data:image/svg+xml;base64,") {
		t.Fatal("static web page does not contain embedded favicon data")
	}
	for _, obsolete := range []string{"queue_name", "/queue/", "state.queues"} {
		if strings.Contains(string(index), obsolete) {
			t.Fatalf("static web page contains obsolete project identifier %q", obsolete)
		}
	}
	for _, want := range []string{"project_name", "/project/", "state.projects"} {
		if !strings.Contains(string(index), want) {
			t.Fatalf("static web page does not contain %q", want)
		}
	}
}

func TestWebSeparatesLogsFromActions(t *testing.T) {
	for _, want := range []string{"function mergeActionColumns(){}", "Job log", "View log", "Source log", "Logs"} {
		if !strings.Contains(webIndexHTML, want) {
			t.Fatalf("web page does not contain %q", want)
		}
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

func TestLoadWebJobsIncludesSchedulerState(t *testing.T) {
	runDir := t.TempDir()
	queue := Queue{Commands: []QueuedCommand{{ID: "job-1", Command: []string{"sleep", "10"}, Executor: "slurm"}}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	writeSchedulerStatus(filepath.Join(runDir, "job-1"), "PENDING")

	jobs, err := loadWebJobs(runDir, RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].SchedulerState != "pending" {
		t.Fatalf("jobs = %#v, want pending scheduler state", jobs)
	}
}

func TestLoadWebJobsProjectsFinishedSchedulerStatus(t *testing.T) {
	runDir := t.TempDir()
	queue := Queue{Commands: []QueuedCommand{{ID: "array-1", Command: []string{"true"}, Executor: "slurm"}}}
	if err := writeJSON(filepath.Join(runDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, "array-1", "status.json"), slurmStatus{Phase: "running", ExitCode: 0, FinishedAt: "2026-09-18T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	jobs, err := loadWebJobs(runDir, RunSummary{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Result == nil || jobs[0].Result.ExitCode != 0 || jobs[0].FinishedAt == "" {
		t.Fatalf("jobs = %#v, want finished result from status.json", jobs)
	}
}

func TestLoadWebJobsUsesCarriedOriginTimestamps(t *testing.T) {
	runsDir := t.TempDir()
	sourceRunDir := filepath.Join(runsDir, "run-1")
	currentRunDir := filepath.Join(runsDir, "run-2")
	queue := Queue{Commands: []QueuedCommand{{
		ID: "job-1", Command: []string{"true"},
		Origin: &JobOrigin{RunID: "run-1", JobID: "job-1", Status: "success"},
	}}}
	if err := writeJSON(filepath.Join(currentRunDir, "commands.json"), queue); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(sourceRunDir, "job-1")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "submitted_at"), []byte("2026-09-16T00:00:01Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "finished_at"), []byte("2026-09-16T00:00:02Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs, err := loadWebJobs(currentRunDir, RunSummary{Results: []JobResult{{ID: "job-1", ExitCode: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].SubmittedAt != "2026-09-16T00:00:01Z" || jobs[0].FinishedAt != "2026-09-16T00:00:02Z" {
		t.Fatalf("job timestamps = %#v, want carried origin timestamps", jobs[0])
	}
}

func TestLoadWebStateIncludesRunContextAndTimeline(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
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
	if run.StartedAt != "2026-09-16 09:00:00 JST" || run.FinishedAt != "2026-09-16 09:00:03 JST" {
		t.Fatalf("run timestamps = %#v, want JST display timestamps", run.RunSummary)
	}
	if run.Jobs[0].SubmittedAt != "2026-09-16 09:00:01 JST" || run.Jobs[0].FinishedAt != "2026-09-16 09:00:02 JST" {
		t.Fatalf("job timestamps = %#v, want submitted and finished timestamps", run.Jobs[0])
	}
	if len(run.Timeline) != 3 || run.Timeline[1].Running != 1 || run.Timeline[2].Finished != 1 || run.Timeline[2].Success != 1 {
		t.Fatalf("timeline = %#v, want pending, submitted, and successful finished transitions", run.Timeline)
	}
}

func TestBuildWebTimelineCountsCarriedResultsAtStart(t *testing.T) {
	summary := RunSummary{StartedAt: "2026-09-16T00:00:00Z"}
	jobs := []webJob{
		{ID: "carried-success", Origin: &JobOrigin{RunID: "previous", JobID: "carried-success"}, SubmittedAt: "2026-09-15T00:00:01Z", FinishedAt: "2026-09-15T00:00:02Z", Result: &JobResult{ID: "carried-success", ExitCode: 0}},
		{ID: "rerun-failed", SubmittedAt: "2026-09-16T00:00:01Z", FinishedAt: "2026-09-16T00:00:02Z", Result: &JobResult{ID: "rerun-failed", ExitCode: 1}},
	}

	timeline := buildWebTimeline(summary, jobs)
	if len(timeline) != 3 {
		t.Fatalf("timeline = %#v, want start, submitted, and finished points", timeline)
	}
	if timeline[0].Pending != 1 || timeline[0].Finished != 1 || timeline[0].Success != 1 {
		t.Fatalf("timeline start = %#v, want carried success counted as finished at start", timeline[0])
	}
	if timeline[2].Pending != 0 || timeline[2].Finished != 2 || timeline[2].Success != 1 || timeline[2].Failed != 1 {
		t.Fatalf("timeline end = %#v, want one carried success and one executed failure", timeline[2])
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
	samples := readLoadSamples(loadSamplesPath(paths, "run-1"))
	if context.StartedLoad != nil && len(samples) != 1 {
		t.Fatalf("load samples = %#v, want initial load sample", samples)
	}
	if err := finishRunContext(paths, "run-1"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(paths.runsDir, "run-1", "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &context); err != nil {
		t.Fatal(err)
	}
	samples = readLoadSamples(loadSamplesPath(paths, "run-1"))
	if context.FinishedLoad != nil && len(samples) != 2 {
		t.Fatalf("load samples = %#v, want initial and final load samples", samples)
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

	request := httptest.NewRequest(http.MethodPost, "/api/copy", strings.NewReader(`{"project_name":"default","run_id":"run-1","selection":"failed"}`))
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
	request := httptest.NewRequest(http.MethodPost, "/api/change", strings.NewReader(`{"project_name":"default","job_id":"job-1","command":["new","arg"],"executor_options":["-p","gpu"]}`))
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

func TestMethodNotAllowedRejectsNonGetOnAPIState(t *testing.T) {
	baseDir := t.TempDir()
	request := httptest.NewRequest(http.MethodPost, "/api/state", nil)
	recorder := httptest.NewRecorder()
	newWebHandler(baseDir, "").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestNewFlagSetWritesUsageToStderr(t *testing.T) {
	fs := newFlagSet("web")
	if fs.Name() != "web" {
		t.Fatalf("flag set name = %q, want %q", fs.Name(), "web")
	}
	if err := fs.Parse([]string{"--unknown-flag"}); err == nil {
		t.Fatal("Parse with an unknown flag did not return an error")
	}
}

func TestInterruptSignalDeliversSIGTERM(t *testing.T) {
	signals := interruptSignal()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signals:
	case <-time.After(time.Second):
		t.Fatal("interruptSignal channel did not receive SIGTERM")
	}
}

func TestCmdWebGeneratesStaticSiteWithoutStartingServer(t *testing.T) {
	baseDir := t.TempDir()
	if _, err := enqueueCommand(baseDir, "default", []string{"echo", "job"}, "", nil, nil, "job", nil); err != nil {
		t.Fatal(err)
	}
	staticDir := t.TempDir()

	if code := cmdWeb([]string{"--basedir", baseDir, "--static-dir", staticDir}); code != 0 {
		t.Fatalf("cmdWeb exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(staticDir, "index.html")); err != nil {
		t.Fatalf("static site was not generated: %v", err)
	}
}

func TestCmdWebRejectsInvalidPort(t *testing.T) {
	baseDir := t.TempDir()
	if code := cmdWeb([]string{"--basedir", baseDir, "--port", "70000"}); code != 1 {
		t.Fatalf("cmdWeb exit code = %d, want 1 for invalid port", code)
	}
}

func TestCmdWebRejectsPositionalArguments(t *testing.T) {
	baseDir := t.TempDir()
	if code := cmdWeb([]string{"--basedir", baseDir, "extra"}); code != 1 {
		t.Fatalf("cmdWeb exit code = %d, want 1 for unexpected positional argument", code)
	}
}
