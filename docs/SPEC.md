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

### 1.1 How tissues reaches Git

tissues runs the installed `git` executable through `os/exec`, with arguments
passed directly. There is no Git library, no GitHub API client, and no shell:
no `sh -c`, no command strings. Every command is run with the caller's
context, so cancellation reaches the git process.

The Git wrapper is deliberately narrow. It verifies the repository, reports
porcelain status, detects HEAD and upstream, fast-forwards, stages exact
paths, commits, pushes, and reports ahead/behind. It is not a general-purpose
Git library and nothing generic escapes through the service.

Commit authorship is whatever Git identity the tissues process is configured
with. Push credentials are the process's own. Neither authenticates the
caller of a tissues operation.

### 1.2 The clean-repository precondition

Every mutation begins by requiring a clean working tree and a clean index:
no modified tracked files, no staged changes, no untracked files. A dirty
repository is refused before anything is pulled, written, staged or
committed, so tissues can never sweep unrelated work into its commits.

Reads do not require a clean tree. They only parse the current filesystem
state and change nothing.

### 1.3 Local mode

```
lock
→ require a clean working tree and index
→ load and validate the issue tree
→ apply one semantic mutation
→ write the exact canonical file(s)
→ git add -- <exact paths>
→ git commit
```

No pull, no push, no remote contact at all — even when a remote is
configured.

### 1.4 Remote-synchronized mode

```
lock
→ require a clean working tree and index
→ if HEAD exists and the branch has an upstream:
      git pull --ff-only          (failure aborts before any mutation)
→ load and validate the issue tree
→ apply one semantic mutation
→ write the exact canonical file(s)
→ git add -- <exact paths>
→ git commit
→ if the branch has an upstream: git push
  otherwise:                     git push --set-upstream origin HEAD
```

Staging always names exact paths. `git add .` is never used.

`git pull --ff-only` is the only synchronization primitive. There is no
separate fetch, no automatic merge, no rebase, no conflict retry and no
force-push. Establishing an upstream creates only the current branch on the
remote.

On the bootstrap case — no HEAD, no upstream, an empty remote named `origin`
— the pull is skipped, the first mutation produces the root commit, and
`git push --set-upstream origin HEAD` creates the remote branch and records
the upstream.

If no upstream exists after the commit, the remote is `origin` by name. There
is no remote discovery or ranking; if no usable `origin` exists, that is
reported as an explicit error.

### 1.5 Divergence is a hard stop

If `git pull --ff-only` fails, the upstream has diverged and the mutation is
abandoned at that point. What tissues guarantees is:

- the requested tissues mutation does not run;
- no canonical tissues file is written by the request;
- the index is unchanged by the request;
- local HEAD does not move;
- the working tree is unchanged;
- no tissues commit is created.

`git pull` fetches before it integrates, so a failed fast-forward may still
have updated remote-tracking refs such as `refs/remotes/origin/main`, and
`FETCH_HEAD`. That is normal Git bookkeeping, not a tissues semantic
mutation, and tissues neither suppresses nor undoes it. The repository is
therefore not guaranteed byte-for-byte identical; canonical tissues state and
the current branch are.

Resolving divergence is human work with ordinary Git tools; v0 does not
attempt it.

### 1.6 A failed push does not undo a commit

If the commit succeeds but the push fails, the commit stands. tissues does
not reset, revert or discard the mutation. The operation returns the mutated
object together with an explicit error stating that the change was committed
locally but not pushed. The working tree stays clean and the branch is simply
ahead of its upstream; a later mutation publishes the backlog once pushing
works again. Success is never claimed for an unpublished change.

### 1.7 An incomplete transaction

Validation and synchronization failures all happen before anything is
written. There remains one window that cannot be moved earlier: canonical
files exist on disk but `git add` or `git commit` has not yet completed.

A failure in that window is its own outcome, distinct from a refusal. The
requested mutation changed canonical files in the working tree but was not
recorded as a commit, so the intended commit does not exist and the mutation
is not durable. The operation reports this explicitly and returns no domain
object.

The repository is left dirty or staged. The clean-repository precondition
therefore refuses every further mutation — as an ordinary repository refusal,
not as another incomplete transaction — until a human repairs the repository
with ordinary Git commands. There is no automatic `reset --hard`, no revert
and no hidden rollback machinery.

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

One application service owns every issue and comment operation. REST, MCP and
any UI will be thin adapters over it; there is no separate REST semantic and
MCP semantic. **REST, MCP, HTML rendering and any browser interface are not
implemented.**

### 3.1 The service

One process serves one repository, in one of two modes: local, or
remote-synchronized (§1.3, §1.4).

The service holds no loaded tree between calls. Every operation, read or
write, loads and validates the issue tree fresh from the filesystem while
holding a single mutex. Reads take the same mutex, so no read can observe a
half-finished pull, write or commit, and a restarted process is
indistinguishable from a running one.

Because no tree is retained, a returned issue or comment is a snapshot.
Mutating it afterwards changes nothing on disk; only another service call
does.

Every operation takes a `context.Context`, which is passed to every git
command.

### 3.2 Operations

`ListIssues` returns the complete root hierarchy. There is no filtering.

`GetIssue` looks up an issue by immutable ID and returns its child hierarchy
and its comments in canonical order.

`CreateIssue` takes an optional parent ID, a required title and an optional
description. The service owns everything else: the ID, the open state, and
both timestamps, with `Created == Updated` at creation.

`UpdateIssue` changes only the title and the description; an omitted field is
untouched. The ID, state, creation time, parent, children and comments cannot
be updated.

`CloseIssue` and `ReopenIssue` change only the state and `Updated`. Closing
does not cascade to children.

`AddComment` takes an issue ID, an author and a body. The service owns the
ID and both timestamps, with `Created == Updated` at creation.

`EditComment` changes only the body, preserving the comment's ID, author and
creation time — so an edit can never move a comment, because ordering is
`Created ASC, ID ASC` and never consults `Updated`.

Only these eight operations exist. No generic filesystem or Git operation is
reachable through the service.

### 3.3 Identity and timestamp ownership

The service mints IDs with `store.NewID` and stamps `Created` and `Updated`
with `model.Timestamp`, then hands complete objects to the store. Callers
never supply IDs, state, timestamps, or derived relationships.

### 3.4 No-op operations do not commit

An operation that changes nothing succeeds, writes nothing, commits nothing,
and leaves `Updated` alone: an update whose fields already match, closing a
closed issue, reopening an open one, editing a comment to its current body.

One changed semantic operation produces exactly one commit, with a
deterministic message:

```
create issue <id>: <title>
update issue <id>
close issue <id>
reopen issue <id>
comment <comment-id> on issue <issue-id>
edit comment <comment-id> on issue <issue-id>
```

No trailers are added and no commit is ever amended.

### 3.5 Outcome classes

Every mutating operation ends in one of six outcomes. Each says something
different about what happened to canonical state, which is what later
adapters need in order to map it:

| Outcome | Canonical state | Result object |
|---|---|---|
| success, including an idempotent no-op | mutated, or deliberately untouched | the object |
| **not found** — unknown issue or comment ID | untouched | nil |
| **invalid request** — ordinary validation failure, such as an empty title or an empty comment body | untouched | nil |
| **repository unusable** — the repository prevented the mutation *before any file was written*: a dirty working tree, invalid tissues content, a Git inspection failure, or an upstream that will not fast-forward (§1.5) | untouched | nil |
| **written but not committed** — canonical files were written but staging or committing did not complete (§1.7); repair is required | changed on disk, not committed | nil |
| **committed locally but not pushed** — the mutation is committed and durable; only publication failed (§1.6) | mutated and committed | the object |

Note what "repository unusable" does *not* cover: it is not a blanket class
for every Git failure. A Git failure after canonical files are written is
"written but not committed", and a Git failure after the commit exists is
"committed locally but not pushed". Confusing the three would tell an adapter
the wrong thing about whether the caller's change survived.

The result-object rule follows from the table and holds for all six mutating
operations: **a non-nil domain object is returned only when the mutation
succeeded — including an idempotent no-op — or alongside "committed locally
but not pushed", where it is committed and durable.** Every other outcome
returns nil, so a caller can never mistake a transient in-memory object for
canonical state. `ListIssues` and `GetIssue` return nil on any error.

Errors wrap git's own output where it is useful.

### 3.6 What the service is not

There is no authentication and no authorization. A comment's `Author` is
self-asserted domain provenance and grants nothing; there is no actor domain
object, and issues carry no creator or editor field. Git host credentials
authenticate the Git process only.

There is no assignment, no queue, no workflow, no labels, no priorities, no
search index and no database.
