#!/usr/bin/env bash
# Records docs/demo-shell.gif and docs/demo-rotari.gif using asciinema + agg.
#
# Requirements:
#   - asciinema (Python package): pip install --user asciinema
#     (or a standalone `asciinema` binary on PATH)
#   - agg (https://github.com/asciinema/agg):
#       cargo install --locked --git https://github.com/asciinema/agg
#
# Usage:
#   scripts/generate-demos.sh
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
output_dir="${repo_dir}/docs"
work_dir=$(mktemp -d)
trap 'rm -rf "${work_dir}"' EXIT INT TERM

require_command() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "required command not found: $1" >&2
        exit 1
    }
}

# asciinema can be a standalone binary or the Python module; prefer whichever is available.
asciinema_cmd=()
if command -v asciinema >/dev/null 2>&1; then
    asciinema_cmd=(asciinema)
elif python3 -m asciinema --version >/dev/null 2>&1; then
    asciinema_cmd=(python3 -m asciinema)
else
    echo "required command not found: asciinema (pip install --user asciinema)" >&2
    exit 1
fi

require_command agg

cols=${ROTARI_DEMO_COLS:-90}
rows=${ROTARI_DEMO_ROWS:-24}
font_size=${ROTARI_DEMO_FONT_SIZE:-14}
theme=${ROTARI_DEMO_THEME:-monokai}

# Build rotari into a directory that comes first on PATH, alongside fake
# `make`/`go` stand-ins so both demos print identical, deterministic output.
bin_dir="${work_dir}/bin"
mkdir -p "${bin_dir}"
if [[ -x "${repo_dir}/rotari" ]]; then
    cp "${repo_dir}/rotari" "${bin_dir}/rotari"
else
    require_command go
    (cd "${repo_dir}" && go build -o "${bin_dir}/rotari" ./cmd/rotari)
fi

# Fake `go` binary: `go test ./...` fails once per working directory, then
# passes after "fixing" (marker file is relative, so each demo is independent).
cat > "${bin_dir}/go" <<'EOF'
#!/bin/sh
marker=./.go-fix-marker
if [ -f "${marker}" ]; then
    echo PASS
else
    touch "${marker}"
    echo "--- FAIL: TestFoo"
    exit 1
fi
EOF
chmod +x "${bin_dir}/go"

record() {
    local name=$1
    local demo_script=$2
    local cast="${work_dir}/${name}.cast"
    local gif="${output_dir}/${name}.gif"

    echo "recording ${name}..."
    rm -f "${cast}"
    "${asciinema_cmd[@]}" rec --overwrite --cols "${cols}" --rows "${rows}" \
        -c "bash ${demo_script}" "${cast}" >/dev/null

    echo "rendering ${gif}..."
    agg --font-size "${font_size}" --theme "${theme}" "${cast}" "${gif}" >/dev/null
}

# --- shell demo: plain background jobs with & / wait --------------------

shell_work_dir="${work_dir}/shell-demo"
mkdir -p "${shell_work_dir}"
cat > "${shell_work_dir}/Makefile" <<'EOF'
build:
	@echo make
EOF
shell_demo="${work_dir}/shell-demo.sh"
cat > "${shell_demo}" <<EOF
#!/usr/bin/env bash
set -uo pipefail
export PATH="${bin_dir}:\$PATH"
cd "${shell_work_dir}"

type_line() {
    local text="\$1"
    printf '\033[32m\$\033[0m '
    local i
    for ((i = 0; i < \${#text}; i++)); do
        printf '%s' "\${text:\$i:1}"
        sleep 0.035
    done
    printf '\n'
    sleep 1.0
}

note() {
    printf '\033[2;37m# %s\033[0m\n' "\$1"
    sleep 1.2
}

type_line 'make > make.log 2>&1 &'
( make ) > make.log 2>&1 &
pid1=\$!

type_line 'go test ./... > test.log 2>&1 &'
( go test ./... ) > test.log 2>&1 &
pid2=\$!
sleep 0.5

type_line 'wait \$pid1; echo "build exit=\$?"'
wait "\$pid1"; echo "build exit=\$?"
sleep 0.8

type_line 'wait \$pid2; echo "unit-tests exit=\$?"'
wait "\$pid2"; echo "unit-tests exit=\$?"
sleep 1.0

note "which log has the failure? no job list, no summary"
type_line 'cat make.log test.log'
cat make.log test.log
sleep 2.0

note "no record is kept once the shell is closed;"
note "you must remember and retype the failed command"
type_line 'go test ./...'
go test ./... || true
sleep 3.0
EOF
chmod +x "${shell_demo}"

# --- rotari demo: add, run, show, and re-run only the failed job ----------

rotari_work_dir="${work_dir}/rotari-demo"
mkdir -p "${rotari_work_dir}"
cat > "${rotari_work_dir}/Makefile" <<'EOF'
build:
	@echo make
EOF

rotari_demo="${work_dir}/rotari-demo.sh"
cat > "${rotari_demo}" <<EOF
#!/usr/bin/env bash
set -uo pipefail
export PATH="${bin_dir}:\$PATH"
export ROTARI_BASEDIR="${work_dir}/fdemo-state"
cd "${rotari_work_dir}"

type_line() {
    local text="\$1"
    printf '\033[32m\$\033[0m '
    local i
    for ((i = 0; i < \${#text}; i++)); do
        printf '%s' "\${text:\$i:1}"
        sleep 0.035
    done
    printf '\n'
    sleep 1.0
}

note() {
    printf '\033[2;37m# %s\033[0m\n' "\$1"
    sleep 1.2
}

# capture output, then reveal it line by line so the reader can follow along
run_slow() {
    "\$@" > "${work_dir}/rotari-cmdout.txt" 2>&1
    local code=\$?
    while IFS= read -r line; do
        printf '%s\n' "\$line"
        sleep 0.18
    done < "${work_dir}/rotari-cmdout.txt"
    return \$code
}

type_line 'rotari add make'
rotari add make
sleep 1.0

type_line 'rotari add go test ./...'
rotari add go test ./...
sleep 1.0

type_line 'rotari run'
run_slow rotari run
job_id=\$(grep -m1 '^  Job:' "${work_dir}/rotari-cmdout.txt" | awk '{print \$2}')
sleep 1.5

note "rotari already knows exactly which job failed"
type_line "rotari show --job-id \${job_id}"
run_slow rotari show --job-id "\${job_id}"
sleep 2.0

note "fix the bug, then re-run only the failed job"
type_line 'rotari retry'
run_slow rotari retry
sleep 3.5
EOF
chmod +x "${rotari_demo}"

record demo-shell "${shell_demo}"
record demo-rotari "${rotari_demo}"

echo "done: ${output_dir}/demo-shell.gif ${output_dir}/demo-rotari.gif"
