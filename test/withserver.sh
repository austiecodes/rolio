#!/usr/bin/env bash
# Run a command against a temporary rolio server and database.
#
# Usage: withserver.sh <name> <command> [arguments]
#
# The command gets these variables:
#   ROLIO_SERVER   address of the temporary server
#   ROLIO_RUN      new run directory outside the repository
#   PATH           starts with the directory of the built rolio binary
#
# Environment:
#   ROLIO_LANGUAGE  zh or en; default: en
#   ROLIO_TEST_PORT port of the temporary server; default: 4857
#   KEEP=1          do not stop the server and the database at exit
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
name="$1"
shift
port="${ROLIO_TEST_PORT:-4857}"
language="${ROLIO_LANGUAGE:-en}"

for tool in docker curl go; do
	command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 2; }
done

# Run directories must be outside this repository. pi loads AGENTS.md from all
# parent directories, and the repository AGENTS.md is not part of a test.
tmp="${TMPDIR:-/tmp}"
run="${tmp%/}/rolio-test/$(date +%Y%m%d-%H%M%S)-$name"
mkdir -p "$run/bin"
container="rolio-test-$$"
server_pid=""

cleanup() {
	if [ "${KEEP:-}" = 1 ]; then
		echo "kept: server pid $server_pid, container $container"
		return
	fi
	[ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
	docker stop "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== build"
(cd "$repo" && go build -o "$run/bin/rolio" ./cmd/rolio && go build -o "$run/bin/rolio-server" ./cmd/rolio-server)

echo "== database"
docker run -d --rm --name "$container" -e POSTGRES_PASSWORD=rolio-test-only -p 127.0.0.1::5432 postgres:18-alpine >/dev/null
pg_port="$(docker port "$container" 5432/tcp | sed 's/.*://')"
# The image first runs a setup server on the Unix socket only. Wait for TCP.
for _ in $(seq 1 60); do
	docker exec "$container" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 && break
	sleep 0.5
done

echo "== server"
if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
	echo "port $port is in use; set ROLIO_TEST_PORT" >&2
	exit 2
fi
cat >"$run/rolio-server.toml" <<TOML
language = "$language"
addr = "127.0.0.1:$port"

[backend.postgres]
dsn = "postgres://postgres:rolio-test-only@127.0.0.1:$pg_port/postgres?sslmode=disable"
schema = "rolio"
TOML
"$run/bin/rolio-server" --config "$run/rolio-server.toml" >"$run/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 60); do
	curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break
	kill -0 "$server_pid" 2>/dev/null || { cat "$run/server.log" >&2; exit 1; }
	sleep 0.5
done

export ROLIO_SERVER="http://127.0.0.1:$port"
export ROLIO_RUN="$run"
export PATH="$run/bin:$PATH"
status=0
"$@" || status=$?
echo "run directory: $run"
exit "$status"
