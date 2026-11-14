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
    let st = null, tTimer = 0, lastTyped = 0, rev = null;
    const pend = [];

    const head = $('div', 'chat-head');
    if (onBack) { const b = $('button', 'icon-btn back'); b.innerHTML = ico('back', 24); b.setAttribute('aria-label', 'Back'); b.onclick = onBack; head.append(b); }
    let av = avatar('?');
    const who = $('div', 'grow'), title = $('div', 'chat-title'), sub = $('div', 'chat-sub'); who.append(title, sub);
    const revealBtn = $('button', 'btn soft sm'); revealBtn.innerHTML = ico('lock', 18) + '<span>Reveal</span>'; revealBtn.hidden = true;
    const moreBtn = $('button', 'icon-btn'); moreBtn.innerHTML = ico('more'); moreBtn.setAttribute('aria-label', 'More options'); moreBtn.setAttribute('aria-haspopup', 'menu');
    head.append(av, who, revealBtn, moreBtn);

    const msgs = $('div', 'msgs'), foot = $('div', 'chat-foot');
    msgs.setAttribute('role', 'log'); msgs.setAttribute('aria-live', 'polite'); msgs.setAttribute('aria-label', 'Messages');
    const typing = $('div', 'mrow them'), tb = $('div', 'm them typing'); tb.append($('i'), $('i'), $('i')); typing.append(tb); typing.hidden = true;
    const hint = $('div', 'hint'), hintBtn = $('button', 'linkbtn', 'Reveal now'); hint.hidden = true; hint.setAttribute('role', 'status');
    hint.append($('span', null, 'Looks like contact info. Want to reveal this properly?'), hintBtn);
    root.append(head, msgs, foot);
    for (let i = 0; i < 3; i++) { const r = $('div', 'mrow ' + (i === 1 ? 'me' : 'them')), s = $('div', 'sk'); s.style.cssText = 'height:42px;border-radius:20px;width:' + (45 + i * 12) + '%'; r.append(s); msgs.append(r); }

    const bottom = (smooth) => msgs.scrollTo({ top: msgs.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
    const resetSub = () => { sub.textContent = !st ? '' : side === 'o' ? 'Anonymous visitor \u00b7 ' + st.label : 'Anonymous chat'; };
    const lastRow = () => { const n = typing.previousElementSibling; return n && n.classList.contains('mrow') ? n : null; };
    const bubble = (m, animate) => {
      const mine = m.side === side, row = $('div', 'mrow ' + (mine ? 'me' : 'them')), b = $('div', 'm ' + (mine ? 'me' : 'them') + ' end');
      row.dataset.id = m.id; row.dataset.side = m.side;
      if (animate) row.classList.add('new');
      if (m.pend) b.classList.add('pend');
      b.append($('span', null, m.body), $('small', 'meta', hm(m.ts) + (mine ? (m.is_read ? ' \u00b7 Read' : m.pend ? ' \u00b7 Sending' : ' \u00b7 Sent') : '')));
      row.append(b);
      const prev = lastRow();
      if (prev && prev.dataset.side === m.side) { prev.firstChild.classList.remove('end'); row.classList.add('follow'); }
      return row;
    };
    const add = (m, animate) => { typing.hidden = true; const row = bubble(m, animate); msgs.insertBefore(row, typing); bottom(animate); return row; };
    const act = async (path) => { try { await api(base + path, {}); load(); } catch (e) { toast(e.message); } };

    const composer = () => {
      const f = $('form', 'composer'), i = $('input'), b = $('button', 'icon-btn send');
      i.placeholder = 'Message'; i.autocomplete = 'off'; i.enterKeyHint = 'send'; i.setAttribute('aria-label', 'Message');
      b.type = 'submit'; b.disabled = true; b.setAttribute('aria-label', 'Send'); b.innerHTML = ico('send', 22);
      i.oninput = () => {
        b.disabled = !i.value.trim();
        const c = CONTACT.test(i.value);
        if (hint.hidden === c) { hint.hidden = !c; if (c) haptic('select'); }
        if (Date.now() - lastTyped > 2000) { lastTyped = Date.now(); api(base + '/typing', {}).catch(() => {}); }
      };
      f.onsubmit = async (e) => {
        e.preventDefault();
        const v = i.value.trim(); if (!v) return;
        i.value = ''; b.disabled = true; hint.hidden = true; haptic('select');
        const row = add({ id: 't' + Date.now(), side, body: v, ts: new Date().toISOString(), pend: true }, true), p = { row, body: v };
        pend.push(p);
        try { await api(base + '/send', { body: v }); }
        catch (er) { row.remove(); pend.splice(pend.indexOf(p), 1); i.value = v; b.disabled = false; shake(f); toast(er.message); }
      };
      f.append(i, b);
      if (matchMedia('(hover:hover)').matches) setTimeout(() => i.focus(), 0);
      return f;
    };

    const render = () => {
      title.textContent = st.peer; resetSub();
      const na = avatar(st.peer); head.replaceChild(na, av); av = na;
      msgs.textContent = ''; msgs.append(typing); typing.hidden = true; pend.length = 0;
      st.messages.forEach((m) => msgs.insertBefore(bubble(m, false), typing)); bottom(false);
      revealBtn.hidden = st.status !== 'open';
      revealBtn.lastChild.textContent = st.reveal.done ? 'Contact' : 'Reveal';
      foot.textContent = ''; hint.hidden = true;
      if (st.status === 'open') foot.append(hint, composer());
      else if (st.status === 'pending' && side === 'o') {
        const bar = $('div', 'bar-note'), a = $('button', 'btn sm', 'Accept'), bl = $('button', 'btn soft sm', 'Block'), r = $('div', 'row');
        a.onclick = () => act('/accept'); bl.onclick = blockSheet; r.append(bl, a);
        bar.append($('span', 'fine', 'New request'), r); foot.append(bar);
      } else {
        const w = $('div');
        w.append($('p', 'fine center', st.status === 'pending' ? 'Waiting for the owner to accept your message\u2026' : 'This chat is closed.'));
        if (st.status === 'pending' && st.auto) w.append($('p', 'fine center', 'Auto-reply: ' + st.auto));
        foot.append(w);
      }
    };

    const fillReveal = () => {
      if (!rev) return;
      const r = st.reveal, body = rev.body; body.textContent = '';
      if (r.done) {
        if (r.contact) {
          const cp = $('button', 'btn wide', 'Copy');
          cp.onclick = async () => { try { await navigator.clipboard.writeText(r.contact); cp.textContent = 'Copied'; haptic('success'); pulse(cp); } catch (e) { toast("Couldn't copy that"); } };
          body.append($('p', 'fine', st.peer + ' shared:'), $('p', 'big mask-in', r.contact), cp, $('p', 'fine', 'This disappears 24 hours after the reveal.'));
        } else body.append($('p', null, 'The reveal window has expired.'));
        return;
      }
      const l = $('label', 'field', 'What will you share?'), i = $('input');
      i.maxLength = 100; i.placeholder = 'WhatsApp number or @handle'; l.append(i);
      const ok = $('button', 'btn wide', r.you ? 'Update contact' : 'Reveal contact');
      ok.onclick = async () => { try { await api(base + '/reveal', { agree: true, contact: i.value }); haptic('success'); await load(); } catch (e) { shake(l); toast(e.message); } };
      body.append($('p', null, 'You both need to agree. Nothing is shared until ' + st.peer + ' agrees too.'),
        $('p', 'fine', 'You: ' + (r.you ? 'ready' : 'not yet') + ' \u00b7 ' + st.peer + ': ' + (r.they ? 'ready' : 'waiting')),
        l, $('p', 'warn', "We can't verify what anyone shares. Only reveal when you trust them."), ok);
      if (r.you) { const w = $('button', 'btn soft wide', 'Withdraw'); w.onclick = async () => { try { await api(base + '/reveal', { agree: false }); await load(); } catch (e) { toast(e.message); } }; body.append(w); }
    };
    const openReveal = () => {
      if (!st) return;
      sheet('Contact reveal', (box) => { const body = $('div', 'rv-body'); box.append(body); rev = { body }; fillReveal(); }, { onClose: () => { rev = null; } });
    };
    revealBtn.onclick = openReveal; hintBtn.onclick = openReveal;

    const reportSheet = () => sheet('Report this chat', (box, close) => {
      let code = '';
      const note = $('textarea'), err = $('p', 'err', 'Pick a reason first'), list = $('div', 'opts');
      err.hidden = true; err.setAttribute('role', 'alert'); list.setAttribute('role', 'radiogroup'); list.setAttribute('aria-label', 'Reason');
      REASONS.forEach(([k, t]) => {
        const l = $('label', 'opt'), r = $('input'); r.type = 'radio'; r.name = 'reason'; r.value = k;
        r.onchange = () => { code = k; err.hidden = true; };
        l.append(r, $('span', null, t)); list.append(l);
      });
      note.rows = 3; note.maxLength = 300; note.placeholder = 'Add details (optional)'; note.setAttribute('aria-label', 'Details');
      const row = $('div', 'row'), ok = $('button', 'btn', 'Send report'), no = $('button', 'btn soft', 'Cancel');
      ok.onclick = async () => {
        if (!code) { err.hidden = false; shake(list); return; }
        try { await api(base + '/report', { code, note: note.value }); haptic('success'); close(); toast('Thanks. Moderators will take a look.'); }
        catch (e) { err.textContent = e.message; err.hidden = false; }
      };
      no.onclick = close; row.append(no, ok);
      box.append($('p', 'fine', "Reports go to the platform's moderators. They only see this chat because you reported it."), list, note, err, row);
    });
    const blockSheet = () => confirmSheet('Block this visitor?', "They won't be able to start new chats on any of your links.", 'Block', () => act('/block'));
    const deleteSheet = () => confirmSheet('Delete this chat?', "It disappears for both of you and can't be undone.", 'Delete chat', async () => {
      try { await api(base + '/leave', {}); onGone && onGone(); } catch (e) { toast(e.message); }
    });
    moreBtn.onclick = () => popover(moreBtn, (pop, close) => {
      menuItem(pop, close, 'flag', 'Report', reportSheet);
      if (side === 'o' && st && st.status !== 'blocked') menuItem(pop, close, 'block', 'Block visitor', blockSheet);
      if (side === 'v' && st && st.resume) menuItem(pop, close, 'link', 'Copy return link', async () => { try { await navigator.clipboard.writeText(location.origin + st.resume); toast('Return link copied. Keep it private.'); } catch (e) { toast("Couldn't copy that"); } });
      menuItem(pop, close, 'trash', 'Delete chat', deleteSheet, true);
    });

    const load = async () => {
      try {
        const was = st ? st.reveal.done : null;
        st = await api(base + '/state'); render();
        if (was === false && st.reveal.done) haptic('success');
        if (rev) fillReveal();
      } catch (e) { if (e.status === 404) onGone && onGone(); }
    };

    const es = new EventSource(base + '/stream'); window._es = es;
    es.onopen = load;
    es.onmessage = (ev) => {
      const d = JSON.parse(ev.data);
      if (d.t === 'msg') {
        if (d.side === side) {
          const k = pend.findIndex((p) => p.body === d.body);
          if (k > -1) { const p = pend.splice(k, 1)[0]; p.row.dataset.id = d.id; p.row.firstChild.classList.remove('pend'); p.row.querySelector('.meta').textContent = hm(d.ts) + ' \u00b7 Sent'; return; }
        }
        if (!msgs.querySelector('[data-id="' + d.id + '"]')) add({ id: d.id, side: d.side, body: d.body, ts: d.ts }, true);
        if (d.side !== side) {
          haptic('select'); api(base + '/state').catch(() => {});
          if (document.hidden && window.Notification && Notification.permission === 'granted') new Notification('New message', { body: 'Open the chat to read it' });
        }
      } else if (d.t === 'typing' && d.side !== side) {
        typing.hidden = false; bottom(true); sub.textContent = 'typing\u2026';
        clearTimeout(tTimer); tTimer = setTimeout(() => { typing.hidden = true; resetSub(); }, 3000);
      } else if (d.t === 'read' && d.s !== side) {
        msgs.querySelectorAll('.mrow.me .meta').forEach((m) => { m.textContent = m.textContent.replace('Sent', 'Read'); });
      } else if (d.t === 'reveal' || d.t === 'status') load();
      else if (d.t === 'gone') onGone && onGone();
    };
  };
})();
