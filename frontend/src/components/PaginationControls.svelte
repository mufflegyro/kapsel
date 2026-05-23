<script>
  let { label = 'Pagination', page = 1, last = 1, onPageChange = () => {} } = $props();
</script>

<div class="jobs-pagination" aria-label={label}>
  <button type="button" onclick={() => onPageChange(page - 1)} disabled={page <= 1}>Previous</button>
  <span>Page {page} of {last}</span>
  <button type="button" onclick={() => onPageChange(page + 1)} disabled={page >= last}>Next</button>
</div>
