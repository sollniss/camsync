// The map: home, the events as circles sized by their photos, and the
// photos of the selection. It follows the selection when that changes and
// otherwise stays where it was panned to.

import { $, el, plural, distanceMetres } from "./util.js";
import { state, register, select, selectedPhotos } from "./state.js";
import { openPhoto } from "./photo.js";

let map = null;
const layers = {};
const kindColor = { trip: "#d9480f", outing: "#e8a33d", home: "#4c8fd6" };

function ensureMap() {
  if (map) return map;
  map = L.map("map", { worldCopyJump: true }).setView([30, 10], 2);
  L.tileLayer(state.config.tiles, { maxZoom: 19, attribution: state.config.attribution }).addTo(map);
  for (const name of ["events", "photos", "home"]) layers[name] = L.layerGroup().addTo(map);
  const Fit = L.Control.extend({
    onAdd: () => el("button", {
      type: "button", class: "map-fit leaflet-bar", title: "Zoom to the selection, or to everything",
      onclick: (ev) => { ev.stopPropagation(); fit(); },
    }, "Fit"),
  });
  new Fit({ position: "topright" }).addTo(map);
  return map;
}

// bounds are the points in view when the map fits: the selection, or
// every event.
let bounds = [];

function renderMap() {
  if (!state.config.tiles) return;
  const lib = state.lib;
  ensureMap();
  for (const l of Object.values(layers)) l.clearLayers();
  bounds = [];
  if (lib.home) {
    L.circleMarker([lib.home.lat, lib.home.lon], { radius: 7, color: "#111", fillColor: "#fff", fillOpacity: 1, weight: 2 })
      .bindTooltip("home" + (lib.home.inferred ? " (inferred)" : ""))
      .addTo(layers.home);
    L.circle([lib.home.lat, lib.home.lon], { radius: distanceMetres(lib.options.homeRadius), color: "#111", weight: 1, fill: false, dashArray: "4 4" }).addTo(layers.home);
  }
  const selectedEvent = state.selection?.type === "event" ? state.selection.index : null;
  const selectedFolder = state.selection?.type === "folder" ? state.selection.index : null;
  lib.events.forEach((e, i) => {
    if (!e.center) return;
    const dim = (selectedEvent !== null && selectedEvent !== i) || (selectedFolder !== null && !e.folders.some((f) => f.folder === selectedFolder));
    L.circleMarker(e.center, {
      radius: 5 + Math.min(14, Math.sqrt(e.photos.length)),
      color: selectedEvent === i ? "#000" : "#fff",
      weight: selectedEvent === i ? 2 : 1,
      fillColor: kindColor[e.kind] || "#999",
      fillOpacity: dim ? 0.25 : 0.8,
    }).bindTooltip(`${e.name} · ${e.kind} · ${plural(e.photos.length, "photo")}`)
      .on("click", () => select("event", i))
      .addTo(layers.events);
    if (!dim) bounds.push(e.center);
  });
  const sel = selectedPhotos();
  if (sel) {
    const list = [...sel];
    for (const idx of list) {
      const p = lib.photos[idx];
      if (p.lat === undefined) continue;
      L.circleMarker([p.lat, p.lon], { radius: 3, color: "#111", weight: 1, fillColor: "#fff", fillOpacity: 1 })
        .bindTooltip(`${p.rel}\n${p.time || ""}`)
        .on("click", () => openPhoto(idx, { list }))
        .addTo(layers.photos);
      bounds.push([p.lat, p.lon]);
    }
  }
}

function fit() {
  if (map && bounds.length > 0) map.fitBounds(bounds, { padding: [20, 20], maxZoom: 12 });
}

register("map", renderMap);
// Drawn and fitted when the selection changes, and once at the start.
register("mapFit", () => { renderMap(); fit(); }, { auto: false });

export function setupMap() {
  if (!state.config.tiles) {
    document.body.classList.add("no-map");
    $("#map").remove();
  }
}
