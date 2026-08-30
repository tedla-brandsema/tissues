#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <identity-platform-api-key>" >&2
    exit 2
fi

IDENTITY_API_KEY="$1"
shift

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" || ! -f "$REPO_ROOT/app/gcp/server/main.go" ]]; then
    echo "Error: run this script from inside the tissues repository." >&2
    exit 1
fi

PROJECT_ID="tissues-dev"
HOST="127.0.0.1"
PORT="18080"
ORIGIN="http://${HOST}:${PORT}"

PROFILE_NAME="dogfood"
DOGFOOD_DIR="$(mktemp -d /tmp/tissues-dogfood.XXXXXX)"
PROFILE_FILE="${DOGFOOD_DIR}/${PROFILE_NAME}.yaml"

cleanup() {
    rm -rf "$DOGFOOD_DIR"
}
trap cleanup EXIT

random_secret() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    else
        python3 -c 'import secrets; print(secrets.token_hex(32))'
    fi
}

# These exist only in this process environment.
AUTH_SIGNING_SECRET="$(random_secret)"
CLIENT_SECRET="$(random_secret)"
TISSUES_SESSION_SECRET="$(random_secret)"

cat >"$PROFILE_FILE" <<EOF
server:
  host: ${HOST}
  port: ${PORT}
  read_timeout: 60s
  write_timeout: 60s

auth:
  enabled: true
  issuer_url: ${ORIGIN}
  mcp_resource_url: ${ORIGIN}/mcp
  client_id: tissues
  client_redirect_uri: ${ORIGIN}/tissues/auth/callback
  project_id: ${PROJECT_ID}
  datastore_ns: tissues-auth-dogfood
  insecure_cookie: true

tissues:
  enabled: true
  bootstrap_tenant_id: 7womw3jzkek74oggxj6f42xak4
  assets:
    bucket: tissues-dev-tissues-assets-dogfood
  storage:
    project_id: ${PROJECT_ID}
    namespace: tissues-dogfood-projects
  auth:
    enabled: true
    broker_url: ${ORIGIN}
    client_id: tissues
    redirect_uri: ${ORIGIN}/tissues/auth/callback
    insecure_cookie: true
EOF

chmod 600 "$PROFILE_FILE"

# Secrets override/populate the typed Config through the normal config loader.
export TISSUES_AUTH_IDENTITY_API_KEY="$IDENTITY_API_KEY"
export TISSUES_AUTH_SIGNING_SECRET="$AUTH_SIGNING_SECRET"
export TISSUES_AUTH_CLIENT_SECRET="$CLIENT_SECRET"

# Note the double TISSUES: application prefix + Tissues.Auth field path.
export TISSUES_TISSUES_AUTH_CLIENT_SECRET="$CLIENT_SECRET"
export TISSUES_TISSUES_AUTH_SESSION_SECRET="$TISSUES_SESSION_SECRET"

# Server.Port deliberately maps to Cloud Run's conventional bare PORT variable.
export PORT="$PORT"

echo "Starting tissues local dogfood..."
echo
echo "  Project:   ${PROJECT_ID}"
echo "  URL:       ${ORIGIN}/?view=open"
echo "  Datastore: tissues-dogfood-projects"
echo "  Assets:    gs://tissues-dev-tissues-assets-dogfood"
echo "  Tenant:    7womw3jzkek74oggxj6f42xak4"
echo "  Profile:   ${PROFILE_FILE}"
echo
echo "Open this in your browser:"
echo
echo "  ${ORIGIN}/?view=open"
echo
echo "Press Ctrl-C to stop."
echo

cd "$REPO_ROOT"

go run ./app/gcp/server \
    --profile="$PROFILE_NAME" \
    --profiles="$DOGFOOD_DIR"
