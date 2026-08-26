// ==UserScript==
// @name         Yummle Save — queue YouTube videos to Yummle
// @namespace    yummle.save
// @version      0.1.0
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
// Install: Tampermonkey (or Violentmonkey) → "Create a new script" → paste →
// save. Default server is http://127.0.0.1:18080 (the local test instance).
// To use a different server, use the script menu command "Set Yummle server
// URL" AND add that host to the @connect list above (Tampermonkey will also
// ask once when the new host is first contacted).

(() => {
  'use strict';

  const SERVER_KEY = 'yummle.serverUrl';
  const DEFAULT_SERVER = 'http://127.0.0.1:18080';
  const VIDEO_LINK_SELECTOR = 'a[href*="/watch?v="], a[href^="/shorts/"]';
  const PROCESSED_ATTR = 'data-yummle-save';

  const DOWNLOAD_ICON =
    '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 7h10"/><path d="M4 12h10"/><path d="M4 17h6"/><path d="M17 14v5"/><path d="m14.5 16.5 2.5 2.5 2.5-2.5"/></svg>';
  const CHECK_ICON =
    '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m4.5 12.5 5 5 10-11"/></svg>';
  const CROSS_ICON =
    '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" aria-hidden="true"><path d="m6 6 12 12"/><path d="M18 6 6 18"/></svg>';

  function serverUrl() {
    const stored = GM_getValue(SERVER_KEY, DEFAULT_SERVER);
    return String(stored || DEFAULT_SERVER).replace(/\/+$/, '');
  }

  function injectStyles() {
    const style = document.createElement('style');
    style.id = 'yummle-save-styles';
    style.textContent = `
      .yummle-save-btn {
        position: absolute;
        top: 6px;
        left: 6px;
        z-index: 9999;
        width: 34px;
        height: 34px;
        padding: 0;
        border: 1px solid rgba(255, 255, 255, 0.35);
        border-radius: 50%;
        background: rgba(0, 0, 0, 0.78);
        color: #fff;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        opacity: 0;
        pointer-events: none;
        transition: opacity 0.15s ease, background 0.15s ease, transform 0.15s ease;
      }
      .yummle-save-btn:hover {
        background: rgba(255, 159, 61, 0.95);
        color: #1d1306;
        transform: scale(1.08);
      }
      .yummle-save-btn[data-state="queued"] {
        background: rgba(46, 160, 67, 0.95);
        color: #fff;
      }
      .yummle-save-btn[data-state="error"] {
        background: rgba(214, 69, 60, 0.95);
        color: #fff;
      }
      a:hover > .yummle-save-btn,
      a:focus-visible > .yummle-save-btn {
        opacity: 1;
        pointer-events: auto;
      }
      @media (hover: none) {
        .yummle-save-btn {
          opacity: 1;
          pointer-events: auto;
        }
      }
    `;
    document.documentElement.appendChild(style);
  }

  function videoIDFromAnchor(anchor) {
    const href = anchor.getAttribute('href') || '';
    const watch = href.match(/[?&]v=([A-Za-z0-9_-]{11})/);
    if (watch) return watch[1];
    const short = href.match(/\/shorts\/([A-Za-z0-9_-]{11})/);
    return short ? short[1] : '';
  }

  // Only attach to real thumbnails (which contain an <img>), not to plain
  // video-title text links that also match the href selector.
  function isThumbnailAnchor(anchor) {
    return !!anchor.querySelector('img');
  }

  function setButtonState(button, state, title) {
    button.dataset.state = state;
    if (title) button.title = title;
    if (state === 'queued') {
      button.innerHTML = CHECK_ICON;
    } else if (state === 'error') {
      button.innerHTML = CROSS_ICON;
    } else {
      button.innerHTML = DOWNLOAD_ICON;
      if (!title) button.title = 'Save to Yummle';
    }
  }

  function queueVideo(button, videoID) {
    if (button.dataset.state === 'loading' || button.dataset.state === 'queued') return;
    const server = serverUrl();
    const url = `https://www.youtube.com/watch?v=${videoID}`;
    setButtonState(button, 'loading', 'Queuing to Yummle…');

    GM_xmlhttpRequest({
      method: 'POST',
      url: `${server}/api/downloads`,
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      data: JSON.stringify({ url }),
      timeout: 15000,
      onload: response => {
        if (response.status >= 200 && response.status < 300) {
          setButtonState(button, 'queued', 'Queued in Yummle ✓');
          setTimeout(() => setButtonState(button, 'idle'), 2500);
          return;
        }
        let message = `Yummle: HTTP ${response.status}`;
        try {
          const body = JSON.parse(response.responseText);
          if (body && body.error) message = body.error;
        } catch {
          // keep the status-based message
        }
        setButtonState(button, 'error', message);
        setTimeout(() => setButtonState(button, 'idle'), 3500);
      },
      onerror: () => {
        setButtonState(button, 'error', 'Yummle unreachable — is the server running?');
        setTimeout(() => setButtonState(button, 'idle'), 3500);
      },
      ontimeout: () => {
        setButtonState(button, 'error', 'Yummle timed out');
        setTimeout(() => setButtonState(button, 'idle'), 3500);
      },
    });
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
    button.setAttribute('aria-label', `Save video to Yummle`);
    button.innerHTML = DOWNLOAD_ICON;
    button.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      queueVideo(button, videoID);
    });
    button.addEventListener('mousedown', event => event.stopPropagation());
    anchor.appendChild(button);
  }

  function processNode(node) {
    if (!node || node.nodeType !== Node.ELEMENT_NODE) return;
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
  }

  function registerMenu() {
    GM_registerMenuCommand('Set Yummle server URL', () => {
      const current = serverUrl();
      const next = window.prompt('Yummle server URL (add this host to @connect in the script header):', current);
      if (next && /^https?:\/\/.+/.test(next.trim())) {
        GM_setValue(SERVER_KEY, next.trim().replace(/\/+$/, ''));
      }
    });
    GM_registerMenuCommand('Open Yummle downloads', () => {
      window.open(`${serverUrl()}/downloads`, '_blank');
    });
  }

  injectStyles();
  registerMenu();
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
