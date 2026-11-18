'use strict';
/* MicroVM Maker UI - dependency-free, CSP-safe (no inline handlers, every dynamic value escaped). */
(() => {
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const READONLY = document.body.dataset.readonly === '1';
let CSRF = null;
const S = { rsets: [], sources: [], vms: [], images: [], store: [], health: null, jobs: [], hist: {}, term: {}, audit: [], watching: null };

/* ---------------------------------------------------------------- helpers */
async function api(path, opts = {}) {
  const o = { method: 'GET', credentials: 'same-origin', headers: {}, ...opts };
  if (o.json !== undefined) { o.body = JSON.stringify(o.json); o.headers['Content-Type'] = 'application/json'; delete o.json; }
  if (o.method !== 'GET') o.headers['X-CSRF-Token'] = CSRF || '';
  const r = await fetch('/api' + path, o);
  if (r.status === 401) { location.href = '/login'; throw new Error('signed out'); }
  const ct = r.headers.get('content-type') || '';
  const data = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error((data && data.error) || String(data) || r.statusText);
  return data;
}
function toast(msg, bad) {
  const d = document.createElement('div');
  d.className = 'toast' + (bad ? ' bad' : '');
  d.textContent = msg;
  $('#toasts').appendChild(d);
  setTimeout(() => d.remove(), bad ? 9000 : 3500);
}
async function busy(btn, fn) {
  if (btn) { btn.classList.add('busy'); btn.disabled = true; }
  try { return await fn(); } catch (e) { toast(e.message, true); } finally { if (btn) { btn.classList.remove('busy'); btn.disabled = false; } }
}
const fmtB = (n) => { n = +n || 0; const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let i = 0; while (n >= 1024 && i < 4) { n /= 1024; i++; } return (i ? n.toFixed(n < 10 ? 1 : 0) : n.toFixed(0)) + ' ' + u[i]; };
const fmtDur = (s) => { s = Math.max(0, Math.round(s || 0)); const d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60); return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : m ? `${m}m ${s % 60}s` : `${s}s`; };
const ago = (iso) => { if (!iso) return '–'; const s = (Date.now() - new Date(iso)) / 1000; return s < 60 ? 'just now' : fmtDur(s) + ' ago'; };
const vmById = (id) => S.vms.find((v) => v.id === id);
const imgByName = (n) => S.images.find((i) => i.name === n);
function spark(vals, max) {
  if (!vals || vals.length < 2) return '<svg class="spark" viewBox="0 0 100 34" preserveAspectRatio="none"></svg>';
  const m = max || Math.max(1, ...vals), n = vals.length;
  const pts = vals.map((v, i) => [(i / (n - 1)) * 100, 32 - Math.min(1, v / m) * 30]);
  const l = pts.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join('');
  return `<svg class="spark" viewBox="0 0 100 34" preserveAspectRatio="none"><path class="a" d="${l}L100 34L0 34Z"/><path class="l" d="${l}" vector-effect="non-scaling-stroke"/></svg>`;
}
const led = (st) => `<span class="led ${esc(st)}" title="${esc(st)}"></span>`;
const tagFor = (st) => ({ running: 'ok', paused: 'warn', starting: 'warn', error: 'bad' }[st] || '');
const ro = READONLY ? 'disabled' : '';
const ICONS = {
  server: '<rect x="3" y="3" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 6.5h.01M7 17.5h.01"/>',
  layers: '<path d="m12 3 9 5-9 5-9-5 9-5Z"/><path d="m3 13 9 5 9-5"/>',
  box: '<path d="M21 8 12 3 3 8v8l9 5 9-5V8Z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/>',
  memory: '<rect x="3" y="7" width="18" height="10" rx="2"/><path d="M7 11v2M11 11v2M15 11v2M6 17v2M10 17v2M14 17v2M18 17v2"/>',
  disk: '<rect x="3" y="6" width="18" height="12" rx="2"/><path d="M7 14h.01M11 14h6"/>',
  net: '<path d="M7 7v10M7 17l-3-3M7 17l3-3M17 17V7M17 7l-3 3M17 7l3 3"/>',
  down: '<path d="M12 5v14M6 13l6 6 6-6"/>', up: '<path d="M12 19V5M6 11l6-6 6 6"/>',
  lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>', chev: '<path d="m6 9 6 6 6-6"/>', ext: '<path d="M7 17 17 7M8 7h9v9"/>',
  sliders: '<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>',
  camera: '<path d="M4 8h3l2-3h6l2 3h3v11H4V8Z"/><circle cx="12" cy="13" r="3.5"/>',
  terminal: '<path d="m4 17 6-5-6-5M12 19h8"/>', file: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9l-6-6Z"/><path d="M14 3v6h6"/>',
  console: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M13 15h4"/>', gauge: '<path d="M12 14l4-4"/><path d="M3.3 17a9 9 0 1 1 17.4 0"/>',
  activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>', clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>', search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  git: '<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="9" r="2"/><path d="M6 7v10M18 11c0 4-6 3-12 6"/>', alert: '<path d="M12 3 2 20h20L12 3Z"/><path d="M12 10v4M12 17h.01"/>',
  check: '<path d="m5 12 5 5 9-10"/>', play: '<path d="M7 4v16l13-8L7 4Z"/>', dots: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
};
const ico = (n) => `<svg class="i" viewBox="0 0 24 24">${ICONS[n] || ''}</svg>`;
const niceMax = (v) => { if (!(v > 0)) return 1; const p = Math.pow(10, Math.floor(Math.log10(v))), n = v / p; return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p; };
const clock = (ms) => new Date(ms).toTimeString().slice(0, 8);
const lastOf = (a) => (a && a.length ? a[a.length - 1] : null);
const peakOf = (a) => (a && a.length ? Math.max(...a) : 0);
const statusLabel = { running: 'Active', stopped: 'Stopped', paused: 'Paused', starting: 'Starting', error: 'Error' };
const pillFor = (st) => `<span class="pill ${tagFor(st)}">${esc(statusLabel[st] || st)}</span>`;
function smoothPath(pts, lo, hi) {
  const cl = (y) => Math.max(lo, Math.min(hi, y));
  const f = (p) => p[0].toFixed(1) + ' ' + cl(p[1]).toFixed(1);
  if (pts.length < 3) return pts.map((p, i) => (i ? 'L' : 'M') + f(p)).join('');
  let d = 'M' + f(pts[0]);
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] || pts[i], p1 = pts[i], p2 = pts[i + 1], p3 = pts[i + 2] || p2;
    d += `C${f([p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6])} ${f([p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6])} ${f(p2)}`;
  }
  return d;
}
function areaChart(id, series, o) {
  const W = 520, H = 170, L = 58, R = 10, T = 8, B = 24, pw = W - L - R, ph = H - T - B;
  const n = Math.max(0, ...series.map((s) => s.vals.length));
  if (n < 2) return `<svg class="chart" viewBox="0 0 ${W} ${H}"><text class="chartnote" x="${W / 2}" y="${H / 2}" text-anchor="middle">Collecting data…</text></svg>`;
  const peak = peakOf(series.flatMap((s) => s.vals));
  let max = o.max;
  if (!max) { const want = Math.max(peak * 1.2, 1); if (o.bytes) { const u = Math.pow(1024, Math.max(0, Math.min(4, Math.floor(Math.log(want) / Math.log(1024))))); max = u * Math.pow(2, Math.ceil(Math.log2(Math.max(1, want / u)))); } else max = niceMax(want); }
  let g = '';
  for (let i = 0; i <= 4; i++) { const y = T + ph * i / 4; g += `<line class="g" x1="${L}" x2="${W - R}" y1="${y}" y2="${y}"/><text x="${L - 8}" y="${y + 3.5}" text-anchor="end">${esc(o.fmt(max * (1 - i / 4)))}</text>`; }
  const ts = o.times || [];
  for (let i = 0; i < 6; i++) { const idx = Math.round((n - 1) * i / 5), t = ts[ts.length - n + idx]; if (t) g += `<text x="${L + pw * idx / (n - 1)}" y="${H - 6}" text-anchor="${i === 0 ? 'start' : i === 5 ? 'end' : 'middle'}">${clock(t)}</text>`; }
  const defs = series.map((s, i) => `<linearGradient id="g${id}${i}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" style="stop-color:var(${s.color});stop-opacity:.38"/><stop offset="1" style="stop-color:var(${s.color});stop-opacity:0"/></linearGradient>`).join('');
  const paths = series.map((s, i) => {
    const off = n - s.vals.length, pts = s.vals.map((v, k) => [L + pw * (k + off) / (n - 1), T + ph * (1 - Math.min(1, v / max))]);
    const d = smoothPath(pts, T, T + ph), a = pts[0], z = pts[pts.length - 1];
    return `<path d="${d}L${z[0].toFixed(1)} ${T + ph}L${a[0].toFixed(1)} ${T + ph}Z" style="fill:url(#g${id}${i})"/><path class="ln" d="${d}" style="stroke:var(${s.color})"/>`;
  }).join('');
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(o.label || 'chart')}"><defs>${defs}</defs>${g}${paths}</svg>`;
}
const chartCard = (title, sums, svg) => `<div class="cc"><div class="cch"><b>${title}</b><span class="sum">${sums}</span></div>${svg}</div>`;


/* ---------------------------------------------------------------- data loading + samples */
async function loadCore() {
  const [vms, images] = await Promise.all([api('/vms'), api('/images')]);
  S.vms = vms; S.images = images;
  $('#n-vms').textContent = vms.length; $('#n-images').textContent = images.length;
  api('/replicasets').then((r) => { S.rsets = r; const n = $('#n-rs'); if (n) n.textContent = r.length; }).catch(() => {});
}
async function loadHealth() { S.health = await api('/health'); }
async function loadJobs() {
  S.jobs = await api('/jobs');
  const act = S.jobs.filter((j) => ['queued', 'running'].includes(j.status));
  $('#jobline').textContent = act.length ? `⏳ ${act.length} job${act.length > 1 ? 's' : ''} running` : '';
}
async function sample() {
  const run = S.vms.filter((v) => v.status === 'running' && v.agent);
  await Promise.all(run.map(async (v) => {
    try {
      const st = await api(`/vms/${v.id}/stats`);
      const h = S.hist[v.id] || (S.hist[v.id] = { last: null, t: [], cpu: [], mem: [], memb: [], rx: [], tx: [], rd: [], wr: [], rss: [] });
      const p = h.last, dt = p ? Math.max(0.5, st.t - p.t) : 1;
      const dT = p ? st.cpu_total - p.cpu_total : 0, dI = p ? st.cpu_idle - p.cpu_idle : 0, rate = (a, b) => (p ? Math.max(0, (a - b) / dt) : 0);
      h.t.push(st.t * 1000);
      h.cpu.push(dT > 0 ? Math.max(0, Math.min(100, 100 * (1 - dI / dT))) : 0);
      h.rx.push(p ? rate(st.net_rx, p.net_rx) : 0); h.tx.push(p ? rate(st.net_tx, p.net_tx) : 0);
      h.rd.push(p ? rate(st.disk_read, p.disk_read) : 0); h.wr.push(p ? rate(st.disk_write, p.disk_write) : 0);
      h.mem.push(st.mem_total ? 100 * st.mem_used / st.mem_total : 0); h.memb.push(st.mem_used || 0);
      h.rss.push(v.proc ? v.proc.rss : 0);
      for (const k of Object.keys(h)) if (Array.isArray(h[k]) && h[k].length > 70) h[k].shift();
      h.last = st;
    } catch (e) { /* VM may be mid-boot */ }
  }));
}

/* ---------------------------------------------------------------- router */
const routes = {};
let cur = { name: '', args: [], live: null };
function parse() { const h = (location.hash || '#/overview').slice(2).split('/'); return { name: h[0] || 'overview', args: h.slice(1) }; }
async function render() {
  const { name, args } = parse();
  $$('[data-nav]').forEach((a) => a.classList.toggle('on', a.dataset.nav === name || (name === 'vm' && a.dataset.nav === 'vms') || (name === 'rs' && a.dataset.nav === 'replicas')));
  const fn = routes[name] || routes.overview;
  cur = { name, args, live: null };
  const out = await fn(...args);
  if (cur.name === name) $('#view').innerHTML = out.html;
  cur.live = out.live || null;
  if (out.after) out.after();
}
window.addEventListener('hashchange', () => render().catch((e) => toast(e.message, true)));
const nav = (h) => { location.hash = h; };

/* ---------------------------------------------------------------- overview */
routes.overview = async () => {
  await Promise.all([loadCore(), loadHealth(), loadJobs()]);
  const h = S.health, run = S.vms.filter((v) => v.running), cap = h.capacity || {};
  const aMem = run.reduce((x, v) => x + v.mem, 0) * 1048576, aCpu = run.reduce((x, v) => x + v.vcpu, 0);
  const probs = h.problems.length ? `<div class="banner ${h.problems.some((p) => /kvm|firecracker|kernel/i.test(p)) ? 'bad' : ''}"><b>Host needs attention</b><ul>${h.problems.map((p) => `<li>${esc(p)}</li>`).join('')}</ul></div>` : '';
  const row = (k, v) => `<div style="display:flex;justify-content:space-between;gap:16px;padding:9px 0;border-bottom:1px solid var(--line)"><span class="muted">${k}</span><span style="text-align:right">${v}</span></div>`;
  const html = `<div class="top"><h1>Overview</h1><span class="grow"></span><button data-act="refresh">${ico('refresh')}Refresh</button><button class="primary" data-act="new-vm" ${ro}>${ico('plus')}New microVM</button></div>${probs}
    <div class="panel" style="padding:0;display:grid;grid-template-columns:1.1fr 1fr" id="sumcard">
      <div style="padding:20px 22px;border-right:1px solid var(--line)"><div class="muted">${ico('server')} <span style="margin-left:4px">microVMs · live</span></div>
        <div style="margin:14px 0 4px"><span style="font-size:40px;font-weight:650;letter-spacing:-.03em">${run.length}</span> <span class="muted">running · of ${S.vms.length} microVMs · ${S.rsets.length} replica set${S.rsets.length === 1 ? '' : 's'}</span></div>
        <div class="meter" style="margin-top:18px"><i style="width:${cap.mem ? Math.min(100, 100 * aMem / cap.mem).toFixed(0) : 0}%"></i></div>
        <div class="muted small" style="margin-top:8px">Memory allocated <b style="color:var(--ink)">${fmtB(aMem)}</b>${cap.mem ? ' of ' + fmtB(cap.mem) + ' on this host' : ''} · vCPUs <b style="color:var(--ink)">${aCpu}</b>${cap.cpus ? ' of ' + cap.cpus + ' cores' : ''}</div></div>
      <div style="padding:6px 22px 8px">${row('Launch mode', h.mode === 'jailer' ? '<span class="tag ok">jailer</span> chroot · cgroup v2 · seccomp' : '<span class="tag warn">direct</span> no jailer isolation')}
        ${row('Firecracker', esc(h.firecracker || 'not installed'))}${row('KVM', h.kvm ? '<span class="tag ok">accessible</span>' : '<span class="tag bad">no access</span>')}
        ${row('Guest kernel', h.kernel.present ? esc(h.kernel.version || '?') + ' <span class="faint">' + fmtB(h.kernel.size) + '</span>' : '<span class="tag bad">missing</span>')}
        ${row('Network', h.helper ? `<code>${esc(h.bridge)}</code> ${esc(h.subnet)} ${h.net.nat ? '<span class="tag ok">NAT</span>' : '<span class="tag warn">NAT?</span>'}${h.net.isolate ? '<span class="tag ok">isolated</span>' : ''}` : '<span class="tag warn">no helper: no network</span>')}
        <div class="actions" style="padding:10px 0 4px"><button class="sm" data-act="kernel" ${ro}>${h.kernel.present ? 'Upgrade kernel' : 'Get kernel'}</button><span class="faint small mono" title="data directory">${esc(h.data)}</span></div></div></div>
    <div class="grid g2"><div class="panel flat"><div class="head"><h2>Running microVMs</h2><span class="grow"></span><a href="#/vms" class="small">View all</a></div><div id="vmlist">${rackList(run, true)}</div></div>
      <div class="panel flat"><div class="head"><h2>Recent jobs</h2></div>${jobsTable(S.jobs.slice(0, 6))}</div></div>`;
  return { html, live: async () => { await Promise.all([loadCore(), sample()]); const el = $('#vmlist'); if (el) el.innerHTML = rackList(S.vms.filter((v) => v.running), true); } };
};

function jobsTable(jobs) {
  if (!jobs.length) return '<p class="muted small">No jobs yet. Build an image or download a kernel to get started.</p>';
  return `<table>${jobs.map((j) => `<tr class="click" data-act="job" data-id="${esc(j.id)}"><td>${esc(j.label)}</td><td><span class="tag ${j.status === 'done' ? 'ok' : j.status === 'error' ? 'bad' : 'warn'}">${esc(j.status)}</span></td><td class="muted small">${ago(j.started)}</td></tr>`).join('')}</table>`;
}

/* ---------------------------------------------------------------- VM list */
const vq = { q: '', f: '' };
function filterVms(list) {
  const q = vq.q.trim().toLowerCase();
  return list.filter((v) => (!vq.f || v.status === vq.f) && (!q || [v.name, v.image, v.ip, v.id].some((x) => String(x || '').toLowerCase().includes(q))));
}
function rackList(vms, compact) {
  if (!vms.length) return `<div class="empty">${compact ? 'Nothing running.' : 'No microVMs match.'} ${READONLY || compact ? '' : '<br><br><button class="primary" data-act="new-vm">Create a microVM</button>'}</div>`;
  return vms.map((v) => {
    const h = S.hist[v.id] || {};
    const cpu = lastOf(h.cpu), mem = lastOf(h.mem), net = (lastOf(h.rx) || 0) + (lastOf(h.tx) || 0);
    return `<div class="rack" data-act="open-vm" data-id="${esc(v.id)}">
      <div class="who"><span class="fav">${ico('server')}<span class="dot ${esc(v.status)}"></span></span><div><span class="nm">${esc(v.name)}</span>${v.rset ? ' <span class="tag">replica</span>' : ''}
        <div class="sub">${v.vcpu} vCPU, ${fmtB(v.mem * 1048576)} RAM, ${v.disk_mb} MiB disk${v.ip ? ' · ' + esc(v.ip) : ''}${v.status === 'running' && !v.agent ? ' · agent starting…' : ''}</div></div></div>
      <div class="m">CPU <b>${cpu == null ? '–' : cpu.toFixed(0) + '%'}</b>${spark(h.cpu, 100)}</div>
      <div class="m">Memory <b>${mem == null ? '–' : mem.toFixed(0) + '%'}</b>${spark(h.mem, 100)}</div>
      <div class="m">Network <b>${fmtB(net)}/s</b>${spark((h.rx || []).map((x, i) => x + (h.tx[i] || 0)))}</div>
      <div class="actions">${v.running ? `<button class="sm" data-act="vm-stop" data-id="${esc(v.id)}" ${ro}>Stop</button>` : `<button class="sm primary" data-act="vm-start" data-id="${esc(v.id)}" ${ro}>Start</button>`}</div></div>`;
  }).join('');
}
routes.vms = async () => {
  await loadCore();
  const opts = ['', 'running', 'stopped', 'paused', 'starting', 'error'].map((x) => `<option value="${x}" ${vq.f === x ? 'selected' : ''}>${x ? statusLabel[x] : 'All statuses'}</option>`).join('');
  return { html: `<div class="top"><h1>MicroVMs</h1><span class="grow"></span><button data-act="refresh">${ico('refresh')}Refresh</button><button class="primary" data-act="new-vm" ${ro}>${ico('plus')}New microVM</button></div>
    <div class="panel flat"><div class="head"><h2>All microVMs <span class="count" id="vmcount">${S.vms.length}</span></h2><span class="grow"></span>
      <select id="vmf" style="width:150px">${opts}</select><div class="searchbox" style="width:260px">${ico('search')}<input type="text" id="vmq" placeholder="Search by name, image or IP" value="${esc(vq.q)}"></div></div>
      <div id="vmlist">${rackList(filterVms(S.vms))}</div></div>`,
    live: async () => { await Promise.all([loadCore(), sample()]); const el = $('#vmlist'); if (el) el.innerHTML = rackList(filterVms(S.vms)); const c = $('#vmcount'); if (c) c.textContent = S.vms.length; } };
};
document.addEventListener('input', (e) => { if (e.target.id === 'vmq') { vq.q = e.target.value; const el = $('#vmlist'); if (el) el.innerHTML = rackList(filterVms(S.vms)); } });
document.addEventListener('change', (e) => { if (e.target.id === 'vmf') { vq.f = e.target.value; const el = $('#vmlist'); if (el) el.innerHTML = rackList(filterVms(S.vms)); } });

/* ---------------------------------------------------------------- VM form (create + settings) */
function cfgOf(v) { return (v && v.config) || {}; }
function vmFields(v, mode) {
  const c = cfgOf(v), b = c.balloon || {}, m = c.mmds || {};
  const off = mode === 'edit' && v && v.running ? 'disabled' : '';
  const env = Object.entries(c.env || {}).map(([k, x]) => `${k}=${x}`).join('\n');
  const cmd = c.command ? (c.command[0] === '/bin/sh' && c.command[1] === '-c' ? c.command[2] : c.command.join(' ')) : '';
  return `<div class="row">
      ${mode === 'new' ? `<div><label>Name</label><input type="text" name="name" placeholder="auto" pattern="[a-z0-9][a-z0-9._-]*" maxlength="48"></div>` : ''}
      <div><label>vCPUs</label><input type="number" name="vcpu" min="1" max="32" value="${v ? v.vcpu : 1}" ${off}></div>
      <div><label>Memory (MiB)</label><input type="number" name="mem" min="64" step="64" value="${v ? v.mem : 512}" ${off}></div>
      <div><label>Disk (MiB)${mode === 'new' ? ' — grow only' : ''}</label><input type="number" name="disk_mb" min="0" placeholder="image size" value="${v && mode === 'edit' ? v.disk_mb : ''}" ${off}></div></div>
    <div><label class="inline"><input type="checkbox" name="net" ${c.net !== false ? 'checked' : ''} ${off}> Network (TAP on bridge)</label>
      <label class="inline"><input type="checkbox" name="entropy" ${c.entropy !== false ? 'checked' : ''} ${off}> virtio-rng entropy</label>
      <label class="inline"><input type="checkbox" name="autostart" ${c.autostart ? 'checked' : ''} ${off}> Start when the app starts</label></div>
    <details><summary>Advanced Firecracker options</summary>
      <div class="row"><div><label>CPU template (deprecated upstream)</label><select name="cpu_template" ${off}>${['None', 'T2', 'T2S', 'T2CL', 'T2A', 'C3', 'V1N1'].map((t) => `<option ${c.cpu_template === t ? 'selected' : ''}>${t}</option>`).join('')}</select></div>
        <div><label>SMT (hyper-threads)</label><select name="smt" ${off}><option value="" ${c.smt == null ? 'selected' : ''}>default</option><option value="true" ${c.smt === true ? 'selected' : ''}>on</option><option value="false" ${c.smt === false ? 'selected' : ''}>off (safer on shared hosts)</option></select></div>
        <div><label>Huge pages</label><select name="huge_pages" ${off}><option ${c.huge_pages !== '2M' ? 'selected' : ''}>None</option><option ${c.huge_pages === 'Transparent' ? 'selected' : ''}>Transparent</option><option ${c.huge_pages === '2M' ? 'selected' : ''}>2M</option></select></div></div>
      <label class="inline"><input type="checkbox" name="track_dirty_pages" ${c.track_dirty_pages ? 'checked' : ''} ${off}> Track dirty pages (needed for differential snapshots)</label>
      <h3>Rate limiters (token buckets)</h3>
      <div class="row"><div><label>Network RX (Mbit/s, 0 = off)</label><input type="number" name="rx_mbit" min="0" value="${c.rx_mbit || 0}" ${off}></div>
        <div><label>Network TX (Mbit/s)</label><input type="number" name="tx_mbit" min="0" value="${c.tx_mbit || 0}" ${off}></div>
        <div><label>Disk bandwidth (Mbit/s)</label><input type="number" name="disk_mbit" min="0" value="${c.disk_mbit || 0}" ${off}></div>
        <div><label>Disk IOPS</label><input type="number" name="disk_iops" min="0" value="${c.disk_iops || 0}" ${off}></div></div>
      <h3>Memory hotplug (virtio-mem)</h3>
      <div class="row"><div><label>Hot-pluggable memory (MiB, 0 = off; multiple of 128)</label><input type="number" name="hotplug_mib" min="0" step="128" value="${c.hotplug_mib || 0}" ${off}></div></div>
      <h3>Memory balloon</h3>
      <label class="inline"><input type="checkbox" name="balloon_on" ${c.balloon ? 'checked' : ''} ${off}> Enable balloon device (reclaim guest memory live)</label>
      <div class="row"><div><label>Initial target (MiB)</label><input type="number" name="balloon_mib" min="0" value="${b.amount_mib || 0}" ${off}></div>
        <div><label>Stats interval (s)</label><input type="number" name="balloon_stats" min="0" value="${b.stats_polling_interval_s ?? 5}" ${off}></div></div>
      <label class="inline"><input type="checkbox" name="balloon_oom" ${b.deflate_on_oom !== false ? 'checked' : ''} ${off}> Deflate on guest OOM</label>
      <h3>Metadata service (MMDS v2, 169.254.169.254)</h3>
      <label class="inline"><input type="checkbox" name="mmds_on" ${c.mmds ? 'checked' : ''} ${off}> Enable</label>
      <textarea name="mmds_data" placeholder='{"role":"web","db":"10.0.0.5"}' ${off}>${esc(m.data ? JSON.stringify(m.data, null, 2) : '')}</textarea>
      <h3>Workload</h3>
      <label>Environment (KEY=value per line)</label><textarea name="env" ${off}>${esc(env)}</textarea>
      <label>Command override (runs with /bin/sh -c; empty = image default)</label><input type="text" name="command" value="${esc(cmd)}" ${off}>
      <label>Extra kernel arguments</label><input type="text" name="boot_args" value="${esc(c.boot_args || '')}" placeholder="e.g. quiet loglevel=3" ${off}></details>`;
}
function readVmForm(f) {
  const g = (n) => f.elements[n], num = (n) => +g(n).value || 0;
  const cfg = {
    net: g('net').checked, entropy: g('entropy').checked, autostart: g('autostart').checked,
    cpu_template: g('cpu_template').value, hotplug_mib: num('hotplug_mib'), smt: g('smt').value === '' ? null : g('smt').value === 'true',
    huge_pages: g('huge_pages').value, track_dirty_pages: g('track_dirty_pages').checked,
    rx_mbit: num('rx_mbit'), tx_mbit: num('tx_mbit'), disk_mbit: num('disk_mbit'), disk_iops: num('disk_iops'),
    boot_args: g('boot_args').value.trim(), balloon: null, mmds: null,
  };
  if (g('balloon_on').checked) cfg.balloon = { amount_mib: num('balloon_mib'), stats_polling_interval_s: num('balloon_stats'), deflate_on_oom: g('balloon_oom').checked };
  if (g('mmds_on').checked) {
    const t = g('mmds_data').value.trim();
    try { cfg.mmds = { data: t ? JSON.parse(t) : {} }; } catch (e) { throw new Error('Metadata must be valid JSON'); }
  }
  cfg.env = {};
  g('env').value.split('\n').map((l) => l.trim()).filter(Boolean).forEach((l) => { const i = l.indexOf('='); if (i < 1) throw new Error(`Bad environment line: ${l}`); cfg.env[l.slice(0, i)] = l.slice(i + 1); });
  const cmd = g('command').value.trim();
  cfg.command = cmd ? ['/bin/sh', '-c', cmd] : null;
  return { vcpu: num('vcpu'), mem: num('mem'), disk_mb: num('disk_mb') || 0, ...cfg, name: g('name') ? g('name').value.trim() : undefined };
}
function openModal(html) {
  $('#modal').innerHTML = `<div class="modal-bg" data-act="modal-bg"><div class="modal" role="dialog" aria-modal="true">${html}</div></div>`;
}
const closeModal = () => { $('#modal').innerHTML = ''; };

function newVmDialog(imageName) {
  if (!S.images.length) { toast('Build or pull an image first', true); nav('#/build'); return; }
  openModal(`<h2>New microVM</h2><form data-form="create-vm">
    <label>Image</label><select name="image">${S.images.map((i) => `<option value="${esc(i.name)}" ${i.name === imageName ? 'selected' : ''}>${esc(i.name)} — ${esc(i.source)}${i.ports.length ? ' · ' + esc(i.ports.join(', ')) : ''}</option>`).join('')}</select>
    ${vmFields(null, 'new')}
    <div class="actions" style="margin-top:16px"><label class="inline"><input type="checkbox" name="start" checked> Start now</label><span class="grow" style="flex:1"></span>
      <button type="button" data-act="close">Cancel</button><button class="primary" type="submit">Create</button></div></form>`);
}

/* ---------------------------------------------------------------- VM detail */
const TABS = [['overview', 'Overview', 'gauge'], ['terminal', 'Terminal', 'terminal'], ['console', 'Console', 'console'], ['files', 'Files', 'file'], ['snapshots', 'Snapshots', 'camera'], ['network', 'Networking', 'net'], ['memory', 'Memory', 'memory'], ['metadata', 'Metadata', 'layers'], ['logs', 'Firecracker', 'activity'], ['settings', 'Settings', 'sliders']];
function vmHeader(v, tab) {
  const port = (v.ports_declared || [])[0] || (v.forwards[0] || {}).guest_port;
  const items = v.running
    ? `${v.status === 'paused' ? `<button data-act="vm-resume" data-id="${v.id}" ${ro}>Resume</button>` : `<button data-act="vm-pause" data-id="${v.id}" ${ro}>Pause</button>`}<button data-act="vm-restart" data-id="${v.id}" ${ro}>Restart</button><button data-act="vm-stop" data-id="${v.id}" ${ro}>Shut down</button><hr><button class="danger" data-act="vm-kill" data-id="${v.id}" ${ro}>Force stop</button>`
    : `<button data-act="vm-clone" data-id="${v.id}" ${ro}>Clone</button><hr><button class="danger" data-act="vm-delete" data-id="${v.id}" ${ro}>Delete</button>`;
  const primary = v.running ? (v.ip && port ? `<a class="btn primary" target="_blank" rel="noopener" href="http://${esc(v.ip)}:${port}" style="background:var(--btn);color:var(--btn-ink);border-color:var(--btn);font-weight:600">Visit site ${ico('ext')}</a>` : '')
    : `<button class="primary" data-act="vm-start" data-id="${v.id}" ${ro}>${ico('play')}Start</button>`;
  return `<div class="top"><span class="crumbs"><a href="#/vms">MicroVMs</a> / <b>${esc(v.name)}</b>${v.rset ? ` · <a href="#/rs/${esc(v.rset)}">replica ${v.ridx} of its set</a>` : ''}</span><span class="grow"></span></div>
    <div class="vmhead"><span class="fav">${ico('server')}<span class="dot ${esc(v.status)}"></span></span><div><h1>${esc(v.name)}${pillFor(v.status)}</h1>
      <div class="meta"><span>${ico('box')}${esc(v.image)}</span><span>${ico('net')}${esc(v.ip || 'no network')}</span><span>${ico('cpu')}${v.vcpu} vCPU · ${fmtB(v.mem * 1048576)}</span>${v.uptime_s != null ? `<span>${ico('clock')}up ${fmtDur(v.uptime_s)}</span>` : ''}<span>${ico('lock')}${v.jailed ? 'jailer' : 'direct'}</span></div></div><span style="flex:1"></span>
      <details class="menu"><summary>Actions ${ico('chev')}</summary><div class="pop">${items}</div></details>${primary}</div>
    ${v.error && !v.running ? `<div class="banner bad" style="margin-top:14px"><b>Last error</b><pre style="white-space:pre-wrap;margin:6px 0 0">${esc(v.error)}</pre></div>` : (v.error ? `<div class="banner small" style="margin-top:14px">${esc(v.error)}</div>` : '')}
    <div class="tabs">${TABS.map(([k, l, ic]) => `<a href="#/vm/${v.id}/${k}" class="${tab === k ? 'on' : ''}">${ico(ic)}${l}</a>`).join('')}</div>`;
}
routes.vm = async (id, tab = 'overview') => {
  await loadCore();
  const v = vmById(id);
  if (!v) return { html: `<div class="empty">That microVM no longer exists. <a href="#/vms">Back to the list</a></div>` };
  const body = await (vmTabs[tab] || vmTabs.overview)(v);
  return { html: vmHeader(v, tab) + `<div id="tabbody">${body.html}</div>`, after: body.after,
    live: async () => { await Promise.all([loadCore(), sample()]); const nv = vmById(id); if (!nv) return nav('#/vms'); if (nv.status !== v.status || nv.running !== v.running || nv.agent !== v.agent) return render(); if (body.live) await body.live(nv); } };
};

function usageHtml(v) {
  const h = S.hist[v.id] || { t: [], cpu: [], mem: [], memb: [], rx: [], tx: [], rd: [], wr: [], last: null }, L = h.last;
  const cpu = lastOf(h.cpu), mp = L && L.mem_total ? 100 * L.mem_used / L.mem_total : null, fp = L && L.fs_total ? 100 * L.fs_used / L.fs_total : null;
  const tile = (ic, label, val, of, pct) => `<div class="tile"><div class="tk">${ico(ic)}${label}</div><div class="tv">${val}<small>${of}</small></div>${pct == null ? '<div class="meter" style="visibility:hidden"><i></i></div>' : `<div class="meter"><i style="width:${Math.max(1, Math.min(100, pct)).toFixed(0)}%"></i></div>`}</div>`;
  const tiles = `<div class="grid g4">${tile('cpu', 'CPU', cpu == null ? '–' : cpu.toFixed(0) + '%', `of ${v.vcpu} vCPU`, cpu)}
    ${tile('memory', 'Memory', L ? fmtB(L.mem_used) : '–', `of ${fmtB(L && L.mem_total ? L.mem_total : v.mem * 1048576)}`, mp)}
    ${tile('disk', 'Disk', L && L.fs_total ? fmtB(L.fs_used) : fmtB(v.disk_mb * 1048576), L && L.fs_total ? `of ${fmtB(L.fs_total)}` : 'allocated', fp)}
    ${tile('net', 'Network · since boot', L ? fmtB(L.net_rx + L.net_tx) : '–', L ? `↓ ${fmtB(L.net_rx)} ↑ ${fmtB(L.net_tx)}` : '', null)}</div>`;
  const times = h.t, rate = (x) => fmtB(x) + '/s';
  const cards = `<div class="grid g2" style="margin-top:14px">
    ${chartCard('CPU', `<span><b>${cpu == null ? '–' : cpu.toFixed(1) + '%'}</b><small>Peak ${peakOf(h.cpu).toFixed(1)}%</small></span>`, areaChart(v.id + 'c', [{ vals: h.cpu, color: '--c-cpu' }], { max: 100, fmt: (x) => Math.round(x) + '%', times, label: 'CPU usage' }))}
    ${chartCard('Memory', `<span><b>${L ? fmtB(L.mem_used) : '–'}</b><small>Peak ${fmtB(peakOf(h.memb))}</small></span>`, areaChart(v.id + 'm', [{ vals: h.memb, color: '--c-mem' }], { max: L && L.mem_total ? L.mem_total : v.mem * 1048576, fmt: (x) => fmtB(x), times, label: 'Memory usage' }))}
    ${chartCard('Network', `<span style="color:var(--c-rx)">${ico('down')}<b style="color:var(--ink)">${fmtB(lastOf(h.rx) || 0)}/s</b><small>Peak ${fmtB(peakOf(h.rx))}/s</small></span><span style="color:var(--c-tx)">${ico('up')}<b style="color:var(--ink)">${fmtB(lastOf(h.tx) || 0)}/s</b><small>Peak ${fmtB(peakOf(h.tx))}/s</small></span>`, areaChart(v.id + 'n', [{ vals: h.rx, color: '--c-rx' }, { vals: h.tx, color: '--c-tx' }], { fmt: rate, bytes: true, times, label: 'Network throughput' }))}
    ${chartCard('Disk I/O', `<span style="color:var(--c-rd)">${ico('down')}<b style="color:var(--ink)">${fmtB(lastOf(h.rd) || 0)}/s</b><small>Peak ${fmtB(peakOf(h.rd))}/s</small></span><span style="color:var(--c-wr)">${ico('up')}<b style="color:var(--ink)">${fmtB(lastOf(h.wr) || 0)}/s</b><small>Peak ${fmtB(peakOf(h.wr))}/s</small></span>`, areaChart(v.id + 'd', [{ vals: h.rd, color: '--c-rd' }, { vals: h.wr, color: '--c-wr' }], { fmt: rate, bytes: true, times, label: 'Disk throughput' }))}</div>`;
  return tiles + `<div class="sec"><h2>Usage</h2><span class="grow"></span><span class="muted small">Live · last ${fmtDur(Math.max(0, (times.length - 1) * 2.5))} · sampled every 2.5 s</span></div>` + cards;
}
const vmTabs = {};
vmTabs.overview = async (v) => {
  const img = imgByName(v.image) || {};
  return { html: `${v.running ? `<div id="usage">${usageHtml(v)}</div>` : '<div class="panel"><p class="muted" style="margin:0">Start the microVM to see live CPU, memory, network and disk usage.</p></div>'}
    <div class="grid g2" style="margin-top:16px"><div class="panel"><h2>Details</h2><dl class="kv">
      <dt>ID</dt><dd class="mono">${esc(v.id)}</dd><dt>Image</dt><dd>${esc(v.image)} <span class="faint">${esc(img.ref || '')}</span></dd>
      <dt>Address</dt><dd class="mono">${esc(v.ip || '–')} · ${esc(v.mac || '')} · ${esc(v.tap || '')}</dd>
      <dt>Disk</dt><dd>${v.disk_mb} MiB</dd><dt>Launch</dt><dd>${v.jailed ? '<span class="tag ok">jailer</span>' : '<span class="tag warn">direct</span>'} ${v.pid ? 'pid ' + v.pid : ''}</dd>
      <dt>Workload</dt><dd class="mono small">${esc((cfgOf(v).command || img.args || []).join(' ') || '(none — idle VM)')}</dd>
      <dt>Guest agent</dt><dd>${v.agent ? '<span class="tag ok">reachable (vsock)</span>' : '<span class="tag">not reachable</span>'}</dd>
      <dt>Created</dt><dd>${ago(v.created)}</dd></dl></div>
      <div class="panel"><h2>Reach it</h2>${v.status === 'running' && v.ip && (v.ports_declared.length || v.forwards.length) ? [...new Set([...(v.ports_declared || []), ...v.forwards.map((f) => f.guest_port)])].map((p) => `<p><a target="_blank" rel="noopener" href="http://${esc(v.ip)}:${p}">http://${esc(v.ip)}:${p}</a> <span class="faint small">(from this host)</span></p>`).join('') : '<p class="muted small">Declared ports appear here once the VM runs. Use Networking → Publish to expose a port on the host.</p>'}
      ${v.forwards.length ? `<h3>Published</h3>${v.forwards.map((f) => `<p class="mono small">host:${f.host_port} → ${f.guest_port}/${esc(f.proto)}</p>`).join('')}` : ''}
      <h3>Host cost</h3>${v.proc ? `<p class="small">${fmtB(v.proc.rss)} resident · ${v.proc.threads} threads · ${v.proc.cpu_s.toFixed(1)} CPU-s</p>` : '<p class="muted small">–</p>'}</div></div>`,
    live: async (nv) => { const c = $('#usage'); if (c && nv.running) c.innerHTML = usageHtml(nv); } };
};
vmTabs.terminal = async (v) => {
  const t = S.term[v.id] || (S.term[v.id] = { cwd: '/', hist: [], hi: 0, out: '<span class="d"># Commands run inside the microVM through the vsock agent (no SSH, no open port 22).\n</span>' });
  return { html: `<div class="term" id="termout">${t.out}</div><form class="termin" data-form="exec" data-id="${v.id}"><input type="text" name="cmd" id="termcmd" autocomplete="off" spellcheck="false" placeholder="${v.agent ? 'ls -la, ps aux, apk add curl …' : 'waiting for the guest agent…'}" ${v.agent && !READONLY ? '' : 'disabled'}><button class="primary" type="submit" ${v.agent && !READONLY ? '' : 'disabled'}>Run</button></form>
    <p class="faint small" style="margin-top:8px">Working directory: <code id="cwd">${esc(t.cwd)}</code> · each command runs as root in its own process (no interactive programs).</p>`,
    after: () => { const o = $('#termout'); o.scrollTop = o.scrollHeight; const i = $('#termcmd'); i && !i.disabled && i.focus(); } };
};
vmTabs.console = async (v) => {
  const txt = await api(`/vms/${v.id}/console`);
  return { html: `<div class="actions" style="margin-bottom:8px"><span class="muted small">Serial console output (kernel + init + workload), refreshed every 2 s</span></div><div class="term" id="console">${esc(txt)}</div>`,
    after: () => { const o = $('#console'); o.scrollTop = o.scrollHeight; },
    live: async () => { const o = $('#console'); if (!o) return; const stick = o.scrollTop + o.clientHeight >= o.scrollHeight - 30; o.textContent = await api(`/vms/${v.id}/console`); if (stick) o.scrollTop = o.scrollHeight; } };
};
vmTabs.logs = async (v) => {
  const txt = v.running || true ? await api(`/vms/${v.id}/firecracker-log`) : '';
  let mc = '';
  if (v.running) { try { const m = await api(`/vms/${v.id}/metrics`); mc = m.fc ? JSON.stringify(m.fc, null, 2) : ''; } catch (e) { mc = e.message; } }
  return { html: `<div class="grid g2"><div><h2>Firecracker log</h2><div class="term" style="height:360px">${esc(txt)}</div></div>
    <div><h2>Latest metrics sample</h2><div class="term" style="height:360px">${esc(mc || '(VM not running)')}</div><p class="faint small">Firecracker flushes metrics to a file inside the VM's sandbox; this is the last JSON line after an on-demand FlushMetrics.</p></div></div>` };
};
vmTabs.files = async (v) => ({ html: `<div class="grid g2"><div class="panel"><h2>Upload to the VM</h2><form data-form="upload" data-id="${v.id}">
    <label>File</label><input type="file" name="file" required><label>Destination path (ends with / to keep the file name)</label><input type="text" name="path" value="/root/" required><label>Mode</label><input type="text" name="mode" value="644" pattern="[0-7]{3,4}">
    <div class="actions" style="margin-top:12px"><button class="primary" type="submit" ${v.agent && !READONLY ? '' : 'disabled'}>Upload</button></div></form></div>
    <div class="panel"><h2>Download from the VM</h2><form data-form="download" data-id="${v.id}"><label>Absolute file path</label><input type="text" name="path" placeholder="/var/log/messages" required>
    <div class="actions" style="margin-top:12px"><button class="primary" type="submit" ${v.agent ? '' : 'disabled'}>Download</button></div></form><p class="faint small">Up to 256 MiB per file.</p></div></div>` });
vmTabs.snapshots = async (v) => {
  const snaps = await api(`/vms/${v.id}/snapshots`);
  return { html: `<div class="panel"><h2>Create a snapshot</h2><p class="muted small">A full Firecracker snapshot pauses the VM for a moment and saves guest memory, CPU/device state and the disk. You can later restore it into a stopped VM and continue exactly where it left off. A snapshot is as large as the VM's RAM; never restore one snapshot into several VMs at once (identical state, duplicate secrets and RNG).</p>
    <form data-form="snapshot" data-id="${v.id}" class="row"><div><label>Name</label><input type="text" name="name" placeholder="auto" pattern="[a-z0-9][a-z0-9._-]*"></div><div><label>Note</label><input type="text" name="note" maxlength="200"></div>
    <div class="auto"><button class="primary" type="submit" ${v.running && !READONLY ? '' : 'disabled'}>Snapshot</button></div></form></div>
    <div class="panel flat"><div class="head"><h2>Snapshots</h2></div>${snaps.length ? `<table><tr><th>Name</th><th>Taken</th><th class="num">Size</th><th></th></tr>${snaps.map((s) => `<tr><td><b>${esc(s.name)}</b> <span class="faint">${esc(s.note || '')}</span></td><td>${ago(s.created)}</td><td class="num">${fmtB(s.size_bytes)}</td>
      <td class="num"><button class="sm" data-act="snap-restore" data-id="${v.id}" data-sid="${esc(s.id)}" ${v.running || READONLY ? 'disabled title="stop the VM first"' : ''}>Restore</button> <button class="sm danger" data-act="snap-del" data-id="${v.id}" data-sid="${esc(s.id)}" ${ro}>Delete</button></td></tr>`).join('')}</table>` : '<div class="empty">No snapshots yet.</div>'}</div>` };
};
vmTabs.network = async (v) => {
  const c = cfgOf(v);
  return { html: `<div class="grid g2"><div class="panel"><h2>Interface</h2><dl class="kv"><dt>Address</dt><dd class="mono">${esc(v.ip || '–')}/24 via ${esc(S.health?.subnet ? S.health.subnet.replace('.0/24', '.1') : '')}</dd><dt>MAC</dt><dd class="mono">${esc(v.mac || '')}</dd><dt>TAP</dt><dd class="mono">${esc(v.tap || '')}</dd><dt>Policy</dt><dd class="small">Outbound NAT · no VM-to-VM traffic · no access to your LAN or to host services</dd></dl>
      <h3>Live rate limits (Mbit/s, 0 = unlimited)</h3><form data-form="live" data-id="${v.id}" class="row"><div><label>RX</label><input type="number" name="rx_mbit" min="0" value="${c.rx_mbit || 0}"></div><div><label>TX</label><input type="number" name="tx_mbit" min="0" value="${c.tx_mbit || 0}"></div><div class="auto"><button type="submit" ${v.running && c.net !== false && !READONLY ? '' : 'disabled'}>Apply</button></div></form>
      <form data-form="live" data-id="${v.id}" class="row"><div><label>Disk bandwidth</label><input type="number" name="disk_mbit" min="0" value="${c.disk_mbit || 0}"></div><div><label>Disk IOPS</label><input type="number" name="disk_iops" min="0" value="${c.disk_iops || 0}"></div><div class="auto"><button type="submit" ${v.running && !READONLY ? '' : 'disabled'}>Apply</button></div></form></div>
    <div class="panel"><h2>Publish a port on this host</h2><p class="muted small">Adds a DNAT rule so other machines can reach the VM through this host's address. Host ports from 1024 up.</p>
      <form data-form="forward" data-id="${v.id}" class="row"><div><label>Host port</label><input type="number" name="host_port" min="1024" max="65535" required></div><div><label>VM port</label><input type="number" name="guest_port" min="1" max="65535" required></div><div><label>Protocol</label><select name="proto"><option>tcp</option><option>udp</option></select></div><div class="auto"><button class="primary" type="submit" ${READONLY ? 'disabled' : ''}>Publish</button></div></form>
      ${v.forwards.length ? `<table style="margin-top:12px">${v.forwards.map((f) => `<tr><td class="mono">host:${f.host_port} → ${esc(v.ip)}:${f.guest_port}/${esc(f.proto)}</td><td class="num"><button class="sm danger" data-act="fwd-del" data-id="${v.id}" data-fid="${f.id}" ${ro}>Remove</button></td></tr>`).join('')}</table>` : ''}</div></div>` };
};
vmTabs.memory = async (v) => {
  const c = cfgOf(v);
  if (!c.balloon && !c.hotplug_mib) return { html: `<div class="panel"><h2>Memory</h2><p class="muted">This VM has neither a balloon nor hot-pluggable memory. Enable them in <a href="#/vm/${v.id}/settings">Settings</a> (stop the VM first). A <b>balloon</b> takes RAM back from a running guest; <b>hotplug</b> (virtio-mem) lets you grow the guest's RAM up to a ceiling while it runs. Both need a guest kernel that supports them.</p></div>` };
  let m = null;
  if (v.running) { try { m = await api(`/vms/${v.id}/metrics`); } catch (e) { /* ignore */ } }
  const bal = c.balloon ? `<div class="panel"><h2>Memory balloon</h2><p class="muted small">Inflating the balloon makes the guest hand pages back to the host. Target = how much memory to reclaim.</p>
    <form data-form="live" data-id="${v.id}"><div class="row"><div><label>Reclaim <b id="bv">${c.balloon.amount_mib}</b> MiB of ${v.mem} MiB</label><input type="range" name="balloon_mib" min="0" max="${v.mem}" step="16" value="${c.balloon.amount_mib}" id="brange"></div>
    <div class="auto"><button class="primary" type="submit" ${v.running && !READONLY ? '' : 'disabled'}>Apply</button></div></div></form>
    ${m && m.balloon ? `<h3>Balloon status</h3><dl class="kv"><dt>Target</dt><dd>${m.balloon.amount_mib} MiB</dd><dt>Guest-reported</dt><dd class="mono small">${esc(JSON.stringify(m.balloon.stats || {}))}</dd></dl>` : ''}</div>` : '';
  const hp = c.hotplug_mib ? `<div class="panel"><h2>Memory hotplug (virtio-mem)</h2><p class="muted small">Ceiling ${c.hotplug_mib} MiB on top of the ${v.mem} MiB boot memory. Set how much extra RAM the guest should have right now.</p>
    <form data-form="live" data-id="${v.id}"><div class="row"><div><label>Extra memory (MiB)</label><input type="number" name="hotplug_mib" min="0" max="${c.hotplug_mib}" step="128" value="${c.hotplug_target_mib || 0}"></div>
    <div class="auto"><button class="primary" type="submit" ${v.running && !READONLY ? '' : 'disabled'}>Apply</button></div></div></form>
    ${m && m.hotplug ? `<dl class="kv"><dt>Plugged</dt><dd>${m.hotplug.plugged_size_mib ?? '–'} MiB</dd><dt>Requested</dt><dd>${m.hotplug.requested_size_mib ?? '–'} MiB</dd></dl>` : ''}</div>` : '';
  return { html: bal + hp };
};
vmTabs.metadata = async (v) => {
  const c = cfgOf(v);
  if (!c.mmds) return { html: `<div class="panel"><h2>Metadata service (MMDS)</h2><p class="muted">Not enabled. MMDS lets a guest fetch configuration (like EC2's instance metadata) from <code>http://169.254.169.254</code> without any network setup. Enable it in <a href="#/vm/${v.id}/settings">Settings</a>.</p></div>` };
  return { html: `<div class="panel"><h2>Metadata service (MMDS v2)</h2><p class="muted small">Inside the guest: <code>TOKEN=$(curl -s -X PUT -H "X-metadata-token-ttl-seconds: 60" http://169.254.169.254/latest/api/token)</code> then <code>curl -s -H "X-metadata-token: $TOKEN" http://169.254.169.254/</code></p>
    <form data-form="live" data-id="${v.id}"><textarea name="mmds" style="min-height:220px">${esc(JSON.stringify(c.mmds.data || {}, null, 2))}</textarea>
    <div class="actions" style="margin-top:10px"><button class="primary" type="submit" ${v.running && !READONLY ? '' : 'disabled'}>Push to running VM</button><span class="faint small">${v.running ? '' : 'Start the VM to push changes live; edit the stored value in Settings.'}</span></div></form></div>` };
};
vmTabs.settings = async (v) => ({ html: `<form class="panel" data-form="settings" data-id="${v.id}"><h2>Settings</h2>${v.running ? '<div class="banner small">Stop the VM to change these; they apply on the next start.</div>' : ''}${vmFields(v, 'edit')}
    <div class="actions" style="margin-top:16px"><button class="primary" type="submit" ${v.running || READONLY ? 'disabled' : ''}>Save</button></div></form>` });

/* ---------------------------------------------------------------- images + build */
routes.images = async () => {
  await Promise.all([loadCore(), loadHealth(), loadJobs(), api('/store').then((s) => { S.store = s; })]);
  const k = S.health.kernel;
  return { html: `<div class="top"><h1>Images</h1><span class="grow"></span><a class="btn" href="#/build">Build an image</a></div>
    <div class="panel"><div class="actions"><div><b>Guest kernel</b> <span class="muted">${k.present ? esc(k.version || 'unknown version') + ' · ' + fmtB(k.size) : 'not downloaded'}</span>${k.sha256 ? `<div class="faint mono small">sha256 ${esc(k.sha256)}</div>` : ''}</div><span style="flex:1"></span><button data-act="kernel" ${ro}>${k.present ? 'Upgrade kernel' : 'Get kernel'}</button></div></div>
    <div class="panel flat"><div class="head"><h2>Installed</h2></div>${S.images.length ? `<table><tr><th>Name</th><th>Source</th><th>Ports</th><th>Entry</th><th class="num">Disk</th><th></th></tr>${S.images.map((i) => `<tr><td><b>${esc(i.name)}</b><div class="faint small">${esc(i.ref || '')}</div></td><td>${esc(i.source)}</td><td class="mono small">${esc((i.ports || []).join(' '))}</td><td class="mono small" style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc((i.args || []).join(' '))}">${esc((i.args || []).join(' ') || '(bare)')}</td><td class="num">${i.size_mb} MiB</td>
      <td class="num"><button class="sm primary" data-act="new-vm-from" data-name="${esc(i.name)}" ${ro}>Launch</button> <button class="sm" data-act="new-rs-from" data-name="${esc(i.name)}" ${ro}>Replicas</button> <a class="btn sm" href="/api/images/${encodeURIComponent(i.name)}/export">ext4</a> <button class="sm danger" data-act="img-del" data-name="${esc(i.name)}" ${ro}>Delete</button></td></tr>${i.notes ? `<tr><td colspan="6" class="warn small" style="color:var(--warn)">${esc(i.notes)}</td></tr>` : ''}`).join('')}</table>` : '<div class="empty">No images yet.</div>'}</div>
    <div class="panel flat"><div class="head"><h2>Catalog</h2><span class="muted small">pulled from the registry, unpacked, converted to ext4</span></div><div class="grid g4" style="padding:16px">${S.store.map((s) => `<div class="stat"><b>${esc(s.title)}</b><div class="muted small" style="min-height:36px">${esc(s.desc)}</div><div class="mono faint small">${esc(s.ref)}</div><div style="margin-top:8px">${s.built ? '<span class="tag ok">installed</span>' : `<button class="sm" data-act="pull" data-ref="${esc(s.ref)}" data-name="${esc(s.name)}" ${ro}>Pull & convert</button>`}</div></div>`).join('')}</div></div>` };
};
routes.build = async (sub = 'registry') => {
  await Promise.all([loadJobs(), loadHealth()]);
  const T = [['registry', 'Registry image'], ['dockerfile', 'Dockerfile'], ['git', 'Git repository'], ['compose', 'Compose'], ['tar', 'Rootfs tarball'], ['official', 'Official Ubuntu']];
  const forms = {
    registry: `<form data-form="b-oci"><p class="muted small">Pulls the image with skopeo, unpacks the layers <b>without executing anything</b>, adds the init/agent and builds an ext4 disk. Entrypoint, env and working dir are taken from the image.</p>
      <div class="row"><div><label>Image reference</label><input type="text" name="ref" placeholder="nginx:alpine or ghcr.io/org/app:tag" required></div><div><label>Name</label><input type="text" name="name" placeholder="auto"></div></div>
      <div class="row"><div><label>Extra free space (MiB)</label><input type="number" name="extra_mb" value="64" min="0"></div><div><label>Entrypoint override</label><input type="text" name="entrypoint"></div><div><label>Cmd override</label><input type="text" name="cmd"></div></div>
      <label>Environment (KEY=value per line)</label><textarea name="env" style="min-height:60px"></textarea>
      <label class="inline"><input type="checkbox" name="refresh"> Re-pull</label><label class="inline"><input type="checkbox" name="force"> Rebuild if it exists</label>
      <div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Build image</button></div></form>`,
    dockerfile: `<form data-form="b-docker"><p class="muted small"><b>RUN steps execute inside a throw-away builder microVM</b> (hardware isolation), never on your host. Supported: FROM (single stage), ARG, ENV, WORKDIR, COPY/ADD (local files), RUN, CMD, ENTRYPOINT, EXPOSE. Needs KVM and a kernel.</p>
      <div class="row"><div><label>Image name</label><input type="text" name="name" required></div></div>
      <label>Dockerfile</label><textarea name="dockerfile" style="min-height:220px" spellcheck="false">FROM alpine:3
RUN apk add --no-cache curl
ENV GREETING=hello
CMD ["/bin/sh", "-c", "echo $GREETING; sleep infinity"]</textarea>
      <label>Files for COPY (JSON {"path": "content"}, optional)</label><textarea name="files" style="min-height:60px" placeholder='{"index.html": "<h1>hi</h1>"}'></textarea>
      <div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Build in microVM</button></div></form>`,
    git: `<form data-form="b-git"><div class="banner small">Want this repeatable (rebuild on every push, roll out to replicas)? Use <a href="#/sources">GitHub builds</a>. This tab is a one-off build.</div><p class="muted small">Shallow-clones an https repository (no hooks, no submodules) and builds its Dockerfile like the Dockerfile tab.</p>
      <div class="row"><div><label>Repository (https://)</label><input type="text" name="repo" placeholder="https://github.com/org/repo" required></div><div><label>Branch</label><input type="text" name="branch" placeholder="default"></div></div>
      <div class="row"><div><label>Image name</label><input type="text" name="name" required></div><div><label>Dockerfile path</label><input type="text" name="dockerfile" value="Dockerfile"></div></div>
      <div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Clone & build</button></div></form>`,
    compose: `<form data-form="b-compose"><p class="muted small">Each service with an <code>image:</code> becomes an image named <code>project-service</code>. <code>environment</code>, <code>command</code>, <code>entrypoint</code> are honoured; networks/volumes are not (each service is its own microVM).</p>
      <label>Project</label><input type="text" name="project" value="stack"><label>docker-compose.yml</label><textarea name="text" style="min-height:200px" spellcheck="false">services:
  web:
    image: nginx:alpine
  cache:
    image: redis:alpine</textarea>
      <label class="inline"><input type="checkbox" name="refresh"> Re-pull</label><div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Build all services</button></div></form>`,
    tar: `<form data-form="b-tar"><p class="muted small">Upload a root filesystem tarball (<code>docker export</code>, debootstrap, Buildroot …, .tar / .tar.gz). The result is a bare microVM: use the Terminal to work inside it.</p>
      <div class="row"><div><label>Image name</label><input type="text" name="name" required></div><div><label>Tarball</label><input type="file" name="file" accept=".tar,.gz,.tgz,.zst" required></div></div>
      <div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Import</button></div></form>`,
    official: `<form data-form="b-official"><p class="muted small">Downloads the Ubuntu root filesystem that the Firecracker project publishes for its getting-started guide and CI, converts it to ext4 and boots it with the built-in init (no systemd). Needs <code>unsquashfs</code> (squashfs-tools). ~2 GiB free space is added.</p>
      <label>Image name</label><input type="text" name="name" value="ubuntu-official"><div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Download & build</button></div></form>`,
  };
  const missing = []; if (!S.health.tools.skopeo || !S.health.tools.umoci) missing.push('skopeo/umoci'); if (!S.health.tools.fakeroot) missing.push('fakeroot (file ownership will be flattened)');
  return { html: `<div class="top"><h1>Build an image</h1></div>${missing.length ? `<div class="banner small">Missing on the host: ${esc(missing.join(', '))} — run <code>./install.sh</code>.</div>` : ''}
    <div class="tabs">${T.map(([k, l]) => `<a href="#/build/${k}" class="${k === sub ? 'on' : ''}">${l}</a>`).join('')}</div>
    <div class="panel">${forms[sub] || forms.registry}</div><div class="panel flat"><div class="head"><h2>Jobs</h2></div><div id="joblist">${jobsTable(S.jobs)}</div></div>`,
    live: async () => { await loadJobs(); const el = $('#joblist'); if (el) el.innerHTML = jobsTable(S.jobs); } };
};

/* ---------------------------------------------------------------- activity + help */
routes.activity = async () => {
  S.audit = await api('/audit'); await loadJobs();
  return { html: `<div class="top"><h1>Activity</h1></div><div class="panel flat"><div class="head"><h2>Jobs</h2></div>${jobsTable(S.jobs)}</div>
    <div class="panel flat"><div class="head"><h2>Audit log</h2><span class="muted small">every state-changing request, newest first</span></div>${S.audit.length ? `<table><tr><th>Time</th><th>From</th><th>Request</th><th>Result</th></tr>${S.audit.map((a) => `<tr><td class="small">${esc(a.ts.replace('T', ' ').slice(0, 19))}</td><td class="mono small">${esc(a.actor)}</td><td class="mono small">${esc(a.action)}</td><td><span class="tag ${a.ok ? 'ok' : 'bad'}">${esc(a.detail)}</span></td></tr>`).join('')}</table>` : '<div class="empty">Nothing yet.</div>'}</div>` };
};
routes.help = async () => {
  await loadHealth();
  const F = [
    ['Replica sets', 'Run N identical microVMs from one image: scale live, start/stop together, self-healing, rolling updates, and an optional round-robin load balancer on one host port.'],
    ['Build from GitHub', 'Point at a repo + your own Dockerfile or compose yml (no framework detection). Rebuild by hand, on a signed push webhook, or by polling; optionally roll the new image out to a replica set.'],
    ['Boot from OCI images', 'Any registry image with a shell becomes a microVM: layers unpacked without executing them, ENV/WORKDIR/ENTRYPOINT/CMD honoured, EXPOSE ports shown.'],
    ['Dockerfile & Git builds', 'RUN steps execute inside a disposable builder microVM, not on the host.'],
    ['Official rootfs & kernel', 'Fetches the Firecracker CI kernel (6.1 line preferred) and the Ubuntu rootfs the project publishes.'],
    ['Jailer isolation', 'With the helper installed every VM runs through Firecracker\'s jailer: chroot + pivot_root, own mount namespace, cgroup v2 CPU/memory/pids limits, unprivileged uid, plus Firecracker\'s built-in seccomp filters.'],
    ['Pause / resume', 'Freeze vCPUs instantly and continue later (PATCH /vm).'],
    ['Snapshots', 'Full snapshots (memory + device state + disk) and restore into a fresh Firecracker process.'],
    ['Memory balloon', 'Reclaim RAM from a running guest and return it, with guest statistics.'],
    ['Rate limiters', 'Token-bucket limits on network RX/TX and disk bandwidth/IOPS, adjustable while the VM runs.'],
    ['Metadata service (MMDS v2)', 'Hand configuration to the guest through 169.254.169.254 without any network setup.'],
    ['virtio-vsock agent', 'Terminal, file upload/download, graceful shutdown and live stats go over vsock: no SSH keys, no open ports.'],
    ['CPU templates, SMT, huge pages', 'Per-VM CPU feature masking for a uniform fleet, hyper-thread control, 2 MiB host pages.'],
    ['Dirty-page tracking', 'Enable it to prepare differential snapshots.'],
    ['Networking', 'Per-VM TAP on a bridge, NAT out, no VM-to-VM traffic, LAN and host services blocked, optional published ports.'],
    ['Metrics & logs', 'Firecracker\'s metrics device and logger, serial console, host RSS and CPU cost of every VM.'],
    ['Survives restarts', 'Restarting the web app does not stop your VMs; they are adopted again.'],
  ];
  return { html: `<div class="top"><h1>What you can do</h1></div><div class="panel">${F.map(([a, b]) => `<div class="feature"><b>${esc(a)}</b><span class="muted">${esc(b)}</span></div>`).join('')}</div>
    <div class="panel"><h2>Not available through Firecracker</h2><p class="muted small">Firecracker is deliberately minimal: no GPU/USB/PCI passthrough, no live migration, no host-folder mounts, and the guest needs a kernel built for it. Use the Terminal/Files tabs or the disk export instead.</p></div>` };
};

/* ---------------------------------------------------------------- actions */
const act = {
  theme() { const r = document.documentElement; const dark = (r.dataset.theme || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')) === 'dark'; r.dataset.theme = dark ? 'light' : 'dark'; localStorage.setItem('mvm-theme', r.dataset.theme); },
  async logout() { await api('/logout', { method: 'POST' }); location.href = '/login'; },
  'new-vm'() { newVmDialog(); },
  'new-vm-from'(t) { newVmDialog(t.dataset.name); },
  close() { closeModal(); },
  'modal-bg'(t, e) { if (e.target === t) closeModal(); },
  'open-vm'(t) { nav(`#/vm/${t.dataset.id}`); },
  async kernel(t) { await busy(t, async () => { const j = await api('/kernel', { method: 'POST', json: {} }); toast('Kernel download started'); watchJob(j.id); }); },
  async pull(t) { await busy(t, async () => { const j = await api('/images/oci', { method: 'POST', json: { ref: t.dataset.ref, name: t.dataset.name } }); toast('Build started'); watchJob(j.id); }); },
  async 'img-del'(t) { if (!confirm(`Delete image ${t.dataset.name}?`)) return; await busy(t, async () => { await api(`/images/${encodeURIComponent(t.dataset.name)}`, { method: 'DELETE' }); toast('Image deleted'); await render(); }); },
  job(t) { watchJob(t.dataset.id); },
  async 'vm-start'(t) { await vmCall(t, 'start', 'Started'); },
  async 'vm-stop'(t) { await vmCall(t, 'stop', 'Shut down'); },
  async 'vm-kill'(t) { if (confirm('Force stop kills the VM immediately (like pulling the plug). Continue?')) await vmCall(t, 'kill', 'Stopped'); },
  async 'vm-restart'(t) { await vmCall(t, 'restart', 'Restarted'); },
  async 'vm-pause'(t) { await vmCall(t, 'pause', 'Paused'); },
  async 'vm-resume'(t) { await vmCall(t, 'resume', 'Resumed'); },
  async 'vm-clone'(t) { await busy(t, async () => { const v = await api(`/vms/${t.dataset.id}/clone`, { method: 'POST', json: {} }); toast('Cloned'); nav(`#/vm/${v.id}`); }); },
  async 'vm-delete'(t) { if (!confirm('Delete this microVM, its disk and its snapshots?')) return; await busy(t, async () => { await api(`/vms/${t.dataset.id}`, { method: 'DELETE' }); toast('Deleted'); nav('#/vms'); }); },
  async 'snap-restore'(t) { if (!confirm('Restore this snapshot? The VM disk and memory return to the snapshot state.')) return; await busy(t, async () => { await api(`/vms/${t.dataset.id}/snapshots/${t.dataset.sid}/restore`, { method: 'POST', json: {} }); toast('Restored and running'); await render(); }); },
  async 'snap-del'(t) { if (!confirm('Delete this snapshot?')) return; await busy(t, async () => { await api(`/vms/${t.dataset.id}/snapshots/${t.dataset.sid}`, { method: 'DELETE' }); await render(); }); },
  async 'fwd-del'(t) { await busy(t, async () => { await api(`/vms/${t.dataset.id}/forwards/${t.dataset.fid}`, { method: 'DELETE' }); await render(); }); },
};
async function vmCall(btn, what, msg) {
  await busy(btn, async () => { await api(`/vms/${btn.dataset.id}/${what}`, { method: 'POST', json: {} }); toast(msg); await loadCore(); await render(); });
}
document.addEventListener('click', (e) => {
  const t = e.target.closest('[data-act]');
  if (!t) return;
  if (t.tagName === 'A' && !t.dataset.act) return;
  if (e.target.closest('button, a') && e.target.closest('button, a') !== t && t.classList.contains('rack')) return;
  const fn = act[t.dataset.act];
  if (e.target.closest('.menu .pop')) $$('details.menu[open]').forEach((d) => { d.open = false; });
  if (fn) { e.stopPropagation(); fn(t, e); }
});
document.addEventListener('input', (e) => { if (e.target.id === 'brange') $('#bv').textContent = e.target.value; });

/* ---------------------------------------------------------------- forms */
const forms = {
  async 'create-vm'(f, btn) {
    const d = readVmForm(f); d.image = f.elements.image.value; d.start = f.elements.start.checked;
    if (!d.name) delete d.name;
    const v = await api('/vms', { method: 'POST', json: d }); closeModal(); toast(d.start ? 'microVM started' : 'microVM created'); nav(`#/vm/${v.id}`);
  },
  async settings(f, btn) { const d = readVmForm(f); delete d.name; await api(`/vms/${btn.form.dataset.id}`, { method: 'PATCH', json: d }); toast('Saved'); await render(); },
  async exec(f) {
    const id = f.dataset.id, t = S.term[id], inp = f.elements.cmd, cmd = inp.value.trim();
    if (!cmd) return;
    t.hist.push(cmd); t.hi = t.hist.length; inp.value = '';
    t.out += `<span class="p">${esc(t.cwd)} $ </span>${esc(cmd)}\n`;
    const wrapped = `${cmd}\n__rc=$?; printf '\\n__MVMPWD__%s' "$PWD"; exit $__rc`;
    const r = await api(`/vms/${id}/exec`, { method: 'POST', json: { cmd: wrapped, workdir: t.cwd, timeout: 120 } });
    let so = r.stdout, m = so.match(/\n?__MVMPWD__(.*)$/s);
    if (m) { t.cwd = m[1].trim() || t.cwd; so = so.slice(0, m.index); }
    t.out += esc(so) + (r.stderr ? `<span class="e">${esc(r.stderr)}</span>` : '') + (so && !so.endsWith('\n') ? '\n' : '') + (r.exit_code ? `<span class="d">[exit ${r.exit_code}]</span>\n` : '');
    const o = $('#termout'); if (o) { o.innerHTML = t.out; o.scrollTop = o.scrollHeight; } const c = $('#cwd'); if (c) c.textContent = t.cwd;
  },
  async upload(f) {
    const fd = new FormData(f); const r = await fetch(`/api/vms/${f.dataset.id}/files`, { method: 'POST', body: fd, headers: { 'X-CSRF-Token': CSRF }, credentials: 'same-origin' });
    const j = await r.json(); if (!r.ok) throw new Error(j.error); toast('Uploaded to ' + j.path);
  },
  async download(f) { const p = f.elements.path.value; location.href = `/api/vms/${f.dataset.id}/files?path=${encodeURIComponent(p)}`; },
  async snapshot(f) { const d = Object.fromEntries(new FormData(f)); await api(`/vms/${f.dataset.id}/snapshots`, { method: 'POST', json: d }); toast('Snapshot created'); await render(); },
  async forward(f) { const d = Object.fromEntries(new FormData(f)); await api(`/vms/${f.dataset.id}/forwards`, { method: 'POST', json: d }); toast('Port published'); await render(); },
  async live(f) {
    const d = {}; for (const [k, v] of new FormData(f)) d[k] = k === 'mmds' ? JSON.parse(v) : +v;
    await api(`/vms/${f.dataset.id}/live`, { method: 'POST', json: d }); toast('Applied'); await loadCore();
  },
  async 'b-oci'(f) {
    const d = Object.fromEntries(new FormData(f)); d.refresh = f.elements.refresh.checked; d.force = f.elements.force.checked; d.extra_mb = +d.extra_mb;
    d.env = {}; (d.env_text = d.env === undefined ? '' : '');
    const envt = f.elements.env.value; d.env = {}; envt.split('\n').map((l) => l.trim()).filter(Boolean).forEach((l) => { const i = l.indexOf('='); if (i > 0) d.env[l.slice(0, i)] = l.slice(i + 1); });
    ['entrypoint', 'cmd', 'name'].forEach((k) => { if (!d[k]) delete d[k]; }); delete d.env_text;
    const j = await api('/images/oci', { method: 'POST', json: d }); watchJob(j.id);
  },
  async 'b-docker'(f) {
    const d = { name: f.elements.name.value, dockerfile: f.elements.dockerfile.value }; const ft = f.elements.files.value.trim();
    if (ft) { try { d.files = JSON.parse(ft); } catch (e) { throw new Error('Files must be valid JSON'); } }
    const j = await api('/images/dockerfile', { method: 'POST', json: d }); watchJob(j.id);
  },
  async 'b-git'(f) { const j = await api('/images/git', { method: 'POST', json: Object.fromEntries(new FormData(f)) }); watchJob(j.id); },
  async 'b-compose'(f) { const j = await api('/images/compose', { method: 'POST', json: { project: f.elements.project.value, text: f.elements.text.value, refresh: f.elements.refresh.checked } }); watchJob(j.id); },
  async 'b-tar'(f) {
    const r = await fetch('/api/images/import', { method: 'POST', body: new FormData(f), headers: { 'X-CSRF-Token': CSRF }, credentials: 'same-origin' });
    const j = await r.json(); if (!r.ok) throw new Error(j.error); watchJob(j.id);
  },
  async 'b-official'(f) { const j = await api('/images/official', { method: 'POST', json: { name: f.elements.name.value } }); watchJob(j.id); },
};
document.addEventListener('submit', async (e) => {
  const f = e.target.closest('form[data-form]');
  if (!f) return;
  e.preventDefault();
  const btn = e.submitter || $('button[type=submit]', f);
  await busy(btn, () => forms[f.dataset.form](f, btn));
});
document.addEventListener('keydown', (e) => {
  if (e.target.id !== 'termcmd') return;
  const id = e.target.form.dataset.id, t = S.term[id];
  if (e.key === 'ArrowUp' && t.hist.length) { t.hi = Math.max(0, t.hi - 1); e.target.value = t.hist[t.hi] || ''; e.preventDefault(); }
  if (e.key === 'ArrowDown') { t.hi = Math.min(t.hist.length, t.hi + 1); e.target.value = t.hist[t.hi] || ''; e.preventDefault(); }
});


/* ---------------------------------------------------------------- replica sets */
const rsEndpoint = (rs) => (rs.lb_port ? `${rs.lb_bind === '0.0.0.0' ? location.hostname : '127.0.0.1'}:${rs.lb_port}` : null);
const dots = (rs) => rs.replicas.map((r) => `<span class="led ${esc(r.ready ? 'running' : r.status)}" title="${esc(r.name)}: ${esc(r.status)}${r.ready ? ' (ready)' : ''}" style="margin-right:4px"></span>`).join('') || '<span class="faint small">no replicas</span>';
function rsRows(list) {
  if (!list.length) return `<div class="empty">No replica sets yet.<br>A replica set runs several identical microVMs from one image.${READONLY ? '' : '<br><br><button class="primary" data-act="new-rs">Create a replica set</button>'}</div>`;
  return list.map((rs) => `<div class="rack" data-act="open-rs" data-id="${esc(rs.id)}" style="grid-template-columns:minmax(180px,1.2fr) 1fr 1fr auto">
    <div>${led(rs.state === 'running' ? (rs.ready === rs.desired && rs.desired ? 'running' : 'starting') : 'stopped')}<span class="nm">${esc(rs.name)}</span><div class="sub">${esc(rs.image)} · ${rs.vcpu} vCPU · ${fmtB(rs.mem * 1048576)} each</div></div>
    <div class="m">Ready <b>${rs.ready} / ${rs.desired}</b>${dots(rs)}</div>
    <div class="m">Entry point <b>${rs.lb_port ? esc(rsEndpoint(rs)) : '–'}</b>${rs.lb && rs.lb.stats ? rs.lb.stats.total + ' conns' : rs.lb_port ? 'balancer off' : 'no load balancer'}</div>
    <div class="actions"><span class="tag ${rs.state === 'running' ? 'ok' : ''}">${esc(rs.state)}</span></div></div>`).join('');
}
routes.replicas = async () => {
  await Promise.all([loadCore(), api('/replicasets').then((r) => { S.rsets = r; })]);
  return { html: `<div class="top"><h1>Replica sets</h1><span class="grow"></span><button class="primary" data-act="new-rs" ${ro}>New replica set</button></div>
    <p class="muted small" style="margin-top:-8px">Several identical microVMs from one image — scale them, update them one by one, and let them heal themselves.</p>
    <div class="panel flat"><div id="rslist">${rsRows(S.rsets)}</div></div>`,
    live: async () => { S.rsets = await api('/replicasets'); const el = $('#rslist'); if (el) el.innerHTML = rsRows(S.rsets); } };
};
function rsTable(rs) {
  return `<table><tr><th>Replica</th><th>State</th><th>Address</th><th>Ready</th><th>Up</th><th></th></tr>${rs.replicas.map((r) => `<tr>
    <td>${led(r.status)}<a href="#/vm/${esc(r.id)}"><b>${esc(r.name)}</b></a></td><td><span class="tag ${tagFor(r.status)}">${esc(r.status)}</span></td>
    <td class="mono small">${esc(r.ip || '–')}${rs.service_port && r.ip && r.status === 'running' ? ` · <a target="_blank" rel="noopener" href="http://${esc(r.ip)}:${rs.service_port}">open</a>` : ''}</td>
    <td>${r.ready ? '<span class="tag ok">ready</span>' : '<span class="tag">–</span>'}</td><td class="small">${r.uptime_s != null ? fmtDur(r.uptime_s) : '–'}</td>
    <td class="num"><button class="sm" data-act="vm-restart" data-id="${esc(r.id)}" ${ro}>Restart</button></td></tr>${r.error ? `<tr><td colspan="6" class="small" style="color:var(--warn)">${esc(r.error)}</td></tr>` : ''}`).join('') || '<tr><td colspan="6" class="empty">No replicas.</td></tr>'}</table>`;
}
routes.rs = async (id) => {
  await loadCore();
  const rs = await api(`/replicasets/${id}`).catch(() => null);
  if (!rs) return { html: '<div class="empty">That replica set no longer exists. <a href="#/replicas">Back</a></div>' };
  const pseudo = { vcpu: rs.vcpu, mem: rs.mem, disk_mb: rs.disk_mb || 0, running: false, config: rs.config };
  const st = rs.lb && rs.lb.stats;
  return { html: `<div class="top"><span class="crumbs"><a href="#/replicas">Replica sets</a> / <b>${esc(rs.name)}</b></span></div>
    <div class="vmhead" style="margin-bottom:16px"><span class="fav">${ico('layers')}<span class="dot ${rs.state === 'running' ? (rs.ready === rs.desired ? 'running' : 'starting') : 'paused'}"></span></span><div><h1>${esc(rs.name)}<span class="pill ${rs.state === 'running' ? 'ok' : ''}">${esc(rs.state)}</span></h1>
      <div class="meta"><span id="rshead">${rs.ready}/${rs.desired} ready · image ${esc(rs.image)}</span></div></div><span class="grow"></span>
      <div class="actions"><button data-act="rs-start" data-id="${esc(rs.id)}" ${ro}>Start all</button><button data-act="rs-stop" data-id="${esc(rs.id)}" ${ro}>Stop all</button><button data-act="rs-restart" data-id="${esc(rs.id)}" ${ro}>Restart all</button>
      <button class="primary" data-act="rs-rollout" data-id="${esc(rs.id)}" ${ro} title="Recreate every replica from the current image and template, one at a time">Rolling update</button><button class="danger" data-act="rs-delete" data-id="${esc(rs.id)}" ${ro}>Delete</button></div></div>
    <div id="rsnote">${rs.note ? `<div class="banner small">${esc(rs.note)}</div>` : ''}</div>
    <div class="grid g2"><div class="panel"><h2>Scale</h2><p class="muted small">Scaling down removes the highest-numbered replicas first. Each replica gets <code>REPLICA_INDEX</code> and <code>REPLICA_SET</code> as environment variables.</p>
      <form data-form="rs-scale" data-id="${esc(rs.id)}" class="row"><div class="auto"><button type="button" data-act="rs-step" data-d="-1">−</button></div><div><label>Replicas</label><input type="number" name="count" min="0" max="32" value="${rs.desired}"></div><div class="auto"><button type="button" data-act="rs-step" data-d="1">+</button></div><div class="auto"><button class="primary" type="submit" ${ro}>Apply</button></div></form></div>
      <div class="panel"><h2>Entry point</h2>${rs.lb_port ? `<p>Round-robin balancer on <code>${esc(rs.lb_bind)}:${rs.lb_port}</code> → replicas :${rs.service_port}</p><div id="lbstats" class="small muted">${st ? `${st.total} connections · ${st.active} active · ${st.failed} failed · ${esc(Object.entries(st.per).map(([k, v]) => k + ' ' + v).join(', '))}` : 'not running (set is stopped?)'}</div>` : '<p class="muted small">No load balancer. Reach each replica by its own address, or add a port below to get one stable address that spreads connections across healthy replicas (TCP).</p>'}</div></div>
    <div class="panel flat" style="margin-top:16px"><div class="head"><h2>Replicas</h2><span class="muted small">${rs.autoheal ? 'auto-heal on: crashed or deleted replicas come back' : 'auto-heal off'}</span></div><div id="rstable">${rsTable(rs)}</div></div>
    <form class="panel" data-form="rs-settings" data-id="${esc(rs.id)}"><h2>Template</h2><p class="muted small">Applies to new replicas right away; run a <b>Rolling update</b> to apply it to existing ones.</p>
      <label>Image</label><select name="image">${S.images.map((i) => `<option ${i.name === rs.image ? 'selected' : ''}>${esc(i.name)}</option>`).join('')}</select>
      ${vmFields(pseudo, 'edit')}
      <h3>Replica set options</h3><label class="inline"><input type="checkbox" name="autoheal" ${rs.autoheal ? 'checked' : ''}> Auto-heal</label>
      <div class="row"><div><label>Load balancer port (host, ≥1024; empty = none)</label><input type="number" name="lb_port" min="1024" max="65535" value="${rs.lb_port || ''}"></div><div><label>Service port in the VMs</label><input type="number" name="service_port" min="1" max="65535" value="${rs.service_port || ''}"></div>
      <div><label>Listen on</label><select name="lb_bind"><option value="127.0.0.1" ${rs.lb_bind !== '0.0.0.0' ? 'selected' : ''}>this host only</option><option value="0.0.0.0" ${rs.lb_bind === '0.0.0.0' ? 'selected' : ''}>all interfaces (reachable from the network)</option></select></div></div>
      <div class="actions" style="margin-top:14px"><button class="primary" type="submit" ${ro}>Save template</button></div></form>`,
    live: async () => {
      const n = await api(`/replicasets/${id}`).catch(() => null); if (!n) return nav('#/replicas');
      const t = $('#rstable'); if (t) t.innerHTML = rsTable(n); const h = $('#rshead'); if (h) h.textContent = `${n.ready}/${n.desired} ready · image ${n.image}`;
      const nt = $('#rsnote'); if (nt) nt.innerHTML = n.note ? `<div class="banner small">${esc(n.note)}</div>` : '';
      const ls = n.lb && n.lb.stats, l = $('#lbstats'); if (l && ls) l.textContent = `${ls.total} connections · ${ls.active} active · ${ls.failed} failed · ${Object.entries(ls.per).map(([k, v]) => k + ' ' + v).join(', ')}`;
    } };
};
function rsExtras(img) {
  return `<div class="row"><div><label>Replicas</label><input type="number" name="count" min="1" max="32" value="2"></div></div>
    <div class="row"><div><label>Load balancer port (optional, ≥1024)</label><input type="number" name="lb_port" min="1024" max="65535" placeholder="none"></div><div><label>Service port in the VMs</label><input type="number" name="service_port" min="1" max="65535" placeholder="${img && img.ports && img.ports[0] ? esc(parseInt(img.ports[0])) : '80'}"></div>
    <div><label>Listen on</label><select name="lb_bind"><option value="127.0.0.1">this host only</option><option value="0.0.0.0">all interfaces</option></select></div></div>
    <label class="inline"><input type="checkbox" name="autoheal" checked> Auto-heal</label>`;
}
function newRsDialog(imageName) {
  if (!S.images.length) { toast('Build or pull an image first', true); nav('#/build'); return; }
  const img = imgByName(imageName) || S.images[0];
  openModal(`<h2>New replica set</h2><form data-form="create-rs"><label>Image</label><select name="image">${S.images.map((i) => `<option value="${esc(i.name)}" ${i.name === img.name ? 'selected' : ''}>${esc(i.name)}</option>`).join('')}</select>
    ${vmFields(null, 'new')}${rsExtras(img)}
    <div class="actions" style="margin-top:16px"><label class="inline"><input type="checkbox" name="start" checked> Start now</label><span style="flex:1"></span><button type="button" data-act="close">Cancel</button><button class="primary" type="submit">Create</button></div></form>`);
  const n = $('#modal input[name=name]'); if (n) { n.placeholder = 'replica set name (required)'; n.required = true; }
}

/* ---------------------------------------------------------------- GitHub builds */
const srcStatus = (s) => (s.building ? '<span class="tag warn">building</span>' : s.last_status === 'ok' ? '<span class="tag ok">built</span>' : s.last_status === 'failed' ? '<span class="tag bad">failed</span>' : s.last_status ? `<span class="tag">${esc(s.last_status)}</span>` : '<span class="tag">never built</span>');
function srcRows(list) {
  if (!list.length) return `<div class="empty">No GitHub sources yet.<br>Add a repository and choose automatic detection (Railpack), a Dockerfile, or Compose.${READONLY ? '' : '<br><br><button class="primary" data-act="new-src">Add a source</button>'}</div>`;
  return `<table><tr><th>Source</th><th>Builds</th><th>Last commit</th><th>Status</th><th></th></tr>${list.map((s) => `<tr><td><b>${esc(s.name)}</b><div class="faint small mono">${esc(s.repo.replace('https://', ''))} @ ${esc(s.branch)}</div></td>
    <td class="small"><span class="tag">${esc(s.kind)}</span> <code>${esc(s.path)}</code><div class="faint small">→ ${s.kind === 'compose' ? 'images ' + esc(s.project) + '-<service>' : 'image ' + esc(s.image_name)}</div></td>
    <td class="mono small">${s.last_commit ? esc(s.last_commit.slice(0, 8)) : '–'}<div class="faint">${s.last_built ? ago(s.last_built) : ''}</div></td><td>${srcStatus(s)}${s.auto_rebuild ? `<div class="faint small">${s.poll_minutes ? 'polls every ' + s.poll_minutes + ' min' : 'on webhook'}</div>` : ''}</td>
    <td class="num"><button class="sm primary" data-act="src-build" data-id="${esc(s.id)}" ${ro}>Build now</button> <button class="sm" data-act="src-check" data-id="${esc(s.id)}">Check</button> <button class="sm" data-act="src-hook" data-id="${esc(s.id)}">Webhook</button> <button class="sm danger" data-act="src-del" data-id="${esc(s.id)}" ${ro}>Delete</button></td></tr>`).join('')}</table>`;
}
routes.sources = async () => {
  await Promise.all([loadJobs(), loadCore(), api('/sources').then((r) => { S.sources = r; })]);
  return { html: `<div class="top"><h1>Build from GitHub</h1><span class="grow"></span><button class="primary" data-act="new-src" ${ro}>Add a source</button></div>
    <p class="muted small" style="margin-top:-8px">Build from GitHub with <b>Railpack auto-detection</b> (no Dockerfile required), a Dockerfile, or Compose. Builds produce <b>images</b> only (Compose: one image per service). Then launch them or put them in a <a href="#/replicas">replica set</a>.</p>
    <div class="panel flat"><div id="srclist">${srcRows(S.sources)}</div></div><div class="panel flat"><div class="head"><h2>Jobs</h2></div><div id="joblist">${jobsTable(S.jobs)}</div></div>`,
    live: async () => { await loadJobs(); S.sources = await api('/sources'); const a = $('#srclist'); if (a) a.innerHTML = srcRows(S.sources); const j = $('#joblist'); if (j) j.innerHTML = jobsTable(S.jobs); } };
};
function srcDialog() {
  openModal(`<h2>Add a GitHub source</h2><form data-form="create-src">
    <div class="row"><div><label>Name</label><input type="text" name="name" required pattern="[a-z0-9][a-z0-9._-]*" maxlength="48"></div><div><label>Branch</label><input type="text" name="branch" value="main"></div></div>
    <label>Repository (https://github.com/owner/repo — public, or private with MVM_GIT_TOKEN on the server)</label><input type="text" name="repo" required placeholder="https://github.com/owner/repo">
    <div class="row"><div><label>Build method</label><select name="kind" id="srckind"><option value="railpack">Auto Detect (Railpack)</option><option value="dockerfile">Dockerfile</option><option value="compose">Docker Compose</option></select></div><div id="srcpathwrap" style="display:none"><label>Dockerfile / Compose path</label><input type="text" name="path" id="srcpath" value="."></div></div>
    <p id="srcmodehelp" class="muted small">Railpack will inspect the repository, detect the application framework, and create the build plan. A Dockerfile is not required.</p>
    <div class="row" id="srcdf"><div><label>Build context (folder, relative to the repo root)</label><input type="text" name="context" id="srccontext" value="."></div><div><label>Image name</label><input type="text" name="image_name" placeholder="defaults to the source name"></div></div>
    <div class="row" id="srccp" style="display:none"><div><label>Image name prefix (images become prefix-service)</label><input type="text" name="project" placeholder="defaults to the source name"></div></div>
    <h3>Rebuild when someone pushes</h3><label class="inline"><input type="checkbox" name="auto_rebuild"> Rebuild automatically</label>
    <div class="row"><div><label>Poll GitHub every N minutes (0 = rely on the webhook)</label><input type="number" name="poll_minutes" min="0" max="1440" value="5"></div><div><label>Then roll out to replica set</label><select name="rollout_set"><option value="">— none —</option>${S.rsets.map((r) => `<option value="${esc(r.id)}">${esc(r.name)}</option>`).join('')}</select></div></div>
    <div class="actions" style="margin-top:16px"><span style="flex:1"></span><button type="button" data-act="close">Cancel</button><button class="primary" type="submit">Save</button></div></form>`);
  syncSourceKind();
}
function syncSourceKind() {
  const kind = $('#srckind')?.value || 'railpack';
  const compose = kind === 'compose';
  const railpack = kind === 'railpack';
  $('#srcpathwrap').style.display = railpack ? 'none' : '';
  $('#srcdf').style.display = compose ? 'none' : '';
  $('#srccp').style.display = compose ? '' : 'none';
  $('#srcpath').value = compose ? 'docker-compose.yml' : railpack ? '.' : 'Dockerfile';
  $('#srccontext').value = '.';
  $('#srcmodehelp').textContent = railpack
    ? 'Railpack will inspect the repository, detect the application framework, and create the build plan. A Dockerfile is not required.'
    : compose
      ? 'Each Compose service is built or pulled as a separate image. Compose networking and volumes are not deployed as a stack.'
      : 'BuildKit will execute the Dockerfile inside a temporary Firecracker builder microVM. The ext4 disk is sized from the extracted rootfs, with 64 MiB extra free space by default.';
}
async function hookDialog(id) {
  const s = await api(`/sources/${id}?secret=1`);
  const url = location.origin + s.webhook_path;
  openModal(`<h2>Webhook for ${esc(s.name)}</h2>
    <p class="muted small">In GitHub: <b>Settings → Webhooks → Add webhook</b>, then paste these. GitHub must be able to reach the URL (a public HTTPS address in front of this server; for local use try a tunnel). Add that host name to <code>MVM_ALLOWED_HOSTS</code>. No public address? Turn on <b>polling</b> instead — it needs nothing inbound.</p>
    <label>Payload URL</label><input type="text" readonly value="${esc(url)}"><label>Content type</label><input type="text" readonly value="application/json">
    <label>Secret</label><input type="text" readonly value="${esc(s.secret)}"><label>Events</label><input type="text" readonly value="Just the push event">
    <p class="faint small" style="margin-top:10px">Requests are accepted only with a valid HMAC-SHA256 signature, only for pushes to <code>${esc(s.branch)}</code>, and only when "Rebuild automatically" is on. A commit that was already built is skipped.</p>
    <div class="actions" style="margin-top:12px"><button data-act="src-rotate" data-id="${esc(s.id)}">Rotate secret</button><span style="flex:1"></span><button class="primary" data-act="close">Done</button></div>`);
}

Object.assign(act, {
  'new-rs'() { newRsDialog(); }, 'new-rs-from'(t) { newRsDialog(t.dataset.name); }, 'open-rs'(t) { nav(`#/rs/${t.dataset.id}`); },
  async 'rs-start'(t) { await rsCall(t, 'start', 'Started'); }, async 'rs-stop'(t) { await rsCall(t, 'stop', 'Stopped'); }, async 'rs-restart'(t) { await rsCall(t, 'restart', 'Restarted'); },
  async 'rs-rollout'(t) { if (!confirm('Rolling update recreates every replica from the current image, one at a time. Continue?')) return; await busy(t, async () => { const j = await api(`/replicasets/${t.dataset.id}/rollout`, { method: 'POST', json: {} }); watchJob(j.id); }); },
  async 'rs-delete'(t) { if (!confirm('Delete this replica set and ALL its microVMs?')) return; await busy(t, async () => { await api(`/replicasets/${t.dataset.id}`, { method: 'DELETE' }); toast('Deleted'); nav('#/replicas'); }); },
  'rs-step'(t) { const i = t.closest('form').elements.count; i.value = Math.max(0, Math.min(32, (+i.value || 0) + (+t.dataset.d))); },
  'new-src'() { srcDialog(); },
  async 'src-build'(t) { await busy(t, async () => { const j = await api(`/sources/${t.dataset.id}/build`, { method: 'POST', json: {} }); watchJob(j.id); }); },
  async 'src-check'(t) { await busy(t, async () => { const r = await api(`/sources/${t.dataset.id}/check`, { method: 'POST', json: {} }); toast(r.behind ? `New commit ${r.remote.slice(0, 8)} available (built: ${r.built ? r.built.slice(0, 8) : 'never'})` : `Up to date at ${r.remote.slice(0, 8)}`); }); },
  async 'src-hook'(t) { await busy(t, () => hookDialog(t.dataset.id)); },
  async 'src-rotate'(t) { await busy(t, async () => { await api(`/sources/${t.dataset.id}/rotate-secret`, { method: 'POST', json: {} }); toast('Secret rotated - update it in GitHub'); await hookDialog(t.dataset.id); }); },
  async 'src-del'(t) { if (!confirm('Delete this source? Built images are kept.')) return; await busy(t, async () => { await api(`/sources/${t.dataset.id}`, { method: 'DELETE' }); await render(); }); },
});
async function rsCall(btn, what, msg) { await busy(btn, async () => { await api(`/replicasets/${btn.dataset.id}/${what}`, { method: 'POST', json: {} }); toast(msg); await render(); }); }
document.addEventListener('change', (e) => {
  if (e.target.id === 'srckind') syncSourceKind();
});
Object.assign(forms, {
  async 'create-rs'(f) {
    const d = readVmForm(f); d.image = f.elements.image.value; d.count = +f.elements.count.value; d.start = f.elements.start.checked; d.autoheal = f.elements.autoheal.checked;
    d.lb_port = +f.elements.lb_port.value || null; d.service_port = +f.elements.service_port.value || null; d.lb_bind = f.elements.lb_bind.value;
    if (d.lb_port && !d.service_port) throw new Error('Set the service port the VMs listen on');
    const rs = await api('/replicasets', { method: 'POST', json: d }); closeModal(); toast('Replica set created'); nav(`#/rs/${rs.id}`);
  },
  async 'rs-scale'(f) { await api(`/replicasets/${f.dataset.id}/scale`, { method: 'POST', json: { count: +f.elements.count.value } }); toast('Scaled'); await render(); },
  async 'rs-settings'(f) {
    const d = readVmForm(f); delete d.name; d.image = f.elements.image.value; d.autoheal = f.elements.autoheal.checked;
    d.lb_port = +f.elements.lb_port.value || null; d.service_port = +f.elements.service_port.value || null; d.lb_bind = f.elements.lb_bind.value;
    await api(`/replicasets/${f.dataset.id}`, { method: 'PATCH', json: d }); toast('Saved - run a rolling update to apply to existing replicas'); await render();
  },
  async 'create-src'(f) {
    const d = Object.fromEntries(new FormData(f)); d.auto_rebuild = f.elements.auto_rebuild.checked; d.poll_minutes = +d.poll_minutes || 0;
    ['image_name', 'project', 'rollout_set'].forEach((k) => { if (!d[k]) delete d[k]; });
    const s = await api('/sources', { method: 'POST', json: d }); closeModal(); toast('Source saved'); await render(); hookDialog(s.id);
  },
});


function notifItems() {
  const it = [];
  ((S.health && S.health.problems) || []).forEach((p) => it.push({ k: 'warn', t: p, h: '#/overview' }));
  S.vms.filter((v) => v.status === 'error').forEach((v) => it.push({ k: 'bad', t: `${v.name} failed: ${(v.error || '').split('\n')[0]}`, h: `#/vm/${v.id}` }));
  S.rsets.filter((r) => r.note).forEach((r) => it.push({ k: 'warn', t: `${r.name}: ${r.note}`, h: `#/rs/${r.id}` }));
  S.jobs.filter((j) => j.status === 'error').slice(0, 3).forEach((j) => it.push({ k: 'bad', t: `Job failed: ${j.label}`, h: '#/activity' }));
  return it;
}
const updateNotif = () => { const n = $('#n-notif'); if (n) n.textContent = notifItems().length; };
const PAGES = [['Overview', '#/overview', 'gauge'], ['MicroVMs', '#/vms', 'server'], ['Replica sets', '#/replicas', 'layers'], ['Images', '#/images', 'box'], ['Build an image', '#/build', 'box'], ['GitHub builds', '#/sources', 'git'], ['Activity', '#/activity', 'activity'], ['What you can do', '#/help', 'check']];
function paletteItems(q) {
  const all = [...PAGES.map(([t, h, ic]) => ({ t, h, ic, s: 'page' })), ...S.vms.map((v) => ({ t: v.name, h: `#/vm/${v.id}`, ic: 'server', s: v.status })), ...S.rsets.map((r) => ({ t: r.name, h: `#/rs/${r.id}`, ic: 'layers', s: 'replica set' })), ...S.images.map((i) => ({ t: i.name, h: '#/images', ic: 'box', s: 'image' }))];
  q = q.trim().toLowerCase();
  return all.filter((x) => !q || x.t.toLowerCase().includes(q)).slice(0, 30);
}
function drawPalette(q, sel = 0) {
  const items = paletteItems(q);
  const box = $('#palitems'); if (!box) return;
  box.innerHTML = items.map((x, i) => `<div class="it ${i === sel ? 'sel' : ''}" data-act="goto" data-h="${esc(x.h)}">${ico(x.ic)}${esc(x.t)}<small>${esc(x.s)}</small></div>`).join('') || '<div class="empty">No matches</div>';
}
Object.assign(act, {
  theme() { const r = document.documentElement; r.dataset.theme = (r.dataset.theme || 'dark') === 'dark' ? 'light' : 'dark'; localStorage.setItem('mvm-theme', r.dataset.theme); },
  async refresh(t) { await busy(t, async () => { await render(); }); },
  'host-info'() { nav('#/overview'); },
  async palette() { await loadCore().catch(() => {}); openModal('<div class="pal"><input type="text" id="palq" placeholder="Search pages, microVMs, replica sets, images…" autocomplete="off"><div class="items" id="palitems"></div></div>'); drawPalette(''); const i = $('#palq'); if (i) i.focus(); },
  goto(t) { closeModal(); nav(t.dataset.h); },
  notifs() {
    const it = notifItems();
    openModal(`<h2>Notifications</h2><div class="nlist">${it.length ? it.map((n) => `<div class="it"><span style="color:var(--${n.k === 'bad' ? 'bad' : 'warn'})">${ico('alert')}</span><div><a data-act="goto" data-h="${esc(n.h)}" href="${esc(n.h)}">${esc(n.t)}</a></div></div>`).join('') : '<div class="empty">All clear. Nothing needs attention.</div>'}</div><div class="actions" style="margin-top:12px"><span style="flex:1"></span><button data-act="close">Close</button></div>`);
  },
});
document.addEventListener('input', (e) => { if (e.target.id === 'palq') drawPalette(e.target.value); });
document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); act.palette(); return; }
  if (e.key === 'Escape' && $('#modal').firstChild) closeModal();
  if (e.target.id === 'palq' && e.key === 'Enter') { const f = paletteItems(e.target.value)[0]; if (f) { closeModal(); nav(f.h); } }
});

/* ---------------------------------------------------------------- job log modal */
function watchJob(id) {
  S.watching = id;
  const draw = (j) => {
    openModal(`<h2>${esc(j.label)} <span class="tag ${j.status === 'done' ? 'ok' : j.status === 'error' ? 'bad' : 'warn'}">${esc(j.status)}</span></h2><div class="logbox" id="joblog">${esc(j.log.join('\n') || 'waiting…')}</div>
      <div class="actions" style="margin-top:12px"><span class="muted small">${j.status === 'done' ? 'Finished.' : j.status === 'error' ? 'Failed – see the log.' : 'Running… you can close this window; the job continues.'}</span><span style="flex:1"></span><button data-act="close">Close</button></div>`);
    const l = $('#joblog'); if (l) l.scrollTop = l.scrollHeight;
  };
  const step = async () => {
    if (S.watching !== id || !$('#modal').firstChild && S.watching !== id) return;
    try {
      const j = await api('/jobs/' + id);
      if ($('#modal').firstChild || j.status === 'queued') draw(j);
      if (['done', 'error'].includes(j.status)) { S.watching = null; await loadCore(); if (['images', 'build', 'overview'].includes(cur.name)) render(); return; }
    } catch (e) { return; }
    setTimeout(step, 1200);
  };
  draw({ label: 'Job', status: 'queued', log: [] }); step();
}

/* ---------------------------------------------------------------- boot */
async function tick() {
  if (document.hidden) return;
  try {
    await loadJobs();
    if (!S.healthAt || Date.now() - S.healthAt > 30000) { await loadHealth(); S.healthAt = Date.now(); }
    updateNotif();
    if (cur.live && !$('#modal').firstChild) await cur.live();
  } catch (e) { /* transient */ }
}
(async function boot() {
  const th = localStorage.getItem('mvm-theme'); if (th) document.documentElement.dataset.theme = th;
  try { const s = await api('/session'); CSRF = s.csrf; } catch (e) { return; }
  loadHealth().then(() => { S.healthAt = Date.now(); const h = $('#wshost'); if (h && S.health.hostname) h.textContent = S.health.hostname + ' · Firecracker ' + (S.health.firecracker || ''); updateNotif(); }).catch(() => {});
  await render().catch((e) => { $('#view').innerHTML = `<div class="banner bad">${esc(e.message)}</div>`; });
  setInterval(tick, 2500);
})();
})();
