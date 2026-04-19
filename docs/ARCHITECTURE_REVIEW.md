# Kapro Architecture Review
**Date:** 2026-04-19  
**Scope:** MVP codebase clean architecture assessment + multi-cloud provider onboarding design  
**Verdict:** STRONG FOUNDATION — 3 gaps to close before provider work begins  

---

## 1. Interface Layer Assessment (KGI / KAI / KCI)

Kapro has three public interfaces. Each is evaluated below.

---

### 1.1 KGI — Kapro Gate Interface (`pkg/gate/gate.go`)

**Score: 9/10 — Excellent**

```
Gate.Evaluate(ctx, Request) → (Result, error)
```

| Dimension | Assessment |
|-----------|------------|
| Single responsibility | ✅ One method, one job: "is it safe to advance?" |
| Stateless | ✅ All gate state in `Sync.Status.Gates[]` — survives restarts |
| Concurrent-safe contract | ✅ Documented in godoc |
| Compile-time checks | ✅ `var _ gate.Gate = (*SoakGate)(nil)` in controllers.go |
| CRI analogy | ✅ Documented clearly; helps onboarding |
| Request richness | ✅ Carries Sync, Policy, Template, Args — gates need nothing else |
| Result richness | ✅ Phase, VendorRef, ConditionResult slice — covers k8s-native + external |
| Builder pattern | ✅ `BuildGateSet(client.Client)` — fully wired, no half-constructed structs |

**Minor issue:** `Result.Passed` (bool) AND `Result.Phase` (enum) are both present. A gate could set `Passed: true` but `Phase: Failed`. The controller normalises from `Phase` when set, but the dual representation adds cognitive overhead for implementors. Recommend deprecating `Passed` in v0.2 and driving exclusively from `Phase`.

---

### 1.2 KAI — Kapro Actuator Interface (`pkg/actuator/actuator.go`)

**Score: 9/10 — Excellent**

```
Apply(ctx, ApplyRequest) → error
IsConverged(ctx, env, version) → (bool, error)
Rollback(ctx, env, previousVersion) → error
```

| Dimension | Assessment |
|-----------|------------|
| Symmetry | ✅ Apply/IsConverged/Rollback forms a complete delivery contract |
| Idempotence documented | ✅ "Apply twice with same version is safe" |
| ApplyRequest struct | ✅ AppKey + PreviousVersion future-proofed |
| FluxActuator pattern | ✅ Writes ManagedCluster.spec.desiredVersion — no direct cluster access |
| Registry | ✅ `actuator.Registry` with `Register(type, impl)` / `Resolve(type)` |
| Error messages | ✅ Include actuator name, env name, op — grep-friendly |

**Gap:** `IsConverged` signature is `(ctx, env, version)` but `Apply` uses `ApplyRequest{..., AppKey}`. When `IsConverged` resolves the AppKey, it reads `reg.Spec.DesiredAppKey` from the ManagedCluster — implicit coupling. Recommend adding `AppKey string` to `IsConverged` signature in v0.2 for explicitness.

---

### 1.3 KCI — Kapro Cluster Interface (`pkg/provider/provider.go`)

**Score: 7/10 — Good design, incomplete wiring**

```
Connector.Connect(ctx, env) → (*rest.Config, error)
Connector.IsReachable(ctx, env) → (bool, error)

RegistrationReader.GetRegistration(ctx, env) → (*ManagedCluster, error)
```

The split into `Connector` (direct network) and `RegistrationReader` (CRD-based) is architecturally clean. The Rancher plugin (`plugins/rancher/`) is a complete reference implementation showing the pattern.

**Gaps:**

1. **No `provider.Registry`** — KAI has `actuator.Registry` for runtime type → impl resolution. KCI has nothing equivalent. The controller hardcodes `crdprovider.CRDProvider{}` in `startSyncController`. When GKE/EKS/AKS connectors land, there's no dispatch mechanism. **This must be built before any cloud connector work begins.**

2. **`ProviderSpec` is a stub** — `types.go` has `ProviderSpec{Type string}` with no cloud-specific fields. The Rancher plugin references `env.Spec.Provider.Rancher` which was stripped. For multi-cloud, each cloud needs a provider config block in `EnvironmentSpec`. See §3 for the full design.

3. **`ClusterCapabilities` has no cloud metadata** — `region` is there, but no `cloud`, `zone`, `accountID`, `clusterName`. These are needed for multi-cloud routing, topology-aware promotion waves, and audit.

---

## 2. Code Clarity and Onboarding Assessment

### Controller Manager (controllermanager.go / controllers.go)

**Score: 10/10 — Exemplary**

The CCM-style pattern (`Register("name", initFn)`, `KAPRO_CONTROLLERS=*,-releasereport`) is the right abstraction. A new engineer reading `controllers.go` sees immediately:
- What controllers exist (`init()` block)
- What each does (godoc comment per `start*` function)
- What dependencies each requires (explicit struct construction)

No magic. No reflection. No hidden wiring.

### Sync FSM (sync_controller.go)

**Score: 8/10 — Clear, one naming issue**

The `switch sync.Status.Phase` pattern is readable. Each `handle*` function has a single responsibility. The finalizer pattern is correct.

**One gap:** `SyncReconciler` has a `CELGate gate.Gate` field alongside `SoakGate`, `MetricsGate`, `ApprovalGate`, `VerificationGate` — but CELGate is for `gateForTemplate` (the dynamic path), while the others are for the fixed FSM phases. This asymmetry is confusing: why is CEL special? Recommendation: move CEL gate construction into `controllers.go` alongside the others and wire it the same way.

### CRDProvider (internal/provider/crd/crd_provider.go)

**Score: 8/10 — Correct, one hardcoded key**

Clean implementation of `RegistrationReader`. Heartbeat staleness check (2 minutes) is a constant — good.

**Bug:** `CurrentVersion` hardcodes `reg.Status.CurrentVersions["ocs"]` — this should use `reg.Spec.DesiredAppKey` or fall back to `"default"`, same logic as `FluxActuator.resolveAppKey()`. Fix before any multi-app deployment.

### Conformance Suites

**Score: 9/10 — Excellent discipline**

Both `conformance/gate/suite.go` and `conformance/provider/suite.go` exist and define the contract tests. The `RunSuite(t, impl)` pattern is exactly right. Any new provider or gate implementation can be tested with one line.

---

## 3. Multi-Cloud Provider Onboarding Design

### 3.1 The Two Onboarding Paths

Kapro supports two cluster onboarding models, and they are not mutually exclusive:

```
Path A: Outbound (CRD Provider) — recommended default for all clouds
────────────────────────────────────────────────────────────────────
  Spoke cluster                    Hub cluster
  ┌─────────────────────┐          ┌──────────────────────────────┐
  │ kapro-cluster-       │  HTTPS  │ ManagedCluster CRD           │
  │ controller          │ ──────► │ (written by controller)      │
  │                     │         │                              │
  │ polls               │         │ kapro-operator reads it      │
  │ spec.desiredVersion │ ◄────── │ writes spec.desiredVersion   │
  └─────────────────────┘         └──────────────────────────────┘

  Works on ALL clouds with zero cloud-specific hub config.
  One-time bootstrap: BootstrapToken HMAC handshake.

Path B: Direct Connect (KCI Connector) — cloud-native fast path
────────────────────────────────────────────────────────────────
  Hub cluster                      Spoke cluster
  ┌─────────────────────┐  HTTPS  ┌──────────────────────────────┐
  │ kapro-operator      │ ──────► │ Kubernetes API server        │
  │ Connector.Connect() │         │ (GKE/EKS/AKS/DO/StackIT)   │
  │ → *rest.Config      │         └──────────────────────────────┘
  └─────────────────────┘

  Requires hub→spoke network path.
  Uses cloud IAM: Workload Identity / IRSA / Managed Identity.
  No cluster-controller agent needed on spoke.
```

**Recommendation:** Ship Path A (CRD provider) for MVP. Build Path B per-cloud in v0.3+. Every cloud below has both paths documented.

---

### 3.2 What Needs to Be Built

#### A. `pkg/provider/registry.go` (NEW — mirrors actuator.Registry)

```go
// Registry maps provider type names to Connector implementations.
type Registry struct {
    mu    sync.RWMutex
    impls map[string]Connector
}

func (r *Registry) Register(typeName string, c Connector) error
func (r *Registry) Resolve(typeName string) (Connector, error)
```

Wire into `ControllerContext.ProviderRegistry *provider.Registry`.

#### B. `api/v1alpha1/types.go` — Extend `ProviderSpec`

```go
type ProviderSpec struct {
    // Type selects the connectivity backend.
    // "" or "crd" → CRD provider (default, works everywhere)
    // "gke"        → GKE Workload Identity + Connect Gateway
    // "eks"        → EKS IRSA + API endpoint
    // "aks"        → AKS Managed Identity
    // "digitalocean" → DigitalOcean API token + cluster ID
    // "stackit"    → StackIT Kubernetes Engine
    // +kubebuilder:validation:Enum="";crd;gke;eks;aks;digitalocean;stackit
    Type string `json:"type,omitempty"`

    GKE          *GKEProviderSpec          `json:"gke,omitempty"`
    EKS          *EKSProviderSpec          `json:"eks,omitempty"`
    AKS          *AKSProviderSpec          `json:"aks,omitempty"`
    DigitalOcean *DigitalOceanProviderSpec `json:"digitalOcean,omitempty"`
    StackIT      *StackITProviderSpec      `json:"stackit,omitempty"`
}
```

#### C. Cloud-specific `ProviderSpec` types

```go
type GKEProviderSpec struct {
    // Project is the GCP project ID.
    Project string `json:"project"`
    // Location is the GKE cluster location (region or zone).
    Location string `json:"location"`
    // ClusterName is the GKE cluster name.
    ClusterName string `json:"clusterName"`
    // WorkloadIdentityPool — if set, uses Workload Identity for auth.
    // Format: PROJECT.svc.id.goog
    WorkloadIdentityPool string `json:"workloadIdentityPool,omitempty"`
    // ServiceAccountRef names the K8s ServiceAccount annotated with
    // iam.gke.io/gcp-service-account for Workload Identity.
    // +optional
    ServiceAccountRef string `json:"serviceAccountRef,omitempty"`
}

type EKSProviderSpec struct {
    // Region is the AWS region (e.g. us-east-1).
    Region string `json:"region"`
    // ClusterName is the EKS cluster name.
    ClusterName string `json:"clusterName"`
    // RoleARN is the IAM role ARN Kapro assumes via IRSA.
    // +optional
    RoleARN string `json:"roleARN,omitempty"`
    // AccountID is the AWS account ID. Used for audit and log correlation.
    AccountID string `json:"accountID,omitempty"`
}

type AKSProviderSpec struct {
    // SubscriptionID is the Azure subscription ID.
    SubscriptionID string `json:"subscriptionID"`
    // ResourceGroup is the resource group containing the AKS cluster.
    ResourceGroup string `json:"resourceGroup"`
    // ClusterName is the AKS cluster name.
    ClusterName string `json:"clusterName"`
    // ClientID is the Azure Managed Identity client ID.
    // +optional
    ClientID string `json:"clientID,omitempty"`
    // TenantID is the Azure tenant ID.
    TenantID string `json:"tenantID,omitempty"`
}

type DigitalOceanProviderSpec struct {
    // ClusterID is the DigitalOcean Kubernetes cluster UUID.
    ClusterID string `json:"clusterID"`
    // TokenSecretRef names a K8s Secret in kapro-system containing
    // key "token" with a DigitalOcean API token.
    TokenSecretRef string `json:"tokenSecretRef"`
    // Region is the DigitalOcean region slug (e.g. nyc1, fra1).
    Region string `json:"region,omitempty"`
}

type StackITProviderSpec struct {
    // ProjectID is the STACKIT project ID.
    ProjectID string `json:"projectID"`
    // ClusterName is the SKE (STACKIT Kubernetes Engine) cluster name.
    ClusterName string `json:"clusterName"`
    // Region is the STACKIT region (e.g. eu01).
    Region string `json:"region"`
    // ServiceAccountKeySecretRef names a K8s Secret containing the
    // STACKIT service account key JSON.
    ServiceAccountKeySecretRef string `json:"serviceAccountKeySecretRef"`
}
```

#### D. Extended `ClusterCapabilities`

```go
type ClusterCapabilities struct {
    // Existing fields
    K8sVersion     string `json:"k8sVersion,omitempty"`
    FluxVersion    string `json:"fluxVersion,omitempty"`
    ArgoCDVersion  string `json:"argoCDVersion,omitempty"`
    SveltosVersion string `json:"sveltosVersion,omitempty"`
    NodeCount      int    `json:"nodeCount,omitempty"`
    Region         string `json:"region,omitempty"`

    // New cloud metadata fields
    Cloud      string `json:"cloud,omitempty"`      // gcp|aws|azure|digitalocean|stackit|on-prem
    Zone       string `json:"zone,omitempty"`        // GCP zone, AZ, etc.
    AccountID  string `json:"accountID,omitempty"`   // GCP project, AWS account, Azure subscription
    ClusterID  string `json:"clusterID,omitempty"`   // Cloud-provider cluster identifier
}
```

---

### 3.3 Per-Cloud Onboarding Flows

#### GCP / GKE

**Path A (Outbound — recommended for MVP):**
1. Create Environment with `spec.provider.type: crd`
2. Create BootstrapToken in kapro-system: `kubectl create -f bootstrap-token.yaml`
3. On GKE spoke: `helm install kapro-cc kapro/cluster-controller --set hub.url=<hub> --set hub.token=<token>`
4. `kapro-cluster-controller` authenticates to hub using BootstrapToken, then rotates to a long-lived ServiceAccount token
5. Writes ManagedCluster heartbeat every 30s
6. Done — Environment is live

**Path B (Direct Connect — v0.3):**
- Uses GKE Connect Gateway + Workload Identity
- `spec.provider.gke.workloadIdentityPool` set → hub SA annotated with `iam.gke.io/gcp-service-account`
- `Connector.Connect()` calls GKE API to get kubeconfig, returns `*rest.Config`
- No cluster-controller needed on spoke

**Auth:** Workload Identity (no static keys). Region: `spec.provider.gke.location`. Multi-region: one Environment per region.

---

#### Azure / AKS

**Path A (Outbound — recommended):**
- Same cluster-controller flow as GKE. On AKS, use `helm install` from Azure Container Registry.
- BootstrapToken HMAC flow works identically across clouds.

**Path B (Direct Connect — v0.3):**
- Uses Azure Managed Identity
- `spec.provider.aks.clientID` → Azure Workload Identity annotation on hub ServiceAccount
- `Connector.Connect()` calls AKS API: `GET /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.ContainerService/managedClusters/{name}/listClusterUserCredential`
- Returns kubeconfig with short-lived AAD token

**Auth:** Azure Managed Identity (no client secrets). MSI + OIDC federation.

---

#### AWS / EKS

**Path A (Outbound — recommended):**
- Same cluster-controller flow. On EKS, deploy via Helm from ECR Public.
- BootstrapToken HMAC over HTTPS — works behind VPC NAT.

**Path B (Direct Connect — v0.3):**
- Uses IRSA (IAM Roles for Service Accounts)
- `spec.provider.eks.roleARN` → hub K8s SA annotated with `eks.amazonaws.com/role-arn`
- `Connector.Connect()` calls `eks.DescribeCluster` → gets API endpoint + CA
- Auth via STS AssumeRoleWithWebIdentity → generates `aws eks get-token`-equivalent bearer token

**Auth:** IRSA — no long-lived AWS credentials. Cross-account supported via role chaining.

---

#### DigitalOcean

**Path A (Outbound — recommended, and likely only path for DO):**
- Same cluster-controller flow. Deploy from DOKS marketplace or Helm.
- BootstrapToken HMAC over HTTPS.

**Path B (Direct Connect — v0.3):**
- Uses DigitalOcean API token stored in K8s Secret
- `spec.provider.digitalOcean.tokenSecretRef` → hub reads `Secret{name: tokenSecretRef, namespace: kapro-system}`
- `Connector.Connect()` calls `GET /v2/kubernetes/clusters/{id}/kubeconfig`
- Returns kubeconfig with cluster cert + bearer token

**Note:** DigitalOcean does not have Workload Identity equivalent. Token in Secret is the only option. Recommend rotating tokens via DOKS API every 24h (future enhancement).

---

#### StackIT

**Path A (Outbound — recommended):**
- Same cluster-controller flow on SKE clusters.
- BootstrapToken HMAC over HTTPS.

**Path B (Direct Connect — v0.3):**
- Uses STACKIT Service Account key (JSON credential)
- `spec.provider.stackit.serviceAccountKeySecretRef` → hub reads JSON key from Secret
- `Connector.Connect()` calls STACKIT SKE API: `GET /v1/projects/{projectID}/clusters/{name}/credentials`
- Returns kubeconfig

**Note:** STACKIT is GDPR-compliant EU-sovereign cloud. Flag Environments with `spec.topology.tier: eu-sovereign` for compliance routing in pipeline selectors.

---

### 3.4 Cluster-Controller Bootstrap Flow (Path A, all clouds)

```
1. Platform engineer creates BootstrapToken CR on hub:
   ───────────────────────────────────────────────────
   apiVersion: kapro.io/v1alpha1
   kind: BootstrapToken
   metadata:
     name: gke-prod-eu-west1
     namespace: kapro-system
   spec:
     environmentRef: gke-prod-eu-west1
     ttl: 1h
     # Kapro operator generates HMAC token and stores in
     # Secret kapro-system/bootstrap-token-gke-prod-eu-west1

2. Engineer deploys kapro-cluster-controller on spoke:
   ───────────────────────────────────────────────────
   helm install kapro-cc kapro/cluster-controller \
     --set hub.url=https://kapro.internal \
     --set hub.bootstrapToken=$(kubectl get secret -n kapro-system \
         bootstrap-token-gke-prod-eu-west1 -o jsonpath='{.data.token}' | base64 -d) \
     --set cluster.environmentRef=gke-prod-eu-west1 \
     --set cluster.cloud=gcp \
     --set cluster.region=europe-west1

3. cluster-controller registers:
   ───────────────────────────────────────────────────
   POST /api/v1alpha1/register
   Authorization: Bearer <bootstrap-HMAC-token>
   Body: ManagedCluster{spec: {environmentRef, capabilities, ...}}

4. Kapro operator validates HMAC, creates ManagedCluster CR, issues long-lived SA token.

5. cluster-controller stores SA token in local Secret, begins 30s heartbeat loop.

6. BootstrapToken CR is deleted (or TTL expires) — it is single-use.

7. Environment is live. First Release can be created.
```

---

## 4. Gaps Summary (Priority Order)

| # | Gap | Severity | Effort | When |
|---|-----|----------|--------|------|
| G1 | `provider.Registry` missing (no dispatch for Connector types) | HIGH | S (1 day) | Before any cloud connector |
| G2 | `ProviderSpec` is a stub — no cloud fields | HIGH | M (2 days) | Before Path B work |
| G3 | `ClusterCapabilities` missing cloud metadata | MEDIUM | S (2 hours) | Before v0.2 |
| G4 | `CurrentVersion()` hardcodes `"ocs"` appKey | MEDIUM | XS (30 min) | Now |
| G5 | Dual `Result.Passed` + `Result.Phase` — deprecate `Passed` | LOW | S (v0.2 cleanup) | v0.2 |
| G6 | `CELGate` wired differently from other gates in SyncReconciler | LOW | S (1 day) | v0.2 |
| G7 | `IsConverged` signature missing explicit AppKey param | LOW | S (v0.2) | v0.2 |

---

## 5. Verdict

The MVP is architecturally sound. The three public interfaces (KGI, KAI, KCI) are clean, well-documented, and principled. The CCM-style controller manager is easy to navigate for new contributors. The CRD-based outbound provider pattern works on every cloud without cloud-specific code in the hub.

**Path to multi-cloud:**
- Fix G1 (provider.Registry) and G4 (appKey bug) immediately
- Add cloud `ProviderSpec` fields (G2) and capabilities metadata (G3) as part of the first cloud connector PR
- Build cloud connectors (Path B) as separate `internal/provider/{gke,eks,aks,digitalocean,stackit}/` packages, each implementing `kci.Connector` and passing `conformance/provider.RunSuite`

The architecture does not need structural changes — it needs to be extended, not refactored.
