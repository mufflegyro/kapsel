<script>
  import {
    channelHref,
    channelInitial,
    feedMetadataLine,
    formatDuration,
    isCatalogOnly,
    thumbnailFallback,
    thumbnailStyle,
    videoHref,
    videoTileLabel,
  } from '../display.js';

  let {
    item,
    showChannel = true,
    flat = false,
    enableCatalogDownload = false,
    downloadJob = null,
    testId = 'video-card',
    navigate = () => {},
    onDownload = () => {},
    downloadDisabled = () => true,
  } = $props();

  let catalogOnly = $derived(isCatalogOnly(item));
  let showDownloadAction = $derived(enableCatalogDownload && catalogOnly && item?.can_download === true);
  let downloadStatus = $derived(downloadJob?.status || 'idle');
  let downloadActive = $derived(downloadStatus === 'loading' || downloadStatus === 'queued' || downloadStatus === 'running');
  let downloadComplete = $derived(downloadStatus === 'succeeded');
  let disabled = $derived(showDownloadAction && (downloadActive || downloadComplete || downloadDisabled(item)));
  let downloadProgress = $derived(progressPercent(downloadJob));
  let playbackProgress = $derived(playbackProgressPercent(item));
  let showPlaybackProgress = $derived(playbackProgress > 0);
  let metadata = $derived(feedMetadataLine(item));
  let downloadButtonLabel = $derived(downloadActive ? 'Downloading' : downloadComplete ? 'Downloaded' : downloadStatus === 'failed' || downloadStatus === 'error' ? 'Retry' : 'Download');
  let videoURL = $derived(videoHref(item?.id || ''));
  let itemChannelHref = $derived(channelHref(item?.channel));
  let duration = $derived(formatDuration(item?.duration_seconds));

  function progressPercent(job) {
    const value = Number(job?.progress) || 0;
    return Math.round(Math.min(1, Math.max(0, value)) * 100);
  }

  function playbackProgressPercent(value) {
    const progress = value?.progress ?? {};
    if (progress.watched || value?.watched) return 100;

    const position = Number(progress.position_seconds ?? value?.position_seconds) || 0;
    const progressDuration = Number(progress.duration_seconds) || 0;
    const duration = progressDuration > 0 ? progressDuration : Number(value?.duration_seconds) || 0;
    if (position <= 0 || duration <= 0) return 0;

    return Math.max(1, Math.round(Math.min(1, Math.max(0, position / duration)) * 100));
  }
</script>

{#snippet thumbnail()}
  <div class:catalog-only={catalogOnly} class:has-thumbnail={!!item?.thumbnail_url} class:downloading={downloadActive} class="thumb" style={thumbnailStyle(item)} aria-hidden="true">
    {#if item?.thumbnail_url}<img src={item.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}<span>{thumbnailFallback(item)}</span>{/if}
    {#if duration}<span class="duration-badge">{duration}</span>{/if}
    {#if showPlaybackProgress}<div class="thumbnail-playback-progress" data-testid="playback-progress-bar" aria-hidden="true"><span style={`width: ${playbackProgress}%`}></span></div>{/if}
    {#if downloadActive}<div class="thumbnail-download-progress"><span style={`width: ${downloadProgress}%`}></span></div><strong class="thumbnail-download-percent" data-testid="download-progress-percent">{downloadProgress}%</strong>{/if}
  </div>
{/snippet}

<article class:flat class="video-tile" data-testid={testId} data-video-id={item?.id}>
  {#if showDownloadAction}
    <button class="thumbnail-link catalog-download-thumb" type="button" disabled={disabled} onclick={() => onDownload(item)} aria-label={`${downloadButtonLabel} ${item?.title || 'video'}`}>
      {@render thumbnail()}
    </button>
  {:else}
    <a class="thumbnail-link" href={videoURL} onclick={event => navigate(event, videoURL)} aria-label={videoTileLabel(item)}>
      {@render thumbnail()}
    </a>
  {/if}
  {#if showPlaybackProgress}<div class="visually-hidden" data-testid="playback-progress" role="progressbar" aria-label={`Playback progress for ${item?.title || 'video'}`} aria-valuemin="0" aria-valuemax="100" aria-valuenow={playbackProgress}>{playbackProgress}% watched</div>{/if}
  {#if showDownloadAction && downloadActive}<div class="visually-hidden" role="progressbar" aria-label={`Download progress for ${item?.title || 'video'}`} aria-valuemin="0" aria-valuemax="100" aria-valuenow={downloadProgress}>{downloadProgress}%</div>{/if}

  <div class:no-avatar={!showChannel} class:no-action={!showChannel && !showDownloadAction} class="tile-meta">
    {#if showChannel}
      <a class:has-thumbnail={!!item?.channel?.thumbnail_url} class="avatar" href={itemChannelHref} onclick={event => navigate(event, itemChannelHref)} aria-label={item?.channel?.name || 'Unknown channel'}>{#if item?.channel?.thumbnail_url}<img src={item.channel.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}{channelInitial(item?.channel?.name)}{/if}</a>
    {/if}
    <div>
      <a class:catalog-title={showDownloadAction} class="tile-title" href={videoURL} onclick={event => navigate(event, videoURL)} aria-label={catalogOnly ? `Open metadata page for ${item?.title || 'video'}` : undefined}>{item?.title}</a>
      {#if showChannel}<a class="tile-channel" href={itemChannelHref} onclick={event => navigate(event, itemChannelHref)}>{item?.channel?.name || 'Unknown channel'}</a>{/if}
      {#if metadata}<span>{metadata}</span>{/if}
    </div>
    {#if showDownloadAction}<button class="catalog-download" type="button" disabled={disabled} onclick={() => onDownload(item)} data-testid="catalog-download-button">{downloadButtonLabel}</button>{/if}
  </div>
</article>
