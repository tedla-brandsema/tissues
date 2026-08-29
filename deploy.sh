#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ID="tissues-dev"
REGION="europe-west4"
REPOSITORY="containers"
SERVICE_NAME="tissues"
RUNTIME_SERVICE_ACCOUNT_NAME="tissues-runtime"
RUNTIME_SERVICE_ACCOUNT="${RUNTIME_SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
AUTH_NAMESPACE="tissues-auth"
TISSUES_NAMESPACE="tissues"
TISSUES_ASSET_BUCKET="tissues-dev-tissues-assets-production"
AUTH_SIGNING_SECRET="tissues-auth-signing-secret"
CLIENT_SECRET="tissues-client-secret"
SESSION_SECRET="tissues-session-secret"
IDENTITY_API_KEY_SECRET="tissues-identity-api-key"
IMAGE_TAG="$(date -u +%Y%m%d-%H%M%S)"
IMAGE_URI="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPOSITORY}/${SERVICE_NAME}:${IMAGE_TAG}"
PACKAGE_DIR=""
FIRST_DEPLOYMENT=false
BOOTSTRAP_CREATED=false
PRODUCTION_REVISION_DEPLOYED=false
PRODUCTION_PROMOTED=false
PUBLIC_ACCESS_ENABLED=false
FINAL_DEPLOY_COMPLETE=false
PACKAGE_ONLY=false
IDENTITY_API_KEY="${TISSUES_IDENTITY_API_KEY-}"
unset TISSUES_IDENTITY_API_KEY

if [[ ${1-} == "--package-only" && $# -eq 1 ]]; then
  PACKAGE_ONLY=true
elif (( $# != 0 )); then
  echo "Usage: $0 [--package-only]" >&2
  exit 2
fi

cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "${PACKAGE_DIR}" && -d "${PACKAGE_DIR}" ]]; then
    rm -rf -- "${PACKAGE_DIR}"
  fi
  if [[ "${FIRST_DEPLOYMENT}" == true && "${BOOTSTRAP_CREATED}" == true && "${FINAL_DEPLOY_COMPLETE}" != true ]]; then
    if [[ "${PRODUCTION_PROMOTED}" == true && "${PUBLIC_ACCESS_ENABLED}" != true ]]; then
      echo "The production revision was promoted, but public invocation was not enabled; the service remains private." >&2
    elif [[ "${PRODUCTION_REVISION_DEPLOYED}" == true ]]; then
      echo "The production revision remains at 0% traffic; the private bootstrap remains serving." >&2
    else
      echo "The private bootstrap remains serving; the production revision was not deployed." >&2
    fi
  fi
  exit "${status}"
}
trap cleanup EXIT

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

build_package_context() {
  "${ROOT_DIR}/build.sh"

  PACKAGE_DIR="$(mktemp -d /tmp/tissues-deploy.XXXXXX)"
  mkdir -p "${PACKAGE_DIR}/build"
  cp "${ROOT_DIR}/Dockerfile" "${PACKAGE_DIR}/Dockerfile"
  cp "${ROOT_DIR}/build/server" "${PACKAGE_DIR}/build/server"

  local -a files=()
  while IFS= read -r file; do
    files+=("${file#./}")
  done < <(cd "${PACKAGE_DIR}" && find . -type f -print | LC_ALL=C sort)

  if (( ${#files[@]} != 2 )) ||
    [[ "${files[0]-}" != "Dockerfile" ]] ||
    [[ "${files[1]-}" != "build/server" ]]; then
    echo "Refusing deployment: Cloud Build context must contain only Dockerfile and build/server." >&2
    printf 'Found: %s\n' "${files[@]-<none>}" >&2
    exit 1
  fi
  if find "${PACKAGE_DIR}" -mindepth 1 ! -type d ! -type f -print -quit | grep -q .; then
    echo "Refusing deployment: Cloud Build context contains a non-regular entry." >&2
    exit 1
  fi
}

secret_exists() {
  gcloud secrets describe "$1" --project="${PROJECT_ID}" >/dev/null 2>&1
}

ensure_generated_secret() {
  local name=$1
  if secret_exists "${name}"; then
    echo "Reusing Secret Manager secret: ${name}"
    return
  fi
  echo "Creating Secret Manager secret: ${name}"
  openssl rand -base64 48 | gcloud secrets create "${name}" \
    --project="${PROJECT_ID}" \
    --replication-policy=automatic \
    --data-file=- \
    --quiet >/dev/null
}

ensure_identity_api_key_secret() {
  if secret_exists "${IDENTITY_API_KEY_SECRET}"; then
    echo "Reusing Secret Manager secret: ${IDENTITY_API_KEY_SECRET}"
    IDENTITY_API_KEY=""
    return
  fi
  if [[ -z "${IDENTITY_API_KEY}" ]]; then
    echo "${IDENTITY_API_KEY_SECRET} does not exist; set TISSUES_IDENTITY_API_KEY for its initial value." >&2
    exit 1
  fi
  echo "Creating Secret Manager secret: ${IDENTITY_API_KEY_SECRET}"
  printf '%s' "${IDENTITY_API_KEY}" | gcloud secrets create "${IDENTITY_API_KEY_SECRET}" \
    --project="${PROJECT_ID}" \
    --replication-policy=automatic \
    --data-file=- \
    --quiet >/dev/null
  IDENTITY_API_KEY=""
}

grant_secret_access() {
  local name=$1
  gcloud secrets add-iam-policy-binding "${name}" \
    --project="${PROJECT_ID}" \
    --member="serviceAccount:${RUNTIME_SERVICE_ACCOUNT}" \
    --role=roles/secretmanager.secretAccessor \
    --condition=None \
    --quiet >/dev/null
}

ensure_asset_bucket() {
  local bucket="gs://${TISSUES_ASSET_BUCKET}"
  local properties
  if ! properties="$(gcloud storage buckets describe "${bucket}" \
    --project="${PROJECT_ID}" \
    --format='value(location,default_storage_class,uniform_bucket_level_access,public_access_prevention,versioning_enabled)' 2>/dev/null)"; then
    echo "Creating production asset bucket: ${bucket}"
    gcloud storage buckets create "${bucket}" \
      --project="${PROJECT_ID}" \
      --location="${REGION}" \
      --default-storage-class=STANDARD \
      --uniform-bucket-level-access \
      --public-access-prevention \
      --quiet
    properties="$(gcloud storage buckets describe "${bucket}" \
      --project="${PROJECT_ID}" \
      --format='value(location,default_storage_class,uniform_bucket_level_access,public_access_prevention,versioning_enabled)')"
  else
    echo "Reusing production asset bucket: ${bucket}"
  fi
  if [[ "${properties}" != $'EUROPE-WEST4\tSTANDARD\tTrue\tenforced\t' ]]; then
    echo "Asset bucket ${bucket} has incompatible location, storage class, access, or versioning settings: ${properties}" >&2
    exit 1
  fi
  gcloud storage buckets add-iam-policy-binding "${bucket}" \
    --member="serviceAccount:${RUNTIME_SERVICE_ACCOUNT}" \
    --role=roles/storage.objectUser \
    --condition=None \
    --quiet >/dev/null
}

ensure_prerequisites() {
  echo "Ensuring required Google Cloud APIs are enabled..."
  gcloud services enable \
    run.googleapis.com \
    artifactregistry.googleapis.com \
    cloudbuild.googleapis.com \
    secretmanager.googleapis.com \
    datastore.googleapis.com \
    storage.googleapis.com \
    --project="${PROJECT_ID}" \
    --quiet

  local format
  if format="$(gcloud artifacts repositories describe "${REPOSITORY}" \
    --project="${PROJECT_ID}" \
    --location="${REGION}" \
    --format='value(format)' 2>/dev/null)"; then
    if [[ "${format}" != "DOCKER" ]]; then
      echo "Artifact Registry repository ${REPOSITORY} in ${REGION} is not Docker format." >&2
      exit 1
    fi
    echo "Reusing Docker repository: ${REPOSITORY}"
  else
    echo "Creating Docker repository: ${REPOSITORY}"
    gcloud artifacts repositories create "${REPOSITORY}" \
      --project="${PROJECT_ID}" \
      --location="${REGION}" \
      --repository-format=docker \
      --quiet
  fi

  if gcloud iam service-accounts describe "${RUNTIME_SERVICE_ACCOUNT}" \
    --project="${PROJECT_ID}" >/dev/null 2>&1; then
    echo "Reusing runtime service account: ${RUNTIME_SERVICE_ACCOUNT}"
  else
    echo "Creating runtime service account: ${RUNTIME_SERVICE_ACCOUNT}"
    gcloud iam service-accounts create "${RUNTIME_SERVICE_ACCOUNT_NAME}" \
      --project="${PROJECT_ID}" \
      --display-name="tissues Cloud Run runtime" \
      --quiet
  fi

  gcloud projects add-iam-policy-binding "${PROJECT_ID}" \
    --member="serviceAccount:${RUNTIME_SERVICE_ACCOUNT}" \
    --role=roles/datastore.user \
    --condition=None \
    --quiet >/dev/null

  ensure_asset_bucket

  ensure_generated_secret "${AUTH_SIGNING_SECRET}"
  ensure_generated_secret "${CLIENT_SECRET}"
  ensure_generated_secret "${SESSION_SECRET}"
  ensure_identity_api_key_secret
  grant_secret_access "${AUTH_SIGNING_SECRET}"
  grant_secret_access "${CLIENT_SECRET}"
  grant_secret_access "${SESSION_SECRET}"
  grant_secret_access "${IDENTITY_API_KEY_SECRET}"
}

service_origin() {
  gcloud run services describe "${SERVICE_NAME}" \
    --project="${PROJECT_ID}" \
    --region="${REGION}" \
    --platform=managed \
    --format='value(status.url)' 2>/dev/null
}

for command in go find grep mktemp cp sort; do
  require_command "${command}"
done

if [[ "${PACKAGE_ONLY}" == true ]]; then
  build_package_context
  echo "Temporary package directory: ${PACKAGE_DIR}"
  echo "Verified Cloud Build context (regular files):"
  echo "Dockerfile"
  echo "build/server"
  echo "Package-only qualification complete; Cloud Build was not invoked."
  exit 0
fi

require_command gcloud
require_command openssl

echo "----------------------------------------"
echo "tissues Cloud Run deployment"
echo "Project                      : ${PROJECT_ID}"
echo "Region                       : ${REGION}"
echo "Cloud Run service            : ${SERVICE_NAME}"
echo "Artifact image               : ${IMAGE_URI}"
echo "Production Datastore namespace: ${TISSUES_NAMESPACE}"
echo "Auth Datastore namespace     : ${AUTH_NAMESPACE}"
echo "Production asset bucket      : ${TISSUES_ASSET_BUCKET}"
echo "----------------------------------------"

ensure_prerequisites
build_package_context

echo "Temporary package directory: ${PACKAGE_DIR}"
echo "Verified Cloud Build context (regular files):"
echo "Dockerfile"
echo "build/server"

echo "Submitting the two-file package context to Cloud Build..."
gcloud builds submit "${PACKAGE_DIR}" \
  --project="${PROJECT_ID}" \
  --tag="${IMAGE_URI}" \
  --quiet

PUBLIC_ORIGIN="$(service_origin || true)"
if [[ -z "${PUBLIC_ORIGIN}" ]]; then
  FIRST_DEPLOYMENT=true
  echo "Creating a private bootstrap service to establish the Cloud Run URL..."
  if ! gcloud run deploy "${SERVICE_NAME}" \
    --project="${PROJECT_ID}" \
    --region="${REGION}" \
    --platform=managed \
    --image="${IMAGE_URI}" \
    --service-account="${RUNTIME_SERVICE_ACCOUNT}" \
    --no-allow-unauthenticated \
    --set-env-vars="TISSUES_AUTH_ENABLED=false,TISSUES_TISSUES_ENABLED=false" \
    --quiet; then
    echo "Private bootstrap service creation failed; no production revision or public IAM change was attempted." >&2
    exit 1
  fi
  BOOTSTRAP_CREATED=true
  PUBLIC_ORIGIN="$(service_origin)"
fi

if [[ ! "${PUBLIC_ORIGIN}" =~ ^https://[^/]+$ ]]; then
  echo "Cloud Run returned an invalid canonical service origin." >&2
  exit 1
fi

NON_SECRET_ENV="TISSUES_SERVER_READ_TIMEOUT=60s,TISSUES_SERVER_WRITE_TIMEOUT=60s,TISSUES_AUTH_ENABLED=true,TISSUES_AUTH_CLIENT_ID=tissues,TISSUES_AUTH_CLIENT_REDIRECT_URI=${PUBLIC_ORIGIN}/tissues/auth/callback,TISSUES_AUTH_PROJECT_ID=${PROJECT_ID},TISSUES_AUTH_DATASTORE_NS=${AUTH_NAMESPACE},TISSUES_AUTH_INSECURE_COOKIE=false,TISSUES_TISSUES_ENABLED=true,TISSUES_TISSUES_STORAGE_PROJECT_ID=${PROJECT_ID},TISSUES_TISSUES_STORAGE_NAMESPACE=${TISSUES_NAMESPACE},TISSUES_TISSUES_ASSETS_BUCKET=${TISSUES_ASSET_BUCKET},TISSUES_TISSUES_AUTH_ENABLED=true,TISSUES_TISSUES_AUTH_BROKER_URL=${PUBLIC_ORIGIN},TISSUES_TISSUES_AUTH_CLIENT_ID=tissues,TISSUES_TISSUES_AUTH_REDIRECT_URI=${PUBLIC_ORIGIN}/tissues/auth/callback,TISSUES_TISSUES_AUTH_INSECURE_COOKIE=false"
SECRET_ENV="TISSUES_AUTH_SIGNING_SECRET=${AUTH_SIGNING_SECRET}:latest,TISSUES_AUTH_CLIENT_SECRET=${CLIENT_SECRET}:latest,TISSUES_AUTH_IDENTITY_API_KEY=${IDENTITY_API_KEY_SECRET}:latest,TISSUES_TISSUES_AUTH_CLIENT_SECRET=${CLIENT_SECRET}:latest,TISSUES_TISSUES_AUTH_SESSION_SECRET=${SESSION_SECRET}:latest"
PRODUCTION_ACCESS_FLAG="--allow-unauthenticated"
if [[ "${FIRST_DEPLOYMENT}" == true ]]; then
  PRODUCTION_ACCESS_FLAG="--no-allow-unauthenticated"
fi

echo "Deploying the fully configured production revision..."
if ! gcloud run deploy "${SERVICE_NAME}" \
  --project="${PROJECT_ID}" \
  --region="${REGION}" \
  --platform=managed \
  --image="${IMAGE_URI}" \
  --service-account="${RUNTIME_SERVICE_ACCOUNT}" \
  --no-traffic \
  "${PRODUCTION_ACCESS_FLAG}" \
  --set-env-vars="${NON_SECRET_ENV}" \
  --update-secrets="${SECRET_ENV}" \
  --quiet; then
  if [[ "${FIRST_DEPLOYMENT}" == true ]]; then
    echo "The private bootstrap remains serving; the fully configured production revision failed." >&2
  else
    echo "The fully configured production revision failed; existing production traffic was not changed." >&2
  fi
  exit 1
fi
PRODUCTION_REVISION_DEPLOYED=true

if ! gcloud run services update-traffic "${SERVICE_NAME}" \
  --project="${PROJECT_ID}" \
  --region="${REGION}" \
  --platform=managed \
  --to-latest \
  --quiet; then
  if [[ "${FIRST_DEPLOYMENT}" == true ]]; then
    echo "The production revision is ready at 0% traffic, but promotion failed; the private bootstrap remains serving." >&2
  else
    echo "The production revision is ready at 0% traffic, but promotion failed; existing production traffic was not changed." >&2
  fi
  exit 1
fi
PRODUCTION_PROMOTED=true

if [[ "${FIRST_DEPLOYMENT}" == true ]]; then
  echo "Enabling public invocation after production traffic promotion..."
  if ! gcloud run services add-iam-policy-binding "${SERVICE_NAME}" \
    --project="${PROJECT_ID}" \
    --region="${REGION}" \
    --member="allUsers" \
    --role="roles/run.invoker" \
    --quiet; then
    echo "The production revision was promoted, but public invocation enablement failed; the service remains private." >&2
    exit 1
  fi
  PUBLIC_ACCESS_ENABLED=true
fi
FINAL_DEPLOY_COMPLETE=true

echo "----------------------------------------"
echo "Deployment complete."
echo "Service URL    : ${PUBLIC_ORIGIN}"
echo "Deployed image : ${IMAGE_URI}"
echo "----------------------------------------"
