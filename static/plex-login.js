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

  // Poll plex.tv for the auth token until it appears or the popup is closed.
  function pollToken(pin, product, popup) {
    return new Promise((resolve, reject) => {
      const tick = async () => {
        try {
          const res = await fetch("https://plex.tv/api/v2/pins/" + pin.id, {
            headers: headers(product),
          });
          const data = await res.json();
          if (data && data.authToken) {
            resolve(data.authToken);
            return;
          }
          if (popup && popup.closed) {
            reject(new Error("Sign-in was cancelled."));
            return;
          }
          setTimeout(tick, 1000);
        } catch (e) {
          reject(e);
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

  async function login(setStatus) {
    const popup = centeredPopup(600, 700);
    try {
      const product = await getProduct();
      const pin = await getPin(product);

      const params = {
        clientID: clientId(),
        code: pin.code,
        "context[device][product]": product,
        "context[device][deviceName]": product,
      };
      const authUrl = AUTH_URL + encode(params);
      if (popup) popup.location.href = authUrl;
      else window.location.href = authUrl; // popup blocked: fall back to redirect

      setStatus && setStatus("Waiting for Plex sign-in\u2026");
      const token = await pollToken(pin, product, popup);
      if (popup && !popup.closed) popup.close();

      const res = await fetch("/api/auth/plex/exchange", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      let data = {};
      try { data = await res.json(); } catch (_) {}
      if (!res.ok) throw new Error(data.error || "Sign-in failed.");
    } catch (e) {
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
