<script>
  import { onDestroy } from 'svelte';
  import PaginationControls from './components/PaginationControls.svelte';
  import RichText from './components/RichText.svelte';
  import VideoCard from './components/VideoCard.svelte';
  import VideoSortToolbar from './components/VideoSortToolbar.svelte';
  import SettingsDiagnosticsPanel from './routes/SettingsDiagnosticsPanel.svelte';
  import WatchRoute from './routes/WatchRoute.svelte';
  import { channelHref, channelInitial, isCatalogOnly, metadataLine, thumbnailFallback, thumbnailStyle, videoHref } from './display.js';
  import '@videojs/html/video/player';
  import '@videojs/html/video/skin';

  const sidebarPrimary = [
    { label: 'Home', href: '/' },
    { label: 'Channels', href: '/channels' },
    { label: 'Playlists', href: '/playlists' },
    { label: 'Downloads', href: '/downloads' },
    { label: 'Settings', href: '/settings' },
  ];
  const sidebarExplore = ['Music', 'Gaming', 'Podcasts', 'Education'];
  const playbackProgressSyncInterval = 10000;
  const libraryPageSize = 50;
  const upNextPageSize = 12;
  const mediaURLPlaybackRefreshLeadMS = 30000;
  const playbackRateStorageKey = 'kapsel.playbackRate';
  const cinemaModeStorageKey = 'kapsel.cinemaMode';
  const volumeNormalizationStorageKey = 'kapsel.volumeNormalization';
  const captionModeStorageKey = 'kapsel.captions';
  const liveJobsPageRefreshDelay = 250;
  const liveJobsPageRetryDelay = 3000;
  const jobStatusFilters = [
    { label: 'All', value: 'all' },
    { label: 'Queued', value: 'queued' },
    { label: 'Running', value: 'running' },
    { label: 'Succeeded', value: 'succeeded' },
    { label: 'Failed', value: 'failed' },
    { label: 'Cancelled', value: 'cancelled' },
  ];
  const videoSortOptions = [
    { label: 'For You', value: 'watching' },
    { label: 'Newest', value: 'newest' },
    { label: 'Recently Downloaded', value: 'downloaded' },
    { label: 'Oldest', value: 'oldest' },
    { label: 'Length', value: 'length' },
    { label: 'Popularity', value: 'popularity' },
  ];
  const utilityRoutes = {
    '/downloads': {
      title: 'Downloads',
      eyebrow: 'Durable jobs',
      body: 'Queue single videos or channel-first downloads and keep an eye on background archive work.',
    },
    '/settings': {
      title: 'Settings',
      eyebrow: 'Single node archive',
      body: 'Readiness checks and local archive controls for this Kapsel node.',
    },
    '/subscriptions': {
      title: 'Subscriptions',
      eyebrow: 'Channels',
      body: 'Subscribed-channel management will build on the local channel archive.',
    },
    '/history': {
      title: 'History',
      eyebrow: 'Playback',
      body: 'Watched videos and progress history will appear here once progress sync is expanded.',
    },
  };

  let path = window.location.pathname;
  let locationSearch = window.location.search;
  let activeRouteKey = '';
  let libraryRequestToken = 0;
  let homeSetupRequestToken = 0;
  let videoRequestToken = 0;
  let mediaURLRefreshRequestToken = 0;
  let sponsorSegmentsRequestToken = 0;
  let upNextRequestToken = 0;
  let channelRequestToken = 0;
  let channelListRequestToken = 0;
  let channelSubscriptionRequestToken = 0;
  let channelDeleteRequestToken = 0;
  let keepForeverRequestToken = 0;
  let markPlayedRequestToken = 0;
  let deleteVideoMediaRequestToken = 0;
  let playlistListRequestToken = 0;
  let playlistRequestToken = 0;
  let searchRequestToken = 0;
  let commentsRequestToken = 0;
  let diagnosticsRequestToken = 0;
  let sessionRequestToken = 0;
  let jobsRequestToken = 0;
  let jobsBackgroundRequestToken = 0;
  let jobsForegroundRequestToken = 0;
  let channelJobTimer;
  let videoJobTimer;
  let previewJobTimer;
  let scanJobTimer;
  let mediaURLRefreshTimer;
  let jobsPollTimer;
  let jobErrorCopyTimer;
  let liveSocket;
  let liveReconnectTimer;
  let liveJobsRefreshTimer;
  let liveJobsRefreshInFlight = false;
  let liveJobsRefreshQueued = false;
  let liveJobsRefreshQueuedDelay = liveJobsPageRefreshDelay;
  let liveConnected = false;
  let pendingLiveJobsSnapshot = null;
  const catalogVideoJobTimers = new Map();
  const liveMissingJobRequests = new Map();
  let activeChannelJobID = '';
  let activeVideoJobID = '';
  let activePreviewJobID = '';
  let activeScanJobID = '';
  let activeScanChannelID = '';
  let homeArchiveState = 'unknown';
  let searchInput = new URLSearchParams(locationSearch).get('q') ?? '';
  let librarySort = videoSortFromSearch(locationSearch);
  let channelURL = '';
  let videoURL = '';
  let watchMediaElement;
  let playbackProgressMutationToken = 0;
  let playbackProgressInvalidation = { videoID: '', version: 0 };
  let userTimestampSeeked = false;
  let loginUsername = '';
  let loginPassword = '';
  let channelJob = { status: 'idle', job: null, error: '' };
  let videoJob = { status: 'idle', job: null, error: '' };
  let previewJob = { status: 'idle', job: null, error: '' };
  let catalogVideoJobs = {};
  let scanJob = { status: 'idle', job: null, error: '' };
  let channelSubscriptionAction = { status: 'idle', error: '' };
  let channelDeleteAction = { status: 'idle', error: '' };
  let channelDeleteResetTimer = null;
  let keepForeverAction = { status: 'idle', error: '' };
  let markPlayedAction = { status: 'idle', error: '' };
  let deleteVideoMediaAction = { status: 'idle', error: '' };
  let channelSubscriptionOverride = { id: '', subscribed: null };
  let library = { status: 'idle', videos: [], pagination: { page: 1, page_size: libraryPageSize, total: 0 }, refreshing: false, loadingMore: false, loadMoreError: '', error: '' };
  let upNextCandidates = { videoID: '', videos: [] };
  let homeSetup = { status: 'idle', hasKnownContent: true, error: '' };
  let video = { status: 'idle', item: null, error: '' };
  let playbackSync = initialPlaybackSync();
  let cinemaMode = savedCinemaMode();
  let volumeNormalization = savedVolumeNormalization();
  let audioNormalizationGraph = null;
  let upNextCountdown = 0;
  let upNextTimer = null;
  let upNextTarget = null;
  let playbackFeedback = { id: 0, type: '' };
  let playbackFeedbackTimer;
  let playbackFeedbackCanShowPlay = false;
  let mediaURLRefreshRetry = { videoID: '', mediaURL: '' };
  let mediaURLRefreshSuppressedPlay = { videoID: '', mediaURL: '' };
  let pendingPlaybackRateRestore = 0;
  let suppressPlaybackRateSave = false;
  let suppressNextPlaybackPause = false;
  let suppressNextLoadedMetadataAutoplay = false;
  let autoplayOnLoad = false;
  let channelPage = { status: 'idle', item: null, videos: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
  let channelListPage = { status: 'idle', channels: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
  let playlistListPage = { status: 'idle', playlists: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
  let playlistPage = { status: 'idle', item: null, videos: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
  let searchPage = { status: 'idle', query: '', results: [], error: '' };
  let commentsPage = { status: 'idle', videoID: '', comments: [], pagination: { page: 1, page_size: 20, total: 0 }, error: '' };
  let diagnostics = { status: 'idle', readiness: null, error: '' };
  let jobsPage = { status: 'idle', filter: 'all', jobs: [], pagination: { page: 1, page_size: 20, total: 0 }, error: '' };
  let jobAction = { id: '', action: '', error: '' };
  let jobErrorCopy = { id: '', status: '' };
  let session = { status: 'idle', auth_enabled: false, configured: true, authenticated: false, username: '', login_required: false, error: '' };
  let loginState = { status: 'idle', error: '' };

  $: videoID = videoIDFromPath(path);
  $: channelID = channelIDFromPath(path);
  $: playlistID = playlistIDFromPath(path);
  $: searchQuery = path === '/search' ? new URLSearchParams(locationSearch).get('q')?.trim() ?? '' : '';
  $: librarySort = videoSortFromSearch(locationSearch, defaultVideoSortForPath(path));
  $: routeKey = `${path}${locationSearch}`;
  $: pageTitle = pageTitleForRoute();
  $: libraryFeedSummary = libraryFeedCountLabel(library.videos.length, library.pagination?.total);
  $: channelSubmitDisabled = isChannelJobActive(channelJob.status) || channelURL.trim() === '';
  $: videoSubmitDisabled = videoJob.status === 'loading' || videoURL.trim() === '';
  $: scanSubmitDisabled = !channelID || isChannelJobActive(scanJob.status);
  $: orderedUpNextVideos = upNextVideosFromEndpoint(upNextCandidates.videos, videoID);
  $: upNextVideo = orderedUpNextVideos[0] ?? null;
  $: recommendations = orderedUpNextVideos.slice(0, 12);
  $: libraryHasMore = library.videos.length > 0 && (Number(library.pagination?.page) || 1) < lastPage(library.pagination, libraryPageSize);
  $: libraryEmptyState = homeLibraryEmptyState(librarySort, showHomeAddChannel, homeArchiveState);
  $: showHomeAddChannel = path === '/' && library.status === 'loaded' && homeSetup.status === 'loaded' && !homeSetup.hasKnownContent;
  $: activeCinemaMode = cinemaMode && !!video.item?.media_url;
  $: syncAudioNormalization(watchMediaElement, volumeNormalization);
  $: scheduleWatchMediaURLRefresh(videoID, video.item);
  $: if (routeKey !== activeRouteKey) {
    activeRouteKey = routeKey;
    cancelUpNext();
    runRouteLoad();
  }

  function runRouteLoad() {
    if (path !== '/downloads') stopJobsPolling();
    if (session.status !== 'loaded') return;
    if (session.auth_enabled && !session.authenticated) return;
    if (path === '/') {
      loadLibrary({ showLoading: library.videos.length === 0 });
      return;
    }
    if (videoID) {
      loadVideo(videoID);
      loadComments(videoID);
      return;
    }
    if (path === '/channels') {
      loadChannels({ showLoading: channelListPage.channels.length === 0 });
      return;
    }
    if (channelID) {
      loadChannel(channelID);
      return;
    }
    if (path === '/playlists') {
      loadPlaylists({ showLoading: playlistListPage.playlists.length === 0 });
      return;
    }
    if (playlistID) {
      loadPlaylist(playlistID, { showLoading: playlistPage.videos.length === 0 });
      return;
    }
    if (path === '/search') {
      loadSearch(searchQuery);
      return;
    }
    if (path === '/settings') {
      loadDiagnostics();
      return;
    }
    if (path === '/downloads') {
      loadJobs({ showLoading: jobsPage.jobs.length === 0 });
    }
  }

  function handlePopState() {
    path = window.location.pathname;
    locationSearch = window.location.search;
    searchInput = new URLSearchParams(locationSearch).get('q') ?? searchInput;
  }

  window.addEventListener('popstate', handlePopState);
  window.addEventListener('focus', handleWatchMediaURLWake);
  document.addEventListener('visibilitychange', handleWatchMediaURLWake);
  loadSession();

  onDestroy(() => {
    window.removeEventListener('popstate', handlePopState);
    window.removeEventListener('focus', handleWatchMediaURLWake);
    document.removeEventListener('visibilitychange', handleWatchMediaURLWake);
    if (channelJobTimer) clearTimeout(channelJobTimer);
    if (videoJobTimer) clearTimeout(videoJobTimer);
    if (previewJobTimer) clearTimeout(previewJobTimer);
    if (scanJobTimer) clearTimeout(scanJobTimer);
    clearMediaURLRefreshTimer();
    if (jobErrorCopyTimer) clearTimeout(jobErrorCopyTimer);
    clearCatalogVideoJobTimers();
    stopJobsPolling();
    stopLiveUpdates();
    cancelUpNext();
    clearPlaybackFeedback();
    destroyAudioNormalizationGraph();
  });

  function navigate(event, nextHref) {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }

    event.preventDefault();
    autoplayOnLoad = false;
    setRoute(nextHref);
  }

  function setRoute(nextHref) {
    const next = new URL(nextHref, window.location.origin);
    if (path === next.pathname && locationSearch === next.search) {
      window.scrollTo({ top: 0, left: 0, behavior: 'auto' });
      return;
    }
    path = next.pathname;
    locationSearch = next.search;
    if (path === '/search') searchInput = new URLSearchParams(locationSearch).get('q') ?? '';
    window.history.pushState({}, '', `${path}${locationSearch}`);
    window.scrollTo({ top: 0, left: 0, behavior: 'auto' });
  }

  function defaultVideoSortForPath(routePath) {
    return routePath === '/' ? 'watching' : 'newest';
  }

  function videoSortFromSearch(search, fallback = 'newest') {
    const value = new URLSearchParams(search).get('sort') || fallback;
    if (value === 'date' || value === 'published') return 'newest';
    if (value === 'recently-downloaded' || value === 'recently_downloaded') return 'downloaded';
    return videoSortOptions.some(option => option.value === value) ? value : fallback;
  }

  function setVideoSort(value) {
    const params = new URLSearchParams(locationSearch);
    if (value && value !== defaultVideoSortForPath(path)) {
      params.set('sort', value);
    } else {
      params.delete('sort');
    }
    const query = params.toString();
    setRoute(`${path}${query ? `?${query}` : ''}`);
  }

  async function loadLibrary(options = {}) {
    const requestToken = ++libraryRequestToken;
    const progressInvalidationVersion = playbackProgressInvalidation.version;
    const page = options.page ?? 1;
    const append = options.append === true && page > 1;
    const sort = options.sort ?? videoSortFromSearch(locationSearch, defaultVideoSortForPath(path));
    if (append) {
      library = { ...library, refreshing: false, loadingMore: true, loadMoreError: '', error: '' };
    } else if (options.showLoading !== false) {
      library = { ...library, status: 'loading', refreshing: true, loadingMore: false, loadMoreError: '', error: '' };
    } else {
      library = { ...library, refreshing: true, loadingMore: false, loadMoreError: '', error: '' };
    }
    try {
      const response = await fetchJSON(`/api/home/videos?page=${page}&page_size=${libraryPageSize}&sort=${encodeURIComponent(sort)}`);
      if (requestToken !== libraryRequestToken) return;
      const responseVideos = response.data ?? [];
      const existingVideoIDs = new Set(library.videos.map(item => item.id));
      const videos = append ? [
        ...library.videos,
        ...responseVideos.filter(item => {
          if (!item?.id || existingVideoIDs.has(item.id)) return false;
          existingVideoIDs.add(item.id);
          return true;
        }),
      ] : responseVideos;
      library = {
        status: 'loaded',
        videos,
        pagination: response.pagination ?? { page, page_size: libraryPageSize, total: responseVideos.length },
        refreshing: false,
        loadingMore: false,
        loadMoreError: '',
        error: '',
      };
      if (!append && path === '/') refreshHomeSetupState(library.pagination.total);
      if (!append && path === '/' && playbackProgressInvalidation.version !== progressInvalidationVersion) {
        loadLibrary({ showLoading: false });
      }
    } catch (error) {
      if (requestToken !== libraryRequestToken) return;
      if (append) {
        library = { ...library, status: 'loaded', refreshing: false, loadingMore: false, loadMoreError: error.message };
        return;
      }
      library = { status: 'error', videos: [], pagination: { page: 1, page_size: libraryPageSize, total: 0 }, refreshing: false, loadingMore: false, loadMoreError: '', error: error.message };
    }
  }

  async function refreshHomeSetupState(videoTotal) {
    const requestToken = ++homeSetupRequestToken;
    const knownVideoTotal = Number(videoTotal) || 0;
    if (knownVideoTotal > 0) {
      homeArchiveState = 'present';
      homeSetup = { status: 'loaded', hasKnownContent: true, error: '' };
      return;
    }
    homeArchiveState = 'checking';
    homeSetup = { status: 'loading', hasKnownContent: false, error: '' };
    try {
      const videos = await fetchJSON('/api/home/videos?page=1&page_size=1&sort=newest');
      if (requestToken !== homeSetupRequestToken) return;
      const newestVideoTotal = Number(videos.pagination?.total ?? videos.data?.length ?? 0) || 0;
      if (newestVideoTotal > 0) {
        homeArchiveState = 'present';
        homeSetup = { status: 'loaded', hasKnownContent: true, error: '' };
        return;
      }
      homeArchiveState = 'empty';
      const response = await fetchJSON('/api/channels?page=1&page_size=1');
      if (requestToken !== homeSetupRequestToken) return;
      const channelTotal = Number(response.pagination?.total ?? response.data?.length ?? 0) || 0;
      homeSetup = { status: 'loaded', hasKnownContent: channelTotal > 0, error: '' };
    } catch (error) {
      if (requestToken !== homeSetupRequestToken) return;
      homeArchiveState = 'error';
      homeSetup = { status: 'error', hasKnownContent: true, error: error.message };
    }
  }

  function homeLibraryEmptyState(sort, canShowSetup, archiveState) {
    if (sort === 'watching') {
      if (archiveState === 'present') {
        return { title: 'No For You videos right now.', body: 'Choose Newest or another sort to browse watched videos.' };
      }
      if (archiveState === 'checking' || archiveState === 'unknown') {
        return { title: 'Checking your archive...', body: 'Looking for watched videos and other library entries.' };
      }
      if (archiveState === 'error') {
        return { title: 'Could not check your archive.', body: 'Choose Newest or another sort to browse the full library.' };
      }
    }
    return {
      title: 'No archived videos yet.',
      body: canShowSetup ? 'Add a channel above or import TubeArchivist data to fill the feed.' : 'Use Downloads, channel scans, or TubeArchivist import to fill the feed.',
    };
  }

  function loadNextLibraryPage() {
    if (path !== '/' || library.status !== 'loaded' || library.refreshing || library.loadingMore || !libraryHasMore) return;
    loadLibrary({ page: (Number(library.pagination?.page) || 1) + 1, append: true, showLoading: false });
  }

  async function loadVideo(id) {
    const requestToken = ++videoRequestToken;
    sponsorSegmentsRequestToken++;
    upNextRequestToken++;
    upNextCandidates = { videoID: id, videos: [] };
    keepForeverRequestToken++;
    keepForeverAction = { status: 'idle', error: '' };
    markPlayedRequestToken++;
    markPlayedAction = { status: 'idle', error: '' };
    deleteVideoMediaRequestToken++;
    deleteVideoMediaAction = { status: 'idle', error: '' };
    mediaURLRefreshRequestToken++;
    mediaURLRefreshRetry = { videoID: '', mediaURL: '' };
    mediaURLRefreshSuppressedPlay = { videoID: '', mediaURL: '' };
    pendingPlaybackRateRestore = 0;
    suppressPlaybackRateSave = false;
    suppressNextPlaybackPause = false;
    suppressNextLoadedMetadataAutoplay = false;
    cinemaMode = savedCinemaMode();
    playbackSync = initialPlaybackSync(id);
    playbackFeedbackCanShowPlay = false;
    clearPlaybackFeedback();
    userTimestampSeeked = false;
    video = { status: 'loading', item: video.item, error: '' };
    try {
      const item = await fetchJSON(`/api/videos/${encodeURIComponent(id)}`);
      if (requestToken !== videoRequestToken) return;
      playbackSync = initialPlaybackSync(id, item);
      video = { status: 'loaded', item, error: '' };
      loadSponsorSegments(item?.id);
      loadUpNextVideos(item?.id);
      if (previewJobTimer) clearTimeout(previewJobTimer);
      previewJobTimer = undefined;
      activePreviewJobID = '';
      previewJob = { status: 'idle', job: null, error: '' };
      if (item?.active_preview_job?.id) {
        activePreviewJobID = item.active_preview_job.id;
        previewJob = { status: item.active_preview_job.status, job: item.active_preview_job, error: item.active_preview_job.error || '' };
        watchPreviewJob(item.active_preview_job.id);
        void fetchMissingLiveJob(item.active_preview_job.id);
      }
      if (item?.active_download_job?.id) {
        setCatalogVideoJob(item.id, catalogVideoJobState(item.active_download_job, item.active_download_job.status));
        watchCatalogVideoJob(item.id, item.active_download_job.id);
      }
    } catch (error) {
      if (requestToken !== videoRequestToken) return;
      video = { status: 'error', item: null, error: error.message };
    }
  }

  async function refreshActiveVideoDetail(id, options = {}) {
    const requestToken = ++videoRequestToken;
    try {
      const item = await fetchJSON(`/api/videos/${encodeURIComponent(id)}`, options.fetchOptions ?? {});
      if (requestToken !== videoRequestToken || videoIDFromPath(path) !== id || video.item?.id !== id) return;
      const nextItem = mergeVideoDetailRefresh(video.item, item, options);
      if (video.item?.media_url && nextItem?.media_url && video.item.media_url !== nextItem.media_url) stagePlaybackRateRestore(watchMediaElement);
      video = { ...video, status: 'loaded', item: nextItem, error: '' };
    } catch {
      // Background metadata refresh should not interrupt playback.
    }
  }

  function mergeVideoDetailRefresh(current, next, options = {}) {
    const item = { ...current, ...next };
    if (options.preserveMediaURL && current?.media_url && next?.media_url && sameMediaURLPath(current.media_url, next.media_url) && !signedMediaURLNeedsRefresh(current.media_url, mediaURLPlaybackRefreshLeadMS)) {
      item.media_url = current.media_url;
    }

    return item;
  }

  function sameMediaURLPath(left, right) {
    return mediaURLPathname(left) !== '' && mediaURLPathname(left) === mediaURLPathname(right);
  }

  function mediaURLPathname(rawURL) {
    try {
      return new URL(rawURL, window.location.origin).pathname;
    } catch {
      return '';
    }
  }

  async function loadSponsorSegments(videoID) {
    const id = videoID || '';
    const requestToken = ++sponsorSegmentsRequestToken;
    if (!id) return;
    try {
      const response = await fetchJSON(`/api/videos/${encodeURIComponent(id)}/sponsor-segments`);
      if (requestToken !== sponsorSegmentsRequestToken || videoIDFromPath(path) !== id || video.item?.id !== id) return;
      const segments = Array.isArray(response.data) ? response.data : [];
      if (segments.length === 0 && Array.isArray(video.item?.sponsor_segments) && video.item.sponsor_segments.length > 0) return;
      video = { ...video, item: { ...video.item, sponsor_segments: segments } };
    } catch {
      // SponsorBlock is optional; playback should not depend on it.
    }
  }

  async function loadUpNextVideos(videoID) {
    const id = videoID || '';
    const requestToken = ++upNextRequestToken;
    if (!id) {
      upNextCandidates = { videoID: '', videos: [] };
      return;
    }
    upNextCandidates = { videoID: id, videos: [] };
    try {
      const response = await fetchJSON(`/api/videos/${encodeURIComponent(id)}/up-next?page_size=${upNextPageSize}`);
      if (requestToken !== upNextRequestToken) return;
      upNextCandidates = { videoID: id, videos: response.data ?? [] };
    } catch {
      if (requestToken !== upNextRequestToken) return;
      upNextCandidates = { videoID: id, videos: [] };
    }
  }

  async function loadChannel(id, options = {}) {
    const requestToken = ++channelRequestToken;
    const progressInvalidationVersion = playbackProgressInvalidation.version;
    const isSameChannel = channelPage.item?.id === id;
    const page = options.page ?? (isSameChannel ? channelPage.pagination.page : 1) ?? 1;
    const pageSize = channelPage.pagination.page_size || 50;
    if (!isSameChannel) {
      channelSubscriptionRequestToken++;
      channelSubscriptionAction = { status: 'idle', error: '' };
      channelSubscriptionOverride = { id: '', subscribed: null };
      channelDeleteAction = { status: 'idle', error: '' };
    }
    channelPage = { ...channelPage, status: 'loading', error: '' };
    try {
      const [item, videos] = await Promise.all([
        fetchJSON(`/api/channels/${encodeURIComponent(id)}`),
        fetchJSON(`/api/channels/${encodeURIComponent(id)}/videos?page=${page}&page_size=${pageSize}&sort=${encodeURIComponent(videoSortFromSearch(locationSearch))}`),
      ]);
      if (requestToken !== channelRequestToken) return;
      if (channelSubscriptionOverride.id === id && channelSubscriptionOverride.subscribed !== null) {
        item.subscribed = channelSubscriptionOverride.subscribed;
      }
      channelPage = { status: 'loaded', item, videos: videos.data ?? [], pagination: videos.pagination ?? { page, page_size: pageSize, total: 0 }, error: '' };
      if (channelSubscriptionAction.status !== 'loading' && channelSubscriptionAction.status !== 'succeeded') {
        channelSubscriptionAction = { status: 'idle', error: '' };
      }
      if (channelID === id && playbackProgressInvalidation.version !== progressInvalidationVersion) {
        loadChannel(id, { showLoading: false });
      }
    } catch (error) {
      if (requestToken !== channelRequestToken) return;
      channelPage = { ...channelPage, status: 'error', item: null, videos: [], error: error.message };
    }
  }

  async function loadChannels(options = {}) {
    const requestToken = ++channelListRequestToken;
    const page = options.page ?? channelListPage.pagination.page ?? 1;
    const pageSize = channelListPage.pagination.page_size || 50;
    if (options.showLoading !== false) {
      channelListPage = { ...channelListPage, status: 'loading', error: '' };
    }
    try {
      const response = await fetchJSON(`/api/channels?page=${page}&page_size=${pageSize}`);
      if (requestToken !== channelListRequestToken) return;
      channelListPage = { status: 'loaded', channels: response.data ?? [], pagination: response.pagination ?? { page, page_size: pageSize, total: 0 }, error: '' };
    } catch (error) {
      if (requestToken !== channelListRequestToken) return;
      channelListPage = { ...channelListPage, status: 'error', channels: [], error: error.message };
    }
  }

  async function loadPlaylists(options = {}) {
    const requestToken = ++playlistListRequestToken;
    const page = options.page ?? playlistListPage.pagination.page ?? 1;
    const pageSize = playlistListPage.pagination.page_size || 50;
    if (options.showLoading !== false) {
      playlistListPage = { ...playlistListPage, status: 'loading', error: '' };
    }
    try {
      const response = await fetchJSON(`/api/playlists?page=${page}&page_size=${pageSize}`);
      if (requestToken !== playlistListRequestToken) return;
      playlistListPage = { status: 'loaded', playlists: response.data ?? [], pagination: response.pagination ?? { page, page_size: pageSize, total: 0 }, error: '' };
    } catch (error) {
      if (requestToken !== playlistListRequestToken) return;
      playlistListPage = { ...playlistListPage, status: 'error', playlists: [], error: error.message };
    }
  }

  async function loadPlaylist(id, options = {}) {
    const requestToken = ++playlistRequestToken;
    const progressInvalidationVersion = playbackProgressInvalidation.version;
    const isSamePlaylist = playlistPage.item?.id === id;
    const page = options.page ?? (isSamePlaylist ? playlistPage.pagination.page : 1) ?? 1;
    const pageSize = playlistPage.pagination.page_size || 50;
    if (options.showLoading !== false) {
      playlistPage = { ...playlistPage, status: 'loading', error: '' };
    }
    try {
      const [item, videos] = await Promise.all([
        fetchJSON(`/api/playlists/${encodeURIComponent(id)}`),
        fetchJSON(`/api/playlists/${encodeURIComponent(id)}/videos?page=${page}&page_size=${pageSize}`),
      ]);
      if (requestToken !== playlistRequestToken) return;
      playlistPage = { status: 'loaded', item, videos: videos.data ?? [], pagination: videos.pagination ?? { page, page_size: pageSize, total: 0 }, error: '' };
      if (playlistID === id && playbackProgressInvalidation.version !== progressInvalidationVersion) {
        loadPlaylist(id, { showLoading: false });
      }
    } catch (error) {
      if (requestToken !== playlistRequestToken) return;
      playlistPage = { ...playlistPage, status: 'error', item: null, videos: [], error: error.message };
    }
  }

  async function loadComments(id, options = {}) {
    const requestToken = ++commentsRequestToken;
    const isSameVideo = commentsPage.videoID === id;
    const page = options.page ?? (isSameVideo ? commentsPage.pagination.page : 1) ?? 1;
    const pageSize = commentsPage.pagination.page_size || 20;
    commentsPage = { ...commentsPage, status: 'loading', videoID: id, error: '' };
    try {
      const response = await fetchJSON(`/api/videos/${encodeURIComponent(id)}/comments?page=${page}&page_size=${pageSize}`);
      if (requestToken !== commentsRequestToken) return;
      commentsPage = { status: 'loaded', videoID: id, comments: response.data ?? [], pagination: response.pagination ?? { page, page_size: pageSize, total: 0 }, error: '' };
    } catch (error) {
      if (requestToken !== commentsRequestToken) return;
      commentsPage = { ...commentsPage, status: 'error', videoID: id, comments: [], error: error.message };
    }
  }

  async function loadSearch(query) {
    const requestToken = ++searchRequestToken;
    if (query === '') {
      searchPage = { status: 'idle', query: '', results: [], error: '' };
      return;
    }
    searchPage = { status: 'loading', query, results: [], error: '' };
    try {
      const response = await fetchJSON(`/api/search?q=${encodeURIComponent(query)}&limit=50`);
      if (requestToken !== searchRequestToken) return;
      searchPage = { status: 'loaded', query, results: response.data ?? [], error: '' };
    } catch (error) {
      if (requestToken !== searchRequestToken) return;
      searchPage = { status: 'error', query, results: [], error: error.message };
    }
  }

  async function loadDiagnostics() {
    const requestToken = ++diagnosticsRequestToken;
    diagnostics = { status: 'loading', readiness: diagnostics.readiness, error: '' };
    try {
      const readiness = await fetchJSON('/api/settings');
      if (requestToken !== diagnosticsRequestToken) return;
      diagnostics = { status: 'loaded', readiness, error: '' };
    } catch (error) {
      if (requestToken !== diagnosticsRequestToken) return;
      diagnostics = { status: 'error', readiness: null, error: error.message };
    }
  }

  async function loadJobs(options = {}) {
    stopJobsPolling();
    const background = options.background === true;
    const requestToken = background ? ++jobsBackgroundRequestToken : ++jobsRequestToken;
    const foregroundRequestToken = jobsRequestToken;
    if (!background) jobsForegroundRequestToken = requestToken;
    const filter = options.filter ?? jobsPage.filter;
    const page = options.page ?? jobsPage.pagination.page ?? 1;
    const pageSize = jobsPage.pagination.page_size || 20;
    const requestedPagination = { ...jobsPage.pagination, page, page_size: pageSize };
    const pendingError = background && jobsPage.status === 'error' ? jobsPage.error : '';
    if (options.showLoading !== false) {
      jobsPage = { ...jobsPage, status: 'loading', filter, pagination: requestedPagination, error: pendingError };
    } else {
      jobsPage = { ...jobsPage, filter, pagination: requestedPagination, error: pendingError };
    }
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    if (filter !== 'all') params.set('status', filter);
    try {
      const response = await fetchJSON(`/api/jobs?${params.toString()}`);
      if (!jobsRequestIsCurrent(background, requestToken, foregroundRequestToken)) return;
      const pagination = response.pagination ?? { page, page_size: pageSize, total: 0 };
      const lastPage = jobsLastPage(pagination);
      if (pagination.page > lastPage) {
        jobsPage = { ...jobsPage, status: 'loaded', filter, pagination, error: '' };
        await loadJobs({ filter, page: lastPage, showLoading: false, background });
        return;
      }
      jobsPage = { status: 'loaded', filter, jobs: mergeRESTJobsWithVisibleUpdates(response.data ?? []), pagination, error: '' };
      applyPendingLiveJobsSnapshot();
      if (!liveConnected) scheduleJobsPolling();
    } catch (error) {
      if (!jobsRequestIsCurrent(background, requestToken, foregroundRequestToken)) return;
      jobsPage = { ...jobsPage, status: 'error', error: error.message };
      if (liveConnected) {
        scheduleLiveJobsPageRefresh(liveJobsPageRetryDelay);
      } else {
        scheduleJobsPolling();
      }
    } finally {
      finishJobsForegroundRequest(background, requestToken);
    }
  }

  function jobsRequestIsCurrent(background, requestToken, foregroundRequestToken) {
    if (background) {
      return requestToken === jobsBackgroundRequestToken && foregroundRequestToken === jobsRequestToken;
    }
    return requestToken === jobsRequestToken;
  }

  function finishJobsForegroundRequest(background, requestToken) {
    if (background || jobsForegroundRequestToken !== requestToken) return;
    jobsForegroundRequestToken = 0;
    if (!liveJobsRefreshQueued) return;
    const delay = liveJobsRefreshQueuedDelay;
    liveJobsRefreshQueued = false;
    liveJobsRefreshQueuedDelay = liveJobsPageRefreshDelay;
    scheduleLiveJobsPageRefresh(delay);
  }

  function scheduleJobsPolling() {
    if (jobsPollTimer) return;
    if (liveConnected) return;
    if (path !== '/downloads') return;
    if (session.auth_enabled && !session.authenticated) return;
    jobsPollTimer = setTimeout(() => {
      jobsPollTimer = undefined;
      loadJobs({ showLoading: false });
    }, 3000);
  }

  function stopJobsPolling() {
    if (jobsPollTimer) {
      clearTimeout(jobsPollTimer);
      jobsPollTimer = undefined;
    }
  }

  function ensureLiveUpdates() {
    if (!liveUpdatesAllowed()) {
      stopLiveUpdates();
      return;
    }
    if (liveSocket && (liveSocket.readyState === WebSocket.CONNECTING || liveSocket.readyState === WebSocket.OPEN)) return;
    if (!('WebSocket' in window)) {
      scheduleJobsPolling();
      resumeJobFallbackPolling();
      return;
    }

    const socket = new WebSocket(liveWebSocketURL());
    liveSocket = socket;
    socket.addEventListener('open', () => {
      if (liveSocket !== socket) return;
      liveConnected = true;
      stopJobsPolling();
      stopJobFallbackPolling();
    });
    socket.addEventListener('message', event => {
      if (liveSocket !== socket) return;
      handleLiveMessage(event.data);
    });
    socket.addEventListener('close', () => {
      if (liveSocket !== socket) return;
      liveSocket = undefined;
      liveConnected = false;
      scheduleLiveReconnect();
      scheduleJobsPolling();
      resumeJobFallbackPolling();
    });
    socket.addEventListener('error', () => {
      if (liveSocket === socket) socket.close();
    });
  }

  function stopLiveUpdates() {
    if (liveReconnectTimer) {
      clearTimeout(liveReconnectTimer);
      liveReconnectTimer = undefined;
    }
    stopLiveJobsPageRefresh({ resetInFlight: true });
    if (liveSocket) {
      const socket = liveSocket;
      liveSocket = undefined;
      socket.close();
    }
    liveConnected = false;
    pendingLiveJobsSnapshot = null;
    liveMissingJobRequests.clear();
  }

  function scheduleLiveReconnect() {
    if (!liveUpdatesAllowed() || liveReconnectTimer) return;
    liveReconnectTimer = setTimeout(() => {
      liveReconnectTimer = undefined;
      ensureLiveUpdates();
    }, 2000);
  }

  function liveUpdatesAllowed() {
    return session.status === 'loaded' && (!session.auth_enabled || session.authenticated);
  }

  function liveWebSocketURL() {
    const url = new URL('/api/live', window.location.href);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    return url.toString();
  }

  function handleLiveMessage(raw) {
    let message;
    try {
      message = JSON.parse(raw);
    } catch {
      return;
    }
    if (message?.type !== 'jobs_snapshot') return;
    const liveJobs = freshestLiveJobs(Array.isArray(message.data) ? message.data : []);
    setPendingLiveJobs(liveJobs, message.pagination);
    mergeVisibleLiveJobs(liveJobs);
    for (const job of liveJobs) applyLiveJob(job);
    refreshMissingLiveJobs(liveJobs);
    reconcileLiveJobsPage(liveJobs, message.pagination);
  }

  function mergeVisibleLiveJobs(liveJobs) {
    if (jobsPage.status === 'idle' || jobsPage.status === 'loading') {
      return;
    }
    const liveByID = new Map(liveJobs.filter(job => job?.id).map(job => [job.id, job]));
    let changed = false;
    const nextJobs = jobsPage.jobs.map(job => {
      const liveJob = liveByID.get(job.id);
      if (!liveJob) return job;
      const merged = mergeJobUpdate(job, liveJob);
      if (merged !== job) changed = true;
      return merged;
    });

    if (!changed) return;
    jobsPage = { ...jobsPage, status: jobsPage.status === 'error' ? 'error' : 'loaded', jobs: nextJobs, error: jobsPage.status === 'error' ? jobsPage.error : '' };
  }

  function mergeJobUpdate(job, update) {
    if (jobUpdateIsOlder(update, job)) return job;
    const merged = { ...job, ...update };
    if (update?.result_summary === undefined && job?.result_summary !== undefined) {
      merged.result_summary = job.result_summary;
    }
    return merged;
  }

  function mergeRESTJobsWithVisibleUpdates(restJobs) {
    const visibleByID = new Map(jobsPage.jobs.filter(job => job?.id).map(job => [job.id, job]));
    return restJobs.map(job => {
      const visibleJob = visibleByID.get(job?.id);
      return visibleJob ? mergeJobUpdate(job, visibleJob) : job;
    });
  }

  function jobUpdateIsOlder(update, job) {
    const updateKey = jobTimestampKey(update?.updated_at);
    const jobKey = jobTimestampKey(job?.updated_at);
    return updateKey !== '' && jobKey !== '' && updateKey < jobKey;
  }

  function jobTimestampKey(value) {
    const text = String(value || '').trim();
    const match = text.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?Z$/);
    if (!match) return '';
    const fraction = (match[2] || '').padEnd(9, '0').slice(0, 9);
    return `${match[1]}.${fraction}Z`;
  }

  function applyPendingLiveJobsSnapshot() {
    if (!pendingLiveJobsSnapshot) return;
    const pending = pendingLiveJobsSnapshot;
    pendingLiveJobsSnapshot = null;
    mergeVisibleLiveJobs(pending.liveJobs);
    if (path === '/downloads' && (jobsPage.status === 'loaded' || jobsPage.status === 'error')) {
      if (liveJobsPageSnapshotNeedsRefresh(pending.liveJobs, pending.pagination)) {
        scheduleLiveJobsPageRefresh();
      } else {
        clearLiveJobsPendingRefresh();
      }
    }
  }

  function setPendingLiveJobs(liveJobs, pagination = null) {
    pendingLiveJobsSnapshot = { liveJobs: freshestLiveJobs(liveJobs), pagination };
  }

  function addPendingLiveJobs(liveJobs) {
    pendingLiveJobsSnapshot = { liveJobs: freshestLiveJobs([...(pendingLiveJobsSnapshot?.liveJobs ?? []), ...liveJobs]), pagination: pendingLiveJobsSnapshot?.pagination ?? null };
  }

  function freshestLiveJobs(liveJobs) {
    const liveByID = new Map();
    for (const job of liveJobs) {
      if (!job?.id) continue;
      const existing = liveByID.get(job.id);
      liveByID.set(job.id, existing && jobUpdateIsOlder(job, existing) ? existing : job);
    }
    return [...liveByID.values()];
  }

  function reconcileLiveJobsPage(liveJobs, pagination) {
    if (path !== '/downloads') return;
    if (jobsPage.status === 'idle' || jobsPage.status === 'loading') return;
    if (liveJobsPageSnapshotNeedsRefresh(liveJobs, pagination)) scheduleLiveJobsPageRefresh();
  }

  function liveJobsPageSnapshotNeedsRefresh(liveJobs, pagination) {
    const expectedIDs = liveJobsPageIDsFromSnapshot(liveJobs, pagination);
    const currentIDs = jobsPage.jobs.map(job => job?.id).filter(Boolean);
    if (expectedIDs !== null && !sameJobIDs(expectedIDs, currentIDs)) {
      if (!sameJobIDSet(expectedIDs, currentIDs)) return true;
      reorderVisibleJobs(expectedIDs);
    }
    if (jobsPage.filter === 'all') {
      const total = liveJobsPaginationTotal(pagination);
      if (total !== null && total !== (Number(jobsPage.pagination?.total) || 0)) return true;
    }
    if (jobsPage.filter !== 'all') {
      const liveByID = new Map(liveJobs.filter(job => job?.id).map(job => [job.id, job]));
      for (const job of jobsPage.jobs) {
        const update = liveByID.get(job?.id);
        if (update && !jobUpdateIsOlder(update, job) && !jobMatchesJobsPageFilter(update)) return true;
      }
      if (liveJobs.some(liveJobMayEnterCurrentFilteredPage)) return true;
    }
    return false;
  }

  function liveJobsPageIDsFromSnapshot(liveJobs, pagination) {
    const page = Number(jobsPage.pagination?.page) || 1;
    const pageSize = Number(jobsPage.pagination?.page_size) || 20;
    const start = Math.max(0, (page - 1) * pageSize);
    const end = start + pageSize;
    const sorted = liveJobsForPageOrdering(liveJobs).filter(jobMatchesJobsPageFilter).sort(compareJobsForList);
    const total = liveJobsPaginationTotal(pagination);
    const snapshotPageSize = liveJobsPaginationPageSize(pagination) ?? liveJobs.length;
    const allJobsCovered = total !== null && total <= snapshotPageSize;
    if (jobsPage.filter !== 'all') {
      const filteredTotal = jobsPagePaginationTotal();
      const coveredFilteredCount = filteredTotal !== null ? Math.min(end, filteredTotal) : end;
      if (!allJobsCovered && sorted.length < coveredFilteredCount) return null;
      return sorted.slice(start, end).map(job => job.id);
    }

    const windowCovered = allJobsCovered || end <= snapshotPageSize || (total !== null && start >= total);
    if (!windowCovered) return null;
    return sorted.slice(start, end).map(job => job.id);
  }

  function liveJobsForPageOrdering(liveJobs) {
    const visibleByID = new Map(jobsPage.jobs.filter(job => job?.id).map(job => [job.id, job]));
    return liveJobs.map(job => visibleByID.get(job?.id) ?? job);
  }

  function liveJobMayEnterCurrentFilteredPage(job) {
    if ((Number(jobsPage.pagination?.page) || 1) !== 1) return false;
    if (!job?.id || !jobMatchesJobsPageFilter(job)) return false;
    if (jobsPage.jobs.some(visible => visible?.id === job.id)) return false;
    const pageSize = Number(jobsPage.pagination?.page_size) || 20;
    if (jobsPage.jobs.length < pageSize) return true;
    return compareJobsForList(job, jobsPage.jobs[jobsPage.jobs.length - 1]) < 0;
  }

  function jobMatchesJobsPageFilter(job) {
    return jobsPage.filter === 'all' || job?.status === jobsPage.filter;
  }

  function compareJobsForList(left, right) {
    return compareDescending(jobOrderTimestampKey(left?.updated_at), jobOrderTimestampKey(right?.updated_at))
      || compareDescending(jobOrderTimestampKey(left?.created_at), jobOrderTimestampKey(right?.created_at))
      || compareDescending(String(left?.id || ''), String(right?.id || ''));
  }

  function jobOrderTimestampKey(value) {
    return jobTimestampKey(value) || String(value || '');
  }

  function compareDescending(left, right) {
    if (left > right) return -1;
    if (left < right) return 1;
    return 0;
  }

  function sameJobIDs(left, right) {
    if (left.length !== right.length) return false;
    return left.every((id, index) => id === right[index]);
  }

  function sameJobIDSet(left, right) {
    if (left.length !== right.length) return false;
    const rightIDs = new Set(right);
    return left.every(id => rightIDs.has(id));
  }

  function reorderVisibleJobs(orderedIDs) {
    const jobsByID = new Map(jobsPage.jobs.filter(job => job?.id).map(job => [job.id, job]));
    jobsPage = { ...jobsPage, jobs: orderedIDs.map(id => jobsByID.get(id)).filter(Boolean) };
  }

  function liveJobsPaginationTotal(pagination) {
    const total = Number(pagination?.total);
    return Number.isFinite(total) && total >= 0 ? total : null;
  }

  function liveJobsPaginationPageSize(pagination) {
    const pageSize = Number(pagination?.page_size);
    return Number.isFinite(pageSize) && pageSize > 0 ? pageSize : null;
  }

  function jobsPagePaginationTotal() {
    const total = Number(jobsPage.pagination?.total);
    return Number.isFinite(total) && total >= 0 ? total : null;
  }

  function scheduleLiveJobsPageRefresh(delay = liveJobsPageRefreshDelay) {
    if (!liveJobsPageRefreshAllowed()) return;
    if (jobsForegroundRequestToken || liveJobsRefreshTimer || liveJobsRefreshInFlight) {
      liveJobsRefreshQueued = true;
      liveJobsRefreshQueuedDelay = Math.max(liveJobsRefreshQueuedDelay, delay);
      return;
    }
    liveJobsRefreshTimer = setTimeout(refreshLiveJobsPage, delay);
  }

  function stopLiveJobsPageRefresh(options = {}) {
    if (liveJobsRefreshTimer) {
      clearTimeout(liveJobsRefreshTimer);
      liveJobsRefreshTimer = undefined;
    }
    if (options.resetInFlight) {
      liveJobsRefreshInFlight = false;
      jobsBackgroundRequestToken += 1;
    }
    clearLiveJobsQueuedRefresh();
  }

  function clearLiveJobsPendingRefresh() {
    if (liveJobsRefreshTimer) {
      clearTimeout(liveJobsRefreshTimer);
      liveJobsRefreshTimer = undefined;
    }
    clearLiveJobsQueuedRefresh();
  }

  function clearLiveJobsQueuedRefresh() {
    liveJobsRefreshQueued = false;
    liveJobsRefreshQueuedDelay = liveJobsPageRefreshDelay;
  }

  function liveJobsPageRefreshAllowed() {
    return liveConnected && path === '/downloads' && (jobsPage.status === 'loaded' || jobsPage.status === 'error');
  }

  async function refreshLiveJobsPage() {
    liveJobsRefreshTimer = undefined;
    if (!liveJobsPageRefreshAllowed()) {
      liveJobsRefreshQueued = false;
      return;
    }
    if (jobsForegroundRequestToken) {
      liveJobsRefreshQueued = true;
      return;
    }
    liveJobsRefreshInFlight = true;
    liveJobsRefreshQueued = false;
    liveJobsRefreshQueuedDelay = liveJobsPageRefreshDelay;
    try {
      await loadJobs({ showLoading: false, background: true });
    } finally {
      liveJobsRefreshInFlight = false;
      if (liveJobsRefreshQueued) {
        const delay = liveJobsRefreshQueuedDelay;
        liveJobsRefreshQueued = false;
        liveJobsRefreshQueuedDelay = liveJobsPageRefreshDelay;
        scheduleLiveJobsPageRefresh(delay);
      }
    }
  }

  function refreshMissingLiveJobs(liveJobs) {
    if (!liveConnected) return;
    const seen = new Set(liveJobs.map(job => job?.id).filter(Boolean));
    for (const id of trackedActiveJobIDs()) {
      if (!seen.has(id)) fetchMissingLiveJob(id);
    }
  }

  function trackedActiveJobIDs() {
    const ids = [];
    if (activeChannelJobID && isChannelJobActive(channelJob.status)) ids.push(activeChannelJobID);
    if (activeScanJobID && isChannelJobActive(scanJob.status)) ids.push(activeScanJobID);
    if (activeVideoJobID && isChannelJobActive(videoJob.status)) ids.push(activeVideoJobID);
    if (activePreviewJobID && isChannelJobActive(previewJob.status)) ids.push(activePreviewJobID);
    for (const state of Object.values(catalogVideoJobs)) {
      if (state?.job?.id && isChannelJobActive(state.status)) ids.push(state.job.id);
    }
    return ids;
  }

  async function fetchMissingLiveJob(id) {
    const now = Date.now();
    const lastRequestedAt = liveMissingJobRequests.get(id) || 0;
    if (now - lastRequestedAt < 3000) return;
    liveMissingJobRequests.set(id, now);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(id)}`);
      addPendingLiveJobs([job]);
      mergeVisibleLiveJobs([job]);
      applyLiveJob(job);
    } catch {
      // The websocket reconnect/fallback path handles persistent failures.
    }
  }

  function applyLiveJob(job) {
    if (!job?.id) return;
    if (job.id === activeChannelJobID) applyChannelJobUpdate(job);
    if (job.id === activeScanJobID) applyScanJobUpdate(job);
    if (job.id === activeVideoJobID) applyVideoJobUpdate(job);
    if (job.id === activePreviewJobID) applyPreviewJobUpdate(job);
    const catalogEntry = Object.entries(catalogVideoJobs).find(([, state]) => state?.job?.id === job.id);
    if (catalogEntry) applyCatalogVideoJobUpdate(catalogEntry[0], job);
  }

  async function fetchJSON(url, options = {}) {
    const response = await fetch(url, { ...options, headers: { ...(options.headers ?? {}), Accept: 'application/json' } });
    if (!response.ok) {
      if (response.status === 401) {
        session = { ...session, status: 'loaded', authenticated: false, login_required: true, error: '' };
        stopLiveUpdates();
      }
      throw new Error(await responseErrorMessage(response));
    }

    return response.json();
  }

  async function addChannel() {
    if (channelJobTimer) clearTimeout(channelJobTimer);
    activeChannelJobID = '';
    channelJob = { status: 'loading', job: null, error: '' };
    try {
      const job = await postJSON('/api/channels', { url: channelURL });
      activeChannelJobID = job.id;
      channelURL = '';
      channelJob = { status: 'queued', job, error: '' };
      if (path === '/downloads') loadJobs({ page: 1, showLoading: false });
      watchChannelJob(job.id);
    } catch (error) {
      activeChannelJobID = '';
      channelJob = { status: 'error', job: null, error: error.message };
    }
  }

  async function scanCurrentChannel() {
    if (!channelID) return;
    if (scanJobTimer) clearTimeout(scanJobTimer);
    activeScanJobID = '';
    activeScanChannelID = '';
    scanJob = { status: 'loading', job: null, error: '' };
    try {
      const job = await postJSON(`/api/channels/${encodeURIComponent(channelID)}/scan`);
      activeScanJobID = job.id;
      activeScanChannelID = channelID;
      scanJob = { status: 'queued', job, error: '' };
      if (path === '/downloads') loadJobs({ page: 1, showLoading: false });
      watchScanJob(job.id);
    } catch (error) {
      activeScanJobID = '';
      scanJob = { status: 'error', job: null, error: error.message };
    }
  }

  async function toggleChannelAutoDownload() {
    const item = channelPage.item;
    if (!item || channelSubscriptionAction.status === 'loading') return;
    const requestToken = ++channelSubscriptionRequestToken;
    const nextSubscribed = !item.subscribed;
    channelSubscriptionAction = { status: 'loading', error: '' };
    try {
      const response = await postJSON(`/api/channels/${encodeURIComponent(item.id)}/subscription`, { subscribed: nextSubscribed }, 'PUT');
      if (requestToken !== channelSubscriptionRequestToken || channelPage.item?.id !== item.id) return;
      const subscribed = !!response.subscribed;
      channelSubscriptionOverride = { id: item.id, subscribed };
      channelPage = { ...channelPage, item: { ...channelPage.item, subscribed } };
      channelListPage = { ...channelListPage, channels: channelListPage.channels.map(channel => channel.id === item.id ? { ...channel, subscribed } : channel) };
      channelSubscriptionAction = { status: 'succeeded', error: '' };
    } catch (error) {
      if (requestToken !== channelSubscriptionRequestToken || channelPage.item?.id !== item.id) return;
      channelSubscriptionOverride = { id: '', subscribed: null };
      channelSubscriptionAction = { status: 'error', error: error.message };
    }
  }

  async function deleteChannel(id) {
    if (channelDeleteAction.status === 'loading') return;
    const found = channelListPage.channels.find(c => c.id === id) || channelPage.item;
    const name = (found && found.name) || 'this channel';
    const confirmed = window.confirm(`Remove "${name}" from the channel library?\n\nCatalog metadata entries are removed too. Channels with downloaded media or playlists cannot be removed.`);
    if (!confirmed) return;
    const requestToken = ++channelDeleteRequestToken;
    channelDeleteAction = { status: 'loading', error: '' };
    try {
      await requestNoContent(`/api/channels/${encodeURIComponent(id)}`, 'DELETE');
      if (requestToken !== channelDeleteRequestToken) return;
      channelDeleteAction = { status: 'succeeded', error: '' };
      if (channelDeleteResetTimer) clearTimeout(channelDeleteResetTimer);
      channelDeleteResetTimer = setTimeout(() => {
        if (channelDeleteAction.status === 'succeeded') channelDeleteAction = { status: 'idle', error: '' };
      }, 4000);
      if (channelPage.item?.id === id) {
        channelPage = { status: 'idle', item: null, videos: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
        navigate({ preventDefault() {}, button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false }, '/channels');
      }
      await loadChannels({ showLoading: false });
    } catch (error) {
      if (requestToken !== channelDeleteRequestToken) return;
      channelDeleteAction = { status: 'error', error: error.message };
    }
  }

  async function toggleKeepForever() {
    const item = video.item;
    if (!item || keepForeverAction.status === 'loading') return;
    const requestToken = ++keepForeverRequestToken;
    const nextKeepForever = !item.keep_forever;
    keepForeverAction = { status: 'loading', error: '' };
    try {
      const response = await postJSON(`/api/videos/${encodeURIComponent(item.id)}/keep-forever`, { keep_forever: nextKeepForever }, 'PUT');
      if (requestToken !== keepForeverRequestToken || video.item?.id !== item.id) return;
      const keepForever = !!response.keep_forever;
      video = { ...video, item: { ...video.item, keep_forever: keepForever } };
      library = { ...library, videos: library.videos.map(entry => entry.id === item.id ? { ...entry, keep_forever: keepForever } : entry) };
      channelPage = { ...channelPage, videos: channelPage.videos.map(entry => entry.id === item.id ? { ...entry, keep_forever: keepForever } : entry) };
      playlistPage = { ...playlistPage, videos: playlistPage.videos.map(entry => entry.id === item.id ? { ...entry, keep_forever: keepForever } : entry) };
      keepForeverAction = { status: 'succeeded', error: '' };
    } catch (error) {
      if (requestToken !== keepForeverRequestToken || video.item?.id !== item.id) return;
      keepForeverAction = { status: 'error', error: error.message };
    }
  }

  async function markVideoPlayed() {
    const item = video.item;
    if (!item || !item.media_url || videoIsWatched(item) || markPlayedAction.status === 'loading') return;
    const requestToken = ++markPlayedRequestToken;
    playbackProgressMutationToken += 1;
    playbackSync = { ...playbackSync, videoID: item.id, pending: false, inFlightKey: '', queued: null, lastKey: '', lastSentAt: Date.now() };
    markPlayedAction = { status: 'loading', error: '' };
    try {
      const progress = await postJSON(`/api/videos/${encodeURIComponent(item.id)}/progress`, playedProgressPayload(item), 'PUT');
      if (requestToken !== markPlayedRequestToken || video.item?.id !== item.id) return;
      updatePlaybackProgressState(item.id, progress);
      markPlayedAction = { status: 'succeeded', error: '' };
    } catch (error) {
      if (requestToken !== markPlayedRequestToken || video.item?.id !== item.id) return;
      markPlayedAction = { status: 'error', error: error.message };
    }
  }

  async function deleteVideoMedia() {
    const item = video.item;
    if (!item?.id || !item.media_url || deleteVideoMediaAction.status === 'loading') return;
    const confirmed = window.confirm(`Delete the local video file for "${item.title || 'this video'}"?\n\nMetadata, comments, thumbnails, and catalog information will stay.`);
    if (!confirmed) return;

    const requestToken = ++deleteVideoMediaRequestToken;
    deleteVideoMediaAction = { status: 'loading', error: '' };
    try {
      if (watchMediaElement && !watchMediaElement.paused) watchMediaElement.pause();
      await requestNoContent(`/api/videos/${encodeURIComponent(item.id)}/media`, 'DELETE');
      if (requestToken !== deleteVideoMediaRequestToken || video.item?.id !== item.id) return;
      deleteVideoMediaAction = { status: 'succeeded', error: '' };
      loadLibrary({ showLoading: false });
      if (channelID) loadChannel(channelID);
      if (playlistID) loadPlaylist(playlistID, { showLoading: false });
      await loadVideo(item.id);
    } catch (error) {
      if (requestToken !== deleteVideoMediaRequestToken || video.item?.id !== item.id) return;
      deleteVideoMediaAction = { status: 'error', error: error.message };
    }
  }

  function playedProgressPayload(item) {
    const duration = Math.max(0, Math.floor(Number(watchMediaElement?.duration) || Number(item?.duration_seconds) || Number(item?.progress?.duration_seconds) || 0));
    const position = duration > 0 ? duration : Math.max(0, Math.floor(Number(watchMediaElement?.currentTime) || Number(item?.position_seconds) || Number(item?.progress?.position_seconds) || 0));
    return { position_seconds: position, duration_seconds: duration, watched: true };
  }

  async function addVideo(rawURL = videoURL, options = {}) {
    if (videoJobTimer) clearTimeout(videoJobTimer);
    activeVideoJobID = '';
    videoJob = { status: 'loading', job: null, error: '' };
    let normalizedURL;
    try {
      normalizedURL = normalizeDirectVideoURLInput(rawURL);
    } catch (error) {
      videoJob = { status: 'error', job: null, error: error.message };
      return;
    }
    try {
      const job = await postJSON('/api/downloads', { url: normalizedURL });
      activeVideoJobID = job.id;
      if (options.clearInput !== false) videoURL = '';
      videoJob = { status: job.status || 'queued', job, error: job.error || '' };
      if (path === '/downloads') loadJobs({ page: 1, showLoading: false });
      watchVideoJob(job.id);
    } catch (error) {
      activeVideoJobID = '';
      videoJob = { status: 'error', job: null, error: error.message };
    }
  }

  function watchChannelJob(id) {
    if (channelJobTimer) clearTimeout(channelJobTimer);
    if (!id || liveConnected) return;
    channelJobTimer = setTimeout(() => pollChannelJob(id), 1500);
  }

  function watchScanJob(id) {
    if (scanJobTimer) clearTimeout(scanJobTimer);
    if (!id || liveConnected) return;
    scanJobTimer = setTimeout(() => pollScanJob(id), 1500);
  }

  function watchVideoJob(id) {
    if (videoJobTimer) clearTimeout(videoJobTimer);
    if (!id || liveConnected) return;
    videoJobTimer = setTimeout(() => pollVideoJob(id), 1500);
  }

  function watchPreviewJob(id) {
    if (previewJobTimer) clearTimeout(previewJobTimer);
    if (!id || liveConnected) return;
    previewJobTimer = setTimeout(() => pollPreviewJob(id), 1500);
  }

  function watchCatalogVideoJob(itemID, jobID) {
    clearCatalogVideoJobTimer(itemID);
    if (!itemID || !jobID || liveConnected) return;
    catalogVideoJobTimers.set(itemID, setTimeout(() => pollCatalogVideoJob(itemID, jobID), 1500));
  }

  function stopJobFallbackPolling() {
    if (channelJobTimer) clearTimeout(channelJobTimer);
    if (videoJobTimer) clearTimeout(videoJobTimer);
    if (previewJobTimer) clearTimeout(previewJobTimer);
    if (scanJobTimer) clearTimeout(scanJobTimer);
    channelJobTimer = undefined;
    videoJobTimer = undefined;
    previewJobTimer = undefined;
    scanJobTimer = undefined;
    clearCatalogVideoJobTimers();
  }

  function resumeJobFallbackPolling() {
    if (liveConnected) return;
    if (activeChannelJobID && isChannelJobActive(channelJob.status)) watchChannelJob(activeChannelJobID);
    if (activeScanJobID && isChannelJobActive(scanJob.status)) watchScanJob(activeScanJobID);
    if (activeVideoJobID && isChannelJobActive(videoJob.status)) watchVideoJob(activeVideoJobID);
    if (activePreviewJobID && isChannelJobActive(previewJob.status)) watchPreviewJob(activePreviewJobID);
    for (const [itemID, state] of Object.entries(catalogVideoJobs)) {
      if (state?.job?.id && isChannelJobActive(state.status)) watchCatalogVideoJob(itemID, state.job.id);
    }
  }

  async function pollChannelJob(id) {
    if (channelJobTimer) clearTimeout(channelJobTimer);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(id)}`);
      if (id !== activeChannelJobID) return;
      applyChannelJobUpdate(job);
      if (id === activeChannelJobID) watchChannelJob(id);
    } catch (error) {
      if (id !== activeChannelJobID) return;
      activeChannelJobID = '';
      channelJob = { status: 'error', job: null, error: error.message };
    }
  }

  function applyChannelJobUpdate(job) {
    if (job.id !== activeChannelJobID) return;
    channelJob = { status: job.status, job, error: job.error || '' };
    if (job.status === 'succeeded') {
      activeChannelJobID = '';
      loadLibrary({ showLoading: false });
      return;
    }
    if (job.status === 'failed' || job.status === 'cancelled') activeChannelJobID = '';
  }

  async function pollScanJob(id) {
    if (scanJobTimer) clearTimeout(scanJobTimer);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(id)}`);
      if (id !== activeScanJobID) return;
      applyScanJobUpdate(job);
      if (id === activeScanJobID) watchScanJob(id);
    } catch (error) {
      if (id !== activeScanJobID) return;
      activeScanJobID = '';
      activeScanChannelID = '';
      scanJob = { status: 'error', job: null, error: error.message };
    }
  }

  function applyScanJobUpdate(job) {
    if (job.id !== activeScanJobID) return;
    scanJob = { status: job.status, job, error: job.error || '' };
    if (job.status === 'succeeded') {
      const scannedChannelID = activeScanChannelID;
      activeScanJobID = '';
      activeScanChannelID = '';
      loadLibrary({ showLoading: false });
      if (channelID === scannedChannelID) loadChannel(scannedChannelID);
      return;
    }
    if (job.status === 'failed' || job.status === 'cancelled') {
      activeScanJobID = '';
      activeScanChannelID = '';
    }
  }

  async function pollVideoJob(id) {
    if (videoJobTimer) clearTimeout(videoJobTimer);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(id)}`);
      if (id !== activeVideoJobID) return;
      applyVideoJobUpdate(job);
      if (id === activeVideoJobID) watchVideoJob(id);
    } catch (error) {
      if (id !== activeVideoJobID) return;
      activeVideoJobID = '';
      videoJob = { status: 'error', job: null, error: error.message };
    }
  }

  async function pollPreviewJob(id) {
    if (previewJobTimer) clearTimeout(previewJobTimer);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(id)}`);
      if (id !== activePreviewJobID) return;
      applyPreviewJobUpdate(job);
      if (id === activePreviewJobID) watchPreviewJob(id);
    } catch (error) {
      if (id !== activePreviewJobID) return;
      activePreviewJobID = '';
      previewJob = { status: 'error', job: null, error: error.message };
    }
  }

  function applyVideoJobUpdate(job) {
    if (job.id !== activeVideoJobID) return;
    videoJob = { status: job.status, job, error: job.error || '' };
    if (job.status === 'succeeded') {
      activeVideoJobID = '';
      loadLibrary({ showLoading: false });
      if (channelID) loadChannel(channelID);
      if (videoID) loadVideo(videoID);
      return;
    }
    if (job.status === 'failed' || job.status === 'cancelled') activeVideoJobID = '';
  }

  function applyPreviewJobUpdate(job) {
    if (job.id !== activePreviewJobID) return;
    previewJob = { status: job.status, job, error: job.error || '' };
    if (job.status === 'succeeded') {
      activePreviewJobID = '';
      loadLibrary({ showLoading: false });
      if (channelID) loadChannel(channelID);
      if (playlistID) loadPlaylist(playlistID, { showLoading: false });
      if (videoID) refreshActiveVideoDetail(videoID, { preserveMediaURL: true });
      return;
    }
    if (job.status === 'failed' || job.status === 'cancelled') activePreviewJobID = '';
  }

  function normalizeDirectVideoURLInput(rawURL) {
    const value = rawURL.trim();
    if (value === '') throw new Error('Video URL is required.');
    let parsed;
    try {
      parsed = new URL(value);
    } catch {
      throw new Error('Enter a valid http or https video URL.');
    }
    parsed.protocol = parsed.protocol.toLowerCase();
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      throw new Error('Video URL must use http or https.');
    }
    const host = parsed.hostname.toLowerCase();
    if (host === 'youtu.be') {
      const videoID = firstURLSegment(parsed.pathname);
      if (!isLikelyYouTubeVideoID(videoID)) throw new Error('Enter a single YouTube video URL.');
      return `https://www.youtube.com/watch?v=${encodeURIComponent(videoID)}`;
    }
    if (host === 'youtube.com' || host.endsWith('.youtube.com')) {
      const path = parsed.pathname.replace(/^\/+|\/+$/g, '');
      if (path === 'watch' && isLikelyYouTubeVideoID(parsed.searchParams.get('v'))) {
        return `https://www.youtube.com/watch?v=${encodeURIComponent(parsed.searchParams.get('v'))}`;
      }
      for (const prefix of ['shorts/', 'embed/', 'live/', 'v/']) {
        const videoID = firstURLSegment(path.slice(prefix.length));
        if (path.startsWith(prefix) && isLikelyYouTubeVideoID(videoID)) {
          return `https://www.youtube.com/watch?v=${encodeURIComponent(videoID)}`;
        }
      }
      throw new Error('Use a single YouTube video URL, not a channel or playlist URL.');
    }

    return parsed.href;
  }

  function firstURLSegment(pathname) {
    return pathname.replace(/^\/+/, '').split('/')[0] || '';
  }

  function isLikelyYouTubeVideoID(value) {
    return /^[A-Za-z0-9_-]{11}$/.test(value || '');
  }

  async function postJSON(url, body, method = 'POST') {
    const options = {
      method,
      headers: { Accept: 'application/json' },
    };
    if (body !== undefined) {
      options.headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(body);
    }
    const response = await fetch(url, options);
    if (!response.ok) {
      if (response.status === 401) {
        session = { ...session, status: 'loaded', authenticated: false, login_required: true, error: '' };
        stopLiveUpdates();
      }
      throw new Error(await responseErrorMessage(response));
    }

    return response.json();
  }

  async function requestNoContent(url, method = 'POST') {
    const response = await fetch(url, { method, headers: { Accept: 'application/json' } });
    if (!response.ok) {
      if (response.status === 401) {
        session = { ...session, status: 'loaded', authenticated: false, login_required: true, error: '' };
        stopLiveUpdates();
      }
      throw new Error(await responseErrorMessage(response));
    }
  }

  async function responseErrorMessage(response) {
    const fallback = `Request failed with status ${response.status}`;
    const text = (await response.text()).trim();
    if (!text) return fallback;
    try {
      const parsed = JSON.parse(text);
      return parsed.error || fallback;
    } catch {
      return text;
    }
  }

  async function loadSession() {
    const requestToken = ++sessionRequestToken;
    session = { ...session, status: 'loading', error: '' };
    try {
      const response = await fetchJSON('/api/session');
      if (requestToken !== sessionRequestToken) return;
      session = { status: 'loaded', auth_enabled: !!response.auth_enabled, configured: response.configured !== false, authenticated: !!response.authenticated, username: response.username || '', login_required: !!response.login_required, error: '' };
      loginState = { status: 'idle', error: '' };
      if (!session.auth_enabled || session.authenticated) {
        ensureLiveUpdates();
        runRouteLoad();
      }
    } catch (error) {
      if (requestToken !== sessionRequestToken) return;
      session = { ...session, status: 'error', error: error.message };
    }
  }

  async function submitLogin(event) {
    event.preventDefault();
    loginState = { status: 'loading', error: '' };
    try {
      const response = await postJSON('/api/login', { username: loginUsername, password: loginPassword });
      loginPassword = '';
      session = { status: 'loaded', auth_enabled: !!response.auth_enabled, configured: response.configured !== false, authenticated: !!response.authenticated, username: response.username || loginUsername, login_required: false, error: '' };
      loginState = { status: 'idle', error: '' };
      ensureLiveUpdates();
      runRouteLoad();
    } catch {
      loginState = { status: 'error', error: 'Invalid username or password.' };
    }
  }

  async function logout() {
    loginState = { status: 'idle', error: '' };
    try {
      const response = await postJSON('/api/logout');
      session = { status: 'loaded', auth_enabled: !!response.auth_enabled, configured: response.configured !== false, authenticated: !!response.authenticated, username: '', login_required: !!response.login_required || !!response.auth_enabled, error: '' };
      library = { status: 'idle', videos: [], error: '' };
      homeSetup = { status: 'idle', hasKnownContent: true, error: '' };
      homeArchiveState = 'unknown';
      video = { status: 'idle', item: null, error: '' };
      channelPage = { status: 'idle', item: null, videos: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
      channelListPage = { status: 'idle', channels: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
      playlistListPage = { status: 'idle', playlists: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
      playlistPage = { status: 'idle', item: null, videos: [], pagination: { page: 1, page_size: 50, total: 0 }, error: '' };
      searchPage = { status: 'idle', query: '', results: [], error: '' };
      commentsPage = { status: 'idle', videoID: '', comments: [], pagination: { page: 1, page_size: 20, total: 0 }, error: '' };
      jobsPage = { status: 'idle', filter: 'all', jobs: [], pagination: { page: 1, page_size: 20, total: 0 }, error: '' };
      channelJob = { status: 'idle', job: null, error: '' };
      videoJob = { status: 'idle', job: null, error: '' };
      previewJob = { status: 'idle', job: null, error: '' };
      catalogVideoJobs = {};
      scanJob = { status: 'idle', job: null, error: '' };
      channelSubscriptionAction = { status: 'idle', error: '' };
      channelSubscriptionOverride = { id: '', subscribed: null };
      activeChannelJobID = '';
      activeVideoJobID = '';
      activePreviewJobID = '';
      activeScanJobID = '';
      activeScanChannelID = '';
      jobAction = { id: '', action: '', error: '' };
      if (channelJobTimer) clearTimeout(channelJobTimer);
      if (videoJobTimer) clearTimeout(videoJobTimer);
      if (previewJobTimer) clearTimeout(previewJobTimer);
      if (scanJobTimer) clearTimeout(scanJobTimer);
      clearCatalogVideoJobTimers();
      stopJobsPolling();
      stopLiveUpdates();
    } catch (error) {
      session = { ...session, error: error.message };
    }
  }

  function submitSearch(event) {
    event.preventDefault();
    const query = searchInput.trim();
    if (query === '') return;
    setRoute(`/search?q=${encodeURIComponent(query)}`);
  }

  function videoIDFromPath(value) {
    if (!value.startsWith('/videos/')) return '';
    try {
      return decodeURIComponent(value.slice('/videos/'.length));
    } catch {
      return '';
    }
  }

  function channelIDFromPath(value) {
    if (!value.startsWith('/channels/')) return '';
    try {
      return decodeURIComponent(value.slice('/channels/'.length));
    } catch {
      return '';
    }
  }

  function playlistIDFromPath(value) {
    if (!value.startsWith('/playlists/')) return '';
    try {
      return decodeURIComponent(value.slice('/playlists/'.length));
    } catch {
      return '';
    }
  }

  function pageTitleForRoute() {
    if (videoID) return video.item?.title ?? 'Video';
    if (channelID) return channelPage.item?.name ?? 'Channel';
    if (playlistID) return playlistPage.item?.title ?? 'Playlist';
    if (path === '/channels') return 'Channels';
    if (path === '/playlists') return 'Playlists';
    if (path === '/search') return searchQuery ? `Search: ${searchQuery}` : 'Search';
    return utilityRoutes[path]?.title ?? 'Home';
  }

  function playlistHref(playlist) {
    return playlist?.id ? `/playlists/${encodeURIComponent(playlist.id)}` : '/playlists';
  }

  function resultHref(result) {
    const type = result.record?.type || result.owner_type;
    const id = result.record?.id || result.owner_id;
    if (type === 'channel') return `/channels/${encodeURIComponent(id)}`;
    if (type === 'playlist') return `/playlists/${encodeURIComponent(id)}`;
    return `/videos/${encodeURIComponent(id)}`;
  }

  function resultTitle(result) {
    return result?.record?.title || result?.owner_id || 'Search result';
  }

  function resultMeta(result) {
    const parts = [String(result?.owner_type || 'result').replaceAll('_', ' ')];
    if (result?.record?.channel?.name) parts.push(result.record.channel.name);
    return parts.join(' · ');
  }

  function isChannelJobActive(status) {
    return status === 'loading' || status === 'queued' || status === 'running';
  }

  function canDownloadCatalogVideo(item) {
    if (item?.members_only) return false;
    return isCatalogOnly(item) && item?.can_download === true;
  }

  function upNextVideosFromEndpoint(items, currentID) {
    return items.filter(item => item?.id && item.id !== currentID && !videoIsWatched(item));
  }

  function videoIsWatched(item) {
    if (item?.progress?.watched !== undefined && item?.progress?.watched !== null) return !!item.progress.watched;
    return !!item?.watched;
  }

  function catalogDownloadDisabled(item) {
    return !canDownloadCatalogVideo(item) || isCatalogVideoJobLocked(catalogVideoJobFor(item)?.status);
  }

  function catalogVideoJobFor(item) {
    return catalogVideoJobs[item?.id] ?? null;
  }

  function isCatalogVideoJobLocked(status) {
    return isChannelJobActive(status) || status === 'succeeded';
  }

  function catalogDownloadButtonLabel(state, noun = '') {
    if (isChannelJobActive(state?.status)) return noun ? `Downloading ${noun}` : 'Downloading';
    if (state?.status === 'succeeded') return noun ? `Downloaded ${noun}` : 'Downloaded';
    if (state?.status === 'failed' || state?.status === 'error') return noun ? `Retry ${noun}` : 'Retry';
    return noun ? `Download ${noun}` : 'Download';
  }

  function catalogVideoJobState(job, fallbackStatus = 'queued') {
    return { status: job?.status || fallbackStatus, progress: Number(job?.progress) || 0, job, error: job?.error || '' };
  }

  function setCatalogVideoJob(videoID, state) {
    if (!videoID) return;
    catalogVideoJobs = { ...catalogVideoJobs, [videoID]: state };
  }

  function clearCatalogVideoJobTimer(videoID) {
    const timer = catalogVideoJobTimers.get(videoID);
    if (timer) clearTimeout(timer);
    catalogVideoJobTimers.delete(videoID);
  }

  function clearCatalogVideoJobTimers() {
    for (const timer of catalogVideoJobTimers.values()) clearTimeout(timer);
    catalogVideoJobTimers.clear();
  }

  function videoPosterURL(item) {
    const thumbnailURL = item?.thumbnail_url || '';
    return thumbnailURL.startsWith('/media/') ? thumbnailURL : undefined;
  }

  async function downloadCatalogItem(item) {
    const itemID = item?.id || '';
    if (catalogDownloadDisabled(item)) return;
    clearCatalogVideoJobTimer(itemID);
    setCatalogVideoJob(itemID, { status: 'loading', progress: 0, job: null, error: '' });
    try {
      const job = await postJSON(`/api/videos/${encodeURIComponent(itemID)}/download`);
      setCatalogVideoJob(itemID, catalogVideoJobState(job));
      if (path === '/downloads') loadJobs({ page: 1, showLoading: false });
      watchCatalogVideoJob(itemID, job.id);
    } catch (error) {
      setCatalogVideoJob(itemID, { status: 'error', progress: 0, job: null, error: error.message });
    }
  }

  async function pollCatalogVideoJob(itemID, jobID) {
    clearCatalogVideoJobTimer(itemID);
    try {
      const job = await fetchJSON(`/api/jobs/${encodeURIComponent(jobID)}`);
      if (catalogVideoJobs[itemID]?.job?.id !== jobID) return;
      applyCatalogVideoJobUpdate(itemID, job);
      if (catalogVideoJobs[itemID]?.job?.id === jobID && isChannelJobActive(catalogVideoJobs[itemID]?.status)) watchCatalogVideoJob(itemID, jobID);
    } catch (error) {
      if (catalogVideoJobs[itemID]?.job?.id !== jobID) return;
      if (isChannelJobActive(catalogVideoJobs[itemID]?.status)) {
        watchCatalogVideoJob(itemID, jobID);
        return;
      }
      setCatalogVideoJob(itemID, { ...catalogVideoJobs[itemID], status: 'error', error: error.message });
    }
  }

  function applyCatalogVideoJobUpdate(itemID, job) {
    const current = catalogVideoJobs[itemID];
    if (current?.job?.id !== job.id) return;
    if (jobUpdateIsOlder(job, current.job)) return;
    const previousStatus = current.status;
    setCatalogVideoJob(itemID, catalogVideoJobState(job));
    if (previousStatus !== 'succeeded' && job.status === 'succeeded') {
      loadLibrary({ showLoading: false });
      if (channelID) loadChannel(channelID);
      if (playlistID) loadPlaylist(playlistID, { showLoading: false });
      if (videoID === itemID) loadVideo(itemID);
    }
  }

  async function runJobAction(job, action) {
    if (!job?.id) return;
    stopJobsPolling();
    jobAction = { id: job.id, action, error: '' };
    try {
      await postJSON(`/api/jobs/${encodeURIComponent(job.id)}/${action}`);
      jobAction = { id: '', action: '', error: '' };
      loadJobs({ showLoading: false });
    } catch (error) {
      jobAction = { id: '', action: '', error: `Could not ${action} ${job.id}: ${error.message}` };
      scheduleJobsPolling();
    }
  }

  function canCancelJob(job) {
    return (job?.status === 'queued' || job?.status === 'running') && !job.cancel_requested && !job.result_summary;
  }

  function canRetryJob(job) {
    return job?.status === 'failed' && !job.result_summary;
  }

  function jobActionInProgress(job, action) {
    return jobAction.id === job?.id && jobAction.action === action;
  }

  function setJobFilter(filter) {
    if (filter === jobsPage.filter) return;
    stopLiveJobsPageRefresh();
    loadJobs({ filter, page: 1 });
  }

  function changeJobsPage(page) {
    stopLiveJobsPageRefresh();
    loadJobs({ page, showLoading: false });
  }

  function changeChannelsPage(page) {
    loadChannels({ page, showLoading: false });
  }

  function changeChannelVideosPage(page) {
    if (!channelID) return;
    loadChannel(channelID, { page, showLoading: false });
  }

  function changePlaylistsPage(page) {
    loadPlaylists({ page, showLoading: false });
  }

  function changePlaylistVideosPage(page) {
    if (!playlistID) return;
    loadPlaylist(playlistID, { page, showLoading: false });
  }

  function changeCommentsPage(page) {
    if (!videoID) return;
    loadComments(videoID, { page });
  }

  function jobsLastPage(pagination = jobsPage.pagination) {
    return lastPage(pagination, 20);
  }

  function lastPage(pagination, fallbackPageSize = 50) {
    const total = Number(pagination?.total) || 0;
    const pageSize = Number(pagination?.page_size) || fallbackPageSize;
    return Math.max(1, Math.ceil(total / pageSize));
  }

  function jobProgressPercent(job) {
    const value = Number(job?.progress) || 0;
    return Math.round(Math.min(1, Math.max(0, value)) * 100);
  }

  function formatJobTime(value) {
    if (!value) return 'Not recorded';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
  }

  function abbreviateJobID(value) {
    const id = String(value || '');
    if (id.length <= 22) return id;
    return `${id.slice(0, 8)}...${id.slice(-6)}`;
  }

  function jobTypeLabel(value) {
    if (value === 'download') return 'Video download';
    if (value === 'channel_first_download') return 'Add channel';
    if (value === 'channel_scan') return 'Channel scan';
    if (value === 'channel_auto_download') return 'Channel auto-download';
    if (value === 'timeline_preview') return 'Timeline preview';
    if (value === 'ta_import') return 'TubeArchivist import';
    return String(value || 'job').replaceAll('_', ' ');
  }

  function jobSummary(job) {
    if (job.error && job.status === 'failed') return jobErrorSummary(job.error);
    if (job.result_summary) return job.result_summary;
    if (job.status === 'queued') return 'Waiting for a worker to claim this job.';
    if (job.status === 'running') return 'Worker is actively processing this job.';
    if (job.error) return `Previous error: ${job.error}`;
    return 'No result details recorded.';
  }

  function jobErrorSummary(error) {
    const lines = String(error || '')
      .split(/\r?\n/)
      .map(line => line.trim())
      .filter(Boolean);
    const candidate = lines.find(line => /^error[:\s]/i.test(line))
      ?? lines.find(line => /(unable|could not|invalid|denied|no space)/i.test(line))
      ?? lines.find(line => /failed/i.test(line))
      ?? lines[0]
      ?? 'Job failed without an error message.';
    const summary = candidate.length > 180 ? `${candidate.slice(0, 177).trimEnd()}...` : candidate;
    return lines.length > 1 ? `${summary} (${lines.length} log lines available)` : summary;
  }

  async function copyJobError(job) {
    if (!job?.error) return;
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(job.error);
      jobErrorCopy = { id: job.id, status: 'Copied raw log' };
    } catch {
      jobErrorCopy = { id: job.id, status: 'Select the raw log to copy it' };
    }
    if (jobErrorCopyTimer) clearTimeout(jobErrorCopyTimer);
    jobErrorCopyTimer = setTimeout(() => {
      if (jobErrorCopy.id === job.id) jobErrorCopy = { id: '', status: '' };
    }, 2500);
  }

  function jobResult(job) {
    if (!job?.result_summary) return null;
    try {
      return JSON.parse(job.result_summary);
    } catch {
      return null;
    }
  }

  function jobTargetHref(job) {
    const result = jobResult(job);
    if (result?.video_id) return videoHref(result.video_id);
    if (result?.channel_id) return `/channels/${encodeURIComponent(result.channel_id)}`;
    return '';
  }

  function jobTargetLabel(job) {
    const result = jobResult(job);
    if (result?.video_id) return 'Open video';
    if (result?.channel_id) return 'Open channel';
    return '';
  }

  function jobCountLabel(total) {
    const value = Number(total) || 0;
    return `${value} ${value === 1 ? 'job' : 'jobs'}`;
  }

  function libraryFeedCountLabel(count, total) {
    const visible = Number(count) || 0;
    if (visible <= 0) return '';
    const reportedTotal = Number(total);
    const boundedTotal = Number.isFinite(reportedTotal) && reportedTotal >= visible ? reportedTotal : visible;
    return `Showing ${visible} of ${boundedTotal} videos`;
  }

  function resultFallback(result) {
    return channelInitial(resultTitle(result));
  }

  function restorePlaybackPosition(event) {
    if (userTimestampSeeked) {
      clearInitialProgressGuard(video.item?.id);
      return;
    }
    const media = event.currentTarget;
    const position = playbackRestorePosition(video.item, media);
    if (position <= 0) return;
    if (Math.abs(media.currentTime - position) > 2) media.currentTime = position;
    clearInitialProgressGuard(video.item?.id);
  }

  function seekVideoTimestamp(seconds) {
    const target = Number(seconds);
    if (!watchMediaElement || !Number.isFinite(target) || target < 0) return;
    const duration = Number(watchMediaElement.duration);
    watchMediaElement.currentTime = Number.isFinite(duration) && duration > 0 ? Math.min(target, duration) : target;
    userTimestampSeeked = true;
    clearInitialProgressGuard(video.item?.id);
  }

  function restoreAndAutoplay(event) {
    const media = event.currentTarget;
    restorePlaybackPosition(event);
    skipSponsorSegment(media);
    const playbackRate = pendingPlaybackRateRestore;
    pendingPlaybackRateRestore = 0;
    suppressPlaybackRateSave = false;
    applySavedPlaybackRate(media, playbackRate);
    const suppressAutoplay = suppressNextLoadedMetadataAutoplay;
    suppressNextLoadedMetadataAutoplay = false;
    autoplayOnLoad = false;
    if (!suppressAutoplay) attemptAutoplay(media);
  }

  function rememberPlaybackRate(event) {
    if (suppressPlaybackRateSave) return;
    const rate = normalizePlaybackRate(event.currentTarget?.playbackRate);
    if (!rate) return;
    try {
      window.localStorage.setItem(playbackRateStorageKey, String(rate));
    } catch {
      // Storage can be unavailable in private or locked-down browsing contexts.
    }
  }

  function applySavedPlaybackRate(media, preferredRate = 0) {
    const rate = normalizePlaybackRate(preferredRate) || savedPlaybackRate();
    if (!media || !rate || Math.abs(Number(media.playbackRate) - rate) < 0.001) return;
    try {
      media.playbackRate = rate;
    } catch {
      // Some media implementations can reject unsupported playback rates.
    }
  }

  function savedPlaybackRate() {
    try {
      return normalizePlaybackRate(window.localStorage.getItem(playbackRateStorageKey));
    } catch {
      return 0;
    }
  }

  function normalizePlaybackRate(value) {
    const rate = Number(value);
    return Number.isFinite(rate) && rate > 0 && rate <= 16 ? rate : 0;
  }

  function attemptAutoplay(media) {
    if (!media || typeof media.play !== 'function') return;
    const playRequest = media.play();
    if (playRequest && typeof playRequest.catch === 'function') void playRequest.catch(() => {});
  }

  function handlePlaybackPlay(event) {
    if (!consumeSuppressedMediaURLRefreshPlay(video.item) && refreshStaleWatchMediaURL(event.currentTarget, { autoplay: true, leadMS: mediaURLPlaybackRefreshLeadMS, pauseActive: true })) return;
    resumeAudioNormalization();
    if (!playbackFeedbackCanShowPlay) return;
    playbackFeedbackCanShowPlay = false;
    showPlaybackFeedback('play');
  }

  function handlePlaybackError(event) {
    refreshStaleWatchMediaURL(event.currentTarget, { autoplay: true, leadMS: 0, pauseActive: true, retryOnce: true });
  }

  function handlePlaybackPause(event) {
    if (suppressNextPlaybackPause) {
      suppressNextPlaybackPause = false;
      handlePlaybackProgress(event, { force: true });
      return;
    }
    handlePlaybackProgress(event, { force: true });
    if (event.currentTarget?.ended) return;
    playbackFeedbackCanShowPlay = true;
    showPlaybackFeedback('pause');
  }

  function refreshStaleWatchMediaURL(media, options = {}) {
    const item = video.item;
    if (!item?.id || !item.media_url || !signedMediaURLNeedsRefresh(item.media_url, options.leadMS ?? 0)) return false;
    if (options.retryOnce && mediaURLRefreshRetry.videoID === item.id && mediaURLRefreshRetry.mediaURL === item.media_url) return false;
    if (media && !media.paused && options.pauseActive !== true) return false;
    if (options.retryOnce) mediaURLRefreshRetry = { videoID: item.id, mediaURL: item.media_url };
    let pausedForRefresh = false;
    if (media && !media.paused && typeof media.pause === 'function') {
      suppressNextPlaybackPause = true;
      try {
        media.pause();
        pausedForRefresh = true;
      } catch {
        suppressNextPlaybackPause = false;
      }
    }
    void refreshWatchMediaURL(item.id, item.media_url, media, { ...options, pausedForRefresh });

    return true;
  }

  function scheduleWatchMediaURLRefresh(activeVideoID, item) {
    clearMediaURLRefreshTimer();
    if (!activeVideoID || item?.id !== activeVideoID || !item?.media_url) return;
    const expiresAt = signedMediaURLExpiresAt(item.media_url);
    if (expiresAt <= 0) return;
    const delay = Math.max(0, expiresAt - Date.now() - mediaURLPlaybackRefreshLeadMS);
    mediaURLRefreshTimer = setTimeout(() => {
      refreshStaleWatchMediaURL(watchMediaElement, { leadMS: mediaURLPlaybackRefreshLeadMS });
    }, delay);
  }

  function clearMediaURLRefreshTimer() {
    if (!mediaURLRefreshTimer) return;
    clearTimeout(mediaURLRefreshTimer);
    mediaURLRefreshTimer = undefined;
  }

  function handleWatchMediaURLWake() {
    if (document.visibilityState === 'hidden') return;
    refreshStaleWatchMediaURL(watchMediaElement, { leadMS: mediaURLPlaybackRefreshLeadMS });
  }

  async function refreshWatchMediaURL(id, staleMediaURL, media, options = {}) {
    const requestToken = ++mediaURLRefreshRequestToken;
    const restorePositionSeconds = mediaCurrentTime(media);
    const restoreDurationSeconds = mediaDuration(media, video.item);
    try {
      const item = await fetchJSON(`/api/videos/${encodeURIComponent(id)}`, { cache: 'no-store' });
      if (requestToken !== mediaURLRefreshRequestToken || video.item?.id !== id || video.item?.media_url !== staleMediaURL) return;
      if (!item?.media_url || item.media_url === staleMediaURL) {
        resumePausedRefreshMedia(media, staleMediaURL, options);
        return;
      }
      const nextItem = { ...video.item, ...item };
      if (restorePositionSeconds > 0) {
        const position = Math.floor(restorePositionSeconds);
        const duration = restoreDurationSeconds || Number(nextItem.duration_seconds) || 0;
        nextItem.position_seconds = position;
        nextItem.progress = { ...(nextItem.progress ?? {}), position_seconds: position, duration_seconds: Math.floor(duration), watched: !!video.item?.watched };
        playbackSync = { ...playbackSync, videoID: id, initialProgressPending: true, initialProgressPositionSeconds: position, restorePositionSeconds: position };
        userTimestampSeeked = false;
      }
      mediaURLRefreshRetry = { videoID: '', mediaURL: '' };
      mediaURLRefreshSuppressedPlay = { videoID: '', mediaURL: '' };
      suppressNextLoadedMetadataAutoplay = !options.autoplay;
      stagePlaybackRateRestore(media);
      video = { ...video, item: nextItem };
      if (options.autoplay) autoplayOnLoad = true;
    } catch {
      resumePausedRefreshMedia(media, staleMediaURL, options);
      // If the refresh fails, leave the existing player state intact; normal error UI can surface the failure.
    }
  }

  function resumePausedRefreshMedia(media, staleMediaURL, options) {
    if (!options.pausedForRefresh || signedMediaURLNeedsRefresh(staleMediaURL, 0)) return;
    mediaURLRefreshSuppressedPlay = { videoID: video.item?.id ?? '', mediaURL: staleMediaURL };
    attemptAutoplay(media);
  }

  function stagePlaybackRateRestore(media) {
    const rate = normalizePlaybackRate(media?.playbackRate) || savedPlaybackRate();
    if (!rate) return;
    pendingPlaybackRateRestore = rate;
    suppressPlaybackRateSave = true;
  }

  function consumeSuppressedMediaURLRefreshPlay(item) {
    if (!item?.id || mediaURLRefreshSuppressedPlay.videoID !== item.id || mediaURLRefreshSuppressedPlay.mediaURL !== item.media_url) return false;
    mediaURLRefreshSuppressedPlay = { videoID: '', mediaURL: '' };

    return true;
  }

  function signedMediaURLNeedsRefresh(rawURL, leadMS) {
    const expiresAt = signedMediaURLExpiresAt(rawURL);
    return expiresAt > 0 && Date.now() + Math.max(0, leadMS) >= expiresAt;
  }

  function signedMediaURLExpiresAt(rawURL) {
    try {
      const expires = Number(new URL(rawURL, window.location.origin).searchParams.get('expires'));
      return Number.isFinite(expires) && expires > 0 ? expires * 1000 : 0;
    } catch {
      return 0;
    }
  }

  function mediaCurrentTime(media) {
    const position = Number(media?.currentTime);
    return Number.isFinite(position) && position > 0 ? position : 0;
  }

  function mediaDuration(media, item) {
    const duration = Number(media?.duration) || Number(item?.duration_seconds) || Number(item?.progress?.duration_seconds) || 0;
    return Number.isFinite(duration) && duration > 0 ? duration : 0;
  }

  function showPlaybackFeedback(type) {
    if (type !== 'play' && type !== 'pause') return;
    if (playbackFeedbackTimer) clearTimeout(playbackFeedbackTimer);
    playbackFeedback = { id: playbackFeedback.id + 1, type };
    playbackFeedbackTimer = setTimeout(clearPlaybackFeedback, 1000);
  }

  function clearPlaybackFeedback() {
    if (playbackFeedbackTimer) clearTimeout(playbackFeedbackTimer);
    playbackFeedbackTimer = undefined;
    playbackFeedback = { id: playbackFeedback.id, type: '' };
  }

  function playbackFeedbackLabel(type) {
    return type === 'pause' ? 'Playback paused' : 'Playback resumed';
  }

  function handlePlaybackProgress(event, options = {}) {
    const media = event.currentTarget;
    const id = video.item?.id;
    if (!id || !media) return;
    if (markPlayedAction.status === 'loading' || (markPlayedAction.status === 'succeeded' && videoIsWatched(video.item))) return;
    if (skipSponsorSegment(media)) return;
    if (deferInitialProgressReset(media, id)) return;
    const payload = playbackProgressPayload(media);
    const key = playbackProgressKey(id, payload);
    const force = options.force === true;
    const now = Date.now();
    if (!force && now - playbackSync.lastSentAt < playbackProgressSyncInterval) return;
    if (!force && (key === playbackSync.lastKey || key === playbackSync.inFlightKey)) return;

    const request = { id, payload, key, mutationToken: playbackProgressMutationToken };
    if (playbackSync.pending && playbackSync.videoID === id) {
      playbackSync = { ...playbackSync, queued: request };
      return;
    }
    void syncPlaybackProgress(request);
  }

  function skipSponsorSegment(media) {
    const segment = sponsorSegmentForTime(Number(media?.currentTime), video.item?.sponsor_segments);
    if (!segment) return false;
    const duration = Number(media.duration);
    const target = Number.isFinite(duration) && duration > 0 ? Math.min(segment.end_seconds, duration) : segment.end_seconds;
    const current = Number(media.currentTime) || 0;
    if (!Number.isFinite(target) || target <= current + 0.05) return false;
    media.currentTime = target;
    return true;
  }

  function sponsorSegmentForTime(currentTime, segments) {
    if (!Number.isFinite(currentTime) || !Array.isArray(segments)) return null;
    for (const segment of segments) {
      const start = Number(segment?.start_seconds);
      const end = Number(segment?.end_seconds);
      if (Number.isFinite(start) && Number.isFinite(end) && start >= 0 && end > start && currentTime >= start && currentTime < end - 0.05) {
        return { start_seconds: start, end_seconds: end };
      }
    }
    return null;
  }

  function initialPlaybackSync(id = '', item = null) {
    const initialProgressPositionSeconds = savedPlaybackPosition(item);
    const restorePositionSeconds = playbackRestorePosition(item);
    return { videoID: id, lastSentAt: 0, pending: false, inFlightKey: '', lastKey: '', queued: null, initialProgressPending: initialProgressPositionSeconds > 0, initialProgressPositionSeconds, restorePositionSeconds };
  }

  function savedPlaybackPosition(item) {
    const position = Math.floor(Number(item?.position_seconds) || 0);
    return position > 0 ? position : 0;
  }

  function playbackRestorePosition(item, media = null) {
    const position = savedPlaybackPosition(item);
    const duration = Math.floor(Number(media?.duration) || Number(item?.duration_seconds) || 0);
    if (position <= 0 || duration <= 0 || position >= duration - 3) return 0;
    return position;
  }

  function deferInitialProgressReset(media, id) {
    if (!playbackSync.initialProgressPending || playbackSync.videoID !== id) return false;
    const position = playbackSync.initialProgressPositionSeconds;
    const current = Number(media.currentTime) || 0;
    if (playbackSync.restorePositionSeconds > 0 && current < position - 2) return true;
    if (Math.floor(current) <= 0) return true;
    clearInitialProgressGuard(id);
    return false;
  }

  function clearInitialProgressGuard(id) {
    if (!playbackSync.initialProgressPending || playbackSync.videoID !== id) return;
    playbackSync = { ...playbackSync, initialProgressPending: false, initialProgressPositionSeconds: 0, restorePositionSeconds: 0 };
  }

  function playbackProgressPayload(media) {
    const position = Math.max(0, Math.floor(Number(media.currentTime) || 0));
    const duration = Math.max(0, Math.floor(Number(media.duration) || Number(video.item?.duration_seconds) || 0));
    return {
      position_seconds: position,
      duration_seconds: duration,
      watched: !!media.ended || nearPlaybackCompletion(position, duration),
    };
  }

  function playbackProgressKey(id, payload) {
    return `${id}:${payload.position_seconds}:${payload.duration_seconds}:${payload.watched}`;
  }

  async function syncPlaybackProgress(request) {
    const { id, payload, key, mutationToken } = request;
    playbackSync = { ...playbackSync, videoID: id, lastSentAt: Date.now(), pending: true, inFlightKey: key, queued: null };
    let saved = false;
    try {
      const progress = await postJSON(`/api/videos/${encodeURIComponent(id)}/progress`, payload, 'PUT');
      saved = true;
      if (mutationToken === playbackProgressMutationToken) {
        if (video.item?.id === id) updatePlaybackProgressState(id, progress);
      }
    } catch {
      // Progress sync is opportunistic; playback should not fail because persistence did.
    } finally {
      if (mutationToken !== playbackProgressMutationToken) return;
      if (playbackSync.videoID !== id || playbackSync.inFlightKey !== key) return;
      const queued = playbackSync.queued;
      playbackSync = { ...playbackSync, pending: false, inFlightKey: '', lastKey: saved ? key : playbackSync.lastKey, queued: null };
      if (queued?.id === id && (!saved || queued.key !== key)) void syncPlaybackProgress(queued);
    }
  }

  function nearPlaybackCompletion(position, duration) {
    if (duration <= 0 || position <= 0) return false;
    let threshold = Math.floor(duration * 0.9);
    if (duration > 30 && duration - 30 > threshold) threshold = duration - 30;
    return position >= Math.max(1, threshold);
  }

  function updatePlaybackProgressState(id, progress) {
    const nextProgress = {
      position_seconds: progress.position_seconds ?? 0,
      duration_seconds: progress.duration_seconds ?? video.item?.duration_seconds ?? 0,
      watched: !!progress.watched,
    };
    if (video.item?.id === id) {
      video = { ...video, item: { ...video.item, position_seconds: nextProgress.position_seconds, duration_seconds: nextProgress.duration_seconds || video.item.duration_seconds, watched: nextProgress.watched, progress: { ...(video.item.progress ?? {}), ...nextProgress } } };
    }
    invalidatePlaybackProgressLists(id);
  }

  function invalidatePlaybackProgressLists(id) {
    playbackProgressInvalidation = { videoID: id, version: playbackProgressInvalidation.version + 1 };
    if (path === '/' && library.status === 'loaded' && !library.refreshing && !library.loadingMore) {
      loadLibrary({ showLoading: false });
    }
    if (channelID && channelPage.status === 'loaded') {
      loadChannel(channelID, { showLoading: false });
    }
    if (playlistID && playlistPage.status === 'loaded') {
      loadPlaylist(playlistID, { showLoading: false });
    }
  }

  function navigateToNextVideo(target) {
    autoplayOnLoad = true;
    if (typeof window !== 'undefined') window.__kapselUpNextAutoplay = true;
    setRoute(videoHref(target.id));
  }

  function startUpNext() {
    cancelUpNext();
    const next = upNextVideo;
    if (!next) return;
    upNextTarget = next;
    upNextCountdown = 5;
    upNextTimer = setInterval(() => {
      upNextCountdown -= 1;
      if (upNextCountdown <= 0) {
        const target = upNextTarget;
        cancelUpNext();
        if (target) navigateToNextVideo(target);
      }
    }, 1000);
  }

  function cancelUpNext() {
    if (upNextTimer) clearInterval(upNextTimer);
    upNextTimer = null;
    upNextCountdown = 0;
    upNextTarget = null;
  }

  function playUpNextNow() {
    const target = upNextTarget || upNextVideo;
    cancelUpNext();
    if (target) navigateToNextVideo(target);
  }

  function toggleCinemaMode() {
    setCinemaMode(!cinemaMode);
  }

  function setCinemaMode(value) {
    cinemaMode = !!value;
    try {
      window.localStorage.setItem(cinemaModeStorageKey, cinemaMode ? '1' : '0');
    } catch {
      // Storage can be unavailable in private or locked-down browsing contexts.
    }
  }

  function savedCinemaMode() {
    try {
      return window.localStorage.getItem(cinemaModeStorageKey) === '1';
    } catch {
      return false;
    }
  }

  function toggleVolumeNormalization() {
    setVolumeNormalization(!volumeNormalization);
    resumeAudioNormalization();
  }

  function setVolumeNormalization(value) {
    volumeNormalization = !!value;
    try {
      window.localStorage.setItem(volumeNormalizationStorageKey, volumeNormalization ? '1' : '0');
    } catch {
      // Storage can be unavailable in private or locked-down browsing contexts.
    }
  }

  function savedVolumeNormalization() {
    try {
      return window.localStorage.getItem(volumeNormalizationStorageKey) === '1';
    } catch {
      return false;
    }
  }

  function syncAudioNormalization(media, enabled) {
    if (audioNormalizationGraph && audioNormalizationGraph.media !== media) destroyAudioNormalizationGraph();
    if (!media) return;
    if (!enabled) {
      setAudioNormalizationRoute(false);
      return;
    }

    if (!ensureAudioNormalizationGraph(media)) return;
    setAudioNormalizationRoute(true);
    if (!media.paused) resumeAudioNormalization();
  }

  function ensureAudioNormalizationGraph(media) {
    if (audioNormalizationGraph?.media === media) return audioNormalizationGraph;
    const AudioContextClass = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextClass) {
      setVolumeNormalization(false);
      return null;
    }

    let context = null;
    let source = null;
    let compressor = null;
    let gain = null;
    try {
      context = new AudioContextClass();
      compressor = context.createDynamicsCompressor();
      compressor.threshold.value = -26;
      compressor.knee.value = 24;
      compressor.ratio.value = 4;
      compressor.attack.value = 0.003;
      compressor.release.value = 0.25;
      gain = context.createGain();
      gain.gain.value = 1.15;
      source = context.createMediaElementSource(media);
      audioNormalizationGraph = { media, context, source, compressor, gain, route: '' };
      return audioNormalizationGraph;
    } catch {
      disconnectAudioNode(source);
      disconnectAudioNode(compressor);
      disconnectAudioNode(gain);
      if (typeof context?.close === 'function') void context.close().catch(() => {});
      destroyAudioNormalizationGraph();
      setVolumeNormalization(false);
      return null;
    }
  }

  function setAudioNormalizationRoute(enabled) {
    const graph = audioNormalizationGraph;
    if (!graph || graph.route === (enabled ? 'normalized' : 'direct')) return;
    disconnectAudioNode(graph.source);
    disconnectAudioNode(graph.compressor);
    disconnectAudioNode(graph.gain);
    if (enabled) {
      graph.source.connect(graph.compressor);
      graph.compressor.connect(graph.gain);
      graph.gain.connect(graph.context.destination);
      graph.route = 'normalized';
    } else {
      graph.source.connect(graph.context.destination);
      graph.route = 'direct';
    }
  }

  function resumeAudioNormalization() {
    const context = audioNormalizationGraph?.context;
    if (!context || context.state !== 'suspended' || typeof context.resume !== 'function') return;
    void context.resume().catch(() => {});
  }

  function destroyAudioNormalizationGraph() {
    const graph = audioNormalizationGraph;
    audioNormalizationGraph = null;
    if (!graph) return;
    disconnectAudioNode(graph.source);
    disconnectAudioNode(graph.compressor);
    disconnectAudioNode(graph.gain);
    if (typeof graph.context.close === 'function') void graph.context.close().catch(() => {});
  }

  function disconnectAudioNode(node) {
    try {
      node?.disconnect();
    } catch {
      // Nodes can already be disconnected while routes are being rebuilt.
    }
  }

  function libraryInfiniteScroll(node, enabled) {
    let observer = null;

    function disconnect() {
      if (observer) observer.disconnect();
      observer = null;
    }

    function update(nextEnabled) {
      enabled = nextEnabled;
      disconnect();
      if (!enabled || typeof IntersectionObserver === 'undefined') return;
      observer = new IntersectionObserver(entries => {
        if (entries.some(entry => entry.isIntersecting)) loadNextLibraryPage();
      }, { rootMargin: '600px 0px' });
      observer.observe(node);
    }

    update(enabled);

    return { update, destroy: disconnect };
  }

  function cinemaModeControl(node, active) {
    let button = null;
    let tooltip = null;
    let frame = 0;
    let attempts = 0;

    function sync(nextActive) {
      active = nextActive;
      if (!button) return;
      const label = active ? 'Exit cinema mode' : 'Enter cinema mode';
      button.innerHTML = cinemaModeIcon(active);
      button.setAttribute('aria-label', label);
      button.setAttribute('aria-pressed', active ? 'true' : 'false');
      if (tooltip) tooltip.textContent = label;
    }

    function install() {
      const root = node.shadowRoot;
      const fullscreenButton = root?.querySelector('media-fullscreen-button');
      const buttonGroup = fullscreenButton?.parentElement || root?.querySelector('.media-button-group:last-of-type');
      if (!buttonGroup) {
        if (attempts >= 60) return;
        attempts += 1;
        frame = requestAnimationFrame(install);
        return;
      }
      if (!button) {
        button = document.createElement('button');
        button.type = 'button';
        button.className = 'media-button media-button--subtle media-button--icon';
        button.setAttribute('commandfor', 'cinema-mode-tooltip');
        button.addEventListener('click', toggleCinemaMode);
        buttonGroup.insertBefore(button, fullscreenButton || null);
        tooltip = document.createElement('media-tooltip');
        tooltip.id = 'cinema-mode-tooltip';
        tooltip.setAttribute('side', 'top');
        tooltip.className = 'media-surface media-tooltip';
        buttonGroup.insertBefore(tooltip, fullscreenButton || null);
      }
      sync(active);
    }

    install();

    return {
      update: sync,
      destroy() {
        if (frame) cancelAnimationFrame(frame);
        button?.removeEventListener('click', toggleCinemaMode);
        button?.remove();
        tooltip?.remove();
      },
    };
  }

  function cinemaModeIcon(active) {
    const path = active
      ? '<path d="M160-160q-33 0-56.5-23.5T80-240v-480q0-33 23.5-56.5T160-800l240 240H160v320h560l80 80H160Zm711-44-71-71v-285H514L274-800h66l67 133 27 27h106l-80-160h100l80 160h120l-80-160h120q33 0 56.5 23.5T880-720v480q0 10-2 19t-7 17ZM791-55 55-791l57-57 736 736-57 57ZM446-400Zm211-18Z" />'
      : '<path d="m160-800 80 160h120l-80-160h80l80 160h120l-80-160h80l80 160h120l-80-160h120q33 0 56.5 23.5T880-720v480q0 33-23.5 56.5T800-160H160q-33 0-56.5-23.5T80-240v-480q0-33 23.5-56.5T160-800Zm0 240v320h640v-320H160Zm0 0v320-320Z" />';
    return `<svg aria-hidden="true" focusable="false" viewBox="0 -960 960 960" width="20" height="20" fill="currentColor">${path}</svg>`;
  }

  function volumeNormalizationControl(node, active) {
    let button = null;
    let tooltip = null;
    let frame = 0;
    let attempts = 0;

    function sync(nextActive) {
      active = nextActive;
      if (!button) return;
      const label = active ? 'Disable volume normalization' : 'Enable volume normalization';
      button.innerHTML = volumeNormalizationIcon(active);
      button.setAttribute('aria-label', label);
      button.setAttribute('aria-pressed', active ? 'true' : 'false');
      if (tooltip) tooltip.textContent = label;
    }

    function install() {
      const root = node.shadowRoot;
      const fullscreenButton = root?.querySelector('media-fullscreen-button');
      const buttonGroup = fullscreenButton?.parentElement || root?.querySelector('.media-button-group:last-of-type');
      if (!buttonGroup) {
        if (attempts >= 60) return;
        attempts += 1;
        frame = requestAnimationFrame(install);
        return;
      }
      if (!button) {
        button = document.createElement('button');
        button.type = 'button';
        button.className = 'media-button media-button--subtle media-button--icon';
        button.setAttribute('commandfor', 'volume-normalization-tooltip');
        button.addEventListener('click', toggleVolumeNormalization);
        buttonGroup.insertBefore(button, fullscreenButton || null);
        tooltip = document.createElement('media-tooltip');
        tooltip.id = 'volume-normalization-tooltip';
        tooltip.setAttribute('side', 'top');
        tooltip.className = 'media-surface media-tooltip';
        buttonGroup.insertBefore(tooltip, fullscreenButton || null);
      }
      sync(active);
    }

    install();

    return {
      update: sync,
      destroy() {
        if (frame) cancelAnimationFrame(frame);
        button?.removeEventListener('click', toggleVolumeNormalization);
        button?.remove();
        tooltip?.remove();
      },
    };
  }

  function volumeNormalizationIcon(active) {
    const path = active
      ? '<path d="M240-80q62 0 101.5-31t60.5-91q17-50 32.5-70t71.5-64q62-50 98-113t36-151q0-119-80.5-199.5T360-880q-119 0-199.5 80.5T80-600h80q0-85 57.5-142.5T360-800q85 0 142.5 57.5T560-600q0 68-27 116t-77 86q-52 38-81 74t-43 78q-14 44-33.5 65T240-160q-33 0-56.5-23.5T160-240H80q0 66 47 113t113 47Zm191-449.5q29-29.5 29-70.5 0-42-29-71t-71-29q-42 0-71 29t-29 71q0 41 29 70.5t71 29.5q42 0 71-29.5ZM740-379l-59-59q19-37 29-77.5t10-84.5q0-44-10-84t-29-77l59-59q29 49 44.5 104.5T800-600q0 61-15.5 116.5T740-379Zm117 116-59-58q39-60 60.5-130T880-598q0-78-22-148.5T797-877l60-60q49 72 76 157.5T960-600q0 94-27 179.5T857-263Z" />'
      : '<path d="M240-80q62 0 101.5-31t60.5-91q17-50 32.5-70t71.5-64q62-50 98-113t36-151q0-119-80.5-199.5T360-880q-119 0-199.5 80.5T80-600h80q0-85 57.5-142.5T360-800q85 0 142.5 57.5T560-600q0 68-27 116t-77 86q-52 38-81 74t-43 78q-14 44-33.5 65T240-160q-33 0-56.5-23.5T160-240H80q0 66 47 113t113 47Zm191-449.5q29-29.5 29-70.5 0-42-29-71t-71-29q-42 0-71 29t-29 71q0 41 29 70.5t71 29.5q42 0 71-29.5ZM740-379l-59-59q19-37 29-77.5t10-84.5q0-44-10-84t-29-77l59-59q29 49 44.5 104.5T800-600q0 61-15.5 116.5T740-379Zm117 116-59-58q39-60 60.5-130T880-598q0-78-22-148.5T797-877l60-60q49 72 76 157.5T960-600q0 94-27 179.5T857-263ZM819-28 27-820l57-57 792 792-57 57Z" />';
    return `<svg aria-hidden="true" focusable="false" viewBox="0 -960 960 960" width="20" height="20" fill="currentColor">${path}</svg>`;
  }

  function captionButtonVisibility(node, hasSubtitles) {
    let frame = 0;
    let attempts = 0;
    let button = null;

    function sync(nextHasSubtitles) {
      hasSubtitles = !!nextHasSubtitles;
      button = node.shadowRoot?.querySelector('media-captions-button') || button;
      if (!button) {
        if (attempts >= 60) return;
        attempts += 1;
        frame = requestAnimationFrame(() => sync(hasSubtitles));
        return;
      }

      button.hidden = !hasSubtitles;
      if (hasSubtitles) button.removeAttribute('aria-hidden');
      else button.setAttribute('aria-hidden', 'true');
      if ('inert' in button) button.inert = !hasSubtitles;
    }

    sync(hasSubtitles);

    return {
      update: sync,
      destroy() {
        if (frame) cancelAnimationFrame(frame);
        if (!button) return;
        button.hidden = false;
        button.removeAttribute('aria-hidden');
        if ('inert' in button) button.inert = false;
      },
    };
  }

  function captionTrackController(node) {
    const controller = new AbortController();
    const boundTracks = new WeakSet();
    let applying = false;
    let frame = 0;

    function captionTracks() {
      return Array.from(node?.textTracks ?? []).filter(track => track.kind === 'captions' || track.kind === 'subtitles');
    }

    function rememberCaptionsEnabled(enabled) {
      try {
        window.localStorage.setItem(captionModeStorageKey, enabled ? '1' : '0');
      } catch {
        // Storage can be unavailable in private or locked-down browsing contexts.
      }
    }

    function savedCaptionsEnabled() {
      try {
        return window.localStorage.getItem(captionModeStorageKey) === '1';
      } catch {
        return false;
      }
    }

    function normalizeCueBox(cue) {
      for (const [key, value] of [['align', 'center'], ['position', 50], ['positionAlign', 'center'], ['size', 100], ['line', 'auto']]) {
        try {
          if (key in cue) cue[key] = value;
        } catch {
          // Some browser cue properties are read-only depending on cue type.
        }
      }
    }

    function normalizeCaptionCues(track) {
      for (const cue of Array.from(track?.cues ?? [])) normalizeCueBox(cue);
    }

    function bindCaptionTrack(track) {
      if (!track || boundTracks.has(track)) return;
      boundTracks.add(track);
      track.addEventListener?.('cuechange', () => normalizeCaptionCues(track), { signal: controller.signal });
    }

    function bindCaptionTracks() {
      for (const track of captionTracks()) bindCaptionTrack(track);
      for (const trackElement of node.querySelectorAll('track[kind="captions"], track[kind="subtitles"]')) {
        trackElement.addEventListener('load', syncCaptionState, { signal: controller.signal });
      }
    }

    function setCaptionModes(enabled, preferredTrack = null) {
      const tracks = captionTracks();
      if (!tracks.length) return;
      const selected = enabled ? (tracks.includes(preferredTrack) ? preferredTrack : tracks.find(track => track.mode === 'showing') || tracks[0]) : null;
      applying = true;
      for (const track of tracks) {
        track.mode = selected && track === selected ? 'showing' : 'disabled';
        if (selected && track === selected) normalizeCaptionCues(track);
      }
      applying = false;
    }

    function applySavedCaptionState() {
      bindCaptionTracks();
      setCaptionModes(savedCaptionsEnabled());
    }

    function syncCaptionState() {
      if (applying) return;
      bindCaptionTracks();
      const tracks = captionTracks();
      if (!tracks.length) return;
      const showing = tracks.filter(track => track.mode === 'showing');
      if (showing.length > 1) {
        setCaptionModes(true, showing[0]);
        rememberCaptionsEnabled(true);
        return;
      }
      if (showing.length === 1) {
        normalizeCaptionCues(showing[0]);
        rememberCaptionsEnabled(true);
        return;
      }
      rememberCaptionsEnabled(false);
    }

    bindCaptionTracks();
    frame = requestAnimationFrame(applySavedCaptionState);
    node.textTracks?.addEventListener?.('addtrack', applySavedCaptionState, { signal: controller.signal });
    node.textTracks?.addEventListener?.('change', syncCaptionState, { signal: controller.signal });

    return {
      destroy() {
        if (frame) cancelAnimationFrame(frame);
        controller.abort();
      },
    };
  }

  function loadHiddenTextTrack(node) {
    if (node?.track) node.track.mode = 'hidden';
  }

  function channelHandle(channel) {
    if (!channel) return '@archive';
    if (channel.handle) return channel.handle;
    return `@${(channel.name || channel.id || 'archive').toLowerCase().replaceAll(' ', '')}`;
  }

  function formatVideoCount(count) {
    const value = Number(count) || 0;
    return `${value} ${value === 1 ? 'video' : 'videos'}`;
  }

  function formatBytes(bytes) {
    let value = Number(bytes) || 0;
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let unitIndex = 0;
    while (value >= 1024 && unitIndex < units.length - 1) {
      value /= 1024;
      unitIndex += 1;
    }

    const digits = unitIndex === 0 || value >= 10 ? 0 : 1;
    return `${value.toFixed(digits)} ${units[unitIndex]}`;
  }

  function settingsRows(readiness) {
    const config = readiness?.configuration;
    if (!config) return [];
    return [
      { label: 'Address', value: config.addr || 'Default listener' },
      { label: 'Data directory', value: config.data_dir || 'Not configured' },
      { label: 'Database path', value: config.db_path || 'Not configured' },
      { label: 'Media root', value: config.media_root || 'Not configured' },
      { label: 'TubeArchivist import root', value: config.import_root || 'API imports disabled' },
      { label: 'yt-dlp path', value: config.yt_dlp_path || 'yt-dlp' },
      { label: 'Media signing', value: config.media_signing_configured ? 'Stable secret configured' : 'Generated per process' },
      { label: 'Authentication', value: config.auth_mode === 'disabled' ? 'Disabled for development' : (config.authentication_configured ? 'Configured' : 'Not configured') },
      { label: 'Session secret', value: config.session_secret_configured ? 'Stable secret configured' : 'Generated per process' },
      { label: 'Minimum free space', value: formatBytes(config.min_free_space_bytes) },
      { label: 'Timeline previews', value: config.previews_enabled ? `Enabled with ${config.ffmpeg_path || 'no ffmpeg path'}` : 'Disabled' },
      { label: 'SponsorBlock', value: config.sponsorblock_enabled ? 'Enabled' : 'Disabled' },
    ];
  }

  function storageMaintenanceRows(summary) {
    return (summary?.usage ?? []).map(item => ({
      label: storageCategoryLabel(item.category),
      value: `${formatBytes(item.bytes)} across ${item.files || 0} ${item.files === 1 ? 'file' : 'files'}`,
    }));
  }

  function storageCategoryLabel(category) {
    if (category === 'media') return 'Media files';
    if (category === 'thumbnail') return 'Thumbnails';
    if (category === 'subtitle') return 'Subtitles';
    if (category === 'derived') return 'Derived assets';
    if (category === 'database') return 'Database';
    return category || 'Storage';
  }

  function checkStateLabel(state) {
    if (state === 'pass') return 'Pass';
    if (state === 'warn') return 'Warn';
    if (state === 'error') return 'Error';
    return state || 'Unknown';
  }

</script>

<svelte:head>
  <title>Kapsel | {pageTitle}</title>
</svelte:head>

<main class="app-shell" class:watch-mode={videoID}>
  <header class="topbar">
    <div class="brand-cluster">
      <a class="brand" href="/" onclick={event => navigate(event, '/')}> <span class="brand-mark" aria-hidden="true"></span><span>Kapsel</span></a>
    </div>

    <form class="search-form" role="search" onsubmit={submitSearch}>
      <label class="visually-hidden" for="site-search">Search archive</label>
      <input id="site-search" bind:value={searchInput} type="search" placeholder="Search" />
      <button type="submit">Search</button>
    </form>

    <div class="top-actions">
      <a class="top-action-link" class:active={path === '/downloads'} href="/downloads" aria-label="Open queue" aria-current={path === '/downloads' ? 'page' : undefined} title="Open queue" onclick={event => navigate(event, '/downloads')}>
        <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24">
          <path d="M4 7h10" />
          <path d="M4 12h10" />
          <path d="M4 17h6" />
          <path d="M17 14v5" />
          <path d="m14.5 16.5 2.5 2.5 2.5-2.5" />
        </svg>
      </a>
      <a class="top-action-link" class:active={path === '/settings'} href="/settings" aria-label="Settings" aria-current={path === '/settings' ? 'page' : undefined} title="Settings" onclick={event => navigate(event, '/settings')}>
        <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24">
          <path d="M4 7h9" />
          <path d="M17 7h3" />
          <circle cx="15" cy="7" r="2" />
          <path d="M4 12h3" />
          <path d="M11 12h9" />
          <circle cx="9" cy="12" r="2" />
          <path d="M4 17h11" />
          <path d="M19 17h1" />
          <circle cx="17" cy="17" r="2" />
        </svg>
      </a>
      {#if session.status === 'loaded' && session.auth_enabled && session.authenticated}
        <span class="session-user">{session.username || 'Signed in'}</span>
        <button class="sign-in" type="button" onclick={logout}>Log out</button>
      {/if}
    </div>
  </header>

  <aside class="sidebar" aria-label="Primary navigation">
    <nav class="side-nav">
      {#each sidebarPrimary as item}
        <a class:active={path === item.href || (item.href === '/' && path === '/')} href={item.href} aria-label={item.label} onclick={event => navigate(event, item.href)}>
          <span class="side-icon" aria-hidden="true">{item.label.slice(0, 1)}</span>
          <span>{item.label}</span>
        </a>
      {/each}
    </nav>

    <section class="side-section" aria-labelledby="explore-title">
      <h2 id="explore-title">Explore</h2>
      {#each sidebarExplore as label}
        <a href="/search?q={encodeURIComponent(label)}" aria-label={label} onclick={event => navigate(event, `/search?q=${encodeURIComponent(label)}`)}>
          <span class="side-icon" aria-hidden="true">{label.slice(0, 1)}</span>
          <span>{label}</span>
        </a>
      {/each}
    </section>
  </aside>

  <section class="content-frame">
    {#if session.status === 'idle' || session.status === 'loading'}
      <div class="state">Checking your local session...</div>
    {:else if session.status === 'error'}
      <div class="state state-error"><strong>Could not check the local session.</strong><span>{session.error}</span></div>
    {:else if session.auth_enabled && !session.authenticated}
      <section class="login-page" aria-label="Login">
        <p>Local archive</p>
        <h1>Sign in to Kapsel</h1>
        {#if !session.configured}
          <div class="state state-error"><strong>No local account is configured.</strong><span>Set KAPSEL_AUTH_USERNAME, KAPSEL_AUTH_PASSWORD_HASH, and KAPSEL_SESSION_SECRET, or explicitly use KAPSEL_AUTH_MODE=disabled for development.</span></div>
        {:else}
          <span>Use the local account configured for this Kapsel node.</span>
          <form class="login-card" onsubmit={submitLogin}>
            <label for="login-username">Username</label>
            <input id="login-username" bind:value={loginUsername} autocomplete="username" required />
            <label for="login-password">Password</label>
            <input id="login-password" bind:value={loginPassword} type="password" autocomplete="current-password" required />
            <button type="submit" disabled={loginState.status === 'loading'}>{loginState.status === 'loading' ? 'Signing in...' : 'Sign in'}</button>
            {#if loginState.error}<div class="state state-error compact" role="alert">{loginState.error}</div>{/if}
          </form>
        {/if}
      </section>
    {:else if path === '/'}
      <section class="home-feed" aria-label="Home feed" data-testid="library-route">
        {#if showHomeAddChannel}
          <form class="channel-dock" data-testid="channel-form" onsubmit={event => { event.preventDefault(); addChannel(); }}>
            <div>
              <strong>Add a channel</strong>
              <span>Paste a YouTube channel URL. Kapsel syncs the latest catalog and imports the first video.</span>
            </div>
            <label class="visually-hidden" for="channel-url">Channel URL</label>
            <input id="channel-url" bind:value={channelURL} type="url" placeholder="https://www.youtube.com/@channel" required data-testid="channel-url-input" />
            <button type="submit" disabled={channelSubmitDisabled} data-testid="channel-submit">
              {#if channelJob.status === 'loading'}Queueing{:else if channelJob.status === 'queued' || channelJob.status === 'running'}Active{:else}Add{/if}
            </button>
            {#if channelJob.status !== 'idle'}
              <div role="status" aria-live="polite" class:state-error={channelJob.status === 'error' || channelJob.status === 'failed'} class="job-state" data-testid="channel-job-status">
                {#if channelJob.status === 'queued'}Channel job queued.{:else if channelJob.status === 'running'}Syncing channel catalog...{:else if channelJob.status === 'succeeded'}Channel catalog synced and first video queued.{:else if channelJob.status === 'failed'}Channel job failed: {channelJob.error || 'unknown error'}{:else if channelJob.status === 'error'}Could not add channel: {channelJob.error}{:else}Channel job status: {channelJob.status}{/if}
              </div>
            {/if}
          </form>
        {/if}

        <VideoSortToolbar sort={librarySort} options={videoSortOptions} controlId="library-sort" labelledBy="Library sort options" summary={libraryFeedSummary} onSortChange={setVideoSort} />

        {#if library.status === 'loading'}
          <div class="state" data-testid="library-loading">Loading your archive...</div>
        {:else if library.status === 'error'}
          <div class="state state-error" data-testid="library-error"><strong>Could not load the library.</strong><span>{library.error}</span></div>
        {:else if library.videos.length === 0}
          <div class="state" data-testid="library-empty"><strong>{libraryEmptyState.title}</strong><span>{libraryEmptyState.body}</span></div>
        {:else}
          <div class="video-grid" data-testid="library-videos">
            {#each library.videos as item (item.id)}
              <VideoCard {item} {navigate} />
            {/each}
          </div>
          {#if libraryHasMore || library.refreshing || library.loadingMore || library.loadMoreError}
            <div class="library-load-more">
              {#if library.refreshing}
                <span role="status" aria-live="polite" data-testid="library-load-more-status">Refreshing videos...</span>
              {:else if library.loadingMore}
                <span role="status" aria-live="polite" data-testid="library-load-more-status">Loading more videos...</span>
              {:else if library.loadMoreError}
                <span role="alert" class="state-error" data-testid="library-load-more-status">Could not load more videos: {library.loadMoreError}</span>
              {:else}
                <span data-testid="library-load-more-status">Showing {library.videos.length} of {library.pagination.total} videos.</span>
              {/if}
              {#if libraryHasMore}
                <button type="button" onclick={loadNextLibraryPage} disabled={library.refreshing || library.loadingMore} data-testid="library-load-more">{library.refreshing ? 'Refreshing...' : library.loadingMore ? 'Loading more...' : library.loadMoreError ? 'Retry' : 'Load more'}</button>
                {#if !library.loadMoreError}
                  <div class="library-load-more-sentinel" aria-hidden="true" use:libraryInfiniteScroll={libraryHasMore && !library.refreshing && !library.loadingMore} data-testid="library-load-more-sentinel"></div>
                {/if}
              {/if}
            </div>
          {/if}
        {/if}
      </section>
    {:else if videoID}
      <WatchRoute {activeCinemaMode} {video} bind:watchMediaElement {cinemaMode} {cinemaModeControl} {volumeNormalization} {volumeNormalizationControl} {captionButtonVisibility} {captionTrackController} {loadHiddenTextTrack} {videoPosterURL} {restoreAndAutoplay} {handlePlaybackPlay} {handlePlaybackError} {rememberPlaybackRate} {skipSponsorSegment} {handlePlaybackProgress} {handlePlaybackPause} {startUpNext} {playbackFeedback} {playbackFeedbackLabel} {catalogVideoJobs} {jobProgressPercent} {upNextCountdown} {upNextTarget} {cancelUpNext} {playUpNextNow} {navigate} {channelHandle} {videoIsWatched} {markVideoPlayed} {markPlayedAction} {deleteVideoMedia} {deleteVideoMediaAction} {toggleKeepForever} {keepForeverAction} {canDownloadCatalogVideo} {isCatalogVideoJobLocked} {downloadCatalogItem} {catalogDownloadButtonLabel} {previewJob} {isChannelJobActive} {seekVideoTimestamp} {commentsPage} commentsLastPage={lastPage(commentsPage.pagination, 20)} {changeCommentsPage} {recommendations} />
    {:else if path === '/channels'}
      <section class="utility-page channel-library" aria-label="Channels">
        <p>Channels</p>
        <h1>Channel library</h1>
        <span>Browse indexed and subscribed channels. Each page is bounded for large local collections.</span>
        {#if channelListPage.status === 'loading'}
          <div class="state compact">Loading channels...</div>
        {:else if channelListPage.status === 'error'}
          <div class="state state-error compact"><strong>Could not load channels.</strong><span>{channelListPage.error}</span></div>
        {:else if channelListPage.channels.length === 0}
          <div class="state compact"><strong>No channels yet.</strong><span>Add a channel or import TubeArchivist metadata to populate this page.</span></div>
        {:else}
          {#if channelDeleteAction.status === 'error'}<div role="alert" class="job-state compact state-error">Could not remove channel: {channelDeleteAction.error}</div>{:else if channelDeleteAction.status === 'succeeded'}<div role="status" aria-live="polite" class="job-state compact">Channel removed.</div>{/if}
          <div class="result-list channel-list">
            {#each channelListPage.channels as channel (channel.id)}
              <div class="channel-row">
                <a href={channelHref(channel)} onclick={event => navigate(event, channelHref(channel))}>
                  <span class:has-thumbnail={!!channel.thumbnail_url} class="result-thumb" style={thumbnailStyle(channel)} aria-hidden="true">{#if channel.thumbnail_url}<img src={channel.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}{channelInitial(channel.name)}{/if}</span>
                  <div class="result-copy">
                    <strong>{channel.name}</strong>
                    <span>{formatVideoCount(channel.video_count)}{channel.subscribed ? ' · Auto-download on' : ''}</span>
                    <p>{channel.description || 'No channel description has been imported yet.'}</p>
                  </div>
                </a>
                <button type="button" class="channel-remove" onclick={() => deleteChannel(channel.id)} disabled={channelDeleteAction.status === 'loading'}>{channelDeleteAction.status === 'loading' ? 'Removing...' : 'Remove channel'}</button>
              </div>
            {/each}
          </div>
          <PaginationControls label="Channel list pagination" page={channelListPage.pagination.page} last={lastPage(channelListPage.pagination)} onPageChange={changeChannelsPage} />
        {/if}
      </section>
    {:else if channelID}
      <section class="channel-page" aria-label="Channel page">
        {#if channelPage.status === 'loading'}
          <div class="state">Loading channel...</div>
        {:else if channelPage.status === 'error'}
          <div class="state state-error"><strong>Could not load this channel.</strong><span>{channelPage.error}</span></div>
        {:else if channelPage.item}
          <header class="channel-hero">
            <div class:has-thumbnail={!!channelPage.item.thumbnail_url} class="channel-avatar" style={thumbnailStyle(channelPage.item)}>{#if channelPage.item.thumbnail_url}<img src={channelPage.item.thumbnail_url} alt="" referrerpolicy="no-referrer" />{:else}{channelInitial(channelPage.item.name)}{/if}</div>
            <div>
              <h1>{channelPage.item.name}</h1>
              <p><strong>{channelHandle(channelPage.item)}</strong> · {formatVideoCount(channelPage.item.video_count)} · {channelPage.item.subscribed ? 'Daily auto-download on' : 'Daily auto-download off'}</p>
              <RichText text={channelPage.item.description || 'No channel description has been imported yet.'} collapsible={true} collapsedMaxHeight="25vh" />
              <div class="channel-actions"><button type="button" onclick={scanCurrentChannel} disabled={scanSubmitDisabled}>{scanJob.status === 'loading' ? 'Starting scan' : isChannelJobActive(scanJob.status) ? 'Scanning' : 'Scan channel'}</button><button type="button" class:active={channelPage.item.subscribed} aria-label="Auto-download" aria-pressed={channelPage.item.subscribed} onclick={toggleChannelAutoDownload} disabled={channelSubscriptionAction.status === 'loading'}>{channelSubscriptionAction.status === 'loading' ? 'Saving auto-download' : channelPage.item.subscribed ? 'Auto-download on' : 'Auto-download off'}</button><button type="button" onclick={() => deleteChannel(channelPage.item.id)} disabled={channelDeleteAction.status === 'loading'}>{channelDeleteAction.status === 'loading' ? 'Removing...' : 'Remove channel'}</button></div>
              {#if scanJob.status !== 'idle'}<div role={scanJob.status === 'error' || scanJob.status === 'failed' ? 'alert' : 'status'} aria-live={scanJob.status === 'error' || scanJob.status === 'failed' ? 'assertive' : 'polite'} class:busy={scanJob.status === 'loading' || scanJob.status === 'queued' || scanJob.status === 'running'} class:done={scanJob.status === 'succeeded'} class:state-error={scanJob.status === 'error' || scanJob.status === 'failed'} class="channel-action-status"><span aria-hidden="true"></span><strong>{#if scanJob.status === 'queued'}Queued{:else if scanJob.status === 'running'}Scanning{:else if scanJob.status === 'succeeded'}Refreshed{:else if scanJob.status === 'failed'}Scan failed{:else if scanJob.status === 'error'}Scan unavailable{:else}Scan status{/if}</strong><small>{#if scanJob.status === 'queued'}Waiting for a worker.{:else if scanJob.status === 'running'}Refreshing the channel catalog.{:else if scanJob.status === 'succeeded'}Channel catalog is up to date.{:else if scanJob.status === 'failed'}{scanJob.error || 'Unknown error'}{:else if scanJob.status === 'error'}{scanJob.error}{:else}{scanJob.status}{/if}</small></div>{/if}
              {#if channelSubscriptionAction.status === 'error'}<div role="alert" class="job-state compact state-error">Could not update auto-download: {channelSubscriptionAction.error}</div>{:else if channelSubscriptionAction.status === 'succeeded'}<div role="status" aria-live="polite" class="job-state compact">Daily auto-download {channelPage.item.subscribed ? 'enabled' : 'disabled'}.</div>{/if}
              {#if channelDeleteAction.status === 'error'}<div role="alert" class="job-state compact state-error">Could not remove channel: {channelDeleteAction.error}</div>{:else if channelDeleteAction.status === 'succeeded'}<div role="status" aria-live="polite" class="job-state compact">Channel removed.</div>{/if}
            </div>
          </header>
          <nav class="channel-tabs" aria-label="Channel sections"><a class="active" href="/channels/{encodeURIComponent(channelID)}" onclick={event => navigate(event, `/channels/${encodeURIComponent(channelID)}`)}>Videos</a><a href="/search?q={encodeURIComponent(channelPage.item.name)}" onclick={event => navigate(event, `/search?q=${encodeURIComponent(channelPage.item.name)}`)}>Search</a></nav>

          <VideoSortToolbar sort={librarySort} options={videoSortOptions} controlId="channel-sort" labelledBy="Channel sort options" onSortChange={setVideoSort} />

          {#if channelPage.videos.length === 0}
            <div class="state">No videos are archived for this channel yet.</div>
          {:else}
            <div class="video-grid channel-grid">
              {#each channelPage.videos as item (item.id)}
                <VideoCard {item} showChannel={false} flat={true} enableCatalogDownload={true} downloadJob={catalogVideoJobs[item.id]} {navigate} onDownload={downloadCatalogItem} downloadDisabled={catalogDownloadDisabled} />
              {/each}
            </div>
            <PaginationControls label="Channel video pagination" page={channelPage.pagination.page} last={lastPage(channelPage.pagination)} onPageChange={changeChannelVideosPage} />
          {/if}
        {/if}
      </section>
    {:else if path === '/playlists'}
      <section class="utility-page" aria-label="Playlists">
        <p>Playlists</p>
        <h1>Playlist library</h1>
        <span>Browse imported playlists and their bounded video pages.</span>
        {#if playlistListPage.status === 'loading'}
          <div class="state compact">Loading playlists...</div>
        {:else if playlistListPage.status === 'error'}
          <div class="state state-error compact"><strong>Could not load playlists.</strong><span>{playlistListPage.error}</span></div>
        {:else if playlistListPage.playlists.length === 0}
          <div class="state compact"><strong>No playlists yet.</strong><span>Imported playlists will appear here.</span></div>
        {:else}
          <div class="result-list">
            {#each playlistListPage.playlists as playlist (playlist.id)}
              <a href={playlistHref(playlist)} onclick={event => navigate(event, playlistHref(playlist))}>
                <span class="result-thumb" aria-hidden="true">P</span>
                <div class="result-copy">
                  <span>{formatVideoCount(playlist.video_count)}{playlist.subscribed ? ' · Subscribed' : ''}{playlist.channel?.name ? ` · ${playlist.channel.name}` : ''}</span>
                  <strong>{playlist.title}</strong>
                  <p>{playlist.description || 'No playlist description has been imported yet.'}</p>
                </div>
              </a>
            {/each}
          </div>
          <PaginationControls label="Playlist list pagination" page={playlistListPage.pagination.page} last={lastPage(playlistListPage.pagination)} onPageChange={changePlaylistsPage} />
        {/if}
      </section>
    {:else if playlistID}
      <section class="utility-page" aria-label="Playlist detail">
        {#if playlistPage.status === 'loading'}
          <div class="state compact">Loading playlist...</div>
        {:else if playlistPage.status === 'error'}
          <div class="state state-error compact"><strong>Could not load playlist.</strong><span>{playlistPage.error}</span></div>
        {:else if playlistPage.item}
          <p>{playlistPage.item.subscribed ? 'Subscribed playlist' : 'Playlist'}</p>
          <h1>{playlistPage.item.title}</h1>
          <span>{formatVideoCount(playlistPage.item.video_count)}{playlistPage.item.channel?.name ? ` from ${playlistPage.item.channel.name}` : ''}</span>
          {#if playlistPage.item.description}<p>{playlistPage.item.description}</p>{/if}
          {#if playlistPage.videos.length === 0 && playlistPage.pagination.total === 0}
            <div class="state compact"><strong>No videos in this playlist.</strong><span>Playlist entries will appear after import or sync.</span></div>
          {:else if playlistPage.videos.length === 0}
            <div class="state compact"><strong>No videos on this page.</strong><span>Use pagination to return to available playlist entries.</span></div>
          {:else}
            <div class="result-list">
              {#each playlistPage.videos as item (item.id)}
                <a href={videoHref(item.id)} onclick={event => navigate(event, videoHref(item.id))}>
                  <span class:has-thumbnail={!!item.thumbnail_url} class="result-thumb" style={thumbnailStyle(item)} aria-hidden="true">{#if item.thumbnail_url}<img src={item.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}{thumbnailFallback(item)}{/if}</span>
                  <div class="result-copy">
                    <span>{metadataLine(item)}</span>
                    <strong>{item.title}</strong>
                    <p>{item.description || 'No description was imported for this video.'}</p>
                  </div>
                </a>
              {/each}
            </div>
            <PaginationControls label="Playlist video pagination" page={playlistPage.pagination.page} last={lastPage(playlistPage.pagination)} onPageChange={changePlaylistVideosPage} />
          {/if}
        {/if}
      </section>
    {:else if path === '/search'}
      <section class="search-page" aria-label="Search results">
        <h1>{searchQuery ? `Search results for ${searchQuery}` : 'Search the archive'}</h1>
        {#if searchPage.status === 'idle'}
          <div class="state">Use the search bar to find archived videos, channels, subtitles, and comments.</div>
        {:else if searchPage.status === 'loading'}
          <div class="state">Searching...</div>
        {:else if searchPage.status === 'error'}
          <div class="state state-error"><strong>Search failed.</strong><span>{searchPage.error}</span></div>
        {:else if searchPage.results.length === 0}
          <div class="state">No local results matched this query.</div>
        {:else}
          <div class="result-list">
            {#each searchPage.results as result}
              <a href={resultHref(result)} onclick={event => navigate(event, resultHref(result))}>
                <span class:has-thumbnail={!!result.record?.thumbnail_url} class="result-thumb" style={thumbnailStyle(result)} aria-hidden="true">{#if result.record?.thumbnail_url}<img src={result.record.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}{resultFallback(result)}{/if}</span>
                <div class="result-copy">
                  <span>{resultMeta(result)}</span>
                  <strong>{resultTitle(result)}</strong>
                  <p>{@html result.snippet}</p>
                  <small>{result.field}</small>
                </div>
              </a>
            {/each}
          </div>
        {/if}
      </section>
    {:else}
      <section class="utility-page" aria-label={utilityRoutes[path]?.title ?? 'Not found'}>
        <p>{utilityRoutes[path]?.eyebrow ?? 'Unknown route'}</p>
        <h1>{utilityRoutes[path]?.title ?? 'Not found'}</h1>
        <span>{utilityRoutes[path]?.body ?? 'This route is not available in Kapsel yet.'}</span>
        {#if path === '/downloads'}
          <div class="download-form-grid">
            <form class="channel-dock large" data-testid="direct-download-form" onsubmit={event => { event.preventDefault(); addVideo(); }}>
              <div><strong>Queue a video</strong><span>Paste a single video URL. Kapsel queues the download and refreshes the library when it finishes.</span></div>
              <label class="visually-hidden" for="download-video-url">Video URL</label>
              <input id="download-video-url" bind:value={videoURL} type="url" placeholder="https://www.youtube.com/watch?v=..." required data-testid="direct-download-url" />
              <button type="submit" disabled={videoSubmitDisabled} data-testid="direct-download-submit">{videoJob.status === 'loading' ? 'Queueing' : 'Add video'}</button>
              {#if videoJob.status !== 'idle'}
                <div role="status" aria-live="polite" class:state-error={videoJob.status === 'error' || videoJob.status === 'failed'} class="job-state" data-testid="direct-download-status">
                  {#if videoJob.status === 'queued'}Video download queued.{:else if videoJob.status === 'running'}Downloading video...{:else if videoJob.status === 'succeeded'}Video imported and library refreshed.{:else if videoJob.status === 'failed'}Video download failed: {videoJob.error || 'unknown error'}{:else if videoJob.status === 'cancelled'}Video download cancelled.{:else if videoJob.status === 'error'}Could not add video: {videoJob.error}{:else}Video job status: {videoJob.status}{/if}
                </div>
              {/if}
            </form>

            <form class="channel-dock large" onsubmit={event => { event.preventDefault(); addChannel(); }}>
              <div><strong>Queue a channel</strong><span>Sync a YouTube channel and queue its first video without leaving the downloads view.</span></div>
              <label class="visually-hidden" for="download-channel-url">Channel URL</label>
              <input id="download-channel-url" bind:value={channelURL} type="url" placeholder="https://www.youtube.com/@channel" required />
              <button type="submit" disabled={channelSubmitDisabled}>Add channel</button>
              {#if channelJob.status !== 'idle'}
                <div role="status" aria-live="polite" class:state-error={channelJob.status === 'error' || channelJob.status === 'failed'} class="job-state">
                  {#if channelJob.status === 'queued'}Channel job queued.{:else if channelJob.status === 'running'}Syncing channel catalog...{:else if channelJob.status === 'succeeded'}Channel catalog synced and first video queued.{:else if channelJob.status === 'failed'}Channel job failed: {channelJob.error || 'unknown error'}{:else if channelJob.status === 'error'}Could not add channel: {channelJob.error}{:else}Channel job status: {channelJob.status}{/if}
                </div>
              {/if}
            </form>
          </div>

          <section class="jobs-panel" aria-label="Durable jobs">
            <div class="jobs-toolbar">
              <div>
                <h2>Queue activity</h2>
                <span>{jobCountLabel(jobsPage.pagination.total)} tracked locally. Live updates use the browser websocket connection.</span>
              </div>
              <button class="refresh-jobs" type="button" onclick={() => loadJobs({ showLoading: false })} disabled={jobsPage.status === 'loading'}>{jobsPage.status === 'loading' ? 'Refreshing...' : 'Refresh'}</button>
            </div>

            <div class="job-filter-row" aria-label="Job status filters">
              {#each jobStatusFilters as filter}
                <button type="button" class:active={jobsPage.filter === filter.value} onclick={() => setJobFilter(filter.value)} aria-pressed={jobsPage.filter === filter.value}>{filter.label}</button>
              {/each}
            </div>
            {#if jobAction.error}
              <div class="state state-error compact" role="alert">{jobAction.error}</div>
            {/if}

            {#if jobsPage.status === 'loading' && jobsPage.jobs.length === 0}
              <div class="state compact">Loading durable jobs...</div>
            {:else if jobsPage.status === 'error' && jobsPage.jobs.length === 0}
              <div class="state state-error compact"><strong>Could not load jobs.</strong><span>{jobsPage.error}</span></div>
            {:else if jobsPage.jobs.length === 0}
              <div class="state compact"><strong>No jobs match this view.</strong><span>Queued downloads, imports, and maintenance tasks will appear here.</span></div>
            {:else}
              {#if jobsPage.status === 'error'}
                <div class="state state-error compact"><strong>Job refresh failed.</strong><span>{jobsPage.error}. Retrying automatically.</span></div>
              {/if}
              <div class="job-list" data-testid="job-dashboard">
                {#each jobsPage.jobs as job (job.id)}
                  <article class:failed={job.status === 'failed'} class:running={job.status === 'running'} class="job-card" data-job-id={job.id}>
                    <div class="job-card-main">
                      <div>
                        <span class="job-type">{jobTypeLabel(job.type)}</span>
                        <h3 title={job.id}>{abbreviateJobID(job.id)}</h3>
                      </div>
                      <span class="status-pill job-status">{job.status}</span>
                    </div>

                    <div class="job-progress" aria-label={`Progress ${jobProgressPercent(job)} percent`}>
                      <span style={`width: ${jobProgressPercent(job)}%`}></span>
                    </div>
                    <div class="job-progress-meta"><span>{jobProgressPercent(job)}%</span><span>{job.attempts}/{job.max_attempts} attempts</span></div>

                    <p class:error-text={!!job.error && job.status === 'failed'} data-testid={job.status === 'failed' && job.error ? 'job-error-summary' : undefined}>{jobSummary(job)}</p>
                    {#if job.status === 'failed' && job.error}
                      <details class="job-raw-details">
                        <summary>Show raw error log</summary>
                        <div class="job-raw-toolbar">
                          <button type="button" onclick={() => copyJobError(job)}>Copy raw log</button>
                          {#if jobErrorCopy.id === job.id && jobErrorCopy.status}<span role="status">{jobErrorCopy.status}</span>{/if}
                        </div>
                        <textarea class="job-raw-error" data-testid="job-raw-error" rows="8" readonly value={job.error} aria-label={`Raw error log for job ${job.id}`}></textarea>
                      </details>
                    {/if}
                    {#if jobTargetHref(job)}
                      <a class="job-target" href={jobTargetHref(job)} onclick={event => navigate(event, jobTargetHref(job))}>{jobTargetLabel(job)}</a>
                    {/if}

                    {#if canCancelJob(job) || canRetryJob(job) || job.cancel_requested || (job.status === 'failed' && job.result_summary)}
                      <div class="job-actions">
                        {#if canCancelJob(job)}
                          <button class="danger" type="button" onclick={() => runJobAction(job, 'cancel')} disabled={jobActionInProgress(job, 'cancel')}>{jobActionInProgress(job, 'cancel') ? 'Cancelling...' : 'Cancel job'}</button>
                        {/if}
                        {#if job.cancel_requested}
                          <span>Cancellation requested</span>
                        {/if}
                        {#if canRetryJob(job)}
                          <button type="button" onclick={() => runJobAction(job, 'retry')} disabled={jobActionInProgress(job, 'retry')}>{jobActionInProgress(job, 'retry') ? 'Retrying...' : 'Retry job'}</button>
                        {:else if job.status === 'failed' && job.result_summary}
                          <span>Retry unavailable after recorded result</span>
                        {/if}
                      </div>
                    {/if}

                    <dl class="job-meta-grid">
                      <div><dt>Job ID</dt><dd><code data-testid="job-full-id">{job.id}</code></dd></div>
                      <div><dt>Updated</dt><dd>{formatJobTime(job.updated_at)}</dd></div>
                      <div><dt>Created</dt><dd>{formatJobTime(job.created_at)}</dd></div>
                      {#if job.completed_at}<div><dt>Completed</dt><dd>{formatJobTime(job.completed_at)}</dd></div>{/if}
                      {#if job.run_after}<div><dt>Run after</dt><dd>{formatJobTime(job.run_after)}</dd></div>{/if}
                    </dl>
                  </article>
                {/each}
              </div>

              <PaginationControls label="Job list pagination" page={jobsPage.pagination.page} last={jobsLastPage()} onPageChange={changeJobsPage} />
            {/if}
          </section>
        {:else if path === '/settings'}
          <SettingsDiagnosticsPanel {diagnostics} {loadDiagnostics} {settingsRows} {checkStateLabel} {formatBytes} {storageMaintenanceRows} />
        {/if}
      </section>
    {/if}
  </section>
</main>
