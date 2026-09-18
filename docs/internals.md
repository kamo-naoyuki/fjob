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
The registry is only an index: run files remain authoritative. Registry
retention and garbage collection are not defined yet.

## Run lifecycle

Queue-editing commands mutate `queue.json`. Starting a run assigns a new ID,
snapshots the queue, records context, and marks it active. Completion writes
results and summary, updates metadata, clears the consumed queue, and removes
the active lock. Saved runs remain until explicitly deleted.

Retries and filtered runs create new history. Selected jobs execute; completed
jobs outside the selection carry forward their result and an origin pointing
to the original output. Dependencies use unique job names within a queue;
unknown names, duplicates, and cycles are rejected before execution.

An array queue command has an inclusive `first-last` range. Runtime expansion
creates one `JobSpec` and persisted job directory per task. Local executors run
those tasks as independent processes. Slurm, PBS, and LSF may submit a complete
selected range as one native array; incomplete selections fall back to
independent submissions so carried or omitted tasks are never started
accidentally.

## Execution boundaries

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

The SSH executor treats its first executor option as the target host and the
remaining options as `ssh` options. It manages a local SSH session per job,
records output and final status locally, and does not require the remote host
to mount the run directory. Its native ID is the local SSH process ID, so
running SSH jobs can only be resumed or cancelled while the supervising rotari
process remains alive.

The server supervises one base directory and may stop when idle, so durable
behavior belongs in files, not memory. The web UI is a projection of the same
model, not a separate database.

A synchronous client disconnect, including Ctrl-C, requests cancellation and
waits for normal run finalization. Async workers are monitored by the server;
their exit decrements the active-run count so the server can stop. Manual
interrupted-run recovery is reserved for failures that bypass finalization.

## Concurrency and safety

Project mutations hold the advisory `state.lock`. `running.lock` represents an
active run and includes host information because local PID checks cannot prove
remote process liveness.

A dead local run lock is removed automatically, but `meta.json` remaining in
`running` or `cancelling` phase with a `last_run_id` marks an interrupted run.
Queue-mutating `add`, `copy`, and `run` operations must reject that state so a
retained execution queue cannot be extended or rerun accidentally. `unlock`
with the exact run ID acknowledges recovery and returns the phase to
`collecting`; it works whether the stale lock remains or was already removed.
Interactive `check` offers the same phase recovery after explicit confirmation
that all jobs have stopped, with a choice to retain or clear queue commands;
the retain choice is displayed as `unlock`, and clearing preserves queue
defaults and run history. If a server is still running, an interactive plain
`check` displays its PID and asks before forcing shutdown, warning that other
projects may be interrupted. Non-interactive `check` only prints the exact
`unlock` and server shutdown commands. `check --server` only pings for an
already-running server and never shuts it down, since that form must stay a
side-effect-free inspection.

Never silently remove a possibly active remote lock. Destructive commands must
reject ambiguous targets, and exact IDs must never degrade into latest-item
selection.

JSON writes use the common atomic write helper. Preserve backward-compatible
reads when adding optional fields; avoid rewriting unrelated history.

## Code map

- Core state, paths, resolution, and locks: `main.go`, `run_registry.go`.
- CLI contracts and orchestration: `cli_spec.go`, command files, `server.go`.
- Short option aliases are defined centrally in `cli_spec.go`; parsing and shell
    completion must consume the same definitions.
- Rerun semantics: `run_selection.go`.
- Execution boundary: `job_executor.go`, `executor_*.go`.
- Read projections: `show.go`, `web.go`.

When behavior crosses these boundaries, add a focused test at the public
command or persisted-state boundary. Keep CLI metadata, completion, README
usage, and this document synchronized only where their contracts actually
change.
