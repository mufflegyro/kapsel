// ==UserScript==
// @name         Yummle Save — queue YouTube videos to Yummle
// @namespace    yummle.save
// @version      0.3.0
// @description  Adds a save button to YouTube video thumbnails; clicking queues the video in your local Yummle archive (same queue as the topbar "Queue a video"). Prototype.
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
// Notes:
// - YouTube's CSP (require-trusted-types-for 'script') blocks innerHTML; all
//   icons are built with DOM APIs. Never reintroduce innerHTML assignments.
// - Safari + Userscripts: GM_registerMenuCommand is unsupported, so every GM
//   call is guarded; the script falls back to localStorage + native fetch
//   (which needs CORS on the server — Yummle now sends Access-Control-Allow-
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
      .yummle-save-btn:hover,
      .yummle-save-btn.highlight {
        background: rgba(255, 159, 61, 0.95) !important;
        color: #1d1306 !important;
        transform: scale(1.1) !important;
      }
      .yummle-save-btn[data-state="queued"] {
        background: rgba(46, 160, 67, 0.95) !important;
        color: #fff !important;
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
    const kind = state === 'queued' ? 'check' : state === 'error' ? 'cross' : 'download';
    button.replaceChildren(buildIcon(kind));
  }

  // POST {url} to the queue. Prefer GM_xmlhttpRequest (cross-origin without
  // CORS, used by Firefox/Chrome Tampermonkey and Safari Userscripts); fall
  // back to native fetch for managers without GM_xmlhttpRequest — that path
  // needs the server to send Access-Control-Allow-Origin (Yummle does for
  // youtube.com origins). Resolves with {status, responseText} or rejects.
  function postToQueue(server, url) {
    const target = `${server}/api/downloads`;
    const headers = { 'Content-Type': 'application/json', Accept: 'application/json' };
    const body = JSON.stringify({ url });

    if (typeof GM_xmlhttpRequest === 'function') {
      return new Promise((resolve, reject) => {
        GM_xmlhttpRequest({
          method: 'POST',
          url: target,
          headers,
          data: body,
          timeout: 15000,
          onload: response => resolve({ status: response.status, responseText: response.responseText || '' }),
          onerror: () => reject(new Error('Yummle unreachable — is the server running?')),
          ontimeout: () => reject(new Error('Yummle timed out')),
        });
      });
    }

    return fetch(target, { method: 'POST', headers, body, mode: 'cors', credentials: 'omit' })
      .then(async response => ({ status: response.status, responseText: await response.text() }))
      .catch(() => {
        throw new Error('Yummle unreachable — server down or CORS not enabled');
      });
  }

  async function queueVideo(button, videoID) {
    if (button.dataset.state === 'loading' || button.dataset.state === 'queued') return;
    const server = serverUrl();
    const url = `https://www.youtube.com/watch?v=${videoID}`;
    setButtonState(button, 'loading', 'Queuing to Yummle…');

    try {
      const response = await postToQueue(server, url);
      if (response.status >= 200 && response.status < 300) {
        setButtonState(button, 'queued', 'Queued in Yummle ✓');
        setTimeout(() => setButtonState(button, 'idle'), 2500);
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
      scriptVersion: '0.3.0',
      loaded: true,
      transport: typeof GM_xmlhttpRequest === 'function' ? 'GM_xmlhttpRequest' : 'fetch (needs server CORS)',
      server: serverUrl(),
      videoLinksFound: links.length,
      thumbnailLinksFound: thumbnailLinks.length,
      buttonsAttached: buttons.length,
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
  console.log(`[Yummle Save] v0.3.0 loaded, server=${serverUrl()}, transport=${typeof GM_xmlhttpRequest === 'function' ? 'GM_xmlhttpRequest' : 'fetch'}`);
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
