(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;

  const P = {
    send: '<path d="M12 19V5M5 12l7-7 7 7"/>',
    back: '<path d="M15 18l-6-6 6-6"/>',
    more: '<circle cx="5" cy="12" r="1.3"/><circle cx="12" cy="12" r="1.3"/><circle cx="19" cy="12" r="1.3"/>',
    copy: '<rect x="9" y="9" width="11" height="11" rx="3"/><path d="M5 15V6a2 2 0 0 1 2-2h9"/>',
    check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
    share: '<path d="M12 3v12M7 8l5-5 5 5"/><path d="M5 13v5a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-5"/>',
    edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/>',
    trash: '<path d="M5 7h14M10 11v6M14 11v6M7 7l1 12a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/>',
    flag: '<path d="M5 21V4M5 4h11l-2 4 2 4H5"/>',
    block: '<circle cx="12" cy="12" r="8.5"/><path d="M6 6l12 12"/>',
    lock: '<rect x="5" y="11" width="14" height="9" rx="3"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
    link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
    chat: '<path d="M5 6a3 3 0 0 1 3-3h8a3 3 0 0 1 3 3v7a3 3 0 0 1-3 3h-4l-5 4v-4a3 3 0 0 1-3-3z"/>',
    gear: '<circle cx="12" cy="12" r="3"/><path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1"/>'
  };
  window.ico = (n, s = 22) => '<svg width="' + s + '" height="' + s + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + P[n] + '</svg>';

  // Short vibrations on phones that support it. iOS Safari does not expose this to web pages.
  window.haptic = (k) => {
    try {
      if (reduce || !navigator.vibrate || localStorage.getItem('haptics') === '0') return;
      navigator.vibrate({ tap: 6, select: 8, success: [8, 40, 14], warn: [18, 50, 18] }[k] || 6);
    } catch (e) {}
  };
  document.addEventListener('pointerdown', (e) => { if (e.target.closest('.btn,.icon-btn,.tab,.sw,.opt,.row-item:not(.static)')) haptic('tap'); }, { passive: true });

  window.vt = (fn) => { if (document.startViewTransition && !reduce) document.startViewTransition(fn); else fn(); };
  window.pulse = (el) => { el.classList.remove('pulse'); void el.offsetWidth; el.classList.add('pulse'); };
  window.shake = (el) => { el.classList.remove('shake'); void el.offsetWidth; el.classList.add('shake'); haptic('warn'); };
  window.stagger = (parent) => [...parent.children].forEach((c, i) => c.style.setProperty('--i', i));

  const AV = ['#C8401B', '#4F6F52', '#9A6A12', '#4A5E94', '#7C4A69'];
  window.avatar = (name, size) => {
    let h = 0; for (const c of name || '?') h = (h * 31 + c.charCodeAt(0)) >>> 0;
    const a = $('span', 'av', (name || '?')[0].toUpperCase()); a.style.background = AV[h % AV.length];
    if (size) a.style.width = a.style.height = size + 'px';
    return a;
  };

  window.toast = (t) => {
    document.querySelectorAll('.toast').forEach((x) => x.remove());
    const x = $('div', 'toast', t); x.setAttribute('role', 'status'); document.body.append(x);
    requestAnimationFrame(() => x.classList.add('in'));
    setTimeout(() => { x.classList.remove('in'); setTimeout(() => x.remove(), 190); }, 2600);
  };

  // Crossfade one icon into another with a little blur.
  window.swapIcon = (btn, name) => {
    const old = btn.querySelector('.ic:not(.out)');
    const n = $('span', 'ic'); n.innerHTML = ico(name); btn.append(n);
    if (old) { old.classList.add('out'); setTimeout(() => old.remove(), 200); }
  };
  window.copyBtn = async (btn, text) => {
    try { await navigator.clipboard.writeText(text); } catch (e) { toast("Couldn't copy that"); return; }
    haptic('success'); pulse(btn); swapIcon(btn, 'check'); setTimeout(() => swapIcon(btn, 'copy'), 1600);
  };

  window.switchEl = (on, cb, label) => {
    const b = $('button', 'sw'); b.type = 'button'; b.setAttribute('role', 'switch'); b.setAttribute('aria-checked', String(!!on)); b.setAttribute('aria-label', label);
    b.append($('span', 'knob'));
    b.onclick = async () => {
      const v = b.getAttribute('aria-checked') !== 'true';
      if ((await cb(v)) !== false) { b.setAttribute('aria-checked', String(v)); haptic('select'); }
    };
    return b;
  };

  if (window.visualViewport) {
    const f = () => document.documentElement.style.setProperty('--vvh', visualViewport.height + 'px');
    visualViewport.addEventListener('resize', f); f();
  }

  // Bottom sheet on phones, centred modal on larger screens. Fades and slides in, shorter on exit.
  window.sheet = function (title, build, opts) {
    opts = opts || {};
    const trig = document.activeElement;
    const ov = $('div', 'ov'), box = $('div', 'sheet'), grab = $('div', 'grab'), h = $('h3', null, title);
    grab.append($('i')); h.id = 'sh' + Date.now();
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-modal', 'true'); box.setAttribute('aria-labelledby', h.id);
    box.append(grab, h);
    let closed = false;
    const key = (e) => {
      if (e.key === 'Escape') close();
      if (e.key !== 'Tab') return;
      const f = [...box.querySelectorAll('button,input,textarea,select,a[href]')].filter((x) => !x.disabled && !x.hidden);
      if (!f.length) return;
      const a = f[0], z = f[f.length - 1];
      if (e.shiftKey && document.activeElement === a) { e.preventDefault(); z.focus(); }
      else if (!e.shiftKey && document.activeElement === z) { e.preventDefault(); a.focus(); }
    };
    const close = () => {
      if (closed) return; closed = true;
      document.removeEventListener('keydown', key);
      ov.classList.remove('in');
      if (opts.onClose) opts.onClose();
      setTimeout(() => { ov.remove(); if (trig && trig.focus && document.contains(trig)) trig.focus(); }, 190);
    };
    ov.onclick = (e) => { if (e.target === ov) close(); };
    document.addEventListener('keydown', key);
    build(box, close);
    [...box.children].forEach((c, i) => { if (i > 1) { c.classList.add('sx'); c.style.setProperty('--i', i - 2); } });
    ov.append(box); document.body.append(ov);
    if (trig && trig.getBoundingClientRect && innerWidth >= 640) {
      const t = trig.getBoundingClientRect(), b = box.getBoundingClientRect();
      box.style.transformOrigin = (t.left + t.width / 2 - b.left) + 'px ' + (t.top + t.height / 2 - b.top) + 'px';
    }
    // Drag the handle to dismiss. Pulling up meets resistance, like a rubber band.
    let y0 = null, t0 = 0, dy = 0;
    grab.addEventListener('pointerdown', (e) => { y0 = e.clientY; t0 = e.timeStamp; dy = 0; grab.setPointerCapture(e.pointerId); box.style.transition = 'none'; });
    grab.addEventListener('pointermove', (e) => { if (y0 === null) return; const raw = e.clientY - y0; dy = raw > 0 ? raw : -Math.pow(-raw, 0.7); box.style.transform = 'translateY(' + dy + 'px)'; });
    const up = (e) => {
      if (y0 === null) return;
      const v = dy / Math.max(1, e.timeStamp - t0); y0 = null; box.style.transition = ''; box.style.transform = '';
      if (dy > 90 || v > 0.6) close();
    };
    grab.addEventListener('pointerup', up); grab.addEventListener('pointercancel', up);
    requestAnimationFrame(() => { ov.classList.add('in'); const f = box.querySelector('input,textarea,select,button'); if (f) f.focus(); });
    return close;
  };

  window.confirmSheet = function (title, text, okLabel, onOk) {
    return window.sheet(title, (box, close) => {
      const row = $('div', 'row'), ok = $('button', 'btn', okLabel), no = $('button', 'btn soft', 'Cancel');
      ok.onclick = async () => { haptic('warn'); close(); await onOk(); };
      no.onclick = close; row.append(no, ok);
      box.append($('p', null, text), row);
    });
  };

  // Small popover that grows out of the button that opened it and shrinks back into it.
  window.popover = function (trig, build) {
    const pop = $('div', 'pop'); pop.setAttribute('role', 'menu');
    let closed = false;
    const off = (e) => { if (!pop.contains(e.target) && e.target !== trig) close(); };
    const key = (e) => { if (e.key === 'Escape') close(); };
    const close = () => {
      if (closed) return; closed = true;
      document.removeEventListener('pointerdown', off, true); document.removeEventListener('keydown', key);
      pop.classList.remove('in'); setTimeout(() => pop.remove(), 160);
    };
    build(pop, close);
    document.body.append(pop);
    const r = trig.getBoundingClientRect(), pr = pop.getBoundingClientRect();
    const left = Math.min(Math.max(8, r.right - pr.width), innerWidth - pr.width - 8);
    const below = r.bottom + 6 + pr.height <= innerHeight;
    const top = below ? r.bottom + 6 : Math.max(8, r.top - pr.height - 6);
    pop.style.left = left + 'px'; pop.style.top = top + 'px';
    pop.style.transformOrigin = (r.left + r.width / 2 - left) + 'px ' + (below ? '0px' : pr.height + 'px');
    document.addEventListener('pointerdown', off, true); document.addEventListener('keydown', key);
    requestAnimationFrame(() => { pop.classList.add('in'); const f = pop.querySelector('button'); if (f) f.focus(); });
    return close;
  };
  window.menuItem = (pop, close, icon, text, fn, danger) => {
    const b = $('button', danger ? 'danger' : ''); b.setAttribute('role', 'menuitem');
    b.innerHTML = ico(icon, 20) + '<span>' + text + '</span>';
    b.onclick = () => { close(); setTimeout(fn, 40); };
    pop.append(b);
  };
})();
