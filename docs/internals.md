# Rotari internals

This is a compact design map for maintainers and coding agents. User-facing
behavior belongs in `README.md`; local implementation details belong in code
and tests. Update this file only when a cross-cutting contract changes, and
replace obsolete rules rather than accumulating history.

## System model

Rotari is file-backed. A base directory contains projects; each project owns
one mutable queue and immutable run history. A run snapshots the queue and
stores execution state, results, and logs by stable job ID. The server
coordinates execution but is not the source of truth. CLI, server, executors,
and web UI must share the same persisted semantics.

The normal state layout is:

```text
<basedir>/
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

Files may appear incrementally while a run is active. Readers must tolerate
missing optional or not-yet-written run files without inventing completed
results.

## Resolution rules

Without a run-location lookup, base directories resolve in this order:

1. `--basedir`
2. `ROTARI_BASEDIR`
3. `./.rotari-state` when present
4. `$XDG_STATE_HOME/rotari`
5. `~/.local/state/rotari`

Projects resolve from `--project-name`, then `ROTARI_PROJECT_NAME`, then the
only project in the resolved base directory. With no projects the name is
`default`; multiple projects require an explicit choice.

Persisted timestamps use UTC RFC3339 values. Human-readable CLI and web
projections convert them to the display location: a valid IANA timezone name
from `TZ` takes precedence; otherwise Go's local location is used, which on
Linux follows the system timezone configured through `/etc/localtime`.

A supplied `--run-id` is exact, never an alias for latest. Existing-run
commands use the master registry to fill in its base directory and project.
Explicit location options take priority, but a conflict with the registry must
fail. An unregistered run uses normal location resolution for backward
compatibility. A missing explicit run is an error, with no latest fallback.

Without `--run-id`, history consumers use `meta.json` `last_run_id`, then the
newest run directory where supported. Current state may take precedence:
`show` displays the active run first, an interrupted run second, then a
non-empty idle queue, before selecting history.

`wait` is the exception among history consumers: when no run ID is supplied,
it requires an active `running.lock` and waits for that run; it does not infer
a historical run.

Run lookup applies to commands consuming existing history (`show`, `wait`,
`copy`, `change`, `remove`, `delete`, and rerun selection), not commands
creating or controlling current state such as `add` and a plain new `run`.
When `wait` receives multiple run IDs, each ID is resolved independently so
one command can wait for asynchronous runs from different projects or base
directories.

Shell completion follows the same location rules but has narrower candidate
semantics. `project-name` lists project directories under the resolved base
directory. `run-id` lists saved run directories under the resolved project.
Without `--run-id`, `job-id` combines IDs from the current queue and job
directories in all saved runs for the resolved project, deduplicated and sorted.
With `--run-id`, `job-id` resolves that run through the master registry and lists
only its immediate job directories; explicit base directory and project options
must agree with the registry entry. Missing state directories produce no
completion candidates rather than an error in the shell.

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
collection is not performed.

## Run lifecycle

Queue-editing commands mutate `queue.json`. Starting a run assigns a new ID,
snapshots the queue, records context, and marks it active. Completion writes
results and summary, updates metadata, clears the consumed queue, and removes
the active lock. Saved runs remain until explicitly deleted.

Retries and filtered runs create new history. Selected jobs execute; completed
jobs outside the selection carry forward their result and an origin pointing
to the original output. Dependencies use unique job names within a queue;
unknown names, duplicates, and cycles are rejected before execution. `add`
also rejects a duplicate job name immediately, without writing the queue, so
that mistake is never deferred to execution time; a `--depends-on` name may
still refer to a job added later in the same queue, so unknown-name and cycle
checks remain deferred to the execution boundary.

An array queue command has an inclusive `first-last` range or an explicit
comma-separated task list. Runtime expansion creates one `JobSpec` and
persisted job directory per selected task. Local executors run those tasks as
independent processes. Slurm, PBS, and LSF may submit a complete contiguous
range as one native array; sparse selections fall back to independent
submissions so scheduler support for sparse native arrays is not required.

Result-based selection (`--failed`/`--unfinished`/`--success` in `copy`, and in
rerun when `--partial-array=false`) and copied-job origin status operate on
the unexpanded `QueuedCommand`, but results are recorded per expanded task ID.
Matching an array command therefore aggregates its task results
(`aggregatedJobResult` in `run_selection.go`): it is "finished" only once
every task has a result, and any non-zero task exit code marks it failed as a
whole.

`run`/`retry` default to `--partial-array=true`: for a filtered rerun,
`planRerunSelection` evaluates each array task's own result against the
selection (`planArrayTaskSelection`) instead of the aggregate, so only the
matching tasks (e.g. the failed ones) re-execute while the rest carry their
own result forward into the new run's summary. Each carried task's `Origin`
is recorded in `QueuedCommand.TaskOrigins` (keyed by task ID, e.g. "id-1"),
separately from the whole-command `Origin` field, since one array command can
have some tasks freshly executed and others carried in the same run.
`loadRunOrigin`/`show`/`web` check both `Origin` and `TaskOrigins` when
resolving where a job's output lives. `--partial-array=false` restores the
older whole-array behavior (any match re-executes every task, using only the
whole-command `Origin`).

`--depends-on` ordering is resolved entirely by rotari itself, wave by wave,
inside `executeMixedRun`; it never relies on scheduler-native dependency
features (e.g. Slurm's `--dependency`). This keeps dependency semantics
identical across every executor, including mixes of local and remote ones in
the same run.

## Execution boundaries

`executeMixedRun` (`mixed_run.go`) is the single execution engine for every
run regardless of executor mix: the synchronous path (`runServerSync` in
`server.go`) and the async worker path (`cmdWorkerRun` in `main.go`) both
drive it, and it is the only place that expands array plans, dispatches to
executors, and writes the run summary. Do not add a per-executor standalone
full-run orchestrator (submit-all/wait-all/finalize) outside this path; extend
`JobExecutor` methods or `executeMixedRun` itself instead. A prior Slurm-only
run path (`executeSlurmRun` and friends) duplicated this and had silently gone
dead after `executeMixedRun` replaced it; it was removed in favor of this rule.

`JobExecutor` is the scheduler boundary. Implementations share lifecycle and
result semantics where supported; scheduler metadata belongs in the job's run
directory. Polling executors persist their latest normalized scheduler state in
`scheduler_status.json`; read projections use it without querying schedulers
directly. Scheduler display names may offer inspection commands but must not be
the only way to locate state.

Task wrappers normalize scheduler-specific task indexes into
`ROTARI_ARRAY_TASK_ID` and the related `ROTARI_ARRAY_*` variables. Job wrappers
also expose stable run, project, job, directory, current-working-directory,
and executable-path variables prefixed with `ROTARI_`.

`QueuedCommand.Environment` stores user-supplied `KEY=VALUE` entries from the
common `--env` option. Its values are passed to every executor and copied into
run snapshots. Generated `ROTARI_*` variables override user values with the
same name; invalid variable names are rejected at all queue mutation boundaries.

`QueuedCommand.WorkingDirectory` stores an optional executor-side working
directory. It is copied into each expanded `JobSpec` and applied before the
command starts by local, SSH, Slurm, PBS, and LSF executors. An SSH path is
resolved on the remote host; it is not the run's local `context.json` `cwd`.

The SSH executor treats its first executor option as the target host and the
remaining options as `ssh` options. It manages a local SSH session per job,
records output and final status locally, and does not require the remote host
to mount the run directory. Its native ID is the local SSH process ID, so
running SSH jobs can only be resumed or cancelled while the supervising rotari
process remains alive.

The server supervises one base directory and may stop when idle, so durable
behavior belongs in files, not memory. The web UI is a projection of the same
model, not a separate database. The optional Python interface is also a
projection: it invokes the installed CLI with `subprocess` and must not
implement queue or execution semantics on its own. `wait --json` emits one
`RunSummary` JSON object per requested run (NDJSON when multiple IDs are
supplied). `show --json` emits one object with the resolved location, run
summary when available, and saved commands. These modes are additive; default
CLI output remains human-facing.

Shared-base operation across hosts relies on the shared filesystem preserving
the semantics of exclusive file creation, atomic rename, and advisory `flock`.
The state lock serializes queue mutations and `running.lock` prevents a second
runner from starting the same project. This is coordination for a consistent
shared filesystem, not distributed locking: it cannot fence a host after a
network partition or determine whether a remote PID is alive. A remote run
lock therefore remains active until an operator confirms the run stopped and
uses `unlock`.
Project state locks and run locks are scoped to each project directory, so
different projects largely isolate queue and run state even under one shared
base directory. Base-level server and registry state, along with the shared
filesystem semantics, remain common dependencies.
Run finalization rechecks that `running.lock` still belongs to the finishing
run while holding the state lock, so a stale runner cannot clear or finalize a
newer run after recovery. A new run lock is written completely to a temporary
file and published without replacing an existing lock, so readers do not see
partially written lock JSON.

`web` binds `--host`/`--port` (default `127.0.0.1:8787`) via `listenWeb`. When
`--port` is left at its default, a busy port falls back to scanning upward
(port+1, port+2, ...) until a free one is found or 65535 is reached; an
explicit `--port` (including `--port 0`, which asks the OS for an ephemeral
port) never falls back and fails immediately if unavailable. The actually
bound address is reported after the listener is created, not the requested
one.

A synchronous client disconnect, including Ctrl-C, requests cancellation and
returns to the caller immediately (exit code 130); the server-side run keeps
executing in the background and only then runs normal finalization. Ctrl-D
sends an explicit detach control before disconnecting instead, leaving the run
uncancelled and transferring completion cleanup to the background waiter. Ctrl-Z
does not send a rotari protocol message: the terminal suspends the foreground
client while the server-side run continues, so `fg` can resume the client but
Ctrl-Z is not a clean detach. If the stopped client is killed when its terminal
closes, the resulting connection EOF follows the normal disconnect path and
requests cancellation. Async workers are monitored by the server; their exit
decrements the active-run count so the server can stop. Manual interrupted-run
recovery is reserved for failures that bypass finalization.
A completed run, sync or async, decrements the active-run count immediately
via `beginRun`/`endRun`; reaching zero stops the server right away rather than
waiting for the idle timeout, so tests and callers must not assume the server
stays up after a run finishes.

CLI terminal colors are semantic presentation, not part of the machine-readable
output contract. Colors are emitted only when the relevant output stream is a
TTY, so redirected and piped output remains plain text. Red denotes errors,
failed runs, failed job output, and failed job results; green denotes successful
completion or successful state-changing confirmations; yellow denotes warnings,
running or blocked state, retries, and recovery/cancellation notices; cyan
denotes informational labels, headings, lifecycle messages, and suggested
actions. White is used for the values attached to colored labels. New messages
should preserve these meanings, while parsers and tests must rely on the text,
not ANSI sequences or color choice.

Human-readable `show` log output uses a lazy pager. With `--no-pager`, or when
stdout is not a TTY, it is written directly to stdout. On a TTY, output is
buffered and written directly when it is at most 24 lines; only longer output
starts the command from `$PAGER`, defaulting to `less -R`. Thus a pager such as
`less -F` and direct output are distinct paths: `less -F` is only invoked after
the length threshold is exceeded, while short output does not start a pager at
all. If the pager cannot be started, the buffered output falls back to stdout.

## Concurrency and safety

Project mutations hold the advisory `state.lock`. `running.lock` represents an
active run and includes host information because local PID checks cannot prove
remote process liveness.

A dead local run lock is removed automatically, but `meta.json` remaining in
`running` or `cancelling` phase with a `last_run_id` marks an interrupted run.
Queue-mutating `add`, `copy`, and `run` operations must reject that state so a
retained execution queue cannot be extended or rerun accidentally. `unlock`
with the exact run ID acknowledges recovery, keeps the retained queue, and
returns the phase to `collecting`; it works whether the stale lock remains or
was already removed. `reset` discards the current, not-yet-run queue while
keeping queue defaults and run history; interactively it asks for the same
confirmation that all jobs have stopped before recovering an interrupted run
and discarding its retained queue, or that confirmation can be supplied up
front as `reset --recover`. `reset` rejects an active run outright. Managing
the background server is a separate concern (`server status`, `server
shutdown`); no project command pings or offers to stop it as a side effect.

Never silently remove a possibly active remote lock. Destructive commands must
reject ambiguous targets, and exact IDs must never degrade into latest-item
selection.

JSON writes use the common atomic write helper. Preserve backward-compatible
reads when adding optional fields; avoid rewriting unrelated history.

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
