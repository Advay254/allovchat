(function () {
  const c = document.getElementById('cd');
  let s = 60;
  setInterval(() => { s--; if (c) c.textContent = Math.max(s, 0); if (s <= 0) location.reload(); }, 1000);
})();
