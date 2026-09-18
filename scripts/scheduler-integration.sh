#!/usr/bin/env bash
set -euo pipefail

executor=${1:-}
runtime=${CONTAINER_RUNTIME:-docker}
workspace=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [[ "$executor" != slurm && "$executor" != pbs ]]; then
    echo "usage: $0 <slurm|pbs>" >&2
    exit 1
fi
if ! command -v "$runtime" >/dev/null 2>&1; then
    echo "container runtime not found: $runtime" >&2
    exit 1
fi

container="rotari-${executor}-$$"
state_dir=$(mktemp -d)
cleanup() {
    "$runtime" rm --force "$container" >/dev/null 2>&1 || true
    rm -rf "$state_dir"
}
trap cleanup EXIT
chmod 0777 "$state_dir"

if [[ "$executor" == slurm ]]; then
    "$runtime" run --detach --name "$container" --hostname slurmctl \
        --interactive --tty --cap-add SYS_ADMIN \
        --volume "$workspace:/workspace" --volume "$state_dir:/state" \
        giovtorres/docker-centos7-slurm:latest >/dev/null
    scheduler_user=root
else
    "$runtime" run --detach --name "$container" --cap-add SYS_RESOURCE \
        --volume "$workspace:/workspace:ro" --volume "$state_dir:/state" \
        naoso5/test-openpbs:latest >/dev/null
    scheduler_user=testuser
fi

for attempt in {1..60}; do
    if [[ "$executor" == slurm ]]; then
        "$runtime" exec "$container" sinfo >/dev/null 2>&1 && break
    elif "$runtime" exec --user "$scheduler_user" "$container" qstat -Bf >/dev/null 2>&1; then
        break
    fi
    if [[ "$attempt" == 60 ]]; then
        "$runtime" logs "$container"
        exit 1
    fi
    sleep 2
done

(cd "$workspace" && go build -o rotari ./cmd/rotari)
rotari=("$runtime" exec --user "$scheduler_user" "$container" /workspace/rotari)
"${rotari[@]}" add --basedir /state --project-name integration \
    --executor "$executor" --job-name array --array 1-2 \
    -- sh -c 'printf "task=%s\n" "$ROTARI_ARRAY_TASK_ID"'
"${rotari[@]}" run --basedir /state --project-name integration --batch-concurrency 2
run_id=$("$runtime" exec --user "$scheduler_user" "$container" find /state/projects/integration/runs -mindepth 1 -maxdepth 1 -type d -print | sed 's#^.*/##' | sort | tail -1)
"${rotari[@]}" show --basedir /state --project-name integration --run-id "$run_id" --logs
for task in 1 2; do
    "$runtime" exec --user "$scheduler_user" "$container" test -s "/state/projects/integration/runs/$run_id"/*-"$task"/status.json
done