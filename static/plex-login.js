"use strict";
// Client-side Plex PIN login (popup + poll), mirroring the approach used by
// apps like Overseerr. The browser asks plex.tv for a PIN, opens the Plex auth
// page in a popup, and polls plex.tv until the PIN yields an authToken. That
// token is then posted to our backend for validation. No server-side callback
// URL is involved, so login works regardless of how the app was reached
// (localhost, LAN IP, reverse proxy, ...).
(function () {
  const PIN_URL = "https://plex.tv/api/v2/pins?strong=true";
  const AUTH_URL = "https://app.plex.tv/auth/#!?";
  const CLIENT_ID_KEY = "pp-plex-client-id";

  // A stable per-browser client identifier, persisted in localStorage.
  function clientId() {
    let id = localStorage.getItem(CLIENT_ID_KEY);
    if (!id) {
      id =
        typeof crypto !== "undefined" && crypto.randomUUID
          ? crypto.randomUUID()
          : "pp-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
      localStorage.setItem(CLIENT_ID_KEY, id);
    }
    return id;
  }

  function headers(product) {
    return {
      Accept: "application/json",
      "X-Plex-Product": product,
      "X-Plex-Client-Identifier": clientId(),
    };
  }

  function encode(params) {
    return Object.keys(params)
      .map((k) => [k, params[k]].map(encodeURIComponent).join("="))
      .join("&");
  }

  async function getProduct() {
    try {
      const res = await fetch("/api/auth/plex/config");
      if (res.ok) {
        const data = await res.json();
        if (data && data.product) return data.product;
      }
    } catch (_) {}
    return "plex-photos";
  }

  async function getPin(product) {
    const res = await fetch(PIN_URL, { method: "POST", headers: headers(product) });
    if (!res.ok) throw new Error("Could not start Plex sign-in (" + res.status + ").");
    const data = await res.json();
    return { id: data.id, code: data.code };
  }

  // Poll plex.tv for the auth token until it appears or we time out.
  //
  // We intentionally do NOT abort when the popup reports `closed`. After the
  // popup navigates to app.plex.tv (a cross-origin document), Cross-Origin
  // -Opener-Policy can sever the opener relationship, making `popup.closed`
  // unreliable (it may read true even while the user is still signing in, or
  // Plex may auto-close the window on success before our next poll lands). So
  // popup state is only used as a soft hint; the PIN itself is the source of
  // truth and we keep polling until it yields a token or expires.
  function pollToken(pin, product, popup) {
    const POLL_INTERVAL_MS = 1000;
    const TIMEOUT_MS = 5 * 60 * 1000; // PINs are valid ~15min; cap our wait.
    const deadline = Date.now() + TIMEOUT_MS;
    let popupClosedAt = 0;

    return new Promise((resolve, reject) => {
      const tick = async () => {
        try {
          // Per Plex's documented polling flow, include the PIN `code`
          // alongside the client identifier when checking the PIN status.
          const url =
            "https://plex.tv/api/v2/pins/" +
            pin.id +
            "?" +
            encode({ code: pin.code });
          const res = await fetch(url, { headers: headers(product) });
          const data = await res.json();
          console.log("[plex-login] poll", res.status, "authToken?", !!(data && data.authToken));
          if (data && data.authToken) {
            resolve(data.authToken);
            return;
          }

          // Soft cancellation: only give up if the popup has been closed for a
          // grace period AND the PIN still has no token. This avoids the race
          // where Plex closes the popup on success a moment before the token
          // becomes readable on the PIN.
          if (popup && popup.closed) {
            if (!popupClosedAt) popupClosedAt = Date.now();
            if (Date.now() - popupClosedAt > 3000) {
              reject(new Error("Sign-in was cancelled."));
              return;
            }
          } else {
            popupClosedAt = 0;
          }

          if (Date.now() > deadline) {
            reject(new Error("Sign-in timed out. Please try again."));
            return;
          }
          setTimeout(tick, POLL_INTERVAL_MS);
        } catch (e) {
          // Transient network/CORS errors shouldn't kill the flow; retry until
          // the deadline.
          if (Date.now() > deadline) {
            reject(e);
            return;
          }
          setTimeout(tick, POLL_INTERVAL_MS);
        }
      };
      tick();
    });
  }

  function centeredPopup(w, h) {
    const left = window.screenX + Math.max(0, (window.outerWidth - w) / 2);
    const top = window.screenY + Math.max(0, (window.outerHeight - h) / 2);
    // Open synchronously (in the click handler) to avoid popup blockers; the
    // real auth URL is set once the PIN is ready.
    return window.open(
      "about:blank",
      "plex-auth",
      "scrollbars=yes,width=" + w + ",height=" + h + ",top=" + top + ",left=" + left
    );
  }

  const log = (...args) => console.log("[plex-login]", ...args);

  async function login(setStatus) {
    const popup = centeredPopup(600, 700);
    try {
      const product = await getProduct();
      log("product", product, "clientID", clientId());
      const pin = await getPin(product);
      log("pin created", pin);

      const params = {
        clientID: clientId(),
        code: pin.code,
        "context[device][product]": product,
        "context[device][deviceName]": product,
      };
      const authUrl = AUTH_URL + encode(params);
      if (popup) popup.location.href = authUrl;
      else window.location.href = authUrl; // popup blocked: fall back to redirect
      log("auth url set, popup?", !!popup);

      setStatus && setStatus("Waiting for Plex sign-in\u2026");
      const token = await pollToken(pin, product, popup);
      log("token received", token ? token.slice(0, 6) + "\u2026" : token);
      if (popup && !popup.closed) popup.close();

      const res = await fetch("/api/auth/plex/exchange", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      let data = {};
      try { data = await res.json(); } catch (_) {}
      log("exchange status", res.status, data);
      if (!res.ok) throw new Error(data.error || "Sign-in failed.");
    } catch (e) {
      log("login failed", e);
      if (popup && !popup.closed) popup.close();
      throw e;
    }
  }

  function init() {
    const btn = document.getElementById("plex-login-btn");
    if (!btn) return;
    const label = document.getElementById("plex-login-label");
    const errEl = document.getElementById("login-error");
    const baseLabel = label ? label.textContent : "Sign in with Plex";

    function setStatus(text) {
      if (label && text) label.textContent = text;
    }
    function showError(msg) {
      if (!errEl) return;
      errEl.textContent = msg || "";
      errEl.hidden = !msg;
    }

    btn.addEventListener("click", async () => {
      btn.disabled = true;
      showError("");
      setStatus("Connecting\u2026");
      try {
        // In mock/dev mode the Plex PIN popup flow doesn't apply; the backend
        // logs us in via a simple redirect through /auth/login.
        let provider = "plex";
        try {
          const res = await fetch("/api/auth/info");
          if (res.ok) provider = (await res.json()).provider || "plex";
        } catch (_) {}
        if (provider === "mock") {
          window.location.href = "/auth/login";
          return;
        }
        await login(setStatus);
        window.location.reload();
      } catch (e) {
        showError(e.message || "Sign-in failed.");
        if (label) label.textContent = baseLabel;
        btn.disabled = false;
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
