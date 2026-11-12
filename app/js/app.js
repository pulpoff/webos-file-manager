/* File Manager for webOS: UI. Runs in webOS 6's Chrome 79, so no ?. or ?? */
'use strict';

var BRIDGE = 'http://127.0.0.1:31231';
// A second host name gives thumbnails their own connection pool, so they
// never queue in front of folder listings or the video stream.
var THUMBS = 'http://localhost:31231';
var KEY = { LEFT: 37, UP: 38, RIGHT: 39, DOWN: 40, OK: 13, BACK: 461, ESC: 27, RED: 403, GREEN: 404, YELLOW: 405, BLUE: 406,
  PLAY: 415, PAUSE: 19, STOP: 413, REW: 412, FF: 417, CH_UP: 33, CH_DOWN: 34, INFO: 457 };

// ---------- small helpers ----------
function $(s, r) { return (r || document).querySelector(s); }
function el(tag, cls, html) { var e = document.createElement(tag); if (cls) e.className = cls; if (html != null) e.innerHTML = html; return e; }
function esc(s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
function q(o) { return Object.keys(o).filter(function (k) { return o[k] != null; }).map(function (k) { return encodeURIComponent(k) + '=' + encodeURIComponent(o[k]); }).join('&'); }
function fmtSize(n) { if (!n) return '0 B'; var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0; while (n >= 1024 && i < 4) { n /= 1024; i++; } return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i]; }
function fmtDate(t) { if (!t) return ''; var d = new Date(t * 1000); return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }); }
function fmtTime(s) { s = Math.max(0, Math.floor(s || 0)); var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60; return (h ? h + ':' + (m < 10 ? '0' : '') : '') + m + ':' + (x < 10 ? '0' : '') + x; }
function extOf(n) { var i = n.lastIndexOf('.'); return i < 0 ? '' : n.slice(i).toLowerCase(); }
function stem(n) { var i = n.lastIndexOf('.'); return i <= 0 ? n : n.slice(0, i); }
function joinPath(a, b) { return a ? a + '/' + b : b; }
function parentPath(p) { var i = p.lastIndexOf('/'); return i < 0 ? '' : p.slice(0, i); }
function baseName(p) { var i = p.lastIndexOf('/'); return i < 0 ? p : p.slice(i + 1); }
function debounce(fn, ms) { var t; return function () { var a = arguments, s = this; clearTimeout(t); t = setTimeout(function () { fn.apply(s, a); }, ms); }; }

var store = {
  get: function (k, d) { try { var v = localStorage.getItem('fm.' + k); return v == null ? d : JSON.parse(v); } catch (e) { return d; } },
  set: function (k, v) { try { localStorage.setItem('fm.' + k, JSON.stringify(v)); } catch (e) {} }
};
var settings = Object.assign({ theme: 'dark', view: 'list', sort: 'name', desc: false, hidden: false, thumbs: true, subSize: 'm', audioLang: '', subLang: '' }, store.get('settings', {}));
function saveSettings() { store.set('settings', settings); }

function api(path, body, timeout) {
  var ctrl = new AbortController();
  var t = setTimeout(function () { ctrl.abort(); }, timeout || 30000);
  // text/plain keeps POSTs "simple", so there is no CORS preflight round trip
  var opt = body ? { method: 'POST', body: JSON.stringify(body), headers: { 'Content-Type': 'text/plain' }, signal: ctrl.signal } : { signal: ctrl.signal };
  return fetch(BRIDGE + path, opt).then(function (r) {
    return r.json().catch(function () { return {}; }).then(function (d) {
      if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
      return d;
    });
  }, function (e) {
    throw new Error(e && e.name === 'AbortError' ? 'The file service did not answer in time' : 'The file service is not running');
  }).then(function (d) { clearTimeout(t); return d; }, function (e) { clearTimeout(t); throw e; });
}
function fileURL(src, path) { return BRIDGE + '/api/file?' + q({ src: src, path: path }); }
var THUMB_W = 480; // one size for lists, grid, details and Home: each file is rendered once
function thumbURL(src, path, e, w) { return THUMBS + '/api/thumb?' + q({ src: src, path: path, w: w || THUMB_W, v: (e.size || 0) + '-' + (e.mtime || 0) }); }

// ---------- file kinds ----------
var KINDS = {
  video: '.mp4 .mkv .avi .mov .m4v .ts .m2ts .mts .wmv .flv .webm .mpg .mpeg .vob .3gp',
  image: '.jpg .jpeg .png .webp .gif .bmp .heic .tif .tiff',
  audio: '.mp3 .wav .flac .aac .ogg .m4a .wma .opus .ac3 .dts .mka',
  sub: '.srt .ass .ssa .vtt .sub .idx',
  doc: '.pdf .txt .doc .docx .xls .xlsx .ppt .pptx .odt .nfo .md .log .csv .json .xml',
  archive: '.zip .rar .7z .tar .gz .bz2 .xz .tgz .iso',
  app: '.apk .ipk .exe .msi .deb'
};
var KIND_OF = {};
Object.keys(KINDS).forEach(function (k) { KINDS[k].split(' ').forEach(function (x) { KIND_OF[x] = k; }); });
function kindOf(e) { return e.dir ? 'dir' : (KIND_OF[extOf(e.name)] || 'file'); }
var ICON = { dir: '', video: '', image: '', audio: '', sub: '', doc: '', archive: '', app: '', file: '',
  usb: '', server: '', share: '', sftp: '', add: '+', pin: '', lock: '' };
var COLOR = { dir: '#4ea4ff', video: '#e5484d', image: '#a855c7', audio: '#f59e0b', sub: '#14a38b', doc: '#3b9ae1', archive: '#9a7b65', app: '#4caf50', file: '#7b8794',
  usb: '#f08c00', server: '#0f9d8f', share: '#3d8bfd', sftp: '#6366f1', add: '#64748b', pin: '#e5484d', lock: '#64748b' };
var PLAYABLE = { video: 1, audio: 1, image: 1 };
var HIDDEN = { '$recycle.bin': 1, 'system volume information': 1, 'lost+found': 1, 'found.000': 1, 'thumbs.db': 1, 'desktop.ini': 1, '.ds_store': 1 };
var LANG = { en: 'English', eng: 'English', ru: 'Russian', rus: 'Russian', es: 'Spanish', spa: 'Spanish', de: 'German', ger: 'German', deu: 'German', fr: 'French', fre: 'French', fra: 'French',
  it: 'Italian', ita: 'Italian', uk: 'Ukrainian', ukr: 'Ukrainian', ja: 'Japanese', jpn: 'Japanese', zh: 'Chinese', chi: 'Chinese', zho: 'Chinese', ko: 'Korean', kor: 'Korean',
  pt: 'Portuguese', por: 'Portuguese', pl: 'Polish', pol: 'Polish', nl: 'Dutch', dut: 'Dutch', nld: 'Dutch', sv: 'Swedish', swe: 'Swedish', fi: 'Finnish', fin: 'Finnish',
  cs: 'Czech', cze: 'Czech', ces: 'Czech', tr: 'Turkish', tur: 'Turkish', ar: 'Arabic', ara: 'Arabic', he: 'Hebrew', heb: 'Hebrew', hu: 'Hungarian', hun: 'Hungarian',
  el: 'Greek', gre: 'Greek', ell: 'Greek', da: 'Danish', dan: 'Danish', no: 'Norwegian', nor: 'Norwegian', ro: 'Romanian', rum: 'Romanian', ron: 'Romanian',
  bg: 'Bulgarian', bul: 'Bulgarian', hr: 'Croatian', hrv: 'Croatian', sr: 'Serbian', srp: 'Serbian', sk: 'Slovak', slo: 'Slovak', slk: 'Slovak', sl: 'Slovenian', slv: 'Slovenian',
  lt: 'Lithuanian', lit: 'Lithuanian', lv: 'Latvian', lav: 'Latvian', et: 'Estonian', est: 'Estonian', be: 'Belarusian', bel: 'Belarusian', kk: 'Kazakh', kaz: 'Kazakh',
  hi: 'Hindi', hin: 'Hindi', th: 'Thai', tha: 'Thai', vi: 'Vietnamese', vie: 'Vietnamese', id: 'Indonesian', ind: 'Indonesian', ka: 'Georgian', geo: 'Georgian', kat: 'Georgian' };
var LANG2 = { eng: 'en', rus: 'ru', spa: 'es', ger: 'de', deu: 'de', fre: 'fr', fra: 'fr', ita: 'it', ukr: 'uk', jpn: 'ja', chi: 'zh', zho: 'zh', kor: 'ko', por: 'pt', pol: 'pl' };
function langName(l) { if (!l) return ''; var k = String(l).toLowerCase(); return LANG[k] || LANG[k.split(/[-_]/)[0]] || l; }
function langKey(l) { var k = String(l || '').toLowerCase().split(/[-_]/)[0]; return LANG2[k] || k; }
function chLabel(n) { return n === 1 ? 'mono' : n === 2 ? 'stereo' : n === 6 ? '5.1' : n === 8 ? '7.1' : n ? n + 'ch' : ''; }

// ---------- toast ----------
var toastTimer;
function toast(msg, ms) {
  var t = $('#toast'); t.textContent = msg; t.classList.add('on');
  clearTimeout(toastTimer); toastTimer = setTimeout(function () { t.classList.remove('on'); }, ms || 2600);
}

// ---------- focus & navigation ----------
var focusEl = null;
var lastOkAt = 0;
function setFocus(e, noScroll) {
  if (!e) return;
  if (focusEl && focusEl !== e) focusEl.classList.remove('focused');
  focusEl = e; e.classList.add('focused');
  if (!noScroll) ensureVisible(e);
  if (e._onFocus) e._onFocus();
}
function ensureVisible(e) {
  var sc = e.parentNode;
  while (sc && sc !== document.body && !(sc.classList && (sc.classList.contains('scroll') || sc.classList.contains('recents') || sc.classList.contains('dlg') || sc.classList.contains('menu')))) sc = sc.parentNode;
  if (!sc || sc === document.body) return;
  var er = e.getBoundingClientRect(), sr = sc.getBoundingClientRect(), pad = 16;
  if (sc.classList.contains('recents')) {
    if (er.left < sr.left) sc.scrollLeft -= sr.left - er.left + pad;
    else if (er.right > sr.right) sc.scrollLeft += er.right - sr.right + pad;
    sc = sc.parentNode; while (sc && !(sc.classList && sc.classList.contains('scroll'))) sc = sc.parentNode;
    if (!sc) return; sr = sc.getBoundingClientRect(); er = e.getBoundingClientRect();
  }
  if (er.top < sr.top + pad) sc.scrollTop -= sr.top + pad - er.top;
  else if (er.bottom > sr.bottom - pad) sc.scrollTop += er.bottom - sr.bottom + pad;
}
function activate(e) { if (e && e._act) e._act(); }
function visible(e) { if (!e.offsetParent && e !== document.body) return false; var r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; }

// Spatial navigation among .f elements inside a layer. Lists (data-nav) are
// one candidate each, and move internally by index for speed.
function spatial(dir, layer) {
  var cur = focusEl;
  var cands = [].slice.call(layer.querySelectorAll('.f')).filter(function (x) { return x !== cur && !x.closest('[data-nav]') && visible(x); });
  [].slice.call(layer.querySelectorAll('[data-nav]')).forEach(function (n) { if (visible(n) && n.children.length && !(cur && n.contains(cur))) cands.push(n); });
  if (!cur || !visible(cur) || !layer.contains(cur)) { if (cands.length) enterCand(cands[0]); return true; }
  var a = cur.getBoundingClientRect(), best = null, bestScore = 1e9;
  cands.forEach(function (c) {
    var b = c.getBoundingClientRect(), main, cross;
    if (dir === 'left') { main = a.left - b.right; cross = overlap(a.top, a.bottom, b.top, b.bottom); }
    else if (dir === 'right') { main = b.left - a.right; cross = overlap(a.top, a.bottom, b.top, b.bottom); }
    else if (dir === 'up') { main = a.top - b.bottom; cross = overlap(a.left, a.right, b.left, b.right); }
    else { main = b.top - a.bottom; cross = overlap(a.left, a.right, b.left, b.right); }
    if (main < -8) return;
    var score = Math.max(0, main) + cross * 2.5;
    if (score < bestScore) { bestScore = score; best = c; }
  });
  if (best) { enterCand(best); return true; }
  return false;
}
function overlap(a1, a2, b1, b2) { if (b2 < a1) return a1 - b2; if (b1 > a2) return b1 - a2; return 0; }
function enterCand(c) {
  if (c.hasAttribute('data-nav')) { var i = c._index || 0; var it = c.children[Math.min(i, c.children.length - 1)]; setFocus(it); return; }
  setFocus(c);
}
function listMove(dir) {
  var cont = focusEl && focusEl.closest('[data-nav]');
  if (!cont) return false;
  var items = cont.children, i = [].indexOf.call(items, focusEl), cols = 1;
  if (cont.getAttribute('data-nav') === 'grid' || cont.classList.contains('grid')) cols = gridCols(cont);
  var n = i;
  if (dir === 'up') n = i - cols; else if (dir === 'down') n = i + cols;
  else if (dir === 'left') { if (cols === 1 || i % cols === 0) return false; n = i - 1; }
  else if (dir === 'right') { if (cols === 1 || i % cols === cols - 1) return false; n = i + 1; }
  if (n < 0) return false;
  if (n >= items.length) { if (dir === 'down' && Math.floor(i / cols) < Math.floor((items.length - 1) / cols)) n = items.length - 1; else return true; }
  cont._index = n; setFocus(items[n]); return true;
}
function gridCols(cont) {
  var k = cont.children, top = k[0] ? k[0].offsetTop : 0, c = 0;
  for (var i = 0; i < k.length && k[i].offsetTop === top; i++) c++;
  return Math.max(1, c);
}
function pageMove(delta) {
  var cont = focusEl && focusEl.closest('[data-nav]'); if (!cont) return;
  var cols = cont.classList.contains('grid') ? gridCols(cont) : 1, first = cont.children[0];
  var per = Math.max(1, Math.floor($('#listwrap').clientHeight / (first ? first.offsetHeight + 4 : 60))) * cols;
  var i = [].indexOf.call(cont.children, focusEl), n = Math.max(0, Math.min(cont.children.length - 1, i + delta * per));
  cont._index = n; setFocus(cont.children[n]);
}

// ---------- screens ----------
var screen = 'home';
function showScreen(name) {
  screen = name;
  $('#home').classList.toggle('on', name === 'home');
  $('#browser').classList.toggle('on', name === 'browser');
  $('#tb-back').classList.toggle('hide', name === 'home');
}
function setHints(list) {
  $('#hints').innerHTML = list.map(function (h) { return '<span>' + (h[0] ? '<i class="k-' + h[0] + '"></i>' : '') + esc(h[1]) + '</span>'; }).join('');
}
function tbButtons(btns) {
  var box = $('#tb-buttons'); box.innerHTML = '';
  btns.forEach(function (b) {
    var e = el('span', 'tbtn f', '<span class="fa">' + b.icon + '</span>' + (b.label ? '<span class="lbl">' + esc(b.label) + '</span>' : ''));
    e.title = b.title || b.label || ''; e._act = b.act; if (b.id) e.id = b.id; box.appendChild(e);
  });
}
function tick() { var d = new Date(); $('#clock').textContent = d.getHours() + ':' + ('0' + d.getMinutes()).slice(-2); }

// ---------- home ----------
var home = {
  usb: [], net: [], lan: [], scanning: false, error: '', timer: null,
  moved: false, wantFocus: null,
  open: function (focusId) {
    this.moved = false; this.wantFocus = focusId || null;
    showScreen('home');
    $('#title').textContent = 'File Manager'; $('#crumbs').textContent = '';
    tbButtons([{ icon: '', title: 'Search the network again', act: function () { home.refresh(true); toast('Searching the network…'); } },
      { icon: '', title: 'Settings', act: openSettings }]);
    setHints([['red', 'Options'], ['yellow', 'Refresh'], ['', 'BACK Exit']]);
    this.render(focusId);
    this.refresh(false);
    clearInterval(this.timer);
    this.timer = setInterval(function () { if (screen === 'home' && !player.on && !dialog.on) home.refresh(false, true); }, 5000);
  },
  refresh: function (force, quiet) {
    var self = this;
    Promise.all([
      api('/api/sources').catch(function (e) { self.error = e.message; return null; }),
      api('/api/discover' + (force ? '?refresh=1' : '')).catch(function () { return null; })
    ]).then(function (r) {
      var s = r[0], d = r[1];
      if (s) self.error = '';
      var before = JSON.stringify([self.usb, self.net, self.lan, self.scanning, self.error]);
      if (s) { self.usb = s.usb || []; self.net = s.network || []; }
      if (d) { self.lan = d.servers || []; self.scanning = d.scanning; }
      if (d && d.scanning) setTimeout(function () { if (screen === 'home') self.refresh(false, true); }, 1500);
      if (JSON.stringify([self.usb, self.net, self.lan, self.scanning, self.error]) !== before || !quiet) {
        if (screen === 'home' && !dialog.on) self.render(self.moved ? focusEl && focusEl._id : self.wantFocus);
      }
    });
  },
  tile: function (o) {
    var t = el('div', 'tile f' + (o.static ? ' static' : ''));
    t.innerHTML = '<div class="circ fa" style="--c:' + o.color + '">' + o.icon + '</div><div class="tinfo"><div class="tname">' + esc(o.name) + '</div>' +
      (o.sub ? '<div class="tsub">' + esc(o.sub) + '</div>' : '') + (o.bar != null ? '<div class="tbar"><i class="' + (o.bar > 90 ? 'warn' : '') + '" style="width:' + o.bar + '%"></i></div>' : '') + '</div>' +
      (o.badge ? '<span class="badge">' + esc(o.badge) + '</span>' : '');
    t._id = o.id; t._act = o.act; t._opts = o.opts;
    if (o.static) t.classList.remove('f');
    return t;
  },
  render: function (focusId) {
    var h = $('#home'), self = this, keepScroll = h.scrollTop; h.innerHTML = '';
    if (this.error) h.appendChild(el('div', 'banner', '<span class="fa spin"></span>&nbsp; ' + esc(this.error) + ' – it restarts by itself, retrying…'));

    h.appendChild(el('div', 'sec-title', 'Storage'));
    var g = el('div', 'tiles'); h.appendChild(g);
    this.usb.forEach(function (u) {
      var used = u.total ? Math.round((u.total - u.free) / u.total * 100) : null;
      g.appendChild(self.tile({ id: u.id, icon: ICON.usb, color: COLOR.usb, name: u.name, sub: (u.total ? fmtSize(u.free) + ' free of ' + fmtSize(u.total) : 'USB drive') + (u.fstype && u.fstype !== 'dir' ? ' · ' + u.fstype.replace('tntfs', 'NTFS').replace('tfat', 'FAT').toUpperCase() : ''),
        bar: used, act: function () { browser.open(u.id, u.name, ''); } }));
    });
    if (!this.usb.length) g.appendChild(this.tile({ static: true, icon: ICON.usb, color: '#4b5563', name: 'No USB drive', sub: 'Plug one in and it appears here' }));

    h.appendChild(el('div', 'sec-title', 'Network' + (this.scanning ? '<span class="sub"><span class="fa spin"></span> searching…</span>' : '')));
    g = el('div', 'tiles'); h.appendChild(g);
    var shown = {};
    this.lan.forEach(function (sv) {
      shown[sv.host.toLowerCase()] = 1;
      g.appendChild(self.tile({ id: 'lan:' + sv.host, icon: ICON.server, color: COLOR.server, name: sv.name, sub: (sv.name !== sv.host ? sv.host + ' · ' : '') + (sv.login ? 'signed in' : 'Windows / Samba'),
        act: function () { browser.open('lan:' + sv.host, sv.name, ''); },
        opts: sv.login ? [['Sign out', function () { api('/api/sources/delete', { ID: sv.login }).then(function () { home.refresh(); }); }]] : null }));
    });
    this.net.forEach(function (s) {
      if (s.kind === 'smb' && !s.share) {
        if (shown[s.host.toLowerCase()]) return; // its server tile is already there
        g.appendChild(self.tile({ id: 'lan:' + s.host, icon: ICON.server, color: COLOR.server, name: s.name, sub: s.host + ' · signed in', badge: 'offline?',
          act: function () { browser.open('lan:' + s.host, s.name, ''); }, opts: [['Sign out', function () { api('/api/sources/delete', { ID: s.id }).then(function () { home.refresh(); }); }]] }));
        return;
      }
      var isSftp = s.kind === 'sftp';
      g.appendChild(self.tile({ id: s.id, icon: isSftp ? ICON.sftp : ICON.share, color: isSftp ? COLOR.sftp : COLOR.share, name: s.name,
        sub: (isSftp ? 'sftp://' + (s.user ? s.user + '@' : '') + s.host : 'smb://' + s.host + '/' + s.share) + (s.path ? '/' + s.path : ''),
        act: function () { browser.open(s.id, s.name, ''); },
        opts: [['Edit', function () { editLocation(s); }], ['Remove', function () { confirmBox('Remove "' + s.name + '"?', 'Its files are not touched.', 'Remove', function () { api('/api/sources/delete', { ID: s.id }).then(function () { home.refresh(); }); }); }]] }));
    });
    g.appendChild(this.tile({ id: 'add', icon: ICON.add, color: COLOR.add, name: 'Add location', sub: 'SMB share or SFTP server', act: function () { editLocation(null); } }));

    var pins = store.get('pins', []);
    if (pins.length) {
      h.appendChild(el('div', 'sec-title', 'Pinned folders'));
      g = el('div', 'tiles'); h.appendChild(g);
      pins.forEach(function (p, i) {
        g.appendChild(self.tile({ id: 'pin:' + i, icon: ICON.pin, color: COLOR.pin, name: p.name, sub: p.srcName + (p.path ? ' / ' + p.path : ''),
          act: function () { browser.open(p.src, p.srcName, p.path); },
          opts: [['Unpin', function () { var ps = store.get('pins', []); ps.splice(i, 1); store.set('pins', ps); home.render(); }]] }));
      });
    }

    var rec = recents.list().filter(function (r) { return r.pos > 15 && !r.done; }).slice(0, 12);
    if (rec.length) {
      h.appendChild(el('div', 'sec-title', 'Continue watching'));
      var row = el('div', 'recents'); h.appendChild(row);
      rec.forEach(function (r) {
        var t = el('div', 'rtile f');
        t.innerHTML = '<div class="rthumb fa">' + ICON[kindOf(r)] + '<img alt=""><div class="rprog" style="width:' + Math.min(100, r.pos / (r.dur || 1) * 100) + '%"></div></div>' +
          '<div class="rname">' + esc(r.name) + '</div><div class="rsub">' + esc(fmtTime(r.pos)) + ' of ' + esc(fmtTime(r.dur)) + ' · ' + esc(r.srcName) + '</div>';
        if (settings.thumbs) { var im = t.querySelector('img'); im.onload = function () { im.classList.add('ok'); }; im.src = thumbURL(r.src, r.path, r); }
        t._id = 'rec:' + r.key;
        t._act = function () { player.openPath(r.src, r.srcName, r.path); };
        t._opts = [['Remove from list', function () { recents.remove(r.key); home.render(); }]];
        row.appendChild(t);
      });
    }
    h.scrollTop = keepScroll;
    var f = focusId && [].slice.call(h.querySelectorAll('.f')).filter(function (x) { return x._id === focusId; })[0];
    if (!f && !this.moved) f = h.querySelector('.tile.f');
    setFocus(f || h.querySelector('.f'), !f);
  },
  key: function (k) {
    if (k === KEY.BACK) { confirmBox('Exit File Manager?', '', 'Exit', function () { window.close(); }); return true; }
    if (k === KEY.YELLOW) { this.refresh(true); toast('Searching the network…'); return true; }
    if (k === KEY.RED && focusEl && focusEl._opts) { menu('Options', focusEl._opts.map(function (o) { return { label: o[0], act: o[1] }; })); return true; }
    return false;
  }
};

// ---------- recently played ----------
var recents = {
  all: function () { return store.get('recents', {}); },
  list: function () { var a = this.all(); return Object.keys(a).map(function (k) { a[k].key = k; return a[k]; }).sort(function (x, y) { return y.at - x.at; }); },
  get: function (src, path) { return this.all()[src + '|' + path]; },
  save: function (src, srcName, path, e, pos, dur) {
    var a = this.all(), k = src + '|' + path;
    a[k] = { src: src, srcName: srcName, path: path, name: baseName(path), size: e.size, mtime: e.mtime, pos: pos, dur: dur, at: Date.now(), done: dur > 0 && pos > dur * 0.94 };
    var keys = Object.keys(a).sort(function (x, y) { return a[y].at - a[x].at; });
    keys.slice(200).forEach(function (x) { delete a[x]; });
    store.set('recents', a);
  },
  remove: function (key) { var a = this.all(); delete a[key]; store.set('recents', a); }
};

// ---------- browser ----------
var collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
var browser = {
  src: '', srcName: '', path: '', all: [], entries: [], seq: 0, probeCache: {},
  open: function (src, srcName, path, focusName) {
    this.src = src; this.srcName = srcName; this.path = path || '';
    showScreen('browser');
    this.chrome();
    $('#title').textContent = this.path ? baseName(this.path) : srcName;
    $('#crumbs').textContent = [srcName].concat(this.path ? this.path.split('/') : []).join('  ›  ');
    this.load(focusName);
  },
  chrome: function () {
    tbButtons([
      { icon: '', label: 'Sort', act: sortMenu },
      { icon: settings.view === 'grid' ? '' : '', label: settings.view === 'grid' ? 'List' : 'Grid', act: toggleView },
      { icon: '', label: 'Pin', act: pinFolder },
      { icon: '', label: 'Home', act: goHome }]);
    setHints([['red', 'Sort'], ['green', settings.view === 'grid' ? 'List view' : 'Grid view'], ['yellow', 'Refresh'], ['blue', 'Home'], ['', 'BACK Up one folder']]);
  },
  load: function (focusName) {
    var self = this, seq = ++this.seq, list = $('#list');
    thumbs.reset();
    list.innerHTML = ''; $('#empty').style.display = 'none'; $('#details').innerHTML = '';
    $('#empty').innerHTML = '<span class="fa spin"></span>Loading…'; $('#empty').style.display = 'block';
    setFocus($('#tb-back'));
    api('/api/list?' + q({ src: this.src, path: this.path })).then(function (d) {
      if (seq !== self.seq) return;
      self.all = d.entries || []; self.render(focusName);
    }).catch(function (e) {
      if (seq !== self.seq) return;
      if (self.src.indexOf('lan:') === 0 && /login|logon|access|denied|password|STATUS_/i.test(e.message)) { loginServer(self.src.slice(4), self.srcName, e.message); }
      $('#empty').innerHTML = '<span class="fa"></span>' + esc(e.message) + '<br><br><span style="font-size:1.1rem">Press YELLOW to retry, BACK to go back.</span>';
      $('#empty').style.display = 'block';
    });
  },
  render: function (focusName) {
    var self = this, list = $('#list'), grid = settings.view === 'grid';
    this.entries = this.all.filter(function (e) { return settings.hidden || (e.name.charAt(0) !== '.' && !HIDDEN[e.name.toLowerCase()]); });
    var s = settings.sort, dir = settings.desc ? -1 : 1;
    this.entries.sort(function (a, b) {
      if (a.dir !== b.dir) return a.dir ? -1 : 1;
      var r = 0;
      if (s === 'date') r = a.mtime - b.mtime; else if (s === 'size') r = a.size - b.size; else if (s === 'type') r = collator.compare(extOf(a.name), extOf(b.name));
      return (r || collator.compare(a.name, b.name)) * (s === 'name' || r ? dir : 1);
    });
    this._rec = recents.all();
    list.className = grid ? 'grid' : ''; list.innerHTML = ''; list._index = 0;
    $('#empty').style.display = this.entries.length ? 'none' : 'block';
    if (!this.entries.length) $('#empty').innerHTML = '<span class="fa"></span>This folder is empty';
    var fi = 0, frag = document.createDocumentFragment();
    this.entries.forEach(function (e, i) { frag.appendChild(self.item(e, i, grid)); if (focusName && e.name === focusName) fi = i; });
    list.appendChild(frag);
    var counts = this.entries.reduce(function (c, e) { c[e.dir ? 0 : 1]++; return c; }, [0, 0]);
    $('#crumbs').textContent = [this.srcName].concat(this.path ? this.path.split('/') : []).join('  ›  ') + '   ·   ' + [counts[0] ? counts[0] + ' folders' : '', counts[1] ? counts[1] + ' files' : ''].filter(Boolean).join(', ');
    if (this.entries.length) { list._index = fi; setFocus(list.children[fi]); }
    else setFocus($('#tb-back'));
    thumbs.observe(list);
  },
  item: function (e, i, grid) {
    var k = kindOf(e), d = el('div', 'item f ' + (grid ? 'tile2' : 'row')), self = this;
    var rp = PLAYABLE[k] && k !== 'image' ? this._rec[this.src + '|' + joinPath(this.path, e.name)] : null;
    var prog = rp && rp.dur ? Math.min(100, rp.pos / rp.dur * 100) : 0;
    var meta = e.dir ? (e.mtime ? fmtDate(e.mtime) : 'Folder') : fmtDate(e.mtime);
    if (grid) {
      d.innerHTML = '<div class="thumb" style="--c:' + COLOR[k] + '"><span class="fa">' + ICON[k] + '</span></div><div class="nm">' + esc(e.name) + '</div>' + (prog && !rp.done ? '<div class="prog" style="width:' + (prog * 0.17) + 'rem"></div>' : '');
    } else {
      d.innerHTML = '<div class="ic fa' + (k === 'image' || k === 'video' ? ' img' : '') + '" style="--c:' + COLOR[k] + '">' + ICON[k] + '</div><div class="txt"><div class="nm">' + esc(e.name) + '</div><div class="mt">' + esc(meta) +
        (rp && rp.done ? '<span class="done-mark fa"></span>' : '') + '</div></div><div class="rt">' + (e.dir ? '' : esc(fmtSize(e.size))) + '</div>' + (prog && !rp.done ? '<div class="prog" style="width:' + (prog * 0.25) + 'rem"></div>' : '');
      if (k === 'image' || k === 'video') d.firstChild.style.background = 'var(--surface2)', d.firstChild.style.color = COLOR[k];
    }
    d._entry = e; d._kind = k;
    if (settings.thumbs && (k === 'image' || k === 'video' || k === 'audio')) d._thumb = thumbURL(this.src, joinPath(this.path, e.name), e);
    d._act = function () { self.activate(e); };
    d._onFocus = function () { $('#list')._index = [].indexOf.call(d.parentNode.children, d); self.showDetails(e); };
    return d;
  },
  activate: function (e) {
    var k = kindOf(e), rel = joinPath(this.path, e.name);
    if (e.dir) { this.open(this.src, this.srcName, rel); return; }
    if (PLAYABLE[k]) { player.openList(this.src, this.srcName, this.path, this.entries.filter(function (x) { return kindOf(x) === k; }), e); return; }
    toast('The TV cannot open ' + (extOf(e.name) || 'this kind of') + ' files');
  },
  up: function () {
    if (!this.path) { goHome(); return; }
    this.open(this.src, this.srcName, parentPath(this.path), baseName(this.path));
  },
  showDetails: debounce(function (e) {
    var k = kindOf(e), rel = joinPath(this.path, e.name), box = $('#details'), self = this;
    box.innerHTML = '<div class="pv" style="--c:' + COLOR[k] + '"><span class="big fa">' + ICON[k] + '</span></div><div class="dname">' + esc(e.name) + '</div><div class="dl"></div>';
    var dl = box.querySelector('.dl');
    function row(kk, v) { if (v) dl.insertAdjacentHTML('beforeend', '<div class="r"><span class="k">' + esc(kk) + '</span><span class="v">' + v + '</span></div>'); }
    row('Type', e.dir ? 'Folder' : esc((extOf(e.name).slice(1) || 'file').toUpperCase() + ' ' + k));
    if (!e.dir) row('Size', esc(fmtSize(e.size)));
    row('Modified', esc(fmtDate(e.mtime)));
    if (settings.thumbs && (k === 'image' || k === 'video' || k === 'audio')) {
      var im = new Image(); im.decoding = 'async';
      im.onload = function () { if (box.querySelector('.dname') && box.querySelector('.dname').textContent === e.name) { var pv = box.querySelector('.pv'); pv.innerHTML = ''; pv.appendChild(im); im.classList.add('ok'); } };
      im.src = thumbURL(this.src, rel, e);
    }
    if (k === 'video' || k === 'audio') {
      var key = this.src + '|' + rel + '|' + e.size + '|' + e.mtime;
      var show = function (p) {
        if (!box.querySelector('.dname') || box.querySelector('.dname').textContent !== e.name || p.error) return;
        if (p.duration) row('Length', esc(fmtTime(p.duration)));
        if (p.video) row('Video', esc(p.video.width + '×' + p.video.height + ' ' + p.video.codec.toUpperCase() + (p.video.hdr ? ' ' + p.video.hdr : '') + (p.video.fps ? ' · ' + p.video.fps + ' fps' : '')));
        if (p.audio && p.audio.length) row('Audio', '<div class="tracks">' + p.audio.map(function (a) { return '<div>' + esc([langName(a.lang) || 'Unknown', a.title, a.codec.toUpperCase() + ' ' + chLabel(a.channels)].filter(Boolean).join(' · ')) + '</div>'; }).join('') + '</div>');
        if (p.subs && p.subs.length) row('Subtitles', '<div class="tracks">' + p.subs.map(function (s) { return '<div>' + esc([langName(s.lang) || 'Unknown', s.title && s.title !== s.lang ? s.title : '', s.external ? 'file' : '', s.text ? '' : 'picture – not supported'].filter(Boolean).join(' · ')) + '</div>'; }).join('') + '</div>');
      };
      if (this.probeCache[key]) show(this.probeCache[key]);
      else api('/api/probe?' + q({ src: this.src, path: rel }), null, 30000).then(function (p) { self.probeCache[key] = p; show(p); }).catch(function () {});
    }
  }, 120),
  key: function (k) {
    if (k === KEY.BACK) { this.up(); return true; }
    if (k === KEY.RED) { sortMenu(); return true; }
    if (k === KEY.GREEN) { toggleView(); return true; }
    if (k === KEY.YELLOW) { this.load(focusEl && focusEl._entry && focusEl._entry.name); return true; }
    if (k === KEY.BLUE) { goHome(); return true; }
    if (k === KEY.CH_UP) { pageMove(-1); return true; }
    if (k === KEY.CH_DOWN) { pageMove(1); return true; }
    return false;
  }
};
function goHome() { home.open(browser.src); }
function toggleView() { settings.view = settings.view === 'grid' ? 'list' : 'grid'; saveSettings(); var n = focusEl && focusEl._entry && focusEl._entry.name; browser.chrome(); browser.render(n); }
function sortMenu() {
  var cur = settings.sort;
  menu('Sort by', [['name', 'Name'], ['date', 'Date modified'], ['size', 'Size'], ['type', 'Type']].map(function (o) {
    return { label: o[1], sel: cur === o[0], act: function () { settings.desc = cur === o[0] ? !settings.desc : (o[0] !== 'name'); settings.sort = o[0]; saveSettings(); browser.render(focusEl && focusEl._entry && focusEl._entry.name); toast('Sorted by ' + o[1].toLowerCase() + (settings.desc ? ', descending' : '')); } };
  }), true);
}
function pinFolder() {
  var pins = store.get('pins', []), p = { src: browser.src, srcName: browser.srcName, path: browser.path, name: browser.path ? baseName(browser.path) : browser.srcName };
  if (pins.some(function (x) { return x.src === p.src && x.path === p.path; })) { toast('Already pinned to Home'); return; }
  pins.push(p); store.set('pins', pins); toast('Pinned "' + p.name + '" to Home');
}

// ---------- thumbnails: only visible ones, two at a time ----------
var thumbs = {
  queue: [], active: 0, obs: null, gen: 0,
  reset: function () { this.gen++; this.queue = []; this.active = 0; if (this.obs) this.obs.disconnect(); },
  observe: function (list) {
    var self = this;
    if (!settings.thumbs) return;
    this.obs = new IntersectionObserver(function (items) {
      items.forEach(function (it) { if (it.isIntersecting) { self.obs.unobserve(it.target); if (it.target._thumb) self.queue.push(it.target); } });
      self.pump();
    }, { root: $('#listwrap'), rootMargin: '300px' });
    [].forEach.call(list.children, function (c) { if (c._thumb) self.obs.observe(c); });
  },
  pump: function () {
    var self = this, gen = this.gen;
    while (this.active < 2 && this.queue.length) {
      var node = this.queue.shift(); this.active++;
      (function (node) {
        var im = new Image(); im.decoding = 'async'; im.alt = '';
        var done = function () { if (gen !== self.gen) return; self.active--; self.pump(); };
        im.onload = function () {
          if (gen === self.gen) {
            var box = node.querySelector('.ic') || node.querySelector('.thumb');
            if (box) { box.textContent = ''; box.appendChild(im); requestAnimationFrame(function () { im.classList.add('ok'); }); }
          }
          done();
        };
        im.onerror = done;
        im.src = node._thumb;
      })(node);
    }
  }
};

// ---------- dialogs and menus ----------
var dialog = {
  on: false, prevFocus: null, onBack: null,
  show: function (node, opts) {
    opts = opts || {};
    var d = $('#dialog'); d.innerHTML = ''; d.appendChild(node);
    d.className = 'on' + (opts.side ? ' side' : '') + (opts.center ? ' center' : '');
    if (!this.on) this.prevFocus = focusEl;
    this.on = true; this.onBack = opts.onBack || null;
    setFocus(opts.focus || node.querySelector('.f.sel') || node.querySelector('.f'));
  },
  close: function () {
    if (!this.on) return;
    this.on = false; $('#dialog').className = ''; $('#dialog').innerHTML = '';
    if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
    if (this.prevFocus && document.contains(this.prevFocus)) setFocus(this.prevFocus, true);
    else if (player.on) player.osd(true);
  },
  key: function (k) {
    if (k === KEY.BACK || k === KEY.ESC) { var cb = this.onBack; this.close(); if (cb) cb(); return true; }
    return false;
  }
};
function btn(label, act, cls) { var b = el('span', 'btn f' + (cls ? ' ' + cls : ''), esc(label)); b._act = act; return b; }
function confirmBox(title, text, okLabel, onOk) {
  var box = el('div', 'dlg'); box.style.width = '50rem';
  box.innerHTML = '<h2>' + esc(title) + '</h2>' + (text ? '<div class="msg">' + esc(text) + '</div>' : '');
  var b = el('div', 'btns'), stay = btn('Cancel', function () { dialog.close(); });
  b.appendChild(stay); b.appendChild(btn(okLabel, function () { dialog.close(); onOk(); }, 'primary')); box.appendChild(b);
  dialog.show(box, { focus: stay });
}
// items: [{label, sub, sel, dis, act}]
function menu(title, items, center) {
  var m = el('div', 'menu');
  if (title) m.appendChild(el('h3', '', esc(title)));
  items.forEach(function (it) {
    var mi = el('div', 'mi f' + (it.sel ? ' sel' : '') + (it.dis ? ' dis' : ''), esc(it.label) + (it.sub ? '<span class="ms">' + esc(it.sub) + '</span>' : ''));
    mi._act = function () { if (it.dis) { if (it.why) toast(it.why); return; } dialog.close(); it.act(); };
    m.appendChild(mi);
  });
  dialog.show(m, { side: !center, center: center });
}
function field(label, value, type, ph) {
  var f = el('div', 'field f'), inp = el('input');
  f.innerHTML = '<label>' + esc(label) + '</label>'; inp.type = type || 'text'; inp.value = value || ''; if (ph) inp.placeholder = ph;
  inp.setAttribute('autocomplete', 'off'); inp.setAttribute('spellcheck', 'false'); f.appendChild(inp);
  f._act = function () { inp.focus(); }; // the webOS keyboard opens on focus
  f._input = inp; return f;
}

function editLocation(src) {
  var isNew = !src; src = src || { kind: 'smb' };
  var box = el('div', 'dlg');
  box.innerHTML = '<h2>' + (isNew ? 'Add network location' : 'Edit location') + '</h2>';
  var kind = src.kind;
  var seg = el('div', 'field'), segIn = el('div', 'seg');
  seg.innerHTML = '<label>Type</label>'; seg.appendChild(segIn);
  var bSmb = el('span', 'f', 'SMB (Windows / Samba)'), bSftp = el('span', 'f', 'SFTP (SSH)');
  segIn.appendChild(bSmb); segIn.appendChild(bSftp); box.appendChild(seg);
  var fName = field('Name', src.name, 'text', 'shown on Home'), fHost = field('Server', src.host, 'text', 'name or IP address'),
    fShare = field('Share', src.share, 'text', 'e.g. VIDEO'), fPath = field('Folder', src.path, 'text', 'optional, inside the share'),
    fUser = field('User', src.user, 'text', 'empty = guest'), fPass = field('Password', '', 'password', src.hasPassword ? 'unchanged' : ''),
    fPort = field('Port', src.port ? String(src.port) : '', 'text', 'default');
  [fName, fHost, fShare, fPath, fUser, fPass, fPort].forEach(function (f) { box.appendChild(f); });
  var keyInfo = el('div', 'msg'); box.appendChild(keyInfo);
  var status = el('div', 'status'); box.appendChild(status);
  var bb = el('div', 'btns'); box.appendChild(bb);
  function setKind(k) {
    kind = k; bSmb.classList.toggle('sel', k === 'smb'); bSftp.classList.toggle('sel', k === 'sftp');
    fShare.style.display = k === 'smb' ? '' : 'none';
    fPath.querySelector('input').placeholder = k === 'smb' ? 'optional, inside the share' : 'optional, default is the home folder';
    fUser.querySelector('input').placeholder = k === 'smb' ? 'empty = guest' : 'required';
    keyInfo.innerHTML = '';
    if (k === 'sftp') api('/api/sshkey').then(function (d) { keyInfo.innerHTML = 'Log in with the password, or add this TV\'s key to the server\'s ~/.ssh/authorized_keys:<div class="keybox">' + esc(d.publicKey) + '</div>'; }).catch(function () {});
  }
  bSmb._act = function () { setKind('smb'); }; bSftp._act = function () { setKind('sftp'); };
  setKind(kind);
  function collect() {
    return { id: src.id || '', kind: kind, name: fName._input.value, host: fHost._input.value, share: fShare._input.value, path: fPath._input.value,
      user: fUser._input.value, password: fPass._input.value, port: parseInt(fPort._input.value, 10) || 0, keepPassword: !isNew };
  }
  function say(t, cls) { status.className = 'status' + (cls ? ' ' + cls : ''); status.innerHTML = t; }
  bb.appendChild(btn('Test', function () {
    say('<span class="fa spin"></span> Connecting…');
    api('/api/sources/test', collect(), 25000).then(function (d) { if (d.ok) say('Connected – ' + d.items + ' items', 'ok'); else say(esc(d.error), 'err'); }).catch(function (e) { say(esc(e.message), 'err'); });
  }));
  bb.appendChild(btn('Shares…', function () {
    if (kind !== 'smb') { say('Only SMB servers have shares', 'err'); return; }
    say('<span class="fa spin"></span> Asking the server…');
    api('/api/smb/shares', collect(), 20000).then(function (d) {
      say('');
      menu('Shares on ' + fHost._input.value, d.shares.map(function (s) { return { label: s, act: function () { editLocation(Object.assign({}, src, collect(), { share: s, hasPassword: src.hasPassword, kind: 'smb' })); } }; }), true);
    }).catch(function (e) { say(esc(e.message), 'err'); });
  }));
  bb.appendChild(btn('Cancel', function () { dialog.close(); }));
  bb.appendChild(btn('Save', function () {
    say('<span class="fa spin"></span> Saving…');
    api('/api/sources/save', collect()).then(function () { dialog.close(); home.refresh(); toast('Saved'); }).catch(function (e) { say(esc(e.message), 'err'); });
  }, 'primary'));
  dialog.show(box, { focus: isNew ? fHost : fName });
}

function loginServer(host, name, why) {
  var box = el('div', 'dlg'); box.style.width = '56rem';
  box.innerHTML = '<h2>Sign in to ' + esc(name) + '</h2><div class="msg">This server did not accept a guest login' + (why ? ' (' + esc(why.replace(/^.*?: /, '')) + ')' : '') + '. The login is saved on the TV.</div>';
  var fUser = field('User', '', 'text'), fPass = field('Password', '', 'password');
  box.appendChild(fUser); box.appendChild(fPass);
  var status = el('div', 'status'); box.appendChild(status);
  var bb = el('div', 'btns'); box.appendChild(bb);
  bb.appendChild(btn('Cancel', function () { dialog.close(); }));
  bb.appendChild(btn('Sign in', function () {
    var c = { kind: 'smb', host: host, name: name, user: fUser._input.value, password: fPass._input.value };
    status.className = 'status'; status.innerHTML = '<span class="fa spin"></span> Signing in…';
    api('/api/sources/test', c, 20000).then(function (d) {
      if (!d.ok) throw new Error(d.error);
      return api('/api/sources/save', c);
    }).then(function () { dialog.close(); browser.open('lan:' + host, name, ''); }).catch(function (e) { status.className = 'status err'; status.textContent = e.message; });
  }, 'primary'));
  dialog.show(box, { focus: fUser });
}

// ---------- root setup through the Homebrew Channel ----------
// The file service runs as root, started at boot by a Homebrew Channel init.d hook. setup.sh
// installs that hook and starts the service. The app runs it as root through the Homebrew
// Channel's exec method when the service does not answer (first launch), and when the
// running service is older than the app (just after an update).
var APP_VERSION = '0.6.0'; // build.sh keeps this in step with appinfo.json
var APP_DIR = '/media/developer/apps/usr/palm/applications/com.pulp.smbbrowser';
var lunaCalls = []; // a PalmServiceBridge must stay referenced until its answer arrives
function luna(uri, params) {
  return new Promise(function (resolve, reject) {
    if (typeof PalmServiceBridge === 'undefined') { reject(new Error('This only works on a webOS TV')); return; }
    var b = new PalmServiceBridge(); lunaCalls.push(b);
    b.onservicecallback = function (msg) {
      lunaCalls.splice(lunaCalls.indexOf(b), 1);
      var r; try { r = JSON.parse(msg); } catch (e) { r = { returnValue: false, errorText: String(msg) }; }
      if (r.returnValue === false) reject(new Error(String(r.stderrString || r.errorText || 'The call failed').trim()));
      else resolve(r);
    };
    b.call(uri, JSON.stringify(params || {}));
  });
}
function rootRun(script) { return luna('luna://org.webosbrew.hbchannel.service/exec', { command: '/bin/sh ' + APP_DIR + '/' + script }); }
function waitHealth(ms) {
  var until = Date.now() + ms;
  return new Promise(function (resolve, reject) {
    (function poll() { api('/api/health', null, 1500).then(resolve, function (e) { if (Date.now() > until) reject(e); else setTimeout(poll, 700); }); })();
  });
}
var setup = {
  busy: false,
  check: function () {
    api('/api/health', null, 3000).then(function (h) {
      if (h.version !== APP_VERSION) setup.run('Updating the file service');
      else setup.offerFfmpeg(h);
    }, function () { setup.run('Setting up the file service'); });
  },
  run: function (title) {
    if (this.busy) return; this.busy = true;
    var box = el('div', 'dlg'); box.style.width = '50rem';
    box.innerHTML = '<h2>' + esc(title) + '</h2><div class="status"><span class="fa spin"></span> Starting it through the Homebrew Channel…</div>';
    dialog.show(box);
    rootRun('setup.sh').then(function () { return waitHealth(15000); }).then(function (h) {
      setup.busy = false;
      if ($('#dialog').contains(box)) dialog.close();
      if (screen === 'home') home.refresh();
      setup.offerFfmpeg(h);
    }).catch(function (e) { setup.busy = false; setup.fail(e); });
  },
  fail: function (e) {
    var box = el('div', 'dlg'); box.style.width = '58rem';
    box.innerHTML = '<h2>The file service did not start</h2><div class="msg">File Manager needs a rooted TV with the Homebrew Channel. ' +
      'Its file service runs as root, and the Homebrew Channel starts it at boot.</div><div class="status err">' + esc(e.message) + '</div>';
    var b = el('div', 'btns'), again = btn('Try again', function () { dialog.close(); setup.run('Setting up the file service'); }, 'primary');
    b.appendChild(btn('Close', function () { dialog.close(); })); b.appendChild(again); box.appendChild(b);
    dialog.show(box, { focus: again });
  },
  // ffmpeg is a separate, optional download: offered once automatically, later from Settings.
  offerFfmpeg: function (h, asked) {
    if (!h || h.ffmpeg || (!asked && store.get('ffmpegOffered', false))) return;
    store.set('ffmpegOffered', true);
    var box = el('div', 'dlg'); box.style.width = '60rem';
    box.innerHTML = '<h2>Download ffmpeg?</h2><div class="msg">Thumbnails, stream information and embedded subtitles need ffmpeg. ' +
      'File Manager can download the static arm64 build from johnvansickle.com (GPLv3): 19 MB to download, about 100 MB on the TV. ' +
      'Browsing and playback work without it, and Settings can download it later.</div>';
    var status = el('div', 'status'); box.appendChild(status);
    var prog = el('div', 'prog', '<i></i>'); prog.style.display = 'none'; box.appendChild(prog);
    var b = el('div', 'btns'), later = btn('Not now', function () { dialog.close(); });
    var get = btn('Download', function () { start(); }, 'primary');
    b.appendChild(later); b.appendChild(get); box.appendChild(b);
    dialog.show(box, { focus: get });
    var shown = function () { return $('#dialog').contains(box); };
    var last = '', lastAt = 0;
    // get-ffmpeg.sh runs in the background; its status says how far it is.
    function start() {
      get.style.display = 'none'; later.textContent = 'Hide'; setFocus(later);
      status.className = 'status'; status.innerHTML = '<span class="fa spin"></span> Starting the download…';
      last = ''; lastAt = Date.now();
      rootRun('get-ffmpeg.sh start').then(poll).catch(failed);
    }
    function poll() {
      return rootRun('get-ffmpeg.sh status').then(function (r) {
        var p = String(r.stdoutString || '').trim().split(' '), phase = p[0], got = +p[1] || 0, total = +p[2] || 0;
        if (phase === 'error') throw new Error(p.slice(3).join(' ') || 'The download failed');
        if (phase === 'done') return waitHealth(20000).then(finished);
        if (p[0] + p[1] !== last) { last = p[0] + p[1]; lastAt = Date.now(); }
        else if (Date.now() - lastAt > 90000) throw new Error('The download stopped making progress');
        var pct = phase === 'download' ? (total ? Math.min(100, got / total * 100) : 0) : (phase === 'start' || phase === 'none') ? 0 : 100;
        prog.style.display = ''; prog.firstChild.style.width = pct.toFixed(1) + '%';
        status.innerHTML = '<span class="fa spin"></span> ' + esc({
          start: 'Starting the download…',
          download: 'Downloading ffmpeg: ' + fmtSize(got) + (total ? ' of ' + fmtSize(total) : ''),
          check: 'Checking the download…', unpack: 'Unpacking…', restart: 'Restarting the file service…'
        }[phase] || 'Working…');
        setTimeout(function () { poll().catch(failed); }, 700);
      });
    }
    function finished(h2) {
      if (!h2.ffmpeg) throw new Error('ffmpeg was installed, but the file service does not see it');
      if (shown()) dialog.close();
      toast('ffmpeg is installed: thumbnails and subtitles are on');
      if (dialog.on) return;
      if (screen === 'home') home.render(); else browser.render(focusEl && focusEl._entry && focusEl._entry.name);
    }
    function failed(e) {
      if (!shown()) { toast('ffmpeg: ' + e.message, 6000); return; }
      prog.style.display = 'none';
      status.className = 'status err'; status.textContent = e.message;
      get.textContent = 'Try again'; get.style.display = ''; later.textContent = 'Close'; setFocus(get);
    }
  }
};

function openSettings() {
  var m = el('div', 'dlg'); m.style.width = '58rem';
  m.innerHTML = '<h2>Settings</h2>';
  var rows = [
    ['Theme', function () { return settings.theme === 'dark' ? 'Dark' : 'Light'; }, function () { settings.theme = settings.theme === 'dark' ? 'light' : 'dark'; applyTheme(); }],
    ['Show hidden files', function () { return settings.hidden ? 'On' : 'Off'; }, function () { settings.hidden = !settings.hidden; }],
    ['Thumbnails', function () { return settings.thumbs ? 'On' : 'Off'; }, function () { settings.thumbs = !settings.thumbs; }],
    ['Subtitle size', function () { return { s: 'Small', m: 'Medium', l: 'Large' }[settings.subSize]; }, function () { settings.subSize = { s: 'm', m: 'l', l: 's' }[settings.subSize]; }],
    ['Preferred audio', function () { return langName(settings.audioLang) || 'File default'; }, function () { settings.audioLang = ''; toast('Audio language is now picked again from the next movie you change it in'); }],
    ['Preferred subtitles', function () { return settings.subLang ? langName(settings.subLang) : 'Off'; }, function () { settings.subLang = ''; }],
    ['Restart file service', function () { return ''; }, function () { api('/api/restart', {}).then(function () { toast('File service restarting…'); }).catch(function (e) { toast(e.message); }); }]
  ];
  rows.forEach(function (r) {
    var o = el('div', 'mi opt f'); o.style.paddingLeft = '1.2rem';
    var paint = function () { o.innerHTML = '<span>' + esc(r[0]) + '</span><span class="ov">' + esc(r[1]()) + '</span>'; };
    paint(); o._act = function () { r[2](); saveSettings(); paint(); };
    m.appendChild(o);
  });
  var info = el('div', 'msg'); info.style.marginTop = '1.2rem'; m.appendChild(info);
  api('/api/health').then(function (h) {
    info.textContent = 'File service ' + h.version + ' · running ' + fmtTime(h.uptime) + ' · thumbnails, subtitles and stream info ' + (h.ffmpeg ? 'available' : 'unavailable (ffmpeg missing)');
    if (h.ffmpeg) return;
    var o = el('div', 'mi opt f', '<span>Download ffmpeg</span><span class="ov">19 MB</span>'); o.style.paddingLeft = '1.2rem';
    o._act = function () { dialog.close(); setup.offerFfmpeg(h, true); };
    m.insertBefore(o, info);
  }).catch(function (e) { info.textContent = e.message; });
  var bb = el('div', 'btns'); bb.appendChild(btn('Close', function () { dialog.close(); if (screen === 'home') home.render(); }, 'primary')); m.appendChild(bb);
  dialog.show(m, { onBack: function () { if (screen === 'home') home.render(); } });
}
function applyTheme() { document.documentElement.setAttribute('data-theme', settings.theme); }

// ---------- player ----------
var player = {
  on: false, mode: '', src: '', srcName: '', dir: '', list: [], idx: 0, entry: null, path: '',
  media: null, probe: null, userPaused: false, attempts: 0, lastT: -1, lastMove: 0, healthyAt: 0, recovering: false,
  osdTimer: null, seekTarget: null, seekTimer: null, seekCount: 0, seekAt: 0, saveAt: 0, subCues: null, subId: '', subTimer: null, subPoll: null, nextTimer: null, wd: null,

  openPath: function (src, srcName, path) {
    var self = this, dir = parentPath(path), name = baseName(path);
    api('/api/list?' + q({ src: src, path: dir })).then(function (d) {
      var k = kindOf({ name: name }), all = (d.entries || []).filter(function (x) { return kindOf(x) === k; });
      all.sort(function (a, b) { return collator.compare(a.name, b.name); });
      var e = all.filter(function (x) { return x.name === name; })[0];
      if (!e) throw new Error('The file is gone');
      self.openList(src, srcName, dir, all, e);
    }).catch(function (e) { toast(e.message); });
  },
  openList: function (src, srcName, dir, list, entry) {
    this.src = src; this.srcName = srcName; this.dir = dir; this.list = list; this.idx = list.indexOf(entry);
    this.prevFocus = focusEl; this.on = true;
    $('#player').className = 'on';
    this.start(entry);
  },
  start: function (e) {
    var self = this, k = kindOf(e), p = $('#player');
    this.stopMedia();
    this.entry = e; this.path = joinPath(this.dir, e.name); this.mode = k; this.probe = null;
    this.userPaused = false; this.attempts = 0; this.lastT = -1; this.lastMove = Date.now(); this.recovering = false;
    p.className = 'on mode-' + k + ' sub-' + settings.subSize;
    $('#ptitle').textContent = e.name;
    $('#psub').textContent = [this.srcName].concat(this.dir ? this.dir.split('/') : []).join(' › ') + (this.list.length > 1 ? '   ·   ' + (this.idx + 1) + ' of ' + this.list.length : '');
    $('#pmsg').className = ''; $('#pnext').className = ''; $('#subtitle').textContent = ''; $('#pinfo').textContent = '';
    $('#paudio-v').textContent = '–'; $('#psubs-v').textContent = 'Off';
    if (k === 'image') { this.showImage(e); this.osd(true); return; }
    this.media = k === 'video' ? $('#video') : $('#audio');
    if (k === 'audio') {
      var art = $('#pcover-art'); art.innerHTML = ICON.audio; $('#pcover-title').textContent = stem(e.name);
      var im = new Image(); im.onload = function () { art.innerHTML = ''; art.appendChild(im); }; im.src = thumbURL(this.src, this.path, e);
    }
    var m = this.media, rp = recents.get(this.src, this.path);
    this.resumeAt = rp && !rp.done && rp.pos > 15 ? rp.pos : 0;
    p.classList.add('busy');
    m.src = fileURL(this.src, this.path);
    m.load();
    this.play();
    api('/api/probe?' + q({ src: this.src, path: this.path }), null, 30000).then(function (pr) {
      if (self.entry !== e) return;
      pr.audio = pr.audio || []; pr.subs = pr.subs || [];
      self.probe = pr; self.paintInfo(); self.applyPrefs();
    }).catch(function () {});
    this.osd(true, $('#pbar'));
    clearInterval(this.wd); this.wd = setInterval(function () { self.watchdog(); }, 1000);
  },
  showImage: function (e) {
    var self = this, img = $('#pimg'), p = $('#player');
    p.classList.add('busy');
    img.onload = function () { p.classList.remove('busy'); };
    img.onerror = function () { if (img.src.indexOf('/api/thumb') >= 0) img.src = fileURL(self.src, self.path); else { p.classList.remove('busy'); self.message('This picture cannot be shown'); } };
    // a screen-sized copy from the bridge: decodes fast and never stalls the UI
    img.src = extOf(e.name) === '.gif' ? fileURL(this.src, this.path) : thumbURL(this.src, this.path, e, 1920);
    [this.idx + 1, this.idx - 1].forEach(function (i) { var n = self.list[i]; if (n && extOf(n.name) !== '.gif') { var pre = new Image(); pre.src = thumbURL(self.src, joinPath(self.dir, n.name), n, 1920); } });
  },
  play: function () {
    var self = this, m = this.media; if (!m) return;
    this.userPaused = false;
    var pr = m.play();
    if (pr && pr.catch) pr.catch(function (err) { if (err && err.name === 'NotAllowedError') self.message('Press OK to start playback'); });
    this.syncIcon();
  },
  pause: function () { if (!this.media) return; this.userPaused = true; this.media.pause(); this.syncIcon(); this.savePos(); },
  toggle: function () { if (!this.media) return; if (this.media.paused) this.play(); else this.pause(); this.osd(true); },
  syncIcon: function () { $('#pplay').textContent = this.media && !this.media.paused ? '' : ''; },
  stopMedia: function () {
    clearInterval(this.subTimer); clearTimeout(this.subPoll); clearTimeout(this.nextTimer); this.subCues = null; this.subId = '';
    ['#video', '#audio'].forEach(function (s) { var m = $(s); try { m.pause(); m.removeAttribute('src'); m.load(); } catch (e) {} });
    var img = $('#pimg'); img.onload = img.onerror = null; img.removeAttribute('src');
    this.media = null;
  },
  close: function () {
    this.savePos();
    clearInterval(this.wd); clearTimeout(this.osdTimer); clearTimeout(this.seekTimer);
    this.stopMedia(); this.on = false; $('#player').className = '';
    if (screen === 'browser') { browser.render(this.entry && this.entry.name); }
    else { home.render(); }
  },
  savePos: function () {
    var m = this.media; if (!m || !this.entry || this.mode === 'image') return;
    if (m.currentTime > 5 && isFinite(m.duration)) recents.save(this.src, this.srcName, this.path, this.entry, m.currentTime, m.duration);
  },
  osd: function (show, focus) {
    var p = $('#player'), self = this;
    clearTimeout(this.osdTimer);
    if (show) {
      p.classList.add('osd-on');
      if (focus) setFocus(focus, true);
      else if (!focusEl || !p.contains(focusEl) || !visible(focusEl)) setFocus(this.mode === 'image' ? null : $('#pbar'), true);
      this.paintBar();
      this.osdTimer = setTimeout(function () { if (!self.media || !self.media.paused || self.mode === 'image') self.osd(false); }, this.mode === 'image' ? 2500 : 4500);
    } else p.classList.remove('osd-on');
  },
  osdOn: function () { return $('#player').classList.contains('osd-on'); },
  paintBar: function () {
    var m = this.media; if (!m) return;
    var d = isFinite(m.duration) ? m.duration : 0, t = this.seekTarget != null ? this.seekTarget : m.currentTime;
    var pc = d ? Math.min(100, t / d * 100) : 0;
    $('#pplayed').style.width = pc + '%'; $('#pknob').style.left = pc + '%';
    var b = 0; try { for (var i = 0; i < m.buffered.length; i++) if (m.buffered.start(i) <= m.currentTime + 1) b = m.buffered.end(i); } catch (e) {}
    $('#pbuf').style.width = (d ? Math.min(100, b / d * 100) : 0) + '%';
    $('#pcur').textContent = fmtTime(t); $('#pdur').textContent = d ? fmtTime(d) : '';
    var ps = $('#pseek');
    if (this.seekTarget != null) { ps.style.display = 'block'; ps.style.left = pc + '%'; ps.textContent = fmtTime(this.seekTarget); } else ps.style.display = 'none';
  },
  paintInfo: function () {
    var p = this.probe; if (!p) return;
    var v = p.video;
    $('#pinfo').textContent = v ? [v.height >= 2000 ? '4K' : v.height >= 1000 ? '1080p' : v.height >= 700 ? '720p' : v.height + 'p', v.codec.toUpperCase(), v.hdr || ''].filter(Boolean).join(' · ') : '';
    this.paintTracks();
  },
  audioLabel: function (i) {
    var a = this.probe && this.probe.audio[i], t = this.media && this.media.audioTracks && this.media.audioTracks[i];
    var lang = (a && a.lang) || (t && t.language) || '';
    return [langName(lang) || 'Track ' + (i + 1), a && a.title, a && (a.codec.toUpperCase() + ' ' + chLabel(a.channels))].filter(Boolean).join(' · ');
  },
  currentAudio: function () { var t = this.media && this.media.audioTracks; if (!t) return 0; for (var i = 0; i < t.length; i++) if (t[i].enabled) return i; return 0; },
  paintTracks: function () {
    // compact corner chips: language code and track title; full names are in the menus
    var n = this.media && this.media.audioTracks ? this.media.audioTracks.length : 0, i = this.currentAudio();
    var a = this.probe && this.probe.audio[i], t = n ? this.media.audioTracks[i] : null;
    var code = langKey((a && a.lang) || (t && t.language) || '').toUpperCase();
    $('#paudio-v').textContent = n || a ? [code || 'Track ' + (i + 1), a && a.title].filter(Boolean).join(' · ') : '–';
    var s = this.subId && this.probe && this.probe.subs.filter(function (x) { return x.id === this.subId; }, this)[0];
    $('#psubs-v').textContent = s ? [langKey(s.lang).toUpperCase() || '?', s.external ? 'file' : s.title && s.title !== s.lang ? s.title : s.forced ? 'forced' : ''].filter(Boolean).join(' · ') : 'Off';
    $('#paudio').classList.toggle('dim', n < 2);
  },
  subLabel: function (s) { return [langName(s.lang) || 'Unknown', s.title && s.title !== s.lang && !s.external ? s.title : '', s.forced ? 'forced' : '', s.external ? 'file' : ''].filter(Boolean).join(' · '); },
  setAudio: function (i) {
    var t = this.media && this.media.audioTracks; if (!t || !t[i]) return;
    for (var j = 0; j < t.length; j++) t[j].enabled = j === i;
    var a = this.probe && this.probe.audio[i]; settings.audioLang = langKey((a && a.lang) || t[i].language); saveSettings();
    this.paintTracks(); toast('Audio: ' + this.audioLabel(i));
  },
  audioMenu: function () {
    var self = this, t = this.media && this.media.audioTracks, n = t ? t.length : 0;
    if (!n) { toast(this.mode === 'video' ? 'This file has one audio track' : 'No audio tracks'); return; }
    var cur = this.currentAudio(), items = [];
    for (var i = 0; i < n; i++) (function (i) { items.push({ label: self.audioLabel(i), sel: i === cur, act: function () { self.setAudio(i); } }); })(i);
    menu('Audio language', items);
  },
  subsMenu: function () {
    var self = this, subs = this.probe ? this.probe.subs : [];
    var items = [{ label: 'Off', sel: !this.subId, act: function () { self.setSubs(null); } }];
    subs.forEach(function (s) {
      items.push({ label: self.subLabel(s), sub: s.text ? (s.codec || '').toUpperCase() : 'picture subtitles – not supported', sel: s.id === self.subId, dis: !s.text, why: 'Picture-based subtitles (PGS/VobSub) cannot be shown', act: function () { self.setSubs(s); } });
    });
    if (!this.probe) items.push({ label: 'Reading the file…', dis: true, why: 'Still reading the file, try again in a moment' });
    else if (!subs.length) items.push({ label: 'This file has no subtitles', dis: true });
    menu('Subtitles', items);
  },
  setSubs: function (s, auto) {
    var self = this, e = this.entry;
    clearInterval(this.subTimer); clearTimeout(this.subPoll); this.subCues = null; $('#subtitle').textContent = '';
    this.subId = s ? s.id : '';
    if (!auto) { settings.subLang = s ? langKey(s.lang) : ''; saveSettings(); }
    this.paintTracks();
    if (!s) { if (!auto) toast('Subtitles off'); return; }
    if (!auto) toast('Subtitles: ' + this.subLabel(s));
    var cues = [];
    var fetchMore = function () {
      api('/api/subs?' + q({ src: self.src, path: self.path, id: s.id, from: cues.length }), null, 30000).then(function (d) {
        if (self.entry !== e || self.subId !== s.id) return;
        cues = cues.concat(d.cues); self.subCues = cues;
        if (!d.done) self.subPoll = setTimeout(fetchMore, cues.length ? 6000 : 2500);
      }).catch(function (err) { if (self.subId === s.id) toast('Subtitles: ' + err.message); });
    };
    fetchMore();
    var last = '';
    this.subTimer = setInterval(function () {
      var m = self.media, c = self.subCues; if (!m || !c) return;
      var t = m.currentTime, lo = 0, hi = c.length - 1, hit = null;
      while (lo <= hi) { var mid = (lo + hi) >> 1; if (c[mid][0] <= t) { lo = mid + 1; } else hi = mid - 1; }
      for (var i = hi; i >= 0 && i > hi - 4; i--) if (c[i][0] <= t && t < c[i][1]) { hit = c[i][2]; break; }
      var txt = hit || '';
      if (txt !== last) { last = txt; $('#subtitle').textContent = txt; }
    }, 150);
  },
  applyPrefs: function () {
    var self = this, m = this.media, p = this.probe;
    if (settings.audioLang && m && m.audioTracks && m.audioTracks.length > 1) {
      for (var i = 0; i < m.audioTracks.length; i++) {
        var a = p.audio[i], lang = langKey((a && a.lang) || m.audioTracks[i].language);
        if (lang === settings.audioLang) { if (!m.audioTracks[i].enabled) { for (var j = 0; j < m.audioTracks.length; j++) m.audioTracks[j].enabled = j === i; } break; }
      }
    }
    if (settings.subLang && p.subs.length) {
      var pick = p.subs.filter(function (s) { return s.text && !s.forced && langKey(s.lang) === settings.subLang; })[0] || p.subs.filter(function (s) { return s.text && langKey(s.lang) === settings.subLang; })[0];
      if (pick) this.setSubs(pick, true);
    }
    setTimeout(function () { self.paintTracks(); }, 300);
  },
  seekBy: function (delta) {
    var m = this.media; if (!m || !isFinite(m.duration)) return;
    var now = Date.now();
    this.seekCount = now - this.seekAt < 600 ? this.seekCount + 1 : 0; this.seekAt = now;
    var step = this.seekCount > 8 ? 120 : this.seekCount > 3 ? 30 : 10;
    var base = this.seekTarget != null ? this.seekTarget : m.currentTime;
    this.seekTarget = Math.max(0, Math.min(m.duration - 2, base + delta * step));
    this.osd(true, $('#pbar'));
    var self = this; clearTimeout(this.seekTimer);
    // one seek after the presses stop, instead of one network seek per press
    this.seekTimer = setTimeout(function () { if (self.seekTarget != null && self.media) { self.media.currentTime = self.seekTarget; } self.seekTarget = null; self.lastMove = Date.now(); self.paintBar(); }, 650);
  },
  next: function (d) {
    var i = this.idx + d;
    if (i < 0 || i >= this.list.length) { toast(d > 0 ? 'This was the last one in the folder' : 'This is the first one in the folder'); return; }
    this.savePos(); this.idx = i; this.start(this.list[i]);
  },
  ended: function () {
    var self = this; this.savePos();
    if (this.idx + 1 >= this.list.length) { this.osd(true); return; }
    var n = this.list[this.idx + 1], left = 8, box = $('#pnext');
    var paint = function () { box.innerHTML = 'Up next in ' + left + ' s<br><b>' + esc(n.name) + '</b><br><span style="color:#aeb7c1">OK play now · BACK cancel</span>'; };
    paint(); box.className = 'on';
    var tickNext = function () { left--; if (left <= 0) { box.className = ''; self.next(1); } else { paint(); self.nextTimer = setTimeout(tickNext, 1000); } };
    this.nextTimer = setTimeout(tickNext, 1000);
  },
  message: function (html, buttons) {
    var b = $('#pmsg'); b.innerHTML = html; b.className = 'on';
    if (buttons) { var bb = el('div', 'btns'); bb.style.justifyContent = 'center'; buttons.forEach(function (x) { bb.appendChild(x); }); b.appendChild(bb); setFocus(bb.querySelector('.f')); }
  },
  // Watchdog: if playback stops moving without the user pausing it, reload the
  // stream at the same spot; if that fails, restart the file service and retry.
  watchdog: function () {
    var m = this.media; if (!m || this.mode === 'image' || !this.on) return;
    var p = $('#player');
    if (m.readyState >= 3 && !m.seeking) p.classList.remove('busy'); else if (!m.paused || this.recovering) p.classList.add('busy');
    var t = m.currentTime;
    if (t !== this.lastT) { this.lastT = t; this.lastMove = Date.now(); if (!this.healthyAt) this.healthyAt = Date.now(); }
    if (this.osdOn()) this.paintBar();
    if (Date.now() - this.saveAt > 10000) { this.saveAt = Date.now(); this.savePos(); }
    if (this.healthyAt && Date.now() - this.healthyAt > 30000) this.attempts = 0;
    if (m.ended || this.userPaused || this.recovering || this.seekTarget != null) return;
    if (m.paused && m.readyState >= 2 && Date.now() - this.lastMove > 1500) { this.play(); return; } // webOS paused us (decoder taken): take it back
    if (Date.now() - this.lastMove > 12000) this.recover('Playback stalled');
  },
  recover: function (why) {
    var self = this, m = this.media; if (!m) return;
    var pos = Math.max(this.lastT, this.resumeAt || 0, 0), e = this.entry;
    this.attempts++; this.recovering = true; this.healthyAt = 0;
    if (this.attempts > 3) {
      this.recovering = false;
      this.message('<b>Cannot play this file</b><br>' + esc(why) + (this.probe && this.probe.video ? '<br><span style="color:#aeb7c1">' + esc(this.probe.video.codec.toUpperCase() + ' ' + this.probe.video.width + '×' + this.probe.video.height) + '</span>' : ''),
        [btn('Try again', function () { $('#pmsg').className = ''; self.attempts = 0; self.reload(pos); }, 'primary'), btn('Close', function () { self.close(); })]);
      return;
    }
    var restart = this.attempts >= 2;
    toast(restart ? 'Restarting the file service…' : 'Reconnecting…');
    var go = function () { if (self.entry !== e || !self.on) return; self.reload(pos); };
    if (!restart) { go(); return; }
    api('/api/restart', {}, 4000).catch(function () {}).then(function () {
      var tries = 0;
      var waitUp = function () { tries++; api('/api/health', null, 1500).then(go, function () { if (tries < 20) setTimeout(waitUp, 750); else go(); }); };
      setTimeout(waitUp, 1200);
    });
  },
  reload: function (pos) {
    var m = this.media, self = this; if (!m) return;
    this.resumeAt = pos; this.lastMove = Date.now(); this.lastT = -1;
    m.pause(); m.removeAttribute('src'); m.load();
    setTimeout(function () {
      if (!self.media) return;
      m.src = fileURL(self.src, self.path) + '&retry=' + self.attempts; m.load(); self.play();
      setTimeout(function () { self.recovering = false; self.lastMove = Date.now(); }, 1500);
    }, 400);
  },
  key: function (k, ev) {
    var osd = this.osdOn(), f = focusEl;
    if ($('#pnext').className === 'on') {
      if (k === KEY.OK) { clearTimeout(this.nextTimer); $('#pnext').className = ''; this.next(1); return true; }
      if (k === KEY.BACK) { clearTimeout(this.nextTimer); $('#pnext').className = ''; this.osd(true); return true; }
    }
    if ($('#pmsg').className === 'on' && $('#pmsg').querySelector('.f')) {
      if (k === KEY.BACK) { this.close(); return true; }
      return false; // let the message buttons navigate
    }
    if (k === KEY.BACK) { if (osd && this.mode !== 'image') { this.osd(false); return true; } this.close(); return true; }
    if (k === KEY.PLAY) { this.play(); this.osd(true); return true; }
    if (k === KEY.PAUSE) { this.pause(); this.osd(true); return true; }
    if (k === KEY.STOP) { this.close(); return true; }
    if (k === KEY.RED) { this.audioMenu(); return true; }
    if (k === KEY.GREEN) { this.subsMenu(); return true; }
    if (k === KEY.CH_UP || k === KEY.YELLOW) { this.next(-1); return true; }
    if (k === KEY.CH_DOWN || k === KEY.BLUE) { this.next(1); return true; }
    if (this.mode === 'image') {
      if (k === KEY.LEFT) { this.next(-1); return true; }
      if (k === KEY.RIGHT) { this.next(1); return true; }
      if (k === KEY.OK || k === KEY.UP || k === KEY.DOWN || k === KEY.INFO) { if (osd) this.osd(false); else this.osd(true); return true; }
      return true;
    }
    if (k === KEY.REW) { this.seekBy(-3); return true; }
    if (k === KEY.FF) { this.seekBy(3); return true; }
    if (!osd) {
      if (k === KEY.LEFT) { this.seekBy(-1); return true; }
      if (k === KEY.RIGHT) { this.seekBy(1); return true; }
      if (k === KEY.OK) { this.toggle(); return true; }
      if (k === KEY.UP) { this.osd(true, this.mode === 'video' ? $('#psubs') : $('#pbar')); return true; }
      if (k === KEY.DOWN) { this.osd(true, $('#pplay')); return true; }
      if (k === KEY.INFO) { this.osd(true); return true; }
      return true;
    }
    this.osd(true, f);
    if (f && f.id === 'pbar') {
      if (k === KEY.LEFT) { this.seekBy(-1); return true; }
      if (k === KEY.RIGHT) { this.seekBy(1); return true; }
      if (k === KEY.OK) { this.toggle(); return true; }
      if (k === KEY.UP && this.mode === 'video') { setFocus($('#psubs'), true); return true; }
      if (k === KEY.DOWN) { setFocus($('#pplay'), true); return true; }
    }
    if (f && f.id === 'psubs' && k === KEY.DOWN) { setFocus($('#pbar'), true); return true; }
    return false; // spatial navigation and OK on the focused control
  }
};
(function wirePlayer() {
  ['#video', '#audio'].forEach(function (s) {
    var m = $(s);
    m.addEventListener('loadedmetadata', function () {
      if (player.media !== m) return;
      if (player.resumeAt > 0 && isFinite(m.duration) && player.resumeAt < m.duration - 5) {
        m.currentTime = player.resumeAt;
        if (player.attempts === 0) toast('Resumed at ' + fmtTime(player.resumeAt) + ' · hold ◀ to go back to the start', 3500);
      }
      player.resumeAt = 0; player.paintBar(); player.paintTracks();
      if (player.probe) player.applyPrefs();
    });
    m.addEventListener('playing', function () { if (player.media === m) { $('#player').classList.remove('busy'); $('#pmsg').className = ''; player.syncIcon(); } });
    m.addEventListener('pause', function () { if (player.media === m) player.syncIcon(); });
    m.addEventListener('waiting', function () { if (player.media === m) $('#player').classList.add('busy'); });
    m.addEventListener('ended', function () { if (player.media === m) player.ended(); });
    m.addEventListener('error', function () {
      if (player.media !== m || !m.getAttribute('src')) return;
      var c = m.error ? m.error.code : 0;
      player.recover(c === 4 ? 'The TV could not open this stream' : c === 3 ? 'The TV could not decode this file' : 'Network error');
    });
    if (s === '#video') m.addEventListener('loadeddata', function () { if (player.media === m) player.paintTracks(); });
  });
  $('#pplay')._act = function () { player.toggle(); };
  $('#prew')._act = function () { player.seekBy(-1); };
  $('#pfwd')._act = function () { player.seekBy(1); };
  $('#pprev')._act = function () { player.next(-1); };
  $('#pnextb')._act = function () { player.next(1); };
  $('#paudio')._act = function () { player.audioMenu(); };
  $('#psubs')._act = function () { player.subsMenu(); };
  $('#pbar')._act = function () { player.toggle(); };
})();

// ---------- key & pointer dispatch ----------
function layerRoot() { if (dialog.on) return $('#dialog'); if (player.on) return $('#player'); return $('#app'); }
document.addEventListener('keydown', function (ev) {
  var k = ev.keyCode, ae = document.activeElement;
  if (ae && ae.tagName === 'INPUT') {
    if (k === KEY.OK || k === KEY.BACK || k === KEY.ESC || k === KEY.UP || k === KEY.DOWN) { ev.preventDefault(); ae.blur(); if (k === KEY.UP || k === KEY.DOWN) spatial(k === KEY.UP ? 'up' : 'down', layerRoot()); }
    return; // typing and the on-screen keyboard
  }
  var handled = false;
  if (dialog.on) handled = dialog.key(k);
  else if (player.on) handled = player.key(k, ev);
  else if (screen === 'home') handled = home.key(k);
  else handled = browser.key(k);
  if (!handled) {
    var dir = k === KEY.LEFT ? 'left' : k === KEY.RIGHT ? 'right' : k === KEY.UP ? 'up' : k === KEY.DOWN ? 'down' : '';
    if (dir) { if (screen === 'home' && !player.on && !dialog.on) home.moved = true; handled = listMove(dir) || spatial(dir, layerRoot()); handled = true; }
    else if (k === KEY.OK) { lastOkAt = Date.now(); activate(focusEl); handled = true; }
  }
  if (player.on && !dialog.on && !$('#player').classList.contains('osd-on') && handled) { /* keys while OSD hidden are player shortcuts */ }
  if (handled) { ev.preventDefault(); ev.stopPropagation(); }
}, true);
// Magic Remote pointer: hover focuses, click activates
document.addEventListener('mouseover', function (ev) {
  var f = ev.target.closest && ev.target.closest('.f'); if (!f || f === focusEl || !layerRoot().contains(f)) return;
  setFocus(f, true); if (player.on) player.osd(true, f);
});
document.addEventListener('click', function (ev) {
  if (Date.now() - lastOkAt < 400) return; // OK already handled as a key
  var f = ev.target.closest && ev.target.closest('.f');
  if (player.on && !dialog.on && !f) { player.osd(true); return; }
  if (!f || !layerRoot().contains(f)) return;
  setFocus(f, true); activate(f);
});
document.addEventListener('mousemove', debounce(function () { if (player.on && !dialog.on) player.osd(true); }, 100));
document.addEventListener('visibilitychange', function () {
  if (document.hidden) { if (player.on && player.media && !player.media.paused) { player.pause(); } player.savePos(); }
  else if (screen === 'home' && !player.on) home.refresh(false, true);
});
$('#tb-back')._act = function () { browser.up(); };

applyTheme(); tick(); setInterval(tick, 30000);
home.open();
setup.check();
