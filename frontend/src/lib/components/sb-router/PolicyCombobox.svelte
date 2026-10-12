<!--
  Combobox выбора/создания NDMS access policy.
  Фильтрует существующие (api.singboxRouterListPolicies); если совпадения нет —
  предлагает создать «X» (api.singboxRouterCreatePolicy). Выбор/создание → onChange(name).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import type { RouterPolicy } from '$lib/types';

  interface Props {
    /** Текущее имя политики. */
    value: string;
    onChange: (name: string) => void;
  }
  let { value, onChange }: Props = $props();

  let policies = $state<RouterPolicy[]>([]);
  let query = $state('');
  let open = $state(false);
  let creating = $state(false);

  onMount(loadPolicies);
  async function loadPolicies() {
    try {
      policies = await api.singboxRouterListPolicies();
    } catch {
      policies = [];
    }
  }

  let filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return policies;
    return policies.filter(
      (p) => p.name.toLowerCase().includes(q) || (p.description ?? '').toLowerCase().includes(q),
    );
  });
  let trimmed = $derived(query.trim());
  let canCreate = $derived(trimmed.length > 0 && !policies.some((p) => p.name === trimmed));

  let currentPolicy = $derived(policies.find((p) => p.name === value));
  let currentLabel = $derived((currentPolicy?.description ?? '').trim());

  function select(name: string) {
    onChange(name);
    query = '';
    open = false;
  }
  async function create() {
    if (!trimmed || creating) return;
    creating = true;
    try {
      const created = await api.singboxRouterCreatePolicy(trimmed);
      await loadPolicies();
      select(created.name);
    } catch (e) {
      notifications.error(m.sb_router_policy_combo_create_failed({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      creating = false;
    }
  }
</script>

<div class="combo">
  {#if value}
    <div class="current">
      {m.sb_router_policy_combo_current()}
      {#if currentLabel && currentLabel !== value}
        <strong>{currentLabel}</strong> <span class="current-id">({value})</span>
      {:else}
        <strong>{value}</strong>
      {/if}
    </div>
  {/if}
  <div class="wrap">
    <input
      class="inp"
      type="text"
      bind:value={query}
      placeholder={m.sb_router_policy_combo_placeholder()}
      onfocus={() => (open = true)}
      onblur={() => setTimeout(() => (open = false), 150)}
    />
    {#if open}
      <div class="panel">
        {#each filtered as p (p.name)}
          <button type="button" class="opt" class:sel={p.name === value} onmousedown={() => select(p.name)}>
            <span class="opt-name">{p.description || p.name}</span>
            <span class="opt-meta">{p.mark ? `${p.mark} · ` : ''}{m.sb_router_policy_combo_devices({ count: p.deviceCount })}</span>
          </button>
        {/each}
        {#if canCreate}
          <button type="button" class="opt create" onmousedown={create} disabled={creating}>
            {creating ? m.sb_router_policy_combo_creating() : m.sb_router_policy_combo_create({ name: trimmed })}
          </button>
        {/if}
        {#if filtered.length === 0 && !canCreate}
          <div class="empty">{m.sb_router_policy_combo_empty()}</div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<style>
  .combo { display: flex; flex-direction: column; gap: 6px; }
  .current { font-size: 11.5px; color: var(--text-muted); }
  .current strong { color: var(--text-primary); }
  .current-id { font-family: var(--font-mono); color: var(--text-secondary); font-size: 11px; }
  .wrap { position: relative; }
  .inp {
    width: 100%; box-sizing: border-box;
    padding: 6px 10px; border-radius: var(--radius-sm); background: var(--bg-primary);
    border: 1px solid var(--border); color: var(--text-primary); font-size: 12.5px; font-family: inherit;
  }
  .panel {
    position: absolute; top: calc(100% + 4px); left: 0; right: 0; z-index: 20;
    max-height: 240px; overflow-y: auto;
    background: var(--bg-secondary); border: 1px solid var(--border); border-radius: var(--radius-sm);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
  }
  .opt {
    display: flex; align-items: center; justify-content: space-between; gap: 10px;
    width: 100%; text-align: left; padding: 8px 10px;
    background: transparent; border: 0; border-bottom: 1px solid var(--border);
    cursor: pointer; font-family: inherit; color: var(--text-primary); font-size: 12.5px;
  }
  .opt:last-child { border-bottom: 0; }
  .opt:hover:not(:disabled) { background: var(--bg-tertiary); }
  .opt.sel { background: var(--accent-soft); }
  .opt-meta { font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); flex-shrink: 0; }
  .opt.create { color: var(--accent); font-weight: 600; }
  .empty { padding: 8px 10px; font-size: 12px; color: var(--text-muted); font-style: italic; }
</style>
