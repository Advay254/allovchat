const $=id=>document.getElementById(id);
const el=(t,c,x)=>{const e=document.createElement(t);if(c)e.className=c;if(x!=null)e.textContent=x;return e};
const slug=location.pathname.split('/')[2];
let token='',tsKey='',pub={};
const ttlWord=h=>h>=168?'7 days':h>=72?'3 days':h>=24?'24 hours':h+(h===1?' hour':' hours');
function openChat(){vt(()=>{$('start').hidden=true;$('chatview').hidden=false;document.body.classList.add('chat-open');mountChat($('chatview'),'/api/l/'+slug,'v',()=>location.reload());offerSave()})}
function loadTS(key){
  // Only Turnstile's public site key is used here. The secret key stays on the server and is checked there.
  const s=document.createElement('script');s.src='https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';s.async=true;
  s.onload=()=>turnstile.render('#ts',{sitekey:key,callback:t=>{token=t}});document.head.append(s);
}
$('sf').onsubmit=async e=>{e.preventDefault();$('serr').hidden=true;
  if(tsKey&&!token){$('serr').textContent='Complete the human check first';$('serr').hidden=false;shake($('sf'));return}
  const b=$('sbtn');b.setAttribute('aria-busy','true');
  try{await api('/api/l/'+slug+'/start',{message:$('sm').value,turnstile:token});haptic('success');openChat()}
  catch(er){b.removeAttribute('aria-busy');$('serr').textContent=er.message;$('serr').hidden=false;shake($('sf'));if(tsKey&&window.turnstile){turnstile.reset();token=''}}};

// Optional: keep this chat in an account so closing the tab never loses it.
async function claim(){await api('/api/l/'+slug+'/claim',{});haptic('success');toast('Saved. Find it under Chats in your account.');document.querySelector('.save-card')&&document.querySelector('.save-card').remove()}
function authSheet(){
  let mode=pub.registration_open?'register':'login';
  sheet('Save this chat',(box,close)=>{
    const f=el('form');f.style.cssText='display:flex;flex-direction:column;gap:12px';
    const t=el('p','fine'),u=el('input'),p=el('input'),err=el('p','err'),ok=el('button','btn wide'),sw=el('button','linkbtn');
    u.autocomplete='username';u.autocapitalize='none';u.required=true;u.placeholder='Username';p.type='password';p.required=true;p.placeholder='Password';err.hidden=true;err.setAttribute('role','alert');ok.type='submit';sw.type='button';
    const set=()=>{t.textContent=mode==='register'?'Create an account to pick this chat up again on any device. Your name in the chat stays a temporary alias.':'Log in to save this chat to your account.';ok.textContent=mode==='register'?'Create account and save':'Log in and save';sw.textContent=mode==='register'?'I already have an account':'Create an account instead';sw.hidden=!pub.registration_open;p.autocomplete=mode==='register'?'new-password':'current-password'};
    sw.onclick=()=>{mode=mode==='register'?'login':'register';set()};set();
    f.onsubmit=async e=>{e.preventDefault();err.hidden=true;ok.setAttribute('aria-busy','true');
      try{await api('/api/'+mode,{username:u.value,password:p.value});await claim();close()}
      catch(er){ok.removeAttribute('aria-busy');err.textContent=er.message;err.hidden=false;shake(f)}};
    f.append(u,p,err,ok);box.append(t,f,sw);
  });
}
async function offerSave(){
  let st;try{st=await api('/api/l/'+slug+'/state')}catch(e){return}
  if(st.saved||localStorage.getItem('nosave_'+slug))return;
  const card=el('div','save-card'),row=el('div','row'),yes=el('button','btn sm','Save to account'),no=el('button','btn soft sm','Not now');
  card.append(el('b',null,'Keep this chat if you close the tab?'),el('p','fine','Optional. Save it to an account to pick it up again on any device.'));
  yes.onclick=async()=>{try{await api('/api/me');await claim()}catch(e){if(e.status===401)authSheet();else toast(e.message)}};
  no.onclick=()=>{localStorage.setItem('nosave_'+slug,'1');card.remove()};
  row.append(yes,no);card.append(row);
  const head=$('chatview').querySelector('.chat-head');if(head)head.after(card);
}
async function boot(){
  try{pub=await api('/api/public');document.title=pub.name+': private chat'}catch(e){}
  let info;try{info=await api('/api/l/'+slug+'/info')}catch(e){$('gone').hidden=false;return}
  $('lbl').textContent=info.label;
  if(info.about){$('about').textContent=info.about;$('about').hidden=false}
  $('welcome').textContent=info.welcome||'Send a message. They will not see who you are, and you do not need an account.';
  $('nico').innerHTML=ico('clock',20);$('ttlnote').textContent='This chat is deleted automatically after '+ttlWord(info.ttl_hours)+'. You can leave any time with one tap.';
  try{await api('/api/l/'+slug+'/state');$('chatview').hidden=false;document.body.classList.add('chat-open');mountChat($('chatview'),'/api/l/'+slug,'v',()=>location.reload());offerSave()}
  catch(e){$('start').hidden=false;stagger(document.querySelector('.intro-in'));if(pub.turnstile_site){tsKey=pub.turnstile_site;loadTS(tsKey)}}
}
boot();
