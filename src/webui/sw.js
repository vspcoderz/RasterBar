'use strict';

// Network-only on purpose.
//
// The server is on loopback and either running or not; caching the shell would
// buy nothing and would serve a UI from a previous binary after an update, which
// is the one failure mode a local app cannot explain. The fetch handler exists
// because Chromium requires one before it offers to install the manifest.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return;
  const url = new URL(event.request.url);
  if (url.origin !== location.origin) return;
  event.respondWith(
    fetch(event.request).catch(() => new Response(
      'RasterBar is not running. Start it with `rasterbar app`.',
      { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } },
    )),
  );
});
