# Benchmarks

These benchmarks measure rolio on public datasets. `../agentloop` is the quick
test for development; use it first.

The benchmark selection follows OpenViking (`benchmark/` at commit `e6418c42`,
2026-06-19). The code in this directory is written for rolio and does not
contain OpenViking code. Datasets are downloaded at pinned revisions into
`~/.cache/rolio-bench` and are not part of this repository.

| Benchmark | Measures | Questions | State |
| --- | --- | --- | --- |
| LoCoMo | facts from long conversations between two persons | 1540 | available |
| LongMemEval | facts from the chat history of one user | 500 | available |
| tau2-bench | task success with experience from earlier tasks | | not available |

```sh
make bench-locomo
make bench-longmemeval

# small runs
SCOPES=conv-26 COUNT=20 make bench-locomo
COUNT=20 make bench-longmemeval
QUESTION_GROUPS=temporal-reasoning COUNT=10 make bench-longmemeval

# baseline without rolio
MODE=files make bench-locomo
```

## Procedure

1. **Ingest.** Each conversation session becomes one Markdown document,
   `/conversations/<scope>/session-NN.md`. The agent does not write them.
2. **Answer.** Each question starts a new pi session. The prompt gives the
   location of the history, the current date, and the question.
3. **Judge.** A model grades each answer against the gold answer as correct or
   wrong. The judge has no tools and no context files.
4. **Statistics.** Accuracy for each question group, tokens, time, and the
   number of tool calls.

A scope is the history that belongs to a question. In LoCoMo, a scope is one of
the 10 conversations. In LongMemEval, each question has its own history, thus
each question is a scope.

`MODE=files` puts the same Markdown documents in the workspace directory and
does not start rolio. It shows what the agent can do with plain files.

`COUNT` selects questions at equal intervals, and only the history of the
selected questions is ingested.

The answers are in `qa.jsonl` and the grades in `judged.jsonl`. Set `OUT` to
continue an interrupted run: questions that have an answer are skipped.

## Datasets

- **LoCoMo.** Category 5 (adversarial questions) has no gold answer and is
  excluded.
- **LongMemEval.** `VARIANT=s` (default) has about 50 sessions for each
  question and is a 277 MB download. `VARIANT=oracle` has only the sessions
  that contain the answer; use it to test the procedure. Questions with an ID
  that ends in `_abs` have no answer in the history: the correct response is to
  say so. The documents are named by their position in time, because the
  session IDs of the dataset show which sessions contain the answer.

The results are not directly comparable to published reports: the model, the
agent, and the judge prompts are different.
