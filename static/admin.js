(function () {
  // Dangerous actions ask first, using the same sheet as the rest of the product.
  document.addEventListener('submit', (e) => {
    const f = e.target;
    if (f.dataset && f.dataset.confirm) {
      e.preventDefault();
      confirmSheet(f.dataset.title || 'Are you sure?', f.dataset.confirm, 'Confirm', () => { f.removeAttribute('data-confirm'); f.submit(); });
    }
  });
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
