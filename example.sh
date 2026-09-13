#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PATH="${script_dir}:${PATH}"
export PATH
export JOBQ_BASEDIR=${JOBQ_BASEDIR:-"${script_dir}/.jobq-state"}
export JOBQ_QUEUE_NAME=${JOBQ_QUEUE_NAME:-${1:-demo}}
slurm_options=${JOBQ_SLURM_OPTIONS:-}
async=${JOBQ_ASYNC:-false}

if ! command -v jobq >/dev/null 2>&1; then
    echo "jobq binary not found in PATH" >&2
    echo "Build it with: go build -o jobq ./cmd/jobq" >&2
    exit 1
fi

jobq check

# Mix local and Slurm jobs in one queue. Slurm options are attached per job.
jobq submit sh -c 'sleep 1; echo local job'
jobq submit --backend slurm --sbatch-option "${slurm_options} --cpus-per-task=2" sh -c 'sleep 2; echo Slurm job'
jobq submit sh -c 'echo failing local job; exit 1'

# Run with separate local and Slurm concurrency limits.
run_args=(--local-concurrency 2 --slurm-max-active 2 --retry 1)
if [[ "${async}" == true ]]; then
    run_args+=(--async)
elif [[ "${async}" != false ]]; then
    echo "JOBQ_ASYNC must be true or false" >&2
    exit 1
fi
jobq run "${run_args[@]}"