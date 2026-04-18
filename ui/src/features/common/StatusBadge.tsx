// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React from 'react';
import { Tag } from 'antd';
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  SyncOutlined,
  ClockCircleOutlined,
  BellOutlined,
  LoadingOutlined,
} from '@ant-design/icons';

type AnyPhase =
  | 'Pending'
  | 'Promoting'
  | 'Progressing'
  | 'Complete'
  | 'Failed'
  | 'WaitingApproval'
  | 'Resolving'
  | 'WaitingPromotions'
  | 'GateCheck'
  | 'Applying'
  | 'Converging'
  | 'Converged'
  | string;

interface Props {
  phase: AnyPhase | undefined;
}

const phaseConfig: Record<string, { color: string; icon?: React.ReactNode }> = {
  Pending:           { color: 'default', icon: <ClockCircleOutlined /> },
  Promoting:         { color: 'processing', icon: <SyncOutlined spin /> },
  Progressing:       { color: 'processing', icon: <LoadingOutlined spin /> },
  Complete:          { color: 'success', icon: <CheckCircleOutlined /> },
  Converged:         { color: 'success', icon: <CheckCircleOutlined /> },
  Failed:            { color: 'error', icon: <CloseCircleOutlined /> },
  Resolving:         { color: 'processing', icon: <SyncOutlined spin /> },
  WaitingPromotions: { color: 'warning', icon: <ClockCircleOutlined /> },
  GateCheck:         { color: 'warning', icon: <SyncOutlined spin /> },
  WaitingApproval:   { color: 'warning', icon: <BellOutlined /> },
  Applying:          { color: 'processing', icon: <SyncOutlined spin /> },
  Converging:        { color: 'warning', icon: <SyncOutlined spin /> },
};

export const StatusBadge: React.FC<Props> = ({ phase }) => {
  const cfg = phaseConfig[phase ?? 'Pending'] ?? { color: 'default' };
  return (
    <Tag color={cfg.color} icon={cfg.icon} style={{ fontSize: 12 }}>
      {phase ?? 'Unknown'}
    </Tag>
  );
};
