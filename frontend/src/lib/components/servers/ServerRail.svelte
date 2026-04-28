<script lang="ts" module>
  export interface RailItem {
    id: string;
    name: string;
    iface: string;
    listenPort: number | string;
    status: 'running' | 'stopped' | 'unknown';
    peerActive?: number;
    peerCount?: number;
    kind: 'managed' | 'system';
  }
</script>

<script lang="ts">
  interface Props {
    items: RailItem[];
    activeId: string;
    onSelect: (id: string) => void;
    onCreate?: () => void;
  }

  let { items, activeId, onSelect, onCreate }: Props = $props();

  let mobileOpen = $state(false);
  const active = $derived(items.find((i) => i.id === activeId) ?? items[0]);
</script>

<aside class="rail" aria-label="Список серверов">
  <header class="rail-header">
    <span class="label">Серверы ({items.length})</span>
  </header>

  {#each items as item (item.id)}
    {@const isActive = item.id === activeId}
    <button
      type="button"
      class="item"
      class:active={isActive}
      onclick={() => onSelect(item.id)}
      aria-current={isActive ? 'true' : undefined}
    >
      <span class="item-title">
        <span class="led led-{item.status}"></span>
        <span class="item-name">{item.name}</span>
      </span>
      <span class="item-meta">
        <span class="iface">{item.iface}{item.listenPort ? `:${item.listenPort}` : ''}</span>
        {#if item.peerCount !== undefined && item.peerCount > 0}
          <span class="dot">·</span>
          <span>{item.peerActive ?? 0}/{item.peerCount} peers</span>
        {/if}
      </span>
    </button>
  {/each}

  {#if onCreate}
    <button type="button" class="create-btn" onclick={onCreate}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
      Новый сервер
    </button>
  {/if}
</aside>

<!-- Mobile selector — replaces the rail below 768px -->
<div class="mobile-selector">
  <button type="button" class="mobile-trigger" onclick={() => (mobileOpen = !mobileOpen)} aria-expanded={mobileOpen}>
    <span class="item-title">
      <span class="led led-{active?.status ?? 'unknown'}"></span>
      <span class="item-name">{active?.name ?? '—'}</span>
    </span>
    <span class="chevron" class:open={mobileOpen}>▾</span>
  </button>
  {#if mobileOpen}
    <div class="mobile-list" role="menu">
      {#each items as item (item.id)}
        <button
          type="button"
          class="item"
          class:active={item.id === activeId}
          onclick={() => { onSelect(item.id); mobileOpen = false; }}
        >
          <span class="item-title">
            <span class="led led-{item.status}"></span>
            <span class="item-name">{item.name}</span>
          </span>
          <span class="item-meta">
            <span class="iface">{item.iface}{item.listenPort ? `:${item.listenPort}` : ''}</span>
          </span>
        </button>
      {/each}
      {#if onCreate}
        <button type="button" class="create-btn" onclick={() => { onCreate?.(); mobileOpen = false; }}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
          Новый сервер
        </button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .rail {
    width: 240px;
    flex-shrink: 0;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius);
    padding: 8px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    align-self: flex-start;
    position: sticky;
    top: 1rem;
  }

  .rail-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 8px 8px;
  }

  .label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--color-text-muted);
  }

  .item {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 10px;
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    cursor: pointer;
    text-align: left;
    color: var(--color-text-primary);
    font: inherit;
    transition: background var(--t-fast) ease, border-color var(--t-fast) ease;
  }

  .item:hover {
    background: var(--color-bg-hover);
  }

  .item.active {
    background: var(--color-accent-tint);
    border-color: var(--color-accent-border);
  }

  .item-title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    font-weight: 500;
  }

  .item-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-meta {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11px;
    color: var(--color-text-muted);
    font-family: var(--font-mono);
  }

  .iface {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .dot { opacity: 0.5; }

  .led {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .led-running {
    background: var(--color-success);
    box-shadow: 0 0 6px var(--color-success);
  }
  .led-stopped,
  .led-unknown {
    background: var(--color-text-muted);
  }

  .create-btn {
    margin-top: 6px;
    padding: 8px 10px;
    background: transparent;
    border: 1px dashed var(--color-border);
    border-radius: var(--radius-sm);
    color: var(--color-text-muted);
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-family: inherit;
  }
  .create-btn:hover {
    border-color: var(--color-accent);
    color: var(--color-accent);
  }
  .create-btn svg { width: 12px; height: 12px; }

  /* ── Mobile selector ── */
  .mobile-selector {
    display: none;
    position: relative;
  }

  .mobile-trigger {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 12px;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    color: var(--color-text-primary);
    cursor: pointer;
    font: inherit;
  }

  .chevron {
    color: var(--color-text-muted);
    transition: transform var(--t-fast) ease;
  }
  .chevron.open {
    transform: rotate(180deg);
  }

  .mobile-list {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    padding: 6px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    z-index: 10;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.2);
  }

  @media (max-width: 768px) {
    .rail { display: none; }
    .mobile-selector { display: block; }
  }
</style>
