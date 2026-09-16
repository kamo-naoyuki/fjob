package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
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
	Jobs    []webJob `json:"jobs"`
	CWD     string   `json:"cwd,omitempty"`
	Running bool     `json:"running"`
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
	handler := newWebHandler(baseDir, *queueNameOption)
	server := &http.Server{Addr: *host + ":" + strconv.Itoa(*port), Handler: handler}
	go func() {
		<-interruptSignal()
		_ = server.Close()
	}()
	fmt.Printf("fjob web listening at http://%s\n", server.Addr)
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
		_, _ = writer.Write([]byte(webIndexHTML))
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
		if copyRequest.Selection != "all" && copyRequest.Selection != "failed" && copyRequest.Selection != "unfinished" && copyRequest.Selection != "success" && copyRequest.Selection != "nonsuccess" {
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
		state.Runs = append(state.Runs, webRun{RunSummary: summary, Jobs: jobs, CWD: context.CWD, Running: runID == state.RunningRunID})
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
		job := webJob{ID: command.ID, Name: command.Name, Command: command.Command, Executor: command.Executor, ExecutorOptions: command.ExecutorOptions, DependsOn: command.DependsOn, Origin: command.Origin}
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
		jobs = append(jobs, webJob{ID: result.ID, Command: result.Command, Result: &resultCopy})
	}
	return jobs, nil
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

func methodNotAllowed(writer http.ResponseWriter) {
	writer.WriteHeader(http.StatusMethodNotAllowed)
}

const webIndexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>fjob</title><style>.runs tr.latest-run td{font-weight:600;background:rgba(184,217,242,.06)}.runs th:last-child,.runs td:last-child{width:1%;min-width:0;white-space:nowrap;text-align:left;padding-left:8px;padding-right:8px}
:root{color-scheme:dark;--bg:#10151b;--panel:#18212b;--line:#2d3a47;--text:#e8eef4;--muted:#94a3b3;--good:#63d297;--bad:#ff7c7c;--warn:#f3c969}.command-guide{white-space:pre-wrap;background:#0b1015;border:1px solid var(--line);padding:14px;color:#d7e2ea;margin:12px 0 18px;overflow:auto}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#10151b,#182733);color:var(--text);font:15px/1.5 ui-sans-serif,system-ui,sans-serif}main{max-width:1100px;margin:0 auto;padding:36px 22px}header{display:flex;justify-content:space-between;align-items:end;border-bottom:1px solid var(--line);padding-bottom:20px;margin-bottom:24px}h1{margin:0;font-size:32px;letter-spacing:.04em}h2{font-size:18px;margin:0 0 12px}.meta{color:var(--muted);font-size:13px}.toolbar{display:flex;gap:8px}button{border:1px solid var(--line);background:#202d39;color:var(--text);padding:8px 12px;border-radius:5px;cursor:pointer}button:hover{border-color:#7190a8}button:disabled{opacity:.45;cursor:not-allowed}input,select{border:1px solid var(--line);background:#101820;color:var(--text);padding:7px 8px;min-width:100px}.dirty{border-color:var(--warn);background:#3b331d;box-shadow:0 0 0 1px rgba(243,201,105,.25)}section{background:rgba(24,33,43,.9);border:1px solid var(--line);padding:18px;margin-bottom:20px}.summary{display:flex;gap:28px;color:var(--muted);font-size:14px}.runs{width:100%;border-collapse:collapse}.runs th,.runs td{text-align:left;border-bottom:1px solid var(--line);padding:10px 8px}.runs th{color:var(--muted);font-size:12px;text-transform:uppercase}.runs th:last-child,.runs td:last-child{white-space:nowrap;width:1%;vertical-align:top}.runs td.latest-run{font-weight:600;background:rgba(184,217,242,.06)}.latest-badge{color:#b8d9f2;font-size:11px;font-weight:400;letter-spacing:.04em;margin-left:6px}.status-finished{color:var(--good)}.status-failed{color:var(--bad)}.status-running{color:var(--warn)}.run-id{font-family:ui-monospace,monospace;color:#b8d9f2;cursor:pointer}.log{white-space:pre-wrap;background:#0b1015;border:1px solid var(--line);padding:14px;min-height:100px;max-height:360px;overflow:auto;color:#d7e2ea}.empty{color:var(--muted);padding:20px 0}.output-modal{position:fixed;inset:0;background:rgba(0,0,0,.72);display:flex;align-items:center;justify-content:center;padding:24px;z-index:10}.output-panel{width:min(1100px,96vw);height:min(760px,90vh);background:var(--panel);border:1px solid var(--line);padding:18px;box-shadow:0 12px 50px #000}.output-panel.compact{width:min(900px,92vw);height:auto}.output-panel header{margin:0 0 12px;padding:0 0 10px}.output-panel .log{height:calc(100% - 48px);max-height:none;margin:0}.output-panel.compact .log{height:auto;max-height:240px;min-height:0}@media(max-width:650px){header{display:block}.toolbar{margin-top:14px}.summary{flex-wrap:wrap;gap:10px}.runs th:nth-child(3),.runs td:nth-child(3){display:none}}
</style></head><body><main><header><div><h1>fjob</h1><div class="meta" id="location">loading...</div></div><div class="toolbar"><button onclick="refresh()">Refresh</button></div></header>
<section><h2 id="page-title">All queues</h2><div class="summary" id="summary"></div></section><div id="app" class="empty">loading...</div><div id="output-modal" class="output-modal" style="display:none" onclick="if(event.target===this)closeOutputModal()"><div class="output-panel" onclick="event.stopPropagation()"><header><strong>Output</strong><button onclick="closeOutputModal()">Close</button></header><pre id="modal-log" class="log"></pre></div></div></main><script>
let state;
let selectedOutput='';
let selectedLog=null;
let followTimer=null;
let sortState={queue:{key:'name',direction:1},run:{key:'started',direction:-1},job:{key:'name',direction:1}};
async function refresh(){if(document.activeElement&&document.activeElement.closest('.web-queue-commands input,.web-queue-commands select'))return;const r=await fetch('/api/state');if(!r.ok){document.getElementById('app').textContent=await r.text();return}state=await r.json();render()}
function render(){const queues=state.queues||[];const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue'){renderOverview(queues);return}const queue=queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(!queue){renderMissing('Queue not found');return}if(parts[2]==='run'){renderRun(queue,decodeURIComponent(parts[3]));return}renderQueue(queue)}
function renderOverview(queues){let queued=0,runs=0,running=0;queues.forEach(q=>{queued+=(q.queue.commands||[]).length;runs+=q.runs.length;running+=q.runs.filter(r=>r.running).length});document.getElementById('location').textContent=state.base_dir+' / all queues';document.getElementById('page-title').textContent='All queues';document.getElementById('summary').innerHTML='<span>'+queues.length+' queues</span><span>'+queued+' queued</span><span>'+runs+' runs</span><span>'+running+' running</span>';const rows=queues.map(q=>{let latest=null;for(const run of q.runs){if(!latest||run.started_at>latest.started_at)latest=run}return '<tr><td><a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'">'+esc(q.queue_name)+'</a></td><td>'+(q.queue.commands||[]).length+'</td><td>'+q.runs.length+'</td><td>'+q.runs.filter(r=>r.running).length+'</td><td>'+(latest?'<a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'/run/'+encodeURIComponent(latest.run_id)+'">'+esc(latest.run_name||latest.run_id)+'</a>':'-')+'</td><td class="status-'+(latest?latest.status:'')+'">'+esc(latest?latest.status:'-')+'</td><td>'+esc(latest?latest.started_at:'-')+'</td></tr>'}).join('');document.getElementById('app').innerHTML=queues.length?'<table class="runs queue-overview"><thead><tr><th data-sort="name">Queue</th><th data-sort="queued">Queued</th><th data-sort="runs">Runs</th><th data-sort="running">Running</th><th>Latest run</th><th data-sort="status">Status</th><th data-sort="started">Started</th></tr></thead><tbody>'+rows+'</tbody></table>':'No queues found.'}
function renderQueue(q){document.getElementById('location').textContent=state.base_dir+' / '+q.queue_name;document.getElementById('page-title').textContent=q.queue_name;document.getElementById('summary').innerHTML='<span>'+(q.queue.commands||[]).length+' queued</span><span>'+q.runs.length+' runs</span><span>'+q.runs.filter(r=>r.running).length+' running</span>';const rows=q.runs.map(r=>'<tr><td><a class="link run-id" href="/queue/'+encodeURIComponent(q.queue_name)+'/run/'+encodeURIComponent(r.run_id)+'">'+esc(r.run_id)+'</a></td><td class="status-'+r.status+'">'+esc(r.status)+(r.running?' ...':'')+'</td><td>'+(r.finished_at?esc(r.exit_code):'-')+'</td><td>'+esc(r.started_at||'-')+'</td><td>'+esc(r.finished_at||'-')+'</td></tr>').join('');document.getElementById('app').innerHTML='<div class="toolbar"><a class="link" href="/">All queues</a></div>'+(rows?'<table class="runs"><thead><tr><th data-sort="run">Run</th><th data-sort="status">Status</th><th data-sort="exit">Exit</th><th data-sort="started">Started</th><th data-sort="finished">Finished</th></tr></thead><tbody>'+rows+'</tbody></table>':'<div class="empty">No runs found.</div>')}
function renderRun(q,runID){const run=q.runs.find(r=>r.run_id===runID);if(!run){renderMissing('Run not found');return}document.getElementById('location').textContent=state.base_dir+' / '+q.queue_name+' / '+runID;document.getElementById('page-title').textContent=run.run_name||runID;document.getElementById('summary').innerHTML='<span>Queue: '+esc(q.queue_name)+'</span><span>Run ID: '+esc(run.run_id)+'</span><span>Status: '+esc(run.status)+'</span><span>Exit: '+(run.finished_at?esc(run.exit_code):'-')+'</span>';const jobs=(run.jobs||[]).map(j=>{const result=j.result;const options=(j.executor_options||[]).join(' ');const dependencies=(j.depends_on||[]).join(', ');const exit=result?esc(result.exit_code):'-';const error=result&&result.error?'<div class="error">'+esc(result.error)+'</div>':'';const output=result?'<button onclick="log(\''+esc(q.queue_name)+'\',\''+esc(runID)+'\',\''+esc(j.id)+'\')">Output</button>':'-';return '<tr><td><strong>'+esc(j.name||'-')+'</strong><div class="meta">'+esc(j.id)+'</div></td><td>'+esc(j.executor||'default')+'</td><td>'+esc(options||'-')+'</td><td>'+esc(dependencies||'-')+'</td><td class="command">'+esc((j.command||[]).join(' '))+'</td><td>'+exit+error+'</td><td>'+output+'</td></tr>'}).join('');const cwd=run.cwd||'-';const copy='cd '+shellQuote(cwd)+' && fjob copy --queue-name '+shellQuote(q.queue_name)+' --run-id '+shellQuote(runID)+' --failed';document.getElementById('app').innerHTML='<div class="toolbar"><a class="link" href="/queue/'+encodeURIComponent(q.queue_name)+'">Back to '+esc(q.queue_name)+'</a></div><p class="meta">Started: '+esc(run.started_at||'-')+' | Finished: '+esc(run.finished_at||'-')+' | Jobs: '+(run.jobs||[]).length+'</p><p>Working directory: <code>'+esc(cwd)+'</code></p><pre class="log">Retry from a terminal:\n'+esc(copy)+'</pre>'+(jobs?'<table class="runs"><thead><tr><th>Job name / ID</th><th>Executor</th><th>Executor options</th><th>Dependencies</th><th>Command</th><th>Exit / error</th><th></th></tr></thead><tbody>'+jobs+'</tbody></table>':'<div class="empty">No job definitions yet.</div>')+'<pre id="log" class="log">Select a job output.</pre>'}
function renderMissing(message){document.getElementById('page-title').textContent='Not found';document.getElementById('summary').textContent='';document.getElementById('app').innerHTML='<a class="link" href="/">All queues</a><p>'+esc(message)+'</p>'}
function enhancePage(){document.querySelectorAll('.web-copy-controls,.web-queue-commands,.web-origin').forEach(e=>e.remove());const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue')return;const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(!queue)return;if(parts[2]==='run'){const runID=decodeURIComponent(parts[3]);const controls=document.createElement('div');controls.className='toolbar web-copy-controls';controls.innerHTML='<button onclick="copyRun(\''+esc(queue.queue_name)+'\',\''+esc(runID)+'\',\'failed\')">Copy failed jobs</button><button onclick="copyRun(\''+esc(queue.queue_name)+'\',\''+esc(runID)+'\',\'all\')">Copy all jobs</button>';document.getElementById('app').prepend(controls);return}const commands=queue.queue.commands||[];const section=document.createElement('section');section.className='web-queue-commands';section.innerHTML='<h2>Current queue</h2>'+(commands.length?'<table class="runs"><thead><tr><th>Job name / ID</th><th>Status</th><th>Executor</th><th>Executor options</th><th>Dependencies</th><th>Command</th></tr></thead><tbody>'+commands.map(j=>'<tr><td><strong>'+esc(j.name||'-')+'</strong><div class="meta">'+esc(j.id)+'</div></td><td>pending</td><td>'+esc(j.executor||'default')+'</td><td>'+esc((j.executor_options||[]).join(' ')||'-')+'</td><td>'+esc((j.depends_on||[]).join(', ')||'-')+'</td><td class="command">'+esc((j.command||[]).join(' '))+'</td></tr>').join('')+'</tbody></table>':'<div class="empty">Queue is empty.</div>');document.getElementById('app').prepend(section);fixQueueSourceColumns(commands)}
async function copyRun(queue,run,selection){const q=state.queues.find(item=>item.queue_name===queue);const existing=(q&&q.queue.commands||[]).length;if(existing&&!confirm('This will replace '+existing+' queued jobs. Continue?'))return;const response=await fetch('/api/copy',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({queue_name:queue,run_id:run,selection:selection,overwrite:existing>0})});const text=await response.text();if(!response.ok){alert(text);return}alert(JSON.parse(text).message);window.location.href='/queue/'+encodeURIComponent(queue)}
function fixQueueSourceColumns(commands){const table=document.querySelector('.web-queue-commands table');if(!table)return;const sourceRunHeader=document.createElement('th');sourceRunHeader.textContent='Source run';const sourceStatusHeader=document.createElement('th');sourceStatusHeader.textContent='Source status';const sourceOutputHeader=document.createElement('th');sourceOutputHeader.textContent='Source output';table.querySelector('thead tr').append(sourceRunHeader,sourceStatusHeader,sourceOutputHeader);const rows=table.querySelectorAll('tbody tr');commands.forEach((job,index)=>{if(!rows[index])return;const sourceRun=document.createElement('td');const sourceStatus=document.createElement('td');const sourceOutput=document.createElement('td');if(job.origin){sourceRun.textContent=job.origin.run_id+'/'+job.origin.job_id;sourceStatus.textContent=job.origin.status;sourceOutput.innerHTML='<button onclick="showOriginalOutput(\''+esc(job.origin.run_id)+'\',\''+esc(job.origin.job_id)+'\',this)">Output</button>'}else{sourceRun.textContent='-';sourceStatus.textContent='-';sourceOutput.textContent='-'}rows[index].append(sourceRun,sourceStatus,sourceOutput)})}
function enhanceQueueSourceContext(commands){const section=document.querySelector('.web-queue-commands');if(!section)return;const origins=commands.filter(job=>job.origin);if(!origins.length)return;const runs=[...new Set(origins.map(job=>job.origin.run_id))];const cwds=[...new Set(origins.map(job=>job.origin.cwd).filter(Boolean))];const context=document.createElement('p');context.className='meta';context.textContent='Source run: '+runs.join(', ')+' | Source working directory: '+(cwds.join(', ')||'-');section.prepend(context)}
function addExecutionGuide(){document.querySelectorAll('.execution-guide').forEach(element=>element.remove());const parts=location.pathname.split('/').filter(Boolean);if(parts[0]!=='queue')return;const queueName=decodeURIComponent(parts[1]);const queue=state.queues.find(item=>item.queue_name===queueName);if(!queue)return;const guide=document.createElement('pre');guide.className='command-guide execution-guide';const basedir=state&&state.base_dir?(' --basedir '+shellQuote(state.base_dir)):' ';if(parts[2]==='run'){const runID=decodeURIComponent(parts[3]);const run=queue.runs.find(item=>item.run_id===runID);if(!run)return;const latest=queue.runs.reduce((current,item)=>!current||item.started_at>current.started_at?item:current,null);const prefix=run.cwd&&run.cwd!=='-'?'cd '+shellQuote(run.cwd)+'\n':'';if(latest&&latest.run_id===runID){guide.textContent='# To rerun failed jobs from the latest run\n'+prefix+'fjob rerun'+basedir+' --queue-name '+shellQuote(queueName)+' --failed'}else{guide.textContent='# To rerun failed jobs from this older run\n'+prefix+'fjob copy'+basedir+' --queue-name '+shellQuote(queueName)+' --run-id '+shellQuote(runID)+' --failed\nfjob rerun'+basedir+' --queue-name '+shellQuote(queueName)+' --job-id JOB_ID'}const workingDirectory=[...document.querySelectorAll('#app p')].find(element=>element.textContent.startsWith('Working directory:'));if(workingDirectory)workingDirectory.after(guide);else document.getElementById('app').prepend(guide);return}const origins=(queue.queue.commands||[]).map(job=>job.origin).filter(Boolean);const directories=[...new Set(origins.map(origin=>origin.cwd).filter(Boolean))];const prefix=directories.length===1?'cd '+shellQuote(directories[0])+'\n':'';guide.textContent='# To execute jobs in current queue\n'+prefix+'fjob run'+basedir+' --queue-name '+shellQuote(queueName);const section=document.querySelector('.web-queue-commands');if(section)section.append(guide)}
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
function styleActionColumns(){document.querySelectorAll('#app table.runs th:last-child,#app table.runs td:last-child').forEach(cell=>{cell.style.width='1%';cell.style.minWidth='0';cell.style.whiteSpace='nowrap';cell.style.textAlign='left'})}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}const originalRender=render;render=function(){originalRender();enhancePage();enhanceQueueOverview();addQueueOverviewPathActions();const parts=location.pathname.split('/').filter(Boolean);if(parts[0]==='queue'&&!parts[2]){const queue=state.queues.find(q=>q.queue_name===decodeURIComponent(parts[1]));if(queue){const commands=queue.queue.commands||[];addQueueEditors(queue,commands);enhanceQueueSourceContext(commands)}}addExecutionGuide();addDeleteRunButton();addPathTableActions();removeLegacyOutputBox();keepGlobalOutputBox();placeOutputBox();renameCopyButtons();labelEquivalentCommand();addRunJobStatusColumn();addRunningOutputButtons();addRunningCancelButtons();mergeActionColumns();labelJobActionHeaders();styleActionColumns();markJobHeaders();markLatestRun();enableTableSorting();restoreSelectedOutput();applyStatusColors()};window.addEventListener('popstate',render);refresh();setInterval(refresh,2000);
</script></body></html>`
