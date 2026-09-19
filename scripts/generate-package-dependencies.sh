#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
output_dir=${1:-"${repo_dir}/docs/web-demo/architecture"}
dot_binary=${DOT_BINARY:-dot}

if ! command -v "${dot_binary}" >/dev/null 2>&1; then
	echo "Graphviz 'dot' is required to generate the dependency graph" >&2
	exit 1
fi

mkdir -p "${output_dir}"

echo "generating package dependency graph in ${output_dir}..."
(cd "${repo_dir}" && {
  echo 'digraph rotari {'
  echo '  graph [rankdir=LR, nodesep=0.35, ranksep=0.8];'
  echo '  node [shape=box, style="rounded,filled", fillcolor=paleturquoise];'
  go list -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/rotari |
    awk '{ source=$1; for (field=2; field<=NF; field++) printf "  \"%s\" -> \"%s\";\n", source, $field }'
  echo '}'
} | "${dot_binary}" -Tsvg > "${output_dir}/package-dependencies.svg")

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
  <h1>Rotari package dependencies</h1>
  <p>This graph is generated from the direct imports of the <code>cmd/rotari</code> Go package during the Pages build.</p>
  <p><img src="package-dependencies.svg" alt="Go package dependency graph"></p>
</body>
</html>
EOF

echo "done"