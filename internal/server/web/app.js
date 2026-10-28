"use strict";

const APP_NAME = "Itan";

// ---------- helpers ----------

const app = document.getElementById("app");
const searchBox = document.getElementById("search");
const toasts = document.getElementById("toasts");

async function api(path, opts = {}) {
  const { body, withTotal, ...rest } = opts;
  const res = await fetch(path, {
    ...rest,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return withTotal ? { data, total: Number(res.headers.get("X-Total-Count")) || data.length } : data;
}

// h("div", {class: "x", onclick: fn}, child, "text", ...) builds DOM safely
// (text is never parsed as HTML).
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    // Skip falsy placeholders from `cond && el` patterns.
    if (c == null || c === false || c === 0 || c === "") continue;
    el.append(c instanceof Node ? c : String(c));
  }
  return el;
}

// Icons are fixed markup from this file, never user data.
const ICONS = {
  play: '<path d="M7 4.6v14.8a1 1 0 0 0 1.5.86l12.2-7.4a1 1 0 0 0 0-1.72L8.5 3.74A1 1 0 0 0 7 4.6z" fill="currentColor" stroke="none"/>',
  pause: '<rect x="6" y="4.5" width="4" height="15" rx="1.2" fill="currentColor" stroke="none"/><rect x="14" y="4.5" width="4" height="15" rx="1.2" fill="currentColor" stroke="none"/>',
  heart: '<path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.7l-1-1.1a5.5 5.5 0 0 0-7.8 7.8l1 1.1L12 21l7.8-7.5 1-1.1a5.5 5.5 0 0 0 0-7.8z"/>',
  star: '<path d="M12 2.8l2.8 5.8 6.4.9-4.6 4.5 1.1 6.3L12 17.3l-5.7 3 1.1-6.3-4.6-4.5 6.4-.9z"/>',
  edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  chevronLeft: '<path d="m15 18-6-6 6-6"/>',
  chevronRight: '<path d="m9 18 6-6-6-6"/>',
  volume: '<path d="M11 5 6 9H3v6h3l5 4z" fill="currentColor"/><path d="M15.5 8.5a5 5 0 0 1 0 7M18.5 5.5a9 9 0 0 1 0 13"/>',
  mute: '<path d="M11 5 6 9H3v6h3l5 4z" fill="currentColor"/><path d="m22 9-6 6M16 9l6 6"/>',
  fullscreen: '<path d="M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M16 21h3a2 2 0 0 0 2-2v-3M3 16v3a2 2 0 0 0 2 2h3"/>',
  exitFullscreen: '<path d="M8 3v3a2 2 0 0 1-2 2H3M21 8h-3a2 2 0 0 1-2-2V3M3 16h3a2 2 0 0 1 2 2v3M16 21v-3a2 2 0 0 1 2-2h3"/>',
  pip: '<rect x="2" y="4" width="20" height="16" rx="2"/><rect x="12" y="11" width="7" height="6" rx="1" fill="currentColor"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 7.5h.01"/>',
  alert: '<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5M12 16h.01"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  refresh: '<path d="M21 12a9 9 0 0 1-15.5 6.2L3 16M3 12a9 9 0 0 1 15.5-6.2L21 8M21 3v5h-5M3 21v-5h5"/>',
  download: '<path d="M12 3v12m0 0-4-4m4 4 4-4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  upload: '<path d="M12 15V3m0 0L8 7m4-4 4 4M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/>',
  replay: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8M3 3v5h5"/>',
  copy: '<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/>',
  film: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M7 3v18M17 3v18M3 8h4M3 12h4M3 16h4M17 8h4M17 12h4M17 16h4"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  sparkle: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9zM19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  back10: '<path d="M4.5 12a7.5 7.5 0 1 0 2.2-5.3L4.5 9"/><path d="M4.5 4.5V9H9"/><text x="12.6" y="15.4" text-anchor="middle" font-size="7" font-weight="800" font-family="system-ui, sans-serif" fill="currentColor" stroke="none">10</text>',
  fwd10: '<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3L19.5 9"/><path d="M19.5 4.5V9H15"/><text x="11.4" y="15.4" text-anchor="middle" font-size="7" font-weight="800" font-family="system-ui, sans-serif" fill="currentColor" stroke="none">10</text>',
  rewind: '<path d="M11 18.5 3 12l8-6.5zM21 18.5 13 12l8-6.5z" fill="currentColor" stroke="none"/>',
  forward: '<path d="m13 18.5 8-6.5-8-6.5zM3 18.5l8-6.5-8-6.5z" fill="currentColor" stroke="none"/>',
  wide: '<rect x="2" y="5" width="20" height="14" rx="2.5"/><path d="m6.5 12 2.2-2.2M6.5 12l2.2 2.2M17.5 12l-2.2-2.2M17.5 12l-2.2 2.2"/>',
  narrow: '<rect x="2" y="5" width="20" height="14" rx="2.5"/><path d="M10 12 7.8 9.8M10 12l-2.2 2.2M14 12l2.2-2.2M14 12l2.2 2.2"/>',
  devices: '<rect x="2" y="4" width="14" height="10" rx="1.5"/><path d="M6 18h6M9 14v4"/><rect x="18" y="8" width="4" height="12" rx="1"/>',
};

function icon(name, cls) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("aria-hidden", "true");
  if (cls) s.setAttribute("class", cls);
  s.innerHTML = ICONS[name];
  return s;
}

const pad = (n) => String(n).padStart(2, "0");

function clock(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hh = Math.floor(sec / 3600), mm = Math.floor((sec % 3600) / 60), ss = sec % 60;
  return hh ? `${hh}:${pad(mm)}:${pad(ss)}` : `${mm}:${pad(ss)}`;
}

// 0:42, 21 min, 1h 42m
function fmtDuration(ms) {
  if (!ms || ms < 0) return "";
  const s = Math.round(ms / 1000);
  if (s < 60) return `0:${pad(s)}`;
  const m = Math.round(s / 60);
  return m < 60 ? `${m} min` : `${Math.floor(m / 60)}h ${m % 60}m`;
}

function fmtBytes(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

const plural = (n, word) => `${n.toLocaleString()} ${word}${n === 1 ? "" : "s"}`;

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
function relTime(ms) {
  const s = Math.round((ms - Date.now()) / 1000), abs = Math.abs(s);
  if (abs < 45) return "just now";
  for (const [unit, sec] of [["year", 31536000], ["month", 2592000], ["week", 604800], ["day", 86400], ["hour", 3600]]) {
    if (abs >= sec) return rtf.format(Math.round(s / sec), unit);
  }
  return rtf.format(Math.round(s / 60) || -1, "minute");
}

let langNames = null;
try { langNames = new Intl.DisplayNames(undefined, { type: "language" }); } catch { /* old browser */ }
function langName(code) {
  if (!code || code === "und") return "";
  try { return langNames?.of(code) || code; } catch { return code; }
}

function debounce(fn, ms) {
  let t;
  const d = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
  d.cancel = () => clearTimeout(t);
  return d;
}

// Browser storage can be unavailable (private mode, blocked site data).
const store = {
  get(k) { try { return localStorage.getItem(k); } catch { return null; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch { /* ignore */ } },
};

function hueFor(s) {
  let x = 0;
  for (const c of s) x = (x * 31 + c.codePointAt(0)) >>> 0;
  return x % 360;
}

const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

const KINDS = ["video", "movie", "episode", "music_video", "concert", "documentary", "lecture", "home_video", "other"];
const KIND_LABELS = {
  video: "Video", movie: "Movie", episode: "Episode", music_video: "Music video", concert: "Concert",
  documentary: "Documentary", lecture: "Lecture", home_video: "Home video", other: "Other",
};
const kindLabel = (k) => KIND_LABELS[k] || k;
const NS_LABELS = { genre: "Genre", tag: "Tag", person: "Person", mood: "Mood", source: "Source" };
const SOURCE_LABELS = { filename: "From file name", folder: "From folder", embedded: "From the file", scraper: "Looked up", user: "Edited" };
const PLAYBACK = {
  direct: "Plays as is",
  remux: "Repackaged for browsers, no quality loss",
  transcode: "Converted for browsers",
};
const CODECS = {
  h264: "H.264", hevc: "HEVC", av1: "AV1", vp9: "VP9", vp8: "VP8", mpeg4: "MPEG-4", mpeg2video: "MPEG-2", vc1: "VC-1",
  aac: "AAC", ac3: "Dolby Digital", eac3: "Dolby Digital Plus", dts: "DTS", truehd: "Dolby TrueHD", opus: "Opus",
  mp3: "MP3", flac: "FLAC", vorbis: "Vorbis", subrip: "SRT", ass: "ASS", mov_text: "Text", webvtt: "WebVTT",
  hdmv_pgs_subtitle: "PGS", dvd_subtitle: "VobSub",
};
const codecName = (c) => (c ? CODECS[c] || c.toUpperCase() : "");
const channelName = (n) => ({ 1: "mono", 2: "stereo", 6: "5.1", 8: "7.1" }[n] || (n ? `${n} ch` : ""));
const qualityLabel = (q) => (!q ? "" : /^4k$/i.test(q) ? "4K" : /^sd$/i.test(q) ? "SD" : q);
const genresOf = (it) => {
  const g = it.tags.filter((t) => t.namespace === "genre");
  return (g.length ? g : it.tags.filter((t) => t.namespace === "tag")).map((t) => t.name);
};

function toast(msg, type = "ok") {
  const t = h("div", { class: `toast ${type}`, role: type === "error" ? "alert" : "status" },
    icon(type === "error" ? "alert" : "check"), msg);
  toasts.append(t);
  setTimeout(() => {
    t.classList.add("out");
    t.addEventListener("animationend", () => t.remove(), { once: true });
  }, type === "error" ? 5000 : 2600);
}

// Overlays (drawer, dialog) take keyboard focus from page shortcuts.
let overlayOpen = false;
let closeOverlay = null;

function confirmDialog({ title, body, confirm = "OK", danger = false }) {
  return new Promise((resolve) => {
    const d = h("dialog", { class: "confirm" },
      h("h2", {}, title),
      h("p", {}, body),
      h("div", { class: "dialog-actions" },
        h("button", { class: "btn btn-ghost", onclick: () => d.close("cancel") }, "Cancel"),
        h("button", { class: `btn ${danger ? "btn-danger" : "btn-primary"}`, onclick: () => d.close("ok") }, confirm)));
    d.addEventListener("close", () => { overlayOpen = false; resolve(d.returnValue === "ok"); d.remove(); });
    document.body.append(d);
    overlayOpen = true;
    d.showModal();
  });
}

async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; } catch { /* needs https or localhost */ }
  const ta = h("textarea", { style: "position:fixed;opacity:0" });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { /* ignore */ }
  ta.remove();
  return ok;
}

const art = (name, spin) => h("div", { class: `empty-art${spin ? " spin" : ""}` }, icon(name));
const labeled = (label, input, extra) => h("label", { class: "field" }, h("span", { class: "field-label" }, label, extra), input);
const logo = () => document.querySelector(".brand .logo").cloneNode(true);

function section({ title, desc, action }, ...children) {
  return h("section", { class: "section" },
    h("div", { class: "section-head" }, h("div", {}, h("h2", {}, title), desc && h("p", {}, desc)), action),
    ...children);
}

// ---------- cards and shelves ----------

// poster returns a generated title card with the thumbnail fading in over it.
function poster(it) {
  const fallback = h("div", { class: "poster-fallback", style: `--h:${hueFor(it.title)}` }, h("span", {}, it.title));
  const img = h("img", { src: `/api/items/${it.id}/thumbnail`, loading: "lazy", decoding: "async", alt: "" });
  img.addEventListener("load", () => img.classList.add("loaded"), { once: true });
  img.addEventListener("error", () => img.remove(), { once: true });
  return [fallback, img];
}

function cardMeta(it) {
  const kind = it.kind !== "video" && it.kind !== "other" && kindLabel(it.kind);
  return [it.year, kind, ...genresOf(it).slice(0, 2)].filter(Boolean).join(" · ");
}

function card(it, { meta, reason = true } = {}) {
  const pct = it.position_ms > 0 && it.duration_ms ? Math.min(100, (it.position_ms / it.duration_ms) * 100) : 0;
  return h("a", { class: "card", href: `#/item/${it.id}`, title: it.title },
    h("div", { class: "thumb" },
      poster(it),
      h("div", { class: "play-hint" }, h("span", {}, icon("play"))),
      it.quality && h("span", { class: "badge q" }, qualityLabel(it.quality)),
      it.completed && !pct && h("span", { class: "badge done", title: "Watched" }, icon("check")),
      it.duration_ms && h("span", { class: "badge dur" }, fmtDuration(it.duration_ms)),
      pct && h("div", { class: "progress" }, h("i", { style: `width:${pct}%` }))),
    h("div", { class: "card-body" },
      h("div", { class: "card-title" }, it.title),
      h("div", { class: "card-meta" }, (meta ?? cardMeta(it)) || " "),
      reason && it.reason && h("div", { class: "card-reason" }, it.reason)));
}

// shelf is a titled row of cards that scrolls sideways, with arrow buttons
// for mouse users.
function shelf(title, items, cardOpts) {
  const row = h("div", { class: "row-scroll" }, items.map((it) => card(it, cardOpts && cardOpts(it))));
  const go = (dir) => row.scrollBy({ left: dir * row.clientWidth * 0.85, behavior: "smooth" });
  const prev = h("button", { class: "btn btn-icon", "aria-label": "Scroll left", onclick: () => go(-1) }, icon("chevronLeft"));
  const next = h("button", { class: "btn btn-icon", "aria-label": "Scroll right", onclick: () => go(1) }, icon("chevronRight"));
  const sync = () => {
    prev.disabled = row.scrollLeft < 8;
    next.disabled = row.scrollLeft + row.clientWidth >= row.scrollWidth - 8;
  };
  row.addEventListener("scroll", sync, { passive: true });
  new ResizeObserver(sync).observe(row);
  return h("section", { class: "shelf" },
    h("div", { class: "shelf-head" }, h("h2", {}, title), h("div", { class: "shelf-nav" }, prev, next)),
    row);
}

function skeletonGrid(n) {
  return h("div", { class: "grid" }, Array.from({ length: n }, () =>
    h("div", {}, h("div", { class: "sk sk-thumb" }), h("div", { class: "sk sk-line" }), h("div", { class: "sk sk-line short" }))));
}

const safeColor = (c) => (/^#[0-9a-f]{3,8}$/i.test(c || "") ? c : null);

function tile(t) {
  const color = safeColor(t.color);
  return h("a", {
    class: "tile", href: libraryHash({ tag: [t.ref] }),
    style: color ? `background:linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 45%, #000))` : `--h:${hueFor(t.name)}`,
  }, h("strong", {}, t.name), h("span", {}, plural(t.count, "video")));
}

// ---------- home ----------

function hero(it, { eyebrow, eyebrowIcon, resume }) {
  const pct = resume && it.duration_ms ? Math.min(100, (it.position_ms / it.duration_ms) * 100) : 0;
  const kind = it.kind !== "video" && it.kind !== "other" && kindLabel(it.kind);
  const meta = [
    it.year, kind,
    resume && it.duration_ms ? `${fmtDuration(it.duration_ms - it.position_ms)} left` : fmtDuration(it.duration_ms),
    ...genresOf(it).slice(0, 2),
  ].filter(Boolean);
  return h("section", { class: "hero" },
    h("div", { class: "hero-bg", style: `background-image:url(/api/items/${it.id}/thumbnail)` }),
    h("div", { class: "hero-inner" },
      h("div", {},
        h("div", { class: "eyebrow" }, icon(eyebrowIcon), eyebrow),
        h("h1", {}, it.title),
        h("div", { class: "hero-meta" },
          meta.map((m) => h("span", {}, m)),
          it.quality && h("span", { class: "pill" }, qualityLabel(it.quality))),
        it.description && h("p", { class: "hero-desc" }, it.description),
        h("div", { class: "hero-actions" },
          h("a", { class: "btn btn-primary btn-lg", href: `#/item/${it.id}?play=1` }, icon("play"), resume ? "Resume" : "Play"),
          h("a", { class: "btn btn-glass btn-lg", href: `#/item/${it.id}` }, icon("info"), "Details"))),
      h("a", { class: "hero-art", href: `#/item/${it.id}?play=1`, "aria-label": `Play ${it.title}`, tabindex: "-1" },
        h("div", { class: "thumb" },
          poster(it),
          h("div", { class: "play-hint" }, h("span", {}, icon("play"))),
          pct && h("div", { class: "progress" }, h("i", { style: `width:${pct}%` }))))));
}

async function renderHome(ctx) {
  const [libs, cont, recs, recent, tags] = await Promise.all([
    api("/api/libraries"),
    api("/api/items?shelf=continue&limit=16"),
    api("/api/recommendations").catch(() => ({ shelves: [] })),
    api("/api/items?sort=added&limit=16"),
    api("/api/tags"),
  ]);
  if (ctx.stale()) return;
  if (!libs.length) return renderWelcome(ctx);
  const total = libs.reduce((n, l) => n + l.items, 0);
  if (!total) return renderScanning(ctx);

  // Feature what you were watching, else the top pick, else the newest video.
  let pick, opts;
  const top = recs.shelves.find((s) => s.id !== "new");
  if (cont.length) [pick, opts] = [cont[0], { eyebrow: "Continue watching", eyebrowIcon: "clock", resume: true }];
  else if (top) [pick, opts] = [top.items[0], { eyebrow: top.items[0].reason || "Recommended for you", eyebrowIcon: "sparkle" }];
  else [pick, opts] = [recent[0], { eyebrow: "Recently added", eyebrowIcon: "sparkle" }];
  const detail = await api(`/api/items/${pick.id}`).catch(() => null);
  if (ctx.stale()) return;

  const genres = tags.filter((t) => t.namespace === "genre");
  const tileTags = (genres.length >= 3 ? genres : tags).slice(0, 12);
  app.replaceChildren(...h("div", {},
    hero({ ...pick, description: detail?.description }, opts),
    cont.length && shelf("Continue watching", cont,
      (it) => ({ meta: it.duration_ms ? `${fmtDuration(it.duration_ms - it.position_ms)} left` : "", reason: false })),
    recs.shelves.map((s) => shelf(s.title, s.items)),
    !recs.shelves.some((s) => s.id === "new") && recent.length && shelf("Recently added", recent),
    tileTags.length > 1 && h("section", { class: "shelf" },
      h("div", { class: "shelf-head" }, h("h2", {}, genres.length >= 3 ? "Browse by genre" : "Browse by tag")),
      h("div", { class: "tiles" }, tileTags.map(tile))),
    h("div", { class: "more-row" },
      h("a", { class: "btn btn-lg", href: "#/library" }, icon("grid"), `Browse all ${plural(total, "video")}`)),
  ).childNodes);
}

async function renderWelcome(ctx) {
  const info = await api("/api/info").catch(() => ({ lan_urls: [] }));
  if (ctx.stale()) return;
  app.replaceChildren(h("div", { class: "welcome" },
    h("div", { class: "welcome-head" },
      logo(),
      h("h1", {}, `Welcome to ${APP_NAME}`),
      h("p", {}, "Point it at a folder of videos, then watch them on any phone, tablet, laptop or TV on your Wi‑Fi.")),
    section({ title: "Add your first folder", desc: "Subfolders are included, and their names become tags: Documentaries/Nature/x.mp4 is tagged Documentaries and Nature." },
      addFolderForm(() => route())),
    info.lan_urls.length && devicesSection(info.lan_urls)));
}

function renderScanning(ctx) {
  const box = h("div", { class: "empty" }, art("refresh", true), h("h2", {}, "Looking for videos…"));
  app.replaceChildren(box);
  const scanAgain = h("button", { class: "btn btn-primary", onclick: async () => {
    await api("/api/scan", { method: "POST" }).catch(() => {});
    tick();
  } }, icon("refresh"), "Scan again");
  const tick = async () => {
    if (ctx.stale()) return;
    const st = await api("/api/scan").catch(() => null);
    if (ctx.stale()) return;
    if (st && st.running) {
      box.replaceChildren(art("refresh", true), h("h2", {}, "Looking for videos…"),
        h("p", {}, `${plural(st.seen, "file")} checked so far.`));
      setTimeout(tick, 1200);
      return;
    }
    const libs = await api("/api/libraries").catch(() => []);
    if (ctx.stale()) return;
    if (libs.some((l) => l.items)) return route();
    box.replaceChildren(art("film"), h("h2", {}, "No videos found yet"),
      h("p", {}, `Nothing playable in ${libs.map((l) => l.path).join(", ")}. ${APP_NAME} looks for MP4, MKV, MOV, WebM, AVI and other video files in these folders and their subfolders.`),
      h("div", { class: "actions" }, scanAgain, h("a", { class: "btn", href: "#/settings" }, icon("folder"), "Manage folders")));
  };
  tick();
}

// ---------- library ----------

// Filters live in the URL so views are bookmarkable and Back works:
// #/library?q=ocean&tag=genre:documentary&exclude=tag:4k&sort=added&kind=movie
function filtersFrom(params) {
  return {
    q: params.get("q") || "", tag: params.getAll("tag"), exclude: params.getAll("exclude"),
    sort: params.get("sort") || "title", kind: params.get("kind") || "",
  };
}

function libraryHash(f) {
  const p = new URLSearchParams();
  if (f.q) p.set("q", f.q);
  (f.tag || []).forEach((t) => p.append("tag", t));
  (f.exclude || []).forEach((t) => p.append("exclude", t));
  if (f.sort && f.sort !== "title") p.set("sort", f.sort);
  if (f.kind) p.set("kind", f.kind);
  const s = p.toString();
  return "#/library" + (s ? "?" + s : "");
}

const PAGE = 60;
const SORTS = [["title", "A–Z"], ["added", "Recently added"], ["played", "Recently watched"], ["year", "Newest first"], ["rating", "Your rating"]];
const FILTER_GROUPS = [["genre", "Genres"], ["tag", "Tags"], ["person", "People"], ["mood", "Moods"], ["source", "Sources"]];

let library = null; // set while the library page is showing

async function renderLibrary(params, ctx) {
  let f = filtersFrom(params);
  document.title = `Library · ${APP_NAME}`;
  if (document.activeElement !== searchBox) searchBox.value = f.q;

  const head = h("div", { class: "page-head" });
  const filtersEl = h("div", { class: "filters" });
  const results = h("div", {}, skeletonGrid(12));
  const sentinel = h("div", { class: "sentinel" });
  const expanded = new Set();
  let tags = [], items = [], total = 0, grid = null, gen = 0, loading = false, loaded = false;
  const filtered = () => Boolean(f.q || f.tag.length || f.exclude.length || f.kind);

  const setFilters = (nf) => {
    f = nf;
    history.replaceState(null, "", libraryHash(f));
    currentHash = visited[visited.length - 1] = location.hash;
    if (document.activeElement !== searchBox) searchBox.value = f.q;
    drawHead();
    drawFilters();
    load(true);
  };

  const pillSelect = (value, options, onchange, label) =>
    h("select", { class: "input pill-select", "aria-label": label, onchange: (e) => onchange(e.target.value) },
      options.map(([v, l]) => h("option", { value: v, selected: v === value }, l)));

  function drawHead() {
    head.replaceChildren(
      h("div", {},
        h("h1", {}, f.q ? `“${f.q}”` : "Library"),
        h("div", { class: "sub" }, !loaded ? " " : total ? plural(total, "video") : "No matches")),
      h("div", { class: "toolbar" },
        filtered() && h("button", { class: "btn btn-ghost btn-sm", onclick: () => setFilters({ q: "", tag: [], exclude: [], kind: "", sort: f.sort }) },
          icon("x"), "Clear filters"),
        pillSelect(f.kind, [["", "All kinds"], ...KINDS.map((k) => [k, kindLabel(k)])], (kind) => setFilters({ ...f, kind }), "Kind"),
        pillSelect(f.sort, SORTS, (sort) => setFilters({ ...f, sort }), "Sort")));
  }

  // Chip click cycles: off -> only these -> hide these -> off.
  function drawFilters() {
    const chip = (t) => {
      const state = f.tag.includes(t.ref) ? "on" : f.exclude.includes(t.ref) ? "off" : "";
      return h("button", {
        class: `chip ${t.namespace} ${state}`,
        title: state === "on" ? "Showing only these. Click to hide them instead." : state === "off" ? "Hidden. Click to clear." : `Show only ${t.name}`,
        onclick: () => {
          const nf = { ...f, tag: f.tag.filter((r) => r !== t.ref), exclude: f.exclude.filter((r) => r !== t.ref) };
          if (state === "") nf.tag.push(t.ref);
          else if (state === "on") nf.exclude.push(t.ref);
          setFilters(nf);
        },
      }, state === "on" && icon("check"), state === "off" && icon("x"), t.name, h("span", { class: "n" }, t.count));
    };
    const groups = FILTER_GROUPS.map(([ns, label]) => [ns, label, tags.filter((t) => t.namespace === ns)]).filter(([, , l]) => l.length);
    filtersEl.replaceChildren(...h("div", {},
      groups.map(([ns, label, list]) => {
        const active = list.filter((t) => f.tag.includes(t.ref) || f.exclude.includes(t.ref));
        const limit = expanded.has(ns) ? list.length : 24;
        const shown = [...new Set([...list.slice(0, limit), ...active])];
        return h("div", { class: "filter-group" },
          h("span", { class: "filter-label" }, label),
          shown.map(chip),
          list.length > shown.length && h("button", { class: "chip chip-add", onclick: () => { expanded.add(ns); drawFilters(); } },
            `+${list.length - shown.length} more`));
      }),
      tags.length > 0 && h("div", { class: "filter-hint" }, "Click a tag to show only those videos, again to hide them, once more to clear.")).childNodes);
  }

  async function load(reset) {
    if (loading && !reset) return;
    const my = reset ? ++gen : gen;
    loading = true;
    const p = new URLSearchParams({
      sort: f.sort,
      limit: reset ? Math.max(PAGE, ctx.restore?.n || 0) : PAGE,
      offset: reset ? 0 : items.length,
    });
    if (f.q) p.set("q", f.q);
    if (f.kind) p.set("kind", f.kind);
    f.tag.forEach((t) => p.append("tag", t));
    f.exclude.forEach((t) => p.append("exclude", t));
    try {
      const { data, total: t } = await api("/api/items?" + p, { withTotal: true });
      if (my !== gen || ctx.stale()) return;
      total = t;
      loaded = true;
      if (reset) {
        items = data;
        grid = data.length ? h("div", { class: "grid" }, data.map((it) => card(it))) : null;
        results.replaceChildren(grid || emptyResults());
        drawHead();
      } else {
        items.push(...data);
        grid.append(...data.map((it) => card(it)));
      }
    } catch (e) {
      if (!loaded) throw e; // first load: show the error page
      if (my === gen) toast(e.message, "error");
    } finally {
      if (my === gen) loading = false;
    }
  }

  const emptyResults = () => filtered()
    ? h("div", { class: "empty" }, art("film"), h("h2", {}, "Nothing matches"),
      h("p", {}, "Try fewer tags, or a different search."),
      h("div", { class: "actions" }, h("button", { class: "btn btn-primary", onclick: () => setFilters({ q: "", tag: [], exclude: [], kind: "", sort: f.sort }) }, "Clear filters")))
    : h("div", { class: "empty" }, art("film"), h("h2", {}, "No videos yet"),
      h("p", {}, "Add a folder of videos, or wait for the scan to finish."),
      h("div", { class: "actions" }, h("a", { class: "btn btn-primary", href: "#/settings" }, icon("folder"), "Manage folders")));

  drawHead();
  [tags] = await Promise.all([api("/api/tags"), load(true)]);
  if (ctx.stale()) return;
  app.replaceChildren(head, filtersEl, results, sentinel);
  drawFilters();

  const io = new IntersectionObserver((entries) => {
    if (entries[0].isIntersecting && !loading && items.length < total) load(false);
  }, { rootMargin: "1200px 0px" });
  io.observe(sentinel);
  library = { setQuery: (q) => setFilters({ ...f, q }) };
  pageMemo = () => ({ n: items.length });
  onLeave = () => io.disconnect();
}

// ---------- item page ----------

async function renderItem(id, params, ctx) {
  let it;
  try {
    it = await api(`/api/items/${id}`);
  } catch (e) {
    if (e.message !== "not found") throw e;
    if (ctx.stale()) return;
    app.replaceChildren(h("div", { class: "empty" }, art("film"), h("h2", {}, "This video isn't here anymore"),
      h("p", {}, "It may have been moved or deleted, or its folder was removed from the library."),
      h("div", { class: "actions" }, h("a", { class: "btn btn-primary", href: "#/" }, "Go home"))));
    return;
  }
  if (ctx.stale()) return;
  document.title = `${it.title} · ${APP_NAME}`;

  const warn = h("div", { class: "warn", hidden: true });
  // Medium player beside the side column by default; wide is remembered
  // in this browser.
  let wide = store.get("player-wide") === "1";
  const watch = h("div", { class: `watch${wide ? " wide" : ""}` });
  const setWide = (w) => {
    wide = w;
    watch.classList.toggle("wide", w);
    store.set("player-wide", w ? "1" : "0");
    player.setWide(w);
  };
  const player = createPlayer(it, warn, { autoplay: params.get("play") === "1", wide, onWide: () => setWide(!wide) });

  // Save the resume point every 10s of playback, and on pause/leave.
  let lastSave = 0;
  const save = () => {
    if (!player.started()) return; // just looking at the page isn't watching
    const pos = player.position();
    if (!pos) return;
    lastSave = Date.now();
    fetch(`/api/items/${id}/progress`, {
      method: "POST", keepalive: true,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ position_ms: Math.round(pos * 1000) }),
    }).catch(() => {}); // best effort; the next save will retry
  };
  player.video.addEventListener("timeupdate", () => { if (Date.now() - lastSave > 10000) save(); });
  player.video.addEventListener("pause", save);
  player.video.addEventListener("ended", save);
  onLeave = () => { save(); player.destroy(); };
  keyHandler = player.handleKey;

  // Each part redraws on its own, so edits never touch the player.
  const head = h("div", {});
  const desc = h("div", {});
  const similar = h("div", {});
  const update = (next, redraw = true) => {
    it = next;
    document.title = `${it.title} · ${APP_NAME}`;
    player.setTitle(it.title);
    if (redraw) { drawHead(); drawDesc(); tags.draw(); }
  };
  const edit = (field) => openEditor(it, update, field);
  const tags = tagEditor(() => it, update);

  function drawHead() {
    const kind = it.kind !== "video" && kindLabel(it.kind);
    const eyebrow = [kind, it.year].filter(Boolean).join(" · ");
    head.replaceChildren(...h("div", {},
      eyebrow && h("div", { class: "eyebrow" }, eyebrow),
      h("div", { class: "watch-title-row" },
        h("h1", {}, it.title),
        h("button", { class: "btn btn-ghost btn-icon", title: "Edit details", "aria-label": "Edit details", onclick: () => edit("title") }, icon("edit"))),
      h("div", { class: "watch-meta" },
        it.duration_ms && h("span", {}, fmtDuration(it.duration_ms)),
        it.quality && h("span", { class: "pill" }, qualityLabel(it.quality)),
        it.hdr && h("span", { class: "pill accent" }, it.hdr.toUpperCase()),
        it.language && h("span", {}, langName(it.language)),
        it.completed && h("span", { class: "pill" }, "Watched")),
      h("div", { class: "watch-actions" },
        favoriteButton(it),
        ratingStars(it, update),
        h("button", { class: "btn", onclick: () => edit("title") }, icon("edit"), "Edit details")),
    ).childNodes);
  }

  function drawDesc() {
    desc.replaceChildren(it.description
      ? h("p", { class: "description" }, it.description)
      : h("p", { class: "description-empty" }, "No description yet. ",
        h("button", { class: "link-btn", onclick: () => edit("description") }, "Add one")));
  }

  drawHead();
  drawDesc();
  watch.append(
    h("div", { class: "watch-player" }, player.el, warn),
    h("div", { class: "watch-main" }, head, desc, tags.el),
    h("aside", { class: "watch-side" }, similar, detailsCard(it)));
  app.replaceChildren(watch);

  api(`/api/items/${id}/similar`).then((items) => {
    if (ctx.stale() || !items.length) return;
    similar.replaceChildren(sideList("More like this", items));
    player.setUpNext(items.find((x) => !x.completed) || items[0]);
  }).catch(() => {});
}

function favoriteButton(it) {
  const btn = h("button", { class: "btn fav-btn" });
  const draw = () => {
    btn.replaceChildren(icon("heart"), "Favorite");
    btn.title = it.favorite ? "Remove from favorites" : "Add to favorites. Favorites shape your recommendations.";
    btn.setAttribute("aria-pressed", String(Boolean(it.favorite)));
  };
  btn.onclick = async () => {
    it.favorite = !it.favorite;
    draw();
    btn.classList.remove("pop");
    void btn.offsetWidth; // restart the animation
    if (it.favorite) btn.classList.add("pop");
    try {
      await api(`/api/items/${it.id}/favorite`, { method: "PUT", body: { favorite: it.favorite } });
    } catch (e) {
      it.favorite = !it.favorite;
      draw();
      toast(e.message, "error");
    }
  };
  draw();
  return btn;
}

// ratingStars shows the 0-10 rating as five stars; each star is worth 2.
// Clicking the current rating again clears it.
function ratingStars(it, onUpdate) {
  const label = h("span", { class: "stars-label" });
  const stars = [1, 2, 3, 4, 5].map((i) => h("button", { "aria-label": `Rate ${i * 2} out of 10`, title: `${i * 2}/10` }, icon("star")));
  const paint = (val) => {
    const n = Math.round((val ?? 0) / 2);
    stars.forEach((b, i) => b.classList.toggle("on", i < n));
    label.textContent = val != null ? `${val}/10` : "Rate";
  };
  stars.forEach((b, i) => {
    b.addEventListener("mouseenter", () => paint((i + 1) * 2));
    b.addEventListener("click", async () => {
      const prev = it.rating;
      const val = prev === (i + 1) * 2 ? null : (i + 1) * 2;
      it.rating = val;
      paint(val);
      try {
        onUpdate(await api(`/api/items/${it.id}`, { method: "PATCH", body: { rating: val } }), false);
        toast(val != null ? `Rated ${val}/10` : "Rating cleared");
      } catch (e) {
        it.rating = prev;
        paint(prev);
        toast(e.message, "error");
      }
    });
  });
  const wrap = h("div", { class: "stars", role: "group", "aria-label": "Your rating", onmouseleave: () => paint(it.rating) }, stars, label);
  paint(it.rating);
  return wrap;
}

function tagEditor(getIt, onUpdate) {
  const el = h("div", {});
  const suggestions = h("datalist", { id: "tag-suggestions" });
  api("/api/tags").then((all) => suggestions.replaceChildren(...all.map((t) =>
    h("option", { value: t.namespace === "tag" ? t.name : `${t.namespace}:${t.name}` })))).catch(() => {});
  const input = h("input", { class: "input chip-input", list: "tag-suggestions", placeholder: "Tag, or genre:Jazz", "aria-label": "Add tags", autocomplete: "off" });
  let adding = false;
  const patch = (body) => api(`/api/items/${getIt().id}`, { method: "PATCH", body });

  const add = async () => {
    const refs = input.value.split(",").map((s) => s.trim()).filter(Boolean);
    if (!refs.length) { adding = false; draw(); return; }
    input.disabled = true;
    try {
      onUpdate(await patch({ add_tags: refs }), false);
      input.value = "";
      draw();
    } catch (e) {
      toast(e.message, "error");
    } finally {
      input.disabled = false;
      input.focus();
    }
  };
  const remove = async (t) => {
    const it = getIt();
    const before = it.tags;
    it.tags = it.tags.filter((x) => x.ref !== t.ref);
    draw();
    try {
      onUpdate(await patch({ remove_tags: [t.ref] }), false);
      draw();
    } catch (e) {
      it.tags = before;
      draw();
      toast(e.message, "error");
    }
  };
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); add(); }
    if (e.key === "Escape") { e.preventDefault(); input.value = ""; adding = false; draw(); }
  });
  input.addEventListener("blur", () => setTimeout(() => {
    if (adding && !input.disabled && !input.value.trim() && document.activeElement !== input) { adding = false; draw(); }
  }, 150));

  function draw() {
    const it = getIt();
    el.replaceChildren(...h("div", {},
      h("div", { class: "section-label" }, "Genres & tags"),
      h("div", { class: "tag-list" },
        it.tags.map((t) => h("span", { class: `chip ${t.namespace}`, title: [NS_LABELS[t.namespace], SOURCE_LABELS[t.source]].filter(Boolean).join(" · ") },
          h("a", { href: libraryHash({ tag: [t.ref] }) }, t.name),
          h("button", { class: "chip-x", "aria-label": `Remove ${t.name}`, title: "Remove", onclick: () => remove(t) }, icon("x")))),
        adding ? input : h("button", { class: "chip chip-add", onclick: () => { adding = true; draw(); input.focus(); } },
          icon("plus"), it.tags.length ? "Add" : "Add genres or tags")),
      adding && h("p", { class: "hint tag-help" },
        "Press Enter to add, commas for several. Start with genre:, person: or mood: to group them, like genre:Jazz or person:Nina Simone."),
      suggestions).childNodes);
  }
  draw();
  return { el, draw };
}

function detailsCard(it) {
  const audio = it.streams.filter((s) => s.type === "audio")
    .map((s) => [codecName(s.codec), channelName(s.channels), langName(s.language)].filter(Boolean).join(" · ")).join(", ");
  const subs = it.streams.filter((s) => s.type === "subtitle").map((s) => langName(s.language) || codecName(s.codec)).join(", ");
  const kv = (rows) => h("dl", { class: "kv" }, rows.filter(([, v]) => v)
    .flatMap(([k, v, cls]) => [h("dt", {}, k), h("dd", { class: cls }, v)]));
  return h("div", { class: "side-card" },
    h("h3", {}, "About this file"),
    kv([
      ["Quality", [qualityLabel(it.quality), it.hdr && it.hdr.toUpperCase()].filter(Boolean).join(" · ")],
      ["Resolution", it.width && `${it.width} × ${it.height}`],
      ["Length", it.duration_ms && clock(it.duration_ms / 1000)],
      ["Video", codecName(it.video_codec)],
      ["Audio", audio],
      ["Subtitles", subs],
      ["Size", fmtBytes(it.size_bytes)],
    ]),
    it.files.length && h("div", { class: "file-box" },
      h("div", { class: "label" }, it.files.length > 1 ? "Files" : "File"),
      it.files.map((f) => h("div", { class: `file-path${f.missing ? " missing" : ""}`, title: f.missing ? "Not found in the last scan" : null },
        h("span", { class: "lib" }, f.library), " / ", f.path))),
    h("details", { class: "more" },
      h("summary", {}, icon("chevronRight"), "Technical details"),
      kv([
        ["Container", it.container],
        ["Bitrate", it.bitrate && `${(it.bitrate / 1e6).toFixed(1)} Mb/s`],
        ["Playback", PLAYBACK[it.playback_mode]],
        ["Content ID", it.content_hash, "mono"],
        ["Problem", it.probe_error],
      ])));
}

// sideList is a compact vertical list of videos for the side column.
function sideList(title, items) {
  return h("section", { class: "side-list" },
    h("h3", {}, title),
    items.map((it) => {
      const pct = it.position_ms > 0 && it.duration_ms ? Math.min(100, (it.position_ms / it.duration_ms) * 100) : 0;
      return h("a", { class: "mini", href: `#/item/${it.id}`, title: it.title },
        h("div", { class: "thumb" },
          poster(it),
          it.duration_ms && h("span", { class: "badge dur" }, fmtDuration(it.duration_ms)),
          pct && h("div", { class: "progress" }, h("i", { style: `width:${pct}%` }))),
        h("div", {},
          h("div", { class: "mini-title" }, it.title),
          h("div", { class: "card-meta" }, cardMeta(it) || "\u00a0")));
    }));
}

function srcBadge(it, field) {
  const s = it.sources[field];
  if (!s) return null;
  return h("span", { class: `src${s === "user" ? " user" : ""}`, title: s === "user" ? "You set this. Rescans won't change it." : "Detected automatically. Edit it to keep your own." },
    SOURCE_LABELS[s] || s);
}

// openEditor shows the edit form in a side drawer over the page, so the
// video keeps playing. onSaved gets the updated item.
function openEditor(it, onSaved, focusField = "title") {
  const prevFocus = document.activeElement;
  const f = {
    title: h("input", { class: "input", value: it.title }),
    original_title: h("input", { class: "input", value: it.original_title ?? "", placeholder: "If it's known by another name" }),
    year: h("input", { class: "input", type: "number", inputmode: "numeric", value: it.year ?? "", min: 1870, max: 2100 }),
    kind: h("select", { class: "input" }, KINDS.map((k) => h("option", { value: k, selected: k === it.kind }, kindLabel(k)))),
    rating: h("input", { class: "input", type: "number", inputmode: "numeric", value: it.rating ?? "", min: 0, max: 10, placeholder: "0–10" }),
    language: h("input", { class: "input", value: it.language ?? "", placeholder: "en, fr, yo…" }),
    description: h("textarea", { class: "input", value: it.description ?? "", placeholder: "What's it about?", rows: 6 }),
  };
  const field = (name, label) => labeled(label, f[name], srcBadge(it, name));
  const error = h("p", { class: "form-error", "aria-live": "polite" });

  const diff = () => {
    const body = {};
    const num = (v) => (v === "" ? null : Number(v));
    const str = (v) => v.trim() || null;
    if (f.title.value.trim() !== it.title) body.title = f.title.value.trim();
    if (str(f.original_title.value) !== (it.original_title ?? null)) body.original_title = f.original_title.value.trim();
    if (num(f.year.value) !== (it.year ?? null)) body.year = num(f.year.value);
    if (f.kind.value !== it.kind) body.kind = f.kind.value;
    if (num(f.rating.value) !== (it.rating ?? null)) body.rating = num(f.rating.value);
    if (str(f.language.value) !== (it.language ?? null)) body.language = f.language.value.trim();
    if (f.description.value !== (it.description ?? "")) body.description = f.description.value;
    return body;
  };

  const saveBtn = h("button", { class: "btn btn-primary" }, icon("check"), "Save");
  const save = async () => {
    const body = diff();
    if (!Object.keys(body).length) return close();
    if (body.title === "") { error.textContent = "The title can't be empty."; f.title.focus(); return; }
    saveBtn.classList.add("busy");
    error.textContent = "";
    try {
      onSaved(await api(`/api/items/${it.id}`, { method: "PATCH", body }));
      toast("Changes saved");
      close();
    } catch (e) {
      error.textContent = e.message;
    } finally {
      saveBtn.classList.remove("busy");
    }
  };
  saveBtn.onclick = save;

  const scrim = h("div", { class: "scrim", onclick: () => close() });
  const drawer = h("aside", { class: "drawer", role: "dialog", "aria-modal": "true", "aria-labelledby": "edit-title" },
    h("div", { class: "drawer-head" },
      h("h2", { id: "edit-title" }, "Edit details"),
      h("button", { class: "btn btn-ghost btn-icon", "aria-label": "Close", onclick: () => close() }, icon("x"))),
    h("div", { class: "drawer-body" },
      field("title", "Title"),
      field("original_title", "Original title"),
      h("div", { class: "field-row" }, field("year", "Year"), field("kind", "Kind")),
      h("div", { class: "field-row" }, field("rating", "Your rating"), field("language", "Language")),
      field("description", "Description"),
      error,
      h("p", { class: "hint" }, "Your edits are kept when the library is rescanned, and travel with exports.")),
    h("div", { class: "drawer-foot" },
      h("span", { class: "hint" }, isMac ? "⌘ Return to save" : "Ctrl+Enter to save"),
      h("button", { class: "btn btn-ghost", onclick: () => close() }, "Cancel"),
      saveBtn));

  const onKey = (e) => {
    if (e.key === "Escape") { e.preventDefault(); close(); }
    else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); save(); }
    else if (e.key === "Enter" && e.target.tagName === "INPUT" && drawer.contains(e.target)) { e.preventDefault(); save(); }
  };
  let closed = false;
  function close() {
    if (closed) return;
    closed = true;
    document.removeEventListener("keydown", onKey, true);
    drawer.classList.remove("open");
    scrim.classList.remove("open");
    document.body.style.overflow = "";
    overlayOpen = false;
    closeOverlay = null;
    setTimeout(() => { drawer.remove(); scrim.remove(); }, 350);
    if (prevFocus && prevFocus.isConnected) prevFocus.focus({ preventScroll: true });
  }

  document.addEventListener("keydown", onKey, true);
  document.body.append(scrim, drawer);
  document.body.style.overflow = "hidden";
  overlayOpen = true;
  closeOverlay = close;
  const first = f[focusField] || f.title;
  first.focus({ preventScroll: true });
  if (first.tagName === "INPUT") first.select();
  requestAnimationFrame(() => {
    scrim.classList.add("open");
    drawer.classList.add("open");
  });
}

// ---------- player ----------

// createPlayer picks how to play an item:
//   direct  - <video src=/stream/id>, range requests
//   HLS     - the server repackages with ffmpeg: video copied (remux), or
//             re-encoded to H.264 if this browser can't decode it (transcode).
//             Played with hls.js (or natively on Safari/iOS). The stream
//             starts at an offset, so the seek bar covers the whole file and
//             we map times ourselves. Nothing starts on the server until play.
// If direct play fails it falls back to HLS; if copying fails, to transcoding.
// Both modes share one set of controls.
//
// opts: autoplay, wide (current layout), onWide (toggle the wide layout).
const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2];
let playbackSpeed = 1; // kept while the app is open, like most video sites

function createPlayer(it, warn, opts = {}) {
  const video = h("video", { preload: "metadata", playsinline: true, "webkit-playsinline": true, poster: `/api/items/${it.id}/thumbnail` });
  const el = h("div", { class: "player paused" }, video);
  const startAt = it.position_ms > 0 ? it.position_ms / 1000 : 0;
  let mode = it.playback_mode && it.playback_mode !== "direct" ? "hls" : "direct";
  let transcode = !canDecode(it.video_codec);
  let hlsPlayer = null, offset = 0, session = null, destroyed = false;
  let hlsStarted = false, pending = startAt, upNext = null, upNextEl = null;

  const showWarn = (msg) => { warn.hidden = false; warn.textContent = msg; };
  const setBusy = (b) => el.classList.toggle("busy", b);

  const vol = parseFloat(store.get("volume"));
  if (vol >= 0 && vol <= 1) video.volume = vol;
  if (store.get("muted") === "1") video.muted = true;
  video.playbackRate = video.defaultPlaybackRate = playbackSpeed;

  // ---- direct ----
  function startDirect() {
    video.src = `/stream/${it.id}`;
    video.addEventListener("loadedmetadata", () => {
      if (mode === "direct" && startAt && startAt < video.duration - 5) video.currentTime = startAt;
    }, { once: true });
    video.addEventListener("error", () => {
      if (mode !== "direct" || destroyed) return;
      mode = "hls";
      pending = video.currentTime || startAt;
      if (el.classList.contains("started")) startHLS(pending);
    }, { once: true });
  }

  // ---- HLS ----
  const total = () => {
    if (mode === "direct") return isFinite(video.duration) && video.duration ? video.duration : (it.duration_ms || 0) / 1000;
    return it.duration_ms ? it.duration_ms / 1000 : offset + (isFinite(video.duration) ? video.duration : 0);
  };

  async function startHLS(at) {
    hlsStarted = true;
    video.removeAttribute("src");
    session ||= Math.random().toString(36).slice(2) + Date.now().toString(36);
    setBusy(true);
    try {
      const r = await api(`/api/items/${it.id}/hls`, { method: "POST", body: { session, start: at, transcode } });
      if (destroyed) return;
      offset = r.start;
      badge.hidden = !r.encoder;
      badge.title = r.encoder ? `This browser can't play the original format, so the server converts it to H.264 as you watch (${r.encoder}).` : "";
      await loadSource(r.url);
      video.play().catch(() => {}); // autoplay may be blocked; the play button still works
    } catch (e) {
      showWarn(`This video couldn't be prepared for streaming: ${e.message}`);
    } finally {
      setBusy(false);
    }
  }

  async function loadSource(url) {
    if (hlsPlayer) { hlsPlayer.destroy(); hlsPlayer = null; }
    const Hls = await loadHlsJs();
    if (Hls && Hls.isSupported()) {
      hlsPlayer = new Hls({ startPosition: 0, maxBufferLength: 60 });
      hlsPlayer.on(Hls.Events.ERROR, (_, data) => {
        if (!data.fatal) return;
        // Usually the session expired while paused: restart where we were,
        // but give up if it keeps failing.
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR && (restarts = recentRestarts() + 1) <= 3) startHLS(position());
        else if (data.type === Hls.ErrorTypes.NETWORK_ERROR) showWarn("Lost the connection to the server.");
        else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && !mediaRecovered) {
          mediaRecovered = true;
          hlsPlayer.recoverMediaError();
        } else if (!transcode) {
          // The browser claimed it could decode the copied video but can't.
          transcode = true;
          mediaRecovered = false;
          startHLS(position());
        } else showWarn("Playback failed in this browser.");
      });
      hlsPlayer.loadSource(url);
      hlsPlayer.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = url; // Safari / iOS play HLS natively
    } else {
      showWarn("This browser can't play HLS streams.");
    }
  }

  let restarts = 0, restartWindow = 0, mediaRecovered = false;
  function recentRestarts() {
    if (Date.now() - restartWindow > 30000) { restartWindow = Date.now(); restarts = 0; }
    return restarts;
  }

  // Seek within what's already been produced; otherwise restart the stream there.
  function seek(t) {
    t = Math.max(0, Math.min(t, Math.max(0, total() - 1)));
    if (mode === "direct") { video.currentTime = t; render(); return; }
    if (!hlsStarted) { pending = t; render(); return; }
    const s = video.seekable;
    const end = s.length ? offset + s.end(s.length - 1) : offset;
    if (t >= offset && t <= end - 1) video.currentTime = t - offset;
    else startHLS(t);
  }

  function position() {
    if (mode === "hls") return hlsStarted ? offset + (video.currentTime || 0) : pending;
    return video.readyState >= 1 ? video.currentTime : pending;
  }

  function play() {
    hideUpNext();
    el.classList.add("started");
    if (mode === "hls" && !hlsStarted) { startHLS(pending); return; }
    // If the browser blocks autoplay, go back to the big play button.
    video.play().catch(() => { if (video.paused) el.classList.remove("started"); });
  }
  const isPaused = () => video.paused || (mode === "hls" && !hlsStarted);
  const togglePlay = () => (isPaused() ? play() : video.pause());

  function toggleFullscreen() {
    if (document.fullscreenElement || document.webkitFullscreenElement) (document.exitFullscreen || document.webkitExitFullscreen).call(document);
    else if (el.requestFullscreen) el.requestFullscreen().catch(() => {});
    else if (el.webkitRequestFullscreen) el.webkitRequestFullscreen();
    else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen(); // iPhone
  }

  // ---- controls ----
  const btn = (name, label, onclick, cls = "") =>
    h("button", { class: `pc-btn ${cls}`, "aria-label": label, "data-tip": label, onclick }, icon(name));
  const setBtn = (b, name, label) => {
    b.replaceChildren(icon(name));
    b.setAttribute("aria-label", label);
    b.dataset.tip = label;
  };

  const bigPlay = h("button", { class: "big-play", "aria-label": "Play", onclick: (e) => { e.stopPropagation(); play(); } }, icon("play"));
  const playBtn = btn("play", "Play (k)", togglePlay, "tip-start");
  const skip = (d) => { seek(position() + d); osd(d < 0 ? "back10" : "fwd10", `${Math.abs(d)} s`); };
  const backBtn = btn("back10", "Back 10 s (j)", () => skip(-10), "pc-skip");
  const fwdBtn = btn("fwd10", "Forward 10 s (l)", () => skip(10), "pc-skip");
  const muteBtn = btn(video.muted ? "mute" : "volume", "Mute (m)", () => { video.muted = !video.muted; });
  const volume = h("input", { class: "pc-vol", type: "range", min: 0, max: 1, step: 0.05, "aria-label": "Volume" });
  volume.addEventListener("input", () => { video.volume = +volume.value; video.muted = +volume.value === 0; });
  const timeNow = h("span", {});
  const timeTotal = h("span", { class: "total" });
  const time = h("span", { class: "pc-time" }, timeNow, " / ", timeTotal);
  const badge = h("span", { class: "pc-badge", hidden: true }, "Converting");

  const speedBtn = h("button", { class: "pc-btn pc-speed", "aria-label": "Playback speed", "data-tip": "Speed", "aria-haspopup": "menu", "aria-expanded": "false" });
  const speedMenu = h("div", { class: "pc-menu", role: "menu", hidden: true });
  const drawSpeed = () => {
    speedBtn.textContent = `${playbackSpeed}×`;
    speedMenu.replaceChildren(h("div", { class: "pc-menu-title" }, "Speed"),
      ...SPEEDS.map((s) => h("button", { role: "menuitemradio", "aria-checked": String(s === playbackSpeed), onclick: (e) => { e.stopPropagation(); setSpeed(s); } },
        s === 1 ? "Normal" : `${s}×`)));
  };
  const setSpeed = (s) => {
    playbackSpeed = s;
    video.playbackRate = video.defaultPlaybackRate = s;
    drawSpeed();
    closeMenu();
  };
  const closeMenu = () => { speedMenu.hidden = true; speedBtn.setAttribute("aria-expanded", "false"); };
  speedBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    speedMenu.hidden = !speedMenu.hidden;
    speedBtn.setAttribute("aria-expanded", String(!speedMenu.hidden));
    poke();
  });
  drawSpeed();

  const pipBtn = document.pictureInPictureEnabled && btn("pip", "Picture in picture", () => {
    if (document.pictureInPictureElement) document.exitPictureInPicture();
    else video.requestPictureInPicture().catch(() => {});
  }, "pc-pip");
  const wideBtn = opts.onWide && btn(opts.wide ? "narrow" : "wide", opts.wide ? "Default view (t)" : "Wide view (t)", () => opts.onWide(), "pc-wide");
  const fullBtn = btn("fullscreen", "Full screen (f)", toggleFullscreen, "tip-end");

  const buf = h("div", { class: "pc-buf" });
  const hover = h("div", { class: "pc-hover" });
  const done = h("div", { class: "pc-done" });
  const rail = h("div", { class: "pc-rail" }, buf, hover, done);
  const knob = h("div", { class: "pc-knob" });
  const tip = h("div", { class: "pc-tip" });
  const track = h("div", { class: "pc-track", role: "slider", tabindex: 0, "aria-label": "Seek", "aria-valuemin": 0 }, rail, knob, tip);

  const osdEl = h("div", { class: "pc-osd", "aria-hidden": "true" });
  const titleEl = h("div", { class: "pc-title" }, it.title);
  el.append(
    h("div", { class: "pc-top" }, titleEl),
    osdEl, bigPlay,
    h("div", { class: "pc" },
      track,
      h("div", { class: "pc-row" },
        playBtn, backBtn, fwdBtn, h("div", { class: "pc-volgroup" }, muteBtn, volume), time,
        h("div", { class: "pc-spacer" }),
        badge, h("div", { class: "pc-speedwrap" }, speedBtn, speedMenu), pipBtn, wideBtn, fullBtn)));

  let osdTimer;
  function osd(name, text) {
    osdEl.replaceChildren(...[icon(name), text && h("span", {}, text)].filter(Boolean));
    osdEl.classList.remove("show");
    void osdEl.offsetWidth; // restart the animation
    osdEl.classList.add("show");
    clearTimeout(osdTimer);
    osdTimer = setTimeout(() => osdEl.classList.remove("show"), 700);
  }

  let dragging = false, dragT = 0;
  const pct = (t) => { const tt = total(); return tt ? Math.min(100, Math.max(0, (t / tt) * 100)) : 0; };
  function render() {
    const tt = total();
    const pos = dragging ? dragT : position();
    done.style.width = knob.style.left = `${pct(pos)}%`;
    let from = 0, to = 0;
    if (mode === "hls" && hlsStarted) {
      const s = video.seekable;
      from = offset;
      to = s.length ? offset + s.end(s.length - 1) : offset;
    } else if (mode === "direct") {
      const b = video.buffered;
      for (let i = 0; i < b.length; i++) {
        if (b.start(i) <= video.currentTime + 1 && b.end(i) >= video.currentTime) { from = b.start(i); to = b.end(i); }
      }
    }
    buf.style.left = `${pct(from)}%`;
    buf.style.width = `${Math.max(0, pct(to) - pct(from))}%`;
    timeNow.textContent = clock(pos);
    timeTotal.textContent = clock(tt);
    track.setAttribute("aria-valuemax", Math.floor(tt));
    track.setAttribute("aria-valuenow", Math.floor(pos));
    track.setAttribute("aria-valuetext", `${clock(pos)} of ${clock(tt)}`);
  }

  const timeAt = (x) => {
    const r = rail.getBoundingClientRect();
    return Math.min(1, Math.max(0, (x - r.left) / r.width)) * total();
  };
  const showTip = (x) => {
    const r = rail.getBoundingClientRect();
    const px = Math.min(r.width, Math.max(0, x - r.left));
    tip.textContent = clock(timeAt(x));
    tip.style.left = `${Math.min(r.width - 26, Math.max(26, px))}px`;
    hover.style.width = `${px}px`;
  };
  track.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    track.setPointerCapture(e.pointerId);
    dragging = true;
    track.classList.add("drag");
    dragT = timeAt(e.clientX);
    showTip(e.clientX);
    render();
  });
  track.addEventListener("pointermove", (e) => {
    showTip(e.clientX);
    if (dragging) { dragT = timeAt(e.clientX); render(); }
  });
  const endDrag = (commit) => {
    if (!dragging) return;
    dragging = false;
    track.classList.remove("drag");
    if (commit) seek(dragT);
    render();
    poke();
  };
  track.addEventListener("pointerup", () => endDrag(true));
  track.addEventListener("pointercancel", () => endDrag(false));

  // Controls hide after a few seconds of playback without mouse movement.
  let idleTimer;
  function poke() {
    el.classList.remove("idle");
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      if (!video.paused && !dragging && speedMenu.hidden) el.classList.add("idle");
    }, 2800);
  }
  let pointerType = "mouse";
  el.addEventListener("pointerdown", (e) => { pointerType = e.pointerType; });
  el.addEventListener("pointermove", (e) => { if (e.pointerType === "mouse") poke(); });
  el.addEventListener("mouseleave", () => { if (!video.paused && speedMenu.hidden) el.classList.add("idle"); });
  document.addEventListener("click", closeMenu); // any click outside the menu closes it

  // Mouse: click plays/pauses, double click goes full screen.
  // Touch: tap shows or hides the controls; double tap on the left or right
  // third skips 10 seconds, like on phone video apps.
  let lastTap = 0, tapTimer;
  video.addEventListener("click", (e) => {
    if (pointerType === "mouse") return togglePlay();
    if (!el.classList.contains("started")) return play();
    const r = el.getBoundingClientRect();
    const x = (e.clientX - r.left) / r.width;
    const now = Date.now();
    if (now - lastTap < 300 && (x < 1 / 3 || x > 2 / 3)) {
      clearTimeout(tapTimer);
      skip(x < 1 / 3 ? -10 : 10);
      lastTap = now; // keep tapping to keep skipping
      return;
    }
    lastTap = now;
    clearTimeout(tapTimer);
    tapTimer = setTimeout(() => {
      if (el.classList.contains("idle") || video.paused) poke();
      else el.classList.add("idle");
    }, 260);
  });
  video.addEventListener("dblclick", () => { if (pointerType === "mouse") toggleFullscreen(); });

  for (const ev of ["timeupdate", "progress", "durationchange", "loadedmetadata", "seeked"]) video.addEventListener(ev, render);
  video.addEventListener("play", () => {
    el.classList.add("started");
    el.classList.remove("paused");
    setBtn(playBtn, "pause", "Pause (k)");
    poke();
  });
  video.addEventListener("pause", () => {
    el.classList.add("paused");
    el.classList.remove("idle");
    setBtn(playBtn, "play", "Play (k)");
  });
  video.addEventListener("waiting", () => setBusy(true));
  video.addEventListener("playing", () => setBusy(false));
  const drawVolume = () => {
    const silent = video.muted || video.volume === 0;
    setBtn(muteBtn, silent ? "mute" : "volume", silent ? "Unmute (m)" : "Mute (m)");
    volume.value = video.muted ? 0 : video.volume;
    volume.style.setProperty("--fill", `${(video.muted ? 0 : video.volume) * 100}%`);
  };
  video.addEventListener("volumechange", () => {
    drawVolume();
    store.set("volume", String(video.volume));
    store.set("muted", video.muted ? "1" : "0");
  });
  drawVolume();
  video.addEventListener("ended", () => { if (upNext) showUpNext(); });

  const onFullscreen = () => {
    const fs = (document.fullscreenElement || document.webkitFullscreenElement) === el;
    setBtn(fullBtn, fs ? "exitFullscreen" : "fullscreen", fs ? "Exit full screen (f)" : "Full screen (f)");
  };
  document.addEventListener("fullscreenchange", onFullscreen);
  document.addEventListener("webkitfullscreenchange", onFullscreen);

  function showUpNext() {
    hideUpNext();
    const n = upNext;
    upNextEl = h("div", { class: "up-next", onclick: (e) => e.stopPropagation() },
      h("button", { class: "pc-btn up-next-close", "aria-label": "Close", onclick: hideUpNext }, icon("x")),
      h("div", { class: "up-next-card" },
        h("div", { class: "thumb" }, poster(n)),
        h("div", {},
          h("div", { class: "eyebrow" }, "Up next"),
          h("h3", {}, n.title),
          h("div", { class: "up-next-actions" },
            h("a", { class: "btn btn-primary", href: `#/item/${n.id}?play=1` }, icon("play"), "Play"),
            h("button", { class: "btn btn-glass", onclick: () => { seek(0); play(); } }, icon("replay"), "Watch again")))));
    el.append(upNextEl);
  }
  function hideUpNext() { if (upNextEl) { upNextEl.remove(); upNextEl = null; } }

  // Keyboard shortcuts, as on most video sites. Returns true if handled.
  function handleKey(e) {
    const k = e.key.length === 1 ? e.key.toLowerCase() : e.key;
    switch (k) {
      case " ": case "k": {
        const willPlay = isPaused();
        togglePlay();
        osd(willPlay ? "play" : "pause");
        break;
      }
      case "ArrowLeft": seek(position() - 5); osd("rewind", "5 s"); break;
      case "ArrowRight": seek(position() + 5); osd("forward", "5 s"); break;
      case "j": skip(-10); break;
      case "l": skip(10); break;
      case "f": toggleFullscreen(); break;
      case "t": if (!opts.onWide) return false; opts.onWide(); break;
      case "m":
        video.muted = !video.muted;
        osd(video.muted ? "mute" : "volume", video.muted ? "Muted" : `${Math.round(video.volume * 100)}%`);
        break;
      default:
        if (/^[0-9]$/.test(k)) { seek((total() * Number(k)) / 10); break; } // 0-9 jump to 0%-90%
        return false;
    }
    poke();
    return true;
  }

  render();
  if (mode === "direct") startDirect();
  if (opts.autoplay) play();

  return {
    el, video, position, handleKey,
    started: () => el.classList.contains("started"),
    setUpNext: (item) => { upNext = item; },
    setTitle: (t) => { titleEl.textContent = t; },
    setWide: (w) => { if (wideBtn) setBtn(wideBtn, w ? "narrow" : "wide", w ? "Default view (t)" : "Wide view (t)"); },
    destroy() {
      destroyed = true;
      clearTimeout(idleTimer);
      clearTimeout(osdTimer);
      document.removeEventListener("fullscreenchange", onFullscreen);
      document.removeEventListener("webkitfullscreenchange", onFullscreen);
      document.removeEventListener("click", closeMenu);
      if (hlsPlayer) hlsPlayer.destroy();
      video.removeAttribute("src");
      video.load();
      if (session) fetch(`/api/hls/${session}`, { method: "DELETE", keepalive: true });
    },
  };
}

// canDecode reports whether this browser can play a video codec inside MP4,
// either through Media Source (hls.js) or natively (Safari).
function canDecode(codec) {
  if (codec === "h264") return true;
  const types = {
    hevc: 'video/mp4; codecs="hvc1.1.6.L93.B0"',
    vp9: 'video/mp4; codecs="vp09.00.10.08"',
    av1: 'video/mp4; codecs="av01.0.05M.08"',
  };
  const type = types[codec];
  if (!type) return false; // MPEG-4/XviD, VC-1, MPEG-2, unknown: always transcode
  // Ask the API that will actually play it: hls.js uses Media Source when it
  // exists; only without it does the browser play HLS natively.
  const mse = window.ManagedMediaSource || window.MediaSource;
  return mse ? mse.isTypeSupported(type) : Boolean(document.createElement("video").canPlayType(type));
}

// hls.js is ~400KB, so it's only loaded when a video needs it.
let hlsJsPromise = null;
function loadHlsJs() {
  hlsJsPromise ||= new Promise((resolve) => {
    const s = h("script", { src: "/vendor/hls.min.js" });
    s.onload = () => resolve(window.Hls);
    s.onerror = () => resolve(null);
    document.head.append(s);
  });
  return hlsJsPromise;
}

// ---------- settings ----------

async function renderSettings(ctx) {
  document.title = `Settings · ${APP_NAME}`;
  const [libs, info] = await Promise.all([api("/api/libraries"), api("/api/info").catch(() => ({ lan_urls: [] }))]);
  if (ctx.stale()) return;
  const scanBox = h("div", {});
  const scanBtn = h("button", { class: "btn btn-primary", onclick: async () => {
    try { await api("/api/scan", { method: "POST" }); } catch { /* already running */ }
    pollScan(scanBox, scanBtn);
  } }, icon("refresh"), "Scan now");

  app.replaceChildren(h("div", { class: "settings" },
    h("div", { class: "page-head" }, h("div", {}, h("h1", {}, "Settings"),
      h("div", { class: "sub" }, "Your folders, other devices and backups"))),
    section({ title: "Your folders", desc: `${APP_NAME} finds the videos in these folders and keeps checking for new ones. Your videos are never changed.`, action: scanBtn },
      libs.length ? libs.map(libCard) : h("p", { class: "hint" }, "No folders yet. Add one below."),
      scanBox),
    section({ title: "Add a folder", desc: `A folder on the computer running ${APP_NAME}, not on the device you're browsing from. Subfolders are included.` },
      addFolderForm(() => route())),
    info.lan_urls.length && devicesSection(info.lan_urls),
    backupSection()));
  pollScan(scanBox, scanBtn);
}

function libCard(lib) {
  const toggle = (key, title, text) => {
    const box = h("input", { type: "checkbox", checked: lib[key] });
    box.addEventListener("change", async () => {
      try {
        await api(`/api/libraries/${lib.id}`, { method: "PATCH", body: { [key]: box.checked } });
        toast(box.checked ? `Turned on: ${title}` : `Turned off: ${title}`);
      } catch (e) {
        box.checked = !box.checked;
        toast(e.message, "error");
      }
    });
    return h("label", { class: "switch" }, box, h("span", { class: "switch-text" }, h("strong", {}, title), h("span", {}, text)));
  };
  const kind = h("select", { class: "input pill-select", "aria-label": "Kind for new videos", onchange: async () => {
    try {
      await api(`/api/libraries/${lib.id}`, { method: "PATCH", body: { default_kind: kind.value } });
      toast(`New videos in ${lib.name} will be marked ${kindLabel(kind.value)}`);
    } catch (e) { toast(e.message, "error"); }
  } }, KINDS.map((k) => h("option", { value: k, selected: k === lib.default_kind }, kindLabel(k))));
  const remove = async () => {
    const ok = await confirmDialog({
      title: `Remove “${lib.name}”?`,
      body: "Its videos disappear from the library. Files on disk are not touched, and your edits come back if you add the folder again.",
      confirm: "Remove", danger: true,
    });
    if (!ok) return;
    try {
      await api(`/api/libraries/${lib.id}`, { method: "DELETE" });
      toast(`Removed ${lib.name}`);
      route();
    } catch (e) { toast(e.message, "error"); }
  };
  return h("div", { class: "lib-card" },
    h("div", { class: "lib-icon" }, icon("folder")),
    h("div", { class: "lib-main" },
      h("div", { class: "lib-title" }, h("strong", {}, lib.name), h("span", { class: "lib-count" }, plural(lib.items, "video"))),
      h("div", { class: "lib-path" }, lib.path),
      h("div", { class: "lib-scanned" }, lib.last_scan_at ? `Checked ${relTime(lib.last_scan_at * 1000)}` : "Not checked yet"),
      h("div", { class: "lib-toggles" },
        toggle("folder_tags", "Tag videos by folder", "New videos get their folder names as tags, like Documentaries and Nature."),
        toggle("write_sidecars", "Keep edits in this folder", `Saves your titles, tags and ratings in one hidden file, .itan.json, so they travel with the drive. Another ${APP_NAME} reads it when you add this folder.`),
        h("div", { class: "toolbar" }, h("span", { class: "hint" }, "New videos here are"), kind))),
    h("div", { class: "lib-actions" }, h("button", { class: "btn btn-danger btn-sm", onclick: remove }, icon("trash"), "Remove")));
}

function addFolderForm(onAdded) {
  const path = h("input", { class: "input", placeholder: "/Users/you/Movies", autocomplete: "off", spellcheck: "false" });
  const name = h("input", { class: "input", placeholder: "Name" });
  const kind = h("select", { class: "input" }, KINDS.map((k) => h("option", { value: k }, kindLabel(k))));
  const err = h("p", { class: "form-error", hidden: true });
  const btn = h("button", { class: "btn btn-primary", type: "submit", style: "height:42px" }, icon("plus"), "Add folder");
  path.addEventListener("input", () => {
    name.placeholder = path.value.trim().replace(/[\\/]+$/, "").split(/[\\/]/).pop() || "Name";
  });
  return h("form", { onsubmit: async (e) => {
    e.preventDefault();
    err.hidden = true;
    if (!path.value.trim()) { err.textContent = "Enter the folder's full path."; err.hidden = false; path.focus(); return; }
    btn.classList.add("busy");
    try {
      await api("/api/libraries", { method: "POST", body: { name: name.value.trim(), path: path.value.trim(), default_kind: kind.value } });
      toast(`Added ${name.value.trim() || name.placeholder}. Looking for videos…`);
      onAdded();
    } catch (x) {
      err.textContent = x.message.replace("path is not a readable folder on the server", "That folder doesn't exist on this computer, or can't be read.");
      err.hidden = false;
    } finally {
      btn.classList.remove("busy");
    }
  } },
  h("div", { class: "add-form" }, labeled("Folder path", path), labeled("Name", name), labeled("Mostly", kind), btn),
  err);
}

function devicesSection(urls) {
  return section({ title: "Watch on other devices", desc: "Open this address in a browser on any phone, tablet, laptop or TV connected to the same Wi‑Fi." },
    h("div", { class: "url-list" }, urls.map((u) => h("div", { class: "url-row" },
      h("a", { href: u, target: "_blank", rel: "noopener" }, u),
      h("button", { class: "btn btn-sm", onclick: async () => {
        if (await copyText(u)) toast("Address copied");
        else toast("Couldn't copy. Select the address and copy it yourself.", "error");
      } }, icon("copy"), "Copy")))));
}

// Backup & restore: download all metadata, or bring an export in, with a
// preview (dry run) of what an import would change.
function backupSection() {
  const history = h("input", { type: "checkbox", checked: true });
  const file = h("input", { type: "file", accept: ".json,.gz,application/json,application/gzip" });
  const fileName = h("span", { class: "file-name" }, "No file chosen");
  const mode = h("select", { class: "input", "aria-label": "Import mode" },
    h("option", { value: "merge" }, "Merge: keep my edits on conflicts"),
    h("option", { value: "replace" }, "Replace: the file wins conflicts"));
  const out = h("div", {});
  file.addEventListener("change", () => { fileName.textContent = file.files[0]?.name || "No file chosen"; out.replaceChildren(); });

  const run = async (dryRun, btn) => {
    if (!file.files.length) { toast("Choose a backup file first", "error"); return; }
    btn.classList.add("busy");
    try {
      const res = await fetch(`/api/import?mode=${mode.value}${dryRun ? "&dry_run=1" : ""}`, { method: "POST", body: file.files[0] });
      const rep = await res.json();
      if (!res.ok) throw new Error(rep.error || res.statusText);
      out.replaceChildren(importReport(rep));
      if (!dryRun) toast("Import finished");
    } catch (e) {
      out.replaceChildren(h("p", { class: "warn" }, e.message));
    } finally {
      btn.classList.remove("busy");
    }
  };
  const previewBtn = h("button", { class: "btn", onclick: () => run(true, previewBtn) }, "Preview");
  const importBtn = h("button", { class: "btn btn-primary", onclick: () => run(false, importBtn) }, "Import");

  return section({ title: "Backup & restore", desc: "Your titles, tags, ratings, favorites and watch history in one file. Keep it as a backup, or bring it to another install. Videos aren't included." },
    h("div", { class: "toolbar" },
      h("button", { class: "btn btn-primary", onclick: () => { location.href = `/api/export?history=${history.checked ? 1 : 0}`; } }, icon("download"), "Download backup"),
      h("label", { class: "switch", style: "align-items:center" }, history, h("span", { class: "switch-text" }, h("strong", {}, "Include watch history")))),
    h("div", { class: "subsection" },
      h("h3", {}, "Restore or import"),
      h("p", { class: "hint" }, "Preview shows what would change. Nothing is written until you press Import."),
      h("div", { class: "import-row" },
        h("label", { class: "btn file-pick" }, icon("upload"), "Choose file", file),
        fileName, mode, previewBtn, importBtn),
      out));
}

function importReport(r) {
  const n = (x, word) => plural(x || 0, word);
  const matched = (r.matched.hash || 0) + (r.matched.path || 0) + (r.matched.name || 0);
  const how = [r.matched.hash && `${r.matched.hash} by content`, r.matched.path && `${r.matched.path} by path`,
    r.matched.name && `${r.matched.name} by file name`].filter(Boolean).join(", ");
  const lines = [
    `${n(r.items, "video")} in the file: ${matched} found here${how ? ` (${how})` : ""}` +
      (r.pending ? `, ${r.pending} not here yet (applied automatically when they show up)` : ""),
    `${n(r.fields_updated, "field")} updated` + (r.fields_kept ? `, ${r.fields_kept} of your edits kept` : ""),
    `${n(r.item_tags_added, "tag")} added, ${r.item_tags_removed || 0} removed, ${n(r.tags_created, "new tag name")}`,
    `${n(r.collections_created, "collection")} and ${n(r.profiles_created, "profile")} created, ${n(r.states_updated, "watch state")} merged`,
  ];
  return h("div", { class: "report" },
    h("strong", {}, r.dry_run ? "Preview: nothing has changed yet" : "Import done"),
    h("ul", {}, lines.map((l) => h("li", {}, l))),
    r.warnings.length > 0 && h("ul", { class: "errors" }, r.warnings.map((w) => h("li", {}, w))));
}

let scanTimer;
async function pollScan(box, btn, wasRunning = false) {
  clearTimeout(scanTimer);
  const st = await api("/api/scan").catch(() => null);
  if (!st || !box.isConnected) return;
  btn.disabled = st.running;
  if (wasRunning && !st.running) {
    toast(st.added ? `Scan finished: ${plural(st.added, "new video")}` : "Scan finished");
    route(); // refresh the counts
    return;
  }
  box.replaceChildren(...h("div", {},
    h("div", { class: "subsection" },
      st.running
        ? h("div", {}, h("div", { class: "scan-status" }, `Scanning… ${plural(st.seen, "file")} checked, ${st.added} new`),
          st.current && h("div", { class: "hint", style: "margin-top:4px" }, st.current),
          h("div", { class: "scan-bar" }, h("i", {})))
        : st.finished_at
          ? h("div", { class: "scan-status" }, icon("check"),
            `Last checked ${relTime(Date.parse(st.finished_at))}: ${plural(st.seen, "file")}, ${st.added} new${st.missing ? `, ${st.missing} missing` : ""}.`)
          : h("div", { class: "scan-status" }, "No scan has run yet."),
      st.errors && st.errors.length > 0 && h("details", { class: "more" },
        h("summary", {}, icon("chevronRight"), plural(st.errors.length, "problem")),
        h("ul", { class: "errors" }, st.errors.map((e) => h("li", {}, e)))))).childNodes);
  if (st.running) scanTimer = setTimeout(() => pollScan(box, btn, true), 1000);
}

// ---------- router ----------

let onLeave = null;   // cleanup for the current page
let keyHandler = null; // page keyboard shortcuts
let pageMemo = null;  // extra state to remember for Back (e.g. items loaded)
let routeSeq = 0;
let currentHash = null;
const scrollMemo = new Map();
const visited = [];

function setNav(path) {
  const page = path.startsWith("/library") ? "library" : path.startsWith("/settings") ? "settings" : path === "/" || path === "" ? "home" : "";
  for (const a of document.querySelectorAll("[data-nav]")) {
    if (a.dataset.nav === page) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
}

function errorState(e) {
  return h("div", { class: "empty" }, art("alert"), h("h2", {}, "Something went wrong"), h("p", {}, e.message),
    h("div", { class: "actions" },
      h("button", { class: "btn btn-primary", onclick: () => route() }, icon("refresh"), "Try again"),
      h("a", { class: "btn", href: "#/" }, "Go home")));
}

async function route() {
  if (closeOverlay) closeOverlay();
  if (onLeave) { onLeave(); onLeave = null; }
  if (currentHash) scrollMemo.set(currentHash, { y: window.scrollY, ...(pageMemo ? pageMemo() : {}) });
  keyHandler = null;
  pageMemo = null;
  library = null;

  let hash = location.hash || "#/";
  if (hash.startsWith("#/?")) { // old library links
    history.replaceState(null, "", "#/library" + hash.slice(2));
    hash = location.hash;
  }
  // Going back to the page before: restore where you were on it.
  const back = visited.length > 1 && visited[visited.length - 2] === hash;
  if (back) visited.pop();
  else if (visited[visited.length - 1] !== hash) visited.push(hash);
  if (visited.length > 50) visited.splice(0, visited.length - 50);
  currentHash = hash;
  const memo = back ? scrollMemo.get(hash) : null;

  const [path, qs] = hash.slice(1).split("?");
  const params = new URLSearchParams(qs || "");
  const seq = ++routeSeq;
  const ctx = { stale: () => seq !== routeSeq, restore: memo };
  setNav(path);
  if (!path.startsWith("/library") && document.activeElement !== searchBox) searchBox.value = "";
  document.title = APP_NAME;
  document.body.classList.add("loading");
  if (!app.firstChild) app.replaceChildren(skeletonGrid(8));
  try {
    const m = path.match(/^\/item\/(\d+)/);
    if (m) await renderItem(m[1], params, ctx);
    else if (path.startsWith("/library")) await renderLibrary(params, ctx);
    else if (path.startsWith("/settings")) await renderSettings(ctx);
    else await renderHome(ctx);
  } catch (e) {
    if (!ctx.stale()) app.replaceChildren(errorState(e));
  } finally {
    if (!ctx.stale()) {
      document.body.classList.remove("loading");
      window.scrollTo(0, memo ? memo.y : 0);
    }
  }
}

// Search: typing filters the library, opening it if needed.
const runSearch = debounce(() => {
  const q = searchBox.value.trim();
  if (library) library.setQuery(q);
  else if (q) location.hash = libraryHash({ q });
}, 220);
searchBox.addEventListener("input", runSearch);
searchBox.addEventListener("keydown", (e) => {
  if (e.key === "Enter") {
    e.preventDefault();
    runSearch.cancel();
    const q = searchBox.value.trim();
    if (library) library.setQuery(q);
    else location.hash = libraryHash({ q });
  } else if (e.key === "Escape") {
    searchBox.blur();
  }
});

document.addEventListener("keydown", (e) => {
  if (overlayOpen || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
  const t = e.target;
  if (t.closest && t.closest("input, textarea, select, [contenteditable='true']")) return;
  if (e.key === "/") { e.preventDefault(); searchBox.focus(); searchBox.select(); return; }
  if (e.key === " " && t.closest && t.closest("button, a")) return; // let Space press the button
  if (keyHandler && keyHandler(e)) e.preventDefault();
});

// The full placeholder doesn't fit on phones.
const narrow = matchMedia("(max-width: 520px)");
const setPlaceholder = () => { searchBox.placeholder = narrow.matches ? "Search" : "Search videos, tags, people"; };
narrow.addEventListener("change", setPlaceholder);
setPlaceholder();

window.addEventListener("hashchange", route);
// Closing or reloading the tab: save progress and stop any server-side stream.
window.addEventListener("pagehide", () => { if (onLeave) { onLeave(); onLeave = null; } });
route();
