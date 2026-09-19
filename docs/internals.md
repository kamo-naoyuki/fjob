# Rotari internals

This is a compact design map for maintainers and coding agents. User-facing
behavior belongs in `README.md`; local implementation details belong in code
and tests. Update this file only when a cross-cutting contract changes, and
replace obsolete rules rather than accumulating history.

## System model

1. Rotari is file-backed.
2. A base directory contains projects. Each project owns one mutable queue and
    a run history.
3. A run snapshots the queue and stores execution state, results, and logs by
    stable job ID.
4. The CLI, server, executors, and web UI project the same persisted state
    model.

The normal state layout is:

```text
<basedir>/
├── server.log
└── projects/<project>/
    ├── queue.json
    ├── meta.json
    ├── state.lock
    ├── running.lock
    └── runs/<run-id>/
        ├── commands.json
        ├── context.json
        ├── summary.json
        └── <job-id>/
            ├── command.json
            ├── output
            └── executor-specific state
```

1. Files may appear incrementally while a run is active.
2. Readers must tolerate missing optional or not-yet-written run files without
    inventing completed results.
3. The background server writes lifecycle, request, and error events to
    `<basedir>/server.log`.
4. The server log is bounded: before an event would make it exceed 1 MiB, the
    regular file is truncated and the new event is written.
5. Run output remains the durable execution record.

## Core design contracts

The following rules govern the current CLI, server, web, and executor design.
An intentional change to one is an architectural change: update this document,
the user-facing documentation, and the affected tests together.

1. A completed run is historical and immutable. Its persisted snapshot,
    results, and logs are not rewritten; it remains available until explicitly
    deleted.
2. Every retry creates a new run. It may use a prior run as its reference, but
    never modifies that source run.
3. Carry-forward writes reused results only into the destination run and never
    changes the source run.
4. Carried-forward jobs retain an origin that identifies the source run and
    job (per task for arrays), so their output remains traceable across runs.
5. The filesystem is the source of truth. Registries and in-memory state are
    indexes or coordination aids and must be recoverable from persisted files.
6. The server coordinates access and execution; it is not persistent
    authority for project or run state.
7. Each project owns one mutable current queue as the staging area for the
    next run. Queue edits change that queue; starting a run snapshots it, and
    normal completion clears the consumed queue. An interrupted run retains
    the queue until it is recovered or reset.
8. A project has at most one active run and runner at a time. That runner may
    execute multiple jobs concurrently, while different projects can run
    independently.
9. Executors implement job execution and scheduler integration, not run
    semantics. Run planning, dependency handling, carry-forward, and summary
    finalization belong to rotari's shared execution path.

## Resolution rules

Without a run-location lookup, base directories resolve in this order:

1. `--basedir`
2. `ROTARI_BASEDIR`
3. `./.rotari-state` when present
4. `$XDG_STATE_HOME/rotari`
5. `~/.local/state/rotari`

- **RES-001:** Projects resolve from `--project-name`, then
    `ROTARI_PROJECT_NAME`, then the only project in the resolved base directory.
    With no projects the name is `default`; multiple projects require an
    explicit choice.
- **RES-002:** Project names and job IDs are single path elements, never
    relative or absolute paths.
- **RES-003:** Empty values, `.`, `..`, absolute paths, and values containing
    `/` or `\` are rejected before filesystem access. This applies to
    `resolvePaths` and `cancelJobs`, including requests from remote callers.
- **RES-004:** Persisted timestamps use UTC RFC3339. Human-readable CLI and web
    views use the IANA timezone from `TZ` when valid, otherwise Go's local
    timezone.
- **RES-005:** A supplied `--run-id` is exact, never an alias for latest.
    Existing-run commands use the master registry for its base directory and
    project.
- **RES-006:** Explicit location options take priority, but conflicts with the
    registry fail. An unregistered run uses normal resolution for compatibility,
    while a missing explicit run is an error with no latest fallback.
- **RES-007:** Without `--run-id`, history consumers use `meta.json`
    `last_run_id`, then the newest run directory where supported. `show` may
    prefer an active run, an interrupted run, or a non-empty idle queue before
    history.
- **RES-008:** `wait` is the exception: without a run ID it requires an active
    `running.lock` and never infers a historical run.
- **RES-009:** Run lookup applies to history commands (`show`, `wait`, `copy`,
    `change`, `remove`, `delete`, and rerun selection), not state-creating
    commands such as `add` or a plain new `run`.
- **RES-010:** Multiple run IDs passed to `wait` are resolved independently, so
    one command may wait for runs from different projects or base directories.
- **RES-011:** Shell completion follows the same location rules with narrower
    candidates: `project-name` lists project directories, `run-id` lists saved
    runs, and `job-id` lists queue and saved-run job IDs according to the
    selected run.
- **RES-012:** Missing state directories produce no completion candidates
    instead of a shell error.

Run IDs contain a UTC timestamp and random suffix. They are collision-resistant
but do not encode a location. The master directory therefore stores one index
record per run:

```text
<masterdir>/runs/<run-id>.json -> { base_dir, project_name, run_id }
```

It resolves from `ROTARI_MASTERDIR`, then `$XDG_STATE_HOME/rotari/master`, then
`~/.local/state/rotari/master`, and is also used for server discovery.

Register new runs before exposing location-independent commands. Re-registering
the same mapping is idempotent; mapping one ID to another location must fail.
The registry is only an index: run files remain authoritative. Deleting a run
through the CLI or web history controls removes its registry entry after the
run files and metadata are updated. Registry entries for runs deleted outside
rotari may remain as orphaned records. `rotari gc` scans for such records and
caches the plan for ten minutes; `rotari gc --apply` removes only unchanged
cached entries whose run directory is still absent. Automatic garbage
collection is not performed. Malformed or invalid registry files are reported
and left untouched for manual inspection.

## Run lifecycle

1. Queue-editing commands mutate `queue.json`. Starting a run assigns a new
    ID, snapshots the queue, records context, and marks it active. Completion
    writes results and summary, updates metadata, clears the consumed queue, and
    removes the active lock. Saved runs remain until explicitly deleted.
2. Retries and filtered runs create new history. `--retry N` retries a failed
    job up to N additional times within the same run. A failed job is one whose
    result has a non-zero exit code and is not explicitly cancelled.
3. An explicit cancellation is terminal for the current run. A job marked
    cancelled, or whose recorded execution state is `cancelled`, is not
    automatically retried by that run's `--retry` loop, even if its exit code is
    non-zero.
4. A later explicit `rotari retry` may select a cancelled job through the
    normal `failed`/`unfinished` result filters. This is a new run, so it is a
    new user decision to execute the job again.
5. In a filtered run, selected jobs execute. Completed jobs outside the
    selection carry forward their result and an origin pointing to the original
    output; jobs without a completed result remain unfinished.
6. Dependencies use unique job names within a queue. Unknown names,
    duplicates, and cycles are rejected before execution. `add` also rejects a
    duplicate job name immediately, without writing the queue, so that mistake
    is never deferred to execution time. A `--depends-on` name may still refer
    to a job added later in the same queue, so unknown-name and cycle checks
    remain deferred to the execution boundary.
7. An array queue command has an inclusive `first-last` range or an explicit
    comma-separated task list. Runtime expansion creates one `JobSpec` and
    persisted job directory per selected task. Local executors run those tasks
    as independent processes. Slurm, PBS, and LSF may submit a complete
    contiguous range as one native array; sparse selections fall back to
    independent submissions so scheduler support for sparse native arrays is
    not required.
8. Result-based selection (`--failed`/`--unfinished`/`--success` in `copy`,
    and in rerun when `--partial-array=false`) and copied-job origin status
    operate on the unexpanded `QueuedCommand`, but results are recorded per
    expanded task ID. Matching an array command therefore aggregates its task
    results (`aggregatedJobResult` in `run_selection.go`): it is "finished" only
    once every task has a result, and any non-zero task exit code marks it failed
    as a whole.
9. `run`/`retry` default to `--partial-array=true`. For a filtered rerun,
    `planRerunSelection` evaluates each array task's own result against the
    selection (`planArrayTaskSelection`) instead of the aggregate, so only the
    matching tasks (for example, the failed ones) re-execute while the rest
    carry their own result forward into the new run's summary. Each carried
    task's `Origin` is recorded in `QueuedCommand.TaskOrigins`, keyed by task ID
    such as `id-1`, separately from the whole-command `Origin` field. This is
    needed because one array command can have some tasks freshly executed and
    others carried in the same run. `loadRunOrigin`/`show`/`web` check both
    `Origin` and `TaskOrigins` when resolving where a job's output lives.
    `--partial-array=false` restores the older whole-array behavior: any match
    re-executes every task, using only the whole-command `Origin`.
10. `--depends-on` ordering is resolved entirely by rotari itself, wave by
     wave, inside `executeMixedRun`; it never relies on scheduler-native
     dependency features such as Slurm's `--dependency`. This keeps dependency
     semantics identical across every executor, including mixes of local and
     remote ones in the same run.

## Run orchestration and executor responsibilities

1. `executeMixedRun` (`mixed_run.go`) is the single execution engine for every
    run, regardless of executor mix.
2. Both the synchronous path (`runServerSync`) and async worker path
    (`cmdWorkerRun`) drive it. It expands array plans, dispatches to executors,
    and writes the run summary.
3. Per-executor full-run orchestrators must not be added outside this path.
    Extend `JobExecutor` methods or `executeMixedRun` instead.
4. The former Slurm-only orchestration path was removed because it duplicated
    this responsibility and became dead after `executeMixedRun` replaced it.
1. `JobExecutor` is the scheduler boundary. Implementations share lifecycle
    and result semantics where supported; scheduler metadata belongs in the
    job's run directory.
2. Polling executors persist normalized scheduler state in
    `scheduler_status.json`. Read projections use it without querying
    schedulers directly.
3. Scheduler display names may offer inspection commands, but must not be the
    only way to locate state. `controlQueueJobs` also writes
    `scheduler_status.json` immediately after successful suspend/resume calls,
    including for executors whose `Wait` loop does not poll that file.
4. Task wrappers normalize scheduler-specific task indexes into
    `ROTARI_ARRAY_TASK_ID` and related `ROTARI_ARRAY_*` variables. Job wrappers
    expose stable run, project, job, directory, working-directory, and
    executable-path variables prefixed with `ROTARI_`.
5. `QueuedCommand.Environment` stores user-supplied `KEY=VALUE` entries from
    `--env`. Values are passed to every executor and copied into run snapshots.
    Generated `ROTARI_*` variables override user values, and invalid names are
    rejected at every queue mutation boundary.
6. `QueuedCommand.WorkingDirectory` is copied into each expanded `JobSpec` and
    applied before commands start by local, SSH, Slurm, PBS, and LSF executors.
    An SSH path is resolved on the remote host, not from local `context.json`.
7. The SSH executor treats its first executor option as the target host and
    the rest as `ssh` options. It records output and final status locally and
    does not require the remote host to mount the run directory. Its native ID
    is the local SSH process ID, so control is possible only while the
    supervising rotari process remains alive.

## Server and read projections

1. The server supervises one base directory and may stop when idle, so durable
    behavior belongs in files, not memory.
2. The web UI is a projection of the same model, not a separate database.
3. The optional Python interface invokes the installed CLI with `subprocess`
    and must not implement queue or execution semantics itself.
4. `wait --json` emits one `RunSummary` object per requested run, using NDJSON
    when multiple IDs are supplied.
5. `show --json` emits one object with the resolved location, available run
    summary, and saved commands. JSON modes are additive; default CLI output
    remains human-facing.

## Job execution durability

1. Every executor runs the command through a self-reporting wrapper that
    writes `<job-id>/status.json` with phase, exit code, and hosts.
2. The wrapper records status independently of the process that launched it,
    so scheduler accounting lag cannot hide the result.
3. The local executor uses the same wrapper. If the coordinating server or
    async worker is killed, the orphaned local job can finish and record its
    own status instead of leaving no result.
4. The existing `show`/`web.go` fallback chain (`status` -> `status.json` ->
    `summary.json`) consumes this state without reader changes.
5. This does not kill or reconcile leftover jobs during recovery;
    `reset --recover` and `unlock` still require the operator to confirm that
    jobs have stopped.

## Shared-state coordination

1. Shared-base operation relies on exclusive file creation, atomic rename, and
    advisory `flock` semantics from the shared filesystem.
2. The state lock serializes queue mutations and `running.lock` prevents a
    second runner from starting the same project.
3. This is coordination, not distributed locking: it cannot fence a host after
    a network partition or determine whether a remote PID is alive. A remote
    run lock remains active until an operator confirms the run stopped and uses
    `unlock`.
4. Project locks are scoped to project directories, so different projects
    largely isolate queue and run state. Server, registry, and filesystem state
    remain common base-level dependencies.
5. `controlQueueJobs` and `cancelJobs` signal local jobs by process group.
    The wrapper's PID is also its process-group ID via `Setpgid`, so a negative
    PID reaches both the wrapper and its command.
6. `localExecutorHostMismatch` compares the current host with
    `context.json` before signaling. A cross-host local control request gets an
    explicit host error instead of a misleading "job is not running" or a PID
    reuse hazard.
7. Scheduler executors and SSH are expected to work from any host with the
    required access. Their control CLIs must still be installed on the host
    issuing the request.
8. `schedulerCommandHint` turns missing scheduler binaries into explicit
    errors and preserves scheduler stdout/stderr, including explanations for
    rejected operations on queued jobs.
9. Whole-run cancel has the same PID locality issue. `runningWorkerHostMismatch`
    checks `running.lock`'s host before signaling; a cross-host request fails
    instead of reporting success while leaving the real runner untouched.
10. Finalization rechecks that `running.lock` belongs to the finishing run while
     holding the state lock. New locks are written to a temporary file and
     published without replacing an existing lock, preventing partial JSON.
11. State and registry trees use centralized permission helpers. The default
     modes are `0755`/`0644`; `ROTARI_PRIVATE_STATE=true` switches new paths to
     owner-only `0700`/`0600`/`0700`.
12. Permission settings apply only to newly created paths. Existing paths are
     not rechmoded, so changing the setting can produce mixed permissions.
13. `generateStaticWeb` is the intentional exception and always emits
     publishable `0755`/`0644` output.

## Web and control-plane security

1. `web` binds `--host`/`--port`, defaulting to `127.0.0.1:8787`.
2. A busy default port scans upward for a free port. An explicit port,
    including `--port 0`, never falls back and fails immediately if unavailable.
3. The actually bound address is reported after listener creation. Non-loopback
    hosts produce a warning because the server has no authentication.
4. `loadWebState` exposes persisted runtime metadata: `running.lock` fields and
    the presence of `server.sock`/`server.pid`. The panel does not query process
    liveness or infer that `state.lock` is held from the file's existence.
5. The Unix-socket control surface is separate from `ROTARI_PRIVATE_STATE`:
    reaching it means controlling the server, not merely reading state.
6. `runServer` always sets the socket to `0600`. On Linux,
    `verifyPeerCredential` rejects connections whose UID differs from the
    server process; on other platforms the socket mode is the enforcement.
7. Web mutating routes are gated by `allowControl`, enabled by default and
    configurable with `--allow-control` or `ROTARI_WEB_ALLOW_CONTROL`.
    `--allow-control=false` rejects them with `403` before reading request
    bodies. Read-only `GET` routes remain available.
8. Web state exposes only whether an environment variable is set. Raw values
    never cross the HTTP boundary; `Value` is populated only by the local
    `rotari env` CLI command.

## Client connection lifecycle

decrements the active-run count so the server can stop. Manual interrupted-run
1. A synchronous client disconnect, including Ctrl-C, requests cancellation and
    returns immediately with exit code 130. The server-side run continues in the
    background and then finalizes normally.
2. Ctrl-D sends an explicit detach before disconnecting. The run is left
    uncancelled and completion cleanup moves to the background waiter.
3. Ctrl-Z sends no rotari protocol message. The terminal suspends the client
    while the server-side run continues; a later EOF follows the normal
    disconnect path and requests cancellation.
4. The server monitors async workers. Worker exit decrements the active-run
    count, and interrupted-run recovery is reserved for failures that bypass
    finalization.
5. Completed sync and async runs decrement the active-run count immediately
    through `beginRun`/`endRun`. When it reaches zero, the server stops without
    waiting for the idle timeout.

## CLI presentation

1. CLI colors are semantic presentation, not machine-readable output. They are
    emitted only on TTY streams; redirected and piped output remains plain text.
2. Red denotes errors and failed results; green denotes success; yellow denotes
    warnings, running/blocked state, retries, and recovery/cancellation; cyan
    denotes informational labels and suggested actions; white denotes values.
3. Parsers and tests must rely on text, not ANSI sequences or color choice.
4. `show` uses a lazy pager. `--no-pager` and non-TTY output go directly to
    stdout. On a TTY, output of at most 24 lines is direct; longer output uses
    `$PAGER`, defaulting to `less -R`. Pager failure falls back to stdout.

## Concurrency and safety

1. Project mutations hold the advisory `state.lock`.
2. `running.lock` represents an active run and includes host information,
   because local PID checks cannot prove remote process liveness.

`inspectProjectRunState` (`main.go`) derives one of three states from just
`running.lock` and `meta.json` -- never from job-level files like a job's own
self-reported `status.json` (see "Job execution durability" above), which only
feeds `show`/the web UI, not this state machine:

| State                 | `running.lock`                                   | `meta.json` phase              | `run`/`add`/`copy`/`change`/`delete`/`remove` | `reset`                                  |
|------------------------|---------------------------------------------------|---------------------------------|------------------------------------------|--------------------------------------------|
| `projectIdle`          | absent, or present but stale (auto-removed)        | `collecting`/`finished`         | allowed                                  | allowed                                    |
| `projectRunning`       | present; owning coordinator PID is alive (a lock recorded on another host is always treated as alive, since liveness can't be checked remotely) | `running`/`cancelling`           | rejected: "is running; ... is not allowed" | rejected: same message                     |
| `projectInterrupted`   | absent, or present but the coordinator PID is dead (auto-removed on the same host) | `running`/`cancelling` with `last_run_id` set | rejected: "has interrupted run ...; recover with unlock" | `--recover` (or interactive confirmation) proceeds |

1. A dead local run lock is removed automatically. `meta.json` remaining in
    `running` or `cancelling` with `last_run_id` marks an interrupted run.
2. `ensureProjectIdleForPaths` is the shared check for `run`, `add`, `copy`,
    `change`, `delete`, and `remove`. It rejects both active and interrupted
    projects with the same message, preventing accidental queue mutation.
3. `interruptedRunStatusDetail` scans job directories rather than
    `summary.json` and reports jobs whose `status` or `status.json` is still
    non-terminal, along with phase and last-update time.
4. Missing or unparseable job status counts as still running. The detail only
    improves rejection and confirmation messages; it does not change what
    `--recover` or `unlock` may do.
5. `unlock` with the exact run ID acknowledges recovery, keeps the retained
    queue, and returns the phase to `collecting`.
6. `reset` discards the current queue while keeping defaults and history. It
    confirms that jobs stopped before recovering an interrupted run, unless
    `reset --recover` supplies that confirmation. It rejects an active run.
7. Server management is separate (`server status`, `server shutdown`); project
    commands do not stop or query the server as a side effect.
8. Never silently remove a possibly active remote lock. Destructive commands
    reject ambiguous targets, and exact IDs never degrade into latest-item
    selection.
9. JSON writes use the common atomic helper. Optional fields must retain
    backward-compatible reads, and unrelated history must not be rewritten.

## Code map

- Core state, paths, resolution, and locks: `main.go`, `run_registry.go`.
- CLI contracts and orchestration: `cli_spec.go`, command files, `server.go`.
- CLI option metadata, including short aliases and CLI-default environment
    variables, is defined centrally in `cli_spec.go`; help, parsing, and shell
    completion must consume the same definitions.
- Rerun semantics: `run_selection.go`.
- Execution engine (single entry point for every executor mix): `mixed_run.go`.
- Execution boundary: `job_executor.go`, `executor_*.go`.
- Read projections: `show.go`, `web.go`.

When behavior crosses these boundaries, add a focused test at the public
command or persisted-state boundary. Keep CLI metadata, completion, README
usage, and this document synchronized only where their contracts actually
change.
