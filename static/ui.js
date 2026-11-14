(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };

  // sheet opens an accessible modal that fades in and out. build(box, close) fills it.
  window.sheet = function (title, build) {
    const prev = document.activeElement;
    const ov = $('div', 'ov'), box = $('div', 'sheet'), h = $('h3', null, title);
    h.id = 'sh' + Date.now();
    box.setAttribute('role', 'dialog'); box.setAttribute('aria-modal', 'true'); box.setAttribute('aria-labelledby', h.id);
    box.append(h);
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
      document.removeEventListener('keydown', key);
      ov.classList.remove('in');
      setTimeout(() => { ov.remove(); if (prev && prev.focus) prev.focus(); }, 200);
    };
    ov.onclick = (e) => { if (e.target === ov) close(); };
    document.addEventListener('keydown', key);
    build(box, close);
    ov.append(box); document.body.append(ov);
    requestAnimationFrame(() => { ov.classList.add('in'); const f = box.querySelector('input,textarea,select,button'); if (f) f.focus(); });
    return close;
  };

  window.confirmSheet = function (title, text, okLabel, onOk) {
    return window.sheet(title, (box, close) => {
      const row = $('div', 'row'), ok = $('button', 'btn', okLabel), no = $('button', 'linkbtn', 'Cancel');
      ok.onclick = async () => { close(); await onOk(); };
      no.onclick = close;
      row.append(ok, no);
      box.append($('p', null, text), row);
    });
  };
})();
