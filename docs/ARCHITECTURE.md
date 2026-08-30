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
            +-- inactive Firestore Native adapter
            +-- private GCS Issue asset adapter
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
remain rejected. Authentication identifies an actor. During the bootstrap
transition every operation resolves to one restart-required configured Tissues
TenantID. The Service retains root Repository and AssetStore boundaries,
resolves the TenantID from the operation context exactly once, and binds the
required roots with that same ID. The current resolver ignores actor context;
later principal-aware selection will replace only that decision. Tenant
identity is never inferred from email, subject, OAuth client, host, or
Datastore namespace.

## Tissues domain and persistence adapters

Project is the only layer above the single Issue type. A canonical immutable
Project key scopes a transactional `NextIssueNumber` allocator. Each Issue's
canonical identity is its immutable `IssueRef`, such as `FLUENT-17`. Parentage
is persisted directly as a canonical same-Project `ParentRef`; identity and hierarchy
assertions occur in the same transaction as mutation.
Comments belong to an Issue. Markdown is canonical rich text; trusted client
HTML is not persisted.

Existing Issues may also own JPEG and PNG assets through a root `AssetStore`
that must be bound with `ForTenant` before it exposes `Put`, `Open`, or `List`.
The GCS adapter inventories deterministic
`tenants/{tenantID}/issues/{PROJECT}/{NUMBER}/{filename}` objects in separate private production
and dogfood buckets; no Datastore Asset entity is introduced. Upload input is
bounded to 6 MiB, decoded and freshly encoded as pixels, and persisted only
after normalization to a 1200-pixel longest edge and 1 MiB maximum. Original
bytes and source metadata are discarded. Assets are read through authenticated
same-origin API routes rather than public or signed GCS URLs.

TenantID and Comment IDs are 16 random bytes encoded as lowercase, unpadded
base32 (26 characters, no timestamp semantics). TenantID is Tissues-owned,
stable across processes, and selected from typed configuration rather than
generated at startup. A Service does not belong permanently to one tenant: it
retains the root Repository and AssetStore, resolves the authoritative TenantID
for each operation, and binds the roots through `ForTenant`. Ordinary domain
and asset operations exist only on the returned tenant stores. Asset operations
resolve once and use that exact ID for both Issue lookup and object access.

Within the existing environment namespace, a non-persisted
`tissues_tenant/{tenantID}` ancestor scopes every domain query and key. The
namespace is transitional storage configuration, not tenancy. A
`tissues_project` named key beneath that ancestor owns its allocator. Its
`tissues_issue` children use the canonical IssueRef as their named identity.
`tissues_comment` entities descend from their Issue. Issue
hierarchy is relationship data, never Datastore ancestry. Descriptions and
bodies are unindexed; trees, parent issue IDs, and comments are derived and
deterministically sorted in Go. Domain timestamps are stored as Unix-nanosecond
integers so Datastore's native timestamp precision cannot erase the required
one-nanosecond comment ordering.

This transitional ancestry puts every Project below the same tenant root key,
so a tenant's descendants share entity-group ancestry. That enables strongly
consistent tenant ancestor queries, but under Datastore's
`OPTIMISTIC_WITH_ENTITY_GROUPS` concurrency mode it also makes tenant-wide
writes share the legacy entity-group contention boundary and one-write-per-
second limit. Other Datastore concurrency modes use their documented
overlapping-data transaction contention behavior instead. This transitional
adapter is not the next persistence epoch; F3 replaces it with Firestore
Native before activation.

The implemented but inactive `services/tissues/firestore` adapter accepts an
already-created official Firestore client and exposes only the same root
`ForTenant(TenantID)` boundary. Application composition still constructs and
uses Datastore; named-database runtime configuration and Firestore activation
belong to the later cutover phase. The Native adapter uses tenant-local flat
collections and does not require a tenant document:

```text
tenants/{tenantID}/projects/{PROJECT}
tenants/{tenantID}/issues/{PROJECT-NUMBER}
tenants/{tenantID}/comments/{IssueRef}~{CommentID}
```

Project, Issue, and Comment documents carry redundant canonical identity for
corruption detection. All domain chronology remains signed Unix-nanosecond
integers rather than Firestore timestamp values. Project pages order by
document ID. Issue overviews order by `updated_ns DESC, project_key ASC,
number ASC`; their versioned base64url cursors bind tenant, query kind, and
optional Project filter to the exact resume tuple.

Native transactions use only transaction-scoped reads and queries before
queuing writes. New Project, Issue, and Comment documents use create-if-absent
semantics after an absent read. Each existing Issue write increments its
persistence-only `comment_order_revision`; the unchanged logical Issue write
at the end of AddComment is therefore a real per-Issue serialization fence.
This preserves strict Comment chronology with Standard edition's default
pessimistic server transactions and remains correct under optimistic
concurrency because two commits cannot succeed from the same observed Issue
revision. No tenant-global fence is introduced.

The adapter's required Standard collection-scope indexes and recommended
single-field exemptions live in
`services/tissues/firestore/firestore.indexes.json`. They cover unfiltered and
Project-filtered Issue overview orderings and the transaction-scoped latest
Comment query; `description` and `body` are exempted because they are not
queried. The file is declarative evidence only in this phase and is not wired
to deployment.

IssueRefs intentionally remain tenant-local human identities: two tenants may
both contain `FLUENT-17`, with the bound repository determining which entity is
addressed. Provider cursors are wrapped with their TenantID so replay against a
different bound tenant is rejected before query execution.

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
and supplies the initial Project for Issue creation. The read model validates
canonical IssueRefs and ParentRefs without loading recursive children/comments. Project-scoped Issue
trees remain the source for parent-Issue-ID suggestions.

Issue create and PATCH payloads contain content only: title and canonical
Markdown description. Hierarchy is changed only for an existing Issue through
the explicit parent route and dedicated browser dialog. That transaction
resolves and validates the parent before persisting the relationship; an empty
parent issue ID detaches the Issue.
