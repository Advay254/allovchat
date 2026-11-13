(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };
  const hm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

  window.api = async (path, body) => {
    const r = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    let j = {};
    try { j = await r.json(); } catch (e) {}
    if (!r.ok) { const er = new Error(j.error || 'Something went wrong'); er.status = r.status; throw er; }
    return j;
  };

  // side: 'o' (owner) or 'v' (visitor). base: '/api/t/ID' or '/api/l/SLUG'
  window.mountChat = function (root, base, side, onGone, onBack) {
    if (window._es) window._es.close();
    root.textContent = '';
    let st = null, tTimer = 0, lastTyped = 0;
    const title = $('div', 'chat-title'), sub = $('div', 'chat-sub');
    const revealBtn = $('button', 'btn ghost sm', 'Reveal');
    const more = $('details', 'more'), menu = $('div', 'menu');
    const rep = $('button', 'linkbtn', 'Report'), del = $('button', 'linkbtn danger', 'Delete chat');
    menu.append(rep, del);
    more.append($('summary', null, '\u22ef'), menu);
    const head = $('div', 'chat-head');
    if (onBack) { const b = $('button', 'linkbtn back', '\u2039'); b.setAttribute('aria-label', 'Back'); b.onclick = onBack; head.append(b); }
    const who = $('div', 'grow'); who.append(title, sub);
    head.append(who, revealBtn, more);
    const msgs = $('div', 'msgs'), foot = $('div', 'chat-foot'), dlg = $('dialog', 'dlg');
    const typing = $('div', 'm them typing'); typing.append($('i'), $('i'), $('i')); typing.hidden = true;
    root.append(head, msgs, foot, dlg);

    const flash = (t) => { const x = $('div', 'toast', t); root.append(x); setTimeout(() => x.remove(), 2500); };
    const bottom = () => { msgs.scrollTop = msgs.scrollHeight; };
    const resetSub = () => { sub.textContent = !st ? '' : side === 'o' ? 'Anonymous visitor \u00b7 ' + st.label : 'Anonymous chat'; };
    const bubble = (m) => {
      const d = $('div', 'm ' + (m.side === side ? 'me' : 'them')); d.dataset.id = m.id;
      d.append($('span', null, m.body), $('small', 'meta', hm(m.ts) + (m.side === side ? (m.is_read ? ' \u00b7 Read' : ' \u00b7 Sent') : '')));
      return d;
    };
    const add = (m) => { typing.hidden = true; msgs.insertBefore(bubble(m), typing); bottom(); };
    const act = async (path) => { try { await api(base + path, {}); load(); } catch (e) { flash(e.message); } };

    const composer = () => {
      const f = $('form', 'composer'), i = $('input'), b = $('button', 'btn', 'Send');
      i.placeholder = 'Type a message'; i.autocomplete = 'off'; i.setAttribute('aria-label', 'Message'); b.type = 'submit';
      i.oninput = () => { if (Date.now() - lastTyped > 2000) { lastTyped = Date.now(); api(base + '/typing', {}).catch(() => {}); } };
      f.onsubmit = async (e) => {
        e.preventDefault();
        const v = i.value.trim(); if (!v) return;
        i.value = ''; b.classList.add('pop'); setTimeout(() => b.classList.remove('pop'), 200);
        try { await api(base + '/send', { body: v }); } catch (er) { i.value = v; flash(er.message); }
      };
      f.append(i, b); setTimeout(() => i.focus(), 0);
      return f;
    };

    const render = () => {
      title.textContent = st.peer; resetSub();
      msgs.textContent = ''; msgs.append(typing); typing.hidden = true;
      st.messages.forEach((m) => msgs.insertBefore(bubble(m), typing)); bottom();
      revealBtn.hidden = st.status !== 'open';
      revealBtn.textContent = st.reveal.done ? 'Contact' : 'Reveal';
      foot.textContent = '';
      if (st.status === 'open') foot.append(composer());
      else if (st.status === 'pending' && side === 'o') {
        const bar = $('div', 'bar'), a = $('button', 'btn', 'Accept'), bl = $('button', 'btn ghost', 'Block');
        a.onclick = () => act('/accept'); bl.onclick = () => act('/block');
        const r = $('div', 'row'); r.append(a, bl);
        bar.append($('span', 'fine', 'New request. Accept to start chatting.'), r); foot.append(bar);
      } else foot.append($('p', 'bar fine', st.status === 'pending' ? 'Waiting for the owner to accept your message\u2026' : 'This chat is closed.'));
    };

    const openReveal = () => {
      const r = st.reveal; dlg.textContent = '';
      dlg.append($('h3', null, r.done ? 'Contact details' : 'Reveal your contact?'));
      if (r.done) {
        if (r.contact) {
          const c = $('button', 'btn', 'Copy');
          c.onclick = () => { navigator.clipboard.writeText(r.contact); c.textContent = 'Copied \u2713'; };
          dlg.append($('p', null, st.peer + ' shared:'), $('p', 'big', r.contact), c, $('p', 'fine', 'This disappears 24 hours after the reveal.'));
        } else dlg.append($('p', null, 'The reveal window has expired.'));
      } else {
        dlg.append($('p', null, 'You both need to agree. Nothing is shared until ' + st.peer + ' agrees too.'),
          $('p', 'fine', 'You: ' + (r.you ? 'ready' : 'not yet') + ' \u00b7 ' + st.peer + ': ' + (r.they ? 'ready' : 'waiting')));
        const l = $('label', null, 'What will you share?'), i = $('input');
        i.maxLength = 100; i.placeholder = 'WhatsApp number or @handle'; l.append(i);
        const ok = $('button', 'btn', r.you ? 'Update contact' : 'Reveal contact');
        ok.onclick = async () => { try { await api(base + '/reveal', { agree: true, contact: i.value }); await load(); } catch (e) { flash(e.message); } };
        dlg.append(l, $('p', 'warn', "We can't verify what anyone shares. Only reveal when you trust them."), ok);
        if (r.you) { const w = $('button', 'btn ghost', 'Withdraw'); w.onclick = async () => { try { await api(base + '/reveal', { agree: false }); await load(); } catch (e) { flash(e.message); } }; dlg.append(w); }
      }
      const x = $('button', 'btn ghost', 'Close'); x.onclick = () => dlg.close(); dlg.append(x);
      if (!dlg.open) dlg.showModal();
    };
    revealBtn.onclick = openReveal;

    rep.onclick = async () => {
      const r = prompt('What happened? This goes to the site admin.'); more.open = false;
      if (!r) return;
      try { await api(base + '/report', { reason: r }); flash('Report sent'); } catch (e) { flash(e.message); }
    };
    del.onclick = async () => {
      more.open = false;
      if (!confirm('Delete this chat for both of you?')) return;
      try { await api(base + '/leave', {}); onGone && onGone(); } catch (e) { flash(e.message); }
    };

    const load = async () => {
      try { st = await api(base + '/state'); render(); if (dlg.open) openReveal(); }
      catch (e) { if (e.status === 404) onGone && onGone(); }
    };

    const es = new EventSource(base + '/stream'); window._es = es;
    es.onopen = load;
    es.onmessage = (ev) => {
      const d = JSON.parse(ev.data);
      if (d.t === 'msg') {
        if (!msgs.querySelector('[data-id="' + d.id + '"]')) add({ id: d.id, side: d.side, body: d.body, ts: d.ts });
        if (d.side !== side) api(base + '/state').catch(() => {});
      } else if (d.t === 'typing' && d.side !== side) {
        typing.hidden = false; bottom(); sub.textContent = 'typing\u2026';
        clearTimeout(tTimer); tTimer = setTimeout(() => { typing.hidden = true; resetSub(); }, 3000);
      } else if (d.t === 'read' && d.s !== side) {
        msgs.querySelectorAll('.me .meta').forEach((m) => { m.textContent = m.textContent.replace('Sent', 'Read'); });
      } else if (d.t === 'reveal' || d.t === 'status') load();
      else if (d.t === 'gone') onGone && onGone();
    };
  };
})();
