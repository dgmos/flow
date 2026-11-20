#!/usr/bin/env bash
# Downloads every public n8n template (https://n8n.io/workflows) into a
# directory as one workflow JSON per file, for the compatibility benchmark:
#
#   scripts/fetch-n8n-templates.sh /tmp/n8n-templates
#   N8N_CORPUS_DIR=/tmp/n8n-templates go test -tags n8ncorpus -run Corpus -v ./internal/n8n
#
# Re-running only fetches templates that are not there yet. Needs curl and
# python3.
set -euo pipefail

dir=${1:?usage: $0 <output-dir>}
api=https://api.n8n.io/api/templates
parallel=${PARALLEL:-8}
mkdir -p "$dir"

total=$(curl -fsS "$api/search?rows=1" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["totalWorkflows"])')
pages=$(( (total + 99) / 100 ))
echo "n8n gallery: $total templates"

for p in $(seq 1 "$pages"); do
  curl -fsS --retry 3 "$api/search?rows=100&page=$p" |
    python3 -I -c 'import json,sys; print("\n".join(str(w["id"]) for w in json.load(sys.stdin)["workflows"]))'
done | sort -un > "$dir/.ids"

fetch() {
  out="$2/$1.json"
  [ -s "$out" ] && return 0
  curl -fsS --retry 3 "$3/workflows/$1" |
    python3 -I -c 'import json,sys; d=json.load(sys.stdin)["workflow"]; json.dump(d.get("workflow", d), sys.stdout)' > "$out.tmp" &&
    mv "$out.tmp" "$out" || rm -f "$out.tmp"
}
export -f fetch
xargs -P "$parallel" -I{} bash -c 'fetch "$@"' _ {} "$dir" "$api" < "$dir/.ids"
echo "saved $(find "$dir" -name '*.json' | wc -l | tr -d ' ') of $(wc -l < "$dir/.ids" | tr -d ' ') templates in $dir"
