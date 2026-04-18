// Mock data for UI development — not shipped in production build

const now = new Date().toISOString();
const ago = (mins: number) => new Date(Date.now() - mins * 60_000).toISOString();

// ── ClusterRegistrations ──────────────────────────────────────────────────────
export const MOCK_CLUSTER_REGISTRATIONS = [
  { metadata: { name: 'de-dev-reg'  }, spec: { environmentRef: 'de-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(1),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'de-prod-reg' }, spec: { environmentRef: 'de-prod' }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(2),    currentVersion: 'v1.2.3', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'fi-dev-reg'  }, spec: { environmentRef: 'fi-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(1),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'fi-prod-reg' }, spec: { environmentRef: 'fi-prod' }, status: { phase: 'Applying',   healthy: true,  lastHeartbeat: ago(3),    currentVersion: 'v1.2.3', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'es-dev-reg'  }, spec: { environmentRef: 'es-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(1),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'es-prod-reg' }, spec: { environmentRef: 'es-prod' }, status: { phase: 'Pending',    healthy: false, lastHeartbeat: ago(120),  currentVersion: 'v1.2.2', fluxVersion: 'v2.3.0', fluxReady: false } },
  { metadata: { name: 'cz-dev-reg'  }, spec: { environmentRef: 'cz-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(2),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'cz-prod-reg' }, spec: { environmentRef: 'cz-prod' }, status: { phase: 'Failed',     healthy: false, lastHeartbeat: ago(45),   currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: false, conditions: [{ type: 'Ready', status: 'False', message: 'pod crashlooping: ocs-checkout' }] } },
  { metadata: { name: 'pl-dev-reg'  }, spec: { environmentRef: 'pl-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(1),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'pl-prod-reg' }, spec: { environmentRef: 'pl-prod' }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(5),    currentVersion: 'v1.2.3', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'pt-dev-reg'  }, spec: { environmentRef: 'pt-dev'  }, status: { phase: 'Converged',  healthy: true,  lastHeartbeat: ago(2),    currentVersion: 'v1.2.4', fluxVersion: 'v2.3.0', fluxReady: true  } },
  { metadata: { name: 'pt-prod-reg' }, spec: { environmentRef: 'pt-prod' }, status: { phase: 'Pending',    healthy: false, lastHeartbeat: ago(200),  currentVersion: 'v1.2.1', fluxVersion: 'v2.2.0', fluxReady: false } },
];

// ── Approvals ─────────────────────────────────────────────────────────────────
export const MOCK_APPROVALS = [
  { metadata: { name: 'approval-fi-prod-1', creationTimestamp: ago(5) }, spec: { kind: 'Promotion', ref: 'ocs-v1.2.4-fi-prod', release: 'ocs-v1.2.4', approvedBy: 'alice', bypass: false, comment: 'metrics nominal, health good' } },
];

// ── Environments ──────────────────────────────────────────────────────────────
export const MOCK_ENVIRONMENTS = [
  { metadata: { name: 'de-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-west3' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'de-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-west3' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.3' } },
  { metadata: { name: 'fi-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-north1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'fi-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-north1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Applying',   activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'es-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-south1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'es-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-south1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Pending',   activeRelease: null } },
  { metadata: { name: 'cz-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-central1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'cz-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-central1' } }, spec: { actuator: { type: 'argocd' } }, status: { phase: 'Failed',   activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'pl-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-central1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'pl-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-central1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.3' } },
  { metadata: { name: 'pt-dev',   labels: { env: 'dev',  tier: 'dev',  region: 'eu-west1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Converged', activeRelease: 'ocs-v1.2.4' } },
  { metadata: { name: 'pt-prod',  labels: { env: 'prod', tier: 'prod', region: 'eu-west1' } }, spec: { actuator: { type: 'flux' } }, status: { phase: 'Pending',   activeRelease: null } },
];

// ── Releases ──────────────────────────────────────────────────────────────────
export const MOCK_RELEASES = [
  {
    metadata: { name: 'ocs-v1.2.4', labels: { 'kapro.io/artifact': 'ocs-v1.2.4' }, creationTimestamp: ago(90) },
    spec: { artifact: 'ocs-v1.2.4', scope: { selector: { matchLabels: { tier: 'prod' } } }, pipelineRef: 'global-rollout' },
    status: { phase: 'Progressing', pipelineRef: 'ocs-v1.2.4-pipeline', conditions: [{ type: 'Ready', status: 'False', message: 'Batch 2 in progress' }] },
  },
  {
    metadata: { name: 'ocs-v1.2.3', labels: { 'kapro.io/artifact': 'ocs-v1.2.3' }, creationTimestamp: ago(3 * 24 * 60) },
    spec: { artifact: 'ocs-v1.2.3', scope: { selector: { matchLabels: { tier: 'prod' } } }, pipelineRef: 'global-rollout' },
    status: { phase: 'Complete', pipelineRef: 'ocs-v1.2.3-pipeline', conditions: [{ type: 'Ready', status: 'True', message: 'All environments converged' }] },
  },
  {
    metadata: { name: 'ocs-v1.2.2', labels: { 'kapro.io/artifact': 'ocs-v1.2.2' }, creationTimestamp: ago(7 * 24 * 60) },
    spec: { artifact: 'ocs-v1.2.2', scope: { selector: { matchLabels: { tier: 'prod' } } }, pipelineRef: 'global-rollout' },
    status: { phase: 'Complete', pipelineRef: 'ocs-v1.2.2-pipeline', conditions: [{ type: 'Ready', status: 'True', message: 'All environments converged' }] },
  },
  {
    metadata: { name: 'ocs-v1.2.5-rc1', labels: { 'kapro.io/artifact': 'ocs-v1.2.5-rc1' }, creationTimestamp: ago(15) },
    spec: { artifact: 'ocs-v1.2.5-rc1', scope: { selector: { matchLabels: { tier: 'dev' } } }, pipelineRef: 'dev-rollout' },
    status: { phase: 'Promoting', pipelineRef: 'ocs-v1.2.5-rc1-pipeline', conditions: [{ type: 'Ready', status: 'False', message: 'Promoting to dev environments' }] },
  },
];

// ── Pipelines ─────────────────────────────────────────────────────────────────
export const MOCK_PIPELINES = [
  {
    metadata: { name: 'ocs-v1.2.4-pipeline' },
    spec: {
      promotion: {
        steps: [
          { name: 'dev',  selector: { matchLabels: { tier: 'dev' } },  policy: 'standard-dev-gate' },
          { name: 'prod', selector: { matchLabels: { tier: 'prod' } }, policy: 'standard-prod-gate', dependsOn: ['dev'] },
        ],
      },
      progression: {
        batches: [
          { name: 'batch-1', dependsOn: [],          selectors: [{ matchLabels: { env: 'prod', region: 'eu-west3' } }] },
          { name: 'batch-2', dependsOn: ['batch-1'], selectors: [{ matchLabels: { env: 'prod', region: 'eu-north1' } }, { matchLabels: { env: 'prod', region: 'eu-central1' } }] },
          { name: 'batch-3', dependsOn: ['batch-2'], selectors: [{ matchLabels: { env: 'prod', region: 'eu-south1' } }, { matchLabels: { env: 'prod', region: 'eu-west1' } }] },
        ],
      },
    },
    status: { phase: 'Progressing', activeStep: 'batch-2' },
  },
  {
    metadata: { name: 'ocs-v1.2.3-pipeline' },
    spec: {
      promotion: {
        steps: [
          { name: 'dev',  selector: { matchLabels: { tier: 'dev' } },  policy: 'standard-dev-gate' },
          { name: 'prod', selector: { matchLabels: { tier: 'prod' } }, policy: 'standard-prod-gate', dependsOn: ['dev'] },
        ],
      },
      progression: {
        batches: [
          { name: 'batch-1', dependsOn: [],          selectors: [{ matchLabels: { env: 'prod', region: 'eu-west3' } }] },
          { name: 'batch-2', dependsOn: ['batch-1'], selectors: [{ matchLabels: { env: 'prod', region: 'eu-north1' } }] },
          { name: 'batch-3', dependsOn: ['batch-2'], selectors: [{ matchLabels: { env: 'prod', region: 'eu-south1' } }] },
        ],
      },
    },
    status: { phase: 'Complete', activeStep: '' },
  },
];

// ── Promotions ────────────────────────────────────────────────────────────────
export const MOCK_PROMOTIONS = [
  { metadata: { name: 'ocs-v1.2.4-de-dev',  labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'de-dev',  version: 'v1.2.4' }, status: { phase: 'Complete', message: 'Converged in 4m32s' } },
  { metadata: { name: 'ocs-v1.2.4-fi-dev',  labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'fi-dev',  version: 'v1.2.4' }, status: { phase: 'Complete', message: 'Converged in 3m11s' } },
  { metadata: { name: 'ocs-v1.2.4-es-dev',  labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'es-dev',  version: 'v1.2.4' }, status: { phase: 'Complete', message: 'Converged in 5m02s' } },
  { metadata: { name: 'ocs-v1.2.4-de-prod', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'de-prod', version: 'v1.2.4' }, status: { phase: 'Complete', message: 'Converged in 8m15s', gateStatus: { metricsPass: true, healthStatus: 'Healthy' } } },
  { metadata: { name: 'ocs-v1.2.4-fi-prod', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'fi-prod', version: 'v1.2.4' }, status: { phase: 'WaitingApproval', message: 'Soak complete — awaiting approval', gateStatus: { soakRemaining: '0s', metricsPass: true, healthStatus: 'Healthy' } } },
  { metadata: { name: 'ocs-v1.2.4-cz-prod', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'cz-prod', version: 'v1.2.4' }, status: { phase: 'Failed', message: 'Health check failed: pod crashlooping' } },
  { metadata: { name: 'ocs-v1.2.4-es-prod', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', environmentRef: 'es-prod', version: 'v1.2.4' }, status: { phase: 'Pending', message: 'Waiting for batch-2 to complete' } },
];

// ── BatchRuns ─────────────────────────────────────────────────────────────────
export const MOCK_BATCHRUNS = [
  { metadata: { name: 'ocs-v1.2.4-batch-1', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', batchName: 'batch-1' }, status: { phase: 'Complete',  message: 'All environments converged' } },
  { metadata: { name: 'ocs-v1.2.4-batch-2', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', batchName: 'batch-2' }, status: { phase: 'WaitingApproval', message: '2/3 environments converged, awaiting approval' } },
  { metadata: { name: 'ocs-v1.2.4-batch-3', labels: { 'kapro.io/release': 'ocs-v1.2.4' } }, spec: { releaseRef: 'ocs-v1.2.4', batchName: 'batch-3' }, status: { phase: 'Pending',  message: 'Waiting for batch-2' } },
];

// ── Artifacts ─────────────────────────────────────────────────────────────────
export const MOCK_ARTIFACTS = [
  { metadata: { name: 'ocs-v1.2.5-rc1', creationTimestamp: ago(15) }, spec: { sources: [{ type: 'oci', oci: { repository: 'registry.example.io/ocs', tag: 'v1.2.5-rc1', digest: 'sha256:ab12cd34' } }], metadata: { releasedBy: 'vinayaka', description: 'RC1 — payment gateway retry fix' } } },
  { metadata: { name: 'ocs-v1.2.4',    creationTimestamp: ago(90) }, spec: { sources: [{ type: 'oci', oci: { repository: 'registry.example.io/ocs', tag: 'v1.2.4',    digest: 'sha256:a1b2c3d4' } }], metadata: { releasedBy: 'vinayaka', description: 'AlloyDB fix + Keycloak probe update' } } },
  { metadata: { name: 'ocs-v1.2.3',    creationTimestamp: ago(3 * 24 * 60) }, spec: { sources: [{ type: 'oci', oci: { repository: 'registry.example.io/ocs', tag: 'v1.2.3',    digest: 'sha256:f0e1d2c3' } }], metadata: { releasedBy: 'marco',    description: 'Session broker stability improvements' } } },
];

// ── Route table ───────────────────────────────────────────────────────────────
export const MOCK_DB: Record<string, unknown[]> = {
  environments:         MOCK_ENVIRONMENTS,
  releases:             MOCK_RELEASES,
  pipelines:            MOCK_PIPELINES,
  promotions:           MOCK_PROMOTIONS,
  batchruns:            MOCK_BATCHRUNS,
  artifacts:            MOCK_ARTIFACTS,
  approvals:            MOCK_APPROVALS,
  clusterregistrations: MOCK_CLUSTER_REGISTRATIONS,
  promotionpolicies: [],
};
