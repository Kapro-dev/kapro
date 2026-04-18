// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Alert, Empty, Input, Select, Space, Tag, Typography, Tooltip } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { listResource } from '../../api/k8s';
import type { Environment, ClusterRegistration, ClusterPhase } from '../../gen/types/kapro';

const { Title, Text } = Typography;

const PHASE_COLORS: Record<string, string> = {
  Converged: '#16a34a',
  Applying: '#2563eb',
  Converging: '#d97706',
  Failed: '#dc2626',
  Pending: '#6b7280',
};

// Tag colors for label values — cycles through a palette
const LABEL_TAG_COLORS = ['blue', 'cyan', 'geekblue', 'purple', 'volcano', 'gold', 'lime'];
const labelColor = (val: string) =>
  LABEL_TAG_COLORS[val.charCodeAt(0) % LABEL_TAG_COLORS.length];

interface EnvironmentCardProps {
  env: Environment;
  reg?: ClusterRegistration;
  groupByKey: string;
}

const EnvironmentCard: React.FC<EnvironmentCardProps> = ({ env, reg }) => {
  const phase: ClusterPhase = (reg?.status?.phase ?? env.status?.phase ?? 'Pending') as ClusterPhase;
  const borderColor = PHASE_COLORS[phase] ?? '#6b7280';
  const healthColor = PHASE_COLORS[phase] ?? '#9ca3af';

  // Show all labels except kapro.io/* internals — they're too noisy
  const displayLabels = Object.entries(env.metadata.labels ?? {}).filter(
    ([k]) => !k.startsWith('kapro.io/'),
  );

  return (
    <Tooltip
      title={
        <div style={{ fontSize: 12 }}>
          <div style={{ fontWeight: 600, marginBottom: 4 }}>{env.metadata.name}</div>
          <div>Phase: {phase}</div>
          {env.status?.activeRelease && <div>Active release: {env.status.activeRelease}</div>}
          {reg?.status?.currentVersion && <div>Version: {reg.status.currentVersion}</div>}
          {reg?.status?.lastHeartbeat && (
            <div>Heartbeat: {new Date(reg.status.lastHeartbeat).toLocaleString()}</div>
          )}
          {displayLabels.length > 0 && (
            <div style={{ marginTop: 4 }}>
              {displayLabels.map(([k, v]) => (
                <div key={k} style={{ color: '#ccc' }}>{k}={v}</div>
              ))}
            </div>
          )}
        </div>
      }
    >
      <div
        style={{
          width: 140,
          padding: '10px 8px',
          border: `2px solid ${borderColor}`,
          borderRadius: 8,
          background: '#fff',
          cursor: 'pointer',
          position: 'relative',
        }}
      >
        {/* Phase dot */}
        <span
          style={{
            position: 'absolute', top: 7, right: 7,
            width: 8, height: 8, borderRadius: '50%',
            background: healthColor, display: 'inline-block',
          }}
        />

        {/* Env name */}
        <Text
          strong
          style={{
            fontSize: 11, display: 'block',
            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
            marginBottom: 4, paddingRight: 14,
          }}
        >
          {env.metadata.name}
        </Text>

        {/* Label chips — all non-kapro.io labels */}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 2, marginBottom: 4 }}>
          {displayLabels.slice(0, 4).map(([k, v]) => (
            <Tag
              key={k}
              color={labelColor(v)}
              style={{ fontSize: 9, padding: '0 3px', lineHeight: '14px', margin: 0 }}
            >
              {v}
            </Tag>
          ))}
          {displayLabels.length > 4 && (
            <Tag style={{ fontSize: 9, padding: '0 3px', lineHeight: '14px', margin: 0 }}>
              +{displayLabels.length - 4}
            </Tag>
          )}
        </div>

        {/* Active release */}
        {env.status?.activeRelease && (
          <Text
            style={{
              fontSize: 9, display: 'block', color: '#6b7280',
              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
            }}
          >
            🚀 {env.status.activeRelease}
          </Text>
        )}
      </div>
    </Tooltip>
  );
};

export const ClusterGridPage: React.FC = () => {
  const [search, setSearch] = useState('');
  const [groupByKey, setGroupByKey] = useState('env');
  const [filterLabel, setFilterLabel] = useState<string>('');

  const { data: environments = [], error: envError } = useQuery({
    queryKey: ['environments'],
    queryFn: () => listResource<Environment>('environments'),
  });

  const { data: registrations = [] } = useQuery({
    queryKey: ['clusterregistrations'],
    queryFn: () => listResource<ClusterRegistration>('clusterregistrations'),
  });

  const regByEnv = useMemo(() => {
    const m = new Map<string, ClusterRegistration>();
    for (const r of registrations) m.set(r.spec.environmentRef, r);
    return m;
  }, [registrations]);

  // Collect all unique label keys present across all environments (excluding kapro.io/*)
  const allLabelKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const e of environments) {
      for (const k of Object.keys(e.metadata.labels ?? {})) {
        if (!k.startsWith('kapro.io/')) keys.add(k);
      }
    }
    return Array.from(keys).sort();
  }, [environments]);

  // Collect distinct values for the active label filter key
  const filterLabelValues = useMemo(() => {
    const vals = new Set<string>();
    for (const e of environments) {
      const v = e.metadata.labels?.[groupByKey];
      if (v) vals.add(v);
    }
    return Array.from(vals).sort();
  }, [environments, groupByKey]);

  const filtered = useMemo(() => {
    return environments.filter(e => {
      if (search && !e.metadata.name.toLowerCase().includes(search.toLowerCase())) return false;
      if (filterLabel && e.metadata.labels?.[groupByKey] !== filterLabel) return false;
      return true;
    });
  }, [environments, search, groupByKey, filterLabel]);

  // Group by selected label key; ungrouped items go under "(none)"
  const groups = useMemo(() => {
    const m = new Map<string, Environment[]>();
    for (const e of filtered) {
      const key = e.metadata.labels?.[groupByKey] ?? '(none)';
      const arr = m.get(key) ?? [];
      arr.push(e);
      m.set(key, arr);
    }
    return Array.from(m.entries()).sort(([a], [b]) => a.localeCompare(b));
  }, [filtered, groupByKey]);

  const convergedCount = environments.filter(e => {
    const reg = regByEnv.get(e.metadata.name);
    return (reg?.status?.phase ?? e.status?.phase) === 'Converged';
  }).length;

  if (envError) return <Alert type="error" message="Failed to load environments" />;

  return (
    <div>
      <Title level={3} style={{ marginBottom: 4 }}>Cluster Grid</Title>
      <Space style={{ marginBottom: 16 }} size={16}>
        <Tag color="blue">{environments.length} environments</Tag>
        <Tag color="green">{convergedCount} converged</Tag>
        <Tag color="red">
          {environments.filter(e => {
            const reg = regByEnv.get(e.metadata.name);
            return (reg?.status?.phase ?? e.status?.phase) === 'Failed';
          }).length} failed
        </Tag>
      </Space>

      <Space style={{ marginBottom: 20 }} wrap>
        <Input
          prefix={<SearchOutlined />}
          placeholder="Search clusters…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{ width: 200 }}
          allowClear
        />
        <Select
          value={groupByKey}
          onChange={v => { setGroupByKey(v); setFilterLabel(''); }}
          style={{ width: 160 }}
          options={allLabelKeys.map(k => ({ value: k, label: `Group by: ${k}` }))}
          placeholder="Group by label"
        />
        {filterLabelValues.length > 0 && (
          <Select
            placeholder={`Filter ${groupByKey}…`}
            allowClear
            value={filterLabel || undefined}
            onChange={v => setFilterLabel(v ?? '')}
            style={{ width: 160 }}
            options={filterLabelValues.map(v => ({ value: v, label: v }))}
          />
        )}
      </Space>

      {filtered.length === 0 && <Empty description="No environments match the current filter" />}

      {groups.map(([groupVal, envs]) => (
        <div key={groupVal} style={{ marginBottom: 28 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
            <Text
              strong
              style={{ fontSize: 13, textTransform: 'uppercase', letterSpacing: '0.05em' }}
            >
              {groupByKey}={groupVal}
            </Text>
            <Tag>{envs.length}</Tag>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10 }}>
            {envs.map(e => (
              <EnvironmentCard
                key={e.metadata.name}
                env={e}
                reg={regByEnv.get(e.metadata.name)}
                groupByKey={groupByKey}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
};

// Keep old export name as alias so App.tsx import still works
export const WorldGridPage = ClusterGridPage;
