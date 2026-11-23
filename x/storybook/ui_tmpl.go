package storybook

import "html/template"

// chromeTokens is the color-scheme token block shared by indexTmpl and
// gridTmpl: a neutral light/dark scheme, so the chrome follows the OS
// theme instead of boxing a dark story in light UI. Stories render
// inside their own iframe documents, so these tokens never reach the
// previewed page.
const chromeTokens = `
:root {
  color-scheme: light dark;
  --bg-light: #f6f8fa; --surface-light: #fff; --surface-hover-light: #fbfcfd;
  --border-light: #d0d7de; --border-muted-light: #e4e9ee;
  --text-light: #1f2328; --text-muted-light: #656d76;
  --accent-light: #0969da; --accent-soft-light: #ddf4ff; --danger-light: #cf222e;
  --bg-dark: #0d1117; --surface-dark: #151b23; --surface-hover-dark: #1b222c;
  --border-dark: #3d444d; --border-muted-dark: #2a313c;
  --text-dark: #f0f6fc; --text-muted-dark: #9198a1;
  --accent-dark: #4493f8; --accent-soft-dark: #0c2d4f; --danger-dark: #f85149;
  --bg: var(--bg-light); --surface: var(--surface-light); --surface-hover: var(--surface-hover-light);
  --border: var(--border-light); --border-muted: var(--border-muted-light);
  --text: var(--text-light); --text-muted: var(--text-muted-light);
  --accent: var(--accent-light); --accent-soft: var(--accent-soft-light); --danger: var(--danger-light);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: var(--bg-dark); --surface: var(--surface-dark); --surface-hover: var(--surface-hover-dark);
    --border: var(--border-dark); --border-muted: var(--border-muted-dark);
    --text: var(--text-dark); --text-muted: var(--text-muted-dark);
    --accent: var(--accent-dark); --accent-soft: var(--accent-soft-dark); --danger: var(--danger-dark);
  }
}
@supports (color: light-dark(#000, #fff)) {
  :root {
    --bg: light-dark(var(--bg-light), var(--bg-dark));
    --surface: light-dark(var(--surface-light), var(--surface-dark));
    --surface-hover: light-dark(var(--surface-hover-light), var(--surface-hover-dark));
    --border: light-dark(var(--border-light), var(--border-dark));
    --border-muted: light-dark(var(--border-muted-light), var(--border-muted-dark));
    --text: light-dark(var(--text-light), var(--text-dark));
    --text-muted: light-dark(var(--text-muted-light), var(--text-muted-dark));
    --accent: light-dark(var(--accent-light), var(--accent-dark));
    --accent-soft: light-dark(var(--accent-soft-light), var(--accent-soft-dark));
    --danger: light-dark(var(--danger-light), var(--danger-dark));
  }
}
`

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><link rel="icon" href="data:,"><title>stories: {{.Dir}}</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
<style>` + chromeTokens + `
[hidden] { display: none !important; }
body { margin: 0; display: flex; height: 100vh; height: 100dvh; font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); }
nav { width: 17rem; flex: none; display: flex; flex-direction: column; background: var(--surface); border-right: 1px solid var(--border); }
nav > header { padding: .75rem .875rem; border-bottom: 1px solid var(--border-muted); }
.brand { font-size: .85rem; font-weight: 600; }
.brand .total { font-size: .72rem; font-weight: 400; color: var(--text-muted); }
.dir { margin-top: .15rem; font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); word-break: break-all; }
.err { color: var(--danger); }
search { display: block; margin-top: .625rem; }
search form { display: flex; gap: .375rem; }
#q { flex: 1; min-width: 0; padding: .3rem .55rem; font: inherit; font-size: .8rem; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 6px; }
#q:focus-visible { outline: 2px solid var(--accent); outline-offset: -1px; border-color: var(--accent); }
#q::placeholder { color: var(--text-muted); }
.qgo { font: inherit; font-size: .78rem; padding: .3rem .6rem; color: var(--text-muted); background: var(--surface); border: 1px solid var(--border); border-radius: 6px; cursor: pointer; }
.qgo:hover { color: var(--accent); border-color: var(--accent); }
.js .qgo { display: none; }
.meta { margin-top: .4rem; font-size: .72rem; color: var(--text-muted); }
.meta:empty { display: none; }
.warn { margin-top: .5rem; font-size: .72rem; color: var(--text-muted); }
.warn code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--danger); }
.list { flex: 1; overflow-y: auto; padding: .375rem .5rem .75rem; container-type: scroll-state; }
.group { display: flex; align-items: center; gap: .375rem; margin-top: .875rem; padding: .25rem .5rem; position: sticky; top: -.375rem; background: var(--surface); z-index: 2; border-bottom: 1px solid var(--border-muted); }
.group:first-child { margin-top: .375rem; }
.group .gname { font-size: .72rem; font-weight: 600; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.grp-n { margin-inline-start: auto; font-size: .68rem; color: var(--text-muted); background: var(--bg); border: 1px solid var(--border-muted); border-radius: 99px; padding: 0 .4rem; }
.gridlink { margin-inline-start: auto; padding: .1rem .5rem; font-size: .68rem; color: var(--text-muted); text-decoration: none; border: 1px solid var(--border); border-radius: 99px; }
.grp-n + .gridlink { margin-inline-start: 0; }
.gridlink:hover { color: var(--accent); border-color: var(--accent); background: var(--accent-soft); }
.gridlink.active { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); font-weight: 600; }
.story { display: block; padding: .4rem .55rem; border-radius: 6px; color: inherit; text-decoration: none; line-height: 1.35; overflow-wrap: anywhere; }
.story:hover { background: var(--surface-hover); }
.story:focus-visible, .gridlink:focus-visible, .act:focus-visible, .viewport a:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.story.active { background: var(--accent-soft); color: var(--accent); }
.story .sname { display: flex; align-items: center; flex-wrap: wrap; gap: .125rem .375rem; font-size: .85rem; font-weight: 500; word-break: break-word; }
.story.active .sname { font-weight: 600; }
.story .kind { margin-inline-start: auto; padding: 0 .35rem; font-size: .62rem; font-weight: 400; letter-spacing: .03em; color: var(--text-muted); border: 1px solid var(--border); border-radius: 99px; }
.story small { display: block; margin-top: .1rem; font-size: .74rem; font-weight: 400; color: var(--text-muted); }
.empty { padding: .75rem .5rem; font-size: .78rem; color: var(--text-muted); }
.list::before, .list::after { content: ""; display: block; position: sticky; block-size: .5rem; z-index: 1; pointer-events: none; opacity: 0; transition: opacity .2s; }
.list::before { inset-block-start: 0; margin-block-end: -.5rem; background: radial-gradient(farthest-side at 50% 0, rgb(0 0 0 / .18), transparent); }
.list::after { inset-block-end: 0; margin-block-start: -.5rem; background: radial-gradient(farthest-side at 50% 100%, rgb(0 0 0 / .18), transparent); }
@container scroll-state(scrollable: block-start) { .list::before { opacity: 1; } }
@container scroll-state(scrollable: block-end) { .list::after { opacity: 1; } }
@supports not (container-type: scroll-state) {
  .list::before, .list::after { animation: edge-shadow linear both; animation-timeline: scroll(); }
  .list::before { animation-range: 0 2rem; }
  .list::after { animation-direction: reverse; animation-range: calc(100% - 2rem) 100%; }
}
@keyframes edge-shadow { to { opacity: 1; } }
main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.bar { display: flex; align-items: center; gap: .5rem; padding: .4rem .75rem; background: var(--surface); border-bottom: 1px solid var(--border); font-size: .8rem; }
.crumb { min-width: 0; display: flex; align-items: baseline; gap: .35rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.crumb .sep { color: var(--text-muted); }
#crumb-group { font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); }
#crumb-name { font-size: .85rem; }
.bar .pos { font-size: .7rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.act { padding: .18rem .55rem; font-size: .74rem; color: var(--text-muted); text-decoration: none; border: 1px solid var(--border); border-radius: 6px; }
.act:hover { color: var(--accent); border-color: var(--accent); }
.bar .spacer { flex: 1; }
.viewport { display: flex; gap: .25rem; font-size: .78rem; }
.viewport a { padding: .2rem .4rem; text-align: center; color: var(--text-muted); text-decoration: none; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; }
.viewport a:hover { color: var(--accent); border-color: var(--accent); }
.viewport a.active { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); font-weight: 600; }
iframe { flex: 1; border: 0; background: var(--surface); }
iframe.sized { margin-inline: auto; border-inline: 1px dashed var(--border); }
</style></head>
<body>
<nav>
<header>
<div class="brand">stories <span class="total">{{.Total}}</span></div>
<div class="dir">{{.Dir}}</div>
{{if .Err}}<small class="err">{{.Err}}</small>{{end}}
<search><form action="" method="get">
<input id="q" name="q" type="search" value="{{.Q}}" placeholder="filter stories — press /" autocomplete="off" autocorrect="off" autocapitalize="none" spellcheck="false" enterkeyhint="go" aria-label="filter stories">
{{if .S}}<input type="hidden" name="s" value="{{.S}}">{{end}}{{if .G}}<input type="hidden" name="g" value="{{.G}}">{{end}}{{if .W}}<input type="hidden" name="w" value="{{.W}}">{{end}}
<button class="qgo" type="submit">filter</button>
</form></search>
<div class="meta" id="count" role="status">{{if .Q}}{{.Matched}} of {{.Total}} shown{{end}}</div>
{{if .Excluded}}<div class="warn" role="note" title="these Story-prefixed funcs are not in //go:build story files and never render">skipped:{{range .Excluded}} <code title="{{.Pos}}">{{.Name}}</code>{{end}}</div>{{end}}
</header>
<div class="list" id="list">
{{range .Groups}}<section class="grp"{{if .Hidden}} hidden{{end}}>
{{if .Name}}<div class="group"><span class="gname">{{.Name}}</span><span class="grp-n">{{len .Entries}}</span><a class="gridlink{{if eq .Name $.G}} active{{end}}" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}} data-kind="grid" data-src="{{.Src}}" data-name="{{.Name}}" title="all variants side by side">grid &#10514;</a></div>{{end}}
{{range .Entries}}<a class="story{{if eq .Name $.S}} active{{end}}" data-name="{{.Name}}" data-group="{{.Group}}"{{if .Src}} data-kind="story" data-src="{{.Src}}"{{end}}{{if .Grid}} data-grid="{{.Grid}}"{{end}} data-pos="{{.Pos}}" data-haystack="{{.Hay}}" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}}{{if eq .Name $.S}} aria-current="true"{{end}}{{if .Hidden}} hidden{{end}} title="{{.Pos}}"><span class="sname">{{.Name}}{{if .IsHTTP}}<span class="kind" title="handler story — replays real status and headers">http &#8599;</span>{{end}}</span>
{{if .Blurb}}<small>{{.Blurb}}</small>{{end}}{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}</section>{{end}}
<p class="empty" id="empty"{{if not .None}} hidden{{end}}>no stories match &#8220;{{.Q}}&#8221;</p>
</div>
</nav>
<main>
<div class="bar">
<span class="crumb"><span id="crumb-group"{{if not .SelGroup}} hidden{{end}}>{{.SelGroup}}</span><span id="crumb-sep" class="sep"{{if not .SelGroup}} hidden{{end}}>/</span><strong id="crumb-name">{{if .G}}{{.G}} variants{{else}}{{.S}}{{end}}</strong><span class="pos" id="crumb-pos">{{.SelPos}}</span></span>
<span class="spacer"></span>
<a id="act-grid" class="act" href="{{.GridHref}}"{{if not .GridHref}} hidden{{end}}{{if .GridSrc}} data-kind="grid" data-src="{{.GridSrc}}" data-name="{{.SelGroup}}"{{end}} title="all variants of this template side by side">grid &#10514;</a>
<a id="act-open" class="act"{{if .OpenHref}} href="{{.OpenHref}}"{{else}} hidden{{end}} target="_blank" title="open this story in its own tab">open &#8599;</a>
<div class="viewport" aria-label="preview width">{{range .Widths}}<a href="?w={{.}}&s={{$.S}}" data-w="{{.}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}full{{end}}</a>{{end}}</div>
</div>
<iframe id="frame" name="story"{{if .Src}} src="{{.Src}}"{{end}}{{if .Sized}} class="sized" style="width: {{.W}}px"{{end}}></iframe>
</main>
<script>(function(){
  document.body.classList.add('js');
  var frame=document.getElementById('frame');
  var list=document.getElementById('list');
  var items=[].slice.call(list.querySelectorAll('a.story'));
  var sections=[].slice.call(list.querySelectorAll('section'));
  var pills=[].slice.call(list.querySelectorAll('a.gridlink'));
  var crumbGroup=document.getElementById('crumb-group');
  var crumbSep=document.getElementById('crumb-sep');
  var crumbName=document.getElementById('crumb-name');
  var crumbPos=document.getElementById('crumb-pos');
  var actGrid=document.getElementById('act-grid');
  var actOpen=document.getElementById('act-open');
  var widths=[].slice.call(document.querySelectorAll('.viewport a'));
  var q=document.getElementById('q');
  var count=document.getElementById('count');
  var empty=document.getElementById('empty');
  function itemByName(name){
    for(var i=0;i<items.length;i++){if(items[i].dataset.name===name)return items[i]}
    return null;
  }
  function pillByName(name){
    for(var i=0;i<pills.length;i++){if(pills[i].dataset.name===name)return pills[i]}
    return null;
  }
  function nameFromURL(u){
    var m=u.pathname.match(/\/story\/([^/]+)$/);if(m)return decodeURIComponent(m[1]);
    m=u.pathname.match(/([^/]+)\.html$/);if(m)return decodeURIComponent(m[1]);
    return '';
  }
  function gridFromURL(u){
    var m=u.pathname.match(/\/grid\/([^/]+)$/);if(m)return decodeURIComponent(m[1]);
    m=u.pathname.match(/grid_([^/]+)\.html$/);if(m)return decodeURIComponent(m[1]);
    return '';
  }
  // cur tracks the live selection — story, grid, width — so the
  // ?s=/?g=/?w= links rendered at load time stay honest: a modified
  // click (new tab) reproduces the current view, not the first-opened one.
  var cur={s:'',g:'',w:''};
  function buildURL(){
    var u=new URL(location.href);
    ['s','g','w'].forEach(function(k){cur[k]?u.searchParams.set(k,cur[k]):u.searchParams.delete(k)});
    var t=(q.value||'').trim();
    t?u.searchParams.set('q',t):u.searchParams.delete('q');
    return u;
  }
  // scrollIntoView only for user-initiated activation: a boot/popstate/
  // iframe-sync mark must not yank the restored sidebar scroll position
  // back to the selected row.
  function mark(a,scroll){
    items.forEach(function(x){
      var on=x===a;
      x.classList.toggle('active',on);
      if(on){x.setAttribute('aria-current','true');if(scroll){x.scrollIntoView({block:'nearest'})}}
      else{x.removeAttribute('aria-current')}
    });
  }
  function markPills(name){
    pills.forEach(function(p){p.classList.toggle('active',p.dataset.name===name)});
  }
  function showCrumb(group,name,pos){
    crumbGroup.textContent=group||'';
    crumbGroup.hidden=!group;
    crumbSep.hidden=!group;
    crumbName.textContent=name||'';
    crumbPos.textContent=pos||'';
  }
  function setBarGrid(group,src){
    if(!group||!src){actGrid.hidden=true;return}
    actGrid.hidden=false;
    actGrid.dataset.src=src;
    actGrid.dataset.name=group;
    actGrid.href='?g='+encodeURIComponent(group);
  }
  // syncHrefs keeps every rendered link in step with the live selection
  // so a modified click reproduces the on-screen view: ?s= rows carry
  // w+q, ?g= pills carry w+q, width presets carry s+g+q.
  function syncHrefs(){
    var t=(q.value||'').trim();
    function patch(a,keys){
      var href=a.getAttribute('href')||'';
      var rel=false;
      if(href.charAt(0)!=='?'){
        // File links reach only the export's static pages; of those,
        // just the grid pages read ?w=, so the other rows keep their
        // plain href while grid links carry the chosen width too.
        if(a.dataset.kind!=='grid'||!href||href.charAt(0)==='/'||/^[a-z][a-z0-9+.-]*:/i.test(href)){return}
        rel=true;
      }
      var u=new URL(href,location.href);
      keys.forEach(function(k){
        var v=k==='q'?t:cur[k];
        v?u.searchParams.set(k,v):u.searchParams.delete(k);
      });
      // A ?-link is replaced wholesale; a file link keeps its path.
      a.setAttribute('href',(rel?href.split('?')[0].split('#')[0]:'')+u.search);
    }
    items.forEach(function(a){if(a.dataset.src){patch(a,['w','q'])}});
    pills.forEach(function(a){patch(a,['w','q'])});
    widths.forEach(function(a){patch(a,['s','g','q'])});
    if(!actGrid.hidden){patch(actGrid,['w','q'])}
  }
  // The iframe can navigate itself (in-page links), leaving the src
  // attribute behind the live location — compare against the latter so
  // re-activating the same story reloads the frame when it drifted.
  function frameNeeds(src){
    try{return new URL(src,location.href).href!==frame.contentWindow.location.href}
    catch(e){return frame.getAttribute('src')!==src}
  }
  function activate(el,push){
    var w=cur.w;
    if(el.dataset.kind==='grid'){
      cur.g=el.dataset.name;cur.s='';
      frame.classList.remove('sized');frame.style.width='';
      var src=el.dataset.src+(w?(el.dataset.src.indexOf('?')<0?'?':'&')+'w='+w:'');
      if(frameNeeds(src)){frame.src=src}
      showCrumb('grid',el.dataset.name+' variants','');
      actGrid.hidden=true;
      actOpen.href=el.dataset.src;actOpen.hidden=false;
      mark(null);
      markPills(cur.g);
    }else{
      cur.s=el.dataset.name;cur.g='';
      frame.classList.toggle('sized',!!w);frame.style.width=w?w+'px':'';
      if(frameNeeds(el.dataset.src)){frame.src=el.dataset.src}
      showCrumb(el.dataset.group,el.dataset.name,el.dataset.pos);
      setBarGrid(el.dataset.group,el.dataset.grid);
      actOpen.href=el.dataset.src;actOpen.hidden=false;
      mark(el,push!==false);
      markPills('');
    }
    syncHrefs();
    if(push!==false){history.pushState(null,'',buildURL())}
  }
  function applyWidth(w,save){
    cur.w=w||'';
    widths.forEach(function(a){a.classList.toggle('active',a.dataset.w===cur.w)});
    if(cur.g){
      // The grid views carry their own ?w=; reload the framed grid at
      // the chosen width.
      var el=pillByName(cur.g);
      if(el){activate(el,false)}
    }else{
      frame.classList.toggle('sized',!!cur.w);
      frame.style.width=cur.w?cur.w+'px':'';
    }
    syncHrefs();
    if(save!==false){history.replaceState(null,'',buildURL())}
  }
  // The iframe can navigate itself (in-page links, exported grids loaded
  // inside it); keep the toolbar and the ?s=/?g= URL honest on load.
  function sync(){
    var loc;
    try{loc=frame.contentWindow.location}catch(e){return}
    if(!loc||loc.href==='about:blank'){return}
    var g=gridFromURL(loc);
    if(g){
      cur.g=g;cur.s='';
      showCrumb('grid',g+' variants','');
      actGrid.hidden=true;
      actOpen.href=loc.href;actOpen.hidden=false;
      mark(null);
      markPills(g);
      if(new URL(location.href).searchParams.get('g')!==g){
        history.replaceState(null,'',buildURL());
      }
      return;
    }
    var a=itemByName(nameFromURL(loc));
    if(a&&a.dataset.src){
      activate(a,false);
      // A Back press can restore only the iframe's history entry, leaving
      // the top-level ?s= pointing at the previously selected story.
      // Re-align it so the URL always names what is on screen.
      if(new URL(location.href).searchParams.get('s')!==a.dataset.name){
        history.replaceState(null,'',buildURL());
      }
      return;
    }
    showCrumb('',loc.pathname,'');
    cur.s='';cur.g='';
    actGrid.hidden=true;actOpen.hidden=true;
    mark(null);
    markPills('');
  }
  frame.addEventListener('load',sync);
  function plainClick(ev){
    return ev.button===0&&!ev.ctrlKey&&!ev.metaKey&&!ev.shiftKey&&!ev.altKey;
  }
  document.addEventListener('click',function(ev){
    var a=ev.target.closest?ev.target.closest('a[data-src]'):null;
    if(!a||!plainClick(ev)){return}
    // Modified clicks (new tab, etc.) keep the browser default; the
    // anchor's href still round-trips through the server without JS.
    ev.preventDefault();
    activate(a);
  });
  widths.forEach(function(a){
    a.addEventListener('click',function(ev){
      if(!plainClick(ev)){return}
      ev.preventDefault();applyWidth(a.dataset.w,true);
    });
  });
  window.addEventListener('popstate',function(){
    var u=new URL(location.href);
    var pq=u.searchParams.get('q')||'';
    if((q.value||'')!==pq){q.value=pq;applyFilter(false)}
    cur.w=u.searchParams.get('w')||'';
    widths.forEach(function(a){a.classList.toggle('active',a.dataset.w===cur.w)});
    var g=u.searchParams.get('g');
    if(g){var el=pillByName(g);if(el){activate(el,false);return}}
    var a=itemByName(u.searchParams.get('s')||'');
    if(a&&a.dataset.src){activate(a,false)}
  });
  function visible(){return items.filter(function(a){return !a.hidden})}
  function applyFilter(save){
    var tokens=(q.value||'').trim().toLowerCase().split(/\s+/).filter(Boolean);
    var shown=0;
    sections.forEach(function(sec){
      var vis=0;
      [].forEach.call(sec.querySelectorAll('a.story'),function(a){
        var ok=tokens.every(function(t){return (a.dataset.haystack||'').indexOf(t)>=0});
        a.hidden=!ok;
        if(ok){vis++}
      });
      sec.hidden=vis===0;
      shown+=vis;
    });
    count.textContent=tokens.length?shown+' of '+items.length+' shown':'';
    empty.hidden=shown>0;
    if(!empty.hidden){empty.textContent='no stories match \u201c'+(q.value||'').trim()+'\u201d'}
    syncHrefs();
    // Boot and popstate render the filter from the URL; only a user
    // edit writes it back, so an untouched ?s=/?g=/?w= survives.
    if(save!==false){history.replaceState(null,'',buildURL())}
  }
  function step(dir){
    var vis=visible();
    if(!vis.length){return}
    var i=vis.indexOf(document.activeElement);
    var n=dir>0?(i<0?0:i+1):(i<0?vis.length-1:i-1);
    n=(n+vis.length)%vis.length;
    vis[n].focus();
  }
  q.addEventListener('input',applyFilter);
  q.form.addEventListener('submit',function(ev){ev.preventDefault()});
  list.addEventListener('scroll',function(){
    try{sessionStorage.setItem('sb_scroll',list.scrollTop)}catch(e){}
  });
  document.addEventListener('keydown',function(ev){
    var t=document.activeElement;
    var typing=t&&(t.tagName==='INPUT'||t.tagName==='TEXTAREA');
    if(ev.key==='/'&&!typing){ev.preventDefault();q.focus();q.select();return}
    if(ev.key==='Escape'&&t===q){q.value='';applyFilter();return}
    if(ev.key==='ArrowDown'||ev.key==='ArrowUp'){
      if(t===q||list.contains(t)){ev.preventDefault();step(ev.key==='ArrowDown'?1:-1)}
      return;
    }
    if(ev.key==='Enter'&&t===q){
      // An IME-confirming Enter must not jump to the first match; Safari
      // reports it as keyCode 229 with isComposing already false.
      if(ev.isComposing||ev.keyCode===229){return}
      var vis=visible();if(vis[0]){vis[0].click()}
    }
  });
  // Honor ?s=/?g=/?w=/?q= in both modes: the live server pre-renders
  // them and the static export relies on this script alone.
  var u=new URL(location.href);
  if(u.searchParams.get('q')&&(q.value||'')!==u.searchParams.get('q')){q.value=u.searchParams.get('q')}
  cur.s=u.searchParams.get('s')||'';
  cur.g=u.searchParams.get('g')||'';
  cur.w=u.searchParams.get('w')||'';
  if((q.value||'').trim()){applyFilter(false)}
  applyWidth(cur.w,false);
  if(cur.g){var el=pillByName(cur.g);if(el){activate(el,false)}}
  else{
    var a=itemByName(cur.s);
    if(a&&a.dataset.src){activate(a,false)}
  }
  sync();
  try{var y=sessionStorage.getItem('sb_scroll');if(y){list.scrollTop=+y}}catch(e){}
})();</script>
</body></html>
`))

// gridTmpl lays one render function's stories side by side, so a CSS or
// template edit can be checked against every data state at once. Each
// variant is an iframe at the chosen width (?w=, flexible when unset).
var gridTmpl = template.Must(template.New("grid").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><link rel="icon" href="data:,"><title>{{.Name}} variants</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
<style>` + chromeTokens + `
body { margin: 0; display: flex; flex-direction: column; height: 100vh; height: 100dvh; font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); }
header { display: flex; gap: .875rem; align-items: baseline; padding: .5rem .875rem; background: var(--surface); border-bottom: 1px solid var(--border); }
header .back { color: var(--text-muted); text-decoration: none; font-size: .85rem; }
header .back:hover { color: var(--accent); }
header strong { font-size: .85rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
header .count { font-size: .74rem; color: var(--text-muted); }
header .viewport { margin-inline-start: auto; }
.viewport { display: flex; gap: .25rem; font-size: .78rem; }
.viewport a { padding: .2rem .4rem; text-align: center; color: var(--text-muted); text-decoration: none; background: var(--surface); border: 1px solid var(--border); border-radius: 6px; }
.viewport a:hover { color: var(--accent); border-color: var(--accent); }
.viewport a.active { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); font-weight: 600; }
.row { flex: 1; display: flex; gap: .875rem; overflow-x: auto; padding: .875rem; }
figure { flex: 1 1 0; min-width: 0; margin: 0; display: flex; flex-direction: column; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
figure.sized { flex: none; }
figcaption { display: flex; align-items: center; gap: .5rem; padding: .35rem .6rem; font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; border-bottom: 1px solid var(--border-muted); }
figcaption .name { color: var(--text); text-decoration: none; font-weight: 600; }
figcaption .name:hover { color: var(--accent); text-decoration: underline; }
figcaption .open { margin-inline-start: auto; color: var(--text-muted); text-decoration: none; font-family: system-ui, sans-serif; }
figcaption .open:hover { color: var(--accent); }
figure iframe { flex: 1; border: 0; width: 100%; }
</style></head>
<body>
<header><a class="back" href="{{.Index}}"{{if .IndexTarget}} target="{{.IndexTarget}}"{{end}}>&#8592; index</a><strong>{{.Name}}</strong><span class="count">{{len .Stories}} variants</span>
<div class="viewport">{{range .Widths}}<a href="?w={{.}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}flex{{end}}</a>{{end}}</div>
</header>
<div class="row">
{{range .Stories}}<figure{{if $.W}} class="sized"{{end}}><figcaption><a class="name" href="{{$.Index}}?s={{.Name}}"{{if $.IndexTarget}} target="{{$.IndexTarget}}"{{end}} title="open {{.Name}} in the index">{{.Name}}</a><a class="open" href="{{.Href}}" target="_blank" title="open this story in its own tab">&#8599;</a></figcaption><iframe src="{{.Href}}"{{if $.W}} style="width: {{$.W}}px"{{end}}></iframe></figure>
{{end}}</div>
<script>(function(){
  // The export writes one static page per grid; ?w= still reaches it
  // through the iframe src the index builds, so apply it client-side.
  var w=new URL(location.href).searchParams.get('w')||'';
  if(!w){return}
  [].forEach.call(document.querySelectorAll('figure'),function(f){
    f.classList.add('sized');
    f.querySelector('iframe').style.width=w+'px';
  });
  [].forEach.call(document.querySelectorAll('.viewport a'),function(a){
    a.classList.toggle('active',(a.getAttribute('href')||'')==='?w='+w);
  });
})();</script>
</body></html>
`))
