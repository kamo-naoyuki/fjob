#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
PATH="${repo_dir}:${PATH}"
export PATH
export ROTARI_BASEDIR=${ROTARI_BASEDIR:-"${repo_dir}/.rotari-state"}
export ROTARI_PROJECT_NAME=${ROTARI_PROJECT_NAME:-${1:-demo}}

# This is a normal preflight check. Recovery prompts are shown only when a
# previous runner left this project interrupted; normal runs do not need them.
rotari check

# Stable markers outside any run directory: these jobs fail on their first
# attempt and succeed afterwards, so "rotari retry" below has a real
# failure to recover from.
rm -f "${ROTARI_BASEDIR}/example-array-task-1-marker" "${ROTARI_BASEDIR}/example-failing-job-marker"

# Mix local and Slurm jobs in one queue. Executor options are attached per job.
# The two jobs after prepare can run in parallel with each other.
rotari add --job-name prepare --executor local sh -c 'sleep 1; echo preparation job'
rotari add --job-name slurm-job --depends-on prepare \
    --executor slurm --array 1-2 --executor-option "--cpus-per-task=2" \
    sh -c 'sleep 2; echo "Slurm array task ${ROTARI_ARRAY_TASK_ID}"
marker="${ROTARI_BASEDIR}/example-array-task-1-marker"
if [ "${ROTARI_ARRAY_TASK_ID}" = "1" ] && [ ! -f "${marker}" ]; then touch "${marker}"; exit 1; fi'
rotari add --job-name failing-job --depends-on prepare \
    --executor local \
    sh -c 'echo failing local job
marker="${ROTARI_BASEDIR}/example-failing-job-marker"
if [ ! -f "${marker}" ]; then touch "${marker}"; exit 1; fi'

# Run with separate local and batch executor concurrency limits. Array task
# 1 and failing-job fail on this first attempt, so the run reports a
# non-zero exit code; continue instead of aborting the script.
run_args=(--local-concurrency 2 --batch-concurrency 2)
rotari run "${run_args[@]}" || true

# By default, "rotari retry" (run --failed --unfinished) re-executes only
# failed/unfinished jobs, and for an array job only its failed tasks: task 1
# runs again here, while task 2 and "prepare" already succeeded and are
# carried forward from the first run instead of executing again. Both
# markers now exist, so every re-executed job succeeds this time.
rotari retry
