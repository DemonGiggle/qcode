package remote

import "html/template"

type remotePage struct {
	BasePath     string
	AuthRequired bool
}

var indexTemplate = template.Must(template.New("remote").Parse(indexHTML))

const indexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><base href="{{.BasePath}}"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#07090d"><title>qcode remote</title><style>
:root {
  color-scheme: dark;
  --bg: #07090d;
  --panel: #0d1117;
  --field: #080b10;
  --line: #253041;
  --muted: #8190a5;
  --text: #d8e0ea;
  --cyan: #43d9e8;
  --green: #54d17a;
  --yellow: #e8bf55;
  --content-width: 72rem;
}

*, *::before, *::after { box-sizing: border-box; }
html {
  min-height: 100%;
  background: var(--bg);
}
body {
  width: 100%;
  height: 100vh;
  height: 100dvh;
  min-height: 100svh;
  margin: 0;
  display: grid;
  grid-template-rows: auto minmax(0, 1fr) auto;
  overflow: hidden;
  background: var(--bg);
  color: var(--text);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.top {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: .45rem;
  padding: calc(.5rem + env(safe-area-inset-top)) calc(.8rem + env(safe-area-inset-right)) .5rem calc(.8rem + env(safe-area-inset-left));
  overflow: hidden;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
}
.brand {
  flex: 0 0 auto;
  margin-right: .35rem;
  color: var(--cyan);
  font-weight: 800;
}
#tabs {
  min-width: 0;
  flex: 1 1 auto;
  display: flex;
  gap: .45rem;
  overflow-x: auto;
  overscroll-behavior-x: contain;
  scrollbar-width: thin;
}
.tab {
  flex: 0 0 auto;
  min-height: 2.75rem;
  border: 1px solid transparent;
  background: transparent;
  color: var(--muted);
  border-radius: .35rem;
  padding: .45rem .7rem;
  white-space: nowrap;
  font: inherit;
  cursor: pointer;
  touch-action: manipulation;
}
.tab.active { border-color: var(--cyan); color: var(--text); }
.tab.running::after { content: ' ●'; color: var(--green); }

main {
  min-width: 0;
  min-height: 0;
  overflow: auto;
  padding: clamp(.75rem, 2vw, 1.25rem);
  overscroll-behavior: contain;
  scrollbar-color: var(--line) transparent;
}
.transcript {
  width: min(100%, var(--content-width));
  margin: 0 auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: break-word;
  line-height: 1.5;
  font: inherit;
  font-size: clamp(.82rem, .78rem + .25vw, .96rem);
}
.empty { color: var(--muted); }

.bottom {
  min-width: 0;
  padding: .65rem max(.75rem, env(safe-area-inset-right)) calc(.65rem + env(safe-area-inset-bottom)) max(.75rem, env(safe-area-inset-left));
  background: var(--panel);
  border-top: 1px solid var(--line);
}
.status,
.waiting,
.interaction,
.command-panel,
form {
  width: min(100%, var(--content-width));
  margin-left: auto;
  margin-right: auto;
}
.status {
  min-width: 0;
  margin-bottom: .45rem;
  color: var(--muted);
  font-size: .76rem;
  line-height: 1.35;
  overflow-wrap: anywhere;
}
.status .online { color: var(--green); }
/* Mirrors the TUI task indicator: a dim spinner with the queued count. */
.waiting {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: .55rem;
  margin-bottom: .4rem;
  color: var(--muted);
  font-size: .76rem;
  line-height: 1.35;
  overflow-wrap: anywhere;
}
.waiting[hidden] { display: none; }
.waiting-text { min-width: 0; flex: 1 1 auto; }
.waiting-cancel {
  flex: 0 0 auto;
  min-height: 0;
  padding: .2rem .6rem;
  background: var(--line);
  color: var(--text);
  font-size: .72rem;
  font-weight: 400;
}
.interaction,
.command-panel {
  display: none;
  margin-bottom: .55rem;
  padding: .7rem;
  border: 1px solid var(--yellow);
  border-radius: .4rem;
}
.interaction.open,
.command-panel.open { display: block; }
.interaction-title,
.command-title { margin-bottom: .45rem; color: var(--yellow); }
.command-help { margin: .25rem 0 .5rem; color: var(--muted); font-size: .8rem; line-height: 1.4; }
.command-actions { display: flex; flex-wrap: wrap; gap: .45rem; margin-top: .55rem; }
.command-actions .cancel { background: var(--line); color: var(--text); font-weight: 400; }
.choices { display: flex; flex-wrap: wrap; gap: .45rem; }
.choices button { flex: 1 1 12rem; background: var(--line); color: var(--text); font-weight: 400; }
.selector-list {
  display: grid;
  gap: .35rem;
  max-height: min(18rem, 32dvh);
  margin: .5rem 0;
  overflow: auto;
  overscroll-behavior: contain;
  -webkit-overflow-scrolling: touch;
}
.selector-list button { width: 100%; text-align: left; background: var(--line); color: var(--text); font-weight: 400; }
.selector-empty { padding: .35rem 0; color: var(--muted); }

form {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: stretch;
  gap: .55rem;
}
input {
  width: 100%;
  min-width: 0;
  min-height: 2.75rem;
  padding: .65rem .75rem;
  background: var(--field);
  border: 1px solid var(--line);
  border-radius: .4rem;
  color: var(--text);
  font: inherit;
  font-size: 16px;
  outline: none;
}
input:focus { border-color: var(--cyan); }
button {
  min-height: 2.75rem;
  padding: .55rem .9rem;
  border: 0;
  border-radius: .4rem;
  background: var(--cyan);
  color: #001014;
  font: inherit;
  font-weight: 800;
  cursor: pointer;
  touch-action: manipulation;
}
button:focus-visible,
input:focus-visible { outline: 2px solid var(--cyan); outline-offset: 2px; }
button:disabled { opacity: .5; cursor: default; }
.notice { color: var(--yellow); }
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}

@media (max-width: 680px) {
  .top { padding-top: calc(.35rem + env(safe-area-inset-top)); padding-bottom: .35rem; }
  .brand { display: none; }
  main { padding: .7rem; }
  .bottom { padding-top: .55rem; }
  .tab { min-height: 2.6rem; padding: .4rem .6rem; }
  .choices button { flex-basis: 100%; }
}
@media (max-width: 480px) {
  .top { padding-left: max(.5rem, env(safe-area-inset-left)); padding-right: max(.5rem, env(safe-area-inset-right)); }
  .bottom { padding-left: max(.55rem, env(safe-area-inset-left)); padding-right: max(.55rem, env(safe-area-inset-right)); }
  .interaction, .command-panel { padding: .6rem; }
  .command-actions { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .command-actions button { width: 100%; }
  form { grid-template-columns: 1fr; gap: .4rem; }
  form button { width: 100%; }
  .selector-list { max-height: min(14rem, 30dvh); }
}
@media (max-height: 520px) and (orientation: landscape) {
  .top { padding-top: .25rem; padding-bottom: .25rem; }
  .tab, button, input { min-height: 2.35rem; }
  .bottom { padding-top: .4rem; }
  .selector-list { max-height: 10rem; }
}
</style></head><body>
<header class="top"><span class="brand" aria-label="qcode remote">qcode</span><nav id="tabs" aria-label="Agents"></nav></header>
<main id="scroll"><pre id="transcript" class="transcript empty">Sign in using the link or QR code displayed by /remote.</pre></main>
<footer class="bottom"><div id="interaction" class="interaction"></div><div id="command" class="command-panel"></div><div id="waiting" class="waiting" hidden></div><div id="status" class="status" role="status" aria-live="polite">Connecting to qcode…</div><button id="thinking" class="cancel" type="button" hidden></button><form id="form"><label class="sr-only" for="input">Message or slash command</label><input id="input" autocomplete="off" autocapitalize="sentences" spellcheck="false" placeholder="Send a prompt or slash command"><button id="send" type="submit">Send</button></form></footer>
<script>
(()=>{
const tabs=document.querySelector('#tabs'),out=document.querySelector('#transcript'),status=document.querySelector('#status'),interaction=document.querySelector('#interaction'),command=document.querySelector('#command'),form=document.querySelector('#form'),input=document.querySelector('#input'),send=document.querySelector('#send'),scroll=document.querySelector('#scroll'),waiting=document.querySelector('#waiting'),thinking=document.querySelector('#thinking');
let snapshot=null,active='',timer=0,closed=false;const cleared={};
let sessionKey='',eventController=null;
const authRequired={{.AuthRequired}};
const storageKey='qcode.remote.session:'+new URL(document.baseURI).pathname;
const events={close(){if(eventController)eventController.abort();eventController=null}};
input.disabled=true;send.disabled=true;
function lockSession(message){closed=true;events.close();clearTimeout(timer);sessionKey='';snapshot=null;try{sessionStorage.removeItem(storageKey)}catch(_){}input.disabled=true;send.disabled=true;waitingRunning=false;drawWaiting();tabs.replaceChildren();interaction.replaceChildren();interaction.className='interaction';command.replaceChildren();command.className='command-panel';out.textContent='Run /remote in the terminal for a new login link.';out.className='transcript empty';status.textContent=message}
async function apiFetch(path,options={}){if(authRequired&&!sessionKey)throw new Error('Authorization required. Run /remote for a new login link.');const headers=new Headers(options.headers);if(authRequired)headers.set('Authorization','Bearer '+sessionKey);const r=await fetch(path,{...options,headers});if(authRequired&&r.status===401){lockSession('Session is no longer authorized. Run /remote for a new login link.');throw new Error('Session is no longer authorized. Run /remote for a new login link.')}return r}
function abortableDelay(ms,signal){return new Promise(resolve=>{if(signal.aborted){resolve();return}const finish=()=>{clearTimeout(id);signal.removeEventListener('abort',finish);resolve()};const id=setTimeout(finish,ms);signal.addEventListener('abort',finish,{once:true})})}
async function streamEvents(){
  events.close();
  const controller=new AbortController();
  eventController=controller;
  const signal=controller.signal;
  let delay=1000;
  while(!closed&&!signal.aborted){
    let reader;
    try{
      const r=await apiFetch('api/v1/events',{signal,cache:'no-store'});
      if(!r.ok)throw new Error('Live updates unavailable');
      if(!r.headers.get('Content-Type')?.startsWith('text/event-stream'))throw new Error('Invalid event stream');
      reader=r.body.getReader();
      const decoder=new TextDecoder();
      let pending='',event='',afterCR=false;
      const line=value=>{
        if(value===''){
          if(event==='refresh'||event==='runtime')schedule();
          event='';
        }else if(value.startsWith('event:'))event=value.slice(6).trim();
      };
      while(!closed&&!signal.aborted){
        const {value,done}=await reader.read();
        if(done)break;
        delay=1000;
        for(const char of decoder.decode(value,{stream:true})){
          if(afterCR){afterCR=false;if(char==='\n')continue}
          if(char==='\r'||char==='\n'){
            line(pending);pending='';afterCR=char==='\r';
          }else{
            pending+=char;
            if(pending.length>65536)throw new Error('Invalid event stream');
          }
        }
      }
    }catch(e){
      if(closed||signal.aborted)break;
    }finally{
      if(reader){try{await reader.cancel()}catch(_){}reader.releaseLock()}
    }
    if(closed||signal.aborted)break;
    status.textContent='Reconnecting…';
    await abortableDelay(delay,signal);
    delay=Math.min(delay*2,15000);
  }
}
async function login(){
  const fragment=new URLSearchParams(location.hash.slice(1));
  const token=fragment.get('login');
  if(token!==null)history.replaceState(null,'',location.pathname+location.search);
	if(!authRequired){closed=false;input.disabled=false;send.disabled=false;streamEvents();load();input.focus();return}
  try{
    const probe=storageKey+'.probe';
    sessionStorage.setItem(probe,'1');
    sessionStorage.removeItem(probe);
    sessionKey=sessionStorage.getItem(storageKey)||'';
  }catch(_){
    status.textContent='Browser session storage is unavailable. Enable it and reopen the login link.';
    return;
  }
  try{
    if(sessionKey){
      const r=await fetch('api/v1/snapshot',{headers:{Authorization:'Bearer '+sessionKey},cache:'no-store'});
      if(r.ok){
        snapshot=await r.json();
      }else if(r.status===401){
        sessionKey='';
        sessionStorage.removeItem(storageKey);
      }else{
        throw new Error('Unable to resume your session. Reload to retry.');
      }
    }
    if(!sessionKey){
      if(!token){lockSession('Authorization required. Open a new link from /remote.');return}
      status.textContent='Signing in…';
      const r=await fetch('api/v1/login',{
        method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({token}),cache:'no-store'
      });
      const result=await r.json();
      if(!r.ok){
        const messages={
          expired_link:'Login link expired after 3 minutes. Run /remote for a new link.',
          used_link:'Login link has already been used. Run /remote for a new link.',
          invalid_link:'Invalid login link. Run /remote for a new link.'
        };
        lockSession(messages[result.error]||'Unable to sign in. Run /remote for a new link.');
        return;
      }
      sessionKey=result.session_key;
      try{sessionStorage.setItem(storageKey,sessionKey)}catch(_){
        lockSession('Unable to save your browser session. Enable session storage and run /remote again.');
        return;
      }
    }
    closed=false;
    input.disabled=false;
    send.disabled=false;
    if(snapshot)render();
    streamEvents();
    load();
    input.focus();
  }catch(_){
    status.textContent=sessionKey?'Unable to connect. Reload to retry.':'Unable to sign in. Run /remote for a new login link.';
  }
}
/* Blue (4) and bright blue (12) are lifted so ANSI blue output, such as level 3+ markdown headings, stays readable on the dark transcript. */
const ansi16=['#000000','#800000','#008000','#808000','#6cb6ff','#800080','#008080','#c0c0c0','#808080','#ff0000','#00ff00','#ffff00','#9ccbff','#ff00ff','#00ffff','#ffffff'];
function schedule(){clearTimeout(timer);timer=setTimeout(load,70)}
async function load(){if(closed)return;try{const r=await apiFetch('api/v1/snapshot',{cache:'no-store'});if(!r.ok)throw new Error(await r.text());const next=await r.json();if(closed)return;snapshot=next;render()}catch(e){status.innerHTML='<span class="notice">'+escapeHTML(String(e))+'</span>'}}
function escapeHTML(s){return String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function ansi256(n){n=Math.max(0,Math.min(255,n));if(n<16)return ansi16[n];if(n<232){const v=n-16,c=x=>x?55+x*40:0;return '#'+[c(Math.floor(v/36)),c(Math.floor(v/6)%6),c(v%6)].map(x=>x.toString(16).padStart(2,'0')).join('')}return '#'+(8+(n-232)*10).toString(16).padStart(2,'0').repeat(3)}
function ansiHTML(value){let state={},text='',html='';const css=()=>[state.fg&&'color:'+state.fg,state.bg&&'background-color:'+state.bg,state.bold&&'font-weight:700',state.dim&&'opacity:.65',state.italic&&'font-style:italic',state.underline&&'text-decoration:underline',state.strike&&'text-decoration:line-through'].filter(Boolean).join(';');const flush=()=>{if(!text)return;const style=css(),safe=escapeHTML(text);html+=style?'<span style="'+style+'">'+safe+'</span>':safe;text=''};for(let i=0;i<value.length;){if(value[i]==='\x1b'&&value[i+1]==='['){const end=value.indexOf('m',i+2);if(end>=0){flush();const codes=value.slice(i+2,end).split(';').map(x=>x===''?0:Number(x)||0);for(let j=0;j<codes.length;j++){const c=codes[j];if(c===0)state={};else if(c===1)state.bold=true;else if(c===2)state.dim=true;else if(c===3)state.italic=true;else if(c===4)state.underline=true;else if(c===9)state.strike=true;else if(c===22){state.bold=false;state.dim=false}else if(c===23)state.italic=false;else if(c===24)state.underline=false;else if(c===29)state.strike=false;else if(c===39)delete state.fg;else if(c===49)delete state.bg;else if(c>=30&&c<=37)state.fg=ansi16[c-30];else if(c>=90&&c<=97)state.fg=ansi16[c-90+8];else if(c>=40&&c<=47)state.bg=ansi16[c-40];else if(c>=100&&c<=107)state.bg=ansi16[c-100+8];else if((c===38||c===48)&&codes[j+1]===5&&j+2<codes.length){state[c===38?'fg':'bg']=ansi256(codes[j+2]);j+=2}else if((c===38||c===48)&&codes[j+1]===2&&j+4<codes.length){state[c===38?'fg':'bg']='rgb('+codes[j+2]+','+codes[j+3]+','+codes[j+4]+')';j+=4}}i=end+1;continue}}text+=value[i++]};flush();return html}
const spinnerFrames=['⠋','⠙','⠹','⠸','⠼','⠴','⠦','⠧','⠇','⠏'];
const waitingText=document.createElement('span'),waitingCancel=document.createElement('button');
let waitingRunning=false,waitingQueued=0,waitingFrame=0;
waitingText.className='waiting-text';waitingText.setAttribute('aria-hidden','true');waitingCancel.type='button';waitingCancel.className='waiting-cancel';waitingCancel.textContent='Cancel';waitingCancel.onclick=()=>{if(active)submitLine('/agent cancel '+active)};
function drawWaiting(){if(!waitingRunning){if(!waiting.hidden){waiting.hidden=true;waiting.replaceChildren()}return}waiting.hidden=false;const text='Waiting ('+spinnerFrames[waitingFrame%spinnerFrames.length]+')'+(waitingQueued?' · '+waitingQueued+' queued':'');if(waitingText.textContent!==text)waitingText.textContent=text;if(waiting.firstChild!==waitingText)waiting.replaceChildren(waitingText,waitingCancel)}
setInterval(()=>{if(!waitingRunning)return;waitingFrame++;drawWaiting()},100);
function render(){if(!snapshot||!snapshot.presentation)return;const views=snapshot.presentation.views||[];if(!views.some(v=>v.id===active))active=snapshot.presentation.active||(views[0]&&views[0].id)||'';tabs.replaceChildren(...views.map(v=>{const b=document.createElement('button');b.className='tab '+(v.id===active?'active ':'')+(v.status==='running'?'running':'');b.textContent=v.name||v.id;b.onclick=()=>{active=v.id;render()};return b}));const v=views.find(v=>v.id===active);thinking.hidden=!v||!v.thinking_available;thinking.textContent=v&&v.thinking_expanded?'Hide thinking':'Show thinking';thinking.onclick=()=>{if(!v)return;submitLines(['/agent switch '+v.id,'/thinking'])};const nearBottom=scroll.scrollHeight-scroll.scrollTop-scroll.clientHeight<80;const lines=v?v.lines.slice(cleared[active]||0):[];out.innerHTML=lines.length?ansiHTML(lines.join('\n')):'No transcript yet.';out.className='transcript '+(lines.length?'':'empty');status.innerHTML=ansiHTML(snapshot.presentation.status_bar||'Connecting to qcode…');const activeSummary=agents().find(x=>x.id===active);waitingRunning=!!activeSummary&&activeSummary.status==='running';waitingQueued=waitingRunning&&activeSummary.queue_depth||0;if(!waitingRunning)waitingFrame=0;drawWaiting();renderInteraction((snapshot.runtime.interactions||[]).find(x=>x.agent_id===active)||(snapshot.runtime.interactions||[])[0]);if(nearBottom)scroll.scrollTop=scroll.scrollHeight}
function choice(label,value,id){const b=document.createElement('button');b.textContent=label;b.onclick=()=>resolve(id,value);return b}
function renderInteraction(item){interaction.replaceChildren();interaction.className='interaction'+(item?' open':'');if(!item)return;let payload={};try{payload=typeof item.payload==='string'?JSON.parse(item.payload):item.payload||{}}catch(_){}const title=document.createElement('div');title.className='interaction-title';title.textContent=item.kind.replaceAll('_',' ');interaction.append(title);const choices=document.createElement('div');choices.className='choices';if(item.kind==='directory_approval'){title.textContent='Directory access: '+(payload.requested||'');choices.append(choice('Grant '+(payload.proposed||'requested path'),{selected:payload.proposed||payload.requested,approved:true},item.id),choice('Deny',{selected:'',approved:false},item.id))}else if(item.kind==='questions'){const questions=Array.isArray(payload)?payload:[];const fields=questions.map(q=>{const field=document.createElement('input'),options=q.Options||q.options||[];field.placeholder=(q.Text||q.text)+(options.length?' — '+options.join(' / '):'');field.autocomplete='off';interaction.append(field);return field});title.textContent='Questions';const answer=choice('Submit answers',null,item.id);answer.onclick=()=>resolve(item.id,fields.map(field=>field.value));choices.append(answer)}else if(item.kind==='learning_approval'){title.textContent='Apply the proposed global learning changes?';choices.append(choice('Apply',true,item.id),choice('Reject',false,item.id))}else{choices.append(choice('Start implementation','implement',item.id),choice('Stay in plan mode','stay',item.id))}interaction.append(choices)}
async function resolve(id,value){try{const r=await apiFetch('api/v1/interactions/'+encodeURIComponent(id)+'/resolve',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({value})});if(!r.ok)throw new Error(await r.text());schedule()}catch(e){status.innerHTML='<span class="notice">'+escapeHTML(String(e))+'</span>'}}
function downloadExport(){const views=(snapshot.presentation.views||[]);let body='<h1>qcode session</h1>';for(const v of views)body+='<h2>'+escapeHTML(v.name||v.id)+'</h2><pre>'+escapeHTML(v.lines.join('\n').replace(/\x1b\[[0-?]*[ -\/]*[@-~]/g,''))+'</pre>';const blob=new Blob(['<!doctype html><meta charset="utf-8"><title>qcode session</title><style>body{background:#07090d;color:#d8e0ea;font-family:monospace;padding:2rem}pre{white-space:pre-wrap}</style>'+body],{type:'text/html'});const a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download='qcode-session.html';a.click();setTimeout(()=>URL.revokeObjectURL(a.href),1000)}
async function postLine(line){const r=await apiFetch('api/v1/actions',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({line})});if(!r.ok)throw new Error(await r.text())}
async function submitLine(line){send.disabled=true;try{await postLine(line);input.value='';schedule()}catch(e){status.innerHTML='<span class="notice">'+escapeHTML(String(e))+'</span>'}finally{send.disabled=closed;if(!closed)input.focus()}}
async function submitLines(lines){send.disabled=true;try{for(const line of lines)await postLine(line);input.value='';schedule()}catch(e){status.innerHTML='<span class="notice">'+escapeHTML(String(e))+'</span>'}finally{send.disabled=closed;if(!closed)input.focus()}}
function closeCommand(){command.replaceChildren();command.className='command-panel';command.onkeydown=null;input.focus()}
function openCommand(title,help,placeholder,onSubmit,submitLabel='Apply'){input.value='';command.replaceChildren();command.className='command-panel open';const heading=document.createElement('div');heading.className='command-title';heading.textContent=title;const hint=document.createElement('div');hint.className='command-help';hint.textContent=help;const field=document.createElement('input');field.placeholder=placeholder;field.autocomplete='off';const actions=document.createElement('div');actions.className='command-actions';const apply=document.createElement('button');apply.textContent=submitLabel;const cancel=document.createElement('button');cancel.className='cancel';cancel.type='button';cancel.textContent='Cancel';const commit=()=>{const value=field.value.trim();if(!value)return;closeCommand();onSubmit(value)};apply.onclick=commit;cancel.onclick=closeCommand;field.onkeydown=e=>{if(e.key==='Escape'){e.preventDefault();closeCommand()}if(e.key==='Enter'){e.preventDefault();commit()}};actions.append(apply,cancel);command.append(heading,hint,field,actions);field.focus()}
function openCloseAgent(name){input.value='';command.replaceChildren();command.className='command-panel open';const heading=document.createElement('div');heading.className='command-title';heading.textContent='Close agent '+name+'?';const hint=document.createElement('div');hint.className='command-help';hint.textContent='This matches the TUI confirmation step.';const actions=document.createElement('div');actions.className='command-actions';const close=document.createElement('button');close.textContent='Close agent';close.onclick=()=>{closeCommand();submitLine('/agent close '+name+' --yes')};const cancel=document.createElement('button');cancel.className='cancel';cancel.type='button';cancel.textContent='Cancel';cancel.onclick=closeCommand;actions.append(close,cancel);command.append(heading,hint,actions);command.tabIndex=0;command.onkeydown=e=>{if(e.key==='Escape'){e.preventDefault();closeCommand()}};close.focus()}
async function catalog(){const r=await apiFetch('api/v1/catalog',{cache:'no-store'});if(!r.ok)throw new Error(await r.text());return r.json()}
function agents(){return snapshot&&snapshot.runtime&&snapshot.runtime.agents||snapshot&&snapshot.presentation&&snapshot.presentation.agents||[]}
function activeModel(){const item=agents().find(x=>x.id===active)||agents().find(x=>x.id===(snapshot&&snapshot.presentation&&snapshot.presentation.active));return item&&item.model||''}
function selectorItem(label,value){return{label:String(label||value),value:String(value)}}
function openList(title,help,items,selected,multi,apply){input.value='';command.replaceChildren();command.className='command-panel open';const heading=document.createElement('div');heading.className='command-title';heading.textContent=title;const hint=document.createElement('div');hint.className='command-help';hint.textContent=help;const filter=document.createElement('input');filter.placeholder='Type to filter';filter.autocomplete='off';const list=document.createElement('div');list.className='selector-list';const chosen=new Set(selected||[]);const draw=()=>{list.replaceChildren();const query=filter.value.trim().toLowerCase();const matches=items.filter(item=>item.label.toLowerCase().includes(query));if(!matches.length){const empty=document.createElement('div');empty.className='selector-empty';empty.textContent=query?'No matching entries.':'No entries available.';list.append(empty);return}for(const item of matches){const button=document.createElement('button');button.type='button';button.className='cancel';button.textContent=(chosen.has(item.value)?(multi?'[x] ':'> '):(multi?'[ ] ':'  '))+item.label;button.onclick=()=>{if(multi){if(chosen.has(item.value))chosen.delete(item.value);else chosen.add(item.value)}else{chosen.clear();chosen.add(item.value)}draw()};list.append(button)}};const actions=document.createElement('div');actions.className='command-actions';const ok=document.createElement('button');ok.textContent='Apply';const cancel=document.createElement('button');cancel.className='cancel';cancel.type='button';cancel.textContent='Cancel';const commit=()=>{const values=[...chosen];closeCommand();apply(values)};ok.onclick=commit;cancel.onclick=closeCommand;filter.oninput=draw;filter.onkeydown=e=>{if(e.key==='Escape'){e.preventDefault();closeCommand()}if(e.key==='Enter'){e.preventDefault();commit()}};actions.append(ok,cancel);command.append(heading,hint,filter,list,actions);draw();filter.focus()}
function chooseModel(data,kind,model){const thinking=data.thinking&&data.thinking[model];const prefix=kind==='agent-model'?'/agent new ':'/model ';if(!thinking||!Array.isArray(thinking.levels)||!thinking.levels.length){submitLine(prefix+model);return}const levels=thinking.levels.map(x=>selectorItem(x,x));const current=thinking.current?[thinking.current]:[];openList('Select thinking level','Choose a thinking level. Cancel leaves the model unchanged.',levels,current,false,v=>{if(!v[0])return;const line='/model '+model+' '+v[0];if(kind==='agent-model')submitLines([prefix+model,line]);else submitLine(line)})}
async function openCatalogSelector(kind){try{const data=await catalog();if(kind==='model'||kind==='agent-model'){const items=(data.models||[]).map(x=>selectorItem(x,x));const title=kind==='agent-model'?'New agent':'Select model';const help=kind==='agent-model'?'Choose a model for the new agent.':'Choose a model for the active agent.';openList(title,help,items,[activeModel()],false,v=>v[0]&&chooseModel(data,kind,v[0]));return}if(kind==='tool'){const items=(data.tools||[]).map(x=>selectorItem(x.name+(x.enabled?' · on':' · off'),x.name));const selected=(data.tools||[]).filter(x=>x.enabled).map(x=>x.name);openList('Select tools','Toggle tools, then apply the complete selection.',items,selected,true,v=>submitLines((data.tools||[]).map(x=>'/tool '+x.name+' '+(v.includes(x.name)?'on':'off'))));return}if(kind==='skill'){const items=(data.skills||[]).map(x=>selectorItem(x.name+(x.description?' — '+x.description:''),x.name));const selected=(data.skills||[]).filter(x=>x.selected).map(x=>x.name);openList('Select skills','Toggle workspace skills, then apply the complete selection.',items,selected,true,v=>submitLine('/skill '+(v.length?v.join(', '):'none')));return}if(kind==='resume'){const items=(data.sessions||[]).map(x=>{const when=x.saved||x.left||x.created;let date='';try{if(when)date=' · '+new Date(when).toLocaleString()}catch(_){}return selectorItem(x.preview+(x.agent_count?' · '+x.agent_count+' agents':'')+date,x.id)});openList('Resume session','Choose a saved session. The current session is excluded.',items,[],false,v=>v[0]&&submitLine('/resume '+v[0]));return}const items=agents().map(x=>selectorItem(x.id+' · '+(x.name||x.id)+' · '+(x.model||'unknown')+' · '+(x.status||'unknown'),x.id));openList('Switch agent','Choose an agent tab to make active in qcode.',items,[active],false,v=>{if(v[0]){active=v[0];render();submitLine('/agent switch '+v[0])}})}catch(e){status.innerHTML='<span class="notice">'+escapeHTML(String(e))+'</span>'}}
function handleCommand(line){if(line==='/model'){openCatalogSelector('model');return true}if(line==='/tool'){openCatalogSelector('tool');return true}if(line==='/skill'){openCatalogSelector('skill');return true}if(line==='/resume'){openCatalogSelector('resume');return true}if(line==='/agent'||line==='/agent new'){openCatalogSelector('agent-model');return true}if(line==='/agent list'||line==='/agent switch'){openCatalogSelector('agent');return true}const switching=line.match(/^\/agent switch\s+(\S+)$/);if(switching){const id=switching[1];if(!agents().some(x=>x.id===id)){status.innerHTML='<span class="notice">Unknown agent: '+escapeHTML(id)+'</span>';return true}active=id;render();submitLine('/agent switch '+id);return true}const closing=line.match(/^\/agent close\s+(\S+)$/);if(closing){openCloseAgent(closing[1]);return true}return false}
form.onsubmit=e=>{e.preventDefault();const line=input.value.trim();if(!line)return;if(line==='/exit'||line==='/quit'){closed=true;events.close();status.textContent='Disconnected';send.disabled=true;input.disabled=true;waitingRunning=false;drawWaiting();return}if(line==='/clear'){const v=(snapshot.presentation.views||[]).find(v=>v.id===active);cleared[active]=v?v.lines.length:0;input.value='';render();return}if(line.startsWith('/export')){downloadExport();input.value='';return}if(handleCommand(line))return;submitLine(line)};
login();
// Pasting a fresh login link into an already-open tab only changes its fragment.
// Reload that document so the normal login bootstrap exchanges the new token.
window.addEventListener('hashchange',()=>{if(new URLSearchParams(location.hash.slice(1)).has('login'))location.reload()});
window.addEventListener('pagehide',()=>events.close());
        window.addEventListener('pageshow',e=>{if(e.persisted&&!closed&&(!authRequired||sessionKey)){streamEvents();load()}})
})();
</script></body></html>`
