# GCP-native architecture

## Deployment boundary

A Server is the process and deployment boundary. A Service is an in-process
component hosted by a Server. One Server can explicitly compose multiple
Services; Services do not own listeners, `PORT`, signals, or Cloud Run
lifecycle.

GCP deploys `app/gcp/server` to Google Cloud Run. The executable owns process
composition, `lib/server` owns the single HTTP listener and graceful lifecycle,
`lib/service` defines the in-process Service SDK, and the concrete auth and
tissues Services run inside that process. Server runtime != Service SDK !=
concrete Service. GCP-native deployment means Cloud Run: this architecture
introduces no GKE, Kubernetes, App Engine, or Compute Engine/VM deployment
assumptions.

```text
Google Cloud Run
    |
    v
app/gcp/server
    owns explicit process composition
    |
    +-- lib/server
    |       Server/process runtime: one listener and lifecycle
    |
    +-- lib/service
    |       in-process Service SDK
    |
    +-- lib/frontend
    |       reusable source-owned UI primitives, theme, and utilities
    |
    +-- services/auth
    |       +-- lib/auth/broker
    |       +-- lib/gcp/auth
    |
    +-- services/tissues
            +-- tissues domain
            +-- tissues Datastore adapter
            +-- same-origin /api/tissues/v1 JSON boundary
            +-- frontend/ React workspace and embedded generated assets
```

Concrete `services/*` implement `lib/service`; construction remains explicit in
the application because each Service has different provider dependencies.

Service activation is concrete and independent. Both Service types always
contribute typed config to the outer application profile, while `Auth.Enabled`
and `Tissues.Enabled` determine whether that Service is constructed in a given
Server deployment. An inactive Service creates no provider client and need not
possess runtime credentials.

## Typed configuration and profiles

The outer application config is stable and contains `Server server.Config`,
`Auth auth.Config`, and `Tissues tissues.Config`. Each Service contribution is
resolved into its own immutable `Profile[Config]` and `Slot[Config]`; Services
receive only that typed handle, never the outer application profile.

Values resolve in the fixed order `defaults < profile < environment < CLI`.
Candidates are fully resolved and validated before atomic replacement. Invalid
reloads leave the active revision unchanged, and service-profile replacement
does not mutate outer Server configuration.

## Internal Services

`services/auth` owns the auth contribution, broker composition, routes,
behavior, and `services/auth/frontend`. Reusable broker infrastructure remains
in `lib/auth/broker`, and reusable GCP auth adapters remain in `lib/gcp/auth`.
`services/tissues` owns the tissues contribution, Project, Issue, and Comment domain,
repository contract, same-origin JSON and browser routes,
`services/tissues/frontend`, and its
schema-specific Cloud Datastore adapter under `services/tissues/datastore`.

Each relying Service controls authentication enforcement independently. When
tissues enforcement is enabled, signed local state preserves the exact safe
original request URI through broker login and callback; unsafe external targets
remain rejected. Authentication identifies an actor but does not partition the
single shared issue workspace.

## Tissues domain and Datastore

Project is the only layer above the single Issue type. A canonical immutable
Project key scopes a transactional `NextIssueNumber` allocator. Issues retain
an opaque 26-character entity identity for internal persistence and expose a
derived, stable human Issue ID such as `FLUENT-17`. Parentage is persisted as
opaque `ParentID` relationship data inside one Project and exposed through the
browser API as the derived parent issue ID; Issue-ID resolution and hierarchy
assertions occur in the same transaction as mutation.
Comments belong to an Issue. Markdown is canonical rich text; trusted client
HTML is not persisted.

Opaque Issue entity identities and Comment IDs are 16 random bytes encoded as
lowercase, unpadded base32 (26 characters, no timestamp semantics). A
`tissues_project` named key is the immutable entity-group root. Its
`tissues_issue` children retain opaque named identities, and the
`tissues_issue_ref` Issue-ID index maps the decimal number to that opaque
identity. `tissues_comment` entities descend from their Issue. Issue
hierarchy is relationship data, never Datastore ancestry. Descriptions and
bodies are unindexed; trees, parent issue IDs, and comments are derived and
deterministically sorted in Go. Domain timestamps are stored as Unix-nanosecond
integers so Datastore's native timestamp precision cannot erase the required
one-nanosecond comment ordering.

Service-specific frontends live with their Service. `lib/frontend` owns only
reusable shadcn-derived primitives, Tailwind theme/base styles, and generic
utilities. `services/tissues/frontend` owns the concrete React application and
API client. Vite emits committed assets beneath that Service for Go embedding;
the Server composition root does not know their filesystem layout.

The tissues browser JSON boundary is `/api/tissues/v1`. With authentication
enabled, initial HTML navigation retains the relying-party login redirect and
exact safe return URL, while unauthenticated API requests receive a structured
JSON 401. Trusted request identity supplies Comment authors at this HTTP
boundary; the domain remains provider-neutral.

## Browser information architecture

The tissues shell has navigation-only entries for `Projects` and `Issues`.
They select one of four explicit, query-string-restorable main views:

```text
ProjectsOverview -> ProjectView (create or existing)
IssuesOverview   -> IssueView   (create or existing)
```

Overview tables use opaque Datastore cursors with Previous/Next history in the
browser. Projects are ordered by canonical key. The global Issue overview is a
lightweight cross-Project read model ordered by `Updated` descending. Its
optional Project filter is an ancestor query, persists locally in the browser,
and supplies the initial Project for Issue creation. The read model derives
and validates human Issue IDs and parent issue IDs without exposing opaque Issue
entity identities or loading recursive children/comments. Project-scoped Issue
trees remain the source for parent-Issue-ID suggestions.

Issue create and PATCH payloads contain content only: title and canonical
Markdown description. Hierarchy is changed only for an existing Issue through
the explicit parent route and dedicated browser dialog. That transaction
resolves and validates the parent before persisting the relationship; an empty
parent issue ID detaches the Issue.
