## Rolio knowledge base

`rolio` is a shared knowledge tree for all your projects. It keeps knowledge
that must stay available after this session and outside this repository.

### Read before you investigate

Before you investigate an unfamiliar system, API, or recurring problem, look
for existing knowledge:

- `rolio search "<words>"` finds documents in the full tree. For each
  document it shows the lines that match. Frequently these lines are the
  answer to a question about one fact.
- The lines under a result are excerpts, a maximum of three for a document.
  `(N more lines match; rolio cat <path> shows the document)` tells that the
  document has more. When the question needs a complete list, or the text
  before or after a line, read the document with `rolio cat`.
- `rolio search "<words>" --path <dir>` searches only below one directory.
- When a search result shows an `abstract:` line or lists a directory, the
  tree has summaries: `rolio abstract <path>` and `rolio overview <path>` give
  the short and the long summary of that document or directory. Read them
  before `rolio cat`. Without these lines in the result, do not use the two
  commands.
- `rolio tree / -L 3` shows the structure.
- `rolio cat <path>` reads one document.

If a document answers the question, use it and name its path in your answer.

### Write when you learn something durable

After you complete a task, record knowledge that agrees with all of these
conditions:

- It was not obvious. You had to investigate to find it.
- It will help in a different session or a different project.
- It is a fact about how something behaves, not a log of what you did.

Do not record secrets, credentials, or task status.

Use one topic for each file:

- `/shared/<topic>/<name>.md` for knowledge that applies to many projects,
  for example an external API or a tool.
- `/projects/<project>/<name>.md` for knowledge about one project only.

Search first. If a document for the topic exists, update it with `rolio edit`
or replace it with `rolio write`. Do not create a second document.

Write Markdown with this frontmatter:

```sh
rolio write /shared/<topic>/<name>.md <<'EOF'
---
title: <short title>
tags: [<tag>, <tag>]
---

# <short title>

<the facts, the symptom that shows the problem, and the correct procedure>
EOF
```
