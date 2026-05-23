<script>
  const noop = () => {};

  let {
    diagnostics = { status: 'idle', readiness: null, error: '' },
    loadDiagnostics = noop,
    settingsRows = () => [],
    checkStateLabel = value => value || 'Unknown',
    formatBytes = value => String(value ?? 0),
    storageMaintenanceRows = () => [],
  } = $props();

  let diagnosticsCopyStatus = $state('');
  let lastDiagnosticsText = $state('');
  let diagnosticsText = $derived(diagnostics.readiness ? JSON.stringify(diagnostics.readiness, null, 2) : '');
  let readinessSummary = $derived(summarizeReadiness(diagnostics.readiness));

  $effect(() => {
    if (diagnostics.status !== 'loading' && diagnosticsText === lastDiagnosticsText) return;
    diagnosticsCopyStatus = '';
    lastDiagnosticsText = diagnosticsText;
  });

  async function copySettingsDiagnostics() {
    if (!diagnosticsText) return;
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(diagnosticsText);
      diagnosticsCopyStatus = 'Copied';
    } catch {
      diagnosticsCopyStatus = 'Select the diagnostics text to copy it';
    }
  }

  function refreshDiagnostics() {
    diagnosticsCopyStatus = '';
    loadDiagnostics();
  }

  function summarizeReadiness(readiness) {
    const checks = readiness?.checks ?? [];
    const passing = checks.filter(check => check.state === 'pass').length;
    const warnings = checks.filter(check => check.state === 'warn').length;
    const blocked = checks.filter(check => check.state === 'error').length;
    const total = checks.length;
    if (total === 0) {
      return { state: 'unknown', label: 'No checks reported', detail: 'Readiness returned without individual checks.' };
    }
    const label = blocked > 0 ? 'Needs attention' : warnings > 0 ? 'Ready with warnings' : 'Ready';
    const detail = `${readinessCountLabel(passing, 'passing')} / ${readinessCountLabel(warnings, 'warning')} / ${readinessCountLabel(blocked, 'blocked')} across ${readinessCountLabel(total, 'check')}.`;
    return { state: blocked > 0 ? 'error' : warnings > 0 ? 'warn' : 'pass', label, detail };
  }

  function readinessCountLabel(value, label) {
    if (label === 'passing') return `${value} passing`;
    if (label === 'blocked') return `${value} blocked`;
    return `${value} ${value === 1 ? label : `${label}s`}`;
  }
</script>

<section class="diagnostics-panel" aria-label="Readiness diagnostics">
  <div class="diagnostics-heading">
    <h2>Readiness</h2>
    <button class="refresh-diagnostics" type="button" onclick={refreshDiagnostics} disabled={diagnostics.status === 'loading'}>{diagnostics.status === 'loading' ? 'Checking...' : 'Check again'}</button>
  </div>
  {#if diagnostics.status === 'loading'}
    <div class="state compact">Checking local configuration...</div>
  {:else if diagnostics.status === 'error'}
    <div class="state state-error compact"><strong>Diagnostics unavailable.</strong><span>{diagnostics.error}</span></div>
  {:else if diagnostics.readiness}
    <section class="settings-section readiness-overview" aria-label="Readiness summary" data-testid="readiness-summary">
      <div class:ready={readinessSummary.state === 'pass'} class:warning={readinessSummary.state === 'warn'} class:blocked={readinessSummary.state === 'error'} class="readiness-summary-card">
        <span>Node health</span>
        <strong>{readinessSummary.label}</strong>
        <p>{readinessSummary.detail}</p>
      </div>
    </section>

    <section class="settings-section" aria-label="Configured paths and switches">
      <h3>Configured values</h3>
      <dl class="settings-grid">
        {#each settingsRows(diagnostics.readiness) as row}
          <div><dt>{row.label}</dt><dd>{row.value}</dd></div>
        {/each}
      </dl>
    </section>

    <section class="settings-section" aria-label="Readiness checks" data-testid="readiness-checks">
      <h3>Checks</h3>
      {#each diagnostics.readiness.checks ?? [] as check}
        <article class:ready={check.state === 'pass'} class:warning={check.state === 'warn'} class:blocked={check.state === 'error'} class="diagnostic-card">
          <div>
            <strong>{check.label}</strong>
            <span>{check.summary}</span>
          </div>
          <span class="status-pill">{checkStateLabel(check.state)}</span>
          {#if check.detail}
            <p>{check.detail}</p>
          {/if}
        </article>
      {/each}
    </section>

    {#if diagnostics.readiness.yt_dlp || diagnostics.readiness.storage}
      <section class="settings-section" aria-label="Tool and storage details">
        <h3>Runtime details</h3>
        {#if diagnostics.readiness.yt_dlp}
          <dl class="settings-grid compact">
            <div><dt>yt-dlp version</dt><dd>{diagnostics.readiness.yt_dlp.version || 'Not detected'}</dd></div>
            <div><dt>Minimum tested</dt><dd>{diagnostics.readiness.yt_dlp.minimum_tested_version}</dd></div>
            {#if diagnostics.readiness.yt_dlp.error}<div><dt>yt-dlp error</dt><dd>{diagnostics.readiness.yt_dlp.error}</dd></div>{/if}
          </dl>
        {/if}
        {#if diagnostics.readiness.storage}
          <dl class="settings-grid compact">
            <div><dt>Minimum free</dt><dd>{formatBytes(diagnostics.readiness.storage.minimum_free_bytes)}</dd></div>
            {#each diagnostics.readiness.storage.paths ?? [] as pathStatus}
              <div>
                <dt>{pathStatus.path}</dt>
                <dd>{formatBytes(pathStatus.available_bytes)} available{#if pathStatus.warning || pathStatus.error}<br />{pathStatus.warning || pathStatus.error}{/if}</dd>
              </div>
            {/each}
          </dl>
        {/if}
      </section>
    {/if}

    {#if diagnostics.readiness.storage_maintenance}
      <section class="settings-section" aria-label="Storage maintenance summary">
        <h3>Storage maintenance</h3>
        <dl class="settings-grid compact">
          {#each storageMaintenanceRows(diagnostics.readiness.storage_maintenance) as row}
            <div><dt>{row.label}</dt><dd>{row.value}</dd></div>
          {/each}
          <div><dt>Orphan files</dt><dd>{diagnostics.readiness.storage_maintenance.orphan_files || 0} files, {formatBytes(diagnostics.readiness.storage_maintenance.orphan_bytes)}</dd></div>
          <div><dt>Missing references</dt><dd>{diagnostics.readiness.storage_maintenance.missing_references || 0} metadata references</dd></div>
        </dl>
      </section>
    {/if}

    <section class="settings-section" aria-label="Copyable diagnostics">
      <div class="diagnostics-copy-heading">
        <h3>Diagnostics JSON</h3>
        <button class="refresh-diagnostics" type="button" onclick={copySettingsDiagnostics}>Copy diagnostics</button>
      </div>
      <p class="diagnostics-copy-note">Raw redacted settings JSON stays available for support without taking over the page.</p>
      <details class="diagnostics-raw">
        <summary>Show raw diagnostics JSON</summary>
        <textarea class="diagnostics-copy" data-testid="diagnostics-json" rows="12" readonly value={diagnosticsText} aria-label="Redacted settings diagnostics"></textarea>
      </details>
      {#if diagnosticsCopyStatus}<p class="copy-status" role="status">{diagnosticsCopyStatus}</p>{/if}
    </section>
  {:else}
    <div class="state compact">Readiness has not been checked yet.</div>
  {/if}
</section>
