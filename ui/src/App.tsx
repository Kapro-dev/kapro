import React from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConfigProvider, theme } from 'antd';

// Pages (features)
// import { ReleasesPage } from './features/release/ReleasesPage';
// import { EnvironmentsPage } from './features/environment/EnvironmentsPage';
// import { WorldGridPage } from './features/world-grid/WorldGridPage';
// import { ApprovalInboxPage } from './features/approval-inbox/ApprovalInboxPage';
// import { AppLayout } from './features/common/AppLayout';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchInterval: 10_000, // poll every 10s — K8s watch preferred but fallback
      staleTime: 5_000,
    },
  },
});

const kaproTheme = {
  token: {
    colorPrimary: '#7c3aed',   // Kapro purple — kangaroo brand colour
    colorSuccess: '#16a34a',
    colorWarning: '#d97706',
    colorError: '#dc2626',
    borderRadius: 6,
    fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, sans-serif",
  },
};

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ConfigProvider theme={{ ...kaproTheme, algorithm: theme.defaultAlgorithm }}>
        <BrowserRouter>
          <Routes>
            <Route path="/" element={<Navigate to="/releases" replace />} />
            {/* Routes wired as features are built:
            <Route element={<AppLayout />}>
              <Route path="/releases" element={<ReleasesPage />} />
              <Route path="/releases/:name" element={<ReleasePage />} />
              <Route path="/environments" element={<EnvironmentsPage />} />
              <Route path="/world" element={<WorldGridPage />} />
              <Route path="/approvals" element={<ApprovalInboxPage />} />
            </Route>
            */}
            <Route
              path="*"
              element={
                <div style={{ padding: 40, textAlign: 'center' }}>
                  <h1>🦘 Kapro</h1>
                  <p>UI scaffolded — features coming soon.</p>
                </div>
              }
            />
          </Routes>
        </BrowserRouter>
      </ConfigProvider>
    </QueryClientProvider>
  );
}
