#!/bin/sh
set +e
status_path='/data/store3/kamo/bgjob/jobq/.fjob-state/queues/demo/runs/20260915-071546-18bd3e93/f6f3314e6/status.json'
write_status() {
    phase=$1
    code=$2
    tmp="${status_path}.tmp.$$"
    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    if [ "$phase" = "running" ]; then
        printf '{"phase":"running","started_at":"%s"}\n' "$now" > "$tmp"
    else
        printf '{"phase":"%s","exit_code":%s,"finished_at":"%s"}\n' "$phase" "$code" "$now" > "$tmp"
    fi
    mv -f "$tmp" "$status_path"
}
write_status running 0
trap 'write_status cancelled 143; exit 143' TERM
trap 'write_status cancelled 130; exit 130' INT
trap 'write_status cancelled 131; exit 131' QUIT
'sh' '-c' 'echo failing local job; exit 1'
code=$?
write_status finished "$code"
exit "$code"
