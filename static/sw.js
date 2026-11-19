// Service worker. It does two small jobs: show generic push alerts, and show an offline page.
// It never stores chats, messages or API responses.
const BUILD = '__BUILD__';
const SITE = __SITE__;
const CACHE = 'shell-' + BUILD;
const SHELL = ['/offline', '/static/style.css?v=' + BUILD, '/static/pages.css?v=' + BUILD, '/favicon.svg'];
const APPLE = /iPhone|iPad|iPod|Macintosh/.test(self.navigator.userAgent);
const TEXT = {
  request: ['New chat request', 'Someone wants to chat. Open ' + SITE + ' to see it.'],
  message: ['New message', 'Open ' + SITE + ' to read it.'],
  accepted: ['Request accepted', 'Your chat request was accepted. Open ' + SITE + ' to continue.'],
  reveal: ['Contact details ready', 'You both agreed to share. Open ' + SITE + ' to see them.']
};

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil((async () => {
    for (const k of await caches.keys()) if (k !== CACHE) await caches.delete(k);
    await self.clients.claim();
  })());
});

// Pages always come from the network. Only when that fails does the offline page appear.
self.addEventListener('fetch', (e) => {
  const r = e.request;
  if (r.method !== 'GET') return;
  const u = new URL(r.url);
  if (u.origin !== self.location.origin) return;
  if (r.mode === 'navigate') {
    e.respondWith(fetch(r).catch(() => caches.match('/offline').then((x) => x || Response.error())));
    return;
  }
  if (SHELL.indexOf(u.pathname + u.search) > -1) e.respondWith(caches.match(r).then((x) => x || fetch(r)));
});

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch (err) {}
  const k = TEXT[d.k] ? d.k : 'message';
  const url = typeof d.u === 'string' && /^\/(app|l\/[a-z0-9]{1,32})$/.test(d.u) ? d.u : '/app';
  const tag = typeof d.g === 'string' && /^[a-f0-9]{1,32}$/.test(d.g) ? d.g : 'chat';
  e.waitUntil((async () => {
    const cs = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    // Chrome lets a push pass quietly when the person is already looking at the app. Apple's browsers do not allow that.
    if (!APPLE && cs.some((c) => c.visibilityState === 'visible' && c.focused)) {
      cs.forEach((c) => c.postMessage({ type: 'push', k }));
      return;
    }
    await self.registration.showNotification(TEXT[k][0], { body: TEXT[k][1], tag, icon: '/icons/192.png', data: { u: url } });
  })());
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.u) || '/app';
  e.waitUntil((async () => {
    const cs = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const c of cs) {
      if (new URL(c.url).pathname === url) { await c.focus(); return; }
    }
    await self.clients.openWindow(url);
  })());
});
