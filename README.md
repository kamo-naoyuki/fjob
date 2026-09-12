# jobq

[![Go CI](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml/badge.svg)](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml)

Lightweight local job queue runner.

This is the Go implementation focused on core job management:

- queue commands with `add`
- submit commands directly to queue files with `submit`
- execute queued commands with `run`
- support sync and no-sync modes

## Build

```sh
go build ./cmd/jobq
```

Binary is generated as `./jobq` in the current directory.

## Commands

### Background server

One server owns one state directory. It starts automatically when `run` is
called, receives run control requests over a Unix socket, and stores queue
state and job history in files. `submit` does not contact the server.

```sh
jobq check --queue-name build
jobq submit --queue-name build make
jobq submit --queue-name build --backend slurm --sbatch-option="-p short" go test ./...
jobq run --queue-name build --local-concurrency 4 --slurm-max-active 8
jobq run --queue-name build --backend slurm --sbatch-option="-p short"
jobq cancel --queue-name build
jobq clear --queue-name build
jobq wait --queue-name build --run-id RUN_ID
jobq run --failed --queue-name build
jobq server status
jobq server list
jobq server shutdown
```

The socket and server lease are stored at:

- `<basedir>/server.sock`
- `<basedir>/server.lock`
- `<basedir>/server.pid`

All servers register in the master directory used by `server list`:

- `JOBQ_MASTERDIR`
- `$XDG_STATE_HOME/jobq/master`
- `~/.local/state/jobq/master`

`server list` pings each registered socket and removes stale registry entries.

`server shutdown` is only needed for administration or cleanup. The server
stops automatically when its last run finishes. It also exits after five
minutes without requests as a fallback. `check` is an optional shell
preflight; `run` repeats the running-queue check immediately before execution.

Use `jobq check --server` when an already-running server is also required. It
checks the server but does not start one.

`cancel` stops the running jobs for a queue without stopping the server. Local
jobs receive `SIGTERM`; Slurm jobs are cancelled with `scancel`.

`clear` removes saved run history and logs while keeping the queued commands.
It refuses to run while the queue is active.

For an asynchronous run, wait for completion with the run ID printed by
`run --async`:

```sh
jobq wait --queue-name build --run-id RUN_ID
```

`wait` prints the final counts and failed-job inspection commands, then
returns the run's exit code.

Run only selected jobs from the latest run:

```sh
jobq run --failed --queue-name build
jobq run --unfinished --queue-name build
jobq run --success --queue-name build
jobq run --nonsuccess --queue-name build
```

To inspect the latest run:

```sh
jobq show --queue-name build
jobq show --queue-name build --failed
jobq show --queue-name build --job-id JOB_ID
```

`show` reads saved run files directly and does not contact the server.

### Add a command

```sh
jobq add [--basedir DIR] [--queue-name NAME] <command ...>
```

Example:

```sh
jobq add --queue-name g1 echo hello
jobq add --queue-name g1 sh -c "sleep 2; echo done"
```

Behavior:

- If the same `queue_name` is currently running, `add` fails.
- If the previous run for `queue_name` is already finished, calling `add` starts a new session and clears previous run history for that queue.

### Run queued commands

```sh
jobq run [--basedir DIR] [--queue-name NAME] [--local-concurrency N] [--slurm-max-active N]
```

Options:

- `--local-concurrency N`: max local worker concurrency (default: 8)
- `--slurm-max-active N`: max active Slurm jobs managed by jobq (default: 8)

`run` waits for all jobs to finish and returns the overall exit code. Use
`--async` to start the run and return immediately.

Queue name resolution priority is `--queue-name`, `JOBQ_QUEUE_NAME`, then
`default`.

The first submit with `--backend` establishes the queue default for the
session. A later submit can carry an explicit backend override. A run must
currently resolve to one backend; mixing local and Slurm jobs in one run is
rejected. For Slurm runs, each job is submitted through a generated wrapper. The
wrapper writes an atomic `status.json`; `squeue` monitors active jobs and
`sacct` reconciles jobs whose wrapper status is missing or delayed.
Each `--sbatch-option` value is shellword-parsed, so options with a separate
value can be written as `--sbatch-option="-p short"`.

Behavior:

- Run uses a snapshot of the queue taken at run start.
- `add` during run is rejected.
- If the queue is empty, run fails.
- Run history is kept after completion and is removed when the next `add` starts a new session.

## Storage directory

Default base directory resolution:

1. `--basedir`
2. `JOBQ_BASEDIR`
3. `$XDG_STATE_HOME/jobq`
4. `~/.local/state/jobq`

Data layout:

- `queues/<queue_name>/queue.json`
- `queues/<queue_name>/meta.json`
- `queues/<queue_name>/state.lock`
- `queues/<queue_name>/running.lock`
- `queues/<queue_name>/runs/<run_id>/...`

## Example script

Build the binary and run the included sample:

```sh
go build -o jobq ./cmd/jobq
./example.sh demo
```

The sample queues three commands, runs at most two in parallel, and leaves the
logs and status files under `.jobq-state/queues/demo/`.

To run the same sample through Slurm:

```sh
JOBQ_BACKEND=slurm JOBQ_SLURM_PARTITION=short ./example.sh demo
```

## Current scope

This repository currently implements the core job management path only.
`show`-related UX and compatibility commands are intentionally out of scope for now.
