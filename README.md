# Rolio

A lightweight knowledge filesystem for agents and humans: a Go server,
PostgreSQL, a thin CLI, and a Vite + React web UI served by the same server.

There is one knowledge tree. `/projects/payment/` and `/shared/auth/` are ordinary
paths, not registered repositories or mounts. The CLI and UI both use HTTP;
only the server connects to PostgreSQL. SQLite is not currently supported.

## Run

The server requires an explicit language: `zh` or `en`.

```sh
cp rolio-server.example.toml rolio-server.toml
export ROLIO_POSTGRES_DSN='postgres://user:password@localhost:5432/rolio?sslmode=disable'
go run ./cmd/rolio-server --config rolio-server.toml
```

Open http://127.0.0.1:4837 for the web UI. The default binding is loopback; the
server currently has no authentication. Database schema objects are created on
startup. Existing documents are preserved, but existing databases need an
explicit `rolio reindex` to populate the new language-specific index. Historical
repo/docset data is not automatically migrated into the single knowledge tree.

```sh
export ROLIO_SERVER=http://127.0.0.1:4837
go run ./cmd/rolio write /projects/payment/guide.md '# Payment guide'
go run ./cmd/rolio tree /
go run ./cmd/rolio cat /projects/payment/guide.md
go run ./cmd/rolio search payment --path /projects
```

Alternatively, put the server address in `~/.rolio/settings.toml`:

```toml
[server]
addr = "http://127.0.0.1:4837"
```

`ROLIO_SERVER` overrides config; `ROLIO_CONFIG` selects another CLI config file.
The CLI does not create project files, install hooks, or synchronize local copies.

## Markdown and frontmatter

The original file is preserved byte-for-byte, including its frontmatter.
Markdown files may contain YAML metadata:

```markdown
---
title: 支付重试
source: https://example.com/payment
tags: [支付, retry]
---

# 支付重试

完整正文，不自动翻译。
```

`title` and `source` must be strings, and `tags` must be a list of strings.
Additional JSON-compatible metadata is retained. `language`, `source_hash`,
`generated_by`, `status`, and `revision` are reserved for the server. Invalid
frontmatter rejects the whole write or edit; it never partially updates a file.
Metadata is indexed alongside the body and path. `cat` prints the raw source;
the HTTP response also includes parsed `body` and `metadata` for the UI.

## Context layers

- **L0**: the short abstract of a directory or a document.
- **L1**: the overview of a directory, with navigation to its sources, or the
  overview of a document.
- **L2**: original documents, read on demand.

L0/L1 are derived views. The original documents are the only stored truth. The
reserved read paths of a directory are `.abstract.md` and `.overview.md`; they
are not editable source documents or ordinary search hits.

Configure a Chat Completions-compatible model to enable generation:

```toml
language = "zh"

[summary]
url = "https://your-provider.example/v1/chat/completions"
model = "your-model"
api_key = "${ROLIO_SUMMARY_API_KEY}"
```

Add this section to the server configuration, retaining the PostgreSQL section.
There is no default provider or model. The provider must support JSON-object
output with `abstract` and `overview` fields. Provider credentials stay on the
server. Document reads, writes, and search work without a model.

```sh
rolio abstract /projects/payment
rolio overview /projects/payment
rolio abstract /projects/payment/guide.md
rolio summary /projects/payment
rolio cat /projects/payment/.abstract.md
rolio refresh /projects/payment
```

With a model, the server keeps the summaries current. No command is necessary.

- A write puts the document and all its ancestor directories in a summary
  queue, in the same database transaction. Many writes to one directory in a
  short period make one job for that directory.
- Workers in the server take the jobs from the bottom of the tree to the top.
  A document gets its summary from its text. A directory gets its summary from
  the summaries of its children: the overview of each document and the
  abstract of each subdirectory. Thus the input for a directory does not grow
  with the size of the documents below it.
- Only the changed document and its ancestors get new summaries. The model is
  called only when its input changed: a change of the frontmatter only, or a
  child with an unchanged summary, causes no call. When the summary of a path
  changes, its parent goes into the queue.
- A generation whose source changes before it finishes cannot publish. The
  path stays in the queue and gets a new summary. A directory that gets writes
  more frequently than one generation takes does not get a current summary
  until the writes stop.
- `summary.concurrency` sets the number of summaries that the server
  generates at the same time. The default is 4.
- When you configure a model for a tree that has documents, or change
  `language`, the server puts all outdated paths in the queue at its start.

A document longer than 200 KB gives only its first 200 KB to the model. An
abstract longer than 256 characters or an overview longer than 8000 characters
is cut. One generation has a time limit of 90 seconds. A generation that the
server did not complete starts again after two minutes.

A generation that fails is tried three times, with a longer delay each time.
The server writes each failure to its log. After the third failure the status
is `failed`: the old summary stays readable, and the directories above use
it. A new write to the path, `rolio refresh`, or a start of the server tries
again.

`rolio refresh <path>` generates the summary of one path again and waits for
the result, to a maximum of 5 minutes. For a directory, the model reads the
summaries of the children, not the documents; only children with no summary or
a failed one are generated again.

After writes, `rolio summary /` shows `ready` when the queue is empty, because
the root is the last job of a write. Paths with the status `failed` are not in
the queue: examine them with `rolio summary <path>`. `rolio refresh` of a path
with no text, or of a directory below which all generations failed, is an
error. Missing, stale, generating, failed, and
ready states are visible in the UI and `rolio summary`. An old summary stays
readable while its path is stale or failed. Reserved sidecar reads include
system frontmatter; `abstract` and `overview` output only their bodies, for a
directory or for a document.

## Language and search

`language` is mandatory and accepts only `zh` and `en`. It controls summary
instructions, index configuration, and UI language. L2 is never translated.

Chinese text is indexed as Han unigrams and overlapping bigrams without requiring
a PostgreSQL extension. English mode additionally uses PostgreSQL's English
stemmer; Chinese mode uses `simple` for Latin words. Both modes accept mixed
Chinese/English text. This is lexical retrieval, not semantic/vector search or
dictionary-based Chinese word segmentation. Search returns an original-content
excerpt rather than guaranteed highlighted matches.

After changing `language`, restart the server, then run:

```sh
rolio reindex
```

Reindex rebuilds parsed metadata and search vectors transactionally for all
existing documents. Search rejects an incomplete or mismatched index until it is
rebuilt. With a model, the server generates all summaries again in the new
language at its start. Without a model, summaries in the old language show as
stale. Run a single configured language per database/schema.

## Commands and API

Browsing: `ls`, `tree`, `cat`, `stat`, `grep`, `find`, `glob`, `search`.
Writing: `write`, `edit`, `rm`. Context: `abstract`, `overview`, `summary`,
`refresh`, `reindex`. Run each command with `--help` for options.

Writes accept `--expected-hash <hash>` for compare-and-swap; `write
--expected-hash '*'` is create-only. The UI uses these conditions for edits and
creates. `rm` recursively deletes a directory; deleting `/` is forbidden.

| API | Purpose |
| --- | --- |
| `GET /v1/ls`, `/tree`, `/cat`, `/stat`, `/grep`, `/find`, `/glob`, `/search` | Browse and search |
| `PUT /v1/write`, `/v1/edit` | Create, replace, or edit |
| `DELETE /v1/delete` | Delete a file or subtree |
| `GET /v1/summary?path=/...` | Inspect the summary of a path and its freshness |
| `POST /v1/refresh?path=/...` | Generate L0/L1 of a path again and wait |
| `POST /v1/reindex` | Rebuild the configured language's index |
| `GET /v1/settings` | Public language, generator availability, index status |
| `GET /healthz` | Liveness |

Use `path` for knowledge paths and `q` for search text. HTTP conditional reads
use ETag/If-None-Match; writes use If-Match or If-None-Match: `*`.

## Development

Go 1.25+, Node.js 20.19+ or 22.12+, and PostgreSQL are required to rebuild all
components. Prebuilt UI assets are included so a Go build works without Node.
Rebuild those assets whenever frontend sources change:

```sh
make build
# bin/rolio and bin/rolio-server

npm --prefix web ci
npm --prefix web run dev
# Vite proxies /v1 to the Go server at 127.0.0.1:4837
```

For tests, use an isolated PostgreSQL database. Integration tests create and drop
unique test schemas. Without the DSN they are skipped.

```sh
ROLIO_TEST_POSTGRES_DSN='postgres://user:password@localhost/test?sslmode=disable' make test
```
