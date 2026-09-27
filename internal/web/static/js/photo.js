// Thumbnails and the photo dialog, which pages through the photos it was
// opened from.

import { $, el, link, folderName } from "./util.js";
import { state, select, render, isExcluded, toggleExcluded } from "./state.js";

export const previewURL = (photo, size) => `api/preview?path=${encodeURIComponent(photo.path)}&size=${size}`;

// thumb is a button showing a photo's preview that opens the photo. ctx
// says what the dialog pages through: list, the photo indexes, and
// suggestion, when the photo is one of a suggestion's.
export function thumb(idx, size, ctx = {}) {
  const p = state.lib.photos[idx];
  const title = `${p.rel}\n${p.camera}\n${p.time || "no date"}`;
  const img = el("img", { src: previewURL(p, size), loading: "lazy", alt: "" });
  const button = el("button", { type: "button", class: "thumb", title, "aria-label": p.rel, onclick: () => openPhoto(idx, ctx) }, img);
  img.addEventListener("error", () => { img.remove(); button.classList.add("noprev"); });
  return button;
}

let current = null; // {idx, list, suggestion}

export function openPhoto(idx, { list = [idx], suggestion = null } = {}) {
  current = { idx, list, suggestion };
  showPhoto();
  const dialog = $("#photo-dialog");
  if (!dialog.open) dialog.showModal();
}

function showPhoto() {
  const { idx, list, suggestion } = current;
  const lib = state.lib;
  const p = lib.photos[idx];
  const img = $("#photo-big");
  img.src = previewURL(p, 1200);
  img.alt = p.rel;
  const pos = list.indexOf(idx);
  $("#photo-pos").textContent = list.length > 1 ? `${pos + 1} / ${list.length}` : "";
  $("#photo-prev").disabled = pos <= 0;
  $("#photo-next").disabled = pos < 0 || pos >= list.length - 1;
  const meta = $("#photo-meta");
  meta.replaceChildren();
  const row = (k, v) => meta.append(el("dt", { text: k }), el("dd", {}, v));
  row("file", p.rel);
  row("camera", p.camera);
  row("format", p.format);
  row("taken", p.time || "no date");
  row("position", p.lat !== undefined ? `${p.lat.toFixed(5)}, ${p.lon.toFixed(5)}` : "none");
  row("folder", link(folderName(lib.folders[p.folder]), () => { $("#photo-dialog").close(); select("folder", p.folder, { toggle: false }); }));
  if (p.event !== null) {
    const e = lib.events[p.event];
    row("event", link(`${e.name} (${e.kind})`, () => { $("#photo-dialog").close(); select("event", p.event, { toggle: false }); }));
  }
  row("sidecar", p.sidecar ? "yes" : "no");
  if (p.error) row("error", p.error);
  const include = $("#photo-include");
  include.hidden = !suggestion;
  if (suggestion) {
    const box = include.querySelector("input");
    box.checked = !isExcluded(suggestion, idx);
  }
}

function step(delta) {
  if (!current) return;
  const pos = current.list.indexOf(current.idx) + delta;
  if (pos < 0 || pos >= current.list.length) return;
  current.idx = current.list[pos];
  showPhoto();
}

$("#photo-prev").addEventListener("click", () => step(-1));
$("#photo-next").addEventListener("click", () => step(1));
$("#photo-include input").addEventListener("change", () => {
  if (!current?.suggestion) return;
  toggleExcluded(current.suggestion, current.idx);
  render("suggestions", "applyBar");
});
$("#photo-dialog").addEventListener("keydown", (ev) => {
  if (ev.target.matches("input[type=text]")) return;
  if (ev.key === "ArrowLeft") { ev.preventDefault(); step(-1); }
  if (ev.key === "ArrowRight") { ev.preventDefault(); step(1); }
  if (ev.key === "x" && current?.suggestion) { $("#photo-include input").click(); }
});
$("#photo-dialog").addEventListener("close", () => { current = null; });
