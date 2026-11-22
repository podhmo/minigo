package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// entry is one row of the index page.
type entry struct {
	Story
	Href   string // where the row's anchor points (serve: ?s=, export: Name.html)
	Src    string // what the preview iframe loads (serve: /story/, export: Name.html); "" when it cannot be framed
	Grid   string // this story's variants grid, for the toolbar's grid action
	Hay    string // lowercased haystack the filter matches: name + doc + group
	Target string // "story" in the static export, "_blank" for served http stories
	Blurb  string // Doc with the redundant story-name prefix dropped
	Hidden bool   // a non-match under ?q=: rendered but hidden so JS can restore it
	Err    string
}

// matchStory answers whether a story survives the ?q= filter: every
// whitespace-separated token must appear in the lowercased haystack of
// name + doc + group.
func matchStory(s Story, q string) bool {
	hay := strings.ToLower(s.Name + " " + s.Doc + " " + s.Group)
	for _, t := range strings.Fields(strings.ToLower(q)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// haystack is the searchable text carried on each row's data-haystack
// attribute; the live filter matches it the same way matchStory does.
func haystack(s Story) string {
	return strings.ToLower(s.Name + " " + s.Doc + " " + s.Group)
}

// blurb is the doc line without its redundant "StoryXxx renders ..."
// prefix — the row already shows the name, so repeating it only adds
// noise to the list.
func blurb(s Story) string {
	rest, ok := strings.CutPrefix(s.Doc, s.Name)
	if !ok {
		return s.Doc
	}
	return strings.TrimLeft(rest, " \t:—-")
}

// group is one render function's set of stories in the index. Ungrouped
// entries (handler stories) sit in the trailing group with an empty
// Name and no grid view: they are not variants of one template.
type group struct {
	Name    string
	Href    string // where the grid pill points (serve: ?g=, export: grid_X.html)
	Src     string // the grid document itself (serve: /grid/, export: grid_X.html)
	Target  string
	Hidden  bool // every entry filtered out under ?q=
	Entries []entry
}

// groupEntries buckets entries by their story's render function
// (Story.Group), ordered by group name; ungrouped entries keep their
// place in a trailing unnamed group.
func groupEntries(entries []entry) []group {
	var order []string
	byName := map[string]*group{}
	var rest []entry
	for _, e := range entries {
		if e.Group == "" {
			rest = append(rest, e)
			continue
		}
		g, ok := byName[e.Group]
		if !ok {
			g = &group{Name: e.Group}
			byName[e.Group] = g
			order = append(order, e.Group)
		}
		g.Entries = append(g.Entries, e)
	}
	slices.Sort(order)
	groups := make([]group, 0, len(order)+1)
	for _, n := range order {
		groups = append(groups, *byName[n])
	}
	if len(rest) > 0 {
		groups = append(groups, group{Entries: rest})
	}
	return groups
}

// gridRows filters a group's entries down to what the grid can frame:
// handler stories replay their app's real response headers, which can
// deny framing, so they stay index links only.
func gridRows(g group) []entry {
	var rows []entry
	for _, e := range g.Entries {
		if !e.IsHTTP() {
			rows = append(rows, e)
		}
	}
	return rows
}

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
  function mark(a){
    items.forEach(function(x){
      var on=x===a;
      x.classList.toggle('active',on);
      if(on){x.setAttribute('aria-current','true');x.scrollIntoView({block:'nearest'})}
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
      if(href.charAt(0)!=='?'){return}
      var u=new URL(href,location.href);
      keys.forEach(function(k){
        var v=k==='q'?t:cur[k];
        v?u.searchParams.set(k,v):u.searchParams.delete(k);
      });
      a.setAttribute('href',u.search);
    }
    items.forEach(function(a){if(a.dataset.src){patch(a,['w','q'])}});
    pills.forEach(function(a){patch(a,['w','q'])});
    widths.forEach(function(a){patch(a,['s','g','q'])});
    if(!actGrid.hidden){patch(actGrid,['w','q'])}
  }
  function activate(el,push){
    var w=cur.w;
    if(el.dataset.kind==='grid'){
      cur.g=el.dataset.name;cur.s='';
      frame.classList.remove('sized');frame.style.width='';
      var src=el.dataset.src+(w?(el.dataset.src.indexOf('?')<0?'?':'&')+'w='+w:'');
      if(frame.getAttribute('src')!==src){frame.src=src}
      showCrumb('grid',el.dataset.name+' variants','');
      actGrid.hidden=true;
      actOpen.href=el.dataset.src;actOpen.hidden=false;
      mark(null);
      markPills(cur.g);
    }else{
      cur.s=el.dataset.name;cur.g='';
      frame.classList.toggle('sized',!!w);frame.style.width=w?w+'px':'';
      if(frame.getAttribute('src')!==el.dataset.src){frame.src=el.dataset.src}
      showCrumb(el.dataset.group,el.dataset.name,el.dataset.pos);
      setBarGrid(el.dataset.group,el.dataset.grid);
      actOpen.href=el.dataset.src;actOpen.hidden=false;
      mark(el);
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

// Export renders every story into dir: index.html plus <Story>.html and
// a copy of the static directory, so serving the export root resolves
// /static/ the same way the live preview does. A failing story still
// gets its row (with the error) so the rest of the book is written, but
// Export then reports the failures.
func (b *Book) Export(ctx context.Context, dir string, stories []Story) error {
	// The containment checks below compare against the absolute static
	// root, so the output must be absolute too — a relative -o would
	// otherwise let dst compare unequal to src and RemoveAll it.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir = absDir
	// Fail fast on pathological layouts before writing anything: the
	// snapshot must neither write inside the static source (recursive
	// self-copy) nor contain it (the re-export cleanup would erase it).
	src := b.staticRoot()
	srcOK := false
	if src != "" {
		// The checks run whenever a static root is named — not only
		// when it is a directory: a `static` that is a regular file or
		// a path os.Stat cannot read must not pass the checks and reach
		// the unconditional cleanup below.
		dst := filepath.Join(dir, "static")
		// Compare link-resolved paths: a symlinked static root can
		// hide containment that the literal paths do not show. The
		// literal check still matters — a source reached through a
		// link inside the output dies with the re-export cleanup.
		realSrc, realDst := resolvePath(src), resolvePath(dst)
		inside := func(a, b string) bool { // a inside-or-equal b
			rel, err := filepath.Rel(b, a)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if inside(dst, src) || inside(realDst, realSrc) {
			return fmt.Errorf("export output must not be inside the static directory: %s", dst)
		}
		if inside(src, dst) || inside(realSrc, realDst) {
			return fmt.Errorf("static directory must not be inside the export output: %s", src)
		}
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			srcOK = true
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var errs []error
	entries := make([]entry, len(stories))
	for i, s := range stories {
		entries[i] = entry{Story: s, Href: s.Name + ".html", Src: s.Name + ".html", Target: "story", Hay: haystack(s), Blurb: blurb(s)}
		if s.Group != "" {
			entries[i].Grid = "grid_" + s.Group + ".html"
		}
		res, err := b.Render(ctx, s.Name)
		if err != nil {
			entries[i].Err = firstLine(err.Error())
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, s.Name+".html"), res.Body, 0o644); err != nil {
			return err
		}
	}
	// The variants grid exports too: one page per render function whose
	// iframes point at the exported story pages.
	groups := groupEntries(entries)
	for i := range groups {
		if groups[i].Name == "" {
			continue
		}
		groups[i].Href = "grid_" + groups[i].Name + ".html"
		groups[i].Src = groups[i].Href
		groups[i].Target = "story"
		f, err := os.Create(filepath.Join(dir, groups[i].Href))
		if err != nil {
			return err
		}
		err = gridTmpl.Execute(f, map[string]any{
			"Index": "index.html",
			// The grid renders inside the story iframe; the back link
			// must leave it, not nest a second index inside the first.
			"IndexTarget": "_top",
			"Name":        groups[i].Name,
			"Stories":     gridRows(groups[i]),
		})
		f.Close()
		if err != nil {
			return err
		}
	}
	f, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	// The first frameable story is the snapshot's landing page; the
	// toolbar shows it selected the way the live index does. Prefer a
	// print story when one exists, matching the live index's default.
	var sel *entry
	for i := range entries {
		if !entries[i].IsHTTP() {
			sel = &entries[i]
			break
		}
	}
	if sel == nil {
		for i := range entries {
			if entries[i].Src != "" {
				sel = &entries[i]
				break
			}
		}
	}
	data := map[string]any{"Dir": b.Dir, "Groups": groups, "Total": len(stories), "Widths": []string{"", "320", "360", "768"}}
	if sel != nil {
		data["S"] = sel.Name
		data["Src"] = sel.Src
		data["SelGroup"] = sel.Group
		data["SelPos"] = sel.Pos
		data["OpenHref"] = sel.Src
		if sel.Grid != "" {
			data["GridHref"] = "?g=" + sel.Group
			data["GridSrc"] = sel.Grid
		}
	}
	if err := indexTmpl.Execute(f, data); err != nil {
		return err
	}
	// The snapshot mirrors the static source: a fresh copy drops assets
	// deleted upstream since the last export, and a vanished source
	// means no static in the output either. The containment checks
	// above keep the removal safe.
	dst := filepath.Join(dir, "static")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if srcOK {
		if err := copyTree(src, dst); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

// copyTree copies regular files under src into dst, preserving the
// layout. Symlinks resolve the way http.FileServer resolves them: linked
// assets land in the snapshot, and an alias naming a directory already
// copied is copied again under its own name. What is refused is a link
// cycle — a directory on its own copy path (self -> .) — tracked by the
// real paths currently on the copy stack, not by every directory seen;
// a dangling link, which the live server would answer 404, is skipped.
func copyTree(src, dst string) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyDir(real, dst, map[string]bool{real: true})
}

func copyDir(dir, dst string, active map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		to := filepath.Join(dst, e.Name())
		mode := e.Type()
		if mode&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			info, err := os.Stat(target)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if active[target] {
					continue
				}
				active[target] = true
				err := copyDir(target, to, active)
				delete(active, target)
				if err != nil {
					return err
				}
				continue
			}
			mode = info.Mode().Type()
		}
		if e.IsDir() {
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			active[real] = true
			err = copyDir(real, to, active)
			delete(active, real)
			if err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Handler serves the index at /, each story at /story/<Name>, and the
// application's static assets under /static/ (default <pkgdir>/static),
// so previews pick up stylesheet edits on a plain browser reload. A fresh
// interpretation on each request reflects source and template edits;
// CSS needs no re-execution at all. Story failures answer 500. Print
// stories answer 200; handler-shaped stories replay the status and
// headers their recorder captured.
func (b *Book) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		stories, err := b.Stories()
		if err != nil {
			// No stories yet (or a malformed one): show the error in the
			// nav rather than failing, so the book is usable while the
			// stories are written.
			if err := indexTmpl.Execute(w, map[string]any{
				"Dir":       b.Dir,
				"Err":       err.Error(),
				"ScriptTag": b.ScriptTag(),
			}); err != nil {
				slog.ErrorContext(r.Context(), "index", "err", err)
			}
			return
		}
		wpx := r.URL.Query().Get("w")
		q := r.URL.Query().Get("q")
		state := ""
		if wpx != "" {
			state += "&w=" + wpx
		}
		if q != "" {
			state += "&q=" + url.QueryEscape(q)
		}
		// Non-matching stories are rendered with a hidden marker rather
		// than omitted: no-JS readers still get the filtered view, and
		// the live filter can un-hide rows when the query changes.
		entries := make([]entry, len(stories))
		matched := 0
		for i, s := range stories {
			e := entry{Story: s, Hay: haystack(s), Blurb: blurb(s), Hidden: q != "" && !matchStory(s, q)}
			if s.IsHTTP() {
				// Handler stories replay the app's real response headers,
				// which can include frame-ancestors 'none' / X-Frame-Options:
				// DENY — they may not render inside the iframe, so they
				// open in a new tab with the headers intact.
				e.Href = "/story/" + s.Name
				e.Target = "_blank"
			} else {
				e.Href = "?s=" + s.Name + state
				e.Src = "/story/" + s.Name
				if s.Group != "" {
					e.Grid = "/grid/" + s.Group
				}
			}
			if !e.Hidden {
				matched++
			}
			entries[i] = e
		}
		// ?s=<Name> selects the previewed story so width switching (a full
		// index reload) keeps it; an unknown or missing name falls back to
		// the first non-HTTP entry.
		sel := r.URL.Query().Get("s")
		known := false
		for _, s := range stories {
			if s.Name == sel && !s.IsHTTP() {
				known = true
				break
			}
		}
		if !known {
			sel = ""
			for _, s := range stories {
				if !s.IsHTTP() {
					sel = s.Name
					break
				}
			}
		}
		groups := groupEntries(entries)
		for i := range groups {
			visible := false
			for _, e := range groups[i].Entries {
				if !e.Hidden {
					visible = true
					break
				}
			}
			groups[i].Hidden = !visible
			if groups[i].Name == "" {
				continue
			}
			groups[i].Href = "?g=" + groups[i].Name + state
			groups[i].Src = "/grid/" + groups[i].Name
		}
		// ?g=<renderFunc> previews the variants grid inside the index
		// frame, the same pane stories render in; the grid page itself
		// stays reachable for a full-width compare.
		g := r.URL.Query().Get("g")
		src := ""
		if g != "" {
			for _, gr := range groups {
				if gr.Name == g {
					src = "/grid/" + g
					if wpx != "" {
						src += "?w=" + wpx
					}
				}
			}
			if src == "" {
				g = ""
			}
		}
		// The toolbar mirrors the selected story: its group, the group's
		// grid, and the story's own URL for opening it in a tab.
		selGroup, selPos, selGrid, open := "", "", "", ""
		for _, e := range entries {
			if e.Name == sel {
				selGroup, selPos, selGrid, open = e.Group, e.Pos, e.Grid, e.Src
				break
			}
		}
		td := map[string]any{
			"Dir":       b.Dir,
			"Groups":    groups,
			"S":         sel,
			"G":         g,
			"SelGroup":  selGroup,
			"SelPos":    selPos,
			"W":         wpx,
			"Q":         q,
			"Total":     len(stories),
			"Matched":   matched,
			"None":      q != "" && matched == 0,
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
		}
		if g != "" {
			td["Src"] = src
			td["OpenHref"] = "/grid/" + g
		} else {
			td["Src"] = open
			td["OpenHref"] = open
			td["Sized"] = wpx != "" && sel != ""
			if selGrid != "" {
				td["GridHref"] = "?g=" + selGroup + state
				td["GridSrc"] = selGrid
			}
		}
		if err := indexTmpl.Execute(w, td); err != nil {
			slog.ErrorContext(r.Context(), "index", "err", err)
		}
	})
	mux.HandleFunc("GET /grid/{group}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("group")
		stories, err := b.Stories()
		if err != nil {
			b.errorPage(w, http.StatusInternalServerError, err)
			return
		}
		var entries []entry
		for _, s := range stories {
			if s.Group == name && !s.IsHTTP() {
				entries = append(entries, entry{Story: s, Href: "/story/" + s.Name})
			}
		}
		if len(entries) == 0 {
			b.errorPage(w, http.StatusNotFound, errors.New("no stories in this group"))
			return
		}
		if err := gridTmpl.Execute(w, map[string]any{
			"Index": "/",
			// The grid may render inside the index's preview frame; its
			// back/story links must replace the whole page, not nest a
			// second index inside the first.
			"IndexTarget": "_top",
			"Name":        name,
			"Stories":     entries,
			"W":           r.URL.Query().Get("w"),
			"Widths":      []string{"", "320", "360", "768"},
			"ScriptTag":   b.ScriptTag(),
		}); err != nil {
			slog.ErrorContext(r.Context(), "grid", "err", err)
		}
	})
	mux.HandleFunc("GET /story/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, err := b.Render(r.Context(), name)
		if err != nil {
			b.errorPage(w, http.StatusInternalServerError, err)
			return
		}
		// Handler-shaped stories recorded real response headers and a
		// status — replay them so the preview exercises what the app's
		// handler emits (security headers, CSP, non-200 states).
		h := w.Header()
		for k, vs := range res.Header {
			h[k] = vs
		}
		if len(res.Body) > 0 && h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/html; charset=utf-8")
		}
		// A story opened in its own tab reloads too — only when the
		// response is HTML, so non-HTML bodies stay byte-identical.
		body := b.AppendLiveReload(h, res.Body)
		w.WriteHeader(res.Status)
		w.Write(body)
	})
	mux.Handle("GET /static/", b.staticHandler())
	b.MountLiveReload(mux)
	return mux
}

// errorPage answers an error response. While watching it is a small
// HTML page carrying the reload script — a text/plain error cannot run
// it, and a page that loses the script never rejoins the reload loop,
// so a transient mid-edit failure would stick until a manual refresh.
func (b *Book) errorPage(w http.ResponseWriter, status int, err error) {
	if tag := b.ScriptTag(); tag != "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<pre>%s</pre>%s", template.HTMLEscapeString(err.Error()), tag)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, err)
}

// resolvePath resolves symlinks in the existing prefix of p and rejoins
// the not-yet-existing tail, so containment checks work on planned paths.
func resolvePath(p string) string {
	rest := ""
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
