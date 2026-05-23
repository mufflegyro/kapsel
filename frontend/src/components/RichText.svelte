<script>
  import { onDestroy, onMount, tick } from 'svelte';
  import { richTextBlocks } from '../display.js';

  export let text = '';
  export let collapsible = false;
  export let collapsedMaxHeight = '25vh';
  export let onTimestamp = null;

  let contentElement;
  let expanded = false;
  let hasOverflow = false;
  let lastText = text;
  let measureToken = 0;
  let syncToken = 0;

  $: blocks = richTextBlocks(text);
  $: collapsed = collapsible && hasOverflow && !expanded;
  $: if (text !== lastText) {
    lastText = text;
    expanded = false;
  }
  $: if (contentElement) {
    blocks;
    collapsible;
    collapsedMaxHeight;
    void scheduleMeasure();
  }
  $: if (contentElement) {
    collapsed;
    void scheduleLinkTabSync();
  }

  onMount(() => {
    window.addEventListener('resize', measureOverflow);
    measureOverflow();
  });

  onDestroy(() => {
    window.removeEventListener('resize', measureOverflow);
  });

  async function scheduleMeasure() {
    const token = ++measureToken;
    await tick();
    if (token === measureToken) measureOverflow();
  }

  function measureOverflow() {
    if (!contentElement || !collapsible) {
      hasOverflow = false;
      void scheduleLinkTabSync();
      return;
    }
    hasOverflow = contentElement.scrollHeight > collapsedPixelLimit() + 1;
    void scheduleLinkTabSync();
  }

  async function scheduleLinkTabSync() {
    const token = ++syncToken;
    await tick();
    if (token === syncToken) syncLinkTabStops();
  }

  function syncLinkTabStops() {
    if (!contentElement) return;
    const controls = contentElement.querySelectorAll('a, button');
    if (!collapsed) {
      for (const control of controls) {
        control.removeAttribute('tabindex');
        control.inert = false;
      }
      return;
    }

    const cutoff = contentElement.getBoundingClientRect().top + contentElement.clientHeight;
    for (const control of controls) {
      const bounds = control.getBoundingClientRect();
      if (bounds.bottom > cutoff - 4) {
        control.tabIndex = -1;
        control.inert = true;
      } else {
        control.removeAttribute('tabindex');
        control.inert = false;
      }
    }
  }

  function collapsedPixelLimit() {
    if (collapsedMaxHeight.endsWith('vh')) {
      return window.innerHeight * (Number.parseFloat(collapsedMaxHeight) / 100);
    }
    if (collapsedMaxHeight.endsWith('px')) return Number.parseFloat(collapsedMaxHeight);

    return window.innerHeight * 0.25;
  }
</script>

{#snippet tokenContent(token)}
  {#if token.href && !token.href.startsWith('mailto:')}
    <a href={token.href} target="_blank" rel="noopener noreferrer">{token.text}<span class="visually-hidden"> opens in a new tab</span></a>
  {:else if token.href}
    <a href={token.href}>{token.text}</a>
  {:else if token.seconds !== undefined && typeof onTimestamp === 'function'}
    <button class="timestamp-link" type="button" aria-label={`Seek to ${token.text}`} onclick={() => onTimestamp(token.seconds)}>{token.text}</button>
  {:else}
    {token.text}
  {/if}
{/snippet}

<div bind:this={contentElement} class:collapsible class:collapsed class="rich-text" style={collapsible ? `--rich-text-collapsed-max: ${collapsedMaxHeight};` : ''}>
  {#each blocks as block}
    {#if block.type === 'heading'}
      <h2>{block.text}</h2>
    {:else if block.type === 'list'}
      <ul>
        {#each block.items as item}
          <li>{#each item as token}{@render tokenContent(token)}{/each}</li>
        {/each}
      </ul>
    {:else}
      <p>{#each block.lines as line, index}{#if index > 0}<br />{/if}{#each line as token}{@render tokenContent(token)}{/each}{/each}</p>
    {/if}
  {/each}
</div>
{#if collapsible && hasOverflow}
  <button class="rich-text-toggle" type="button" aria-expanded={expanded} onclick={() => { expanded = !expanded; }}>{expanded ? 'Show less' : 'Show more'}</button>
{/if}
