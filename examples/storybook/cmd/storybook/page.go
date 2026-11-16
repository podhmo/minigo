package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// entry is one row of the index page.
type entry struct {
	Story
	Href      string // index link: ?s=<Name> in live mode, <Name>.html in export
	FrameHref string // URL the preview iframe loads for this story; "" when it cannot be framed
	Grid      string // side-by-side grid URL of the entry's group
	Target    string // "story" in the static export, "_blank" for served http stories
	Blurb     string // Doc with the redundant story-name prefix dropped
	Err       string
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
	Href    string // the side-by-side grid view
	Target  string
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

// gridHref is a group's grid URL, "" when the story has no group.
func gridHref(groups []group, name string) string {
	for _, g := range groups {
		if g.Name == name {
			return g.Href
		}
	}
	return ""
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
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>stories: {{.Dir}}</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
<style>` + chromeTokens + `
[hidden] { display: none !important; }
body { margin: 0; display: flex; height: 100vh; height: 100dvh; font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); }
nav { width: 17rem; flex: none; display: flex; flex-direction: column; background: var(--surface); border-right: 1px solid var(--border); }
nav > header { padding: .75rem .875rem; border-bottom: 1px solid var(--border-muted); }
.brand { font-size: .85rem; font-weight: 600; }
.dir { margin-top: .15rem; font-size: .72rem; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); word-break: break-all; }
.err { color: var(--danger); }
.search { display: flex; align-items: center; gap: .5rem; margin-top: .625rem; }
.search input { flex: 1; min-width: 0; padding: .3rem .55rem; font: inherit; font-size: .8rem; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 6px; }
.search input:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
.search input::placeholder { color: var(--text-muted); }
.search .count { font-size: .72rem; color: var(--text-muted); white-space: nowrap; }
.list { flex: 1; overflow-y: auto; padding: .375rem .5rem .75rem; container-type: scroll-state; }
.group { display: flex; align-items: center; gap: .375rem; margin-top: .875rem; padding: .25rem .5rem; }
.group:first-child { margin-top: .375rem; }
.group .gname { font-size: .72rem; font-weight: 600; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--text-muted); }
.gridlink { margin-inline-start: auto; padding: .1rem .5rem; font-size: .68rem; color: var(--text-muted); text-decoration: none; border: 1px solid var(--border); border-radius: 99px; }
.gridlink:hover { color: var(--accent); border-color: var(--accent); background: var(--accent-soft); }
.story { display: block; padding: .4rem .55rem; border-radius: 6px; color: inherit; text-decoration: none; line-height: 1.35; overflow-wrap: anywhere; }
.story:hover { background: var(--surface-hover); }
.story:focus-visible, .gridlink:focus-visible, .act:focus-visible, .viewport a:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.story.active { background: var(--accent-soft); color: var(--accent); }
.story .sname { display: flex; align-items: center; flex-wrap: wrap; gap: .125rem .375rem; font-size: .85rem; font-weight: 500; word-break: break-word; }
.story.active .sname { font-weight: 600; }
.story .kind { margin-inline-start: auto; padding: 0 .35rem; font-size: .62rem; font-weight: 400; letter-spacing: .03em; color: var(--text-muted); border: 1px solid var(--border); border-radius: 99px; }
.story small { display: block; margin-top: .1rem; font-size: .74rem; font-weight: 400; color: var(--text-muted); }
.hidden { display: none !important; }
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
<div class="brand">stories</div>
<div class="dir">{{.Dir}}</div>
{{if .Err}}<small class="err">{{.Err}}</small>{{end}}
<div class="search"><input id="filter" type="search" placeholder="filter stories  ( / )" autocomplete="off" aria-label="filter stories"><span class="count" id="count"></span></div>
</header>
<div class="list" id="list">
{{range .Groups}}<section>
{{if .Name}}<div class="group"><span class="gname">{{.Name}}</span><a class="gridlink" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}} title="all {{.Name}} variants side by side">grid &#10514;</a></div>{{end}}
{{range .Entries}}<a class="story{{if eq .Name $.S}} active{{end}}" data-name="{{.Name}}" data-group="{{.Group}}"{{if .FrameHref}} data-frame="{{.FrameHref}}"{{end}}{{if .Grid}} data-grid="{{.Grid}}"{{end}} data-search="{{.Name}} {{.Doc}}" href="{{.Href}}"{{if .Target}} target="{{.Target}}"{{end}}{{if eq .Name $.S}} aria-current="true"{{end}}><span class="sname">{{.Name}}{{if .IsHTTP}}<span class="kind" title="handler story — replays real status and headers">http &#8599;</span>{{end}}</span>
{{if .Blurb}}<small>{{.Blurb}}</small>{{end}}{{if .Err}}<small class="err">{{.Err}}</small>{{end}}</a>
{{end}}</section>{{end}}
<div class="empty" id="empty" hidden>no matching stories</div>
</div>
</nav>
<main>
<div class="bar">
<span class="crumb"><span id="crumb-group"{{if not .SelGroup}} hidden{{end}}>{{.SelGroup}}</span><span id="crumb-sep" class="sep"{{if not .SelGroup}} hidden{{end}}>/</span><strong id="crumb-name">{{.S}}</strong></span>
<span class="spacer"></span>
<a id="act-grid" class="act" href="{{.GridHref}}"{{if not .GridHref}} hidden{{end}} title="all variants of this template side by side">grid &#10514;</a>
<a id="act-open" class="act"{{if .OpenHref}} href="{{.OpenHref}}"{{else}} hidden{{end}} target="_blank" title="open this story in its own tab">open &#8599;</a>
<div class="viewport" aria-label="preview width">{{range .Widths}}<a href="?w={{.}}&s={{$.S}}" data-w="{{.}}"{{if eq . $.W}} class="active"{{end}}>{{if .}}{{.}}px{{else}}full{{end}}</a>{{end}}</div>
</div>
<iframe id="frame" name="story"{{if .S}} src="{{.OpenHref}}"{{else}}{{if .Default}} src="{{.Default}}"{{end}}{{end}}{{if .W}} class="sized" style="width: {{.W}}px"{{end}}></iframe>
</main>
<script>(function(){
  var frame=document.getElementById('frame');
  var list=document.getElementById('list');
  var items=[].slice.call(list.querySelectorAll('a.story'));
  var sections=[].slice.call(list.querySelectorAll('section'));
  var crumbGroup=document.getElementById('crumb-group');
  var crumbSep=document.getElementById('crumb-sep');
  var crumbName=document.getElementById('crumb-name');
  var actGrid=document.getElementById('act-grid');
  var actOpen=document.getElementById('act-open');
  var widths=[].slice.call(document.querySelectorAll('.viewport a'));
  var q=document.getElementById('filter');
  var count=document.getElementById('count');
  var empty=document.getElementById('empty');
  function itemByName(name){
    for(var i=0;i<items.length;i++){if(items[i].dataset.name===name)return items[i]}
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
  function params(o){
    var u=new URL(location.href);
    if(o.s!==undefined){o.s?u.searchParams.set('s',o.s):u.searchParams.delete('s')}
    if(o.w!==undefined){o.w?u.searchParams.set('w',o.w):u.searchParams.delete('w')}
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
  function showCrumb(group,name){
    crumbGroup.textContent=group||'';
    crumbGroup.hidden=!group;
    crumbSep.hidden=!group;
    crumbName.textContent=name||'';
  }
  function showStory(a){
    showCrumb(a.dataset.group,a.dataset.name);
    if(a.dataset.grid){actGrid.href=a.dataset.grid;actGrid.hidden=false}else{actGrid.hidden=true}
    if(a.dataset.frame){actOpen.href=a.dataset.frame;actOpen.hidden=false}else{actOpen.hidden=true}
    mark(a);
  }
  function withW(href,w){
    var u=new URL(href,location.href);
    if(w){u.searchParams.set('w',w)}else{u.searchParams.delete('w')}
    return u.href;
  }
  function applyWidth(w,save){
    frame.classList.toggle('sized',!!w);
    frame.style.width=w?w+'px':'';
    widths.forEach(function(a){a.classList.toggle('active',a.dataset.w===w)});
    // The grid views carry their own ?w=; keep every link to them in
    // step with the chosen width so a grid opens at the same size.
    [].forEach.call(document.querySelectorAll('.gridlink'),function(a){a.href=withW(a.href,w)});
    items.forEach(function(a){if(a.dataset.grid){a.dataset.grid=withW(a.dataset.grid,w)}});
    if(!actGrid.hidden&&actGrid.getAttribute('href')){actGrid.href=withW(actGrid.href,w)}
    if(save){history.replaceState(null,'',params({w:w}))}
  }
  function sync(){
    var loc;
    try{loc=frame.contentWindow.location}catch(e){return}
    if(!loc||loc.href==='about:blank'){return}
    var g=gridFromURL(loc);
    if(g){
      showCrumb('grid',g+' variants');
      actGrid.hidden=true;
      actOpen.href=loc.href;actOpen.hidden=false;
      mark(null);
      return;
    }
    var a=itemByName(nameFromURL(loc));
    if(a){showStory(a);return}
    showCrumb('',loc.pathname);
    actGrid.hidden=true;actOpen.hidden=true;
    mark(null);
  }
  frame.addEventListener('load',sync);
  function plainClick(ev){
    return ev.button===0&&!ev.ctrlKey&&!ev.metaKey&&!ev.shiftKey&&!ev.altKey;
  }
  items.forEach(function(a){
    if(!a.dataset.frame){return}
    a.addEventListener('click',function(ev){
      // Modified clicks (new tab, etc.) keep the browser default.
      if(!plainClick(ev)){return}
      ev.preventDefault();
      frame.src=a.dataset.frame;
      history.pushState(null,'',params({s:a.dataset.name}));
      showStory(a);
    });
  });
  widths.forEach(function(a){
    a.addEventListener('click',function(ev){
      if(!plainClick(ev)){return}
      ev.preventDefault();applyWidth(a.dataset.w,true);
    });
  });
  window.addEventListener('popstate',function(){
    var u=new URL(location.href),a=itemByName(u.searchParams.get('s')||'');
    if(a&&a.dataset.frame){frame.src=a.dataset.frame;showStory(a)}
    applyWidth(u.searchParams.get('w')||'',false);
  });
  function visible(){return items.filter(function(a){return !a.classList.contains('hidden')})}
  function applyFilter(){
    var needle=(q.value||'').trim().toLowerCase();
    var shown=0;
    items.forEach(function(a){
      var hit=!needle||(a.dataset.search||'').toLowerCase().indexOf(needle)>=0;
      a.classList.toggle('hidden',!hit);
      if(hit){shown++}
    });
    sections.forEach(function(sec){
      var any=[].some.call(sec.querySelectorAll('a.story'),function(a){return !a.classList.contains('hidden')});
      sec.classList.toggle('hidden',!any);
    });
    count.textContent=needle?shown+'/'+items.length:'';
    empty.hidden=shown!==0;
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
  document.addEventListener('keydown',function(ev){
    var t=document.activeElement;
    var typing=t&&(t.tagName==='INPUT'||t.tagName==='TEXTAREA');
    if(ev.key==='/'&&!typing){ev.preventDefault();q.focus();q.select();return}
    if(ev.key==='Escape'&&t===q){q.value='';applyFilter();return}
    if(ev.key==='ArrowDown'||ev.key==='ArrowUp'){
      if(t===q||list.contains(t)){ev.preventDefault();step(ev.key==='ArrowDown'?1:-1)}
      return;
    }
    if(ev.key==='Enter'&&t===q){var vis=visible();if(vis[0]){vis[0].click()}}
  });
  // Honor ?s= / ?w= in both modes: the live server pre-renders them and
  // the static export relies on this script alone.
  var u=new URL(location.href);
  applyWidth(u.searchParams.get('w')||'',false);
  var a=itemByName(u.searchParams.get('s')||'');
  if(a&&a.dataset.frame){
    var want=new URL(a.dataset.frame,location.href);
    if(frame.src!==want.href){frame.src=want.href}
    showStory(a);
  }else{sync()}
})();</script>
</body></html>
`))

// gridTmpl lays one render function's stories side by side, so a CSS or
// template edit can be checked against every data state at once. Each
// variant is an iframe at the chosen width (?w=, flexible when unset).
var gridTmpl = template.Must(template.New("grid").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>{{.Name}} variants</title>{{if .ScriptTag}}{{.ScriptTag}}{{end}}
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
		entries[i] = entry{Story: s, Href: s.Name + ".html", FrameHref: s.Name + ".html", Target: "story", Blurb: blurb(s)}
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
		groups[i].Target = "story"
		for j := range groups[i].Entries {
			groups[i].Entries[j].Grid = groups[i].Href
		}
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
			if entries[i].FrameHref != "" {
				sel = &entries[i]
				break
			}
		}
	}
	data := map[string]any{"Dir": b.Dir, "Groups": groups, "Widths": []string{"", "320", "360", "768"}}
	if sel != nil {
		data["S"] = sel.Name
		data["Default"] = sel.Href
		data["SelGroup"] = sel.Group
		data["GridHref"] = gridHref(groups, sel.Group)
		data["OpenHref"] = sel.FrameHref
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
		entries := make([]entry, len(stories))
		for i, s := range stories {
			if s.IsHTTP() {
				// Handler stories replay the app's real response headers,
				// which can include frame-ancestors 'none' / X-Frame-Options:
				// DENY — they may not render inside the iframe, so they
				// open in a new tab with the headers intact.
				entries[i] = entry{Story: s, Href: "/story/" + s.Name, Target: "_blank", Blurb: blurb(s)}
				continue
			}
			href := "?s=" + s.Name
			if wpx != "" {
				href += "&w=" + wpx
			}
			entries[i] = entry{Story: s, Href: href, FrameHref: "/story/" + s.Name, Blurb: blurb(s)}
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
			if groups[i].Name == "" {
				continue
			}
			groups[i].Href = "/grid/" + groups[i].Name
			if wpx != "" {
				groups[i].Href += "?w=" + wpx
			}
			for j := range groups[i].Entries {
				groups[i].Entries[j].Grid = groups[i].Href
			}
		}
		// The toolbar mirrors the selected story: its group, the group's
		// grid, and the story's own URL for opening it in a tab.
		selGroup, grid, open := "", "", ""
		for _, e := range entries {
			if e.Name == sel {
				selGroup = e.Group
				grid = gridHref(groups, e.Group)
				open = e.FrameHref
				break
			}
		}
		if err := indexTmpl.Execute(w, map[string]any{
			"Dir":       b.Dir,
			"Groups":    groups,
			"S":         sel,
			"SelGroup":  selGroup,
			"GridHref":  grid,
			"OpenHref":  open,
			"W":         wpx,
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
		}); err != nil {
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
			"Index":     "/",
			"Name":      name,
			"Stories":   entries,
			"W":         r.URL.Query().Get("w"),
			"Widths":    []string{"", "320", "360", "768"},
			"ScriptTag": b.ScriptTag(),
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
