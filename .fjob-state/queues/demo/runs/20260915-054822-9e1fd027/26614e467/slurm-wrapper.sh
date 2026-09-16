#!/bin/sh
set +e
status_path='/data/store3/kamo/bgjob/jobq/.fjob-state/queues/demo/runs/20260915-054822-9e1fd027/26614e467/status.json'
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
'sh' '-c' 'sleep 2; echo Slurm job'
code=$?
write_status finished "$code"
exit "$code"
