// Notifications in the browser. Nothing here asks for permission by itself: the prompt only appears
// after a tap on a "Turn on notifications" control, and every call here is made from such a tap.
(function () {
  const sw = 'serviceWorker' in navigator;
  const ok = sw && 'PushManager' in window && 'Notification' in window;
  const ios = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
  const install = ios && !standalone; // iPhone and iPad only allow push for an app added to the Home Screen

  const toBytes = (s) => { s = s.replace(/-/g, '+').replace(/_/g, '/'); const b = atob(s + '==='.slice((s.length + 3) % 4)); return Uint8Array.from(b, (c) => c.charCodeAt(0)); };
  const toB64 = (buf) => { let s = ''; new Uint8Array(buf).forEach((c) => { s += String.fromCharCode(c); }); return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); };
  const path = (s, a) => (s.kind === 'thread' ? '/api/l/' + encodeURIComponent(s.slug) + '/push/' + a : '/api/push/' + a);
  const flag = (s) => (s.kind === 'thread' ? 'push_' + s.slug : 'push_owner');

  let regP = null;
  const reg = () => regP || (regP = navigator.serviceWorker.register('/sw.js', { scope: '/', updateViaCache: 'none' }).then(() => navigator.serviceWorker.ready));
  if (sw && !window.AndroidApp) reg().catch(() => {}); // registering asks for nothing; it only enables the offline page and, later, alerts. The Android app has its own.

  const sub = async () => { if (!sw) return null; const r = await navigator.serviceWorker.getRegistration('/'); return r ? r.pushManager.getSubscription() : null; };

  const push = {
    supported: () => ok,
    needsInstall: () => ok && install,
    // 'unsupported' | 'install' | 'blocked' | 'on' | 'off'
    async status(scope) {
      if (!ok) return 'unsupported';
      if (install) return 'install';
      if (Notification.permission === 'denied') return 'blocked';
      if (Notification.permission === 'granted' && localStorage.getItem(flag(scope)) === '1' && (await sub())) return 'on';
      return 'off';
    },
    // True when it makes sense to offer the "turn on" prompt to this person.
    async canAsk(scope) { return ok && !install && Notification.permission === 'default' && (await push.status(scope)) === 'off'; },
    async enable(scope) {
      if (!ok) throw new Error("Notifications aren't supported in this browser.");
      if (install) throw new Error('Add this app to your Home Screen first, then turn notifications on from there.');
      const perm = Notification.permission === 'granted' ? 'granted' : await Notification.requestPermission(); // must stay the first await: it needs the tap
      if (perm !== 'granted') throw new Error(perm === 'denied' ? 'Notifications are blocked in your browser settings.' : 'Notifications were not turned on.');
      const r = await reg();
      const { key } = await api('/api/push/key');
      const want = toBytes(key);
      let s = await r.pushManager.getSubscription();
      if (s && s.options && s.options.applicationServerKey && toB64(s.options.applicationServerKey) !== key) { await s.unsubscribe(); s = null; }
      if (!s) s = await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: want });
      const j = s.toJSON();
      await api(path(scope, 'subscribe'), { endpoint: j.endpoint, p256dh: j.keys.p256dh, auth: j.keys.auth });
      localStorage.setItem(flag(scope), '1');
    },
    // Removes this device from the server for this scope. The browser's own subscription stays, so other chats keep working.
    async disable(scope) {
      localStorage.removeItem(flag(scope));
      const s = await sub();
      if (s) { try { await api(path(scope, 'unsubscribe'), { endpoint: s.endpoint }); } catch (e) {} }
    },
    // Keeps the server in step if the browser renewed its subscription. Never prompts.
    async sync(scope) {
      try {
        if (!ok || install || Notification.permission !== 'granted' || localStorage.getItem(flag(scope)) !== '1') return;
        if (sessionStorage.getItem(flag(scope) + '_sync')) return;
        sessionStorage.setItem(flag(scope) + '_sync', '1');
        const r = await reg();
        let s = await r.pushManager.getSubscription();
        if (!s) { const { key } = await api('/api/push/key'); s = await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: toBytes(key) }); }
        const j = s.toJSON();
        await api(path(scope, 'subscribe'), { endpoint: j.endpoint, p256dh: j.keys.p256dh, auth: j.keys.auth });
      } catch (e) {}
    }
  };
  window.push = push;
})();
