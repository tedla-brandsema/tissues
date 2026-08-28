# 🤧 tissues

GCP-native issue tracking for humans and agents.

This repository is a Go workspace organized into:

- `app/`: executable application composition roots.
- `lib/`: reusable infrastructure shared by applications.

The current executables are `app/gcp/tissues`, the bootstrap product service,
and `app/gcp/auth`, the separate Identity Platform authentication broker. The
published Git-backed v0 is parked at `archive/git-backed-v0`; the active
implementation is now GCP-native.

## Build

```sh
go build ./app/gcp/tissues
go build ./app/gcp/auth
```

## Configuration

Typed Go `Config` structs are the configuration schema. A named `Profile[T]`
is a resolved, validated, revisioned instance of that type. Values resolve in
one fixed order:

```text
defaults < named profile < environment < explicit CLI flags
```

Choose a profile with `--profile NAME` and its directory with
`--profiles DIRECTORY`; defaults are `default` and `./profiles`. The equivalent
bootstrap variables are `<PREFIX>_PROFILE` and `<PREFIX>_PROFILES`, with CLI
taking precedence. A missing profile file is allowed when defaults and
overrides are sufficient. File profiles are strict `.json`, `.yaml`, or `.yml`
documents; unknown fields and ambiguous same-name extensions are rejected.

File, environment, and flag names derive from the same field path. For example,
`Service.Auth.BrokerURL` becomes `service.auth.broker_url`,
`TISSUES_SERVICE_AUTH_BROKER_URL`, and `--service-auth-broker-url`. Run either
executable with `--help` to inspect its generated field flags. Cloud Run's
special bare `PORT` name is declared on `service.Config.Port` as a narrow source
override; the default port is 8080.

An application profile composes stable HTTP server settings with a mandatory
typed configuration contribution from every service, including an explicit
empty contribution when a service has no settings. The tissues service profile
is held separately so accepted live fields can be replaced without mutating
server configuration;
restart-tagged changes are explicitly reported as requiring reconstruction.

Tissues auth is optional. With `service.auth.enabled: false`, browser traffic
passes through. When enabled, tissues acts as a relying party for the separately
deployable central auth service and requires its broker URL, client credentials,
redirect URI, and session secret. Cookies are secure unless local development
explicitly opts into insecure cookies.

Fields tagged as secrets are redacted from provenance and diagnostics. Profile
files can technically contain them, but deployed profiles should inject secrets
from an external secret delivery mechanism. Secret Manager integration is not
part of this slice. Environment variables and CLI flags are intended as
overrides, not as a hand-maintained primary configuration schema.
