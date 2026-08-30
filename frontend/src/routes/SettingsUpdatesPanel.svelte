<script>
  const noop = () => {};

  let {
    updates = null,
    loadUpdates = noop,
    fetchJSON = noop,
    onJobCreated = noop,
    formatJobTime = value => value || '',
  } = $props();

  let busyAction = $state('');
  let actionError = $state('');
  let confirmVersion = $state('');
  let checkQueued = $state(false);
  let pollTimer;

  $effect(() => () => clearInterval(pollTimer));

  const pending = $derived(updates?.pending ?? null);
  const statusLabel = $derived(offerStatusLabel(pending?.status));
  const recentOffers = $derived(updates?.recent ?? []);

  function offerStatusLabel(status) {
    switch (status) {
      case 'pending':
        return 'Awaiting approval';
      case 'approved':
        return 'Approved, applying';
      case 'applied':
        return 'Applied';
      case 'dismissed':
        return 'Dismissed';
      case 'failed':
        return 'Failed';
      default:
        return status || 'Unknown';
    }
  }

  async function checkNow() {
    busyAction = 'check';
    actionError = '';
    try {
      await fetchJSON('/api/updates/check', { method: 'POST' });
      checkQueued = true;
      onJobCreated();
    } catch (error) {
      actionError = error.message;
    } finally {
      busyAction = '';
    }
  }

  function requestApproval(version) {
    actionError = '';
    confirmVersion = version;
  }

  function cancelApproval() {
    confirmVersion = '';
  }

  async function approve() {
    if (!pending) return;
    busyAction = 'approve';
    actionError = '';
    try {
      const response = await fetchJSON(`/api/updates/${pending.id}/approve`, { method: 'POST' });
      confirmVersion = '';
      onJobCreated();
      if (response?.job?.id) {
        // The apply job swaps the binary and restarts the service; poll the
        // summary so the panel flips to approved/applied once the job runs.
        pollAfterApproval();
      } else {
        loadUpdates();
      }
    } catch (error) {
      actionError = error.message;
    } finally {
      busyAction = '';
    }
  }

  async function dismiss() {
    if (!pending) return;
    busyAction = 'dismiss';
    actionError = '';
    try {
      await fetchJSON(`/api/updates/${pending.id}/dismiss`, { method: 'POST' });
      loadUpdates();
    } catch (error) {
      actionError = error.message;
    } finally {
      busyAction = '';
    }
  }

  function pollAfterApproval() {
    clearInterval(pollTimer);
    let attempts = 0;
    pollTimer = setInterval(async () => {
      attempts += 1;
      try {
        await loadUpdates();
      } catch {
        // 401/restart in-flight; keep polling until the window is exhausted.
      }
      const status = pending?.status;
      if (status === 'applied' || status === 'failed' || attempts >= 30) {
        clearInterval(pollTimer);
        loadUpdates();
      }
    }, 2000);
  }

  function releaseNotesSummary(notes) {
    const text = String(notes ?? '').trim();
    if (!text) return '';
    const firstLine = text.split(/\r?\n/).map(line => line.trim()).find(line => line.length > 0) ?? '';
    return firstLine.length > 220 ? `${firstLine.slice(0, 217)}...` : firstLine;
  }
</script>

<section class="diagnostics-panel" aria-label="Application updates">
  <div class="diagnostics-heading">
    <h2>Updates</h2>
    <button class="refresh-diagnostics" type="button" onclick={checkNow} disabled={busyAction !== ''}>{busyAction === 'check' ? 'Checking...' : 'Check now'}</button>
  </div>

  {#if actionError}
    <div class="state state-error compact" role="alert">{actionError}</div>
  {/if}
  {#if checkQueued && !pending}
    <div class="state compact" role="status">Release check queued. It appears in Downloads once it runs.</div>
  {/if}

  {#if updates}
    <section class="settings-section" aria-label="Update status" data-testid="update-status">
      <dl class="settings-grid compact">
        <div><dt>Running version</dt><dd data-testid="running-version">{updates.current_version || 'dev'}</dd></div>
        <div><dt>Release source</dt><dd><code>{updates.repo}</code></dd></div>
        <div>
          <dt>Background checks</dt>
          <dd>{updates.update_checks_enabled ? `Every ${updates.check_interval_label || '—'}` : 'Disabled — set KAPSEL_UPDATE_CHECK_INTERVAL to enable'}</dd>
        </div>
        {#if updates.last_check}
          <div>
            <dt>Last check</dt>
            <dd>{formatJobTime(updates.last_check.updated_at) || updates.last_check.updated_at} — {updates.last_check.status}{#if updates.last_check.detail}<br />{updates.last_check.detail}{/if}</dd>
          </div>
        {/if}
      </dl>
    </section>

    <section class="settings-section" aria-label="Pending update" data-testid="pending-update">
      <h3>Pending update</h3>
      {#if pending}
        <article class="diagnostic-card warning update-offer">
          <div>
            <strong>{pending.version}</strong>
            <span>{statusLabel}</span>
          </div>
          <span class="status-pill">{statusLabel}</span>
          {#if pending.published_at}<p>Published {formatJobTime(pending.published_at) || pending.published_at}</p>{/if}
          {#if pending.release_notes}<p>{releaseNotesSummary(pending.release_notes)}</p>{/if}
          {#if pending.release_url}<p><a href={pending.release_url} target="_blank" rel="noreferrer">Release notes on GitHub</a></p>{/if}
          {#if pending.error}<p class="state-error">{pending.error}</p>{/if}
          {#if pending.status === 'pending' || pending.status === 'failed'}
            {#if confirmVersion === pending.version}
              <div class="update-confirm">
                <p>Replace kapsel with <strong>{pending.version}</strong>? The database is backed up first, then the service restarts itself.</p>
                <button type="button" class="update-approve" onclick={approve} disabled={busyAction !== ''}>{busyAction === 'approve' ? 'Applying...' : 'Confirm update'}</button>
                <button type="button" onclick={cancelApproval} disabled={busyAction !== ''}>Cancel</button>
              </div>
            {:else}
              <div class="update-actions">
                <button type="button" class="update-approve" onclick={() => requestApproval(pending.version)} disabled={busyAction !== ''}>Approve update</button>
                <button type="button" onclick={dismiss} disabled={busyAction !== ''}>{busyAction === 'dismiss' ? 'Dismissing...' : 'Dismiss'}</button>
              </div>
            {/if}
          {/if}
        </article>
      {:else}
        <div class="state compact">No update is waiting for approval.</div>
      {/if}
    </section>

    {#if recentOffers.length > 0}
      <section class="settings-section" aria-label="Update history">
        <h3>History</h3>
        <dl class="settings-grid compact">
          {#each recentOffers as offer}
            <div>
              <dt>{offer.version}</dt>
              <dd>{offerStatusLabel(offer.status)}{#if offer.updated_at} — {formatJobTime(offer.updated_at) || offer.updated_at}{/if}{#if offer.error}<br />{offer.error}{/if}</dd>
            </div>
          {/each}
        </dl>
      </section>
    {/if}
  {:else}
    <div class="state compact">Update status has not been loaded yet.</div>
  {/if}
</section>
