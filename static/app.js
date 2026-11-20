const $=id=>document.getElementById(id);
const el=(t,c,x)=>{const e=document.createElement(t);if(c)e.className=c;if(x!=null)e.textContent=x;return e};
const TABS=[['home','Dashboard','grid'],['chats','Chats','chat'],['links','Links','link'],['settings','Settings','gear']];
let pub={},me='',cur=null,mode='login',last=null,first=true,tab='home',tsTok='',tsOn=false,linkCid=null,busy=false;
const origin=()=>pub.base_url||location.origin;
const ago=ts=>{const s=(Date.now()-new Date(ts))/1000;if(s<60)return 'now';if(s<3600)return Math.floor(s/60)+'m';if(s<86400)return Math.floor(s/3600)+'h';return Math.floor(s/86400)+'d'};
const skel=n=>{const f=document.createDocumentFragment();for(let i=0;i<n;i++){const r=el('div','row-item static'),a=el('span','sk'),b=el('div','grow'),l1=el('div','sk'),l2=el('div','sk');a.style.cssText='width:44px;height:44px;border-radius:50%';l1.style.cssText='height:14px;width:50%;margin-bottom:8px';l2.style.cssText='height:12px;width:78%';b.append(l1,l2);r.append(a,b);f.append(r)}return f};
const placeholder=()=>{const c=$('chatview');c.textContent='';c.append(el('p','chat-empty','Pick a chat to read it.'))};
const TTL=[[1,'1 hour'],[6,'6 hours'],[24,'24 hours'],[72,'3 days'],[168,'7 days']];
const ttlText=h=>(TTL.find(x=>x[0]===h)||[0,h+' hours'])[1];
const te=new TextEncoder(),td=new TextDecoder();

// ---------- sign in ----------
function setMode(m){mode=m;const l=m==='login';$('atitle').textContent=l?'Welcome back':'Create your account';$('asub').textContent=l?'Log in to your account.':"It's quick and easy.";$('abtn').textContent=l?'Log in':'Create account';$('switch').textContent=l?'Create an account':'I already have an account';$('ap').autocomplete=l?'current-password':'new-password'}
$('switch').onclick=()=>setMode(mode==='login'?'register':'login');
function showChallenge(site){
  if(tsOn){if(window.turnstile)turnstile.reset();tsTok='';return}
  tsOn=true;const s=document.createElement('script');s.src='https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';s.async=true;
  s.onload=()=>turnstile.render('#ats',{sitekey:site,callback:t=>{tsTok=t}});document.head.append(s);
}
// After too many wrong passwords the button counts down instead of letting people keep guessing.
function lockout(sec){
  const b=$('abtn'),label=mode==='login'?'Log in':'Create account';let s=sec;b.disabled=true;
  $('aerr').textContent='Too many attempts. Please wait before trying again.';$('aerr').hidden=false;
  const tick=()=>{if(s<=0){b.disabled=false;b.textContent=label;$('aerr').hidden=true;return}
    b.textContent='Try again in '+Math.floor(s/60)+':'+String(s%60).padStart(2,'0');s--;setTimeout(tick,1000)};
  tick();
}
$('af').onsubmit=async e=>{e.preventDefault();if(busy)return;busy=true;$('aerr').hidden=true;const b=$('abtn');b.setAttribute('aria-busy','true');
  const body=mode==='login'?{username:$('au').value,password:$('ap').value,turnstile:tsTok}:{username:$('au').value,password:$('ap').value};
  try{
    const r=await fetch('/api/'+mode,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(r.ok){location.reload();return}
    b.removeAttribute('aria-busy');
    if(r.status===429&&j.retry_after){lockout(j.retry_after)}
    else if(j.challenge){showChallenge(j.site);$('aerr').textContent=j.error;$('aerr').hidden=false}
    else{$('aerr').textContent=j.error||'Something went wrong. Please try again.';$('aerr').hidden=false;shake($('af'))}
  }catch(er){b.removeAttribute('aria-busy');$('aerr').textContent='Network problem. Check your connection and try again.';$('aerr').hidden=false}
  busy=false};
// The same key is reused until the link is created, so a retry or double tap can never make two links.
$('lf').onsubmit=async e=>{e.preventDefault();if(busy)return;busy=true;$('lerr').hidden=true;linkCid=linkCid||e2e.rand(16);
  try{await api('/api/links',{label:$('ll').value,cid:linkCid});linkCid=null;$('ll').value='';haptic('success');await loadLinks()}
  catch(er){$('lerr').textContent=er.message;$('lerr').hidden=false;shake($('lf'))}
  busy=false};

// ---------- tabs ----------
function buildTabs(){const nav=$('tabbar');TABS.forEach(([k,t,i])=>{const b=el('button','tab');b.id='t-'+k;b.setAttribute('role','tab');b.setAttribute('aria-selected',String(k===tab));b.innerHTML=ico(i,24)+'<span>'+t+'</span>';b.onclick=()=>setTab(k);nav.append(b)})}
function setTab(k){if(k===tab)return;
  vt(()=>{tab=k;TABS.forEach(([n])=>{$('tab-'+n).hidden=n!==k;$('t-'+n).setAttribute('aria-selected',String(n===k))});$('tabtitle').textContent=TABS.find(x=>x[0]===k)[1];scrollTo(0,0)});
  const f={home:loadHome,links:loadLinks,settings:loadSettings}[k];if(f)f()}
function setBadge(n){const t=$('t-chats');let b=t.querySelector('.badge');if(n>0){if(!b){b=el('span','badge');t.append(b)}b.textContent=n>99?'99+':n}else if(b)b.remove()}

// ---------- end-to-end encryption keys for links ----------
// Each link has a key pair made in this browser. The public half is published; the private half stays here.
async function ensureKeys(ls){
  if(!e2e.ok)return;
  for(const l of ls){
    if(l.pubkey)continue;
    const make=async()=>{
      let ent=e2e.K.get('link:'+l.id);
      if(!ent){const p=await e2e.newPair();ent={priv:p.priv,pub:p.pub};e2e.K.set('link:'+l.id,ent)}
      try{await api('/api/links/'+l.id+'/key',{pub:ent.pub});l.pubkey=ent.pub}catch(er){}
    };
    // The lock stops two open tabs from each making a different key for the same link.
    if(navigator.locks)await navigator.locks.request('e2e-link-'+l.id,make);else await make();
  }
}
const hasKey=l=>{const k=e2e.K.get('link:'+l.id);return !!(k&&k.pub===l.pubkey)};
function keySheet(l){
  sheet('Encryption key',(box,close)=>{
    box.append(el('p','fine','Chats on this link are end-to-end encrypted with a key that lives only in this browser. Back it up to read these chats on another device. Anyone with this key can read them, so keep it private.'));
    if(hasKey(l)){const t=el('textarea');t.readOnly=true;t.rows=4;t.value=e2e.b64(te.encode(JSON.stringify(e2e.K.get('link:'+l.id))));
      const cp=el('button','btn wide','Copy key');cp.onclick=async()=>{try{await navigator.clipboard.writeText(t.value);cp.textContent='Copied';haptic('success')}catch(e){toast("Couldn't copy that")}};box.append(t,cp)}
    const t2=el('textarea');t2.rows=3;t2.placeholder='Paste a key here to restore it';const rs=el('button','btn soft wide','Restore key');
    rs.onclick=()=>{try{const k=JSON.parse(td.decode(e2e.unb64(t2.value.trim())));if(!k||!k.priv||k.pub!==l.pubkey)throw 0;e2e.K.set('link:'+l.id,k);haptic('success');toast('Key restored');close();loadLinks();loadChatsSoon()}catch(e){toast("That key doesn't match this link");shake(t2)}};
    box.append(t2,rs);
  });
}

// ---------- chats ----------
function emptyInbox(){
  const d=el('div','empty');
  d.innerHTML='<svg width="96" height="72" viewBox="0 0 96 72" fill="none" aria-hidden="true"><rect x="6" y="6" width="56" height="34" rx="12" style="fill:var(--line)"/><rect x="34" y="32" width="56" height="34" rx="12" style="fill:var(--pri)"/><circle cx="48" cy="49" r="3" fill="#fff"/><circle cx="62" cy="49" r="3" fill="#fff"/><circle cx="76" cy="49" r="3" fill="#fff"/></svg>';
  d.append(el('p',null,'No chats yet. Make a link and share it to get started.'));return d;
}
function notifyMaybe(ts){
  const req=ts.filter(t=>t.role==='owner'&&t.status==='pending').length,un=ts.reduce((n,t)=>n+Number(t.unread),0);
  last={req,un};setBadge(req+un);
}
// Chat previews are decrypted here, in the browser. Derived keys are remembered so a refresh doesn't redo the maths.
const kc=new Map();
async function preview(t){
  if(!t.e2e)return t.preview;
  try{
    const own=t.role==='owner',ent=own?e2e.K.get('link:'+t.link_id):e2e.K.get('g:'+t.slug),theirs=own?t.gpub:t.lpub;
    if(!ent||!ent.priv)return 'Encrypted message';
    const ck=t.id+'|'+theirs;let k=kc.get(ck);if(!k){k=await e2e.shared(ent.priv,theirs);kc.set(ck,k)}
    return await e2e.open(k,t.preview,t.last_side)
  }catch(e){return 'Encrypted message'}
}
let inflight=false;
async function loadChats(){
  if(inflight)return;inflight=true;
  try{
    let ts;try{ts=await api('/api/chats')}catch(e){if(e.status===401)location.reload();return}
    notifyMaybe(ts);
    await Promise.all(ts.map(async t=>{t.text=t.preview?await preview(t):''}));
    const box=$('threads');box.textContent='';box.classList.toggle('stag',first);
    if(!ts.length)box.append(emptyInbox());
    ts.forEach(t=>{
      const own=t.role==='owner',b=el('button','row-item'+(t.id===cur?' on':'')),m=el('div','grow'),top=el('div','row-top'),nm=el('b',null,t.peer);
      if(!own)nm.append(el('span','role','Joined'));
      top.append(nm,el('span','fine',ago(t.last_at)));
      m.append(top,el('div','fine ellip',t.label+' \u00b7 '+(own&&t.status==='pending'?'Request: ':'')+t.text));
      b.append(avatar(t.peer),m);
      if(t.unread>0||(own&&t.status==='pending')){const d=el('span','dot');d.setAttribute('role','img');d.setAttribute('aria-label',t.status==='pending'?'New request':'Unread messages');b.append(d)}
      b.onclick=()=>select(t,own);box.append(b)});
    if(first){stagger(box);first=false}
  }finally{inflight=false}
}
// Several triggers in a row (closing a chat, returning to the tab, saving a key) cause one refresh.
const loadChatsSoon=debounce(loadChats,400);
// Poll gently: every 8 seconds while the tab is open, every 30 while it is hidden.
function poll(){setTimeout(async()=>{await loadChats();poll()},document.hidden?30000:8000)}
document.addEventListener('visibilitychange',()=>{if(!document.hidden)loadChatsSoon()});
function select(t,own){cur=t.id;haptic('select');document.body.classList.add('chat-open');mountChat($('chatview'),(own?'/api/t/':'/api/m/')+t.id,own?'o':'v',closeChat,closeChat);loadChatsSoon()}
function closeChat(){cur=null;document.body.classList.remove('chat-open');if(window._es){window._es.close();window._es=null}clearInterval(window._exp);setTimeout(()=>{if(!cur)placeholder()},320);loadChatsSoon()}

// ---------- links ----------
function shareSheet(l){
  const u=origin()+'/l/'+l.slug;
  sheet('Share your link',(box,close)=>{
    // The QR code is drawn by the server, so no outside script runs in this page.
    const qr=el('div','qr'),img=new Image();img.width=220;img.height=220;img.alt='QR code for '+l.label;img.src='/api/links/'+l.id+'/qr.png';qr.append(img);
    const row=el('div','row'),cp=el('button','btn','Copy link'),dl=el('button','btn soft','Share card');
    cp.onclick=async()=>{try{await navigator.clipboard.writeText(u);cp.textContent='Copied';haptic('success');pulse(cp)}catch(e){toast("Couldn't copy that")}};
    dl.onclick=()=>shareCard(l,u,img);row.append(cp,dl);
    box.append(qr,el('p','fine center',u.replace(/^https?:\/\//,'')),row);
  });
}
async function shareCard(l,u,img){
  const mark=new Image();mark.src='/static/logo-mark.svg';
  try{await Promise.all([img.decode(),mark.decode()])}catch(e){toast('Still loading. Try again in a second.');return}
  const W=1080,H=1450,c=document.createElement('canvas');c.width=W;c.height=H;const g=c.getContext('2d');
  const rr=(x,y,w,h,r)=>{g.beginPath();if(g.roundRect)g.roundRect(x,y,w,h,r);else g.rect(x,y,w,h)};
  const ic=(d,x,y,s,col)=>{g.save();g.translate(x,y);g.scale(s/24,s/24);g.strokeStyle=col;g.lineWidth=1.75;g.lineCap='round';g.lineJoin='round';g.stroke(new Path2D(d));g.restore()};
  const F='system-ui,-apple-system,Segoe UI,sans-serif';
  g.fillStyle='#0B1220';g.fillRect(0,0,W,H);
  const gr=g.createRadialGradient(W-60,160,10,W-60,160,640);gr.addColorStop(0,'rgba(37,99,255,.4)');gr.addColorStop(1,'rgba(37,99,255,0)');g.fillStyle=gr;g.fillRect(0,0,W,H);
  rr(28,28,W-56,H-56,56);g.strokeStyle='#1E2B45';g.lineWidth=3;g.stroke();
  g.drawImage(mark,292,92,140,84);g.textAlign='left';g.font='700 72px '+F;g.fillStyle='#fff';g.fillText('allov',452,152);
  const w=g.measureText('allov').width;g.fillStyle='#2563FF';g.fillText('chat',452+w,152);
  g.textAlign='center';g.font='800 56px '+F;g.fillStyle='#fff';g.fillText('Meet without revealing.',W/2,272);g.fillStyle='#2563FF';g.fillText('Connect without pressure.',W/2,340);
  g.font='400 30px '+F;g.fillStyle='#93A0B8';g.fillText('A safe, temporary space to connect without',W/2,402);g.fillText('revealing your identity.',W/2,442);
  rr(250,484,580,580,60);g.fillStyle='#0F1A2D';g.fill();g.strokeStyle='#1E3A8A';g.lineWidth=3;g.stroke();
  rr(274,508,532,532,40);g.fillStyle='#fff';g.fill();g.drawImage(img,300,534,480,480);
  g.strokeStyle='#2563FF';g.lineWidth=9;g.lineCap='round';
  [[296,560,1,1],[784,560,-1,1],[296,988,1,-1],[784,988,-1,-1]].forEach(([x,y,dx,dy])=>{g.beginPath();g.moveTo(x,y+dy*54);g.lineTo(x,y);g.lineTo(x+dx*54,y);g.stroke()});
  rr(470,704,140,140,30);g.fillStyle='#0B1220';g.fill();g.drawImage(mark,486,736,108,65);
  rr(318,1094,444,66,33);g.fillStyle='#0F1A2D';g.fill();g.strokeStyle='#1E2B45';g.lineWidth=2;g.stroke();
  ic('M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1',350,1111,32,'#2563FF');
  g.textAlign='center';g.font='500 30px '+F;g.fillStyle='#fff';g.fillText('Scan to start a conversation',566,1137);
  const feats=[['M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z M9 12l2 2 4-4','Private','Your identity','stays protected.'],['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17z M12 7.5V12l3 2','Temporary','Gone in 7 days','at the most.'],['M9 6a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M15 10a4 4 0 1 0 0 8 4 4 0 0 0 0-8z','Mutual','Share only when','you are both ready.']];
  feats.forEach(([d,t,a,b],i)=>{const cx=210+i*330;ic(d,cx-100,1218,64,'#2563FF');g.textAlign='left';g.font='700 32px '+F;g.fillStyle='#fff';g.fillText(t,cx-26,1244);g.font='400 23px '+F;g.fillStyle='#93A0B8';g.fillText(a,cx-26,1282);g.fillText(b,cx-26,1312)});
  rr(120,1344,840,76,38);g.fillStyle='#0F1A2D';g.fill();g.strokeStyle='#1E2B45';g.lineWidth=2;g.stroke();
  g.textAlign='left';g.font='500 28px '+F;g.fillStyle='#fff';g.fillText(u.replace(/^https?:\/\//,'').split('/')[0],170,1392);
  g.textAlign='right';g.fillStyle='#2563FF';g.font='600 28px '+F;g.fillText('Open Source  </>',912,1392);
  const a=document.createElement('a');a.download='allovchat-card.png';a.href=c.toDataURL('image/png');a.click();haptic('success');
}
function editSheet(l){
  sheet('Edit link',(box,close)=>{
    const f=el('form');f.style.cssText='display:flex;flex-direction:column;gap:12px';
    const mk=(lab,inp)=>{const w=el('label','field',lab);w.append(inp);f.append(w);return inp};
    const label=mk('Name',el('input'));label.value=l.label;label.maxLength=60;label.required=true;
    const welcome=mk('Welcome message guests see',el('textarea'));welcome.value=l.welcome;welcome.rows=3;welcome.maxLength=280;
    const about=mk('What you are chatting about (optional)',el('input'));about.value=l.about;about.maxLength=120;
    const ttl=mk('Chats on this link are deleted after',el('select'));TTL.forEach(([h,t])=>ttl.append(new Option(t,h)));ttl.value=l.ttl_hours;
    const auto=mk('Auto-reply for new requests (optional)',el('input'));auto.value=l.autoreply;auto.maxLength=200;
    const hrs=()=>{const s=el('select');s.append(new Option('Off',-1));for(let h=0;h<24;h++)s.append(new Option(String(h).padStart(2,'0')+':00',h));return s};
    const qs=mk('Quiet hours start',hrs());qs.value=l.quiet_start;
    const qe=mk('Quiet hours end',hrs());qe.value=l.quiet_end;
    const err=el('p','err');err.hidden=true;err.setAttribute('role','alert');
    const row=el('div','row'),x=el('button','btn soft','Cancel'),ok=el('button','btn','Save');ok.type='submit';x.type='button';x.onclick=close;row.append(x,ok);
    f.append(err,row);
    f.onsubmit=async e=>{e.preventDefault();ok.setAttribute('aria-busy','true');
      try{await api('/api/links/'+l.id+'/edit',{label:label.value,welcome:welcome.value,about:about.value,autoreply:auto.value,quiet_start:+qs.value,quiet_end:+qe.value,tz:new Date().getTimezoneOffset(),ttl_hours:+ttl.value});haptic('success');close();loadLinks();if(tab==='home')loadHome()}
      catch(er){ok.removeAttribute('aria-busy');err.textContent=er.message;err.hidden=false;shake(f)}};
    box.append(f);
  });
}
function linkRow(l,compact){
  const u=origin()+'/l/'+l.slug,r=el('div','row-item static'),m=el('div','grow'),nm=el('b',null,l.label+(l.active?'':' (paused)'));
  if(l.pubkey&&!hasKey(l))nm.append(el('span','keytag','Key missing here'));
  m.append(nm,el('div','fine ellip',u.replace(/^https?:\/\//,'')),el('div','fine',l.chats+(l.chats===1?' chat':' chats')+' \u00b7 ends after '+ttlText(l.ttl_hours)));
  r.append(m);
  // Switches send the exact state they want, so repeating a request can never flip it back.
  if(compact){r.append(switchEl(l.active,async v=>{try{await api('/api/links/'+l.id+'/active',{on:v});return true}catch(e){toast(e.message);return false}},'Link is active'));return r}
  const cp=el('button','icon-btn');cp.innerHTML='<span class="ic">'+ico('copy')+'</span>';cp.setAttribute('aria-label','Copy link');cp.onclick=()=>copyBtn(cp,u);
  const mb=el('button','icon-btn');mb.innerHTML=ico('more');mb.setAttribute('aria-label','Link options');mb.setAttribute('aria-haspopup','menu');
  mb.onclick=()=>popover(mb,(pop,close)=>{
    menuItem(pop,close,'share','Share\u2026',()=>shareSheet(l));
    menuItem(pop,close,'edit','Edit details',()=>editSheet(l));
    menuItem(pop,close,'key','Encryption key',()=>keySheet(l));
    menuItem(pop,close,l.active?'block':'check',l.active?'Pause link':'Resume link',async()=>{await api('/api/links/'+l.id+'/active',{on:!l.active});loadLinks()});
    menuItem(pop,close,'trash','Delete link',()=>confirmSheet('Delete this link?','The link and all of its chats will be removed.','Delete link',async()=>{await api('/api/links/'+l.id+'/delete',{});e2e.K.del('link:'+l.id);loadLinks();loadChatsSoon()}),true);
  });
  r.append(cp,mb);return r;
}
async function loadLinks(){
  const box=$('links');let ls;
  try{ls=await api('/api/links')}catch(e){return}
  await ensureKeys(ls);
  box.textContent='';box.classList.add('stag');
  if(!ls.length)box.append(el('p','fine center pad','Create your first link above, then share it anywhere.'));
  ls.forEach(l=>box.append(linkRow(l,false)));stagger(box);
}

// ---------- dashboard ----------
async function loadHome(){
  const box=$('home');let s,ls;
  try{[s,ls]=await Promise.all([api('/api/stats'),api('/api/links')])}catch(e){return}
  await ensureKeys(ls);
  box.textContent='';
  const hi=el('div','dash-hi'),cta=el('button','btn sm','New link');hi.append(el('p','fine','Welcome back, '+me+'.'),cta);
  cta.onclick=()=>{setTab('links');setTimeout(()=>$('ll').focus(),320)};
  const g=el('div','stats-grid stag');
  [['Conversations',s.chats],['Links created',s.links],['Contacts shared',s.shared],['Messages',s.messages]].forEach(([t,v])=>{const c=el('div','stat-card');c.append(el('span',null,t),el('b',null,String(v)));g.append(c)});
  stagger(g);
  const h=el('div','sec-h'),all=el('button','linkbtn','View all');h.append(el('h2',null,'Your links'),all);all.onclick=()=>setTab('links');
  const list=el('div','group stag');
  if(!ls.length)list.append(el('p','fine center pad','No links yet. Tap New link to make your first one.'));
  ls.slice(0,3).forEach(l=>list.append(linkRow(l,true)));stagger(list);
  box.append(hi,g,h,list);
}

// ---------- settings ----------
async function loadSettings(){
  const box=$('settings');box.textContent='';box.classList.add('stag');
  const row=(title,sub,right)=>{const r=el('div','row-item static'),m=el('div','grow');m.append(el('b',null,title));if(sub)m.append(el('div','fine',sub));r.append(m);if(right)r.append(right);box.append(r);return r};
  const scope={kind:'owner'},ps=await push.status(scope);
  const psub={unsupported:"This browser can't show notifications",install:'On iPhone and iPad, add this app to your Home Screen first, then turn this on from there',blocked:'Blocked in your browser settings. Allow notifications for this site to turn them on',on:'You hear about new requests, messages and shared contacts. Alerts never show names or message text',off:'Get a short alert about new requests and messages when the app is closed'}[ps];
  row('Notifications',psub,['unsupported','install','blocked'].includes(ps)?null:switchEl(ps==='on',async v=>{
    try{if(v){await push.enable(scope);toast('Notifications are on')}else await push.disable(scope);return true}
    catch(e){toast(e.message);return false}},'Notifications'));
  row('Privacy mode by default','Hide typing, read receipts and online status in new chats you open',switchEl(localStorage.getItem('privacy')==='1',v=>{localStorage.setItem('privacy',v?'1':'0');return true},'Privacy mode by default'));
  row('Haptics','Short vibrations on phones that support them',switchEl(localStorage.getItem('haptics')!=='0',v=>{localStorage.setItem('haptics',v?'1':'0');if(v)haptic('success');return true},'Haptics'));
  let n=0;try{n=(await api('/api/blocks')).count}catch(e){}
  const bl=el('button','btn soft sm','Clear');bl.onclick=()=>confirmSheet('Clear blocked guests?','They will be able to start chats with you again.','Clear all',async()=>{await api('/api/blocks/clear',{});loadSettings()});
  row('Blocked guests',n?n+(n===1?' person':' people'):'Nobody blocked',n?bl:null);
  const so=el('button','btn soft sm','Log out');so.onclick=async()=>{await push.disable({kind:'owner'}).catch(()=>{});await api('/api/logout',{});location.reload()};
  row('Signed in as '+me,null,so);
  stagger(box);
}
async function boot(){
  pub=await api('/api/public');
  document.title=pub.name+': dashboard';
  if(pub.announcement){$('banner').textContent=pub.announcement;$('banner').hidden=false}
  $('switch').hidden=!pub.registration_open;
  try{me=(await api('/api/me')).username;$('shell').hidden=false;buildTabs();placeholder();
    $('threads').append(skel(3));loadHome();loadChats();poll();push.sync({kind:'owner'})}
  catch(e){$('auth').hidden=false}
}
// After the first chat you accept, offer alerts once. The prompt only appears if they tap the button.
window.addEventListener('chat:accepted',async()=>{
  if(localStorage.getItem('push_asked')||!(await push.canAsk({kind:'owner'})))return;
  localStorage.setItem('push_asked','1');
  sheet('Want a nudge when someone writes?',(box,close)=>{
    const row=el('div','row'),no=el('button','btn soft','Not now'),yes=el('button','btn','Turn on notifications');
    no.onclick=close;
    yes.onclick=async()=>{try{await push.enable({kind:'owner'});toast('Notifications are on');close()}catch(e){toast(e.message)}};
    row.append(no,yes);box.append(el('p',null,'Get a short alert about new requests and messages, even when allovchat is closed. Alerts never show names or message text.'),row);
  });
});
if(navigator.serviceWorker)navigator.serviceWorker.addEventListener('message',e=>{if(e.data&&e.data.type==='push')loadChatsSoon()});
boot();
