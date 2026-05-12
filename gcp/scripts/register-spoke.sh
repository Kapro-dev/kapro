#!/usr/bin/env bash
# register-spoke.sh — register a GKE spoke cluster with Kapro hub using Workload Identity.
#
# Handles both same-project and cross-project scenarios.
# Called by CI pipeline (Cloud Build / GitHub Actions) after WIF authentication.
#
# Usage:
#   register-spoke.sh \
#     --name           spoke-de \
#     --hub-project    my-hub-project \
#     --hub-cluster    hub-cluster \
#     --hub-region     europe-west4 \
#     --spoke-project  my-spoke-project \          # omit for same-project
#     --spoke-cluster  spoke-de \
#     --spoke-region   europe-west1 \
#     --hub-url        https://10.132.0.10 \        # private endpoint; omit for public
#     --image          europe-west4-docker.pkg.dev/my-hub-project/kapro/cluster-controller:v1.0 \
#     --gcp-sa         kapro-cc@my-spoke-project.iam.gserviceaccount.com \  # omit if no WI needed
#     --labels         tier=prod,country=de
#
# Prerequisites:
#   - gcloud authenticated (WIF in CI, gcloud auth login locally)
#   - kapro CLI on PATH
#   - hub operator running with KAPRO_HUB_API_URL set
set -euo pipefail

# ── defaults ──────────────────────────────────────────────────────────────────
HUB_PROJECT=""
HUB_CLUSTER=""
HUB_REGION=""
SPOKE_PROJECT=""   # defaults to HUB_PROJECT if omitted (same-project)
SPOKE_CLUSTER=""
SPOKE_REGION=""
CLUSTER_NAME=""
HUB_URL=""
IMAGE=""  # cluster-controller image, set when spoke-side agent is available
GCP_SA=""
LABELS=""

# ── parse args ────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case $1 in
    --name)           CLUSTER_NAME="$2";  shift 2 ;;
    --hub-project)    HUB_PROJECT="$2";   shift 2 ;;
    --hub-cluster)    HUB_CLUSTER="$2";   shift 2 ;;
    --hub-region)     HUB_REGION="$2";    shift 2 ;;
    --spoke-project)  SPOKE_PROJECT="$2"; shift 2 ;;
    --spoke-cluster)  SPOKE_CLUSTER="$2"; shift 2 ;;
    --spoke-region)   SPOKE_REGION="$2";  shift 2 ;;
    --hub-url)        HUB_URL="$2";       shift 2 ;;
    --image)          IMAGE="$2";         shift 2 ;;
    --gcp-sa)         GCP_SA="$2";        shift 2 ;;
    --labels)         LABELS="$2";        shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

# ── validate ──────────────────────────────────────────────────────────────────
[[ -z "$CLUSTER_NAME" ]]  && { echo "ERROR: --name is required" >&2; exit 1; }
[[ -z "$HUB_PROJECT" ]]   && { echo "ERROR: --hub-project is required" >&2; exit 1; }
[[ -z "$HUB_CLUSTER" ]]   && { echo "ERROR: --hub-cluster is required" >&2; exit 1; }
[[ -z "$HUB_REGION" ]]    && { echo "ERROR: --hub-region is required" >&2; exit 1; }
[[ -z "$SPOKE_CLUSTER" ]] && { echo "ERROR: --spoke-cluster is required" >&2; exit 1; }
[[ -z "$SPOKE_REGION" ]]  && { echo "ERROR: --spoke-region is required" >&2; exit 1; }

# Default spoke project = hub project (same-project scenario)
SPOKE_PROJECT="${SPOKE_PROJECT:-$HUB_PROJECT}"

# ── kubeconfigs ───────────────────────────────────────────────────────────────
HUB_KUBECONFIG=$(mktemp)
SPOKE_KUBECONFIG=$(mktemp)
trap 'rm -f "$HUB_KUBECONFIG" "$SPOKE_KUBECONFIG"' EXIT

echo "🔑 Getting hub credentials (project=$HUB_PROJECT, cluster=$HUB_CLUSTER, region=$HUB_REGION)..."
KUBECONFIG="$HUB_KUBECONFIG" gcloud container clusters get-credentials "$HUB_CLUSTER" \
  --region "$HUB_REGION" \
  --project "$HUB_PROJECT"

echo "🔑 Getting spoke credentials (project=$SPOKE_PROJECT, cluster=$SPOKE_CLUSTER, region=$SPOKE_REGION)..."
KUBECONFIG="$SPOKE_KUBECONFIG" gcloud container clusters get-credentials "$SPOKE_CLUSTER" \
  --region "$SPOKE_REGION" \
  --project "$SPOKE_PROJECT"

# ── build kapro cluster join args ─────────────────────────────────────────────
KAPRO_ARGS=(
  cluster join
  --name           "$CLUSTER_NAME"
  --hub-kubeconfig "$HUB_KUBECONFIG"
  --spoke-kubeconfig "$SPOKE_KUBECONFIG"
  --wait
)

[[ -n "$IMAGE" ]]    && KAPRO_ARGS+=(--image "$IMAGE")
[[ -n "$HUB_URL" ]]  && KAPRO_ARGS+=(--hub-url "$HUB_URL")
[[ -n "$LABELS" ]]   && KAPRO_ARGS+=(--labels "$LABELS")
[[ -n "$GCP_SA" ]]   && KAPRO_ARGS+=(--gcp-service-account "$GCP_SA")

# ── run ───────────────────────────────────────────────────────────────────────
echo ""
echo "🚀 Running: kapro ${KAPRO_ARGS[*]}"
echo ""
kapro "${KAPRO_ARGS[@]}"
