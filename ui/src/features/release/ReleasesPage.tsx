// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useMemo, useState } from 'react';
import { Input, Tag, Space, Alert, Empty, Row, Col, Card, Typography } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useReleases } from './hooks';
import { StatusBadge } from '../common/StatusBadge';
import { StatusBar } from '../common/StatusBar';
import type { Release, ReleasePhase } from '../../gen/types/kapro';
import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';

dayjs.extend(relativeTime);

const { Text, Title } = Typography;

const PHASE_FILTER_OPTIONS: (ReleasePhase | 'All')[] = ['All', 'Complete', 'Progressing', 'Promoting', 'Failed', 'Pending'];

const phaseBorderColor: Record<string, string> = {
  Complete:    '#16a34a',
  Progressing: '#2563eb',
  Promoting:   '#2563eb',
  Failed:      '#dc2626',
  Pending:     '#6b7280',
};

interface ReleaseTileProps {
  release: Release;
  onClick: () => void;
}

const ReleaseTile: React.FC<ReleaseTileProps> = ({ release, onClick }) => {
  const phase = release.status?.phase ?? 'Pending';
  const borderColor = phaseBorderColor[phase] ?? '#6b7280';
  const labels = release.spec.scope?.selector?.matchLabels ?? {};

  return (
    <Card
      hoverable
      onClick={onClick}
      style={{
        borderLeft: `4px solid ${borderColor}`,
        borderRadius: 6,
        cursor: 'pointer',
        marginBottom: 0,
      }}
      styles={{ body: { padding: '14px 16px' } }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 10 }}>
        <Text strong style={{ fontSize: 15 }}>{release.metadata.name}</Text>
        <StatusBadge phase={phase} />
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '80px 1fr', gap: '4px 8px', fontSize: 12 }}>
        <Text type="secondary">Artifact</Text>
        <Text ellipsis style={{ maxWidth: 180 }}>{release.spec.artifact}</Text>

        <Text type="secondary">Pipeline</Text>
        <Text>{release.spec.pipelineRef}</Text>

        <Text type="secondary">Scope</Text>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 2 }}>
          {Object.entries(labels).map(([k, v]) => (
            <Tag key={k} style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}>
              {k}={v}
            </Tag>
          ))}
        </div>

        <Text type="secondary">Created</Text>
        <Text>{release.metadata.creationTimestamp ? dayjs(release.metadata.creationTimestamp).fromNow() : '—'}</Text>
      </div>
    </Card>
  );
};

export const ReleasesPage: React.FC = () => {
  const navigate = useNavigate();
  const { data: releases = [], isLoading, error } = useReleases();
  const [search, setSearch] = useState('');
  const [phaseFilter, setPhaseFilter] = useState<string>('All');

  const phaseCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const r of releases) {
      const p = r.status?.phase ?? 'Pending';
      counts[p] = (counts[p] ?? 0) + 1;
    }
    return counts;
  }, [releases]);

  const filtered = useMemo(() =>
    releases.filter(r => {
      const matchSearch = !search || r.metadata.name.toLowerCase().includes(search.toLowerCase());
      const matchPhase = phaseFilter === 'All' || (r.status?.phase ?? 'Pending') === phaseFilter;
      return matchSearch && matchPhase;
    }),
    [releases, search, phaseFilter]
  );

  if (error) return <Alert type="error" message="Failed to load releases" description={(error as Error).message} />;

  return (
    <div>
      <div style={{ marginBottom: 20 }}>
        <Title level={3} style={{ marginBottom: 12 }}>Releases</Title>
        <StatusBar counts={phaseCounts} />
      </div>

      <Space style={{ marginBottom: 16 }} wrap>
        <Input
          prefix={<SearchOutlined />}
          placeholder="Search releases…"
          value={search}
          onChange={e => setSearch(e.target.value)}
          style={{ width: 240 }}
          allowClear
        />
        {PHASE_FILTER_OPTIONS.map(p => (
          <Tag
            key={p}
            color={phaseFilter === p ? '#7c3aed' : undefined}
            style={{ cursor: 'pointer', userSelect: 'none' }}
            onClick={() => setPhaseFilter(p)}
          >
            {p}
          </Tag>
        ))}
      </Space>

      {!isLoading && filtered.length === 0 && <Empty description="No releases found" />}

      <Row gutter={[16, 16]}>
        {filtered.map(r => (
          <Col key={r.metadata.name} xs={24} sm={24} md={12} lg={8}>
            <ReleaseTile release={r} onClick={() => navigate(`/releases/${r.metadata.name}`)} />
          </Col>
        ))}
      </Row>
    </div>
  );
};
