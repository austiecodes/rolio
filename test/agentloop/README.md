# Agent loop

This directory tests rolio with a real agent. Develop in one agent, then run
`pi` here to see if the guide in `guide.md` makes an agent record and reuse
knowledge.

```sh
make loop                         # scenario record-recall, all steps
STEPS=2-recall make loop          # baseline: recall with an empty store
GUIDE=none make loop              # baseline: no AGENTS.md
PI_MODEL=magpie/zai/glm-5.3 make loop  # other model
PI_ARGS='--thinking low' make loop
```

Each run starts a temporary PostgreSQL container and a rolio server built from
the working tree. Each step is a new pi session in a new workspace outside this
repository. `guide.md` is copied into the workspace as `AGENTS.md`. The prompts
do not mention rolio.

pi uses your `~/.pi/agent` directory for credentials and models. Your global
`~/.pi/agent/AGENTS.md` is thus also loaded. Extensions, skills, and prompt
templates are disabled.

## Scenario layout

```text
scenarios/<name>/<step>/prompt.md       the user message
scenarios/<name>/<step>/workspace/      files copied into the workspace
scenarios/<name>/<step>/expect-store    patterns that the store must contain
scenarios/<name>/<step>/expect-answer   patterns that the last answer must contain
```

Steps run in name order and share one store. Each pattern line is a
case-insensitive extended regular expression.

## Run directory

The script prints the run directory. For each step it contains:

- `events.jsonl`: the full pi event stream
- `rolio-calls.txt`: the shell commands that called rolio
- `tool-counts.txt`: the number of calls for each tool
- `answer.md`: the last assistant message
- `store/all.md`: all documents in the store after the step

`results.txt` contains the PASS and FAIL lines.
