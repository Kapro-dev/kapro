// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useState, useCallback, useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert, Badge, Button, Form, Input, Modal, Space, Table, Tag, Typography, message,
} from 'antd';
import { BellOutlined, WarningOutlined } from '@ant-design/icons';
import { listResource, createApproval, watchResource } from '../../api/k8s';
import { StatusBadge } from '../common/StatusBadge';
import type { Promotion, BatchRun } from '../../gen/types/kapro';
import dayjs from 'dayjs';
import relativeTime from 'dayjs/plugin/relativeTime';

dayjs.extend(relativeTime);

const { Title, Text } = Typography;

type GateStatus = {
  soakRemaining?: string;
  metricsPass?: boolean;
  healthStatus?: string;
};

type PendingItem = {
  key: string;
  kind: 'Promotion' | 'Batch';
  name: string;
  namespace: string;
  environmentRef: string;
  releaseRef: string;
  version?: string;
  phase: string;
  gateStatus?: GateStatus;
  conditions?: { type: string; status: string; message?: string }[];
  creationTimestamp?: string;
};

interface ApprovalModalProps {
  open: boolean;
  item: PendingItem | null;
  bypass: boolean;
  onConfirm: (approvedBy: string, comment: string) => Promise<void>;
  onCancel: () => void;
}

const ApprovalModal: React.FC<ApprovalModalProps> = ({ open, item, bypass, onConfirm, onCancel }) => {
  const [form] = Form.useForm<{ approvedBy: string; comment: string }>();
  const [loading, setLoading] = useState(false);

  const handleOk = async () => {
    const vals = await form.validateFields();
    setLoading(true);
    try {
      await onConfirm(vals.approvedBy, vals.comment ?? '');
      form.resetFields();
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      open={open}
      title={
        bypass ? (
          <span style={{ color: '#dc2626' }}><WarningOutlined /> Bypass All Gates — P0 Hotfix Only</span>
        ) : (
          <span>Approve {item?.kind}: {item?.name}</span>
        )
      }
      onOk={handleOk}
      onCancel={onCancel}
      confirmLoading={loading}
      okText={bypass ? 'Bypass (P0 Only)' : 'Approve'}
      okButtonProps={{ danger: bypass }}
    >
      {bypass && (
        <Alert
          type="warning"
          message="⚠️ This bypasses all gates — soak time, metrics, and health checks. Use only for P0 hotfixes."
          style={{ marginBottom: 16 }}
        />
      )}
      <Form form={form} layout="vertical">
        <Form.Item label="Your name" name="approvedBy" rules={[{ required: true, message: 'Name required' }]}>
          <Input placeholder="e.g. alice@example.com" />
        </Form.Item>
        <Form.Item label="Comment (optional)" name="comment">
          <Input.TextArea rows={2} placeholder="Reason for approval…" />
        </Form.Item>
      </Form>
    </Modal>
  );
};

export const ApprovalInboxPage: React.FC = () => {
  const qc = useQueryClient();
  const [modal, setModal] = useState<{ item: PendingItem; bypass: boolean } | null>(null);

  // Live watch — promotions advance without requiring a manual refresh
  useEffect(() => {
    const ctrl = new AbortController();
    watchResource<Promotion>('promotions', () => {
      qc.invalidateQueries({ queryKey: ['promotions'] });
    }, ctrl.signal);
    return () => ctrl.abort();
  }, [qc]);

  const { data: promotions = [], error: promError } = useQuery({
    queryKey: ['promotions', 'waiting'],
    queryFn: () => listResource<Promotion>('promotions'),
    select: (all) => all.filter(p => p.status?.phase === 'WaitingApproval'),
    refetchInterval: 5_000,
  });

  const { data: batchRuns = [], error: batchError } = useQuery({
    queryKey: ['batchruns', 'waiting'],
    queryFn: () => listResource<BatchRun>('batchruns'),
    select: (all) => all.filter(b => b.status?.phase === 'WaitingApproval'),
    refetchInterval: 5_000,
  });

  const pendingItems: PendingItem[] = [
    ...promotions.map(p => ({
      key: `prom-${p.metadata.name}`,
      kind: 'Promotion' as const,
      name: p.metadata.name,
      namespace: p.metadata.namespace ?? 'default',
      environmentRef: p.spec.environmentRef,
      releaseRef: p.spec.releaseRef,
      version: p.spec.version,
      phase: p.status?.phase ?? 'WaitingApproval',
      gateStatus: p.status?.gateStatus,
      conditions: p.status?.conditions,
      creationTimestamp: p.metadata.creationTimestamp,
    })),
    ...batchRuns.map(b => ({
      key: `batch-${b.metadata.name}`,
      kind: 'Batch' as const,
      name: b.metadata.name,
      namespace: b.metadata.namespace ?? 'default',
      environmentRef: '',
      releaseRef: b.spec.releaseRef,
      phase: b.status?.phase ?? 'WaitingApproval',
      gateStatus: undefined,
      conditions: b.status?.conditions,
      creationTimestamp: b.metadata.creationTimestamp,
    })),
  ];

  const handleConfirm = useCallback(async (
    item: PendingItem,
    bypass: boolean,
    approvedBy: string,
    comment: string,
  ) => {
    await createApproval({
      name: `approval-${item.environmentRef || item.name}-${Date.now()}`,
      namespace: '',
      kind: item.kind,
      environmentRef: item.environmentRef || item.name,
      release: item.releaseRef,
      approvedBy,
      bypass,
      comment,
    });
    message.success(`${bypass ? 'Bypassed' : 'Approved'} successfully`);
    qc.invalidateQueries({ queryKey: ['promotions'] });
    qc.invalidateQueries({ queryKey: ['batchruns'] });
    setModal(null);
  }, [qc]);

  const columns = [
    {
      title: 'Environment',
      key: 'env',
      render: (_: unknown, r: PendingItem) => (
        <Space>
          <Text>{r.environmentRef || r.name}</Text>
        </Space>
      ),
    },
    {
      title: 'Release',
      key: 'release',
      render: (_: unknown, r: PendingItem) => (
        <Space>
          <Text strong>{r.releaseRef}</Text>
          {r.version && <Tag style={{ fontSize: 11 }}>{r.version}</Tag>}
        </Space>
      ),
    },
    {
      title: 'Kind',
      dataIndex: 'kind',
      key: 'kind',
      render: (k: string) => <Tag color={k === 'Promotion' ? 'blue' : 'purple'}>{k}</Tag>,
    },
    {
      title: 'Gate Status',
      key: 'gate',
      render: (_: unknown, r: PendingItem) => {
        const g = r.gateStatus;
        if (!g) return <StatusBadge phase={r.phase} />;
        return (
          <Space size={4} wrap>
            <StatusBadge phase={r.phase} />
            {g.soakRemaining && <Tag color="orange">Soak: {g.soakRemaining}</Tag>}
            {g.metricsPass !== undefined && <Tag color={g.metricsPass ? 'green' : 'red'}>Metrics</Tag>}
            {g.healthStatus && <Tag>{g.healthStatus}</Tag>}
          </Space>
        );
      },
    },
    {
      title: 'Waiting Since',
      key: 'since',
      render: (_: unknown, r: PendingItem) =>
        r.creationTimestamp ? dayjs(r.creationTimestamp).fromNow() : '—',
    },
    {
      title: 'Actions',
      key: 'actions',
      render: (_: unknown, r: PendingItem) => (
        <Space>
          <Button
            type="primary"
            size="small"
            style={{ background: '#16a34a', borderColor: '#16a34a' }}
            onClick={() => setModal({ item: r, bypass: false })}
          >
            Approve
          </Button>
          <Button
            danger
            size="small"
            onClick={() => setModal({ item: r, bypass: true })}
          >
            Bypass
          </Button>
        </Space>
      ),
    },
  ];

  const err = promError || batchError;
  if (err) return <Alert type="error" message="Failed to load approvals" description={(err as Error).message} />;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
        <Title level={3} style={{ margin: 0 }}>Approval Inbox</Title>
        <Badge count={pendingItems.length} style={{ background: pendingItems.length > 0 ? '#d97706' : '#9ca3af' }}>
          <BellOutlined style={{ fontSize: 20 }} />
        </Badge>
      </div>

      <Table<PendingItem>
        dataSource={pendingItems}
        columns={columns}
        rowKey="key"
        size="middle"
        locale={{ emptyText: <div style={{ padding: 32, textAlign: 'center' }}>✅ No pending approvals</div> }}
        pagination={{ pageSize: 20 }}
      />

      <ApprovalModal
        open={!!modal}
        item={modal?.item ?? null}
        bypass={modal?.bypass ?? false}
        onConfirm={async (approvedBy, comment) => {
          if (modal) await handleConfirm(modal.item, modal.bypass, approvedBy, comment);
        }}
        onCancel={() => setModal(null)}
      />
    </div>
  );
};
