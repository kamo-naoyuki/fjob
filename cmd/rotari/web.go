package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const webDefaultPort = 8787

type webRun struct {
	RunSummary
	Jobs     []webJob           `json:"jobs"`
	CWD      string             `json:"cwd,omitempty"`
	Context  RunContext         `json:"context,omitempty"`
	Timeline []webTimelinePoint `json:"timeline,omitempty"`
	Running  bool               `json:"running"`
}

type webJob struct {
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	Command         []string   `json:"command"`
	Executor        string     `json:"executor,omitempty"`
	ExecutorOptions []string   `json:"executor_options,omitempty"`
	DependsOn       []string   `json:"depends_on,omitempty"`
	Result          *JobResult `json:"result,omitempty"`
	Origin          *JobOrigin `json:"origin,omitempty"`
	SubmittedAt     string     `json:"submitted_at,omitempty"`
	FinishedAt      string     `json:"finished_at,omitempty"`
}

type webTimelinePoint struct {
	At       string `json:"at"`
	Pending  int    `json:"pending"`
	Running  int    `json:"running"`
	Finished int    `json:"finished"`
	Success  int    `json:"success"`
	Failed   int    `json:"failed"`
}

type webQueueState struct {
	QueueName    string   `json:"queue_name"`
	Queue        Queue    `json:"queue"`
	Runs         []webRun `json:"runs"`
	RunnerPID    int      `json:"runner_pid,omitempty"`
	RunningRunID string   `json:"running_run_id,omitempty"`
}

type webState struct {
	BaseDir   string          `json:"base_dir"`
	Queues    []webQueueState `json:"queues"`
	UpdatedAt string          `json:"updated_at"`
}

type webCopyRequest struct {
	QueueName string `json:"queue_name"`
	RunID     string `json:"run_id"`
	Selection string `json:"selection"`
	Append    bool   `json:"append"`
	Overwrite bool   `json:"overwrite"`
}

type webChangeRequest struct {
	QueueName            string   `json:"queue_name"`
	JobID                string   `json:"job_id"`
	SetJobName           string   `json:"set_job_name,omitempty"`
	Command              []string `json:"command,omitempty"`
	Executor             string   `json:"executor,omitempty"`
	ExecutorOptions      []string `json:"executor_options,omitempty"`
	ClearExecutorOptions bool     `json:"clear_executor_options"`
	DependsOn            []string `json:"depends_on,omitempty"`
	ClearDependsOn       bool     `json:"clear_depends_on"`
}

type webRemoveRequest struct {
	QueueName string `json:"queue_name"`
	JobID     string `json:"job_id"`
}

type webCancelRequest struct {
	QueueName string `json:"queue_name"`
	JobID     string `json:"job_id"`
}

type webClearRequest struct {
	QueueName string `json:"queue_name"`
	RunID     string `json:"run_id"`
}

func cmdWeb(args []string) int {
	fs := newFlagSet("web")
	basedir := cliString(fs, "basedir", "")
	queueNameOption := cliString(fs, "queue-name", "")
	host := cliString(fs, "host", "127.0.0.1")
	port := cliInt(fs, "port", webDefaultPort)
	staticDir := cliString(fs, "static-dir", "")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if len(fs.Args()) != 0 || *port < 1 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "usage: "+cliUsage("web"))
		return 1
	}
	baseDir, _, err := resolveBaseDir(*basedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve state directory: %v\n", err)
		return 1
	}
	if *staticDir != "" {
		if err := generateStaticWeb(*staticDir, baseDir, *queueNameOption); err != nil {
			fmt.Fprintf(os.Stderr, "failed to generate static web: %v\n", err)
			return 1
		}
		return 0
	}
	handler := newWebHandler(baseDir, *queueNameOption)
	server := &http.Server{Addr: *host + ":" + strconv.Itoa(*port), Handler: handler}
	go func() {
		<-interruptSignal()
		_ = server.Close()
	}()
	fmt.Printf("rotari web listening at http://%s\n", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "web server failed: %v\n", err)
		return 1
	}
	return 0
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func interruptSignal() <-chan os.Signal {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals
}

func newWebHandler(baseDir, queueFilter string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(webHTML()))
	})
	mux.HandleFunc("/api/state", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		state, err := loadWebState(baseDir, queueFilter)
		if err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, state)
	})
	mux.HandleFunc("/api/log", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		queueName := request.URL.Query().Get("queue_name")
		runID, jobID := request.URL.Query().Get("run_id"), request.URL.Query().Get("job_id")
		if !validWebID(queueName) || !validWebID(runID) || !validWebID(jobID) {
			writeWebError(writer, fmt.Errorf("queue_name, run_id and job_id are required"))
			return
		}
		data, err := os.ReadFile(filepath.Join(baseDir, "queues", queueName, "runs", runID, jobID, "output"))
		if err != nil {
			writeWebError(writer, err)
			return
		}
		lines := strings.Split(string(data), "\n")
		if request.URL.Query().Get("tail") != "" {
			count, parseErr := strconv.Atoi(request.URL.Query().Get("tail"))
			if parseErr != nil || count < 1 {
				writeWebError(writer, fmt.Errorf("tail must be a positive integer"))
				return
			}
			before := 0
			if value := request.URL.Query().Get("before"); value != "" {
				before, parseErr = strconv.Atoi(value)
				if parseErr != nil || before < 0 {
					writeWebError(writer, fmt.Errorf("before must be a non-negative integer"))
					return
				}
			}
			end := len(lines) - before
			if end < 0 {
				end = 0
			}
			start := end - count
			if start < 0 {
				start = 0
			}
			if start < end {
				lines = lines[start:end]
			} else {
				lines = nil
			}
			data = []byte(strings.Join(lines, "\n"))
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = writer.Write(data)
	})
	mux.HandleFunc("/api/copy", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var copyRequest webCopyRequest
		if err := json.NewDecoder(request.Body).Decode(&copyRequest); err != nil {
			writeWebError(writer, err)
			return
		}
		if !validWebID(copyRequest.QueueName) || !validWebID(copyRequest.RunID) {
			writeWebError(writer, fmt.Errorf("queue_name and run_id are required"))
			return
		}
		if copyRequest.Append && copyRequest.Overwrite {
			writeWebError(writer, fmt.Errorf("append and overwrite cannot be used together"))
			return
		}
		if copyRequest.Selection == "" {
			copyRequest.Selection = "failed"
		}
		if copyRequest.Selection != "all" && copyRequest.Selection != "failed" && copyRequest.Selection != "unfinished" && copyRequest.Selection != "success" {
			writeWebError(writer, fmt.Errorf("unsupported copy selection %q", copyRequest.Selection))
			return
		}
		message, err := copyRunToQueue(baseDir, copyRequest.QueueName, copyRequest.RunID, copyRequest.Selection, nil, copyRequest.Append, copyRequest.Overwrite)
		if err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, map[string]string{"message": message})
	})
	mux.HandleFunc("/api/change", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var change webChangeRequest
		if err := json.NewDecoder(request.Body).Decode(&change); err != nil {
			writeWebError(writer, err)
			return
		}
		if !validWebID(change.QueueName) || !validWebID(change.JobID) || len(change.Command) == 0 {
			writeWebError(writer, fmt.Errorf("queue_name, job_id, and command are required"))
			return
		}
		message, err := changeBatch(baseDir, change.QueueName, "", change.JobID, "", change.Executor,
			change.ExecutorOptions, change.ClearExecutorOptions, change.SetJobName, change.DependsOn, change.ClearDependsOn, change.Command)
		if err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, map[string]string{"message": message})
	})
	mux.HandleFunc("/api/remove", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var remove webRemoveRequest
		if err := json.NewDecoder(request.Body).Decode(&remove); err != nil {
			writeWebError(writer, err)
			return
		}
		if !validWebID(remove.QueueName) || !validWebID(remove.JobID) {
			writeWebError(writer, fmt.Errorf("queue_name and job_id are required"))
			return
		}
		message, err := removeBatch(baseDir, remove.QueueName, "", []string{remove.JobID}, "")
		if err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, map[string]string{"message": message})
	})
	mux.HandleFunc("/api/clear-run", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var clear webClearRequest
		if err := json.NewDecoder(request.Body).Decode(&clear); err != nil {
			writeWebError(writer, err)
			return
		}
		if !validWebID(clear.QueueName) || !validWebID(clear.RunID) {
			writeWebError(writer, fmt.Errorf("queue_name and run_id are required"))
			return
		}
		if err := clearRunHistory(baseDir, clear.QueueName, clear.RunID); err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, map[string]string{"message": "run history deleted"})
	})
	mux.HandleFunc("/api/cancel-job", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var cancel webCancelRequest
		if err := json.NewDecoder(request.Body).Decode(&cancel); err != nil {
			writeWebError(writer, err)
			return
		}
		if !validWebID(cancel.QueueName) || !validWebID(cancel.JobID) {
			writeWebError(writer, fmt.Errorf("queue_name and job_id are required"))
			return
		}
		message, err := cancelQueueJobs(baseDir, cancel.QueueName, []string{cancel.JobID}, false)
		if err != nil {
			writeWebError(writer, err)
			return
		}
		writeWebJSON(writer, map[string]string{"message": message})
	})
	return mux
}

func loadWebState(baseDir, queueFilter string) (webState, error) {
	state := webState{BaseDir: baseDir, UpdatedAt: nowRFC3339()}
	queueNames := []string{}
	if queueFilter != "" {
		queueNames = append(queueNames, queueFilter)
	} else {
		entries, err := os.ReadDir(filepath.Join(baseDir, "queues"))
		if err != nil && !os.IsNotExist(err) {
			return webState{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				queueNames = append(queueNames, entry.Name())
			}
		}
		sort.Strings(queueNames)
	}
	for _, queueName := range queueNames {
		paths, err := resolvePaths(baseDir, queueName)
		if err != nil {
			return webState{}, err
		}
		queueState, err := loadWebQueueState(paths)
		if err != nil {
			return webState{}, err
		}
		state.Queues = append(state.Queues, queueState)
	}
	return state, nil
}

func generateStaticWeb(outputDir, baseDir, queueFilter string) error {
	state, err := loadWebState(baseDir, queueFilter)
	if err != nil {
		return err
	}
	logs := map[string]string{}
	for _, queue := range state.Queues {
		for _, run := range queue.Runs {
			for _, job := range run.Jobs {
				path := filepath.Join(baseDir, "queues", queue.QueueName, "runs", run.RunID, job.ID, "output")
				data, readErr := os.ReadFile(path)
				if readErr == nil {
					logs[staticLogKey(queue.QueueName, run.RunID, job.ID)] = string(data)
				}
			}
		}
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	logsJSON, err := json.Marshal(logs)
	if err != nil {
		return err
	}
	var escapedState, escapedLogs bytes.Buffer
	json.HTMLEscape(&escapedState, stateJSON)
	json.HTMLEscape(&escapedLogs, logsJSON)
	bootstrap := fmt.Sprintf(`<script>
window.__ROTARI_STATIC_STATE__=%s;
window.__ROTARI_STATIC_LOGS__=%s;
window.fetch=async function(input, init){
  const request=new URL(input, window.location.href);
  if(request.pathname.endsWith('/api/state')) return new Response(JSON.stringify(window.__ROTARI_STATIC_STATE__), {headers:{'Content-Type':'application/json'}});
  if(request.pathname.endsWith('/api/log')) {
    const key=staticLogKey(request.searchParams.get('queue_name'), request.searchParams.get('run_id'), request.searchParams.get('job_id'));
    return new Response(window.__ROTARI_STATIC_LOGS__[key] || '', {headers:{'Content-Type':'text/plain'}});
  }
  return new Response('This is a read-only static demo.', {status:405});
};
function staticLogKey(queue, run, job){return [queue, run, job].join('/');}
function staticRootPath(){const pathname=window.location.pathname;const parts=pathname.split('/').filter(Boolean);const queueIndex=parts.indexOf('queue');if(queueIndex>=0)return '/'+parts.slice(0,queueIndex).join('/');if(pathname.endsWith('/index.html'))return '/'+parts.slice(0,-1).join('/');if(pathname.endsWith('/'))return parts.length?'/'+parts.join('/'):'';return '/'+parts.slice(0,-1).join('/')}
function routeParts(){const root=staticRootPath().split('/').filter(Boolean);return window.location.pathname.split('/').filter(Boolean).slice(root.length)}
function staticPath(path){return staticRootPath().replace(/\/$/,'')+path}
function rewriteStaticLinks(){document.querySelectorAll('a[href^="/"]').forEach(link=>{link.setAttribute('href',staticPath(link.getAttribute('href')))})}
</script>`, escapedState.String(), escapedLogs.String())
	baseTemplate := webHTML()
	staticTemplate := strings.ReplaceAll(baseTemplate, "location.pathname.split('/').filter(Boolean)", "routeParts()")
	staticTemplate = strings.ReplaceAll(staticTemplate, "location.pathname!=='/'&&location.pathname!==''", "routeParts().length")
	staticTemplate = strings.ReplaceAll(staticTemplate, "state=await r.json();render()", "state=await r.json();render();rewriteStaticLinks()")
	template := strings.Replace(staticTemplate, "<script>\nconst executorNames=", bootstrap+"<script>\nconst executorNames=", 1)
	if template == baseTemplate {
		return errors.New("web HTML script marker not found")
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeStaticWebPage(filepath.Join(outputDir, "index.html"), template); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outputDir, ".nojekyll"), nil, 0o644); err != nil {
		return err
	}
	for _, queue := range state.Queues {
		queuePath := filepath.Join(outputDir, "queue", url.PathEscape(queue.QueueName))
		if err := writeStaticWebPage(filepath.Join(queuePath, "index.html"), template); err != nil {
			return err
		}
		for _, run := range queue.Runs {
			runPath := filepath.Join(queuePath, "run", url.PathEscape(run.RunID))
			if err := writeStaticWebPage(filepath.Join(runPath, "index.html"), template); err != nil {
				return err
			}
		}
	}
	return nil
}

func staticLogKey(queueName, runID, jobID string) string {
	return queueName + "/" + runID + "/" + jobID
}

func writeStaticWebPage(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o644)
}

func loadWebQueueState(paths pathSet) (webQueueState, error) {
	queue, err := loadQueue(paths.queueFile)
	if err != nil {
		return webQueueState{}, err
	}
	state := webQueueState{QueueName: paths.queueName, Queue: queue}
	runningStartedAt := ""
	if data, err := os.ReadFile(paths.lockFile); err == nil {
		var lock LockInfo
		if json.Unmarshal(data, &lock) == nil {
			state.RunningRunID = lock.RunID
			state.RunnerPID = lock.PID
			runningStartedAt = lock.StartedAt
		}
	}
	entries, err := os.ReadDir(paths.runsDir)
	if err != nil && !os.IsNotExist(err) {
		return webQueueState{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		summary, err := loadRunSummary(filepath.Join(paths.runsDir, runID, "summary.json"))
		if err != nil {
			summary = RunSummary{RunID: runID, Status: "running", StartedAt: runningStartedAt}
		}
		if summary.RunID == "" {
			summary.RunID = runID
		}
		jobs, err := loadWebJobs(filepath.Join(paths.runsDir, runID), summary)
		if err != nil {
			return webQueueState{}, err
		}
		context := RunContext{}
		if data, contextErr := os.ReadFile(filepath.Join(paths.runsDir, runID, "context.json")); contextErr == nil {
			_ = json.Unmarshal(data, &context)
		}
		state.Runs = append(state.Runs, webRun{RunSummary: summary, Jobs: jobs, CWD: context.CWD, Context: context, Timeline: buildWebTimeline(summary, jobs), Running: runID == state.RunningRunID})
	}
	sort.Slice(state.Runs, func(i, j int) bool { return state.Runs[i].RunID > state.Runs[j].RunID })
	return state, nil
}

func loadWebJobs(runDir string, summary RunSummary) ([]webJob, error) {
	results := make(map[string]JobResult, len(summary.Results))
	for _, result := range summary.Results {
		results[result.ID] = result
	}
	commands, err := loadQueue(filepath.Join(runDir, "commands.json"))
	if err != nil {
		return nil, err
	}
	jobs := make([]webJob, 0, len(commands.Commands))
	for _, command := range commands.Commands {
		job := webJob{ID: command.ID, Name: command.Name, Command: command.Command, Executor: command.Executor, ExecutorOptions: command.ExecutorOptions, DependsOn: command.DependsOn, Origin: command.Origin, SubmittedAt: readJobTimestamp(runDir, command.ID, "submitted_at"), FinishedAt: readJobTimestamp(runDir, command.ID, "finished_at")}
		if result, ok := results[command.ID]; ok {
			job.Result = &result
		}
		jobs = append(jobs, job)
		delete(results, command.ID)
	}
	for _, result := range summary.Results {
		if _, exists := results[result.ID]; !exists {
			continue
		}
		resultCopy := result
		jobs = append(jobs, webJob{ID: result.ID, Command: result.Command, Result: &resultCopy, SubmittedAt: readJobTimestamp(runDir, result.ID, "submitted_at"), FinishedAt: readJobTimestamp(runDir, result.ID, "finished_at")})
	}
	return jobs, nil
}

func readJobTimestamp(runDir, jobID, name string) string {
	data, err := os.ReadFile(filepath.Join(runDir, jobID, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func buildWebTimeline(summary RunSummary, jobs []webJob) []webTimelinePoint {
	type event struct {
		at       string
		pending  int
		running  int
		finished int
		success  int
		failed   int
	}
	events := make([]event, 0, len(jobs)*2)
	for _, job := range jobs {
		if job.SubmittedAt != "" {
			events = append(events, event{at: job.SubmittedAt, pending: -1, running: 1})
		}
		if job.FinishedAt != "" {
			finished := event{at: job.FinishedAt, running: -1, finished: 1}
			if job.Result != nil && job.Result.ExitCode == 0 {
				finished.success = 1
			} else {
				finished.failed = 1
			}
			events = append(events, finished)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	points := []webTimelinePoint{{At: summary.StartedAt, Pending: len(jobs)}}
	pending, running, finished, success, failed := len(jobs), 0, 0, 0, 0
	for i := 0; i < len(events); {
		at := events[i].at
		event := event{at: at}
		for i < len(events) && events[i].at == at {
			event.pending += events[i].pending
			event.running += events[i].running
			event.finished += events[i].finished
			event.success += events[i].success
			event.failed += events[i].failed
			i++
		}
		pending += event.pending
		running += event.running
		finished += event.finished
		success += event.success
		failed += event.failed
		points = append(points, webTimelinePoint{At: event.at, Pending: pending, Running: running, Finished: finished, Success: success, Failed: failed})
	}
	return points
}

func validWebID(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func writeWebJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func writeWebError(writer http.ResponseWriter, err error) {
	http.Error(writer, err.Error(), http.StatusBadRequest)
}

func webHTML() string {
	executorJSON, _ := json.Marshal(executorNames())
	template := strings.Replace(webIndexHTML, "<script>\nlet state;", "<script>\nconst executorNames="+string(executorJSON)+";\nlet state;", 1)
	template = strings.Replace(template, "rotari copy", "rotari retry", -1)
	template = strings.Replace(template, "\\nrotari rerun'+basedir+' --queue-name '+shellQuote(queueName)+' --job-id JOB_ID", "", -1)
	template = strings.Replace(template, "rotari rerun", "rotari retry", -1)
	// "retry" already means --failed --unfinished, so drop the now-redundant flag.
	template = strings.Replace(template, " --failed", "", -1)
	template = strings.Replace(template, "rerun failed jobs", "retry failed or unfinished jobs", -1)
	return strings.Replace(template,
		`<select class="executor-input"><option value="local">local</option><option value="slurm">slurm</option></select>`,
		`<select class="executor-input">'+executorNames.map(name=>'<option value="'+esc(name)+'">'+esc(name)+'</option>').join('')+'</select>`, 1)
}

func methodNotAllowed(writer http.ResponseWriter) {
	writer.WriteHeader(http.StatusMethodNotAllowed)
}

const webIndexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>rotari</title><style>.runs tr.latest-run td{font-weight:600;background:rgba(184,217,242,.06)}.runs th:last-child,.runs td:last-child{width:1%;min-width:0;white-space:nowrap;text-align:left;padding-left:8px;padding-right:8px}
:root{color-scheme:dark;--bg:#10151b;--panel:#18212b;--line:#2d3a47;--text:#e8eef4;--muted:#94a3b3;--good:#63d297;--bad:#ff7c7c;--warn:#f3c969}.command-guide{white-space:pre-wrap;background:#0b1015;border:1px solid var(--line);padding:14px;color:#d7e2ea;margin:12px 0 18px;overflow:auto}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#10151b,#182733);color:var(--text);font:15px/1.5 ui-sans-serif,system-ui,sans-serif}main{max-width:1100px;margin:0 auto;padding:36px 22px}header{display:flex;justify-content:space-between;align-items:end;border-bottom:1px solid var(--line);padding-bottom:20px;margin-bottom:24px}h1{margin:0;font-size:32px;letter-spacing:.04em}h2{font-size:18px;margin:0 0 12px}.meta{color:var(--muted);font-size:13px}.toolbar{display:flex;gap:8px}button{border:1px solid var(--line);background:#202d39;color:var(--text);padding:8px 12px;border-radius:5px;cursor:pointer}button:hover{border-color:#7190a8}button:disabled{opacity:.45;cursor:not-allowed}input,select{border:1px solid var(--line);background:#101820;color:var(--text);padding:7px 8px;min-width:100px}.dirty{border-color:var(--warn);background:#3b331d;box-shadow:0 0 0 1px rgba(243,201,105,.25)}section{background:rgba(24,33,43,.9);border:1px solid var(--line);padding:18px;margin-bottom:20px}.summary{display:flex;gap:28px;color:var(--muted);font-size:14px}.runs{width:100%;border-collapse:collapse}.runs th,.runs td{text-align:left;border-bottom:1px solid var(--line);padding:10px 8px}.runs th{color:var(--muted);font-size:12px;text-transform:uppercase}.runs th:last-child,.runs td:last-child{white-space:nowrap;width:1%;vertical-align:top}.runs td.latest-run{font-weight:600;background:rgba(184,217,242,.06)}.latest-badge{color:#b8d9f2;font-size:11px;font-weight:400;letter-spacing:.04em;margin-left:6px}.status-finished{color:var(--good)}.status-failed{color:var(--bad)}.status-running{color:var(--warn)}.run-id{font-family:ui-monospace,monospace;color:#b8d9f2;cursor:pointer}.log{white-space:pre-wrap;background:#0b1015;border:1px solid var(--line);padding:14px;min-height:100px;max-height:360px;overflow:auto;color:#d7e2ea}.empty{color:var(--muted);padding:20px 0}.output-modal{position:fixed;inset:0;background:rgba(0,0,0,.72);display:flex;align-items:center;justify-content:center;padding:24px;z-index:10}.output-panel{width:min(1100px,96vw);height:min(760px,90vh);background:var(--panel);border:1px solid var(--line);padding:18px;box-shadow:0 12px 50px #000}.output-panel.compact{width:min(900px,92vw);height:auto}.output-panel header{margin:0 0 12px;padding:0 0 10px}.output-panel .log{height:calc(100% - 48px);max-height:none;margin:0}.output-panel.compact .log{height:auto;max-height:240px;min-height:0}@media(max-width:650px){header{display:block}.toolbar{margin-top:14px}.summary{flex-wrap:wrap;gap:10px}.runs th:nth-child(3),.runs td:nth-child(3){display:none}}
</style></head><body><main><header><div><h1>rotari</h1><div class="meta" id="location">loading...</div></div><div class="toolbar"><button onclick="refresh()">Refresh</button></div></header>
<section><h2 id="page-title">All queues</h2><div class="summary" id="summary"></div></section><div id="app" class="empty">loading...</div><div id="output-modal" class="output-modal" style="display:none" onclick="if(event.target===this)closeOutputModal()"><div class="output-panel" onclick="event.stopPropagation()"><header><strong>Output</strong><button onclick="closeOutputModal()">Close</button></header><pre id="modal-log" class="log"></pre></div></div></main><script>
let state;
const expandedRunGraphics={};
let selectedOutput='';
let selectedLog=null;
let followTimer=null;
let sortState={queue:{key:'name',direction:1},run:{key:'started',direction:-1},job:{key:'name',direction:1}};
async function refresh(){if(document.activeElement&&document.activeElement.closest('.web-queue-commands input,.web-queue-commands select'))return;const r=await fetch('/api/state');if(!r.ok){document.getElementById('app').textContent=await r.text();return}state=await r.json();render()}
function render(){const queues=state.queues||[];const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'){renderOverview(queues);return}const queue=queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(!queue){renderMissing('Queue not found');return}if(parts[2]==='run'){renderRun(queue,decodeURIComponent(parts[3]));return}renderQueue(queue)}
function renderOverview(queues){let queued=0,runs=0,running=0;queues.forEach(q=>{queued+=(q.queue.commands||[]).length;runs+=q.runs.length;running+=q.runs.filter(r=>r.running).length});document.getElementById('location').textContent=state.base_dir+' / all queues';document.getElementById('page-title').textContent='All queues';document.getElementById('summary').innerHTML='<span>'+queues.length+' queues</span><span>'+queued+' queued</span><span>'+runs+' runs</span><span>'+running+' running</span>';const rows=queues.map(q=>{let latest=null;for(const run of q.runs){if(!latest||run.started_at>latest.started_at)latest=run}return '<tr><td><a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'">'+esc(q.queue_name)+'</a></td><td>'+(q.queue.commands||[]).length+'</td><td>'+q.runs.length+'</td><td>'+q.runs.filter(r=>r.running).length+'</td><td>'+(latest?'<a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'/run/'+encodeURIComponent(latest.run_id)+'">'+esc(latest.run_name||latest.run_id)+'</a>':'-')+'</td><td class="status-'+(latest?latest.status:'')+'">'+esc(latest?latest.status:'-')+'</td><td>'+esc(latest?latest.started_at:'-')+'</td></tr>'}).join('');document.getElementById('app').innerHTML=queues.length?'<table class="runs queue-overview"><thead><tr><th data-sort="name">Queue</th><th data-sort="queued">Queued</th><th data-sort="runs">Runs</th><th data-sort="running">Running</th><th>Latest run</th><th data-sort="status">Status</th><th data-sort="started">Started</th></tr></thead><tbody>'+rows+'</tbody></table>':'No queues found.'}
function renderQueue(q){document.getElementById('location').textContent=state.base_dir+' / '+q.queue_name;document.getElementById('page-title').textContent=q.queue_name;document.getElementById('summary').innerHTML='<span>'+(q.queue.commands||[]).length+' queued</span><span>'+q.runs.length+' runs</span><span>'+q.runs.filter(r=>r.running).length+' running</span>';const rows=q.runs.map(r=>'<tr><td><a class="link run-id" href="/queue/'+encodeURIComponent(q.queue_name)+'/run/'+encodeURIComponent(r.run_id)+'">'+esc(r.run_id)+'</a></td><td class="status-'+r.status+'">'+esc(r.status)+(r.running?' ...':'')+'</td><td>'+(r.finished_at?esc(r.exit_code):'-')+'</td><td>'+esc(r.started_at||'-')+'</td><td>'+esc(r.finished_at||'-')+'</td></tr>').join('');document.getElementById('app').innerHTML='<div class="toolbar"><a class="link" href="/">All queues</a></div>'+(rows?'<table class="runs"><thead><tr><th data-sort="run">Run</th><th data-sort="status">Status</th><th data-sort="exit">Exit</th><th data-sort="started">Started</th><th data-sort="finished">Finished</th></tr></thead><tbody>'+rows+'</tbody></table>':'<div class="empty">No runs found.</div>')}
function renderRun(q,runID){const run=q.runs.find(r=>r.run_id===runID);if(!run){renderMissing('Run not found');return}document.getElementById('location').textContent=state.base_dir+' / '+q.queue_name+' / '+runID;document.getElementById('page-title').textContent=run.run_name||runID;document.getElementById('summary').innerHTML='<span>Queue: '+esc(q.queue_name)+'</span><span>Run ID: '+esc(run.run_id)+'</span><span>Status: '+esc(run.status)+'</span><span>Exit: '+(run.finished_at?esc(run.exit_code):'-')+'</span>';const jobs=(run.jobs||[]).map(j=>{const result=j.result;const options=(j.executor_options||[]).join(' ');const dependencies=(j.depends_on||[]).join(', ');const exit=result?esc(result.exit_code):'-';const error=result&&result.error?'<div class="error">'+esc(result.error)+'</div>':'';const logRun=j.origin?j.origin.run_id:runID;const logJob=j.origin?j.origin.job_id:j.id;const output=result?'<button onclick="log(\''+esc(q.queue_name)+'\',\''+esc(logRun)+'\',\''+esc(logJob)+'\')">Output</button>':'-';const jobName=esc(j.name||'-')+(j.origin?'<div class="meta">carried from '+esc(j.origin.run_id)+'</div>':'');return '<tr><td><strong>'+jobName+'</strong><div class="meta">'+esc(j.id)+'</div></td><td>'+esc(j.executor||'default')+'</td><td>'+esc(options||'-')+'</td><td>'+esc(dependencies||'-')+'</td><td class="command">'+esc((j.command||[]).join(' '))+'</td><td>'+exit+error+'</td><td>'+output+'</td></tr>'}).join('');const cwd=run.cwd||'-';const copy='cd '+shellQuote(cwd)+' && rotari copy --queue-name '+shellQuote(q.queue_name)+' --run-id '+shellQuote(runID)+' --failed';document.getElementById('app').innerHTML='<div class="toolbar"><a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'">Back to '+esc(q.queue_name)+'</a></div><p class="meta">Started: '+esc(run.started_at||'-')+' | Finished: '+esc(run.finished_at||'-')+' | Jobs: '+(run.jobs||[]).length+'</p><p>Working directory: <code>'+esc(cwd)+'</code></p><pre class="log">Retry from a terminal:\n'+esc(copy)+'</pre>'+(jobs?'<table class="runs"><thead><tr><th>Job name / ID</th><th>Executor</th><th>Executor options</th><th>Dependencies</th><th>Command</th><th>Exit / error</th><th></th></tr></thead><tbody>'+jobs+'</tbody></table>':'<div class="empty">No job definitions yet.</div>')+'<pre id="log" class="log">Select a job output.</pre>'}
function renderMissing(message){document.getElementById('page-title').textContent='Not found';document.getElementById('summary').textContent='';document.getElementById('app').innerHTML='<a class="link" href="/">All queues</a><p>'+esc(message)+'</p>'}
function enhancePage(){document.querySelectorAll('.web-copy-controls,.web-queue-commands,.web-origin').forEach(e=>e.remove());const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue')return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(!queue)return;if(parts[2]==='run'){const runID=decodeURIComponent(parts[3]);const controls=document.createElement('div');controls.className='toolbar web-copy-controls';controls.innerHTML='<button onclick="copyRun(\''+esc(queue.queue_name)+'\',\''+esc(runID)+'\',\'failed\')">Copy failed jobs</button><button onclick="copyRun(\''+esc(queue.queue_name)+'\',\''+esc(runID)+'\',\'all\')">Copy all jobs</button>';document.getElementById('app').prepend(controls);return}const commands=queue.queue.commands||[];const section=document.createElement('section');section.className='web-queue-commands';section.innerHTML='<h2>Current queue</h2>'+(commands.length?'<table class="runs"><thead><tr><th>Job name / ID</th><th>Status</th><th>Executor</th><th>Executor options</th><th>Dependencies</th><th>Command</th></tr></thead><tbody>'+commands.map(j=>'<tr><td><strong>'+esc(j.name||'-')+'</strong><div class="meta">'+esc(j.id)+'</div></td><td>pending</td><td>'+esc(j.executor||'default')+'</td><td>'+esc((j.executor_options||[]).join(' ')||'-')+'</td><td>'+esc((j.depends_on||[]).join(', ')||'-')+'</td><td class="command">'+esc((j.command||[]).join(' '))+'</td></tr>').join('')+'</tbody></table>':'<div class="empty">Queue is empty.</div>');document.getElementById('app').prepend(section);fixQueueSourceColumns(commands)}
async function copyRun(queue,run,selection){const q=state.queues.find(item=>item.queue_name===queue);const existing=(q&&q.queue.commands||[]).length;if(existing&&!confirm('This will replace '+existing+' queued jobs. Continue?'))return;const response=await fetch('/api/copy',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,run_id:run,selection:selection,overwrite:existing>0})});const text=await response.text();if(!response.ok){alert(text);return}alert(JSON.parse(text).message);window.location.href='/queue/'+encodeURIComponent(queue)}
function fixQueueSourceColumns(commands){const table=document.querySelector('.web-queue-commands table');if(!table)return;const sourceRunHeader=document.createElement('th');sourceRunHeader.textContent='Source run';const sourceStatusHeader=document.createElement('th');sourceStatusHeader.textContent='Source status';const sourceOutputHeader=document.createElement('th');sourceOutputHeader.textContent='Source output';table.querySelector('thead tr').append(sourceRunHeader,sourceStatusHeader,sourceOutputHeader);const rows=table.querySelectorAll('tbody tr');commands.forEach((job,index)=>{if(!rows[index])return;const sourceRun=document.createElement('td');const sourceStatus=document.createElement('td');const sourceOutput=document.createElement('td');if(job.origin){sourceRun.textContent=job.origin.run_id+'/'+job.origin.job_id;sourceStatus.textContent=job.origin.status;sourceOutput.innerHTML='<button onclick="showOriginalOutput(\''+esc(job.origin.run_id)+'\',\''+esc(job.origin.job_id)+'\',this)">Output</button>'}else{sourceRun.textContent='-';sourceStatus.textContent='-';sourceOutput.textContent='-'}rows[index].append(sourceRun,sourceStatus,sourceOutput)})}
function enhanceQueueSourceContext(commands){const section=document.querySelector('.web-queue-commands');if(!section)return;const origins=commands.filter(job=>job.origin);if(!origins.length)return;const runs=[...new Set(origins.map(job=>job.origin.run_id))];const cwds=[...new Set(origins.map(job=>job.origin.cwd).filter(Boolean))];const context=document.createElement('p');context.className='meta';context.textContent='Source run: '+runs.join(', ')+' | Source working directory: '+(cwds.join(', ')||'-');section.prepend(context)}
function addExecutionGuide(){document.querySelectorAll('.execution-guide').forEach(element=>element.remove());const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue')return;const queueName=decodeURIComponent(parts[1]);const queue=state.queues.find(item=>item.queue_name===queueName);if(!queue)return;const guide=document.createElement('pre');guide.className='command-guide execution-guide';const basedir=state&&state.base_dir?(' --basedir '+shellQuote(state.base_dir)):' ';if(parts[2]==='run'){const runID=decodeURIComponent(parts[3]);const run=queue.runs.find(item=>item.run_id===runID);if(!run)return;const latest=queue.runs.reduce((current,item)=>!current||item.started_at>current.started_at?item:current,null);const prefix=run.cwd&&run.cwd!=='-'?'cd '+shellQuote(run.cwd)+'\n':'';if(latest&&latest.run_id===runID){guide.textContent='# To rerun failed jobs from the latest run\n'+prefix+'rotari rerun'+basedir+' --queue-name '+shellQuote(queueName)+' --failed'}else{guide.textContent='# To rerun failed jobs from this older run\n'+prefix+'rotari copy'+basedir+' --queue-name '+shellQuote(queueName)+' --run-id '+shellQuote(runID)+' --failed\nrotari rerun'+basedir+' --queue-name '+shellQuote(queueName)+' --job-id JOB_ID'}const workingDirectory=[...document.querySelectorAll('#app p')].find(element=>element.textContent.startsWith('Working directory:'));if(workingDirectory)workingDirectory.after(guide);else document.getElementById('app').prepend(guide);return}const origins=(queue.queue.commands||[]).map(job=>job.origin).filter(Boolean);const directories=[...new Set(origins.map(origin=>origin.cwd).filter(Boolean))];const prefix=directories.length===1?'cd '+shellQuote(directories[0])+'\n':'';guide.textContent='# To execute jobs in current queue\n'+prefix+'rotari run'+basedir+' --queue-name '+shellQuote(queueName);const section=document.querySelector('.web-queue-commands');if(section)section.append(guide)}
function addPathButton(cell,path){const button=document.createElement('button');button.textContent='Path';button.onclick=()=>showPath(path,button);cell.append(' ',button)}
function addDeleteRunButton(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const controls=document.querySelector('.web-copy-controls');if(!controls)return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const button=document.createElement('button');button.textContent='Delete';button.disabled=!!(run&&run.running);button.title=button.disabled?'Running runs cannot be deleted':'Delete run history';button.onclick=()=>deleteRun(decodeURIComponent(parts[1]),decodeURIComponent(parts[3]));controls.append(button)}
async function deleteRun(queue,run){if(!confirm('Delete run history '+run+'?'))return;const response=await fetch('/api/clear-run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,run_id:run})});const text=await response.text();if(!response.ok){alert(text);return}window.location.href='/queue/'+encodeURIComponent(queue)}
function isCompactOutput(value){return value.length<1200&&value.split('\n').length<=18}
function showPath(path){selectedOutput=path;const output=ensureModalOutput();output.textContent=path;openOutputModal(true)}
function addPathTableActions(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue')return;const queueName=decodeURIComponent(parts[1]);const queue=state.queues.find(item=>item.queue_name===queueName);if(!queue)return;if(parts[2]==='run'){const runID=decodeURIComponent(parts[3]);const run=queue.runs.find(item=>item.run_id===runID);const table=document.querySelector('#app table.runs');if(!run||!table)return;const header=document.createElement('th');header.textContent='Actions';table.querySelector('thead tr').append(header);table.querySelectorAll('tbody tr').forEach((row,index)=>{const cell=document.createElement('td');const job=run.jobs[index];if(job)addPathButton(cell,state.base_dir+'/queues/'+queueName+'/runs/'+runID+'/'+job.id);row.append(cell)})}else{const runTable=[...document.querySelectorAll('#app table.runs')].find(table=>!table.closest('.web-queue-commands'));if(runTable){const header=document.createElement('th');header.textContent='Actions';runTable.querySelector('thead tr').append(header);runTable.querySelectorAll('tbody tr').forEach((row,index)=>{const run=queue.runs[index];const cell=document.createElement('td');if(run){addPathButton(cell,state.base_dir+'/queues/'+queueName+'/runs/'+run.run_id);const deleteButton=document.createElement('button');deleteButton.textContent='Delete';deleteButton.onclick=()=>deleteRun(queueName,run.run_id);cell.append(' ',deleteButton)}row.append(cell)})}}}
function updateDirtyField(field){field.classList.toggle('dirty',field.value!==field.dataset.initial);updateRowSaveState(field.closest('tr'))}
function updateRowSaveState(row){if(!row)return;const save=row.querySelector('.save-job');if(!save)return;save.disabled=[...row.querySelectorAll('input,select')].every(field=>field.value===field.dataset.initial)}
function addQueueEditors(queue,commands){const table=document.querySelector('.web-queue-commands table');if(!table)return;const actionHeader=document.createElement('th');actionHeader.textContent='Actions';table.querySelector('thead tr').append(actionHeader);table.querySelectorAll('tbody tr').forEach((row,index)=>{const job=commands[index];if(!job)return;row.children[0].innerHTML='<input class="job-name-input" value="'+esc(job.name||'')+'"><div class="meta">'+esc(job.id)+'</div>';row.children[2].innerHTML='<select class="executor-input"><option value="local">local</option><option value="slurm">slurm</option></select>';row.children[2].querySelector('select').value=job.executor||'local';row.children[3].innerHTML='<input class="executor-option-input" value="'+esc(JSON.stringify(job.executor_options||[]))+'">';row.children[4].innerHTML='<input class="depends-input" value="'+esc(JSON.stringify(job.depends_on||[]))+'">';row.children[5].innerHTML='<input class="command-input" value="'+esc(JSON.stringify(job.command))+'">';row.querySelectorAll('input,select').forEach(field=>{field.dataset.initial=field.value;field.addEventListener('input',()=>updateDirtyField(field));field.addEventListener('change',()=>updateDirtyField(field))});const save=document.createElement('button');save.className='save-job';save.textContent='Save';save.disabled=true;save.onclick=()=>saveQueueJob(queue.queue_name,job.id,row);const remove=document.createElement('button');remove.textContent='Remove';remove.onclick=()=>removeQueueJob(queue.queue_name,job.id,job.name||job.id);const actions=document.createElement('td');actions.append(save,' ',remove);row.append(actions)})}
async function saveQueueJob(queue,jobID,row){const parse=(selector,label)=>{try{const value=JSON.parse(row.querySelector(selector).value);if(!Array.isArray(value))throw new Error(label+' must be an array');return value}catch(error){throw new Error(label+': '+error.message)}};let command,options,depends;try{command=parse('.command-input','command');options=parse('.executor-option-input','Executor options');depends=parse('.depends-input','dependencies');if(!command.length)throw new Error('command must not be empty')}catch(error){alert(error.message);return}const response=await fetch('/api/change',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,job_id:jobID,set_job_name:row.querySelector('.job-name-input').value,command:command,executor:row.querySelector('.executor-input').value,executor_options:options,clear_executor_options:options.length===0,depends_on:depends,clear_depends_on:depends.length===0})});const text=await response.text();if(!response.ok){alert(text);return}await refresh()}
async function removeQueueJob(queue,jobID,label){if(!confirm('Remove '+label+' from the queue?'))return;const response=await fetch('/api/remove',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,job_id:jobID})});const text=await response.text();if(!response.ok){alert(text);return}await refresh()}
async function loadLogChunk(queue,run,job,before){const response=await fetch('/api/log?queue_name='+encodeURIComponent(queue)+'&run_id='+encodeURIComponent(run)+'&job_id='+encodeURIComponent(job)+'&tail=200&before='+before);return response.text()}
function attachLogLoader(output){output.onscroll=async()=>{if(output.scrollTop>20||!selectedLog||selectedLog.loading||selectedLog.done)return;if(followTimer)clearInterval(followTimer);followTimer=null;selectedLog.loading=true;const previousHeight=output.scrollHeight;const chunk=await loadLogChunk(selectedLog.queue,selectedLog.run,selectedLog.job,selectedLog.before+200);if(!chunk){selectedLog.done=true}else{selectedLog.before+=200;output.textContent=chunk+selectedOutput;selectedOutput=output.textContent;openOutputModal(isCompactOutput(selectedOutput));output.scrollTop=output.scrollHeight-previousHeight}selectedLog.loading=false}}
async function showOriginalOutput(run,job,trigger){const parts=location.pathname.split('/').filter(Boolean);await showLog(decodeURIComponent(parts[1]),run,job,trigger||window.event?.currentTarget)}
async function showLog(queue,run,job){if(followTimer)clearInterval(followTimer);selectedLog={queue:queue,run:run,job:job,before:0,loading:false,done:false};selectedOutput=await loadLogChunk(queue,run,job,0);const output=ensureModalOutput();output.textContent=selectedOutput;openOutputModal(isCompactOutput(selectedOutput));output.scrollTop=output.scrollHeight;attachLogLoader(output);followTimer=setInterval(followOutput,2000)}
async function followOutput(){if(!selectedLog||selectedLog.before>0||selectedLog.loading)return;const latest=await loadLogChunk(selectedLog.queue,selectedLog.run,selectedLog.job,0);if(latest&&latest!==selectedOutput){selectedOutput=latest;const output=ensureModalOutput();output.textContent=latest;output.scrollTop=output.scrollHeight;openOutputModal(isCompactOutput(latest))}}
async function log(queue,run,job,trigger){await showLog(queue,run,job,trigger||window.event?.currentTarget)}
function shellQuote(v){return "'"+String(v||'').replace(/'/g,"'\\''")+"'"}
function keepGlobalOutputBox(){}
function removeLegacyOutputBox(){document.querySelectorAll('#app pre.log:not(.row-log)').forEach(element=>element.remove())}
function ensureOutputBox(){let output=document.getElementById('log');if(!output){output=document.createElement('pre');output.id='log';output.className='log';const main=document.querySelector('main');const section=document.getElementById('page-title')?.parentElement;if(main&&section)main.insertBefore(output,section)}return output}
function ensureModalOutput(){return document.getElementById('modal-log')||ensureOutputBox()}
function openOutputModal(compact){const modal=document.getElementById('output-modal');modal.style.display='flex';modal.querySelector('.output-panel').classList.toggle('compact',!!compact)}
function closeOutputModal(){document.getElementById('output-modal').style.display='none';if(followTimer)clearInterval(followTimer);followTimer=null;selectedLog=null;selectedOutput=''}
function restoreSelectedOutput(){if(selectedLog&&selectedOutput){const output=ensureModalOutput();output.textContent=selectedOutput;openOutputModal(isCompactOutput(selectedOutput));attachLogLoader(output)}}
function placeOutputBox(){}
function renameCopyButtons(){document.querySelectorAll('.web-copy-controls button').forEach(button=>{if(button.textContent==='Copy failed jobs')button.textContent='Create queue from failed jobs';if(button.textContent==='Copy all jobs')button.textContent='Create queue from all jobs'})}
function labelEquivalentCommand(){document.querySelectorAll('pre.log').forEach(pre=>{pre.textContent=pre.textContent.replace('Retry from a terminal:','Equivalent command:')})}
function applyStatusColors(){const colors={pending:'#f3c969',unfinished:'#f3c969',running:'#f3c969',success:'#63d297',finished:'#63d297',failed:'#ff7c7c',blocked:'#ff9f68'};document.querySelectorAll('td,span').forEach(element=>{const value=element.textContent.trim().toLowerCase();if(colors[value])element.style.color=colors[value]})}
function sortTable(table,key,stateKey){const headers=[...table.querySelectorAll('thead th')];const index=headers.findIndex(header=>header.dataset.sort===key);if(index<0)return;const state=sortState[stateKey];const rows=[...table.querySelectorAll('tbody tr')];rows.sort((left,right)=>{const a=left.children[index].textContent.trim(),b=right.children[index].textContent.trim();const na=Number(a),nb=Number(b);if(a==='-')return 1;if(b==='-')return -1;if(Number.isFinite(na)&&Number.isFinite(nb))return (na-nb)*state.direction;return a.localeCompare(b,undefined,{numeric:true})*state.direction});const body=table.querySelector('tbody');rows.forEach(row=>body.append(row));headers.forEach(header=>{if(!header.dataset.sort)return;header.style.cursor='pointer';const label=header.dataset.label||header.textContent.trim();header.dataset.label=label;header.textContent=label+(header.dataset.sort===state.key?(state.direction===1?' ↑':' ↓'):' ↕');header.onclick=()=>{if(state.key===header.dataset.sort)state.direction*=-1;else{state.key=header.dataset.sort;state.direction=1}sortTable(table,state.key,stateKey)}})}
function markJobHeaders(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const table=document.querySelector('#app table.runs');if(!table)return;const keys=['name','status','executor','slurm','depends','command','exit'];table.querySelectorAll('thead th').forEach((header,index)=>{if(index<keys.length)header.dataset.sort=keys[index]})}
function enableTableSorting(){const queueTable=document.querySelector('.queue-overview');if(queueTable)sortTable(queueTable,sortState.queue.key,'queue');const parts=location.pathname.split('/').filter(Boolean);const runTable=document.querySelector('#app table.runs');if(runTable&&!parts[2])sortTable(runTable,sortState.run.key,'run');if(runTable&&parts[2]==='run')sortTable(runTable,sortState.job.key,'job')}
function enhanceQueueOverview(){const parts=location.pathname.split('/').filter(Boolean);if(parts.length)return;const queues=state.queues||[];document.querySelectorAll('#app section').forEach((section,index)=>{const queue=queues[index];if(!queue)return;let latest=null;for(const run of queue.runs){if(!latest||run.started_at>latest.started_at)latest=run}const latestHTML=latest?'<div class="meta">Latest run: <a class="link" href="/queue/'+encodeURIComponent(queue.queue_name)+'/run/'+encodeURIComponent(latest.run_id)+'">'+esc(latest.run_name||latest.run_id)+'</a></div><div class="summary"><span class="status-'+esc(latest.status)+'">'+esc(latest.status)+'</span><span>Started: '+esc(latest.started_at||'-')+'</span><span>Finished: '+esc(latest.finished_at||'-')+'</span></div>':'<div class="meta">No runs yet</div>';section.innerHTML='<h2><a class="link" href="/queue/'+encodeURIComponent(queue.queue_name)+'">'+esc(queue.queue_name)+'</a></h2><div class="summary"><span>'+(queue.queue.commands||[]).length+' queued</span><span>'+queue.runs.length+' runs</span><span>'+queue.runs.filter(r=>r.running).length+' running</span></div>'+latestHTML})}
function markLatestRun(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2])return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));const table=document.querySelector('#app table.runs');if(!queue||!table)return;table.querySelectorAll('tbody tr').forEach(row=>{row.classList.remove('latest-run');const badge=row.querySelector('.latest-badge');if(badge)badge.remove()});let latest=null;for(const run of queue.runs){if(!latest||run.started_at>latest.started_at)latest=run}if(!latest)return;table.querySelectorAll('tbody tr').forEach(row=>{const link=row.querySelector('a.run-id');if(link&&decodeURIComponent(link.getAttribute('href')).endsWith('/run/'+latest.run_id)){row.classList.add('latest-run');row.children[0].insertAdjacentHTML('beforeend','<span class="latest-badge">latest</span>')}})}
function addQueueOverviewPathActions(){if(location.pathname!=='/'&&location.pathname!=='')return;const table=document.querySelector('.queue-overview');if(!table)return;const header=document.createElement('th');header.textContent='Actions';table.querySelector('thead tr').append(header);const queues=state.queues||[];table.querySelectorAll('tbody tr').forEach((row,index)=>{const queue=queues[index];const cell=document.createElement('td');if(queue)addPathButton(cell,state.base_dir+'/queues/'+queue.queue_name);row.append(cell)})}
function addRunJobStatusColumn(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const table=document.querySelector('#app table.runs');if(!run||!table||table.querySelector('.job-status-header'))return;const header=document.createElement('th');header.className='job-status-header';header.dataset.sort='status';header.textContent='Status';table.querySelector('thead tr').insertBefore(header,table.querySelector('thead tr').children[1]);const rows=table.querySelectorAll('tbody tr');(run.jobs||[]).forEach((job,index)=>{if(!rows[index])return;const status=document.createElement('td');const result=job.result;if(!result)status.textContent=run.running?'running':'pending';else if(result.error==='blocked by failed dependency')status.textContent='blocked';else status.textContent=result.exit_code===0?'success':'failed';rows[index].insertBefore(status,rows[index].children[1])})}
function addRunningOutputButtons(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const table=document.querySelector('#app table.runs');if(!run||!run.running||!table)return;table.querySelectorAll('tbody tr').forEach((row,index)=>{const cell=row.children[row.children.length-2];if(cell&&cell.textContent.trim()==='-'){const job=run.jobs[index];if(job){const button=document.createElement('button');button.textContent='Output';button.onclick=()=>showLog(queue.queue_name,run.run_id,job.id,button);cell.textContent='';cell.append(button)}}})}
function addRunningCancelButtons(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const table=document.querySelector('#app table.runs');if(!run||!run.running||!table)return;table.querySelectorAll('tbody tr').forEach((row,index)=>{const job=run.jobs[index];const actions=row.lastElementChild;if(!job||!actions||actions.querySelector('.cancel-job'))return;const button=document.createElement('button');button.className='cancel-job';button.textContent='Cancel';button.onclick=()=>cancelJob(queue.queue_name,job.id,job.name||job.id);actions.append(' ',button)})}
async function cancelJob(queue,jobID,label){if(!confirm('Cancel '+label+'?'))return;const response=await fetch('/api/cancel-job',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,job_id:jobID})});const text=await response.text();if(!response.ok){alert(text);return}await refresh()}
function mergeActionColumns(){document.querySelectorAll('#app table.runs').forEach(table=>{const headerRow=table.querySelector('thead tr');const bodyRows=table.querySelectorAll('tbody tr');if(!headerRow||!bodyRows.length)return;const headers=headerRow.children;if(headers.length<2||headers[headers.length-1].textContent.trim()!=='Actions')return;const actionIndex=headers.length-1;const outputIndex=actionIndex-1;const outputLabel=headers[outputIndex].textContent.trim();if(outputLabel!=='Output'&&outputLabel!=='Source output')return;headers[outputIndex].remove();bodyRows.forEach(row=>{const outputCell=row.children[outputIndex];const actionCell=row.children[actionIndex];if(outputCell&&actionCell){const nodes=[...outputCell.childNodes,...actionCell.childNodes].filter(node=>node.nodeType!==3||node.textContent.trim());actionCell.textContent='';nodes.forEach(node=>actionCell.append(node));outputCell.remove()}})})}
function normalizeJobActionHeaders(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const table=document.querySelector('#app table.runs');if(!table)return;const headers=table.querySelectorAll('thead th');if(headers.length>=2)headers[headers.length-2].textContent='Output'}
function mergeActionColumns(){document.querySelectorAll('#app table.runs').forEach(table=>{const headerRow=table.querySelector('thead tr');const bodyRows=table.querySelectorAll('tbody tr');if(!headerRow||!bodyRows.length)return;const headers=headerRow.children;if(headers.length<2||headers[headers.length-1].textContent.trim()!=='Actions')return;const actionIndex=headers.length-1;const outputIndex=actionIndex-1;const label=headers[outputIndex].textContent.trim();if(label&&label!=='Output'&&label!=='Source output')return;headers[outputIndex].remove();bodyRows.forEach(row=>{const outputCell=row.children[outputIndex],actionCell=row.children[actionIndex];if(!outputCell||!actionCell)return;const nodes=[...outputCell.childNodes,...actionCell.childNodes].filter(node=>node.nodeType!==3||node.textContent.trim());actionCell.textContent='';nodes.forEach(node=>actionCell.append(node));outputCell.remove()})})}
function labelJobActionHeaders(){document.querySelectorAll('#app table.runs').forEach(table=>{const headers=table.querySelectorAll('thead th');if(headers.length){headers[headers.length-1].textContent='Actions';headers[headers.length-1].dataset.sort=''}})}
function styleActionColumns(){moveActionColumnsLeft();document.querySelectorAll('#app table.runs th:first-child,#app table.runs td:first-child').forEach(cell=>{if(cell.textContent.trim()==='Actions'||cell.querySelector('button')){cell.style.width='1%';cell.style.minWidth='0';cell.style.whiteSpace='nowrap';cell.style.textAlign='left'}})}
function addRunHeatmap(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.run-heatmap'))return;const section=document.createElement('section');section.className='run-heatmap';section.style.background='linear-gradient(135deg,rgba(30,48,58,.95),rgba(24,33,43,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.justifyContent='space-between';heading.style.alignItems='baseline';heading.style.gap='12px';const title=document.createElement('h2');title.textContent='Run heatmap';title.style.margin='0';const note=document.createElement('span');note.textContent='Click a tile to inspect the job';note.style.color='var(--muted)';note.style.fontSize='12px';heading.append(title,note);section.append(heading);const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='10px';legend.style.margin='10px 0 14px';const grid=document.createElement('div');grid.style.display='grid';grid.style.gridTemplateColumns='repeat(auto-fit,minmax(120px,1fr))';grid.style.gap='8px';const colors={success:['#1d6b52','#b4f0c8'],failed:['#8f3b47','#ffd2d2'],blocked:['#87502d','#ffe1b0'],running:['#80651e','#fff0ae'],pending:['#3a4a57','#cbd9e4']};['success','failed','blocked','running','pending'].forEach(status=>{const item=document.createElement('span');item.style.color=colors[status][1];item.style.fontSize='12px';const swatch=document.createElement('i');swatch.style.display='inline-block';swatch.style.width='10px';swatch.style.height='10px';swatch.style.marginRight='5px';swatch.style.background=colors[status][0];swatch.style.border='1px solid '+colors[status][1];item.append(swatch,status);legend.append(item)});section.append(legend,grid);(run.jobs||[]).forEach((job,index)=>{const result=job.result;const status=!result?(run.running?'running':'pending'):result.error==='blocked by failed dependency'?'blocked':result.exit_code===0?'success':'failed';const tile=document.createElement('button');tile.type='button';tile.title=(job.name||job.id)+' - '+status;tile.style.display='flex';tile.style.flexDirection='column';tile.style.alignItems='flex-start';tile.style.gap='2px';tile.style.minHeight='68px';tile.style.padding='10px';tile.style.border='1px solid '+colors[status][1];tile.style.borderRadius='4px';tile.style.background=colors[status][0];tile.style.color=colors[status][1];tile.style.textAlign='left';tile.style.overflow='hidden';const name=document.createElement('strong');name.textContent=job.name||job.id;name.style.maxWidth='100%';name.style.overflow='hidden';name.style.textOverflow='ellipsis';name.style.whiteSpace='nowrap';const detail=document.createElement('span');detail.textContent=status+(result&&result.exit_code!==undefined?' / exit '+result.exit_code:'');detail.style.fontSize='12px';detail.style.opacity='.9';tile.append(name,detail);tile.onclick=()=>{const row=document.querySelectorAll('#app table.runs tbody tr')[index];if(row){row.scrollIntoView({behavior:'smooth',block:'center'});row.style.outline='2px solid '+colors[status][1];setTimeout(()=>row.style.outline='',1200)}};grid.append(tile)});const table=app.querySelector('table.runs');if(table)app.insertBefore(section,table);else app.prepend(section)}
function addRunStatistics(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.run-statistics'))return;const counts={success:0,failed:0,blocked:0,running:0,pending:0};(run.jobs||[]).forEach(job=>{const result=job.result;const status=!result?(run.running?'running':'pending'):result.error==='blocked by failed dependency'?'blocked':result.exit_code===0?'success':'failed';counts[status]++});const total=(run.jobs||[]).length;const completed=counts.success+counts.failed;const successRate=completed?Math.round(counts.success/completed*100):0;const colors={success:['#1d6b52','#b4f0c8'],failed:['#8f3b47','#ffd2d2'],blocked:['#87502d','#ffe1b0'],running:['#80651e','#fff0ae'],pending:['#3a4a57','#cbd9e4']};const section=document.createElement('section');section.className='run-statistics';section.style.background='linear-gradient(135deg,rgba(30,48,58,.95),rgba(24,33,43,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.justifyContent='space-between';heading.style.alignItems='baseline';heading.style.gap='12px';const title=document.createElement('h2');title.textContent='Run statistics';title.style.margin='0';const note=document.createElement('span');note.textContent=total+' jobs';note.style.color='var(--muted)';note.style.fontSize='12px';heading.append(title,note);const metrics=document.createElement('div');metrics.style.display='grid';metrics.style.gridTemplateColumns='repeat(auto-fit,minmax(140px,1fr))';metrics.style.gap='12px';metrics.style.margin='16px 0';[['Success rate',successRate+'%'],['Succeeded',counts.success],['Failed',counts.failed],['In progress',counts.running],['Pending',counts.pending]].forEach(([label,value])=>{const metric=document.createElement('div');metric.style.borderLeft='3px solid #385160';metric.style.paddingLeft='10px';const valueElement=document.createElement('strong');valueElement.textContent=value;valueElement.style.display='block';valueElement.style.fontSize='22px';const labelElement=document.createElement('span');labelElement.textContent=label;labelElement.style.color='var(--muted)';labelElement.style.fontSize='12px';metric.append(valueElement,labelElement);metrics.append(metric)});const bar=document.createElement('div');bar.style.display='flex';bar.style.height='14px';bar.style.overflow='hidden';bar.style.borderRadius='3px';bar.title='Job status distribution';['success','failed','blocked','running','pending'].forEach(status=>{if(!counts[status])return;const segment=document.createElement('span');segment.style.width=(counts[status]/Math.max(total,1)*100)+'%';segment.style.background=colors[status][0];segment.title=status+': '+counts[status];bar.append(segment)});const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='12px';legend.style.marginTop='10px';['success','failed','blocked','running','pending'].forEach(status=>{if(!counts[status])return;const item=document.createElement('span');item.textContent=status+' '+counts[status];item.style.color=colors[status][1];item.style.fontSize='12px';legend.append(item)});section.append(heading,metrics,bar,legend);const table=app.querySelector('table.runs');if(table)app.insertBefore(section,table);else app.prepend(section)}
function addRunEnvironment(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.run-environment'))return;const context=run.context||{};const load=value=>value?Number(value.one).toFixed(2)+' / '+Number(value.five).toFixed(2)+' / '+Number(value.fifteen).toFixed(2):'-';const section=document.createElement('section');section.className='run-environment';section.style.background='linear-gradient(135deg,rgba(25,45,49,.95),rgba(24,33,43,.92))';section.style.border='1px solid #3d5f62';section.style.padding='18px';section.style.margin='16px 0 20px';section.innerHTML='<div style="display:flex;justify-content:space-between;align-items:baseline;gap:12px"><h2 style="margin:0">Run environment</h2><span class="meta">load average: 1 / 5 / 15 min</span></div><div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin-top:14px"><div style="border:1px solid var(--line);background:rgba(10,16,21,.45);padding:12px"><div class="meta">Host</div><strong>'+esc(context.hostname||'-')+'</strong></div><div style="border:1px solid var(--line);background:rgba(10,16,21,.45);padding:12px"><div class="meta">Start load</div><strong>'+esc(load(context.started_load))+'</strong></div><div style="border:1px solid var(--line);background:rgba(10,16,21,.45);padding:12px"><div class="meta">Finish load</div><strong>'+esc(load(context.finished_load))+'</strong></div></div>';const stats=app.querySelector('.run-statistics');if(stats)stats.after(section);else app.prepend(section)}
function addJobTimeline(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.job-timeline'))return;const points=(run.timeline||[]).filter(point=>point.at);if(points.length<2)return;const times=points.map(point=>Date.parse(point.at)).filter(Number.isFinite);if(!times.length)return;const start=Math.min(...times),end=Math.max(...times),span=Math.max(1,end-start);const max=Math.max(1,...points.flatMap(point=>[point.pending||0,point.running||0,point.success||0,point.failed||0]));const x=point=>40+(Date.parse(point.at)-start)/span*500;const y=value=>170-(value/max)*130;const line=key=>points.map(point=>x(point).toFixed(1)+','+y(point[key]||0).toFixed(1)).join(' ');const label=value=>new Date(value).toLocaleTimeString();const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';section.innerHTML='<div style="display:flex;justify-content:space-between;align-items:baseline;gap:12px"><h2 style="margin:0">Job timeline</h2><span class="meta">'+esc(label(start))+' - '+esc(label(end))+'</span></div><svg viewBox="0 0 580 210" role="img" aria-label="job count timeline" style="width:100%;height:auto;margin-top:10px;display:block"><g stroke="#2d3a47" stroke-width="1"><line x1="40" y1="170" x2="540" y2="170"/><line x1="40" y1="40" x2="40" y2="170"/></g><g fill="#94a3b3" font-size="11"><text x="8" y="44">'+max+'</text><text x="16" y="174">0</text><text x="40" y="194">'+esc(label(start))+'</text><text x="460" y="194">'+esc(label(end))+'</text></g><polyline fill="none" stroke="#94a3b3" stroke-width="3" points="'+line('pending')+'"/><polyline fill="none" stroke="#f3c969" stroke-width="3" points="'+line('running')+'"/><polyline fill="none" stroke="#63d297" stroke-width="3" points="'+line('success')+'"/><polyline fill="none" stroke="#ff7c7c" stroke-width="3" points="'+line('failed')+'"/></svg><div class="summary" style="gap:16px"><span style="color:#94a3b3">pending</span><span style="color:#f3c969">running</span><span style="color:#63d297">success</span><span style="color:#ff7c7c">failed</span></div>';const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function addJobTimeline(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.job-timeline'))return;const counts={success:0,failed:0,blocked:0,running:0,pending:0};(run.jobs||[]).forEach(job=>{const result=job.result;const status=!result?(run.running?'running':'pending'):result.error==='blocked by failed dependency'?'blocked':result.exit_code===0?'success':'failed';counts[status]++});const total=Math.max(1,(run.jobs||[]).length);const colors={success:'#63d297',failed:'#ff7c7c',blocked:'#ff9f68',running:'#f3c969',pending:'#94a3b3'};const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.justifyContent='space-between';heading.style.alignItems='baseline';heading.style.gap='12px';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.textContent=(run.jobs||[]).length+' jobs';note.className='meta';heading.append(title,note);const bar=document.createElement('div');bar.style.display='flex';bar.style.height='22px';bar.style.margin='16px 0 12px';bar.style.overflow='hidden';bar.style.borderRadius='3px';bar.title='Job status distribution';const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='12px';['success','failed','blocked','running','pending'].forEach(status=>{if(!counts[status])return;const segment=document.createElement('span');segment.style.width=(counts[status]/total*100)+'%';segment.style.background=colors[status];segment.title=status+': '+counts[status];bar.append(segment);const item=document.createElement('span');item.textContent=status+' '+counts[status]+' ('+Math.round(counts[status]/total*100)+'%)';item.style.color=colors[status];item.style.fontSize='12px';legend.append(item)});section.append(heading,bar,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function addJobTimeline(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.job-timeline'))return;const points=(run.timeline||[]).filter(point=>point.at);if(!points.length)return;const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};const keys=['pending','running','success','failed'];const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.justifyContent='space-between';heading.style.alignItems='baseline';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.className='meta';note.textContent=points.length+' time points';heading.append(title,note);const chart=document.createElement('div');chart.style.display='grid';chart.style.gap='7px';chart.style.marginTop='14px';points.forEach(point=>{const row=document.createElement('div');row.style.display='grid';row.style.gridTemplateColumns='92px 1fr';row.style.alignItems='center';row.style.gap='10px';const label=document.createElement('span');label.className='meta';label.textContent=new Date(point.at).toLocaleTimeString();const bar=document.createElement('div');bar.style.display='flex';bar.style.height='16px';bar.style.overflow='hidden';bar.style.borderRadius='3px';bar.title=keys.map(key=>key+': '+(point[key]||0)).join(' | ');const total=keys.reduce((sum,key)=>sum+(point[key]||0),0)||1;keys.forEach(key=>{const count=point[key]||0;if(!count)return;const segment=document.createElement('span');segment.style.width=count/total*100+'%';segment.style.background=colors[key];bar.append(segment)});row.append(label,bar);chart.append(row)});const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='18px';legend.style.marginTop='12px';keys.forEach(key=>{const item=document.createElement('span');item.textContent=key;item.style.color=colors[key];item.style.borderLeft='3px solid '+colors[key];item.style.paddingLeft='8px';legend.append(item)});section.append(heading,chart,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function addJobTimeline(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');const points=(run&&run.timeline||[]).filter(point=>point.at);if(!run||!app||app.querySelector('.job-timeline')||!points.length)return;const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};const keys=['pending','running','success','failed'];const total=Math.max(1,(run.jobs||[]).length);const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.alignItems='baseline';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.className='meta';note.style.marginLeft='auto';note.textContent='time → / share ↑';heading.append(title,note);const plot=document.createElement('div');plot.style.display='flex';plot.style.alignItems='flex-end';plot.style.gap='8px';plot.style.height='190px';plot.style.marginTop='14px';plot.style.padding='8px 8px 0 34px';plot.style.borderLeft='1px solid var(--line)';plot.style.borderBottom='1px solid var(--line)';points.forEach(point=>{const column=document.createElement('div');column.style.flex='1 1 0';column.style.minWidth='18px';column.style.height='100%';column.style.display='flex';column.style.flexDirection='column';column.style.justifyContent='flex-end';const bar=document.createElement('div');bar.style.display='flex';bar.style.flexDirection='column-reverse';bar.style.height='100%';bar.style.justifyContent='flex-start';bar.title=keys.map(key=>key+': '+(point[key]||0)).join(' | ');keys.forEach(key=>{const count=point[key]||0;if(!count)return;const segment=document.createElement('span');segment.style.height=count/total*100+'%';segment.style.background=colors[key];segment.style.minHeight='2px';bar.append(segment)});const label=document.createElement('span');label.className='meta';label.style.fontSize='10px';label.style.textAlign='center';label.style.marginTop='5px';label.textContent=new Date(point.at).toLocaleTimeString();column.append(bar,label);plot.append(column)});const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='18px';legend.style.marginTop='12px';keys.forEach(key=>{const item=document.createElement('span');item.textContent=key;item.style.color=colors[key];item.style.borderLeft='3px solid '+colors[key];item.style.paddingLeft='8px';legend.append(item)});section.append(heading,plot,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function collapseRunGraphics(){document.querySelectorAll('.run-statistics,.run-environment').forEach(section=>{const content=section.children[1];if(content){content.style.display='grid';content.style.gridTemplateColumns=section.classList.contains('run-statistics')?'repeat(5,minmax(0,1fr))':'repeat(3,minmax(0,1fr))';content.style.gap='12px'}});document.querySelectorAll('.run-statistics,.run-environment,.job-timeline').forEach(section=>{if(section.dataset.collapsible)return;section.dataset.collapsible='true';const heading=section.firstElementChild;if(!heading)return;const key=section.className;const button=document.createElement('button');button.type='button';button.style.marginRight='8px';button.setAttribute('aria-expanded',String(!!expandedRunGraphics[key]));const apply=expanded=>{[...section.children].slice(1).forEach((child,index)=>{const isTimelinePlot=section.classList.contains('job-timeline')&&index===0;child.style.display=expanded?(isTimelinePlot?'flex':(index===0&&(section.classList.contains('run-statistics')||section.classList.contains('run-environment'))?'grid':'')):'none'});button.setAttribute('aria-expanded',String(expanded));button.textContent=expanded?'-':'+';expandedRunGraphics[key]=expanded};button.onclick=()=>apply(!expandedRunGraphics[key]);heading.style.display='flex';heading.style.alignItems='center';heading.insertBefore(button,heading.firstChild);apply(!!expandedRunGraphics[key])})}
function addJobTimeline(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');const points=(run&&run.timeline||[]).filter(point=>point.at);if(!run||!app||app.querySelector('.job-timeline')||!points.length)return;const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};const keys=['pending','running','success','failed'];const total=Math.max(1,(run.jobs||[]).length);const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.alignItems='baseline';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.className='meta';note.style.marginLeft='auto';note.textContent='time → / share ↑';heading.append(title,note);const plot=document.createElement('div');plot.className='timeline-plot';plot.style.display='flex';plot.style.alignItems='flex-end';plot.style.gap='10px';plot.style.height='190px';plot.style.marginTop='14px';plot.style.padding='8px 8px 0 34px';plot.style.borderLeft='1px solid var(--line)';plot.style.borderBottom='1px solid var(--line)';points.forEach(point=>{const column=document.createElement('div');column.style.flex='1 1 0';column.style.minWidth='24px';column.style.height='100%';column.style.display='flex';column.style.flexDirection='column';column.style.justifyContent='flex-end';const bar=document.createElement('div');bar.className='timeline-bar';bar.style.display='flex';bar.style.flexDirection='column-reverse';bar.style.height='100%';bar.style.justifyContent='flex-start';bar.title=keys.map(key=>key+': '+(point[key]||0)).join(' | ');keys.forEach(key=>{const count=point[key]||0;if(!count)return;const segment=document.createElement('span');segment.style.height=count/total*100+'%';segment.style.background=colors[key];segment.style.minHeight='2px';bar.append(segment)});const label=document.createElement('span');label.className='meta';label.style.fontSize='10px';label.style.textAlign='center';label.style.marginTop='5px';label.textContent=new Date(point.at).toLocaleTimeString();column.append(bar,label);plot.append(column)});const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='18px';legend.style.marginTop='12px';keys.forEach(key=>{const item=document.createElement('span');item.textContent=key;item.style.display='inline-flex';item.style.color=colors[key];item.style.borderLeft='3px solid '+colors[key];item.style.paddingLeft='8px';legend.append(item)});section.append(heading,plot,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function moveActionColumnsLeft(){document.querySelectorAll('#app table.runs').forEach(table=>{const headerRow=table.querySelector('thead tr');if(!headerRow)return;const actionHeader=[...headerRow.children].find(header=>header.textContent.trim()==='Actions');if(!actionHeader)return;headerRow.insertBefore(actionHeader,headerRow.firstChild);table.querySelectorAll('tbody tr').forEach(row=>{const actionCell=[...row.children].find(cell=>cell.querySelector('button'));if(actionCell)row.insertBefore(actionCell,row.firstChild)})})}
function spaceGraphicLegends(){document.querySelectorAll('.run-statistics > div:last-child,.job-timeline > div:last-child').forEach(legend=>{legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.columnGap='24px';legend.style.rowGap='8px';legend.querySelectorAll('span').forEach(item=>{const failed=item.textContent.trim().startsWith('failed');if(failed){item.style.color='#ff7c7c'}item.style.display='inline-flex';item.style.whiteSpace='nowrap';item.style.borderLeft='3px solid '+item.style.color;item.style.paddingLeft='10px';item.style.paddingRight='8px';item.style.marginRight='4px'})})}
function showTimelineBar(){document.querySelectorAll('.job-timeline').forEach(section=>{const bar=section.children[1];if(bar)bar.style.display='flex'})}
function renderJobTimelineScratch(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.job-timeline'))return;const points=run.timeline||[];const keys=['pending','running','success','failed'];const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};const total=Math.max(1,(run.jobs||[]).length);const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.alignItems='baseline';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.className='meta';note.style.marginLeft='auto';note.textContent='time → / share ↑';heading.append(title,note);const plot=document.createElement('div');plot.className='timeline-plot';plot.style.display='flex';plot.style.alignItems='flex-end';plot.style.gap='10px';plot.style.height='190px';plot.style.overflowX='auto';plot.style.marginTop='14px';plot.style.padding='8px 12px 0 34px';plot.style.borderLeft='1px solid var(--line)';plot.style.borderBottom='1px solid var(--line)';points.forEach(point=>{const column=document.createElement('div');column.style.flex='0 0 28px';column.style.width='28px';column.style.height='100%';column.style.display='flex';column.style.flexDirection='column';column.style.justifyContent='flex-end';const bar=document.createElement('div');bar.className='timeline-bar';bar.style.display='flex';bar.style.flexDirection='column-reverse';bar.style.height='100%';bar.title=keys.map(key=>key+': '+(point[key]||0)).join(' | ');keys.forEach(key=>{const count=point[key]||0;if(!count)return;const segment=document.createElement('span');segment.style.height=count/total*100+'%';segment.style.background=colors[key];segment.style.minHeight='2px';bar.append(segment)});const label=document.createElement('span');label.className='meta';label.style.fontSize='10px';label.style.textAlign='center';label.style.marginTop='5px';label.textContent=point.at?new Date(point.at).toLocaleTimeString():'-';column.append(bar,label);plot.append(column)});const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='18px';legend.style.marginTop='12px';keys.forEach(key=>{const item=document.createElement('span');item.textContent=key;item.style.display='inline-flex';item.style.color=colors[key];item.style.borderLeft='3px solid '+colors[key];item.style.paddingLeft='8px';legend.append(item)});section.append(heading,plot,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function renderJobTimelineScratch(){const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'||parts[2]!=='run')return;const queue=state.queues.find(item=>item.queue_name===decodeURIComponent(parts[1]));const run=queue&&queue.runs.find(item=>item.run_id===decodeURIComponent(parts[3]));const app=document.getElementById('app');if(!run||!app||app.querySelector('.job-timeline'))return;const points=run.timeline||[];const keys=['pending','running','success','failed'];const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};const total=Math.max(1,(run.jobs||[]).length);const width=Math.max(560,points.length*100+70),height=260,left=42,top=18,right=14,bottom=58,plotWidth=width-left-right,plotHeight=height-top-bottom;const section=document.createElement('section');section.className='job-timeline';section.style.background='linear-gradient(135deg,rgba(29,39,49,.95),rgba(20,29,38,.92))';section.style.border='1px solid #385160';section.style.padding='18px';section.style.margin='16px 0 20px';const heading=document.createElement('div');heading.style.display='flex';heading.style.alignItems='baseline';const title=document.createElement('h2');title.textContent='Job timeline';title.style.margin='0';const note=document.createElement('span');note.className='meta';note.style.marginLeft='auto';note.textContent='time → / share ↑';heading.append(title,note);const chart=document.createElement('div');chart.style.overflowX='auto';chart.style.marginTop='14px';const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 '+width+' '+height);svg.setAttribute('role','img');svg.setAttribute('aria-label','Job timeline chart');svg.style.display='block';svg.style.width=width+'px';svg.style.height=height+'px';const line=(x1,y1,x2,y2,color='#2d3a47',dash='')=>{const element=document.createElementNS('http://www.w3.org/2000/svg','line');Object.entries({x1,y1,x2,y2,stroke:color,'stroke-width':'1'}).forEach(([key,value])=>element.setAttribute(key,value));if(dash)element.setAttribute('stroke-dasharray',dash);svg.append(element)};const text=(x,y,value,anchor='end')=>{const element=document.createElementNS('http://www.w3.org/2000/svg','text');element.setAttribute('x',x);element.setAttribute('y',y);element.setAttribute('fill','#94a3b3');element.setAttribute('font-size','11');element.setAttribute('text-anchor',anchor);element.textContent=value;svg.append(element)};[0,50,100].forEach(percent=>{const y=top+plotHeight-(percent/100*plotHeight);line(left,y,width-right,y,'#2d3a47',percent?'4 4':'');text(left-7,y+4,percent+'%')});line(left,top,left,top+plotHeight,'#94a3b3');line(left,top+plotHeight,width-right,top+plotHeight,'#94a3b3');points.forEach((point,index)=>{const x=left+((index+0.5)/Math.max(points.length,1))*plotWidth;let y=top+plotHeight;keys.forEach(key=>{const count=point[key]||0;if(!count)return;const segmentHeight=count/total*plotHeight;y-=segmentHeight;const rect=document.createElementNS('http://www.w3.org/2000/svg','rect');rect.setAttribute('x',x-14);rect.setAttribute('y',y);rect.setAttribute('width',28);rect.setAttribute('height',segmentHeight);rect.setAttribute('fill',colors[key]);rect.setAttribute('rx','2');rect.setAttribute('title',key+': '+count);svg.append(rect)});line(x,top+plotHeight,x,top+plotHeight+4,'#94a3b3');text(x,height-24,point.at?new Date(point.at).toLocaleTimeString():'-', 'middle')});text(width/2,height-4,'time','middle');const yLabel=document.createElementNS('http://www.w3.org/2000/svg','text');yLabel.setAttribute('x','12');yLabel.setAttribute('y',height/2);yLabel.setAttribute('fill','#94a3b3');yLabel.setAttribute('font-size','11');yLabel.setAttribute('text-anchor','middle');yLabel.setAttribute('transform','rotate(-90 12 '+height/2+')');yLabel.textContent='share';svg.append(yLabel);chart.append(svg);const legend=document.createElement('div');legend.style.display='flex';legend.style.flexWrap='wrap';legend.style.gap='18px';legend.style.marginTop='10px';keys.forEach(key=>{const item=document.createElement('span');item.textContent=key;item.style.display='inline-flex';item.style.color=colors[key];item.style.borderLeft='3px solid '+colors[key];item.style.paddingLeft='8px';legend.append(item)});section.append(heading,chart,legend);const env=app.querySelector('.run-environment');if(env)env.after(section);else app.prepend(section)}
function syncTimelineBar(){}
function fixTimelineBarWidths(){document.querySelectorAll('.timeline-plot').forEach(plot=>{plot.style.display='flex';plot.style.flexDirection='row';plot.style.flexWrap='nowrap';plot.style.alignItems='flex-end';plot.style.overflowX='auto';plot.style.height='230px';plot.style.paddingBottom='46px'});document.querySelectorAll('.timeline-plot>div').forEach(column=>{column.style.flex='0 0 92px';column.style.width='92px';column.style.minWidth='92px';column.style.height='180px';const bar=column.querySelector('.timeline-bar');if(bar){bar.style.width='28px';bar.style.height='160px';bar.style.flex='0 0 160px';bar.style.marginLeft='auto';bar.style.marginRight='auto'}const label=column.querySelector('.timeline-bar+span');if(label){label.style.display='block';label.style.width='92px';label.style.whiteSpace='nowrap';label.style.textAlign='center';label.style.transform='none';label.style.position='static';label.style.fontSize='10px'}})}
function alignTimelineHeading(){document.querySelectorAll('.job-timeline>div:first-child').forEach(heading=>{heading.style.paddingLeft='0'})}
function alignGraphicHeadings(){document.querySelectorAll('.run-statistics>div:first-child,.run-environment>div:first-child,.job-timeline>div:first-child').forEach(heading=>{heading.style.display='flex';heading.style.justifyContent='flex-start';heading.style.alignItems='center';const title=heading.querySelector('h2');const note=heading.querySelector('.meta');if(title)title.style.margin='0';if(note)note.style.marginLeft='auto'})}
function fixTimelineLegendColors(){const colors={pending:'#94a3b3',running:'#f3c969',success:'#63d297',failed:'#ff7c7c'};document.querySelectorAll('.job-timeline span').forEach(item=>{const key=item.textContent.trim();if(colors[key]){item.style.color=colors[key];item.style.borderLeftColor=colors[key]}})}
function fixRunStatisticsColors(){const colors={succeeded:'#63d297',failed:'#ff7c7c','in progress':'#f3c969',pending:'#94a3b3'};document.querySelectorAll('.run-statistics strong').forEach(value=>{const metric=value.parentElement;const label=metric?metric.textContent.toLowerCase():'';const key=Object.keys(colors).find(name=>label.includes(name));if(key)value.style.color=colors[key]});document.querySelectorAll('.run-statistics .meta,.run-statistics div span').forEach(label=>{label.style.color='var(--muted)'})}
function simplifyRunStatistics(){document.querySelectorAll('.run-statistics').forEach(section=>{[...section.children].slice(1).forEach(child=>{child.style.display='none'})})}
const originalEnhancePage=enhancePage;enhancePage=function(){originalEnhancePage();addRunStatistics();addRunEnvironment();renderJobTimelineScratch();spaceGraphicLegends();simplifyRunStatistics();fixTimelineBarWidths();syncTimelineBar();collapseRunGraphics();alignTimelineHeading();alignGraphicHeadings()}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}const originalRender=render;render=function(){originalRender();enhancePage();enhanceQueueOverview();addQueueOverviewPathActions();const parts=location.pathname.split('/').filter(Boolean);if(parts[0]==='queue'&&!parts[2]){const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(queue){const commands=queue.queue.commands||[];addQueueEditors(queue,commands);enhanceQueueSourceContext(commands)}}addExecutionGuide();addDeleteRunButton();addPathTableActions();removeLegacyOutputBox();keepGlobalOutputBox();placeOutputBox();renameCopyButtons();labelEquivalentCommand();addRunJobStatusColumn();addRunningOutputButtons();addRunningCancelButtons();mergeActionColumns();labelJobActionHeaders();styleActionColumns();markJobHeaders();markLatestRun();enableTableSorting();restoreSelectedOutput();applyStatusColors();fixRunStatisticsColors();fixTimelineLegendColors()};window.addEventListener('popstate',render);refresh();setInterval(refresh,2000);
</script></body></html>`
