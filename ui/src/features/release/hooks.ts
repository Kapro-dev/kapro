// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';
import { listResource, getResource, watchResource } from '../../api/k8s';
import type { Release, Promotion, Pipeline, BatchRun, Environment } from '../../gen/types/kapro';

export function useReleases() {
  const qc = useQueryClient();

  useEffect(() => {
    const ctrl = new AbortController();
    watchResource<Release>('releases', (type, obj) => {
      qc.setQueryData<Release[]>(['releases'], (prev = []) => {
        if (type === 'DELETED') return prev.filter(r => r.metadata.name !== obj.metadata.name);
        const idx = prev.findIndex(r => r.metadata.name === obj.metadata.name);
        if (idx >= 0) { const next = [...prev]; next[idx] = obj; return next; }
        return [...prev, obj];
      });
    }, ctrl.signal);
    return () => ctrl.abort();
  }, [qc]);

  return useQuery({
    queryKey: ['releases'],
    queryFn: () => listResource<Release>('releases'),
  });
}

export function useRelease(name: string) {
  return useQuery({
    queryKey: ['releases', name],
    queryFn: () => getResource<Release>('releases', name),
    enabled: !!name,
  });
}

export function useEnvironments() {
  return useQuery({
    queryKey: ['environments'],
    queryFn: () => listResource<Environment>('environments'),
    staleTime: 30_000,
  });
}

export function usePromotions(releaseRef?: string) {
  const qc = useQueryClient();

  useEffect(() => {
    const ctrl = new AbortController();
    watchResource<Promotion>('promotions', () => {
      qc.invalidateQueries({ queryKey: ['promotions'] });
    }, ctrl.signal);
    return () => ctrl.abort();
  }, [qc]);

  return useQuery({
    queryKey: ['promotions', releaseRef],
    queryFn: async () => {
      const all = await listResource<Promotion>('promotions');
      if (!releaseRef) return all;
      return all.filter(p =>
        p.metadata.labels?.['kapro.io/release'] === releaseRef ||
        p.spec.releaseRef === releaseRef
      );
    },
    refetchInterval: 10_000,
    enabled: true,
  });
}

export function useBatchRuns(releaseRef?: string) {
  const qc = useQueryClient();

  useEffect(() => {
    const ctrl = new AbortController();
    watchResource<BatchRun>('batchruns', () => {
      qc.invalidateQueries({ queryKey: ['batchruns'] });
    }, ctrl.signal);
    return () => ctrl.abort();
  }, [qc]);

  return useQuery({
    queryKey: ['batchruns', releaseRef],
    queryFn: async () => {
      const all = await listResource<BatchRun>('batchruns');
      if (!releaseRef) return all;
      return all.filter(b =>
        b.metadata.labels?.['kapro.io/release'] === releaseRef ||
        b.spec.releaseRef === releaseRef
      );
    },
    refetchInterval: 10_000,
    enabled: true,
  });
}

export function usePipeline(name?: string) {
  return useQuery({
    queryKey: ['pipelines', name],
    queryFn: () => getResource<Pipeline>('pipelines', name!),
    enabled: !!name,
  });
}
