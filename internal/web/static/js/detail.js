// The detail pane: what the selected event or folder holds.

import { $, el, fmtDistance, folderName, plural } from "./util.js";
import { state, register, select } from "./state.js";
import { thumb } from "./photo.js";
import { replan } from "./options.js";

function chip(badge, text, onclick) {
  return el("button", { type: "button", class: "chip", onclick }, el("span", { class: "badge " + badge, text: badge }), " " + text);
}

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
      return chip(f.kind, `${folderName(f)} (${fc.count})`, () => select("folder", fc.folder, { toggle: false }));
    })));
    photos = e.photos;
  } else {
    const f = lib.folders[sel.index];
    box.replaceChildren(el("h3", {}, el("span", { class: "badge " + f.kind, text: f.kind }), " ", folderName(f)));
    const given = lib.options.inboxes.includes(f.rel);
    let inbox = null;
    if (given) {
      inbox = el("button", { type: "button", onclick: () => setInbox(f.rel, false) }, "Stop treating as inbox");
    } else if (f.kind !== "inbox") {
      inbox = el("button", { type: "button", title: "Photos are sorted out of inboxes: trips found here get folders of their own", onclick: () => setInbox(f.rel, true) }, "Treat as inbox");
    }
    row("note", el("span", {}, f.note, inbox ? " " : null, inbox));
    row("photos", `${f.photos.length}` + (f.undated ? `, ${f.undated} undated` : ""));
    row("events", el("span", { class: "chips" }, f.events.map((ec) => {
      const e = lib.events[ec.event];
      return chip(e.kind, `${e.name} (${ec.count}/${e.photos.length})`, () => select("event", ec.event, { toggle: false }));
    })));
    photos = f.photos;
  }
  box.append(dl, photoGrid(photos, 120));
}

function setInbox(rel, on) {
  const inboxes = state.lib.options.inboxes.filter((r) => r !== rel);
  if (on) inboxes.push(rel);
  const params = new URLSearchParams();
  if (inboxes.length === 0) params.append("inbox", "");
  for (const r of inboxes) params.append("inbox", r);
  replan(params);
}

function photoGrid(indexes, limit) {
  const grid = el("div", { class: "grid" });
  const add = (from, to) => {
    for (const idx of indexes.slice(from, to)) {
      const p = state.lib.photos[idx];
      grid.append(el("figure", {}, thumb(idx, 160, { list: indexes }), el("figcaption", { text: p.rel.split("/").pop() })));
    }
  };
  add(0, limit);
  if (indexes.length > limit) {
    const more = el("button", { type: "button", class: "more" }, `Show ${plural(indexes.length - limit, "more photo")}`);
    more.addEventListener("click", () => { more.remove(); add(limit, indexes.length); });
    grid.append(more);
  }
  return grid;
}

register("detail", renderDetail);
