"use strict";

const state = {
  user: null,
  libraries: [],
  activeLibraryId: null,
  favorites: new Set(),
  // viewer state
  photos: [],
  photoIndex: 0,
  currentAlbum: null,
  slideshowTimer: null,
  slideshowActive: false,
  infoOpen: false,
};

// ── slideshow settings ──
// fitMode: "fit" (contain, no crop) | "fill" (crop to fill) | "scroll" (fill width, auto-pan)
const SS_DEFAULTS = {
  interval: 3.5, transition: "fade", fitMode: "fit", loop: true, showInfo: false,
  shuffle: false, autostart: false, hideDelay: 3,
};
function loadSlideshowSettings() {
  try {
    const stored = JSON.parse(localStorage.getItem("ss-settings") || "{}");
    // Migrate legacy boolean "stretch" → fitMode.
    if (stored.fitMode === undefined && stored.stretch !== undefined) {
      stored.fitMode = stored.stretch ? "fill" : "fit";
    }
    delete stored.stretch;
    return { ...SS_DEFAULTS, ...stored };
  } catch (_) {
    return { ...SS_DEFAULTS };
  }
}
function saveSlideshowSettings(s) {
  localStorage.setItem("ss-settings", JSON.stringify(s));
  syncPrefsToServer();
}
let ssSettings = loadSlideshowSettings();

// ── general UI preferences ──
// density: thumbnail tile size; sort: default album/collection order.
const PREFS_DEFAULTS = { density: "medium", sort: "name" };
function loadPrefs() {
  try {
    return { ...PREFS_DEFAULTS, ...JSON.parse(localStorage.getItem("ui-prefs") || "{}") };
  } catch (_) {
    return { ...PREFS_DEFAULTS };
  }
}
function savePrefs(p) {
  prefs = p;
  localStorage.setItem("ui-prefs", JSON.stringify(p));
  syncPrefsToServer();
}
let prefs = loadPrefs();

// Maps the density preference to a tile box size (px).
const DENSITY_BOX = { small: 140, medium: 200, large: 280 };
function photoBox() { return DENSITY_BOX[prefs.density] || DENSITY_BOX.medium; }

// ── server-synced preferences ──
// Both UI prefs and slideshow settings are persisted server-side per user so
// they follow the user across devices. localStorage acts as an offline cache.
let _prefsSyncTimer = null;
function syncPrefsToServer() {
  // Debounce rapid changes (e.g. typing in a number field).
  clearTimeout(_prefsSyncTimer);
  _prefsSyncTimer = setTimeout(() => {
    api("/api/preferences", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ui: prefs, slideshow: ssSettings }),
    }).catch(() => { /* offline: localStorage cache still holds the value */ });
  }, 400);
}
async function loadPrefsFromServer() {
  let remote;
  try {
    remote = await api("/api/preferences");
  } catch (_) {
    return; // fall back to localStorage values already loaded
  }
  if (remote && typeof remote === "object") {
    if (remote.ui && typeof remote.ui === "object") {
      prefs = { ...PREFS_DEFAULTS, ...remote.ui };
      localStorage.setItem("ui-prefs", JSON.stringify(prefs));
    }
    if (remote.slideshow && typeof remote.slideshow === "object") {
      ssSettings = { ...SS_DEFAULTS, ...remote.slideshow };
      localStorage.setItem("ss-settings", JSON.stringify(ssSettings));
    }
  }
}

// ── helpers ──
const $ = (sel) => document.querySelector(sel);
const el = (tag, props = {}, children = []) => {
  const node = document.createElement(tag);
  Object.entries(props).forEach(([k, v]) => {
    if (k === "class") node.className = v;
    else if (k === "html") node.innerHTML = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined) node.setAttribute(k, v);
  });
  (Array.isArray(children) ? children : [children]).forEach((c) => {
    if (c == null) return;
    node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  });
  return node;
};
const icon = (name) => `<svg class="icon" viewBox="0 0 24 24"><use href="#i-${name}"/></svg>`;
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
// Custom cover/background art is stored under the internal art dir and flagged
// with an "@art/" sentinel; it is served from /api/art instead of /api/photo.
const ART_PREFIX = "@art/";
const isArtPath = (path) => typeof path === "string" && path.startsWith(ART_PREFIX);
const encodePath = (path) => path.split("/").map(encodeURIComponent).join("/");
const artURL = (path) => "/api/art/" + encodePath(path.slice(ART_PREFIX.length));
const thumbURL = (path) => (isArtPath(path) ? artURL(path) : "/api/thumb/" + encodePath(path));
const photoURL = (path) => (isArtPath(path) ? artURL(path) : "/api/photo/" + encodePath(path));

// ── box-fit tiles (Windows Photos style) ──
// Each tile fits within a square box, preserving the photo's real aspect
// ratio. Wide photos get capped width, tall photos get capped height.
function sizeToBox(tile, img) {
  const box = photoBox();
  const apply = () => {
    const w = img.naturalWidth, h = img.naturalHeight;
    if (!(w > 0 && h > 0)) return;
    if (w >= h) {
      tile.style.width = box + "px";
      tile.style.height = Math.round(box * (h / w)) + "px";
    } else {
      tile.style.height = box + "px";
      tile.style.width = Math.round(box * (w / h)) + "px";
    }
  };
  // Square placeholder until the real ratio is known.
  tile.style.width = box + "px";
  tile.style.height = box + "px";
  if (img.complete && img.naturalWidth) apply();
  else img.addEventListener("load", apply, { once: true });
}

async function api(path, opts = {}) {
  const res = await fetch(path, { credentials: "same-origin", ...opts });
  if (res.status === 401) {
    showLogin();
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  return res.json();
}

// ── auth/bootstrap ──
function showLogin() {
  $("#login-screen").hidden = false;
  $("#app").hidden = true;
}

async function boot() {
  try {
    state.user = await api("/api/me");
  } catch (_) {
    showLogin();
    return;
  }
  $("#login-screen").hidden = true;
  $("#app").hidden = false;

  const initials = (state.user.username || "?").slice(0, 2).toUpperCase();
  $("#avatar").textContent = initials;
  $("#avatar").title = state.user.username;

  if (state.user.isAdmin) {
    $("#admin-section").hidden = false;
    $("#admin-divider").hidden = false;
  }

  await loadPrefsFromServer();
  state.libraries = await api("/api/libraries");
  await loadFavorites();
  renderSidebar();
  // Restore the view from the current URL (deep-link / reload support).
  const initial = history.state || pathToRoute(location.pathname);
  navigate(initial, { fromPopState: true });
}

// Back/forward navigation: re-render the view for the popped history entry.
window.addEventListener("popstate", (e) => {
  if ($("#app").hidden) return;
  const route = e.state || pathToRoute(location.pathname);
  navigate(route, { fromPopState: true });
});

async function loadFavorites() {
  try {
    const favs = await api("/api/favorites");
    state.favorites = new Set(favs.map((a) => a.id));
  } catch (_) {
    state.favorites = new Set();
  }
}

// Toggle favorite status of an album, updating the server and local state.
async function toggleFavorite(albumId) {
  const isFav = state.favorites.has(albumId);
  try {
    await api(`/api/nodes/${albumId}/favorite`, { method: isFav ? "DELETE" : "PUT" });
    if (isFav) state.favorites.delete(albumId);
    else state.favorites.add(albumId);
    return !isFav;
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
    return isFav;
  }
}

function renderSidebar() {
  const container = $("#sidebar-libs");
  container.innerHTML = "";
  if (state.libraries.length === 0) {
    container.appendChild(el("div", { class: "sidebar-item", style: "cursor:default;color:var(--text-hint)", text: t("sidebar.noLibrary") }));
  }
  state.libraries.forEach((lib) => {
    const item = el("div", {
      class: "sidebar-item",
      "data-lib": lib.id,
      html: `${icon("layout-grid")}<span>${esc(lib.name)}</span>`,
      onclick: () => navigate({ view: "library", libraryId: lib.id }),
    });
    container.appendChild(item);
  });
}

function setActiveSidebar(libId, adminRoute, home) {
  document.querySelectorAll(".sidebar-item").forEach((i) => i.classList.remove("active"));
  if (adminRoute) {
    const route = adminRoute === true ? "admin" : adminRoute;
    const a = document.querySelector(`.sidebar-item[data-route="${route}"]`);
    if (a) a.classList.add("active");
  } else if (home) {
    const a = document.querySelector('.sidebar-item[data-route="home"]');
    if (a) a.classList.add("active");
  } else if (libId) {
    const a = document.querySelector(`.sidebar-item[data-lib="${libId}"]`);
    if (a) a.classList.add("active");
  }
}

// ── router ──
// Serialize a route object into a URL path. Extra context (collectionName) is
// kept in history.state so breadcrumbs survive reload/back-forward.
function routeToPath(route) {
  switch (route.view) {
    case "home": return "/";
    case "library": return `/library/${route.libraryId}`;
    case "node": return `/library/${route.libraryId}/node/${route.nodeId}`;
    case "admin": return "/admin";
    case "users": return "/users";
    case "errors": return "/errors";
    case "settings": return "/settings";
    default: return "/";
  }
}

// Parse the current location into a route object. Returns null for the root so
// the caller can fall back to the home view.
function pathToRoute(pathname) {
  const parts = pathname.split("/").filter(Boolean);
  if (parts.length === 0) return { view: "home" };
  if (parts[0] === "admin") return { view: "admin" };
  if (parts[0] === "users") return { view: "users" };
  if (parts[0] === "errors") return { view: "errors" };
  if (parts[0] === "settings") return { view: "settings" };
  if (parts[0] === "library" && parts[1]) {
    const libraryId = decodeURIComponent(parts[1]);
    if (parts[2] === "node" && parts[3]) {
      return { view: "node", libraryId, nodeId: decodeURIComponent(parts[3]) };
    }
    return { view: "library", libraryId };
  }
  return { view: "home" };
}

async function navigate(route, opts = {}) {
  // Reflect the route in the address bar (unless we're restoring from history).
  if (!opts.fromPopState) {
    const path = routeToPath(route);
    if (path !== location.pathname) {
      history.pushState(route, "", path);
    } else {
      history.replaceState(route, "", path);
    }
  }

  const main = $("#main");
  main.innerHTML = `<div class="loading">${esc(t("common.loading"))}</div>`;
  if (route.libraryId) state.activeLibraryId = route.libraryId;
  updateTopnav(route.view);
  try {
    switch (route.view) {
      case "home": setActiveSidebar(null, false, true); await renderHome(main); break;
      case "library": setActiveSidebar(route.libraryId); await renderLibrary(main, route.libraryId); break;
      case "node": await renderNode(main, route); break;
      case "admin": setActiveSidebar(null, true); await renderAdmin(main); break;
      case "users": setActiveSidebar(null, "users"); await renderAdminUsers(main); break;
      case "errors": setActiveSidebar(null, "errors"); await renderErrorLog(main); break;
      case "settings": setActiveSidebar(null); renderSettings(main); break;
    }
  } catch (e) {
    main.innerHTML = `<div class="empty">${esc(t("alert.error", { msg: e.message }))}</div>`;
  }
}

function updateTopnav(view) {
  // The library link is redundant with the breadcrumb / hero parent link, so
  // it is no longer surfaced in the top nav. Home now lives in the sidebar and
  // its active state is managed by setActiveSidebar().
  const libLink = $("#nav-library");
  if (libLink) {
    libLink.hidden = true;
    libLink.classList.remove("active");
  }
}

// ── views ──
async function renderHome(main) {
  main.innerHTML = "";
  main.appendChild(el("div", { class: "section-header", html: `<div><div class="section-title">${esc(t("home.title"))}</div><div class="section-sub">${esc(t("home.libraryCount", { n: state.libraries.length }))}</div></div>` }));

  if (state.libraries.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("home.noLibrary") }));
    return;
  }

  // ── Swimlanes ──
  // Fire every request up front so the page assembles with one round of
  // concurrent fetches instead of 2 + 2N sequential round trips. Each promise
  // is caught individually so one failing library does not break the page.
  const favsP = api("/api/favorites").catch(() => []);
  const recentP = api("/api/recent").catch(() => []);
  const randomPs = state.libraries.map((lib) => api(`/api/libraries/${lib.id}/random-albums`).catch(() => []));
  const nodesPs = state.libraries.map((lib) => api(`/api/libraries/${lib.id}/nodes`).catch(() => []));

  // Favoris
  const favs = await favsP;
  state.favorites = new Set(favs.map((a) => a.id));
  if (favs.length > 0) {
    main.appendChild(swimlane(t("home.favorites"), favs.map((a) => nodeCard(a.libraryId, a, { parent: a.libraryName, poster: true }))));
  }

  // Récemment consultés
  const recent = await recentP;
  if (recent.length > 0) {
    main.appendChild(swimlane(t("home.recent"), recent.map((a) => nodeCard(a.libraryId, a, { parent: a.libraryName, poster: true }))));
  }

  // Suggestions aléatoires — one lane per library
  for (let i = 0; i < state.libraries.length; i++) {
    const lib = state.libraries[i];
    const rnd = await randomPs[i];
    if (rnd.length > 0) {
      main.appendChild(swimlane(t("home.random", { name: lib.name }), rnd.map((a) => nodeCard(a.libraryId, a, { parent: a.libraryName, poster: true }))));
    }
  }

  // ── Top-level nodes grouped by library ──
  for (let i = 0; i < state.libraries.length; i++) {
    const lib = state.libraries[i];
    const block = el("div", { class: "lib-block" });
    block.appendChild(el("div", {
      class: "section-header",
      html: `<div><div class="section-title">${esc(lib.name)}</div><div class="section-sub">${esc(t("home.collectionCount", { n: lib.collectionCount }))}</div></div>`,
    }));
    const grid = el("div", { class: "grid grid--landscape" });
    const nodes = await nodesPs[i];
    nodes.forEach((n) => grid.appendChild(nodeCard(lib.id, n)));
    if (nodes.length === 0) grid.appendChild(el("div", { class: "empty", text: t("home.emptyScan") }));
    block.appendChild(grid);
    main.appendChild(block);
  }
}

async function renderLibrary(main, libraryId) {
  const lib = state.libraries.find((l) => l.id === libraryId);
  const nodes = await api(`/api/libraries/${libraryId}/nodes`);
  main.innerHTML = "";

  const cover = lib?.coverPhoto || nodes.find((c) => c.coverPhoto)?.coverPhoto || "";
  const totalPhotos = nodes.reduce((n, c) => n + (c.photoCount || 0), 0);
  const actions = [];
  if (totalPhotos > 0 || nodes.length > 0) {
    actions.push(el("button", {
      class: "btn accent", html: `${icon("play")} ${esc(t("viewer.slideshow"))}`,
      onclick: () => startLibrarySlideshow(libraryId, lib ? lib.name : t("library.default")),
    }));
  }
  if (state.user.isAdmin) {
    actions.push(el("button", {
      class: "btn icon-btn", html: icon("pen"), title: t("library.editTitle"),
      onclick: () => openEditModal({ type: "library", id: libraryId, name: lib ? lib.name : "", cover: lib?.coverPhoto || "", background: lib?.backgroundPhoto || "", sortTitle: lib?.sortTitle || "", summary: lib?.summary || "" }),
    }));
  }
  main.appendChild(hero(lib ? lib.name : t("library.default"), "", cover, actions, lib?.backgroundPhoto || "", {
    meta: [t("meta.collections", { n: nodes.length }), t("meta.photos", { n: totalPhotos })],
    description: lib?.summary || "",
  }));

  const grid = el("div", { class: "grid grid--landscape" });
  nodes.forEach((c) => grid.appendChild(nodeCard(libraryId, c)));
  if (nodes.length === 0) grid.appendChild(el("div", { class: "empty", text: t("home.emptyScan") }));
  main.appendChild(grid);
}

// ── hero + menu helpers ──
// opts (optional): { meta: [string], description: string, breadcrumb: Node }
function hero(title, sub, coverPath, actions, bgPath, opts) {
  opts = opts || {};
  const node = el("div", { class: "hero" });

  // Full-bleed faded backdrop (prefer dedicated background, fall back to cover).
  const backdrop = el("div", { class: "hero-backdrop" });
  const art = bgPath || coverPath;
  if (art) {
    backdrop.appendChild(el("div", { class: "hero-backdrop-img", style: `background-image:url('${photoURL(art)}')` }));
  }
  backdrop.appendChild(el("div", { class: "hero-backdrop-fade" }));
  node.appendChild(backdrop);

  if (opts.breadcrumb) {
    opts.breadcrumb.classList.add("hero-breadcrumb");
    node.appendChild(opts.breadcrumb);
  }

  const content = el("div", { class: "hero-content" });

  // Poster (left).
  const poster = el("div", { class: "hero-poster" });
  if (coverPath) {
    poster.appendChild(el("img", { src: thumbURL(coverPath), alt: "" }));
  } else {
    poster.classList.add("placeholder");
    poster.innerHTML = icon("image");
  }
  content.appendChild(poster);

  // Info (right): title block pinned to top, actions pinned to bottom.
  const info = el("div", { class: "hero-info" });

  const top = el("div", { class: "hero-info-top" });
  if (opts.parentLink) {
    top.appendChild(el("a", { class: "hero-parent", text: opts.parentLink.text, onclick: opts.parentLink.onclick }));
  }
  top.appendChild(el("div", { class: "hero-title", text: title }));
  if (sub) top.appendChild(el("div", { class: "hero-sub", text: sub }));
  if (opts.meta && opts.meta.length) {
    const meta = el("div", { class: "hero-meta" });
    opts.meta.forEach((m) => meta.appendChild(el("span", { class: "hero-chip", text: m })));
    top.appendChild(meta);
  }
  if (opts.description) {
    top.appendChild(el("div", { class: "hero-desc", text: opts.description }));
  }
  info.appendChild(top);

  if (actions && actions.length) {
    const act = el("div", { class: "hero-actions" });
    actions.forEach((a) => act.appendChild(a));
    info.appendChild(act);
  }

  content.appendChild(info);
  node.appendChild(content);
  return node;
}

function buildMenu(items) {
  const wrap = el("div", { class: "menu-wrap" });
  const btn = el("button", { class: "menu-btn", html: icon("dots"), title: t("common.options") });
  const menu = el("div", { class: "menu", hidden: "" });
  items.forEach((it) => {
    menu.appendChild(el("div", {
      class: "menu-item",
      html: `${icon(it.icon)}<span>${esc(it.label)}</span>`,
      onclick: (e) => { e.stopPropagation(); menu.hidden = true; it.onclick(); },
    }));
  });
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    document.querySelectorAll(".menu").forEach((m) => { if (m !== menu) m.hidden = true; });
    menu.hidden = !menu.hidden;
  });
  wrap.appendChild(btn);
  wrap.appendChild(menu);
  return wrap;
}

// Recursively gather all photos under a node (its own photos plus those of all
// descendant nodes), capped to keep slideshows responsive.
async function collectNodePhotos(nodeId, cap = 1000) {
  let photos = [];
  const stack = [nodeId];
  while (stack.length && photos.length < cap) {
    const id = stack.shift();
    let data;
    try { data = await api(`/api/nodes/${id}`); } catch (_) { continue; }
    photos = photos.concat(data.photos || []);
    for (const c of (data.children || [])) stack.push(c.id);
  }
  return photos;
}

// Fisher–Yates shuffle (returns a new array).
function shuffled(arr) {
  const a = arr.slice();
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [a[i], a[j]] = [a[j], a[i]];
  }
  return a;
}

// maybeShuffle applies the shuffle preference to a photo list for slideshows.
function maybeShuffle(photos) {
  return ssSettings.shuffle ? shuffled(photos) : photos;
}

async function startNodeSlideshow(nodeId, name) {
  let photos = await collectNodePhotos(nodeId);
  if (photos.length === 0) { alert(t("alert.noPhotoCollection")); return; }
  photos = maybeShuffle(photos);
  openViewer(photos, 0, { id: nodeId, name, coverScope: "node" }, true);
}

async function startLibrarySlideshow(libraryId, name) {
  const nodes = await api(`/api/libraries/${libraryId}/nodes`);
  let photos = [];
  for (const c of nodes) {
    photos = photos.concat(await collectNodePhotos(c.id));
    if (photos.length >= 1000) break;
  }
  if (photos.length === 0) { alert(t("alert.noPhotoLibrary")); return; }
  photos = maybeShuffle(photos);
  openViewer(photos, 0, { id: libraryId, name, coverScope: null }, true);
}

// nodeCard renders a single tree node as a clickable card. A node may be a
// collection (has children), an album (has photos), or both. Clicking always
// navigates into the node view, which decides what to show. A favorite (heart)
// toggle is shown for nodes that hold photos (album view).
// opts: { poster: bool (use 3:2 poster layout), parent: string (parent label) }
function nodeCard(libraryId, n, opts = {}) {
  const art = n.backgroundPhoto || n.coverPhoto;
  const thumb = art ? `<img src="${thumbURL(art)}" alt="" loading="lazy">` : icon(n.hasChildren ? "folder" : "photo");
  const isAlbum = (n.photoCount || 0) > 0;
  const isFav = state.favorites.has(n.id);
  const favHtml = isAlbum
    ? `<button class="card-fav${isFav ? " active" : ""}" title="${esc(t("album.fav"))}">${icon(isFav ? "heart-filled" : "heart")}</button>`
    : "";
  const parentHtml = opts.parent ? `<div class="card-parent">${esc(opts.parent)}</div>` : "";

  // Counts line: show sub-collection and/or photo counts as applicable.
  const counts = [];
  if (n.hasChildren || (n.childCount || 0) > 0) counts.push(`<span>${esc(t("card.collectionCount", { n: n.childCount || 0 }))}</span>`);
  counts.push(`<span>${esc(t("meta.photos", { n: n.photoCount || 0 }))}</span>`);

  const card = el("div", {
    class: "card " + (opts.poster ? "card--poster" : "card--landscape"),
    onclick: () => navigate({ view: "node", libraryId, nodeId: n.id }),
    html: `<div class="card-thumb">${thumb}${favHtml}</div>
      <div class="card-body">
        <div class="card-title">${esc(n.name)}</div>
        ${parentHtml}
        <div class="card-counts">${counts.join("")}</div>
      </div>`,
  });
  const favBtn = card.querySelector(".card-fav");
  if (favBtn) {
    favBtn.addEventListener("click", async (e) => {
      e.stopPropagation();
      const nowFav = await toggleFavorite(n.id);
      favBtn.classList.toggle("active", nowFav);
      favBtn.innerHTML = icon(nowFav ? "heart-filled" : "heart");
    });
  }
  return card;
}

// A swimlane: title + horizontally-scrolling row of cards.
function swimlane(title, cards) {
  const block = el("div", { class: "lib-block" });
  block.appendChild(el("div", { class: "section-header", html: `<div><div class="section-title">${esc(title)}</div></div>` }));
  const row = el("div", { class: "row-scroll" });
  cards.forEach((c) => row.appendChild(c));
  block.appendChild(row);
  return block;
}

// renderNode renders a single tree node: sub-collections as a grid on top
// (when present) and the node's own photos below (when present). A node may be
// a collection, an album, or both at once.
// sortItems orders a list of {name, path?} by the user's default sort pref.
// "name" → A→Z by name; "date" → by filename descending (newest-ish first),
// matching how cameras name files chronologically.
function sortItems(items, key) {
  const arr = items.slice();
  if (prefs.sort === "name") {
    arr.sort((a, b) => (a.name || "").localeCompare(b.name || "", undefined, { numeric: true, sensitivity: "base" }));
  } else if (prefs.sort === "nameDesc") {
    arr.sort((a, b) => (b.name || "").localeCompare(a.name || "", undefined, { numeric: true, sensitivity: "base" }));
  }
  return arr;
}

async function renderNode(main, route) {
  const data = await api(`/api/nodes/${route.nodeId}`);
  const node = data.node;
  const children = sortItems(data.children || []);
  const photos = sortItems(data.photos || []);
  const ancestors = data.ancestors || [];
  const lib = state.libraries.find((l) => l.id === route.libraryId) || { id: node.libraryId };
  const libName = lib && lib.name ? lib.name : t("library.default");
  main.innerHTML = "";

  // Breadcrumb: library > ...ancestors... > node.
  const bc = el("div", { class: "breadcrumb" });
  bc.appendChild(el("a", { text: libName, onclick: () => navigate({ view: "library", libraryId: route.libraryId }) }));
  for (const a of ancestors) {
    bc.appendChild(el("span", { html: icon("chevron-right") }));
    bc.appendChild(el("a", { text: a.name, onclick: () => navigate({ view: "node", libraryId: route.libraryId, nodeId: a.id }) }));
  }
  bc.appendChild(el("span", { html: icon("chevron-right") }));
  bc.appendChild(el("span", { text: node.name }));

  // Parent link points to the immediate ancestor (or the library at the top).
  const parent = ancestors.length ? ancestors[ancestors.length - 1] : null;
  const parentLink = parent
    ? { text: parent.name, onclick: () => navigate({ view: "node", libraryId: route.libraryId, nodeId: parent.id }) }
    : { text: libName, onclick: () => navigate({ view: "library", libraryId: route.libraryId }) };

  if (node.favorite) state.favorites.add(node.id); else state.favorites.delete(node.id);

  const hasPhotos = photos.length > 0;
  const actions = [];
  if (hasPhotos || children.length > 0) {
    actions.push(el("button", {
      class: "btn accent", html: `${icon("play")} ${esc(t("viewer.slideshow"))}`,
      onclick: () => hasPhotos && children.length === 0
        ? openViewer(maybeShuffle(photos), 0, node, true)
        : startNodeSlideshow(node.id, node.name),
    }));
  }
  if (hasPhotos) {
    const favBtn = el("button", {
      class: "btn icon-btn" + (state.favorites.has(node.id) ? " accent" : ""),
      title: t("album.fav"),
      html: icon(state.favorites.has(node.id) ? "heart-filled" : "heart"),
    });
    favBtn.addEventListener("click", async () => {
      const nowFav = await toggleFavorite(node.id);
      favBtn.classList.toggle("accent", nowFav);
      favBtn.innerHTML = icon(nowFav ? "heart-filled" : "heart");
    });
    actions.push(favBtn);
  }
  if (state.user.isAdmin) {
    actions.push(el("button", {
      class: "btn icon-btn", html: icon("pen"), title: t("node.editTitle"),
      onclick: () => openEditModal({ type: "node", id: node.id, libraryId: route.libraryId, name: node.name, cover: node.coverPhoto || "", background: node.backgroundPhoto || "", sortTitle: node.sortTitle || "", summary: node.summary || "", contentRating: node.contentRating || "", year: node.year || "", studio: node.studio || "", folderPath: node.folderPath || "" }),
    }));
  }

  // Split children by their derived type: leaf folders with photos render as
  // poster "albums", everything else as landscape "collections".
  const childCollections = children.filter((c) => c.type !== "album");
  const childAlbums = children.filter((c) => c.type === "album");

  const meta = [];
  if (childCollections.length > 0) meta.push(t("meta.collections", { n: childCollections.length }));
  if (childAlbums.length > 0) meta.push(t("meta.albums", { n: childAlbums.length }));
  meta.push(t("meta.photos", { n: photos.length }));
  if (node.year) meta.push(node.year);
  if (node.contentRating) meta.push(node.contentRating);
  if (node.studio) meta.push(node.studio);
  main.appendChild(hero(node.name, "", node.coverPhoto || "", actions, node.backgroundPhoto || "", {
    meta,
    description: node.summary || "",
    breadcrumb: bc,
    parentLink,
  }));

  // Sub-collections (children with their own sub-folders) as landscape cards.
  if (childCollections.length > 0) {
    const block = el("div", { class: "lib-block" });
    block.appendChild(el("div", { class: "section-header", html: `<div><div class="section-title">${esc(t("node.collections"))}</div></div>` }));
    const cgrid = el("div", { class: "grid grid--landscape" });
    childCollections.forEach((c) => {
      if (c.favorite) state.favorites.add(c.id);
      cgrid.appendChild(nodeCard(route.libraryId, c));
    });
    block.appendChild(cgrid);
    main.appendChild(block);
  }

  // Leaf albums (children holding photos) as poster cards.
  if (childAlbums.length > 0) {
    const block = el("div", { class: "lib-block" });
    block.appendChild(el("div", { class: "section-header", html: `<div><div class="section-title">${esc(t("node.albums"))}</div></div>` }));
    const agrid = el("div", { class: "grid" });
    childAlbums.forEach((c) => {
      if (c.favorite) state.favorites.add(c.id);
      agrid.appendChild(nodeCard(route.libraryId, c, { poster: true, parent: node.name }));
    });
    block.appendChild(agrid);
    main.appendChild(block);
  }

  // The node's own photos below.
  if (hasPhotos) {
    if (childCollections.length > 0 || childAlbums.length > 0) {
      main.appendChild(el("div", { class: "section-header", html: `<div><div class="section-title">${esc(t("node.photos"))}</div></div>` }));
    }
    const grid = el("div", { class: "photo-grid justified" });
    state.currentAlbum = node;
    photos.forEach((p, i) => {
      const isCover = node.coverPhoto && node.coverPhoto === p.path;
      const img = el("img", { src: thumbURL(p.path), alt: esc(p.name), loading: "lazy" });
      const tile = el("div", {
        class: "photo-thumb" + (isCover ? " cover" : ""),
        onclick: () => openViewer(photos, i, node, false),
      });
      tile.appendChild(img);
      sizeToBox(tile, img);
      grid.appendChild(tile);
    });
    main.appendChild(grid);
  }

  if (children.length === 0 && !hasPhotos) {
    main.appendChild(el("div", { class: "empty", text: t("node.empty") }));
  }

  if (state.user.isAdmin && hasPhotos) {
    main.appendChild(el("div", { class: "admin-note", html: `${icon("star")} ${esc(t("album.adminCoverNote"))}`, title: t("album.adminCoverTitle") }));
  }

  // Auto-start the slideshow when opening an album, if the user opted in.
  // Only for leaf albums (photos, no sub-collections) and not when returning
  // to an already-open viewer.
  if (ssSettings.autostart && hasPhotos && children.length === 0 && $("#viewer").hidden) {
    openViewer(maybeShuffle(photos), 0, node, true);
  }
}

// ── viewer ──
function openViewer(photos, index, album, slideshow) {
  state.photos = photos;
  state.photoIndex = index;
  state.currentAlbum = album;
  $("#viewer").hidden = false;
  // Cover button only when viewing a real album (not collection/library slideshows).
  $("#viewer-cover").hidden = !state.user.isAdmin || !(album && album.id && album.coverScope === undefined);
  updateViewer();
  if (slideshow) startSlideshow(); else applyOverlay();
}

function updateViewer() {
  const p = state.photos[state.photoIndex];
  if (!p) return;
  const img = $("#viewer-img");
  img.classList.remove("fade", "slide", "stretch", "scroll", "fit");
  img.style.removeProperty("--scroll-from");
  img.style.removeProperty("--scroll-to");
  img.style.removeProperty("--scroll-dur");
  // re-trigger transition animation
  void img.offsetWidth;
  if (state.slideshowActive && ssSettings.transition !== "none") {
    img.classList.add(ssSettings.transition);
  }
  // Photo fit mode (only active during slideshow; manual viewing always uses "fit").
  const mode = state.slideshowActive ? ssSettings.fitMode : "fit";
  if (mode === "fill") img.classList.add("stretch");
  else if (mode === "scroll") img.classList.add("scroll");
  else if (mode === "fit") img.classList.add("fit");
  img.src = photoURL(p.path);
  if (mode === "scroll") setupAutoScroll(img);
  $("#viewer-caption").textContent = `${p.name}  (${state.photoIndex + 1}/${state.photos.length})`;
  if (state.infoOpen) loadInfo();
  applyOverlay();
}

// Auto-scroll: fill one screen dimension, then pan across the overflowing
// dimension over the slideshow interval so the cropped part is revealed.
function setupAutoScroll(img) {
  const apply = () => {
    const vw = window.innerWidth, vh = window.innerHeight;
    const iw = img.naturalWidth || vw, ih = img.naturalHeight || vh;
    const imgRatio = iw / ih, screenRatio = vw / vh;
    // Pan along whichever dimension overflows after covering the screen.
    const vertical = imgRatio < screenRatio; // tall/portrait → scroll up/down
    const dur = Math.max(1, ssSettings.interval);
    if (vertical) {
      const overflow = Math.max(0, ih * (vw / iw) - vh);
      img.style.setProperty("--scroll-from", "translateY(0)");
      img.style.setProperty("--scroll-to", `translateY(-${overflow}px)`);
    } else {
      const overflow = Math.max(0, iw * (vh / ih) - vw);
      img.style.setProperty("--scroll-from", "translateX(0)");
      img.style.setProperty("--scroll-to", `translateX(-${overflow}px)`);
    }
    img.style.setProperty("--scroll-dur", `${dur}s`);
    img.classList.remove("scroll-run");
    void img.offsetWidth;
    img.classList.add("scroll-run");
  };
  if (img.complete && img.naturalWidth) apply();
  else img.addEventListener("load", apply, { once: true });
}

function applyOverlay() {
  const overlay = $("#viewer-overlay");
  if (state.slideshowActive && ssSettings.showInfo) {
    overlay.hidden = false;
    overlay.textContent = "…";
    const p = state.photos[state.photoIndex];
    fetchExif(p.path).then((info) => {
      const bits = [];
      if (info && info.dateTaken) bits.push(info.dateTaken);
      if (info && info.camera) bits.push(info.camera);
      overlay.textContent = bits.length ? bits.join(" · ") : p.name;
    }).catch(() => { overlay.textContent = p.name; });
  } else {
    overlay.hidden = true;
  }
}

function viewerStep(delta) {
  const n = state.photos.length;
  const next = state.photoIndex + delta;
  if (state.slideshowActive && !ssSettings.loop && (next < 0 || next >= n)) {
    stopSlideshow();
    return;
  }
  state.photoIndex = (next + n) % n;
  updateViewer();
}

function closeViewer() {
  stopSlideshow();
  state.infoOpen = false;
  $("#viewer-info").hidden = true;
  $("#viewer").hidden = true;
  $("#viewer-img").src = "";
  $("#viewer-overlay").hidden = true;
}

// ── slideshow controls auto-hide ──
let _hideControlsTimer = null;
function scheduleHideControls() {
  clearTimeout(_hideControlsTimer);
  $("#viewer").classList.remove("controls-hidden");
  if (!state.slideshowActive) return;
  _hideControlsTimer = setTimeout(() => {
    if (state.slideshowActive) $("#viewer").classList.add("controls-hidden");
  }, Math.max(1, ssSettings.hideDelay || 3) * 1000);
}
function cancelHideControls() {
  clearTimeout(_hideControlsTimer);
  _hideControlsTimer = null;
  $("#viewer").classList.remove("controls-hidden");
}
$("#viewer").addEventListener("mousemove", () => { if (state.slideshowActive) scheduleHideControls(); });
$("#viewer").addEventListener("mouseleave", () => { if (state.slideshowActive) scheduleHideControls(); });

function startSlideshow() {
  stopSlideshow();
  state.slideshowActive = true;
  $("#viewer-slideshow").innerHTML = `${icon("play")} ${esc(t("viewer.pause"))}`;
  updateViewer();
  state.slideshowTimer = setInterval(() => viewerStep(1), Math.max(1, ssSettings.interval) * 1000);
  scheduleHideControls();
}
function stopSlideshow() {
  if (state.slideshowTimer) { clearInterval(state.slideshowTimer); state.slideshowTimer = null; }
  state.slideshowActive = false;
  cancelHideControls();
  $("#viewer-slideshow").innerHTML = `${icon("play")} ${esc(t("viewer.slideshow"))}`;
  const img = $("#viewer-img");
  img.classList.remove("fade", "slide", "stretch", "scroll", "scroll-run", "fit");
  img.style.removeProperty("--scroll-from");
  img.style.removeProperty("--scroll-to");
  img.style.removeProperty("--scroll-dur");
  $("#viewer-overlay").hidden = true;
}

// ── EXIF / info panel ──
const exifCache = new Map();
async function fetchExif(path) {
  if (exifCache.has(path)) return exifCache.get(path);
  const url = "/api/exif/" + path.split("/").map(encodeURIComponent).join("/");
  const info = await api(url);
  exifCache.set(path, info);
  return info;
}

function toggleInfo() {
  state.infoOpen = !state.infoOpen;
  $("#viewer-info").hidden = !state.infoOpen;
  $("#viewer-info-btn").classList.toggle("accent", state.infoOpen);
  if (state.infoOpen) loadInfo();
}

async function loadInfo() {
  const p = state.photos[state.photoIndex];
  if (!p) return;
  const body = $("#viewer-info-body");
  body.innerHTML = `<div class="loading">${esc(t("common.loading"))}</div>`;
  try {
    const info = await fetchExif(p.path);
    body.innerHTML = "";
    const rows = [
      [t("info.name"), p.name],
      [t("info.date"), info.dateTaken],
      [t("info.camera"), info.camera],
      [t("info.lens"), info.lens],
      [t("info.dimensions"), info.width && info.height ? `${info.width} × ${info.height}` : ""],
      [t("info.exposure"), info.exposure],
      [t("info.aperture"), info.aperture],
      [t("info.iso"), info.iso],
      [t("info.focalLength"), info.focalLength],
      [t("info.gps"), info.gps],
    ];
    let any = false;
    rows.forEach(([k, v]) => {
      if (!v) return;
      any = true;
      body.appendChild(el("div", { class: "info-row", html: `<span class="info-key">${esc(k)}</span><span class="info-val">${esc(v)}</span>` }));
    });
    if (!any) body.appendChild(el("div", { class: "empty", text: t("info.noExif") }));
  } catch (e) {
    body.innerHTML = `<div class="empty">${esc(t("alert.error", { msg: e.message }))}</div>`;
  }
}

async function setCurrentAsCover() {
  const p = state.photos[state.photoIndex];
  if (!p || !state.currentAlbum) return;
  try {
    await api("/api/cover", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: "node", id: state.currentAlbum.id, photo: p.path }),
    });
    state.currentAlbum.coverPhoto = p.path;
    $("#viewer-cover").innerHTML = `${icon("star")} ${esc(t("viewer.coverSet"))}`;
    setTimeout(() => { $("#viewer-cover").innerHTML = `${icon("star")} ${esc(t("viewer.setCover"))}`; }, 1500);
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── slideshow settings modal ──
function openSsModal() {
  ssSettings = loadSlideshowSettings();
  $("#ss-interval").value = ssSettings.interval;
  $("#ss-transition").value = ssSettings.transition;
  $("#ss-fit").value = ssSettings.fitMode;
  $("#ss-loop").checked = ssSettings.loop;
  $("#ss-showinfo").checked = ssSettings.showInfo;
  $("#ss-modal").hidden = false;
}
function closeSsModal() { $("#ss-modal").hidden = true; }
function saveSs() {
  // Preserve fields that aren't in this modal (shuffle, autostart, hideDelay).
  ssSettings = {
    ...ssSettings,
    interval: parseFloat($("#ss-interval").value) || SS_DEFAULTS.interval,
    transition: $("#ss-transition").value,
    fitMode: $("#ss-fit").value,
    loop: $("#ss-loop").checked,
    showInfo: $("#ss-showinfo").checked,
  };
  saveSlideshowSettings(ssSettings);
  closeSsModal();
  if (!$("#viewer").hidden && state.slideshowActive) startSlideshow();
}

// ── entity edit modal (rename collection / library) ──
let editingEntity = null;
let editSelectedCover = null;
let editSelectedBg = null;

// Upload a custom poster/background (file or URL) to the current entity.
async function uploadArt({ kind, file, url }) {
  if (!editingEntity) throw new Error(t("alert.noEntity"));
  const fd = new FormData();
  fd.append("target", editingEntity.type);
  fd.append("id", editingEntity.id);
  fd.append("kind", kind);
  if (file) fd.append("file", file);
  if (url) fd.append("url", url);
  return api("/api/art", { method: "POST", body: fd });
}

async function handleArtSource(kind, src) {
  const overlayBtn = kind === "background" ? $("#edit-bg-choose") : $("#edit-poster-choose");
  const prev = overlayBtn ? overlayBtn.textContent : "";
  if (overlayBtn) { overlayBtn.textContent = t("art.sending"); overlayBtn.disabled = true; }
  try {
    const res = await uploadArt({ kind, ...src });
    const newPath = res && res.photo;
    if (kind === "background") {
      editSelectedBg = newPath;
      editingEntity.background = newPath;
    } else {
      editSelectedCover = newPath;
      editingEntity.cover = newPath;
    }
    await reloadArtGrids();
  } catch (e) {
    alert(t("alert.uploadFailed", { msg: e.message }));
  } finally {
    if (overlayBtn) { overlayBtn.textContent = prev; overlayBtn.disabled = false; }
  }
}

// Re-fetch the entity photos and rebuild both art grids, preserving selection.
async function reloadArtGrids() {
  const photos = await loadEntityPhotos(editingEntity);
  fillArtGrid($("#edit-poster-grid"), photos, editSelectedCover || editingEntity.cover, false);
  fillArtGrid($("#edit-bg-grid"), photos, editSelectedBg || editingEntity.background, true);
}

function artItem(photoPath, currentCover, isBg) {
  const item = el("div", { class: "plex-edit-art-item" + (isBg ? " bg" : "") + (photoPath === currentCover ? " selected" : "") });
  item.appendChild(el("img", { src: thumbURL(photoPath), alt: "", loading: "lazy" }));
  const check = el("div", { class: "plex-edit-art-check", html: icon("check") });
  item.appendChild(check);
  item.addEventListener("click", () => {
    item.closest(".plex-edit-art-grid").querySelectorAll(".plex-edit-art-item").forEach((i) => i.classList.remove("selected"));
    item.classList.add("selected");
    if (isBg) editSelectedBg = photoPath; else editSelectedCover = photoPath;
  });
  return item;
}

function fillArtGrid(gridEl, photos, currentSel, isBg) {
  gridEl.innerHTML = "";
  const list = (photos || []).slice();
  // Surface the current cover/background as its own tile when it isn't already
  // part of the listed photos. This covers both custom uploads (@art/...) and
  // the default auto-generated art that containers (library/collection) carry,
  // so the user can always see and re-select it — or replace it. Kept per-grid
  // so a background never appears in the poster grid and vice-versa.
  if (currentSel && !list.some((x) => (x.path || x) === currentSel)) {
    list.unshift({ name: isArtPath(currentSel) ? t("art.custom") : t("art.current"), path: currentSel });
  }
  if (list.length === 0) {
    gridEl.innerHTML = `<div class="plex-edit-art-empty">${icon("photo")}<span>${esc(t("art.none"))}</span></div>`;
    return;
  }
  list.forEach((p) => gridEl.appendChild(artItem(p.path || p, currentSel, isBg)));
}

// loadEntityPhotos returns the photos used to populate the poster/background
// grids. Only albums list their own photos here; libraries and collections are
// containers, so (like Plex) their art grids start empty and only show the
// currently-set cover/background plus any custom-uploaded art. This avoids
// dumping every photo in the library into the picker.
async function loadEntityPhotos(entity) {
  let photos = entity.photos || [];
  if (photos.length === 0 && entity.type === "node" && entity.id) {
    try {
      const ad = await api(`/api/nodes/${entity.id}`);
      photos = ad.photos || [];
    } catch (_) {}
  }
  return photos;
}

async function openEditModal(entity) {
  editingEntity = entity;
  editSelectedCover = entity.cover || null;
  editSelectedBg = entity.background || null;
  const titles = { library: t("edit.library"), node: t("edit.node") };
  $("#edit-modal-title").textContent = titles[entity.type] || t("edit.title");
  $("#edit-name").value = entity.name || "";
  $("#edit-sort-title").value = entity.sortTitle || entity.name || "";
  // Nodes can't be renamed (name = folder name on disk); libraries can.
  const nameRow = $("#edit-name").closest(".form-row") || $("#edit-name").parentElement;
  if (nameRow) nameRow.hidden = entity.type === "node";

  // Summary applies to all entity types.
  $("#edit-field-summary").hidden = false;
  $("#edit-summary").value = entity.summary || "";
  // Folder path / year / rating / studio apply to nodes only.
  const hasMeta = entity.type === "node";
  $("#edit-field-folder").hidden = !hasMeta;
  $("#edit-field-meta").hidden = !hasMeta;
  if (hasMeta) {
    $("#edit-folder-path").value = entity.folderPath || "";
    $("#edit-year").value = entity.year || "";
    $("#edit-content-rating").value = entity.contentRating || "";
    $("#edit-studio").value = entity.studio || "";
  }
  switchEditTab("general");

  const photos = await loadEntityPhotos(entity);
  fillArtGrid($("#edit-poster-grid"), photos, editSelectedCover, false);
  fillArtGrid($("#edit-bg-grid"), photos, editSelectedBg, true);

  $("#edit-modal").hidden = false;
}

function closeEditModal() { $("#edit-modal").hidden = true; }

function switchEditTab(name) {
  document.querySelectorAll(".plex-edit-tab").forEach((t) => t.classList.toggle("active", t.dataset.tab === name));
  document.querySelectorAll(".plex-edit-pane").forEach((p) => p.classList.toggle("active", p.id === `edit-pane-${name}`));
}
async function saveEdit() {
  if (!editingEntity) return;
  const name = $("#edit-name").value.trim();
  // Libraries are renamable and require a name; nodes mirror their folder name.
  if (editingEntity.type === "library" && !name) { alert(t("alert.nameRequired")); return; }
  try {
    if (editingEntity.type === "library") {
      const lib = state.libraries.find((l) => l.id === editingEntity.id);
      await api(`/api/admin/libraries/${editingEntity.id}`, {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name,
          rootPath: lib ? lib.rootPath : "",
          whitelist: lib ? lib.whitelist : [],
          sortTitle: $("#edit-sort-title").value.trim(),
          summary: $("#edit-summary").value.trim(),
        }),
      });
      state.libraries = await api("/api/libraries");
      renderSidebar();
    } else if (editingEntity.type === "node") {
      await api(`/api/admin/nodes/${editingEntity.id}`, {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          sortTitle: $("#edit-sort-title").value.trim(),
          summary: $("#edit-summary").value.trim(),
          contentRating: $("#edit-content-rating").value.trim(),
          year: $("#edit-year").value.trim(),
          studio: $("#edit-studio").value.trim(),
        }),
      });
    }
    // Save cover if changed (uploads already persisted server-side; this
    // covers picking an existing photo from the grid).
    if (editSelectedCover && editSelectedCover !== editingEntity.cover) {
      try {
        await api("/api/cover", {
          method: "PUT", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ target: editingEntity.type, id: editingEntity.id, photo: editSelectedCover, kind: "cover" }),
        });
      } catch (_) {}
    }
    // Save background if changed
    if (editSelectedBg && editSelectedBg !== editingEntity.background) {
      try {
        await api("/api/cover", {
          method: "PUT", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ target: editingEntity.type, id: editingEntity.id, photo: editSelectedBg, kind: "background" }),
        });
      } catch (_) {}
    }
    closeEditModal();
    if (editingEntity.type === "library") {
      navigate({ view: "library", libraryId: editingEntity.id });
    } else {
      navigate({ view: "node", libraryId: editingEntity.libraryId, nodeId: editingEntity.id });
    }
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── admin: auto-scan settings ──
async function buildAutoScanPanel() {
  let current = 0;
  try {
    const s = await api("/api/admin/settings");
    current = s.autoScanIntervalHours || 0;
  } catch (e) { /* fall back to 0 */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.autoScan"))}</div><div class="lib-path">${esc(t("admin.autoScanHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const select = el("select", { class: "btn sm" });
  const options = [
    [0, t("admin.autoScanOff")],
    [8, t("admin.autoScanEvery", { n: 8 })],
    [24, t("admin.autoScanEvery", { n: 24 })],
    [48, t("admin.autoScanEvery", { n: 48 })],
  ];
  options.forEach(([val, label]) => {
    const opt = el("option", { text: label });
    opt.value = String(val);
    if (val === current) opt.selected = true;
    select.appendChild(opt);
  });
  select.addEventListener("change", async (ev) => {
    const hours = parseInt(ev.target.value, 10) || 0;
    try {
      await api("/api/admin/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ autoScanIntervalHours: hours }),
      });
    } catch (e) {
      alert(t("alert.error", { msg: e.message }));
    }
  });
  actions.appendChild(select);
  panel.appendChild(actions);
  return panel;
}

// ── admin ──
async function renderAdmin(main) {
  const libs = await api("/api/admin/libraries");
  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("admin.title"))}</div><div class="section-sub">${esc(t("admin.subtitle"))}</div>` }));
  header.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("admin.newLibrary"))}`, onclick: () => openLibModal(null) }));
  main.appendChild(header);

  main.appendChild(await buildAutoScanPanel());

  if (libs.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("admin.noLibrary") }));
    return;
  }

  libs.forEach((lib) => {
    const row = el("div", { class: "lib-row" });
    row.appendChild(el("div", {
      html: `<div class="lib-name">${esc(lib.name)}</div><div class="lib-path">${esc(lib.rootPath)} &nbsp;·&nbsp; ${esc((lib.whitelist || []).join(", ") || "—")}</div>`,
    }));
    const actions = el("div", { class: "lib-actions" });
    actions.appendChild(el("span", { class: "pill", text: t("meta.collections", { n: lib.collectionCount }) }));
    actions.appendChild(el("button", { class: "btn sm", html: `${icon("refresh")} ${esc(t("admin.scan"))}`, onclick: (ev) => scanLibrary(lib.id, ev.currentTarget) }));
    actions.appendChild(el("button", { class: "btn sm", html: icon("edit"), onclick: () => openLibModal(lib) }));
    actions.appendChild(el("button", { class: "btn sm danger", html: icon("trash"), onclick: () => deleteLibrary(lib) }));
    row.appendChild(actions);
    main.appendChild(row);
  });
}

// ── admin: users / library access ──
let adminLibsCache = [];

async function renderAdminUsers(main) {
  const [users, libs] = await Promise.all([
    api("/api/admin/users"),
    api("/api/admin/libraries"),
  ]);
  adminLibsCache = libs;
  const libNames = {};
  libs.forEach((l) => { libNames[l.id] = l.name; });

  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("users.title"))}</div><div class="section-sub">${esc(t("users.subtitle"))}</div>` }));
  header.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("users.add"))}`, onclick: () => openUserModal(null) }));
  main.appendChild(header);

  if (!users || users.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("users.none") }));
    return;
  }

  users.forEach((u) => {
    const row = el("div", { class: "lib-row" });
    const libList = (u.libraryIds || []).map((id) => libNames[id] || id);
    const adminBadge = u.isAdmin ? ` <span class="pill pill-admin">${esc(t("users.admin"))}</span>` : "";
    const access = u.isAdmin
      ? esc(t("users.allLibraries"))
      : (libList.length ? esc(libList.join(", ")) : esc(t("users.noAccess")));
    row.appendChild(el("div", {
      html: `<div class="lib-name">${esc(u.username)}${adminBadge}</div><div class="lib-path">${access}</div>`,
    }));
    const actions = el("div", { class: "lib-actions" });
    if (!u.isAdmin) {
      actions.appendChild(el("button", { class: "btn sm", html: `${icon("edit")} ${esc(t("users.editAccess"))}`, onclick: () => openUserModal(u) }));
      actions.appendChild(el("button", { class: "btn sm danger", html: icon("trash"), onclick: () => deleteUser(u) }));
    } else {
      actions.appendChild(el("span", { class: "pill", text: t("users.allLibraries") }));
    }
    row.appendChild(actions);
    main.appendChild(row);
  });
}

// ── admin: scan error log ──
async function renderErrorLog(main) {
  const errors = await api("/api/admin/errors");
  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("errors.title"))}</div><div class="section-sub">${esc(t("errors.subtitle"))}</div>` }));
  if (errors && errors.length) {
    header.appendChild(el("button", { class: "btn", html: `${icon("trash")} ${esc(t("errors.clear"))}`, onclick: () => clearErrorLog() }));
  }
  main.appendChild(header);

  if (!errors || errors.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("errors.none") }));
    return;
  }

  errors.forEach((e) => {
    const when = e.occurredAt ? new Date(e.occurredAt).toLocaleString() : "";
    const lib = e.libraryName || e.libraryId || "—";
    const row = el("div", { class: "lib-row" });
    row.appendChild(el("div", {
      html: `<div class="lib-name">${esc(lib)} <span class="pill">${esc(e.source || "")}</span></div>` +
        `<div class="lib-path err-msg">${esc(e.message)}</div>`,
    }));
    row.appendChild(el("div", { class: "lib-actions" }, [
      el("span", { class: "section-sub", text: when }),
    ]));
    main.appendChild(row);
  });
}

async function clearErrorLog() {
  if (!confirm(t("errors.confirmClear"))) return;
  try {
    await api("/api/admin/errors", { method: "DELETE" });
    await renderErrorLog($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

let editingUsername = null;
function openUserModal(user) {
  editingUsername = user ? user.username : null;
  $("#user-modal-title").textContent = user ? t("users.editAccessTitle") : t("users.new");
  const nameInput = $("#user-username");
  nameInput.value = user ? user.username : "";
  nameInput.disabled = !!user;
  const granted = new Set(user ? (user.libraryIds || []) : []);

  const container = $("#user-libs");
  container.innerHTML = "";
  if (!adminLibsCache.length) {
    container.appendChild(el("div", { class: "field-hint", text: t("users.noLibrariesYet") }));
  }
  adminLibsCache.forEach((lib) => {
    const label = el("label", { class: "lib-check" });
    const cb = el("input", { type: "checkbox", value: lib.id });
    cb.checked = granted.has(lib.id);
    label.appendChild(cb);
    label.appendChild(el("span", { text: lib.name }));
    container.appendChild(label);
  });
  $("#user-modal").hidden = false;
}
function closeUserModal() { $("#user-modal").hidden = true; }

async function saveUser() {
  const username = $("#user-username").value.trim();
  if (!username) { alert(t("users.usernameRequired")); return; }
  const libraryIds = Array.from($("#user-libs").querySelectorAll("input[type=checkbox]:checked")).map((c) => c.value);
  try {
    if (editingUsername) {
      await api(`/api/admin/users/${encodeURIComponent(editingUsername)}`, {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ libraryIds }),
      });
    } else {
      await api("/api/admin/users", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, libraryIds }),
      });
    }
    closeUserModal();
    await renderAdminUsers($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function deleteUser(user) {
  if (!confirm(t("users.confirmDelete", { name: user.username }))) return;
  try {
    await api(`/api/admin/users/${encodeURIComponent(user.username)}`, { method: "DELETE" });
    await renderAdminUsers($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

function renderSettings(main) {
  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  const ver = (state.user && state.user.version) || "dev";
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("settings.title"))}</div><div class="section-sub">${esc(t("settings.subtitle"))} · ${esc(ver)}</div>` }));
  main.appendChild(header);

  const group = el("div", { class: "settings-group" });
  const row = el("div", { class: "lib-row" });
  row.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("settings.language"))}</div><div class="lib-path">${esc(t("settings.language.desc"))}</div>`,
  }));

  const select = el("select", { class: "lang-select" });
  [["en", "English"], ["fr", "Français"]].forEach(([val, label]) => {
    select.appendChild(el("option", { value: val, text: label }));
  });
  select.value = getLang();
  select.addEventListener("change", () => {
    setLang(select.value);
    renderSidebar();
    navigate({ view: "settings" });
  });

  const actions = el("div", { class: "lib-actions" });
  actions.appendChild(select);
  row.appendChild(actions);
  group.appendChild(row);

  const generalRow = (nameKey, descKey, control) => {
    const r = el("div", { class: "lib-row" });
    r.appendChild(el("div", {
      html: `<div class="lib-name">${esc(t(nameKey))}</div><div class="lib-path">${esc(t(descKey))}</div>`,
    }));
    r.appendChild(el("div", { class: "lib-actions" }, [control]));
    group.appendChild(r);
  };

  const updatePref = (key, value) => {
    savePrefs({ ...prefs, [key]: value });
    navigate({ view: "settings" });
  };

  const density = el("select", { class: "lang-select" });
  [["small", t("settings.density.small")], ["medium", t("settings.density.medium")], ["large", t("settings.density.large")]].forEach(([val, label]) => {
    density.appendChild(el("option", { value: val, text: label }));
  });
  density.value = prefs.density;
  density.addEventListener("change", () => updatePref("density", density.value));
  generalRow("settings.density", "settings.density.desc", density);

  const sort = el("select", { class: "lang-select" });
  [["name", t("settings.sort.name")], ["nameDesc", t("settings.sort.nameDesc")]].forEach(([val, label]) => {
    sort.appendChild(el("option", { value: val, text: label }));
  });
  sort.value = prefs.sort;
  sort.addEventListener("change", () => updatePref("sort", sort.value));
  generalRow("settings.sort", "settings.sort.desc", sort);

  main.appendChild(group);

  renderSlideshowSettings(main);
}

// Slideshow defaults shown on the Settings page. These share the same
// "ss-settings" localStorage as the in-viewer settings modal, so changes
// stay in sync in both places.
function renderSlideshowSettings(main) {
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("settings.slideshow"))}</div>` }));
  main.appendChild(header);

  ssSettings = loadSlideshowSettings();

  const updateSetting = (key, value) => {
    ssSettings = { ...ssSettings, [key]: value };
    saveSlideshowSettings(ssSettings);
    if (!$("#viewer").hidden && state.slideshowActive) startSlideshow();
  };

  const group = el("div", { class: "settings-group" });

  const settingRow = (nameKey, descKey, control) => {
    const r = el("div", { class: "lib-row" });
    r.appendChild(el("div", {
      html: `<div class="lib-name">${esc(t(nameKey))}</div><div class="lib-path">${esc(t(descKey))}</div>`,
    }));
    r.appendChild(el("div", { class: "lib-actions" }, [control]));
    group.appendChild(r);
  };

  const interval = el("input", { type: "number", min: "1", max: "60", step: "0.5", value: ssSettings.interval });
  interval.addEventListener("change", () => {
    const v = parseFloat(interval.value) || SS_DEFAULTS.interval;
    interval.value = v;
    updateSetting("interval", v);
  });
  settingRow("ss.duration", "settings.slideshow.duration.desc", interval);

  const transition = el("select", { class: "lang-select" });
  [["none", t("ss.transition.none")], ["fade", t("ss.transition.fade")], ["slide", t("ss.transition.slide")]].forEach(([val, label]) => {
    transition.appendChild(el("option", { value: val, text: label }));
  });
  transition.value = ssSettings.transition;
  transition.addEventListener("change", () => updateSetting("transition", transition.value));
  settingRow("ss.transition", "settings.slideshow.transition.desc", transition);

  const fit = el("select", { class: "lang-select" });
  [["fit", t("ss.fit.fit")], ["fill", t("ss.fit.fill")], ["scroll", t("ss.fit.scroll")]].forEach(([val, label]) => {
    fit.appendChild(el("option", { value: val, text: label }));
  });
  fit.value = ssSettings.fitMode;
  fit.addEventListener("change", () => updateSetting("fitMode", fit.value));
  settingRow("ss.fit", "settings.slideshow.fit.desc", fit);

  const toggle = (key) => {
    const cb = el("input", { type: "checkbox" });
    cb.checked = !!ssSettings[key];
    cb.addEventListener("change", () => updateSetting(key, cb.checked));
    return cb;
  };
  settingRow("ss.loop", "settings.slideshow.loop.desc", toggle("loop"));
  settingRow("ss.showInfo", "settings.slideshow.showInfo.desc", toggle("showInfo"));
  settingRow("settings.slideshow.shuffle", "settings.slideshow.shuffle.desc", toggle("shuffle"));
  settingRow("settings.slideshow.autostart", "settings.slideshow.autostart.desc", toggle("autostart"));

  const hideDelay = el("input", { type: "number", min: "1", max: "30", step: "1", value: ssSettings.hideDelay });
  hideDelay.addEventListener("change", () => {
    const v = parseInt(hideDelay.value, 10) || SS_DEFAULTS.hideDelay;
    hideDelay.value = v;
    updateSetting("hideDelay", v);
  });
  settingRow("settings.slideshow.hideDelay", "settings.slideshow.hideDelay.desc", hideDelay);

  main.appendChild(group);
}

async function scanLibrary(id, btn) {
  const original = btn.innerHTML;
  btn.innerHTML = `${icon("refresh")} …`;
  btn.disabled = true;
  try {
    await api(`/api/admin/libraries/${id}/scan`, { method: "POST" });
    showScanBanner();
    await pollScanProgress(id);
    hideScanBanner();
    state.libraries = await api("/api/libraries");
    renderSidebar();
    await renderAdmin($("#main"));
  } catch (e) {
    hideScanBanner();
    alert(t("alert.scanFailed", { msg: e.message }));
    btn.innerHTML = original;
    btn.disabled = false;
  }
}

// ── Plex-like scan progress banner ──
function ensureScanBanner() {
  let bar = $("#scan-banner");
  if (!bar) {
    bar = el("div", { id: "scan-banner", class: "scan-banner", hidden: "" });
    bar.innerHTML = `
      <div class="scan-banner-inner">
        <span class="scan-spinner">${icon("refresh")}</span>
        <div class="scan-banner-text">
          <div class="scan-banner-title"></div>
          <div class="scan-banner-sub"></div>
        </div>
      </div>
      <div class="scan-banner-progress"><div class="scan-banner-fill"></div></div>`;
    document.body.appendChild(bar);
  }
  return bar;
}

function showScanBanner() {
  const bar = ensureScanBanner();
  bar.querySelector(".scan-banner-title").textContent = t("scan.scanning");
  bar.querySelector(".scan-banner-sub").textContent = "";
  bar.querySelector(".scan-banner-fill").style.width = "0%";
  bar.hidden = false;
}

function hideScanBanner() {
  const bar = $("#scan-banner");
  if (bar) bar.hidden = true;
}

function updateScanBanner(p) {
  const bar = ensureScanBanner();
  const sub = bar.querySelector(".scan-banner-sub");
  const fill = bar.querySelector(".scan-banner-fill");

  if (p.phase === "thumbnails") {
    // Phase 2: pre-generating thumbnails for the whole library.
    sub.textContent = t("scan.thumbs", { cur: p.thumbDone || 0, total: p.thumbTotal || 0 });
    const pct = p.thumbTotal ? Math.round((p.thumbDone / p.thumbTotal) * 100) : 100;
    fill.style.width = pct + "%";
    return;
  }

  // Phase 1: indexing the directory tree.
  if (p.currentDir) {
    sub.textContent = p.total
      ? t("scan.progress", { cur: p.current, total: p.total, dir: p.currentDir })
      : p.currentDir;
  } else {
    sub.textContent = t("scan.indexing");
  }
  const pct = p.total ? Math.round((p.current / p.total) * 100) : 0;
  fill.style.width = pct + "%";
}

async function pollScanProgress(id) {
  for (;;) {
    const p = await api(`/api/admin/libraries/${id}/scan-progress`);
    updateScanBanner(p);
    if (p.error) throw new Error(p.error);
    if (p.done) return;
    await new Promise((r) => setTimeout(r, 400));
  }
}

async function deleteLibrary(lib) {
  if (!confirm(t("confirm.deleteLibrary", { name: lib.name }))) return;
  try {
    await api(`/api/admin/libraries/${lib.id}`, { method: "DELETE" });
    state.libraries = await api("/api/libraries");
    renderSidebar();
    await renderAdmin($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

let editingLibId = null;
function openLibModal(lib) {
  editingLibId = lib ? lib.id : null;
  $("#lib-modal-title").textContent = lib ? t("lib.edit") : t("lib.new");
  $("#lib-name").value = lib ? lib.name : "";
  $("#lib-root").value = lib ? lib.rootPath : "./photos";
  $("#lib-whitelist").value = lib ? (lib.whitelist || []).join(", ") : "";
  $("#lib-browser").hidden = true;
  $("#lib-modal").hidden = false;
}
function closeLibModal() { $("#lib-modal").hidden = true; }

async function saveLib() {
  const payload = {
    name: $("#lib-name").value.trim(),
    rootPath: $("#lib-root").value.trim(),
    whitelist: $("#lib-whitelist").value.split(",").map((s) => s.trim()).filter(Boolean),
  };
  if (!payload.name || !payload.rootPath) { alert(t("alert.nameRootRequired")); return; }
  try {
    if (editingLibId) {
      await api(`/api/admin/libraries/${editingLibId}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    } else {
      await api("/api/admin/libraries", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    }
    closeLibModal();
    state.libraries = await api("/api/libraries");
    renderSidebar();
    await renderAdmin($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── wiring ──
document.addEventListener("click", (e) => {
  const route = e.target.closest("[data-route]");
  if (route) {
    e.preventDefault();
    navigate({ view: route.getAttribute("data-route") });
  }
});

$("#viewer-close").addEventListener("click", closeViewer);
$("#viewer-prev").addEventListener("click", () => { stopSlideshow(); viewerStep(-1); });
$("#viewer-next").addEventListener("click", () => { stopSlideshow(); viewerStep(1); });
$("#viewer-slideshow").addEventListener("click", () => { if (state.slideshowActive) stopSlideshow(); else startSlideshow(); });
$("#viewer-cover").addEventListener("click", setCurrentAsCover);
$("#viewer-info-btn").addEventListener("click", toggleInfo);
$("#viewer-info-close").addEventListener("click", toggleInfo);
$("#viewer-settings-btn").addEventListener("click", openSsModal);
$("#ss-cancel").addEventListener("click", closeSsModal);
$("#ss-save").addEventListener("click", saveSs);
$("#edit-cancel").addEventListener("click", closeEditModal);
$("#edit-cancel-x").addEventListener("click", closeEditModal);
$("#edit-save").addEventListener("click", saveEdit);
document.querySelectorAll(".plex-edit-tab").forEach((tab) => {
  tab.addEventListener("click", () => switchEditTab(tab.dataset.tab));
});
$("#lib-cancel").addEventListener("click", closeLibModal);
$("#lib-save").addEventListener("click", saveLib);
$("#user-cancel").addEventListener("click", closeUserModal);
$("#user-save").addEventListener("click", saveUser);

// user dropdown menu (avatar)
(function setupUserMenu() {
  const avatar = $("#avatar");
  const dropdown = $("#user-dropdown");
  if (!avatar || !dropdown) return;

  function close() {
    dropdown.hidden = true;
    avatar.setAttribute("aria-expanded", "false");
  }
  function toggle() {
    const open = dropdown.hidden;
    dropdown.hidden = !open;
    avatar.setAttribute("aria-expanded", String(open));
  }

  avatar.addEventListener("click", (e) => { e.stopPropagation(); toggle(); });
  avatar.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); toggle(); }
    if (e.key === "Escape") close();
  });

  // Navigation to settings is handled by the global [data-route] handler;
  // here we just close the dropdown after selecting an item.
  dropdown.addEventListener("click", () => close());

  document.addEventListener("click", (e) => {
    if (!$("#user-menu").contains(e.target)) close();
  });
})();

const navLibraryLink = $("#nav-library");
if (navLibraryLink) {
  navLibraryLink.addEventListener("click", () => {
    if (state.activeLibraryId) navigate({ view: "library", libraryId: state.activeLibraryId });
  });
}

// edit modal: poster / background art sources (choose file, URL, drag & drop)
(function setupArtSources() {
  function makeFileInput(kind) {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "image/*";
    input.hidden = true;
    document.body.appendChild(input);
    input.addEventListener("change", () => {
      const file = input.files && input.files[0];
      input.value = "";
      if (file) handleArtSource(kind, { file });
    });
    return input;
  }

  const posterFile = makeFileInput("cover");
  const bgFile = makeFileInput("background");

  const posterChoose = $("#edit-poster-choose");
  if (posterChoose) posterChoose.addEventListener("click", () => posterFile.click());
  const bgChoose = $("#edit-bg-choose");
  if (bgChoose) bgChoose.addEventListener("click", () => bgFile.click());

  const posterUrl = $("#edit-poster-url");
  if (posterUrl) posterUrl.addEventListener("click", () => {
    const u = prompt(t("prompt.posterUrl"));
    if (u && u.trim()) handleArtSource("cover", { url: u.trim() });
  });
  const bgUrl = $("#edit-bg-url");
  if (bgUrl) bgUrl.addEventListener("click", () => {
    const u = prompt(t("prompt.backgroundUrl"));
    if (u && u.trim()) handleArtSource("background", { url: u.trim() });
  });

  // Drag & drop onto each grid
  function wireDrop(gridId, kind) {
    const grid = $("#" + gridId);
    if (!grid) return;
    ["dragenter", "dragover"].forEach((ev) =>
      grid.addEventListener(ev, (e) => { e.preventDefault(); grid.classList.add("drag-over"); }));
    ["dragleave", "drop"].forEach((ev) =>
      grid.addEventListener(ev, (e) => { e.preventDefault(); grid.classList.remove("drag-over"); }));
    grid.addEventListener("drop", (e) => {
      const dt = e.dataTransfer;
      if (!dt) return;
      const file = dt.files && dt.files[0];
      if (file && file.type.startsWith("image/")) { handleArtSource(kind, { file }); return; }
      const uri = dt.getData("text/uri-list") || dt.getData("text/plain");
      if (uri && /^https?:\/\//.test(uri.trim())) handleArtSource(kind, { url: uri.trim() });
    });
  }
  wireDrop("edit-poster-grid", "cover");
  wireDrop("edit-bg-grid", "background");
})();

// library modal: Browse folder
// ── server-side folder browser (photos live on the server, not the client) ──
let browserPath = "";

async function loadBrowser(path) {
  const data = await api("/api/admin/browse?path=" + encodeURIComponent(path || ""));
  browserPath = data.path || "/";
  $("#lib-browser-path").textContent = browserPath;
  const up = $("#lib-browser-up");
  up.disabled = !data.hasParent;
  up.hidden = !data.hasParent;
  up.dataset.parent = data.parent || "";
  up.closest(".dir-browser-actions").toggleAttribute("data-no-up", !data.hasParent);
  // Keep the field in sync with the absolute path we're browsing.
  $("#lib-root").value = browserPath;
  const list = $("#lib-browser-list");
  list.textContent = "";
  if (!data.dirs.length) {
    const li = document.createElement("li");
    li.className = "dir-browser-empty";
    li.textContent = t("lib.browseEmpty");
    list.appendChild(li);
    return;
  }
  for (const d of data.dirs) {
    const li = document.createElement("li");
    li.className = "dir-browser-item";
    li.dataset.path = d.path;
    const icon = document.createElement("span");
    icon.className = "dir-browser-icon";
    icon.textContent = "📁";
    const name = document.createElement("span");
    name.textContent = d.name;
    li.append(icon, name);
    list.appendChild(li);
  }
}

async function openBrowser() {
  const browser = $("#lib-browser");
  browser.hidden = false;
  // Start at the current field value if it's an absolute path; otherwise let the
  // backend default to the photos root.
  const cur = $("#lib-root").value.trim();
  const start = cur.startsWith("/") || /^[a-zA-Z]:/.test(cur) ? cur : "";
  try {
    await loadBrowser(start);
  } catch (_) {
    try { await loadBrowser(""); } catch (e) { alert(t("alert.error", { msg: e.message })); }
  }
}

$("#lib-browse").addEventListener("click", () => {
  const browser = $("#lib-browser");
  if (browser.hidden) openBrowser();
  else browser.hidden = true;
});

$("#lib-browser-list").addEventListener("click", (e) => {
  const item = e.target.closest(".dir-browser-item");
  if (!item) return;
  loadBrowser(item.dataset.path).catch((err) => alert(t("alert.error", { msg: err.message })));
});

$("#lib-browser-up").addEventListener("click", (e) => {
  if (e.currentTarget.disabled) return;
  loadBrowser(e.currentTarget.dataset.parent || "").catch((err) => alert(t("alert.error", { msg: err.message })));
});

$("#lib-browser-select").addEventListener("click", () => {
  $("#lib-root").value = browserPath;
  $("#lib-browser").hidden = true;
});

// close any open dropdown menu on outside click
document.addEventListener("click", () => {
  document.querySelectorAll(".menu").forEach((m) => { m.hidden = true; });
});

document.addEventListener("keydown", (e) => {
  if ($("#viewer").hidden) return;
  if (e.key === "Escape") closeViewer();
  else if (e.key === "ArrowLeft") { stopSlideshow(); viewerStep(-1); }
  else if (e.key === "ArrowRight") { stopSlideshow(); viewerStep(1); }
});

applyStaticTranslations();
boot();
