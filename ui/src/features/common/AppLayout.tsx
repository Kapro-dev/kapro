// UI patterns inspired by ArgoCD (Apache 2.0) — https://github.com/argoproj/argo-cd
import React, { useState } from 'react';
import { Layout, Menu, Badge } from 'antd';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  RocketOutlined,
  GlobalOutlined,
  CheckCircleOutlined,
  ClusterOutlined,
  BellOutlined,
} from '@ant-design/icons';
import { listResource } from '../../api/k8s';
import type { Promotion, BatchRun } from '../../gen/types/kapro';

const { Sider, Content } = Layout;

function useApprovalBadgeCount() {
  const { data: promotions = [] } = useQuery({
    queryKey: ['promotions', 'waiting'],
    queryFn: () => listResource<Promotion>('promotions'),
    select: (all) => all.filter(p => p.status?.phase === 'WaitingApproval'),
    refetchInterval: 10_000,
  });
  const { data: batchRuns = [] } = useQuery({
    queryKey: ['batchruns', 'waiting'],
    queryFn: () => listResource<BatchRun>('batchruns'),
    select: (all) => all.filter(b => b.status?.phase === 'WaitingApproval'),
    refetchInterval: 10_000,
  });
  return promotions.length + batchRuns.length;
}

export const AppLayout: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const [collapsed, setCollapsed] = useState(false);
  const approvalCount = useApprovalBadgeCount();

  const navItems = [
    { key: '/releases',     icon: <RocketOutlined />,   label: 'Releases' },
    { key: '/world',        icon: <GlobalOutlined />,    label: 'Cluster Grid' },
    {
      key: '/approvals',
      icon: (
        <Badge count={approvalCount} size="small" offset={[4, -4]} style={{ background: '#d97706' }}>
          <BellOutlined />
        </Badge>
      ),
      label: (
        <span style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          Approvals
          {approvalCount > 0 && (
            <Badge count={approvalCount} size="small" style={{ background: '#d97706', marginLeft: 8 }} />
          )}
        </span>
      ),
    },
    { key: '/environments', icon: <ClusterOutlined />, label: 'Environments' },
  ];

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider
        collapsible
        collapsed={collapsed}
        onCollapse={setCollapsed}
        style={{ background: '#1a1a2e', position: 'fixed', height: '100vh', left: 0, top: 0, zIndex: 100 }}
        width={220}
      >
        {/* Logo */}
        <div
          style={{
            padding: collapsed ? '20px 8px' : '20px 24px',
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            borderBottom: '1px solid #2d2d4e',
            marginBottom: 8,
            cursor: 'pointer',
            transition: 'padding 0.2s',
          }}
          onClick={() => navigate('/releases')}
        >
          <span style={{ fontSize: 24 }}>🦘</span>
          {!collapsed && (
            <span style={{ color: '#a78bfa', fontWeight: 700, fontSize: 18, letterSpacing: '-0.3px' }}>
              Kapro
            </span>
          )}
        </div>

        <Menu
          mode="inline"
          selectedKeys={[location.pathname.split('/').slice(0, 2).join('/')]}
          items={navItems}
          onClick={({ key }) => navigate(key)}
          style={{ background: '#1a1a2e', border: 'none' }}
          theme="dark"
        />
      </Sider>

      <Layout style={{ marginLeft: collapsed ? 80 : 220, transition: 'margin-left 0.2s' }}>
        <Content style={{ padding: 24, background: '#f9fafb', minHeight: '100vh' }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
};
