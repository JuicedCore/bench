package harness

// Page chrome for the run book. Colors follow the data-viz reference palette:
// categorical slots 1-12 for series, reserved status colors that always travel
// with an icon and a label, and hairline recessive grid/axes.

const runbookHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Benchmark Run Book</title>
<style>
:root{
  color-scheme:light;
  --page:#f4f5f3;--surface:#fbfbfa;--raised:#ffffff;--side:#f7f7f5;
  --ink:#141413;--ink-2:#5c5b57;--muted:#7a7974;
  --grid:#e4e3dc;--axis:#c9c8bf;--ring:rgba(20,20,19,.08);
  --focus:#2a78d6;
  --s1:#2a78d6;--s2:#eb6834;--s3:#0f9d8e;--s4:#7c5cbf;--s5:#3d8b40;--s6:#c43d7e;--s7:#8c6d3f;--s8:#1a8fb3;--s9:#6b8e23;--s10:#c45c26;--s11:#5c6b7a;--s12:#c4a035;
  --good:#006300;--good-bg:#e7f3e7;
  --warn:#8a5a00;--warn-bg:#fdf3dd;
  --serious:#9c4318;--serious-bg:#fbeae2;
  --bad:#b42e2e;--bad-bg:#fbe7e7;
  --accent:#2a78d6;--accent-bg:#e8f1fc;
  --side-w:288px;
}
@media (prefers-color-scheme:dark){:root:where(:not([data-theme="light"])){
  color-scheme:dark;
  --page:#111110;--surface:#1a1a19;--raised:#222220;--side:#161615;
  --ink:#f4f3ee;--ink-2:#c3c2b7;--muted:#9a9990;
  --grid:#2c2c2a;--axis:#3a3a36;--ring:rgba(255,255,255,.08);
  --s1:#3987e5;--s2:#d95926;--s3:#2ec4b6;--s4:#9b7ee0;--s5:#5dce60;--s6:#e56aa0;--s7:#c49a6c;--s8:#4eb8d9;--s9:#a3c94a;--s10:#e08950;--s11:#8a9aab;--s12:#e0c14a;
  --good:#3fbf3f;--good-bg:#132613;
  --warn:#e8b030;--warn-bg:#2b2311;
  --serious:#ec835a;--serious-bg:#2e1c14;
  --bad:#ef7070;--bad-bg:#301818;
  --accent:#6da7ec;--accent-bg:#16263a;
}}
:root[data-theme="dark"]{
  color-scheme:dark;
  --page:#111110;--surface:#1a1a19;--raised:#222220;--side:#161615;
  --ink:#f4f3ee;--ink-2:#c3c2b7;--muted:#9a9990;
  --grid:#2c2c2a;--axis:#3a3a36;--ring:rgba(255,255,255,.08);
  --s1:#3987e5;--s2:#d95926;--s3:#2ec4b6;--s4:#9b7ee0;--s5:#5dce60;--s6:#e56aa0;--s7:#c49a6c;--s8:#4eb8d9;--s9:#a3c94a;--s10:#e08950;--s11:#8a9aab;--s12:#e0c14a;
  --good:#3fbf3f;--good-bg:#132613;
  --warn:#e8b030;--warn-bg:#2b2311;
  --serious:#ec835a;--serious-bg:#2e1c14;
  --bad:#ef7070;--bad-bg:#301818;
  --accent:#6da7ec;--accent-bg:#16263a;
}
*{box-sizing:border-box}
html,body{margin:0;background:var(--page);color:var(--ink)}
body{font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
code,pre{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px}
.mute{color:var(--muted)}
button,select,input{font:inherit;color:inherit}
button:focus-visible,select:focus-visible,input:focus-visible,a:focus-visible,.group-h:focus-visible{
  outline:2px solid var(--focus);outline-offset:2px
}
.shell{display:grid;grid-template-columns:var(--side-w) minmax(0,1fr);min-height:100vh}
.side{position:sticky;top:0;height:100vh;overflow:hidden;display:flex;flex-direction:column;border-right:1px solid var(--grid);background:var(--side);padding:12px 10px 24px}
.navbody{flex:1;overflow-y:auto;min-height:0;padding:0 2px 8px}
.navtoggle{display:none}
.brandrow{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:2px 4px 12px;flex:0 0 auto}
.brand{display:block;font-weight:650;font-size:14.5px;color:var(--ink);letter-spacing:-.01em}
.side-actions{display:flex;align-items:center;gap:6px}
.theme{padding:5px 9px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink-2);font:12px/1.2 inherit;cursor:pointer}
.iconbtn{width:32px;height:32px;padding:0;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink-2);cursor:pointer;position:relative}
.sidetoggle::before,.side-reopen::before{
  content:"";position:absolute;width:7px;height:7px;border-left:1.6px solid currentColor;border-bottom:1.6px solid currentColor
}
.sidetoggle::before{left:13px;top:12px;transform:rotate(45deg)}
.side-reopen{display:none;position:fixed;z-index:6;left:12px;top:12px;width:auto;height:34px;padding:0 12px 0 30px;font:12.5px/34px inherit;font-weight:650;color:var(--ink)}
.side-reopen::before{left:12px;top:13px;transform:rotate(-135deg)}
.search{width:100%;padding:8px 10px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink);margin:0 0 12px}
.group{margin:0 0 4px;border-radius:10px}
.group-h{display:flex;align-items:center;gap:8px;width:100%;padding:7px 8px;border:0;border-radius:8px;background:transparent;color:var(--ink);cursor:pointer;text-align:left}
.group-h::before{
  content:"";width:6px;height:6px;flex:0 0 auto;border-right:1.6px solid var(--muted);border-bottom:1.6px solid var(--muted);transform:rotate(45deg);margin:0 2px 2px 0;transition:transform .15s
}
.group.collapsed .group-h::before{transform:rotate(-45deg);margin-bottom:0}
.group-h:hover{background:var(--accent-bg)}
.group-h .plat{font-weight:650;font-size:12.5px;flex:1;overflow:hidden;text-overflow:ellipsis}
.group-h .mute{font-size:11.5px;font-variant-numeric:tabular-nums;margin-left:auto}
.group.collapsed ul{display:none}
.plat{font-weight:650}
.callout.notes{background:var(--warn-bg);border-color:color-mix(in srgb,var(--warn) 40%,transparent);border-left:3px solid var(--warn);margin:0 0 20px}
.callout.notes>summary{cursor:pointer;list-style:none;display:flex;align-items:center;gap:8px}
.callout.notes>summary::-webkit-details-marker{display:none}
.callout.notes>summary::before{content:"";width:6px;height:6px;border-right:1.6px solid var(--warn);border-bottom:1.6px solid var(--warn);transform:rotate(45deg);margin-bottom:2px}
.callout.notes:not([open])>summary::before{transform:rotate(-45deg);margin-bottom:0}
.callout.notes strong{color:var(--warn)}
.callout.notes ol{margin:8px 0 8px 20px;padding:0}
.callout.notes li{margin:8px 0}
.callout.notes p{margin:8px 0}
.callout.notes .notes-table{margin:10px 0}
.callout.notes .notes-table table{font-size:13px}
.callout.notes .notes-table th,.callout.notes .notes-table td{white-space:normal}
table.runs tr.plat-h th{text-align:left;background:var(--side);color:var(--ink);font-size:13px;font-weight:650;padding:11px 12px;border-bottom:1px solid var(--grid);position:static}
.side ul{list-style:none;margin:0;padding:0 0 6px}
.side li a{display:grid;grid-template-columns:18px minmax(0,1fr) auto;grid-template-rows:auto auto;gap:1px 8px;align-items:center;padding:6px 8px 6px 10px;border-radius:8px;color:var(--ink);border-left:3px solid transparent}
.side li a:hover{background:var(--accent-bg);text-decoration:none}
.side li.active a{background:var(--accent-bg);border-left-color:var(--accent);font-weight:600}
.side .st{grid-row:1 / span 2}
.side .nm{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:600;font-size:13px}
.side .when{font-size:11px;color:var(--muted);font-variant-numeric:tabular-nums}
.side .sub{grid-column:2 / span 2;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11.5px;color:var(--muted)}
.pill{display:inline-flex;align-items:center;padding:1px 8px;border-radius:999px;font-size:12px;font-weight:650;letter-spacing:.01em;white-space:nowrap;border:1px solid transparent}
.pill.wl.w-write{color:var(--s1);background:var(--accent-bg);border-color:color-mix(in srgb,var(--s1) 28%,transparent)}
.pill.wl.w-mixed{color:var(--s4);background:color-mix(in srgb,var(--s4) 14%,transparent);border-color:color-mix(in srgb,var(--s4) 28%,transparent)}
.pill.wl.w-read{color:var(--s3);background:color-mix(in srgb,var(--s3) 14%,transparent);border-color:color-mix(in srgb,var(--s3) 28%,transparent)}
.pill.wl.w-xfer{color:var(--s2);background:color-mix(in srgb,var(--s2) 14%,transparent);border-color:color-mix(in srgb,var(--s2) 28%,transparent)}
.pill.wl.w-other{color:var(--ink-2);background:var(--surface);border-color:var(--ring)}
.pill.mode.native{color:var(--s6);background:color-mix(in srgb,var(--s6) 12%,transparent);border-color:color-mix(in srgb,var(--s6) 28%,transparent)}
.pill.mode.normalized{color:var(--s5);background:var(--good-bg);border-color:color-mix(in srgb,var(--s5) 28%,transparent)}
.pill.proto{color:var(--ink);background:var(--raised);border-color:var(--axis)}
.loadpath{display:flex;gap:14px;align-items:flex-start;background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:12px 16px;margin:0 0 18px}
.lp-k{font-size:11px;text-transform:uppercase;letter-spacing:.06em;color:var(--ink-2);font-weight:650;flex:0 0 auto;padding-top:3px}
.lp-v{font-size:13.5px;line-height:1.45}
table.runs tr.plat-h .lp{font-weight:500;color:var(--muted);font-size:12.5px;margin-left:12px}
table.load-legend td.wrap{min-width:220px}
.wl-bar{display:flex;flex-wrap:wrap;gap:8px;margin:4px 0 18px}
.wl-count{display:inline-flex;align-items:center;gap:6px;padding:5px 10px;border:1px solid var(--ring);border-radius:999px;background:var(--surface)}
.wl-count b{font-variant-numeric:tabular-nums}
.st{display:inline-grid;place-items:center;width:16px;height:16px;border-radius:50%;font-size:10px;font-weight:700;line-height:1}
.st.completed,.badge.completed{color:var(--good);background:var(--good-bg)}
.st.excluded,.badge.excluded{color:var(--warn);background:var(--warn-bg)}
.st.failed,.badge.failed{color:var(--bad);background:var(--bad-bg)}
.st.aborted,.badge.aborted{color:var(--serious);background:var(--serious-bg)}
main{padding:28px 36px 72px;max-width:1280px;width:100%}
.page+.page{margin-top:48px;padding-top:32px;border-top:1px solid var(--grid)}
.js .page+.page{margin-top:0;padding-top:0;border-top:0}
.js .page{display:none}.js .page.shown{display:block}
.page-h h1{font-size:26px;line-height:1.2;margin:6px 0 8px;font-weight:650;letter-spacing:-.02em}
.crumbs{display:flex;justify-content:space-between;gap:12px;font-size:13px}
.pager a{margin-left:16px}
.meta{display:flex;flex-wrap:wrap;gap:8px 10px;align-items:center;color:var(--ink-2);margin:0 0 20px}
.badge{display:inline-flex;gap:4px;align-items:center;padding:2px 10px;border-radius:999px;font-size:12.5px;font-weight:600;white-space:nowrap}
h2{font-size:16px;margin:28px 0 10px;font-weight:650;letter-spacing:-.01em}
h3{font-size:12px;margin:0 0 8px;font-weight:650;text-transform:uppercase;letter-spacing:.05em;color:var(--ink-2)}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(170px,100%),1fr));gap:12px;margin:8px 0 4px}
.tile{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px}
.tile-l{font-size:12px;color:var(--ink-2);letter-spacing:.01em}
.tile-v{font-size:24px;font-weight:650;margin-top:2px;line-height:1.15;letter-spacing:-.02em;font-variant-numeric:tabular-nums}
.tile-n{font-size:12px;color:var(--muted);margin-top:4px}
.callout{border-radius:10px;padding:12px 16px;margin:0 0 16px;border:1px solid var(--ring);background:var(--surface)}
.callout ul{margin:6px 0 0 18px;padding:0}
.callout pre{white-space:pre-wrap;margin:8px 0 0;max-height:320px;overflow:auto}
.callout.failed{background:var(--bad-bg)}.callout.failed strong{color:var(--bad)}
.callout.excluded{background:var(--warn-bg)}.callout.excluded strong{color:var(--warn)}
.callout.aborted{background:var(--serious-bg)}.callout.aborted strong{color:var(--serious)}
.filters{display:flex;flex-wrap:wrap;gap:10px 16px;margin:20px 0 12px;position:sticky;top:0;z-index:2;padding:10px 12px;background:var(--surface);border:1px solid var(--ring);border-radius:12px}
.filters label{display:flex;gap:8px;align-items:center;color:var(--ink-2);font-size:13px}
.filters select{padding:6px 10px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink)}
.scroll{overflow-x:auto;border:1px solid var(--ring);border-radius:12px;background:var(--surface)}
table{border-collapse:collapse;width:100%}
th,td{padding:8px 12px;text-align:left;border-bottom:1px solid var(--grid);white-space:nowrap;vertical-align:top}
tbody tr:last-child td{border-bottom:0}
th{font-size:12px;font-weight:650;color:var(--ink-2);background:var(--raised);position:sticky;top:0}
.num{text-align:right;font-variant-numeric:tabular-nums}
td.wrap{white-space:normal;min-width:240px;word-break:break-word}
td.good{color:var(--good)}td.warn{color:var(--warn);white-space:normal;min-width:220px}
table.runs tbody tr:hover{background:var(--accent-bg)}
.charts{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(380px,100%),1fr));gap:16px}
.chart{margin:0;background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px 8px}
.chart figcaption{font-weight:650;font-size:13.5px}
.legend{display:flex;flex-wrap:wrap;gap:6px 14px;margin:6px 0 0;font-size:12px;color:var(--ink-2)}
.legend span{display:inline-flex;align-items:center;gap:6px}
.key{display:inline-block;width:14px;height:2px;border-radius:1px}
.key.s1{background:var(--s1)}.key.s2{background:var(--s2)}.key.s3{background:var(--s3)}.key.s4{background:var(--s4)}.key.s5{background:var(--s5)}.key.s6{background:var(--s6)}.key.s7{background:var(--s7)}.key.s8{background:var(--s8)}.key.s9{background:var(--s9)}.key.s10{background:var(--s10)}.key.s11{background:var(--s11)}.key.s12{background:var(--s12)}
.plot{position:relative}
.chart svg{display:block;width:100%;height:auto;overflow:visible}
.chart .grid{stroke:var(--grid);stroke-width:1}
.chart .base{stroke:var(--axis);stroke-width:1}
.chart .tick{fill:var(--muted);font-size:11.5px;font-variant-numeric:tabular-nums}
.chart .line{fill:none;stroke-width:2;stroke-linejoin:round;stroke-linecap:round}
.chart .line.s1{stroke:var(--s1)}.chart .line.s2{stroke:var(--s2)}.chart .line.s3{stroke:var(--s3)}.chart .line.s4{stroke:var(--s4)}.chart .line.s5{stroke:var(--s5)}.chart .line.s6{stroke:var(--s6)}.chart .line.s7{stroke:var(--s7)}.chart .line.s8{stroke:var(--s8)}.chart .line.s9{stroke:var(--s9)}.chart .line.s10{stroke:var(--s10)}.chart .line.s11{stroke:var(--s11)}.chart .line.s12{stroke:var(--s12)}
.chart .dot{stroke:var(--surface);stroke-width:2}
.chart .dot.s1{fill:var(--s1)}.chart .dot.s2{fill:var(--s2)}.chart .dot.s3{fill:var(--s3)}.chart .dot.s4{fill:var(--s4)}.chart .dot.s5{fill:var(--s5)}.chart .dot.s6{fill:var(--s6)}.chart .dot.s7{fill:var(--s7)}.chart .dot.s8{fill:var(--s8)}.chart .dot.s9{fill:var(--s9)}.chart .dot.s10{fill:var(--s10)}.chart .dot.s11{fill:var(--s11)}.chart .dot.s12{fill:var(--s12)}
.chart .hit{fill:transparent;cursor:crosshair;outline:none}
.chart .cross{stroke:var(--axis);stroke-width:1;visibility:hidden}
.details{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(280px,100%),1fr));gap:12px}
.card{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px}
dl{display:grid;grid-template-columns:minmax(110px,max-content) 1fr;gap:6px 14px;margin:0}
dt{color:var(--ink-2)}dd{margin:0;word-break:break-word}
details{margin:12px 0}summary{cursor:pointer;color:var(--ink-2)}
details .scroll{margin-top:8px}
.caveats{margin:0;padding-left:18px}.caveats li{margin:4px 0}
.files{display:flex;flex-wrap:wrap;gap:8px;list-style:none;margin:0;padding:0}
.files a{display:inline-block;padding:4px 10px;border:1px solid var(--ring);border-radius:8px;background:var(--surface);font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px}
.tooltip{position:fixed;z-index:10;pointer-events:none;background:var(--raised);color:var(--ink);border:1px solid var(--ring);border-radius:8px;padding:8px 10px;font-size:12.5px;min-width:150px}
.tooltip .tt{color:var(--ink-2);margin-bottom:4px}
.tooltip .row{display:grid;grid-template-columns:14px auto 1fr;gap:6px;align-items:center}
.tooltip .row b{font-variant-numeric:tabular-nums}
.tooltip .row span:last-child{color:var(--ink-2)}
html.side-collapsed{--side-w:0px}
html.side-collapsed .side{visibility:hidden;padding:0;border:0;pointer-events:none}
html.side-collapsed .side-reopen{display:inline-flex;align-items:center}
html.side-collapsed main{padding-top:56px}
@media (max-width:860px){
  .shell{grid-template-columns:minmax(0,1fr)}
  html.side-collapsed{--side-w:0px}
  html.side-collapsed .side{visibility:visible;padding:12px 16px;border:0;border-bottom:1px solid var(--grid);pointer-events:auto;height:auto}
  html.side-collapsed .side-reopen,html.side-collapsed main{padding-top:0}
  .side-reopen,.sidetoggle{display:none}
  html.side-collapsed .side-reopen{display:none}
  .side{position:static;height:auto;overflow:visible;padding:12px 16px;display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px;border-right:0;border-bottom:1px solid var(--grid)}
  .brandrow{padding:0;flex:1}
  .navtoggle{display:inline-block;padding:6px 12px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink)}
  .navbody{display:none;flex-basis:100%;max-height:60vh;overflow-y:auto;padding-top:8px}
  .side.open .navbody{display:block}
  .crumbs{flex-wrap:wrap}.pager a{margin-left:0;margin-right:16px}
  dl{grid-template-columns:1fr}dd{margin-bottom:6px}
  main{padding:20px 16px 48px}
  .charts{grid-template-columns:1fr}
}
@media print{
  .side,.filters,.pager,.tooltip,.theme,.sidetoggle,.side-reopen,.navtoggle{display:none!important}
  .shell{display:block}
  .js .page{display:block!important;break-before:page}
}
@media (prefers-reduced-motion:reduce){
  .group-h::before,.callout.notes>summary::before{transition:none}
}
</style>
</head>
<body>
<script>document.documentElement.classList.add("js")</script>
`

const runbookScript = `<script>
(function(){
  var pages = Array.prototype.slice.call(document.querySelectorAll("section.page"));
  var navItems = Array.prototype.slice.call(document.querySelectorAll(".side li[data-run]"));
  var groups = Array.prototype.slice.call(document.querySelectorAll(".side .group"));
  var root = document.documentElement;
  var SIDE_KEY = "bench-runbook-side";
  var GROUP_KEY = "bench-runbook-groups";

  function loadGroups(){
    try { return JSON.parse(localStorage.getItem(GROUP_KEY) || "{}"); } catch (e) { return {}; }
  }
  function setGroup(g, open, persist){
    g.classList.toggle("collapsed", !open);
    var btn = g.querySelector(".group-h");
    if (btn) btn.setAttribute("aria-expanded", open ? "true" : "false");
    if (persist) {
      var st = loadGroups();
      st[g.getAttribute("data-group-platform") || ""] = open;
      try { localStorage.setItem(GROUP_KEY, JSON.stringify(st)); } catch (e) {}
    }
  }
  (function restoreGroups(){
    var st = loadGroups();
    groups.forEach(function(g){
      var name = g.getAttribute("data-group-platform");
      if (name && Object.prototype.hasOwnProperty.call(st, name)) setGroup(g, !!st[name], false);
    });
  })();
  groups.forEach(function(g){
    var btn = g.querySelector(".group-h");
    if (!btn) return;
    btn.addEventListener("click", function(){
      setGroup(g, g.classList.contains("collapsed"), true);
    });
  });

  function desktop(){ return window.innerWidth > 860; }
  function setSide(collapsed){
    root.classList.toggle("side-collapsed", collapsed);
    try { localStorage.setItem(SIDE_KEY, collapsed ? "collapsed" : "open"); } catch (e) {}
  }
  try { if (localStorage.getItem(SIDE_KEY) === "collapsed") root.classList.add("side-collapsed"); } catch (e) {}
  var sidetoggle = document.querySelector(".sidetoggle");
  var reopen = document.querySelector(".side-reopen");
  if (sidetoggle) sidetoggle.addEventListener("click", function(){ setSide(true); });
  if (reopen) reopen.addEventListener("click", function(){ setSide(false); });

  function show(){
    var id = decodeURIComponent(location.hash.replace(/^#/, "")) || "overview";
    var target = document.getElementById(id);
    if (!target || !target.classList.contains("page")) { target = document.getElementById("overview"); id = "overview"; }
    pages.forEach(function(p){ p.classList.toggle("shown", p === target); });
    navItems.forEach(function(li){
      var on = li.getAttribute("data-run") === id;
      li.classList.toggle("active", on);
      if (on) {
        var g = li.closest(".group");
        if (g) setGroup(g, true, true);
        if (desktop() && li.scrollIntoView) li.scrollIntoView({block: "nearest"});
      }
    });
    document.title = (id === "overview" ? "Benchmark Run Book" : target.querySelector("h1").textContent + " · Run Book");
    window.scrollTo(0, 0);
  }
  window.addEventListener("hashchange", show);
  show();

  var side = document.querySelector(".side"), toggle = document.querySelector(".navtoggle");
  if (toggle) {
    toggle.addEventListener("click", function(){
      var open = side.classList.toggle("open");
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
    });
  }
  document.querySelectorAll(".side li a").forEach(function(a){
    a.addEventListener("click", function(){
      if (!toggle) return;
      side.classList.remove("open");
      toggle.setAttribute("aria-expanded", "false");
    });
  });

  var search = document.querySelector(".search");
  var selects = Array.prototype.slice.call(document.querySelectorAll("select[data-filter]"));
  function applyFilters(){
    var q = (search && search.value || "").trim().toLowerCase();
    var want = {};
    selects.forEach(function(s){ want[s.getAttribute("data-filter")] = s.value; });
    function ok(el){
      if (want.platform && el.getAttribute("data-platform") !== want.platform) return false;
      if (want.status && el.getAttribute("data-status") !== want.status) return false;
      if (want.workload && el.getAttribute("data-workload") !== want.workload) return false;
      if (want.mode && el.getAttribute("data-mode") !== want.mode) return false;
      return true;
    }
    document.querySelectorAll("table.runs tbody tr").forEach(function(tr){
      if (tr.classList.contains("plat-h")) return;
      var text = tr.getAttribute("data-text") || "";
      tr.hidden = !ok(tr) || (q && text.indexOf(q) < 0);
    });
    navItems.forEach(function(li){
      li.hidden = !ok(li) || (q && (li.getAttribute("data-text") || "").indexOf(q) < 0);
    });
    document.querySelectorAll(".side .group").forEach(function(g){
      var empty = !g.querySelector("li:not([hidden])");
      g.hidden = empty;
      if (!empty && q) setGroup(g, true, false);
    });
    document.querySelectorAll("table.runs tbody tr.plat-h").forEach(function(h){
      var n = h.nextElementSibling, any = false;
      while (n && !n.classList.contains("plat-h")) {
        if (!n.hidden) any = true;
        n = n.nextElementSibling;
      }
      h.hidden = !any;
    });
  }
  if (search) search.addEventListener("input", applyFilters);
  selects.forEach(function(s){ s.addEventListener("change", applyFilters); });

  var themeBtn = document.querySelector(".theme");
  function applyTheme(v){
    if (v === "dark" || v === "light") root.setAttribute("data-theme", v);
    else root.removeAttribute("data-theme");
  }
  try { applyTheme(localStorage.getItem("bench-runbook-theme")); } catch (e) {}
  if (themeBtn) themeBtn.addEventListener("click", function(){
    var cur = root.getAttribute("data-theme");
    if (!cur) cur = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    var next = cur === "dark" ? "light" : "dark";
    applyTheme(next);
    try { localStorage.setItem("bench-runbook-theme", next); } catch (e) {}
  });

  var tip = document.querySelector(".tooltip");
  function place(ev, rect){
    var x = ev && ev.clientX != null ? ev.clientX : rect.left + rect.width / 2;
    var y = ev && ev.clientY != null ? ev.clientY : rect.top;
    var w = tip.offsetWidth, h = tip.offsetHeight;
    var left = x + 14, top = y + 14;
    if (left + w > window.innerWidth - 8) left = x - w - 14;
    if (top + h > window.innerHeight - 8) top = y - h - 14;
    tip.style.left = Math.max(8, left) + "px";
    tip.style.top = Math.max(8, top) + "px";
  }
  function open(hit, ev){
    var data;
    try { data = JSON.parse(hit.getAttribute("data-tip")); } catch (e) { return; }
    tip.textContent = "";
    var t = document.createElement("div"); t.className = "tt"; t.textContent = data.t; tip.appendChild(t);
    (data.r || []).forEach(function(r){
      var row = document.createElement("div"); row.className = "row";
      var key = document.createElement("i"); key.className = "key s" + r[2];
      var val = document.createElement("b"); val.textContent = r[0];
      var lab = document.createElement("span"); lab.textContent = r[1];
      row.appendChild(key); row.appendChild(val); row.appendChild(lab); tip.appendChild(row);
    });
    tip.hidden = false;
    var svg = hit.ownerSVGElement, cross = svg.querySelector(".cross"), x = hit.getAttribute("data-x");
    cross.setAttribute("x1", x); cross.setAttribute("x2", x); cross.style.visibility = "visible";
    place(ev, hit.getBoundingClientRect());
  }
  function close(hit){
    tip.hidden = true;
    var cross = hit.ownerSVGElement.querySelector(".cross");
    if (cross) cross.style.visibility = "hidden";
  }
  document.querySelectorAll(".chart .hit").forEach(function(hit){
    hit.setAttribute("tabindex", "0");
    hit.addEventListener("pointermove", function(ev){ open(hit, ev); });
    hit.addEventListener("pointerleave", function(){ close(hit); });
    hit.addEventListener("focus", function(){ open(hit, null); });
    hit.addEventListener("blur", function(){ close(hit); });
  });
})();
</script>
</body>
</html>
`
