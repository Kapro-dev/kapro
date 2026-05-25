# GCP and GKE

Kapro's APIs are cloud-neutral. This page covers the optional GCP/GKE helper
path for teams that already run GKE, Google Artifact Registry, and Workload
Identity.

## Hub and Spoke Setup

Typical GKE shape:

```text
GKE hub cluster
  kapro-operator
  optional Flux Operator / GitOps backend

GKE spoke clusters
  Flux or kapro-cluster-controller, depending on delivery mode
```

Prerequisites:

- GKE clusters with Workload Identity enabled.
- Google Artifact Registry for Kapro and workload artifacts.
- `gcloud`, `kubectl`, and `helm`.

Install the operator on the hub:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

## Registering A Spoke

The helper script `examples/04-substrates/02-cloud/00-gcp/register-spoke.sh`:

1. fetches hub and spoke kubeconfigs with `gcloud container clusters get-credentials`;
2. runs the Kapro cluster join/bootstrap flow with the supplied labels;
3. cleans up temporary kubeconfigs on exit.

Same-project example:

```bash
./examples/04-substrates/02-cloud/00-gcp/register-spoke.sh \
  --name          spoke-de \
  --hub-project   my-project \
  --hub-cluster   hub \
  --hub-region    europe-west4 \
  --spoke-cluster spoke-de \
  --spoke-region  europe-west1 \
  --image         europe-west4-docker.pkg.dev/my-project/kapro/cluster-controller:v0.1.2 \
  --gcp-sa        kapro-cc@my-project.iam.gserviceaccount.com \
  --labels        tier=prod,country=de
```

Cross-project example:

```bash
./examples/04-substrates/02-cloud/00-gcp/register-spoke.sh \
  --name          spoke-de \
  --hub-project   my-hub-project \
  --hub-cluster   hub \
  --hub-region    europe-west4 \
  --spoke-project my-spoke-de-project \
  --spoke-cluster spoke-de \
  --spoke-region  europe-west1 \
  --hub-url       https://10.132.0.10 \
  --image         europe-west4-docker.pkg.dev/my-hub-project/kapro/cluster-controller:v0.1.2 \
  --gcp-sa        kapro-cc@my-spoke-de-project.iam.gserviceaccount.com \
  --labels        tier=prod,country=de
```

## Workload Identity

Create a CI service account that can register spokes:

```bash
gcloud iam service-accounts create kapro-spoke-register \
  --project="$HUB_PROJECT" \
  --display-name="Kapro spoke registration"

gcloud projects add-iam-policy-binding "$HUB_PROJECT" \
  --member="serviceAccount:kapro-spoke-register@$HUB_PROJECT.iam.gserviceaccount.com" \
  --role="roles/container.developer"

gcloud iam service-accounts add-iam-policy-binding \
  "kapro-spoke-register@$HUB_PROJECT.iam.gserviceaccount.com" \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/$WIF_POOL/attribute.repository/$GITHUB_REPO"
```

Create a spoke pod identity for pulling Kapro images from Artifact Registry:

```bash
gcloud iam service-accounts create kapro-cc \
  --project="$SPOKE_PROJECT" \
  --display-name="Kapro cluster-controller"

gcloud projects add-iam-policy-binding "$SPOKE_PROJECT" \
  --member="serviceAccount:kapro-cc@$SPOKE_PROJECT.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.reader"

gcloud iam service-accounts add-iam-policy-binding \
  "kapro-cc@$SPOKE_PROJECT.iam.gserviceaccount.com" \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:$SPOKE_PROJECT.svc.id.goog[kapro-system/kapro-cluster-controller]"
```

GCP Workload Identity only covers GCP access. Kapro's hub-to-spoke trust still
uses Kubernetes bootstrap, CSR approval, and client certificates.
