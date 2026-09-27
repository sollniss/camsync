// Small helpers shared by every part of the page.

export const $ = (sel) => document.querySelector(sel);

// el builds an element. Attributes starting with "on" become listeners,
// "class" and "text" set the class name and text content; children may be
// nodes, strings or nested arrays, and null or undefined are skipped.
export const el = (tag, attrs = {}, ...children) => {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v === true) node.setAttribute(k, "");
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined) continue;
    node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return node;
};

// link is a button that looks like a link, so that it can be reached with
// the keyboard.
export const link = (text, onclick, attrs = {}) => el("button", { type: "button", class: "link", onclick, ...attrs }, text);

export async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "X-Camsync": "1", "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
    throw new Error(msg);
  }
  return res.json();
}

export function toast(message, kind = "info") {
  const box = $("#toasts");
  const t = el("div", { class: "toast " + kind, role: kind === "error" ? "alert" : "status" }, message);
  const close = () => t.remove();
  t.append(el("button", { type: "button", class: "close", "aria-label": "Dismiss", onclick: close }, "×"));
  box.append(t);
  setTimeout(close, kind === "error" ? 12000 : 5000);
}

// busy disables a button and changes its label while fn runs.
export async function busy(button, label, fn) {
  const old = button.textContent;
  button.disabled = true;
  button.textContent = label;
  try {
    return await fn();
  } finally {
    button.disabled = false;
    button.textContent = old;
  }
}

export const plural = (n, word, many = word + "s") => `${n} ${n === 1 ? word : many}`;
export const baseName = (path) => path.split(/[\\/]/).pop();
export const folderName = (f) => (f.rel === "." ? "(root)" : f.rel);
export const parseTime = (s) => new Date(s.replace(" ", "T") + "Z").getTime();
export const DAY = 86400000;

export function fmtDistance(m) {
  return m < 1000 ? `${Math.round(m)} m` : `${(m / 1000).toFixed(1)} km`;
}

export function distanceMetres(s) {
  const m = /^([\d.]+)\s*(km|m)?$/.exec(s || "");
  if (!m) return 0;
  return parseFloat(m[1]) * (m[2] === "km" ? 1000 : 1);
}

// joinPath puts a slash-separated path relative to root under it, with the
// separator root uses.
export function joinPath(root, rel) {
  const sep = root.includes("\\") && !root.includes("/") ? "\\" : "/";
  if (rel === "" || rel === ".") return root;
  return root.replace(/[\\/]+$/, "") + sep + rel.split("/").join(sep);
}
