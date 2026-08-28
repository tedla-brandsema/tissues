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
    +-- services/auth
    |       +-- lib/auth/broker
    |       +-- lib/gcp/auth
    |
    +-- services/tissues
            +-- tissues domain
            +-- tissues Datastore adapter
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
`services/tissues` owns the tissues contribution, Issue and Comment domain,
repository contract, bootstrap routes, `services/tissues/frontend`, and its
schema-specific Cloud Datastore adapter under `services/tissues/datastore`.

Each relying Service controls authentication enforcement independently. When
tissues enforcement is enabled, signed local state preserves the exact safe
original request URI through broker login and callback; unsafe external targets
remain rejected. Authentication identifies an actor but does not partition the
single shared issue workspace.

## Tissues domain and Datastore

There is exactly one Issue type. Parentage is mutable `ParentID` relationship
data, not an Epic/Story/Task taxonomy. Comments belong to an Issue. Markdown is
canonical rich text; trusted client HTML is not persisted.

Issue IDs and Comment IDs are 16 random bytes encoded as lowercase, unpadded
base32 (26 characters, no timestamp semantics). Datastore uses that value as a
named StringID. `tissues_issue` entities are root keys and store only canonical
Issue fields. `tissues_comment` entities are children of their Issue key and
store only canonical Comment fields. Descriptions and bodies are unindexed;
children and comments are derived and deterministically sorted in Go. Domain
timestamps are stored as Unix-nanosecond integers so Datastore's native
timestamp precision cannot erase the required one-nanosecond comment ordering.

Service-specific frontends live with their Service. Shared frontend facilities
will live in `lib/frontend`; the planned stack is React, shadcn/ui, and Tailwind
CSS, but that frontend implementation belongs to a later slice.
