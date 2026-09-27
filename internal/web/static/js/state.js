// The state of the page and the functions that change it. Each part of the
// page registers a render function; changes call render with the parts
// they affect, and every render function reads the state and rewrites its
// part of the page.

export const state = {
  config: { tiles: "", attribution: "" },
  lib: null, // the library as the server sent it: the plan and the photos
  selection: null, // {type: "event" | "folder", index}
  onlyRelated: false,
  view: null, // timeline [t0, t1] in ms
  undo: null, // the last applied plan that can be undone, or null
  // The review, kept in local storage per library. Suggestions are known
  // by an ID derived from what they propose, so a replan never hands a
  // tick to a different suggestion.
  accepted: new Set(), // suggestion IDs
  excluded: new Map(), // suggestion ID -> Set of photo paths left out
  dest: new Map(), // suggestion ID -> destination relative to the root
};

const renderers = {};
const manual = new Set(); // parts only redrawn when named

export function register(name, fn, { auto = true } = {}) {
  renderers[name] = fn;
  if (!auto) manual.add(name);
}

// render redraws the named parts, or all but the manual ones.
export function render(...names) {
  const all = Object.keys(renderers).filter((n) => !manual.has(n));
  for (const name of names.length ? names : all) renderers[name]?.();
}

// ------------------------------------------------------------- the library

// setLibrary takes a whole library, after loading, rescanning or moving.
// Review entries of suggestions that are gone are dropped.
export function setLibrary(lib) {
  state.lib = lib;
  const ids = new Set(lib.suggestions.map((s) => s.id));
  for (const id of state.accepted) if (!ids.has(id)) state.accepted.delete(id);
  for (const id of state.excluded.keys()) if (!ids.has(id)) state.excluded.delete(id);
  for (const id of state.dest.keys()) if (!ids.has(id)) state.dest.delete(id);
  saveReview();
  afterPlan();
}

// setPlan takes a new plan of the same photos. The review is kept whole:
// replanning back and forth brings earlier ticks back.
export function setPlan(plan) {
  state.lib = { ...plan, photos: state.lib.photos };
  afterPlan();
}

function afterPlan() {
  const lib = state.lib;
  for (const p of lib.photos) p.event = null;
  lib.events.forEach((e, i) => { for (const idx of e.photos) lib.photos[idx].event = i; });
  lib.folders.forEach((f, i) => { for (const idx of f.photos) lib.photos[idx].folder = i; });
  lib.folderByPath = new Map(lib.folders.map((f, i) => [f.path, i]));
  lib.folderByRel = new Map(lib.folders.map((f, i) => [f.rel, i]));
  if (state.selection) {
    const list = state.selection.type === "event" ? lib.events : lib.folders;
    if (state.selection.index >= list.length) state.selection = null;
  }
  render();
  saveHash();
}

// ---------------------------------------------------------------- selection

export function isSelected(type, index) {
  return !!state.selection && state.selection.type === type && state.selection.index === index;
}

export function select(type, index, { toggle = true } = {}) {
  state.selection = toggle && isSelected(type, index) ? null : { type, index };
  render("folders", "timeline", "detail", "suggestions");
  render("mapFit");
  saveHash();
}

export function clearSelection() {
  if (!state.selection) return;
  state.selection = null;
  render("folders", "timeline", "detail", "suggestions", "mapFit");
  saveHash();
}

// selectedPhotos returns the photos of the selection as a set of indexes,
// or null.
export function selectedPhotos() {
  const sel = state.selection;
  if (!sel) return null;
  const list = sel.type === "event" ? state.lib.events : state.lib.folders;
  return new Set(list[sel.index].photos);
}

export function suggestionRelated(s) {
  const sel = state.selection;
  if (!sel) return true;
  if (sel.type === "event") return s.event === sel.index;
  const folder = state.lib.folders[sel.index];
  return s.from === sel.index || destination(s).path === folder.path;
}

// ------------------------------------------------------------- the review

// destination is where a suggestion's photos go: the one the user typed or
// the one suggested. rel is relative to the root; exists says whether a
// folder of the library is there. Stray suggestions have none until one
// is given.
export function destination(s) {
  const lib = state.lib;
  const rel = state.dest.get(s.id);
  if (rel !== undefined) {
    const i = lib.folderByRel.get(rel === "" ? "." : rel);
    return { rel, path: i !== undefined ? lib.folders[i].path : null, exists: i !== undefined, edited: true };
  }
  if (!s.to) return { rel: "", path: null, exists: false, edited: false };
  return { rel: s.toRel, path: s.to, exists: s.exists, edited: false };
}

export const hasDestination = (s) => state.dest.has(s.id) || !!s.to;

export function includedPhotos(s) {
  const out = state.excluded.get(s.id);
  if (!out || out.size === 0) return s.photos;
  return s.photos.filter((idx) => !out.has(state.lib.photos[idx].path));
}

export function isExcluded(s, idx) {
  return !!state.excluded.get(s.id)?.has(state.lib.photos[idx].path);
}

export function toggleExcluded(s, idx) {
  const path = state.lib.photos[idx].path;
  let set = state.excluded.get(s.id);
  if (!set) state.excluded.set(s.id, (set = new Set()));
  if (set.has(path)) set.delete(path); else set.add(path);
  if (set.size === 0) state.excluded.delete(s.id);
  saveReview();
}

export function setAccepted(s, on) {
  if (on && hasDestination(s)) state.accepted.add(s.id); else state.accepted.delete(s.id);
  saveReview();
}

// setDestination overrides where a suggestion goes; null goes back to the
// suggested folder.
export function setDestination(s, rel) {
  if (rel === null || (s.to && rel === s.toRel)) state.dest.delete(s.id);
  else state.dest.set(s.id, rel);
  if (!hasDestination(s)) state.accepted.delete(s.id);
  saveReview();
}

// acceptedMoves lists the moves of the accepted suggestions. Destinations
// are relative to the root; the server resolves them.
export function acceptedMoves() {
  const moves = new Map();
  for (const s of state.lib.suggestions) {
    if (!state.accepted.has(s.id) || !hasDestination(s)) continue;
    const to = destination(s).rel || "."; // "" would be no destination at all
    for (const idx of includedPhotos(s)) {
      const path = state.lib.photos[idx].path;
      if (!moves.has(path)) moves.set(path, { path, to });
    }
  }
  return [...moves.values()];
}

const reviewKey = () => "camsync-review:" + state.lib.root;

export function loadReview() {
  try {
    const saved = JSON.parse(localStorage.getItem(reviewKey()) || "null");
    if (!saved) return;
    state.accepted = new Set(saved.accepted || []);
    state.excluded = new Map(Object.entries(saved.excluded || {}).map(([id, paths]) => [id, new Set(paths)]));
    state.dest = new Map(Object.entries(saved.dest || {}));
  } catch { /* a broken entry is as good as none */ }
}

function saveReview() {
  if (!state.lib) return;
  const data = {
    accepted: [...state.accepted],
    excluded: Object.fromEntries([...state.excluded].map(([id, set]) => [id, [...set]])),
    dest: Object.fromEntries(state.dest),
  };
  try { localStorage.setItem(reviewKey(), JSON.stringify(data)); } catch { /* storage full or off */ }
}

export function clearReview() {
  state.accepted.clear();
  state.excluded.clear();
  state.dest.clear();
  saveReview();
}

// ---------------------------------------------------------------- the URL

// The selection and the timeline's range live in the URL hash, so that a
// reload shows the same. Events are named by their first photo's time and
// folders by their path, which survive a replan better than indexes.
let hashTimer = null;
export function saveHash() {
  clearTimeout(hashTimer);
  hashTimer = setTimeout(() => {
    if (!state.lib) return;
    const q = new URLSearchParams();
    const sel = state.selection;
    if (sel && sel.type === "event") q.set("event", state.lib.events[sel.index].first);
    if (sel && sel.type === "folder") q.set("folder", state.lib.folders[sel.index].rel);
    if (state.view) q.set("view", state.view.map((t) => Math.round(t / 60000)).join(","));
    history.replaceState(null, "", "#" + q.toString());
  }, 200);
}

export function loadHash() {
  const q = new URLSearchParams(location.hash.slice(1));
  const lib = state.lib;
  if (q.has("event")) {
    const i = lib.events.findIndex((e) => e.first === q.get("event"));
    if (i >= 0) state.selection = { type: "event", index: i };
  } else if (q.has("folder")) {
    const i = lib.folderByRel.get(q.get("folder"));
    if (i !== undefined) state.selection = { type: "folder", index: i };
  }
  const view = (q.get("view") || "").split(",").map(Number);
  if (view.length === 2 && view.every(Number.isFinite) && view[0] < view[1]) state.view = view.map((m) => m * 60000);
}
