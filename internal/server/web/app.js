"use strict";

// ---------- helpers ----------

const app = document.getElementById("app");
const searchBox = document.getElementById("search");

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: opts.body ? { "Content-Type": "application/json" } : {},
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
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
    // Skip falsy placeholders from `cond && el` patterns (but keep the text "0").
    if (c == null || c === false || c === 0 || c === "") continue;
    el.append(c instanceof Node ? c : String(c));
  }
  return el;
}

function fmtDuration(ms) {
  if (!ms) return "";
  const s = Math.round(ms / 1000), hh = Math.floor(s / 3600), mm = Math.floor((s % 3600) / 60);
  return hh ? `${hh}h ${mm}m` : `${mm}m`;
}

function fmtBytes(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

const KINDS = ["video", "movie", "episode", "music_video", "concert", "documentary", "lecture", "home_video", "other"];
const kindLabel = (k) => k.replace("_", " ").replace(/^./, (c) => c.toUpperCase());

// ---------- library view ----------

// Filters live in the URL hash so views are bookmarkable and Back works:
// #/?q=ocean&tag=genre:documentary&exclude=tag:4k&sort=added
function readFilters() {
  const p = new URLSearchParams(location.hash.split("?")[1] || "");
  return { q: p.get("q") || "", tag: p.getAll("tag"), exclude: p.getAll("exclude"), sort: p.get("sort") || "title" };
}

function writeFilters(f) {
  const p = new URLSearchParams();
  if (f.q) p.set("q", f.q);
  f.tag.forEach((t) => p.append("tag", t));
  f.exclude.forEach((t) => p.append("exclude", t));
  if (f.sort !== "title") p.set("sort", f.sort);
  const s = p.toString();
  history.replaceState(null, "", "#/" + (s ? "?" + s : ""));
  route();
}

function card(it) {
  const initials = it.title.split(/\s+/).slice(0, 2).map((w) => w[0]).join("").toUpperCase();
  const img = h("img", { src: `/api/items/${it.id}/thumbnail`, loading: "lazy", alt: "" });
  img.onerror = () => img.replaceWith(h("div", { class: "initials" }, initials));
  const meta = [it.year, kindLabel(it.kind), ...it.tags.filter((t) => t.namespace === "genre").map((t) => t.name)]
    .filter(Boolean).join(" · ");
  return h("a", { class: "card", href: `#/item/${it.id}` },
    h("div", { class: "thumb" },
      img,
      it.quality && h("span", { class: "badge" }, it.quality.toUpperCase()),
      it.duration_ms && h("span", { class: "badge dur" }, fmtDuration(it.duration_ms)),
      it.position_ms > 0 && it.duration_ms &&
        h("div", { class: "progress", style: `width:${Math.min(100, (it.position_ms / it.duration_ms) * 100)}%` }),
    ),
    h("div", { class: "title", title: it.title }, it.title),
    h("div", { class: "meta" }, meta || " "),
    it.reason && h("div", { class: "reason", title: it.reason }, it.reason),
  );
}

// shelf is a titled, horizontally scrolling row of cards.
function shelf(title, items) {
  return h("section", { class: "shelf" },
    h("h2", {}, title),
    h("div", { class: "row-scroll" }, items.map(card)));
}

async function renderLibrary() {
  const f = readFilters();
  if (document.activeElement !== searchBox) searchBox.value = f.q;

  const params = new URLSearchParams({ sort: f.sort });
  if (f.q) params.set("q", f.q);
  f.tag.forEach((t) => params.append("tag", t));
  f.exclude.forEach((t) => params.append("exclude", t));
  const filtered = Boolean(f.q || f.tag.length || f.exclude.length);

  const [items, tags, cont, recs, libs] = await Promise.all([
    api("/api/items?" + params),
    api("/api/tags"),
    filtered ? [] : api("/api/items?shelf=continue&limit=12"),
    filtered ? { shelves: [] } : api("/api/recommendations").catch(() => ({ shelves: [] })),
    api("/api/libraries"),
  ]);
  const shelves = recs.shelves;

  if (!libs.length) {
    app.replaceChildren(h("div", { class: "empty" },
      h("p", {}, "No libraries yet."),
      h("a", { href: "#/settings" }, h("button", { class: "primary" }, "Add a video folder"))));
    return;
  }

  // Chip click cycles: neutral -> include -> exclude -> neutral.
  const chip = (t) => {
    const state = f.tag.includes(t.ref) ? "on" : f.exclude.includes(t.ref) ? "off" : "";
    return h("span", {
      class: `chip ${t.namespace} ${state}`,
      title: t.ref,
      onclick: () => {
        const nf = { ...f, tag: f.tag.filter((r) => r !== t.ref), exclude: f.exclude.filter((r) => r !== t.ref) };
        if (state === "") nf.tag.push(t.ref);
        else if (state === "on") nf.exclude.push(t.ref);
        writeFilters(nf);
      },
    }, t.name, h("span", { class: "n" }, t.count));
  };

  const sortSel = h("select", { onchange: (e) => writeFilters({ ...f, sort: e.target.value }) },
    [["title", "Title"], ["added", "Recently added"], ["year", "Year"], ["rating", "Rating"], ["played", "Recently played"]]
      .map(([v, l]) => h("option", { value: v, selected: v === f.sort }, l)));

  // h() wrapper drops the falsy `cond && el` placeholders (replaceChildren would print them).
  app.replaceChildren(...h("div", {},
    h("div", { class: "toolbar" },
      sortSel,
      filtered && h("button", { onclick: () => writeFilters({ q: "", tag: [], exclude: [], sort: f.sort }) }, "Clear filters"),
      h("span", { class: "count" }, `${items.length} item${items.length === 1 ? "" : "s"}`)),
    tags.length && h("div", { class: "chips" }, tags.map(chip),
      h("span", { class: "hint" }, "click: include · again: exclude")),
    cont.length && shelf("Continue watching", cont),
    ...shelves.map((s) => shelf(s.title, s.items)),
    (cont.length || shelves.length) && h("h2", {}, "All"),
    items.length ? h("div", { class: "grid" }, items.map(card))
      : h("div", { class: "empty" }, filtered ? "Nothing matches these filters." : "No videos found yet. Is a scan running?"),
  ).childNodes);
}

let searchTimer;
searchBox.addEventListener("input", () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    const f = location.hash.startsWith("#/item") || location.hash.startsWith("#/settings")
      ? { q: "", tag: [], exclude: [], sort: "title" } : readFilters();
    writeFilters({ ...f, q: searchBox.value.trim() });
  }, 250);
});

// ---------- item view ----------

async function renderItem(id) {
  const it = await api(`/api/items/${id}`);
  document.title = `${it.title} · Itan`;

  const warn = h("div", { class: "warn", hidden: true });
  const player = createPlayer(it, warn);

  // Save the resume point every 10s of playback, and on pause/leave.
  let lastSave = 0;
  const save = () => {
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

  const sub = [it.year, kindLabel(it.kind), fmtDuration(it.duration_ms), it.quality && it.quality.toUpperCase()]
    .filter(Boolean).join(" · ");
  const similar = h("div", {});

  app.replaceChildren(h("div", { class: "item" },
    h("div", {},
      player.el, warn,
      h("div", { class: "title-row" }, h("h1", {}, it.title), favoriteButton(it)),
      h("div", { class: "sub" }, sub),
      it.description && h("p", {}, it.description),
      tagPanel(it),
      similar,
    ),
    h("div", {}, editPanel(it), techPanel(it)),
  ));

  api(`/api/items/${id}/similar`).then((items) => {
    if (items.length) similar.replaceChildren(shelf("More like this", items));
  }).catch(() => {});
}

function favoriteButton(it) {
  const btn = h("button", { class: "fav" });
  const draw = () => {
    btn.textContent = it.favorite ? "♥" : "♡";
    btn.title = it.favorite ? "Remove from favorites" : "Add to favorites (improves recommendations)";
    btn.setAttribute("aria-pressed", String(it.favorite));
  };
  btn.onclick = async () => {
    it.favorite = !it.favorite;
    draw();
    try {
      await api(`/api/items/${it.id}/favorite`, { method: "PUT", body: { favorite: it.favorite } });
    } catch (e) {
      it.favorite = !it.favorite;
      draw();
      alert(e.message);
    }
  };
  draw();
  return btn;
}

// ---------- player ----------

// createPlayer picks how to play an item:
//   direct  - <video src=/stream/id>, native controls, range requests
//   HLS     - the server repackages with ffmpeg: video copied (remux), or
//             re-encoded to H.264 if this browser can't decode it (transcode).
//             Played with hls.js (or natively on Safari/iOS). The stream
//             starts at an offset, so we draw our own seek bar over the whole
//             file and map times ourselves.
// If direct play fails it falls back to HLS; if copying fails, to transcoding.
function createPlayer(it, warn) {
  const video = h("video", { preload: "metadata", playsinline: true });
  const el = h("div", { class: "player" }, video);
  const startAt = it.position_ms > 0 ? it.position_ms / 1000 : 0;
  let mode = it.playback_mode && it.playback_mode !== "direct" ? "hls" : "direct";
  let transcode = !canDecode(it.video_codec);
  let hlsPlayer = null, offset = 0, session = null, controls = null, destroyed = false;

  const showWarn = (msg) => { warn.hidden = false; warn.textContent = msg; };

  // ---- direct ----
  function startDirect() {
    video.controls = true;
    video.src = `/stream/${it.id}`;
    video.addEventListener("loadedmetadata", () => {
      if (startAt && startAt < video.duration - 5) video.currentTime = startAt;
    }, { once: true });
    video.addEventListener("error", () => {
      if (mode !== "direct" || destroyed) return;
      mode = "hls";
      startHLS(video.currentTime || startAt);
    }, { once: true });
  }

  // ---- HLS ----
  const total = () => (it.duration_ms ? it.duration_ms / 1000 : offset + (isFinite(video.duration) ? video.duration : 0));

  async function startHLS(at) {
    video.controls = false;
    video.removeAttribute("src");
    if (!controls) { controls = hlsControls(); el.append(controls.el); }
    session ||= Math.random().toString(36).slice(2) + Date.now().toString(36);
    controls.setBusy(true);
    try {
      const r = await api(`/api/items/${it.id}/hls`, { method: "POST", body: { session, start: at, transcode } });
      if (destroyed) return;
      offset = r.start;
      controls.setEncoder(r.encoder);
      await loadSource(r.url);
      video.play().catch(() => {}); // autoplay may be blocked; the play button still works
    } catch (e) {
      showWarn(`This video couldn't be prepared for streaming: ${e.message}`);
    } finally {
      controls.setBusy(false);
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
    t = Math.max(0, Math.min(t, total() - 1));
    const s = video.seekable;
    const end = s.length ? offset + s.end(s.length - 1) : offset;
    if (t >= offset && t <= end - 1) video.currentTime = t - offset;
    else startHLS(t);
  }

  function position() {
    return mode === "hls" ? offset + (video.currentTime || 0) : video.currentTime || 0;
  }

  function hlsControls() {
    const play = h("button", { class: "pc-btn", title: "Play/pause", onclick: () => (video.paused ? video.play() : video.pause()) }, "▶");
    const time = h("span", { class: "pc-time" }, "0:00");
    const bar = h("input", { type: "range", class: "pc-bar", min: 0, max: 1, step: 1, value: 0 });
    const mute = h("button", { class: "pc-btn", title: "Mute", onclick: () => { video.muted = !video.muted; } }, "🔊");
    const full = h("button", { class: "pc-btn", title: "Fullscreen", onclick: () => {
      if (document.fullscreenElement) document.exitFullscreen();
      else if (el.requestFullscreen) el.requestFullscreen();
      else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen(); // iPhone
    } }, "⛶");
    const badge = h("span", { class: "pc-badge", hidden: true }, "Transcoding");
    let dragging = false;
    const render = () => {
      const tt = total();
      bar.max = Math.max(1, Math.floor(tt));
      if (!dragging) bar.value = Math.floor(position());
      time.textContent = `${clock(dragging ? +bar.value : position())} / ${clock(tt)}`;
    };
    bar.addEventListener("input", () => { dragging = true; render(); });
    bar.addEventListener("change", () => { dragging = false; seek(+bar.value); });
    video.addEventListener("timeupdate", render);
    video.addEventListener("durationchange", render);
    video.addEventListener("play", () => { play.textContent = "❚❚"; });
    video.addEventListener("pause", () => { play.textContent = "▶"; });
    video.addEventListener("volumechange", () => { mute.textContent = video.muted ? "🔇" : "🔊"; });
    video.addEventListener("click", () => (video.paused ? video.play() : video.pause()));
    render();
    return {
      el: h("div", { class: "pc" }, play, time, bar, badge, mute, full),
      setBusy: (b) => el.classList.toggle("busy", b),
      setEncoder: (enc) => {
        badge.hidden = !enc;
        badge.title = enc ? `Converted to H.264 on the server for this browser (${enc})` : "";
      },
    };
  }

  if (mode === "direct") startDirect();
  else startHLS(startAt);

  return {
    el, video, position,
    destroy() {
      destroyed = true;
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

function clock(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hh = Math.floor(sec / 3600), mm = Math.floor((sec % 3600) / 60), ss = sec % 60;
  const p = (n) => String(n).padStart(2, "0");
  return hh ? `${hh}:${p(mm)}:${p(ss)}` : `${mm}:${p(ss)}`;
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

function srcBadge(it, field) {
  const s = it.sources[field];
  if (!s) return null;
  return h("span", { class: `src ${s === "user" ? "user" : ""}`, title: `Value came from: ${s}` }, s === "user" ? "edited" : s);
}

function editPanel(it) {
  const fields = {
    title: h("input", { type: "text", value: it.title }),
    year: h("input", { type: "number", value: it.year ?? "", min: 1870, max: 2100 }),
    kind: h("select", {}, KINDS.map((k) => h("option", { value: k, selected: k === it.kind }, kindLabel(k)))),
    rating: h("input", { type: "number", value: it.rating ?? "", min: 0, max: 10, placeholder: "0–10" }),
    description: h("textarea", { value: it.description ?? "" }),
  };
  const field = (name, label) => h("div", { class: "field" },
    h("label", {}, h("span", {}, label), srcBadge(it, name)), fields[name]);
  const status = h("span", { class: "saved" });

  const save = async () => {
    const body = {};
    const num = (v) => (v === "" ? null : Number(v));
    if (fields.title.value !== it.title) body.title = fields.title.value;
    if (num(fields.year.value) !== it.year) body.year = num(fields.year.value);
    if (fields.kind.value !== it.kind) body.kind = fields.kind.value;
    if (num(fields.rating.value) !== it.rating) body.rating = num(fields.rating.value);
    if (fields.description.value !== (it.description ?? "")) body.description = fields.description.value;
    if (!Object.keys(body).length) { status.textContent = "No changes"; return; }
    try {
      await api(`/api/items/${it.id}`, { method: "PATCH", body });
      route();
    } catch (e) { status.textContent = e.message; }
  };

  return h("div", { class: "panel" },
    h("h3", {}, "Details"),
    field("title", "Title"),
    h("div", { class: "row" }, field("year", "Year"), field("kind", "Kind"), field("rating", "Rating")),
    field("description", "Description"),
    h("div", { class: "actions" }, h("button", { class: "primary", onclick: save }, "Save"), status),
  );
}

function tagPanel(it) {
  const input = h("input", { type: "text", placeholder: "Add tag: nature, genre:Music, mood:chill" });
  const patch = async (body) => {
    try { await api(`/api/items/${it.id}`, { method: "PATCH", body }); route(); }
    catch (e) { alert(e.message); }
  };
  const add = () => {
    const refs = input.value.split(",").map((s) => s.trim()).filter(Boolean);
    if (refs.length) patch({ add_tags: refs });
  };
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") add(); });

  return h("div", { class: "panel" },
    h("h3", {}, "Genres & tags"),
    h("div", { class: "chips" },
      it.tags.length ? it.tags.map((t) => h("span", { class: `chip ${t.namespace}`, title: `${t.ref} (from ${t.source})` },
        h("a", { href: `#/?tag=${encodeURIComponent(t.ref)}`, style: "text-decoration:none" }, t.name),
        h("button", { class: "x", title: "Remove", onclick: () => patch({ remove_tags: [t.ref] }) }, "×")))
        : h("span", { class: "hint" }, "No tags yet")),
    h("div", { class: "tag-add" }, input, h("button", { onclick: add }, "Add")),
  );
}

function techPanel(it) {
  const audio = it.streams.filter((s) => s.type === "audio")
    .map((s) => [s.codec, s.channels && `${s.channels}ch`, s.language].filter(Boolean).join(" ")).join(", ");
  const subs = it.streams.filter((s) => s.type === "subtitle").map((s) => s.language || s.codec).join(", ");
  const rows = [
    ["Quality", it.quality && it.quality.toUpperCase() + (it.hdr ? ` ${it.hdr.toUpperCase()}` : "")],
    ["Resolution", it.width && `${it.width}×${it.height}`],
    ["Video", it.video_codec],
    ["Audio", audio],
    ["Subtitles", subs],
    ["Container", it.container],
    ["Bitrate", it.bitrate && `${(it.bitrate / 1e6).toFixed(1)} Mb/s`],
    ["Playback", it.playback_mode],
    ["Size", fmtBytes(it.size_bytes)],
    ["Hash", it.content_hash],
    ["Probe error", it.probe_error],
    ...it.files.map((f) => ["File", `${f.library}: ${f.path}${f.missing ? " (missing)" : ""}`]),
  ].filter(([, v]) => v);
  return h("div", { class: "panel" },
    h("h3", {}, "Technical"),
    h("dl", { class: "tech" }, rows.flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)])),
  );
}

// ---------- settings view ----------

async function renderSettings() {
  document.title = "Libraries · Itan";
  const libs = await api("/api/libraries");
  const name = h("input", { type: "text", placeholder: "Documentaries" });
  const path = h("input", { type: "text", placeholder: "/Users/me/Videos/Docs" });
  const kind = h("select", {}, KINDS.map((k) => h("option", { value: k }, kindLabel(k))));
  const msg = h("div", { class: "status" });
  const status = h("div", { class: "status" });

  const add = async () => {
    try {
      await api("/api/libraries", { method: "POST", body: { name: name.value, path: path.value, default_kind: kind.value } });
      route();
    } catch (e) { msg.textContent = e.message; }
  };
  const remove = async (lib) => {
    if (!confirm(`Remove library "${lib.name}"? Files are not touched, and your edits come back if you add it again.`)) return;
    await api(`/api/libraries/${lib.id}`, { method: "DELETE" });
    route();
  };
  const scanBtn = h("button", { class: "primary", onclick: async () => {
    try { await api("/api/scan", { method: "POST" }); } catch (e) { /* already running */ }
    pollScan(status, scanBtn);
  } }, "Scan now");

  const sidecarToggle = (lib) => {
    const note = h("span", { class: "status" });
    const box = h("input", { type: "checkbox", checked: lib.write_sidecars, onchange: async () => {
      note.textContent = "";
      try { await api(`/api/libraries/${lib.id}`, { method: "PATCH", body: { write_sidecars: box.checked } }); }
      catch (e) { note.textContent = e.message; }
    } });
    return h("label", { class: "check", title: "Saves your edits in a small file next to each edited video, so they travel with the files" },
      box, " Save edits as .meta.json next to videos ", note);
  };

  app.replaceChildren(
    h("div", { class: "panel" },
      h("h3", {}, "Libraries"),
      libs.length ? libs.map((l) => h("div", { class: "lib" },
        h("div", { class: "info" },
          h("div", {}, h("strong", {}, l.name), ` · ${l.items} item${l.items === 1 ? "" : "s"} · ${kindLabel(l.default_kind)}`),
          h("div", { class: "path" }, l.path,
            l.last_scan_at ? ` · scanned ${new Date(l.last_scan_at * 1000).toLocaleString()}` : " · not scanned yet"),
          sidecarToggle(l)),
        h("button", { class: "danger", onclick: () => remove(l) }, "Remove")))
        : h("p", { class: "status" }, "No libraries yet. Add the folder where your videos live."),
    ),
    backupPanel(),
    h("div", { class: "panel" },
      h("h3", {}, "Add a folder"),
      h("div", { class: "form-grid" },
        h("div", { class: "field" }, h("label", {}, "Name"), name),
        h("div", { class: "field" }, h("label", {}, "Folder path on the server"), path),
        h("div", { class: "field" }, h("label", {}, "Default kind"), kind),
        h("div", { class: "field" }, h("button", { class: "primary", onclick: add }, "Add"))),
      msg,
      h("p", { class: "status" }, "Subfolder names become tags: Docs/Nature/x.mkv is tagged Docs and Nature."),
    ),
    h("div", { class: "panel" }, h("h3", {}, "Scanning"), h("div", { class: "actions" }, scanBtn), status),
  );
  pollScan(status, scanBtn);
}

// Backup & restore: download all metadata, or bring an export in, with a
// preview (dry run) of what an import would change.
function backupPanel() {
  const history = h("input", { type: "checkbox", checked: true });
  const file = h("input", { type: "file", accept: ".json,.gz,application/json,application/gzip" });
  const mode = h("select", {},
    h("option", { value: "merge" }, "Merge: keep my edits when they conflict"),
    h("option", { value: "replace" }, "Replace: the file wins every conflict"));
  const out = h("div", { class: "report" });

  const run = async (dryRun) => {
    if (!file.files.length) { out.replaceChildren(h("p", { class: "status" }, "Choose an export file first.")); return; }
    out.replaceChildren(h("p", { class: "status" }, dryRun ? "Checking…" : "Importing…"));
    try {
      const res = await fetch(`/api/import?mode=${mode.value}${dryRun ? "&dry_run=1" : ""}`, { method: "POST", body: file.files[0] });
      const rep = await res.json();
      if (!res.ok) throw new Error(rep.error || res.statusText);
      out.replaceChildren(importReport(rep));
    } catch (e) {
      out.replaceChildren(h("p", { class: "warn" }, e.message));
    }
  };

  return h("div", { class: "panel" },
    h("h3", {}, "Backup & restore"),
    h("p", { class: "status" },
      "Your titles, tags, ratings, collections and watch history, in one file you can keep or move to another install. Videos aren't included."),
    h("div", { class: "actions" },
      h("button", { class: "primary", onclick: () => { location.href = `/api/export?history=${history.checked ? 1 : 0}`; } }, "Download export"),
      h("label", { class: "check" }, history, " Include watch history")),
    h("div", { class: "import-row" },
      file, mode,
      h("button", { onclick: () => run(true) }, "Preview"),
      h("button", { class: "primary", onclick: () => run(false) }, "Import")),
    out,
  );
}

function importReport(r) {
  const n = (x, word) => `${x} ${word}${x === 1 ? "" : "s"}`;
  const matched = (r.matched.hash || 0) + (r.matched.path || 0) + (r.matched.name || 0);
  const how = [r.matched.hash && `${r.matched.hash} by content`, r.matched.path && `${r.matched.path} by path`,
    r.matched.name && `${r.matched.name} by file name`].filter(Boolean).join(", ");
  const lines = [
    `${n(r.items, "video")} in the file: ${matched} found here${how ? ` (${how})` : ""}` +
      (r.pending ? `, ${r.pending} not found yet (applied automatically when they show up in a scan)` : ""),
    `${n(r.fields_updated, "field")} updated` + (r.fields_kept ? `, ${r.fields_kept} of your edits kept over the file's values` : ""),
    `${n(r.item_tags_added, "tag")} added, ${r.item_tags_removed} removed, ${n(r.tags_created, "new tag name")}`,
    `${n(r.collections_created, "collection")} and ${n(r.profiles_created, "profile")} created, ${n(r.states_updated, "watch state")} merged`,
  ];
  return h("div", {},
    h("p", {}, h("strong", {}, r.dry_run ? "Preview, nothing changed yet:" : "Import done:")),
    h("ul", {}, lines.map((l) => h("li", {}, l))),
    r.warnings.length ? h("ul", { class: "errors" }, r.warnings.map((w) => h("li", {}, w))) : "",
  );
}

let scanTimer;
async function pollScan(el, btn) {
  clearTimeout(scanTimer);
  const st = await api("/api/scan").catch(() => null);
  if (!st || !el.isConnected) return;
  btn.disabled = st.running;
  el.replaceChildren(
    st.running
      ? `Scanning… ${st.seen} files seen, ${st.added} new${st.current ? ` — ${st.current}` : ""}`
      : st.finished_at
        ? `Last scan finished ${new Date(st.finished_at).toLocaleTimeString()}: ${st.seen} files, ${st.added} new, ${st.missing} missing.`
        : "No scan has run yet.",
    st.errors.length ? h("ul", { class: "errors" }, st.errors.map((e) => h("li", {}, e))) : "",
  );
  if (st.running) scanTimer = setTimeout(() => pollScan(el, btn), 1000);
}

// ---------- router ----------

let onLeave = null;

async function route() {
  if (onLeave) { onLeave(); onLeave = null; }
  document.title = "Itan";
  const hash = location.hash || "#/";
  try {
    const m = hash.match(/^#\/item\/(\d+)/);
    if (m) await renderItem(m[1]);
    else if (hash.startsWith("#/settings")) await renderSettings();
    else await renderLibrary();
  } catch (e) {
    app.replaceChildren(h("div", { class: "empty" }, `Something went wrong: ${e.message}`));
  }
}

window.addEventListener("hashchange", route);
// Closing or reloading the tab: save progress and stop any server-side stream.
window.addEventListener("pagehide", () => { if (onLeave) { onLeave(); onLeave = null; } });
route();
