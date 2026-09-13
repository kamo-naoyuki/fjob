#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PATH="${script_dir}:${PATH}"
export PATH
export FJOB_BASEDIR=${FJOB_BASEDIR:-"${script_dir}/.fjob-state"}
export FJOB_QUEUE_NAME=${FJOB_QUEUE_NAME:-${1:-demo}}
slurm_options=${FJOB_SLURM_OPTIONS:-}
async=${FJOB_ASYNC:-false}

if ! command -v fjob >/dev/null 2>&1; then
    echo "fjob binary not found in PATH" >&2
    echo "Build it with: go build -o fjob ./cmd/fjob" >&2
    exit 1
fi

fjob check

# Mix local and Slurm jobs in one queue. Slurm options are attached per job.
# The two jobs after prepare can run in parallel with each other.
fjob submit --job-name prepare sh -c 'sleep 1; echo preparation job'
fjob submit --job-name slurm-job --depends-on prepare \
    --backend slurm --sbatch-option "${slurm_options} --cpus-per-task=2" \
    sh -c 'sleep 2; echo Slurm job'
fjob submit --job-name failing-job --depends-on prepare \
    sh -c 'echo failing local job; exit 1'

# Run with separate local and Slurm concurrency limits.
run_args=(--local-concurrency 2 --slurm-max-active 2 --retry 1)
if [[ "${async}" == true ]]; then
    run_args+=(--async)
elif [[ "${async}" != false ]]; then
    echo "FJOB_ASYNC must be true or false" >&2
    exit 1
fi
fjob run "${run_args[@]}"