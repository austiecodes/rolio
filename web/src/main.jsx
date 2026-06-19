import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import Markdown from "react-markdown";
import "./style.css";

async function api(op, params = {}, options = {}) {
  const response = await fetch(
    `/v1/${op}?${new URLSearchParams(params)}`,
    options,
  );
  const data = await response.json();
  if (!response.ok)
    throw new Error(data.error?.message || `HTTP ${response.status}`);
  return data;
}
const parent = (p) => p.slice(0, p.lastIndexOf("/")) || "/";
const basename = (p) => p.split("/").filter(Boolean).at(-1) || "/";
const join = (p, n) => `${p === "/" ? "" : p}/${n}`;
function Icon({ type = "file" }) {
  return (
    <svg
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      aria-hidden="true"
    >
      {type === "folder" ? (
        <path d="M3 7V5h6l2 2h10v12H3z" />
      ) : type === "search" ? (
        <>
          <circle cx="10" cy="10" r="6" />
          <path d="m15 15 6 6" />
        </>
      ) : (
        <>
          <path d="M6 3h8l4 4v14H6z" />
          <path d="M14 3v5h4M9 12h6M9 16h6" />
        </>
      )}
    </svg>
  );
}
function App() {
  const [settings, setSettings] = useState(null),
    [directory, setDirectory] = useState("/"),
    [nodes, setNodes] = useState([]),
    [summary, setSummary] = useState(null);
  const [selected, setSelected] = useState(""),
    [doc, setDoc] = useState(null),
    [level, setLevel] = useState("L1"),
    [query, setQuery] = useState(""),
    [results, setResults] = useState(null);
  const [editing, setEditing] = useState(false),
    [draft, setDraft] = useState(""),
    [editHash, setEditHash] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(""),
    [loading, setLoading] = useState(false),
    [tick, setTick] = useState(0);
  const [dialog, setDialog] = useState(""),
    [newPath, setNewPath] = useState("");
  const generation = useRef(0);
  const zh = settings?.language === "zh",
    t = (a, b) => (zh ? a : b);
  const locked = editing || !!busy;
  const statusName = (s) =>
    ({
      ready: t("已更新", "Ready"),
      missing: t("待生成", "Missing"),
      stale: t("已过期", "Stale"),
      generating: t("生成中", "Generating"),
      failed: t("生成失败", "Failed"),
    })[s] || s;
  useEffect(() => {
    const c = new AbortController();
    api("settings", {}, { signal: c.signal })
      .then((s) => {
        setSettings(s);
        document.documentElement.lang = s.language;
      })
      .catch((e) => {
        if (e.name !== "AbortError") setError(e.message);
      });
    return () => c.abort();
  }, [tick]);
  useEffect(() => {
    const c = new AbortController();
    setLoading(true);
    setSummary(null);
    setNodes([]);
    setResults(null);
    generation.current++;
    Promise.all([
      api("ls", { path: directory }, { signal: c.signal }),
      api("summary", { path: directory }, { signal: c.signal }),
    ])
      .then(([ls, s]) => {
        setNodes(ls.nodes || []);
        setSummary(s);
      })
      .catch((e) => {
        if (e.name !== "AbortError") setError(e.message);
      })
      .finally(() => {
        if (!c.signal.aborted) setLoading(false);
      });
    return () => c.abort();
  }, [directory, tick]);
  useEffect(() => {
    setDoc(null);
    if (!selected) return;
    const c = new AbortController();
    api("cat", { path: selected }, { signal: c.signal })
      .then(setDoc)
      .catch((e) => {
        if (e.name !== "AbortError") setError(e.message);
      });
    return () => c.abort();
  }, [selected, tick]);
  useEffect(() => {
    if (editing || !summary || summary.status !== "generating") return;
    const id = setInterval(() => setTick((x) => x + 1), 4000);
    return () => clearInterval(id);
  }, [summary, editing]);
  useEffect(() => {
    if (!editing) return;
    const warn = (e) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [editing]);
  function navigate(p) {
    if (locked) return;
    setError("");
    setDirectory(p);
    setSelected("");
    setLevel("L1");
  }
  function openFile(p) {
    if (locked) return;
    setError("");
    setDirectory(parent(p));
    setSelected(p);
    setLevel("L2");
  }
  async function action(name, fn) {
    setBusy(name);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy("");
    }
  }
  async function search(e) {
    e.preventDefault();
    if (locked || !query.trim()) return;
    const version = ++generation.current;
    setError("");
    try {
      const r = await api("search", { q: query, path: directory, limit: 100 });
      if (generation.current === version) setResults(r);
    } catch (e) {
      if (generation.current === version) setError(e.message);
    }
  }
  function sourceLink(href) {
    if (!href) return;
    if (
      /^[a-z][a-z0-9+.-]*:/i.test(href) ||
      href.startsWith("//") ||
      href.startsWith("#")
    )
      return null;
    const p = new URL(
      href,
      `http://rolio.local${directory === "/" ? "/" : directory + "/"}`,
    ).pathname;
    try {
      return decodeURIComponent(p);
    } catch {
      return null;
    }
  }
  const content =
    level === "L0"
      ? summary?.abstract
      : level === "L1"
        ? summary?.overview
        : (doc?.body ?? doc?.content);
  const list = results ? results.results || [] : nodes;
  const crumbs = directory.split("/").filter(Boolean);
  return (
    <div className="shell">
      <aside className="rail">
        <a
          className="brand"
          href="/"
          onClick={(e) => {
            e.preventDefault();
            navigate("/");
          }}
        >
          <span className="brand-mark">r.</span>rolio
          <span className="brand-dot" />
        </a>
        <div className="rail-caption">KNOWLEDGE SPACE</div>
        <button
          className="rail-link active"
          onClick={() => navigate("/")}
          disabled={locked}
        >
          <Icon type="folder" />
          {t("知识库", "Library")}
          <span>↗</span>
        </button>
        <div className="rail-note">
          <span className="tiny-dot" />
          {t("一棵树，所有知识。", "One tree. Shared knowledge.")}
          <p>
            {t(
              "先看摘要，按需深入。",
              "Start with an overview. Read deeper when needed.",
            )}
          </p>
        </div>
        <div className="rail-bottom">
          <span className="avatar">{zh ? "中" : "EN"}</span>
          <div>
            {t("中文知识空间", "English workspace")}
            <small>PostgreSQL · Rolio server</small>
          </div>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <span className="section-title">{t("知识库", "Library")}</span>
          <form className="search" onSubmit={search}>
            <Icon type="search" />
            <input
              aria-label={t("搜索当前目录", "Search current directory")}
              placeholder={t(
                "搜索当前目录中的知识…",
                "Search knowledge in this directory…",
              )}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              disabled={locked}
            />
            <kbd>↵</kbd>
          </form>
          <button
            className="primary"
            disabled={locked || !settings}
            onClick={() => {
              setNewPath(join(directory, "untitled.md"));
              setDialog("new");
            }}
          >
            ＋ {t("新建文档", "New document")}
          </button>
        </header>
        <main>
          <div className="heading">
            <div>
              <div className="eyebrow">YOUR CONTEXT, ORGANIZED</div>
              <h1>
                {directory === "/"
                  ? t("知识，从这里开始", "A home for your knowledge")
                  : basename(directory)}
              </h1>
              <p>
                {t(
                  "浏览、理解和维护你的知识，让每一次读取都更有价值。",
                  "Browse, understand, and maintain context worth keeping.",
                )}
              </p>
            </div>
            <div className="count">
              <strong>{nodes.length}</strong>
              <span>{t("个条目", "items")}</span>
            </div>
          </div>
          {error && (
            <div className="notice error" role="alert">
              <span>{error}</span>
              <button onClick={() => setError("")} aria-label="Dismiss">
                ×
              </button>
            </div>
          )}
          {!!settings?.index.stale && (
            <div className="notice">
              <span>
                {t(
                  "检索索引尚未建立或语言已变更。",
                  "The search index is missing or was built for another language.",
                )}{" "}
                ({settings.index.stale})
              </span>
              <button
                disabled={locked}
                onClick={() =>
                  action("reindex", async () => {
                    await api("reindex", {}, { method: "POST" });
                    setTick((x) => x + 1);
                  })
                }
              >
                {t("重建索引", "Rebuild index")}
              </button>
            </div>
          )}
          <nav className="breadcrumbs" aria-label="Breadcrumb">
            <button disabled={locked} onClick={() => navigate("/")}>
              {t("全部知识", "All knowledge")}
            </button>
            {crumbs.map((part, i) => (
              <React.Fragment key={i}>
                <span>/</span>
                <button
                  disabled={locked}
                  onClick={() =>
                    navigate("/" + crumbs.slice(0, i + 1).join("/"))
                  }
                >
                  {part}
                </button>
              </React.Fragment>
            ))}
          </nav>
          <div className="browser">
            <section className="file-pane">
              <div className="pane-heading">
                <span>
                  {results
                    ? t("搜索结果", "Search results")
                    : t("目录内容", "Contents")}
                </span>
                {results ? (
                  <button disabled={locked} onClick={() => setResults(null)}>
                    {t("返回", "Clear")}
                  </button>
                ) : (
                  <small>{loading ? "…" : nodes.length}</small>
                )}
              </div>
              <div className="file-list">
                {directory !== "/" && !results && (
                  <button
                    className="file-row parent"
                    disabled={locked}
                    onClick={() => navigate(parent(directory))}
                  >
                    ↰ <span>..</span>
                  </button>
                )}
                {list.map((n) => (
                  <button
                    key={n.path}
                    disabled={locked}
                    className={`file-row ${selected === n.path ? "selected" : ""}`}
                    onClick={() =>
                      n.kind === "dir" ? navigate(n.path) : openFile(n.path)
                    }
                  >
                    <span
                      className={n.kind === "dir" ? "folder-icon" : "file-icon"}
                    >
                      <Icon type={n.kind === "dir" ? "folder" : "file"} />
                    </span>
                    <span className="file-name">
                      {n.name || basename(n.path)}
                      <small>
                        {results
                          ? n.path
                          : n.kind === "dir"
                            ? t("文件夹", "Folder")
                            : `${Math.max(1, Math.ceil((n.size || 0) / 1024))} KB`}
                      </small>
                    </span>
                    <span className="row-arrow">
                      {n.kind === "dir" ? "›" : "↗"}
                    </span>
                  </button>
                ))}
                {!loading && list.length === 0 && (
                  <div className="empty compact">
                    <Icon type="folder" />
                    <p>
                      {results
                        ? t("未找到匹配的知识", "No matching knowledge")
                        : t("这个目录还没有文档", "No documents here yet")}
                    </p>
                  </div>
                )}
              </div>
              <div className="pane-footer">
                {results
                  ? `${results.total} ${t("条匹配，最多显示 100 条", "matches, showing up to 100")}`
                  : t("选择文档阅读 L2 原文", "Select a document to read L2")}
              </div>
            </section>
            <section className="reader">
              <div className="reader-toolbar">
                <div className="tabs" role="tablist">
                  {["L0", "L1", "L2"].map((l) => (
                    <button
                      role="tab"
                      aria-selected={level === l}
                      disabled={locked || (l === "L2" && !selected)}
                      key={l}
                      className={level === l ? "active" : ""}
                      onClick={() => setLevel(l)}
                    >
                      {l}
                      <span>
                        {l === "L0"
                          ? t("摘要", "Abstract")
                          : l === "L1"
                            ? t("概览", "Overview")
                            : t("原文", "Source")}
                      </span>
                    </button>
                  ))}
                </div>
                {level !== "L2" ? (
                  <button
                    className="text-button"
                    disabled={
                      locked ||
                      !settings?.summary_enabled ||
                      !nodes.length ||
                      summary?.status === "generating"
                    }
                    onClick={() =>
                      action("refresh", async () => {
                        try {
                          await api(
                            "refresh",
                            { path: directory },
                            { method: "POST" },
                          );
                        } finally {
                          setTick((x) => x + 1);
                        }
                      })
                    }
                  >
                    ↻{" "}
                    {busy === "refresh"
                      ? t("生成中…", "Generating…")
                      : t("刷新摘要", "Refresh")}
                  </button>
                ) : (
                  !editing && (
                    <button
                      className="text-button"
                      disabled={locked || !doc}
                      onClick={() => {
                        setDraft(doc.content);
                        setEditHash(doc.hash);
                        setEditing(true);
                      }}
                    >
                      {t("编辑", "Edit")} ↗
                    </button>
                  )
                )}
              </div>
              <div className="reader-content">
                <div className="document-heading">
                  <span className="eyebrow">
                    {level === "L2" ? "SOURCE DOCUMENT" : "DIRECTORY CONTEXT"}
                  </span>
                  <h2>
                    {level === "L2"
                      ? basename(selected)
                      : basename(directory) === "/"
                        ? t("知识库概览", "Library overview")
                        : basename(directory)}
                  </h2>
                  <div className="document-meta">
                    <code>{level === "L2" ? selected : directory}</code>
                    {level !== "L2" && summary && (
                      <span className={`status ${summary.status}`}>
                        {statusName(summary.status)} ·{" "}
                        {summary.language.toUpperCase()}
                      </span>
                    )}
                  </div>
                </div>
                {editing ? (
                  <>
                    <label className="editor-label" htmlFor="editor">
                      Markdown + YAML frontmatter
                    </label>
                    <textarea
                      id="editor"
                      className="editor"
                      spellCheck="false"
                      value={draft}
                      onChange={(e) => setDraft(e.target.value)}
                      disabled={!!busy}
                    />
                    <div className="edit-actions">
                      <button
                        className="primary"
                        disabled={!!busy}
                        onClick={() =>
                          action("save", async () => {
                            await api(
                              "write",
                              { path: selected },
                              {
                                method: "PUT",
                                headers: { "If-Match": `"${editHash}"` },
                                body: draft,
                              },
                            );
                            setEditing(false);
                            setTick((x) => x + 1);
                          })
                        }
                      >
                        {t("保存更改", "Save changes")}
                      </button>
                      <button
                        disabled={!!busy}
                        onClick={() => {
                          setEditing(false);
                          setTick((x) => x + 1);
                        }}
                      >
                        {t("取消", "Cancel")}
                      </button>
                      <button
                        className="danger"
                        disabled={!!busy}
                        onClick={() => setDialog("delete")}
                      >
                        {t("删除文档", "Delete document")}
                      </button>
                    </div>
                  </>
                ) : (
                  <>
                    {level === "L2" && doc?.metadata_error && (
                      <p className="summary-error" role="alert">
                        {doc.metadata_error}
                      </p>
                    )}
                    {level === "L2" &&
                      doc?.metadata &&
                      Object.keys(doc.metadata).length > 0 && (
                        <dl className="metadata">
                          {Object.entries(doc.metadata).map(([k, v]) => (
                            <div key={k}>
                              <dt>{k}</dt>
                              <dd>
                                {Array.isArray(v)
                                  ? v.join(" · ")
                                  : typeof v === "object"
                                    ? JSON.stringify(v)
                                    : String(v)}
                              </dd>
                            </div>
                          ))}
                        </dl>
                      )}
                    {content ? (
                      <article className="markdown">
                        <Markdown
                          components={{
                            a: ({ href, children }) => {
                              const p = sourceLink(href);
                              return (
                                <a
                                  href={href}
                                  onClick={
                                    p
                                      ? (e) => {
                                          e.preventDefault();
                                          openFile(p);
                                        }
                                      : undefined
                                  }
                                  target={p ? undefined : "_blank"}
                                  rel="noreferrer"
                                >
                                  {children}
                                </a>
                              );
                            },
                          }}
                        >
                          {content}
                        </Markdown>
                      </article>
                    ) : (
                      <div className="empty">
                        <div className="empty-glyph">
                          {level === "L0" ? "Aa" : "≋"}
                        </div>
                        <h3>
                          {level === "L2"
                            ? doc
                              ? t("空文档", "Empty document")
                              : t("正在读取…", "Loading…")
                            : t(
                                "让知识先有一个概览",
                                "Give this knowledge an overview",
                              )}
                        </h3>
                        <p>
                          {level === "L2"
                            ? ""
                            : settings?.summary_enabled
                              ? t(
                                  "点击「刷新摘要」，生成当前目录的 L0 和 L1。",
                                  "Refresh to generate L0 and L1 for this directory.",
                                )
                              : t(
                                  "配置摘要模型后，即可生成中文或英文概览。",
                                  "Configure a summary model to generate directory context.",
                                )}
                        </p>
                      </div>
                    )}
                    {level !== "L2" && summary?.error && (
                      <p className="summary-error">{summary.error}</p>
                    )}
                  </>
                )}
              </div>
              <div className="reader-footer">
                <span className="tiny-dot" />
                {level === "L2"
                  ? t(
                      "原文完整保留 · 修改受版本保护",
                      "Original content preserved · Version-protected edits",
                    )
                  : t(
                      "L0 快速判断 · L1 理解全貌 · L2 按需深入",
                      "L0 scan · L1 understand · L2 explore",
                    )}
              </div>
            </section>
          </div>
          <footer className="page-footer">
            <span>ROLIO / KNOWLEDGE FILESYSTEM</span>
            <span>
              {busy
                ? t("正在处理…", "Working…")
                : t("保持简单，持续积累。", "Keep it simple. Keep learning.")}
            </span>
          </footer>
        </main>
      </div>
      {dialog && (
        <div className="modal-backdrop">
          <section
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="dialog-title"
          >
            <h2 id="dialog-title">
              {dialog === "new"
                ? t("新建文档", "New document")
                : t("删除文档？", "Delete document?")}
            </h2>
            {dialog === "new" ? (
              <>
                <label htmlFor="new-path">
                  {t("知识路径", "Knowledge path")}
                </label>
                <input
                  id="new-path"
                  autoFocus
                  value={newPath}
                  onChange={(e) => setNewPath(e.target.value)}
                  placeholder="/shared/guide.md"
                />
                <p>
                  {t(
                    "使用普通路径组织知识，文档支持 Markdown 和 frontmatter。",
                    "Use a path to organize your Markdown document.",
                  )}
                </p>
              </>
            ) : (
              <p>{selected}</p>
            )}
            {error && (
              <p className="summary-error" role="alert">
                {error}
              </p>
            )}
            <div className="edit-actions">
              <button disabled={!!busy} onClick={() => setDialog("")}>
                {t("取消", "Cancel")}
              </button>
              <button
                className={dialog === "delete" ? "danger" : "primary"}
                disabled={!!busy || (!newPath.trim() && dialog === "new")}
                onClick={() =>
                  action(dialog, async () => {
                    if (dialog === "new") {
                      if (!newPath.startsWith("/") || !newPath.endsWith(".md"))
                        throw new Error(
                          t(
                            "请输入以 / 开头、以 .md 结尾的路径",
                            "Use an absolute path ending in .md",
                          ),
                        );
                      const r = await api(
                        "write",
                        { path: newPath },
                        {
                          method: "PUT",
                          headers: { "If-None-Match": "*" },
                          body: `---\ntitle: ${JSON.stringify(basename(newPath).replace(/\.md$/, ""))}\ntags: []\n---\n\n# ${basename(newPath).replace(/\.md$/, "")}\n`,
                        },
                      );
                      setDirectory(parent(r.node.path));
                      setSelected(r.node.path);
                      setLevel("L2");
                    } else {
                      await api(
                        "delete",
                        { path: selected },
                        {
                          method: "DELETE",
                          headers: { "If-Match": `"${editHash}"` },
                        },
                      );
                      setSelected("");
                      setLevel("L1");
                      setEditing(false);
                    }
                    setDialog("");
                    setTick((x) => x + 1);
                  })
                }
              >
                {dialog === "new"
                  ? t("创建文档", "Create document")
                  : t("确认删除", "Delete")}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")).render(<App />);
