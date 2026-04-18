import React from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConfigProvider, theme } from 'antd';
import { AppLayout } from './features/common/AppLayout';
import { ReleasesPage } from './features/release/ReleasesPage';
import { ReleasePage } from './features/release/ReleasePage';
import { WorldGridPage } from './features/world-grid/WorldGridPage';
import { ApprovalInboxPage } from './features/approval-inbox/ApprovalInboxPage';
import { EnvironmentsPage } from './features/environment/EnvironmentsPage';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchInterval: 10_000,
      staleTime: 5_000,
      retry: 5,
      retryDelay: attemptIndex => Math.min(1000 * 2 ** attemptIndex, 15_000),
    },
  },
});

const kaproTheme = {
  token: {
    colorPrimary: '#7c3aed',
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
            <Route element={<AppLayout />}>
              <Route path="/releases" element={<ReleasesPage />} />
              <Route path="/releases/:name" element={<ReleasePage />} />
              <Route path="/world" element={<WorldGridPage />} />
              <Route path="/approvals" element={<ApprovalInboxPage />} />
              <Route path="/environments" element={<EnvironmentsPage />} />
            </Route>
            <Route path="*" element={<Navigate to="/releases" replace />} />
          </Routes>
        </BrowserRouter>
      </ConfigProvider>
    </QueryClientProvider>
  );
}
