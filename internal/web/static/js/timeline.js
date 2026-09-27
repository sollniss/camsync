// The timeline: one row per kind of event, zoomed with the wheel, panned by
// dragging, reset by double click.

import { $, parseTime, DAY, plural } from "./util.js";
import { state, register, select, saveHash } from "./state.js";

const LEFT = 44, RIGHT = 8, TOP = 22, BOTTOM = 18;
const ns = "http://www.w3.org/2000/svg";

function make(tag, attrs) {
  const n = document.createElementNS(ns, tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  return n;
}

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
// into a corner.
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
  if (!state.view) state.view = focusedRange();
  const [t0, t1] = state.view;
  const x = (t) => LEFT + ((t - t0) / (t1 - t0)) * (W - LEFT - RIGHT);
  const rows = { trip: 0, outing: 1, home: 2 };
  const rowH = (H - TOP - BOTTOM) / 3;
  const focused = document.activeElement?.closest?.("#timeline .tl-event")?.dataset.index;
  svg.replaceChildren();
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);

  // Axis with ticks chosen for the visible span.
  const axis = make("g", { class: "tl-axis" });
  const span = t1 - t0;
  const step = span > 4 * 365 * DAY ? "year" : span > 120 * DAY ? "month" : span > 8 * DAY ? "day" : "hour";
  const tick = new Date(t0);
  tick.setUTCMinutes(0, 0, 0);
  if (step !== "hour") tick.setUTCHours(0);
  if (step === "month" || step === "year") tick.setUTCDate(1);
  if (step === "year") tick.setUTCMonth(0);
  const fmt = { year: (d) => d.getUTCFullYear(), month: (d) => d.toISOString().slice(0, 7), day: (d) => d.toISOString().slice(5, 10), hour: (d) => d.toISOString().slice(11, 16) };
  let guard = 0;
  let lastLabelX = -Infinity;
  while (tick.getTime() <= t1 && guard++ < 400) {
    const px = x(tick.getTime());
    if (px >= LEFT) {
      axis.append(make("line", { x1: px, x2: px, y1: TOP - 4, y2: H - BOTTOM }));
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
    const t = make("text", { x: 4, y: TOP + r * rowH + rowH / 2 + 4, class: "tl-row" });
    t.textContent = kind;
    svg.append(t);
  }

  const selectedEvent = state.selection?.type === "event" ? state.selection.index : null;
  const selectedFolder = state.selection?.type === "folder" ? state.selection.index : null;
  lib.events.forEach((e, i) => {
    const a = parseTime(e.first), b = parseTime(e.last);
    if (b < t0 || a > t1) return;
    const x0 = Math.max(LEFT, x(a));
    const x1 = Math.min(W - RIGHT, x(b));
    const w = Math.max(4, x1 - x0);
    const r = rows[e.kind] ?? 1;
    let cls = "tl-event " + e.kind;
    if (e.located === 0) cls += " unplaced";
    if (selectedEvent === i) cls += " selected";
    if (selectedFolder !== null && !e.folders.some((f) => f.folder === selectedFolder)) cls += " dim";
    const label = `${e.name} · ${e.kind} · ${plural(e.photos.length, "photo")} · ${Object.keys(e.cameras).join(", ")}`;
    const rect = make("rect", {
      x: x0, y: TOP + r * rowH + 3, width: w, height: rowH - 6, rx: 2, class: cls,
      tabindex: 0, role: "button", "aria-label": label, "aria-pressed": String(selectedEvent === i), "data-index": i,
    });
    const title = make("title", {});
    title.textContent = label;
    rect.append(title);
    rect.addEventListener("click", (ev) => { ev.stopPropagation(); select("event", i); });
    rect.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); select("event", i); }
    });
    svg.append(rect);
  });
  if (focused !== undefined) svg.querySelector(`.tl-event[data-index="${focused}"]`)?.focus();
}

register("timeline", renderTimeline);

// Wheel zooms around the cursor, dragging pans, double click resets.
const svg = $("#timeline");
let drag = null;
svg.addEventListener("wheel", (ev) => {
  if (!state.view) return;
  ev.preventDefault();
  const [t0, t1] = state.view;
  const rect = svg.getBoundingClientRect();
  const frac = Math.min(1, Math.max(0, (ev.clientX - rect.left - LEFT) / (rect.width - LEFT - RIGHT)));
  const pivot = t0 + frac * (t1 - t0);
  const [f0, f1] = fullRange();
  const span = Math.max(3600000, Math.min((t1 - t0) * (ev.deltaY > 0 ? 1.25 : 0.8), f1 - f0));
  state.view = [pivot - frac * span, pivot + (1 - frac) * span];
  renderTimeline();
  saveHash();
}, { passive: false });
svg.addEventListener("mousedown", (ev) => {
  if (!state.view || ev.button !== 0) return;
  drag = { x: ev.clientX, view: state.view.slice() };
  svg.classList.add("dragging");
});
window.addEventListener("mousemove", (ev) => {
  if (!drag) return;
  const rect = svg.getBoundingClientRect();
  const dt = ((ev.clientX - drag.x) / (rect.width - LEFT - RIGHT)) * (drag.view[1] - drag.view[0]);
  state.view = [drag.view[0] - dt, drag.view[1] - dt];
  renderTimeline();
});
window.addEventListener("mouseup", () => {
  if (!drag) return;
  drag = null;
  svg.classList.remove("dragging");
  saveHash();
});
svg.addEventListener("dblclick", () => { state.view = fullRange(); renderTimeline(); saveHash(); });
window.addEventListener("resize", () => state.lib && renderTimeline());
