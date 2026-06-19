## Rolio knowledge base

`rolio` is a shared knowledge tree for all your projects. It keeps knowledge
that must stay available after this session and outside this repository.

### Read before you investigate

Before you investigate an unfamiliar system, API, or recurring problem, look
for existing knowledge:

- `rolio search "<keywords>"` finds documents in the full tree.
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
