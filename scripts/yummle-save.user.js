// ==UserScript==
// @name         Yummle Save — queue YouTube videos to Yummle
// @namespace    yummle.save
// @version      0.4.0
// @description  Adds a save button to YouTube video thumbnails; clicking queues the video in your local Yummle archive (same queue as the topbar "Queue a video"). Videos already in the archive show a permanent green check. Prototype.
// @author       Yummle
// @match        https://www.youtube.com/*
// @match        https://m.youtube.com/*
// @exclude      https://www.youtube.com/embed/*
// @grant        GM_xmlhttpRequest
// @grant        GM_getValue
// @grant        GM_setValue
// @grant        GM_registerMenuCommand
// @connect      127.0.0.1
// @connect      localhost
// @run-at       document-idle
// @noframes
// ==/UserScript==
//
// Install: Tampermonkey / Violentmonkey (Firefox, Chrome) or Userscripts
// (Safari) → create a new script → paste → save → reload the YouTube tab.
//
// Configure the server:
//   - Tampermonkey/Violentmonkey: script-menu command "Set Yummle server URL".
//   - Userscripts (Safari): GM_registerMenuCommand is NOT implemented there,
//     so run this once in the browser console on the YouTube tab:
//         __yummleSave.setServer('https://yummle.local.n45.xyz')
//   - Any manager: edit DEFAULT_SERVER below and re-save.
// When using a Tampermonkey-style manager with a non-default host, add that
// host to the @connect list above (Userscripts asks for domain access once).
//
// Already-archived videos: as thumbnails scroll into view the script asks
// GET /api/videos/<youtube-id> (the archive stores YouTube IDs as its video
// ids, so no server change was needed). A 200 turns the button into a
// permanent green check; 404 keeps it normal. Requires being logged in when
// the instance has auth enabled (GM_xmlhttpRequest sends the session cookie;
// the fetch fallback works on auth-disabled instances only).
//
// Notes:
// - YouTube's CSP (require-trusted-types-for 'script') blocks innerHTML; all
//   icons are built with DOM APIs. Never reintroduce innerHTML assignments.
// - Safari + Userscripts: GM_registerMenuCommand is unsupported, so every GM
//   call is guarded; the script falls back to localStorage + native fetch
//   (which needs CORS on the server — Yummle sends Access-Control-Allow-
//   Origin for youtube.com origins).
// - Diagnostics: run `__yummleSave.debug()` in the console.

(() => {
  'use strict';

  const SERVER_KEY = 'yummle.serverUrl';
  const DEFAULT_SERVER = 'http://127.0.0.1:18080';
  const VIDEO_LINK_SELECTOR = 'a[href*="/watch?v="], a[href^="/shorts/"]';
  const PROCESSED_ATTR = 'data-yummle-save';
  const SVG_NS = 'http://www.w3.org/2000/svg';

  // Storage that works on every manager: GM_* when granted, localStorage
  // otherwise (Safari + Userscripts has no GM_registerMenuCommand but does
  // implement GM_getValue/GM_setValue; this guards the rest).
  function storageGet(key, fallback) {
    if (typeof GM_getValue === 'function') return GM_getValue(key, fallback);
    try {
      const value = localStorage.getItem(`yummle:${key}`);
      return value === null ? fallback : value;
    } catch {
      return fallback;
    }
  }

  function storageSet(key, value) {
    if (typeof GM_setValue === 'function') {
      GM_setValue(key, value);
      return;
    }
    try {
      localStorage.setItem(`yummle:${key}`, value);
    } catch {
      // no persistence available; the in-memory default still applies
    }
  }

  function serverUrl() {
    const stored = storageGet(SERVER_KEY, DEFAULT_SERVER);
    return String(stored || DEFAULT_SERVER).replace(/\/+$/, '');
  }

  function setServer(url) {
    const value = String(url || '').trim().replace(/\/+$/, '');
    if (!/^https?:\/\/.+/.test(value)) throw new Error('Yummle server URL must start with http:// or https://');
    storageSet(SERVER_KEY, value);
  }

  function injectStyles() {
    const style = document.createElement('style');
    style.id = 'yummle-save-styles';
    style.textContent = `
      .yummle-save-btn {
        position: absolute !important;
        top: 6px !important;
        left: 6px !important;
        z-index: 9999 !important;
        width: 34px !important;
        height: 34px !important;
        padding: 0 !important;
        margin: 0 !important;
        border: 1px solid rgba(255, 255, 255, 0.4) !important;
        border-radius: 50% !important;
        background: rgba(0, 0, 0, 0.78) !important;
        color: #fff !important;
        cursor: pointer !important;
        display: flex !important;
        align-items: center !important;
        justify-content: center !important;
        opacity: 0.92 !important;
        pointer-events: auto !important;
        box-shadow: 0 1px 4px rgba(0, 0, 0, 0.5) !important;
        transition: background 0.15s ease, transform 0.15s ease, opacity 0.15s ease !important;
      }
      .yummle-save-btn:not([data-state="archived"]):hover,
      .yummle-save-btn:not([data-state="archived"]).highlight {
        background: rgba(255, 159, 61, 0.95) !important;
        color: #1d1306 !important;
        transform: scale(1.1) !important;
      }
      .yummle-save-btn[data-state="queued"],
      .yummle-save-btn[data-state="archived"] {
        background: rgba(46, 160, 67, 0.95) !important;
        color: #fff !important;
      }
      .yummle-save-btn[data-state="archived"] {
        cursor: default !important;
      }
      .yummle-save-btn[data-state="error"] {
        background: rgba(214, 69, 60, 0.95) !important;
        color: #fff !important;
      }
    `;
    document.documentElement.appendChild(style);
  }

  // Build icons with DOM APIs only: YouTube's CSP (require-trusted-types-for
  // 'script') blocks HTML string sinks like innerHTML.
  function buildIcon(kind) {
    const paths =
      kind === 'check'
        ? ['m4.5 12.5 5 5 10-11']
        : kind === 'cross'
          ? ['m6 6 12 12', 'M18 6 6 18']
          : ['M4 7h10', 'M4 12h10', 'M4 17h6', 'M17 14v5', 'm14.5 16.5 2.5 2.5 2.5-2.5'];
    const strokeWidth = kind === 'download' ? '2.2' : '2.6';
    const svg = document.createElementNS(SVG_NS, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', '16');
    svg.setAttribute('height', '16');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', strokeWidth);
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    for (const d of paths) {
      const path = document.createElementNS(SVG_NS, 'path');
      path.setAttribute('d', d);
      svg.appendChild(path);
    }
    return svg;
  }

  function videoIDFromAnchor(anchor) {
    const href = anchor.getAttribute('href') || '';
    const watch = href.match(/[?&]v=([A-Za-z0-9_-]{11})/);
    if (watch) return watch[1];
    const short = href.match(/\/shorts\/([A-Za-z0-9_-]{11})/);
    return short ? short[1] : '';
  }

  // Only attach to real thumbnails, not to plain video-title text links that
  // also match the href selector.
  function isThumbnailAnchor(anchor) {
    return !!anchor.querySelector('img') || !!anchor.querySelector('ytd-thumbnail');
  }

  function setButtonState(button, state, title) {
    button.dataset.state = state;
    if (title) {
      button.title = title;
    } else if (state === 'idle') {
      button.title = 'Save to Yummle';
    }
    const kind = state === 'queued' || state === 'archived' ? 'check' : state === 'error' ? 'cross' : 'download';
    button.replaceChildren(buildIcon(kind));
  }

  // One request helper for both transports. Prefer GM_xmlhttpRequest
  // (cross-origin without CORS, used by Firefox/Chrome Tampermonkey and
  // Safari Userscripts); fall back to native fetch for managers without it —
  // that path needs Yummle's CORS for youtube.com origins. Resolves with
  // {status, responseText} or rejects.
  function apiRequest(method, target, body) {
    const headers = { Accept: 'application/json' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }

    if (typeof GM_xmlhttpRequest === 'function') {
      return new Promise((resolve, reject) => {
        GM_xmlhttpRequest({
          method,
          url: target,
          headers,
          data: body === undefined ? undefined : body,
          timeout: 15000,
          onload: response => resolve({ status: response.status, responseText: response.responseText || '' }),
          onerror: () => reject(new Error('Yummle unreachable — is the server running?')),
          ontimeout: () => reject(new Error('Yummle timed out')),
        });
      });
    }

    const options = { method, headers: headers, mode: 'cors', credentials: 'omit' };
    if (body !== undefined) options.body = body;
    return fetch(target, options)
      .then(async response => ({ status: response.status, responseText: await response.text() }))
      .catch(() => {
        throw new Error('Yummle unreachable — server down or CORS not enabled');
      });
  }

  async function queueVideo(button, videoID) {
    if (button.dataset.state === 'loading' || button.dataset.state === 'queued' || button.dataset.state === 'archived') return;
    const server = serverUrl();
    const url = `https://www.youtube.com/watch?v=${videoID}`;
    setButtonState(button, 'loading', 'Queuing to Yummle…');

    try {
      const response = await apiRequest('POST', `${server}/api/downloads`, JSON.stringify({ url }));
      if (response.status >= 200 && response.status < 300) {
        archiveChecked.set(videoID, true);
        // Brief "Queued" feedback, then the permanent archived check.
        setButtonState(button, 'queued', 'Queued in Yummle ✓');
        setTimeout(() => setButtonState(button, 'archived', 'Already in Yummle archive'), 2500);
        return;
      }
      let message = `Yummle: HTTP ${response.status}`;
      try {
        const parsed = JSON.parse(response.responseText || '');
        if (parsed && parsed.error) message = parsed.error;
      } catch {
        // keep the status-based message
      }
      setButtonState(button, 'error', message);
      setTimeout(() => setButtonState(button, 'idle'), 3500);
    } catch (error) {
      setButtonState(button, 'error', error.message);
      setTimeout(() => setButtonState(button, 'idle'), 3500);
    }
  }

  async function checkArchived(button, anchor, videoID) {
    if (archiveChecking.has(videoID)) return;
    if (archiveChecked.has(videoID)) {
      // Re-rendered anchor got a fresh button: re-apply the known status.
      if (archiveChecked.get(videoID)) setButtonState(button, 'archived', 'Already in Yummle archive');
      return;
    }
    archiveChecking.add(videoID);
    const server = serverUrl();
    try {
      const response = await apiRequest('GET', `${server}/api/videos/${encodeURIComponent(videoID)}`);
      const archived = response.status === 200;
      archiveChecked.set(videoID, archived);
      if (archived) setButtonState(button, 'archived', 'Already in Yummle archive');
    } catch {
      // network/auth hiccup — leave the button normal; it can be retried by
      // re-scrolling or after the next page render.
      archiveChecked.delete(videoID);
    } finally {
      archiveChecking.delete(videoID);
    }
  }

  function attachButton(anchor, videoID) {
    if (anchor.getAttribute(PROCESSED_ATTR) === '1') return;
    anchor.setAttribute(PROCESSED_ATTR, '1');

    if (getComputedStyle(anchor).position === 'static') {
      anchor.style.position = 'relative';
    }

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'yummle-save-btn';
    button.dataset.state = 'idle';
    button.title = 'Save to Yummle';
    button.setAttribute('aria-label', 'Save video to Yummle');
    setButtonState(button, 'idle');
    button.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      queueVideo(button, videoID);
    });
    button.addEventListener('mousedown', event => event.stopPropagation());
    anchor.addEventListener('mouseenter', () => button.classList.add('highlight'));
    anchor.addEventListener('mouseleave', () => button.classList.remove('highlight'));
    anchor.appendChild(button);

    archiveVideoIDs.set(anchor, videoID);
    observeArchiveCheck(anchor);
  }

  // ---- already-archived detection ---------------------------------------
  // For YouTube, Yummle stores the YouTube id as the video's own id, so a
  // 200 from GET /api/videos/<id> means the video is already in the archive.
  const archiveVideoIDs = new WeakMap();
  const archiveChecked = new Map();
  const archiveChecking = new Set();
  let archiveObserver = null;
  let archiveObserverFallback = null;

  function observeArchiveCheck(anchor) {
    if (archiveObserver) {
      archiveObserver.observe(anchor);
      return;
    }
    if (typeof IntersectionObserver === 'function') {
      archiveObserver = new IntersectionObserver(entries => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          const target = entry.target;
          archiveObserver.unobserve(target);
          const videoID = archiveVideoIDs.get(target);
          const button = target.querySelector('.yummle-save-btn');
          if (videoID && button) checkArchived(button, target, videoID);
        }
      }, { rootMargin: '300px' });
      archiveObserver.observe(anchor);
      return;
    }
    // No IntersectionObserver (very old Safari): check everything once after
    // the page settles.
    if (!archiveObserverFallback) {
      archiveObserverFallback = setTimeout(() => {
        for (const target of document.querySelectorAll(VIDEO_LINK_SELECTOR)) {
          const videoID = archiveVideoIDs.get(target);
          const button = target.querySelector('.yummle-save-btn');
          if (videoID && button) checkArchived(button, target, videoID);
        }
      }, 4000);
    }
  }

  function processNode(node) {
    if (!node || node.nodeType !== Node.ELEMENT_NODE) return;
    try {
      if (node.matches && node.matches(VIDEO_LINK_SELECTOR)) {
        const videoID = videoIDFromAnchor(node);
        if (videoID && isThumbnailAnchor(node)) attachButton(node, videoID);
      }
      if (node.querySelectorAll) {
        const anchors = node.querySelectorAll(VIDEO_LINK_SELECTOR);
        for (const anchor of anchors) {
          if (anchor.getAttribute(PROCESSED_ATTR) === '1') continue;
          const videoID = videoIDFromAnchor(anchor);
          if (videoID && isThumbnailAnchor(anchor)) attachButton(anchor, videoID);
        }
      }
    } catch (error) {
      // One bad anchor must not stop the rescan loop.
      console.warn('[Yummle Save] attach failed:', error);
    }
  }

  function registerMenu() {
    // GM_registerMenuCommand is not implemented in Userscripts (Safari);
    // guard so the script still runs there.
    if (typeof GM_registerMenuCommand !== 'function') return;
    GM_registerMenuCommand('Set Yummle server URL', () => {
      const current = serverUrl();
      const next = window.prompt('Yummle server URL (add this host to @connect in the script header):', current);
      if (next && /^https?:\/\/.+/.test(next.trim())) {
        setServer(next.trim());
      }
    });
    GM_registerMenuCommand('Open Yummle downloads', () => {
      window.open(`${serverUrl()}/downloads`, '_blank');
    });
    GM_registerMenuCommand('Yummle save: diagnostic', () => {
      console.log(__yummleSave.debug());
    });
  }

  function debugInfo() {
    const buttons = document.querySelectorAll('.yummle-save-btn');
    const links = document.querySelectorAll(VIDEO_LINK_SELECTOR);
    const thumbnailLinks = [...links].filter(isThumbnailAnchor);
    return {
      scriptVersion: '0.4.0',
      loaded: true,
      transport: typeof GM_xmlhttpRequest === 'function' ? 'GM_xmlhttpRequest' : 'fetch (needs server CORS)',
      server: serverUrl(),
      videoLinksFound: links.length,
      thumbnailLinksFound: thumbnailLinks.length,
      buttonsAttached: buttons.length,
      archiveChecked: archiveChecked.size,
      archiveKnown: [...archiveChecked.values()].filter(Boolean).length,
      sample: [...links].slice(0, 3).map(a => ({
        href: a.getAttribute('href'),
        hasImg: !!a.querySelector('img'),
        hasThumbnailEl: !!a.querySelector('ytd-thumbnail'),
        attached: a.getAttribute(PROCESSED_ATTR) === '1',
      })),
    };
  }

  window.__yummleSave = {
    debug: debugInfo,
    setServer,
  };

  injectStyles();
  registerMenu();
  console.log(`[Yummle Save] v0.4.0 loaded, server=${serverUrl()}, transport=${typeof GM_xmlhttpRequest === 'function' ? 'GM_xmlhttpRequest' : 'fetch'}`);
  processNode(document.body);

  const observer = new MutationObserver(mutations => {
    for (const mutation of mutations) {
      if (mutation.type !== 'childList') continue;
      for (const node of mutation.addedNodes) {
        if (node.nodeType === Node.ELEMENT_NODE) processNode(node);
      }
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });

  // Fallback against YouTube's lazy rendering and SPA re-renders.
  const rescanTimer = setInterval(() => processNode(document.body), 3000);
  window.addEventListener('beforeunload', () => clearInterval(rescanTimer), { once: true });
})();