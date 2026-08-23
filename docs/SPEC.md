# tissues v0 specification

tissues is an issue tracker whose canonical data is ordinary Markdown files in
an ordinary Git repository.

This document describes only what is implemented today. It is not a roadmap.

The system has three layers, and this document is organized around them:

```
Git semantics
    ↓
tissues semantics
    ↓
interface/application semantics
```

---

## 1. Git semantics

Git provides files and directories, commit history, replication, remotes,
fetch/pull/push, its own conflict and concurrency behaviour, and — at the
hosting layer, entirely outside tissues — repository authentication and
access control.

tissues adds only what Git does not provide: valid issue and comment
structure, immutable domain identity, domain timestamps, open/closed state,
containment, and comment ordering.

Two consequences follow, and both are load-bearing:

> A repository can be valid Git while containing invalid tissues data.

Git will happily track a malformed `issue.md`, a duplicated ID or a directory
whose name is not an ID. tissues therefore validates on every load and fails
closed. It never guesses at or repairs malformed content.

> Git timestamps describe repository events. tissues timestamps describe
> issue and comment events.

A rebase, an amend or a fresh clone changes commit times, file modification
times and blob identity. None of that may change a `Created` or `Updated`
value, because those are recorded in the document itself.

No Git operation is implemented at this stage. The Markdown repository layer
reads and writes files; it does not stage, commit, pull or push.

---

## 2. tissues semantics

### 2.1 Domain objects

There are exactly two, and no others.

An **issue** has an immutable ID, a single-line title, a state of `open` or
`closed`, a created timestamp, an updated timestamp, a Markdown description,
and optionally a parent issue.

A **comment** has an immutable ID, an author, a created timestamp, an updated
timestamp, and a Markdown body.

An issue may contain child issues and comments. There is nothing else: no
assignee, no labels, no priority, no workflow, no metadata bag.

`ParentID`, `Children` and `Comments` are *derived*. They are reconstructed
from the filesystem when the tree is loaded, and never serialized.

Derived issue fields are store-owned. Caller-supplied derived relationships
are never persisted or treated as canonical.

### 2.2 Identity

An ID is 128 bits from `crypto/rand`, base32-encoded without padding and
lowercased: exactly 26 characters from the alphabet `a-z2-7`.

IDs carry no meaning. They do not encode creation time and are never used for
chronological ordering. Chronology comes exclusively from the explicit domain
timestamps.

An ID is assigned once and never changes. Nothing in tissues rewrites the ID
of an existing issue or comment; an attempt to write under a different ID is
an unknown-object error.

**Uniqueness:** issue and comment IDs share a single namespace. Every ID must
be unique across the entire loaded tree — an issue and a comment may not
share an ID either. One namespace makes uniqueness one check, and with 128
random bits it costs nothing.

### 2.3 Repository hierarchy

```
issues/
└── <issue-id>[-<slug>]/
    ├── issue.md
    ├── comments/
    │   └── <comment-id>.md
    └── issues/
        └── <child-issue-id>[-<slug>]/
            ├── issue.md
            ├── comments/
            └── issues/
```

Filesystem containment *is* issue containment. An issue's parent is the issue
whose `issues/` directory contains it; a top-level issue is one directly
inside the repository-root `issues/` directory. Moving a directory reparents
the issue, and nothing inside any document changes.

**Parent identity is never serialized into `issue.md`.** Containment is the
single source of truth, so there is nothing to keep in sync.

`comments/` and `issues/` are created only when they are needed. Their
absence loads as an empty collection. No placeholder files are used.

A repository with no `issues/` directory is a valid, empty tree.

### 2.4 Paths are not identity

The directory name is `<id>` or `<id>-<slug>`. Only the ID prefix is
identity, and it must match the ID declared inside `issue.md`; a mismatch is
an error. Likewise a comment's filename must be `<comment-id>.md` and must
match the ID declared inside the document.

The slug is decoration for humans browsing the repository. It is derived from
the title at creation time: lowercased, ASCII `a-z0-9` retained, every other
run collapsed to a single `-`, leading and trailing `-` trimmed, truncated to
40 characters. If nothing usable remains, the directory is the bare ID.

**Changing an issue's title does not rename its directory.** The slug can
therefore go stale, and that is intentional: renaming would churn history for
decoration, and the path was never identity in the first place.

### 2.5 Domain timestamps

`Created` and `Updated` are recorded in the document as RFC3339 UTC at whole-
second precision, for example `2026-08-23T13:20:11Z`. Offsets other than `Z`
and sub-second precision are rejected on both write and read.

They are domain facts. They are never inferred from file modification time,
Git commit time, a filename, or an ID.

`Created` is immutable. `Updated` must not be earlier than `Created`.

### 2.6 Comment ordering

Comments are presented in `Created` ascending order, tie-broken by `ID`
ascending. Because IDs are unique, this is a total order and is therefore
deterministic across processes and machines.

`Updated` is never consulted for ordering. Editing a comment consequently
cannot change its conversational position, no matter how much later the edit
happens.

Child issues are returned in directory-name order, which is lexical by ID and
likewise deterministic.

### 2.7 Validation, fail-closed

Every load parses and validates every document. Any of the following is an
error, and a failing document fails the whole load:

- a malformed or missing marker, or metadata in the wrong order;
- a state other than `open` or `closed`;
- an empty or multi-line issue title;
- an empty comment author or an empty comment body;
- a malformed ID, or an ID that disagrees with its directory or filename;
- a timestamp that is not RFC3339 UTC at second precision;
- `Updated` earlier than `Created`;
- a duplicate ID anywhere in the tree;
- a directory inside `issues/` whose name does not begin with a valid ID.

Files that are not tissues content — a `README.md` beside the issue
directories, a stray note inside one — are ignored rather than rejected.
tissues validates what it owns and leaves the rest of the repository alone.

### 2.8 Canonical `issue.md` grammar

```markdown
# Support nested issues

<!-- tissues:issue:v0 -->
- **ID:** `abcdefghijklmnopqrstuvwxyz`
- **State:** open
- **Created:** 2026-08-23T13:20:11Z
- **Updated:** 2026-08-23T14:02:44Z

---

Issues should be able to contain child issues, represented by
filesystem containment.
```

The grammar is positional and exact:

1. line 1 is `# ` followed by a non-empty, single-line title;
2. line 2 is blank;
3. line 3 is exactly `<!-- tissues:issue:v0 -->`;
4. lines 4–7 are exactly these four metadata lines, in this order:
   `- **ID:** ` + the ID in backticks, `- **State:** `, `- **Created:** `,
   `- **Updated:** `;
5. line 8 is blank;
6. line 9 is exactly `---`;
7. line 10 is blank;
8. everything after line 10 is the Markdown description, verbatim.

The description is opaque: because the grammar is positional, a description
may contain `---`, lines that look like metadata, or anything else, without
ambiguity. It may also be empty.

The only normalization is the file's single final newline, which is added on
write and stripped on read so that documents round-trip exactly.

The parser is deliberately not extensible. An unknown, extra, missing or
reordered metadata line is an error, not an extension point.

### 2.9 Canonical comment grammar

```markdown
<!-- tissues:comment:v0 -->
- **Author:** agent@example
- **ID:** `zyxwvutsrqponmlkjihgfedcba`
- **Created:** 2026-08-23T13:41:02Z
- **Updated:** 2026-08-23T13:41:02Z

---

Agreed. Containment is the whole point.
```

1. line 1 is exactly `<!-- tissues:comment:v0 -->`;
2. lines 2–5 are the four metadata lines in this order: `Author`, `ID`,
   `Created`, `Updated`;
3. line 6 is blank;
4. line 7 is exactly `---`;
5. line 8 is blank;
6. everything after line 8 is the Markdown body, which must not be empty.

There is no synthetic heading. The same strictness and the same final-newline
rule apply.

The `Author` field is provenance: it records who a comment claims to be from.
It is not authentication and grants nothing.

### 2.10 The Markdown repository layer

`internal/store` is the only code that knows about Markdown and the
filesystem. `internal/model` holds the domain types and their invariants and
knows about neither.

`store.Load(root)` returns a `Tree`: one scan of the repository, with
parentage reconstructed, comments ordered, and everything validated. There is
no cache and no index; rescanning is the refresh mechanism.

A `Tree` can look up an issue by ID, look up a comment by ID within an issue,
create a root or child issue, rewrite an existing issue, create a comment,
and rewrite an existing comment. Each write returns the repository-relative
path of the document it wrote, so a future Git layer can stage exact paths.

Creating an issue takes its parent as an argument, not as a field. An issue
handed to the store for creation must arrive with `ParentID`, `Children` and
`Comments` empty, and a request carrying any of them is rejected — the store
refuses input it cannot honour rather than silently discarding it, exactly as
it does for malformed documents. On success the store fills in `ParentID`
from the parent argument and the new issue has no children and no comments.
The observable tree therefore never contains a relationship that the
mutation did not also put on disk.

The store deliberately does *not* generate IDs or timestamps for objects
handed to it, own any application command such as close or reopen, take any
lock, or touch Git. Its job is validation, serialization, parsing, lookup and
persistence.

---

## 3. Interface/application semantics

Not implemented yet.

There is no service layer, no REST API, no MCP server and no HTML interface
at this stage. When they arrive, REST and MCP will be thin adapters over one
service layer, which will own issue and comment operations; there will not be
separate REST semantics and MCP semantics.

Timestamp and ID generation belong to that service layer: it will stamp
`Created` and `Updated` via `model.Timestamp` and mint IDs via `store.NewID`,
then hand complete objects to the store to persist.
