# GCP-native architecture

Slice A establishes a multi-module Go workspace with two composition roots:
the bootstrap tissues service and its separate authentication broker. Shared
HTTP lifecycle, authentication, templates, filesystem helpers, and signing
helpers live in focused `lib/` modules. It intentionally defines no Issue or
Comment domain model and makes no issue-persistence decision.

## Shared workspace

The first GCP-native tissues product is one shared issue workspace.
Authentication identifies the actor performing an operation; it does not
define the data partition. There is no Workspace or Tenant domain object.

## Deferred domain contracts

Slice B will define Issue and Comment persistence. The planned public domain ID
contract remains 16 bytes from `crypto/rand`, encoded as lowercase base32 with
no padding: 26 characters with no timestamp semantics. Retained authentication
tokens do not change that future domain contract.

Markdown remains the intended canonical rich-text representation. No
HTML-oriented editor or trusted client-supplied HTML persistence is part of
this slice, and no editor framework has been selected.

## Typed configuration and profiles

The typed Go configuration model is:

```text
Config type          = typed schema
Profile[T]           = named, revisioned, resolved and validated Config
Application config   = stable outer server + mandatory service contributions
Service profile      = independently reloadable typed contribution
Slot[T]              = atomic currently active Profile[T]
```

Every service has a configuration contribution, including an explicit empty
typed contribution when it currently has no configurable fields:

```text
Application profile
    |
    +-- outer/server config
    |
    +-- service config contribution(s)
            |
            +-- Profile[ServiceConfig]
                    |
                    +-- Slot[ServiceConfig]
```

Defaults are declared on Config fields. A candidate resolves only present
values in the fixed order `defaults < profile < environment < CLI`, then runs
one final Valex pass and optional cross-field validation. Published profiles
are immutable snapshots. Reload builds and validates a complete candidate
before atomic replacement; invalid candidates leave the active revision
unchanged, and an effective no-op does not advance its monotonically increasing
revision. Changed fields are classified as live or restart-required.

Profile persistence is separate from profile semantics. Slice A-R provides
memory and local strict JSON/YAML stores. Cloning copies one definition into a
new name whose revisions evolve independently. There is deliberately no profile
inheritance, base-profile chain, filesystem watcher, or GCP profile store.

In short:

```text
service implementation
+ mandatory typed config contribution
+ profile
= service instance
```

Cloning creates another configuration instance, not another codebase. Services
receive only their typed sub-configuration, never an untyped application-wide
map.

## Authentication composition

Central authentication remains a separate deployable service. Tissues can
independently disable relying-party enforcement or enable it around its browser
routes. When enabled, signed local state carries the exact safe original
request URI through broker login and callback; external redirect targets are
rejected. Subject and email enter downstream request context, without adding
domain authorization in this slice.

## Frontend ownership

Every deployable service owns its frontend beneath `app/.../<service>/frontend`.
Shared frontend components and tooling belong under `lib/frontend`; service
frontends import and consume them. There is no global SPA that owns all
services. The selected future shared stack is React, shadcn/ui, and Tailwind
CSS, but no Node tooling or implementation of that stack belongs in this slice.
