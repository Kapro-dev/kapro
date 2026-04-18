// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useState, useCallback } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  Alert, Button, Card, Col, Form, Input, Modal, Row,
  Space, Spin, Tag, Typography, message, Empty,
} from 'antd';
import {
  ArrowLeftOutlined, ArrowRightOutlined, CheckCircleOutlined, WarningOutlined,
} from '@ant-design/icons';
import { useRelease, usePipeline, usePromotions, useEnvironments } from './hooks';
import { createApproval } from '../../api/k8s';
import type { Promotion, Environment, Pipeline } from '../../gen/types/kapro';
import { useQueryClient } from '@tanstack/react-query';
import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';

dayjs.extend(relativeTime);

const { Text, Title } = Typography;

// ─── Phase colours ────────────────────────────────────────────────────────────
const PHASE_COLOR: Record<string, { bg: string; border: string; fg: string }> = {
  Converged:       { bg: '#f0fdf4', border: '#18BE94', fg: '#166534' },
  Complete:        { bg: '#f0fdf4', border: '#18BE94', fg: '#166534' },
  Applying:        { bg: '#eff6ff', border: '#3b82f6', fg: '#1e40af' },
  HealthCheck:     { bg: '#fdf4ff', border: '#a855f7', fg: '#6b21a8' },
  Soaking:         { bg: '#fff7ed', border: '#f97316', fg: '#9a3412' },
  MetricsCheck:    { bg: '#fdf4ff', border: '#a855f7', fg: '#6b21a8' },
  WaitingApproval: { bg: '#fffbeb', border: '#f59e0b', fg: '#92400e' },
  Failed:          { bg: '#fef2f2', border: '#ef4444', fg: '#991b1b' },
  Pending:         { bg: '#f9fafb', border: '#d1d5db', fg: '#6b7280' },
};

const RELEASE_PHASE_COLOR: Record<string, string> = {
  Promoting: '#3b82f6', Progressing: '#a855f7', Complete: '#18BE94',
  Failed: '#ef4444', Pending: '#6b7280',
};

const FLAGS: Record<string, string> = {
  de: '🇩🇪', fi: '🇫🇮', fr: '🇫🇷', es: '🇪🇸', pt: '🇵🇹',
  it: '🇮🇹', pl: '🇵🇱', cz: '🇨🇿', at: '🇦🇹', se: '🇸🇪',
  no: '🇳🇴', dk: '🇩🇰', nl: '🇳🇱', be: '🇧🇪', sk: '🇸🇰',
};

function flag(country: string) {
  return FLAGS[country] ?? '🌍';
}

function matchLabels(envLabels: Record<string, string>, selector: Record<string, string>) {
  return Object.entries(selector).every(([k, v]) => envLabels[k] === v);
}

// ─── Stage node: one cluster inside a country pipeline ───────────────────────
function StageNode({
  promotion, stageName, onApprove,
}: {
  promotion: Promotion | undefined;
  stageName: string;
  onApprove: (p: Promotion) => void;
}) {
  if (!promotion) {
    return (
      <div style={{
        border: '2px dashed #e5e7eb',
        borderRadius: 8,
        padding: '10px 14px',
        minWidth: 130,
        background: '#f9fafb',
        textAlign: 'center',
      }}>
        <Text type="secondary" style={{ fontSize: 11 }}>{stageName}</Text>
        <div><Text type="secondary" style={{ fontSize: 10 }}>no cluster</Text></div>
      </div>
    );
  }

  const phase = promotion.status?.phase ?? 'Pending';
  const c = PHASE_COLOR[phase] ?? PHASE_COLOR.Pending;
  const waiting = phase === 'WaitingApproval';

  return (
    <div style={{
      border: `2px solid ${c.border}`,
      borderRadius: 8,
      background: c.bg,
      padding: '10px 14px',
      minWidth: 130,
      boxShadow: waiting ? `0 0 0 3px ${c.border}40` : undefined,
      transition: 'box-shadow 0.3s',
    }}>
      <Text style={{ fontSize: 11, fontWeight: 600, color: '#374151', display: 'block' }}>
        {stageName}
      </Text>
      <Text style={{ fontSize: 12, color: '#6b7280', display: 'block', marginBottom: 4 }}>
        {promotion.spec.environmentRef}
      </Text>
      <Tag style={{
        fontSize: 10, padding: '0 5px', lineHeight: '16px',
        background: c.bg, borderColor: c.border, color: c.fg,
      }}>
        {phase}
      </Tag>
      {waiting && (
        <Button
          type="primary" size="small" icon={<CheckCircleOutlined />}
          onClick={() => onApprove(promotion)}
          style={{ marginTop: 8, width: '100%', background: '#18BE94', borderColor: '#18BE94', fontSize: 11 }}
        >
          Approve
        </Button>
      )}
    </div>
  );
}

// ─── One country pipeline card ────────────────────────────────────────────────
function CountryPipelineCard({
  country,
  stages,       // ordered: [{name, promotion}]
  onApprove,
}: {
  country: string;
  stages: { name: string; promotion: Promotion | undefined }[];
  onApprove: (p: Promotion) => void;
}) {
  const allConverged = stages.every(s =>
    s.promotion && ['Converged', 'Complete'].includes(s.promotion.status?.phase as string)
  );
  const anyFailed   = stages.some(s => s.promotion?.status?.phase === 'Failed');
  const anyWaiting  = stages.some(s => s.promotion?.status?.phase === 'WaitingApproval');
  const anyActive   = stages.some(s => s.promotion?.status?.phase &&
    !['Pending','Converged','Complete','Failed'].includes(s.promotion.status.phase as string)
  );

  const borderColor = anyFailed ? '#ef4444'
    : anyWaiting    ? '#f59e0b'
    : anyActive     ? '#3b82f6'
    : allConverged  ? '#18BE94'
    : '#e5e7eb';

  return (
    <div style={{
      border: `2px solid ${borderColor}`,
      borderRadius: 10,
      background: '#fff',
      padding: '10px 14px 14px',
      boxShadow: '0 2px 8px rgba(0,0,0,0.06)',
      minWidth: 280,
    }}>
      {/* Country header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
        <span style={{ fontSize: 24 }}>{flag(country)}</span>
        <Text style={{ fontWeight: 700, fontSize: 15, textTransform: 'uppercase', letterSpacing: 0.5 }}>
          {country}
        </Text>
        {allConverged && <Tag color="green" style={{ marginLeft: 4 }}>done</Tag>}
        {anyFailed    && <Tag color="red"   style={{ marginLeft: 4 }}>failed</Tag>}
      </div>

      {/* Stage flow: dev → arrow → prod → ... */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 0 }}>
        {stages.map((s, i) => (
          <React.Fragment key={s.name}>
            {i > 0 && (
              <div style={{ padding: '0 8px', color: '#9ca3af', display: 'flex', alignItems: 'center' }}>
                <ArrowRightOutlined style={{ fontSize: 16 }} />
              </div>
            )}
            <StageNode stageName={s.name} promotion={s.promotion} onApprove={onApprove} />
          </React.Fragment>
        ))}
      </div>
    </div>
  );
}

// ─── Batch group section ──────────────────────────────────────────────────────
function BatchGroup({
  name, cards,
}: {
  name: string;
  cards: React.ReactNode;
}) {
  return (
    <div style={{ marginBottom: 28 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12,
      }}>
        <div style={{ height: 2, width: 20, background: '#d1d5db' }} />
        <Text style={{
          fontSize: 11, fontWeight: 700, textTransform: 'uppercase',
          letterSpacing: 1, color: '#9ca3af',
        }}>
          {name}
        </Text>
        <div style={{ flex: 1, height: 1, background: '#e5e7eb' }} />
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16 }}>
        {cards}
      </div>
    </div>
  );
}

// ─── Main page ────────────────────────────────────────────────────────────────
export const ReleasePage: React.FC = () => {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const { data: release, isLoading, error } = useRelease(name ?? '');
  const { data: pipeline } = usePipeline(release?.spec.pipelineRef);
  const { data: promotions = [] } = usePromotions(name);
  const { data: environments = [] } = useEnvironments();

  const [approveTarget, setApproveTarget] = useState<Promotion | null>(null);
  const [form] = Form.useForm<{ approvedBy: string; comment: string }>();
  const [approving, setApproving] = useState(false);

  const envMap = React.useMemo(() => {
    const m = new Map<string, Environment>();
    for (const e of environments) m.set(e.metadata.name, e);
    return m;
  }, [environments]);

  const handleApprove = useCallback(async () => {
    if (!approveTarget || !name) return;
    const vals = await form.validateFields();
    setApproving(true);
    try {
      await createApproval({
        name: `approval-${approveTarget.spec.environmentRef}-${Date.now()}`,
        namespace: '',
        kind: 'Promotion',
        environmentRef: approveTarget.spec.environmentRef,
        release: name,
        approvedBy: vals.approvedBy,
        bypass: false,
        comment: vals.comment ?? '',
      });
      message.success(`✅ Approved ${approveTarget.spec.environmentRef}`);
      qc.invalidateQueries({ queryKey: ['promotions'] });
      setApproveTarget(null);
      form.resetFields();
    } catch (e) {
      message.error(`Failed: ${(e as Error).message}`);
    } finally {
      setApproving(false);
    }
  }, [approveTarget, name, form, qc]);

  if (isLoading) return <Spin size="large" style={{ display: 'block', margin: '80px auto' }} />;
  if (error) return <Alert type="error" message="Failed to load release" description={(error as Error).message} />;
  if (!release) return <Empty description="Release not found" />;

  const phase = release.status?.phase ?? 'Pending';
  const phaseColor = RELEASE_PHASE_COLOR[phase] ?? '#6b7280';
  const waitingCount = promotions.filter(p => p.status?.phase === 'WaitingApproval').length;

  // Pipeline steps define the stage order inside each country pipeline
  const promotionSteps = pipeline?.spec.promotion?.steps ?? [
    { name: 'dev', selector: { matchLabels: { tier: 'dev' } }, policy: undefined, dependsOn: [] },
    { name: 'prod', selector: { matchLabels: { tier: 'prod' } }, policy: undefined, dependsOn: ['dev'] },
  ];

  // Extract unique countries from all promotions
  const countries = Array.from(
    new Set(promotions.map(p => p.spec.environmentRef.split('-')[0]))
  ).sort();

  // For each country + each stage, find the matching promotion
  function getCountryStagePromotion(country: string, step: { name: string; selector?: { matchLabels?: Record<string, string> } }) {
    return promotions.find(p => {
      if (!p.spec.environmentRef.startsWith(country + '-')) return false;
      if (!step.selector?.matchLabels || Object.keys(step.selector.matchLabels).length === 0) {
        return p.spec.environmentRef === `${country}-${step.name}`;
      }
      const env = envMap.get(p.spec.environmentRef);
      if (!env) return p.spec.environmentRef === `${country}-${step.name}`;
      return matchLabels(env.metadata.labels ?? {}, step.selector.matchLabels);
    });
  }

  // Group countries by progression batch (using prod env labels + batch selectors)
  const progressionBatches = pipeline?.spec.progression?.batches ?? [];

  function getCountryBatch(country: string): string {
    const prodEnvName = `${country}-prod`;
    const env = envMap.get(prodEnvName);
    if (!env) return 'ungrouped';
    const labels = env.metadata.labels ?? {};
    for (const batch of progressionBatches) {
      if (batch.selectors?.some(sel => matchLabels(labels, sel.matchLabels ?? {}))) {
        return batch.name;
      }
    }
    return 'ungrouped';
  }

  // Group countries: batches first (in order), then ungrouped
  const batchNames = progressionBatches.length > 0
    ? [...progressionBatches.map(b => b.name), 'ungrouped']
    : ['ungrouped'];

  const grouped: Record<string, string[]> = {};
  for (const bn of batchNames) grouped[bn] = [];
  for (const c of countries) {
    const bn = getCountryBatch(c);
    if (!grouped[bn]) grouped[bn] = [];
    grouped[bn].push(c);
  }

  return (
    <div style={{ padding: '0 4px' }}>
      {/* Back button */}
      <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/releases')} style={{ marginBottom: 16 }}>
        Back to Releases
      </Button>

      {/* Release header card */}
      <Card
        style={{ marginBottom: 24, borderLeft: `4px solid ${phaseColor}`, borderRadius: 10 }}
        bodyStyle={{ padding: '12px 20px' }}
      >
        <Row align="middle" gutter={24}>
          <Col>
            <Title level={4} style={{ margin: 0 }}>{release.metadata.name}</Title>
            <Text type="secondary" style={{ fontSize: 12 }}>
              artifact: {release.spec.artifact} &nbsp;·&nbsp;
              pipeline: {release.spec.pipelineRef}
            </Text>
          </Col>
          <Col>
            <Tag color={phaseColor} style={{ fontSize: 14, padding: '2px 12px' }}>{phase}</Tag>
          </Col>
          <Col>
            <Text type="secondary" style={{ fontSize: 12 }}>
              {release.metadata.creationTimestamp
                ? dayjs(release.metadata.creationTimestamp).fromNow()
                : ''}
            </Text>
          </Col>
          <Col flex="auto" style={{ textAlign: 'right' }}>
            <Space>
              <Tag>
                {promotions.filter(p =>
                  ['Converged', 'Complete'].includes(p.status?.phase as string)
                ).length}/{promotions.length} converged
              </Tag>
              {waitingCount > 0 && (
                <Tag icon={<WarningOutlined />} color="warning">{waitingCount} waiting approval</Tag>
              )}
            </Space>
          </Col>
        </Row>
      </Card>

      {/* Country pipelines grouped by progression batch */}
      <div>
        {batchNames.map(batchName => {
          const batchCountries = grouped[batchName] ?? [];
          if (batchCountries.length === 0) return null;

          const displayName = batchName === 'ungrouped'
            ? 'unassigned (no batch)'
            : `Batch: ${batchName}`;

          const batchDeps = progressionBatches.find(b => b.name === batchName)?.dependsOn ?? [];

          return (
            <BatchGroup
              key={batchName}
              name={batchDeps.length > 0 ? `${displayName} — after ${batchDeps.join(', ')}` : displayName}
              cards={batchCountries.map(country => (
                <CountryPipelineCard
                  key={country}
                  country={country}
                  stages={promotionSteps.map(step => ({
                    name: step.name,
                    promotion: getCountryStagePromotion(country, step),
                  }))}
                  onApprove={setApproveTarget}
                />
              ))}
            />
          );
        })}
        {countries.length === 0 && (
          <Empty description="No promotions found for this release" />
        )}
      </div>

      {/* Approve Modal */}
      <Modal
        open={!!approveTarget}
        title={
          <span>
            <CheckCircleOutlined style={{ color: '#18BE94', marginRight: 8 }} />
            Approve: {approveTarget?.spec.environmentRef}
          </span>
        }
        onOk={handleApprove}
        onCancel={() => { setApproveTarget(null); form.resetFields(); }}
        confirmLoading={approving}
        okText="Approve"
        okButtonProps={{ style: { background: '#18BE94', borderColor: '#18BE94' } }}
      >
        <Form form={form} layout="vertical" style={{ marginTop: 16 }}>
          <Form.Item label="Your name" name="approvedBy" rules={[{ required: true, message: 'Required' }]}>
            <Input placeholder="e.g. vinayaka" autoFocus />
          </Form.Item>
          <Form.Item label="Comment" name="comment">
            <Input.TextArea rows={2} placeholder="Reason for approval…" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
};
