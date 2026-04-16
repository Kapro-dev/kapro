// Kapro CRD TypeScript types — mirrors api/v1alpha1/types.go

export interface KaproMeta {
  name: string;
  namespace?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  creationTimestamp?: string;
  ownerReferences?: { kind: string; name: string }[];
}

// ---- Artifact ---------------------------------------------------------------

export interface Artifact {
  metadata: KaproMeta;
  spec: {
    sources: { type: string; oci?: { repository: string; tag: string; digest: string } }[];
    metadata?: { releasedBy?: string; description?: string };
  };
}

// ---- Environment ------------------------------------------------------------

export interface Environment {
  metadata: KaproMeta;
  spec: {
    actuator: {
      type: 'flux' | 'argocd';
      flux?: { namespace: string; ociRepository: string; kustomizationPath: string };
    };
    healthCheck?: { endpoint: string; interval: string };
  };
  status?: {
    activeRelease?: string;
    phase?: string;
  };
}

// ---- ClusterRegistration ----------------------------------------------------

export type ClusterPhase = 'Pending' | 'Applying' | 'Converging' | 'Converged' | 'Failed';

export interface ClusterRegistration {
  metadata: KaproMeta;
  spec: {
    environmentRef: string;
    controllerVersion?: string;
  };
  status?: {
    lastHeartbeat?: string;
    healthy?: boolean;
    fluxVersion?: string;
    fluxReady?: boolean;
    currentVersion?: string;
    phase?: ClusterPhase;
    conditions?: { type: string; status: string; message?: string }[];
  };
}

// ---- Release ----------------------------------------------------------------

export type ReleasePhase = 'Pending' | 'Promoting' | 'Progressing' | 'Complete' | 'Failed';

export interface Release {
  metadata: KaproMeta;
  spec: {
    artifact: string;
    scope: { selector: { matchLabels: Record<string, string> } };
    pipelineRef: string;
    pipelineOverrides?: { gatePolicy?: { bakePeriod?: string } };
  };
  status?: {
    phase?: ReleasePhase;
    pipelineRef?: string;
    conditions?: { type: string; status: string; message?: string }[];
  };
}

// ---- Pipeline ---------------------------------------------------------------

export interface Pipeline {
  metadata: KaproMeta;
  spec: {
    promotion: {
      steps: {
        name: string;
        selector: { matchLabels: Record<string, string> };
        policy: string;
        dependsOn?: string[];
      }[];
    };
    progression: {
      batches: {
        name: string;
        dependsOn?: string[];
        selectors: { matchLabels: Record<string, string> }[];
      }[];
    };
  };
  status?: {
    phase?: string;
    activeStep?: string;
  };
}

// ---- Approval ---------------------------------------------------------------

export interface Approval {
  metadata: KaproMeta;
  spec: {
    kind: 'Promotion' | 'Batch';
    ref: string;
    release: string;
    approvedBy: string;
    bypass?: boolean;
    comment?: string;
  };
}
