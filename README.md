# jobq

[![Go CI](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml/badge.svg)](https://github.com/kamo-naoyuki/jobq/actions/workflows/ci.yml)

Lightweight local job queue for running commands locally or through Slurm.
It keeps job output and results so failed jobs can be inspected and run again.

## Build

```sh
go build -o jobq ./cmd/jobq
```

Check the version:

```sh
jobq version
jobq --version
```

Prebuilt binaries for Linux and macOS are available from the GitHub Releases
page. Go is only required when building from source or using `go install`.

Download a binary for your platform from
[GitHub Releases](https://github.com/kamo-naoyuki/jobq/releases), make it
executable, and put it somewhere on your `PATH`:

```sh
chmod +x jobq-linux-amd64
install -m 755 jobq-linux-amd64 ~/.local/bin/jobq
```

Available binaries are `jobq-linux-amd64`, `jobq-linux-arm64`,
`jobq-darwin-amd64`, and `jobq-darwin-arm64`.

With Go installed, you can install directly instead:

```sh
go install github.com/kamo-naoyuki/jobq/cmd/jobq@latest
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

## Environment variables

| Variable | Purpose |
| --- | --- |
| `JOBQ_BASEDIR` | Base directory for queue state. Overridden by `--basedir`. |
| `JOBQ_QUEUE_NAME` | Default queue name. Overridden by `--queue-name`. |
| `JOBQ_MASTERDIR` | Directory used by `jobq server list` to find supervisors. |
| `XDG_STATE_HOME` | Base location used when `JOBQ_BASEDIR` or `JOBQ_MASTERDIR` is not set. |

The included `example.sh` also supports:

| Variable | Purpose |
| --- | --- |
| `JOBQ_SLURM_OPTIONS` | Common Slurm options used by the example, such as `-p short`. |
| `JOBQ_ASYNC` | Set to `true` to run the example asynchronously; defaults to `false`. |

## Slurm

Backend and Slurm options can be set per command:

```sh
jobq submit --queue-name build make
jobq submit --queue-name build \
  --backend slurm \
  --sbatch-option="-p short --cpus-per-task=2" \
  ./heavy-test.sh
jobq run --queue-name build --local-concurrency 4 --slurm-max-active 8 --retry 2
```

Local and Slurm commands may be mixed in the same queue. Use
`--local-concurrency` for local jobs and `--slurm-max-active` for Slurm jobs.
Use `--retry N` to retry failed jobs up to N additional times.
Use `--retry -1` to retry failed jobs indefinitely.

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
