# Rotari FAQ

Answers to specific "what happens if...?" questions about rotari's behavior.
For feature walkthroughs, see [README.md](../README.md); for the underlying
contracts, see [internals.md](internals.md).

## Projects, queues, and runs

**What's the difference between a project, a queue, and a run?**
A project is a named container (`--project-name`) that holds one current
queue and its saved run history. The queue (`queue.json`) is the batch of
commands waiting to execute. A run is an immutable snapshot of a queue at the
moment `run` started, kept under `runs/<run-id>/` with its own logs and
results. See [Projects, queues, runs, and state](../README.md#projects-queues-runs-and-state).

**I didn't pass `--project-name` — which project does rotari use?**
`--project-name`, then `ROTARI_PROJECT_NAME`, then the only project in the
resolved state directory. If no project exists yet, it defaults to `default`.
If multiple projects exist and none of the above narrows it down, rotari
errors and asks you to pick one explicitly.

**`rotari show` displayed my queue, not the run I expected — why?**
Without `--run-id`, `show` prioritizes current state: an active run first, an
interrupted run second, a non-empty idle queue third, and only then the
latest saved run. Pass `--run-id` (or `--runs` to list all saved runs) to
target a specific run regardless of current queue state.

## Retry, copy, and array jobs

**Can an array run only selected task IDs?**
Yes. Use `--array 1,3,4` for a sparse task list (ranges such as `1-10` are
also supported). Sparse lists run as independent scheduler submissions, so
they do not require native sparse-array support from PBS, LSF, or Slurm.

**I ran `rotari retry` — which jobs actually rerun?**
`retry` is shorthand for `run --failed --unfinished`: jobs that failed or
never finished are re-executed; jobs that already succeeded are carried
forward into the new run with their previous result, not re-run. Use
`--success` to force-rerun jobs that already succeeded.

**Does retrying an array job rerun every task?**
No, by default (`--partial-array=true`) only the tasks matching the filter
(e.g. the failed ones) are re-executed; the rest carry forward their own
previous result. Pass `--partial-array=false` to rerun the entire array
whenever any one task matches, matching pre-partial-array behavior.

**Why did `copy` reuse the same job ID instead of generating a new one?**
`copy` only assigns a new job ID when the original one would collide with a
job already in the destination queue. Otherwise the original ID — and any
dependency relationships between copied jobs — is preserved.

**If a prerequisite job (`--depends-on`) fails, what happens to the jobs that depend on it?**
They are recorded as `blocked` and are never executed for that run. A retry
reruns only the failed prerequisite (and any other failed/unfinished jobs);
once it succeeds, the previously blocked dependents run on the next
`rotari run`/`retry` that includes them.

## Interrupted runs and locking

**A runner process died mid-run — what do I do?**
Run `rotari unlock --run-id RUN_ID` (the exact command is shown by `show`) to
acknowledge the stopped run and keep its retained queue, once you've
confirmed the jobs really stopped. To discard the retained queue instead, run
`rotari reset` — interactively it asks for the same confirmation, or supply
it up front with `rotari reset --recover`. `add`, `copy`, and `run` stay
blocked until one of these is run.

**A remote host's lock looks stuck even though the job actually stopped — why won't `unlock` go away automatically?**
Rotari only auto-clears a run lock by checking whether the PID that created
it is still alive, and it can only do that on the same host. A lock created
on another host is always treated as active. Confirm independently that the
run has really stopped, then run `rotari unlock --run-id RUN_ID` explicitly;
running it while the job might still be alive can let a second run start
against the same queue.

**Is it safe to Ctrl-C a synchronous `rotari run`?**
Yes, but the terminal returns immediately, before jobs actually stop. Ctrl-C
prints "Cancellation requested..." and exits with code 130 right away — it
doesn't wait for cleanup. The background supervisor keeps working after your
shell prompt is back: it cancels the still-running jobs and only then does
normal finalization (summary, queue clearing, lock removal). If you run
`add`/`copy`/`run` against the same project right after Ctrl-C, expect it to
be rejected as "not allowed" until finalization catches up a moment later —
that's normal and needs no `unlock`, unless the process is killed outright
(see the next question).

**Can I detach a synchronous run without cancelling it?**
Yes. Press Ctrl-D while `rotari run` is waiting for progress. The client exits
cleanly and the run continues in the background; this is equivalent to
starting the run with `--async` after it has begun. This is the key difference
from Ctrl-Z: Ctrl-D is rotari's detach command, while Ctrl-Z is the shell's
job-control suspend command.

**What happens if I press Ctrl-Z during a synchronous run?**
The terminal suspends the foreground `rotari` client, but the run continues in
the background because it is supervised by the separate server process. Use
`fg` to resume the client and keep watching progress. Do not use Ctrl-Z as a
way to detach: the stopped client is still a shell job, and its terminal
connection is not cleanly handed off. If you close the terminal while it is
stopped, the client is killed and the server sees a disconnect, which requests
cancellation of the run. Use Ctrl-D for a one-way detach, or start with
`rotari run --async` when you already know you do not need the interactive
progress view.

**Can `wait` find the run ID for me?**
Yes. With no run ID, `rotari wait` detects the active run for the resolved
project and waits for it. Specify `--project-name` when the base directory has
multiple projects.

**How does rotari actually stop a running job on Ctrl-C or `rotari cancel`?**
It depends on the executor. `local` sends `SIGTERM` to the job's direct child
process only — not a process group — so a wrapper script that spawns its own
children must forward the signal itself if you want those killed too. `ssh`
sends `SIGTERM` to the local `ssh` client process supervising the remote
command; whether that reaches the remote command depends on your `ssh`
options (e.g. a `-tt` pseudo-terminal). `slurm`/`pbs`/`lsf` call the
scheduler's native cancel command (`scancel`/`qdel`/`bkill`) against the
job's cluster ID instead of signaling a local process. In every case rotari
only asks the job to stop (`SIGTERM` or the scheduler equivalent); it never
escalates to `SIGKILL` for you, so a job that ignores the signal keeps
running until it exits on its own or you intervene manually.

## Web UI

**Is there a Python API?**
Yes. The optional `python/` package is a thin subprocess wrapper around the
`rotari` executable. It does not reimplement queue or execution behavior.
Run `source ./activate_python.sh` first, then install it with
`python3 -m pip install --no-deps ./python`; `wait` and `show` consume the
CLI's JSON output, while CLI errors remain exceptions. It accepts executable
argument lists, not Python functions or closures to serialize and submit. This
is deliberately different from function-oriented frameworks such as
[Submitit](https://github.com/facebookincubator/submitit).

**Does closing/stopping `rotari web` stop my jobs?**
No. The web UI is a separate, optional process you start explicitly
(`rotari web`) purely to view status; it is a read-only status viewer (plus a
confirmation-gated job copy action) and never starts, stops, or otherwise
controls the runner. It is unrelated to the background supervisor described
below — closing it has no effect on any run.

## Background server (supervisor)

**Do I need to start a server manually?**
No. Unlike `rotari web`, this supervisor process is never started by hand —
`run`/`add`/`copy` launch it automatically when needed, it is not shown
anywhere in their own output, and it stops on its own once the run finishes
(or once idle, for a server kept up for other reasons). `rotari server
status`/`list`/`shutdown` are for inspection and manual cleanup only — useful
if you want to confirm the supervisor is still finishing a Ctrl-C'd run, or
to force it down.

## Timestamps and environment

**What timezone are the times shown by `show`/`web` in?**
Everything is stored in UTC. For display, rotari uses a valid IANA timezone
from `TZ` if set, otherwise Go's local location (the system timezone via
`/etc/localtime` on Linux).

**A CLI option and its matching `ROTARI_*` environment variable are both set — which wins?**
The explicit command-line option always takes precedence over the
environment variable.
