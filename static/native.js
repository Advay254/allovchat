// Runs only inside the Android app, where the interface is bundled with the app and talks to the user's own server.
// It adds three things without changing the rest of the code: a bearer token on every API call, a local copy of what
// was last loaded so the app opens and reads offline, and a queue for messages written while offline.
(function () {
  const A = window.AndroidApp;
  if (!A) return;
  const BASE = A.server().replace(/\/+$/, '');
  const TOKEN = A.token();
  const realFetch = window.fetch.bind(window);
  const MAX_AGE = 7 * 864e5; // chats never live longer than 7 days, so the local copy doesn't either

  // ---- local copy (IndexedDB) ----
  let dbp = null;
  const open = () => dbp || (dbp = new Promise((res, rej) => {
    const r = indexedDB.open('allov', 1);
    r.onupgradeneeded = () => r.result.createObjectStore('c');
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  }));
  const idb = async (mode, fn) => {
    const d = await open();
    return new Promise((res, rej) => { const t = d.transaction('c', mode); const r = fn(t.objectStore('c')); t.oncomplete = () => res(r && r.result); t.onerror = () => rej(t.error); });
  };
  const cget = (k) => idb('readonly', (s) => s.get(k)).catch(() => null);
  const cset = (k, v) => idb('readwrite', (s) => s.put(v, k)).catch(() => {});
  const wipe = () => new Promise((res) => { try { open().then((d) => d.close()).catch(() => {}); } catch (e) {} const r = indexedDB.deleteDatabase('allov'); dbp = null; r.onsuccess = r.onerror = r.onblocked = () => res(); });
  // Old entries go on start-up.
  idb('readwrite', (s) => { const c = s.openCursor(); c.onsuccess = () => { const cur = c.result; if (cur) { if (!cur.value || Date.now() - cur.value.t > MAX_AGE) cur.delete(); cur.continue(); } }; return c; }).catch(() => {});

  const json = (o, status) => new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } });
  const offlineErr = () => json({ error: "You're offline. Try again when you're connected." }, 503);
  const signedOut = async () => { await wipe(); A.signedOut(); };
  const timed = (url, init, ms) => { const c = new AbortController(); const t = setTimeout(() => c.abort(), ms); return realFetch(url, { ...init, signal: c.signal }).finally(() => clearTimeout(t)); };
  const withAuth = (init) => { const h = new Headers((init && init.headers) || {}); h.set('Authorization', 'Bearer ' + TOKEN); return { ...init, headers: h }; };

  // Messages waiting to be sent belong in the chat they were written in, even before the server has them.
  const withQueued = (path, text) => {
    const m = path.match(/^(\/api\/(?:t|m)\/\d+)\/state(?:\?|$)/);
    if (!m) return text;
    try {
      const q = JSON.parse(A.pendingFor(m[1] + '/send') || '[]');
      if (!q.length) return text;
      const st = JSON.parse(text);
      const have = new Set((st.messages || []).map((x) => x.cid).filter(Boolean));
      q.forEach((e) => { try { const b = JSON.parse(e.body); if (have.has(b.cid)) return; st.messages.push({ id: 'q' + b.cid, side: st.side, body: b.body, ts: new Date(e.ts).toISOString(), is_read: false, cid: b.cid, pend: true }); } catch (x) {} });
      return JSON.stringify(st);
    } catch (e) { return text; }
  };

  const cachedGet = async (url, init) => {
    try {
      const r = await timed(BASE + url, withAuth(init), 9000);
      if (r.status === 401) { await signedOut(); return r; }
      if (!r.ok) return r;
      let text = await r.text();
      if (url === '/api/public') { try { const p = JSON.parse(text); if (!p.base_url) { p.base_url = BASE; text = JSON.stringify(p); } } catch (e) {} } // share links must point at the server, not at the app
      cset(url, { t: Date.now(), body: text });
      return new Response(withQueued(url, text), { status: 200, headers: { 'Content-Type': 'application/json' } });
    } catch (e) {
      const hit = await cget(url);
      if (hit && Date.now() - hit.t < MAX_AGE) {
        try { const st = JSON.parse(hit.body); if (st && st.expires_at && new Date(st.expires_at) < new Date()) return offlineErr(); } catch (x) {}
        return new Response(withQueued(url, hit.body), { status: 200, headers: { 'Content-Type': 'application/json', 'X-Offline': '1' } });
      }
      return offlineErr();
    }
  };

  const post = async (url, init) => {
    if (url === '/api/logout') {
      try { await timed(BASE + '/api/app/logout', withAuth({ ...init, method: 'POST' }), 5000); } catch (e) {}
      await signedOut();
      return json({ ok: true });
    }
    const send = /^\/api\/(?:t|m)\/\d+\/send$/.test(url);
    try {
      const r = await timed(BASE + url, withAuth(init), send ? 15000 : 20000);
      if (r.status === 401) await signedOut();
      else if (r.ok && url === '/api/account/delete') await signedOut();
      return r;
    } catch (e) {
      // No connection: a message is kept on the phone and sent as soon as the connection is back, even if the app is closed.
      // Its key (cid) lets the server drop it if the first attempt did arrive after all.
      if (send && A.enqueue(url, init.body)) return json({ ok: true, queued: true });
      return offlineErr();
    }
  };

  window.fetch = function (input, init) {
    const url = typeof input === 'string' ? input : input && input.url;
    if (typeof url !== 'string' || url.indexOf('/api/') !== 0) return realFetch(input, init);
    const method = ((init && init.method) || 'GET').toUpperCase();
    return method === 'GET' ? cachedGet(url, init) : post(url, init);
  };

  // ---- live updates: the browser's EventSource can't send a token, so this one does the same job with fetch ----
  class LiveStream {
    constructor(path) { this.url = path; this.readyState = 0; this.closed = false; this.onopen = this.onmessage = this.onerror = null; this.ctl = null; this.run(); }
    // Messages that arrive live are added to the saved copy of the chat, so it is complete when the app is opened offline.
    remember(data) {
      const m = this.url.match(/^(\/api\/(?:t|m)\/\d+)\/stream/);
      if (!m) return;
      try {
        const d = JSON.parse(data);
        if (d.t !== 'msg') return;
        cget(m[1] + '/state').then((hit) => {
          if (!hit) return;
          const st = JSON.parse(hit.body);
          if (st.messages.some((x) => x.id === d.id || (d.cid && x.cid === d.cid))) return;
          st.messages.push({ id: d.id, side: d.side, body: d.body, ts: d.ts, is_read: false, cid: d.cid });
          cset(m[1] + '/state', { t: hit.t, body: JSON.stringify(st) });
        });
      } catch (e) {}
    }
    close() { this.closed = true; this.readyState = 2; if (this.ctl) this.ctl.abort(); }
    async run() {
      let wait = 1000;
      while (!this.closed) {
        try {
          this.ctl = new AbortController();
          const r = await realFetch(BASE + this.url, { headers: { Authorization: 'Bearer ' + TOKEN, Accept: 'text/event-stream' }, signal: this.ctl.signal });
          if (r.status === 401) { await signedOut(); return; }
          if (!r.ok || !r.body) throw new Error('stream');
          this.readyState = 1; wait = 1000;
          if (this.onopen) this.onopen({});
          const rd = r.body.getReader(), dec = new TextDecoder();
          let buf = '';
          for (;;) {
            const { done, value } = await rd.read();
            if (done) break;
            buf += dec.decode(value, { stream: true });
            let i;
            while ((i = buf.indexOf('\n\n')) > -1) {
              const chunk = buf.slice(0, i); buf = buf.slice(i + 2);
              const data = chunk.split('\n').filter((l) => l.indexOf('data:') === 0).map((l) => l.slice(5).replace(/^ /, '')).join('\n');
              if (data) this.remember(data);
              if (data && this.onmessage) this.onmessage({ data });
            }
          }
        } catch (e) { if (this.closed) return; }
        if (this.closed) return;
        this.readyState = 0;
        if (this.onerror) this.onerror({});
        // The chat screen loads its messages when the stream opens. With no connection it never does, so ask once for a
        // refresh, which is then answered from the local copy.
        if (!this.asked && this.onmessage) { this.asked = true; this.onmessage({ data: '{"t":"status"}' }); }
        await new Promise((r) => setTimeout(r, wait));
        wait = Math.min(wait * 2, 15000);
      }
    }
  }
  window.EventSource = LiveStream;

  // Images from the API (the QR code) need the token too.
  const d = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'src');
  Object.defineProperty(HTMLImageElement.prototype, 'src', {
    configurable: true,
    get() { return d.get.call(this); },
    set(v) {
      if (typeof v === 'string' && v.indexOf('/api/') === 0) realFetch(BASE + v, withAuth({})).then((r) => r.blob()).then((b) => d.set.call(this, URL.createObjectURL(b))).catch(() => {});
      else d.set.call(this, v);
    }
  });

  // ---- notifications: the app's own background connection replaces browser push ----
  window.push = {
    supported: () => true,
    needsInstall: () => false,
    status: async () => (A.notificationsOn() ? 'on' : 'off'),
    canAsk: async () => !A.notificationsOn(),
    enable: async () => {
      A.enableNotifications();
      for (let i = 0; i < 60; i++) { if (A.notificationsOn()) return; await new Promise((r) => setTimeout(r, 500)); }
      throw new Error('Notifications were not turned on.');
    },
    disable: async () => { A.disableNotifications(); },
    sync: async () => {}
  };

  // ---- queued messages: the phone sends them, then asks the open chat to refresh ----
  window.addEventListener('online', () => A.flush());
  window.addEventListener('allov:flushed', () => { if (window._es && window._es.onmessage) window._es.onmessage({ data: '{"t":"status"}' }); });
  A.flush();
})();
