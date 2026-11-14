(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };
  const hm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const CONTACT = /(\+?\d[\d\s().-]{7,}\d)|(^|\s)@[A-Za-z0-9_.]{3,}|[\w.+-]+@[\w-]+\.[\w.]+|(wa\.me|t\.me)\/\S+/i;
  const REASONS = [['spam', 'Spam or ads'], ['harassment', 'Harassment or abuse'], ['scam', 'Scam or asking for money'], ['explicit', 'Sexual or explicit content'], ['other', 'Something else']];

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
    const more = $('details', 'more'), menu = $('div', 'menu'), sum = $('summary', null, '\u22ef');
    sum.setAttribute('aria-label', 'More options');
    const rep = $('button', 'linkbtn', 'Report'), blk = $('button', 'linkbtn', 'Block visitor'), res = $('button', 'linkbtn', 'Copy return link'), del = $('button', 'linkbtn danger', 'Delete chat');
    blk.hidden = side !== 'o'; res.hidden = true;
    menu.append(rep, blk, res, del); more.append(sum, menu);
    const head = $('div', 'chat-head');
    if (onBack) { const b = $('button', 'linkbtn back', '\u2039'); b.setAttribute('aria-label', 'Back to inbox'); b.onclick = onBack; head.append(b); }
    const who = $('div', 'grow'); who.append(title, sub);
    head.append(who, revealBtn, more);
    const msgs = $('div', 'msgs'), foot = $('div', 'chat-foot'), dlg = $('dialog', 'dlg');
    msgs.setAttribute('role', 'log'); msgs.setAttribute('aria-live', 'polite'); msgs.setAttribute('aria-label', 'Messages');
    const typing = $('div', 'm them typing'); typing.append($('i'), $('i'), $('i')); typing.hidden = true;
    const hint = $('div', 'hint'); hint.hidden = true; hint.setAttribute('role', 'status');
    const hintBtn = $('button', 'linkbtn', 'Reveal now');
    hint.append($('span', null, 'Looks like contact info. Want to reveal this properly?'), hintBtn);
    root.append(head, msgs, foot, dlg);

    const flash = (t) => { const x = $('div', 'toast', t); x.setAttribute('role', 'status'); root.append(x); setTimeout(() => x.remove(), 2800); };
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
      i.oninput = () => {
        hint.hidden = !CONTACT.test(i.value);
        if (Date.now() - lastTyped > 2000) { lastTyped = Date.now(); api(base + '/typing', {}).catch(() => {}); }
      };
      f.onsubmit = async (e) => {
        e.preventDefault();
        const v = i.value.trim(); if (!v) return;
        i.value = ''; hint.hidden = true; b.classList.add('pop'); setTimeout(() => b.classList.remove('pop'), 200);
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
      res.hidden = !st.resume; blk.hidden = side !== 'o' || st.status === 'blocked';
      foot.textContent = ''; hint.hidden = true;
      if (st.status === 'open') foot.append(hint, composer());
      else if (st.status === 'pending' && side === 'o') {
        const bar = $('div', 'bar'), a = $('button', 'btn', 'Accept'), bl = $('button', 'btn ghost', 'Block');
        a.onclick = () => act('/accept'); bl.onclick = () => blk.onclick();
        const r = $('div', 'row'); r.append(a, bl);
        bar.append($('span', 'fine', 'New request. Accept to start chatting.'), r); foot.append(bar);
      } else {
        const w = $('div');
        w.append($('p', 'bar fine', st.status === 'pending' ? 'Waiting for the owner to accept your message\u2026' : 'This chat is closed.'));
        if (st.status === 'pending' && st.auto) w.append($('p', 'fine', 'Auto-reply: ' + st.auto));
        foot.append(w);
      }
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
    hintBtn.onclick = openReveal;

    rep.onclick = () => {
      more.open = false;
      sheet('Report this chat', (box, close) => {
        let code = '';
        const note = $('textarea'), err = $('p', 'err', 'Pick a reason first');
        err.hidden = true; err.setAttribute('role', 'alert');
        const list = $('div', 'opts'); list.setAttribute('role', 'radiogroup'); list.setAttribute('aria-label', 'Reason');
        REASONS.forEach(([k, t]) => {
          const l = $('label', 'opt'), r = $('input'); r.type = 'radio'; r.name = 'reason'; r.value = k;
          r.onchange = () => { code = k; err.hidden = true; };
          l.append(r, $('span', null, t)); list.append(l);
        });
        note.rows = 3; note.maxLength = 300; note.placeholder = 'Add details (optional)'; note.setAttribute('aria-label', 'Details');
        const row = $('div', 'row'), ok = $('button', 'btn', 'Send report'), no = $('button', 'linkbtn', 'Cancel');
        ok.onclick = async () => {
          if (!code) { err.hidden = false; return; }
          try { await api(base + '/report', { code, note: note.value }); close(); flash('Thanks. Moderators will take a look.'); }
          catch (e) { err.textContent = e.message; err.hidden = false; }
        };
        no.onclick = close; row.append(ok, no);
        box.append($('p', 'fine', "Reports go to the platform's moderators. They only see this chat because you reported it."), list, note, err, row);
      });
    };
    blk.onclick = () => {
      more.open = false;
      confirmSheet('Block this visitor?', "They won't be able to start new chats on any of your links.", 'Block', () => act('/block'));
    };
    res.onclick = async () => {
      more.open = false;
      if (st && st.resume) { await navigator.clipboard.writeText(location.origin + st.resume); flash('Return link copied. Keep it private.'); }
    };
    del.onclick = () => {
      more.open = false;
      confirmSheet('Delete this chat?', "It disappears for both of you and can't be undone.", 'Delete chat', async () => {
        try { await api(base + '/leave', {}); onGone && onGone(); } catch (e) { flash(e.message); }
      });
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
        if (d.side !== side) {
          api(base + '/state').catch(() => {});
          if (document.hidden && window.Notification && Notification.permission === 'granted') new Notification('New message', { body: 'Open the chat to read it' });
        }
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
