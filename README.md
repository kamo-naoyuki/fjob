# jobq

[![Go CI](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml/badge.svg)](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml)

Lightweight local job queue for running commands locally or through Slurm.
It keeps job output and results so failed jobs can be inspected and run again.

## Build

```sh
go build -o jobq ./cmd/jobq
```

## Quick start

```sh
jobq check --queue-name build
jobq submit --queue-name build make
jobq submit --queue-name build go test ./...
jobq run --queue-name build
```

`submit` adds a command. `run` executes the queued commands and waits for
completion. The queue name can be supplied with `--queue-name`,
`JOBQ_QUEUE_NAME`, or omitted to use `default`.

## Slurm

Backend and Slurm options can be set per command:

```sh
jobq submit --queue-name build make
jobq submit --queue-name build \
  --backend slurm \
  --sbatch-option="-p short --cpus-per-task=2" \
  ./heavy-test.sh
jobq run --queue-name build --local-concurrency 4 --slurm-max-active 8
```

Local and Slurm commands may be mixed in the same queue. Use
`--local-concurrency` for local jobs and `--slurm-max-active` for Slurm jobs.

## Async runs

```sh
jobq run --queue-name build --async
jobq wait --queue-name build --run-id RUN_ID
```

The async start message prints commands for checking status and cancelling the
run. `wait` returns the overall run exit code.

## Inspect and recover

Inspect the latest run:

```sh
jobq show --queue-name build
jobq show --queue-name build --failed
jobq show --queue-name build --job-id JOB_ID
```

Run selected jobs from the latest run:

```sh
jobq run --queue-name build --failed
jobq run --queue-name build --unfinished
jobq run --queue-name build --success
jobq run --queue-name build --nonsuccess
```

Stop running jobs without stopping the supervisor:

```sh
jobq cancel --queue-name build
```

Remove saved run logs while keeping queued commands:

```sh
jobq clear --queue-name build
```

## Server management

The supervisor starts automatically when a run needs it and stops after the
run finishes. These commands are mainly useful for inspection and cleanup:

```sh
jobq server status
jobq server list
jobq server shutdown
```

## Example

Build and run the included example:

```sh
go build -o jobq ./cmd/jobq
./example.sh demo
```

The example includes local and Slurm jobs in one queue. Set
`JOBQ_ASYNC=true` to use async mode.
