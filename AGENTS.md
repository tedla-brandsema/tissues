# Working on tissues

Development instructions for agents (and humans) changing this codebase.

This file is about *building* tissues. It is not documentation for agents
*using* tissues; that will live in MCP tool descriptions and product docs.

## What tissues is

An embarrassingly simple issue tracker whose canonical data is ordinary
Markdown files in an ordinary Git repository. Humans and agents share it.

Layering, top to bottom:

```
REST (human/software clients)   MCP (agents)
                  \             /
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
- Canonical issue data is Markdown on disk. The filesystem is the state;
  there is no cache and no index.
- Use the standard library where practical. Third-party dependencies are
  added only when a task concretely requires one (currently: Goldmark for
  rendering, the official Go MCP SDK for MCP — neither is needed yet).
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

Two rules in the service are load-bearing, not stylistic:

- It never retains a `store.Tree` between calls. Every operation loads fresh
  filesystem state under the mutex, which is what makes a restarted process
  identical to a running one.
- Every mutation requires a clean working tree and index, and stages exact
  paths. `git add .` must never appear.
