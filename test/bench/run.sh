#!/usr/bin/env bash
# Run a public question benchmark with pi.
#
# Usage: run.sh <locomo|longmemeval>
#
# Environment:
#   MODE       rolio (default): the history is in rolio
#              files: the history is files in the workspace, without rolio
#   VARIANT    longmemeval only. s (default): about 50 sessions for each
#              question. oracle: only the sessions that contain the answer
#   SCOPES     comma-separated scopes; default: all
#              locomo: conversation IDs, for example conv-26
#              longmemeval: question IDs
#   QUESTION_GROUPS  comma-separated question groups, for example temporal-reasoning
#   COUNT      maximum number of questions, at equal intervals; default: all
#   PARALLEL   number of parallel pi sessions; default: 4
#   PI_MODEL   pi model for the answers and the judge; default: zai-coding-cn/glm-5.3
#   THINKING   pi thinking level for the answers; default: the pi default
#   GUIDE      file copied to the workspace as AGENTS.md in rolio mode
#   OUT        result directory; a run with the same OUT continues that run
#   ROLIO_SUMMARY_URL, ROLIO_SUMMARY_MODEL
#              rolio mode only. The server generates the summaries of the
#              history with this model, and the questions start when all
#              summaries are complete. Default: no summaries
#   SUMMARY_WAIT  maximum time in seconds for the summaries; default: 3600
#   See ../withserver.sh for the other server options.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
name="${1:-}"
mode="${MODE:-rolio}"
case "$mode" in rolio | files) ;; *) echo "unknown MODE: $mode" >&2; exit 2 ;; esac

# Datasets are not part of this repository. Each one is pinned to a revision
# and a checksum.
case "$name" in
locomo)
	# https://github.com/snap-research/locomo, 2024-08-10
	file=locomo10.json
	revision=cbfbc1dba6bc53d00625212a0f22d55ffee7c1fc
	url="https://raw.githubusercontent.com/snap-research/locomo/$revision/data/$file"
	sha256=79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4
	;;
longmemeval)
	# https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned, 2025-09-19
	revision=98d7416c24c778c2fee6e6f3006e7a073259d48f
	case "${VARIANT:-s}" in
	s)
		file=longmemeval_s_cleaned.json
		sha256=d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442
		;;
	oracle)
		file=longmemeval_oracle.json
		sha256=821a2034d219ab45846873dd14c14f12cfe7776e73527a483f9dac095d38620c
		;;
	*) echo "unknown VARIANT: $VARIANT" >&2; exit 2 ;;
	esac
	url="https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/$revision/$file"
	;;
*) echo "usage: run.sh <locomo|longmemeval>" >&2; exit 2 ;;
esac

if [ -z "${ROLIO_RUN:-}" ]; then
	if [ "$mode" = rolio ]; then
		exec "$here/../withserver.sh" "$name-$mode" "$0" "$@"
	fi
	tmp="${TMPDIR:-/tmp}"
	export ROLIO_RUN="${tmp%/}/rolio-test/$(date +%Y%m%d-%H%M%S)-$name-$mode"
	mkdir -p "$ROLIO_RUN"
fi
for tool in pi curl shasum; do
	command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

cache="${XDG_CACHE_HOME:-$HOME/.cache}/rolio-bench/$name-$revision"
data="$cache/$file"
if [ ! -f "$data" ]; then
	echo "== download $file"
	mkdir -p "$cache"
	curl -fSL --progress-bar "$url" -o "$data.part"
	echo "$sha256  $data.part" | shasum -a 256 -c - >/dev/null || { echo "dataset checksum mismatch: $data.part" >&2; exit 1; }
	mv "$data.part" "$data"
fi

out="${OUT:-$ROLIO_RUN/result}"
workspace="$ROLIO_RUN/workspace"
mkdir -p "$out" "$workspace"
bench="$ROLIO_RUN/bench"
(cd "$repo" && go build -o "$bench" ./test/bench)
selection=(--data "$data" --scopes "${SCOPES:-}" --groups "${QUESTION_GROUPS:-}" --count "${COUNT:-0}")

echo "== ingest ($mode)"
if [ "$mode" = rolio ]; then
	"$bench" "$name" ingest "${selection[@]}"
	guide="${GUIDE:-$repo/test/agentloop/guide.md}"
	[ "$guide" = none ] || cp "$guide" "$workspace/AGENTS.md"
	if [ -n "${ROLIO_SUMMARY_URL:-}" ] && [ -n "${ROLIO_SUMMARY_MODEL:-}" ]; then
		echo "== summaries"
		# The root is the last job of the summary queue.
		limit="${SUMMARY_WAIT:-3600}"
		start=$SECONDS
		while :; do
			state="$(rolio summary / 2>/dev/null | sed -n 's/.*"status":"\([a-z]*\)".*/\1/p' || true)"
			[ "$state" = ready ] && break
			if [ "$state" = failed ]; then
				echo "the summary of / failed: $(rolio summary /)" >&2
				exit 1
			fi
			if [ $((SECONDS - start)) -ge "$limit" ]; then
				echo "the summaries are not complete after $limit seconds (status of /: ${state:-unknown}); set SUMMARY_WAIT" >&2
				exit 1
			fi
			sleep 2
		done
		# The root has no summary when the generations below it failed.
		rolio abstract / >/dev/null 2>&1 || { echo "the summary of / is empty; see $ROLIO_RUN/server.log" >&2; exit 1; }
		echo "summaries complete after $((SECONDS - start)) seconds"
		# An attempt that fails and a later attempt that succeeds are in the
		# log too, thus this is not an error.
		failed="$(grep -c 'attempt .* failed' "$ROLIO_RUN/server.log" || true)"
		[ "$failed" = 0 ] || echo "warning: $failed summary generations failed; see $ROLIO_RUN/server.log" >&2
	fi
else
	"$bench" "$name" ingest "${selection[@]}" --dir "$workspace"
fi

echo "== qa"
"$bench" "$name" qa "${selection[@]}" --out "$out" --workspace "$workspace" --mode "$mode" \
	--parallel "${PARALLEL:-4}" --model "${PI_MODEL:-zai-coding-cn/glm-5.3}" --thinking "${THINKING:-}"

echo "== judge"
"$bench" "$name" judge --out "$out" --parallel "${PARALLEL:-4}" --model "${PI_MODEL:-zai-coding-cn/glm-5.3}"

echo "== result"
"$bench" "$name" stat --out "$out" --data "$data"
echo "result directory: $out"
