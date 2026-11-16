(function () {
  // ---- extra icons in the same style as the base set: 24px grid, 1.75 stroke, round caps ----
  const E = {
    shield: '<path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z"/><path d="M9 12l2 2 4-4"/>',
    exit: '<path d="M10 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4"/><path d="M15 8l4 4-4 4M19 12H9"/>',
    clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
    eyeoff: '<path d="M4 4l16 16"/><path d="M10.6 6.2A9 9 0 0 1 12 6c4 0 7 3.5 8.5 6-.6 1-1.4 2-2.4 2.9M6.5 7.9C5 9 3.9 10.6 3.5 12c1.5 2.6 4.5 6 8.5 6 1.2 0 2.3-.3 3.3-.8"/>',
    user: '<circle cx="12" cy="8" r="3.5"/><path d="M5 20c0-3.5 3-6 7-6s7 2.5 7 6"/>',
    grid: '<rect x="4" y="4" width="7" height="7" rx="2"/><rect x="13" y="4" width="7" height="7" rx="2"/><rect x="4" y="13" width="7" height="7" rx="2"/><rect x="13" y="13" width="7" height="7" rx="2"/>',
    globe: '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17M12 3.5c2.5 2.5 3.5 5.5 3.5 8.5s-1 6-3.5 8.5c-2.5-2.5-3.5-5.5-3.5-8.5s1-6 3.5-8.5z"/>',
    code: '<path d="M8 8l-4 4 4 4M16 8l4 4-4 4M13.5 5l-3 14"/>',
    key: '<circle cx="8" cy="15" r="3.5"/><path d="M10.5 12.5L19 4M15 8l2.5 2.5"/>'
  };
  const base = window.ico;
  window.ico = (n, s = 22) => E[n] ? '<svg width="' + s + '" height="' + s + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + E[n] + '</svg>' : base(n, s);

  // ---- run a function once things go quiet, so a burst of events makes one request instead of many ----
  window.debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };

  // ---- end-to-end encryption in the browser (WebCrypto) ----
  // Each chat has a shared secret from ECDH (P-256) between the link's key and the guest's key. Messages are
  // sealed with AES-256-GCM before they leave the device. The server only ever stores and relays ciphertext.
  const enc = new TextEncoder(), dec = new TextDecoder();
  const b64 = (buf) => { const a = new Uint8Array(buf); let s = ''; for (let i = 0; i < a.length; i++) s += String.fromCharCode(a[i]); return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); };
  const unb64 = (s) => { s = s.replace(/-/g, '+').replace(/_/g, '/'); while (s.length % 4) s += '='; const r = atob(s), a = new Uint8Array(r.length); for (let i = 0; i < r.length; i++) a[i] = r.charCodeAt(i); return a; };
  const CURVE = { name: 'ECDH', namedCurve: 'P-256' };
  // Private keys live in this browser only. They are never sent to the server.
  const K = {
    get(k) { try { return JSON.parse(localStorage.getItem('e2e.' + k)); } catch (e) { return null; } },
    set(k, v) { localStorage.setItem('e2e.' + k, JSON.stringify(v)); },
    del(k) { localStorage.removeItem('e2e.' + k); }
  };
  async function newPair() {
    const p = await crypto.subtle.generateKey(CURVE, true, ['deriveBits']);
    return { pub: b64(await crypto.subtle.exportKey('raw', p.publicKey)), priv: await crypto.subtle.exportKey('jwk', p.privateKey) };
  }
  async function shared(privJwk, pubB64) {
    const priv = await crypto.subtle.importKey('jwk', privJwk, CURVE, false, ['deriveBits']);
    const pub = await crypto.subtle.importKey('raw', unb64(pubB64), CURVE, false, []);
    const bits = await crypto.subtle.deriveBits({ name: 'ECDH', public: pub }, priv, 256);
    const hk = await crypto.subtle.importKey('raw', bits, 'HKDF', false, ['deriveKey']);
    return crypto.subtle.deriveKey({ name: 'HKDF', hash: 'SHA-256', salt: enc.encode('allovchat-e2e-v1'), info: enc.encode('chat') }, hk, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  }
  // ctx separates messages ('m') from contact details ('c'); side binds a message to who sent it.
  const aad = (side, ctx) => enc.encode('v1:' + (ctx || 'm') + ':' + side);
  async function seal(key, text, side, ctx) {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const ct = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad(side, ctx) }, key, enc.encode(text));
    return 'e1.' + b64(iv) + '.' + b64(ct);
  }
  async function open(key, body, side, ctx) {
    const p = String(body).split('.');
    if (p[0] !== 'e1' || p.length !== 3) throw new Error('bad format');
    return dec.decode(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: unb64(p[1]), additionalData: aad(side, ctx) }, key, unb64(p[2])));
  }
  // A short code both people can compare. If it matches on both screens, nobody swapped the keys.
  async function safety(a, b) {
    const [x, y] = [a, b].sort();
    const h = new Uint8Array(await crypto.subtle.digest('SHA-256', enc.encode('allovchat-safety-v1|' + x + '|' + y)));
    return [...h.slice(0, 12)].map((v) => v.toString(16).padStart(2, '0')).join('').toUpperCase().match(/.{1,4}/g).join(' ');
  }
  const rand = (n) => [...crypto.getRandomValues(new Uint8Array(n))].map((v) => v.toString(16).padStart(2, '0')).join('');
  window.e2e = { K, newPair, shared, seal, open, safety, b64, unb64, rand, ok: !!(window.crypto && crypto.subtle) };
})();
