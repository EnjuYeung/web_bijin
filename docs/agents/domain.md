# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

This repo is **single-context**: one glossary and one decision log, both at the repo root.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root: the domain glossary.
- **`DECISIONS.md`** at the repo root: this repo's decision log, used *instead of* `docs/adr/`. Read the sections that touch the area you're about to work in. Later sections can supersede earlier ones (marked 「历史决策」 or 「被替代」); the latest one wins.

If `CONTEXT.md` doesn't exist, **proceed silently**. Don't flag its absence; don't suggest creating it upfront. The `/domain-modeling` skill (reached via `/grill-with-docs` and `/improve-codebase-architecture`) creates it lazily when terms actually get resolved.

## Recording decisions

Wherever a skill says "write an ADR" or "create `docs/adr/NNNN-*.md`", append a new section to the end of `DECISIONS.md` instead, headed `## YYYY-MM-DD HH:MM:SS CST：<title>` like the existing entries, saying why A was chosen over B. Never create `docs/adr/`: `AGENTS.md` makes `DECISIONS.md` the single decision log. When a new decision replaces an older one, note the replacement on the older section, as existing entries do.

Write `CONTEXT.md` and `DECISIONS.md` in Chinese, matching the other project docs.

## File structure

```
/
├── CONTEXT.md       ← glossary (created lazily)
├── DECISIONS.md     ← decision log (replaces docs/adr/)
├── *.go
└── frontend/
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal: either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag decision conflicts

If your output contradicts a section of `DECISIONS.md`, surface it explicitly rather than silently overriding, naming the section by its heading:

> _Contradicts DECISIONS.md「索引用 SQLite 文件，不用单独数据库容器」, but worth reopening because…_
