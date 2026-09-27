// Applying the accepted moves and undoing them. Both are tried as a dry run
// first, and the dialog shows what would happen before anything moves.

import { $, el, api, busy, toast, plural, joinPath } from "./util.js";
import { state, render, setLibrary, acceptedMoves } from "./state.js";

const dialog = $("#apply-dialog");

// ask shows the dialog and resolves to whether the user confirmed.
function ask(title, body, confirm) {
  $("#apply-title").textContent = title;
  $("#apply-body").replaceChildren(...body);
  const ok = $("#apply-confirm");
  ok.hidden = !confirm;
  ok.textContent = confirm || "";
  $("#apply-cancel").textContent = confirm ? "Cancel" : "Close";
  dialog.returnValue = "";
  dialog.showModal();
  if (confirm) ok.focus();
  return new Promise((resolve) => {
    dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true });
  });
}

const rel = (path) => {
  const root = state.lib.root.replace(/[\\/]+$/, "");
  if (path === root) return "(root)";
  return path.startsWith(root) ? path.slice(root.length + 1).replace(/\\/g, "/") : path;
};

// outcome describes a (dry) run: the moves grouped by destination, the
// folders created and every failure.
function outcome(res, back = false) {
  const byDest = new Map();
  for (const r of res.results) {
    if (r.error) continue;
    byDest.set(r.to, (byDest.get(r.to) || 0) + 1);
  }
  const created = new Set(res.created);
  const nodes = [];
  if (byDest.size > 0) {
    nodes.push(el("table", { class: "dests" },
      el("thead", {}, el("tr", {}, el("th", { text: back ? "Back to" : "To" }), el("th", { class: "num", text: "Photos" }))),
      el("tbody", {}, [...byDest].sort((a, b) => a[0].localeCompare(b[0])).map(([to, n]) =>
        el("tr", {}, el("td", { class: "mono" }, rel(to), created.has(to) ? el("span", { class: "new", text: "new" }) : null), el("td", { class: "num", text: n }))))));
  }
  const failed = res.results.filter((r) => r.error);
  if (failed.length > 0) {
    nodes.push(el("h4", { class: "err", text: `${plural(failed.length, "photo")} cannot move` }));
    nodes.push(el("table", { class: "failures" }, el("tbody", {}, failed.map((r) =>
      el("tr", {}, el("td", { class: "mono", text: rel(r.path) }), el("td", { class: "err", text: r.error }))))));
  }
  return nodes;
}

const warning = () => el("div", { class: "warning" }, "Catalog applications such as Lightroom or digiKam lose track of moved files. Immich rescans external libraries on its own.");

function finished(res, what) {
  state.undo = res.undo;
  if (res.library) setLibrary(res.library); else render("applyBar");
  const parts = [`${res.moved} ${what}`];
  if (res.failed) parts.push(`${res.failed} failed`);
  if (res.created.length) parts.push(`${plural(res.created.length, "folder")} created`);
  toast(parts.join(", ") + ".", res.failed ? "error" : "ok");
  if (res.journalError) toast("The moves could not be recorded for undo: " + res.journalError, "error");
  if (res.failed) ask("Some photos did not move", outcome(res), null);
}

$("#apply").addEventListener("click", async () => {
  const moves = acceptedMoves();
  if (moves.length === 0) return;
  let dry;
  try {
    dry = await busy($("#apply"), "Checking…", () => api("api/apply", { method: "POST", body: JSON.stringify({ moves, dryRun: true }) }));
  } catch (e) {
    toast("Could not check the moves: " + e.message, "error");
    render("applyBar");
    return;
  }
  render("applyBar");
  const summary = el("p", {}, `${plural(dry.moved, "photo")} ${dry.moved === 1 ? "goes" : "go"} to ${plural(new Set(dry.results.filter((r) => !r.error).map((r) => r.to)).size, "folder")}` +
    (dry.created.length ? `, ${plural(dry.created.length, "new folder")} among them` : "") + ". Sidecars move with their photos. Nothing is overwritten, and the moves can be undone.");
  const ok = await ask(`Move ${plural(dry.moved, "photo")}?`, [summary, ...outcome(dry), warning()], dry.moved > 0 ? "Move files" : null);
  if (!ok) return;
  try {
    const res = await busy($("#apply"), "Moving…", () => api("api/apply", { method: "POST", body: JSON.stringify({ moves }) }));
    finished(res, "moved");
  } catch (e) {
    toast("Could not move: " + e.message, "error");
    render("applyBar");
  }
});

$("#undo").addEventListener("click", async () => {
  let dry;
  try {
    dry = await busy($("#undo"), "Checking…", () => api("api/undo", { method: "POST", body: JSON.stringify({ dryRun: true }) }));
  } catch (e) {
    toast("Could not check the undo: " + e.message, "error");
    render("applyBar");
    return;
  }
  render("applyBar");
  const when = state.undo ? new Date(state.undo.time).toLocaleString() : "last";
  const summary = el("p", {}, `The plan applied ${when} moved ${plural(dry.results.length, "photo")}; ${dry.moved} can go back where ${dry.moved === 1 ? "it was" : "they were"}. Folders it created are removed once empty.`);
  const ok = await ask("Undo the last apply?", [summary, ...outcome(dry, true)], dry.moved > 0 ? "Move back" : null);
  if (!ok) return;
  try {
    const res = await busy($("#undo"), "Moving back…", () => api("api/undo", { method: "POST", body: JSON.stringify({}) }));
    finished(res, "moved back");
  } catch (e) {
    toast("Could not undo: " + e.message, "error");
    render("applyBar");
  }
});

// The saved plan is what organize --apply reads; it finds the sidecars
// itself.
$("#download-plan").addEventListener("click", () => {
  const root = state.lib.root;
  const plan = { root, moves: acceptedMoves().map((m) => ({ path: m.path, to: joinPath(root, m.to) })) };
  const blob = new Blob([JSON.stringify(plan, null, 2)], { type: "application/json" });
  const a = el("a", { href: URL.createObjectURL(blob), download: "camsync-plan.json" });
  document.body.append(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(a.href);
});
