---
name: memory-remember
description: Save durable decisions, conventions, and lessons to rolio memory once they are confirmed - resolved trade-offs, breakthrough moments, "next time do X" lessons. Suggest saving at valuable moments, not routine work. Also update or forget existing memories that are wrong or obsolete. Not for searching memories.
---

# Remember memory

## When to save (moment detection)

- **Decision**: options were compared; one was chosen, with a reason.
- **Lesson**: "next time do X"; a preventive measure; a pattern recognized.
- **Breakthrough**: long debugging resolved; a root cause was found.
- **Twist**: an unexpected cause-effect; an assumption that proved wrong.
- **Convention**: the user stated how things are done here.

Skip: routine fixes, work in progress, unconfirmed guesses, anything no future
session needs. One to three saves per session is typical; quality over
quantity.

When unsure, ask first: "This seems worth keeping - <one-line essence>. Save it?"

## Quality

Good - atomic and actionable:

- "Go errors use sentinel values with errors.Is; predicate error functions are banned here."
- "Chose table-driven tests: one test function per behavior, shared setup in a helper."

Poor: "Fixed some bugs", a conversation transcript, several topics in one item.

## Commands

Content always arrives on stdin. `--json` prints one machine-readable envelope
on stdout; diagnostics go to stderr.

```bash
# Save
echo "Use uv for all Python dependency management." | \
  rolio remember --scope project:demo --kind convention --confidence 0.9

# Replace the content; the old revision is required
echo "Use uv; never mix pip and uv in one project." | \
  rolio update <memory-id> --revision <revision> --kind convention

# Delete if the revision still matches
rolio forget <memory-id> --revision <revision>
```

Kinds: `note`, `preference`, `convention`, `fact`, `solution`, `lesson`, `plan`.
Confidence: 0.0 to 1.0 in tenths.
Scopes: `global` (personal, cross-project) or `project:<key>`.
Keep content under 16 KiB and one topic per memory; long text belongs in a
file, with the memory holding the pointer and the reason.

## Rules for agents

1. Never write to `global` from a project task unless the user asks for it.
2. Update a memory when it is wrong or incomplete; forget it when it is
   obsolete. Never delete silently when a correction would help future sessions.
3. Exit `4` conflict: the memory changed. The error prints the current record
   (`current <id> revision <rev>`); merge your change into it and retry with
   that revision - no separate read is needed.
4. Exit `5` or `7` on a write has an unknown outcome: `get` the memory back
   before you write again. Do not retry blindly.
5. Exit codes: `0` success · `2` usage · `3` invalid input · `4` conflict ·
   `5` connection or timeout · `6` unauthorized · `7` write outcome unknown.

Full CLI contract: `rolio --help`.
