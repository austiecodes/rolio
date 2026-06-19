#!/bin/sh
# Optional session memory recall. Automatic recall stays off unless the user
# opts in with ROLIO_AUTO_RECALL=1 and names the scope. Failures never block
# the session: memory is auxiliary, not a prerequisite.

set -u

if [ "${ROLIO_AUTO_RECALL:-0}" != "1" ]; then
  exit 0
fi

if [ -z "${ROLIO_SCOPE:-}" ]; then
  echo "rolio: ROLIO_AUTO_RECALL is on but ROLIO_SCOPE is not set; skipping recall." >&2
  exit 0
fi

LIMIT="${ROLIO_RECALL_LIMIT:-5}"
QUERY="${ROLIO_RECALL_QUERY:-What conventions, decisions, and lessons should I remember for this session?}"

rolio recall "$QUERY" --scope "$ROLIO_SCOPE" --limit "$LIMIT" 2>/dev/null || exit 0
