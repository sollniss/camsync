// The header: the library's figures and the planning options.

import { $, api, busy, toast, plural } from "./util.js";
import { state, register, setPlan, setLibrary } from "./state.js";

const fields = ["gap", "jump", "homeRadius", "minTrip", "minTripPhotos", "layout"];

function renderHeader() {
  const lib = state.lib;
  $("#root").textContent = lib.root;
  const undated = lib.photos.filter((p) => !p.time).length;
  const home = lib.home
    ? `home ${lib.home.lat.toFixed(4)}, ${lib.home.lon.toFixed(4)}${lib.home.inferred ? " (inferred)" : ""}`
    : "home unknown (no positions)";
  $("#stats").textContent = `${plural(lib.photos.length, "photo")} · ${plural(lib.events.length, "event")} · ${plural(lib.folders.length, "folder")} · ${home}` +
    (undated ? ` · ${undated} undated` : "") + (lib.ignored ? ` · ${plural(lib.ignored, "other file")}` : "");
  const f = $("#options");
  const o = lib.options;
  for (const name of fields) f[name].value = o[name];
  f.home.value = o.home || "";
  f.inboxes.value = o.inboxes.join("; ");
}

register("header", renderHeader);

// replan plans the photos again with the options in params; those not
// given stay as they are.
export async function replan(params) {
  const button = $("#replan");
  document.body.classList.add("busy");
  try {
    await busy(button, "Planning…", async () => {
      setPlan(await api("api/plan", {
        method: "POST",
        body: params.toString(),
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
      }));
    });
  } catch (e) {
    toast("Could not replan: " + e.message, "error");
  } finally {
    document.body.classList.remove("busy");
  }
}

$("#options").addEventListener("submit", (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const params = new URLSearchParams();
  for (const name of fields) params.set(name, f[name].value.trim());
  params.set("home", f.home.value.trim());
  const inboxes = f.inboxes.value.split(";").map((s) => s.trim()).filter(Boolean);
  if (inboxes.length === 0) params.append("inbox", "");
  for (const r of inboxes) params.append("inbox", r);
  replan(params);
});

$("#rescan").addEventListener("click", async () => {
  document.body.classList.add("busy");
  try {
    await busy($("#rescan"), "Scanning…", async () => setLibrary(await api("api/rescan", { method: "POST" })));
    toast("Library scanned again.", "ok");
  } catch (e) {
    toast("Could not scan: " + e.message, "error");
  } finally {
    document.body.classList.remove("busy");
  }
});
