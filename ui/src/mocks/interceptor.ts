// Fetch interceptor — only active in dev (import.meta.env.DEV)
// Intercepts /apis/kapro.io/v1alpha1/<resource> calls and returns mock data.
import { MOCK_DB } from './data';

const API_PREFIX = '/apis/kapro.io/v1alpha1/';

const origFetch = window.fetch.bind(window);

window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;

  // Only intercept kapro API calls (skip watch requests — they'd hang)
  if (url.startsWith(API_PREFIX) && !url.includes('?watch=true')) {
    const path = url.slice(API_PREFIX.length).split('?')[0];  // e.g. "releases" or "releases/foo"
    const parts = path.split('/');
    const resource = parts[0];
    const name = parts[1];

    const db = MOCK_DB[resource];
    if (db !== undefined) {
      if (name) {
        // Single resource GET
        const item = db.find((r: any) => r.metadata?.name === name);
        if (item) {
          return new Response(JSON.stringify(item), { status: 200, headers: { 'Content-Type': 'application/json' } });
        }
        return new Response(JSON.stringify({ message: `${resource}/${name} not found` }), { status: 404 });
      }
      // List
      return new Response(JSON.stringify({ items: db }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
  }

  // POST to create approval — swallow silently in mock mode
  if (url.includes('/approvals') && init?.method === 'POST') {
    return new Response('{}', { status: 201, headers: { 'Content-Type': 'application/json' } });
  }

  // Pass-through everything else
  return origFetch(input, init);
};

// Silence watch calls — return a never-ending stream that just closes immediately
const origFetchForWatch = window.fetch.bind(window);
window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  if (url.includes('?watch=true')) {
    // Return an empty readable stream so the watcher just silently stops
    const stream = new ReadableStream({ start(c) { c.close(); } });
    return new Response(stream, { status: 200 });
  }
  return origFetchForWatch(input, init);
};
