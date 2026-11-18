// Landing page demos: four short, scripted product walkthroughs drawn with plain DOM and CSS.
// They start when scrolled into view, pause when hidden or when the visitor taps Pause, and do not autoplay
// for people who ask for reduced motion (they see the finished scene and can replay it).
(() => {
  const root = document.getElementById('lp-demo');
  if (!root) return;
  const $ = (s) => root.querySelector(s);
  const body = $('.lp-body'), screen = $('.lp-screen'), nameEl = $('.lp-name'), subEl = $('.lp-sub'), avEl = $('.lp-av'), cap = $('.lp-cap'), pauseBtn = $('.lp-pause');
  const tabs = [...root.querySelectorAll('.lp-tab')];
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const STOP = {};
  let run = 0, cur = -1, auto = !reduce, paused = false, seen = false;
  const sleep = (ms) => new Promise((r) => setTimeout(r, reduce ? 0 : ms));
  const holding = () => paused || !seen || document.hidden;
  const make = (cls, html) => { const d = document.createElement('div'); d.className = cls; if (html != null) d.innerHTML = html; return d; }; // html is always a fixed string written here
  const trim = () => { while (body.children.length > 7) body.firstElementChild.remove(); };

  const ctx = (id) => {
    let tp = null;
    const alive = () => { if (id !== run) throw STOP; };
    return {
      wait: async (ms) => { await sleep(ms); while (holding() && id === run) await sleep(200); alive(); },
      head: (name, sub) => { nameEl.textContent = name; subEl.textContent = sub; avEl.textContent = name[0]; },
      sub: (t) => { subEl.textContent = t; },
      add: (cls, html) => { alive(); const e = make('lp-in ' + cls, html); body.append(e); trim(); return e; },
      msg: (side, text) => { alive(); const row = make('mrow new ' + side); row.append(make('m ' + side + ' end', text)); body.append(row); trim(); return row; },
      typing: (on) => {
        alive();
        if (tp) { tp.remove(); tp = null; }
        if (on) { tp = make('mrow them'); tp.append(make('m them typing', '<i></i><i></i><i></i>')); body.append(tp); trim(); }
      },
      tap: (el) => { el.classList.add('tap'); setTimeout(() => el.classList.remove('tap'), reduce ? 0 : 260); },
      toast: (t) => { const e = make('lp-toast', t); screen.append(e); setTimeout(() => e.remove(), reduce ? 2500 : 1700); },
    };
  };

  const D = [
    {
      cap: 'Name a link, share it anywhere, and accept each request before a chat starts.',
      run: async (c) => {
        c.head('Your links', 'Dashboard');
        const link = c.add('lp-card', '<small>Used bike post</small><b>.../l/k7x2m9</b><span class="lp-row"><span class="lp-b" data-k="copy">Copy link</span><span class="lp-b">QR code</span></span>');
        await c.wait(1400); c.tap(link.querySelector('[data-k=copy]')); c.toast('Link copied');
        await c.wait(1800); c.add('lp-note', 'Shared in a group post.');
        await c.wait(1500);
        const req = c.add('lp-card', '<small>New request</small><b>Quiet Fox</b><p>Hi, is the bike still for sale?</p><span class="lp-row"><span class="lp-b">Block</span><span class="lp-b p" data-k="ok">Accept</span></span>');
        await c.wait(1700); c.tap(req.querySelector('[data-k=ok]'));
        await c.wait(600); c.add('lp-note', 'Chat started. Everyone gets a fresh temporary name.');
        await c.wait(2400);
      },
    },
    {
      cap: 'Messages are encrypted in the browser. Only the two of you can read them.',
      run: async (c) => {
        c.head('Quiet Fox', 'Anonymous guest');
        c.add('lp-note', 'End-to-end encrypted. Only you and Quiet Fox can read this.');
        await c.wait(1000); c.msg('them', 'Hi, is the bike still for sale?');
        await c.wait(1200); c.msg('me', 'Yes, happy to answer anything.');
        await c.wait(900); c.typing(true); c.sub('typing\u2026'); await c.wait(1500); c.typing(false); c.sub('Anonymous guest');
        c.msg('them', 'Great. Is it still under warranty?');
        await c.wait(1300); c.msg('me', 'Two years left. Receipts included.');
        await c.wait(2600);
      },
    },
    {
      cap: 'Contact details appear once, and only after both people agree.',
      run: async (c) => {
        c.head('Quiet Fox', 'Anonymous guest');
        c.msg('them', 'Can we move this to WhatsApp?');
        await c.wait(1200); c.msg('me', 'Sure. Let us do a reveal.');
        await c.wait(1100);
        const rv = c.add('lp-card', '<small>Contact reveal</small><b>Nothing is shared until you both agree</b><span class="lp-rv"><span data-k="you">You: not yet</span><span data-k="they">Quiet Fox: not yet</span></span><span class="lp-row"><span class="lp-b p" data-k="go">Reveal contact</span></span>');
        await c.wait(1500); c.tap(rv.querySelector('[data-k=go]'));
        const you = rv.querySelector('[data-k=you]'); you.textContent = 'You: ready'; you.classList.add('ok');
        await c.wait(1700);
        const they = rv.querySelector('[data-k=they]'); they.textContent = 'Quiet Fox: ready'; they.classList.add('ok');
        await c.wait(1100);
        rv.className = 'lp-in lp-card done'; rv.innerHTML = '<small>You both agreed</small><b class="lp-big">@maya.rides</b><small>Shown once. Gone in 24 hours.</small>';
        await c.wait(2800);
      },
    },
    {
      cap: 'Hide your own activity and theirs disappears for you too. Fair for both sides.',
      run: async (c) => {
        c.head('Quiet Fox', 'Online');
        c.msg('them', 'Are you free tonight?');
        await c.wait(1000); c.typing(true); c.sub('typing\u2026');
        await c.wait(1500);
        const pv = c.add('lp-card', '<span class="lp-line"><b>Privacy mode</b><span class="lp-sw"><i></i></span></span><small>Hide your typing, read receipts and online status.</small>');
        await c.wait(1300); c.tap(pv.querySelector('.lp-sw')); pv.querySelector('.lp-sw').classList.add('on');
        c.typing(false); c.sub('Anonymous guest');
        await c.wait(1000); c.add('lp-note', 'Privacy is on. You do not see theirs, and they do not see yours.');
        await c.wait(2800);
      },
    },
  ];

  const select = (i) => {
    tabs.forEach((t, k) => { t.setAttribute('aria-selected', String(k === i)); t.tabIndex = k === i ? 0 : -1; });
    cap.textContent = D[i].cap;
  };
  const play = async (i) => {
    const id = ++run; cur = i; select(i); body.textContent = '';
    const c = ctx(id);
    try {
      await D[i].run(c);
      await c.wait(1400);
      if (reduce) return;
      play(auto ? (i + 1) % D.length : i);
    } catch (e) { if (e !== STOP) throw e; }
  };

  tabs.forEach((t, i) => {
    t.addEventListener('click', () => { auto = false; play(i); });
    t.addEventListener('keydown', (e) => {
      const k = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0;
      if (!k) return;
      e.preventDefault();
      const n = (i + k + tabs.length) % tabs.length; tabs[n].focus(); auto = false; play(n);
    });
  });
  pauseBtn.textContent = reduce ? 'Replay demo' : 'Pause demo';
  pauseBtn.addEventListener('click', () => {
    if (reduce) { play(cur < 0 ? 0 : cur); return; }
    paused = !paused;
    pauseBtn.textContent = paused ? 'Play demo' : 'Pause demo';
    pauseBtn.setAttribute('aria-pressed', String(paused));
  });

  const start = () => { if (cur < 0) play(0); };
  if ('IntersectionObserver' in window) {
    new IntersectionObserver((es) => { seen = es[0].isIntersecting; if (seen) start(); }, { threshold: 0.35 }).observe(root);
  } else { seen = true; start(); }
})();
