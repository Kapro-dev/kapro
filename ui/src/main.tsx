import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './App';

// Mock interceptor — set VITE_USE_MOCK=true to enable
if (import.meta.env.DEV && import.meta.env.VITE_USE_MOCK === 'true') {
  await import('./mocks/interceptor');
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
