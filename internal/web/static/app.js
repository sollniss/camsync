// The organize front end. State lives here: the library as the server
// sent it, the current selection and the accepted suggestions. Every
// render function reads that state and rewrites its part of the page.

const state = {
  lib: null,
  selection: null, // {type: "event" | "folder", index}
  accepted: new Set(),
  onlyRelated: false,
  view: null, // timeline [t0, t1] in ms
};

const $ = (sel) => document.querySelector(sel);
const el = (tag, attrs = {}, ...children) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};

const previewURL = (photo, size) => `api/preview?path=${encodeURIComponent(photo.path)}&size=${size}`;
const baseName = (path) => path.split("/").pop();

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "X-Camsync": "1", "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  return res.json();
}

// ---------------------------------------------------------------- loading

async function load(params) {
  const q = params ? "?" + params.toString() : "";
  setLibrary(await api("api/library" + q));
}

function setLibrary(lib) {
  state.lib = lib;
  // Suggestions may have been renumbered; keep only ids that still exist.
  const ids = new Set(lib.suggestions.map((s) => s.id));
  state.accepted = new Set([...state.accepted].filter((id) => ids.has(id)));
  if (state.selection) {
    const list = state.selection.type === "event" ? lib.events : lib.folders;
    if (state.selection.index >= list.length) state.selection = null;
  }
  renderAll();
}

function renderAll() {
  renderHeader();
  renderFolders();
  renderTimeline();
  renderMap();
  renderDetail();
  renderSuggestions();
  renderNotes();
  renderApplyBar();
}

// ----------------------------------------------------------------- header

function renderHeader() {
  const lib = state.lib;
  $("#root").textContent = lib.root;
  const undated = lib.photos.filter((p) => !p.time).length;
  const home = lib.home
    ? `home ${lib.home.lat.toFixed(4)}, ${lib.home.lon.toFixed(4)}${lib.home.inferred ? " (inferred)" : ""}`
    : "home unknown (no positions)";
  $("#stats").textContent = `${lib.photos.length} photos · ${lib.events.length} events · ${lib.folders.length} folders · ${home}` +
    (undated ? ` · ${undated} undated` : "") + (lib.ignored ? ` · ${lib.ignored} other files` : "");
  const f = $("#options");
  const o = lib.options;
  f.gap.value = o.gap;
  f.jump.value = o.jump;
  f.homeRadius.value = o.homeRadius;
  f.minTrip.value = o.minTrip;
  f.minTripPhotos.value = o.minTripPhotos;
  f.layout.value = o.layout;
  f.home.value = o.home || "";
}

$("#options").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const params = new URLSearchParams();
  for (const name of ["gap", "jump", "homeRadius", "minTrip", "minTripPhotos", "layout"]) {
    params.set(name, f[name].value.trim());
  }
  params.set("home", f.home.value.trim());
  try {
    await load(params);
  } catch (e) {
    alert(e.message);
  }
});

$("#rescan").addEventListener("click", async () => {
  const btn = $("#rescan");
  btn.disabled = true;
  btn.textContent = "Scanning…";
  try {
    setLibrary(await api("api/rescan", { method: "POST" }));
  } catch (e) {
    alert(e.message);
  } finally {
    btn.disabled = false;
    btn.textContent = "Rescan";
  }
});

// ---------------------------------------------------------------- folders

function renderFolders() {
  const lib = state.lib;
  $("#folder-count").textContent = lib.folders.length;
  const ul = $("#folders");
  ul.replaceChildren(
    ...lib.folders.map((f, i) =>
      el("li", {
        class: isSelected("folder", i) ? "selected" : "",
        title: `${f.rel}\n${f.note}`,
        onclick: () => select("folder", i),
      },
        el("span", { class: "badge " + f.kind, text: f.kind }),
        el("span", { class: "name", text: f.rel === "." ? "(root)" : f.rel }),
        el("span", { class: "n", text: f.photos.length }),
      ),
    ),
  );
}

// --------------------------------------------------------------- selection

function isSelected(type, index) {
  return state.selection && state.selection.type === type && state.selection.index === index;
}

function select(type, index) {
  state.selection = isSelected(type, index) ? null : { type, index };
  renderFolders();
  renderTimeline();
  renderMap();
  renderDetail();
  renderSuggestions();
}

// Photos related to the selection, as a set of indexes, or null.
function selectedPhotos() {
  const sel = state.selection;
  if (!sel) return null;
  const list = sel.type === "event" ? state.lib.events : state.lib.folders;
  return new Set(list[sel.index].photos);
}

function suggestionRelated(s) {
  const sel = state.selection;
  if (!sel) return true;
  if (sel.type === "event") return s.event === sel.index;
  const folder = state.lib.folders[sel.index];
  return s.from === sel.index || s.to === folder.path;
}

// --------------------------------------------------------------- timeline

const parseTime = (s) => new Date(s.replace(" ", "T") + "Z").getTime();
const DAY = 86400000;

function fullRange() {
  const evs = state.lib.events;
  if (evs.length === 0) return [Date.now() - DAY, Date.now()];
  let t0 = Infinity, t1 = -Infinity;
  for (const e of evs) {
    t0 = Math.min(t0, parseTime(e.first));
    t1 = Math.max(t1, parseTime(e.last));
  }
  const pad = Math.max((t1 - t0) * 0.03, DAY);
  return [t0 - pad, t1 + pad];
}

// The initial view is the shortest span that holds 90 % of the photos, so
// that a camera dated 2010 by a reset clock does not squeeze the real years
// into a corner. Double click shows everything.
function focusedRange() {
  const times = state.lib.photos.filter((p) => p.time).map((p) => parseTime(p.time)).sort((a, b) => a - b);
  if (times.length < 20) return fullRange();
  const k = Math.ceil(times.length * 0.9);
  let best = 0;
  for (let i = 1; i + k <= times.length; i++) {
    if (times[i + k - 1] - times[i] < times[best + k - 1] - times[best]) best = i;
  }
  const t0 = times[best], t1 = times[best + k - 1];
  const pad = Math.max((t1 - t0) * 0.03, DAY);
  return [t0 - pad, t1 + pad];
}

function renderTimeline() {
  const svg = $("#timeline");
  const lib = state.lib;
  const W = svg.clientWidth || 800;
  const H = svg.clientHeight || 150;
  const left = 44, right = 8, top = 22, bottom = 18;
  if (!state.view) state.view = focusedRange();
  const [t0, t1] = state.view;
  const x = (t) => left + ((t - t0) / (t1 - t0)) * (W - left - right);
  const rows = { trip: 0, outing: 1, home: 2 };
  const rowH = (H - top - bottom) / 3;
  const ns = "http://www.w3.org/2000/svg";
  const make = (tag, attrs) => {
    const n = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    return n;
  };
  svg.replaceChildren();
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);

  // Axis with ticks chosen for the visible span.
  const axis = make("g", { class: "tl-axis" });
  const span = t1 - t0;
  const step = span > 4 * 365 * DAY ? "year" : span > 120 * DAY ? "month" : span > 8 * DAY ? "day" : "hour";
  let tick = new Date(t0);
  tick.setUTCMinutes(0, 0, 0);
  if (step !== "hour") tick.setUTCHours(0);
  if (step === "month" || step === "year") tick.setUTCDate(1);
  if (step === "year") tick.setUTCMonth(0);
  const fmt = { year: (d) => d.getUTCFullYear(), month: (d) => d.toISOString().slice(0, 7), day: (d) => d.toISOString().slice(5, 10), hour: (d) => d.toISOString().slice(11, 16) };
  let guard = 0;
  let lastLabelX = -Infinity;
  while (tick.getTime() <= t1 && guard++ < 400) {
    const px = x(tick.getTime());
    if (px >= left) {
      axis.append(make("line", { x1: px, x2: px, y1: top - 4, y2: H - bottom }));
      if (px - lastLabelX > 40) {
        const t = make("text", { x: px + 2, y: H - 4 });
        t.textContent = fmt[step](tick);
        axis.append(t);
        lastLabelX = px;
      }
    }
    if (step === "year") tick.setUTCFullYear(tick.getUTCFullYear() + 1);
    else if (step === "month") tick.setUTCMonth(tick.getUTCMonth() + 1);
    else if (step === "day") tick.setUTCDate(tick.getUTCDate() + 1);
    else tick.setUTCHours(tick.getUTCHours() + 1);
  }
  svg.append(axis);
  for (const [kind, r] of Object.entries(rows)) {
    const t = make("text", { x: 4, y: top + r * rowH + rowH / 2 + 4, class: "tl-row" });
    t.textContent = kind;
    svg.append(t);
  }

  const selectedEvent = state.selection && state.selection.type === "event" ? state.selection.index : null;
  const selectedFolder = state.selection && state.selection.type === "folder" ? state.selection.index : null;
  lib.events.forEach((e, i) => {
    const a = parseTime(e.first), b = parseTime(e.last);
    if (b < t0 || a > t1) return;
    const x0 = Math.max(left, x(a));
    const x1 = Math.min(W - right, x(b));
    const w = Math.max(4, x1 - x0);
    const r = rows[e.kind] ?? 1;
    let cls = "tl-event " + e.kind;
    if (e.located === 0) cls += " unplaced";
    if (selectedEvent === i) cls += " selected";
    if (selectedFolder !== null && !e.folders.some((f) => f.folder === selectedFolder)) cls += " dim";
    const rect = make("rect", { x: x0, y: top + r * rowH + 3, width: w, height: rowH - 6, rx: 2, class: cls });
    const title = make("title", {});
    title.textContent = `${e.name} · ${e.kind} · ${e.photos.length} photos · ${Object.keys(e.cameras).join(", ")}`;
    rect.append(title);
    rect.addEventListener("click", (ev) => { ev.stopPropagation(); select("event", i); });
    svg.append(rect);
  });
}

// Wheel zooms around the cursor, dragging pans, double click resets.
(() => {
  const svg = $("#timeline");
  let drag = null;
  svg.addEventListener("wheel", (ev) => {
    if (!state.view) return;
    ev.preventDefault();
    const [t0, t1] = state.view;
    const rect = svg.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (ev.clientX - rect.left - 44) / (rect.width - 52)));
    const pivot = t0 + frac * (t1 - t0);
    const factor = ev.deltaY > 0 ? 1.25 : 0.8;
    let span = (t1 - t0) * factor;
    span = Math.max(3600000, Math.min(span, fullRange()[1] - fullRange()[0]));
    state.view = [pivot - frac * span, pivot + (1 - frac) * span];
    renderTimeline();
  }, { passive: false });
  svg.addEventListener("mousedown", (ev) => {
    drag = { x: ev.clientX, view: state.view.slice(), moved: false };
    svg.classList.add("dragging");
  });
  window.addEventListener("mousemove", (ev) => {
    if (!drag) return;
    const rect = svg.getBoundingClientRect();
    const dt = ((ev.clientX - drag.x) / (rect.width - 52)) * (drag.view[1] - drag.view[0]);
    if (Math.abs(ev.clientX - drag.x) > 2) drag.moved = true;
    state.view = [drag.view[0] - dt, drag.view[1] - dt];
    renderTimeline();
  });
  window.addEventListener("mouseup", () => { drag = null; svg.classList.remove("dragging"); });
  svg.addEventListener("dblclick", () => { state.view = fullRange(); renderTimeline(); });
  window.addEventListener("resize", () => state.lib && renderTimeline());
})();

// -------------------------------------------------------------------- map

let map = null;
let mapLayers = { events: null, photos: null, home: null };
const kindColor = { trip: "#d9480f", outing: "#e8a33d", home: "#4c8fd6" };

function ensureMap() {
  if (map) return map;
  map = L.map("map", { worldCopyJump: true }).setView([30, 10], 2);
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
  }).addTo(map);
  mapLayers.events = L.layerGroup().addTo(map);
  mapLayers.photos = L.layerGroup().addTo(map);
  mapLayers.home = L.layerGroup().addTo(map);
  return map;
}

function renderMap() {
  const lib = state.lib;
  const m = ensureMap();
  mapLayers.events.clearLayers();
  mapLayers.photos.clearLayers();
  mapLayers.home.clearLayers();
  const bounds = [];
  if (lib.home) {
    L.circleMarker([lib.home.lat, lib.home.lon], { radius: 7, color: "#111", fillColor: "#fff", fillOpacity: 1, weight: 2 })
      .bindTooltip("home" + (lib.home.inferred ? " (inferred)" : ""))
      .addTo(mapLayers.home);
    L.circle([lib.home.lat, lib.home.lon], { radius: distanceMetres(lib.options.homeRadius), color: "#111", weight: 1, fill: false, dashArray: "4 4" }).addTo(mapLayers.home);
  }
  const selectedEvent = state.selection && state.selection.type === "event" ? state.selection.index : null;
  const selectedFolder = state.selection && state.selection.type === "folder" ? state.selection.index : null;
  lib.events.forEach((e, i) => {
    if (!e.center) return;
    const dim = (selectedEvent !== null && selectedEvent !== i) || (selectedFolder !== null && !e.folders.some((f) => f.folder === selectedFolder));
    const marker = L.circleMarker(e.center, {
      radius: 5 + Math.min(14, Math.sqrt(e.photos.length)),
      color: selectedEvent === i ? "#000" : "#fff",
      weight: selectedEvent === i ? 2 : 1,
      fillColor: kindColor[e.kind] || "#999",
      fillOpacity: dim ? 0.25 : 0.8,
    }).bindTooltip(`${e.name} · ${e.kind} · ${e.photos.length} photos`);
    marker.on("click", () => select("event", i));
    marker.addTo(mapLayers.events);
    if (!dim) bounds.push(e.center);
  });
  const sel = selectedPhotos();
  if (sel) {
    for (const idx of sel) {
      const p = lib.photos[idx];
      if (p.lat === undefined) continue;
      L.circleMarker([p.lat, p.lon], { radius: 3, color: "#111", weight: 1, fillColor: "#fff", fillOpacity: 1 })
        .bindTooltip(`${p.rel}\n${p.time || ""}`)
        .on("click", () => openPhoto(idx))
        .addTo(mapLayers.photos);
      bounds.push([p.lat, p.lon]);
    }
  }
  if (bounds.length > 0) m.fitBounds(bounds, { padding: [20, 20], maxZoom: 12 });
}

function distanceMetres(s) {
  const m = /^([\d.]+)\s*(km|m)?$/.exec(s || "");
  if (!m) return 0;
  return parseFloat(m[1]) * (m[2] === "km" ? 1000 : 1);
}

// ----------------------------------------------------------------- detail

function renderDetail() {
  const box = $("#detail");
  const sel = state.selection;
  const lib = state.lib;
  if (!sel) {
    box.replaceChildren(el("p", { class: "muted", text: "Select an event on the timeline or map, or a folder on the left." }));
    return;
  }
  const dl = el("dl");
  const row = (k, v) => dl.append(el("dt", { text: k }), el("dd", {}, v));
  let photos;
  if (sel.type === "event") {
    const e = lib.events[sel.index];
    box.replaceChildren(el("h3", {}, el("span", { class: "badge " + e.kind, text: e.kind }), " ", e.name));
    row("photos", `${e.photos.length}, ${e.located} located` + (e.center ? ` within ${fmtDistance(e.radius)}` : ""));
    row("cameras", Object.entries(e.cameras).map(([c, n]) => `${c} (${n})`).join(", "));
    row("folders", el("span", { class: "chips" }, e.folders.map((fc) => {
      const f = lib.folders[fc.folder];
      return el("span", { class: "chip", onclick: () => select("folder", fc.folder) }, el("span", { class: "badge " + f.kind, text: f.kind }), ` ${f.rel === "." ? "(root)" : f.rel} (${fc.count})`);
    })));
    photos = e.photos;
  } else {
    const f = lib.folders[sel.index];
    box.replaceChildren(el("h3", {}, el("span", { class: "badge " + f.kind, text: f.kind }), " ", f.rel === "." ? "(root)" : f.rel));
    row("note", f.note);
    row("photos", `${f.photos.length}` + (f.undated ? `, ${f.undated} undated` : ""));
    row("events", el("span", { class: "chips" }, f.events.map((ec) => {
      const e = lib.events[ec.event];
      return el("span", { class: "chip", onclick: () => select("event", ec.event) }, el("span", { class: "badge " + e.kind, text: e.kind }), ` ${e.name} (${ec.count}/${e.photos.length})`);
    })));
    photos = f.photos;
  }
  box.append(dl);
  box.append(photoGrid(photos, 120));
}

function fmtDistance(m) {
  return m < 1000 ? `${Math.round(m)} m` : `${(m / 1000).toFixed(1)} km`;
}

function thumb(idx, size) {
  const p = state.lib.photos[idx];
  const img = el("img", { src: previewURL(p, size), loading: "lazy", alt: p.rel, title: `${p.rel}\n${p.camera}\n${p.time || "no date"}`, onclick: () => openPhoto(idx) });
  img.addEventListener("error", () => img.replaceWith(el("div", { class: "noprev", title: img.title, onclick: () => openPhoto(idx) })));
  return img;
}

function photoGrid(indexes, limit) {
  const grid = el("div", { class: "grid" });
  const render = (from, to) => {
    for (const idx of indexes.slice(from, to)) {
      const p = state.lib.photos[idx];
      grid.append(el("figure", {}, thumb(idx, 160), el("figcaption", { text: baseName(p.path) })));
    }
  };
  render(0, limit);
  if (indexes.length > limit) {
    const more = el("button", { type: "button", text: `show ${indexes.length - limit} more` });
    more.addEventListener("click", () => { more.remove(); render(limit, indexes.length); });
    grid.append(more);
  }
  return grid;
}

// ------------------------------------------------------------ suggestions

function renderSuggestions() {
  const lib = state.lib;
  $("#suggestion-count").textContent = lib.suggestions.length;
  const box = $("#suggestions");
  if (lib.suggestions.length === 0) {
    box.replaceChildren(el("p", { class: "muted", text: "Nothing to tidy with these settings." }));
    return;
  }
  box.replaceChildren(...lib.suggestions.map((s) => {
    const related = suggestionRelated(s);
    const accepted = state.accepted.has(s.id);
    const card = el("div", { class: "card" + (accepted ? " accepted" : "") + (related ? "" : state.onlyRelated ? " hidden" : " unrelated"), "data-id": s.id });
    const from = s.from !== null ? lib.folders[s.from].rel : "several folders";
    const check = el("input", { type: "checkbox" });
    check.checked = accepted;
    if (!s.to) check.disabled = true;
    check.addEventListener("change", () => {
      if (check.checked) state.accepted.add(s.id); else state.accepted.delete(s.id);
      card.classList.toggle("accepted", check.checked);
      renderApplyBar();
    });
    card.append(el("div", { class: "head" },
      el("label", {}, check, el("span", { class: "badge " + s.kind, text: s.kind }), el("span", { class: "muted", text: `${s.photos.length} photos` })),
      el("span", { class: "conf " + s.confidence, text: s.confidence, title: "How sure the event membership is: high with positions on both sides, low by time alone" }),
    ));
    const route = el("div", { class: "route" });
    route.append(s.from !== null ? el("span", { class: "link", text: from === "." ? "(root)" : from, onclick: () => select("folder", s.from) }) : from);
    if (s.to) {
      route.append(el("span", { class: "arrow", text: "→" }));
      const toFolder = lib.folders.findIndex((f) => f.path === s.to);
      route.append(toFolder >= 0
        ? el("span", { class: "link", text: s.toRel, onclick: () => select("folder", toFolder) })
        : el("span", {}, s.toRel, el("span", { class: "new", text: "new" })));
    }
    card.append(route);
    const reason = el("div", { class: "reason" }, s.reason);
    if (s.event !== null) {
      reason.append(" ", el("span", { class: "link", text: "show event", onclick: () => select("event", s.event) }));
    }
    card.append(reason);
    const thumbs = el("div", { class: "thumbs" });
    const shown = s.photos.slice(0, 6);
    for (const idx of shown) thumbs.append(thumb(idx, 128));
    if (s.photos.length > shown.length) {
      thumbs.append(el("div", { class: "more", text: `+${s.photos.length - shown.length}`, title: "show all", onclick: () => {
        thumbs.replaceChildren(...s.photos.map((idx) => thumb(idx, 128)));
      } }));
    }
    card.append(thumbs);
    return card;
  }));
}

$("#only-related").addEventListener("change", (ev) => { state.onlyRelated = ev.target.checked; renderSuggestions(); });
$("#accept-high").addEventListener("click", () => acceptWhere((s) => s.confidence === "high"));
$("#accept-all").addEventListener("click", () => acceptWhere(() => true));
$("#accept-none").addEventListener("click", () => { state.accepted.clear(); renderSuggestions(); renderApplyBar(); });

function acceptWhere(pred) {
  for (const s of state.lib.suggestions) {
    if (s.to && pred(s) && (!state.onlyRelated || suggestionRelated(s))) state.accepted.add(s.id);
  }
  renderSuggestions();
  renderApplyBar();
}

function acceptedMoves() {
  const moves = [];
  for (const s of state.lib.suggestions) {
    if (!state.accepted.has(s.id) || !s.to) continue;
    for (const idx of s.photos) moves.push({ path: state.lib.photos[idx].path, to: s.to });
  }
  return moves;
}

function renderApplyBar() {
  const n = acceptedMoves().length;
  const btn = $("#apply");
  btn.disabled = n === 0;
  btn.textContent = `Apply ${n} move${n === 1 ? "" : "s"}`;
  $("#download-plan").disabled = n === 0;
}

// ------------------------------------------------------------------ notes

function renderNotes() {
  const ul = $("#notes");
  const notes = state.lib.notes;
  if (notes.length === 0) {
    ul.replaceChildren(el("li", { class: "undated muted", text: "Nothing to report." }));
    return;
  }
  ul.replaceChildren(...notes.map((n) => {
    const li = el("li", { class: n.kind }, n.text);
    if (n.folder !== null) li.append(" ", el("span", { class: "link", text: "show folder", onclick: () => select("folder", n.folder) }));
    if (n.photos.length > 0) {
      const strip = el("div", { class: "thumbs" });
      li.append(" ", el("span", { class: "link", text: `${n.photos.length} photos`, onclick: () => {
        strip.replaceChildren(...n.photos.slice(0, 40).map((idx) => thumb(idx, 128)));
      } }), strip);
    }
    return li;
  }));
}

// ------------------------------------------------------------------ apply

$("#download-plan").addEventListener("click", () => {
  const plan = { root: state.lib.root, moves: acceptedMoves().map((m) => {
    const p = state.lib.photos.find((x) => x.path === m.path);
    return { path: m.path, sidecar: p && p.sidecar ? sidecarPath(p) : undefined, to: m.to };
  }) };
  const blob = new Blob([JSON.stringify(plan, null, 2)], { type: "application/json" });
  const a = el("a", { href: URL.createObjectURL(blob), download: "camsync-plan.json" });
  document.body.append(a);
  a.click();
  a.remove();
});

// The server fills sidecars in from its scan; the downloaded plan only
// marks that one exists, and organize --apply resolves it again.
function sidecarPath() { return undefined; }

$("#apply").addEventListener("click", () => {
  const moves = acceptedMoves();
  const dests = new Set(moves.map((m) => m.to));
  const newDirs = [...dests].filter((d) => !state.lib.folders.some((f) => f.path === d));
  $("#apply-title").textContent = `Move ${moves.length} photo${moves.length === 1 ? "" : "s"}?`;
  $("#apply-body").replaceChildren(
    el("p", {}, `${moves.length} photos go to ${dests.size} folder${dests.size === 1 ? "" : "s"}` + (newDirs.length ? `, ${newDirs.length} of them new` : "") + ". Sidecars move with their photos. Nothing is overwritten."),
    el("div", { class: "warning" }, "Catalog applications such as Lightroom or digiKam lose track of moved files. Immich rescans external libraries on its own."),
  );
  $("#apply-confirm").hidden = false;
  $("#apply-cancel").textContent = "Cancel";
  const dialog = $("#apply-dialog");
  dialog.returnValue = "";
  dialog.showModal();
});

$("#apply-dialog").addEventListener("close", async () => {
  const dialog = $("#apply-dialog");
  if (dialog.returnValue !== "confirm") return;
  const moves = acceptedMoves();
  $("#apply").disabled = true;
  $("#apply").textContent = "Moving…";
  let res;
  try {
    res = await api("api/apply", { method: "POST", body: JSON.stringify({ moves }) });
  } catch (e) {
    alert(e.message);
    renderApplyBar();
    return;
  }
  const rows = res.results.filter((r) => r.error).map((r) => el("tr", {}, el("td", { class: "mono", text: r.path }), el("td", { class: "err", text: r.error })));
  $("#apply-title").textContent = "Done";
  $("#apply-body").replaceChildren(
    el("p", {}, `${res.moved} moved, ${res.failed} failed` + (res.created.length ? `, ${res.created.length} folders created` : "") + "."),
    rows.length ? el("table", {}, ...rows) : null,
  );
  $("#apply-confirm").hidden = true;
  $("#apply-cancel").textContent = "Close";
  dialog.returnValue = "";
  dialog.showModal();
  state.accepted.clear();
  if (res.library) setLibrary(res.library); else renderApplyBar();
});

// ------------------------------------------------------------------ photo

function openPhoto(idx) {
  const p = state.lib.photos[idx];
  const img = $("#photo-big");
  img.src = previewURL(p, 1200);
  img.alt = p.rel;
  const meta = $("#photo-meta");
  meta.replaceChildren();
  const row = (k, v) => meta.append(el("dt", { text: k }), el("dd", {}, v));
  row("file", p.rel);
  row("camera", p.camera);
  row("format", p.format);
  row("taken", p.time || "no date");
  row("position", p.lat !== undefined ? `${p.lat.toFixed(5)}, ${p.lon.toFixed(5)}` : "none");
  row("folder", el("span", { class: "link", text: state.lib.folders[p.folder].rel, onclick: () => { $("#photo-dialog").close(); select("folder", p.folder); } }));
  if (p.event !== null) {
    const e = state.lib.events[p.event];
    row("event", el("span", { class: "link", text: `${e.name} (${e.kind})`, onclick: () => { $("#photo-dialog").close(); select("event", p.event); } }));
  }
  row("sidecar", p.sidecar ? "yes" : "no");
  if (p.error) row("error", p.error);
  $("#photo-dialog").showModal();
}

// ------------------------------------------------------------------- init

load().catch((e) => {
  document.body.replaceChildren(el("p", { class: "warning", text: "Could not load the library: " + e.message }));
});
