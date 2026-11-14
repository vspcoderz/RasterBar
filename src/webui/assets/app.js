'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const audio = $('#audio');

const PAGE_TITLES = { search: 'Search', library: 'Library', now: 'Now playing', queue: 'Queue', settings: 'Settings', about: 'About' };
const DEFAULT_RAMP = " .'`^\",:;Il!i~+_-?][}{1)(|\\/tfjrxnuvczXYUJCLQ0OZmwqpdbkhao*#MW&8%B@$";

let state = null;
let ramp = DEFAULT_RAMP;
let page = 'search';
let searchResults = [];
let libraryResults = [];
let libraryDir = '';
let rootsLoaded = false;
let everConnected = false;
let connecting = false;
let lastAudioSrc = '';
let audioBlocked = false;
let audioRetries = 0;
let draggingSeek = false;
let draggingVolume = false;
let rafId = 0;
let pending = { 0: null, 1: null };
let queueSig = '';
let settingsSig = '';
let listsSig = '';
let lastTimeUpdate = 0;
let deferredInstall = null;
let clearArmed = 0;
let saveTimer = 0;

const decoder = new TextDecoder();

// ---------------------------------------------------------------------------
// format and sanitise

function esc(value) {
  return String(value == null ? '' : value).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function safeURL(value) {
  if (!value) return '';
  try {
    const u = new URL(String(value), location.origin);
    return (u.protocol === 'http:' || u.protocol === 'https:') ? u.href : '';
  } catch {
    return '';
  }
}

function fmtTime(seconds) {
  let s = Number(seconds);
  if (!Number.isFinite(s) || s < 0) s = 0;
  s = Math.floor(s);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`;
}

function normalise(t) {
  const local = t.local === true || Boolean(t.local_path);
  return {
    raw: t,
    id: t.id || t.ID || '',
    title: t.title || 'Untitled',
    channel: t.channel || (local ? 'local' : 'unknown'),
    duration: t.duration || '--:--',
    thumb: t.thumb || t.thumbs || '',
    local,
    localPath: t.local_path || t.localPath || '',
    url: t.url || '',
    series: t.series || '',
  };
}

function thumbFor(t) {
  const n = normalise(t);
  if (n.thumb) return safeURL(n.thumb);
  if (n.id) return safeURL(`https://i.ytimg.com/vi/${n.id}/mqdefault.jpg`);
  return '';
}

// ---------------------------------------------------------------------------
// API

async function api(path, body, method = 'POST') {
  const init = { method, headers: { Accept: 'application/json' } };
  if (method !== 'GET' && body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (!res.ok) {
    let msg = res.status + ' ' + res.statusText;
    try {
      const j = await res.json();
      if (j && j.error) msg = j.error;
    } catch { /* not json */ }
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  return res.json();
}

function toast(message, kind = 'ok') {
  const el = document.createElement('div');
  el.className = 'toast' + (kind === 'error' ? ' toast--error' : '');
  const icon = document.createElement('span');
  icon.className = 'icon ' + (kind === 'error' ? 'i-alert-circle' : 'i-check');
  const text = document.createElement('span');
  text.textContent = message;
  el.append(icon, text);
  $('#toasts').append(el);
  setTimeout(() => { el.style.opacity = '0'; setTimeout(() => el.remove(), 180); }, kind === 'error' ? 6000 : 3500);
}

function setSaveState(kind) {
  const el = $('#saveState');
  if (!el) return;
  el.className = 'save-state' + (kind ? ' is-' + kind : '');
  el.textContent = kind === 'saving' ? 'Saving' : kind === 'saved' ? 'Saved' : '';
  clearTimeout(saveTimer);
  if (kind === 'saved') saveTimer = setTimeout(() => setSaveState(''), 2200);
}

// ---------------------------------------------------------------------------
// navigation

function go(next, push = true) {
  if (!PAGE_TITLES[next]) next = 'search';
  page = next;
  $$('.page').forEach((el) => { el.hidden = el.dataset.page !== next; });
  $$('.nav-item, .mobile-nav__item').forEach((el) => {
    el.classList.toggle('is-active', el.dataset.page === next);
  });
  $('#topbarPage').textContent = PAGE_TITLES[next];
  if (push && location.hash !== '#' + next) history.pushState(null, '', '#' + next);
  if (next === 'library' && !rootsLoaded) loadRoots();
  if (next === 'now') requestAnimationFrame(fitMonos);
  window.scrollTo({ top: 0, behavior: 'instant' in window ? 'instant' : 'auto' });
}

document.addEventListener('click', (event) => {
  const nav = event.target.closest('[data-page]');
  if (nav && (nav.tagName === 'BUTTON' || nav.tagName === 'A')) {
    event.preventDefault();
    go(nav.dataset.page);
    return;
  }
  const palette = event.target.closest('[data-palette]');
  if (palette) {
    setPalette(palette.dataset.palette, Boolean(palette.closest('[data-choices="palette"]')));
  }
});

window.addEventListener('popstate', () => {
  go((location.hash || '#search').slice(1), false);
});

// ---------------------------------------------------------------------------
// events stream

function concat(a, b) {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

function setConn(live) {
  const el = $('#conn');
  el.classList.toggle('is-live', live);
  el.classList.toggle('is-down', !live && everConnected);
  $('#connLabel').textContent = live ? 'Live' : everConnected ? 'Reconnecting' : 'Connecting';
}

async function connect() {
  if (connecting) return;
  connecting = true;
  setConn(false);
  try {
    const res = await fetch('/api/events', { headers: { Accept: 'application/octet-stream' } });
    if (!res.ok) throw new Error('events: ' + res.status);
    everConnected = true;
    setConn(true);
    const reader = res.body.getReader();
    let buf = new Uint8Array(0);
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf = concat(buf, value);
      while (buf.length >= 5) {
        const len = (buf[1] << 24) | (buf[2] << 16) | (buf[3] << 8) | buf[4];
        if (buf.length < 5 + len) break;
        const type = buf[0];
        const payload = buf.subarray(5, 5 + len);
        if (type === 1) {
          try { applyState(JSON.parse(decoder.decode(payload))); } catch (err) { console.warn('state', err); }
        } else if (type === 2) {
          handleFrame(payload);
        }
        buf = buf.slice(5 + len);
      }
    }
  } catch (err) {
    if (err.name !== 'AbortError') console.warn(err);
  } finally {
    connecting = false;
    setConn(false);
    setTimeout(connect, 1400);
  }
}

// ---------------------------------------------------------------------------
// frames

function handleFrame(payload) {
  if (payload.length < 7) return;
  pending[payload[0]] = {
    mono: payload[1] === 1,
    cols: (payload[2] << 8) | payload[3],
    rows: (payload[4] << 8) | payload[5],
    ppc: payload[6] || 1,
    data: payload.subarray(7),
  };
  if (!rafId) rafId = requestAnimationFrame(flushFrames);
}

function flushFrames() {
  rafId = 0;
  const viz = pending[0];
  const video = pending[1];
  pending[0] = pending[1] = null;
  const videoMode = Boolean(state && state.video);
  const split = Boolean(state && state.split);
  if (viz) {
    if (viz.mono) {
      drawMono(videoMode && split ? $('#dockMono') : $('#vizMono'), viz);
    } else if (videoMode && split) {
      drawColor($('#dockCanvas'), viz);
    } else {
      drawColor($('#vizCanvas'), viz);
    }
  }
  if (video) {
    if (video.mono) drawMono($('#videoMono'), video);
    else drawColor($('#videoCanvas'), video);
  }
}

function drawColor(canvas, frame) {
  if (!canvas) return;
  const w = frame.cols;
  const h = frame.rows * frame.ppc;
  if (w < 1 || h < 1) return;
  if (canvas.width !== w || canvas.height !== h) {
    canvas.width = w;
    canvas.height = h;
    canvas.__img = null;
  }
  let img = canvas.__img;
  if (!img) {
    img = new ImageData(w, h);
    const px = img.data;
    for (let i = 3; i < px.length; i += 4) px[i] = 255;
    canvas.__img = img;
  }
  const src = frame.data;
  const px = img.data;
  const n = Math.min(src.length, w * h * 3);
  for (let i = 0, j = 0; i < n; i += 3, j += 4) {
    px[j] = src[i];
    px[j + 1] = src[i + 1];
    px[j + 2] = src[i + 2];
  }
  canvas.getContext('2d').putImageData(img, 0, 0);
}

function drawMono(pre, frame) {
  if (!pre) return;
  const { cols, rows, data } = frame;
  const lines = new Array(rows);
  const last = ramp.length - 1;
  for (let y = 0; y < rows; y++) {
    let line = '';
    for (let x = 0; x < cols; x++) {
      const b = data[y * cols + x] || 0;
      line += ramp[Math.min(last, (b * ramp.length / 256) | 0)];
    }
    lines[y] = line;
  }
  pre.textContent = lines.join('\n');
  fitMono(pre, cols, rows);
}

function fitMono(pre, cols, rows) {
  if (!pre || pre.hidden) return;
  const box = pre.parentElement;
  if (!box) return;
  const w = box.clientWidth || 640;
  const h = box.clientHeight || 360;
  const byWidth = (w - 20) / Math.max(1, cols) / 0.61;
  const byHeight = (h - 20) / Math.max(1, rows) / 1.02;
  pre.style.fontSize = Math.max(2, Math.min(byWidth, byHeight)) + 'px';
  pre.dataset.cols = cols;
  pre.dataset.rows = rows;
}

function fitMonos() {
  ['#vizMono', '#videoMono', '#dockMono'].forEach((sel) => {
    const pre = $(sel);
    if (pre && !pre.hidden && pre.dataset.cols) fitMono(pre, Number(pre.dataset.cols), Number(pre.dataset.rows));
  });
}
window.addEventListener('resize', fitMonos);

// ---------------------------------------------------------------------------
// state

function applyState(next) {
  const before = state;
  state = next;
  if (next.ramp) ramp = next.ramp;
  $('#navCount').textContent = String((next.queue || []).length);
  $('#navLive').hidden = !next.hasTrack;
  setConn(true);

  const has = Boolean(next.hasTrack);
  $('#nowEmpty').hidden = has;
  $('#nowBody').hidden = !has;
  $('#nowMini').hidden = !has;
  if (has) {
    $('#npTitle').textContent = next.title || 'Untitled';
    $('#npChannel').textContent = next.channel || '';
    $('#stageTitle').textContent = next.title || 'Untitled';
    $('#stageChannel').textContent = next.channel || '';
    $('#nowMiniTitle').textContent = next.title || 'Untitled';
    $('#nowMiniChannel').textContent = next.channel || '';
    $('#nowMiniArt').innerHTML = thumbFor({ thumb: next.thumb, id: next.trackId })
      ? `<img src="${esc(thumbFor({ thumb: next.thumb, id: next.trackId }))}" alt="">`
      : '<span class="icon i-audio-lines"></span>';
  }
  if (next.video !== undefined) setStageMode(next);
  syncAudio(next, before);
  syncProgress(next);
  syncPlayButton(next);
  updateStageGrid(next);
  updateStageMessage(next);
  renderVizAndPalettes(next);
  renderQueue(next);
  renderSettings(next);
  updateMediaSession(next);
  if (!has) $('#npTitle').textContent = 'Nothing playing';
}

function setStageMode(st) {
  const video = Boolean(st.video);
  const split = Boolean(st.split);
  $('#stage').classList.toggle('mode-video', video);
  $('#stage').classList.toggle('mode-music', !video);
  $('#stage').classList.toggle('has-split', split);
  const pip = $('#pip');
  const dock = $('#dock');
  const pipOpen = split && !video;
  pip.hidden = !pipOpen;
  dock.hidden = !(split && video);
  $('#videoSlot').style.display = video ? 'block' : 'none';
  $('#vizSlot').style.visibility = video ? 'hidden' : 'visible';
  $('#stageMode').textContent = video ? 'Video' : 'Visualizer';
  const canvas = $('#videoCanvas');
  if (pipOpen) {
    if (canvas.parentElement !== $('#pipBody')) $('#pipBody').append(canvas);
  } else if (canvas.parentElement !== $('#videoSlot')) {
    $('#videoSlot').append(canvas);
  }
  $('#splitBtn').classList.toggle('is-active', split);
  $('#splitLabel').textContent = split ? 'Split on' : 'Split';
  $$('[data-mode]').forEach((b) => b.classList.toggle('is-active', b.dataset.mode === (video ? 'video' : 'music')));
  $('#splitBtn').disabled = Boolean(st.music && st.local && !st.split && false);
}

function updateStageGrid(st) {
  const cols = st.cols || 180;
  const rows = st.rows || 50;
  $('#stageGrid').textContent = cols + ' x ' + rows;
}

function updateStageMessage(st) {
  const msg = $('#stageMsg');
  const text = $('#stageMsgText');
  if (st.error) {
    msg.hidden = false;
    msg.classList.add('is-error');
    msg.querySelector('.icon').className = 'icon i-alert-circle';
    text.textContent = st.error;
    $('#statusLine').hidden = true;
    return;
  }
  if (st.loading) {
    msg.hidden = false;
    msg.classList.remove('is-error');
    msg.querySelector('.icon').className = 'icon i-loader-2 spin';
    text.textContent = 'Resolving stream';
    return;
  }
  msg.hidden = true;
  const status = String(st.status || '');
  const line = $('#statusLine');
  if (status && status !== 'playing' && !status.startsWith('viz ') && !status.startsWith('palette ')) {
    line.hidden = false;
    line.textContent = status;
    line.classList.toggle('is-error', status.startsWith('download failed') || status.startsWith('end of queue'));
  } else {
    line.hidden = true;
  }
}

// ---------------------------------------------------------------------------
// audio

function syncAudio(st, before) {
  if (!st.hasTrack) {
    if (lastAudioSrc) {
      lastAudioSrc = '';
      audio.pause();
      audio.removeAttribute('src');
      audio.load();
    }
    return;
  }
  if (st.audioSrc && st.audioSrc !== lastAudioSrc) {
    lastAudioSrc = st.audioSrc;
    audio.src = st.audioSrc;
    audioRetries = 0;
    audioBlocked = false;
    audio.load();
    if (!st.paused) attemptPlay();
  }
  if (st.paused && !audio.paused) audio.pause();
  if (st.volume != null && !draggingVolume && audio.volume !== st.volume / 100) {
    audio.volume = st.volume / 100;
    $('#volume').value = String(st.volume);
    paintRange($('#volume'));
  }
}

function attemptPlay() {
  const p = audio.play();
  if (p && p.catch) {
    p.catch((err) => {
      if (err && err.name === 'NotAllowedError') {
        audioBlocked = true;
        toast('The browser blocked audio. Press play to start it.', 'error');
      }
    });
  }
}

audio.addEventListener('timeupdate', () => {
  const now = performance.now();
  if (now - lastTimeUpdate < 220) return;
  lastTimeUpdate = now;
  syncProgress(state);
});

audio.addEventListener('durationchange', () => syncProgress(state));

audio.addEventListener('ended', () => {
  if (!lastAudioSrc) return;
  api('/api/report', { position: audio.currentTime, duration: audio.duration || 0, ended: true }).catch(() => {});
});

audio.addEventListener('error', () => {
  if (!lastAudioSrc) return;
  if (audioRetries < 1) {
    audioRetries++;
    setTimeout(() => {
      audio.load();
      if (state && !state.paused) attemptPlay();
    }, 700);
    return;
  }
  toast('Audio failed to load. Press play to retry.', 'error');
});

function syncProgress(st) {
  if (!st || !st.hasTrack) {
    $('#seek').max = '100';
    $('#seek').value = '0';
    paintRange($('#seek'));
    $('#posLabel').textContent = '0:00';
    $('#durLabel').textContent = '0:00';
    $('#nowMiniFill').style.width = '0%';
    return;
  }
  const duration = Number.isFinite(audio.duration) && audio.duration > 0 ? audio.duration : (st.duration || 0);
  const position = draggingSeek ? Number($('#seek').value) : (Number.isFinite(audio.currentTime) && audio.currentTime > 0 ? audio.currentTime : (st.position || 0));
  const max = duration > 0 ? duration : Math.max(100, position);
  $('#seek').max = String(max);
  if (!draggingSeek) $('#seek').value = String(Math.min(position, max));
  paintRange($('#seek'));
  $('#posLabel').textContent = fmtTime(position);
  $('#durLabel').textContent = duration > 0 ? fmtTime(duration) : '--:--';
  const pct = duration > 0 ? Math.min(100, (position / duration) * 100) : 0;
  $('#nowMiniFill').style.width = pct.toFixed(2) + '%';
}

function paintRange(el) {
  if (!el) return;
  const min = Number(el.min || 0);
  const max = Number(el.max || 100);
  const value = Number(el.value || 0);
  const pct = max > min ? ((value - min) / (max - min)) * 100 : 0;
  el.style.setProperty('--pct', pct.toFixed(2) + '%');
}

function syncPlayButton(st) {
  const playing = !st.paused;
  $('#playIcon').className = 'icon ' + (playing ? 'i-pause' : 'i-play');
  $('#playBtn').setAttribute('aria-label', playing ? 'Pause' : 'Play');
  $('#navLive').hidden = !st.hasTrack;
  $('#splitBtn').classList.toggle('is-active', Boolean(st.split));
  $('#splitLabel').textContent = st.split ? 'Split on' : 'Split';
  $$('[data-mode]').forEach((b) => b.classList.toggle('is-active', b.dataset.mode === (st.video ? 'video' : 'music')));
}

async function togglePlay() {
  if (!state || !state.hasTrack) return;
  try {
    if (audio.paused) {
      await audio.play();
      await api('/api/transport', { action: 'play' });
    } else {
      audio.pause();
      await api('/api/transport', { action: 'pause' });
    }
    audioBlocked = false;
  } catch (err) {
    toast(err.message || 'Could not change playback state', 'error');
  }
}

function seekTo(value) {
  if (!state || !state.hasTrack) return;
  const duration = Number.isFinite(audio.duration) && audio.duration > 0 ? audio.duration : (state.duration || 0);
  let v = Number(value);
  if (!Number.isFinite(v)) v = 0;
  if (duration > 0) v = Math.max(0, Math.min(duration, v));
  else v = Math.max(0, v);
  try { audio.currentTime = v; } catch { /* not ready */ }
  if (state) state.position = v;
  syncProgress(state);
  api('/api/transport', { action: 'seek', value: v }).catch((e) => toast(e.message, 'error'));
}

function skip(delta) {
  if (!state || !state.hasTrack) return;
  const base = Number.isFinite(audio.currentTime) ? audio.currentTime : (state.position || 0);
  seekTo(base + delta);
}

function setVolume(value) {
  const v = Math.max(0, Math.min(100, Math.round(Number(value))));
  audio.muted = false;
  audio.volume = v / 100;
  $('#volume').value = String(v);
  $('#volLabel').textContent = String(v);
  paintRange($('#volume'));
  $('#volIcon').className = 'icon ' + (v === 0 ? 'i-volume-x' : 'i-volume-2');
  clearTimeout(setVolume.timer);
  setVolume.timer = setTimeout(() => {
    api('/api/transport', { action: 'volume', value: v }).catch(() => {});
  }, 180);
}

$('#seek').addEventListener('pointerdown', () => { draggingSeek = true; });
$('#seek').addEventListener('input', () => {
  paintRange($('#seek'));
  $('#posLabel').textContent = fmtTime(Number($('#seek').value));
});
$('#seek').addEventListener('change', () => {
  draggingSeek = false;
  seekTo(Number($('#seek').value));
});
$('#seek').addEventListener('pointerup', () => { draggingSeek = false; });

$('#volume').addEventListener('input', () => {
  draggingVolume = true;
  setVolume($('#volume').value);
});
$('#volume').addEventListener('change', () => { draggingVolume = false; });

$('#muteBtn').addEventListener('click', () => {
  audio.muted = !audio.muted;
  $('#volIcon').className = 'icon ' + (audio.muted || audio.volume === 0 ? 'i-volume-x' : 'i-volume-2');
  toast(audio.muted ? 'Muted' : 'Sound on');
});

// ---------------------------------------------------------------------------
// transport delegation

document.addEventListener('click', async (event) => {
  const action = event.target.closest('[data-action]');
  if (!action) return;
  const name = action.dataset.action;
  try {
    switch (name) {
      case 'toggle': await togglePlay(); break;
      case 'pause': audio.pause(); await api('/api/transport', { action: 'pause' }); break;
      case 'play': await audio.play(); await api('/api/transport', { action: 'play' }); break;
      case 'prev': await api('/api/transport', { action: 'prev' }); break;
      case 'next': await api('/api/transport', { action: 'next' }); break;
      case 'back': skip(-10); break;
      case 'fwd': skip(10); break;
      case 'seek': seekTo(action.dataset.value); break;
      case 'volume': setVolume(action.dataset.value); break;
      case 'mute': audio.muted = !audio.muted; break;
      case 'split': await api('/api/mode', { split: !(state && state.split) }); break;
      case 'split-off': await api('/api/mode', { split: false }); break;
      case 'viz-prev': await stepViz(-1); break;
      case 'viz-next': await stepViz(1); break;
      case 'download': await downloadCurrent(); break;
      case 'fullscreen': await toggleFullscreen(); break;
    }
  } catch (err) {
    toast(err.message || 'Action failed', 'error');
  }
});

async function stepViz(dir) {
  if (!state || !state.styles || !state.styles.length) return;
  const at = state.styles.findIndex((s) => s.toLowerCase() === String(state.viz || '').toLowerCase());
  const next = state.styles[(at + dir + state.styles.length) % state.styles.length];
  await api('/api/viz', { style: next });
  state.viz = next;
  renderVizAndPalettes(state);
}

async function setPalette(name, save) {
  if (!name) return;
  try {
    await api('/api/viz', { palette: name });
    if (state) state.palette = name;
    if (save) await saveSettings({ palette: name });
    else renderVizAndPalettes(state);
  } catch (err) {
    toast(err.message, 'error');
  }
}

async function downloadCurrent() {
  if (!state || !state.hasTrack) return;
  const track = { ID: state.trackId, Title: state.title, Channel: state.channel, URL: state.trackURL, Thumbs: state.thumb };
  if (!track.URL) {
    toast('This track is already local.', 'error');
    return;
  }
  await api('/api/download', { track });
  toast('Download started');
}

async function toggleFullscreen() {
  const stage = $('#stage');
  try {
    if (!document.fullscreenElement) await stage.requestFullscreen();
    else await document.exitFullscreen();
  } catch (err) {
    toast('Fullscreen is not available here.', 'error');
  }
}

// ---------------------------------------------------------------------------
// now playing visuals

function renderVizAndPalettes(st) {
  if (!st) return;
  if (st.styles) {
    const sig = st.styles.join('|');
    if (sig !== listsSig + '|viz') {
      const wrap = $('[data-choices="viz"]');
      if (wrap) {
        wrap.innerHTML = st.styles.map((name) => `<button class="chip" type="button" data-setting="visualizer" data-setting-value="${esc(name)}">${esc(name)}</button>`).join('');
      }
      listsSig = sig + '|viz';
    }
  }
  const vizName = st.viz || 'bars';
  $('#vizName').textContent = vizName;
  $('#vizCurrent').textContent = vizName;
  $('#npTitle').title = st.title || '';
  if (st.palettes && st.paletteColors) {
    const sig = st.palettes.join('|');
    if ($('#palettes').dataset.sig !== sig) {
      const html = st.palettes.map((name) => {
        const colors = st.paletteColors[name] || ['#222', '#444', '#666', '#888'];
        const grad = `linear-gradient(90deg, ${colors.map((c, i) => `${esc(c)} ${i * 25}% ${(i + 1) * 25}%`).join(', ')})`;
        return `<button class="palette" type="button" data-palette="${esc(name)}" title="${esc(name)}"><span class="palette__swatch" style="background:${grad}"></span><span class="palette__name">${esc(name)}</span></button>`;
      }).join('');
      $('#palettes').innerHTML = html;
      $('#palettes').dataset.sig = sig;
      const settings = $('[data-choices="palette"]');
      if (settings) settings.innerHTML = html;
    }
    $$('.palette').forEach((el) => el.classList.toggle('is-active', el.dataset.palette === st.palette));
  }
  $$('[data-setting="visualizer"]').forEach((el) => {
    el.classList.toggle('is-active', el.dataset.settingValue === vizName);
  });
}

// ---------------------------------------------------------------------------
// track rows

function channelOf(t) {
  const n = normalise(t);
  return n.channel || (n.local ? 'local' : 'YouTube');
}

function rowHTML(t, index, context) {
  const n = normalise(t);
  const thumb = thumbFor(t);
  const current = context === 'queue' && state && index === state.index;
  const downloading = state && state.downloading && state.downloading[n.id];
  const art = thumb
    ? `<img src="${esc(thumb)}" alt="" loading="lazy"><span class="icon i-music"></span>`
    : '<span class="icon i-music"></span>';
  const playLabel = current ? 'Playing' : 'Play';
  const actions = [];
  if (current) {
    actions.push(`<span class="badge-playing">Playing</span>`);
  } else {
    actions.push(`<button class="btn btn-primary btn-sm" type="button" data-act="play" data-index="${index}">${playLabel}</button>`);
  }
  if (context !== 'queue') {
    actions.push(`<button class="btn btn-ghost btn-sm btn-icon" type="button" data-act="queue" data-index="${index}" aria-label="Add to queue" title="Add to queue"><span class="icon i-plus"></span></button>`);
  }
  if (!n.local) {
    actions.push(`<button class="btn btn-ghost btn-sm btn-icon" type="button" data-act="download" data-index="${index}" aria-label="Download" title="Download"><span class="icon i-download"></span></button>`);
  }
  const meta = [esc(n.channel), esc(n.duration), n.local ? 'local' : 'YouTube'];
  const dlLine = downloading ? `<span class="track__download">${esc(downloading)}</span>` : '';
  return `<div class="track${current ? ' is-current' : ''}" data-index="${index}">
    <div class="track__art">${art}</div>
    <div class="track__body">
      <span class="track__title" title="${esc(n.title)}">${esc(n.title)}</span>
      <span class="track__meta"><span>${meta[0]}</span><span class="dot"></span><span>${meta[1]}</span><span class="dot"></span><span>${meta[2]}</span>${n.series ? `<span class="dot"></span><span>${esc(n.series)}</span>` : ''}</span>
    </div>
    <div class="track__actions">${actions.join('')}${dlLine}</div>
  </div>`;
}

function renderList(container, list, context, emptyEl) {
  if (!container) return;
  container.innerHTML = list.map((t, i) => rowHTML(t, i, context)).join('');
  if (emptyEl) emptyEl.hidden = list.length > 0;
}

function bindRowActions(container, list, context) {
  if (!container || container.dataset.bound) return;
  container.dataset.bound = '1';
  container.addEventListener('click', async (event) => {
    const btn = event.target.closest('[data-act]');
    if (!btn) return;
    const index = Number(btn.dataset.index);
    const track = list[index];
    if (!track) return;
    try {
      if (btn.dataset.act === 'play') {
        if (context === 'queue') await api('/api/play', { index });
        else await api('/api/queue', { tracks: list.map((x) => x.raw || x), index });
        go('now');
      } else if (btn.dataset.act === 'queue') {
        await api('/api/queue/add', { tracks: [track.raw || track] });
        toast('Added to queue');
      } else if (btn.dataset.act === 'download') {
        await api('/api/download', { track: track.raw || track });
        toast('Download started');
      }
    } catch (err) {
      toast(err.message, 'error');
    }
  });
}

// Panels that replace their own list lose the bound listener; bind after each render instead.
function rebind() {
  const pairs = [
    [$('#searchResults'), searchResults, 'search'],
    [$('#libraryResults'), libraryResults, 'library'],
    [$('#queueList'), state ? state.queue : [], 'queue'],
  ];
  for (const [el, list, ctx] of pairs) {
    if (!el) continue;
    el.__list = list;
    el.__ctx = ctx;
  }
}

document.addEventListener('click', async (event) => {
  const btn = event.target.closest('[data-act]');
  if (!btn) return;
  const holder = btn.closest('#searchResults, #libraryResults, #queueList, #libraryStripList');
  if (!holder) return;
  event.stopPropagation();
  const list = holder.__list || [];
  const ctx = holder.__ctx || 'search';
  const index = Number(btn.dataset.index);
  const track = list[index];
  if (!track) return;
  try {
    if (btn.dataset.act === 'play') {
      if (ctx === 'queue') await api('/api/play', { index });
      else await api('/api/queue', { tracks: list.map((x) => x.raw || x), index });
      go('now');
    } else if (btn.dataset.act === 'queue') {
      await api('/api/queue/add', { tracks: [track.raw || track] });
      toast('Added to queue');
    } else if (btn.dataset.act === 'download') {
      await api('/api/download', { track: track.raw || track });
      toast('Download started');
    }
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ---------------------------------------------------------------------------
// queue

function renderQueue(st) {
  if (!st) return;
  const queue = st.queue || [];
  const sig = queue.map((t) => t.id + '|' + t.title).join('/') + '#' + st.index + '|' + JSON.stringify(st.downloading || {});
  if (sig === queueSig) {
    $$('#queueList .track').forEach((el) => el.classList.toggle('is-current', Number(el.dataset.index) === st.index));
    return;
  }
  queueSig = sig;
  const listEl = $('#queueList');
  listEl.innerHTML = queue.map((t, i) => rowHTML(t, i, 'queue')).join('');
  listEl.__list = queue;
  listEl.__ctx = 'queue';
  $('#queueEmpty').hidden = queue.length > 0;
  $('#queueSub').textContent = queue.length
    ? `${queue.length} track${queue.length === 1 ? '' : 's'}${st.hasTrack ? `, playing ${st.index + 1}` : ''}.`
    : 'Nothing queued.';

  const next = queue.slice(st.index + (st.hasTrack ? 1 : 0), st.index + 6);
  const up = $('#upNext');
  if (!next.length) {
    up.innerHTML = `<p class="up-next__empty">${queue.length ? 'End of the queue.' : 'Queue something from Search.'}</p>`;
  } else {
    up.innerHTML = next.map((t, k) => {
      const i = st.index + (st.hasTrack ? 1 : 0) + k;
      return `<button class="up-next__item" type="button" data-play-index="${i}">
        <span class="up-next__idx">${String(i + 1).padStart(2, '0')}</span>
        <span class="up-next__title">${esc(t.title || 'Untitled')}</span>
        <span class="up-next__dur">${esc(t.duration || '')}</span>
      </button>`;
    }).join('');
  }
  rebind();
}

document.addEventListener('click', async (event) => {
  const item = event.target.closest('[data-play-index]');
  if (!item) return;
  try {
    await api('/api/play', { index: Number(item.dataset.playIndex) });
    go('now');
  } catch (err) {
    toast(err.message, 'error');
  }
});

$('#queueClear').addEventListener('click', async () => {
  const btn = $('#queueClear');
  if (!clearArmed) {
    clearArmed = setTimeout(() => { clearArmed = 0; btn.innerHTML = '<span class="icon i-trash-2"></span><span>Clear</span>'; }, 4000);
    btn.innerHTML = '<span>Click again to clear</span>';
    return;
  }
  clearTimeout(clearArmed);
  clearArmed = 0;
  btn.innerHTML = '<span class="icon i-trash-2"></span><span>Clear</span>';
  try {
    await api('/api/queue', { tracks: [] });
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ---------------------------------------------------------------------------
// search

function looksLikeURL(q) {
  return /^(https?|ftp):\/\//i.test(q) || /^www\./i.test(q) || /^[a-z0-9.-]+\.[a-z]{2,}(\/|$)/i.test(q);
}

async function doSearch(query) {
  const q = query.trim();
  if (!q) return;
  if (looksLikeURL(q)) {
    const url = /^https?:\/\//i.test(q) ? q : 'https://' + q;
    try {
      await api('/api/queue', { tracks: [{ title: url, url }], index: 0 });
      go('now');
    } catch (err) {
      toast(err.message, 'error');
    }
    return;
  }
  $('#searchBtn').disabled = true;
  $('#searchBtn').textContent = 'Searching';
  $('#searchEmpty').hidden = true;
  $('#searchResultsHead').hidden = true;
  $('#searchResults').innerHTML = '<div class="track"><div class="track__art"><span class="icon i-loader-2 spin"></span></div><div class="track__body"><span class="track__title">Searching YouTube</span><span class="track__meta"><span>one yt-dlp lookup</span></span></div><div></div></div>';
  try {
    const data = await api('/api/search', { q });
    searchResults = data.tracks || [];
    renderList($('#searchResults'), searchResults, 'search', $('#searchEmpty'));
    $('#searchResults').__list = searchResults;
    $('#searchResults').__ctx = 'search';
    $('#searchResultsHead').hidden = searchResults.length === 0;
    $('#searchResultsLabel').textContent = `${searchResults.length} result${searchResults.length === 1 ? '' : 's'} for "${q}"`;
    if (!searchResults.length) {
      $('#searchEmpty').hidden = false;
      $('#searchEmpty .empty__title').textContent = 'No results';
      $('#searchEmpty .empty__text').textContent = 'Try a different query, or paste a video link directly.';
    } else {
      $('#searchEmpty').hidden = true;
    }
  } catch (err) {
    $('#searchResults').innerHTML = '';
    $('#searchEmpty').hidden = false;
    $('#searchEmpty .empty__title').textContent = 'Search failed';
    $('#searchEmpty .empty__text').textContent = err.message;
    toast(err.message, 'error');
  } finally {
    $('#searchBtn').disabled = false;
    $('#searchBtn').textContent = 'Search';
  }
}

$('#searchForm').addEventListener('submit', (event) => {
  event.preventDefault();
  doSearch($('#searchInput').value);
});

$('#searchChips').addEventListener('click', (event) => {
  const chip = event.target.closest('[data-query]');
  if (!chip) return;
  $('#searchInput').value = chip.dataset.query;
  doSearch(chip.dataset.query);
});

$('#searchQueueAll').addEventListener('click', async () => {
  if (!searchResults.length) return;
  try {
    await api('/api/queue', { tracks: searchResults, index: 0 });
    go('now');
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ---------------------------------------------------------------------------
// library

async function loadRoots() {
  rootsLoaded = true;
  try {
    const data = await api('/api/roots', undefined, 'GET');
    const roots = data.roots || [];
    $('#roots').innerHTML = roots.map((r) => `<button class="chip" type="button" data-root="${esc(r.dir)}"><span class="icon i-folder" style="width:13px;height:13px;margin-right:5px"></span>${esc(r.name)}</button>`).join('');
  } catch (err) {
    rootsLoaded = false;
    console.warn(err);
  }
}

$('#roots').addEventListener('click', (event) => {
  const root = event.target.closest('[data-root]');
  if (!root) return;
  scanLibrary(root.dataset.root);
});

$('#pickFolder').addEventListener('click', async () => {
  try {
    const data = await api('/api/pick-folder', {});
    scanLibrary(data.dir);
  } catch (err) {
    toast(err.message, 'error');
  }
});

$('#probeToggle').addEventListener('change', () => {
  if (libraryDir) scanLibrary(libraryDir);
});

async function scanLibrary(dir) {
  $('#libraryEmpty').hidden = true;
  $('#libraryHead').hidden = true;
  $('#libraryResults').innerHTML = '<div class="track"><div class="track__art"><span class="icon i-loader-2 spin"></span></div><div class="track__body"><span class="track__title">Scanning folder</span><span class="track__meta"><span>' + esc(dir) + '</span></span></div><div></div></div>';
  try {
    const data = await api('/api/library', { dir, probe: $('#probeToggle').checked });
    libraryDir = data.dir || dir;
    libraryResults = data.tracks || [];
    $('#libraryDir').textContent = libraryDir + (data.truncated ? ' (first 500)' : '');
    renderList($('#libraryResults'), libraryResults, 'library', $('#libraryEmpty'));
    $('#libraryResults').__list = libraryResults;
    $('#libraryResults').__ctx = 'library';
    $('#libraryLabel').textContent = `${libraryResults.length} track${libraryResults.length === 1 ? '' : 's'}`;
    $('#libraryHead').hidden = libraryResults.length === 0;
    if (!libraryResults.length) {
      $('#libraryEmpty').hidden = false;
      $('#libraryEmpty .empty__title').textContent = 'No playable files';
      $('#libraryEmpty .empty__text').textContent = 'RasterBar looks for video and audio containers, with or without episode markers.';
    }
    renderLibraryStrip();
  } catch (err) {
    $('#libraryResults').innerHTML = '';
    $('#libraryEmpty').hidden = false;
    $('#libraryEmpty .empty__title').textContent = 'Could not scan that folder';
    $('#libraryEmpty .empty__text').textContent = err.message;
    toast(err.message, 'error');
  }
}

function renderLibraryStrip() {
  const box = $('#libraryStrip');
  const list = $('#libraryStripList');
  if (!box || !list) return;
  if (!libraryResults.length) {
    box.hidden = true;
    list.innerHTML = '';
    return;
  }
  box.hidden = false;
  const items = libraryResults.slice(0, 4);
  list.innerHTML = items.map((t, i) => rowHTML(t, i, 'library')).join('');
  list.__list = items;
  list.__ctx = 'library';
  list.dataset.sig = libraryResults.map((t) => t.LocalPath).join('|');
}

$('#libraryQueueAll').addEventListener('click', async () => {
  if (!libraryResults.length) return;
  try {
    await api('/api/queue', { tracks: libraryResults, index: 0 });
    go('now');
  } catch (err) {
    toast(err.message, 'error');
  }
});

// ---------------------------------------------------------------------------
// settings

function renderSettings(st) {
  if (!st) return;
  const s = st.settings || {};
  const sig = JSON.stringify(s) + '|' + st.viz + '|' + st.palette + '|' + st.video + '|' + (st.cacheDir || '');
  if (sig === settingsSig) return;
  settingsSig = sig;

  const mode = s.mode || (st.video ? 'video' : 'music');
  $$('[data-setting="mode"] [data-setting-value]').forEach((b) => b.classList.toggle('is-active', b.dataset.settingValue === mode));
  $$('[data-setting="quality"] [data-setting-value]').forEach((b) => b.classList.toggle('is-active', Number(b.dataset.settingValue) === Number(s.quality || 0)));
  $$('[data-setting="cookies"] [data-setting-value]').forEach((b) => b.classList.toggle('is-active', (b.dataset.settingValue || '') === (s.cookiesFromBrowser || '')));
  $$('[data-setting="visualizer"]').forEach((b) => b.classList.toggle('is-active', (b.dataset.settingValue || '').toLowerCase() === String(s.viz || st.viz || '').toLowerCase()));
  $$('[data-choices="palette"] .palette').forEach((b) => b.classList.toggle('is-active', b.dataset.palette === (s.palette || st.palette)));
  $('#cacheDirLabel').textContent = s.cacheDir || st.cacheDir || 'No folder set.';
}

async function saveSettings(patch) {
  if (!state) return;
  const previous = state.settings || {};
  const next = { ...previous, ...patch };
  state.settings = next;
  settingsSig = '';
  renderSettings(state);
  setSaveState('saving');
  try {
    const saved = await api('/api/settings', next);
    state.settings = saved;
    settingsSig = '';
    setSaveState('saved');
    toast('Settings saved');
  } catch (err) {
    state.settings = previous;
    settingsSig = '';
    setSaveState('');
    toast(err.message, 'error');
  }
}

document.addEventListener('click', (event) => {
  const btn = event.target.closest('[data-setting-value]');
  if (!btn) return;
  const group = btn.closest('[data-setting]');
  if (!group) return;
  const setting = group.dataset.setting;
  const value = btn.dataset.settingValue;
  if (setting === 'mode') saveSettings({ mode: value });
  else if (setting === 'quality') saveSettings({ quality: Number(value) });
  else if (setting === 'visualizer') saveSettings({ viz: value });
  else if (setting === 'palette') saveSettings({ palette: value });
  else if (setting === 'cookies') saveSettings({ cookiesFromBrowser: value });
});

$('#settingsPickFolder').addEventListener('click', async () => {
  try {
    const data = await api('/api/pick-folder', {});
    await saveSettings({ cacheDir: data.dir });
  } catch (err) {
    toast(err.message, 'error');
  }
});

$('#settingsClearFolder').addEventListener('click', () => saveSettings({ cacheDir: '' }));

// ---------------------------------------------------------------------------
// about

async function loadVersion() {
  try {
    const data = await api('/api/version', undefined, 'GET');
    $('#versionPill').textContent = data.version || 'development';
  } catch { /* non-fatal */ }
}

$('#btnCheckUpdate').addEventListener('click', async () => {
  const btn = $('#btnCheckUpdate');
  btn.disabled = true;
  const label = btn.querySelector('span:last-child');
  const old = label.textContent;
  label.textContent = 'Checking';
  try {
    const data = await api('/api/update', {});
    $('#updateMsg').hidden = false;
    $('#updateMsg').textContent = String(data.message || '').replace(/ — /g, ' - ');
  } catch (err) {
    toast(err.message, 'error');
  } finally {
    btn.disabled = false;
    label.textContent = old;
  }
});

// ---------------------------------------------------------------------------
// media session

function updateMediaSession(st) {
  if (!('mediaSession' in navigator)) return;
  try {
    if (st.hasTrack) {
      const art = thumbFor({ thumb: st.thumb, id: st.trackId });
      navigator.mediaSession.metadata = new MediaMetadata({
        title: st.title || 'RasterBar',
        artist: st.channel || '',
        album: 'RasterBar',
        artwork: art ? [{ src: art, sizes: '320x180', type: 'image/jpeg' }] : [],
      });
    } else {
      navigator.mediaSession.metadata = null;
    }
    navigator.mediaSession.setActionHandler('play', () => togglePlay());
    navigator.mediaSession.setActionHandler('pause', () => togglePlay());
    navigator.mediaSession.setActionHandler('nexttrack', () => api('/api/transport', { action: 'next' }).catch(() => {}));
    navigator.mediaSession.setActionHandler('previoustrack', () => api('/api/transport', { action: 'prev' }).catch(() => {}));
  } catch { /* unsupported */ }
}

// ---------------------------------------------------------------------------
// keyboard

document.addEventListener('keydown', (event) => {
  const tag = (event.target && event.target.tagName) || '';
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || event.target.isContentEditable) return;
  if (event.metaKey || event.ctrlKey || event.altKey) return;
  switch (event.key) {
    case ' ':
      event.preventDefault();
      togglePlay();
      break;
    case 'ArrowLeft':
      event.preventDefault();
      skip(-10);
      break;
    case 'ArrowRight':
      event.preventDefault();
      skip(10);
      break;
    case 'n':
      api('/api/transport', { action: 'next' }).catch(() => {});
      break;
    case 'p':
      api('/api/transport', { action: 'prev' }).catch(() => {});
      break;
    case 'm':
      audio.muted = !audio.muted;
      break;
    case 'f':
      toggleFullscreen();
      break;
    case '/':
      event.preventDefault();
      go('search');
      $('#searchInput').focus();
      break;
  }
});

// ---------------------------------------------------------------------------
// install and service worker

window.addEventListener('beforeinstallprompt', (event) => {
  event.preventDefault();
  deferredInstall = event;
  if (!window.matchMedia('(display-mode: standalone)').matches) {
    $('#btnInstall').hidden = false;
    $('#installBar').hidden = false;
  }
});

async function promptInstall() {
  if (!deferredInstall) return;
  deferredInstall.prompt();
  try { await deferredInstall.userChoice; } catch { /* dismissed */ }
  deferredInstall = null;
  $('#installBar').hidden = true;
  $('#btnInstall').hidden = true;
}

$('#btnInstall').addEventListener('click', promptInstall);
$('#installConfirm').addEventListener('click', promptInstall);
$('#installDismiss').addEventListener('click', () => { $('#installBar').hidden = true; });

window.addEventListener('appinstalled', () => {
  toast('RasterBar installed');
  $('#installBar').hidden = true;
  $('#btnInstall').hidden = true;
});

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}

// The first row of each panel gets its list attached after rendering.
const rowErrorHandler = (event) => {
  if (event.target && event.target.tagName === 'IMG') event.target.remove();
};

['#searchResults', '#libraryResults', '#queueList', '#libraryStripList'].forEach((sel) => {
  const el = $(sel);
  if (!el) return;
  el.addEventListener('error', rowErrorHandler, true);
});

// ---------------------------------------------------------------------------
// boot

const initial = (location.hash || '#search').slice(1);
go(initial, false);
loadVersion();
connect();
audio.volume = 0.8;
paintRange($('#volume'));
paintRange($('#seek'));
