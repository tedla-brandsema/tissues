# Working on tissues

Development instructions for people and agents changing this repository.

## Structure

- `app/` contains executable composition roots. Keep configuration and wiring
  there; domain and reusable infrastructure do not belong there.
- `services/` contains complete concrete in-process Services. Every concrete
  Service implements the `lib/service` SDK and owns its typed configuration.
- `lib/` contains reusable libraries and SDKs. Dependencies must remain explicit;
  do not introduce a service locator, dependency-injection framework, or
  speculative abstraction.
- Use the standard library where practical, including `http.ServeMux` for HTTP
  routing and `log/slog` for logging.
- A Server is the process/deployment boundary; a Service is an in-process
  component hosted by a Server. One Server can compose multiple Services.
  Services do not own listeners, `PORT`, signals, or Cloud Run lifecycle.
- `lib/server` is the Server/process runtime and does not define concrete
  Service behavior. `lib/service` is the in-process Service SDK and does not
  own listener or process lifecycle. Complete concrete Services do not belong
  under `lib/`.
- Every Service contributes typed configuration and owns its service-specific
  frontend beneath `services/<service>/frontend`. Shared frontend
  components and tooling belong under `lib/frontend`; service frontends
  consume them. `lib/frontend` must contain only reusable primitives, theme,
  and generic utilities; concrete domain types, API clients, routes, and views
  remain with their Service. There is no global SPA that owns all services.

## Product boundaries

- The active implementation is GCP-native. The prior Git storage behavior is
  historical and belongs only to `archive/git-backed-v0`.
- GCP deploys the Server application to Google Cloud Run. Do not introduce
  GKE, Kubernetes, App Engine, or Compute Engine/VM deployment assumptions.
- Do not transplant source-product concepts or generic page/content models.
- Slice A contains infrastructure and a bootstrap only. Do not infer unbuilt
  Issue, Comment, browser, REST, MCP, or persistence behavior from it.
- Once the domain arrives, there is exactly one Issue type. Relationships are
  data, not an Epic/Story/Task taxonomy.
- The first product is one shared issue workspace. Authentication identifies
  the actor; it does not partition issue data or create a Workspace/Tenant
  domain object.
- Markdown remains the intended canonical rich-text representation. Do not
  persist trusted client HTML or choose an editor without an explicit task.

## Engineering rules

- Simplicity is a hard requirement. Add abstractions only for concrete current
  behavior.
- Keep executable wiring thin and shared infrastructure focused.
- Config structs are authoritative. Application and service code must not call
  `os.Getenv` for normal configuration; `lib/core/config` alone owns source
  resolution. Precedence is always defaults, profile, environment, then
  explicit CLI.
- Profiles are immutable snapshots. Fully resolve and validate a candidate
  before atomic replacement. An invalid reload must never alter the active
  profile, and service-profile replacement must not mutate unrelated outer
  application/server configuration.
- Mark and redact secrets. Never put secret values in diagnostics, provenance,
  errors, or logs.
- A service receives its typed sub-configuration, not the entire application
  configuration.
- Every service must contribute a typed configuration struct. A service with no
  configurable fields contributes an explicit empty config contribution.
  Service code must not source configuration outside that contribution.
- A service observes configuration through its typed Profile/Slot handle, not
  the complete application configuration.
- Auth is a separate internal Service. Each relying Service may enable or
  disable enforcement independently. Browser auth must return to the exact
  original safe local URL.
- Never commit credentials, project-specific secrets, API keys, or development
  credential fallbacks. Deployed cookies are secure by default.
- Use `slog`; do not add an alternate logging library.
- Tests must exercise production behavior, use ephemeral listeners where
  practical, and report construction errors rather than hiding them.
- Browser-visible interaction semantics must not be considered qualified solely
  by jsdom/component tests. User-critical browser contracts require Playwright
  coverage, with Firefox and Chromium for tissues.
- When frontend source changes, regenerate and stage the corresponding embedded
  frontend output. For tissues, `npm run build` refreshes
  `services/tissues/frontend/generated`.
- `graphify-out/` is persistent local generated analysis state. It is
  intentionally Git-ignored. Graphify may create or refresh it, but agents must
  not delete it merely to obtain a clean Git working tree. Ignored generated
  state is not a Git cleanliness violation.
- Do not commit or push unless a task explicitly requests it.

## Before reporting completion

Run the following from every retained module root:

```sh
gofmt -l .
go vet ./...
go test ./...
go test -count=1 -shuffle=on ./...
go mod verify
```

From the workspace root, also run:

```sh
gofmt -l app services lib
git diff --check
```

For frontend changes, also run:

```sh
npm test
npm run typecheck
npm run build
```
