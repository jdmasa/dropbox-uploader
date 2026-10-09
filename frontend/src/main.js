import './style.css';
import { icon, hydrateIcons } from './icons.js';
import { t, tn, tErr, setLang, getLang, applyStatic, LANGS } from './i18n.js';

// Wails bindings (window.go.main.App.*) and runtime (window.runtime.*).
const api = () => window.go.main.App;
const rt = () => window.runtime;
const $ = (id) => document.getElementById(id);

const state = {
  app: null,
  places: [],
  local: null,          // current local listing
  sel: new Set(),       // selected local paths
  anchor: -1,           // index for shift-click range selection
  view: 'grid',
  dbx: null,            // current Dropbox listing
  dbxPath: '',
  q: { items: new Map(), order: [], summary: null },
  tab: 'uploading',
  uploading: false,
};

// ---------- helpers ----------

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

function fmtBytes(n) {
  if (!n) return '0 KB';
  const u = ['bytes', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
}

function fmtDuration(sec) {
  if (!sec || !isFinite(sec)) return '';
  if (sec < 60) return t('dur.lessMin');
  const m = Math.round(sec / 60);
  if (m < 60) return t('dur.min', { m });
  return t('dur.hmin', { h: Math.floor(m / 60), m: m % 60 });
}

function fmtDate(s) {
  const d = new Date(s);
  if (isNaN(d) || d.getFullYear() < 1980) return '';
  return d.toLocaleDateString(getLang(), { year: 'numeric', month: 'short', day: 'numeric' });
}

// Go errors arrive as "err.code" or "err.code|detail" and are translated here.
function errText(e) {
  return tErr(e?.message ?? e);
}

function isNotConnected(e) {
  return String(e?.message ?? e ?? '').startsWith('err.notConnected');
}

function toast(msg, kind = '') {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.innerHTML = (kind === 'ok' ? icon('check') : kind === 'bad' ? icon('alert') : '') + `<span>${esc(msg)}</span>`;
  $('toasts').appendChild(el);
  setTimeout(() => el.remove(), kind === 'bad' ? 7000 : 4000);
}

function showError(e) {
  const msg = errText(e);
  if (isNotConnected(e)) {
    state.app.connected = false;
    showWelcome();
  }
  toast(msg, 'bad');
}

// Promise-based dialog: { title, text, input, okText, cancelText, danger }
function dialog(o) {
  return new Promise((resolve) => {
    $('dialogTitle').textContent = o.title || '';
    $('dialogText').textContent = o.text || '';
    $('dialogText').hidden = !o.text;
    const input = $('dialogInput');
    input.hidden = o.input === undefined;
    input.value = o.input ?? '';
    $('dialogOk').textContent = o.okText || t('common.ok');
    $('dialogOk').classList.toggle('danger', !!o.danger);
    $('dialogCancel').textContent = o.cancelText || t('common.cancel');
    $('dialog').hidden = false;
    (input.hidden ? $('dialogOk') : input).focus();
    const done = (ok) => {
      $('dialog').hidden = true;
      $('dialogOk').onclick = $('dialogCancel').onclick = input.onkeydown = null;
      resolve(ok ? (input.hidden ? true : input.value) : null);
    };
    $('dialogOk').onclick = () => done(true);
    $('dialogCancel').onclick = () => done(false);
    input.onkeydown = (e) => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); };
  });
}

const localThumb = (p) => `/local/thumb?p=${encodeURIComponent(p)}`;
const localFile = (p) => `/local/file?p=${encodeURIComponent(p)}`;
const dbxThumb = (p) => `/dropbox/thumb?p=${encodeURIComponent(p)}`;
const KIND_ICON = { folder: 'folder', image: 'image', video: 'video', other: 'file' };
const BROWSER_IMAGES = /\.(jpe?g|png|gif|webp|bmp)$/i;

// Load images/videos only when they scroll into view.
function makeLazy(root) {
  return new IntersectionObserver((entries, obs) => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      const el = e.target;
      if (el.dataset.src) { el.src = el.dataset.src; delete el.dataset.src; }
      obs.unobserve(el);
    }
  }, { root, rootMargin: '400px' });
}
const localLazy = makeLazy($('localFiles'));
const dbxLazy = makeLazy($('dbxFiles'));

// Fallbacks when a preview cannot be produced: image -> video frame -> icon.
function onMediaError(e) {
  const el = e.target;
  if (el.tagName !== 'IMG' && el.tagName !== 'VIDEO') return;
  const box = el.parentElement;
  if (!box) return;
  if (el.tagName === 'IMG' && box.dataset.kind === 'video' && box.dataset.path && !box.dataset.triedVideo) {
    box.dataset.triedVideo = '1';
    const v = document.createElement('video');
    v.muted = true; v.preload = 'metadata';
    v.src = localFile(box.dataset.path) + '#t=1';
    el.replaceWith(v);
    return;
  }
  el.outerHTML = icon(KIND_ICON[box.dataset.kind] || 'file');
}
$('localFiles').addEventListener('error', onMediaError, true);
$('dbxFiles').addEventListener('error', onMediaError, true);
$('queueList').addEventListener('error', onMediaError, true);

function mediaHTML(e, lazyAttr = 'data-src') {
  if (e.kind === 'folder') return icon('folder');
  if (e.thumb) return `<img ${lazyAttr}="${esc(localThumb(e.path))}" alt="">`;
  if (e.kind === 'video') return `<video ${lazyAttr}="${esc(localFile(e.path))}#t=1" muted preload="metadata"></video>`;
  return icon(KIND_ICON[e.kind] || 'file');
}

function webviewMajor() {
  const m = /(?:Edg|Chrome)\/(\d+)/.exec(navigator.userAgent);
  return m ? +m[1] : 0;
}

// ---------- startup ----------

async function init() {
  hydrateIcons();
  wireUI();
  state.app = await api().GetState();
  setLang(state.app.lang);
  fillLanguageSelects();
  applyStatic();
  state.view = state.app.viewMode === 'list' ? 'list' : 'grid';
  $('workers').value = String(state.app.workers);
  setViewButtons();
  restoreQueueHeight();

  rt().EventsOn('queue:update', applyQueueUpdate);
  rt().EventsOn('queue:alldone', onAllDone);
  // Dropping files from Explorer needs WebView2 113+; Windows 7/8.1 stop at 109.
  if (webviewMajor() >= 113 || state.app.platform !== 'windows') rt().OnFileDrop(onExternalDrop, true);
  else $('dropboxPanel').classList.remove('drop-target');
  applyQueueUpdate(await api().GetQueue());

  if (state.app.connected) {
    hideWelcome();
    await loadAll();
  } else {
    showWelcome();
    loadLocal(''); // the left side works even before connecting
    loadPlaces();
  }
}

async function loadAll() {
  updateAccount();
  await Promise.all([loadPlaces(), loadLocal(''), loadDropbox(state.app.lastDropbox || '')]);
}

function updateAccount() {
  const a = state.app;
  $('accountName').textContent = a.connected ? (a.accountName || t('account.connected')) : t('account.notConnected');
  $('accountEmail').textContent = a.connected ? t('account.connectedAs', { name: a.accountName + (a.accountEmail ? ` (${a.accountEmail})` : '') }) : t('account.notConnected');
  updateUploadButton();
}

// ---------- welcome / connect ----------

function showWelcome() {
  $('welcome').hidden = false;
  $('appKeyBox').hidden = !state.app.needsAppKey;
  $('appKeyHelp').innerHTML = t('welcome.appKeyHelp', { uri: esc(state.app.redirectUri) });
  $('connectBtn').disabled = state.app.needsAppKey;
  updateAccount();
}

function hideWelcome() { $('welcome').hidden = true; }

async function connect() {
  const btn = $('connectBtn');
  btn.disabled = true;
  btn.textContent = t('welcome.waiting');
  $('connectHint').textContent = t('welcome.hintWaiting');
  $('connectError').hidden = true;
  $('cancelConnect').hidden = false;
  try {
    state.app = await api().Connect();
    hideWelcome();
    await loadAll();
    toast(t('toast.connected', { name: state.app.accountName }), 'ok');
  } catch (e) {
    $('connectError').textContent = errText(e);
    $('connectError').hidden = false;
  } finally {
    $('cancelConnect').hidden = true;
    btn.disabled = false;
    btn.textContent = t('welcome.connect');
    $('connectHint').textContent = t('welcome.hint');
  }
}

// ---------- left panel: this computer ----------

async function loadPlaces() {
  try {
    state.places = await api().Places();
  } catch { state.places = []; }
  renderPlaces();
}

function renderPlaces() {
  const cur = state.local?.path || '';
  // Highlight the most specific place that contains the current folder.
  let best = null;
  for (const p of state.places) {
    if (cur === p.path || cur.startsWith(p.path.replace(/[\\/]$/, '') + (cur.includes('\\') ? '\\' : '/'))) {
      if (!best || p.path.length > best.path.length) best = p;
    }
  }
  $('places').innerHTML = state.places.map((p, i) =>
    `<button class="place ${best === p ? 'active' : ''}" data-i="${i}" title="${esc(p.path)}">${icon(p.icon)}<span>${esc(placeName(p))}</span></button>`).join('');
}

// Known folders are translated; drives show their volume label or a translated kind.
function placeName(p) {
  if (!p.drive) return t(`place.${p.icon}`);
  const name = p.name || t(`drive.${p.icon}`);
  return p.letter ? `${name} (${p.letter}:)` : name;
}

async function loadLocal(path) {
  const box = $('localFiles');
  if (!state.local) box.innerHTML = `<div class="loading">${t('local.loading')}</div>`;
  try {
    const l = await api().ListLocal(path);
    l.entries = l.entries || [];
    if (state.local?.path !== l.path) {
      state.sel.clear();
      state.anchor = -1;
      box.scrollTop = 0;
    } else {
      // Keep the selection for files that still exist.
      const still = new Set(l.entries.map((e) => e.path));
      for (const p of [...state.sel]) if (!still.has(p)) state.sel.delete(p);
    }
    state.local = l;
    renderLocal();
    renderPlaces();
  } catch (e) {
    if (!state.local) box.innerHTML = `<div class="error-msg">${esc(errText(e))}</div>`;
    else toast(errText(e), 'bad');
  }
}

function renderLocal() {
  const l = state.local;
  renderCrumbs($('localCrumbs'), l.crumbs, 'local');
  $('localUp').disabled = !l.parent;
  const box = $('localFiles');
  box.className = `files ${state.view}`;
  localLazy.disconnect();
  if (!l.entries.length) {
    box.innerHTML = `<div class="empty">${t('local.empty')}</div>`;
    updateSelection();
    return;
  }
  if (state.view === 'grid') {
    box.innerHTML = l.entries.map((e, i) => `
      <div class="tile ${e.kind} ${state.sel.has(e.path) ? 'selected' : ''}" data-i="${i}" draggable="true" title="${esc(e.name)}">
        <label class="sel"><input type="checkbox" tabindex="-1" ${state.sel.has(e.path) ? 'checked' : ''}></label>
        ${e.kind === 'folder' ? `<button class="open-btn" data-open="1">${t('local.open')}</button>` : ''}
        <div class="thumb" data-kind="${e.kind}" data-path="${esc(e.path)}">${mediaHTML(e)}
          ${e.kind === 'video' ? `<span class="badge">${icon('play')}${t('local.video')}</span>` : ''}</div>
        <div class="name">${esc(e.name)}</div>
        <div class="sub">${e.kind === 'folder' ? t('local.folder') : fmtBytes(e.size)}</div>
      </div>`).join('');
  } else {
    box.innerHTML = l.entries.map((e, i) => `
      <div class="row ${e.kind} ${state.sel.has(e.path) ? 'selected' : ''}" data-i="${i}" draggable="true">
        <input type="checkbox" tabindex="-1" ${state.sel.has(e.path) ? 'checked' : ''}>
        <div class="rthumb" data-kind="${e.kind}" data-path="${esc(e.path)}">${mediaHTML(e)}</div>
        <div class="rname">${esc(e.name)}</div>
        <div class="rsize">${e.kind === 'folder' ? '' : fmtBytes(e.size)}</div>
        <div class="rdate">${fmtDate(e.modTime)}</div>
      </div>`).join('');
  }
  box.querySelectorAll('[data-src]').forEach((el) => localLazy.observe(el));
  updateSelection();
}

function setSelected(i, on) {
  const e = state.local.entries[i];
  if (on) state.sel.add(e.path); else state.sel.delete(e.path);
  const el = $('localFiles').querySelector(`[data-i="${i}"]`);
  if (el) {
    el.classList.toggle('selected', on);
    el.querySelector('input').checked = on;
  }
}

function onLocalClick(ev) {
  const el = ev.target.closest('[data-i]');
  if (!el) return;
  const i = +el.dataset.i;
  const e = state.local.entries[i];
  if (ev.target.closest('[data-open]')) { loadLocal(e.path); return; }
  if (ev.shiftKey && state.anchor >= 0) {
    const [a, b] = [Math.min(state.anchor, i), Math.max(state.anchor, i)];
    for (let k = a; k <= b; k++) setSelected(k, true);
  } else {
    setSelected(i, !state.sel.has(e.path));
    state.anchor = i;
  }
  updateSelection();
}

function onLocalDblClick(ev) {
  const el = ev.target.closest('[data-i]');
  if (!el || ev.target.closest('.sel, input')) return;
  const e = state.local.entries[+el.dataset.i];
  if (e.kind === 'folder') loadLocal(e.path);
  else if (e.kind === 'image' || e.kind === 'video') openPreview(+el.dataset.i);
}

function selectAll(on) {
  state.local?.entries.forEach((_, i) => setSelected(i, on));
  updateSelection();
}

function selectedEntries() {
  return (state.local?.entries || []).filter((e) => state.sel.has(e.path));
}

function updateSelection() {
  const sel = selectedEntries();
  const folders = sel.filter((e) => e.kind === 'folder').length;
  const files = sel.length - folders;
  const bytes = sel.reduce((s, e) => s + (e.kind === 'folder' ? 0 : e.size), 0);
  const parts = [];
  if (files) parts.push(tn('sel.files', files, { size: fmtBytes(bytes) }));
  if (folders) parts.push(tn('sel.folders', folders));
  $('selectionInfo').textContent = sel.length ? t('sel.selected', { what: parts.join(t('sel.and')) }) : '';
  $('clearSel').hidden = !sel.length;
  const total = state.local?.entries.length || 0;
  $('selectAll').checked = total > 0 && sel.length === total;
  $('selectAll').indeterminate = sel.length > 0 && sel.length < total;
  updateUploadButton();
}

function updateUploadButton() {
  const n = state.sel.size;
  const connected = state.app?.connected;
  $('uploadBtn').disabled = !n || !connected || state.uploading;
  const dest = state.dbxPath ? state.dbxPath.split('/').pop() : 'Dropbox';
  $('uploadCaption').innerHTML = !connected ? esc(t('upload.captionNotConnected'))
    : n ? tn('upload.caption', n, { dest: esc(dest) })
      : esc(t('upload.captionNone'));
}

// ---------- right panel: Dropbox ----------

async function loadDropbox(path) {
  const box = $('dbxFiles');
  box.innerHTML = `<div class="loading">${t('dbx.loading')}</div>`;
  try {
    const l = await api().ListDropbox(path);
    l.entries = l.entries || [];
    state.dbx = l;
    state.dbxPath = l.path;
    renderDropbox();
  } catch (e) {
    box.innerHTML = `<div class="error-msg">${esc(errText(e))}</div>`;
    if (isNotConnected(e)) showError(e);
  }
}

function renderDropbox() {
  const l = state.dbx;
  renderCrumbs($('dbxCrumbs'), l.crumbs, 'dbx');
  $('dbxUp').disabled = !l.path;
  const destName = l.path || t('dbx.root');
  $('destPath').textContent = destName;
  $('dropDest').textContent = l.path ? l.path.split('/').pop() : 'Dropbox';
  updateUploadButton();
  const box = $('dbxFiles');
  dbxLazy.disconnect();
  if (!l.entries.length) {
    box.innerHTML = `<div class="empty">${t('dbx.empty')}</div>`;
    return;
  }
  box.innerHTML = l.entries.map((e, i) => `
    <div class="tile ${e.kind}" data-i="${i}" ${e.kind === 'folder' ? `data-path="${esc(e.path)}"` : ''} title="${esc(e.name)}${e.kind === 'folder' ? ` – ${t('dbx.clickToOpen')}` : ''}">
      <div class="thumb" data-kind="${e.kind}">${e.kind === 'folder' ? icon('folder')
        : e.thumb ? `<img data-src="${esc(dbxThumb(e.path))}" alt="">` : icon(KIND_ICON[e.kind] || 'file')}</div>
      <div class="name">${esc(e.name)}</div>
    </div>`).join('');
  box.querySelectorAll('[data-src]').forEach((el) => dbxLazy.observe(el));
}

function onDropboxClick(ev) {
  const el = ev.target.closest('[data-i]');
  if (!el) return;
  const e = state.dbx.entries[+el.dataset.i];
  if (e.kind === 'folder') loadDropbox(e.path);
}

function onDropboxDblClick(ev) {
  const el = ev.target.closest('[data-i]');
  if (!el) return;
  const e = state.dbx.entries[+el.dataset.i];
  if (e.kind !== 'folder') api().OpenInDropbox(e.path, false);
}

async function newFolder() {
  const name = await dialog({ title: t('dbx.newFolderTitle'), text: t('dbx.inside', { path: state.dbxPath || 'Dropbox' }), input: '', okText: t('dbx.create') });
  if (!name) return;
  try {
    const p = await api().CreateDropboxFolder(state.dbxPath, name);
    await loadDropbox(p);
    toast(t('dbx.folderCreated', { name }), 'ok');
  } catch (e) { showError(e); }
}

function renderCrumbs(nav, crumbs, side) {
  nav.innerHTML = (crumbs || []).map((c, i) =>
    (i ? `<span class="sep">${icon('chevronRight')}</span>` : '') +
    `<button data-side="${side}" data-path="${esc(c.path)}" title="${esc(c.path || 'Dropbox')}">${esc(c.name)}</button>`).join('');
  nav.scrollLeft = nav.scrollWidth;
}

// ---------- uploading ----------

async function enqueue(paths, dest) {
  if (!paths.length) return;
  if (!state.app.connected) { showWelcome(); return; }
  state.uploading = true;
  updateUploadButton();
  try {
    const n = await api().Enqueue(paths, dest);
    if (n === 0) toast(t('toast.alreadyQueued'));
    else toast(tn('toast.added', n), 'ok');
    expandQueue();
    setTab('uploading');
  } catch (e) {
    showError(e);
  } finally {
    state.uploading = false;
    updateUploadButton();
  }
}

async function uploadSelected() {
  const paths = selectedEntries().map((e) => e.path);
  await enqueue(paths, state.dbxPath);
  selectAll(false);
}

// Files dragged in from Windows Explorer (or the macOS Finder).
function onExternalDrop(x, y, paths) {
  $('dropboxPanel').classList.remove('dragging');
  const folder = document.elementFromPoint(x, y)?.closest('#dbxFiles .tile.folder');
  enqueue(paths || [], folder ? folder.dataset.path : state.dbxPath);
}

// Drag from the left panel to the right panel (inside the app).
function wireInternalDrag() {
  const left = $('localFiles');
  const right = $('dropboxPanel');
  left.addEventListener('dragstart', (ev) => {
    const el = ev.target.closest('[data-i]');
    if (!el) return;
    const i = +el.dataset.i;
    if (!state.sel.has(state.local.entries[i].path)) {
      selectAll(false);
      setSelected(i, true);
      updateSelection();
    }
    ev.dataTransfer.setData('text/x-local-paths', JSON.stringify(selectedEntries().map((e) => e.path)));
    ev.dataTransfer.effectAllowed = 'copy';
  });
  let depth = 0;
  right.addEventListener('dragenter', (ev) => {
    if (!ev.dataTransfer.types.includes('text/x-local-paths')) return;
    depth++;
    right.classList.add('dragging');
  });
  right.addEventListener('dragleave', () => {
    if (--depth <= 0) { depth = 0; right.classList.remove('dragging'); }
  });
  right.addEventListener('dragover', (ev) => {
    if (!ev.dataTransfer.types.includes('text/x-local-paths')) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = 'copy';
    right.querySelectorAll('.drop-hover').forEach((t) => t.classList.remove('drop-hover'));
    const folder = ev.target.closest('.tile.folder');
    if (folder) folder.classList.add('drop-hover');
    $('dropDest').textContent = folder ? folder.querySelector('.name').textContent : (state.dbxPath.split('/').pop() || 'Dropbox');
  });
  right.addEventListener('drop', (ev) => {
    const data = ev.dataTransfer.getData('text/x-local-paths');
    depth = 0;
    right.classList.remove('dragging');
    right.querySelectorAll('.drop-hover').forEach((t) => t.classList.remove('drop-hover'));
    if (!data) return;
    ev.preventDefault();
    const folder = ev.target.closest('.tile.folder');
    enqueue(JSON.parse(data), folder ? folder.dataset.path : state.dbxPath).then(() => selectAll(false));
  });
}

// ---------- preview ----------

let previewList = [];
let previewIdx = 0;

function openPreview(i) {
  previewList = state.local.entries.filter((e) => e.kind === 'image' || e.kind === 'video');
  previewIdx = Math.max(0, previewList.indexOf(state.local.entries[i]));
  $('preview').hidden = false;
  showPreview();
}

function showPreview() {
  const e = previewList[previewIdx];
  if (!e) return closePreview();
  const stage = $('previewStage');
  if (e.kind === 'video') {
    stage.innerHTML = `<video src="${esc(localFile(e.path))}" controls autoplay></video>`;
    stage.querySelector('video').onerror = () => {
      stage.innerHTML = `<div class="noprev">${icon('video')}<p>${t('preview.videoUnsupported')}</p></div>`;
    };
  } else if (BROWSER_IMAGES.test(e.name)) {
    stage.innerHTML = `<img src="${esc(localFile(e.path))}" alt="">`;
  } else {
    stage.innerHTML = e.thumb ? `<img src="${esc(localThumb(e.path))}" alt="">`
      : `<div class="noprev">${icon('image')}<p>${t('preview.noPreview')}</p></div>`;
  }
  $('previewCaption').textContent = t('preview.caption', { name: e.name, size: fmtBytes(e.size), i: previewIdx + 1, n: previewList.length });
  $('previewPrev').hidden = previewIdx === 0;
  $('previewNext').hidden = previewIdx >= previewList.length - 1;
}

function closePreview() {
  $('preview').hidden = true;
  $('previewStage').innerHTML = '';
}

// ---------- queue panel ----------

const ROW_H = 50;
const TAB_STATUSES = {
  uploading: ['checking', 'uploading', 'finishing'],
  queued: ['queued'],
  done: ['done'],
  skipped: ['skipped'],
  failed: ['failed'],
};
const rowEls = new Map();
let queueRenderPending = false;

function applyQueueUpdate(u) {
  const q = state.q;
  if (u.full) q.items.clear();
  for (const it of u.items || []) q.items.set(it.id, it);
  for (const id of u.removed || []) q.items.delete(id);
  if (u.order) q.order = u.order;
  else if (u.removed?.length) q.order = q.order.filter((id) => q.items.has(id));
  q.summary = u.summary;
  renderSummary();
  scheduleQueueRender();
}

function scheduleQueueRender() {
  if (queueRenderPending) return;
  queueRenderPending = true;
  requestAnimationFrame(() => { queueRenderPending = false; renderQueue(); });
}

function tabItems() {
  const want = TAB_STATUSES[state.tab];
  const out = [];
  for (const id of state.q.order) {
    const it = state.q.items.get(id);
    if (it && want.includes(it.status)) out.push(it);
  }
  return out;
}

function renderQueue() {
  const list = $('queueList');
  const items = tabItems();
  $('queueSpacer').style.height = `${items.length * ROW_H}px`;
  const emptyText = {
    uploading: state.q.summary?.paused ? t('queue.emptyPaused') : t('queue.emptyUploading'),
    queued: t('queue.emptyQueued'), done: t('queue.emptyDone'),
    skipped: t('queue.emptySkipped'), failed: t('queue.emptyFailed'),
  }[state.tab];
  $('queueEmpty').textContent = emptyText;
  $('queueEmpty').hidden = items.length > 0;

  const first = Math.max(0, Math.floor(list.scrollTop / ROW_H) - 4);
  const last = Math.min(items.length, first + Math.ceil(list.clientHeight / ROW_H) + 8);
  const keep = new Set();
  for (let i = first; i < last; i++) {
    const it = items[i];
    keep.add(it.id);
    let el = rowEls.get(it.id);
    if (!el) {
      el = createRow(it);
      rowEls.set(it.id, el);
      list.appendChild(el);
    }
    el.style.top = `${i * ROW_H}px`;
    updateRow(el, it);
  }
  for (const [id, el] of rowEls) {
    if (!keep.has(id)) { el.remove(); rowEls.delete(id); }
  }
}

function itemKind(it) {
  const n = it.name.toLowerCase();
  if (/\.(mp4|m4v|mov|webm|avi|mkv|3gp|mts|m2ts|wmv|mpg|mpeg)$/.test(n)) return 'video';
  if (/\.(jpe?g|png|gif|webp|bmp|heic|heif|cr2|nef|arw|dng|raf|orf|tiff?)$/.test(n)) return 'image';
  return 'other';
}

function createRow(it) {
  const el = document.createElement('div');
  el.className = 'qrow';
  el.dataset.id = it.id;
  const kind = itemKind(it);
  const canThumb = kind !== 'other' && (state.app?.platform === 'windows' || BROWSER_IMAGES.test(it.name));
  el.innerHTML = `
    <div class="qthumb" data-kind="${kind}">${canThumb ? `<img src="${esc(localThumb(it.local))}" loading="lazy" alt="">` : icon(KIND_ICON[kind])}</div>
    <div class="qname"><div class="n"></div><div class="d"></div></div>
    <div class="qsize"></div>
    <div class="qprog"><div class="progress"><div class="bar"></div></div><div class="t"><span class="pct"></span><span class="spd"></span></div></div>
    <div class="qstatus"></div>
    <div class="qactions"></div>`;
  el.querySelector('.n').textContent = it.name;
  el.querySelector('.n').title = it.local;
  return el;
}

const STATUS_VIEW = {
  queued: ['', ''],
  checking: ['busy', 'refresh'],
  uploading: ['busy', 'upload'],
  finishing: ['busy', 'cloud'],
  done: ['ok', 'check'],
  skipped: ['ok', 'check'],
  failed: ['bad', 'alert'],
};

function updateRow(el, it) {
  const key = `${it.status}|${it.sent}|${Math.round(it.speed)}|${it.error}|${it.resultPath}`;
  if (el._key === key) return;
  el._key = key;
  const dest = it.resultPath || it.dest;
  el.querySelector('.d').textContent = t('queue.to', { folder: dest.substring(0, dest.lastIndexOf('/')) || '/' });
  el.querySelector('.qsize').textContent = fmtBytes(it.size);
  const pct = it.size ? Math.min(100, Math.floor((it.sent / it.size) * 100)) : (['done', 'skipped', 'finishing'].includes(it.status) ? 100 : 0);
  const prog = el.querySelector('.progress');
  prog.classList.toggle('done', it.status === 'done' || it.status === 'skipped');
  prog.querySelector('.bar').style.width = `${pct}%`;
  el.querySelector('.pct').textContent = `${pct}%`;
  el.querySelector('.spd').textContent = it.status === 'uploading' && it.speed > 0 ? `${fmtBytes(it.speed)}/s` : '';
  const [cls, ic] = STATUS_VIEW[it.status] || ['', ''];
  const label = t(`status.${it.status}`);
  const st = el.querySelector('.qstatus');
  st.className = `qstatus ${cls}`;
  const text = it.status === 'failed' && it.error ? tErr(it.error) : label;
  st.innerHTML = (ic ? icon(ic) : '') + `<span>${esc(text)}</span>`;
  st.title = text;
  const acts = [];
  if (it.status === 'queued') acts.push(['top', 'top', 'act.top'], ['remove', 'close', 'act.remove']);
  if (it.status === 'checking' || it.status === 'uploading') acts.push(['remove', 'close', 'act.cancel']);
  if (it.status === 'failed') acts.push(['retry', 'refresh', 'act.retry'], ['remove', 'close', 'act.remove']);
  if (it.status === 'done' || it.status === 'skipped') acts.push(['open', 'external', 'act.open'], ['remove', 'close', 'act.remove']);
  el.querySelector('.qactions').innerHTML = acts.map(([a, ic2, k]) => `<button data-act="${a}" title="${esc(t(k))}">${icon(ic2)}</button>`).join('');
}

function onQueueClick(ev) {
  const b = ev.target.closest('[data-act]');
  if (!b) return;
  const id = +b.closest('.qrow').dataset.id;
  const it = state.q.items.get(id);
  switch (b.dataset.act) {
    case 'top': api().MoveToTop([id]); break;
    case 'remove': api().Remove([id]); break;
    case 'retry': api().Retry([id]); break;
    case 'open': if (it) api().OpenInDropbox(it.resultPath || it.dest, false); break;
  }
}

function renderSummary() {
  const s = state.q.summary;
  if (!s) return;
  const total = s.queued + s.uploading + s.done + s.skipped + s.failed;
  const finished = s.done + s.skipped;
  const pct = s.totalBytes ? Math.floor((s.sentBytes / s.totalBytes) * 100) : 0;
  let title;
  if (!total) title = t('queue.none');
  else if (s.paused && (s.queued || s.uploading)) title = t('sum.paused', { done: finished, total });
  else if (s.busy) title = t('sum.uploading', { done: finished, total });
  else if (s.failed) title = tn('sum.failed', s.failed);
  else title = tn('sum.allDone', finished);
  $('sumTitle').textContent = title;
  const parts = [];
  if (total) parts.push(t('sum.bytes', { sent: fmtBytes(s.sentBytes), total: fmtBytes(s.totalBytes), pct }));
  if (s.busy && !s.paused && s.speed > 0) parts.push(`${fmtBytes(s.speed)}/s`);
  if (s.busy && !s.paused && s.eta > 0) parts.push(t('sum.left', { time: fmtDuration(s.eta) }));
  $('sumDetail').textContent = parts.join(' · ');
  const bar = $('sumBar');
  bar.style.width = `${total ? pct : 0}%`;
  bar.parentElement.classList.toggle('done', total > 0 && !s.busy && !s.failed && !(s.paused && s.queued));
  bar.parentElement.classList.toggle('paused', s.paused && (s.queued + s.uploading) > 0);

  const pending = s.queued + s.uploading;
  const pb = $('pauseBtn');
  pb.innerHTML = s.paused ? `${icon('play')} ${esc(t('queue.resume'))}` : `${icon('pause')} ${esc(t('queue.pause'))}`;
  pb.disabled = !pending && !s.paused;
  $('cancelAllBtn').disabled = !pending && !s.failed;

  for (const [id, n] of [['cUploading', s.uploading], ['cQueued', s.queued], ['cDone', s.done], ['cSkipped', s.skipped], ['cFailed', s.failed]]) {
    $(id).textContent = n;
    if (n) delete $(id).dataset.zero; else $(id).dataset.zero = '1';
  }
  $('retryFailedBtn').hidden = !s.failed;
  $('clearFinishedBtn').hidden = !finished;
  rt().WindowSetTitle(s.busy && total ? t('title.uploading', { pct, done: finished, total }) : 'Dropbox Uploader');
}

function setTab(tab) {
  state.tab = tab;
  document.querySelectorAll('.tabs > button[data-tab]').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  $('queueList').scrollTop = 0;
  for (const el of rowEls.values()) el.remove();
  rowEls.clear();
  renderQueue();
}

function onAllDone(s) {
  const msg = s.failed ? tn('alldone.failed', s.failed)
    : tn('alldone.ok', s.done) + (s.skipped ? t('alldone.skipped', { n: s.skipped }) : '') + '.';
  toast(msg, s.failed ? 'bad' : 'ok');
  setTab(s.failed ? 'failed' : 'done');
  if (state.dbx) loadDropbox(state.dbxPath);
}

function expandQueue() {
  $('queue').classList.remove('collapsed');
  try { localStorage.setItem('queueCollapsed', '0'); } catch {}
}

function restoreQueueHeight() {
  try {
    const h = +localStorage.getItem('queueH');
    if (h >= 120) document.documentElement.style.setProperty('--queue-h', `${h}px`);
    if (localStorage.getItem('queueCollapsed') === '1') $('queue').classList.add('collapsed');
  } catch {}
}

function wireQueueResize() {
  const handle = $('queueResize');
  handle.addEventListener('mousedown', (ev) => {
    ev.preventDefault();
    const startY = ev.clientY;
    const startH = $('queueBody').getBoundingClientRect().height;
    const move = (e) => {
      const h = Math.max(120, Math.min(window.innerHeight - 540, startH + (startY - e.clientY)));
      document.documentElement.style.setProperty('--queue-h', `${h}px`);
      scheduleQueueRender();
    };
    const up = () => {
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
      try { localStorage.setItem('queueH', String(Math.round($('queueBody').getBoundingClientRect().height))); } catch {}
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  });
}

// ---------- wiring ----------

function setViewButtons() {
  $('viewGrid').classList.toggle('active', state.view === 'grid');
  $('viewList').classList.toggle('active', state.view === 'list');
}

function fillLanguageSelects() {
  for (const id of ['language', 'welcomeLanguage']) {
    $(id).innerHTML = LANGS.map((l) => `<option value="${l.code}">${l.name}</option>`).join('');
    $(id).value = getLang();
  }
}

// Switch language and redraw everything that contains text.
async function changeLanguage(code) {
  setLang(code);
  fillLanguageSelects();
  api().SetLanguage(code);
  applyStatic();
  updateAccount();
  if (!$('welcome').hidden) showWelcome();
  renderPlaces();
  if (state.local) renderLocal();
  if (state.dbx) renderDropbox();
  renderSummary();
  for (const el of rowEls.values()) el.remove();
  rowEls.clear();
  renderQueue();
}

function wireUI() {
  $('language').onchange = (e) => changeLanguage(e.target.value);
  $('welcomeLanguage').onchange = (e) => changeLanguage(e.target.value);
  $('connectBtn').onclick = connect;
  $('cancelConnect').onclick = () => api().CancelConnect();
  $('saveAppKey').onclick = async () => {
    try {
      await api().SetAppKey($('appKeyInput').value);
      state.app = await api().GetState();
      showWelcome();
    } catch (e) { $('connectError').textContent = errText(e); $('connectError').hidden = false; }
  };

  $('accountBtn').onclick = (e) => { e.stopPropagation(); $('accountMenu').hidden = !$('accountMenu').hidden; };
  document.addEventListener('click', () => { $('accountMenu').hidden = true; });
  $('openLogs').onclick = () => api().OpenLogFolder();
  $('disconnect').onclick = async () => {
    const ok = await dialog({ title: t('dlg.disconnectTitle'), text: t('dlg.disconnectText'), okText: t('dlg.disconnect'), danger: true });
    if (!ok) return;
    state.app = await api().Disconnect();
    showWelcome();
  };
  $('workers').onchange = (e) => api().SetWorkers(+e.target.value);

  // Left panel
  $('places').onclick = (e) => {
    const b = e.target.closest('[data-i]');
    if (b) loadLocal(state.places[+b.dataset.i].path);
  };
  $('chooseFolder').onclick = async () => {
    const p = await api().ChooseFolder().catch(showError);
    if (p) loadLocal(p);
  };
  $('chooseFiles').onclick = async () => {
    const files = await api().ChooseFiles().catch(showError);
    if (!files?.length) return;
    const ok = await dialog({
      title: tn('dlg.uploadFilesTitle', files.length),
      text: t('dlg.uploadFilesText', { dest: state.dbxPath || t('dbx.root') }),
      okText: t('dlg.upload'),
    });
    if (ok) enqueue(files, state.dbxPath);
  };
  $('localUp').onclick = () => state.local?.parent && loadLocal(state.local.parent);
  $('localRefresh').onclick = () => state.local && loadLocal(state.local.path);
  $('viewGrid').onclick = () => { state.view = 'grid'; setViewButtons(); renderLocal(); api().SetViewMode('grid'); };
  $('viewList').onclick = () => { state.view = 'list'; setViewButtons(); renderLocal(); api().SetViewMode('list'); };
  $('selectAll').onchange = (e) => selectAll(e.target.checked);
  $('clearSel').onclick = () => selectAll(false);
  $('localFiles').addEventListener('click', onLocalClick);
  $('localFiles').addEventListener('dblclick', onLocalDblClick);
  document.querySelectorAll('.crumbs').forEach((nav) => nav.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-path]');
    if (!b) return;
    if (b.dataset.side === 'local') loadLocal(b.dataset.path); else loadDropbox(b.dataset.path);
  }));

  // Middle
  $('uploadBtn').onclick = uploadSelected;

  // Right panel
  $('dbxFiles').addEventListener('click', onDropboxClick);
  $('dbxFiles').addEventListener('dblclick', onDropboxDblClick);
  $('dbxUp').onclick = () => state.dbx && state.dbxPath && loadDropbox(state.dbx.parent);
  $('dbxRefresh').onclick = () => loadDropbox(state.dbxPath);
  $('newFolder').onclick = newFolder;
  $('openDropboxWeb').onclick = () => api().OpenInDropbox(state.dbxPath || '/', true);
  wireInternalDrag();

  // Queue
  $('queueToggle').onclick = () => {
    const c = $('queue').classList.toggle('collapsed');
    try { localStorage.setItem('queueCollapsed', c ? '1' : '0'); } catch {}
    scheduleQueueRender();
  };
  $('pauseBtn').onclick = () => (state.q.summary?.paused ? api().Resume() : api().Pause());
  $('cancelAllBtn').onclick = async () => {
    const ok = await dialog({ title: t('dlg.cancelAllTitle'), text: t('dlg.cancelAllText'), okText: t('dlg.cancelAllOk'), cancelText: t('dlg.keepUploading'), danger: true });
    if (ok) api().CancelAll();
  };
  $('retryFailedBtn').onclick = () => { api().RetryFailed(); setTab('uploading'); };
  $('clearFinishedBtn').onclick = () => api().ClearFinished();
  document.querySelectorAll('.tabs > button[data-tab]').forEach((b) => { b.onclick = () => setTab(b.dataset.tab); });
  $('queueList').addEventListener('scroll', scheduleQueueRender);
  $('queueList').addEventListener('click', onQueueClick);
  window.addEventListener('resize', scheduleQueueRender);
  wireQueueResize();

  // Preview
  $('previewClose').onclick = closePreview;
  $('previewPrev').onclick = () => { previewIdx--; showPreview(); };
  $('previewNext').onclick = () => { previewIdx++; showPreview(); };
  $('preview').addEventListener('click', (e) => { if (e.target.id === 'previewStage') closePreview(); });

  // Keyboard
  document.addEventListener('keydown', (e) => {
    if (!$('preview').hidden) {
      if (e.key === 'Escape') closePreview();
      if (e.key === 'ArrowLeft' && previewIdx > 0) { previewIdx--; showPreview(); }
      if (e.key === 'ArrowRight' && previewIdx < previewList.length - 1) { previewIdx++; showPreview(); }
      return;
    }
    if (!$('dialog').hidden || e.target.tagName === 'INPUT') return;
    if (e.key === 'F5') { e.preventDefault(); if (state.local) loadLocal(state.local.path); if (state.app.connected) loadDropbox(state.dbxPath); }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') { e.preventDefault(); selectAll(true); }
    if (e.key === 'Escape') selectAll(false);
    if (e.key === 'Backspace' && state.local?.parent) loadLocal(state.local.parent);
  });
  // Stop the webview's own context menu (looks out of place in a desktop app).
  document.addEventListener('contextmenu', (e) => { if (e.target.tagName !== 'INPUT') e.preventDefault(); });
}

init().catch((e) => toast(errText(e), 'bad'));
