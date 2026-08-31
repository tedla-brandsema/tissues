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
    |       +-- Firestore Native CodeStore in lib/auth/broker
    |       +-- lib/gcp/auth
    |
    +-- services/tissues
            +-- tissues domain
            +-- Firestore Native adapter
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

The outer application config contains `Server server.Config`, a deployment-level
`Firestore` contribution with `ProjectID` and `DatabaseID`, `Auth auth.Config`,
and `Tissues tissues.Config`. When either Service is active, the executable
requires a nonblank, whitespace-exact project and non-default named database.
When both are inactive it creates no Firestore client and needs no Firestore
configuration or ADC. Each Service contribution is resolved into its own
immutable `Profile[Config]` and `Slot[Config]`; Services receive only that typed
handle, never the outer application profile or physical database coordinates.

Values resolve in the fixed order `defaults < profile < environment < CLI`.
Candidates are fully resolved and validated before atomic replacement. Invalid
reloads leave the active revision unchanged, and service-profile replacement
does not mutate outer Server configuration.

## Internal Services

`services/auth` owns the auth contribution, broker composition, routes,
behavior, and `services/auth/frontend`. Reusable broker infrastructure remains
in `lib/auth/broker`, and reusable GCP auth adapters remain in `lib/gcp/auth`.
The broker has a Firestore Native authorization-code adapter rooted
at `oauthAuthorizationCodes/{sha256(rawCode)}`. Authorization codes are global
issuer state, not tenant data, and the raw bearer credential is never stored or
used as a document ID. `expires_unix` remains the synchronous semantic-expiry
authority; the matching Firestore timestamp `expires_at` exists only for
eventual TTL cleanup. The future TTL policy is collection group
`oauthAuthorizationCodes`, field `expires_at`, for cleanup only. Source
composition injects this adapter from the shared named Firestore client. The
currently deployed pre-F6 production revision was built from older Datastore
source; it is deployment history, not a current backend option. F6 installs and
verifies the TTL policy before activation.
`services/tissues` owns the tissues contribution, Project, Issue, and Comment domain,
repository contract, same-origin JSON and browser routes,
`services/tissues/frontend`, and its sole persistence adapter under
`services/tissues/firestore`.

Each relying Service controls authentication enforcement independently. When
tissues enforcement is enabled, signed local state preserves the exact safe
original request URI through broker login and callback; unsafe external targets
remain rejected. Authentication identifies an actor. During the bootstrap
transition every operation resolves to one restart-required configured Tissues
TenantID. The Service retains root Repository and AssetStore boundaries,
resolves the TenantID from the operation context exactly once, and binds the
required roots with that same ID. The current resolver ignores actor context;
later principal-aware selection will replace only that decision. Tenant
identity is never inferred from email, subject, OAuth client, host, database,
or project.

## Tissues domain and persistence

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

The `services/tissues/firestore` adapter accepts the one official
Firestore client created by `app/gcp/server` with
`firestore.NewClientWithDatabase`. The same client feeds the Auth CodeStore and
the Tissues repository and is closed once by the application. The adapter
exposes only the same root `ForTenant(TenantID)` boundary. It uses tenant-local
flat collections and does not require a tenant document:

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

Production targets project `tissues-dev`, named Standard Native database
`tissues-native`, in `europe-west4`, with bootstrap TenantID
`7womw3jzkek74oggxj6f42xak4`. Local dogfood targets the separate named database
`tissues-native-dogfood` in the same project, with bootstrap TenantID
`64ovir4zjz42qfw6paawmyffga`. Their GCS buckets are also physically separate.
OAuth authorization codes are global issuer state and production and dogfood
use different issuers, so TenantID cannot provide environment isolation;
database identity is deployment configuration and is not derived from TenantID.

Current source is Firestore Native only. The currently deployed pre-F6 revision
was built from older Datastore source:

```text
pre-F6 deployed revision
    older Datastore source

corrected F5 source; activated in F6
    Firestore Native Tissues state + Firestore OAuth codes only
```

There is no migration, dual read, dual write, shadow persistence, fallback, or
Datastore compatibility implementation. Outstanding old OAuth authorization
codes are disposable and clients may reauthorize. F6 prepares and qualifies
dogfood first, then prepares and qualifies production before the one-way clean
production replacement: create the dogfood database, install its required
indexes and TTL policy, qualify real Tissues and OAuth behavior, then create and
configure production, install its indexes and TTL policy, deploy, and qualify
production. None of those provider operations occur in F5.

The runtime service account retains Google's predefined
`roles/datastore.user` role as the Firestore data read/write role. Its
historical name does not represent a Datastore-mode compatibility path.

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

Overview tables use opaque provider cursors with Previous/Next history in the
browser. Projects are ordered by canonical key. The global Issue overview is a
lightweight cross-Project read model ordered by `Updated` descending. Its
optional Project filter uses `project_key` equality filtering with its declared
ordering/index, persists locally in the browser, and supplies the initial
Project for Issue creation. The read model validates
canonical IssueRefs and ParentRefs without loading recursive children/comments. Project-scoped Issue
trees remain the source for parent-Issue-ID suggestions.

Issue create and PATCH payloads contain content only: title and canonical
Markdown description. Hierarchy is changed only for an existing Issue through
the explicit parent route and dedicated browser dialog. That transaction
resolves and validates the parent before persisting the relationship; an empty
parent issue ID detaches the Issue.
