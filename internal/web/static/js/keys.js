// Keyboard shortcuts for going through the suggestions. The photo dialog
// handles its own arrows.

import { $ } from "./util.js";
import { clearSelection } from "./state.js";
import { moveFocus, onFocused } from "./suggestions.js";

document.addEventListener("keydown", (ev) => {
  if (ev.defaultPrevented || ev.ctrlKey || ev.metaKey || ev.altKey) return;
  if (document.querySelector("dialog[open]")) return;
  const t = ev.target;
  if (t.matches("input, select, textarea, [contenteditable]")) return;
  const onButton = t.matches("button, a, [role=button]");
  switch (ev.key) {
    case "j":
      moveFocus(1);
      break;
    case "k":
      moveFocus(-1);
      break;
    case "x":
      if (!onFocused("accept")) return;
      break;
    case " ":
      if (onButton || !onFocused("accept")) return;
      break;
    case "e":
      if (!onFocused("edit")) return;
      break;
    case "o":
    case "Enter":
      if (onButton || !onFocused("show")) return;
      break;
    case "Escape":
      clearSelection();
      break;
    case "/":
      $("#folder-filter").focus();
      break;
    case "?":
      $("#help-dialog").showModal();
      break;
    default:
      return;
  }
  ev.preventDefault();
});

$("#help").addEventListener("click", () => $("#help-dialog").showModal());
