package harness

// Page chrome for the run book. Colors follow the data-viz reference palette:
// categorical slots 1-2 (blue, orange) for series, reserved status colors that
// always travel with an icon and a label, and hairline recessive grid/axes.

const runbookHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Benchmark Run Book</title>
<style>
:root{
  color-scheme:light;
  --page:#f9f9f7;--surface:#fcfcfb;--raised:#ffffff;
  --ink:#0b0b0b;--ink-2:#52514e;--muted:#6f6e69;
  --grid:#e1e0d9;--axis:#c3c2b7;--ring:rgba(11,11,11,.10);
  --s1:#2a78d6;--s2:#eb6834;
  --good:#006300;--good-bg:#e7f3e7;
  --warn:#8a5a00;--warn-bg:#fdf3dd;
  --serious:#9c4318;--serious-bg:#fbeae2;
  --bad:#b42e2e;--bad-bg:#fbe7e7;
  --accent:#2a78d6;--accent-bg:#e8f1fc;
}
@media (prefers-color-scheme:dark){:root:where(:not([data-theme="light"])){
  color-scheme:dark;
  --page:#0d0d0d;--surface:#1a1a19;--raised:#20201f;
  --ink:#ffffff;--ink-2:#c3c2b7;--muted:#9a9990;
  --grid:#2c2c2a;--axis:#383835;--ring:rgba(255,255,255,.10);
  --s1:#3987e5;--s2:#d95926;
  --good:#3fbf3f;--good-bg:#132613;
  --warn:#e8b030;--warn-bg:#2b2311;
  --serious:#ec835a;--serious-bg:#2e1c14;
  --bad:#ef7070;--bad-bg:#301818;
  --accent:#6da7ec;--accent-bg:#16263a;
}}
:root[data-theme="dark"]{
  color-scheme:dark;
  --page:#0d0d0d;--surface:#1a1a19;--raised:#20201f;
  --ink:#ffffff;--ink-2:#c3c2b7;--muted:#9a9990;
  --grid:#2c2c2a;--axis:#383835;--ring:rgba(255,255,255,.10);
  --s1:#3987e5;--s2:#d95926;
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
.shell{display:grid;grid-template-columns:280px minmax(0,1fr);min-height:100vh}
.side{position:sticky;top:0;height:100vh;overflow-y:auto;border-right:1px solid var(--grid);background:var(--surface);padding:16px 12px 32px}
.navtoggle{display:none}
.brand{display:block;font-weight:650;font-size:15px;color:var(--ink);padding:4px 8px 12px}
.search{width:100%;padding:7px 10px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink);font:inherit;margin-bottom:12px}
.group{margin:10px 0 4px}
.group-h{font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;color:var(--ink-2);padding:4px 8px}
.side ul{list-style:none;margin:0;padding:0}
.side li a{display:grid;grid-template-columns:18px minmax(0,1fr) auto;gap:6px;align-items:center;padding:5px 8px;border-radius:6px;color:var(--ink)}
.side li a:hover{background:var(--accent-bg);text-decoration:none}
.side li.active a{background:var(--accent-bg);font-weight:600}
.side .nm{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.side .when{font-size:12px;color:var(--muted);font-variant-numeric:tabular-nums}
.st{display:inline-grid;place-items:center;width:16px;height:16px;border-radius:50%;font-size:10px;font-weight:700;line-height:1}
.st.completed,.badge.completed{color:var(--good);background:var(--good-bg)}
.st.excluded,.badge.excluded{color:var(--warn);background:var(--warn-bg)}
.st.failed,.badge.failed{color:var(--bad);background:var(--bad-bg)}
.st.aborted,.badge.aborted{color:var(--serious);background:var(--serious-bg)}
main{padding:28px 32px 64px;max-width:1240px;width:100%}
.page+.page{margin-top:48px;padding-top:32px;border-top:1px solid var(--grid)}
.js .page+.page{margin-top:0;padding-top:0;border-top:0}
.js .page{display:none}.js .page.shown{display:block}
.page-h h1{font-size:24px;line-height:1.25;margin:4px 0 6px;font-weight:650}
.crumbs{display:flex;justify-content:space-between;gap:12px;font-size:13px}
.pager a{margin-left:16px}
.meta{display:flex;flex-wrap:wrap;gap:6px 14px;align-items:center;color:var(--ink-2);margin:0 0 20px}
.badge{display:inline-flex;gap:4px;align-items:center;padding:2px 10px;border-radius:999px;font-size:12.5px;font-weight:600;white-space:nowrap}
h2{font-size:17px;margin:32px 0 12px;font-weight:650}
h3{font-size:13px;margin:0 0 8px;font-weight:600;text-transform:uppercase;letter-spacing:.04em;color:var(--ink-2)}
.tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(170px,100%),1fr));gap:12px;margin:8px 0 8px}
.tile{background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px}
.tile-l{font-size:12.5px;color:var(--ink-2)}
.tile-v{font-size:24px;font-weight:650;margin-top:2px;line-height:1.2}
.tile-n{font-size:12px;color:var(--muted);margin-top:4px}
.callout{border-radius:10px;padding:12px 16px;margin:0 0 16px;border:1px solid var(--ring)}
.callout ul{margin:6px 0 0 18px;padding:0}
.callout pre{white-space:pre-wrap;margin:8px 0 0;max-height:320px;overflow:auto}
.callout.failed{background:var(--bad-bg)}.callout.failed strong{color:var(--bad)}
.callout.excluded{background:var(--warn-bg)}.callout.excluded strong{color:var(--warn)}
.callout.aborted{background:var(--serious-bg)}.callout.aborted strong{color:var(--serious)}
.filters{display:flex;flex-wrap:wrap;gap:12px;margin:24px 0 12px}
.filters label{display:flex;gap:8px;align-items:center;color:var(--ink-2);font-size:13px}
.filters select{padding:6px 10px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink);font:inherit}
.scroll{overflow-x:auto;border:1px solid var(--ring);border-radius:10px;background:var(--surface)}
table{border-collapse:collapse;width:100%}
th,td{padding:8px 12px;text-align:left;border-bottom:1px solid var(--grid);white-space:nowrap;vertical-align:top}
tbody tr:last-child td{border-bottom:0}
th{font-size:12.5px;font-weight:600;color:var(--ink-2);background:var(--raised);position:sticky;top:0}
.num{text-align:right;font-variant-numeric:tabular-nums}
td.wrap{white-space:normal;min-width:240px;word-break:break-word}
td.good{color:var(--good)}td.warn{color:var(--warn);white-space:normal;min-width:220px}
table.runs tbody tr:hover{background:var(--accent-bg)}
.charts{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(380px,100%),1fr));gap:16px}
.chart{margin:0;background:var(--surface);border:1px solid var(--ring);border-radius:12px;padding:14px 16px 8px}
.chart figcaption{font-weight:600;font-size:13.5px}
.legend{display:flex;gap:16px;margin:6px 0 0;font-size:12.5px;color:var(--ink-2)}
.legend span{display:inline-flex;align-items:center;gap:6px}
.key{display:inline-block;width:14px;height:2px;border-radius:1px}
.key.s1{background:var(--s1)}.key.s2{background:var(--s2)}
.plot{position:relative}
.chart svg{display:block;width:100%;height:auto;overflow:visible}
.chart .grid{stroke:var(--grid);stroke-width:1}
.chart .base{stroke:var(--axis);stroke-width:1}
.chart .tick{fill:var(--muted);font-size:11.5px;font-variant-numeric:tabular-nums}
.chart .line{fill:none;stroke-width:2;stroke-linejoin:round;stroke-linecap:round}
.chart .line.s1{stroke:var(--s1)}.chart .line.s2{stroke:var(--s2)}
.chart .dot{stroke:var(--surface);stroke-width:2}
.chart .dot.s1{fill:var(--s1)}.chart .dot.s2{fill:var(--s2)}
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
.tooltip{position:fixed;z-index:10;pointer-events:none;background:var(--raised);color:var(--ink);border:1px solid var(--ring);box-shadow:0 6px 24px rgba(0,0,0,.14);border-radius:8px;padding:8px 10px;font-size:12.5px;min-width:150px}
.tooltip .tt{color:var(--ink-2);margin-bottom:4px}
.tooltip .row{display:grid;grid-template-columns:14px auto 1fr;gap:6px;align-items:center}
.tooltip .row b{font-variant-numeric:tabular-nums}
.tooltip .row span:last-child{color:var(--ink-2)}
@media (max-width:860px){
  .shell{grid-template-columns:minmax(0,1fr)}
  .side{position:static;height:auto;border-right:0;border-bottom:1px solid var(--grid);padding:12px 16px;display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px}
  .brand{padding:0}
  .navtoggle{display:inline-block;padding:6px 12px;border:1px solid var(--axis);border-radius:8px;background:var(--raised);color:var(--ink);font:inherit}
  .navbody{display:none;flex-basis:100%;max-height:60vh;overflow-y:auto}
  .side.open .navbody{display:block}
  .crumbs{flex-wrap:wrap}.pager a{margin-left:0;margin-right:16px}
  dl{grid-template-columns:1fr}dd{margin-bottom:6px}
  main{padding:20px 16px 48px}
  .charts{grid-template-columns:1fr}
}
@media print{
  .side,.filters,.pager,.tooltip{display:none!important}
  .shell{display:block}
  .js .page{display:block!important;break-before:page}
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
  function show(){
    var id = decodeURIComponent(location.hash.replace(/^#/, "")) || "overview";
    var target = document.getElementById(id);
    if (!target || !target.classList.contains("page")) { target = document.getElementById("overview"); id = "overview"; }
    pages.forEach(function(p){ p.classList.toggle("shown", p === target); });
    navItems.forEach(function(li){
      var on = li.getAttribute("data-run") === id;
      li.classList.toggle("active", on);
      if (on && window.innerWidth > 860 && li.scrollIntoView) { li.scrollIntoView({block: "nearest"}); }
    });
    document.title = (id === "overview" ? "Benchmark Run Book" : target.querySelector("h1").textContent + " · Run Book");
    window.scrollTo(0, 0);
  }
  window.addEventListener("hashchange", show);
  show();

  var side = document.querySelector(".side"), toggle = document.querySelector(".navtoggle");
  toggle.addEventListener("click", function(){
    var open = side.classList.toggle("open");
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
  });
  document.querySelectorAll(".side li a").forEach(function(a){
    a.addEventListener("click", function(){ side.classList.remove("open"); toggle.setAttribute("aria-expanded", "false"); });
  });

  // Overview filters + sidebar search scope rows and nav items alike.
  var search = document.querySelector(".search");
  var selects = Array.prototype.slice.call(document.querySelectorAll("select[data-filter]"));
  function applyFilters(){
    var q = (search.value || "").trim().toLowerCase();
    var want = {};
    selects.forEach(function(s){ want[s.getAttribute("data-filter")] = s.value; });
    function ok(el){
      if (want.platform && el.getAttribute("data-platform") !== want.platform) return false;
      if (want.status && el.getAttribute("data-status") !== want.status) return false;
      return true;
    }
    document.querySelectorAll("table.runs tbody tr").forEach(function(tr){
      var li = document.querySelector('.side li[data-run="' + tr.getAttribute("data-run") + '"]');
      var text = li ? li.getAttribute("data-text") : "";
      tr.hidden = !ok(tr) || (q && text.indexOf(q) < 0);
    });
    navItems.forEach(function(li){
      li.hidden = !ok(li) || (q && li.getAttribute("data-text").indexOf(q) < 0);
    });
    document.querySelectorAll(".side .group").forEach(function(g){
      g.hidden = !g.querySelector("li:not([hidden])");
    });
  }
  search.addEventListener("input", applyFilters);
  selects.forEach(function(s){ s.addEventListener("change", applyFilters); });

  // Chart tooltips: one readout for every series at the hovered x. Built with
  // textContent - labels come from result files.
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
