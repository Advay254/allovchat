// End-to-end check of the key vault with real WebCrypto against the real server binary.
//
// It plays the part of the browser: it makes the master key, seals it, uploads sealed chat keys, then signs in from a
// "fresh browser" that holds nothing and reads the same chat history back. It also covers a password change and a
// recovery with the recovery code. The seal/unseal code below is the reference for the client in docs/encryption.md.
//
//   ALLOV_BIN=./allovchat DATABASE_URL=postgres://... node test/vault.mjs
import { spawn, execSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const { subtle } = globalThis.crypto;
const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const bin = process.env.ALLOV_BIN || (execSync('go build -o allov-vault . && echo allov-vault', { cwd: repo }).toString().trim() && path.join(repo, 'allov-vault'));
if (!process.env.DATABASE_URL) { console.error('set DATABASE_URL'); process.exit(2); }
const PORT = process.env.PORT || '8098', BASE = 'http://localhost:' + PORT;
const server = spawn(bin, [], { env: { ...process.env, ADMIN_USERNAME: 'admin', ADMIN_PASSWORD: 'longpassphrase123', ADMIN_PATH: 'zeus', PORT, TRUSTED_PROXY_HOPS: '0' }, stdio: 'ignore', cwd: repo });
process.on('exit', () => server.kill());

let fails = 0;
const ok = (c, m) => { console.log(c ? 'ok  ' : 'FAIL', m); if (!c) fails++; };
const te = new TextEncoder(), td = new TextDecoder();
const b64 = (buf) => Buffer.from(buf).toString('base64url');
const unb64 = (s) => new Uint8Array(Buffer.from(s, 'base64url'));
const rand = (n) => crypto.getRandomValues(new Uint8Array(n));

// ---- the browser's end-to-end chat code, as in static/brand.js ----
const CURVE = { name: 'ECDH', namedCurve: 'P-256' };
async function newPair() { const p = await subtle.generateKey(CURVE, true, ['deriveBits']); return { pub: b64(await subtle.exportKey('raw', p.publicKey)), priv: await subtle.exportKey('jwk', p.privateKey) }; }
async function shared(privJwk, pubB64) {
  const priv = await subtle.importKey('jwk', privJwk, CURVE, false, ['deriveBits']);
  const pub = await subtle.importKey('raw', unb64(pubB64), CURVE, false, []);
  const hk = await subtle.importKey('raw', await subtle.deriveBits({ name: 'ECDH', public: pub }, priv, 256), 'HKDF', false, ['deriveKey']);
  return subtle.deriveKey({ name: 'HKDF', hash: 'SHA-256', salt: te.encode('allovchat-e2e-v1'), info: te.encode('chat') }, hk, { name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
}
const chatAad = (side, ctx) => te.encode('v1:' + (ctx || 'm') + ':' + side);
async function chatSeal(key, text, side) { const iv = rand(12); return 'e1.' + b64(iv) + '.' + b64(await subtle.encrypt({ name: 'AES-GCM', iv, additionalData: chatAad(side) }, key, te.encode(text))); }
async function chatOpen(key, body, side) { const p = body.split('.'); return td.decode(await subtle.decrypt({ name: 'AES-GCM', iv: unb64(p[1]), additionalData: chatAad(side) }, key, unb64(p[2]))); }

// ---- the vault (what phase A1 builds into the shared client code) ----
const aesKey = (raw) => subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']);
async function seal(key, bytes, aad) { const iv = rand(12); return 'k1.' + b64(iv) + '.' + b64(await subtle.encrypt({ name: 'AES-GCM', iv, additionalData: te.encode(aad) }, key, bytes)); }
async function unseal(key, blob, aad) { const p = blob.split('.'); return new Uint8Array(await subtle.decrypt({ name: 'AES-GCM', iv: unb64(p[1]), additionalData: te.encode(aad) }, key, unb64(p[2]))); }
const AAD = { mkPw: (id) => `allovchat-vault-v1|mk-pw|${id}`, mkRc: (id) => `allovchat-vault-v1|mk-rc|${id}`, rcShow: (id) => `allovchat-vault-v1|rc-display|${id}`, key: (id, kind, ref) => `allovchat-vault-v1|key|${id}|${kind}|${ref}` };
async function pwKey(password, salt, iter) {
  const base = await subtle.importKey('raw', te.encode(password), 'PBKDF2', false, ['deriveBits']);
  return aesKey(await subtle.deriveBits({ name: 'PBKDF2', hash: 'SHA-256', salt: unb64(salt), iterations: iter }, base, 256));
}
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
function newRecoveryCode() { // 160 random bits, shown in groups of four
  const b = rand(20); let bits = '', s = '';
  for (const v of b) bits += v.toString(2).padStart(8, '0');
  for (let i = 0; i < 160; i += 5) s += B32[parseInt(bits.slice(i, i + 5), 2)];
  return s.match(/.{4}/g).join('-');
}
async function rcDerive(code) { // one input, two independent outputs: a key that seals, and a proof for the server
  const ikm = await subtle.importKey('raw', te.encode(code.toUpperCase().replace(/[^A-Z2-7]/g, '')), 'HKDF', false, ['deriveBits']);
  const out = (info) => subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: te.encode('allovchat-recovery-v1'), info: te.encode(info) }, ikm, 256);
  return { wrap: await aesKey(await out('wrap')), auth: b64(await out('auth')) };
}
async function makeVault(id, mk, password, iter) {
  const salt = b64(rand(16)), code = newRecoveryCode(), rc = await rcDerive(code), mkKey = await aesKey(mk);
  return { code, body: { iter, salt, wrapped_pw: await seal(await pwKey(password, salt, iter), mk, AAD.mkPw(id)), wrapped_rc: await seal(rc.wrap, mk, AAD.mkRc(id)), rc_auth: rc.auth, rc_display: await seal(mkKey, te.encode(code), AAD.rcShow(id)) } };
}

// ---- the server ----
async function J(p, { method = 'GET', body, cookie } = {}) {
  const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json', ...(cookie ? { Cookie: cookie } : {}) }, body: body ? JSON.stringify(body) : undefined });
  return { s: r.status, j: await r.json().catch(() => null), c: (r.headers.get('set-cookie') || '').split(';')[0] };
}
for (let i = 0; i < 60; i++) { try { if ((await fetch(BASE + '/health')).ok) break; } catch (e) {} await new Promise((r) => setTimeout(r, 250)); }

const user = 'vault' + Math.floor(Math.random() * 1e6), pass1 = 'correct horse battery', pass2 = 'a different passphrase', pass3 = 'third passphrase here';

// 1. device one: account, master key, vault
const reg = await J('/api/register', { method: 'POST', body: { username: user, password: pass1 } });
let cookie = reg.c;
const v0 = (await J('/api/vault', { cookie })).j;
ok(v0.exists === false && v0.recommended_iter >= v0.min_iter, 'new account starts with no vault, server suggests ' + v0.recommended_iter + ' iterations');
const id = v0.id, mk = rand(32);
let t0 = performance.now();
const made = await makeVault(id, mk, pass1, v0.recommended_iter);
console.log('     (password stretching took ' + Math.round(performance.now() - t0) + ' ms on this machine)');
ok((await J('/api/vault/create', { method: 'POST', cookie, body: made.body })).s === 200, 'vault created');
const mkKey = await aesKey(mk);

// 2. a link with its key pair; the private half goes into the vault sealed with the master key
const link = (await J('/api/links', { method: 'POST', cookie, body: { label: 'Vault test link', cid: '' } })).j;
const links = (await J('/api/links', { cookie })).j, lid = links.find((l) => l.slug === link.slug).id;
const lp = await newPair();
const sealedLink = await seal(mkKey, te.encode(JSON.stringify({ priv: lp.priv, pub: lp.pub })), AAD.key(id, 'link', lid));
ok((await J('/api/vault/keys', { method: 'POST', cookie, body: { keys: [{ kind: 'link', ref: lid, blob: sealedLink }] } })).s === 200, 'sealed link key stored');
ok((await J('/api/links/' + lid + '/key', { method: 'POST', cookie, body: { pub: lp.pub } })).s === 200, 'link public key published');

// 3. a guest writes, the owner accepts and answers
const gp = await newPair(), gtok = b64(rand(16)).replace(/[^a-f0-9]/g, 'a').padEnd(32, 'a').slice(0, 32).toLowerCase();
const gkey = await shared(gp.priv, lp.pub);
const first = 'Hello, this is the guest', reply = 'Hi guest, this is the owner';
ok((await J('/api/l/' + link.slug + '/start', { method: 'POST', body: { message: await chatSeal(gkey, first, 'v'), token: gtok, turnstile: '', pub: gp.pub } })).s === 200, 'guest started an encrypted chat');
const chats = (await J('/api/chats', { cookie })).j, tid = chats[0].id;
await J('/api/t/' + tid + '/accept', { method: 'POST', cookie, body: {} });
const okey = await shared(lp.priv, chats[0].gpub);
ok((await J('/api/t/' + tid + '/send', { method: 'POST', cookie, body: { body: await chatSeal(okey, reply, 'o'), cid: b64(rand(16)).toLowerCase().replace(/[^a-f0-9]/g, 'b').padEnd(32, 'b') } })).s === 200, 'owner replied');

// 4. a fresh browser: nothing stored locally, only the password
async function freshRead(password, label) {
  const login = await J('/api/login', { method: 'POST', body: { username: user, password } });
  if (login.s !== 200) { ok(false, label + ': sign in failed ' + login.s); return null; }
  const ck = login.c, v = (await J('/api/vault', { cookie: ck })).j;
  const m = await unseal(await pwKey(password, v.kdf.salt, v.kdf.iter), v.wrapped_pw, AAD.mkPw(v.id)); // wrong password or swapped blob throws here
  const mkey = await aesKey(m), ks = (await J('/api/vault/keys', { cookie: ck })).j.keys;
  const k = ks.find((x) => x.kind === 'link' && x.ref === lid);
  const ent = JSON.parse(td.decode(await unseal(mkey, k.blob, AAD.key(v.id, 'link', lid))));
  const list = (await J('/api/chats', { cookie: ck })).j, st = (await J('/api/t/' + list[0].id + '/state', { cookie: ck })).j;
  const key = await shared(ent.priv, list[0].gpub);
  const texts = []; for (const msg of st.messages) texts.push(await chatOpen(key, msg.body, msg.side));
  ok(texts.length === 2 && texts[0] === first && texts[1] === reply, label + ': fresh browser reads the same history');
  return { ck, v, m };
}
const f1 = await freshRead(pass1, 'after sign-in');

// the server never holds anything readable
const rawKeys = JSON.stringify((await J('/api/vault/keys', { cookie: f1.ck })).j) + JSON.stringify((await J('/api/vault', { cookie: f1.ck })).j);
ok(!rawKeys.includes(lp.priv.d) && !rawKeys.includes(b64(mk)), 'vault responses contain no private key or master key');

// sealed keys are bound to their link: moving one to another link makes it unreadable
const link2 = (await J('/api/links', { method: 'POST', cookie: f1.ck, body: { label: 'Second link', cid: '' } })).j;
const lid2 = (await J('/api/links', { cookie: f1.ck })).j.find((l) => l.slug === link2.slug).id;
await J('/api/vault/keys', { method: 'POST', cookie: f1.ck, body: { keys: [{ kind: 'link', ref: lid2, blob: sealedLink }] } }); // the server can't tell the difference
let swapped = false;
try { await unseal(mkKey, sealedLink, AAD.key(id, 'link', lid2)); swapped = true; } catch (e) {}
ok(!swapped, 'a sealed key moved to another link fails to open');
let wrongPw = false;
try { await unseal(await pwKey('not the password', f1.v.kdf.salt, f1.v.kdf.iter), f1.v.wrapped_pw, AAD.mkPw(id)); wrongPw = true; } catch (e) {}
ok(!wrongPw, 'a wrong password cannot open the master key');

// 5. password change: unwrap with the old password, re-wrap with the new one, no chat lost
{
  const salt = b64(rand(16)), iter = f1.v.kdf.iter + 1000;
  const body = { current_password: pass1, new_password: pass2, version: f1.v.version, iter, salt, wrapped_pw: await seal(await pwKey(pass2, salt, iter), f1.m, AAD.mkPw(id)) };
  ok((await J('/api/vault/password', { method: 'POST', cookie: f1.ck, body })).s === 200, 'password changed with the vault re-wrapped');
  ok((await J('/api/login', { method: 'POST', body: { username: user, password: pass1 } })).s === 401, 'old password no longer signs in');
  await freshRead(pass2, 'after password change');
}

// 6. forgot the password: recovery code opens the vault, new password and a new recovery code
{
  const shownBlob = (await J('/api/vault', { cookie: (await J('/api/login', { method: 'POST', body: { username: user, password: pass2 } })).c })).j.rc_display;
  const code = td.decode(await unseal(mkKey, shownBlob, AAD.rcShow(id)));
  ok(code === made.code, 'recovery code can be shown again after the password is entered');
  const rc = await rcDerive(code);
  const open = await J('/api/vault/recover/open', { method: 'POST', body: { username: user, rc_auth: rc.auth } });
  ok(open.s === 200, 'recovery code accepted');
  const m = await unseal(rc.wrap, open.j.wrapped_rc, AAD.mkRc(id));
  ok(Buffer.compare(Buffer.from(m), Buffer.from(mk)) === 0, 'recovery code opens the same master key');
  const salt = b64(rand(16)), iter = v0.recommended_iter, code2 = newRecoveryCode(), rc2 = await rcDerive(code2);
  const fin = await J('/api/vault/recover/finish', { method: 'POST', body: { username: user, rc_auth: rc.auth, new_password: pass3, iter, salt,
    wrapped_pw: await seal(await pwKey(pass3, salt, iter), m, AAD.mkPw(id)), wrapped_rc: await seal(rc2.wrap, m, AAD.mkRc(id)), new_rc_auth: rc2.auth, rc_display: await seal(await aesKey(m), te.encode(code2), AAD.rcShow(id)) } });
  ok(fin.s === 200, 'recovery finished with a new password and a new recovery code');
  ok((await J('/api/vault/recover/open', { method: 'POST', body: { username: user, rc_auth: rc.auth } })).s === 401, 'the used recovery code stops working');
  ok((await J('/api/login', { method: 'POST', body: { username: user, password: pass2 } })).s === 401, 'password from before recovery stops working');
  await freshRead(pass3, 'after recovery');
}

server.kill();
console.log(fails ? fails + ' check(s) failed' : 'all vault checks passed');
process.exit(fails ? 1 : 0);
