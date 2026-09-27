// The suggestions: cards to accept, with the photos to leave out and the
// destination to change, and the notes below them.

import { $, el, link, toast, plural, folderName } from "./util.js";
import {
  state, register, render, select, suggestionRelated, destination, hasDestination,
  includedPhotos, isExcluded, toggleExcluded, setAccepted, setDestination, acceptedMoves, clearReview,
} from "./state.js";
import { thumb } from "./photo.js";

const expanded = new Set(); // suggestion IDs showing all their photos
let editing = null; // suggestion ID whose destination is being edited

// cleanRel tidies a typed destination into a path relative to the root,
// or throws when it leaves the library or goes into a hidden folder.
function cleanRel(text) {
  const parts = text.trim().replace(/\\/g, "/").split("/").map((s) => s.trim()).filter((s) => s !== "" && s !== ".");
  for (const part of parts) {
    if (part.startsWith(".")) throw new Error(`"${part}": hidden folders and ".." are not part of the library`);
  }
  return parts.join("/");
}

function destinationEditor(s, dest) {
  const input = el("input", {
    type: "text", list: "folder-list", value: dest.rel, "aria-label": "Destination folder, relative to the library",
    placeholder: "folder, relative to the library",
  });
  const commit = () => {
    try {
      setDestination(s, cleanRel(input.value));
      editing = null;
      render("suggestions", "applyBar");
    } catch (e) {
      toast(e.message, "error");
    }
  };
  const cancel = () => { editing = null; render("suggestions"); };
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") { ev.preventDefault(); commit(); }
    if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); cancel(); }
  });
  setTimeout(() => { input.focus(); input.select(); });
  return el("span", { class: "edit" }, input,
    el("button", { type: "button", onclick: commit }, "OK"),
    el("button", { type: "button", onclick: cancel }, "Cancel"));
}

function card(s) {
  const lib = state.lib;
  const related = suggestionRelated(s);
  const accepted = state.accepted.has(s.id);
  const dest = destination(s);
  const included = includedPhotos(s);
  const cls = "card" + (accepted ? " accepted" : "") + (related ? "" : state.onlyRelated ? " hidden" : " unrelated");
  const node = el("div", { class: cls, "data-id": s.id, tabindex: "0", role: "group", "aria-label": `${s.kind} suggestion, ${plural(s.photos.length, "photo")}` });

  const check = el("input", { type: "checkbox", "aria-label": "Accept" });
  check.checked = accepted;
  check.disabled = !hasDestination(s) || included.length === 0;
  check.addEventListener("change", () => { setAccepted(s, check.checked); render("suggestions", "applyBar"); });
  const count = included.length === s.photos.length ? plural(s.photos.length, "photo") : `${included.length} of ${plural(s.photos.length, "photo")}`;
  node.append(el("div", { class: "head" },
    el("label", {}, check, el("span", { class: "badge " + s.kind, text: s.kind }), el("span", { class: "muted", text: count })),
    el("span", { class: "conf " + s.confidence, text: s.confidence, title: "How sure the event membership is: high with positions on both sides, low by time alone" }),
  ));

  const route = el("div", { class: "route" });
  route.append(s.from !== null ? link(folderName(lib.folders[s.from]), () => select("folder", s.from, { toggle: false })) : "several folders");
  route.append(el("span", { class: "arrow", "aria-hidden": "true", text: "→" }));
  if (editing === s.id) {
    route.append(destinationEditor(s, dest));
  } else {
    if (!hasDestination(s)) {
      route.append(el("span", { class: "muted", text: "nowhere yet" }));
    } else if (dest.exists) {
      const i = lib.folderByPath.get(dest.path);
      route.append(link(folderName(lib.folders[i]), () => select("folder", i, { toggle: false })));
    } else {
      route.append(el("span", {}, dest.rel || "(root)", el("span", { class: "new", text: "new" })));
    }
    route.append(" ", link(hasDestination(s) ? "change" : "choose", () => { editing = s.id; render("suggestions"); }, { class: "link small", title: "Choose another destination (e)" }));
    if (dest.edited) {
      route.append(" ", link("reset", () => { setDestination(s, null); render("suggestions", "applyBar"); }, { class: "link small", title: "Back to " + (s.toRel || "no destination") }));
    }
  }
  node.append(route);

  const reason = el("div", { class: "reason" }, s.reason);
  if (s.event !== null) reason.append(" ", link("show event", () => select("event", s.event, { toggle: false })));
  node.append(reason);

  const thumbs = el("div", { class: "thumbs" });
  const all = expanded.has(s.id);
  const shown = all ? s.photos : s.photos.slice(0, 6);
  for (const idx of shown) {
    const out = isExcluded(s, idx);
    const toggle = el("button", {
      type: "button", class: "exclude", "aria-pressed": String(out),
      title: out ? "Move this photo too" : "Leave this photo where it is",
      "aria-label": (out ? "Include " : "Leave out ") + lib.photos[idx].rel,
      onclick: () => { toggleExcluded(s, idx); render("suggestions", "applyBar"); },
    }, out ? "+" : "×");
    thumbs.append(el("div", { class: "thumb-wrap" + (out ? " excluded" : "") }, thumb(idx, 128, { list: s.photos, suggestion: s }), toggle));
  }
  if (!all && s.photos.length > shown.length) {
    thumbs.append(el("button", { type: "button", class: "more", title: "Show all", onclick: () => { expanded.add(s.id); render("suggestions"); } }, `+${s.photos.length - shown.length}`));
  }
  node.append(thumbs);
  return node;
}

function renderSuggestions() {
  const lib = state.lib;
  const box = $("#suggestions");
  const focusedId = document.activeElement?.closest?.("#suggestions .card")?.dataset.id;
  const focusedWasCard = document.activeElement?.matches?.("#suggestions .card");
  $("#suggestion-count").textContent = lib.suggestions.length;
  $("#folder-list").replaceChildren(...lib.folders.filter((f) => f.rel !== ".").map((f) => el("option", { value: f.rel })));
  if (lib.suggestions.length === 0) {
    box.replaceChildren(el("p", { class: "muted", text: "Nothing to tidy with these settings." }));
    return;
  }
  box.replaceChildren(...lib.suggestions.map(card));
  if (focusedId && focusedWasCard && editing === null) box.querySelector(`.card[data-id="${focusedId}"]`)?.focus();
}

function renderNotes() {
  const ul = $("#notes");
  const notes = state.lib.notes;
  if (notes.length === 0) {
    ul.replaceChildren(el("li", { class: "undated muted", text: "Nothing to report." }));
    return;
  }
  ul.replaceChildren(...notes.map((n) => {
    const li = el("li", { class: n.kind }, n.text);
    if (n.folder !== null) li.append(" ", link("show folder", () => select("folder", n.folder, { toggle: false })));
    if (n.photos.length > 0) {
      const strip = el("div", { class: "thumbs" });
      li.append(" ", link(plural(n.photos.length, "photo"), () => {
        strip.replaceChildren(...n.photos.slice(0, 40).map((idx) => thumb(idx, 128, { list: n.photos })));
      }), strip);
    }
    return li;
  }));
}

function renderApplyBar() {
  const n = acceptedMoves().length;
  const btn = $("#apply");
  btn.disabled = n === 0;
  btn.textContent = `Apply ${plural(n, "move")}`;
  $("#download-plan").disabled = n === 0;
  const undo = $("#undo");
  undo.disabled = !state.undo;
  undo.title = state.undo
    ? `Move the ${plural(state.undo.moves, "photo")} of the plan applied ${new Date(state.undo.time).toLocaleString()} back`
    : "Nothing applied yet";
}

register("suggestions", renderSuggestions);
register("notes", renderNotes);
register("applyBar", renderApplyBar);

// -------------------------------------------------------------- toolbar

function acceptWhere(pred) {
  for (const s of state.lib.suggestions) {
    if (pred(s) && (!state.onlyRelated || suggestionRelated(s)) && includedPhotos(s).length > 0) setAccepted(s, true);
  }
  render("suggestions", "applyBar");
}

$("#only-related").addEventListener("change", (ev) => { state.onlyRelated = ev.target.checked; render("suggestions"); });
$("#accept-high").addEventListener("click", () => acceptWhere((s) => s.confidence === "high"));
$("#accept-all").addEventListener("click", () => acceptWhere(() => true));
$("#accept-none").addEventListener("click", () => {
  clearReview();
  expanded.clear();
  render("suggestions", "applyBar");
});

// ------------------------------------------------------------- keyboard

const cards = () => [...document.querySelectorAll("#suggestions .card:not(.hidden)")];
const focusedCard = () => document.activeElement?.closest?.("#suggestions .card");
const suggestionOf = (node) => state.lib.suggestions.find((s) => s.id === node.dataset.id);

export function moveFocus(delta) {
  const list = cards();
  if (list.length === 0) return;
  const i = list.indexOf(focusedCard());
  const next = list[i < 0 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, i + delta))];
  next.focus();
  next.scrollIntoView({ block: "nearest" });
}

// onFocused runs an action on the focused card's suggestion: "accept",
// "edit" or "show".
export function onFocused(action) {
  const node = focusedCard();
  if (!node) return false;
  const s = suggestionOf(node);
  if (!s) return false;
  if (action === "accept") {
    if (!hasDestination(s)) { toast("Choose a destination first (e).", "info"); return true; }
    setAccepted(s, !state.accepted.has(s.id));
    render("suggestions", "applyBar");
  } else if (action === "edit") {
    editing = s.id;
    render("suggestions");
  } else if (action === "show") {
    if (s.event !== null) select("event", s.event, { toggle: false });
    else if (s.from !== null) select("folder", s.from, { toggle: false });
    node.focus();
  }
  return true;
}
