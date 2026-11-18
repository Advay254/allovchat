// Behaviour added on top of the base UI kit (ui.js) and the owner app (app.js).
(function () {
  const $ = (t, c, x) => { const e = document.createElement(t); if (c) e.className = c; if (x != null) e.textContent = x; return e; };

  // 1. Menus follow the keyboard pattern their role promises: arrows, Home and End move between items, Escape returns focus.
  if (typeof window.popover === 'function') {
    const base = window.popover;
    window.popover = function (trig, build) {
      const close = base(trig, build);
      const pop = [...document.querySelectorAll('.pop')].pop();
      if (pop) {
        pop.addEventListener('keydown', (e) => {
          const items = [...pop.querySelectorAll('[role=menuitem]')];
          if (!items.length) return;
          const i = items.indexOf(document.activeElement);
          const go = (n) => { e.preventDefault(); items[(n + items.length) % items.length].focus(); };
          if (e.key === 'ArrowDown') go(i + 1);
          else if (e.key === 'ArrowUp') go(i - 1);
          else if (e.key === 'Home') go(0);
          else if (e.key === 'End') go(items.length - 1);
          else if (e.key === 'Escape') setTimeout(() => trig.focus(), 0);
          else if (e.key === 'Tab') close();
        });
      }
      return close;
    };
  }

  // 2. Settings: the privacy default explains that it works both ways, and accounts can be deleted after two confirmations.
  if (typeof window.loadSettings === 'function' && typeof window.sheet === 'function') {
    const orig = window.loadSettings;
    window.loadSettings = async function () {
      await orig.apply(this, arguments);
      const box = document.getElementById('settings');
      if (!box) return;
      box.querySelectorAll('.row-item b').forEach((b) => {
        if (b.textContent !== 'Privacy mode by default') return;
        const s = b.parentElement.querySelector('.fine');
        if (s) s.textContent = "Hide your typing, read receipts and online status in new chats. You won't see theirs either.";
      });
      const row = $('div', 'row-item static'), m = $('div', 'grow'), btn = $('button', 'btn leave', 'Delete account');
      m.append($('b', null, 'Delete account'), $('div', 'fine', 'Permanently remove your account, links and chats.'));
      btn.onclick = stepOne;
      row.append(m, btn); box.append(row);
    };
  }

  // First confirmation: say exactly what will be removed.
  function stepOne() {
    sheet('Delete your account?', (box, close) => {
      const ul = $('ul', 'del-list');
      ['Your account and every sign-in session', 'Every link you made, and every chat on those links, for both people', 'Your list of blocked guests'].forEach((t) => ul.append($('li', null, t)));
      const row = $('div', 'row'), no = $('button', 'btn soft', 'Cancel'), go = $('button', 'btn leave', 'Continue');
      no.onclick = close; go.onclick = () => { close(); setTimeout(stepTwo, 260); };
      row.append(no, go);
      box.append($('p', null, 'This permanently deletes:'), ul, $('p', 'fine', 'This cannot be undone. Nobody, including us, can bring it back.'), row);
    });
  }

  // Second confirmation: the typed word and the password. The server checks both again.
  function stepTwo() {
    sheet('Confirm deletion', (box, close) => {
      const l1 = $('label', 'field', 'Type DELETE to confirm'), t = $('input');
      t.autocomplete = 'off'; t.autocapitalize = 'characters'; t.spellcheck = false; l1.append(t);
      const l2 = $('label', 'field', 'Your password'), p = $('input');
      p.type = 'password'; p.autocomplete = 'current-password'; l2.append(p);
      const err = $('p', 'err'); err.hidden = true; err.setAttribute('role', 'alert');
      const row = $('div', 'row'), no = $('button', 'btn soft', 'Cancel'), go = $('button', 'btn leave', 'Delete my account');
      go.disabled = true;
      const ready = () => { go.disabled = t.value.trim() !== 'DELETE' || !p.value; };
      t.oninput = ready; p.oninput = ready;
      no.onclick = close;
      go.onclick = async () => {
        go.disabled = true; err.hidden = true;
        try {
          await api('/api/account/delete', { password: p.value, confirm: t.value.trim() });
          haptic('warn'); close(); toast('Your account has been deleted.');
          setTimeout(() => location.replace('/app'), 900);
        } catch (e) { err.textContent = e.message; err.hidden = false; ready(); shake(box); }
      };
      row.append(no, go);
      box.append($('p', null, 'This is the last step. Your account and everything on it will be deleted right away.'), l1, l2, err, row);
    });
  }
})();
