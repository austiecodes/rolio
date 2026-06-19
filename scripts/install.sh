#!/bin/sh
# One-command install: build and install the rolio CLI locally, fetch the
# pinned embedding model, then start PostgreSQL and rolio-server with
# docker compose. Safe to re-run; every step is idempotent.

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

fail() {
  echo "install: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$2"
}

need cargo "cargo not found; install Rust from https://rustup.rs (rust-toolchain.toml pins 1.96.0)"
need docker "docker not found; install Docker Desktop or an equivalent engine"
python3 --version >/dev/null 2>&1 || fail "python3 not found (used to fetch the pinned model)"

if ! docker compose version >/dev/null 2>&1; then
  fail "docker compose v2 not available"
fi

echo "==> Building and installing the rolio CLI into ~/.cargo/bin"
cargo install --path crates/rolio-cli --locked
command -v rolio >/dev/null 2>&1 || fail "rolio was installed but ~/.cargo/bin is not on PATH"

echo "==> Fetching the pinned embedding model (~610 MB, skipped when verified)"
python3 scripts/setup-embedding.py

if [ ! -f .env ]; then
  printf 'ROLIO_TOKEN=%s\n' "$(uuidgen 2>/dev/null || python3 -c 'import uuid; print(uuid.uuid4())')" > .env
  echo "==> Generated a new bearer token in .env"
fi

echo "==> Starting PostgreSQL and rolio-server (first build takes a few minutes)"
docker compose up -d --build

echo "==> Waiting for the server to become ready"
i=0
while [ "$i" -lt 60 ]; do
  if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 2
done
curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 || fail "server did not become ready; inspect: docker compose logs rolio-server"

TOKEN=$(sed -n 's/^ROLIO_TOKEN=//p' .env)
cat <<INSTALL

Rolio is running.

  export ROLO_URL=http://127.0.0.1:8080
  export ROLO_TOKEN=$TOKEN

  echo 'First memory.' | rolio remember --scope global
  rolio recall 'first' --scope global

Logs:      docker compose logs -f rolio-server
Stop:      docker compose down
Reset DB:  docker compose down -v
INSTALL
