// Kapro Kubernetes API client
// Talks directly to the K8s API server — no connect-rpc, no gRPC.
// Uses native K8s watch for live updates.

const API_GROUP = 'kapro.io';
const API_VERSION = 'v1alpha1';
const BASE = `/apis/${API_GROUP}/${API_VERSION}`;

export type KaproResource =
  | 'artifacts'
  | 'environments'
  | 'clusterregistrations'
  | 'promotionpolicies'
  | 'pipelines'
  | 'releases'
  | 'approvals';

// List a cluster-scoped resource
export async function listResource<T>(resource: KaproResource): Promise<T[]> {
  const res = await fetch(`${BASE}/${resource}`);
  if (!res.ok) throw new Error(`Failed to list ${resource}: ${res.statusText}`);
  const data = await res.json();
  return data.items ?? [];
}

// Get a single resource
export async function getResource<T>(resource: KaproResource, name: string, namespace?: string): Promise<T> {
  const path = namespace
    ? `/api/v1/namespaces/${namespace}/${resource}/${name}`
    : `${BASE}/${resource}/${name}`;
  const res = await fetch(path);
  if (!res.ok) throw new Error(`Failed to get ${resource}/${name}: ${res.statusText}`);
  return res.json();
}

// Watch a resource — yields events via callback
export function watchResource<T>(
  resource: KaproResource,
  onEvent: (type: 'ADDED' | 'MODIFIED' | 'DELETED', obj: T) => void,
  signal?: AbortSignal
): void {
  const url = `${BASE}/${resource}?watch=true`;

  fetch(url, { signal })
    .then(async (res) => {
      const reader = res.body?.getReader();
      if (!reader) return;
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop() ?? '';
        for (const line of lines) {
          if (!line.trim()) continue;
          try {
            const event = JSON.parse(line);
            onEvent(event.type, event.object);
          } catch {
            // ignore parse errors on partial chunks
          }
        }
      }
    })
    .catch((err) => {
      if (err.name !== 'AbortError') console.error('watch error', resource, err);
    });
}

// Create an Approval to unblock a Promotion or Batch
export async function createApproval(approval: {
  name: string;
  namespace: string;
  kind: 'Promotion' | 'Batch';
  ref: string;
  release: string;
  approvedBy: string;
  bypass?: boolean;
  comment?: string;
}): Promise<void> {
  const body = {
    apiVersion: `${API_GROUP}/${API_VERSION}`,
    kind: 'Approval',
    metadata: { name: approval.name, namespace: approval.namespace },
    spec: {
      kind: approval.kind,
      ref: approval.ref,
      release: approval.release,
      approvedBy: approval.approvedBy,
      bypass: approval.bypass ?? false,
      comment: approval.comment ?? '',
    },
  };

  const res = await fetch(
    `/api/v1/namespaces/${approval.namespace}/approvals`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }
  );

  if (!res.ok) throw new Error(`Failed to create Approval: ${res.statusText}`);
}
