# Rolio

Explicit long-term memory for people and coding agents. A memory is a scoped
note you can read, correct, and delete; nothing is captured automatically.
Writes are conditional (`If-None-Match: *` to create, `If-Match: <revision>`
to change), so nothing is overwritten silently.

```
rolio CLI ──HTTP v1──> rolio-server ──> PostgreSQL (pgvector)
                            └──> local Qwen3-Embedding-0.6B (Q8_0, llama.cpp)
```

## Install

One command builds and installs the CLI, fetches the pinned embedding model
(~610 MB), and starts PostgreSQL plus rolio-server with docker compose:

```bash
git clone <this-repository> && cd rolio
./scripts/install.sh
```

Prerequisites: [Rust](https://rustup.rs) (rust-toolchain.toml pins 1.96.0),
Docker with compose v2, and python3. The installer is idempotent — re-run it
after pulling changes.

It prints the server URL and a generated bearer token; keep them in your
environment:

```bash
export ROLO_URL=http://127.0.0.1:8080
export ROLO_TOKEN=<token from .env>
```

The stack keeps data in the `pgdata` volume and the model in
`.native/embedding/`. PostgreSQL is not published to the host; only the
server listens on `127.0.0.1:8080`. Logs: `docker compose logs -f
rolio-server`; stop: `docker compose down`; wipe the database: `docker
compose down -v`.

### Manual steps

Install only the CLI from source:

```bash
cargo install --path crates/rolio-cli --locked    # binary: ~/.cargo/bin/rolio
```

Fetch the pinned model (the server refuses any other artifact):

```bash
python3 scripts/setup-embedding.py
```

Run the server with compose (reads `ROLIO_TOKEN` from `.env`):

```bash
docker compose up -d --build
```

Run the server outside Docker instead (PostgreSQL 13+ with pgvector):

```bash
export ROLO_ADDR=127.0.0.1:8080
export ROLO_TOKEN=<token>
export ROLO_DATABASE_URL=postgres://...
export ROLO_ARTIFACTS=.native/embedding/qwen3-embedding-0.6b   # default
cargo run --all-features -p rolio-server
```

## CLI

Content always arrives on stdin. `--json` prints one machine-readable
envelope on stdout; diagnostics go to stderr. URL and token resolve from
`--url` / `--token`, then `ROLO_URL` / `ROLO_TOKEN`, then
`~/.config/rolio/config.toml` (`url`, `token`, `timeout_secs`).

```bash
echo "Use uv for all Python dependency management." | \
  rolio remember --scope project:demo --kind convention --confidence 0.9

rolio recall "how do we manage dependencies?" --scope project:demo --limit 5

rolio get <memory-id>
rolio list --scope project:demo

echo "Use uv; never mix pip and uv in one project." | \
  rolio update <memory-id> --revision <revision> --kind convention

rolio forget <memory-id> --revision <revision>
```

Kinds: `note preference convention fact solution lesson plan`. Scopes:
`global` (personal, cross-project) or `project:<key>`. Confidence is 0.0–1.0
in tenths and only breaks equal-score ties.

Exit codes: `0` success · `2` usage · `3` invalid input · `4` conflict (read
again, then retry) · `5` connection or timeout · `6` unauthorized · `7` write
outcome unknown (read the memory back before writing again).

## Plugins

Both plugins are thin: two skills that document the CLI by direction
(`memory-recall` for reads, `memory-remember` for writes) and an optional
SessionStart hook that recalls project memories. The hook stays off unless
`ROLIO_AUTO_RECALL=1` and `ROLIO_SCOPE` are set. Nothing else is configured;
there is no MCP server.

### Claude Code

Try it in one session:

```bash
claude --plugin-dir ./plugins/claude-code
```

Install it persistently from the local marketplace in this repository
(`claude plugin validate ./plugins/claude-code` checks the layout):

```
/plugin marketplace add ./plugins
/plugin install rolio@rolio
```

Then start a new session; the skills are available as `/rolio:memory-recall`
and `/rolio:memory-remember`.

### Codex

Add the local marketplace and install the plugin from it:

```bash
codex plugin marketplace add ./plugins
codex plugin add rolio@rolio
```

Start a new Codex session, review the bundled hook when Codex asks you to
trust it, and the skills are available. To use the skills without the plugin
bundle, copy `plugins/codex/skills/memory-recall/` and
`plugins/codex/skills/memory-remember/` into your Codex skills directory.

## Development

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo test --workspace --all-features --locked
# Against a real PostgreSQL (pgvector):
ROLIO_TEST_DATABASE_URL=postgres://... cargo test -p rolio-server --all-features --locked
# Against the pinned model (ignored by default):
cargo test -p rolio-server --all-features --locked real_model_ -- --ignored --test-threads=1
```

Crates: `rolio-core` (value types), `rolio-server` (storage contract,
PostgreSQL adapter, local embedding, HTTP), `rolio-cli` (the client).
Vectors travel through the pgvector text protocol handled inside the
adapter; the server never trusts client-supplied vectors.
