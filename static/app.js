"use strict";

const state = {
  user: null,
  libraries: [],
  activeLibraryId: null,
  favorites: new Set(),
  // Frame TVs configured by an admin; populated at boot. The TV nav item is
  // revealed only when at least one exists.
  tvs: [],
  // viewer state
  photos: [],
  photoIndex: 0,
  currentAlbum: null,
  slideshowTimer: null,
  slideshowActive: false,
  infoOpen: false,
  // When the viewer is showing photos from a playlist, this holds that
  // playlist's id so "Remove from playlist" can act on the current photo.
  currentPlaylistId: null,
};

// ── slideshow settings ──
// fitMode: "fit" (contain, no crop) | "fill" (crop to fill) | "scroll" (fill width, auto-pan)
const SS_DEFAULTS = {
  // interval is seconds per photo. Allowed range: 2s … 3600s (60 min). Default 30s (Google Photos-style).
  interval: 30, transition: "fade", fitMode: "fit", loop: true, showInfo: false,
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

// Pending image swap during a fade transition. The CSS "viewer-fade" keyframe
// runs 1.6s total (0.8s out, 0.8s in), so we swap the source at the 0.8s
// midpoint — when the frame is fully faded out — for a true out→swap→in cue.
let viewerFadeTimer = null;
const VIEWER_FADE_HALF_MS = 800;

// ── general UI preferences ──
// photoSize: photo thumbnail box size in px (driven by the size slider).
// albumView/photoView: "grid" (cards / justified thumbs) or "list" (compact
// rows with a small thumbnail). sort: default album/collection order.
const PREFS_DEFAULTS = { photoSize: 200, sort: "name", albumView: "grid", photoView: "grid" };

// Thumbnail size is a 3-stop scale (small / medium / large), in px. Medium is
// the default. Legacy "density" values and any older continuous sizes snap onto
// the nearest stop.
const SIZE_STOPS = [140, 200, 300];
const DENSITY_BOX = { small: 140, medium: 200, large: 300 };
const clampSize = (n) => {
  n = Number(n);
  if (!Number.isFinite(n)) return PREFS_DEFAULTS.photoSize;
  let best = SIZE_STOPS[0];
  for (const s of SIZE_STOPS) if (Math.abs(s - n) < Math.abs(best - n)) best = s;
  return best;
};
// Slider position (0..2) for the current size; defaults to medium.
const sizeIndex = () => { const i = SIZE_STOPS.indexOf(clampSize(prefs.photoSize)); return i < 0 ? 1 : i; };
const sizeFromIndex = (i) => SIZE_STOPS[+i] || PREFS_DEFAULTS.photoSize;

// Fold any legacy `density` value into a `photoSize` so old saved prefs keep
// working after the switch to a continuous size slider.
function migratePrefs(p) {
  if (p.photoSize == null && p.density) p.photoSize = DENSITY_BOX[p.density] || DENSITY_BOX.medium;
  p.photoSize = clampSize(p.photoSize != null ? p.photoSize : PREFS_DEFAULTS.photoSize);
  delete p.density;
  return p;
}

function loadPrefs() {
  let stored = {};
  try { stored = JSON.parse(localStorage.getItem("ui-prefs") || "{}"); } catch (_) { stored = {}; }
  return migratePrefs({ ...PREFS_DEFAULTS, ...stored });
}
function savePrefs(p) {
  prefs = p;
  localStorage.setItem("ui-prefs", JSON.stringify(p));
  syncPrefsToServer();
}
let prefs = loadPrefs();

function photoBox() { return clampSize(prefs.photoSize); }

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
      prefs = migratePrefs({ ...PREFS_DEFAULTS, ...remote.ui });
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
  // `apply` reads the live box size each time, so the same tile can be resized
  // on the fly when the size slider moves (via tile._applySize).
  const apply = () => {
    const box = photoBox();
    const w = img.naturalWidth, h = img.naturalHeight;
    if (!(w > 0 && h > 0)) {
      // Square placeholder until the real ratio is known.
      tile.style.width = box + "px";
      tile.style.height = box + "px";
      return;
    }
    if (w >= h) {
      tile.style.width = box + "px";
      tile.style.height = Math.round(box * (h / w)) + "px";
    } else {
      tile.style.height = box + "px";
      tile.style.width = Math.round(box * (w / h)) + "px";
    }
  };
  tile._applySize = apply;
  apply();
  if (!(img.complete && img.naturalWidth)) img.addEventListener("load", apply, { once: true });
}

// Justified photo grids registered for live resizing by the size slider. The
// list is rebuilt on every view render; stale (detached) grids are skipped.
let photoGrids = [];
function resizePhotoGrids() {
  for (const g of photoGrids) {
    if (!g.isConnected) continue;
    g.querySelectorAll(".photo-thumb").forEach((tile) => { if (tile._applySize) tile._applySize(); });
  }
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
  await loadTVs();
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

// loadTVs fetches the configured Frame TVs and reveals the TV nav item when any
// exist (any authenticated user may view/control them).
async function loadTVs() {
  try {
    state.tvs = await api("/api/tvs");
  } catch (_) {
    state.tvs = [];
  }
  const nav = $("#nav-tv");
  if (nav) nav.hidden = !(state.tvs && state.tvs.length > 0);
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
    case "playlists": return "/playlists";
    case "playlist": return `/playlist/${route.playlistId}`;
    case "tv": return "/tv";
    case "tvDetail": return `/tv/${route.tvId}`;
    case "admin": return "/admin";
    case "admin-tv": return "/admin/tv";
    case "users": return "/users";
    case "jobs": return "/jobs";
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
  if (parts[0] === "playlists") return { view: "playlists" };
  if (parts[0] === "playlist" && parts[1]) return { view: "playlist", playlistId: decodeURIComponent(parts[1]) };
  if (parts[0] === "tv" && parts[1]) return { view: "tvDetail", tvId: decodeURIComponent(parts[1]) };
  if (parts[0] === "tv") return { view: "tv" };
  if (parts[0] === "admin" && parts[1] === "tv") return { view: "admin-tv" };
  if (parts[0] === "admin") return { view: "admin" };
  if (parts[0] === "users") return { view: "users" };
  if (parts[0] === "jobs") return { view: "jobs" };
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

  // Leaving any view stops the TV dashboard's live polling.
  stopTVPolling();

  const main = $("#main");
  main.innerHTML = `<div class="loading">${esc(t("common.loading"))}</div>`;
  if (route.libraryId) state.activeLibraryId = route.libraryId;
  updateTopnav(route.view);
  try {
    switch (route.view) {
      case "home": setActiveSidebar(null, false, true); await renderHome(main); break;
      case "library": setActiveSidebar(route.libraryId); await renderLibrary(main, route.libraryId); break;
      case "node": await renderNode(main, route); break;
      case "playlists": setActiveSidebar(null, "playlists"); await renderPlaylists(main); break;
      case "playlist": setActiveSidebar(null, "playlists"); await renderPlaylist(main, route); break;
      case "tv": setActiveSidebar(null, "tv"); await renderTVList(main); break;
      case "tvDetail": setActiveSidebar(null, "tv"); await renderTVDetail(main, route); break;
      case "admin": setActiveSidebar(null, true); await renderAdmin(main); break;
      case "admin-tv": setActiveSidebar(null, "admin-tv"); await renderAdminTVs(main); break;
      case "users": setActiveSidebar(null, "users"); await renderAdminUsers(main); break;
      case "jobs": setActiveSidebar(null, "jobs"); await renderJobs(main); break;
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
    const nodes = await nodesPs[i];
    if (nodes.length === 0) {
      block.appendChild(el("div", { class: "empty", text: t("home.emptyScan") }));
    } else {
      block.appendChild(carousel(nodes.map((n) => nodeCard(lib.id, n)), { variant: "landscape" }));
    }
    main.appendChild(block);
  }
}

async function renderLibrary(main, libraryId) {
  const lib = state.libraries.find((l) => l.id === libraryId);
  const nodes = await api(`/api/libraries/${libraryId}/nodes`);
  main.innerHTML = "";

  const cover = lib?.coverPhoto || nodes.find((c) => c.coverPhoto)?.coverPhoto || "";
  const totalPhotos = nodes.reduce((n, c) => n + (c.totalPhotoCount != null ? c.totalPhotoCount : (c.photoCount || 0)), 0);
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

  if (nodes.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("home.emptyScan") }));
  } else {
    nodes.forEach((c) => { if (c.favorite) state.favorites.add(c.id); });
    const block = el("div", { class: "lib-block" });
    const rerender = () => navigate({ view: "library", libraryId });
    block.appendChild(sectionHeader(t("node.collections"), [viewToggle("albumView", rerender)]));
    if (prefs.albumView === "list") {
      block.appendChild(nodeListView(libraryId, nodes));
    } else {
      block.appendChild(cardGrid(nodes.map((c) => nodeCard(libraryId, c)), { variant: "landscape" }));
    }
    main.appendChild(block);
  }
}

// ── playlists ──
// Playlists are user-owned, hand-curated photo sets independent of the folder
// tree. The list page shows poster cards; a detail page shows the photos with a
// slideshow and per-photo removal.
async function renderPlaylists(main) {
  const pls = await api("/api/playlists");
  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("playlist.title"))}</div><div class="section-sub">${esc(t("playlist.count", { n: pls.length }))}</div>` }));
  header.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("playlist.newShort"))}`, onclick: () => openPlaylistModal(null) }));
  main.appendChild(header);

  if (!pls || pls.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("playlist.none") }));
    return;
  }
  main.appendChild(cardGrid(pls.map(playlistCard), { variant: "poster" }));
}

function playlistCard(pl) {
  const art = pl.coverPhoto;
  const thumb = art ? `<img src="${thumbURL(art)}" alt="" loading="lazy">` : icon("playlist");
  return el("div", {
    class: "card card--poster",
    onclick: () => navigate({ view: "playlist", playlistId: pl.id }),
    html: `<div class="card-thumb">${thumb}</div>
      <div class="card-body">
        <div class="card-title">${esc(pl.name)}</div>
        <div class="card-counts"><span>${esc(t("meta.photos", { n: pl.photoCount || 0 }))}</span></div>
      </div>`,
  });
}

async function renderPlaylist(main, route) {
  const data = await api(`/api/playlists/${route.playlistId}`);
  const pl = data.playlist;
  const photos = data.photos || [];
  main.innerHTML = "";

  const actions = [];
  if (photos.length > 0) {
    actions.push(el("button", {
      class: "btn accent", html: `${icon("play")} ${esc(t("viewer.slideshow"))}`,
      onclick: () => openPlaylistViewer(pl, photos, 0, true),
    }));
  }
  if (photos.length > 0 && state.tvs && state.tvs.length > 0) {
    actions.push(el("button", {
      class: "btn", html: `${icon("cast")} ${esc(t("tv.sendToTV"))}`,
      onclick: () => sendPlaylistToTV(pl.id),
    }));
  }
  actions.push(el("button", { class: "btn icon-btn", html: icon("pen"), title: t("playlist.rename"), onclick: () => openPlaylistModal(pl) }));
  actions.push(el("button", { class: "btn icon-btn danger", html: icon("trash"), title: t("playlist.delete"), onclick: () => deletePlaylist(pl) }));

  main.appendChild(hero(pl.name, "", pl.coverPhoto || "", actions, "", {
    meta: [t("meta.photos", { n: photos.length })],
  }));

  if (photos.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("playlist.empty") }));
    return;
  }

  photoGrids = [];
  const rerender = () => navigate({ view: "playlist", playlistId: pl.id });
  const onRemove = async (p) => { await removeFromPlaylist(pl.id, p.path); rerender(); };

  const block = el("div", { class: "lib-block" });
  const controls = [];
  if (prefs.photoView !== "list") controls.push(sizeSlider());
  controls.push(viewToggle("photoView", rerender));
  block.appendChild(sectionHeader(t("node.photos"), controls));
  if (prefs.photoView === "list") {
    block.appendChild(photoListView(photos, (i) => openPlaylistViewer(pl, photos, i, false), { onRemove }));
  } else {
    block.appendChild(photoGridView(photos, (i) => openPlaylistViewer(pl, photos, i, false), { onRemove }));
  }
  main.appendChild(block);
}

// openPlaylistViewer opens the viewer for a playlist's photos, tagging the album
// context with coverScope "playlist" so the viewer offers "Remove from playlist".
function openPlaylistViewer(pl, photos, index, slideshow) {
  const list = slideshow ? maybeShuffle(photos) : photos;
  openViewer(list, slideshow ? 0 : index, { id: pl.id, name: pl.name, coverScope: "playlist" }, slideshow);
}

let editingPlaylist = null;
function openPlaylistModal(pl) {
  editingPlaylist = pl;
  $("#playlist-modal-title").textContent = pl ? t("playlist.rename") : t("playlist.new");
  $("#playlist-name").value = pl ? pl.name : "";
  $("#playlist-modal").hidden = false;
  setTimeout(() => $("#playlist-name").focus(), 30);
}
function closePlaylistModal() { $("#playlist-modal").hidden = true; editingPlaylist = null; }

async function savePlaylistName() {
  const name = $("#playlist-name").value.trim();
  if (!name) { alert(t("playlist.nameRequired")); return; }
  try {
    if (editingPlaylist) {
      const id = editingPlaylist.id;
      await api(`/api/playlists/${id}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
      closePlaylistModal();
      navigate({ view: "playlist", playlistId: id });
    } else {
      const pl = await api("/api/playlists", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
      closePlaylistModal();
      navigate({ view: "playlist", playlistId: pl.id });
    }
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function deletePlaylist(pl) {
  if (!confirm(t("playlist.confirmDelete", { name: pl.name }))) return;
  try {
    await api(`/api/playlists/${pl.id}`, { method: "DELETE" });
    navigate({ view: "playlists" });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function removeFromPlaylist(playlistId, path) {
  try {
    await api(`/api/playlists/${playlistId}/items`, {
      method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }),
    });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── add-to-playlist picker ──
// pickerPhotos holds the {name, path} entries queued to be added. The picker
// lists existing playlists (server returns them most-recently-used first) plus
// a "New playlist…" shortcut.
let pickerPhotos = [];
async function openPlaylistPicker(photos, label) {
  pickerPhotos = (photos || []).filter((p) => p && p.path).map((p) => ({ name: p.name || "", path: p.path }));
  if (pickerPhotos.length === 0) { alert(t("playlist.noPhotos")); return; }
  $("#playlist-pick-sub").textContent = label
    ? t("playlist.addingFrom", { name: label, n: pickerPhotos.length })
    : t("playlist.nPhotos", { n: pickerPhotos.length });
  const list = $("#playlist-pick-list");
  list.innerHTML = `<div class="loading">${esc(t("common.loading"))}</div>`;
  $("#playlist-pick-modal").hidden = false;

  let pls = [];
  try { pls = await api("/api/playlists"); } catch (_) { pls = []; }
  list.innerHTML = "";
  if (!pls.length) {
    list.appendChild(el("div", { class: "field-hint", text: t("playlist.noneYet") }));
    return;
  }
  pls.forEach((pl) => {
    list.appendChild(el("button", {
      class: "playlist-pick-item",
      onclick: () => addPhotosToPlaylist(pl.id, pickerPhotos),
      html: `<span class="playlist-pick-name">${esc(pl.name)}</span>` +
        `<span class="playlist-pick-count">${esc(t("meta.photos", { n: pl.photoCount || 0 }))}</span>`,
    }));
  });
}
function closePlaylistPicker() { $("#playlist-pick-modal").hidden = true; pickerPhotos = []; }

async function addPhotosToPlaylist(playlistId, photos) {
  try {
    const res = await api(`/api/playlists/${playlistId}/items`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ photos }),
    });
    closePlaylistPicker();
    if (res && res.async) {
      toast(t("playlist.addingBackground", { n: res.queued != null ? res.queued : photos.length }));
      return;
    }
    const n = res && res.added != null ? res.added : photos.length;
    toast(n > 0 ? t("playlist.added", { n }) : t("playlist.alreadyIn"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── lightweight toast ──
let _toastTimer = null;
function toast(msg) {
  let box = $("#toast");
  if (!box) {
    box = el("div", { id: "toast", class: "toast" });
    document.body.appendChild(box);
  }
  box.textContent = msg;
  // Force reflow so re-triggering the transition works on rapid toasts.
  box.classList.remove("show");
  void box.offsetWidth;
  box.classList.add("show");
  clearTimeout(_toastTimer);
  _toastTimer = setTimeout(() => box.classList.remove("show"), 2200);
}

// ── Frame TV dashboard + control ──
// A TV's live status comes from GET /api/tv/{id}/status; the dashboard polls it
// every few seconds while mounted. "Status" is the intent (playing/stopped) and
// "step" is the current swap-loop activity, surfaced for debugging.

// Curated matte presets (Samsung matte ids are "<style>_<color>"). "none" is the
// safest default; the rest cover the most common framing looks.
const MATTE_OPTIONS = [
  ["none", "None"],
  ["auto", "Auto · match photo"],
  ["modern_polar", "Modern · White"],
  ["modern_warm", "Modern · Warm"],
  ["modern_black", "Modern · Black"],
  ["flexible_polar", "Flexible · White"],
  ["shadowbox_polar", "Shadowbox · White"],
  ["panoramic_polar", "Panoramic · White"],
];

// Art Mode post-process effects (The Frame's "painterly" filters). Values are
// the TV's canonical filter ids; "none" disables the effect. Applied off-screen
// before each photo is shown so there is no visible "develop" transition.
const FILTER_OPTIONS = [
  ["none", "tv.filter.none"],
  ["Wash", "tv.filter.wash"],
  ["Pastel", "tv.filter.pastel"],
  ["Feuve", "tv.filter.feuve"],
  ["Ink", "tv.filter.ink"],
  ["Aqua", "tv.filter.aqua"],
  ["ArtDeco", "tv.filter.artDeco"],
];

let tvPollTimer = null;
// Playlists available to the TV detail view; loaded once per visit and used to
// populate the "channel" dropdown and to resolve the playing playlist's name.
let tvDetailPlaylists = [];
function stopTVPolling() {
  if (tvPollTimer) { clearInterval(tvPollTimer); tvPollTimer = null; }
}

// tvStatusKey collapses a live snapshot into one of stopped|playing|error.
function tvStatusKey(snap) {
  if (snap && snap.error) return "error";
  return (snap && snap.status) || "stopped";
}
function tvStatusLabel(snap) { return t("tv.status." + tvStatusKey(snap)); }
function tvBadge(snap) {
  const k = tvStatusKey(snap);
  return `<span class="tv-badge tv-badge--${k}">${esc(t("tv.status." + k))}</span>`;
}

function tvCard(tv) {
  const snap = tv.status || {};
  const art = snap.currentPath
    ? `<img src="${thumbURL(snap.currentPath)}" alt="" loading="lazy">`
    : `<div class="tv-card-empty">${icon("tv")}</div>`;
  return el("div", {
    class: "card card--landscape",
    onclick: () => navigate({ view: "tvDetail", tvId: tv.id }),
    html: `<div class="card-thumb tv-card-thumb">${art}${tvBadge(snap)}</div>
      <div class="card-body">
        <div class="card-title">${esc(tv.name)}</div>
        <div class="card-counts"><span>${esc(tvStatusLabel(snap))}</span></div>
      </div>`,
  });
}

async function renderTVList(main) {
  const tvs = await api("/api/tvs");
  state.tvs = tvs;
  const nav = $("#nav-tv");
  if (nav) nav.hidden = !(tvs && tvs.length > 0);

  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("tv.title"))}</div><div class="section-sub">${esc(t("tv.subtitle"))}</div>` }));
  if (state.user && state.user.isAdmin) {
    header.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("tv.addBtn"))}`, onclick: () => openTVModal(null) }));
  }
  main.appendChild(header);

  if (!tvs || tvs.length === 0) {
    const empty = el("div", { class: "empty" }, [el("div", { text: t("tv.none") })]);
    if (state.user && state.user.isAdmin) empty.appendChild(el("div", { class: "section-sub", text: t("tv.noneAdminHint") }));
    main.appendChild(empty);
    return;
  }
  main.appendChild(cardGrid(tvs.map(tvCard), { variant: "landscape" }));
}

async function renderTVDetail(main, route) {
  const id = route.tvId;
  let data;
  try {
    data = await api(`/api/tv/${id}/status`);
  } catch (e) {
    main.innerHTML = `<div class="empty">${esc(t("alert.error", { msg: e.message }))}</div>`;
    return;
  }
  try { tvDetailPlaylists = await api("/api/playlists"); } catch (_) { tvDetailPlaylists = []; }

  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("button", { class: "btn sm", html: `${icon("chevron-left")} ${esc(t("tv.title"))}`, onclick: () => navigate({ view: "tv" }) }));
  main.appendChild(header);

  const heroEl = el("div", { class: "tv-hero", id: "tv-hero" });
  const panelEl = el("div", { class: "tv-panel", id: "tv-panel" });
  main.appendChild(heroEl);
  main.appendChild(panelEl);

  const paint = (d) => { updateTVHero(heroEl, d); renderTVPanel(panelEl, d); };
  paint(data);

  stopTVPolling();
  tvPollTimer = setInterval(async () => {
    if (!document.body.contains(heroEl)) { stopTVPolling(); return; }
    try { paint(await api(`/api/tv/${id}/status`)); } catch (_) { /* transient */ }
  }, 2500);
}

// screenHTML renders the TV "screen" preview (current photo or empty state)
// plus the status badge. Kept separate so polling can refresh just the picture
// without rebuilding the controls underneath (which would discard edits).
function screenHTML(d) {
  const snap = d.status || {};
  const k = tvStatusKey(snap);
  const screen = snap.currentPath
    ? `<img src="${photoURL(snap.currentPath)}" alt="">`
    : `<div class="tv-hero-empty">${icon("tv")}<span>${esc(t("tv.notPlaying"))}</span></div>`;
  return `${screen}<span class="tv-badge tv-badge--${k}">${esc(t("tv.status." + k))}</span>`;
}

// updateTVHero is the poll-time entry point. It rebuilds the controls only when
// the playing/stopped mode flips (so in-progress edits to the inline options
// survive the 2.5s refresh); otherwise it just repaints the screen preview.
function updateTVHero(heroEl, d) {
  const snap = d.status || {};
  const mode = snap.status === "playing" ? "playing" : "stopped";
  if (heroEl.dataset.mode !== mode || !heroEl.firstChild) {
    renderTVHero(heroEl, d);
  } else {
    updateTVScreen(heroEl, d);
  }
}

// updateTVScreen repaints only the screen preview when the shown photo or
// status changed, avoiding needless <img> reloads (and flicker) on each poll.
function updateTVScreen(heroEl, d) {
  const snap = d.status || {};
  const shot = (snap.currentPath || "") + "|" + tvStatusKey(snap);
  if (heroEl.dataset.shot === shot) return;
  heroEl.dataset.shot = shot;
  const screenEl = heroEl.querySelector(".tv-hero-screen");
  if (screenEl) screenEl.innerHTML = screenHTML(d);
}

function renderTVHero(heroEl, d) {
  const snap = d.status || {};
  const playing = snap.status === "playing";
  heroEl.dataset.mode = playing ? "playing" : "stopped";
  heroEl.dataset.shot = (snap.currentPath || "") + "|" + tvStatusKey(snap);
  heroEl.innerHTML = `
    <div class="tv-hero-screen">${screenHTML(d)}</div>
    <div class="tv-hero-bar">
      <div class="tv-hero-title">${esc(d.name)}</div>
      <div class="tv-hero-channel" id="tv-hero-channel"></div>
      <div class="tv-hero-actions"></div>
    </div>`;
  const channel = heroEl.querySelector(".tv-hero-channel");
  const actions = heroEl.querySelector(".tv-hero-actions");

  if (playing) {
    // While playing, the channel is locked: show the playlist name plus an
    // (i) that explains it can only be changed once stopped.
    channel.appendChild(el("div", { class: "tv-channel-locked" }, [
      el("span", { class: "tv-channel-name", text: currentPlaylistName(snap.playlistId) }),
      el("button", {
        class: "tv-channel-info", type: "button",
        title: t("tv.channelLocked"), "aria-label": t("tv.channelLocked"),
        html: icon("info"), onclick: () => toast(t("tv.channelLocked")),
      }),
    ]));
    actions.appendChild(el("button", { class: "btn", html: `${icon("skip")} ${esc(t("tv.skip"))}`, onclick: () => tvControl(d.id, "skip") }));
    actions.appendChild(el("button", { class: "btn danger", html: `${icon("stop")} ${esc(t("tv.stop"))}`, onclick: () => tvControl(d.id, "stop") }));
  } else if (tvDetailPlaylists.length === 0) {
    channel.appendChild(el("span", { class: "field-hint", text: t("tv.noPlaylists") }));
  } else {
    // Stopped: pick the playlist ("channel") from a dropdown. When the chosen
    // channel has saved progress, offer Resume (continue the deck/position)
    // plus Restart (fresh shuffle); otherwise a plain Play. Pressing any of
    // these first flushes the inline options so playback uses the latest setup.
    const sel = el("select", { class: "control sm tv-channel-select", id: "tv-channel-select" });
    tvDetailPlaylists.forEach((p) => {
      sel.appendChild(el("option", { value: p.id, text: `${p.name} · ${t("meta.photos", { n: p.photoCount || 0 })}` }));
    });
    const resumeId = snap.resumable ? snap.resumePlaylistId : "";
    if (snap.playlistId && tvDetailPlaylists.some((p) => p.id === snap.playlistId)) {
      sel.value = snap.playlistId;
    }
    channel.appendChild(sel);

    const start = async (fn) => { await flushTVSettings(d.id, heroEl); await fn(); };
    const renderActions = () => {
      actions.innerHTML = "";
      const selId = sel.value;
      if (resumeId && selId === resumeId) {
        const frac = snap.resumeTotal ? ` (${(snap.position || 0) + 1}/${snap.resumeTotal})` : "";
        actions.appendChild(el("button", {
          class: "btn accent", html: `${icon("play")} ${esc(t("tv.resume"))}${esc(frac)}`,
          onclick: () => start(() => tvResume(d.id)),
        }));
        actions.appendChild(el("button", {
          class: "btn", title: t("tv.restartHint"), html: `${icon("refresh")} ${esc(t("tv.restart"))}`,
          onclick: () => start(() => tvPlay(d.id, selId)),
        }));
      } else {
        actions.appendChild(el("button", {
          class: "btn accent", html: `${icon("play")} ${esc(t("tv.play"))}`,
          onclick: () => { if (selId) start(() => tvPlay(d.id, selId)); },
        }));
      }
    };
    sel.addEventListener("change", renderActions);
    renderActions();
  }

  // Inline display/playback options, below the first line in the same card.
  // Editable while stopped (auto-saved), read-only while playing.
  heroEl.appendChild(renderTVOptions(d, playing));
}

// ── inline TV options (display + playback) ──
// These mirror what used to live in the admin modal, but are edited right on
// the TV page. Changes auto-save to the per-TV config (debounced) so the last
// setup is remembered, and they lock to read-only while the TV is playing.

let tvSettingsTimer = null;

// collectTVOptions reads the current option controls within a hero card into a
// settings payload for PUT /api/tv/{id}/settings.
function collectTVOptions(scope) {
  const get = (k) => scope.querySelector(`[data-tvopt="${k}"]`);
  const caps = CAPTION_FIELDS.filter((k) => {
    const cb = scope.querySelector(`[data-tvcap="${k}"]`);
    return cb && cb.checked;
  });
  return {
    displayMode: get("displayMode") ? get("displayMode").value : "blur-fill",
    smartFill: get("smartFill") ? get("smartFill").checked : false,
    matte: get("matte") ? get("matte").value : "none",
    bgColor: get("bgColor") ? get("bgColor").value : "#000000",
    borderPct: get("borderPct") ? Math.max(0, Math.min(40, parseInt(get("borderPct").value, 10) || 0)) : 0,
    intervalSeconds: get("interval") ? (parseInt(get("interval").value, 10) || DEFAULT_INTERVAL) : DEFAULT_INTERVAL,
    playOrder: get("playOrder") && get("playOrder").value === "random" ? "random" : "sequential",
    photoFilter: get("photoFilter") ? get("photoFilter").value : "none",
    captionFields: caps,
  };
}

async function saveTVSettings(id, payload) {
  try {
    await api(`/api/tv/${id}/settings`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  } catch (e) {
    toast(t("alert.error", { msg: e.message }));
  }
}

// scheduleTVSettingsSave debounces auto-save so dragging a number/colour or
// flipping several toggles results in a single write.
function scheduleTVSettingsSave(id, scope) {
  clearTimeout(tvSettingsTimer);
  tvSettingsTimer = setTimeout(() => saveTVSettings(id, collectTVOptions(scope)), 400);
}

// flushTVSettings cancels any pending debounce and writes immediately, used
// right before play/resume so playback always uses the on-screen options.
async function flushTVSettings(id, scope) {
  clearTimeout(tvSettingsTimer);
  const opts = scope.querySelector(".tv-options");
  if (!opts || opts.classList.contains("tv-options--locked")) return;
  await saveTVSettings(id, collectTVOptions(scope));
}

// applyTVOptVisibility shows only the option rows relevant to the chosen
// display style (matte for "tv-matte", colour/border for "fit-color").
function applyTVOptVisibility(wrap) {
  const modeEl = wrap.querySelector('[data-tvopt="displayMode"]');
  if (!modeEl) return;
  const mode = modeEl.value;
  const matte = wrap.querySelector(".tv-opt-matte");
  const color = wrap.querySelector(".tv-opt-color");
  if (matte) matte.hidden = mode !== "tv-matte";
  if (color) color.hidden = mode !== "fit-color";
}

function tvOptSelect(key, options, value, locked, onChange) {
  const sel = el("select", { class: "control sm", "data-tvopt": key });
  options.forEach(([v, label]) => { const o = el("option", { text: label }); o.value = v; sel.appendChild(o); });
  sel.value = value;
  if (locked) sel.disabled = true;
  sel.addEventListener("change", onChange);
  return sel;
}

function tvOptField(labelText, control, extraClass) {
  return el("label", { class: "field tv-opt" + (extraClass ? " " + extraClass : "") }, [
    el("span", { text: labelText }), control,
  ]);
}

function renderTVOptions(d, locked) {
  const wrap = el("div", { class: "tv-options" + (locked ? " tv-options--locked" : "") });
  const onChange = () => { applyTVOptVisibility(wrap); if (!locked) scheduleTVSettingsSave(d.id, wrap); };

  // Display style.
  wrap.appendChild(tvOptField(t("tv.displayMode"), tvOptSelect("displayMode", [
    ["blur-fill", t("tv.mode.blurFill")],
    ["fill", t("tv.mode.fill")],
    ["fit-color", t("tv.mode.fitColor")],
    ["tv-matte", t("tv.mode.tvMatte")],
  ], d.displayMode || "blur-fill", locked, onChange)));

  // Swap interval.
  wrap.appendChild(tvOptField(t("tv.interval"), tvOptSelect("interval",
    INTERVAL_OPTIONS.map((s) => [String(s), intervalLabel(s)]),
    String(nearestInterval(d.intervalSeconds || DEFAULT_INTERVAL)), locked, onChange)));

  // Play order.
  wrap.appendChild(tvOptField(t("tv.playOrder"), tvOptSelect("playOrder", [
    ["sequential", t("tv.order.sequential")],
    ["random", t("tv.order.random")],
  ], d.playOrder === "random" ? "random" : "sequential", locked, onChange)));

  // Art effect (Frame post-process filter), applied off-screen before each swap.
  wrap.appendChild(tvOptField(t("tv.filter"),
    tvOptSelect("photoFilter", FILTER_OPTIONS.map(([v, k]) => [v, t(k)]),
      d.photoFilter || "none", locked, onChange)));

  // Matte (only relevant for the "tv-matte" display style).
  wrap.appendChild(tvOptField(t("tv.matte"),
    tvOptSelect("matte", MATTE_OPTIONS, d.matte || "none", locked, onChange), "tv-opt-matte"));

  // Background colour + border (only relevant for "fit-color").
  const bg = el("input", { type: "color", "data-tvopt": "bgColor", value: d.bgColor || "#000000" });
  const border = el("input", { type: "number", "data-tvopt": "borderPct", min: "0", max: "40", step: "1", value: String(d.borderPct || 0) });
  if (locked) { bg.disabled = true; border.disabled = true; }
  bg.addEventListener("change", onChange);
  border.addEventListener("change", onChange);
  wrap.appendChild(el("div", { class: "tv-opt tv-opt-color" }, [
    el("label", { class: "field" }, [el("span", { text: t("tv.bgColor") }), bg]),
    el("label", { class: "field" }, [el("span", { text: t("tv.border") }), border]),
  ]));

  // Smart fill toggle.
  const smart = el("input", { type: "checkbox", "data-tvopt": "smartFill" });
  smart.checked = !!d.smartFill;
  if (locked) smart.disabled = true;
  smart.addEventListener("change", onChange);
  wrap.appendChild(el("div", { class: "tv-opt tv-opt-wide" }, [
    el("label", { class: "field checkbox" }, [smart, el("span", { text: t("tv.smartFill") })]),
    el("small", { class: "field-hint", text: t("tv.smartFillHint") }),
  ]));

  // Caption overlays.
  const caps = new Set(d.captionFields || []);
  const grid = el("div", { class: "tv-caption-fields" });
  CAPTION_FIELDS.forEach((kf) => {
    const cb = el("input", { type: "checkbox", "data-tvcap": kf });
    cb.checked = caps.has(kf);
    if (locked) cb.disabled = true;
    cb.addEventListener("change", onChange);
    grid.appendChild(el("label", { class: "field checkbox" }, [cb, el("span", { text: t("tv.cap." + kf) })]));
  });
  wrap.appendChild(el("div", { class: "tv-opt tv-opt-wide" }, [
    el("span", { class: "tv-opt-label", text: t("tv.caption") }),
    el("small", { class: "field-hint", text: t("tv.captionHint") }),
    grid,
  ]));

  if (locked) {
    wrap.appendChild(el("div", { class: "tv-options-lock", text: t("tv.optionsLocked") }));
  }
  applyTVOptVisibility(wrap);
  return wrap;
}

// currentPlaylistName resolves a playlist id to its display name using the
// playlists loaded for the TV detail view, falling back to a generic label.
function currentPlaylistName(playlistId) {
  const p = (tvDetailPlaylists || []).find((x) => x.id === playlistId);
  return p ? p.name : t("tv.playlist");
}

function renderTVPanel(panelEl, d) {
  const snap = d.status || {};
  const rows = [];
  rows.push([t("tv.status"), tvStatusLabel(snap)]);
  rows.push([t("tv.step"), t("tv.step." + (snap.step || "idle"))]);
  if (snap.currentName) {
    const pos = snap.total ? ` (${(snap.position || 0) + 1}/${snap.total})` : "";
    rows.push([t("tv.current"), snap.currentName + pos]);
  }
  const mins = Math.round((d.intervalSeconds || 0) / 60);
  rows.push([t("tv.intervalLabel"), t("tv.minutesShort", { n: mins })]);
  if (snap.nextSwapAt && snap.status === "playing") {
    const secs = Math.max(0, Math.round((new Date(snap.nextSwapAt).getTime() - Date.now()) / 1000));
    rows.push([t("tv.nextSwap"), t("tv.nextSwapIn", { n: fmtDuration(secs) })]);
  }
  if (snap.error) rows.push([t("tv.lastError"), snap.error]);

  panelEl.innerHTML = "";
  const body = el("div", { class: "tv-panel-body" });
  const info = el("div", { class: "tv-panel-info" });
  info.appendChild(el("div", { class: "section-title", text: t("tv.status") }));
  rows.forEach(([key, val]) => {
    info.appendChild(el("div", { class: "info-row" }, [
      el("span", { class: "info-key", text: key }),
      el("span", { class: "info-val", text: val }),
    ]));
  });
  body.appendChild(info);

  // Right column: the next-image preview on top, then the history list below.
  const side = el("div", { class: "tv-panel-side" });

  // Next-image preview: a thumbnail with its filename underneath.
  if (snap.status === "playing" && snap.nextPath) {
    side.appendChild(el("figure", { class: "tv-next" }, [
      el("span", { class: "info-key", text: t("tv.next") }),
      el("img", { class: "tv-next-thumb", src: thumbURL(snap.nextPath), alt: "", loading: "lazy" }),
      el("figcaption", { class: "tv-next-name", text: snap.nextName || "" }),
    ]));
  }

  // History: the last few shown photos (newest first) with a quick remove that
  // drops the photo from the playlist, so an unwanted frame won't come back.
  if (Array.isArray(snap.history) && snap.history.length) {
    side.appendChild(renderTVHistory(d.id, snap));
  }

  if (side.childNodes.length) body.appendChild(side);
  panelEl.appendChild(body);
}

// renderTVHistory builds the recently-shown list. Each row carries a remove (−)
// button that deletes the photo from the playing playlist.
function renderTVHistory(tvId, snap) {
  const playlistId = snap.playlistId;
  const wrap = el("div", { class: "tv-history" }, [
    el("span", { class: "info-key", text: t("tv.history") }),
  ]);
  const list = el("ul", { class: "tv-history-list" });
  snap.history.forEach((item) => {
    const removeBtn = el("button", {
      class: "tv-history-remove", type: "button",
      title: t("tv.removeFromPlaylist"), "aria-label": t("tv.removeFromPlaylist"),
      html: icon("x"),
    });
    const row = el("li", { class: "tv-history-item" }, [
      el("img", { class: "tv-history-thumb", src: thumbURL(item.path), alt: "", loading: "lazy" }),
      el("div", { class: "tv-history-meta" }, [
        el("div", { class: "tv-history-name", text: item.name || "" }),
        el("div", { class: "tv-history-time", text: fmtRelative(item.shownAt) }),
      ]),
      removeBtn,
    ]);
    removeBtn.addEventListener("click", () => removeFromTVHistory(tvId, playlistId, item, row, removeBtn));
    list.appendChild(row);
  });
  wrap.appendChild(list);
  return wrap;
}

// removeFromTVHistory drops a photo from the playing playlist. It updates the
// row optimistically (the next status poll reflects the new playlist).
async function removeFromTVHistory(tvId, playlistId, item, row, btn) {
  if (!playlistId) { alert(t("tv.removeNoPlaylist")); return; }
  btn.disabled = true;
  try {
    await api(`/api/playlists/${playlistId}/items`, {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: item.path }),
    });
    row.classList.add("removed");
    toast(t("tv.removedToast", { name: item.name || "" }));
  } catch (e) {
    btn.disabled = false;
    alert(t("alert.error", { msg: e.message }));
  }
}

// fmtRelative renders a short "time since" label (e.g. "just now", "5m ago")
// for the history timestamps; falls back to a clock time for older entries.
function fmtRelative(iso) {
  const then = new Date(iso).getTime();
  if (!then) return "";
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (secs < 10) return t("time.justNow");
  if (secs < 60) return t("time.secondsAgo", { n: secs });
  const mins = Math.floor(secs / 60);
  if (mins < 60) return t("time.minutesAgo", { n: mins });
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return t("time.hoursAgo", { n: hrs });
  return new Date(then).toLocaleDateString();
}

function fmtDuration(secs) {
  if (secs >= 3600) { const h = Math.floor(secs / 3600), m = Math.round((secs % 3600) / 60); return `${h}h ${m}m`; }
  if (secs >= 60) { const m = Math.floor(secs / 60), s = secs % 60; return `${m}m ${s}s`; }
  return `${secs}s`;
}

async function tvControl(id, action) {
  try {
    await api(`/api/tv/${id}/${action}`, { method: "POST" });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
    return;
  }
  if (action !== "skip") {
    try {
      const d = await api(`/api/tv/${id}/status`);
      const heroEl = $("#tv-hero"), panelEl = $("#tv-panel");
      if (heroEl) renderTVHero(heroEl, d);
      if (panelEl) renderTVPanel(panelEl, d);
    } catch (_) { /* poll will refresh */ }
  }
}

async function tvPlay(tvId, playlistId) {
  try {
    await api(`/api/tv/${tvId}/play`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ playlistId }) });
    const d = await api(`/api/tv/${tvId}/status`);
    const heroEl = $("#tv-hero"), panelEl = $("#tv-panel");
    if (heroEl) renderTVHero(heroEl, d);
    if (panelEl) renderTVPanel(panelEl, d);
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// tvResume continues a stopped TV from its saved position/deck (no playlist
// body needed — the server uses the persisted channel).
async function tvResume(tvId) {
  try {
    await api(`/api/tv/${tvId}/resume`, { method: "POST" });
    const d = await api(`/api/tv/${tvId}/status`);
    const heroEl = $("#tv-hero"), panelEl = $("#tv-panel");
    if (heroEl) renderTVHero(heroEl, d);
    if (panelEl) renderTVPanel(panelEl, d);
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// sendPlaylistToTV is the "Send to TV" action from a playlist: pick a TV (when
// more than one), start playback, and jump to its dashboard.
async function sendPlaylistToTV(playlistId) {
  let tvs = state.tvs || [];
  if (!tvs.length) { try { tvs = await api("/api/tvs"); state.tvs = tvs; } catch (_) { tvs = []; } }
  if (!tvs.length) { alert(t("tv.none")); return; }

  let target;
  if (tvs.length === 1) {
    target = tvs[0];
  } else {
    const choice = await chooserModal(t("tv.chooseTV"), tvs.map((x) => ({ id: x.id, label: x.name })));
    if (!choice) return;
    target = tvs.find((x) => x.id === choice.id);
  }
  if (!target) return;
  try {
    await api(`/api/tv/${target.id}/play`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ playlistId }) });
    toast(t("tv.started", { name: target.name }));
    navigate({ view: "tvDetail", tvId: target.id });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// chooserModal renders a lightweight one-shot picker and resolves with the
// chosen option ({id,label,sub}) or null when dismissed.
function chooserModal(title, options) {
  return new Promise((resolve) => {
    const overlay = el("div", { class: "modal" });
    const card = el("div", { class: "modal-card" });
    card.appendChild(el("div", { class: "modal-title", text: title }));
    const list = el("div", { class: "playlist-pick-list" });
    let settled = false;
    const done = (val) => { if (settled) return; settled = true; overlay.remove(); resolve(val); };
    options.forEach((o) => {
      list.appendChild(el("button", {
        class: "playlist-pick-item",
        onclick: () => done(o),
        html: `<span class="playlist-pick-name">${esc(o.label)}</span>` +
          (o.sub ? `<span class="playlist-pick-count">${esc(o.sub)}</span>` : ""),
      }));
    });
    card.appendChild(list);
    const acts = el("div", { class: "modal-actions" });
    acts.appendChild(el("button", { class: "btn", text: t("common.close"), onclick: () => done(null) }));
    card.appendChild(acts);
    overlay.appendChild(card);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) done(null); });
    document.body.appendChild(overlay);
  });
}

// ── admin: Frame TV management ──
async function buildTVRows(main) {
  let tvs = [];
  try { tvs = await api("/api/admin/tvs"); } catch (_) { tvs = []; }
  if (!tvs.length) {
    main.appendChild(el("div", { class: "empty", text: t("tv.none") }));
    return;
  }
  tvs.forEach((tv) => {
    const row = el("div", { class: "lib-row" });
    const modeKey = { "blur-fill": "blurFill", "fill": "fill", "fit-color": "fitColor", "tv-matte": "tvMatte" }[tv.displayMode] || "blurFill";
    row.appendChild(el("div", {
      html: `<div class="lib-name">${esc(tv.name)}</div>` +
        `<div class="lib-path">${esc(tv.ip)} &nbsp;·&nbsp; ${esc(t("tv.mode." + modeKey))} &nbsp;·&nbsp; ${esc(intervalLabel(tv.intervalSeconds || DEFAULT_INTERVAL))}</div>`,
    }));
    const actions = el("div", { class: "lib-actions" });
    actions.appendChild(el("button", { class: "btn sm", html: icon("edit"), onclick: () => openTVModal(tv) }));
    actions.appendChild(el("button", { class: "btn sm danger", html: icon("trash"), onclick: () => deleteTV(tv) }));
    row.appendChild(actions);
    main.appendChild(row);
  });
}

// Caption fields offered in the TV modal (keys must match the backend).
const CAPTION_FIELDS = ["date", "year", "camera", "location", "filename", "album"];

// Swap interval presets, in seconds.
const INTERVAL_OPTIONS = [30, 45, 60, 180, 300, 600, 900, 1800];
const DEFAULT_INTERVAL = 300;

function intervalLabel(s) {
  return s < 60 ? t("tv.unitSec", { n: s }) : t("tv.unitMin", { n: s / 60 });
}
// Snap an arbitrary stored interval to the closest preset so legacy values
// (e.g. 3600s) still select a valid option.
function nearestInterval(s) {
  return INTERVAL_OPTIONS.reduce((best, v) => (Math.abs(v - s) < Math.abs(best - s) ? v : best), INTERVAL_OPTIONS[0]);
}

// The admin TV modal now edits only the TV's identity (name + IP). All display
// and playback options are edited inline on the TV detail page, so editing here
// must not clobber them — we carry the existing config through on save.
let editingTVId = null;
let editingTVData = null;

function openTVModal(tv) {
  editingTVId = tv ? tv.id : null;
  editingTVData = tv || null;
  $("#tv-modal-title").textContent = tv ? t("tv.edit") : t("tv.new");
  $("#tv-name").value = tv ? tv.name : "";
  $("#tv-ip").value = tv ? tv.ip : "";
  const msg = $("#tv-test-msg");
  msg.hidden = true; msg.textContent = ""; msg.className = "setup-msg";
  $("#tv-modal").hidden = false;
  setTimeout(() => $("#tv-name").focus(), 30);
}
function closeTVModal() { $("#tv-modal").hidden = true; editingTVId = null; editingTVData = null; }

async function saveTV() {
  const base = editingTVData || {};
  const payload = {
    name: $("#tv-name").value.trim(),
    ip: $("#tv-ip").value.trim(),
    // Preserve the display/playback options (edited on the TV page) so an
    // identity edit here doesn't reset them. New TVs fall back to defaults.
    matte: base.matte || "none",
    displayMode: base.displayMode || "blur-fill",
    bgColor: base.bgColor || "#000000",
    borderPct: base.borderPct || 0,
    smartFill: !!base.smartFill,
    captionFields: base.captionFields || [],
    intervalSeconds: base.intervalSeconds || DEFAULT_INTERVAL,
    playOrder: base.playOrder === "random" ? "random" : "sequential",
    photoFilter: base.photoFilter || "none",
  };
  if (!payload.name || !payload.ip) { alert(t("alert.nameRootRequired")); return; }
  try {
    if (editingTVId) {
      await api(`/api/admin/tvs/${editingTVId}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    } else {
      await api("/api/admin/tvs", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
    }
    closeTVModal();
    await loadTVs();
    await renderAdminTVs($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function deleteTV(tv) {
  if (!confirm(t("tv.confirmDelete", { name: tv.name }))) return;
  try {
    await api(`/api/admin/tvs/${tv.id}`, { method: "DELETE" });
    await loadTVs();
    await renderAdminTVs($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function testTV() {
  const ip = $("#tv-ip").value.trim();
  const msg = $("#tv-test-msg");
  if (!ip) { msg.hidden = false; msg.className = "setup-msg error"; msg.textContent = t("tv.ipHint"); return; }
  msg.hidden = false; msg.className = "setup-msg"; msg.textContent = t("tv.testing");
  try {
    const r = await api("/api/admin/tvs/test", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ip }) });
    if (!r.reachable) { msg.className = "setup-msg error"; msg.textContent = t("tv.testUnreachable"); return; }
    if (r.frameSupported) {
      msg.className = "setup-msg success";
      msg.textContent = r.model ? t("tv.testModel", { model: r.model }) : t("tv.testFrame");
    } else {
      msg.className = "setup-msg";
      msg.textContent = t("tv.testNoFrame");
    }
  } catch (e) {
    msg.className = "setup-msg error";
    msg.textContent = t("alert.error", { msg: e.message });
  }
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
    const bgImg = el("div", { class: "hero-backdrop-img", style: `background-image:url('${photoURL(art)}')` });
    backdrop.appendChild(bgImg);
    // Size the backdrop to the art's full natural height at the container
    // width, so the image is shown in full (top-aligned, full width) and
    // continues below the hero behind the tiles. The hero keeps its own
    // content-driven height, so the content stays at its original top Y.
    const probe = new Image();
    const sizeBackdrop = () => {
      if (probe.naturalWidth > 0 && node.clientWidth > 0) {
        const h = node.clientWidth * (probe.naturalHeight / probe.naturalWidth);
        backdrop.style.height = `${Math.round(h)}px`;
      }
    };
    probe.onload = sizeBackdrop;
    probe.src = photoURL(art);
    // Re-measure on resize since the backdrop height depends on container width.
    window.addEventListener("resize", sizeBackdrop);
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
  // Poster cards are 3:4 (portrait) and should prefer the cover (poster) image;
  // landscape cards are 16:9 and should prefer the background image. Fall back
  // to whichever art is available.
  const art = opts.poster
    ? (n.coverPhoto || n.backgroundPhoto)
    : (n.backgroundPhoto || n.coverPhoto);
  const thumb = art ? `<img src="${thumbURL(art)}" alt="" loading="lazy">` : icon(n.hasChildren ? "folder" : "photo");
  const isAlbum = (n.photoCount || 0) > 0;
  const isFav = state.favorites.has(n.id);
  const favHtml = isAlbum
    ? `<button class="card-fav${isFav ? " active" : ""}" title="${esc(t("album.fav"))}">${icon(isFav ? "heart-filled" : "heart")}</button>`
    : "";
  // Parent line: show the full path of collections leading to the album
  // (library › collection1 › collection2), with the album name kept as the
  // title above. folderPath is relative to the library root and includes the
  // album's own folder as its last segment, which we drop here.
  const pathSegs = opts.parent ? [opts.parent] : [];
  const rel = (n.folderPath || "").replace(/^\.[\\/]?/, "");
  if (rel && rel !== ".") {
    const parts = rel.split(/[\\/]/).filter(Boolean);
    parts.pop();
    pathSegs.push(...parts);
  }
  const pathText = pathSegs.join(" / ");
  const parentHtml = pathSegs.length
    ? `<div class="card-parent" title="${esc(pathText)}">${pathSegs.map((s) => esc(s)).join('<span class="card-path-sep">/</span>')}</div>`
    : "";

  // Counts line: show sub-collection and/or photo counts as applicable. Use the
  // recursive total so a collection that only holds sub-folders still shows how
  // many photos live beneath it instead of a misleading "0 photos". Empty
  // collections drop the photos line entirely.
  const totalPhotos = n.totalPhotoCount != null ? n.totalPhotoCount : (n.photoCount || 0);
  const counts = [];
  if (n.hasChildren || (n.childCount || 0) > 0) counts.push(`<span>${esc(t("card.collectionCount", { n: n.childCount || 0 }))}</span>`);
  if (totalPhotos > 0 || isAlbum) counts.push(`<span>${esc(t("meta.photos", { n: totalPhotos }))}</span>`);

  const card = el("div", {
    class: "card " + (opts.poster ? "card--poster" : "card--landscape"),
    onclick: () => navigate({ view: "node", libraryId, nodeId: n.id }),
    html: `<div class="card-thumb">${thumb}${favHtml}</div>
      <div class="card-body">
        <div class="card-title" title="${esc(n.name)}">${esc(n.name)}</div>
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
  block.appendChild(carousel(cards));
  return block;
}

// carousel renders cards in a single horizontally-scrolling row with prev/next
// buttons (Overseerr-style). Pressing a button pages by the visible width, so
// the number of cards advanced scales with the screen size. Buttons hide at the
// ends and the whole control degrades to a plain scrollable row on touch.
// opts: { variant: "landscape" | "poster" } controls card sizing in the row.
function rowLimit() {
  const n = state.user && state.user.rowLimit;
  return Number.isInteger(n) && n > 0 ? n : 16;
}

function carousel(cards, opts = {}) {
  const wrap = el("div", { class: "carousel" + (opts.variant ? " carousel--" + opts.variant : "") });
  const row = el("div", { class: "row-scroll" });
  // Cap how many items load into a single row to keep large folders snappy.
  cards.slice(0, rowLimit()).forEach((c) => row.appendChild(c));

  const prev = el("button", { class: "carousel-nav carousel-prev", "aria-label": t("viewer.prev"), title: t("viewer.prev"), html: icon("chevron-left") });
  const next = el("button", { class: "carousel-nav carousel-next", "aria-label": t("viewer.next"), title: t("viewer.next"), html: icon("chevron-right") });

  const page = (dir) => {
    // Scroll by ~90% of the viewport so a sliver of the next page stays visible
    // as a continuity cue, mirroring how Overseerr pages its sliders.
    const amount = Math.max(row.clientWidth * 0.9, 160);
    row.scrollBy({ left: dir * amount, behavior: "smooth" });
  };
  prev.addEventListener("click", () => page(-1));
  next.addEventListener("click", () => page(1));

  const updateButtons = () => {
    const max = row.scrollWidth - row.clientWidth;
    const overflowing = max > 4;
    wrap.classList.toggle("has-overflow", overflowing);
    prev.classList.toggle("disabled", row.scrollLeft <= 1);
    next.classList.toggle("disabled", row.scrollLeft >= max - 1);
  };
  row.addEventListener("scroll", updateButtons, { passive: true });
  window.addEventListener("resize", updateButtons);
  // Defer once so layout (and lazily-sized cards) settle before measuring.
  requestAnimationFrame(updateButtons);

  wrap.appendChild(prev);
  wrap.appendChild(row);
  wrap.appendChild(next);
  return wrap;
}

// A wrapping grid of cards: unlike `carousel`, this lays every card out in a
// multi-row grid so large folders (e.g. 30 albums) are all visible at once
// instead of hidden behind a single horizontal scroller.
function cardGrid(cards, opts = {}) {
  const grid = el("div", { class: "card-grid" + (opts.variant ? " card-grid--" + opts.variant : "") });
  cards.forEach((c) => grid.appendChild(c));
  return grid;
}

// ── view controls (size slider + grid/list toggle) ──
// A section header with a title on the left and optional controls on the right
// (the size slider and/or the grid↔list toggle).
function sectionHeader(title, controls) {
  const h = el("div", { class: "section-header" });
  h.appendChild(el("div", { html: `<div class="section-title">${esc(title)}</div>` }));
  if (controls && controls.length) h.appendChild(el("div", { class: "view-toolbar" }, controls));
  return h;
}

// A range slider that scales the justified photo thumbnails. Dragging resizes
// the currently rendered grids live (no refetch); the value is persisted on
// release.
function sizeSlider() {
  const wrap = el("div", { class: "size-slider", title: t("view.size") });
  const input = el("input", {
    type: "range", min: 0, max: SIZE_STOPS.length - 1, step: 1,
    value: sizeIndex(), "aria-label": t("view.size"),
  });
  input.addEventListener("input", () => {
    prefs.photoSize = sizeFromIndex(input.value);
    resizePhotoGrids();
  });
  input.addEventListener("change", () => savePrefs({ ...prefs, photoSize: sizeFromIndex(input.value) }));
  wrap.appendChild(el("span", { class: "size-slider-icon size-slider-icon--sm", html: icon("photo") }));
  wrap.appendChild(input);
  wrap.appendChild(el("span", { class: "size-slider-icon size-slider-icon--lg", html: icon("photo") }));
  return wrap;
}

// A segmented grid↔list toggle bound to a pref key. Switching re-renders the
// current view via the supplied callback.
function viewToggle(prefKey, rerender) {
  const wrap = el("div", { class: "view-toggle" });
  const make = (mode, ic, label) => {
    const b = el("button", {
      class: "view-toggle-btn" + (prefs[prefKey] === mode ? " active" : ""),
      title: label, "aria-label": label, html: icon(ic),
    });
    b.addEventListener("click", () => {
      if (prefs[prefKey] === mode) return;
      savePrefs({ ...prefs, [prefKey]: mode });
      rerender();
    });
    return b;
  };
  wrap.appendChild(make("grid", "layout-grid", t("view.grid")));
  wrap.appendChild(make("list", "list", t("view.list")));
  return wrap;
}

// The justified, box-fit photo grid. Registered for live resizing by the size
// slider. opts: { coverPath, onRemove(p) }.
function photoGridView(photos, onClick, opts = {}) {
  const grid = el("div", { class: "photo-grid justified" });
  photos.forEach((p, i) => {
    const isCover = opts.coverPath && opts.coverPath === p.path;
    const img = el("img", { src: thumbURL(p.path), alt: esc(p.name), loading: "lazy" });
    const tile = el("div", { class: "photo-thumb" + (isCover ? " cover" : ""), onclick: () => onClick(i) });
    tile.appendChild(img);
    if (opts.onRemove) {
      const rm = el("button", { class: "photo-remove", title: t("playlist.removeFrom"), html: icon("x") });
      rm.addEventListener("click", (e) => { e.stopPropagation(); opts.onRemove(p); });
      tile.appendChild(rm);
    }
    sizeToBox(tile, img);
    grid.appendChild(tile);
  });
  photoGrids.push(grid);
  return grid;
}

// A compact, table-like list of photos: a small fixed thumbnail plus the file
// name per row. opts: { onRemove(p) }.
function photoListView(photos, onClick, opts = {}) {
  const list = el("div", { class: "photo-list" });
  photos.forEach((p, i) => {
    const row = el("div", { class: "photo-list-row", onclick: () => onClick(i) });
    row.appendChild(el("div", { class: "photo-list-thumb" }, [
      el("img", { src: thumbURL(p.path), alt: "", loading: "lazy" }),
    ]));
    row.appendChild(el("div", { class: "photo-list-name", text: p.name, title: p.name }));
    if (opts.onRemove) {
      const rm = el("button", { class: "photo-list-remove", title: t("playlist.removeFrom"), html: icon("x") });
      rm.addEventListener("click", (e) => { e.stopPropagation(); opts.onRemove(p); });
      row.appendChild(rm);
    }
    list.appendChild(row);
  });
  return list;
}

// A compact, table-like list of albums/collections: small thumbnail, name and
// counts per row. opts: { poster, parent } mirror nodeCard's art preferences.
function nodeListView(libraryId, nodes, opts = {}) {
  const list = el("div", { class: "node-list" });
  nodes.forEach((n) => {
    const art = opts.poster ? (n.coverPhoto || n.backgroundPhoto) : (n.backgroundPhoto || n.coverPhoto);
    const thumb = art ? `<img src="${thumbURL(art)}" alt="" loading="lazy">` : icon(n.hasChildren ? "folder" : "photo");
    const totalPhotos = n.totalPhotoCount != null ? n.totalPhotoCount : (n.photoCount || 0);
    const counts = [];
    if (n.hasChildren || (n.childCount || 0) > 0) counts.push(t("card.collectionCount", { n: n.childCount || 0 }));
    if (totalPhotos > 0 || (n.photoCount || 0) > 0) counts.push(t("meta.photos", { n: totalPhotos }));
    const row = el("div", {
      class: "node-list-row",
      onclick: () => navigate({ view: "node", libraryId, nodeId: n.id }),
      html: `<div class="node-list-thumb">${thumb}</div>
        <div class="node-list-info">
          <div class="node-list-name" title="${esc(n.name)}">${esc(n.name)}</div>
          <div class="node-list-meta">${esc(counts.join("  ·  "))}</div>
        </div>`,
    });
    list.appendChild(row);
  });
  return list;
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
  photoGrids = [];
  const rerenderNode = () => navigate({ view: "node", libraryId: route.libraryId, nodeId: route.nodeId });

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

    // Add this album's photos to a playlist.
    actions.push(el("button", {
      class: "btn icon-btn", title: t("playlist.addAlbum"), html: icon("playlist-add"),
      onclick: () => openPlaylistPicker(photos, node.name),
    }));
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
  meta.push(t("meta.photos", { n: node.totalPhotoCount != null ? node.totalPhotoCount : photos.length }));
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
    childCollections.forEach((c) => { if (c.favorite) state.favorites.add(c.id); });
    const block = el("div", { class: "lib-block" });
    block.appendChild(sectionHeader(t("node.collections"), [viewToggle("albumView", rerenderNode)]));
    if (prefs.albumView === "list") {
      block.appendChild(nodeListView(route.libraryId, childCollections));
    } else {
      block.appendChild(cardGrid(childCollections.map((c) => nodeCard(route.libraryId, c)), { variant: "landscape" }));
    }
    main.appendChild(block);
  }

  // Leaf albums (children holding photos) as poster cards.
  if (childAlbums.length > 0) {
    childAlbums.forEach((c) => { if (c.favorite) state.favorites.add(c.id); });
    const block = el("div", { class: "lib-block" });
    block.appendChild(sectionHeader(t("node.albums"), [viewToggle("albumView", rerenderNode)]));
    if (prefs.albumView === "list") {
      block.appendChild(nodeListView(route.libraryId, childAlbums, { poster: true, parent: node.name }));
    } else {
      const cards = childAlbums.map((c) => nodeCard(route.libraryId, c, { poster: true, parent: node.name }));
      block.appendChild(cardGrid(cards, { variant: "poster" }));
    }
    main.appendChild(block);
  }

  // The node's own photos below.
  if (hasPhotos) {
    state.currentAlbum = node;
    const block = el("div", { class: "lib-block" });
    const controls = [];
    if (prefs.photoView !== "list") controls.push(sizeSlider());
    controls.push(viewToggle("photoView", rerenderNode));
    block.appendChild(sectionHeader(t("node.photos"), controls));
    if (prefs.photoView === "list") {
      block.appendChild(photoListView(photos, (i) => openViewer(photos, i, node, false)));
    } else {
      block.appendChild(photoGridView(photos, (i) => openViewer(photos, i, node, false), { coverPath: node.coverPhoto }));
    }
    main.appendChild(block);
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
  // The "..." menu always offers "Add to playlist". Poster/background are admin
  // actions on a real album (not collection/library slideshows); "Remove from
  // playlist" only when viewing photos that came from a playlist.
  const realAlbum = state.user.isAdmin && !!(album && album.id && album.coverScope === undefined);
  const inPlaylist = !!(album && album.coverScope === "playlist" && album.id);
  state.currentPlaylistId = inPlaylist ? album.id : null;
  $("#viewer-set-poster").hidden = !realAlbum;
  $("#viewer-set-background").hidden = !realAlbum;
  $("#viewer-remove-playlist").hidden = !inPlaylist;
  $("#viewer-menu-wrap").hidden = false;
  closeViewerMenu();
  updateViewer();
  if (slideshow) startSlideshow(); else applyOverlay();
}

function updateViewer() {
  const p = state.photos[state.photoIndex];
  if (!p) return;
  const img = $("#viewer-img");
  if (viewerFadeTimer) { clearTimeout(viewerFadeTimer); viewerFadeTimer = null; }
  img.classList.remove("fade", "slide", "stretch", "scroll", "fit");
  img.style.removeProperty("--scroll-from");
  img.style.removeProperty("--scroll-to");
  img.style.removeProperty("--scroll-dur");
  // re-trigger transition animation
  void img.offsetWidth;

  // Photo fit mode (only active during slideshow; manual viewing always uses "fit").
  const mode = state.slideshowActive ? ssSettings.fitMode : "fit";
  if (mode === "fill") img.classList.add("stretch");
  else if (mode === "scroll") img.classList.add("scroll");
  else if (mode === "fit") img.classList.add("fit");

  // Swap the image and its caption/info to the current photo.
  const swap = () => {
    img.src = photoURL(p.path);
    if (mode === "scroll") setupAutoScroll(img);
    $("#viewer-caption").textContent = `${p.name}  (${state.photoIndex + 1}/${state.photos.length})`;
    if (state.infoOpen) loadInfo();
    applyOverlay();
  };

  if (state.slideshowActive && ssSettings.transition === "fade") {
    // Fade the previous frame out first, then swap once it's hidden so the
    // sequence reads as fade-out → change → fade-in (not change → fade).
    img.classList.add("fade");
    viewerFadeTimer = setTimeout(() => { viewerFadeTimer = null; swap(); }, VIEWER_FADE_HALF_MS);
  } else {
    if (state.slideshowActive && ssSettings.transition === "slide") img.classList.add("slide");
    swap();
  }
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
  closeViewerMenu();
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
  $("#viewer").classList.add("slideshow-running");
  $("#viewer-slideshow").innerHTML = `${icon("play")} ${esc(t("viewer.pause"))}`;
  updateViewer();
  state.slideshowTimer = setInterval(() => viewerStep(1), Math.max(1, ssSettings.interval) * 1000);
  scheduleHideControls();
}
function stopSlideshow() {
  if (state.slideshowTimer) { clearInterval(state.slideshowTimer); state.slideshowTimer = null; }
  if (viewerFadeTimer) { clearTimeout(viewerFadeTimer); viewerFadeTimer = null; }
  state.slideshowActive = false;
  $("#viewer").classList.remove("slideshow-running");
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

    // Extra "Details" section: place name and people, shown only when the
    // photo has indexed metadata available.
    const people = Array.isArray(info.people) ? info.people : [];
    if (info.place || people.length) {
      any = true;
      body.appendChild(el("div", { class: "info-section", text: t("info.details") }));
      if (info.place) {
        body.appendChild(el("div", { class: "info-row", html: `<span class="info-key">${esc(t("info.place"))}</span><span class="info-val">${esc(info.place)}</span>` }));
      }
      if (people.length) {
        const chips = people.map((name) => `<span class="info-chip">${esc(name)}</span>`).join("");
        body.appendChild(el("div", { class: "info-row", html: `<span class="info-key">${esc(t("info.people"))}</span><span class="info-chips">${chips}</span>` }));
      }
    }

    if (!any) body.appendChild(el("div", { class: "empty", text: t("info.noExif") }));
  } catch (e) {
    body.innerHTML = `<div class="empty">${esc(t("alert.error", { msg: e.message }))}</div>`;
  }
}

// ── viewer "..." menu (set poster / background) ──
function toggleViewerMenu() {
  const menu = $("#viewer-menu");
  if (menu.hidden) openViewerMenu(); else closeViewerMenu();
}

function openViewerMenu() {
  // Rebuild labels at open time so translations apply regardless of i18n load order.
  $("#viewer-set-poster").innerHTML = `${icon("star")} <span>${esc(t("viewer.setPoster"))}</span>`;
  $("#viewer-set-background").innerHTML = `${icon("image")} <span>${esc(t("viewer.setBackground"))}</span>`;
  $("#viewer-menu").hidden = false;
  $("#viewer-menu-btn").setAttribute("aria-expanded", "true");
}

function closeViewerMenu() {
  const menu = $("#viewer-menu");
  if (!menu) return;
  menu.hidden = true;
  $("#viewer-menu-btn").setAttribute("aria-expanded", "false");
}

// setCurrentArt sets the current photo as the album's poster (cover) or background.
// kind: "cover" | "background".
async function setCurrentArt(kind) {
  closeViewerMenu();
  const p = state.photos[state.photoIndex];
  if (!p || !state.currentAlbum) return;
  const item = kind === "background" ? $("#viewer-set-background") : $("#viewer-set-poster");
  const iconName = kind === "background" ? "image" : "star";
  const setLabel = kind === "background" ? t("viewer.setBackground") : t("viewer.setPoster");
  const doneLabel = kind === "background" ? t("viewer.backgroundSet") : t("viewer.posterSet");
  try {
    await api("/api/cover", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target: "node", id: state.currentAlbum.id, photo: p.path, kind }),
    });
    if (kind === "background") state.currentAlbum.backgroundPhoto = p.path;
    else state.currentAlbum.coverPhoto = p.path;
    item.innerHTML = `${icon(iconName)} <span>${esc(doneLabel)}</span>`;
    setTimeout(() => { item.innerHTML = `${icon(iconName)} <span>${esc(setLabel)}</span>`; }, 1500);
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
    interval: Math.min(3600, Math.max(2, parseFloat($("#ss-interval").value) || SS_DEFAULTS.interval)),
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
// grids. Like Plex, the picker does NOT dump every photo in the album: the grid
// starts with just the default (auto-generated, first-scanned) cover/background
// — surfaced by fillArtGrid from the current selection — plus any custom art the
// user explicitly adds via the "choose an image" button or by setting a photo as
// the cover from the viewer. So we intentionally return no listed photos here.
async function loadEntityPhotos(_entity) {
  return [];
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
  const select = el("select", { class: "control sm" });
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

// ── admin: settings save helper ──
async function saveAdminSetting(body) {
  try {
    await api("/api/admin/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── admin: scan CPU threads ──
async function buildScanThreadsPanel() {
  let workers = 1;
  try {
    const s = await api("/api/admin/settings");
    if (s.workerThreads) workers = s.workerThreads;
    else if (s.thumbnailWorkers) workers = s.thumbnailWorkers;
  } catch (e) { /* fall back to defaults */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.workerThreads"))}</div><div class="lib-path">${esc(t("admin.workerThreadsHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const workerSelect = el("select", { class: "control sm" });
  [1, 2, 3, 4, 6, 8].forEach((val) => {
    const opt = el("option", { text: t("admin.workerThreadsOption", { n: val }) });
    opt.value = String(val);
    if (val === workers) opt.selected = true;
    workerSelect.appendChild(opt);
  });
  workerSelect.addEventListener("change", (ev) => {
    saveAdminSetting({ workerThreads: parseInt(ev.target.value, 10) || 1 });
  });
  actions.appendChild(workerSelect);

  panel.appendChild(actions);
  return panel;
}

// ── admin: thumbnail resizing algorithm ──
async function buildThumbFilterPanel() {
  let filter = "lanczos";
  try {
    const s = await api("/api/admin/settings");
    if (s.thumbnailFilter) filter = s.thumbnailFilter;
  } catch (e) { /* fall back to defaults */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.thumbFilterTitle"))}</div><div class="lib-path">${esc(t("admin.thumbFilterHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const filterSelect = el("select", { class: "control sm" });
  ["lanczos", "catmullrom", "linear", "box", "nearest"].forEach((val) => {
    const opt = el("option", { text: t("admin.thumbFilter." + val) });
    opt.value = val;
    if (val === filter) opt.selected = true;
    filterSelect.appendChild(opt);
  });
  filterSelect.addEventListener("change", (ev) => {
    saveAdminSetting({ thumbnailFilter: ev.target.value });
  });
  actions.appendChild(filterSelect);

  panel.appendChild(actions);
  return panel;
}

// ── admin: reverse-geocoding mode ──
// Trades place-label precision against memory/CPU. "accurate" is intentionally
// heavy (high RAM, slow), which matters on small NAS hosts.
async function buildGeocodeModePanel() {
  let mode = "nearest";
  try {
    const s = await api("/api/admin/settings");
    if (s.geocodeMode) mode = s.geocodeMode;
  } catch (e) { /* fall back to defaults */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.geocodeMode"))}</div><div class="lib-path">${esc(t("admin.geocodeModeHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const modeSelect = el("select", { class: "control sm" });
  ["off", "nearest", "accurate"].forEach((val) => {
    const opt = el("option", { text: t("admin.geocodeMode." + val) });
    opt.value = val;
    if (val === mode) opt.selected = true;
    modeSelect.appendChild(opt);
  });
  modeSelect.addEventListener("change", (ev) => {
    saveAdminSetting({ geocodeMode: ev.target.value });
  });
  actions.appendChild(modeSelect);

  panel.appendChild(actions);
  return panel;
}

// ── admin: row item limit ──
// How many items load into each home/library/collection carousel row before
// the user scrolls. Lower keeps very large folders snappy.
async function buildRowLimitPanel() {
  let current = 16;
  try {
    const s = await api("/api/admin/settings");
    if (s.rowLimit) current = s.rowLimit;
  } catch (e) { /* fall back to default */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.rowLimit"))}</div><div class="lib-path">${esc(t("admin.rowLimitHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const select = el("select", { class: "control sm" });
  [8, 12, 16, 24, 32, 48].forEach((val) => {
    const opt = el("option", { text: String(val) });
    opt.value = String(val);
    if (val === current) opt.selected = true;
    select.appendChild(opt);
  });
  select.addEventListener("change", async (ev) => {
    const n = parseInt(ev.target.value, 10) || 16;
    try {
      await api("/api/admin/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ rowLimit: n }),
      });
      // Reflect immediately so carousels rendered this session pick it up.
      if (state.user) state.user.rowLimit = n;
    } catch (e) {
      alert(t("alert.error", { msg: e.message }));
    }
  });
  actions.appendChild(select);
  panel.appendChild(actions);
  return panel;
}

// How many scan timing reports to retain before older ones are pruned.
async function buildScanReportLimitPanel() {
  let current = 10;
  try {
    const s = await api("/api/admin/settings");
    if (s.scanReportLimit) current = s.scanReportLimit;
  } catch (e) { /* fall back to default */ }

  const panel = el("div", { class: "lib-row" });
  panel.appendChild(el("div", {
    html: `<div class="lib-name">${esc(t("admin.reportLimit"))}</div><div class="lib-path">${esc(t("admin.reportLimitHint"))}</div>`,
  }));

  const actions = el("div", { class: "lib-actions" });
  const select = el("select", { class: "control sm" });
  [5, 10, 15, 20, 30, 50].forEach((val) => {
    const opt = el("option", { text: String(val) });
    opt.value = String(val);
    if (val === current) opt.selected = true;
    select.appendChild(opt);
  });
  select.addEventListener("change", async (ev) => {
    const n = parseInt(ev.target.value, 10) || 10;
    try {
      await api("/api/admin/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scanReportLimit: n }),
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

  // ── Section 1: Libraries ──
  const libHeader = el("div", { class: "section-header" });
  libHeader.appendChild(el("div", { html: `<div class="section-title">${esc(t("admin.librariesTitle"))}</div><div class="section-sub">${esc(t("admin.librariesSub"))}</div>` }));
  libHeader.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("admin.newLibrary"))}`, onclick: () => openLibModal(null) }));
  main.appendChild(libHeader);

  if (libs.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("admin.noLibrary") }));
  } else {
    libs.forEach((lib) => {
      const row = el("div", { class: "lib-row" });
      row.appendChild(el("div", {
        html: `<div class="lib-name">${esc(lib.name)}</div><div class="lib-path">${esc(lib.rootPath)} &nbsp;·&nbsp; ${esc(t("meta.photos", { n: lib.photoCount || 0 }))}</div>`,
      }));
      const actions = el("div", { class: "lib-actions" });
      actions.appendChild(el("span", { class: "pill", text: t("meta.collections", { n: lib.collectionCount }) }));
      actions.appendChild(el("button", { class: "btn sm", html: `${icon("refresh")} ${esc(t("admin.scan"))}`, onclick: (ev) => scanLibrary(lib.id, ev.currentTarget) }));
      actions.appendChild(el("button", { class: "btn sm", title: t("admin.deepScanHint"), html: `${icon("refresh")} ${esc(t("admin.deepScan"))}`, onclick: (ev) => scanLibrary(lib.id, ev.currentTarget, true) }));
      actions.appendChild(el("button", { class: "btn sm", html: icon("edit"), onclick: () => openLibModal(lib) }));
      actions.appendChild(el("button", { class: "btn sm danger", html: icon("trash"), onclick: () => deleteLibrary(lib) }));
      row.appendChild(actions);
      main.appendChild(row);
    });
  }

  // ── Section 2: Library management (settings) ──
  const settingsHeader = el("div", { class: "section-header" });
  settingsHeader.appendChild(el("div", { html: `<div class="section-title">${esc(t("admin.settingsTitle"))}</div><div class="section-sub">${esc(t("admin.settingsSub"))}</div>` }));
  main.appendChild(settingsHeader);

  const settingsGroup = el("div", { class: "settings-group" });
  settingsGroup.appendChild(await buildAutoScanPanel());
  settingsGroup.appendChild(await buildScanThreadsPanel());
  settingsGroup.appendChild(await buildThumbFilterPanel());
  settingsGroup.appendChild(await buildGeocodeModePanel());
  settingsGroup.appendChild(await buildRowLimitPanel());
  settingsGroup.appendChild(await buildScanReportLimitPanel());
  main.appendChild(settingsGroup);
}

// ── admin: Frame TVs ──
async function renderAdminTVs(main) {
  main.innerHTML = "";
  const tvHeader = el("div", { class: "section-header" });
  tvHeader.appendChild(el("div", { html: `<div class="section-title">${esc(t("tv.adminTitle"))}</div><div class="section-sub">${esc(t("tv.adminSub"))}</div>` }));
  tvHeader.appendChild(el("button", { class: "btn accent", html: `${icon("plus")} ${esc(t("tv.addBtn"))}`, onclick: () => openTVModal(null) }));
  main.appendChild(tvHeader);
  await buildTVRows(main);
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
  const [errors, quarantine] = await Promise.all([
    api("/api/admin/errors"),
    api("/api/admin/quarantine").catch(() => []),
  ]);
  main.innerHTML = "";
  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("errors.title"))}</div><div class="section-sub">${esc(t("errors.subtitle"))}</div>` }));
  if (errors && errors.length) {
    header.appendChild(el("button", { class: "btn", html: `${icon("trash")} ${esc(t("errors.clear"))}`, onclick: () => clearErrorLog() }));
  }
  main.appendChild(header);

  if (!errors || errors.length === 0) {
    main.appendChild(el("div", { class: "empty", text: t("errors.none") }));
  } else {
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

  renderQuarantine(main, quarantine || []);
}

// renderQuarantine appends the "quarantined media" section: photos that could
// not be decoded (even after repair) and so are hidden from users until the
// source file is fixed and the photo released for a re-scan.
function renderQuarantine(main, items) {
  const header = el("div", { class: "section-header", style: "margin-top:28px" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("quarantine.title"))}</div><div class="section-sub">${esc(t("quarantine.subtitle"))}</div>` }));
  if (items.length) {
    header.appendChild(el("button", { class: "btn", html: `${icon("trash")} ${esc(t("quarantine.clear"))}`, onclick: () => clearQuarantine() }));
  }
  main.appendChild(header);

  if (!items.length) {
    main.appendChild(el("div", { class: "empty", text: t("quarantine.none") }));
    return;
  }

  items.forEach((q) => {
    const when = q.lastSeen ? new Date(q.lastSeen).toLocaleString() : "";
    const lib = q.libraryName || q.libraryId || "—";
    const row = el("div", { class: "lib-row" });
    row.appendChild(el("div", {
      html: `<div class="lib-name">${esc(lib)} <span class="pill">${esc(q.phase || "")}</span></div>` +
        `<div class="lib-path">${esc(q.photoPath)}</div>` +
        `<div class="lib-path err-msg">${esc(q.reason)}</div>`,
    }));
    row.appendChild(el("div", { class: "lib-actions" }, [
      el("span", { class: "section-sub", text: when }),
      el("button", { class: "btn sm", text: t("quarantine.release"), onclick: () => releaseQuarantine(q.photoPath) }),
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

async function releaseQuarantine(path) {
  try {
    await api("/api/admin/quarantine/release", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path }) });
    await renderErrorLog($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

async function clearQuarantine() {
  if (!confirm(t("quarantine.confirmClear"))) return;
  try {
    await api("/api/admin/quarantine", { method: "DELETE" });
    await renderErrorLog($("#main"));
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
}

// ── admin: jobs ──
let jobsPollTimer = null;

async function renderJobs(main) {
  if (jobsPollTimer) { clearTimeout(jobsPollTimer); jobsPollTimer = null; }
  main.innerHTML = "";

  const header = el("div", { class: "section-header" });
  header.appendChild(el("div", { html: `<div class="section-title">${esc(t("jobs.title"))}</div><div class="section-sub">${esc(t("jobs.subtitle"))}</div>` }));
  main.appendChild(header);

  const list = el("div", { id: "jobs-list" });
  main.appendChild(list);

  // Scan timing reports tied to a visible job are shown inline on that job row
  // (see buildJobRow). This section only holds "orphan" reports: auto-scans,
  // which have no job, and reports whose job has been pruned from history.
  const reportsHeader = el("div", { class: "section-header", id: "reports-header", style: "margin-top:28px" });
  reportsHeader.appendChild(el("div", {
    html: `<div class="section-title">${esc(t("reports.title"))}</div><div class="section-sub">${esc(t("reports.subtitle"))}</div>`,
  }));
  main.appendChild(reportsHeader);
  const reportsList = el("div", { id: "reports-list" });
  main.appendChild(reportsList);

  await refreshJobs(list);
}

// renderOrphanReports fills the standalone reports section with reports that are
// not attached to a visible job row, hiding the section entirely when empty.
function renderOrphanReports(reports) {
  const list = document.getElementById("reports-list");
  const header = document.getElementById("reports-header");
  if (!list) return;
  list.innerHTML = "";
  if (!reports || reports.length === 0) {
    if (header) header.style.display = "none";
    return;
  }
  if (header) header.style.display = "";
  reports.forEach((r) => list.appendChild(buildReportRow(r)));
}

function buildReportRow(r) {
  const row = el("div", { class: "lib-row" });
  let statusPill;
  if (r.status === "success") statusPill = `<span class="pill pill-ok">${esc(t("jobs.success"))}</span>`;
  else if (r.status === "canceled") statusPill = `<span class="pill">${esc(t("reports.canceled"))}</span>`;
  else statusPill = `<span class="pill pill-danger">${esc(t("jobs.failed"))}</span>`;

  const left = el("div");
  left.appendChild(el("div", {
    class: "lib-name",
    html: `${esc(r.libraryName || "—")} ${statusPill}`,
  }));
  const when = r.finishedAt ? new Date(r.finishedAt).toLocaleString()
    : (r.startedAt ? new Date(r.startedAt).toLocaleString() : "");
  left.appendChild(el("div", { class: "lib-path", text: when }));
  row.appendChild(left);

  const actions = el("div", { class: "lib-actions" });
  actions.appendChild(el("button", {
    class: "btn sm",
    text: t("reports.view"),
    onclick: () => openReportModal(r.id),
  }));
  row.appendChild(actions);
  return row;
}

// fmtMs renders a millisecond value as a compact, human-readable duration.
function fmtMs(ms) {
  if (ms == null) return "—";
  if (ms >= 60000) {
    const m = Math.floor(ms / 60000);
    const s = Math.round((ms % 60000) / 1000);
    return `${m}m ${s}s`;
  }
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
  if (ms >= 10) return `${Math.round(ms)} ms`;
  return `${ms.toFixed(1)} ms`;
}

function reportTaskLabel(key) {
  const label = t("reports.task." + key);
  return label === "reports.task." + key ? key : label;
}

function reportPhaseLabel(name) {
  const label = t("jobs.phase." + name);
  return label === "jobs.phase." + name ? name : label;
}

async function openReportModal(id) {
  let rec;
  try {
    rec = await api(`/api/admin/reports/${encodeURIComponent(id)}`);
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
    return;
  }
  const rep = rec.report || {};
  const overlay = el("div", { class: "modal" });
  const card = el("div", { class: "modal-card", style: "max-width:720px;width:92vw;max-height:88vh;overflow:auto" });

  const statusKey = rec.status === "success" ? "jobs.success" : (rec.status === "canceled" ? "reports.canceled" : "jobs.failed");
  card.appendChild(el("div", { class: "modal-title", text: `${rec.libraryName || "—"} · ${t(statusKey)}` }));

  const counts = rep.counts || {};
  const summary = [
    [t("reports.total"), fmtMs(rep.wallMs)],
    [t("reports.photos"), counts.photos || 0],
    [t("reports.thumbsGenerated"), counts.thumbsGenerated || 0],
    [t("reports.thumbsSkipped"), counts.thumbsSkipped || 0],
    [t("reports.metaIndexed"), counts.metaIndexed || 0],
    [t("reports.metaSkipped"), counts.metaSkipped || 0],
  ];
  if (counts.thumbsQuarantined) {
    summary.push([t("reports.thumbsQuarantined"), counts.thumbsQuarantined]);
  }
  const summaryWrap = el("div", { class: "report-summary" });
  summary.forEach(([label, val]) => {
    summaryWrap.appendChild(el("div", {
      class: "report-stat",
      html: `<div class="report-stat-val">${esc(String(val))}</div><div class="report-stat-label">${esc(label)}</div>`,
    }));
  });
  card.appendChild(summaryWrap);

  // Per-task measurements table.
  if (rep.tasks && rep.tasks.length) {
    card.appendChild(el("div", { class: "report-section-title", text: t("reports.tasks") }));
    card.appendChild(buildReportTable(
      [t("reports.col.task"), t("reports.col.count"), t("reports.col.total"), t("reports.col.avg"), t("reports.col.min"), t("reports.col.max")],
      rep.tasks.map((tk) => [reportTaskLabel(tk.key), String(tk.count), fmtMs(tk.totalMs), fmtMs(tk.avgMs), fmtMs(tk.minMs), fmtMs(tk.maxMs)]),
    ));
  }

  // Per-phase wall-clock table.
  if (rep.phases && rep.phases.length) {
    card.appendChild(el("div", { class: "report-section-title", text: t("reports.phases") }));
    card.appendChild(buildReportTable(
      [t("reports.col.phase"), t("reports.col.total")],
      rep.phases.map((ph) => [reportPhaseLabel(ph.name), fmtMs(ph.wallMs)]),
    ));
  }

  // Capped error excerpt.
  const errs = rep.errors || {};
  if (errs.total > 0) {
    card.appendChild(el("div", { class: "report-section-title", text: `${t("reports.errors")} (${errs.total})` }));
    const errList = el("div", { class: "report-errors" });
    (errs.items || []).forEach((e) => {
      errList.appendChild(el("div", {
        class: "report-error",
        html: `<span class="pill">${esc(reportPhaseLabel(e.phase))}</span> <span class="report-error-item">${esc(e.item)}</span><div class="report-error-msg">${esc(e.msg)}</div>`,
      }));
    });
    if (errs.truncated) {
      errList.appendChild(el("div", { class: "report-error-more", text: t("reports.moreErrors", { n: errs.total - (errs.items || []).length }) }));
    }
    card.appendChild(errList);
  }

  const acts = el("div", { class: "modal-actions" });
  acts.appendChild(el("button", { class: "btn", text: t("common.close"), onclick: () => overlay.remove() }));
  card.appendChild(acts);

  overlay.appendChild(card);
  overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
  document.body.appendChild(overlay);
}

function buildReportTable(headers, rows) {
  const table = el("table", { class: "report-table" });
  const thead = el("thead");
  const htr = el("tr");
  headers.forEach((h, i) => htr.appendChild(el("th", { text: h, class: i === 0 ? "" : "num" })));
  thead.appendChild(htr);
  table.appendChild(thead);
  const tbody = el("tbody");
  rows.forEach((cells) => {
    const tr = el("tr");
    cells.forEach((c, i) => tr.appendChild(el("td", { text: c, class: i === 0 ? "" : "num" })));
    tbody.appendChild(tr);
  });
  table.appendChild(tbody);
  return table;
}

async function refreshJobs(list) {
  let jobs, reports;
  try {
    // Reports are fetched alongside jobs so each finished job row can link to
    // its own timing report; the rest fall into the orphan-reports section.
    [jobs, reports] = await Promise.all([
      api("/api/admin/jobs"),
      api("/api/admin/reports"),
    ]);
  } catch (e) {
    list.innerHTML = `<div class="empty">${esc(t("alert.error", { msg: e.message }))}</div>`;
    return;
  }
  // Bail out if the user navigated away while the request was in flight.
  if (!document.body.contains(list)) return;

  const reportByJob = new Map();
  (reports || []).forEach((r) => { if (r.jobId) reportByJob.set(String(r.jobId), r); });

  list.innerHTML = "";
  let anyRunning = false;
  if (!jobs || jobs.length === 0) {
    list.appendChild(el("div", { class: "empty", text: t("jobs.none") }));
  } else {
    jobs.forEach((j) => {
      if (j.status === "running") anyRunning = true;
      list.appendChild(buildJobRow(j, reportByJob.get(String(j.id))));
    });
  }

  // Reports without a visible job row (auto-scans, or jobs pruned from history)
  // keep their own list below.
  const jobIds = new Set((jobs || []).map((j) => String(j.id)));
  renderOrphanReports((reports || []).filter((r) => !r.jobId || !jobIds.has(String(r.jobId))));

  // Poll while a job is active so progress updates live.
  if (anyRunning) {
    jobsPollTimer = setTimeout(() => refreshJobs(list), 1000);
  }
}

function jobTypeLabel(type) {
  if (type === "scan") return t("jobs.typeScan");
  if (type === "thumbnails") return t("jobs.typeThumbnails");
  if (type === "cleanup") return t("jobs.typeCleanup");
  if (type === "playlist-add") return t("jobs.typePlaylistAdd");
  return type;
}

function buildJobRow(j, report) {
  const row = el("div", { class: "lib-row" });
  const target = j.target || "—";
  const left = el("div");

  let statusPill;
  if (j.status === "running") {
    statusPill = j.paused
      ? `<span class="pill">${esc(t("jobs.paused"))}</span>`
      : `<span class="pill">${esc(t("jobs.running"))}</span>`;
  } else if (j.status === "success") {
    statusPill = `<span class="pill pill-ok">${esc(t("jobs.success"))}</span>`;
  } else {
    statusPill = `<span class="pill pill-danger">${esc(t("jobs.failed"))}</span>`;
  }

  left.appendChild(el("div", {
    class: "lib-name",
    html: `${esc(jobTypeLabel(j.type))} · ${esc(target)} ${statusPill}`,
  }));

  if (j.status === "running") {
    const total = j.total || 0;
    const done = j.done || 0;
    const pct = total > 0 ? Math.round((done / total) * 100) : 0;
    const phase = j.phase ? `${esc(t("jobs.phase." + j.phase) || j.phase)} · ` : "";
    const detail = total > 0 ? `${phase}${done}/${total} (${pct}%)` : (j.phase || t("jobs.running"));
    const bar = el("div", { class: "job-progress" });
    bar.appendChild(el("div", { class: "job-progress-bar", style: `width:${pct}%` }));
    left.appendChild(el("div", { class: "lib-path", text: detail }));
    left.appendChild(bar);
    if (j.current) {
      left.appendChild(el("div", { class: "lib-path", text: `${t("jobs.current")}: ${j.current}` }));
    }
  } else if (j.status === "failed" && j.message) {
    left.appendChild(el("div", { class: "lib-path err-msg", text: j.message }));
  }
  row.appendChild(left);

  const when = j.finishedAt ? new Date(j.finishedAt).toLocaleString()
    : (j.startedAt ? new Date(j.startedAt).toLocaleString() : "");
  const rightActions = el("div", { class: "lib-actions" }, [
    el("span", { class: "section-sub", text: when }),
  ]);
  if (j.status === "running") {
    if (j.paused) {
      rightActions.appendChild(el("button", {
        class: "btn sm accent",
        html: `${icon("play")} ${esc(t("jobs.resume"))}`,
        onclick: (ev) => pauseResumeJob(j.id, "resume", ev.currentTarget),
      }));
    } else {
      rightActions.appendChild(el("button", {
        class: "btn sm",
        html: `${icon("pause")} ${esc(t("jobs.pause"))}`,
        onclick: (ev) => pauseResumeJob(j.id, "pause", ev.currentTarget),
      }));
    }
    rightActions.appendChild(el("button", {
      class: "btn sm danger",
      html: `${icon("stop")} ${esc(t("jobs.cancel"))}`,
      onclick: (ev) => cancelJob(j.id, ev.currentTarget),
    }));
  } else if (report) {
    // Finished job with a stored timing report: surface it inline.
    rightActions.appendChild(el("button", {
      class: "btn sm",
      text: t("reports.view"),
      onclick: () => openReportModal(report.id),
    }));
  }
  row.appendChild(rightActions);
  return row;
}

async function cancelJob(id, btn) {
  if (!confirm(t("jobs.cancelConfirm"))) return;
  if (btn) { btn.disabled = true; btn.textContent = t("jobs.canceling"); }
  try {
    await api(`/api/admin/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST" });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
    if (btn) { btn.disabled = false; }
    return;
  }
  // Refresh promptly so the row flips to "failed · interrupted by user".
  const list = document.getElementById("jobs-list");
  if (list) await refreshJobs(list);
}

// pauseResumeJob holds (pause) or continues (resume) the running job without
// ending it, then refreshes so the row flips between its Pause/Resume states.
async function pauseResumeJob(id, action, btn) {
  if (btn) { btn.disabled = true; }
  try {
    await api(`/api/admin/jobs/${encodeURIComponent(id)}/${action}`, { method: "POST" });
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
    if (btn) { btn.disabled = false; }
    return;
  }
  const list = document.getElementById("jobs-list");
  if (list) await refreshJobs(list);
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

  const select = el("select", { class: "control sm" });
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

  // Thumbnail size as a slider (same control offered inline on album pages).
  const sizeWrap = el("div", { class: "size-slider size-slider--settings", title: t("view.size") });
  const sizeInput = el("input", {
    type: "range", min: 0, max: SIZE_STOPS.length - 1, step: 1,
    value: sizeIndex(), "aria-label": t("view.size"),
  });
  sizeInput.addEventListener("input", () => { prefs.photoSize = sizeFromIndex(sizeInput.value); resizePhotoGrids(); });
  sizeInput.addEventListener("change", () => savePrefs({ ...prefs, photoSize: sizeFromIndex(sizeInput.value) }));
  sizeWrap.appendChild(el("span", { class: "size-slider-icon size-slider-icon--sm", html: icon("photo") }));
  sizeWrap.appendChild(sizeInput);
  sizeWrap.appendChild(el("span", { class: "size-slider-icon size-slider-icon--lg", html: icon("photo") }));
  generalRow("settings.density", "settings.density.desc", sizeWrap);

  const albumView = el("select", { class: "control sm" });
  [["grid", t("view.grid")], ["list", t("view.list")]].forEach(([val, label]) => {
    albumView.appendChild(el("option", { value: val, text: label }));
  });
  albumView.value = prefs.albumView;
  albumView.addEventListener("change", () => updatePref("albumView", albumView.value));
  generalRow("settings.albumView", "settings.albumView.desc", albumView);

  const photoView = el("select", { class: "control sm" });
  [["grid", t("view.grid")], ["list", t("view.list")]].forEach(([val, label]) => {
    photoView.appendChild(el("option", { value: val, text: label }));
  });
  photoView.value = prefs.photoView;
  photoView.addEventListener("change", () => updatePref("photoView", photoView.value));
  generalRow("settings.photoView", "settings.photoView.desc", photoView);

  const sort = el("select", { class: "control sm" });
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

  // Duration per photo: 2s minimum, 3600s (60 min) maximum.
  const interval = el("input", { class: "control sm", type: "number", min: "2", max: "3600", step: "1", value: ssSettings.interval });
  interval.addEventListener("change", () => {
    let v = parseFloat(interval.value) || SS_DEFAULTS.interval;
    v = Math.min(3600, Math.max(2, v));
    interval.value = v;
    updateSetting("interval", v);
  });
  settingRow("ss.duration", "settings.slideshow.duration.desc", interval);

  const transition = el("select", { class: "control sm" });
  [["none", t("ss.transition.none")], ["fade", t("ss.transition.fade")], ["slide", t("ss.transition.slide")]].forEach(([val, label]) => {
    transition.appendChild(el("option", { value: val, text: label }));
  });
  transition.value = ssSettings.transition;
  transition.addEventListener("change", () => updateSetting("transition", transition.value));
  settingRow("ss.transition", "settings.slideshow.transition.desc", transition);

  const fit = el("select", { class: "control sm" });
  [["fit", t("ss.fit.fit")], ["fill", t("ss.fit.fill")], ["scroll", t("ss.fit.scroll")]].forEach(([val, label]) => {
    fit.appendChild(el("option", { value: val, text: label }));
  });
  fit.value = ssSettings.fitMode;
  fit.addEventListener("change", () => updateSetting("fitMode", fit.value));
  settingRow("ss.fit", "settings.slideshow.fit.desc", fit);

  const toggle = (key) => {
    const cb = el("input", { class: "control", type: "checkbox" });
    cb.checked = !!ssSettings[key];
    cb.addEventListener("change", () => updateSetting(key, cb.checked));
    return cb;
  };
  settingRow("ss.loop", "settings.slideshow.loop.desc", toggle("loop"));
  settingRow("ss.showInfo", "settings.slideshow.showInfo.desc", toggle("showInfo"));
  settingRow("settings.slideshow.shuffle", "settings.slideshow.shuffle.desc", toggle("shuffle"));
  settingRow("settings.slideshow.autostart", "settings.slideshow.autostart.desc", toggle("autostart"));

  const hideDelay = el("input", { class: "control sm", type: "number", min: "1", max: "30", step: "1", value: ssSettings.hideDelay });
  hideDelay.addEventListener("change", () => {
    const v = parseInt(hideDelay.value, 10) || SS_DEFAULTS.hideDelay;
    hideDelay.value = v;
    updateSetting("hideDelay", v);
  });
  settingRow("settings.slideshow.hideDelay", "settings.slideshow.hideDelay.desc", hideDelay);

  main.appendChild(group);
}

async function scanLibrary(id, btn, deep) {
  const original = btn.innerHTML;
  btn.innerHTML = `${icon("refresh")} …`;
  btn.disabled = true;
  try {
    const url = deep
      ? `/api/admin/libraries/${id}/scan?deep=1`
      : `/api/admin/libraries/${id}/scan`;
    await api(url, { method: "POST" });
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
  const title = bar.querySelector(".scan-banner-title");
  const sub = bar.querySelector(".scan-banner-sub");
  const fill = bar.querySelector(".scan-banner-fill");

  if (p.phase === "thumbnails") {
    // Phase 2: generating missing thumbnails for the whole library.
    title.textContent = t("scan.thumbnails");
    sub.textContent = t("scan.thumbs", { cur: p.thumbDone || 0, total: p.thumbTotal || 0 });
    const pct = p.thumbTotal ? Math.round((p.thumbDone / p.thumbTotal) * 100) : 100;
    fill.style.width = pct + "%";
    return;
  }

  if (p.phase === "metadata") {
    // Phase 3: indexing per-photo metadata (EXIF / sidecar / geocode).
    title.textContent = t("scan.metadata");
    sub.textContent = t("scan.thumbs", { cur: p.metaDone || 0, total: p.metaTotal || 0 });
    const pct = p.metaTotal ? Math.round((p.metaDone / p.metaTotal) * 100) : 100;
    fill.style.width = pct + "%";
    return;
  }

  if (p.phase === "cleanup") {
    // Deep scan only: removing orphaned thumbnails.
    title.textContent = t("scan.cleanup");
    sub.textContent = t("scan.thumbs", { cur: p.cleanupDone || 0, total: p.cleanupTotal || 0 });
    const pct = p.cleanupTotal ? Math.round((p.cleanupDone / p.cleanupTotal) * 100) : 100;
    fill.style.width = pct + "%";
    return;
  }

  // Phase 1: indexing the directory tree.
  title.textContent = t("scan.scanning");
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
// Click on the dark backdrop (anywhere that isn't the photo or a control) closes
// the viewer, matching the behaviour of typical web lightboxes.
$("#viewer").addEventListener("click", (e) => {
  if (e.target.closest(".viewer-img, .viewer-nav, .viewer-close, .viewer-toolbar, .viewer-info, .viewer-menu, .viewer-overlay")) return;
  closeViewer();
});
$("#viewer-prev").addEventListener("click", () => { stopSlideshow(); viewerStep(-1); });
$("#viewer-next").addEventListener("click", () => { stopSlideshow(); viewerStep(1); });
$("#viewer-slideshow").addEventListener("click", () => { if (state.slideshowActive) stopSlideshow(); else startSlideshow(); });
$("#viewer-menu-btn").addEventListener("click", (e) => { e.stopPropagation(); toggleViewerMenu(); });
$("#viewer-set-poster").addEventListener("click", () => setCurrentArt("cover"));
$("#viewer-set-background").addEventListener("click", () => setCurrentArt("background"));
$("#viewer-add-playlist").addEventListener("click", () => {
  closeViewerMenu();
  const p = state.photos[state.photoIndex];
  if (p) openPlaylistPicker([{ name: p.name, path: p.path }]);
});
$("#viewer-remove-playlist").addEventListener("click", async () => {
  closeViewerMenu();
  const p = state.photos[state.photoIndex];
  if (!p || !state.currentPlaylistId) return;
  const playlistId = state.currentPlaylistId;
  await removeFromPlaylist(playlistId, p.path);
  // Drop it from the in-memory list and keep the viewer on a valid photo.
  const idx = state.photos.findIndex((x) => x.path === p.path);
  if (idx >= 0) state.photos.splice(idx, 1);
  if (state.photos.length === 0) {
    closeViewer();
    navigate({ view: "playlist", playlistId });
    return;
  }
  state.photoIndex = state.photoIndex % state.photos.length;
  updateViewer();
});
document.addEventListener("click", (e) => {
  if (!$("#viewer-menu").hidden && !e.target.closest("#viewer-menu-wrap")) closeViewerMenu();
});
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

// Frame TV admin modal (identity only; display options live on the TV page)
$("#tv-cancel").addEventListener("click", closeTVModal);
$("#tv-save").addEventListener("click", saveTV);
$("#tv-test").addEventListener("click", testTV);

// playlist modals (create/rename + add-to-playlist picker)
$("#playlist-cancel").addEventListener("click", closePlaylistModal);
$("#playlist-save").addEventListener("click", savePlaylistName);
$("#playlist-name").addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); savePlaylistName(); } });
$("#playlist-pick-cancel").addEventListener("click", closePlaylistPicker);
$("#playlist-pick-new").addEventListener("click", async () => {
  const name = prompt(t("playlist.newPrompt"));
  if (!name || !name.trim()) return;
  try {
    const pl = await api("/api/playlists", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: name.trim() }) });
    await addPhotosToPlaylist(pl.id, pickerPhotos);
  } catch (e) {
    alert(t("alert.error", { msg: e.message }));
  }
});

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
  // A modal (e.g. the add-to-playlist picker) can sit above the viewer; let it
  // own keyboard input instead of closing/navigating the viewer underneath.
  if (document.querySelector(".modal:not([hidden])")) {
    if (e.key === "Escape") { closePlaylistPicker(); closePlaylistModal(); }
    return;
  }
  if (e.key === "Escape") closeViewer();
  else if (e.key === "ArrowLeft") { stopSlideshow(); viewerStep(-1); }
  else if (e.key === "ArrowRight") { stopSlideshow(); viewerStep(1); }
});

// ── sidebar collapse (Plex-style burger toggle) ──
(function setupSidebarToggle() {
  const KEY = "sidebar-collapsed";
  const btn = $("#sidebar-toggle");
  if (!btn) return;

  function apply(collapsed) {
    document.body.classList.toggle("app-collapsed", collapsed);
    btn.setAttribute("aria-pressed", String(collapsed));
    const label = collapsed ? t("nav.expand") : t("nav.collapse");
    btn.title = label;
    btn.setAttribute("aria-label", label);
  }

  apply(localStorage.getItem(KEY) === "1");

  btn.addEventListener("click", () => {
    const collapsed = !document.body.classList.contains("app-collapsed");
    localStorage.setItem(KEY, collapsed ? "1" : "0");
    apply(collapsed);
  });
})();

// ── global search (albums & collections) ──
(function setupSearch() {
  const input = $("#search-input");
  const panel = $("#search-results");
  if (!input || !panel) return;

  let timer = null;
  let lastQuery = "";
  let activeIdx = -1;
  let results = [];

  function close() {
    panel.hidden = true;
    activeIdx = -1;
  }

  function go(node) {
    close();
    input.blur();
    navigate({ view: "node", libraryId: node.libraryId, nodeId: node.id });
  }

  function render() {
    panel.innerHTML = "";
    if (results.length === 0) {
      panel.appendChild(el("div", { class: "search-empty", text: t("search.noResults") }));
      panel.hidden = false;
      return;
    }
    results.forEach((node, i) => {
      const cover = node.coverPhoto
        ? el("img", { class: "search-result-thumb", src: thumbURL(node.coverPhoto), alt: "", loading: "lazy" })
        : el("div", { class: "search-result-thumb placeholder", html: icon("layout-grid") });
      const sub = node.childCount > 0
        ? t("search.collection", { n: node.childCount })
        : t("search.album", { n: node.totalPhotoCount != null ? node.totalPhotoCount : (node.photoCount || 0) });
      const item = el("div", {
        class: "search-result-item" + (i === activeIdx ? " active" : ""),
        "data-idx": i,
        onclick: () => go(node),
      }, [
        cover,
        el("div", { class: "search-result-meta" }, [
          el("div", { class: "search-result-name", text: node.name }),
          el("div", { class: "search-result-sub", text: sub }),
        ]),
      ]);
      panel.appendChild(item);
    });
    panel.hidden = false;
  }

  async function run(q) {
    try {
      results = await api("/api/search?q=" + encodeURIComponent(q));
    } catch (_) {
      results = [];
    }
    activeIdx = -1;
    if (input.value.trim() === q) render();
  }

  input.addEventListener("input", () => {
    const q = input.value.trim();
    clearTimeout(timer);
    if (q === "") { lastQuery = ""; close(); return; }
    if (q === lastQuery) return;
    lastQuery = q;
    panel.innerHTML = `<div class="search-loading">${esc(t("common.loading"))}</div>`;
    panel.hidden = false;
    timer = setTimeout(() => run(q), 250);
  });

  input.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { close(); input.blur(); return; }
    if (panel.hidden || results.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = (activeIdx + 1) % results.length;
      render();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = (activeIdx - 1 + results.length) % results.length;
      render();
    } else if (e.key === "Enter") {
      if (activeIdx >= 0 && results[activeIdx]) go(results[activeIdx]);
      else if (results[0]) go(results[0]);
    }
  });

  input.addEventListener("focus", () => {
    if (results.length > 0 && input.value.trim() !== "") render();
  });

  document.addEventListener("click", (e) => {
    if (!e.target.closest(".topbar-search")) close();
  });
})();

applyStaticTranslations();
boot();
