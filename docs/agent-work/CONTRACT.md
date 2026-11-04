# Compatibility contract

Owner direction, 2026-11-03. This replaces the earlier byte-parity contract
(kept in git history and summarized in `archive/`).

**Goal:** a pure-Go server (`go-backend/cmd/audiobookshelf-go`,
`CGO_ENABLED=0`, no Node at runtime) that replaces Audiobookshelf v2.37.2 in an
existing installation, with clients, books and progress. The code is simple,
clear and fast.

**Fixed contracts**

1. The SQLite database: the pinned v2.37.2 schema, value formats and data
   semantics, plus the `/config` and `/metadata` trees it refers to. Existing
   installs open unchanged, and TS v2.37.2 can reopen what Go wrote
   (rollback). No schema change and no implicit migration.
2. The client protocols: the HTTP routes, fields, types, status codes and
   auth flows, and the Socket.IO events, that the bundled web client, the iOS
   and Android apps and documented-API clients rely on.

**Not contract:** code structure, SQL text, HTTP framing representation where client semantics are preserved (ETag style,
header order, compression), JSON key order and whitespace, error text no
client parses, log text, and routes no client calls (removed only with owner
approval, ROADMAP R2).

Conditional caching (ETag/If-None-Match/304), byte ranges (206/416 and
Content-Range), required MIME formats and observable media/auth semantics are
part of the fixed client contract. The old web/log/trial inventory alone does
not establish absence of callers. Removal requires multi-client evidence and
per-operation owner approval.

**Reference:** upstream `advplyr/audiobookshelf` v2.37.2,
`edb1f7efc92ecacdef51aafb4d9d5054ce10604f` (`server/`, `client/`, `docs/`),
never modified. It explains what the contracts mean; it is not a byte oracle
or an architecture template.

**Proof:** contract tests, persistence tests (including the rollback check)
and functional journeys, described in `go-backend/ARCHITECTURE.md`. Work order
is `go-backend/ROADMAP.md`; checks per change are in `go-backend/AGENTS.md`.

**Approved exclusions (unchanged):** databases below the pinned migration
level are refused, not migrated; ebook support is a stub (files are grouped
and served, no ebook engine).

**Libraries:** only those approved by the owner, listed in
`go-backend/ARCHITECTURE.md`.
