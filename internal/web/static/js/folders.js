// The folder pane: the library's folders as a tree that can be searched.

import { $, el } from "./util.js";
import { state, register, select, isSelected } from "./state.js";

let filter = "";
let collapsed = null; // tree paths folded away; set on the first render

// tree turns the folders' relative paths into nested nodes. Directories
// without photos of their own, such as the "Trips" above the trips, are
// nodes without a folder.
function tree(folders) {
  const root = { name: "", path: "", folder: null, children: new Map(), photos: 0 };
  folders.forEach((f, i) => {
    if (f.rel === ".") {
      root.folder = i;
      return;
    }
    let node = root;
    const parts = f.rel.split("/");
    parts.forEach((name, depth) => {
      if (!node.children.has(name)) {
        node.children.set(name, { name, path: parts.slice(0, depth + 1).join("/"), folder: null, children: new Map(), photos: 0 });
      }
      node = node.children.get(name);
    });
    node.folder = i;
  });
  const count = (node) => {
    node.photos = (node.folder !== null ? folders[node.folder].photos.length : 0);
    for (const c of node.children.values()) node.photos += count(c);
    return node.photos;
  };
  count(root);
  return root;
}

function matches(node, folders) {
  if (!filter) return true;
  if (node.path.toLowerCase().includes(filter)) return true;
  if (node.folder !== null && folders[node.folder].kind === filter) return true;
  for (const c of node.children.values()) if (matches(c, folders)) return true;
  return false;
}

function renderFolders() {
  const lib = state.lib;
  const root = tree(lib.folders);
  if (collapsed === null) {
    // Large libraries start folded below the top level.
    collapsed = new Set();
    if (lib.folders.length > 40) {
      const fold = (node, depth) => {
        for (const c of node.children.values()) {
          if (c.children.size > 0 && depth >= 1) collapsed.add(c.path);
          fold(c, depth + 1);
        }
      };
      fold(root, 0);
    }
  }
  // The selected folder is always in view.
  if (state.selection && state.selection.type === "folder") {
    const parts = lib.folders[state.selection.index].rel.split("/");
    for (let i = 1; i < parts.length; i++) collapsed.delete(parts.slice(0, i).join("/"));
  }
  $("#folder-count").textContent = lib.folders.length;

  const row = (node, depth) => {
    const open = filter !== "" || !collapsed.has(node.path);
    const f = node.folder !== null ? lib.folders[node.folder] : null;
    const toggle = node.children.size > 0
      ? el("button", {
        type: "button", class: "twisty", "aria-expanded": String(open), "aria-label": (open ? "Collapse " : "Expand ") + node.path,
        onclick: () => { if (open) collapsed.add(node.path); else collapsed.delete(node.path); renderFolders(); },
      }, open ? "▾" : "▸")
      : el("span", { class: "twisty" });
    const label = f
      ? el("button", {
        type: "button", class: "name" + (isSelected("folder", node.folder) ? " selected" : ""), title: `${f.rel}\n${f.note}`,
        "aria-pressed": String(isSelected("folder", node.folder)), onclick: () => select("folder", node.folder),
      }, el("span", { class: "badge " + f.kind, text: f.kind }), el("span", { class: "label", text: node.path === "" ? "(root)" : node.name }))
      : el("span", { class: "name dir" }, el("span", { class: "label", text: node.name }));
    const n = node.children.size > 0 && !open ? node.photos : f ? f.photos.length : "";
    const li = el("li", { style: `--depth:${depth}` }, el("div", { class: "row" }, toggle, label, el("span", { class: "n", text: n })));
    return li;
  };
  const items = [];
  if (root.folder !== null && matches({ path: "(root)", folder: root.folder, children: new Map() }, lib.folders)) {
    items.push(row({ ...root, children: new Map(), path: "" }, 0));
  }
  const walk = (node, depth) => {
    for (const c of [...node.children.values()].sort((a, b) => a.name.localeCompare(b.name))) {
      if (!matches(c, lib.folders)) continue;
      items.push(row(c, depth));
      if (filter !== "" || !collapsed.has(c.path)) walk(c, depth + 1);
    }
  };
  walk(root, 0);
  $("#folders").replaceChildren(...(items.length ? items : [el("li", { class: "muted empty", text: "No folder matches." })]));
  const sel = $("#folders .name.selected");
  if (sel) sel.scrollIntoView({ block: "nearest" });
}

register("folders", renderFolders);

$("#folder-filter").addEventListener("input", (ev) => {
  filter = ev.target.value.trim().toLowerCase();
  renderFolders();
});
