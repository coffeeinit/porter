try { const t = localStorage.getItem('mvm-theme'); if (t) document.documentElement.dataset.theme = t; } catch (e) { /* ignore */ }
'use strict';
document.getElementById('f').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = document.getElementById('err');
  err.textContent = '';
  try {
    const r = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: document.getElementById('t').value }), credentials: 'same-origin' });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) { err.textContent = j.error || 'Sign-in failed'; return; }
    location.href = '/';
  } catch (x) { err.textContent = 'Cannot reach the server'; }
});
