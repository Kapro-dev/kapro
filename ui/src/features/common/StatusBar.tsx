// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React from 'react';
import { Tooltip } from 'antd';

const phaseColors: Record<string, string> = {
  Complete:          '#16a34a',
  Converged:         '#16a34a',
  Progressing:       '#2563eb',
  Promoting:         '#2563eb',
  Applying:          '#2563eb',
  Resolving:         '#2563eb',
  WaitingApproval:   '#d97706',
  WaitingPromotions: '#d97706',
  GateCheck:         '#d97706',
  Converging:        '#d97706',
  Failed:            '#dc2626',
  Pending:           '#6b7280',
};

interface Props {
  counts: Record<string, number>;
}

export const StatusBar: React.FC<Props> = ({ counts }) => {
  const total = Object.values(counts).reduce((a, b) => a + b, 0);
  if (total === 0) return null;

  const entries = Object.entries(counts).filter(([, n]) => n > 0);

  return (
    <div>
      {/* Segmented bar */}
      <div style={{ display: 'flex', height: 8, borderRadius: 4, overflow: 'hidden', background: '#e5e7eb' }}>
        {entries.map(([phase, count]) => (
          <Tooltip key={phase} title={`${count} ${phase}`}>
            <div
              style={{
                width: `${(count / total) * 100}%`,
                background: phaseColors[phase] ?? '#6b7280',
                cursor: 'pointer',
                transition: 'width 0.3s',
              }}
            />
          </Tooltip>
        ))}
      </div>
      {/* Count badges row */}
      <div style={{ display: 'flex', gap: 16, marginTop: 6, flexWrap: 'wrap' }}>
        {entries.map(([phase, count]) => (
          <span key={phase} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 12, color: '#6b7280' }}>
            <span
              style={{
                display: 'inline-block',
                width: 8,
                height: 8,
                borderRadius: '50%',
                background: phaseColors[phase] ?? '#6b7280',
              }}
            />
            <strong style={{ color: '#374151' }}>{count}</strong> {phase}
          </span>
        ))}
      </div>
    </div>
  );
};
