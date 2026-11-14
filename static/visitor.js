const $=id=>document.getElementById(id);
const slug=location.pathname.split('/')[2];
let token='',tsKey='';
function openChat(){vt(()=>{$('start').hidden=true;$('chatview').hidden=false;document.body.classList.add('chat-open');mountChat($('chatview'),'/api/l/'+slug,'v',()=>location.reload())})}
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
async function boot(){
  let p={};try{p=await api('/api/public');document.title=p.name+': private chat'}catch(e){}
  let info;try{info=await api('/api/l/'+slug+'/info')}catch(e){$('gone').hidden=false;return}
  $('lbl').textContent=info.label;
  if(info.about){$('about').textContent=info.about;$('about').hidden=false}
  $('welcome').textContent=info.welcome||'Send a message. They will not see who you are, and you do not need an account.';
  try{await api('/api/l/'+slug+'/state');$('chatview').hidden=false;document.body.classList.add('chat-open');mountChat($('chatview'),'/api/l/'+slug,'v',()=>location.reload())}
  catch(e){$('start').hidden=false;stagger(document.querySelector('.intro-in'));if(p.turnstile_site){tsKey=p.turnstile_site;loadTS(tsKey)}}
}
boot();
