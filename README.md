# tissues

tissues is an embarrassingly simple Git-backed Markdown issue tracker for
humans and agents.

Issues and comments are ordinary Markdown files in an ordinary Git
repository. tissues adds only the things Git does not: immutable IDs, domain
timestamps, open/closed state, containment, comment ordering, and browser,
REST and MCP interfaces over all of it. Git keeps the history, the replication
and the remotes.

## Build

```bash
go build -o tissues ./cmd/tissues
```

The MCP interface uses the official Go MCP SDK.

## Prepare an issue repository

tissues does not create repositories, and it does not manage Git
credentials. Point it at a Git repository you already control — usually a
clone:

```bash
git clone git@github.com:you/my-issues.git
```

Or start one from scratch:

```bash
mkdir my-issues
cd my-issues
git init -b main
git remote add origin git@github.com:you/my-issues.git
```

A repository with no `issues/` directory is valid and starts fine; the first
issue creates it.

## Run

```bash
./tissues serve -repo /path/to/my-issues
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-repo` | `.` | Git repository holding the tissues data |
| `-addr` | `127.0.0.1:8080` | Address to listen on |
| `-remote-sync` | `true` | Pull before and push after every change |

With `-remote-sync=true` (the default), each change runs `git pull --ff-only`
before the mutation and `git push` after it, using whatever Git credentials
the tissues process already has. On the very first change in a fresh
repository it runs `git push --set-upstream origin HEAD`.

For local-only commits, with no remote contact at all:

```bash
./tissues serve -repo /path/to/my-issues -remote-sync=false
```

Every change requires a clean working tree and index. tissues will refuse to
run while you have unrelated uncommitted work in that repository, so it can
never sweep your changes into its commits.

## Browser

Open [http://127.0.0.1:8080/](http://127.0.0.1:8080/) after starting the
server. The server-rendered interface uses the same issues and service as REST
and MCP, requires no JavaScript, safely renders Markdown, and supports the
ordinary issue and comment lifecycle operations.

## REST

```bash
# list the whole issue hierarchy
curl -s localhost:8080/api/issues

# create an issue (add "parent_id" to create a child)
curl -s -X POST localhost:8080/api/issues \
  -d '{"title":"Fix token refresh","description":"It expires early."}'

# comment on it
curl -s -X POST localhost:8080/api/issues/$ID/comments \
  -d '{"author":"you@example","body":"I reproduced this."}'

# close it
curl -s -X POST localhost:8080/api/issues/$ID/close
```

The full route set is eight endpoints: list and create issues, get and update
one issue, close, reopen, add a comment, edit a comment. See `docs/SPEC.md`
for the request and response shapes and the error codes.

One note worth knowing before you write a client: a `502` with
`"code": "not_pushed"` means the change **was** committed to your local Git
repository and only the push to the remote failed. Do not retry the request —
the issue or comment already exists. Fix the remote and the next change
publishes the backlog.

## MCP

The same server exposes the eight service operations as MCP tools over
Streamable HTTP:

```text
http://127.0.0.1:8080/mcp
```

The tools are `list_issues`, `get_issue`, `create_issue`, `update_issue`,
`close_issue`, `reopen_issue`, `add_comment`, and `edit_comment`. MCP and REST
share one service and one repository, so agents and humans see the same issue
hierarchy and comments. There is no stdio mode.

An MCP tool result marked as an error because a push failed still carries the
committed issue or comment as structured output. Its warning says that the
mutation exists locally and must not be blindly repeated.

## Safety

**v0 has no HTTP authentication.** This applies to the browser UI, REST and
MCP. The default listener is loopback-only. Browser mutation forms additionally
require a same-origin loopback `Origin`; this prevents a foreign webpage from
submitting an ordinary form to a local tissues process, but it is not
authentication. Do not expose the server directly to an untrusted network:
anyone who can reach the port can create, edit and close issues through REST or
MCP, and can cause the process to push to your Git remote with its credentials.

## Canonical storage

The truth is the Markdown, not the server. After the calls above:

```bash
$ git -C my-issues log --oneline
9f2c1ab comment mfz4x... on issue k7qd2...
3e81b40 create issue k7qd2...: Fix token refresh

$ cat my-issues/issues/k7qd2*/issue.md
# Fix token refresh

<!-- tissues:issue:v0 -->
- **ID:** `k7qd2...`
- **State:** open
- **Created:** 2026-08-23T13:20:11Z
- **Updated:** 2026-08-23T13:20:11Z

---

It expires early.
```

Nothing is cached and nothing is indexed. Point tissues at a fresh clone of
the same repository and it reconstructs identical state, because the files
are the state.

## Not implemented in v0

There is no authentication, assignment, labels, priorities, queues, workflow,
or search.
