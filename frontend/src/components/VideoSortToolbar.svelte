<script>
  let {
    sort = 'newest',
    options = [],
    controlId = 'video-sort',
    labelledBy = 'Library sort options',
    summary = '',
    onSortChange = () => {},
    hideWatched = null,
    onHideWatchedChange = null,
  } = $props();
</script>

<div class="library-toolbar" aria-label={labelledBy}>
  <div class="toolbar-summary">
    {#if summary}<span class="feed-summary" data-testid="library-feed-summary">{summary}</span>{/if}
  </div>
  <div class="toolbar-controls">
    {#if onHideWatchedChange}
      <label class="toolbar-toggle" for={`${controlId}-hide-watched`}>
        <input
          id={`${controlId}-hide-watched`}
          data-testid={`${controlId}-hide-watched`}
          type="checkbox"
          checked={!!hideWatched}
          onchange={event => onHideWatchedChange(event.currentTarget.checked)}
        />
        Hide watched
      </label>
    {/if}
    <label for={controlId}>Sort by
      <select id={controlId} value={sort} onchange={event => onSortChange(event.currentTarget.value)}>
        {#each options as option}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
    </label>
  </div>
</div>
