(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };
  const hm = (ts) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const CONTACT = /(\+?\d[\d\s().-]{7,}\d)|(^|\s)@[A-Za-z0-9_.]{3,}|[\w.+-]+@[\w-]+\.[\w.]+|(wa\.me|t\.me)\/\S+/i;
  const REASONS = [['spam', 'Spam or ads'], ['harassment', 'Harassment or abuse'], ['scam', 'Scam or asking for money'], ['explicit', 'Sexual or explicit content'], ['other', 'Something else']];
  const QUICK = 'https://www.wikipedia.org/'; // where the emergency exit sends a guest, so nothing about the chat stays on screen
  const left = (ms) => { const m = Math.floor(ms / 6e4), h = Math.floor(m / 60), d = Math.floor(h / 24); return d >= 2 ? d + ' days' : h >= 2 ? h + ' hours' : h >= 1 ? '1 hour' : Math.max(m, 1) + ' minutes'; };
  const te = new TextEncoder();

  window.api = async (path, body) => {
    const r = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    let j = {};
    try { j = await r.json(); } catch (e) {}
    if (!r.ok) { const er = new Error(j.error || 'Something went wrong'); er.status = r.status; throw er; }
    return j;
  };

  // side: 'o' (link owner) or 'v' (guest). base: '/api/t/ID', '/api/m/ID' (saved to an account) or '/api/l/SLUG'
  window.mountChat = function (root, base, side, onGone, onBack) {
    if (window._es) window._es.close();
    clearInterval(window._exp);
    root.textContent = '';
    let st = null, key = null, tTimer = 0, lastTyped = 0, rev = null, online = false, typingOn = false, defDone = false, unseen = 0;
    const pend = [], failed = [], viaLink = base.indexOf('/api/l/') === 0, saved = base.indexOf('/api/m/') === 0, other = side === 'o' ? 'v' : 'o';
    // Privacy mode works both ways: someone who hides their own activity doesn't see the other person's either.
    const quiet = () => !!(st && st.privacy);
    const FAIR = "You won't see their typing, read receipts or online status either.";

    const head = $('div', 'chat-head');
    if (onBack) { const b = $('button', 'icon-btn back'); b.innerHTML = ico('back', 24); b.setAttribute('aria-label', 'Back'); b.onclick = onBack; head.append(b); }
    let av = avatar('?', 40);
    const who = $('div', 'grow'), title = $('div', 'chat-title'), sub = $('div', 'chat-sub idle'); who.append(title, sub);
    const revealBtn = $('button', 'btn soft sm reveal-btn'); revealBtn.innerHTML = ico('user', 18) + '<span class="lbl">Reveal</span>'; revealBtn.hidden = true; revealBtn.setAttribute('aria-label', 'Contact reveal');
    const leaveBtn = $('button', 'btn leave'); leaveBtn.innerHTML = ico('exit', 18) + '<span class="lbl">Leave</span>'; leaveBtn.setAttribute('aria-label', 'Leave this chat now');
    const moreBtn = $('button', 'icon-btn'); moreBtn.innerHTML = ico('more'); moreBtn.setAttribute('aria-label', 'More options'); moreBtn.setAttribute('aria-haspopup', 'menu');
    head.append(av, who, revealBtn, leaveBtn, moreBtn);

    // One compact strip: encryption on the first line, time left (and privacy state) on the second.
    const note = $('div', 'e2e-note'); note.hidden = true;
    const nIc = $('span'), nTxt = $('div', 'grow'), n1 = $('div', 'n1'), n2 = $('div', 'n2'), nVer = $('button', 'linkbtn', 'Verify');
    nTxt.append(n1, n2); nVer.hidden = true; nVer.onclick = () => verifySheet(); note.append(nIc, nTxt, nVer);
    const expiry = $('div', 'expiry'), exIcon = $('span'), exTxt = $('span'); exIcon.innerHTML = ico('clock', 18); expiry.append(exIcon, exTxt); expiry.hidden = true; expiry.setAttribute('role', 'status');
    const conn = $('div', 'conn'); conn.hidden = true; conn.setAttribute('role', 'status');
    const msgs = $('div', 'msgs'), foot = $('div', 'chat-foot');
    msgs.setAttribute('role', 'log'); msgs.setAttribute('aria-live', 'polite'); msgs.setAttribute('aria-label', 'Messages');
    const typing = $('div', 'mrow them'), tb = $('div', 'm them typing'); tb.append($('i'), $('i'), $('i')); typing.append(tb); typing.hidden = true;
    const jumpWrap = $('div', 'jumpwrap'), jump = $('button', 'jump'), badge = $('span', 'badge');
    jump.type = 'button'; jump.hidden = true; jump.setAttribute('aria-label', 'Jump to the latest message'); jump.innerHTML = ico('back', 20); badge.hidden = true; jump.append(badge); jumpWrap.append(jump);
    const hint = $('div', 'hint'), hintBtn = $('button', 'linkbtn', 'Reveal now'); hint.hidden = true; hint.setAttribute('role', 'status');
    hint.append($('span', null, 'Looks like contact info. Want to reveal this properly?'), hintBtn);
    root.append(head, note, expiry, conn, msgs, foot);
    for (let i = 0; i < 3; i++) { const r = $('div', 'mrow ' + (i === 1 ? 'me' : 'them')), s = $('div', 'sk'); s.style.cssText = 'height:42px;border-radius:20px;width:' + (45 + i * 12) + '%'; r.append(s); msgs.append(r); }

    // ---- encryption helpers ----
    const setupKey = async () => {
      key = null;
      if (!st.e2e || !window.e2e.ok) return;
      const mine = side === 'o' ? e2e.K.get('link:' + st.link_id) : e2e.K.get('g:' + st.slug), theirs = side === 'o' ? st.gpub : st.lpub;
      if (!mine || !mine.priv || !theirs) return;
      try { key = await e2e.shared(mine.priv, theirs); } catch (e) { key = null; }
    };
    const dec = async (body, s, ctx) => {
      if (!st.e2e) return body;
      if (!key) return '[Encrypted. The key isn\'t on this device]';
      try { return await e2e.open(key, body, s, ctx); } catch (e) { return '[Couldn\'t decrypt this message]'; }
    };
    const timeLine = () => st ? 'Ends in ' + left(Math.max(new Date(st.expires_at) - Date.now(), 0)) : '';
    const paintNote2 = () => { n2.textContent = timeLine(); if (quiet()) n2.append($('span', 'pv', ' \u00b7 Privacy on (works both ways)')); };
    const renderNote = () => {
      const ok = !!(st.e2e && key);
      note.hidden = false; note.classList.toggle('ok', ok);
      nIc.innerHTML = ico(ok ? 'lock' : 'shield', 16);
      n1.textContent = '';
      if (ok) n1.append($('span', 'long', 'End-to-end encrypted. Only you and the other person can read this chat.'), $('span', 'short', 'End-to-end encrypted'));
      else if (st.e2e) n1.textContent = "This chat is encrypted, but its key isn't on this device. Open your return link on the device where you started it.";
      else n1.textContent = "End-to-end encryption isn't active in this chat.";
      nVer.hidden = !ok;
      paintNote2();
    };
    const verifySheet = async () => {
      let code = '';
      try { code = await e2e.safety(st.lpub, st.gpub); } catch (e) { toast("Couldn't work out the code"); return; }
      sheet('Verify encryption', (box, close) => {
        const b = $('button', 'btn wide', 'Done'); b.onclick = close;
        box.append($('p', null, 'If this code matches on both screens, nobody is sitting in the middle of this chat.'), $('p', 'big', code), $('p', 'fine', 'Compare it somewhere other than this chat if you can, for example by voice. The code changes if either key changes.'), b);
      });
    };

    // ---- scrolling: stay at the bottom unless the reader has scrolled up ----
    const near = () => msgs.scrollHeight - msgs.scrollTop - msgs.clientHeight < 160;
    const bottom = (smooth) => msgs.scrollTo({ top: msgs.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
    const paintJump = () => { jump.hidden = near(); badge.hidden = !unseen; badge.textContent = unseen > 9 ? '9+' : String(unseen); };
    msgs.addEventListener('scroll', () => { if (near()) unseen = 0; paintJump(); }, { passive: true });
    jump.onclick = () => { unseen = 0; bottom(true); };

    const refresh = () => {
      if (!st) return;
      const ms = new Date(st.expires_at) - Date.now();
      if (ms <= 0) { onGone && onGone(); return; }
      const live = quiet() ? '' : typingOn ? 'typing\u2026' : online ? 'Online' : '';
      sub.textContent = live || (side === 'o' ? 'Anonymous guest' : 'Anonymous chat');
      sub.classList.toggle('idle', !live);
      paintNote2();
      if (ms <= 12 * 3600e3) { expiry.hidden = false; exTxt.textContent = 'This conversation will be deleted in ' + left(ms) + '. Wrap up, and share contact details now if you both want to.'; } else expiry.hidden = true;
    };
    const lastRow = () => { const n = typing.previousElementSibling; return n && n.classList.contains('mrow') ? n : null; };
    const bubble = (m, animate) => {
      const mine = m.side === side, row = $('div', 'mrow ' + (mine ? 'me' : 'them')), b = $('div', 'm ' + (mine ? 'me' : 'them') + ' end');
      row.dataset.id = m.id; row.dataset.side = m.side;
      if (animate) row.classList.add('new');
      if (m.pend) b.classList.add('pend');
      b.append($('span', null, m.text), $('small', 'meta', hm(m.ts) + (mine ? (m.is_read && !quiet() ? ' \u00b7 Read' : m.pend ? ' \u00b7 Sending' : ' \u00b7 Sent') : '')));
      row.append(b);
      const prev = lastRow();
      if (prev && prev.dataset.side === m.side) { prev.firstChild.classList.remove('end'); row.classList.add('follow'); }
      return row;
    };
    const add = (m, animate) => {
      const stick = near() || m.side === side;
      typing.hidden = true;
      const row = bubble(m, animate); msgs.insertBefore(row, typing);
      if (stick) bottom(animate); else { unseen++; paintJump(); }
      return row;
    };
    const act = async (path) => { try { await api(base + path, {}); load(); } catch (e) { toast(e.message); } };
    // Many incoming messages in a row cause one "mark as read" request, not one each.
    const markRead = debounce(() => api(base + '/state').catch(() => {}), 700);

    // ---- sending: a message that fails stays on screen with Retry, and retrying reuses its key so it is stored once ----
    const setMeta = (p, t) => { p.row.querySelector('.meta').textContent = hm(p.ts) + ' \u00b7 ' + t; };
    const drop = (p) => { let k = pend.indexOf(p); if (k > -1) pend.splice(k, 1); k = failed.indexOf(p); if (k > -1) failed.splice(k, 1); };
    const markFailed = (p) => {
      const b = p.row.firstChild; b.classList.remove('pend'); b.classList.add('failed'); setMeta(p, 'Not sent');
      if (!p.row.querySelector('.fail-actions')) {
        const a = $('div', 'fail-actions'), r = $('button', null, 'Retry'), d = $('button', null, 'Delete');
        r.type = 'button'; d.type = 'button';
        r.onclick = () => trySend(p); d.onclick = () => { p.row.remove(); drop(p); };
        a.append(r, d); b.append(a);
      }
      if (failed.indexOf(p) < 0) failed.push(p);
    };
    const trySend = async (p) => {
      const fa = p.row.querySelector('.fail-actions'); if (fa) fa.remove();
      const b = p.row.firstChild; b.classList.remove('failed'); b.classList.add('pend'); setMeta(p, 'Sending');
      if (pend.indexOf(p) < 0) pend.push(p);
      try { await api(base + '/send', { body: p.ct, cid: p.cid }); const k = failed.indexOf(p); if (k > -1) failed.splice(k, 1); }
      catch (er) { markFailed(p); toast(er.message); }
    };

    const composer = () => {
      const f = $('form', 'composer'), i = $('textarea'), b = $('button', 'icon-btn send'), cnt = $('span', 'count');
      const lim = { mx: 0, words: [] };
      const fine = matchMedia('(hover:hover) and (pointer:fine)').matches; // Enter sends on a desktop keyboard; on a phone it starts a new line
      i.rows = 1; i.placeholder = 'Message'; i.autocomplete = 'off'; i.enterKeyHint = fine ? 'send' : 'enter'; i.setAttribute('aria-label', 'Message');
      b.type = 'submit'; b.disabled = true; b.setAttribute('aria-label', 'Send'); b.innerHTML = ico('send', 22);
      cnt.hidden = true; cnt.setAttribute('aria-hidden', 'true');
      (window._limits = window._limits || api('/api/limits').catch(() => ({}))).then((l) => { if (l && l.max_msg_len) { lim.mx = l.max_msg_len; i.maxLength = lim.mx; } if (l && l.blocked_words) lim.words = l.blocked_words; });
      const fit = () => { i.style.height = 'auto'; i.style.height = Math.min(i.scrollHeight, 140) + 'px'; };
      const blocked = (s) => { const t = s.toLowerCase(); return lim.words.some((w) => t.indexOf(w) > -1); };
      i.oninput = () => {
        fit();
        b.disabled = !i.value.trim();
        const rem = lim.mx ? lim.mx - i.value.length : 0;
        cnt.hidden = !(lim.mx && rem <= 100); cnt.textContent = rem;
        const c = CONTACT.test(i.value);
        if (hint.hidden === c) { hint.hidden = !c; if (c) haptic('select'); }
        if (!quiet() && Date.now() - lastTyped > 2000) { lastTyped = Date.now(); api(base + '/typing', {}).catch(() => {}); }
      };
      i.onkeydown = (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && fine) { e.preventDefault(); if (i.value.trim()) f.requestSubmit(); } };
      f.onsubmit = async (e) => {
        e.preventDefault();
        const v = i.value.trim(); if (!v) return;
        if (blocked(v)) { shake(f); toast("That message can't be sent here. Please reword it."); return; }
        i.value = ''; fit(); b.disabled = true; cnt.hidden = true; hint.hidden = true; haptic('select');
        // The message gets its own key (cid) before it is sent. If the request is retried, the server stores it only once.
        const cid = e2e.rand(16), ts = new Date().toISOString();
        let ct = v;
        try { if (st.e2e) ct = await e2e.seal(key, v, side); } catch (er) { i.value = v; fit(); toast("Couldn't encrypt that message"); return; }
        const row = add({ id: 't' + cid, side, body: ct, text: v, ts, pend: true }, true), p = { row, cid, ct, ts };
        pend.push(p);
        try { await api(base + '/send', { body: ct, cid }); }
        catch (er) { markFailed(p); toast(er.message); }
      };
      f.append(i, cnt, b);
      if (fine) setTimeout(() => i.focus(), 0);
      return f;
    };

    const render = () => {
      title.textContent = st.peer; online = !!st.online && !st.privacy;
      const na = avatar(st.peer, 40); head.replaceChild(na, av); av = na;
      renderNote();
      msgs.textContent = ''; msgs.append(typing, jumpWrap); typing.hidden = true; pend.length = 0; unseen = 0;
      st.messages.forEach((m) => msgs.insertBefore(bubble(m, false), typing));
      failed.forEach((p) => { msgs.insertBefore(p.row, typing); pend.push(p); }); // unsent messages stay put across a refresh
      bottom(false); paintJump();
      revealBtn.hidden = st.status !== 'open';
      revealBtn.lastChild.textContent = st.reveal.done ? 'Contact' : 'Reveal';
      foot.textContent = ''; hint.hidden = true;
      if (st.status === 'open') foot.append(hint, st.e2e && !key ? $('p', 'fine center', "You can't send or read messages here without this chat's key.") : composer());
      else if (st.status === 'pending' && side === 'o') {
        const bar = $('div', 'bar-note'), a = $('button', 'btn sm', 'Accept'), bl = $('button', 'btn soft sm', 'Block'), r = $('div', 'row');
        a.onclick = () => act('/accept'); bl.onclick = blockSheet; r.append(bl, a);
        bar.append($('span', 'fine', 'New request'), r); foot.append(bar);
      } else {
        const w = $('div');
        w.append($('p', 'fine center', st.status === 'pending' ? 'Waiting for them to accept your message\u2026' : 'This chat is closed.'));
        if (st.status === 'pending' && st.auto) w.append($('p', 'fine center', 'Auto-reply: ' + st.auto));
        foot.append(w);
      }
      refresh();
    };

    const fillReveal = () => {
      if (!rev) return;
      const r = st.reveal, body = rev.body; body.textContent = '';
      if (r.done) {
        if (r.contact) {
          const cp = $('button', 'btn wide', 'Copy');
          cp.onclick = async () => { try { await navigator.clipboard.writeText(r.text); cp.textContent = 'Copied'; haptic('success'); pulse(cp); } catch (e) { toast("Couldn't copy that"); } };
          body.append($('p', 'fine', st.peer + ' shared:'), $('p', 'big mask-in', r.text), cp, $('p', 'fine', 'This disappears 24 hours after the reveal.'));
        } else body.append($('p', null, 'The reveal window has expired.'));
        return;
      }
      const l = $('label', 'field', 'What will you share?'), i = $('input');
      i.maxLength = 100; i.placeholder = 'WhatsApp number or @handle'; l.append(i);
      const ok = $('button', 'btn wide', r.you ? 'Update contact' : 'Reveal contact');
      ok.onclick = async () => {
        const v = i.value.trim();
        if (!v) { shake(l); return; }
        try {
          const ct = st.e2e ? await e2e.seal(key, v, side, 'c') : v;
          await api(base + '/reveal', { agree: true, contact: ct }); haptic('success'); await load();
        } catch (e) { shake(l); toast(e.message || "Couldn't send that"); }
      };
      body.append($('p', null, 'You both need to agree. Nothing is shared until ' + st.peer + ' agrees too.'),
        $('p', 'fine', 'You: ' + (r.you ? 'ready' : 'not yet') + ' \u00b7 ' + st.peer + ': ' + (r.they ? 'ready' : 'waiting')),
        l, $('p', 'warn', "We can't verify what anyone shares. Only reveal when you trust them."), ok);
      if (r.you) { const w = $('button', 'btn soft wide', 'Withdraw'); w.onclick = async () => { try { await api(base + '/reveal', { agree: false }); await load(); } catch (e) { toast(e.message); } }; body.append(w); }
    };
    const openReveal = () => {
      if (!st) return;
      if (st.e2e && !key) { toast("This chat's key isn't on this device"); return; }
      sheet('Contact reveal', (box) => { const body = $('div', 'rv-body'); box.append(body); rev = { body }; fillReveal(); }, { onClose: () => { rev = null; } });
    };
    revealBtn.onclick = openReveal; hintBtn.onclick = openReveal;

    // Emergency exit: no questions, no confirmation. The chat is deleted for both people.
    const leaveNow = async () => {
      haptic('warn');
      try { await api(base + '/leave', {}); } catch (e) {}
      if (viaLink) { if (st) e2e.K.del('g:' + st.slug); location.replace(QUICK); return; }
      onGone && onGone();
    };
    leaveBtn.onclick = leaveNow;

    // Called when privacy turns on, from the switch or from the saved default: hide everything we were showing about them.
    const goQuiet = () => {
      typingOn = false; typing.hidden = true; online = false;
      msgs.querySelectorAll('.mrow.me .meta').forEach((m) => { m.textContent = m.textContent.replace('Read', 'Sent'); });
      refresh();
    };
    const privacySheet = () => sheet('Privacy mode', (box, close) => {
      const row = $('div', 'row'), t = $('div', 'grow'); t.append($('b', null, 'Hide my activity'), $('div', 'fine', 'No typing indicator, read receipts or online status from you in this chat.'));
      const sw = switchEl(!!st.privacy, async (v) => {
        try {
          await api(base + '/privacy', { on: v }); st.privacy = v;
          if (v) { goQuiet(); toast('Privacy is on. ' + FAIR); }
          else { refresh(); load(); }
          return true;
        } catch (e) { toast(e.message); return false; }
      }, 'Privacy mode');
      row.append(t, sw);
      const fair = $('p', 'fair'); fair.innerHTML = ico('eyeoff', 18);
      fair.append($('span', null, "It works both ways, to keep things fair. While it's on, you also won't see their typing, read receipts or online status, even if they haven't turned privacy on."));
      const done = $('button', 'btn soft wide', 'Done'); done.onclick = close;
      box.append(row, fair, done);
    });
    const retentionSheet = () => sheet('Delete this chat sooner', (box, close) => {
      const ms = new Date(st.expires_at) - Date.now(), opts = [[1, 'In 1 hour'], [6, 'In 6 hours'], [24, 'In 24 hours'], [72, 'In 3 days']].filter(([h]) => h * 3600e3 < ms);
      if (!opts.length) { box.append($('p', null, 'This chat already ends in less than an hour.')); return; }
      box.append($('p', 'fine', 'Chats always end within 7 days. Either of you can end this one sooner, but nobody can extend it.'));
      const list = $('div', 'opts');
      opts.forEach(([h, t]) => { const b = $('button', 'btn soft wide', t); b.onclick = async () => { try { await api(base + '/retention', { hours: h }); haptic('success'); close(); toast('Okay. This chat will be deleted ' + t.toLowerCase() + '.'); load(); } catch (e) { toast(e.message); } }; list.append(b); });
      box.append(list);
    });
    const reportSheet = () => sheet('Report this chat', (box, close) => {
      let code = '';
      const cid = e2e.rand(16), note2 = $('textarea'), err = $('p', 'err', 'Pick a reason first'), list = $('div', 'opts'), ev = $('input');
      err.hidden = true; err.setAttribute('role', 'alert'); list.setAttribute('role', 'radiogroup'); list.setAttribute('aria-label', 'Reason');
      REASONS.forEach(([k, t]) => {
        const l = $('label', 'opt'), r = $('input'); r.type = 'radio'; r.name = 'reason'; r.value = k;
        r.onchange = () => { code = k; err.hidden = true; };
        l.append(r, $('span', null, t)); list.append(l);
      });
      note2.rows = 3; note2.maxLength = 300; note2.placeholder = 'Add details (optional)'; note2.setAttribute('aria-label', 'Details');
      const row = $('div', 'row'), ok = $('button', 'btn', 'Send report'), no = $('button', 'btn soft', 'Cancel');
      let evBox = null;
      if (st.e2e) { evBox = $('label', 'opt'); ev.type = 'checkbox'; ev.checked = true; evBox.append(ev, $('span', null, 'Attach the last 8 messages so our team can see what happened')); }
      ok.onclick = async () => {
        if (!code) { err.hidden = false; shake(list); return; }
        const evidence = st.e2e && ev.checked ? st.messages.slice(-8).map((m) => (m.side === side ? 'You' : 'Them') + ': ' + m.text).join('\n') : '';
        try { await api(base + '/report', { code, note: note2.value, evidence, cid }); haptic('success'); close(); toast('Thanks. Our team will take a look.'); }
        catch (e) { err.textContent = e.message; err.hidden = false; }
      };
      no.onclick = close; row.append(no, ok);
      box.append($('p', 'fine', st.e2e ? 'Reports go to our team. This chat is end-to-end encrypted, so we can only see what you choose to attach.' : 'Reports go to our team. We only see this chat because you reported it.'), list, note2);
      if (evBox) box.append(evBox);
      box.append(err, row);
    });
    const blockSheet = () => confirmSheet('Block this guest?', "They won't be able to start new chats on any of your links.", 'Block', () => act('/block'));
    moreBtn.onclick = () => popover(moreBtn, (pop, close) => {
      menuItem(pop, close, 'flag', 'Report', reportSheet);
      menuItem(pop, close, 'eyeoff', quiet() ? 'Privacy mode: on' : 'Privacy mode', privacySheet);
      menuItem(pop, close, 'clock', 'Delete sooner', retentionSheet);
      if (st && st.e2e && key) menuItem(pop, close, 'shield', 'Verify encryption', verifySheet);
      if (side === 'o' && st && st.status !== 'blocked') menuItem(pop, close, 'block', 'Block guest', blockSheet);
      if (viaLink && st && st.resume) menuItem(pop, close, 'link', 'Copy return link', async () => {
        const ent = e2e.K.get('g:' + st.slug), frag = ent ? '#k=' + e2e.b64(te.encode(JSON.stringify(ent))) : '';
        try { await navigator.clipboard.writeText(location.origin + st.resume + frag); toast('Return link copied. It holds your key, so keep it private.'); } catch (e) { toast("Couldn't copy that"); }
      });
      if (saved) menuItem(pop, close, 'user', 'Remove from my account', async () => { try { await api(base + '/unclaim', {}); onGone && onGone(); } catch (e) { toast(e.message); } });
    });

    const load = async () => {
      try {
        const was = st ? st.reveal.done : null;
        const s = await api(base + '/state');
        const firstLoad = !st; st = s;
        if (firstLoad || (st.e2e && !key)) await setupKey();
        await Promise.all(st.messages.map(async (m) => { m.text = await dec(m.body, m.side); }));
        st.reveal.text = st.reveal.contact ? await dec(st.reveal.contact, other, 'c') : '';
        render();
        if (was === false && st.reveal.done) haptic('success');
        if (rev) fillReveal();
        if (!defDone) {
          defDone = true;
          if (localStorage.getItem('privacy') === '1' && !st.privacy) {
            api(base + '/privacy', { on: true }).then(() => { st.privacy = true; goQuiet(); toast('Privacy mode is on by default. ' + FAIR); }).catch(() => {});
          }
        }
      } catch (e) { if (e.status === 404) onGone && onGone(); }
    };
    window._exp = setInterval(refresh, 30000);

    const es = new EventSource(base + '/stream'); window._es = es;
    es.onopen = () => { conn.hidden = true; load(); };
    es.onerror = () => { conn.hidden = false; conn.textContent = navigator.onLine ? 'Reconnecting\u2026' : "You're offline. We'll reconnect when you're back."; };
    es.onmessage = async (ev) => {
      const d = JSON.parse(ev.data);
      if (d.t === 'msg') {
        if (d.side === side) {
          const k = d.cid ? pend.findIndex((p) => p.cid === d.cid) : -1;
          if (k > -1) { const p = pend[k]; drop(p); p.row.dataset.id = d.id; p.row.firstChild.classList.remove('pend', 'failed'); const fa = p.row.querySelector('.fail-actions'); if (fa) fa.remove(); setMeta(p, 'Sent'); return; }
        }
        if (msgs.querySelector('[data-id="' + d.id + '"]')) return;
        add({ id: d.id, side: d.side, body: d.body, text: await dec(d.body, d.side), ts: d.ts }, true);
        if (d.side !== side) {
          haptic('select'); markRead();
          if (document.hidden && window.Notification && Notification.permission === 'granted') new Notification('New message', { body: 'Open the chat to read it' });
        }
      } else if (d.t === 'typing' && d.side !== side) {
        if (quiet()) return;
        typingOn = true; typing.hidden = false; if (near()) bottom(true); refresh();
        clearTimeout(tTimer); tTimer = setTimeout(() => { typingOn = false; typing.hidden = true; refresh(); }, 3000);
      } else if (d.t === 'presence' && d.side !== side) {
        if (quiet()) return;
        online = d.on; refresh();
      } else if (d.t === 'read' && d.s !== side) {
        if (quiet()) return;
        msgs.querySelectorAll('.mrow.me .meta').forEach((m) => { m.textContent = m.textContent.replace('Sent', 'Read'); });
      } else if (d.t === 'reveal' || d.t === 'status' || d.t === 'retention') load();
      else if (d.t === 'gone') onGone && onGone();
    };
  };
})();
