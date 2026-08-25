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

`Created` and `Updated` are recorded in the document as canonical RFC3339Nano
in UTC. All of these are valid:

```
2026-08-23T15:21:33Z
2026-08-23T15:21:33.1Z
2026-08-23T15:21:33.123456789Z
```

Canonical means there is exactly one spelling per instant: a whole second
carries no fractional part at all, and a fractional part carries no trailing
zeros. `…:33.0Z` and `…:33.500Z` are therefore rejected, as is any offset
other than `Z`. Precision below a nanosecond does not exist.

They are domain facts. They are never inferred from file modification time,
Git commit time, a filename, or an ID.

`Created` is immutable. `Updated` must not be earlier than `Created`.

**`Created` is the domain event timestamp used for comment chronology.** That
is why the representation carries sub-second precision: two comments written
inside the same second must still be distinguishable (§2.6).

### 2.6 Comment ordering

Comments are presented in `Created` ascending order, tie-broken by `ID`
ascending. Because IDs are unique, this is a total order and is therefore
deterministic across processes and machines.

`Updated` is never consulted for ordering. **Editing a comment never changes
its conversational position**, no matter how much later the edit happens.

The ID tie-break exists for canonical comments that arrive from outside
tissues — imported or hand-written — and happen to carry an identical
`Created`. Comments that tissues creates itself do not tie, because of the
rule in §3.2: when adding a comment, tissues assigns a `Created` timestamp
strictly later than the latest existing comment on that issue. If the
wall-clock candidate is not later, tissues advances it by one nanosecond.
That is what preserves conversational order for normal tissues writes, even
when two comments are submitted inside one clock tick.

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
- a timestamp that is not canonical RFC3339Nano in UTC;
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

One application service owns every issue and comment operation. Adapters over
it are thin transports that own no domain semantics of their own. REST, MCP
and the `tissues serve` command are implemented (§4, §5). **HTML rendering and
any browser interface are not implemented.**

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

`Created` is the wall clock, except that it is always strictly later than the
`Created` of every comment already on that issue: if the wall-clock candidate
is not later than the latest existing comment, tissues advances it by one
nanosecond. Comments created through the service therefore preserve the order
the service was called in, even when several arrive within one clock tick, and
even if the clock stands still or steps backwards. This needs no stored
sequence, counter or index — the issue's own comments, already in canonical
order, carry everything the decision depends on.

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

---

## 4. HTTP interface

`internal/rest` is a transport adapter over the service. It decodes requests,
encodes responses and maps outcome classes to status codes. It holds no issue
or comment semantics, and it reaches the domain only through the eight
service operations.

Every request's `context.Context` is passed straight into the service
operation, and from there into every git command.

### 4.1 Routes

```
GET    /api/issues                                list the complete hierarchy
POST   /api/issues                                create an issue
GET    /api/issues/{id}                           one issue, with children and comments
PUT    /api/issues/{id}                           update title and/or description
POST   /api/issues/{id}/close                     close
POST   /api/issues/{id}/reopen                    reopen
POST   /api/issues/{id}/comments                  add a comment
PUT    /api/issues/{id}/comments/{commentID}      edit a comment's body
```

That is the whole surface. There is no filtering, pagination, search, delete,
move, or assignment route.

### 4.2 Representations

Transport shapes live in `internal/rest`; `internal/model` carries no JSON
tags, because the domain does not know it is served over HTTP. Names are
snake_case and timestamps are RFC3339 strings, carrying whatever sub-second
precision the domain timestamp has (§2.5).

An issue:

```json
{
  "id": "…", "title": "…", "state": "open",
  "created": "2026-08-23T13:20:11Z", "updated": "2026-08-23T14:02:44Z",
  "description": "…", "parent_id": "",
  "children": [], "comments": []
}
```

`children` and `comments` are always arrays, never `null`, at every level of
the hierarchy. `parent_id` is the empty string for a root issue. No
filesystem path and no Git metadata appears in a response.

A comment:

```json
{
  "id": "…", "author": "…",
  "created": "2026-08-23T13:41:02Z", "updated": "2026-08-23T13:41:02Z",
  "body": "…"
}
```

`GET /api/issues` wraps the roots: `{"issues": [ … ]}`.

Request bodies carry only what the caller may set. Creating an issue takes
`parent_id`, `title` and `description`; updating one takes `title` and
`description`, where an omitted field is left untouched (§3.2). Adding a
comment takes `author` and `body`; editing one takes `body` alone — the
transport offers no way to change a comment's ID, author, or either
timestamp.

`author` is provenance only. HTTP does not authenticate it (§4.6).

### 4.3 Request decoding

A request body must be exactly one JSON object. Malformed JSON, an unknown
field, and a trailing second JSON value are each rejected with
`400 invalid_request`. So is any body that is not a JSON object: a string, a
number, a boolean, an array, or `null` — which matters because Go's JSON
decoder would otherwise accept `null` for a struct and leave it zero-valued,
making an update request look like one with every field omitted. An empty
object `{}` is a JSON object and goes through as an ordinary request, which
for an update means every field omitted and therefore a no-op.

There is no schema system and no negotiation.

### 4.4 Status and error mapping

Errors are always JSON, never plain text:

```json
{"code": "invalid_request", "error": "a useful message"}
```

The outcome classes of §3.5 map one-to-one onto status codes:

| Outcome | Status | `code` | `result` |
|---|---|---|---|
| success — create | 201 | — | the object |
| success — read, update, close, reopen, edit | 200 | — | the object |
| not found | 404 | `not_found` | absent |
| invalid request | 400 | `invalid_request` | absent |
| repository unusable | 409 | `repository_unusable` | absent |
| written but not committed | 500 | `incomplete` | absent |
| committed locally but not pushed | 502 | `not_pushed` | **the committed object** |
| anything unclassified | 500 | `internal` | absent |

Those six codes are the complete public set.

Routing failures use the same envelope and the same codes. An unsupported
method on a real API path is `405` with `invalid_request` and an `Allow`
header; an unknown path below `/api/` is `404` with `not_found`. No API
response is ever plain text. (The non-API root `/` is not an API path and is
not covered by this.)

`409 repository_unusable` covers a dirty working tree, invalid tissues
content, and an upstream that will not fast-forward. Nothing was mutated.

`500 incomplete` states in its message that canonical files may have been
written to the working tree but the intended Git commit was not completed,
and that the repository needs manual repair before further changes.

### 4.5 `502 not_pushed` carries the result

This is the one response that returns both an error and a domain object,
because the service does: the mutation is committed in the local Git
repository and only publication failed.

```json
{
  "code": "not_pushed",
  "error": "… including git's own detail …",
  "result": { "id": "…", "title": "…", … }
}
```

**A client must not blindly retry a non-idempotent create merely because the
status is not 2xx.** Inspect `code`: on `not_pushed` the issue or comment
already exists locally, and retrying would create a duplicate. The `result`
object is the created or updated one. A later successful mutation publishes
the accumulated local commits.

### 4.6 No authentication

v0 has no authentication, no authorization, no sessions, no cookies and no
CORS. Anyone who can reach the port can create, edit and close issues, and
can cause the process to push to its Git remote with the credentials that
process holds. The default listener is therefore loopback-only, and exposing
the server to an untrusted network is unsupported and unsafe.

### 4.7 `tissues serve`

```
tissues serve [-repo .] [-addr 127.0.0.1:8080] [-remote-sync=true]
```

| Flag | Default | Meaning |
|---|---|---|
| `-repo` | `.` | Git repository holding the tissues data |
| `-addr` | `127.0.0.1:8080` | Listen address; the loopback default is load-bearing (§4.6) |
| `-remote-sync` | `true` | Select the remote-synchronized mode (§1.4) rather than local mode (§1.3) |

`-remote-sync` selects between the two existing service modes. The command
introduces no third Git behaviour.

At startup the command constructs one service, which verifies the directory
is a Git working tree, and then reads and validates the entire issue tree once
before listening. The same service pointer backs REST at `/api/` and MCP at
`/mcp`, so both transports share one mutex and one transaction sequence. A
repository that is not valid Git, or that holds invalid tissues content,
fails at startup rather than on the first request. A repository with no
`issues/` directory is valid and starts normally.

tissues never initializes a repository, creates one, or configures a remote.
Those stay ordinary Git responsibilities.

`tissues` with no command, or with an unknown command, prints usage and exits
non-zero. `SIGINT` and `SIGTERM` shut the server down gracefully.

---

## 5. MCP interface

`internal/mcpserver` is a transport adapter over the same application service
as REST. MCP tools do not define separate issue or comment semantics.

### 5.1 Transport

MCP is served at `/mcp` on the same HTTP server as REST, using the official Go
MCP SDK's Streamable HTTP handler. The handler is stateless and uses JSON
responses. Stateless mode means tissues retains no MCP application session
state and makes no server-to-client requests; each bounded tool call operates
entirely through the shared service.

### 5.2 Tools

There are exactly eight tools:

| Tool | Arguments |
|---|---|
| `list_issues` | none |
| `get_issue` | `id` |
| `create_issue` | optional `parent_id`, required `title`, optional `description` |
| `update_issue` | `id`, optional `title`, optional `description` |
| `close_issue` | `id` |
| `reopen_issue` | `id` |
| `add_comment` | `issue_id`, `author`, `body` |
| `edit_comment` | `issue_id`, `comment_id`, `body` |

Inputs and outputs are typed. The SDK infers their JSON Schemas from Go
types, except for the recursive issue output: SDK v1.7.0 rejects recursive Go
types during inference, so tissues supplies the equivalent recursive output
schema explicitly.

### 5.3 Results

Issue results contain `id`, `title`, `state`, canonical `created` and `updated`
strings, `description`, `parent_id`, recursive `children`, and `comments`.
Comment results contain `id`, `author`, canonical `created` and `updated`
strings, and `body`. Empty issue lists, children, and comments are empty arrays.
Filesystem paths and Git metadata are not exposed.

Ordinary service failures become MCP tool results with `IsError=true` and
useful text content. They are tool execution failures, not JSON-RPC protocol
errors, and carry no structured result.

`ErrNotPushed` is also returned with `IsError=true`, but it retains the normal
structured issue or comment result. Its warning states that the mutation is a
durable local Git commit, publication failed, and the caller must not blindly
retry it.

### 5.4 Security

MCP has no authentication or authorization. The loopback listener default is
therefore load-bearing. REST and MCP use the same process Git credentials;
exposing either interface directly to an untrusted network is unsupported and
unsafe.
