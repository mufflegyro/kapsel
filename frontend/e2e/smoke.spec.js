import { expect, test } from '@playwright/test';

const seededPlaybackProgress = { position_seconds: 42, duration_seconds: 125, watched: false };

test.beforeEach(async ({ context, request }) => {
  await resetSeededPlaybackProgress(request);
  await context.addInitScript(() => {
    class KapselFakeWebSocket extends EventTarget {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSING = 2;
      static CLOSED = 3;

      constructor(url) {
        super();
        this.url = url;
        this.readyState = KapselFakeWebSocket.CONNECTING;
        window.__kapselLiveSockets = window.__kapselLiveSockets || [];
        window.__kapselLiveSockets.push(this);
        queueMicrotask(() => {
          if (this.readyState !== KapselFakeWebSocket.CONNECTING) return;
          if (window.__kapselLiveFailOpen) {
            this.readyState = KapselFakeWebSocket.CLOSED;
            this.dispatchEvent(new Event('error'));
            this.dispatchEvent(new CloseEvent('close'));
            return;
          }
          this.readyState = KapselFakeWebSocket.OPEN;
          this.dispatchEvent(new Event('open'));
        });
      }

      send() {}

      close() {
        if (this.readyState === KapselFakeWebSocket.CLOSED) return;
        this.readyState = KapselFakeWebSocket.CLOSED;
        this.dispatchEvent(new CloseEvent('close'));
      }

      __emit(message) {
        if (this.readyState !== KapselFakeWebSocket.OPEN) return;
        this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(message) }));
      }
    }

    window.WebSocket = KapselFakeWebSocket;
    window.__kapselLiveEmit = message => {
      for (const socket of window.__kapselLiveSockets || []) socket.__emit(message);
    };
  });
  await context.route('**/*', async route => {
    const requestURL = route.request().url();
    if (isLocalRequest(requestURL)) {
      await route.continue();
      return;
    }

    await route.abort('blockedbyclient');
    throw new Error(`Unexpected external browser request: ${requestURL}`);
  });
});

async function resetSeededPlaybackProgress(request) {
  const response = await request.post('/__e2e/reset-progress');
  await expect(response).toBeOK();
}

async function expectPlaybackProgress(card, percent) {
  await expect(card.getByTestId('playback-progress')).toHaveAttribute('aria-valuenow', String(percent));
  await expect(card.getByTestId('playback-progress-bar').locator('span')).toHaveAttribute('style', `width: ${percent}%;`);
}

async function expectNoPlaybackProgress(card) {
  await expect(card.getByTestId('playback-progress')).toHaveCount(0);
  await expect(card.getByTestId('playback-progress-bar')).toHaveCount(0);
}

function mockHomeVideo(index) {
  const label = String(index).padStart(2, '0');
  return {
    id: `mock-home-${label}`,
    title: `Mock Infinite Video ${label}`,
    description: 'A deterministic paginated home-feed fixture.',
    published_at: '2026-05-03T12:00:00Z',
    duration_seconds: 90 + index,
    view_count: index,
    watched: false,
    channel: { id: 'mock-home-channel', name: 'Mock Home Channel' },
  };
}

function mockUpNextVideo(id, title, channelID, publishedAt, options = {}) {
  const progressPositionSeconds = options.positionSeconds ?? (options.watched ? 90 : 0);
  return {
    id,
    title,
    description: 'A deterministic up-next ordering fixture.',
    published_at: publishedAt,
    archived_at: publishedAt,
    duration_seconds: 90,
    view_count: 1,
    media_url: options.available ? `/media/${id}.mp4` : undefined,
    thumbnail_url: options.thumbnailURL ?? `/media/${id}.jpg`,
    archive_state: options.available ? 'downloaded' : 'catalog-only',
    watched: !!options.watched,
    channel: { id: channelID, name: channelID === 'e2e-channel' ? 'E2E Test Channel' : 'Other Channel' },
    progress: { watched: !!options.watched, position_seconds: progressPositionSeconds, duration_seconds: 90 },
  };
}

test('home feed automatically appends the next page', async ({ page }) => {
  const pageRequests = [];
  let releasePage2;
  const page2CanFinish = new Promise(resolve => {
    releasePage2 = resolve;
  });
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/home/videos') {
      await route.continue();
      return;
    }

    const pageNumber = Number(url.searchParams.get('page') || '1');
    expect(url.searchParams.get('page_size')).toBe('50');
    expect(url.searchParams.get('sort')).toBe('watching');
    pageRequests.push(pageNumber);
    if (pageNumber === 2) await page2CanFinish;
    await route.fulfill({
      json: {
        data: pageNumber === 1 ? Array.from({ length: 50 }, (_, index) => mockHomeVideo(index + 1)) : [mockHomeVideo(51), mockHomeVideo(52)],
        pagination: { page: pageNumber, page_size: 50, total: 52 },
      },
    });
  });

  await page.goto('/');
  await expect(page.getByTestId('video-card')).toHaveCount(50);

  await page.getByTestId('library-load-more-sentinel').scrollIntoViewIfNeeded();
  await expect(page.getByTestId('library-load-more')).toBeDisabled();
  await page.getByTestId('library-load-more-sentinel').scrollIntoViewIfNeeded();
  await page.waitForTimeout(100);
  expect(pageRequests).toEqual([1, 2]);
  releasePage2();
  await expect(page.getByTestId('video-card')).toHaveCount(52);
  await expect(page.getByText('Mock Infinite Video 51')).toBeVisible();
  await expect(page.getByTestId('library-load-more-sentinel')).toHaveCount(0);
  expect(pageRequests).toEqual([1, 2]);
});

test('home add-channel prompt only appears on empty setup', async ({ page }) => {
  let channelTotal = 1;
  let channelRequests = 0;
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/home/videos') {
      await route.continue();
      return;
    }
    await route.fulfill({ json: { data: [], pagination: { page: 1, page_size: 50, total: 0 } } });
  });
  await page.route('**/api/channels?*', async route => {
    channelRequests += 1;
    await route.fulfill({
      json: {
        data: channelTotal > 0 ? [{ id: 'known-channel', name: 'Known Channel' }] : [],
        pagination: { page: 1, page_size: 1, total: channelTotal },
      },
    });
  });

  await page.goto('/');
  await expect(page.getByTestId('library-empty')).toBeVisible();
  await expect.poll(() => channelRequests).toBe(1);
  await expect(page.getByTestId('channel-form')).toHaveCount(0);

  channelTotal = 0;
  await page.reload();
  await expect(page.getByTestId('library-empty')).toBeVisible();
  await expect.poll(() => channelRequests).toBe(2);
  await expect(page.getByTestId('channel-form')).toBeVisible();
});

test('home For You empty state points to explicit sorts', async ({ page }) => {
  const watchedVideo = { ...mockHomeVideo(1), id: 'watched-home-video', title: 'Watched Home Video', watched: true, progress: { watched: true, position_seconds: 90, duration_seconds: 90 } };
  let releaseNewestProbe;
  const newestProbeCanFinish = new Promise(resolve => {
    releaseNewestProbe = resolve;
  });
  let channelRequests = 0;
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('sort') === 'newest') {
      await newestProbeCanFinish;
      await route.fulfill({ json: { data: [watchedVideo], pagination: { page: 1, page_size: 50, total: 1 } } });
      return;
    }
    await route.fulfill({ json: { data: [], pagination: { page: 1, page_size: 50, total: 0 } } });
  });
  await page.route('**/api/channels?*', async route => {
    channelRequests += 1;
    await route.fulfill({ json: { data: [], pagination: { page: 1, page_size: 1, total: 0 } } });
  });

  await page.goto('/');
  await expect(page.getByTestId('library-empty')).toContainText('Checking your archive...');
  await expect(page.getByTestId('library-empty')).not.toContainText('No archived videos yet.');
  releaseNewestProbe();
  await expect(page.getByTestId('library-empty')).toContainText('No For You videos right now.');
  await expect(page.getByTestId('library-empty')).toContainText('Choose Newest or another sort to browse watched videos.');
  await expect(page.getByTestId('library-empty')).not.toContainText('No archived videos yet.');
  await expect(page.getByTestId('channel-form')).toHaveCount(0);
  expect(channelRequests).toBe(0);

  await page.getByLabel('Sort by').selectOption('newest');
  await expect(page.getByTestId('video-card').filter({ hasText: 'Watched Home Video' })).toBeVisible();
});

test('home browse chrome shows feed position, aligned titles, and quieter explore links', async ({ page }) => {
  const videos = [
    {
      ...mockHomeVideo(1),
      id: 'grid-long-title',
      title: 'A Very Long Archive Title That Should Clamp Cleanly Across The Home Grid Without Pushing Neighboring Metadata Down',
    },
    { ...mockHomeVideo(2), id: 'grid-short-title', title: 'Short archive clip' },
    { ...mockHomeVideo(3), id: 'grid-sparse-catalog', title: 'Sparse catalog entry', published_at: '', view_count: 0, archive_state: 'catalog-only' },
  ];
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/home/videos') {
      await route.continue();
      return;
    }
    await route.fulfill({ json: { data: videos, pagination: { page: 1, page_size: 50, total: 3 } } });
  });

  await page.goto('/');

  await expect(page.getByTestId('library-feed-summary')).toHaveText('Showing 3 of 3 videos');
  await expect(page.getByTestId('library-route')).not.toContainText('Archive feed');
  await expect(page.getByTestId('library-route')).not.toContainText('Newest first');
  await expect(page.getByTestId('video-card')).toHaveCount(3);
  await expect(page.getByTestId('video-card').filter({ hasText: 'Sparse catalog entry' }).locator('.tile-meta')).not.toContainText('Metadata archived');
  const titleHeights = await page.getByTestId('video-card').locator('.tile-title').evaluateAll(elements => elements.map(element => Math.round(element.getBoundingClientRect().height)));
  expect(Math.max(...titleHeights) - Math.min(...titleHeights)).toBeLessThanOrEqual(2);

  const primaryHome = page.locator('.side-nav').getByRole('link', { name: 'Home' });
  const exploreMusic = page.locator('.side-section a').filter({ hasText: 'Music' });
  await expect(exploreMusic).toHaveCount(1);
  await expect(primaryHome).toHaveAttribute('aria-label', 'Home');
  await expect(exploreMusic).toHaveAttribute('aria-label', 'Music');
  const navWeights = await Promise.all([primaryHome, exploreMusic].map(locator => locator.evaluate(element => Number(getComputedStyle(element).fontWeight))));
  expect(navWeights[0]).toBeGreaterThan(navWeights[1]);
  const iconOpacity = await Promise.all([
    primaryHome.locator('.side-icon').evaluate(element => Number(getComputedStyle(element).opacity)),
    exploreMusic.locator('.side-icon').evaluate(element => Number(getComputedStyle(element).opacity)),
  ]);
  expect(iconOpacity[0]).toBeGreaterThan(iconOpacity[1]);

  await primaryHome.focus();
  await expect(primaryHome).toHaveCSS('outline-style', 'solid');
});

test('critical archive flows render without network downloads', async ({ page }) => {
  let legacyRouteVideoListRequests = 0;
  await page.route('**/api/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/videos' && (url.searchParams.get('home') === '1' || url.searchParams.get('channel'))) legacyRouteVideoListRequests += 1;
    await route.continue();
  });
  await page.route('**/api/videos/e2e-catalog-video/download', async route => {
    await route.fulfill({ json: downloadJob('e2e-catalog-download', 'queued', 0.37), status: 202 });
  });
  await page.route('**/api/jobs/e2e-catalog-download', async route => {
    await route.fulfill({ json: downloadJob('e2e-catalog-download', 'running', 0.37) });
  });
  await page.route('**/api/videos/e2e-catalog-video-2/download', async route => {
    await route.fulfill({ json: downloadJob('e2e-video-page-download', 'queued', 0.42), status: 202 });
  });
  await page.route('**/api/jobs/e2e-video-page-download', async route => {
    await route.fulfill({ json: downloadJob('e2e-video-page-download', 'running', 0.42) });
  });

  await page.goto('/');

  const topActions = page.locator('.top-actions');
  await expect(topActions.getByRole('link', { name: 'Open queue' })).toHaveAttribute('href', '/downloads');
  await expect(topActions.getByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings');
  await expect(page.getByTestId('library-route')).toBeVisible();
  await expect(page.getByLabel('Sort by')).toHaveValue('watching');
  await expect(page.getByLabel('Sort by').locator('option[value="watching"]')).toHaveText('For You');
  await expect(page.getByLabel('Sort by').locator('option[value="downloaded"]')).toHaveText('Recently Downloaded');
  await expect(page.getByTestId('channel-form')).toHaveCount(0);
  const startedLibraryCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  await expect(startedLibraryCard).toBeVisible();
  await expect(startedLibraryCard.locator('.avatar img')).toHaveAttribute('src', /\/media\/channels\/e2e-channel\.jpg\?/);
  await expectPlaybackProgress(startedLibraryCard, 34);
  await expectNoPlaybackProgress(page.getByTestId('video-card').filter({ hasText: 'E2E Catalog Orbit Brief' }));
  await expect(page.getByTestId('library-videos').getByTestId('catalog-download-button')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /coming soon/i })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'History' })).toHaveCount(0);

  await page.getByLabel('Sort by').selectOption('popularity');
  await expect(page).toHaveURL(/sort=popularity/);
  await expect(page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' })).toBeVisible();

  await page.getByRole('link', { name: 'Watch E2E Lunar Archive Smoke' }).click();
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'E2E Lunar Archive Smoke' })).toBeVisible();
  await expect(page.getByLabel('Video player')).toBeVisible();
  await expect(page.getByTestId('media-availability')).toHaveCount(0);
  await expect(page.locator('.channel-lockup .avatar img')).toHaveAttribute('src', /\/media\/channels\/e2e-channel\.jpg\?/);
  const keepForeverToggle = page.getByTestId('keep-forever-toggle');
  await expect(keepForeverToggle).toHaveText('Keep forever');
  await expect(keepForeverToggle).toHaveAttribute('aria-pressed', 'false');
  await keepForeverToggle.click();
  await expect(keepForeverToggle).toHaveText('Kept forever');
  await expect(keepForeverToggle).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(async () => {
    const response = await page.request.get('/api/videos/e2e-video');
    const body = await response.json();
    return body.keep_forever;
  }).toBe(true);
  const videoDescription = page.getByLabel('Video description');
  await expect(videoDescription).not.toContainText('42s in');
  await expect(videoDescription.getByRole('link', { name: /https:\/\/example\.com\/watch-details opens in a new tab/ })).toHaveAttribute('href', 'https://example.com/watch-details');
  await expect(videoDescription.locator('script')).toHaveCount(0);
  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 0 });
  });
  await videoDescription.getByRole('button', { name: 'Seek to 1:05' }).click();
  await expect.poll(() => page.locator('video').evaluate(media => Math.floor(media.currentTime))).toBe(65);
  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
  });
  await expect.poll(() => page.locator('video').evaluate(media => Math.floor(media.currentTime))).toBe(65);
  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await expect.poll(async () => {
    const response = await page.request.get('/api/videos/e2e-video/progress');
    const body = await response.json();
    return body.position_seconds;
  }).toBe(65);
  const thumbnailTrack = page.locator('video track[kind="metadata"][label="thumbnails"]');
  await expect(thumbnailTrack).toHaveAttribute('src', '/api/videos/e2e-video/timeline-preview.vtt');
  await expect.poll(() => thumbnailTrack.evaluate(track => track.track.mode)).toBe('hidden');
  const chaptersTrack = page.locator('video track[kind="chapters"][label="Chapters"]');
  await expect(chaptersTrack).toHaveAttribute('src', '/api/videos/e2e-video/chapters.vtt');
  await expect.poll(() => chaptersTrack.evaluate(track => track.track.mode)).toBe('hidden');
  await expect.poll(() => chaptersTrack.evaluate(track => track.track.cues?.length ?? 0)).toBe(2);
  await expect(page.locator('video-skin button[aria-label="Chapters"]')).toHaveCount(0);
  await expect(page.locator('video track[kind="captions"]')).toHaveCount(2);
  await expect.poll(() => captionTrackModes(page)).toEqual(['disabled', 'disabled']);
  const captionsButton = page.locator('video-skin media-captions-button');
  await expect(captionsButton).not.toHaveAttribute('hidden', '');
  await expect(captionsButton).not.toHaveAttribute('aria-hidden', 'true');
  await expect(captionsButton).toHaveAttribute('aria-label', 'Enable captions');
  await captionsButton.evaluate(button => button.click());
  await expect.poll(() => showingCaptionTrackCount(page)).toBe(1);
  await expect.poll(() => firstCaptionCueBox(page)).toEqual({ align: 'center', position: 50, size: 100 });
  await captionsButton.evaluate(button => button.click());
  await expect.poll(() => showingCaptionTrackCount(page)).toBe(0);
  await captionsButton.evaluate(button => button.click());
  await expect.poll(() => showingCaptionTrackCount(page)).toBe(1);
  await page.goto('/');
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.getByTestId('keep-forever-toggle')).toHaveText('Kept forever');
  await expect.poll(() => showingCaptionTrackCount(page)).toBe(1);
  await page.locator('video-skin media-captions-button').evaluate(button => button.click());
  await expect.poll(() => showingCaptionTrackCount(page)).toBe(0);
  await page.goto('/');
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect.poll(() => captionTrackModes(page)).toEqual(['disabled', 'disabled']);
  const chaptersResponse = await page.request.get('/api/videos/e2e-video/chapters.vtt');
  await expect(chaptersResponse).toBeOK();
  await expect(chaptersResponse.text()).resolves.toContain('00:00:00.000 --> 00:02:00.000\nE2E opening');
  await expect(page.getByRole('button', { name: 'Like' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Share' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Save' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'More' })).toHaveCount(0);
  await expect(page.getByText('1 imported')).toBeVisible();
  await expect(page.getByText('Smoke Tester')).toBeVisible();
  await expect(page.getByText('A deterministic imported comment.')).toBeVisible();
  await expect.poll(() => page.locator('.comment-list').evaluate(element => getComputedStyle(element).display)).toBe('grid');
  await expect.poll(() => page.locator('.comments-box').evaluate(element => getComputedStyle(element).alignContent)).toBe('start');

  await page.getByRole('searchbox', { name: 'Search archive' }).fill('lunar smoke');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Search results for lunar smoke' })).toBeVisible();
  await expect(page.getByText('E2E Lunar Archive Smoke').first()).toBeVisible();

  await page.goto('/channels');
  await expect(page.getByRole('heading', { name: 'Channel library' })).toBeVisible();
  await expect(page.locator('.channel-list').getByRole('link').filter({ hasText: 'E2E Test Channel' })).toContainText('Deterministic channel for browser smoke tests.');

  await page.goto('/channels/e2e-channel');
  await expect(page.getByRole('heading', { name: 'E2E Test Channel' })).toBeVisible();
  await expect(page.getByText('3 videos')).toBeVisible();
  const startedChannelCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  await expectPlaybackProgress(startedChannelCard, 52);
  await expect(page.getByRole('button', { name: 'Show more' })).toBeVisible();
  const clippedLink = page.locator('.rich-text a[href="https://example.com/after-expand"]');
  await expect.poll(() => isClipped(clippedLink)).toBe(true);
  await expect(clippedLink).toHaveAttribute('tabindex', '-1');
  await expect(clippedLink).toHaveAttribute('inert', '');
  await expect(page.getByRole('heading', { name: 'Links' })).toBeVisible();
  const introLink = page.getByRole('link', { name: /https:\/\/example\.com\/intro opens in a new tab/ });
  await introLink.focus();
  await expect(introLink).toBeFocused();
  await expect(introLink).not.toHaveAttribute('tabindex', '-1');
  await expect(introLink).not.toHaveAttribute('inert', '');
  await expect(introLink).toHaveAttribute('href', 'https://example.com/intro');
  const projectNotes = page.getByRole('link', { name: /project notes opens in a new tab/ });
  await expect(projectNotes).toHaveAttribute('href', 'https://example.com/kapsel');
  await expect(projectNotes).toHaveAttribute('target', '_blank');
  await expect(projectNotes).toHaveAttribute('rel', /noopener/);
  await expect(projectNotes).toHaveAttribute('rel', /noreferrer/);
  await expect(page.getByRole('link', { name: /https:\/\/example\.org\/archive opens in a new tab/ })).toHaveAttribute('href', 'https://example.org/archive');
  await expect(page.getByRole('link', { name: /https:\/\/example\.net\/wrapped opens in a new tab/ })).toHaveAttribute('href', 'https://example.net/wrapped');
  const emailLink = page.getByRole('link', { name: 'smoke@example.com' });
  await expect(emailLink).toHaveAttribute('href', 'mailto:smoke@example.com');
  await expect(emailLink).not.toHaveAttribute('target', '_blank');
  await expect(page.getByRole('link', { name: 'bad' })).toHaveCount(0);
  await expect(page.locator('.rich-text script')).toHaveCount(0);
  await page.getByRole('button', { name: 'Show more' }).click();
  await expect(page.getByRole('button', { name: 'Show less' })).toBeVisible();
  await expect.poll(() => isClipped(clippedLink)).toBe(false);
  await expect(clippedLink).not.toHaveAttribute('tabindex', '-1');
  await expect(clippedLink).not.toHaveAttribute('inert', '');
  await expect(page.getByRole('button', { name: 'Scan channel' })).toBeVisible();
  const autoDownloadToggle = page.getByRole('button', { name: 'Auto-download' });
  await expect(autoDownloadToggle).toHaveAttribute('aria-pressed', /^(true|false)$/);
  await expect(autoDownloadToggle).toContainText(/Auto-download (on|off)/);
  const wasAutoDownloadEnabled = (await autoDownloadToggle.getAttribute('aria-pressed')) === 'true';
  await autoDownloadToggle.click();
  await expect(autoDownloadToggle).toHaveAttribute('aria-pressed', wasAutoDownloadEnabled ? 'false' : 'true');
  await expect(autoDownloadToggle).toContainText(wasAutoDownloadEnabled ? 'Auto-download off' : 'Auto-download on');
  await expect(page.getByText(`Daily auto-download ${wasAutoDownloadEnabled ? 'disabled' : 'enabled'}.`)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Subscribe' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Join' })).toHaveCount(0);
  await expect(page.getByLabel('Sort by')).toHaveValue('newest');
  const moonBrief = page.locator('article.video-tile').filter({ hasText: 'E2E Catalog Moon Brief' });
  const orbitBrief = page.locator('article.video-tile').filter({ hasText: 'E2E Catalog Orbit Brief' });
  await expect(moonBrief).toContainText('2026');
  await expect(orbitBrief.getByTestId('catalog-download-button')).toBeEnabled();
  await moonBrief.getByRole('button', { name: 'Download E2E Catalog Moon Brief' }).click();
  await emitLiveJobs(page, [downloadJob('e2e-catalog-download', 'running', 0.37)]);
  await expect(moonBrief.getByTestId('catalog-download-button')).toHaveText('Downloading');
  await expect(moonBrief.getByTestId('download-progress-percent')).toHaveText('37%');
  await expect(moonBrief.getByRole('progressbar', { name: 'Download progress for E2E Catalog Moon Brief' })).toHaveAttribute('aria-valuenow', '37');
  await expect(orbitBrief.getByTestId('catalog-download-button')).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Queue active' })).toHaveCount(0);

  await page.goto('/videos/e2e-catalog-video-2');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'E2E Catalog Orbit Brief' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download video' })).toBeEnabled();
  await page.getByRole('button', { name: 'Download video' }).click();
  await emitLiveJobs(page, [downloadJob('e2e-video-page-download', 'running', 0.42)]);
  await expect(page.getByRole('button', { name: 'Downloading video' })).toBeVisible();
  await expect(page.getByTestId('download-progress-percent')).toHaveText('42%');
  await expect(page.getByRole('progressbar', { name: 'Download progress for E2E Catalog Orbit Brief' })).toHaveAttribute('aria-valuenow', '42');

  await page.goto('/channels/e2e-channel');

  await page.getByLabel('Sort by').selectOption('oldest');
  await expect(page).toHaveURL(/sort=oldest/);
  await expect(page.locator('article.video-tile').first()).toContainText('E2E Lunar Archive Smoke');
  expect(legacyRouteVideoListRequests).toBe(0);

  await page.goto('/');
  await topActions.getByRole('link', { name: 'Open queue' }).click();
  await expect(page.getByLabel('Durable jobs')).toBeVisible();
  await page.getByLabel('Channel URL').fill('https://www.youtube.com/@queued-smoke');
  await page.getByRole('button', { name: 'Add channel' }).click();
  await expect(page.getByText('Channel job queued.')).toBeVisible();
  await expect(page.getByTestId('job-dashboard')).toContainText('Add channel');
  await topActions.getByRole('link', { name: 'Settings' }).click();
  await expect(page.getByLabel('Readiness diagnostics')).toBeVisible();
});

test('catalog-only pages make media availability explicit', async ({ page }) => {
  await page.route('**/api/videos/e2e-catalog-video-2', async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, description: '' } });
  });

  await page.goto('/');
  const playableCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  const catalogCard = page.getByTestId('video-card').filter({ hasText: 'E2E Catalog Orbit Brief' });
  await expect(catalogCard.locator('.thumb')).toHaveClass(/catalog-only/);
  await expect(playableCard.locator('.tile-meta')).not.toContainText('Playable local media');
  await expect(playableCard.locator('.tile-meta')).not.toContainText('Ready to watch locally');
  await expect(catalogCard.locator('.tile-meta')).not.toContainText('Metadata only');
  await expect(catalogCard.locator('.tile-meta')).not.toContainText('No media file downloaded yet');

  await page.goto('/videos/e2e-catalog-video-2');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.getByLabel('Video player')).toHaveCount(0);
  await expect(page.getByLabel('Video media unavailable locally')).toBeVisible();
  await expect(page.getByTestId('media-availability')).toContainText('Metadata only - no media file downloaded yet');
  await expect(page.getByTestId('video-download-button')).toHaveText('Download video');
  await expect(page.locator('.action-row button').first()).toHaveAttribute('data-testid', 'video-download-button');
  await expect(page.getByTestId('keep-forever-toggle')).toHaveText('Protect from cleanup');
  await expect(page.getByLabel('Video description')).toHaveClass(/compact-empty/);
  await expect(page.getByLabel('Video description')).toContainText('No description imported yet.');
  await expect(page.getByLabel('Comments')).toHaveClass(/compact-empty/);
  await expect(page.getByLabel('Comments')).toContainText('No comments imported yet.');
  await expect(page.getByTestId('video-detail-route')).not.toContainText('Archived locally');
});

test('catalog download success snapshots refresh route data once', async ({ page }) => {
  let videoRequests = 0;
  const succeededJob = { ...downloadJob('catalog-repeat-success', 'succeeded', 1), updated_at: '2026-05-04T12:00:05Z' };
  await page.route('**/api/videos/e2e-catalog-video-2', async route => {
    videoRequests += 1;
    await route.continue();
  });
  await page.route('**/api/videos/e2e-catalog-video-2/download', async route => {
    await route.fulfill({ json: downloadJob('catalog-repeat-success', 'queued', 0), status: 202 });
  });

  await page.goto('/videos/e2e-catalog-video-2');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await page.getByTestId('video-download-button').click();
  await expect(page.getByTestId('video-download-button')).toHaveText('Downloading video');

  await emitLiveJobs(page, [succeededJob]);
  await expect.poll(() => videoRequests).toBe(2);

  await emitLiveJobs(page, [succeededJob]);
  await page.waitForTimeout(500);
  expect(videoRequests).toBe(2);
});

test('in-app video navigation starts at the top after scrolling', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('library-route')).toBeVisible();
  await page.evaluate(() => {
    const spacer = document.createElement('div');
    spacer.style.height = '1200px';
    spacer.dataset.testid = 'scroll-spacer';
    document.querySelector('[data-testid="library-videos"]')?.before(spacer);
  });

  const startedLibraryCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  await startedLibraryCard.scrollIntoViewIfNeeded();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(500);
  await startedLibraryCard.getByRole('link', { name: 'Watch E2E Lunar Archive Smoke' }).click();

  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);

  await page.getByRole('link', { name: 'Yummle' }).click();
  await expect(page.getByTestId('library-route')).toBeVisible();
  await page.evaluate(() => {
    const spacer = document.createElement('div');
    spacer.style.height = '1200px';
    spacer.dataset.testid = 'same-route-scroll-spacer';
    document.querySelector('[data-testid="library-videos"]')?.before(spacer);
  });
  await startedLibraryCard.scrollIntoViewIfNeeded();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(500);
  await page.getByRole('link', { name: 'Home' }).click();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);

  await startedLibraryCard.scrollIntoViewIfNeeded();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(500);
  await page.getByRole('link', { name: 'Yummle' }).click();
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
});

test('video detail shows an existing active catalog download job', async ({ page }) => {
  await page.route('**/api/videos/e2e-catalog-video-2', async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, active_download_job: downloadJob('existing-catalog-download', 'running', 0.82) } });
  });

  await page.goto('/videos/e2e-catalog-video-2');

  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Downloading video' })).toBeVisible();
  await expect(page.getByTestId('download-progress-percent')).toHaveText('82%');
  await expect(page.getByRole('progressbar', { name: 'Download progress for E2E Catalog Orbit Brief' })).toHaveAttribute('aria-valuenow', '82');
});

test('watch player hides captions control when no subtitles exist', async ({ page }) => {
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, subtitles: [] } });
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video track[kind="captions"]')).toHaveCount(0);
  await expect(page.locator('video-skin media-captions-button')).toHaveAttribute('hidden', '');
  await expect(page.locator('video-skin media-captions-button')).toHaveAttribute('aria-hidden', 'true');
});

test('watch player skips sponsor segments during playback', async ({ page }) => {
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, sponsor_segments: [{ start_seconds: 10, end_seconds: 15 }] } });
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();

  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 10 });
    Object.defineProperty(media, 'duration', { configurable: true, value: 125 });
    media.dispatchEvent(new Event('seeked', { bubbles: true }));
  });
  await expect.poll(() => page.locator('video').evaluate(media => Math.floor(media.currentTime))).toBe(15);

  await page.locator('video').evaluate(media => {
    media.currentTime = 10;
    media.dispatchEvent(new Event('timeupdate', { bubbles: true }));
  });
  await expect.poll(() => page.locator('video').evaluate(media => Math.floor(media.currentTime))).toBe(15);
});

test('watch player preserves speed when timeline previews arrive', async ({ page }) => {
  let videoRequests = 0;
  let releasePreviewRefresh;
  const previewRefreshCanFinish = new Promise(resolve => {
    releasePreviewRefresh = resolve;
  });
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    videoRequests += 1;
    if (videoRequests === 2) await previewRefreshCanFinish;
    await route.fulfill({
      response,
      json: {
        ...body,
        active_preview_job: videoRequests === 1 ? { ...downloadJob('preview-job', 'running', 0.5), type: 'timeline_preview' } : undefined,
        timeline_preview: videoRequests === 1 ? undefined : { sprite_url: '/media/derived/previews/e2e-video/sprite.jpg', vtt_url: '/media/derived/previews/e2e-video.vtt', interval_seconds: 10, frame_width: 160, frame_height: 90, columns: 5, preview_count: 3 },
      },
    });
  });

  await page.goto('/videos/e2e-video');
  const media = page.locator('video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await media.evaluate(element => {
    element.playbackRate = 1.75;
    element.dispatchEvent(new Event('ratechange', { bubbles: true }));
    window.__kapselWatchMediaElement = element;
  });

  await emitLiveJobs(page, [{ ...downloadJob('preview-job', 'succeeded', 1), type: 'timeline_preview' }]);
  await expect.poll(() => videoRequests).toBe(2);
  expect(await page.evaluate(() => window.__kapselWatchMediaElement === document.querySelector('video'))).toBe(true);
  releasePreviewRefresh();

  await expect(media.locator('track[kind="metadata"]')).toHaveAttribute('src', /derived\/previews\/e2e-video\.vtt/);
  expect(await page.evaluate(() => window.__kapselWatchMediaElement === document.querySelector('video'))).toBe(true);
  await expect.poll(() => media.evaluate(element => element.playbackRate)).toBe(1.75);
});

test('watch player refreshes expiring signed media URL after idle wake', async ({ page }) => {
  const initialNowSeconds = 1_893_456_000;
  await page.addInitScript(() => {
    window.__kapselPlayCalls = 0;
    HTMLMediaElement.prototype.play = function () {
      window.__kapselPlayCalls += 1;

      return Promise.resolve();
    };
  });
  await page.clock.setFixedTime(new Date(initialNowSeconds * 1000));
  await page.clock.resume();
  let videoRequests = 0;
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    videoRequests += 1;
    await route.fulfill({
      response,
      json: {
        ...body,
        media_url: videoRequests === 1 ? `/media/videos/sample.mp4?expires=${initialNowSeconds + 60}&signature=expiring` : `/media/videos/sample.mp4?expires=${initialNowSeconds + 3600}&signature=fresh`,
      },
    });
  });

  await page.goto('/videos/e2e-video');
  const media = page.locator('video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(media).toHaveAttribute('src', /signature=expiring/);
  await media.evaluate(element => {
    element.playbackRate = 1.75;
    element.dispatchEvent(new Event('ratechange', { bubbles: true }));
  });
  await page.getByRole('button', { name: 'Seek to 0:42' }).click();
  await expect.poll(() => media.evaluate(element => Math.floor(element.currentTime))).toBe(42);
  await page.clock.setFixedTime(new Date((initialNowSeconds + 31) * 1000));
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));

  await expect.poll(() => videoRequests).toBe(2);
  await expect(media).toHaveAttribute('src', /signature=fresh/);
  await media.evaluate(element => {
    element.playbackRate = 1;
    element.dispatchEvent(new Event('ratechange', { bubbles: true }));
    element.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
  });
  await expect.poll(() => media.evaluate(element => Math.floor(element.currentTime))).toBe(42);
  await expect.poll(() => media.evaluate(element => element.playbackRate)).toBe(1.75);
  expect(await page.evaluate(() => window.__kapselPlayCalls)).toBe(0);
});

test('watch player does not loop when expiring media URL refresh returns same URL', async ({ page }) => {
  const initialNowSeconds = 1_893_456_000;
  const mediaURL = `/media/videos/sample.mp4?expires=${initialNowSeconds + 60}&signature=same`;
  await page.clock.setFixedTime(new Date(initialNowSeconds * 1000));
  await page.clock.resume();
  let videoRequests = 0;
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    videoRequests += 1;
    await route.fulfill({ response, json: { ...body, media_url: mediaURL } });
  });

  await page.goto('/videos/e2e-video');
  const media = page.locator('video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(media).toHaveAttribute('src', /signature=same/);
  await media.evaluate(element => {
    window.__kapselPaused = false;
    window.__kapselPlayCalls = 0;
    Object.defineProperty(element, 'paused', { configurable: true, get: () => window.__kapselPaused });
    element.pause = () => {
      window.__kapselPaused = true;
      element.dispatchEvent(new Event('pause', { bubbles: true }));
    };
    element.play = () => {
      window.__kapselPlayCalls += 1;
      window.__kapselPaused = false;
      element.dispatchEvent(new Event('play', { bubbles: true }));

      return Promise.resolve();
    };
  });
  await page.clock.setFixedTime(new Date((initialNowSeconds + 31) * 1000));

  await media.evaluate(element => {
    element.dispatchEvent(new Event('play', { bubbles: true }));
  });

  await expect.poll(() => videoRequests).toBe(2);
  await page.waitForTimeout(100);
  expect(videoRequests).toBe(2);
  expect(await page.evaluate(() => window.__kapselPlayCalls)).toBe(1);
});

test('watch player retries once when an expired signed media URL errors', async ({ page }) => {
  const initialNowSeconds = 1_893_456_000;
  await page.clock.setFixedTime(new Date(initialNowSeconds * 1000));
  await page.clock.resume();
  let videoRequests = 0;
  let releaseRefresh;
  const refreshCanFinish = new Promise(resolve => {
    releaseRefresh = resolve;
  });
  await page.route('**/api/videos/e2e-video', async route => {
    const response = await route.fetch();
    const body = await response.json();
    videoRequests += 1;
    if (videoRequests === 2) await refreshCanFinish;
    await route.fulfill({
      response,
      json: {
        ...body,
        media_url: videoRequests === 1 ? `/media/videos/sample.mp4?expires=${initialNowSeconds + 3600}&signature=stale` : `/media/videos/sample.mp4?expires=${initialNowSeconds + 7200}&signature=fresh`,
      },
    });
  });

  await page.goto('/videos/e2e-video');
  const media = page.locator('video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(media).toHaveAttribute('src', /signature=stale/);
  await page.clock.setFixedTime(new Date((initialNowSeconds + 3601) * 1000));

  await media.evaluate(element => {
    element.dispatchEvent(new Event('error', { bubbles: true }));
  });
  await expect.poll(() => videoRequests).toBe(2);
  await media.evaluate(element => {
    element.dispatchEvent(new Event('error', { bubbles: true }));
  });
  await page.waitForTimeout(100);
  expect(videoRequests).toBe(2);
  releaseRefresh();

  await expect.poll(() => videoRequests).toBe(2);
  await expect(media).toHaveAttribute('src', /signature=fresh/);
});

test('downloads live updates preserve REST page order', async ({ page }) => {
  const recentJob = (index, status = 'succeeded', progress = 1) => ({
    ...downloadJob(`recent-${String(index).padStart(2, '0')}`, status, progress),
    updated_at: `2026-05-04T12:${String(59 - index).padStart(2, '0')}:00Z`,
  });
  const initialJobs = Array.from({ length: 20 }, (_, index) => recentJob(index));
  const liveJobs = [
    ...Array.from({ length: 50 }, (_, index) => recentJob(index)),
    { ...recentJob(5, 'running', 0.66), updated_at: '2026-05-04T13:00:00Z' },
    { ...downloadJob('recent-new', 'succeeded', 1), updated_at: '2026-05-04T12:58:30.123456789Z' },
    { ...downloadJob('active-old', 'running', 0.82), updated_at: '2026-05-04T11:00:00Z' },
  ];
  const refreshedJobs = [
    { ...recentJob(5, 'running', 0.66), updated_at: '2026-05-04T13:00:00Z' },
    recentJob(0),
    { ...downloadJob('recent-new', 'succeeded', 1), updated_at: '2026-05-04T12:58:30.123456789Z' },
    ...[1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18].map(index => recentJob(index)),
  ];
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    const data = jobsRequests === 1 ? initialJobs : refreshedJobs;
    await route.fulfill({ json: { data, pagination: { page: 1, page_size: 20, total: 61 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="recent-00"]')).toBeVisible();

  await emitLiveJobs(page, liveJobs, { page: 1, page_size: 50, total: 61 });

  await expect.poll(() => jobsRequests).toBe(2);
  await expect(page.locator('[data-job-id="active-old"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="job-dashboard"] .job-card').first()).toHaveAttribute('data-job-id', 'recent-05');
  await expect(page.locator('[data-job-id="recent-00"]')).toBeVisible();
  await expect(page.locator('[data-job-id="recent-new"]')).toBeVisible();
  await expect(page.locator('[data-job-id="recent-05"]')).toContainText('66%');
  await expect(page.locator('[data-job-id="recent-19"]')).toHaveCount(0);
  await expect(page.locator('[data-job-id="recent-20"]')).toHaveCount(0);

  await emitLiveJobs(page, liveJobs, { page: 1, page_size: 50, total: 61 });
  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(2);
});

test('downloads visible live updates do not REST refresh', async ({ page }) => {
  const initialJob = { ...downloadJob('visible-live-job', 'running', 0.2), updated_at: '2026-05-04T12:00:00Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [initialJob], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="visible-live-job"]')).toContainText('20%');

  await emitLiveJobs(page, [{ ...initialJob, progress: 0.5, updated_at: '2026-05-04T12:00:01Z' }]);
  await emitLiveJobs(page, [{ ...initialJob, progress: 0.7, updated_at: '2026-05-04T12:00:02Z' }]);

  await expect(page.locator('[data-job-id="visible-live-job"]')).toContainText('70%');
  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(1);
});

test('downloads visible live reordering does not REST refresh', async ({ page }) => {
  const firstJob = { ...downloadJob('visible-order-a', 'running', 0.2), updated_at: '2026-05-04T12:00:00Z' };
  const secondJob = { ...downloadJob('visible-order-b', 'running', 0.3), updated_at: '2026-05-04T11:59:00Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [firstJob, secondJob], pagination: { page: 1, page_size: 20, total: 2 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-testid="job-dashboard"] .job-card').first()).toHaveAttribute('data-job-id', 'visible-order-a');

  await emitLiveJobs(page, [firstJob, { ...secondJob, progress: 0.7, updated_at: '2026-05-04T12:00:05Z' }], { page: 1, page_size: 50, total: 2 });

  await expect(page.locator('[data-testid="job-dashboard"] .job-card').first()).toHaveAttribute('data-job-id', 'visible-order-b');
  await expect(page.locator('[data-job-id="visible-order-b"]')).toContainText('70%');
  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(1);
});

test('downloads stale live ordering does not move fresher visible rows', async ({ page }) => {
  const firstJob = { ...downloadJob('visible-stale-a', 'running', 0.8), updated_at: '2026-05-04T12:05:00Z' };
  const secondJob = { ...downloadJob('visible-stale-b', 'running', 0.3), updated_at: '2026-05-04T12:04:00Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [firstJob, secondJob], pagination: { page: 1, page_size: 20, total: 2 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-testid="job-dashboard"] .job-card').first()).toHaveAttribute('data-job-id', 'visible-stale-a');

  await emitLiveJobs(page, [{ ...firstJob, progress: 0.1, updated_at: '2026-05-04T12:01:00Z' }, secondJob], { page: 1, page_size: 50, total: 2 });

  await expect(page.locator('[data-testid="job-dashboard"] .job-card').first()).toHaveAttribute('data-job-id', 'visible-stale-a');
  await expect(page.locator('[data-job-id="visible-stale-a"]')).toContainText('80%');
  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(1);
});

test('downloads live updates preserve existing result summaries', async ({ page }) => {
  const summarized = {
    ...downloadJob('summarized-job', 'running', 0.5),
    error: '',
    result_summary: '{"video_id":"e2e-video"}',
    updated_at: '2026-05-04T12:00:01Z',
  };
  let fallbackRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    await route.fulfill({ json: { data: [summarized], pagination: { page: 1, page_size: 20, total: 1 } } });
  });
  await page.route('**/api/downloads', async route => {
    await route.fulfill({ json: downloadJob('summarized-job', 'queued', 0), status: 202 });
  });
  await page.route('**/api/jobs/summarized-job', async route => {
    fallbackRequests += 1;
    await route.fulfill({ json: { ...downloadJob('summarized-job', 'running', 0.6), updated_at: '2026-05-04T12:00:02Z' } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="summarized-job"]')).toContainText('"video_id":"e2e-video"');
  await page.getByTestId('direct-download-url').fill('https://www.youtube.com/watch?v=abc123DEF45');
  await page.getByTestId('direct-download-submit').click();
  await expect(page.getByTestId('direct-download-status')).toContainText('Video download queued.');

  await emitLiveJobs(page, [{ ...downloadJob('other-running-job', 'running', 0.2), updated_at: '2026-05-04T12:00:03Z' }]);

  await expect.poll(() => fallbackRequests).toBe(1);
  await expect(page.locator('[data-job-id="summarized-job"]')).toContainText('"video_id":"e2e-video"');
});

test('downloads page can queue another video while the previous video job is active', async ({ page }) => {
  const submittedURLs = [];
  await page.route('**/api/jobs?*', async route => {
    await route.fulfill({ json: { data: [], pagination: { page: 1, page_size: 20, total: 0 } } });
  });
  await page.route('**/api/downloads', async route => {
    submittedURLs.push(route.request().postDataJSON().url);
    await route.fulfill({ json: downloadJob(`direct-queue-${submittedURLs.length}`, 'queued', 0), status: 202 });
  });

  await page.goto('/downloads');
  const urlInput = page.getByTestId('direct-download-url');
  const submit = page.getByTestId('direct-download-submit');

  await urlInput.fill('https://www.youtube.com/watch?v=abc123DEF45');
  await submit.click();
  await expect.poll(() => submittedURLs.length).toBe(1);
  await expect(page.getByTestId('direct-download-status')).toContainText('Video download queued.');

  await urlInput.fill('https://www.youtube.com/watch?v=def123GHI45');
  await expect(submit).toBeEnabled();
  await submit.click();

  await expect.poll(() => submittedURLs.length).toBe(2);
  expect(submittedURLs).toEqual([
    'https://www.youtube.com/watch?v=abc123DEF45',
    'https://www.youtube.com/watch?v=def123GHI45',
  ]);
});

test('downloads page reflects duplicate active video job status', async ({ page }) => {
  let downloadRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    await route.fulfill({ json: { data: [], pagination: { page: 1, page_size: 20, total: 0 } } });
  });
  await page.route('**/api/downloads', async route => {
    downloadRequests += 1;
    const status = downloadRequests === 1 ? 'queued' : 'running';
    await route.fulfill({ json: downloadJob('duplicate-active-job', status, status === 'running' ? 0.4 : 0), status: 202 });
  });

  await page.goto('/downloads');
  await page.getByTestId('direct-download-url').fill('https://www.youtube.com/watch?v=abc123DEF45');
  await page.getByTestId('direct-download-submit').click();
  await expect(page.getByTestId('direct-download-status')).toContainText('Video download queued.');

  await page.getByTestId('direct-download-url').fill('https://www.youtube.com/watch?v=abc123DEF45');
  await page.getByTestId('direct-download-submit').click();

  await expect.poll(() => downloadRequests).toBe(2);
  await expect(page.getByTestId('direct-download-status')).toContainText('Downloading video...');
});

test('failed jobs summarize long logs while preserving raw details', async ({ page }) => {
  const jobID = '01234567-89ab-cdef-0123-456789abcdef';
  const rawError = [
    'Command failed with exit code 1: ffmpeg -hide_banner -i archive-input.webm archive-output.mp4',
    'ffmpeg version 7.1 Copyright (c) the FFmpeg developers',
    'frame=9000 fps=118 q=-1.0 size=262144kB time=00:05:00.00 bitrate=7158.3kbits/s speed=3.95x',
    'ERROR: unable to download video data from upstream after retries',
    'stderr: a very long diagnostic line that should stay available for debugging rather than dominating the job card by default',
  ].join('\n');
  const failedJob = {
    ...downloadJob(jobID, 'failed', 1),
    attempts: 3,
    max_attempts: 3,
    error: rawError,
    updated_at: '2026-05-04T12:15:00Z',
  };
  await page.route('**/api/jobs?*', async route => {
    await route.fulfill({ json: { data: [failedJob], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');

  const card = page.locator(`[data-job-id="${jobID}"]`);
  await expect(card).toBeVisible();
  await expect(card.locator('h3')).toHaveText('01234567...abcdef');
  await expect(card.getByTestId('job-full-id')).toHaveText(jobID);
  await expect(card.getByTestId('job-error-summary')).toContainText('ERROR: unable to download video data from upstream after retries');
  await expect(card.getByTestId('job-error-summary')).not.toContainText('frame=9000');
  await expect(card.getByTestId('job-raw-error')).toBeHidden();

  await card.getByText('Show raw error log').click();
  await expect(card.getByTestId('job-raw-error')).toHaveValue(/frame=9000/);
  await expect(card.getByRole('button', { name: 'Copy raw log' })).toBeVisible();
});

test('settings diagnostics show readiness summary before collapsed raw JSON', async ({ page }) => {
  await page.route('**/api/settings', async route => {
    await route.fulfill({
      json: {
        configuration: {
          addr: '127.0.0.1:18080',
          auth_mode: 'local',
          data_dir: '/tmp/kapsel-data',
          db_path: '/tmp/kapsel-data/kapsel.db',
          import_root: '/tmp/kapsel-imports',
          media_root: '/tmp/kapsel-media',
          media_signing_configured: true,
          authentication_configured: false,
          session_secret_configured: false,
          media_url_ttl_seconds: 3600,
          min_free_space_bytes: 1073741824,
          previews_enabled: true,
          sponsorblock_enabled: true,
          ffmpeg_path: '/usr/bin/ffmpeg',
          yt_dlp_path: '/usr/bin/yt-dlp',
        },
        checks: [
          { id: 'media_signing', label: 'Media signing', state: 'pass', summary: 'Stable media URL signing is configured.' },
          { id: 'authentication', label: 'Authentication', state: 'warn', summary: 'Local auth is configured with an ephemeral session secret.' },
          { id: 'storage', label: 'Storage', state: 'error', summary: 'Storage readiness needs attention.', detail: 'media root has no free space' },
        ],
        yt_dlp: { available: true, version: '2026.05.01', minimum_tested_version: '2025.01.01' },
        storage: {
          ok: false,
          minimum_free_bytes: 1073741824,
          paths: [{ path: '/tmp/kapsel-media', available_bytes: 1024, warning: 'media root is below the configured free-space floor' }],
        },
        storage_maintenance: {
          media_files: 3,
          media_bytes: 4096,
          orphan_files: 2,
          orphan_bytes: 2048,
          missing_references: 1,
        },
      },
    });
  });

  await page.goto('/settings');

  const summary = page.getByTestId('readiness-summary');
  await expect(summary).toBeVisible();
  await expect(summary).toContainText('1 blocked');
  await expect(summary).toContainText('1 warning');
  await expect(page.locator('[data-testid="readiness-summary"] ~ [data-testid="readiness-checks"]')).toHaveCount(1);

  const rawDiagnostics = page.getByTestId('diagnostics-json');
  await expect(rawDiagnostics).toBeHidden();
  await expect(page.getByRole('button', { name: 'Copy diagnostics' })).toBeVisible();
  await page.getByText('Show raw diagnostics JSON').click();
  await expect(rawDiagnostics).toBeVisible();
  await expect(rawDiagnostics).toHaveValue(/"authentication_configured": false/);
});

test('downloads live updates respect filtered empty pages', async ({ page }) => {
  const pageOneJob = { ...downloadJob('page-one-job', 'succeeded', 1), updated_at: '2026-05-04T12:00:02Z' };
  const filteredFailedJob = { ...downloadJob('filtered-failed-job', 'failed', 1), error: 'failed now', updated_at: '2026-05-04T12:00:03Z' };
  let failedRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('status') === 'failed') {
      failedRequests += 1;
      const data = failedRequests === 1 ? [] : [filteredFailedJob];
      await route.fulfill({ json: { data, pagination: { page: 1, page_size: 20, total: data.length } } });
      return;
    }
    await route.fulfill({ json: { data: [pageOneJob], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="page-one-job"]')).toBeVisible();
  await page.locator('.job-filter-row').getByRole('button', { name: 'Failed' }).click();
  await expect(page.getByText('No jobs match this view.')).toBeVisible();

  await emitLiveJobs(page, [
    { ...downloadJob('ignored-running-job', 'running', 0.6), updated_at: '2026-05-04T12:00:04Z' },
    filteredFailedJob,
  ]);

  await expect.poll(() => failedRequests).toBe(2);
  await expect(page.locator('[data-job-id="filtered-failed-job"]')).toBeVisible();
  await expect(page.locator('[data-job-id="ignored-running-job"]')).toHaveCount(0);
});

test('downloads filtered pages ignore incomplete live snapshots', async ({ page }) => {
  const allJob = { ...downloadJob('all-running-job', 'running', 0.4), updated_at: '2026-05-04T12:00:05Z' };
  const failedJobs = [
    { ...downloadJob('filtered-visible-1', 'failed', 1), error: 'failed one', updated_at: '2026-05-04T11:00:00Z' },
    { ...downloadJob('filtered-visible-2', 'failed', 1), error: 'failed two', updated_at: '2026-05-04T10:00:00Z' },
  ];
  let failedRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('status') === 'failed') {
      failedRequests += 1;
      await route.fulfill({ json: { data: failedJobs, pagination: { page: 1, page_size: 20, total: 25 } } });
      return;
    }
    await route.fulfill({ json: { data: [allJob], pagination: { page: 1, page_size: 20, total: 100 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="all-running-job"]')).toBeVisible();
  await page.locator('.job-filter-row').getByRole('button', { name: 'Failed' }).click();
  await expect(page.locator('[data-job-id="filtered-visible-1"]')).toBeVisible();

  await emitLiveJobs(page, [allJob], { page: 1, page_size: 50, total: 100 });

  await page.waitForTimeout(500);
  expect(failedRequests).toBe(1);
  await expect(page.locator('[data-job-id="filtered-visible-2"]')).toBeVisible();
});

test('downloads filtered later pages reconcile when live snapshot covers them', async ({ page }) => {
  const failedJob = (index, updatedAt) => ({ ...downloadJob(`filtered-page-${index}`, 'failed', 1), error: `failed ${index}`, updated_at: updatedAt });
  const pageOneJobs = Array.from({ length: 20 }, (_, index) => failedJob(index, `2026-05-04T12:${String(59 - index).padStart(2, '0')}:00Z`));
  const initialPageTwoJob = failedJob(20, '2026-05-04T12:39:00Z');
  const newFailedJob = { ...downloadJob('filtered-page-new', 'failed', 1), error: 'new failure', updated_at: '2026-05-04T13:00:00Z' };
  let pageTwoRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('status') === 'failed') {
      const pageNumber = Number(url.searchParams.get('page') || '1');
      if (pageNumber === 2) pageTwoRequests += 1;
      const data = pageNumber === 1 ? pageOneJobs : pageTwoRequests === 1 ? [initialPageTwoJob] : [pageOneJobs[19], initialPageTwoJob];
      await route.fulfill({ json: { data, pagination: { page: pageNumber, page_size: 20, total: pageTwoRequests > 1 ? 22 : 21 } } });
      return;
    }
    await route.fulfill({ json: { data: [downloadJob('all-filtered-page-job', 'running', 0.2)], pagination: { page: 1, page_size: 20, total: 100 } } });
  });

  await page.goto('/downloads');
  await page.locator('.job-filter-row').getByRole('button', { name: 'Failed' }).click();
  await page.getByLabel('Job list pagination').getByRole('button', { name: 'Next' }).click();
  await expect(page.locator('[data-job-id="filtered-page-20"]')).toBeVisible();

  await emitLiveJobs(page, [newFailedJob, ...pageOneJobs, initialPageTwoJob], { page: 1, page_size: 50, total: 100 });

  await expect.poll(() => pageTwoRequests).toBe(2);
  await expect(page.locator('[data-job-id="filtered-page-19"]')).toBeVisible();
  await expect(page.locator('[data-job-id="filtered-page-20"]')).toBeVisible();
});

test('downloads live updates avoid inserting new jobs on later pages', async ({ page }) => {
  const pageTwoJob = { ...downloadJob('page-two-job', 'running', 0.2), updated_at: '2026-05-04T12:00:00.1Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [pageTwoJob], pagination: { page: 2, page_size: 20, total: 75 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="page-two-job"]')).toBeVisible();

  await emitLiveJobs(page, [
    { ...pageTwoJob, progress: 0.75, updated_at: '2026-05-04T12:00:05Z' },
    { ...downloadJob('new-page-one-job', 'succeeded', 1), updated_at: '2026-05-04T12:00:06Z' },
  ]);

  await expect.poll(() => jobsRequests).toBe(2);
  await expect(page.locator('[data-job-id="page-two-job"]')).toContainText('75%');
  await expect(page.locator('[data-job-id="new-page-one-job"]')).toHaveCount(0);
});

test('downloads live refresh does not override user pagination', async ({ page }) => {
  const pageOneJob = { ...downloadJob('page-one-user-job', 'running', 0.2), updated_at: '2026-05-04T12:00:00Z' };
  const pageTwoJob = { ...downloadJob('page-two-user-job', 'running', 0.4), updated_at: '2026-05-04T11:59:59Z' };
  let pageTwoRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    const url = new URL(route.request().url());
    const requestedPage = Number(url.searchParams.get('page') || '1');
    if (requestedPage === 2) {
      pageTwoRequests += 1;
      if (pageTwoRequests === 1) await new Promise(resolve => setTimeout(resolve, 500));
      if (pageTwoRequests > 1) {
        await route.fulfill({ status: 500, body: 'late live refresh failed' });
        return;
      }
    }
    const data = requestedPage === 2 ? [pageTwoJob] : [pageOneJob];
    await route.fulfill({ json: { data, pagination: { page: requestedPage, page_size: 20, total: 40 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="page-one-user-job"]')).toBeVisible();
  await emitLiveJobs(page, [{ ...pageOneJob, progress: 0.6, updated_at: '2026-05-04T12:00:05Z' }], { page: 1, page_size: 50, total: 40 });
  await page.getByLabel('Job list pagination').getByRole('button', { name: 'Next' }).click();
  await emitLiveJobs(page, [{ ...pageTwoJob, progress: 0.7, updated_at: '2026-05-04T12:00:06Z' }], { page: 1, page_size: 1, total: 40 });

  await expect(page.locator('[data-job-id="page-two-user-job"]')).toBeVisible();
  await expect(page.locator('[data-job-id="page-two-user-job"]')).toContainText('70%');
  await expect(page.locator('[data-job-id="page-one-user-job"]')).toHaveCount(0);
});

test('downloads manual REST refresh keeps fresher row data', async ({ page }) => {
  const initialJob = { ...downloadJob('fresh-rest-job', 'running', 0.2), updated_at: '2026-05-04T12:00:00Z' };
  const liveJob = { ...initialJob, progress: 0.5, updated_at: '2026-05-04T12:00:01Z' };
  const refreshedJob = { ...initialJob, progress: 0.9, updated_at: '2026-05-04T12:00:02Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [jobsRequests === 1 ? initialJob : refreshedJob], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="fresh-rest-job"]')).toContainText('20%');
  await emitLiveJobs(page, [liveJob]);
  await expect(page.locator('[data-job-id="fresh-rest-job"]')).toContainText('50%');
  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(1);

  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect.poll(() => jobsRequests).toBe(2);
  await expect(page.locator('[data-job-id="fresh-rest-job"]')).toContainText('90%');
});

test('downloads REST refresh keeps fresher visible live rows', async ({ page }) => {
  const restJob = { ...downloadJob('fresh-live-job', 'running', 0.5), updated_at: '2026-05-04T12:00:05Z' };
  const freshLiveJob = { ...restJob, progress: 0.7, updated_at: '2026-05-04T12:00:07Z' };
  const staleLiveJob = { ...restJob, progress: 0.1, updated_at: '2026-05-04T12:00:01Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    await route.fulfill({ json: { data: [restJob], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="fresh-live-job"]')).toContainText('50%');
  await emitLiveJobs(page, [freshLiveJob]);
  await expect(page.locator('[data-job-id="fresh-live-job"]')).toContainText('70%');

  await page.getByRole('button', { name: 'Refresh' }).click();
  await expect.poll(() => jobsRequests).toBe(2);
  await expect(page.locator('[data-job-id="fresh-live-job"]')).toContainText('70%');

  await emitLiveJobs(page, [staleLiveJob]);

  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(2);
  await expect(page.locator('[data-job-id="fresh-live-job"]')).toContainText('70%');
});

test('downloads stale live updates do not clear refresh errors', async ({ page }) => {
  const job = { ...downloadJob('stale-error-job', 'running', 0.5), updated_at: '2026-05-04T12:00:05Z' };
  const newJob = { ...downloadJob('new-error-job', 'running', 0.1), updated_at: '2026-05-04T12:00:07Z' };
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests += 1;
    if (jobsRequests > 1) {
      await route.fulfill({ status: 500, body: 'refresh failed' });
      return;
    }
    await route.fulfill({ json: { data: [job], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="stale-error-job"]')).toContainText('50%');
  await emitLiveJobs(page, [{ ...job, progress: 0.7, updated_at: '2026-05-04T12:00:06Z' }, newJob]);
  await expect(page.getByText('Job refresh failed.')).toBeVisible();
  await expect(page.locator('[data-job-id="stale-error-job"]')).toContainText('70%');
  const requestsAfterFailure = jobsRequests;

  await emitLiveJobs(page, [{ ...job, progress: 0.1, updated_at: '2026-05-04T12:00:01Z' }]);

  await page.waitForTimeout(500);
  expect(jobsRequests).toBe(requestsAfterFailure);
  await expect(page.getByText('Job refresh failed.')).toBeVisible();
  await expect(page.locator('[data-job-id="stale-error-job"]')).toContainText('70%');
});

test('downloads polling resumes while websocket reconnects fail', async ({ page }) => {
  let jobsRequests = 0;
  await page.route('**/api/jobs?*', async route => {
    jobsRequests++;
    await route.fulfill({ json: { data: [downloadJob('polling-job', 'running', 0.25)], pagination: { page: 1, page_size: 20, total: 1 } } });
  });

  await page.goto('/downloads');
  await expect(page.locator('[data-job-id="polling-job"]')).toBeVisible();
  await page.evaluate(() => {
    window.__kapselLiveFailOpen = true;
    for (const socket of window.__kapselLiveSockets || []) socket.close();
  });

  await expect.poll(() => jobsRequests, { timeout: 7000 }).toBeGreaterThan(1);
});

test('up next overlay appears on video end with countdown and navigation', async ({ page }) => {
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('ended', { bubbles: true }));
  });

  await expect(page.getByTestId('up-next-overlay')).toBeVisible();
  await expect(page.getByTestId('up-next-countdown')).toHaveText('5');

  await page.getByTestId('up-next-cancel').click();
  await expect(page.getByTestId('up-next-overlay')).toHaveCount(0);

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('ended', { bubbles: true }));
  });

  await expect(page.getByTestId('up-next-overlay')).toBeVisible();
  await page.getByTestId('up-next-play-now').click();
  await expect(page.getByTestId('up-next-overlay')).toHaveCount(0);
});

test('up next renders endpoint order with playable videos before catalog-only entries', async ({ page }) => {
  let upNextRequests = 0;
  let legacyCandidateRequests = 0;
  await page.route('**/api/videos/e2e-video/up-next?*', async route => {
    const url = new URL(route.request().url());
    expect(url.searchParams.get('page_size')).toBe('12');
    upNextRequests += 1;
    const data = [
      mockUpNextVideo('already-watched-candidate', 'Already Watched Candidate', 'other-channel', '2026-05-10T12:00:00Z', { available: true, watched: true }),
      mockUpNextVideo('newer-available-unrelated', 'Newer Available Unrelated', 'other-channel', '2026-05-08T12:00:00Z', { available: true }),
      mockUpNextVideo('same-channel-started', 'Same Channel Started', 'e2e-channel', '2026-05-03T12:00:00Z', { available: true, positionSeconds: 42 }),
      mockUpNextVideo('same-channel-unstarted-available', 'Same Channel Unstarted Available', 'e2e-channel', '2026-05-04T12:00:00Z', { available: true }),
      mockUpNextVideo('same-channel-catalog-newest', 'Same Channel Catalog Newest', 'e2e-channel', '2026-05-09T12:00:00Z'),
      mockUpNextVideo('catalog-remaining', 'Catalog Remaining', 'other-channel', '2026-05-07T12:00:00Z'),
    ];
    await route.fulfill({ json: { data, pagination: { page: 1, page_size: 12, total: data.length } } });
  });
  await page.route('**/api/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/videos') {
      await route.continue();
      return;
    }
    if (url.searchParams.get('home') === '1' || url.searchParams.get('channel')) legacyCandidateRequests += 1;
    await route.continue();
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  await expect.poll(() => upNextRequests).toBe(1);
  expect(legacyCandidateRequests).toBe(0);

  const recommendationTitles = page.locator('.recommendation-title');
  await expect(recommendationTitles.nth(0)).toHaveText('Newer Available Unrelated');
  await expect(recommendationTitles.nth(1)).toHaveText('Same Channel Started');
  await expect(recommendationTitles.nth(2)).toHaveText('Same Channel Unstarted Available');
  await expect(recommendationTitles.nth(3)).toHaveText('Same Channel Catalog Newest');
  await expect(recommendationTitles.nth(4)).toHaveText('Catalog Remaining');
  await expect(page.getByText('Already Watched Candidate')).toHaveCount(0);

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('ended', { bubbles: true }));
  });

  await expect(page.getByTestId('up-next-overlay')).toContainText('Newer Available Unrelated');
  await expect(page.getByTestId('up-next-backdrop')).toHaveAttribute('src', /newer-available-unrelated\.jpg/);
});

test('up next countdown navigates to next video automatically', async ({ page }) => {
  await page.clock.setFixedTime(Date.now());
  await page.clock.resume();

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('ended', { bubbles: true }));
  });

  await expect(page.getByTestId('up-next-overlay')).toBeVisible();
  await expect(page.getByTestId('up-next-countdown')).toHaveText('5');

  await page.clock.fastForward(5000);

  await expect(page).not.toHaveURL(/\/videos\/e2e-video$/);
  await expect(page.getByTestId('up-next-overlay')).toHaveCount(0);
  const didAutoplay = await page.evaluate(() => !!window.__kapselUpNextAutoplay);
  expect(didAutoplay).toBe(true);
});

test('watch page preserves saved progress during initial media events', async ({ page }) => {
  const progressWrites = [];
  await page.route('**/api/videos/e2e-video/progress', async route => {
    if (route.request().method() === 'PUT') {
      progressWrites.push(route.request().postDataJSON());
    }
    await route.continue();
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();

  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 0 });
    media.dispatchEvent(new Event('timeupdate', { bubbles: true }));
  });
  await page.waitForTimeout(250);

  const response = await page.request.get('/api/videos/e2e-video/progress');
  await expect(response).toBeOK();
  await expect(response.json()).resolves.toMatchObject({ position_seconds: 42, duration_seconds: 125, watched: false });
  expect(progressWrites).toEqual([]);

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await expect.poll(() => progressWrites.length).toBe(1);
  expect(progressWrites[0]).toMatchObject({ position_seconds: 42, duration_seconds: 125, watched: false });
});

test('watch progress refreshes home thumbnails on entry instead of mutating cached rows', async ({ page }) => {
  let homeRequests = 0;
  let releaseHomeRefresh;
  const homeRefreshCanFinish = new Promise(resolve => {
    releaseHomeRefresh = resolve;
  });
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/home/videos') {
      await route.continue();
      return;
    }
    homeRequests += 1;
    if (homeRequests === 2) await homeRefreshCanFinish;
    await route.continue();
  });

  await page.goto('/');
  const homeCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  await expectPlaybackProgress(homeCard, 34);

  await page.getByRole('link', { name: 'Watch E2E Lunar Archive Smoke' }).click();
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 65 });
    Object.defineProperty(media, 'duration', { configurable: true, value: 125 });
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await expect.poll(async () => {
    const response = await page.request.get('/api/videos/e2e-video/progress');
    const body = await response.json();
    return body.position_seconds;
  }).toBe(65);

  await page.getByRole('link', { name: 'Yummle' }).click();
  await expect.poll(() => homeRequests).toBe(2);
  await expectPlaybackProgress(homeCard, 34);

  releaseHomeRefresh();
  await expectPlaybackProgress(homeCard, 52);
});

test('watch progress invalidation follows stale in-flight home refreshes', async ({ page }) => {
  let homeRequests = 0;
  let staleHomeBody = '';
  let releaseStaleHomeRefresh;
  const staleHomeRefreshCanFinish = new Promise(resolve => {
    releaseStaleHomeRefresh = resolve;
  });
  await page.route('**/api/home/videos?*', async route => {
    const url = new URL(route.request().url());
    if (url.pathname !== '/api/home/videos') {
      await route.continue();
      return;
    }
    homeRequests += 1;
    if (homeRequests === 1) {
      const response = await route.fetch();
      staleHomeBody = await response.text();
      await route.fulfill({ response, body: staleHomeBody });
      return;
    }
    if (homeRequests === 2) {
      await staleHomeRefreshCanFinish;
      await route.fulfill({ status: 200, contentType: 'application/json', body: staleHomeBody });
      return;
    }
    await route.continue();
  });

  let releaseProgressSave;
  let resolveProgressStarted;
  const progressStarted = new Promise(resolve => {
    resolveProgressStarted = resolve;
  });
  const progressCanFinish = new Promise(resolve => {
    releaseProgressSave = resolve;
  });
  await page.route('**/api/videos/e2e-video/progress', async route => {
    if (route.request().method() !== 'PUT') {
      await route.continue();
      return;
    }
    resolveProgressStarted();
    await progressCanFinish;
    await route.continue();
  });

  await page.goto('/');
  const homeCard = page.getByTestId('video-card').filter({ hasText: 'E2E Lunar Archive Smoke' });
  await expectPlaybackProgress(homeCard, 34);

  await page.getByRole('link', { name: 'Watch E2E Lunar Archive Smoke' }).click();
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  const progressResponse = page.waitForResponse(response => response.url().includes('/api/videos/e2e-video/progress') && response.request().method() === 'PUT');
  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 65 });
    Object.defineProperty(media, 'duration', { configurable: true, value: 125 });
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await progressStarted;

  await page.getByRole('link', { name: 'Yummle' }).click();
  await expect.poll(() => homeRequests).toBe(2);
  releaseProgressSave();
  await progressResponse;
  releaseStaleHomeRefresh();

  await expect.poll(() => homeRequests).toBe(3);
  await expectPlaybackProgress(homeCard, 52);
});

test('watch page can mark the current video as played', async ({ page }) => {
  const progressWrites = [];
  await page.route('**/api/videos/e2e-video/progress', async route => {
    if (route.request().method() === 'PUT') {
      progressWrites.push(route.request().postDataJSON());
    }
    await route.continue();
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  const markPlayedButton = page.getByTestId('mark-played-button');
  await expect(markPlayedButton).toBeVisible();

  await markPlayedButton.click();

  await expect.poll(() => progressWrites.length).toBe(1);
  expect(progressWrites[0]).toMatchObject({ position_seconds: 125, duration_seconds: 125, watched: true });
  await expect.poll(async () => {
    const response = await page.request.get('/api/videos/e2e-video/progress');
    const body = await response.json();
    return body;
  }).toMatchObject({ position_seconds: 125, duration_seconds: 125, watched: true });
  await expect(markPlayedButton).toHaveCount(0);
});

test('watch page confirms before deleting local video media', async ({ page }) => {
  const initialResponse = await page.request.get('/api/videos/e2e-video');
  await expect(initialResponse).toBeOK();
  const initialDetail = await initialResponse.json();
  let deleteRequests = 0;
  let mediaDeleted = false;

  await page.route('**/api/videos/e2e-video', async route => {
    if (route.request().method() !== 'GET') {
      await route.continue();
      return;
    }
    const body = { ...initialDetail };
    if (mediaDeleted) {
      delete body.media_url;
      body.archive_state = 'catalog-only';
      body.can_download = true;
    }
    await route.fulfill({ json: body });
  });
  await page.route('**/api/videos/e2e-video/media', async route => {
    if (route.request().method() !== 'DELETE') {
      await route.continue();
      return;
    }
    deleteRequests += 1;
    mediaDeleted = true;
    await route.fulfill({ status: 204, body: '' });
  });

  await page.goto('/videos/e2e-video');
  await expect(page.locator('video')).toBeVisible();
  const deleteButton = page.getByTestId('delete-video-media-button');
  await expect(deleteButton).toBeVisible();

  let dismissedMessage = '';
  page.once('dialog', async dialog => {
    dismissedMessage = dialog.message();
    await dialog.dismiss();
  });
  await deleteButton.click();
  expect(dismissedMessage).toContain('Delete the local video file');
  expect(deleteRequests).toBe(0);
  await expect(page.locator('video')).toBeVisible();

  let acceptedMessage = '';
  const deleteResponse = page.waitForResponse(response => response.url().endsWith('/api/videos/e2e-video/media') && response.request().method() === 'DELETE');
  page.once('dialog', async dialog => {
    acceptedMessage = dialog.message();
    await dialog.accept();
  });
  await deleteButton.click();
  await deleteResponse;

  expect(acceptedMessage).toContain('Metadata, comments, thumbnails, and catalog information will stay.');
  expect(deleteRequests).toBe(1);
  await expect(page.locator('video')).toHaveCount(0);
  await expect(page.getByTestId('media-availability')).toContainText('Metadata only');
  await expect(page.getByText('E2E Lunar Archive Smoke')).toBeVisible();
});

test('watch page keeps played state after stale progress completes', async ({ page }) => {
  const progressWrites = [];
  let releaseStaleProgress;
  const staleProgressCanFinish = new Promise(resolve => {
    releaseStaleProgress = resolve;
  });
  await page.route('**/api/videos/e2e-video/progress', async route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON();
      progressWrites.push(body);
      if (!body.watched) {
        await staleProgressCanFinish;
      }
    }
    await route.continue();
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  const markPlayedButton = page.getByTestId('mark-played-button');
  await expect(markPlayedButton).toBeVisible();

  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 42 });
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await expect.poll(() => progressWrites.filter(write => !write.watched).length).toBe(1);

  await markPlayedButton.click();
  await expect.poll(() => progressWrites.some(write => write.watched)).toBe(true);
  await expect(markPlayedButton).toHaveCount(0);

  releaseStaleProgress();
  await expect.poll(async () => {
    const response = await page.request.get('/api/videos/e2e-video/progress');
    const body = await response.json();
    return body;
  }).toMatchObject({ position_seconds: 125, duration_seconds: 125, watched: true });
  await expect(page.getByTestId('mark-played-button')).toHaveCount(0);
});

test('watch page suppresses playback progress while marking played', async ({ page }) => {
  const progressWrites = [];
  let releaseMarkPlayed;
  const markPlayedCanFinish = new Promise(resolve => {
    releaseMarkPlayed = resolve;
  });
  await page.route('**/api/videos/e2e-video/progress', async route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON();
      progressWrites.push(body);
      if (body.watched) {
        await markPlayedCanFinish;
      }
    }
    await route.continue();
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  const markPlayedButton = page.getByTestId('mark-played-button');

  await markPlayedButton.click();
  await expect.poll(() => progressWrites.length).toBe(1);
  expect(progressWrites[0]).toMatchObject({ watched: true });

  await page.locator('video').evaluate(media => {
    Object.defineProperty(media, 'currentTime', { configurable: true, writable: true, value: 42 });
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await page.waitForTimeout(100);
  expect(progressWrites).toHaveLength(1);

  releaseMarkPlayed();
  await expect(markPlayedButton).toHaveCount(0);
});

test('watch page remembers playback speed and attempts autoplay', async ({ page }) => {
  await page.addInitScript(() => {
    window.__kapselPlayCalls = 0;
    HTMLMediaElement.prototype.play = function () {
      window.__kapselPlayCalls += 1;

      return Promise.resolve();
    };
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  await page.locator('video').evaluate(media => {
    media.playbackRate = 1.5;
    media.dispatchEvent(new Event('ratechange', { bubbles: true }));
  });

  await page.goto('/');
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
  });

  await expect.poll(() => page.locator('video').evaluate(media => media.playbackRate)).toBe(1.5);
  await expect.poll(() => page.evaluate(() => window.__kapselPlayCalls)).toBeGreaterThan(0);
});

test('watch page shows transient play and pause feedback', async ({ page }) => {
  await page.clock.install();
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('play', { bubbles: true }));
  });
  await expect(page.getByTestId('playback-feedback')).toHaveCount(0);

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('pause', { bubbles: true }));
  });
  await expect(page.getByTestId('playback-feedback')).toHaveAttribute('aria-label', 'Playback paused');
  await expect(page.getByTestId('playback-feedback')).toHaveClass(/show-pause/);

  await page.clock.fastForward(1100);
  await expect(page.getByTestId('playback-feedback')).toHaveCount(0);

  await page.locator('video').evaluate(media => {
    media.dispatchEvent(new Event('play', { bubbles: true }));
  });
  await expect(page.getByTestId('playback-feedback')).toHaveAttribute('aria-label', 'Playback resumed');
  await expect(page.getByTestId('playback-feedback')).toHaveClass(/show-play/);
  await expect.poll(() => page.getByTestId('playback-feedback').evaluate(node => {
    const icon = node.querySelector('svg');
    return icon.getBoundingClientRect().width / node.getBoundingClientRect().width;
  })).toBeGreaterThan(0.68);

  await page.clock.fastForward(1100);
  await expect(page.getByTestId('playback-feedback')).toHaveCount(0);
});

test('watch page ignores blocked autoplay and unavailable playback speed storage', async ({ page }) => {
  await page.addInitScript(() => {
    window.__kapselUnhandledRejections = 0;
    window.addEventListener('unhandledrejection', () => {
      window.__kapselUnhandledRejections += 1;
    });
    HTMLMediaElement.prototype.play = function () {
      return Promise.reject(new DOMException('Autoplay blocked', 'NotAllowedError'));
    };
    Storage.prototype.getItem = function () {
      throw new Error('storage unavailable');
    };
    Storage.prototype.setItem = function () {
      throw new Error('storage unavailable');
    };
  });

  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  await page.locator('video').evaluate(media => {
    media.playbackRate = 1.25;
    media.dispatchEvent(new Event('ratechange', { bubbles: true }));
    media.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
  });

  await expect(page.getByLabel('Video player')).toBeVisible();
  await page.waitForTimeout(100);
  expect(await page.evaluate(() => window.__kapselUnhandledRejections)).toBe(0);
});

test('cinema mode expands the player and moves up next below it', async ({ page }) => {
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  const cinemaButton = page.locator('video-skin button[aria-label="Enter cinema mode"]');
  await expect(cinemaButton).not.toHaveAttribute('title');
  await expect(page.locator('video-skin #cinema-mode-tooltip')).toHaveText('Enter cinema mode');
  await expect(cinemaButton).not.toHaveText('Cinema');
  await expect(cinemaButton.locator('svg')).toHaveCount(1);
  await expect(cinemaButton).toHaveAttribute('aria-pressed', 'false');
  await cinemaButton.evaluate(button => button.click());
  await expect(page.getByTestId('video-detail-route')).toHaveClass(/cinema-mode/);

  const routeBox = await page.getByTestId('video-detail-route').boundingBox();
  const playerBox = await page.locator('.watch-player').boundingBox();
  const recommendationsBox = await page.locator('.recommendations').boundingBox();
  expect(playerBox.width).toBeGreaterThan(routeBox.width * 0.92);
  expect(recommendationsBox.y).toBeGreaterThanOrEqual(playerBox.y + playerBox.height - 1);

  await page.goto('/');
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toHaveClass(/cinema-mode/);

  const exitCinemaButton = page.locator('video-skin button[aria-label="Exit cinema mode"]');
  await expect(exitCinemaButton).not.toHaveAttribute('title');
  await expect(page.locator('video-skin #cinema-mode-tooltip')).toHaveText('Exit cinema mode');
  await expect(exitCinemaButton).toHaveAttribute('aria-pressed', 'true');
  await exitCinemaButton.evaluate(button => button.click());
  await expect(page.getByTestId('video-detail-route')).not.toHaveClass(/cinema-mode/);
  if (routeBox.width > 900) {
    const restoredPlayerBox = await page.locator('.watch-player').boundingBox();
    expect(restoredPlayerBox.width).toBeLessThan(routeBox.width * 0.9);
  }
});

test('watch player exposes persisted volume normalization control', async ({ page }) => {
  await page.addInitScript(() => {
    const events = [];
    const activeConnections = [];
    let nextNodeID = 0;
    let nextContextID = 0;

    class KapselFakeAudioNode {
      constructor(type) {
        this.type = type;
        this.id = `${type}-${++nextNodeID}`;
      }

      connect(target) {
        const connection = { fromID: this.id, from: this.type, toID: target?.id || 'destination', to: target?.type || 'destination' };
        activeConnections.push(connection);
        events.push({ name: 'connect', ...connection });
        return target;
      }

      disconnect() {
        for (let index = activeConnections.length - 1; index >= 0; index -= 1) {
          if (activeConnections[index].fromID === this.id) activeConnections.splice(index, 1);
        }
        events.push({ name: 'disconnect', nodeType: this.type });
      }
    }

    class KapselFakeCompressorNode extends KapselFakeAudioNode {
      constructor() {
        super('compressor');
        this.threshold = { value: 0 };
        this.knee = { value: 0 };
        this.ratio = { value: 0 };
        this.attack = { value: 0 };
        this.release = { value: 0 };
      }
    }

    class KapselFakeGainNode extends KapselFakeAudioNode {
      constructor() {
        super('gain');
        this.gain = { value: 1 };
      }
    }

    class KapselFakeAudioContext {
      constructor() {
        this.id = ++nextContextID;
        this.state = 'suspended';
        this.destination = new KapselFakeAudioNode('destination');
        events.push({ name: 'context-created', contextID: this.id });
      }

      createMediaElementSource(media) {
        if (media.__kapselAudioSourceCreated) throw new Error('duplicate media element source');
        media.__kapselAudioSourceCreated = true;
        events.push({ name: 'source-created', contextID: this.id });
        return new KapselFakeAudioNode('source');
      }

      createDynamicsCompressor() {
        events.push({ name: 'compressor-created', contextID: this.id });
        return new KapselFakeCompressorNode();
      }

      createGain() {
        events.push({ name: 'gain-created', contextID: this.id });
        return new KapselFakeGainNode();
      }

      resume() {
        this.state = 'running';
        events.push({ name: 'resume', contextID: this.id });
        return Promise.resolve();
      }

      close() {
        events.push({ name: 'close', contextID: this.id });
        return Promise.resolve();
      }
    }

    window.__kapselAudioEvents = events;
    window.__kapselAudioConnections = activeConnections;
    Object.defineProperty(window, 'AudioContext', { configurable: true, value: KapselFakeAudioContext });
    Object.defineProperty(window, 'webkitAudioContext', { configurable: true, value: KapselFakeAudioContext });
  });
  await page.goto('/videos/e2e-video');
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video')).toBeAttached();
  const normalizeButton = page.locator('video-skin button[aria-label="Enable volume normalization"]');
  await expect(normalizeButton).not.toHaveAttribute('title');
  await expect(page.locator('video-skin #volume-normalization-tooltip')).toHaveText('Enable volume normalization');
  await expect(normalizeButton.locator('svg')).toHaveCount(1);
  await expect(normalizeButton).toHaveAttribute('aria-pressed', 'false');

  await normalizeButton.evaluate(button => button.click());
  await expect(page.locator('video-skin button[aria-label="Disable volume normalization"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('video-skin #volume-normalization-tooltip')).toHaveText('Disable volume normalization');
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem('kapsel.volumeNormalization'))).toBe('1');
  await expect.poll(() => audioEventCount(page, 'source-created')).toBe(1);
  await expect.poll(() => hasActiveAudioConnection(page, 'source', 'compressor')).toBe(true);
  await expect.poll(() => hasActiveAudioConnection(page, 'compressor', 'gain')).toBe(true);
  await expect.poll(() => hasActiveAudioConnection(page, 'gain', 'destination')).toBe(true);

  await page.locator('a.brand').click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByTestId('library-route')).toBeVisible();
  await expect.poll(() => audioEventCount(page, 'close')).toBe(1);
  await expect.poll(() => activeAudioConnectionCount(page)).toBe(0);
  await page.goBack();
  await expect(page).toHaveURL(/\/videos\/e2e-video$/);
  await expect(page.getByTestId('video-detail-route')).toBeVisible();
  await expect(page.locator('video-skin button[aria-label="Disable volume normalization"]')).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(() => audioEventCount(page, 'source-created')).toBe(2);

  const sourceCreationsBeforeDisable = await audioEventCount(page, 'source-created');
  await page.locator('video-skin button[aria-label="Disable volume normalization"]').evaluate(button => button.click());
  await expect(page.locator('video-skin button[aria-label="Enable volume normalization"]')).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem('kapsel.volumeNormalization'))).toBe('0');
  await expect.poll(() => audioEventCount(page, 'source-created')).toBe(sourceCreationsBeforeDisable);
  await expect.poll(() => hasActiveAudioConnection(page, 'source', 'destination')).toBe(true);
  await expect.poll(() => hasActiveAudioConnection(page, 'source', 'compressor')).toBe(false);
});

function isLocalRequest(requestURL) {
  const url = new URL(requestURL);
  if (['about:', 'blob:', 'data:'].includes(url.protocol)) return true;
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false;

  return ['127.0.0.1', 'localhost', '::1', '[::1]'].includes(url.hostname);
}

async function isClipped(locator) {
  return locator.evaluate(element => {
    const container = element.closest('.rich-text');
    if (!container) return false;

    return element.getBoundingClientRect().bottom > container.getBoundingClientRect().bottom + 1;
  });
}

async function captionTrackModes(page) {
  return page.locator('video').evaluate(media => {
    return Array.from(media.textTracks)
      .filter(track => track.kind === 'captions' || track.kind === 'subtitles')
      .map(track => track.mode);
  });
}

async function showingCaptionTrackCount(page) {
  const modes = await captionTrackModes(page);
  return modes.filter(mode => mode === 'showing').length;
}

async function firstCaptionCueBox(page) {
  return page.locator('video').evaluate(media => {
    const track = Array.from(media.textTracks).find(item => item.kind === 'captions' || item.kind === 'subtitles');
    const cue = track?.cues?.[0];
    if (!cue) return null;
    return { align: cue.align, position: cue.position, size: cue.size };
  });
}

async function audioEventCount(page, name) {
  return page.evaluate(eventName => (window.__kapselAudioEvents || []).filter(event => event.name === eventName).length, name);
}

async function activeAudioConnectionCount(page) {
  return page.evaluate(() => (window.__kapselAudioConnections || []).length);
}

async function hasActiveAudioConnection(page, from, to) {
  return page.evaluate(([source, destination]) => (window.__kapselAudioConnections || []).some(connection => {
    return connection.from === source && connection.to === destination;
  }), [from, to]);
}

function downloadJob(id, status, progress) {
  return {
    id,
    type: 'download',
    status,
    progress,
    attempts: status === 'queued' ? 0 : 1,
    max_attempts: 3,
    error: '',
    created_at: '2026-05-04T12:00:00Z',
    updated_at: '2026-05-04T12:00:01Z',
  };
}

async function emitLiveJobs(page, jobs, pagination = { page: 1, page_size: 50, total: jobs.length }) {
  await expect.poll(() => page.evaluate(() => (window.__kapselLiveSockets || []).some(socket => socket.readyState === WebSocket.OPEN))).toBe(true);
  await page.evaluate(({ items, pagination: livePagination }) => {
    window.__kapselLiveEmit?.({
      type: 'jobs_snapshot',
      data: items,
      pagination: livePagination,
    });
  }, { items: jobs, pagination });
}
