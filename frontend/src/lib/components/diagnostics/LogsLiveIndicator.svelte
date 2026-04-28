<script lang="ts">
  import { Badge, StatusDot } from '$lib/components/ui';

  interface Props {
    paused: boolean;
    bufferCount: number;
    entries: number;
    onResume?: () => void;
  }

  let { paused, bufferCount, entries, onResume }: Props = $props();
</script>

<div class="indicator">
  {#if paused}
    <Badge variant="warning" size="sm">PAUSED</Badge>
    {#if bufferCount > 0}
      <button type="button" class="buffer-chip" onclick={onResume}>
        +{bufferCount} {bufferCount === 1 ? 'новая' : 'новых'} ↑
      </button>
    {/if}
  {:else}
    <span class="live-badge">
      <StatusDot variant="success" pulse size="sm" />
      <Badge variant="success" size="sm">LIVE</Badge>
    </span>
  {/if}
  <span class="entries-count">{entries} {entries === 1 ? 'запись' : 'записей'}</span>
</div>

<style>
  .indicator {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 12px;
  }

  .live-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }

  .buffer-chip {
    background: var(--color-accent);
    color: white;
    border: none;
    border-radius: var(--radius-pill);
    padding: 0.125rem 0.625rem;
    font: inherit;
    font-size: 11px;
    font-weight: 600;
    cursor: pointer;
    transition: filter var(--t-fast) ease;
  }

  .buffer-chip:hover {
    filter: brightness(1.1);
  }

  .entries-count {
    color: var(--color-text-muted);
    font-variant-numeric: tabular-nums;
  }
</style>
