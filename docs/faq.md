# Rotari FAQ

Answers to specific "what happens if...?" questions about rotari's behavior.
For feature walkthroughs, see [README.md](../README.md); for the underlying
contracts, see [internals.md](internals.md).

## Projects, queues, runs, and registry

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

**How do I clean up run registry entries left by manual deletion?**
Run `rotari gc` to scan for registry entries whose run directories no longer
exist. It caches the candidates for ten minutes and does not delete anything
by itself. Malformed or invalid registry files are reported and left
untouched; inspect their run data and repair or remove them manually. Review
the result, then run `rotari gc --apply`; it removes only unchanged candidates
and skips any run directory that has reappeared.

## Retries, copying, arrays, and dependencies

**Can an array run only selected task IDs?**
Yes. Use `--array 1,3,4` for a sparse task list (ranges such as `1-10` are
also supported). Sparse lists run as independent scheduler submissions, so
they do not require native sparse-array support from PBS, LSF, or Slurm.

**I ran `rotari retry` — which jobs actually rerun?**
`retry` is shorthand for `run --failed --unfinished`: jobs that failed or
never finished are re-executed; jobs that already succeeded are carried
forward into the new run with their previous result, not re-run. A job
explicitly cancelled during a run is not automatically retried by that run's
`--retry` loop, but it can be selected by a later `rotari retry`. Use
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

**What happens if runners on multiple hosts use the same project?**
This is supported when every host sees the same `basedir` through a shared
filesystem whose `O_EXCL`, atomic rename, and advisory `flock` operations work
correctly (for example, an NFSv4 mount configured for file locking). Rotari
uses the state lock to serialize queue updates and the run lock to prevent two
runners from starting the same project at once. This is still file-based
coordination, not a distributed lock service: it cannot fence a host after a
network partition, verify a remote PID, or repair inconsistent/stale mounts.
If the hosts use different project names, their queue, metadata, and run locks
are separate, so direct state-file interference is much less likely; the
shared `basedir` server/registry and filesystem still remain common
infrastructure.
If a host fails during a run, confirm independently that its jobs have
stopped before using `rotari unlock --run-id RUN_ID` from another host. Do not
run the same project through mounts that do not share a consistent view of the
state files.

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

## Client control and job cancellation

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
It depends on the executor. `local` runs the job command through its own
self-reporting wrapper script in its own process group, and sends `SIGTERM` to
that whole process group, so both the wrapper and the command it launched are
signaled — but a command that itself spawns further children of its own still
has to forward the signal to those if you want them killed too. `ssh` sends
`SIGTERM` to the local `ssh` client process supervising the remote command;
whether that reaches the remote command depends on your `ssh` options (e.g. a
`-tt` pseudo-terminal). `slurm`/`pbs`/`lsf` call the scheduler's native cancel
command (`scancel`/`qdel`/`bkill`) against the job's cluster ID instead of
signaling a local process. In every case rotari only asks the job to stop
(`SIGTERM` or the scheduler equivalent); it never escalates to `SIGKILL` for
you, so a job that ignores the signal keeps running until it exits on its own
or you intervene manually.

**I ran the runner on one host and `rotari web`/CLI on another over a shared
base directory — why do `cancel`/`suspend`/`resume` say the job isn't
running even though it clearly is?**
For a `local`-executor job, those commands signal the job by PID, and a PID
is only meaningful on the host that actually spawned the process. Run the
command from the same host as the runner instead. rotari detects this
mismatch by comparing the current host against the run's recorded hostname
and reports which host the job actually runs on, rather than a bare "not
running".

**What about plain `rotari cancel` with no `--job-id`, cancelling the whole
run at once — does that have the same cross-host problem?**
Yes, and it used to be worse: without a specific job, cancel signals the
runner's whole process group by the PID recorded in `running.lock`. From the
wrong host that PID doesn't exist, and the old code treated "no such
process" as "already stopped" and reported success — silently doing nothing
while the real runner, on another host, kept going. rotari now checks the
lock's recorded host first and errors instead of guessing, the same way
per-job `cancel`/`suspend`/`resume` do.

**Same setup, but the job runs through Slurm/PBS/LSF instead of `local` —
why do I still get an error from `rotari web`/CLI on another host?**
Unlike `local`, these executors don't use PID signaling, so there's no host
mismatch check for them. But `cancel`/`suspend`/`resume` still shell out to
that scheduler's own client command (`scancel`/`scontrol`, `qdel`/`qsig`,
`bkill`/`bstop`/`bresume`), which must be installed and configured on
whichever host runs it. A web/CLI host outside the cluster that only shares
the state directory over NFS typically doesn't have those binaries, so the
command fails with a "command not found" error naming the missing binary
instead of a scheduler response. Run `rotari web`/CLI from a host that has
that scheduler's client tools installed (e.g. a login node), or use `ssh` as
the executor, whose control commands only need local SSH access to the
remote host.

**The run/job status column says "running", but for a Slurm/PBS/LSF job
that can also mean it's actually still queued (`PENDING`) in the scheduler,
not executing yet — what happens if I `suspend`/`cancel` it then?**
rotari's own "running" here only means "submitted and not yet finished", not
"currently executing on a compute node" — it doesn't poll the scheduler
before every UI render. `cancel` works either way (`scancel`/`qdel`/`bkill`
also dequeue a still-pending job). `suspend`, however, is rejected by the
scheduler itself for a job that hasn't started running yet; rotari surfaces
that scheduler's own explanation (e.g. Slurm's `slurm_suspend error: Job is
not running`) instead of a bare "exit status 1", so the error tells you the
job is pending rather than actually running. `resume` on a job that was
never suspended is normally a harmless no-op for the same reason.

## Python interface

**Is there a Python API?**
Yes. The optional `python/` package is a thin subprocess wrapper around the
`rotari` executable. It does not reimplement queue or execution behavior.
Install it with
`python3 -m pip install --no-deps ./python`; `wait` and `show` consume the
CLI's JSON output, while CLI errors remain exceptions. It accepts executable
argument lists, not Python functions or closures to serialize and submit. This
is deliberately different from function-oriented frameworks such as
[Submitit](https://github.com/facebookincubator/submitit).

## Web UI

**Does closing/stopping `rotari web` stop my jobs?**
No. The web UI is a separate, optional process you start explicitly
(`rotari web`) and never starts or stops the runner itself — closing it has
no effect on any run. It is unrelated to the background supervisor described
below.

**What does the project page's “Project runtime” panel show?**
It shows the persisted runner-lock record for that project, when present, and
whether the local coordinator's socket and PID records exist. It is a
troubleshooting view, not a liveness probe: in particular, the short-lived
advisory state lock is intentionally not inspected.

**Is `rotari web` safe to expose beyond `127.0.0.1`?**
Treat it as an unauthenticated admin surface, not a public dashboard. By
default the `copy`/`change`/`remove`/`cancel`/`clear-run` APIs are enabled
with no authentication; pass `--allow-control=false` for a read-only UI that
rejects them with `403`. Environment variable *values* are never returned by
`/api/state` or `/environment/` (only whether each is set), so secrets in
your shell environment are not exposed. Binding `--host` to anything other
than loopback exposes job logs and (unless `--allow-control=false`)
job-control operations to anyone who can reach that address; only do so on a
trusted network.

## Background server (supervisor)

**Do I need to start a server manually?**
No. Unlike `rotari web`, this supervisor process is never started by hand —
`run`/`add`/`copy` launch it automatically when needed, it is not shown
anywhere in their own output, and it stops on its own once the run finishes
(or once idle, for a server kept up for other reasons). `rotari server
status`/`list`/`shutdown` are for inspection and manual cleanup only — useful
if you want to confirm the supervisor is still finishing a Ctrl-C'd run, or
to force it down.

**Who can control my jobs through the server's Unix socket?**
Anyone who can connect to `<basedir>/server.sock` can `submit`/`cancel`/
`suspend`/`resume`/`run`/`shutdown` — there's no separate authentication.
The socket itself is always created `0600` regardless of other settings, and
on Linux the server additionally checks the connecting peer's UID
(`SO_PEERCRED`) against its own before accepting. This protects against
other unprivileged users on a shared machine, not against root or anyone
who already has your UID's access.

**Can other people on the cluster read my job logs/commands, or is everything locked down now?**
By default, yes — the state directory (`queue.json`, job `output`, working
directories, etc.) keeps rotari's traditional `0755`/`0644` permissions, so
you can still hand a colleague a log path directly, which is common on
shared HPC/lab filesystems. If you'd rather keep that private, set
`ROTARI_PRIVATE_STATE=true` to switch newly created state to owner-only
`0700`/`0600`; it doesn't retroactively change existing directories, and the
server's control socket is always `0600` either way (see above).

**What happens to jobs that are still running if the server/supervisor
process itself is killed (`kill -9`, OOM, host reboot, ...)?**
The jobs themselves are not children of the server in a way that would kill
them too — they (or, for `local`, the wrapper script running them) keep
running to completion and record their own final `status.json`, the same
self-reporting mechanism scheduler executors already relied on. What's lost is
the coordinator that was going to collect all jobs' results into the run's
`summary.json` and clear the queue/lock: the run is left as "interrupted"
(`meta.json` phase stays `running`/`cancelling`), and `add`/`copy`/`run`
against that project are rejected until you resolve it with `unlock` (keep
the queue, mark it recovered) or `reset --recover` (discard the queue). Rotari
does not automatically detect that recovery moment and kill leftover running
jobs for you — `reset --recover` only asks you to confirm they have actually
stopped before discarding state, since it cannot safely assume a process ID
recorded before the crash still refers to the same job (PIDs get reused, and
jobs on another host can't be signaled locally at all). The rejection/
confirmation messages themselves now say how many jobs still look non-terminal
(based on that same `status`/`status.json`) and when the run was last known to
be active, so you have a starting point before deciding whether to `unlock` or
`reset --recover` — but you still have to make that call yourself.

## Timestamps and environment

**What timezone are the times shown by `show`/`web` in?**
Everything is stored in UTC. For display, rotari uses a valid IANA timezone
from `TZ` if set, otherwise Go's local location (the system timezone via
`/etc/localtime` on Linux).

**A CLI option and its matching `ROTARI_*` environment variable are both set — which wins?**
The explicit command-line option always takes precedence over the
environment variable.
