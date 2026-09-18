#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
PATH="${repo_dir}:${PATH}"
export PATH
export ROTARI_BASEDIR=${ROTARI_BASEDIR:-"${repo_dir}/.rotari-state"}
export ROTARI_PROJECT_NAME=${ROTARI_PROJECT_NAME:-${1:-demo}}
async=${ROTARI_RUN_ASYNC:-false}

if ! command -v rotari >/dev/null 2>&1; then
    echo "rotari binary not found in PATH" >&2
    echo "Build it with: go build -o rotari ./cmd/rotari" >&2
    exit 1
fi

# This is a normal preflight check. Recovery prompts are shown only when a
# previous runner left this project interrupted; normal runs do not need them.
rotari check

# Mix local and Slurm jobs in one queue. Executor options are attached per job.
# The two jobs after prepare can run in parallel with each other.
rotari add --job-name prepare --executor local sh -c 'sleep 1; echo preparation job'
rotari add --job-name slurm-job --depends-on prepare \
    --executor slurm --array 1-2 --executor-option "--cpus-per-task=2" \
    sh -c 'sleep 2; echo "Slurm array task ${ROTARI_ARRAY_TASK_ID}"'
rotari add --job-name failing-job --depends-on prepare \
    --executor local \
    sh -c 'echo failing local job; exit 1'

# Run with separate local and batch- executor concurrency limits.
run_args=(--local-concurrency 2 --batch-concurrency 2 --retry 1)
if [[ "${async}" == true ]]; then
    run_args+=(--async)
elif [[ "${async}" != false ]]; then
    echo "ROTARI_RUN_ASYNC must be true or false" >&2
    exit 1
fi
rotari run "${run_args[@]}"
