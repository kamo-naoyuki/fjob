# fjob

[![Go CI](https://github.com/kamo-naoyuki/fjob/actions/workflows/ci.yml/badge.svg)](https://github.com/kamo-naoyuki/fjob/actions/workflows/ci.yml)

Lightweight local job queue for running commands locally or through Slurm.
It keeps job output and results so failed jobs can be inspected and run again.

## Build and installation

```sh
go build -o fjob ./cmd/fjob
```

Check the version:

```sh
fjob version
fjob --version
```

Prebuilt binaries for Linux and macOS are available from the GitHub Releases
page. Go is only required when building from source or using `go install`.

Download a binary for your platform from
[GitHub Releases](https://github.com/kamo-naoyuki/fjob/releases), make it
executable, and put it somewhere on your `PATH`:

```sh
install -m 755 fjob-linux-amd64 ~/.local/bin/fjob
```

Available binaries are `fjob-linux-amd64`, `fjob-linux-arm64`,
`fjob-darwin-amd64`, and `fjob-darwin-arm64`.

With Go installed, you can install directly instead:

```sh
go install github.com/kamo-naoyuki/fjob/cmd/fjob@latest
```

### Shell completion

Completion scripts are available for Bash and Zsh:

```sh
# Install for the default shell reported by $SHELL
fjob completion install

# Select the shell explicitly when running a nested shell
fjob completion install bash
fjob completion install zsh
```

The completion is generated from the CLI metadata used by the program, and
covers subcommands, command options, backend values, run selection values, and
the `server` subcommands. `completion install` updates the shell configuration
idempotently; it does not duplicate an existing fjob completion block. Start a
new shell after installation, or source the shell configuration to apply it to
the current shell.

For manual setup, `fjob completion bash` and `fjob completion zsh` print the
raw completion scripts.

## Quick start

```sh
fjob check --queue-name build
fjob submit --queue-name build --job-name build make
fjob submit --queue-name build --name unit-tests go test ./...
fjob run --queue-name build
```

`submit` adds a command. `run` executes the queued commands and waits for
completion. The queue name can be supplied with `--queue-name`,
`FJOB_QUEUE_NAME`, or omitted. When omitted, if only one queue exists in the state directory, it will be automatically selected; if multiple queues exist, you will be prompted to specify one.
Use `--job-name NAME` (or its `--name NAME` alias) to label a submitted job.
Use `--depends-on NAME` to make a job wait for a named prerequisite. Repeat the
option to specify multiple prerequisites:

```sh
fjob submit --job-name prepare ./prepare.sh
fjob submit --job-name train --depends-on prepare ./train.sh
fjob run
```

Jobs without dependencies run in parallel. Dependencies must refer to named
jobs in the same queue; unknown jobs and dependency cycles are rejected before
the run starts. If a prerequisite fails, dependent jobs are recorded as
`blocked` and are not executed. Retries rerun only failed jobs, not jobs that
already succeeded. `run --failed` likewise reruns failed job bodies without
rerunning successful prerequisites.

## Directory, queue, and run

The state directory contains queues, and each queue contains its current
commands and run history:

```text
<basedir>/
└── queues/
  └── <queue-name>/
    ├── queue.json
    └── runs/
      └── <run-id>/
        ├── commands.json
        ├── summary.json
        └── <job-id>/
          └── output
```

`submit` adds commands to the queue's `queue.json`. It does not create a run.
`run` reads the current queue, creates a new `<run-id>`, and saves the command
snapshot and results under `runs/<run-id>/`. A run is therefore an execution
record, while a queue is the reusable set of commands waiting to be executed.

Run history is not modified when new commands are submitted. The commands in
`queue.json` remain available for the next run, so running the same queue again
will execute them again unless the queue is replaced by a selection such as
`run --failed` or otherwise changed.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `FJOB_BASEDIR` | Base directory for queue state. Overridden by `--basedir`. |
| `FJOB_QUEUE_NAME` | Default queue name. Overridden by `--queue-name`. |
| `FJOB_MASTERDIR` | Directory used by `fjob server list` to find supervisors. |
| `XDG_STATE_HOME` | Base location used when `FJOB_BASEDIR` or `FJOB_MASTERDIR` is not set. |

The resolution order for the state directory is:
1. `--basedir` option
2. `FJOB_BASEDIR` environment variable
3. `./.fjob-state` (if it exists in the current directory)
4. Default location (`$XDG_STATE_HOME/fjob` or `~/.local/state/fjob`)

The resolution logic for the queue name when `--queue-name` is omitted is:
1. `--queue-name` option
2. `FJOB_QUEUE_NAME` environment variable
3. Automatically select if exactly one queue exists in the state directory
4. Default queue name (`default`) if no queues exist yet (if multiple queues exist, an error will prompt you to specify one)

The included `example.sh` also supports:

| Variable | Purpose |
| --- | --- |
| `FJOB_SLURM_OPTIONS` | Common Slurm options used by the example, such as `-p short`. |
| `FJOB_ASYNC` | Set to `true` to run the example asynchronously; defaults to `false`. |

## Slurm

Backend and Slurm options can be set per command:

```sh
fjob submit --queue-name build make
fjob submit --queue-name build \
  --backend slurm \
  --sbatch-option="-p short --cpus-per-task=2" \
  ./heavy-test.sh
fjob run --queue-name build --local-concurrency 4 --slurm-max-active 8 --retry 2
```

Local and Slurm commands may be mixed in the same queue. Use
`--local-concurrency` for local jobs and `--slurm-max-active` for Slurm jobs.
Use `--retry N` to retry failed jobs up to N additional times.
Use `--retry -1` to retry failed jobs indefinitely.

## Async runs

```sh
fjob run --queue-name build --async
fjob wait --queue-name build
```

The async start message prints commands for checking status and cancelling the
run. `wait` returns the overall run exit code.

## Inspect and recover

To inspect the latest run or list all runs:

```sh
fjob show --queue-name build
fjob show --queue-name build --runs
fjob show --queue-name build --failed
fjob show --queue-name build --job-id JOB_ID
fjob show --queue-name build --logs
fjob show --queue-name build --failed-logs
```

`--logs` prints the output log for every job in the selected run.
`--failed-logs` prints logs only for jobs that failed. Both options accept
`--run-id RUN_ID` to inspect a specific run.

Run selected jobs from the latest run:

```sh
fjob run --queue-name build --failed
fjob run --queue-name build --unfinished
fjob run --queue-name build --success
fjob run --queue-name build --nonsuccess
```

Stop running jobs without stopping the supervisor:

```sh
fjob cancel --queue-name build
```

Remove saved run logs while keeping queued commands:

```sh
fjob clear --queue-name build
```

## Server management

The supervisor starts automatically when a run needs it and stops after the
run finishes. These commands are mainly useful for inspection and cleanup:

```sh
fjob server status
fjob server list
fjob server shutdown
```

## Example

Build and run the included example:

```sh
go build -o fjob ./cmd/fjob
./example.sh demo
```

The example includes local and Slurm jobs in one queue. Set
`FJOB_ASYNC=true` to use async mode.
