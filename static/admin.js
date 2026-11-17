(function () {
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  // Dangerous actions ask first, using the same sheet as the rest of the product.
  document.addEventListener('submit', (e) => {
    const f = e.target;
    if (f.dataset && f.dataset.confirm) {
      e.preventDefault();
      confirmSheet(f.dataset.title || 'Are you sure?', f.dataset.confirm, 'Confirm', () => { f.removeAttribute('data-confirm'); f.submit(); });
    }
  });
  // Phone navigation: a hamburger button opens a dropdown. Tapping outside, pressing Escape or choosing a section closes it.
  const side = document.getElementById('adm-side'), burger = document.getElementById('adm-burger');
  if (side && burger) {
    const set = (open) => {
      side.classList.toggle('open', open);
      burger.setAttribute('aria-expanded', String(open));
      burger.setAttribute('aria-label', open ? 'Close menu' : 'Open menu');
    };
    burger.addEventListener('click', () => set(!side.classList.contains('open')));
    document.addEventListener('pointerdown', (e) => { if (side.classList.contains('open') && !side.contains(e.target)) set(false); });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && side.classList.contains('open')) { set(false); burger.focus(); } });
    matchMedia('(min-width: 900px)').addEventListener('change', (e) => { if (e.matches) set(false); });
  }
  // Reports: only one is open at a time. The browser already does this where it supports <details name>; this covers the rest.
  const reps = document.querySelectorAll('details.rep');
  reps.forEach((d) => d.addEventListener('toggle', () => {
    if (!d.open) return;
    reps.forEach((o) => { if (o !== d && o.open) o.open = false; });
    // Closing the previous one can push this one under the sticky bar on a phone, so bring it back into view.
    if (matchMedia('(max-width: 899px)').matches) {
      const bar = side ? side.offsetHeight : 0, top = d.getBoundingClientRect().top;
      if (top < bar + 8) scrollBy({ top: top - bar - 12, behavior: reduce ? 'auto' : 'smooth' });
    }
  }));
  // After too many wrong passwords the sign-in button stays off until the timer ends.
  const b = document.getElementById('lbtn'), t = document.getElementById('lwait');
  if (b && t && b.dataset.wait) {
    let s = +b.dataset.wait; b.disabled = true;
    const tick = () => {
      if (s <= 0) { b.disabled = false; t.textContent = ''; return; }
      t.textContent = 'Try again in ' + Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0'); s--; setTimeout(tick, 1000);
    };
    tick();
  }
  const fl = document.querySelector('[data-flash]');
  if (fl && window.toast) toast(fl.dataset.flash);
})();
