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
npm run e2e
```

The Playwright suite qualifies user-critical browser contracts in both Firefox
and Chromium against deterministic local frontend fixtures.

The local launcher is prepared to run both Services against the shared
`tissues-dev` Identity Platform and the named `tissues-native-dogfood`
Firestore Native database. Supply the development Identity Platform API key at
launch time:

```sh
./run-tissues-local.sh '<identity-platform-api-key>'
```

The key is passed only to the process environment; do not store a real key in
the repository. Do not run the launcher until the dogfood database has been
created and approved for provider testing in F6. Local dogfood uses stable
TenantID `64ovir4zjz42qfw6paawmyffga` and private bucket
`tissues-dev-tissues-assets-dogfood`. Production instead uses the separately
deployed `tissues-native` database, TenantID
`7womw3jzkek74oggxj6f42xak4`, and bucket
`tissues-dev-tissues-assets-production`.

## Deploy to Cloud Run

Build the complete Linux/amd64 application binary locally:

```sh
./build.sh
```

Deploy the resulting `build/server` artifact with:

```sh
./deploy.sh
```

Cloud Build receives only the prebuilt binary and its minimal Dockerfile; repository
source is not submitted to Cloud Build. On first deployment, provide the existing
Identity Platform API key through the local environment so it can be initialized in
Secret Manager without placing it on a command line or in a repository file:

```sh
export TISSUES_IDENTITY_API_KEY='...'
./deploy.sh
```

Later deployments reuse the existing Secret Manager value and do not require the key
locally. The deployment uses `tissues-dev`, `europe-west4`, the `containers` Artifact
Registry repository, and the `tissues` Cloud Run service.

Source composition is Firestore Native only. The currently serving production
revision was built from older Datastore source and remains deployed until F6;
that deployment history is not a supported compatibility or fallback path. F6
performs a one-way clean production replacement after database setup and
provider qualification. No data migration is required.

Typed Go `Config` structs are the configuration schema. A named `Profile[T]`
is resolved, validated, immutable, and revisioned. Values resolve as:

```text
defaults < named profile < environment < explicit CLI flags
```

The outer profile contains Server, deployment-level Firestore, auth, and tissues
contributions. `firestore.project_id` and `firestore.database_id` select the one
named deployment database shared by the Auth and Tissues persistence adapters.
Every Service type
contributes typed config even when inactive; `auth.enabled` and
`tissues.enabled` control concrete activation. Each active Service receives
only its own `Profile[Config]`/`Slot[Config]`. Cloud Run's bare `PORT` override
belongs solely to `server.Config.Port`.

Auth and tissues own their frontends under `services/auth/frontend` and
`services/tissues/frontend`. The tissues React/TypeScript application consumes
reusable shadcn-derived primitives and Tailwind v4 theme styles from
`lib/frontend`; there is no global cross-Service SPA. Its same-origin browser
API is namespaced beneath `/api/tissues/v1`.

Source composition uses one explicitly named Firestore Native client for both
the global OAuth authorization-code store and tenant-local Tissues repository.
Production uses `tissues-dev / tissues-native`; local dogfood uses the separate
`tissues-dev / tissues-native-dogfood` database. Their stable bootstrap
TenantIDs are respectively `7womw3jzkek74oggxj6f42xak4` and
`64ovir4zjz42qfw6paawmyffga`. Database separation is required because OAuth
authorization codes are global issuer state rather than tenant-scoped data, and
production and dogfood are different issuers. One Service retains the root
stores and binds them after the per-operation tenant decision, so a future
request-derived resolver can serve multiple tenants without changing protocol
or persistence contracts.
Authentication enforcement remains independently optional and preserves exact
safe local return URLs. Secrets are tagged and redacted from configuration
diagnostics.

Existing Issues may own private JPEG or PNG assets in Cloud Storage. Uploads
are limited to 6 MiB, decoded and freshly encoded server-side, constrained to a
1200-pixel longest edge and 1 MiB stored size, and served only through
authenticated same-origin API URLs. Original uploads and source metadata are
not retained. Pixel orientation is authoritative in this slice; JPEGs that
depend on EXIF orientation may display unrotated. Production and local dogfood
use separate private buckets.

The shared installation contains Projects keyed by immutable uppercase prefixes
such as `FLUENT`. The product presents that key as the Project ID. Each Project
transactionally allocates immutable Issue numbers, producing a stable,
human-facing Issue ID such as `FLUENT-17`. No separate opaque Issue identity
exists: the IssueRef is canonical within its bound tenant and is the identity
exposed by the browser API.

The browser has two navigation areas: Projects and Issues. Each opens a
cursor-paged table in the main work area; creation and editing use dedicated
main views rather than contextual controls in the side navigation. Project
IDs and Issue IDs remain immutable. Issue create and edit forms contain only
Project selection (on create), title, and Markdown description. Existing Issue
hierarchy changes use the dedicated parent dialog. The Issues overview can be
filtered by Project; that persistent filter also defaults the Project selected
when creating an Issue. Choosing a different Project in the create form does
not change the overview filter.
