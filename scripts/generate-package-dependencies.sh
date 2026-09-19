#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
output_dir=${1:-"${repo_dir}/docs/web-demo/architecture"}
dot_binary=${DOT_BINARY:-dot}
callvis_binary=${CALLVIS_BINARY:-"$(go env GOPATH)/bin/go-callvis"}

if ! command -v "${dot_binary}" >/dev/null 2>&1; then
  echo "Graphviz 'dot' is required to generate the call graph" >&2
  exit 1
fi

if [[ ! -x "${callvis_binary}" ]]; then
  echo "go-callvis not found at ${callvis_binary}; install it before running this script" >&2
	exit 1
fi

mkdir -p "${output_dir}"

echo "generating call graph in ${output_dir}..."
(cd "${repo_dir}" && "${callvis_binary}" \
  -group pkg,type \
  -nostd \
  -format svg \
  -file "${output_dir}/call-graph" \
  ./cmd/rotari)
rm -f "${output_dir}/call-graph.gv"

echo "generating Go documentation in ${output_dir}..."
godoc_text=$(mktemp)
trap 'rm -f "${godoc_text}"' EXIT INT TERM
(cd "${repo_dir}" && go doc -all ./cmd/rotari > "${godoc_text}")
{
  echo '<!doctype html>'
  echo '<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">'
  echo '<title>Rotari Go documentation</title></head><body>'
  echo '<p><a href="./">Back to architecture</a> | <a href="call-graph.svg">Open call graph</a></p><h1>Rotari Go documentation</h1><pre>'
  sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' "${godoc_text}"
  echo '</pre></body></html>'
} > "${output_dir}/godoc.html"

cat > "${output_dir}/index.html" <<'EOF'
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Rotari architecture</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: sans-serif; line-height: 1.5; margin: 2rem auto; max-width: 72rem; padding: 0 1rem; }
    img { height: auto; max-width: 100%; }
  </style>
</head>
<body>
  <p><a href="../">Back to web demo</a></p>
  <h1>Rotari architecture</h1>
  <ul>
    <li><a href="call-graph.svg">Function call graph</a></li>
    <li><a href="godoc.html">Go documentation</a></li>
  </ul>
  <p>The call graph is generated from <code>cmd/rotari</code> during the Pages build.</p>
</body>
</html>
EOF

echo "done"
