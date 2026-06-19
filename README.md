# Rolio

Explicit long-term memory for people and coding agents. Rolio keeps memories
you can independently read, correct, and delete; nothing is captured
automatically and no session is uploaded.

A memory has a scope (`global` or `project:<key>`), a controlled kind
(note / preference / convention / fact / solution / lesson / plan), a
confidence, an optional source, and content in Markdown. Writes are
conditional: creation only when the ID is absent, update and delete only with
the expected revision. There is no unconditional overwrite.

## Shape

```
rolio CLI ──HTTP v1──> rolio-server ──> PostgreSQL (pgvector)
                            └──> local Qwen3-Embedding-0.6B (Q8_0, llama.cpp)
```

- `rolio-core` — memory value types and validation; no I/O.
- `rolio-server` — storage contract, PostgreSQL adapter, local embedding,
  memory operations, and the HTTP service.
- `rolio-cli` — the single client surface; agents integrate through it.
- `plugins/` — Claude Code and Codex integrations: a skill documenting the CLI
  plus an opt-in session hook. No MCP server.

Clients never open the database and never submit vectors; the server generates
and validates embeddings with one pinned local model. Replacing the model or
the vector dimension is an explicit export-and-rebuild operation.

## Build and run

Requires Rust 1.96, PostgreSQL 13+ with the pgvector extension, and the pinned
model artifact on macOS ARM64.

```bash
python3 scripts/setup-embedding.py          # installs the pinned GGUF (~610 MB)

export ROLO_ADDR=127.0.0.1:8080
export ROLO_TOKEN=<bearer token>
export ROLO_DATABASE_URL=postgres://...     # pgvector-enabled database
cargo run --all-features -p rolio-server
```

The CLI picks its URL and token from `--url` / `--token`, then `ROLO_URL` /
`ROLO_TOKEN`, then `~/.config/rolio/config.toml`.

```bash
echo "Prefer revision conditions over blind writes." | \
  rolio remember --scope project:rolio --kind convention
rolio recall "write discipline" --scope project:rolio --limit 5
rolio list --scope project:rolio
```

## Verify

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets --all-features --locked -- -D warnings
cargo test --workspace --all-features --locked
# Optional, against a real PostgreSQL:
ROLIO_TEST_DATABASE_URL=postgres://... cargo test -p rolio-server --all-features --locked
```

The PostgreSQL and real-model tests skip with a notice when their environment
is absent.
