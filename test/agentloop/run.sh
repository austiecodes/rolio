#!/usr/bin/env bash
# Run one agent scenario against a temporary rolio server.
#
# Usage: run.sh [scenario]            (default: record-recall)
#
# Environment:
#   STEPS          space-separated step names; default: all steps in order
#   GUIDE          file copied to each workspace as AGENTS.md; default: guide.md
#                  set GUIDE=none to run without an AGENTS.md
#   PI_MODEL       pi model; default: zai-coding-cn/glm-5.3
#   PI_ARGS        more pi options, for example "--thinking low"
#   STEP_TIMEOUT   seconds for each pi step; default: 900
#   See ../withserver.sh for the server options.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
scenario="${1:-record-recall}"
scenario_dir="$here/scenarios/$scenario"
[ -d "$scenario_dir" ] || { echo "unknown scenario: $scenario" >&2; exit 2; }
if [ -z "${ROLIO_RUN:-}" ]; then
	exec "$here/../withserver.sh" "$scenario" "$0" "$@"
fi

guide="${GUIDE:-$here/guide.md}"
step_timeout="${STEP_TIMEOUT:-900}"
model="${PI_MODEL:-zai-coding-cn/glm-5.3}"
run="$ROLIO_RUN"
for tool in pi jq; do
	command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

# Stop a step that does not complete in time.
with_timeout() {
	local seconds="$1"
	shift
	"$@" &
	local pid=$!
	(sleep "$seconds" && kill "$pid") 2>/dev/null &
	local watchdog=$!
	disown "$watchdog"
	local status=0
	wait "$pid" || status=$?
	pkill -P "$watchdog" 2>/dev/null || true
	kill "$watchdog" 2>/dev/null || true
	return "$status"
}

run_pi() {
	local workspace="$1" out="$2"
	cd "$workspace"
	# shellcheck disable=SC2086
	exec pi --mode json --model "$model" --session-dir "$out/sessions" \
		--no-extensions --no-skills --no-prompt-templates ${PI_ARGS:-} \
		-- "$(cat "$out/prompt.md")" </dev/null >"$out/events.jsonl" 2>"$out/pi.stderr"
}

dump_store() {
	local out="$1"
	mkdir -p "$out"
	rolio glob '**' >"$out/paths.txt" 2>/dev/null || true
	: >"$out/all.md"
	while IFS= read -r path; do
		[ -n "$path" ] || continue
		printf '\n===== %s =====\n' "$path" >>"$out/all.md"
		rolio cat "$path" >>"$out/all.md" 2>&1 || true
	done <"$out/paths.txt"
}

# check <label> <pattern file> <text file>: each line is a case-insensitive ERE.
check() {
	local label="$1" patterns="$2" text="$3" pattern
	[ -f "$patterns" ] || return 0
	while IFS= read -r pattern; do
		[ -n "$pattern" ] || continue
		if grep -Eiq -- "$pattern" "$text"; then
			echo "PASS $label: /$pattern/" | tee -a "$run/results.txt"
		else
			echo "FAIL $label: /$pattern/" | tee -a "$run/results.txt"
		fi
	done <"$patterns"
}

assistant_calls='select(.type=="message_end" and .message.role=="assistant") | .message.content[]? | select(.type=="toolCall")'

steps="${STEPS:-$(cd "$scenario_dir" && ls -d */ | tr -d / | sort | tr '\n' ' ')}"
: >"$run/results.txt"
for step in $steps; do
	src="$scenario_dir/$step"
	out="$run/$step"
	workspace="$run/workspace-$step"
	mkdir -p "$out" "$workspace"
	[ -d "$src/workspace" ] && cp -R "$src/workspace/." "$workspace/"
	[ "$guide" = none ] || cp "$guide" "$workspace/AGENTS.md"
	cp "$src/prompt.md" "$out/prompt.md"

	echo "== step $step"
	started=$(date +%s)
	status=0
	with_timeout "$step_timeout" run_pi "$workspace" "$out" || status=$?
	echo "pi exit $status after $(($(date +%s) - started))s"

	jq -r "$assistant_calls | select(.name==\"bash\") | .arguments.command" "$out/events.jsonl" 2>/dev/null |
		grep -E '(^|[^[:alnum:]_/-])rolio ' >"$out/rolio-calls.txt" || true
	jq -r "$assistant_calls | .name" "$out/events.jsonl" 2>/dev/null | sort | uniq -c >"$out/tool-counts.txt" || true
	jq -rs '[.[] | select(.type=="message_end" and .message.role=="assistant")] | last | .message.content[]? | select(.type=="text") | .text' \
		"$out/events.jsonl" >"$out/answer.md" 2>/dev/null || true
	dump_store "$out/store"

	echo "rolio calls: $(wc -l <"$out/rolio-calls.txt" | tr -d ' ')"
	[ "$status" = 0 ] || echo "FAIL $step: pi exit $status" | tee -a "$run/results.txt"
	# pi exits 0 when the model provider fails, so read the last stop reason.
	model_error="$(jq -rs '[.[] | select(.type=="message_end" and .message.role=="assistant")] | last | .message | select(.stopReason!="stop") | "\(.stopReason): \(.errorMessage // "")"' "$out/events.jsonl" 2>/dev/null || true)"
	[ -z "$model_error" ] || echo "FAIL $step: model did not finish ($model_error)" | tee -a "$run/results.txt"
	check "$step store" "$src/expect-store" "$out/store/all.md"
	check "$step answer" "$src/expect-answer" "$out/answer.md"
done

echo "== summary"
cat "$run/results.txt"
! grep -q '^FAIL' "$run/results.txt"
