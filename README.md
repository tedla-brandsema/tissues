# 🤧 tissues

GCP-native issue tracking for humans and agents.

The Cloud Run deployment target is the executable `app/gcp/server`. Code under
`app/` composes executables, `services/` contains concrete in-process Services,
and `lib/` contains reusable libraries and SDKs. `lib/server` owns the single
listener and process lifecycle; `lib/service` defines the contract implemented
by `services/auth` and `services/tissues`. The historical Git-backed product is
parked at `archive/git-backed-v0`.

## Develop locally

Build the Server from the workspace root, or run it with a named JSON/YAML
profile from a directory outside the repository:

```sh
go build -o /tmp/tissues-server ./app/gcp/server
go run ./app/gcp/server --profile=local --profiles=/path/to/profiles
```

The browser workspace is an npm workspace. Its normal build refreshes the
generated assets embedded by the tissues Go Service:

```sh
npm install
npm test
npm run typecheck
npm run build
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
`services/tissues/frontend`. The tissues React/TypeScript application consumes
reusable shadcn-derived primitives and Tailwind v4 theme styles from
`lib/frontend`; there is no global cross-Service SPA. Its same-origin browser
API is namespaced beneath `/api/tissues/v1`.

The tissues Service uses Cloud Datastore through ADC. Its typed storage config
requires an explicit project ID and defaults its namespace to `tissues`.
Authentication enforcement remains independently optional and preserves exact
safe local return URLs. Secrets are tagged and redacted from configuration
diagnostics.
