// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useState, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Alert, Empty, Input, Select, Space, Table, Tag, Typography } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { listResource } from '../../api/k8s';
import { StatusBadge } from '../common/StatusBadge';
import type { Environment, ClusterRegistration } from '../../gen/types/kapro';
import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';

dayjs.extend(relativeTime);

const { Title } = Typography;

export const EnvironmentsPage: React.FC = () => {
  const [search, setSearch] = useState('');
  const [providerFilter, setProviderFilter] = useState<string>('');

  const { data: environments = [], error: envErr } = useQuery({
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

  const filtered = useMemo(() => environments.filter(e => {
    if (search && !e.metadata.name.toLowerCase().includes(search.toLowerCase())) return false;
    if (providerFilter && e.spec.actuator.type !== providerFilter) return false;
    return true;
  }), [environments, search, providerFilter]);

  const columns = [
    {
      title: 'Name',
      dataIndex: ['metadata', 'name'],
      key: 'name',
      sorter: (a: Environment, b: Environment) => a.metadata.name.localeCompare(b.metadata.name),
    },
    {
      title: 'Provider',
      key: 'provider',
      render: (_: unknown, r: Environment) => (
        <Tag color={r.spec.actuator.type === 'flux' ? 'blue' : 'purple'}>
          {r.spec.actuator.type}
        </Tag>
      ),
    },
    {
      title: 'Actuator',
      key: 'actuator',
      render: (_: unknown, r: Environment) => {
        const flux = r.spec.actuator.flux;
        if (flux) return <span style={{ fontSize: 12 }}>{flux.namespace}/{flux.ociRepository}</span>;
        return '—';
      },
    },
    {
      title: 'Active Release',
      key: 'release',
      render: (_: unknown, r: Environment) => r.status?.activeRelease ?? '—',
    },
    {
      title: 'Phase',
      key: 'phase',
      render: (_: unknown, r: Environment) => {
        const reg = regByEnv.get(r.metadata.name);
        const phase = reg?.status?.phase ?? r.status?.phase;
        return <StatusBadge phase={phase} />;
      },
    },
    {
      title: 'Last Heartbeat',
      key: 'heartbeat',
      render: (_: unknown, r: Environment) => {
        const reg = regByEnv.get(r.metadata.name);
        const hb = reg?.status?.lastHeartbeat;
        return hb ? dayjs(hb).fromNow() : '—';
      },
    },
  ];

  if (envErr) return <Alert type="error" message="Failed to load environments" description={(envErr as Error).message} />;

  return (
    <div>
      <Title level={3} style={{ marginBottom: 16 }}>Environments</Title>

      <Space style={{ marginBottom: 16 }} wrap>
        <Input
          prefix={<SearchOutlined />}
          placeholder="Search environments…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{ width: 220 }}
          allowClear
        />
        <Select
          placeholder="Provider type"
          allowClear
          value={providerFilter || undefined}
          onChange={v => setProviderFilter(v ?? '')}
          style={{ width: 150 }}
          options={[
            { value: 'flux', label: 'Flux' },
            { value: 'argocd', label: 'ArgoCD' },
          ]}
        />
      </Space>

      <Table<Environment>
        dataSource={filtered}
        columns={columns}
        rowKey={r => r.metadata.name}
        size="middle"
        locale={{ emptyText: <Empty description="No environments found" /> }}
        pagination={{ pageSize: 20 }}
      />
    </div>
  );
};
