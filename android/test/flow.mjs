// Full login-to-chat check of the app's interface, in a real browser.
//
// It serves the files the way the APK does (assets/setup.html and assets/static/*, with /app mapped to setup.html or
// static/app.html), puts a stand-in for the Android bridge in front of them, and drives the real screens:
// sign-in form -> dashboard -> chat -> send -> offline queue -> reopen offline -> delivery -> sign out.
// It uses only the files and HTTP, so it runs the same on a laptop and in CI.
//
//   ALLOV_BIN=./allovchat DATABASE_URL=postgres://... node android/test/flow.mjs
import { spawn, execSync } from 'node:child_process';
import https from 'node:https';
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'allov-flow-'));
const APP = 'https://localhost:9443'; // stands in for https://appassets.androidplatform.net
const SRV = 'https://localhost:8443'; // the user's server, over https as the app requires
const RAW = 'http://localhost:8099';

// ---- the APK's assets, laid out as the build does it ----
const assets = path.join(tmp, 'assets');
fs.mkdirSync(path.join(assets, 'static'), { recursive: true });
fs.cpSync(path.join(repo, 'static'), path.join(assets, 'static'), { recursive: true });
fs.copyFileSync(path.join(repo, 'android/app/src/main/assets/setup.html'), path.join(assets, 'setup.html'));

execSync(`openssl req -x509 -newkey rsa:2048 -nodes -keyout ${tmp}/k.pem -out ${tmp}/c.pem -days 2 -subj /CN=localhost 2>/dev/null`);
const tls = { key: fs.readFileSync(`${tmp}/k.pem`), cert: fs.readFileSync(`${tmp}/c.pem`) };

let signedIn = false;
const seen = []; // [status, path] for every request the app made to its own origin
const MIME = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml' };
const appServer = https.createServer(tls, (req, res) => {
  const p = req.url.split('?')[0];
  if (p === '/__signed') { signedIn = req.url.endsWith('1'); res.end('ok'); return; }
  let file = null;
  if (p === '/' || p === '/app' || p === '/app/') file = signedIn ? 'static/app.html' : 'setup.html';
  else if (p.startsWith('/static/') && !p.includes('..')) file = p.slice(1);
  const f = file && path.join(assets, file);
  const ok = f && fs.existsSync(f) && fs.statSync(f).isFile();
  seen.push([ok ? 200 : 404, p]);
  if (!ok) { res.writeHead(404); res.end('not found'); return; }
  res.writeHead(200, { 'Content-Type': MIME[path.extname(f)] || 'application/octet-stream' });
  res.end(fs.readFileSync(f));
}).listen(9443);

// https in front of the Go server (it speaks plain http), streaming so live updates work
const proxy = https.createServer(tls, (req, res) => {
  const up = http.request(RAW + req.url, { method: req.method, headers: req.headers }, (r) => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
  up.on('error', () => { try { res.writeHead(502); res.end(); } catch (e) {} });
  req.pipe(up);
  res.on('close', () => up.destroy());
}).listen(8443);

const bin = process.env.ALLOV_BIN || (execSync('go build -o allov-flow . && echo allov-flow', { cwd: repo }).toString().trim() && path.join(repo, 'allov-flow'));
const db = process.env.DATABASE_URL;
if (!db) { console.error('set DATABASE_URL'); process.exit(2); }
const server = spawn(bin, [], { env: { ...process.env, DATABASE_URL: db, ADMIN_USERNAME: 'admin', ADMIN_PASSWORD: 'longpassphrase123', ADMIN_PATH: 'zeus', PORT: '8099', TRUSTED_PROXY_HOPS: '0', APP_ORIGINS: APP }, stdio: 'ignore', cwd: repo });
await new Promise((r) => setTimeout(r, 2500));

let fails = 0;
const ok = (c, m) => { console.log(c ? 'ok  ' : 'FAIL', m); if (!c) fails++; };
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const J = async (u, o = {}) => { const r = await fetch(RAW + u, { ...o, headers: { 'Content-Type': 'application/json', ...(o.headers || {}) }, body: o.body ? JSON.stringify(o.body) : undefined }); return { r, j: await r.json().catch(() => ({})) }; };

const user = 'flow' + Math.floor(Math.random() * 1e6), pass = 'correct horse';
const reg = await J('/api/register', { method: 'POST', body: { username: user, password: pass } });
const cookie = reg.r.headers.get('set-cookie').split(';')[0];
const link = (await J('/api/links', { method: 'POST', headers: { Cookie: cookie }, body: { label: 'Used bike post', cid: '' } })).j;
await J('/api/l/' + link.slug + '/start', { method: 'POST', body: { message: 'Hello from the guest', token: 'ab'.repeat(16), turnstile: '', pub: '' } });
const mine = (await J('/api/chats', { headers: { Cookie: cookie } })).j;
const tid = mine[0].id;
await J('/api/t/' + tid + '/accept', { method: 'POST', headers: { Cookie: cookie }, body: {} });

async function launch() {
  const args = ['--no-sandbox', '--ignore-certificate-errors'];
  if (process.env.CHROME_PATH) { const p = await import('puppeteer-core'); return (p.default || p).launch({ executablePath: process.env.CHROME_PATH, headless: 'shell', args }); }
  try { const c = (await import('@sparticuz/chromium')).default; const p = await import('puppeteer-core'); return (p.default || p).launch({ args: [...c.args, '--ignore-certificate-errors'], executablePath: await c.executablePath(), headless: 'shell' }); }
  catch (e) { const p = await import('puppeteer'); return (p.default || p).launch({ headless: true, args }); }
}
const browser = await launch();
const page = await browser.newPage();
await page.setViewport({ width: 420, height: 860, isMobile: true, hasTouch: true });
page.on('pageerror', (e) => console.log('PAGE ERROR', e.message));
let blocked = false;
await page.setRequestInterception(true);
page.on('request', (r) => { if (blocked && r.url().startsWith(SRV)) r.abort('internetdisconnected'); else r.continue(); });

// A stand-in for the Kotlin bridge: same methods, same rules (https only, a 48-character token), state kept in localStorage.
await page.evaluateOnNewDocument((APPO) => {
  const real = window.fetch.bind(window); window.__real = real;
  const get = () => { try { return JSON.parse(localStorage.getItem('stub.session') || 'null') || {}; } catch (e) { return {}; } };
  const q = () => JSON.parse(localStorage.getItem('stub.q') || '[]');
  const sync = (v) => { const x = new XMLHttpRequest(); x.open('GET', APPO + '/__signed?' + v, false); x.send(); };
  window.__signedOut = 0;
  window.AndroidApp = {
    server: () => get().server || '', token: () => get().token || '', lastServer: () => get().last || '',
    saveSession(server, token) { if (!server.startsWith('https://') || !/^[a-f0-9]{48}$/.test(token)) return; localStorage.setItem('stub.session', JSON.stringify({ server, token, last: server })); sync(1); },
    signedOut() { window.__signedOut++; const s = get(); localStorage.setItem('stub.session', JSON.stringify({ last: s.last })); localStorage.removeItem('stub.q'); sync(0); location.replace('/app'); },
    enqueue(path, body) { const a = q(); a.push({ path, body, ts: Date.now() }); localStorage.setItem('stub.q', JSON.stringify(a)); return true; },
    pendingFor(path) { return JSON.stringify(q().filter((e) => e.path === path)); },
    flush() { window.__flush && window.__flush(); },
    notificationsOn: () => false, enableNotifications() {}, disableNotifications() {}
  };
  window.__flush = async () => {
    const s = get(); const left = []; let sent = false;
    for (const e of q()) { try { const r = await real(s.server + e.path, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + s.token }, body: e.body }); if (r.status === 401 || r.status >= 500 || r.status === 429) left.push(e); else sent = true; } catch (x) { left.push(e); } }
    localStorage.setItem('stub.q', JSON.stringify(left)); if (sent) window.dispatchEvent(new Event('allov:flushed'));
  };
}, APP);
const txt = (sel) => page.evaluate((s) => (s ? document.querySelector(s) : document.body).innerText, sel || '');
const tokenForApi = (await J('/api/app/login', { method: 'POST', body: { username: user, password: pass } })).j.token;
const msgCount = async (body) => { const s = (await J('/api/t/' + tid + '/state', { headers: { Authorization: 'Bearer ' + tokenForApi } })).j; return (s.messages || []).filter((m) => m.body === body).length; };

try {
// 1. first launch: no session, so /app is the sign-in screen
await page.goto(APP + '/app', { waitUntil: 'networkidle2' }); await wait(500);
ok(await page.evaluate(() => !!document.getElementById('srv')), '/app shows the sign-in screen when there is no session');
ok((await txt()).includes('Connect to your server'), 'the sign-in text is there (styles and scripts loaded from the bundle)');

// 2. https is enforced, wrong password is refused, then the real sign-in
const fill = async (srv, u, p) => { await page.$eval('#srv', (e) => (e.value = '')); await page.type('#srv', srv); await page.$eval('#u', (e) => (e.value = '')); await page.type('#u', u); await page.$eval('#p', (e) => (e.value = '')); await page.type('#p', p); await page.click('#go'); await wait(1200); };
await fill('http://localhost:8099', user, pass);
ok((await txt('#err')).toLowerCase().includes('https'), 'a plain http server address is refused');
await fill(SRV, user, 'wrong wrong');
ok((await txt('#err')).length > 0 && await page.evaluate(() => !!document.getElementById('srv')), 'a wrong password shows an error and stays on the form');
await fill(SRV, user, pass);
await wait(2500);
ok(await page.evaluate(() => !document.getElementById('shell').hidden), 'after signing in, /app opens the bundled interface (no DNS, no hosted site)');
ok(page.url() === APP + '/app', 'the address is still the app\'s own');

// 3. dashboard and chat
await page.evaluate(() => document.getElementById('t-chats').click()); await wait(800);
ok((await txt()).includes('Used bike post'), 'the chat list loads from the server with the token');
await page.evaluate(() => document.querySelector('#threads .row-item').click()); await wait(1800);
ok((await txt()).includes('Hello from the guest'), 'opening the chat shows its messages');
await page.type('.composer textarea', 'online message'); await page.evaluate(() => document.querySelector('.composer .send').click()); await wait(1500);
ok((await msgCount('online message')) === 1, 'a message sent online reaches the server once');

// 4. offline: queue, reopen, deliver
blocked = true; await wait(300);
await page.type('.composer textarea', 'offline message'); await page.evaluate(() => document.querySelector('.composer .send').click()); await wait(1500);
ok((await page.evaluate(() => JSON.parse(localStorage.getItem('stub.q') || '[]'))).length === 1, 'with no connection the message is queued on the phone');
ok((await txt()).includes('Sending'), 'and shows as Sending');
await page.reload({ waitUntil: 'domcontentloaded' }); await wait(2500);
ok(await page.evaluate(() => !document.getElementById('shell').hidden), 'the app reopens with no connection (interface comes from the APK)');
await page.evaluate(() => document.getElementById('t-chats').click()); await wait(800);
await page.evaluate(() => document.querySelector('#threads .row-item').click()); await wait(1800);
const t2 = await txt('.msgs');
ok(t2.includes('Hello from the guest') && t2.includes('online message') && t2.includes('offline message'), 'offline, the whole chat including the queued message is readable');
blocked = false; await wait(300); await page.evaluate(() => window.__flush()); await wait(2500);
ok((await msgCount('offline message')) === 1, 'when the connection returns the queued message arrives exactly once');
ok((await page.evaluate(() => JSON.parse(localStorage.getItem('stub.q') || '[]'))).length === 0, 'and the queue is empty');

// 5. every file the app asked for from its own origin was found (only icons the app has no use for may be missing)
const bad = seen.filter(([s, p]) => s !== 200 && !/^\/(favicon\.ico|icons\/|manifest\.webmanifest)/.test(p));
ok(bad.length === 0, 'every page, script, style and image the app requested was in the bundle' + (bad.length ? ' (missing: ' + bad.map((b) => b[1]).join(', ') + ')' : ''));
ok(seen.some(([, p]) => p === '/static/native.js') && seen.some(([, p]) => p === '/static/app.js'), 'native.js and app.js were loaded from the bundle');

// 6. signing out returns to the sign-in screen and revokes the token on the server
const appToken = await page.evaluate(() => JSON.parse(localStorage.getItem('stub.session')).token);
ok((await J('/api/me', { headers: { Authorization: 'Bearer ' + appToken } })).r.status === 200, 'the app\'s token works before signing out');
await page.evaluate(() => document.getElementById('t-settings').click()); await wait(800);
await page.evaluate(() => { const b = [...document.querySelectorAll('#settings button')].find((x) => x.textContent === 'Log out'); b.click(); }); await wait(2500);
ok(await page.evaluate(() => !!document.getElementById('srv')), 'logging out returns to the sign-in screen');
ok((await J('/api/me', { headers: { Authorization: 'Bearer ' + appToken } })).r.status === 401, 'and the token no longer works on the server');

} catch (e) { console.log('FAIL the flow stopped early:', e.message); fails++; }
await browser.close(); server.kill(); appServer.close(); proxy.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
