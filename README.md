# 🤧 tissues

GCP-native issue tracking for humans and agents.

The Cloud Run deployment target is the executable `app/gcp/server`. Code under
`app/` composes executables, `services/` contains concrete in-process Services,
and `lib/` contains reusable libraries and SDKs. `lib/server` owns the single
listener and process lifecycle; `lib/service` defines the contract implemented
by `services/auth` and `services/tissues`. The historical Git-backed product is
parked at `archive/git-backed-v0`.

```sh
go build ./app/gcp/server
```

Typed Go `Config` structs are the configuration schema. A named `Profile[T]`
is resolved, validated, immutable, and revisioned. Values resolve as:

```text
defaults < named profile < environment < explicit CLI flags
```

The outer profile contains Server, auth, and tissues contributions. Every
Service type contributes typed config even when inactive; `auth.enabled` and
`tissues.enabled` control concrete activation. Each active Service receives
only its own `Profile[Config]`/`Slot[Config]`. Cloud Run's bare `PORT` override
belongs solely to `server.Config.Port`.

Auth and tissues own their frontends under `services/auth/frontend` and
`services/tissues/frontend`. The planned shared frontend stack is React,
shadcn/ui, and Tailwind CSS; it is not implemented in this slice.

The tissues Service uses Cloud Datastore through ADC. Its typed storage config
requires an explicit project ID and defaults its namespace to `tissues`.
Authentication enforcement remains independently optional and preserves exact
safe local return URLs. Secrets are tagged and redacted from configuration
diagnostics.
