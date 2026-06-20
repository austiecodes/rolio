# Memory workflow roadmap

The target workflow: agents recall proactively at the right moments, distill
and save durable insights at valuable moments, and correct what they stored.
The CLI surface for agents shrinks to four commands: `remember`, `recall`,
`update`, `forget`.

Locked decisions:

- `forget` stays. Explicit deletion is a design principle.
- `update` stays. A correction must preserve the memory identity and
  timestamps; forget-plus-remember loses both.
- `get` and `list` go away once their remaining uses are covered (P1, P2).
- HTTP endpoints stay even when the CLI drops a command; the wire protocol is
  not the agent surface.

## Done

- 2026-06-20: skill split. `memory-recall` and `memory-remember` replace the
  single `rolio` skill; descriptions are trigger-oriented and mutually
  exclusive. Score bands calibrated with `scripts/calibrate-recall-bands.sh`
  (direct 0.57-0.77, confusable 0.42-0.60, off-domain 0.24-0.39; direct and
  confusable overlap, so the bands are soft guidance).
- 2026-06-20: conflict envelope. `revision_conflict` and `already_exists`
  responses carry `error.current`, the current record, fetched best effort by
  the server. A vanished record still yields the plain conflict. The CLI
  human mode prints `current <id> revision <rev>` under the error line; JSON
  mode prints the envelope verbatim.

## P1 - drop `get` (agent surface 6 to 5)

`recall` results already carry `id`, `revision`, and full `content`, and
conflicts now carry the current record. Nothing needs a read by ID alone.

- [ ] Update the skills: the exit 4 rule becomes "the error prints the
      current record; merge and retry with that revision".
- [ ] Exit 5/7 recovery switches from `rolio get` to two probes:
      `recall` the topic to re-find the record, or re-run `remember --id`
      (an `already_exists` error proves the earlier write landed).
- [ ] Remove `Command::Get` and its client path from the CLI.
- [ ] Remove the `rolio get` reference in the transport-failure hint in
      `crates/rolio-cli/src/main.rs`.

Backend: none.

## P2 - drop `list` (agent surface 5 to 4)

Enumeration is maintenance, not an agent flow. Scope audit moves to `psql`
until a dedicated command earns its place in the locked surface.

- [ ] Decide the audit story: accept `psql`-only auditing, or design a
      `rolio audit` command later.
- [ ] Remove `Command::List` and the cursor plumbing from the CLI.

Backend: none for the removal itself.

## P3 - working-memory briefing

The missing piece: a ranked digest per scope (recent updates, high-confidence
items, unresolved flags) instead of raw enumeration.

- [ ] `GET /v1/briefing?scopes=...` built on existing list and search
      machinery, ranked by `updated_at` and `confidence`. No schema change.
- [ ] CLI `rolio brief --scope project:x` prints the digest.
- [ ] The SessionStart hook prefers the briefing when it exists.

Backend: one read-only endpoint; no store change.

## P4 - thread storage (optional, largest)

Raw session transcripts as a separate data class next to memories; distill
into memories on demand.

- [ ] New `threads` table and endpoints; imports read agent session files.
- [ ] Distill flow: thread search, then `remember` the durable part.

Backend: schema migration plus a new service path. Defer until P3 proves the
briefing useful.
