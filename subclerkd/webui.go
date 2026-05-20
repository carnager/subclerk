package main

const webUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Subclerk</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#f3f4f6;--bg2:#ffffff;--bg3:#e5e7eb;
  --fg:#1f2937;--fg2:#6b7280;--fg3:#9ca3af;
  --accent:#3b82f6;--accent2:#2563eb;
  --danger:#ef4444;--border:#e5e7eb;
  --playing-bg:rgba(59,130,246,0.08);
  --hover-bg:rgba(0,0,0,0.03);
  --search-bg:rgba(0,0,0,0.5);
}
.dark{
  --bg:#111827;--bg2:#1f2937;--bg3:#374151;
  --fg:#f3f4f6;--fg2:#9ca3af;--fg3:#6b7280;
  --accent:#3b82f6;--accent2:#60a5fa;
  --danger:#f87171;--border:#374151;
  --playing-bg:rgba(59,130,246,0.15);
  --hover-bg:rgba(255,255,255,0.03);
  --search-bg:rgba(0,0,0,0.7);
}
html,body{height:100%;font-family:system-ui,-apple-system,sans-serif;background:var(--bg);color:var(--fg)}
body{display:flex;flex-direction:column;overflow:hidden}

/* HEADER */
#header{display:flex;align-items:center;gap:12px;padding:8px 16px;background:var(--bg2);border-bottom:1px solid var(--border);flex-shrink:0}
#header h1{font-size:1.1rem;font-weight:700;white-space:nowrap}
#searchBtn{cursor:pointer;padding:6px 14px;border-radius:6px;background:var(--bg3);border:1px solid var(--border);color:var(--fg2);font-size:.85rem;flex-grow:1;max-width:400px;text-align:left}
#searchBtn:hover{border-color:var(--accent)}
.hdr-btn{background:none;border:none;cursor:pointer;color:var(--fg2);font-size:1.1rem;padding:4px 8px;border-radius:4px}
.hdr-btn:hover{background:var(--bg3)}

/* MAIN */
#main{display:flex;flex:1;overflow:hidden}

/* LIBRARY */
#library{width:320px;min-width:240px;max-width:400px;display:flex;flex-direction:column;border-right:1px solid var(--border);background:var(--bg2);overflow:hidden}
#libView{flex:1;overflow-y:auto}
.lib-back{display:flex;align-items:center;gap:6px;padding:8px 12px;font-size:.8rem;color:var(--accent);cursor:pointer;border-bottom:1px solid var(--border);font-weight:500}
.lib-back:hover{background:var(--hover-bg)}
.lib-heading{padding:8px 12px;font-size:.7rem;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--fg3);background:var(--bg);border-bottom:1px solid var(--border)}
.lib-row{padding:7px 12px;font-size:.85rem;cursor:pointer;display:flex;align-items:center;gap:8px;border-bottom:1px solid var(--border)}
.lib-row:hover{background:var(--hover-bg)}
.lib-row .name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lib-row .meta{color:var(--fg3);font-size:.75rem;flex-shrink:0}
.lib-row .acts{display:none;gap:3px;flex-shrink:0}
.lib-row:hover .acts{display:flex}
.act{background:none;border:1px solid var(--border);cursor:pointer;color:var(--fg2);font-size:.65rem;padding:2px 6px;border-radius:3px}
.act:hover{background:var(--accent);color:#fff;border-color:var(--accent)}

/* QUEUE */
#queue{flex:1;display:flex;flex-direction:column;overflow:hidden;background:var(--bg2)}
#queueHdr{padding:8px 16px;font-size:.7rem;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--fg3);background:var(--bg);border-bottom:1px solid var(--border);display:flex;align-items:center;justify-content:space-between}
#queueHdr button{font-size:.7rem;padding:3px 8px;border-radius:4px;background:var(--bg3);border:1px solid var(--border);color:var(--fg2);cursor:pointer}
#queueHdr button:hover{border-color:var(--danger);color:var(--danger)}
#queueList{flex:1;overflow-y:auto}
.qi{display:flex;align-items:center;padding:6px 16px;font-size:.85rem;gap:8px;border-bottom:1px solid var(--border);cursor:grab;user-select:none}
.qi:hover{background:var(--hover-bg)}
.qi.playing{background:var(--playing-bg)}
.qi.drag-over{border-top:2px solid var(--accent)}
.qi .num{color:var(--fg3);font-size:.75rem;width:24px;text-align:right;flex-shrink:0}
.qi .col{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.qi .c-artist{flex:3;min-width:0}
.qi .c-title{flex:4;min-width:0;font-weight:500}
.qi .c-album{flex:3;min-width:0;color:var(--fg2)}
.qi .c-dur{flex-shrink:0;width:44px;text-align:right;color:var(--fg3);font-size:.75rem}
.qi .rm{background:none;border:none;cursor:pointer;color:var(--fg3);padding:2px 6px;font-size:.9rem;border-radius:3px;flex-shrink:0;opacity:0;transition:opacity .15s}
.qi:hover .rm{opacity:1}
.qi .rm:hover{color:var(--danger);background:var(--bg3)}
#queueEmpty{padding:40px;text-align:center;color:var(--fg3)}

/* PLAYER */
#player{background:var(--bg2);border-top:1px solid var(--border);padding:8px 16px;flex-shrink:0}
#playerInfo{display:flex;align-items:center;gap:16px;margin-bottom:6px}
#npText{flex:1;min-width:0}
#npText .np-t{font-weight:600;font-size:.9rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
#npText .np-s{font-size:.8rem;color:var(--fg2);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
#controls{display:flex;align-items:center;gap:8px}
#controls button{background:none;border:none;cursor:pointer;font-size:1.2rem;color:var(--fg);padding:4px 8px;border-radius:4px}
#controls button:hover{background:var(--bg3)}
.play-btn{background:var(--accent)!important;color:#fff!important;border-radius:50%!important;width:36px;height:36px;display:flex;align-items:center;justify-content:center;font-size:1rem!important}
.play-btn:hover{background:var(--accent2)!important}
#seekRow{display:flex;align-items:center;gap:10px}
.tl{font-size:.75rem;color:var(--fg3);width:40px;font-variant-numeric:tabular-nums}
.tl.r{text-align:right}
#seekWrap{flex:1;height:24px;display:flex;align-items:center;cursor:pointer;position:relative}
#seekTrack{width:100%;height:4px;border-radius:2px;background:var(--bg3);position:relative;overflow:visible;transition:height .15s}
#seekWrap:hover #seekTrack{height:6px}
#seekFill{height:100%;border-radius:2px;background:var(--accent);position:absolute;left:0;top:0;pointer-events:none}
#seekThumb{width:0;height:0;border-radius:50%;background:var(--accent);position:absolute;top:50%;transform:translate(-50%,-50%);pointer-events:none;transition:width .15s,height .15s,box-shadow .15s}
#seekWrap:hover #seekThumb{width:14px;height:14px;box-shadow:0 0 0 4px rgba(59,130,246,0.2)}

/* SEARCH */
#searchOvl{display:none;position:fixed;inset:0;z-index:100;background:var(--search-bg);align-items:flex-start;justify-content:center;padding-top:80px}
#searchOvl.open{display:flex}
#searchBox{background:var(--bg2);border-radius:12px;width:90%;max-width:600px;max-height:70vh;display:flex;flex-direction:column;overflow:hidden;box-shadow:0 20px 60px rgba(0,0,0,0.3)}
#searchInp{border:none;outline:none;padding:16px 20px;font-size:1rem;background:transparent;color:var(--fg);border-bottom:1px solid var(--border)}
#searchRes{overflow-y:auto;flex:1}
.sr-sec{padding:8px 16px 4px;font-size:.7rem;font-weight:600;text-transform:uppercase;letter-spacing:.05em;color:var(--fg3)}
.sr-row{padding:8px 16px;font-size:.85rem;cursor:pointer;display:flex;align-items:center;gap:8px}
.sr-row:hover,.sr-row.sel{background:var(--hover-bg)}
.sr-row.sel{background:var(--playing-bg);outline:2px solid var(--accent);outline-offset:-2px}
.sr-row .sr-m{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sr-row .sr-sub{color:var(--fg2);font-size:.75rem}
.sr-row .sr-acts{display:none;gap:2px;flex-shrink:0}
.sr-row:hover .sr-acts,.sr-row.sel .sr-acts{display:flex}
.sr-row .sr-acts button{background:var(--bg3);border:1px solid var(--border);cursor:pointer;color:var(--fg2);font-size:.7rem;padding:2px 6px;border-radius:3px}
.sr-row .sr-acts button:hover{background:var(--accent);color:#fff;border-color:var(--accent)}
#searchNone{padding:30px;text-align:center;color:var(--fg3);display:none}

/* RATING OVERLAY */
#rateOvl{display:none;position:fixed;inset:0;z-index:100;background:var(--search-bg);align-items:center;justify-content:center}
#rateOvl.open{display:flex}
#rateBox{background:var(--bg2);border-radius:12px;padding:20px 24px;min-width:240px;box-shadow:0 20px 60px rgba(0,0,0,0.3)}
#rateBox h3{font-size:.85rem;font-weight:600;margin-bottom:4px}
#rateBox .rate-sub{font-size:.75rem;color:var(--fg2);margin-bottom:12px}
.rate-grid{display:flex;flex-wrap:wrap;gap:6px}
.rate-btn{width:36px;height:36px;border-radius:6px;border:1px solid var(--border);background:var(--bg3);color:var(--fg);font-size:.85rem;font-weight:600;cursor:pointer;display:flex;align-items:center;justify-content:center}
.rate-btn:hover{background:var(--accent);color:#fff;border-color:var(--accent)}
.rate-btn.active{background:var(--accent);color:#fff;border-color:var(--accent)}
.rate-btn.del{width:auto;padding:0 10px;font-size:.75rem;color:var(--danger);border-color:var(--danger)}
.rate-btn.del:hover{background:var(--danger);color:#fff}
.rate-cur{font-size:.75rem;color:var(--fg2);margin-top:10px}
#rateHints{font-size:.7rem;color:var(--fg3);margin-top:8px}

/* SCROLLBARS */
::-webkit-scrollbar{width:8px;height:8px}
::-webkit-scrollbar-track{background:var(--bg)}
::-webkit-scrollbar-thumb{background:var(--bg3);border-radius:4px}
::-webkit-scrollbar-thumb:hover{background:var(--fg3)}
*{scrollbar-width:thin;scrollbar-color:var(--bg3) var(--bg)}

</style>
</head>
<body>

<div id="header">
  <h1>Subclerk</h1>
  <div id="searchBtn">Search... <span style="float:right;opacity:.5">Ctrl+K</span></div>
  <button class="hdr-btn" id="scrobbleBtn" title="Toggle Last.fm scrobbling">&#9835;</button>
  <button class="hdr-btn" id="btnRandom" title="Random album">&#127922;</button>
  <button class="hdr-btn" id="btnRefresh" title="Refresh cache">&#8635;</button>
  <button class="hdr-btn" id="darkBtn">&#9788;</button>
</div>

<div id="main">
  <div id="library">
    <div id="libView"></div>
  </div>
  <div id="queue">
    <div id="queueHdr">
      <span>Queue <span id="qCnt"></span></span>
      <button id="btnClear">Clear</button>
    </div>
    <div id="queueList"></div>
    <div id="queueEmpty">Queue is empty. Browse or search to add music.</div>
  </div>
</div>

<div id="player">
  <div id="playerInfo">
    <div id="npText">
      <div class="np-t" id="npTitle">&#8212;</div>
      <div class="np-s" id="npSub"></div>
    </div>
    <div id="controls">
      <button id="btnPrev">&#9198;</button>
      <button class="play-btn" id="btnPlay">&#9654;</button>
      <button id="btnNext">&#9197;</button>
      <button id="btnRateTrack" title="Rate track" style="font-size:.8rem">&#9733;T</button>
      <button id="btnRateAlbum" title="Rate album" style="font-size:.8rem">&#9733;A</button>
    </div>
  </div>
  <div id="seekRow">
    <span class="tl" id="timePos">0:00</span>
    <div id="seekWrap"><div id="seekTrack"><div id="seekFill"></div></div><div id="seekThumb"></div></div>
    <span class="tl r" id="timeDur">0:00</span>
  </div>
</div>

<div id="rateOvl">
  <div id="rateBox">
    <h3 id="rateTitle">Rate</h3>
    <div class="rate-sub" id="rateSub"></div>
    <div class="rate-grid" id="rateGrid"></div>
    <div class="rate-cur" id="rateCur"></div>
    <div id="rateHints">[1-0] rate [Delete] remove [Esc] close</div>
  </div>
</div>

<div id="searchOvl">
  <div id="searchBox">
    <input id="searchInp" type="text" placeholder="Search albums and tracks..." autocomplete="off">
    <div id="searchRes"></div>
    <div id="searchNone">No results</div>
  </div>
</div>

<script>
(function(){
"use strict";

var B = window.location.origin + '/api/v1';
var playState = 'stopped';
var seeking = false;
var currentDur = 0;
var scrobbleOn = false;

// --- state for library ---
var libArtists = [];
var libAlbums = [];
var libTracks = [];
var libMode = 'artists'; // 'artists' | 'albums' | 'tracks'
var libCurArtist = '';
var libCurAlbumId = '';

// --- state for queue drag ---
var dragIdx = -1;

// ========== HELPERS ==========
function esc(s) {
  if (!s) return '';
  var d = document.createElement('div');
  d.appendChild(document.createTextNode(String(s)));
  return d.innerHTML;
}

function fmt(s) {
  if (!s || s < 0) return '0:00';
  var m = Math.floor(s/60), sec = Math.floor(s%60);
  return m+':'+(sec<10?'0':'')+sec;
}

function $(id) { return document.getElementById(id); }

function api(method, path, body) {
  var o = { method: method };
  if (body) { o.headers = {'Content-Type':'application/json'}; o.body = JSON.stringify(body); }
  return fetch(B+'/'+path, o).then(function(r){return r.json();}).catch(function(){return {};});
}

// ========== LIBRARY ==========
function renderLib() {
  var v = $('libView');
  var html = '';
  if (libMode === 'artists') {
    html += '<div class="lib-heading">Artists ('+libArtists.length+')</div>';
    for (var i=0; i<libArtists.length; i++) {
      html += '<div class="lib-row" data-type="artist" data-i="'+i+'">' +
        '<span class="name">'+esc(libArtists[i])+'</span>' +
        '<div class="acts">' +
          '<button class="act" data-do="artist-play" data-i="'+i+'">&#9654;</button>' +
          '<button class="act" data-do="artist-add" data-i="'+i+'">+</button>' +
        '</div></div>';
    }
  } else if (libMode === 'albums') {
    html += '<div class="lib-back" data-type="back-artists">&#8592; Artists</div>';
    html += '<div class="lib-heading">'+esc(libCurArtist)+' ('+libAlbums.length+' albums)</div>';
    for (var i=0; i<libAlbums.length; i++) {
      var a = libAlbums[i];
      html += '<div class="lib-row" data-type="album" data-i="'+i+'">' +
        '<span class="meta">'+(esc(a.date)||'')+'</span>' +
        '<span class="name">'+esc(a.album)+'</span>' +
        '<div class="acts">' +
          '<button class="act" data-do="album-play" data-i="'+i+'">&#9654;</button>' +
          '<button class="act" data-do="album-ins" data-i="'+i+'">&#10549;</button>' +
          '<button class="act" data-do="album-add" data-i="'+i+'">+</button>' +
        '</div></div>';
    }
  } else if (libMode === 'tracks') {
    html += '<div class="lib-back" data-type="back-albums">&#8592; '+esc(libCurArtist)+'</div>';
    var albumName = libTracks.length ? libTracks[0].album : '';
    html += '<div class="lib-heading">'+esc(albumName)+'</div>';
    for (var i=0; i<libTracks.length; i++) {
      var t = libTracks[i];
      html += '<div class="lib-row" data-type="track" data-i="'+i+'">' +
        '<span class="meta" style="width:20px;text-align:right">'+(t.tracknumber||'')+'</span>' +
        '<span class="name">'+esc(t.title)+'</span>' +
        '<div class="acts">' +
          '<button class="act" data-do="track-play" data-i="'+i+'">&#9654;</button>' +
          '<button class="act" data-do="track-ins" data-i="'+i+'">&#10549;</button>' +
          '<button class="act" data-do="track-add" data-i="'+i+'">+</button>' +
        '</div></div>';
    }
  }
  v.innerHTML = html;
}

// Event delegation on the library view
$('libView').addEventListener('click', function(e) {
  // Check if an action button was clicked
  var actBtn = e.target.closest('[data-do]');
  if (actBtn) {
    e.stopPropagation();
    var action = actBtn.getAttribute('data-do');
    var idx = parseInt(actBtn.getAttribute('data-i'), 10);
    if (action === 'artist-play') { doArtistPlay(idx); return; }
    if (action === 'artist-add') { doArtistAdd(idx); return; }
    if (action === 'album-play') { addAlbum(libAlbums[idx].id, 'replace'); return; }
    if (action === 'album-ins') { addAlbum(libAlbums[idx].id, 'insert'); return; }
    if (action === 'album-add') { addAlbum(libAlbums[idx].id, 'add'); return; }
    if (action === 'track-play') { addTrack(libTracks[idx].id, 'replace'); return; }
    if (action === 'track-ins') { addTrack(libTracks[idx].id, 'insert'); return; }
    if (action === 'track-add') { addTrack(libTracks[idx].id, 'add'); return; }
    return;
  }

  // Check row or back button click
  var row = e.target.closest('[data-type]');
  if (!row) return;
  var type = row.getAttribute('data-type');
  var idx = parseInt(row.getAttribute('data-i'), 10);

  if (type === 'artist') {
    var name = libArtists[idx];
    if (!name) return;
    libCurArtist = name;
    api('GET','browse/albums?artist='+encodeURIComponent(name)).then(function(data) {
      libAlbums = Array.isArray(data) ? data : [];
      libMode = 'albums';
      renderLib();
    });
  } else if (type === 'album') {
    var album = libAlbums[idx];
    if (!album) return;
    libCurAlbumId = album.id;
    api('GET','browse/tracks?album_id='+album.id).then(function(data) {
      libTracks = Array.isArray(data) ? data : [];
      libMode = 'tracks';
      renderLib();
    });
  } else if (type === 'track') {
    addTrack(libTracks[idx].id, 'add');
  } else if (type === 'back-artists') {
    libMode = 'artists';
    renderLib();
  } else if (type === 'back-albums') {
    libMode = 'albums';
    renderLib();
  }
});

function doArtistPlay(idx) {
  var name = libArtists[idx];
  api('GET','browse/albums?artist='+encodeURIComponent(name)).then(function(albums) {
    if (!Array.isArray(albums) || !albums.length) return;
    var ids = albums.map(function(a){return a.id;});
    api('POST','playlist/add/albums',{album_ids:ids,mode:'replace'}).then(function(){setTimeout(refresh,300);});
  });
}
function doArtistAdd(idx) {
  var name = libArtists[idx];
  api('GET','browse/albums?artist='+encodeURIComponent(name)).then(function(albums) {
    if (!Array.isArray(albums) || !albums.length) return;
    var ids = albums.map(function(a){return a.id;});
    api('POST','playlist/add/albums',{album_ids:ids,mode:'add'}).then(function(){setTimeout(refresh,300);});
  });
}

function addAlbum(id, mode) {
  api('POST','playlist/add/album/'+id,{mode:mode}).then(function(){setTimeout(refresh,300);});
}
function addTrack(id, mode) {
  api('POST','playlist/add/track/'+id,{mode:mode}).then(function(){setTimeout(refresh,300);});
}

function loadArtists() {
  api('GET','browse/artists').then(function(data) {
    libArtists = Array.isArray(data) ? data : [];
    libMode = 'artists';
    renderLib();
  });
}

// ========== QUEUE (with drag & drop) ==========
var queueData = [];

function renderQueue(q) {
  queueData = Array.isArray(q) ? q : [];
  var list = $('queueList');
  var empty = $('queueEmpty');
  $('qCnt').textContent = queueData.length ? '('+queueData.length+')' : '';
  if (!queueData.length) { list.innerHTML=''; empty.style.display=''; return; }
  empty.style.display='none';
  var html = '';
  for (var i=0; i<queueData.length; i++) {
    var e = queueData[i];
    var cls = e.current ? 'qi playing' : 'qi';
    var dur = e.duration ? Math.floor(e.duration/60)+':'+('0'+Math.floor(e.duration%60)).slice(-2) : '';
    html += '<div class="'+cls+'" draggable="true" data-pos="'+e.position+'">' +
      '<span class="num">'+(e.position+1)+'</span>' +
      '<span class="col c-artist">'+esc(e.artist||'')+'</span>' +
      '<span class="col c-title">'+esc(e.title||'Unknown')+'</span>' +
      '<span class="col c-album">'+esc(e.album||'')+'</span>' +
      '<span class="c-dur">'+dur+'</span>' +
      '<button class="rm" data-rm="'+e.position+'" title="Remove">\u00d7</button>' +
      '</div>';
  }
  list.innerHTML = html;
}

// Queue: remove button
$('queueList').addEventListener('click', function(e) {
  var rm = e.target.closest('[data-rm]');
  if (rm) {
    e.stopPropagation();
    var pos = rm.getAttribute('data-rm');
    api('DELETE','playback/queue/'+pos).then(function(){setTimeout(refresh,200);});
  }
});

// Queue: drag & drop
$('queueList').addEventListener('dragstart', function(e) {
  var row = e.target.closest('[data-pos]');
  if (!row) return;
  dragIdx = parseInt(row.getAttribute('data-pos'), 10);
  e.dataTransfer.effectAllowed = 'move';
  row.style.opacity = '0.4';
});

$('queueList').addEventListener('dragend', function(e) {
  dragIdx = -1;
  var row = e.target.closest('[data-pos]');
  if (row) row.style.opacity = '';
  // Clear all drag-over highlights
  var items = $('queueList').querySelectorAll('.drag-over');
  for (var i=0; i<items.length; i++) items[i].classList.remove('drag-over');
});

$('queueList').addEventListener('dragover', function(e) {
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  var row = e.target.closest('[data-pos]');
  // Clear previous highlights
  var items = $('queueList').querySelectorAll('.drag-over');
  for (var i=0; i<items.length; i++) items[i].classList.remove('drag-over');
  if (row) row.classList.add('drag-over');
});

$('queueList').addEventListener('dragleave', function(e) {
  var row = e.target.closest('[data-pos]');
  if (row) row.classList.remove('drag-over');
});

$('queueList').addEventListener('drop', function(e) {
  e.preventDefault();
  var row = e.target.closest('[data-pos]');
  if (!row || dragIdx < 0) return;
  var toIdx = parseInt(row.getAttribute('data-pos'), 10);
  if (dragIdx === toIdx) return;
  api('POST','playback/queue/move',{from:dragIdx,to:toIdx}).then(function(){
    dragIdx = -1;
    setTimeout(refresh,200);
  });
});

// ========== PLAYER (smooth seekbar interpolation) ==========
var knownPos = 0;       // last position from server
var knownPosAt = 0;     // performance.now() when we got it
var animFrame = 0;
var seekLockUntil = 0;  // ignore server pos updates until this time

function interpolatedPos() {
  if (playState !== 'playing' || currentDur <= 0) return knownPos;
  var elapsed = (performance.now() - knownPosAt) / 1000;
  var p = knownPos + elapsed;
  return p > currentDur ? currentDur : p;
}

function setSeekUI(pos) {
  var pct = currentDur > 0 ? (pos / currentDur) * 100 : 0;
  if (pct > 100) pct = 100;
  $('seekFill').style.width = pct + '%';
  $('seekThumb').style.left = pct + '%';
  $('timePos').textContent = fmt(pos);
}

function tickSeek() {
  if (!seeking) setSeekUI(interpolatedPos());
  animFrame = requestAnimationFrame(tickSeek);
}

function refresh() {
  Promise.all([api('GET','playback/status'), api('GET','playback/queue')]).then(function(res) {
    var st = res[0], q = res[1];
    playState = st.state || 'stopped';
    $('btnPlay').innerHTML = playState==='playing' ? '&#9208;' : '&#9654;';
    $('npTitle').textContent = st.title || '\u2014';
    $('npSub').textContent = [st.artist, st.album, st.date].filter(Boolean).join(' \u2014 ');
    var pos = st.time_pos||0;
    currentDur = st.duration||0;
    $('timeDur').textContent = fmt(currentDur);
    if (performance.now() < seekLockUntil) {
      // ignore server position during seek grace period
    } else if (!seeking) {
      knownPos = pos;
      knownPosAt = performance.now();
      setSeekUI(pos);
    }
    renderQueue(Array.isArray(q) ? q : []);
  }).catch(function(){});
}

$('btnPlay').addEventListener('click', function() {
  api('POST', playState==='playing' ? 'playback/pause' : 'playback/play').then(function(){setTimeout(refresh,200);});
});
$('btnPrev').addEventListener('click', function() {
  api('POST','playback/prev').then(function(){setTimeout(refresh,300);});
});
$('btnNext').addEventListener('click', function() {
  api('POST','playback/next').then(function(){setTimeout(refresh,300);});
});
$('btnClear').addEventListener('click', function() {
  api('POST','playback/stop').then(function(){setTimeout(refresh,300);});
});

// Seekbar interaction (custom div-based)
function seekFromEvent(e) {
  var rect = $('seekWrap').getBoundingClientRect();
  var pct = (e.clientX - rect.left) / rect.width;
  if (pct < 0) pct = 0;
  if (pct > 1) pct = 1;
  return pct * currentDur;
}

$('seekWrap').addEventListener('mousedown', function(e) {
  if (currentDur <= 0) return;
  seeking = true;
  var pos = seekFromEvent(e);
  knownPos = pos;
  knownPosAt = performance.now();
  setSeekUI(pos);

  function onMove(ev) {
    var p = seekFromEvent(ev);
    knownPos = p;
    knownPosAt = performance.now();
    setSeekUI(p);
  }
  function onUp(ev) {
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
    var finalPos = seekFromEvent(ev);
    knownPos = finalPos;
    knownPosAt = performance.now();
    seekLockUntil = performance.now() + 1500;
    seeking = false;
    api('POST','playback/seek',{position:finalPos}).then(function(){setTimeout(refresh,300);});
  }
  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
});

// ========== SEARCH (keyboard navigable) ==========
var searchTO = null;
var srSel = -1; // selected index in search results

function openSearch() {
  $('searchOvl').classList.add('open');
  $('searchInp').value = '';
  $('searchInp').focus();
  $('searchRes').innerHTML = '';
  $('searchNone').style.display = 'none';
  srSel = -1;
}
function closeSearch() {
  $('searchOvl').classList.remove('open');
  srSel = -1;
}
function searchIsOpen() {
  return $('searchOvl').classList.contains('open');
}

function srRows() {
  return $('searchRes').querySelectorAll('.sr-row');
}

function srHighlight(idx) {
  var rows = srRows();
  for (var i=0; i<rows.length; i++) rows[i].classList.remove('sel');
  srSel = idx;
  if (idx >= 0 && idx < rows.length) {
    rows[idx].classList.add('sel');
    rows[idx].scrollIntoView({block:'nearest'});
  }
}

function srAction(mode) {
  var rows = srRows();
  if (srSel < 0 || srSel >= rows.length) return;
  var row = rows[srSel];
  var type = row.getAttribute('data-sr-type');
  var id = row.getAttribute('data-sr-id');
  if (type === 'album') addAlbum(id, mode);
  else if (type === 'track') addTrack(id, mode);
  closeSearch();
}

$('searchBtn').addEventListener('click', openSearch);
$('searchOvl').addEventListener('click', function(e) {
  if (e.target === $('searchOvl')) closeSearch();
});

$('searchInp').addEventListener('input', function() {
  clearTimeout(searchTO);
  var q = this.value.trim();
  if (!q) { $('searchRes').innerHTML=''; $('searchNone').style.display='none'; srSel=-1; return; }
  searchTO = setTimeout(function(){ doSearch(q); }, 150);
});

$('searchInp').addEventListener('keydown', function(e) {
  var rows = srRows();
  var len = rows.length;
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    srHighlight(srSel < len-1 ? srSel+1 : 0);
  } else if (e.key === 'ArrowUp') {
    e.preventDefault();
    srHighlight(srSel > 0 ? srSel-1 : len-1);
  } else if (e.key === 'Enter') {
    e.preventDefault();
    if (e.shiftKey) srAction('replace');
    else srAction('add');
  } else if (e.key === 'Tab' && !e.shiftKey) {
    e.preventDefault();
    srAction('insert');
  }
});

function doSearch(q) {
  api('GET','search?q='+encodeURIComponent(q)).then(function(r) {
    var albums = r.albums||[], tracks = r.tracks||[];
    if (!albums.length && !tracks.length) { $('searchRes').innerHTML=''; $('searchNone').style.display=''; srSel=-1; return; }
    $('searchNone').style.display='none';
    var html = '';
    if (albums.length) {
      html += '<div class="sr-sec">Albums ('+albums.length+')</div>';
      for (var i=0; i<albums.length; i++) {
        var a = albums[i];
        html += '<div class="sr-row" data-sr-type="album" data-sr-id="'+esc(a.id)+'">'+
          '<div class="sr-m"><strong>'+esc(a.albumartist)+'</strong> \u2014 '+esc(a.album)+' <span class="sr-sub">'+esc(a.date)+'</span></div>'+
          '<div class="sr-acts">'+
            '<button data-sr-do="play">&#9654;</button>'+
            '<button data-sr-do="ins">&#10549;</button>'+
            '<button data-sr-do="add">+</button>'+
          '</div></div>';
      }
    }
    if (tracks.length) {
      html += '<div class="sr-sec">Tracks ('+tracks.length+')</div>';
      for (var i=0; i<tracks.length; i++) {
        var t = tracks[i];
        html += '<div class="sr-row" data-sr-type="track" data-sr-id="'+esc(t.id)+'">'+
          '<div class="sr-m">'+esc(t.title)+' <span class="sr-sub">'+esc([t.artist,t.album].filter(Boolean).join(' \u2014 '))+'</span></div>'+
          '<div class="sr-acts">'+
            '<button data-sr-do="play">&#9654;</button>'+
            '<button data-sr-do="ins">&#10549;</button>'+
            '<button data-sr-do="add">+</button>'+
          '</div></div>';
      }
    }
    $('searchRes').innerHTML = html;
    srSel = -1;
  });
}

$('searchRes').addEventListener('click', function(e) {
  var actBtn = e.target.closest('[data-sr-do]');
  var row = e.target.closest('[data-sr-type]');
  if (!row) return;
  var type = row.getAttribute('data-sr-type');
  var id = row.getAttribute('data-sr-id');
  var mode = 'add';
  if (actBtn) {
    var d = actBtn.getAttribute('data-sr-do');
    if (d === 'play') mode = 'replace';
    else if (d === 'ins') mode = 'insert';
  }
  if (type === 'album') addAlbum(id, mode);
  else if (type === 'track') addTrack(id, mode);
  closeSearch();
});

// ========== SCROBBLE ==========
$('scrobbleBtn').addEventListener('click', function() {
  api('POST','scrobble/toggle').then(function(r) {
    scrobbleOn = r.enabled||false;
    updateScrobbleUI();
  });
});
function updateScrobbleUI() {
  $('scrobbleBtn').style.color = scrobbleOn ? 'var(--accent)' : 'var(--fg3)';
  $('scrobbleBtn').title = scrobbleOn ? 'Scrobbling: ON' : 'Scrobbling: OFF';
}
api('GET','scrobble/status').then(function(r) { scrobbleOn = r.enabled||false; updateScrobbleUI(); });

// ========== RATING ==========
var rateMode = ''; // 'track' or 'album'
var rateSel = -1;
var rateValues = ['1','2','3','4','5','6','7','8','9','10','Delete'];

function openRating(mode) {
  rateMode = mode;
  rateSel = -1;
  $('rateOvl').classList.add('open');
  var endpoint = mode === 'track' ? 'current_track/rating' : 'current_album/rating';
  api('GET', endpoint).then(function(r) {
    var label = mode === 'track' ? (r.title||'') : (r.album||'');
    var sub = mode === 'track' ? [r.artist,r.album].filter(Boolean).join(' \u2014 ') : [r.albumartist,r.date].filter(Boolean).join(' \u2014 ');
    $('rateTitle').textContent = 'Rate ' + (mode === 'track' ? 'Track' : 'Album');
    $('rateSub').textContent = label + (sub ? ' \u2014 ' + sub : '');
    var cur = r.rating || 'none';
    $('rateCur').textContent = 'Current: ' + cur;
    renderRateGrid(cur);
  });
}

function renderRateGrid(current) {
  var html = '';
  for (var i = 0; i < rateValues.length; i++) {
    var v = rateValues[i];
    var cls = 'rate-btn';
    if (v === current) cls += ' active';
    if (v === 'Delete') cls += ' del';
    if (i === rateSel) cls += ' active';
    html += '<button class="'+cls+'" data-rate="'+v+'">'+v+'</button>';
  }
  $('rateGrid').innerHTML = html;
}

function submitRating(value) {
  var endpoint = rateMode === 'track' ? 'current_track/rating' : 'current_album/rating';
  api('POST', endpoint, {rating: value}).then(function() {
    closeRating();
  });
}

function closeRating() {
  $('rateOvl').classList.remove('open');
  rateMode = '';
  rateSel = -1;
}

$('rateOvl').addEventListener('click', function(e) {
  if (e.target === $('rateOvl')) closeRating();
  var btn = e.target.closest('[data-rate]');
  if (btn) submitRating(btn.getAttribute('data-rate'));
});

$('btnRateTrack').addEventListener('click', function() { openRating('track'); });
$('btnRateAlbum').addEventListener('click', function() { openRating('album'); });

// ========== HEADER BUTTONS ==========
$('btnRandom').addEventListener('click', function() {
  api('POST','playback/random/album').then(function(){setTimeout(refresh,300);});
});
$('btnRefresh').addEventListener('click', function() {
  api('POST','cache/update');
});
$('darkBtn').addEventListener('click', function() {
  var dark = document.documentElement.classList.toggle('dark');
  try { localStorage.theme = dark?'dark':'light'; } catch(x) {}
  $('darkBtn').innerHTML = dark ? '&#9790;' : '&#9788;';
});

// Init dark mode
try {
  if (localStorage.theme==='dark'||(!localStorage.theme&&window.matchMedia('(prefers-color-scheme:dark)').matches))
    document.documentElement.classList.add('dark');
} catch(x) {}
$('darkBtn').innerHTML = document.documentElement.classList.contains('dark') ? '&#9790;' : '&#9788;';

// ========== KEYBOARD ==========
// Global hotkeys (active when search is closed and no input focused):
//   /  or Ctrl+K  - open search
//   Space         - toggle play/pause
//   >             - next track
//   <             - previous track
//   s             - stop
//   r             - random album
//   d             - toggle dark mode
//
// Search hotkeys:
//   ArrowUp/Down  - navigate results
//   Enter         - add selected to queue
//   Shift+Enter   - replace queue with selected
//   Tab           - insert after current
//   Escape        - close search

document.addEventListener('keydown', function(e) {
  var tag = document.activeElement.tagName;
  var inInput = (tag === 'INPUT' || tag === 'TEXTAREA');
  var sOpen = searchIsOpen();
  var rOpen = $('rateOvl').classList.contains('open');

  // Rating overlay keys
  if (rOpen) {
    if (e.key === 'Escape') { closeRating(); return; }
    if (e.key >= '1' && e.key <= '9') { submitRating(e.key); return; }
    if (e.key === '0') { submitRating('10'); return; }
    if (e.key === 'Delete' || e.key === 'Backspace') { submitRating('Delete'); return; }
    return;
  }

  // Always: Ctrl+K opens search
  if (e.key==='k' && (e.ctrlKey||e.metaKey)) { e.preventDefault(); openSearch(); return; }
  // Always: Escape closes search
  if (e.key==='Escape' && sOpen) { e.preventDefault(); closeSearch(); return; }

  // If search is open, the searchInp keydown handler takes care of navigation
  if (sOpen) return;
  // If focused on some other input, don't intercept
  if (inInput) return;

  if (e.key === '/') { e.preventDefault(); openSearch(); }
  else if (e.key === ' ') { e.preventDefault(); $('btnPlay').click(); }
  else if (e.key === '>') { $('btnNext').click(); }
  else if (e.key === '<') { $('btnPrev').click(); }
  else if (e.key === 's') { api('POST','playback/stop').then(function(){setTimeout(refresh,200);}); }
  else if (e.key === 'r') { $('btnRandom').click(); }
  else if (e.key === 'd') { $('darkBtn').click(); }
  else if (e.key === 't') { openRating('track'); }
  else if (e.key === 'R') { openRating('album'); }
});

// ========== INIT ==========
loadArtists();
refresh();
setInterval(refresh, 800);
tickSeek();

})();
</script>
</body>
</html>`
