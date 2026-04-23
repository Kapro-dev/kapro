# GCP Integration

Helpers for registering Kapro spoke clusters on GKE using Workload Identity.
Zero Terraform required — infra team handles the IAM setup.

## What `scripts/register-spoke.sh` does

1. Calls `gcloud container clusters get-credentials` for both hub and spoke
2. Calls `kapro cluster join` with the right flags
3. Cleans up temp kubeconfigs on exit

## Prerequisites

The infra team must create two GSAs per spoke and bind them:

### 1. Pipeline GSA (for CI — runs `kapro cluster join`)

```bash
# Create pipeline SA in hub project
gcloud iam service-accounts create kapro-pipeline \
  --project=$HUB_PROJECT \
  --display-name="Kapro pipeline — registers spoke clusters"

# Hub project: read/write MemberCluster, read bootstrap Secrets
gcloud projects add-iam-policy-binding $HUB_PROJECT \
  --member="serviceAccount:kapro-pipeline@$HUB_PROJECT.iam.gserviceaccount.com" \
  --role="roles/container.developer"

# Cross-project: apply spoke manifests in each spoke project
gcloud projects add-iam-policy-binding $SPOKE_PROJECT \
  --member="serviceAccount:kapro-pipeline@$HUB_PROJECT.iam.gserviceaccount.com" \
  --role="roles/container.developer"

# WIF binding (GitHub Actions example)
gcloud iam service-accounts add-iam-policy-binding \
  kapro-pipeline@$HUB_PROJECT.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="principalSet://iam.googleapis.com/$WIF_POOL/attribute.repository/$GITHUB_REPO"
```

### 2. Pod GSA (for cluster-controller pod — pulls image from Artifact Registry)

```bash
# Create pod SA in spoke project
gcloud iam service-accounts create kapro-cc \
  --project=$SPOKE_PROJECT \
  --display-name="Kapro cluster-controller pod identity"

# Artifact Registry read access (no imagePullSecrets needed)
gcloud projects add-iam-policy-binding $SPOKE_PROJECT \
  --member="serviceAccount:kapro-cc@$SPOKE_PROJECT.iam.gserviceaccount.com" \
  --role="roles/artifactregistry.reader"

# Bind KSA → GSA (GKE Workload Identity)
gcloud iam service-accounts add-iam-policy-binding \
  kapro-cc@$SPOKE_PROJECT.iam.gserviceaccount.com \
  --role="roles/iam.workloadIdentityUser" \
  --member="serviceAccount:$SPOKE_PROJECT.svc.id.goog[kapro-system/kapro-cluster-controller]"
```

## Same-project usage (hub + spoke in same project)

```bash
./gcp/scripts/register-spoke.sh \
  --name          spoke-de \
  --hub-project   my-project \
  --hub-cluster   hub \
  --hub-region    europe-west4 \
  --spoke-cluster spoke-de \
  --spoke-region  europe-west1 \
  --image         europe-west4-docker.pkg.dev/my-project/kapro/cluster-controller:v1.0 \
  --gcp-sa        kapro-cc@my-project.iam.gserviceaccount.com \
  --labels        tier=prod,country=de
```

## Cross-project usage (hub project A, spoke project B)

```bash
./gcp/scripts/register-spoke.sh \
  --name          spoke-de \
  --hub-project   my-hub-project \
  --hub-cluster   hub \
  --hub-region    europe-west4 \
  --spoke-project my-spoke-de-project \
  --spoke-cluster spoke-de \
  --spoke-region  europe-west1 \
  --hub-url       https://10.132.0.10 \   # hub private endpoint (VPC peering)
  --image         europe-west4-docker.pkg.dev/my-hub-project/kapro/cluster-controller:v1.0 \
  --gcp-sa        kapro-cc@my-spoke-de-project.iam.gserviceaccount.com \
  --labels        tier=prod,country=de
```

## 33-country pipeline loop

```bash
for COUNTRY in de fi fr pl nl se no dk es pt it; do
  ./gcp/scripts/register-spoke.sh \
    --name          spoke-$COUNTRY \
    --hub-project   my-hub-project \
    --hub-cluster   hub \
    --hub-region    europe-west4 \
    --spoke-project my-spoke-$COUNTRY-project \
    --spoke-cluster spoke-$COUNTRY \
    --spoke-region  europe-west1 \
    --hub-url       https://10.132.0.10 \
    --image         $IMAGE \
    --gcp-sa        kapro-cc@my-spoke-$COUNTRY-project.iam.gserviceaccount.com \
    --labels        tier=prod,country=$COUNTRY
done
```

## What stays Kubernetes-native (no GCP involved)

The cluster-controller pod connects to the hub Kubernetes API using:
1. Bootstrap: Kubernetes SA token → CSR → approval (pure K8s RBAC)
2. Ongoing: mTLS client certificate issued by hub

GCP Workload Identity only covers:
- Pipeline authenticating to GCP to get kubeconfigs
- Pod pulling images from Artifact Registry (no imagePullSecrets)
