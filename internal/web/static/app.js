// The organize front end. The parts of the page live in js/; this loads
// the library and draws everything once.

import { el, api } from "./js/util.js";
import { state, render, setLibrary, loadReview, loadHash } from "./js/state.js";
import { setupMap } from "./js/map.js";
import "./js/options.js";
import "./js/folders.js";
import "./js/timeline.js";
import "./js/detail.js";
import "./js/suggestions.js";
import "./js/apply.js";
import "./js/keys.js";

async function init() {
  const [config, lib, undo] = await Promise.all([api("api/config"), api("api/library"), api("api/journal")]);
  state.config = config;
  state.undo = undo;
  setupMap();
  state.lib = lib; // loadReview and loadHash need the root and the folders
  loadReview();
  setLibrary(lib);
  loadHash();
  render();
  render("mapFit");
}

init().catch((e) => {
  document.body.replaceChildren(el("p", { class: "warning", text: "Could not load the library: " + e.message }));
});
