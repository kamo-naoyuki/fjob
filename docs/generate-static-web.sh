#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
output_dir=${1:-"${script_dir}/web-demo"}
work_dir=$(mktemp -d)
trap 'rm -rf "${work_dir}"' EXIT INT TERM

binary="${work_dir}/rotari"
state_dir="${work_dir}/state"
go_binary=${GO_BINARY:-/usr/bin/go}

if [[ ! -x "${go_binary}" ]]; then
	echo "Go compiler not found at ${go_binary}; set GO_BINARY to its absolute path" >&2
	exit 1
fi

echo "building rotari..."
(cd "${repo_dir}" && "${go_binary}" build -o "${binary}" ./cmd/rotari)

export ROTARI_BASEDIR="${state_dir}"
export ROTARI_PROJECT_NAME=demo

"${binary}" check
"${binary}" add --job-name prepare sh -c 'echo preparation complete'
"${binary}" add --job-name train --depends-on prepare sh -c 'echo training complete'
"${binary}" add --job-name failed sh -c 'echo validation failed; exit 1'
"${binary}" run --run-name "Demo run" || true
first_run_id=$(find "${state_dir}/projects/demo/runs" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort | tail -n 1)
if [[ -z "${first_run_id}" ]]; then
	echo "first run was not created" >&2
	exit 1
fi

# Fix the failing job, then retry only it. "prepare" and "train" already
# succeeded, so they are carried forward from the Demo run instead of being
# re-executed: the retry run's page links back to their original output.
"${binary}" change --job-name failed -- sh -c 'echo validation now passes'
"${binary}" run --failed --run-name "Retry run"

# Keep a useful current queue in the demo so the queue page is not empty.
run_count=$(find "${state_dir}/projects/demo/runs" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | wc -l)
if [[ "${run_count}" -lt 2 ]]; then
	echo "expected two demo runs, found ${run_count}" >&2
	exit 1
fi
"${binary}" copy --run-id "${first_run_id}" --failed --unfinished --overwrite

echo "generating static pages in ${output_dir}..."
"${binary}" web --static-dir "${output_dir}"
echo "done"
