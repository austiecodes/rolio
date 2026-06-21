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
4. **Statistics.** Accuracy for each question group, tokens, time, the number
   of tool calls, and the evidence statistics.

A scope is the history that belongs to a question. In LoCoMo, a scope is one of
the 10 conversations. In LongMemEval, each question has its own history, thus
each question is a scope.

`MODE=files` puts the same Markdown documents in the workspace directory and
does not start rolio. It shows what the agent can do with plain files.

`COUNT` selects questions at equal intervals, and only the history of the
selected questions is ingested.

The answers are in `qa.jsonl` and the grades in `judged.jsonl`. Set `OUT` to
continue an interrupted run: questions that have an answer are skipped.

## Evidence statistics

Each dataset tells which turns of the history contain the answer of a
question. These are the evidence turns. The statistics show if the agent saw
them, and they separate two causes of a wrong answer: the agent did not see
the evidence, or it saw the evidence and did not use it correctly.

`traces/<question>.jsonl` has the tool calls, the tool results, and the
assistant messages of pi for each question. The `stat` step calculates the
statistics from the traces and the dataset. Thus you can change the rules and
run `bench <dataset> stat --out <directory> --data <dataset file>` again
without a new run. The traces contain each tool result, thus a full run
writes much data.

The agent frequently puts many commands in one call, for example
`rolio search x; rolio tree /`. Thus the rules use the text of the tool
results and not the text of the commands.

- **turns seen**: the part of the evidence turns for which a tool result
  contains text of the turn. For LoCoMo this text is the turn ID that each
  line of a document has, for example `(D1:3)`. For LongMemEval it is the
  first 80 bytes of the turn or of one of its long lines.
- **all turns seen**: the part of the questions for which the agent saw all
  evidence turns.
- **accuracy**: the accuracy of the questions with all evidence turns seen,
  and of the other questions.
- **listed by a search**: the part of the evidence documents that were a line
  in a result of `rolio search`. A search result shows the path and the start
  of a document, not the text that agrees with the query.
- **results of rolio search**: the number of tool results that contain the
  output of `rolio search`, the part of them in which no search listed a
  document, the part that came after such a result, and the part of the
  questions that had such a result.

Limits:

- A turn is seen when its text is in a tool result. This does not tell if the
  model used it. A tool result that pi made shorter does not contain all text
  of the command output.
- The snippet of a search result is the start of a document and not the text
  that the search found. A turn in a snippet is not seen.
- For LoCoMo, a turn is seen when its ID is in a tool result. A command that
  cuts the lines can show the ID without the full text of the turn.
- For LongMemEval, a result that shows only a short line of a long turn does
  not count, and a result that shows a different line of the turn than the
  line with the answer counts. A turn of which all lines are shorter than 40
  bytes is not in the statistics.
- A search of which the output goes through a different command, for example
  `rolio search x | awk '{print $1}'`, is not counted when that command
  changes the lines.
- A tool result with the output of two searches is one result. It is without
  a document only when no search in it listed a document.
- `rolio search --json` has a different output. It is not in the search
  statistics, and a turn in its snippets can count as seen.
- Abstention questions of LongMemEval, and questions without usable evidence
  in the dataset, are not in the evidence statistics.
- The agent can read files that are not in the workspace. Make sure that the
  computer has no other copy of the dataset in a location that the agent can
  find.

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
