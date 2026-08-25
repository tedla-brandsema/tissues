# Working on tissues

Development instructions for agents (and humans) changing this codebase.

This file is about *building* tissues. It is not documentation for agents
*using* tissues; that will live in MCP tool descriptions and product docs.

## What tissues is

An embarrassingly simple issue tracker whose canonical data is ordinary
Markdown files in an ordinary Git repository. Humans and agents share it.

Layering, top to bottom:

```
REST (software clients)   browser UI (humans)   MCP (agents)
                  \              |              /
                   service layer          <- owns all issue/comment semantics
                          |
              Markdown repository layer   <- internal/store
                          |
                       git CLI
```

REST and MCP are thin adapters over one service layer. There is never a
separate REST semantic and MCP semantic. New behaviour goes in the service,
not in an adapter.

## Rules

- Go is the implementation language.
- **Simplicity is a hard requirement.** Prefer boring, explicit code. Reject
  abstractions whose only justification is "we may need this later".
- Do not expand v0 scope speculatively. The only domain objects are Issue and
  Comment. If a task seems to need more, stop and say so instead.
- Every issue is the same type. Its optional attachment to another issue is
  mutable; containment is a relationship, not an Epic/Story/Task taxonomy.
- Canonical issue data is Markdown on disk. The filesystem is the state;
  there is no cache and no index.
- Use the standard library where practical. Third-party dependencies are
  added only when a task concretely requires one (currently: the official Go
  MCP SDK; Goldmark is reserved for implemented rendering work).
- Do not add queues, workflow engines, labels, priorities, assignment,
  databases, JavaScript frameworks, Git libraries, or GitHub API integration
  unless a later task explicitly asks for them.
- Git is reached through the installed `git` CLI, never through a library.

## Before reporting completion

```
gofmt -l .        # must print nothing
go vet ./...
go test ./...
```

Do not commit or push unless the task explicitly asks for it.

## Where things live

- `docs/SPEC.md` — the v0 specification. It describes only what is
  implemented. Keep it in step with the code; do not add roadmap material.
- `internal/model` — domain types and their invariants. Knows nothing about
  Markdown or the filesystem.
- `internal/store` — canonical Markdown serialization, strict parsing,
  validation and filesystem layout.
- `internal/gitcli` — a narrow wrapper around the `git` executable. Keep it
  narrow: it exists only to support the service transaction.
- `internal/service` — the one application service. Every issue and comment
  operation lives here.
- `internal/rest` — the HTTP transport adapter. REST is a transport adapter;
  issue and comment semantics stay in `internal/service`. Transport JSON
  shapes live here so `internal/model` carries no JSON tags.
- `internal/mcpserver` — the MCP transport adapter. Its nine tools map
  directly to service operations; MCP representations and schema concerns
  stay here, and domain semantics stay in `internal/service`.
- `internal/webui` — the server-rendered browser adapter. Templates, safe
  Markdown rendering, form transport concerns and browser security stay here.
- `cmd/tissues` — the `tissues serve` executable. Flags only; it adds no Git
  or domain behaviour of its own.

Two rules in the service are load-bearing, not stylistic:

- It never retains a `store.Tree` between calls. Every operation loads fresh
  filesystem state under the mutex, which is what makes a restarted process
  identical to a running one.
- Every mutation requires a clean working tree and index, and stages exact
  paths. `git add .` must never appear.
- Moving an issue must preserve its complete subtree and directory basename,
  reject cycles, and stage both the old and new directory paths exactly.
- REST, MCP and the web UI in one served repository must share the same
  `*service.Service`; never construct per-adapter services with separate mutexes.
- Only output from Goldmark's default safe renderer may become `template.HTML`;
  never enable `html.WithUnsafe` or trust arbitrary strings as HTML.
- Browser mutation forms require a same-origin loopback `Origin`.
- The HTTP listener defaults to loopback. v0 has no authentication and the
  process may hold Git push credentials, so do not change that default.
