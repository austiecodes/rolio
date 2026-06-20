#!/usr/bin/env bash
# Calibrate the recall score bands documented in the memory-recall skill.
#
# Seeds a dedicated scope with topic-distinct memories, scores labeled queries
# against them, prints the score distribution per label, then deletes the
# seeded data. Requires a running server and a configured CLI.
#
# Labels:
#   direct      paraphrase of one seeded memory; the top hit must be that memory
#   confusable  same domain (developer tooling), no seeded answer; the top hit
#               should score below the direct band
#   off-domain  unrelated domain; the top hit should score near the floor
#
# Usage: scripts/calibrate-recall-bands.sh
set -euo pipefail

SCOPE=project:calib
declare -a IDS REVS

cleanup() {
  set +e
  local i
  for i in "${!IDS[@]}"; do
    rolio forget "${IDS[$i]}" --revision "${REVS[$i]}" >/dev/null 2>&1
  done
}
trap cleanup EXIT

seed() {
  local out
  out=$(printf '%s\n' "$1" | rolio remember --scope "$SCOPE" --kind convention --confidence 0.9 --json)
  IDS+=("$(jq -r .data.id <<<"$out")")
  REVS+=("$(jq -r .data.revision <<<"$out")")
}

score_top() { # label query
  rolio recall "$2" --scope "$SCOPE" --limit 3 --json |
    jq -r --arg l "$1" '.data.results[0] // {score: -1, id: "none"}
      | [$l, (.score | tostring), .id] | @tsv'
}

# --- seed: 12 topic-distinct memories --------------------------------------
declare -a MEMO=(
  "All Go errors use sentinel values with errors.Is; never predicate functions."
  "HTTP handlers are closure factories that return one Engine; no Server-struct methods."
  "PostgreSQL migrations live in sql/migrations and are embedded into the binary at build time."
  "Run integration tests only after docker compose up postgres; unit tests need no services."
  "The CLI surface is locked at 11 commands; removed verbs never come back."
  "Commit messages follow conventional prefixes such as feat: and fix: with no milestone tags."
  "Use uv for all Python dependency management; never mix pip and uv in one project."
  "Embeddings come from Qwen3 at 1024 dimensions through llama-cpp-2 in the server process."
  "Bearer tokens resolve from --token, then ROLO_TOKEN, then the user config file."
  "A write that fails with exit 5 has an unknown outcome; get the memory before you retry."
  "Keep each memory under 16 KiB and one topic; long text belongs in a file."
  "Session auto-recall stays off unless ROLIO_AUTO_RECALL=1 and ROLIO_SCOPE are both set."
)
for m in "${MEMO[@]}"; do seed "$m"; done

# --- queries: label<TAB>query<TAB>expected seeded index (direct only) -------
declare -a QUERIES=(
  $'direct\thow should I return errors in Go?\t0'
  $'direct\twhere do database schema changes go?\t2'
  $'direct\thow do we run the tests?\t3'
  $'direct\twhat package manager does python use here?\t6'
  $'direct\twhich model produces the embeddings?\t7'
  $'direct\thow are commit messages formatted?\t5'
  $'direct\twhat happens when a write times out?\t9'
  $'direct\thow large can one memory be?\t10'
  $'direct\thow does recall at session start work?\t11'
  $'direct\twhere does the auth token come from?\t8'
  $'direct\thow are HTTP handlers structured?\t1'
  $'direct\twhich verbs does the command line tool expose?\t4'
  $'confusable\thow do I add a new subcommand?\t-1'
  $'confusable\twhat CI runs on pull requests?\t-1'
  $'confusable\twhich port does the server listen on?\t-1'
  $'confusable\thow do I export memories to markdown?\t-1'
  $'confusable\twhat is the docker image tag?\t-1'
  $'confusable\thow is the config file validated?\t-1'
  $'off-domain\tbest recipe for a sourdough starter?\t-1'
  $'off-domain\thow do I train for a marathon?\t-1'
  $'off-domain\twhat year did the Berlin wall fall?\t-1'
  $'off-domain\trecommend a beginner piano etude\t-1'
  $'off-domain\thow do I remove a red wine stain?\t-1'
  $'off-domain\twhat is the cheapest flight to Lisbon?\t-1'
)

# --- run --------------------------------------------------------------------
tmp=$(mktemp)
for q in "${QUERIES[@]}"; do
  IFS=$'\t' read -r label query expect <<<"$q"
  row=$(score_top "$label" "$query")
  IFS=$'\t' read -r l s topid <<<"$row"
  verdict=""
  if [ "$label" = direct ]; then
    if [ "$topid" = "${IDS[$expect]}" ]; then verdict="hit"; else verdict="MISS"; fi
  fi
  printf '%s\t%s\t%s\t%s\n' "$l" "$s" "$verdict" "$query" | tee -a "$tmp"
done

# --- summary -----------------------------------------------------------------
echo
echo "== summary: top-hit score per label =="
awk -F'\t' 'NR>0 && !/summary/ {n[$1]++; if($2>mx[$1]||n[$1]==1)mx[$1]=$2; if(n[$1]==1||$2<mn[$1])mn[$1]=$2; s[$1]+=$2}
  END {for (l in n) printf "%-11s n=%-2d min=%.3f avg=%.3f max=%.3f\n", l, n[l], mn[l], s[l]/n[l], mx[l]}' "$tmp"
echo
echo "== direct queries sorted by score (band boundary candidates) =="
grep '^direct' "$tmp" | sort -t$'\t' -k2 -n | cut -f2,4
echo
echo "== confusable + off-domain sorted by score =="
grep -Ev '^direct' "$tmp" | sort -t$'\t' -k2 -n | cut -f1,2,4
rm -f "$tmp"
