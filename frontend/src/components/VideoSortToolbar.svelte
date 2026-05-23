<script>
  let {
    sort = 'newest',
    options = [],
    controlId = 'video-sort',
    labelledBy = 'Library sort options',
    summary = '',
    onSortChange = () => {},
  } = $props();
</script>

<div class="library-toolbar" aria-label={labelledBy}>
  <div class="toolbar-summary">
    {#if summary}<span class="feed-summary" data-testid="library-feed-summary">{summary}</span>{/if}
  </div>
  <label for={controlId}>Sort by
    <select id={controlId} value={sort} onchange={event => onSortChange(event.currentTarget.value)}>
      {#each options as option}
        <option value={option.value}>{option.label}</option>
      {/each}
    </select>
  </label>
</div>
