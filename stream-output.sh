#!/usr/bin/env bash
set -euo pipefail

duration=${STREAM_OUTPUT_SECONDS:-60}
interval=${STREAM_OUTPUT_INTERVAL:-1}
started_at=$(date +%s)
count=0

while true; do
    now=$(date +%s)
    elapsed=$((now - started_at))
    if (( elapsed >= duration )); then
        break
    fi
    count=$((count + 1))
    printf 'streaming output: line=%d elapsed=%ss\n' "${count}" "${elapsed}"
    sleep "${interval}"
done

printf 'streaming output: finished lines=%d duration=%ss\n' "${count}" "${duration}"
