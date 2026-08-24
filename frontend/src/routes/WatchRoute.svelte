<script>
  import PaginationControls from '../components/PaginationControls.svelte';
  import RichText from '../components/RichText.svelte';
  import { channelHref, channelInitial, formatDate, formatDuration, isCatalogOnly, isMembersOnly, metadataLine, thumbnailFallback, thumbnailStyle, videoHref, videoTileLabel } from '../display.js';

  const noop = () => {};

  let {
    activeCinemaMode = false,
    video = { status: 'idle', item: null, error: '' },
    watchMediaElement = $bindable(),
    cinemaMode = false,
    cinemaModeControl = noop,
    volumeNormalization = false,
    volumeNormalizationControl = noop,
    captionButtonVisibility = noop,
    captionTrackController = noop,
    loadHiddenTextTrack = noop,
    videoPosterURL = () => '',
    restoreAndAutoplay = noop,
    handlePlaybackPlay = noop,
    handlePlaybackError = noop,
    rememberPlaybackRate = noop,
    skipSponsorSegment = noop,
    handlePlaybackProgress = noop,
    handlePlaybackPause = noop,
    startUpNext = noop,
    playbackFeedback = { id: 0, type: '' },
    playbackFeedbackLabel = () => '',
    catalogVideoJobs = {},
    jobProgressPercent = () => 0,
    upNextCountdown = 0,
    upNextTarget = null,
    cancelUpNext = noop,
    playUpNextNow = noop,
    navigate = noop,
    channelHandle = () => '',
    videoIsWatched = () => false,
    markVideoPlayed = noop,
    markPlayedAction = { status: 'idle', error: '' },
    deleteVideoMedia = noop,
    deleteVideoMediaAction = { status: 'idle', error: '' },
    toggleKeepForever = noop,
    keepForeverAction = { status: 'idle', error: '' },
    canDownloadCatalogVideo = () => false,
    isCatalogVideoJobLocked = () => false,
    downloadCatalogItem = noop,
    catalogDownloadButtonLabel = () => '',
    previewJob = { status: 'idle', error: '' },
    isChannelJobActive = () => false,
    seekVideoTimestamp = null,
    commentsPage = { status: 'idle', comments: [], pagination: { page: 1, total: 0 }, error: '' },
    commentsLastPage = 1,
    changeCommentsPage = noop,
    recommendations = [],
  } = $props();

  let catalogOnly = $derived(isCatalogOnly(video.item));
  let membersOnly = $derived(isMembersOnly(video.item));
  let hasDescription = $derived(String(video.item?.description || '').trim() !== '');
  let commentsEmpty = $derived(commentsPage.status !== 'loading' && commentsPage.status !== 'error' && commentsPage.comments.length === 0);
  let keepForeverLabel = $derived(catalogOnly ? (keepForeverAction.status === 'loading' ? 'Saving protection' : video.item?.keep_forever ? 'Protected from cleanup' : 'Protect from cleanup') : (keepForeverAction.status === 'loading' ? 'Saving keep state' : video.item?.keep_forever ? 'Kept forever' : 'Keep forever'));
  let deleteVideoMediaLabel = $derived(deleteVideoMediaAction.status === 'loading' ? 'Deleting video' : 'Delete video');
</script>

<section class:cinema-mode={activeCinemaMode} class="watch-page" aria-label="Video detail" data-testid="video-detail-route">
  <div class="watch-main">
    {#if video.status === 'loading'}
      <div class="state" data-testid="video-loading">Loading archived video...</div>
    {:else if video.status === 'error'}
      <div class="state state-error" data-testid="video-error"><strong>Could not load this video.</strong><span>{video.error}</span></div>
    {:else if video.item}
      <div class="watch-player" data-testid="video-detail" data-video-id={video.item.id} role="group" aria-label={video.item.media_url ? 'Video player' : 'Video media unavailable locally'}>
        {#if video.item.media_url}
          <video-player class="videojs-player" data-testid="videojs-player">
            <video-skin use:cinemaModeControl={cinemaMode} use:volumeNormalizationControl={volumeNormalization} use:captionButtonVisibility={(video.item.subtitles?.length ?? 0) > 0}>
              <!-- svelte-ignore a11y_media_has_caption: caption tracks are injected dynamically when archived subtitles exist. -->
              <video bind:this={watchMediaElement} slot="media" src={video.item.media_url} poster={videoPosterURL(video.item)} preload="metadata" playsinline use:captionTrackController onloadedmetadata={restoreAndAutoplay} onplay={handlePlaybackPlay} onerror={handlePlaybackError} onratechange={rememberPlaybackRate} onseeked={event => skipSponsorSegment(event.currentTarget)} ontimeupdate={handlePlaybackProgress} onpause={handlePlaybackPause} onended={event => { handlePlaybackProgress(event, { force: true }); startUpNext(); }}>
                {#if video.item.timeline_preview?.vtt_url}
                  <track kind="metadata" label="thumbnails" src={video.item.timeline_preview.vtt_url} use:loadHiddenTextTrack />
                {/if}
                {#if video.item.chapters_vtt_url}
                  <track kind="chapters" label="Chapters" src={video.item.chapters_vtt_url} use:loadHiddenTextTrack />
                {/if}
                {#each video.item.subtitles ?? [] as caption}
                  <track kind="captions" src={caption.url} srclang={caption.language} label={caption.label || caption.language} />
                {/each}
              </video>
            </video-skin>
          </video-player>
          {#if playbackFeedback.type}
            {#key playbackFeedback.id}
              <div class:show-pause={playbackFeedback.type === 'pause'} class:show-play={playbackFeedback.type === 'play'} class="playback-feedback" data-testid="playback-feedback" aria-label={playbackFeedbackLabel(playbackFeedback.type)} aria-live="polite">
                {#if playbackFeedback.type === 'pause'}
                  <svg aria-hidden="true" focusable="false" viewBox="0 0 18 18"><rect width="5" height="14" x="2" y="2" fill="currentColor" rx="1.75" /><rect width="5" height="14" x="11" y="2" fill="currentColor" rx="1.75" /></svg>
                {:else}
                  <svg aria-hidden="true" focusable="false" viewBox="0 0 18 18"><path fill="currentColor" d="m14.051 10.723-7.985 4.964a1.98 1.98 0 0 1-2.758-.638A2.06 2.06 0 0 1 3 13.964V4.036C3 2.91 3.895 2 5 2c.377 0 .747.109 1.066.313l7.985 4.964a2.057 2.057 0 0 1 .627 2.808c-.16.257-.373.475-.627.637" /></svg>
                {/if}
              </div>
            {/key}
          {/if}
        {:else}
          <div class:catalog-only={catalogOnly} class:members-only={membersOnly} class:has-thumbnail={!!video.item.thumbnail_url} class:downloading={isChannelJobActive(catalogVideoJobs[video.item.id]?.status)} class="watch-fallback-thumb" style={thumbnailStyle(video.item)} aria-hidden="true">
            {#if video.item.thumbnail_url}<img src={video.item.thumbnail_url} alt="" referrerpolicy="no-referrer" />{:else}<span>{thumbnailFallback(video.item)}</span>{/if}
            {#if isChannelJobActive(catalogVideoJobs[video.item.id]?.status)}<div class="thumbnail-download-progress"><span style={`width: ${jobProgressPercent(catalogVideoJobs[video.item.id])}%`}></span></div><strong class="thumbnail-download-percent" data-testid="download-progress-percent">{jobProgressPercent(catalogVideoJobs[video.item.id])}%</strong>{/if}
          </div>
          {#if isChannelJobActive(catalogVideoJobs[video.item.id]?.status)}<div class="visually-hidden" role="progressbar" aria-label={`Download progress for ${video.item.title || 'video'}`} aria-valuemin="0" aria-valuemax="100" aria-valuenow={jobProgressPercent(catalogVideoJobs[video.item.id])}>{jobProgressPercent(catalogVideoJobs[video.item.id])}%</div>{/if}
          <div class="state">This video has metadata but no playable media URL yet.</div>
        {/if}
        {#if upNextCountdown > 0 && upNextTarget}
          {#if upNextTarget.thumbnail_url}
            <img class="up-next-backdrop" src={upNextTarget.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" data-testid="up-next-backdrop" />
            <div class="up-next-backdrop-scrim" aria-hidden="true"></div>
          {/if}
          <div class="up-next-overlay" data-testid="up-next-overlay" role="dialog" aria-modal="false" tabindex="-1" aria-label={`Up next: playing ${upNextTarget.title} in ${upNextCountdown} seconds`} onkeydown={event => { if (event.key === 'Escape') cancelUpNext(); }}>
            <div class="up-next-countdown-ring" data-testid="up-next-countdown" aria-hidden="true">{upNextCountdown}</div>
            <div class="up-next-info">
              <strong>Up next</strong>
              <span>{upNextTarget.title}</span>
            </div>
            <div class="up-next-actions">
              <button type="button" onclick={cancelUpNext} data-testid="up-next-cancel">Cancel</button>
              <button type="button" class="up-next-play" onclick={playUpNextNow} data-testid="up-next-play-now">Play now</button>
            </div>
          </div>
        {/if}
      </div>

      <div class="watch-details">
        <h1 class="watch-title">{video.item.title}</h1>
        {#if catalogOnly && membersOnly}<p class="media-availability metadata-only" data-testid="media-availability"><strong>Members only - join the channel to watch</strong><span>This video is restricted to channel members, so Kapsel cannot download it.</span></p>{:else if catalogOnly}<p class="media-availability metadata-only" data-testid="media-availability"><strong>Metadata only - no media file downloaded yet</strong><span>Download the video to make it playable from this Kapsel node.</span></p>{/if}
        <div class="watch-row">
          <a class="channel-lockup" href={channelHref(video.item.channel)} onclick={event => navigate(event, channelHref(video.item.channel))}>
            <span class:has-thumbnail={!!video.item.channel?.thumbnail_url} class="avatar large" aria-hidden="true">
              {#if video.item.channel?.thumbnail_url}<img src={video.item.channel.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}{channelInitial(video.item.channel?.name)}{/if}
            </span>
            <span><strong>{video.item.channel?.name || 'Unknown channel'}</strong><small>{channelHandle(video.item.channel)}</small></span>
          </a>
          <div class="action-row">
            {#if catalogOnly && canDownloadCatalogVideo(video.item)}
              <button class="catalog-download watch-download primary-action" type="button" disabled={!canDownloadCatalogVideo(video.item) || isCatalogVideoJobLocked(catalogVideoJobs[video.item.id]?.status)} onclick={() => downloadCatalogItem(video.item)} data-testid="video-download-button">{catalogDownloadButtonLabel(catalogVideoJobs[video.item.id], 'video')}</button>
            {/if}
            {#if video.item.media_url && !videoIsWatched(video.item)}
              <button class="catalog-download watch-download mark-played-button" type="button" onclick={markVideoPlayed} disabled={markPlayedAction.status === 'loading'} data-testid="mark-played-button">{markPlayedAction.status === 'loading' ? 'Marking as played' : 'Mark as played'}</button>
            {/if}
            <button class:active={video.item.keep_forever} class:secondary-action={catalogOnly} class="catalog-download watch-download" type="button" aria-pressed={!!video.item.keep_forever} onclick={toggleKeepForever} disabled={keepForeverAction.status === 'loading'} data-testid="keep-forever-toggle">{keepForeverLabel}</button>
            {#if video.item.media_url}
              <button class="catalog-download watch-download danger-action" type="button" onclick={deleteVideoMedia} disabled={deleteVideoMediaAction.status === 'loading'} data-testid="delete-video-media-button">{deleteVideoMediaLabel}</button>
            {/if}
            {#if !catalogOnly && canDownloadCatalogVideo(video.item)}
              <button class="catalog-download watch-download" type="button" disabled={!canDownloadCatalogVideo(video.item) || isCatalogVideoJobLocked(catalogVideoJobs[video.item.id]?.status)} onclick={() => downloadCatalogItem(video.item)} data-testid="video-download-button">{catalogDownloadButtonLabel(catalogVideoJobs[video.item.id], 'video')}</button>
            {/if}
          </div>
        </div>
        {#if catalogVideoJobs[video.item.id]?.error}<div class="job-state compact state-error" role="alert">Could not download video: {catalogVideoJobs[video.item.id].error}</div>{/if}
        {#if isChannelJobActive(previewJob.status)}<div class="job-state compact">Generating timeline previews...</div>{:else if previewJob.status === 'failed' || previewJob.status === 'error'}<div class="job-state compact state-error" role="alert">Could not generate timeline preview: {previewJob.error}</div>{/if}
        {#if markPlayedAction.status === 'error'}<div class="job-state compact state-error" role="alert">Could not mark as played: {markPlayedAction.error}</div>{/if}
        {#if keepForeverAction.status === 'error'}<div class="job-state compact state-error" role="alert">Could not update keep forever: {keepForeverAction.error}</div>{/if}
        {#if deleteVideoMediaAction.status === 'error'}<div class="job-state compact state-error" role="alert">Could not delete video media: {deleteVideoMediaAction.error}</div>{/if}

        <section class:compact-empty={catalogOnly && !hasDescription} class="description-box" aria-label="Video description">
          <strong>{metadataLine(video.item)}</strong>
          {#if hasDescription}
            <RichText text={video.item.description} onTimestamp={video.item.media_url ? seekVideoTimestamp : null} />
          {:else}
            <p>{catalogOnly ? 'No description imported yet.' : 'No description was imported for this video.'}</p>
          {/if}
        </section>

        <section class:compact-empty={catalogOnly && commentsEmpty} class="comments-box" aria-label="Comments">
          <div class="comments-heading"><h2>Comments</h2><span>{commentsPage.pagination.total || 0} imported</span></div>
          {#if commentsPage.status === 'loading'}
            <div class="state compact">Loading comments...</div>
          {:else if commentsPage.status === 'error'}
            <div class="state state-error compact"><strong>Could not load comments.</strong><span>{commentsPage.error}</span></div>
          {:else if commentsPage.comments.length === 0}
            <p>{catalogOnly ? 'No comments imported yet.' : 'No archived comments have been imported for this video.'}</p>
          {:else}
            <div class="comment-list">
              {#each commentsPage.comments as comment (comment.id)}
                <article class="comment-card">
                  <div><strong>{comment.author || 'Archived commenter'}</strong><span>{formatDate(comment.published_at)}</span></div>
                  <p>{comment.text}</p>
                  <small>{comment.like_count || 0} likes{comment.reply_count ? ` · ${comment.reply_count} replies` : ''}</small>
                </article>
              {/each}
            </div>
            <PaginationControls label="Comment pagination" page={commentsPage.pagination.page} last={commentsLastPage} onPageChange={changeCommentsPage} />
          {/if}
        </section>
      </div>
    {/if}
  </div>

  <aside class="recommendations" aria-label="Recommendations">
    <h2>Up next</h2>
    {#if recommendations.length === 0}
      <div class="state compact">More videos will appear here once the archive has more items.</div>
    {:else}
      {#each recommendations as item (item.id)}
        <article class="recommendation-card">
          <a class:catalog-only={isCatalogOnly(item)} class:has-thumbnail={!!item.thumbnail_url} class="mini-thumb" style={thumbnailStyle(item)} href={videoHref(item.id)} onclick={event => navigate(event, videoHref(item.id))} aria-label={videoTileLabel(item)}>
            {#if item.thumbnail_url}<img src={item.thumbnail_url} alt="" loading="lazy" referrerpolicy="no-referrer" />{:else}<span>{thumbnailFallback(item)}</span>{/if}
            {#if formatDuration(item.duration_seconds)}<span class="duration-badge">{formatDuration(item.duration_seconds)}</span>{/if}
          </a>
          <div>
            <a class="recommendation-title" href={videoHref(item.id)} onclick={event => navigate(event, videoHref(item.id))}>{item.title}</a>
            <span>{item.channel?.name || 'Unknown channel'}</span>
            <small>{metadataLine(item)}</small>
          </div>
        </article>
      {/each}
    {/if}
  </aside>
</section>
