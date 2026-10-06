<script lang="ts">
  import { Modal, Button } from '$lib/components/ui';
  import { Search, Globe, Check, Layers, Sparkles } from 'lucide-svelte';
  import { ServiceIcon } from '$lib/components/dnsroutes';
  import { presetCatalog } from '$lib/stores/presets';
  import { singboxRouterCatalogPresetFilter } from '$lib/utils/catalog-preset';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import type {
    CatalogPreset,
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeSubscription,
    MihomoNativeRuleProvider
  } from '$lib/types';

  interface Props {
    open: boolean;
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    existingProviders: MihomoNativeRuleProvider[];
    onClose: () => void;
    onAdded: () => void;
  }

  let {
    open = false,
    groups = [],
    proxies = [],
    subscriptions = [],
    existingProviders = [],
    onClose,
    onAdded
  }: Props = $props();

  let query = $state('');
  let categoryFilter = $state<string>('all');
  let selected = $state<Set<string>>(new Set());
  let selectedProxy = $state<string>('');
  let createRouteRules = $state<boolean>(true);
  let targetOutbound = $state<string>('');
  let adding = $state(false);

  const CATEGORY_LABELS: Record<string, string> = {
    all: 'Все',
    social: 'Соцсети',
    media: 'Медиа',
    ai: 'AI',
    developer: 'Разработка',
    cloud: 'Облако',
    gaming: 'Игры',
    block: 'Блокировки'
  };

  // Collect available outbounds / proxies for downloading providers
  const availableDownloadRoutes = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string }> = [
      { value: 'DIRECT', label: 'DIRECT (Напрямую через провайдера)', group: 'Системные' }
    ];

    for (const g of groups) {
      if (g.enabled) {
        list.push({ value: g.name, label: `${g.name} (${g.type})`, group: 'Группы Mihomo' });
      }
    }

    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(i => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: 'Подписки Mihomo' });
      }
    }

    const awgList = $awgTagsStore.data ?? [];
    for (const t of awgList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.label ? `${t.label} (${t.tag})` : t.tag,
          group: t.kind === 'system' ? 'Системные WireGuard' : 'AWG туннели'
        });
      }
    }

    const sbTunList = $singboxTunnels.data ?? [];
    for (const t of sbTunList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag,
          group: 'Туннели Sing-box'
        });
      }
    }

    for (const p of proxies) {
      if (p.enabled && !list.some(i => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: 'Прокси узлы' });
      }
    }

    return list;
  });

  // Target outbounds / groups for routing rule
  const availableTargetOutbounds = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string }> = [
      { value: 'DIRECT', label: 'DIRECT (Напрямую в интернет)', group: 'Системные' },
      { value: 'REJECT', label: 'REJECT (Блокировать трафик)', group: 'Системные' }
    ];

    for (const g of groups) {
      if (g.enabled) {
        list.push({ value: g.name, label: `${g.name} (${g.type})`, group: 'Группы Mihomo' });
      }
    }

    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(i => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: 'Подписки Mihomo' });
      }
    }

    const awgList = $awgTagsStore.data ?? [];
    for (const t of awgList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.label ? `${t.label} (${t.tag})` : t.tag,
          group: t.kind === 'system' ? 'Системные WireGuard' : 'AWG туннели'
        });
      }
    }

    const sbTunList = $singboxTunnels.data ?? [];
    for (const t of sbTunList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag,
          group: 'Туннели Sing-box'
        });
      }
    }

    for (const p of proxies) {
      if (p.enabled && !list.some(i => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: 'Прокси узлы' });
      }
    }

    return list;
  });

  $effect(() => {
    if (!targetOutbound && availableTargetOutbounds.length > 0) {
      // Pick first user group, subscription or tunnel if available, else first item
      const preferred = availableTargetOutbounds.find(
        (o) => o.group === 'Группы Mihomo' || o.group === 'Подписки Mihomo' || o.group === 'AWG туннели' || o.group === 'Туннели Sing-box'
      );
      targetOutbound = preferred ? preferred.value : availableTargetOutbounds[0].value;
    }
  });

  // Extract preset items from catalog
  interface CatalogItem {
    id: string;
    name: string;
    geoName: string;
    category: string;
    description?: string;
    mrsUrl: string;
  }

  const items = $derived.by(() => {
    const allPresets = $presetCatalog.filter(p => singboxRouterCatalogPresetFilter(p));
    const list: CatalogItem[] = [];

    for (const p of allPresets) {
      const sb = p.engines?.singbox;
      if (!sb || !sb.ruleSets || sb.ruleSets.length === 0) continue;

      for (const rs of sb.ruleSets) {
        const geoName = rs.tag.replace(/^geosite-/, '');
        // MetaCubeX high-performance MRS geosite file
        const mrsUrl = `https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/${geoName}.mrs`;
        list.push({
          id: `${p.id}_${geoName}`,
          name: p.name,
          geoName,
          category: p.category || 'other',
          description: p.notice || '',
          mrsUrl
        });
      }
    }

    // Sort alphabetically
    return list.sort((a, b) => a.name.localeCompare(b.name, 'ru'));
  });

  const existingGeoNames = $derived(
    new Set(existingProviders.map(p => p.name.toLowerCase().trim()))
  );

  const filteredItems = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return items.filter(it => {
      if (categoryFilter !== 'all' && it.category !== categoryFilter) return false;
      if (q) {
        const hay = `${it.name} ${it.geoName} ${it.category}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  });

  function toggleSelect(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }

  function selectAllFiltered() {
    const next = new Set(selected);
    for (const it of filteredItems) {
      if (!existingGeoNames.has(it.geoName.toLowerCase())) {
        next.add(it.id);
      }
    }
    selected = next;
  }

  function clearSelection() {
    selected = new Set();
  }

  async function handleBatchAdd() {
    if (selected.size === 0) return;
    adding = true;
    let addedCount = 0;
    let rulesCount = 0;

    try {
      for (const item of items) {
        if (!selected.has(item.id)) continue;
        if (existingGeoNames.has(item.geoName.toLowerCase())) continue;

        await api.mihomoNativeSaveRuleProvider({
          name: item.geoName,
          type: 'http',
          url: item.mrsUrl,
          path: `./rules/${item.geoName}.mrs`,
          behavior: 'domain',
          format: 'mrs',
          interval: 86400,
          proxy: selectedProxy || undefined,
          enabled: true
        });
        addedCount++;

        if (createRouteRules && targetOutbound) {
          await api.mihomoNativeSaveRule({
            type: 'RULE-SET',
            payload: item.geoName,
            outbound: targetOutbound,
            enabled: true
          });
          rulesCount++;
        }
      }

      if (rulesCount > 0) {
        notifications.success(`Добавлено наборов .mrs: ${addedCount}, создано правил: ${rulesCount} → ${targetOutbound}`);
      } else {
        notifications.success(`Добавлено наборов правил: ${addedCount}`);
      }
      selected = new Set();
      onAdded();
      onClose();
    } catch (err) {
      notifications.error(err instanceof Error ? err.message : 'Ошибка при добавлении наборов');
    } finally {
      adding = false;
    }
  }
</script>

<Modal {open} title="📚 Каталог наборов правил (Rule Providers)" onclose={onClose} size="xl">
  <div class="catalog-modal">
    <!-- Search and Categories Toolbar -->
    <div class="toolbar">
      <div class="search-input-wrap">
        <Search size={15} class="search-icon" />
        <input
          type="text"
          bind:value={query}
          placeholder="Поиск по названию или домену (youtube, discord, ai...)"
          class="search-input"
        />
        {#if query}
          <button type="button" class="clear-btn" onclick={() => (query = '')}>✕</button>
        {/if}
      </div>

      <div class="categories-bar">
        {#each Object.entries(CATEGORY_LABELS) as [catId, label]}
          <button
            type="button"
            class="cat-chip"
            class:active={categoryFilter === catId}
            onclick={() => (categoryFilter = catId)}
          >
            {label}
          </button>
        {/each}
      </div>
    </div>

    <!-- Route selectors & Action settings -->
    <div class="settings-bar">
      <!-- Download route selector -->
      <div class="setting-item">
        <div class="route-label">
          <Globe size={15} />
          <span>Маршрут скачивания (.mrs):</span>
        </div>
        <div class="route-select-wrap">
          <select bind:value={selectedProxy} class="route-select">
            {#each availableDownloadRoutes as item}
              <option value={item.value}>[{item.group}] {item.label}</option>
            {/each}
          </select>
        </div>
      </div>

      <!-- Auto routing rule option -->
      <div class="setting-item routing-rule-box">
        <label class="checkbox-label">
          <input type="checkbox" bind:checked={createRouteRules} />
          <span class="chk-text">Сразу создать правило маршрутизации:</span>
        </label>
        {#if createRouteRules}
          <div class="route-select-wrap target-select-wrap">
            <select bind:value={targetOutbound} class="route-select target-select">
              {#each availableTargetOutbounds as item}
                <option value={item.value}>[{item.group}] {item.label}</option>
              {/each}
            </select>
          </div>
        {/if}
      </div>

      <div class="selection-actions">
        <button type="button" class="link-btn" onclick={selectAllFiltered}>Выбрать все</button>
        {#if selected.size > 0}
          <button type="button" class="link-btn" onclick={clearSelection}>Сбросить ({selected.size})</button>
        {/if}
      </div>
    </div>

    <!-- Grid of Catalog Items -->
    <div class="items-grid">
      {#if filteredItems.length === 0}
        <div class="empty-hint">
          По запросу «{query}» ничего не найдено
        </div>
      {/if}

      {#each filteredItems as item}
        {@const isAdded = existingGeoNames.has(item.geoName.toLowerCase())}
        {@const isSelected = selected.has(item.id)}

        <button
          type="button"
          class="item-card"
          class:selected={isSelected}
          class:added={isAdded}
          disabled={isAdded}
          onclick={() => toggleSelect(item.id)}
        >
          <div class="card-left">
            <div class="card-check">
              {#if isAdded}
                <span class="added-badge">Уже в списке</span>
              {:else if isSelected}
                <div class="checkbox-circle checked">
                  <Check size={12} />
                </div>
              {:else}
                <div class="checkbox-circle"></div>
              {/if}
            </div>

            <div class="card-icon-wrap">
              <ServiceIcon name={item.name} size={32} />
            </div>
          </div>

          <div class="card-body">
            <span class="card-name">{item.name}</span>
            <span class="card-tag font-mono">geosite:{item.geoName}</span>
          </div>

          <div class="card-type-badge font-mono">.mrs</div>
        </button>
      {/each}
    </div>
  </div>

  {#snippet actions()}
    <div class="modal-footer-row">
      <span class="selected-summary">
        {#if selected.size > 0}
          Выбрано: <strong>{selected.size}</strong>
        {:else}
          Выберите наборы
        {/if}
      </span>
      <div class="btn-group">
        <Button variant="ghost" onclick={onClose} disabled={adding}>Отмена</Button>
        <Button
          variant="primary"
          onclick={handleBatchAdd}
          disabled={selected.size === 0 || adding}
          loading={adding}
        >
          {#snippet iconBefore()}
            <Sparkles size={14} />
          {/snippet}
          Добавить ({selected.size})
        </Button>
      </div>
    </div>
  {/snippet}
</Modal>

<style>
  .catalog-modal {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-height: 440px;
    max-height: 70vh;
  }

  .toolbar {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .search-input-wrap {
    position: relative;
    display: flex;
    align-items: center;
    width: 100%;
  }

  .search-input-wrap :global(.search-icon) {
    position: absolute;
    left: 12px;
    color: var(--text-muted, #94a3b8);
    pointer-events: none;
  }

  .search-input {
    width: 100%;
    padding: 10px 36px 10px 36px;
    background: var(--bg-surface, rgba(255, 255, 255, 0.04));
    border: 1px solid var(--border-color, rgba(255, 255, 255, 0.1));
    border-radius: 8px;
    color: var(--text-primary, #fff);
    font-size: 13px;
  }

  .search-input:focus {
    outline: none;
    border-color: var(--color-primary, #3b82f6);
  }

  .clear-btn {
    position: absolute;
    right: 10px;
    background: none;
    border: none;
    color: var(--text-muted, #94a3b8);
    cursor: pointer;
    font-size: 14px;
    padding: 4px;
  }

  .categories-bar {
    display: flex;
    align-items: center;
    gap: 6px;
    overflow-x: auto;
    padding-bottom: 4px;
  }

  .cat-chip {
    padding: 5px 12px;
    border-radius: 9999px;
    border: 1px solid var(--border-color, rgba(255, 255, 255, 0.08));
    background: var(--bg-surface, rgba(255, 255, 255, 0.02));
    color: var(--text-secondary, #94a3b8);
    font-size: 12px;
    cursor: pointer;
    white-space: nowrap;
    transition: all 0.15s ease;
  }

  .cat-chip:hover {
    background: rgba(255, 255, 255, 0.06);
    color: var(--text-primary, #fff);
  }

  .cat-chip.active {
    background: var(--color-primary, #3b82f6);
    border-color: var(--color-primary, #3b82f6);
    color: #fff;
    font-weight: 500;
  }

  .settings-bar {
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid var(--border-color, rgba(255, 255, 255, 0.06));
    border-radius: 8px;
    padding: 10px 12px;
  }

  .setting-item {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }

  .routing-rule-box {
    padding-top: 6px;
    border-top: 1px solid var(--border-color, rgba(255, 255, 255, 0.04));
  }

  .checkbox-label {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    user-select: none;
  }

  .chk-text {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary, #fff);
  }

  .target-select-wrap {
    min-width: 220px;
  }

  .target-select {
    border-color: rgba(59, 130, 246, 0.4);
    background: rgba(59, 130, 246, 0.05);
  }

  .route-label {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary, #cbd5e1);
  }

  .route-select-wrap {
    flex: 1;
    min-width: 200px;
  }

  .route-select {
    width: 100%;
    padding: 6px 10px;
    background: var(--bg-base, #111);
    border: 1px solid var(--border-color, #333);
    border-radius: 6px;
    color: var(--text-primary, #fff);
    font-size: 12px;
  }

  .selection-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 2px;
  }

  .link-btn {
    background: none;
    border: none;
    color: var(--color-primary, #60a5fa);
    font-size: 12px;
    cursor: pointer;
    padding: 2px 4px;
    text-decoration: underline;
  }

  .items-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 8px;
    overflow-y: auto;
    padding: 4px 2px;
    flex: 1;
  }

  .empty-hint {
    grid-column: 1 / -1;
    text-align: center;
    padding: 32px;
    color: var(--text-muted, #64748b);
    font-size: 13px;
  }

  .item-card {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    background: var(--bg-surface, rgba(255, 255, 255, 0.02));
    border: 1px solid var(--border-color, rgba(255, 255, 255, 0.08));
    border-radius: 8px;
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
  }

  .item-card:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.05);
    border-color: rgba(255, 255, 255, 0.16);
  }

  .item-card.selected {
    border-color: var(--color-primary, #3b82f6);
    background: rgba(59, 130, 246, 0.08);
  }

  .item-card.added {
    opacity: 0.55;
    cursor: default;
    background: rgba(255, 255, 255, 0.01);
  }

  .card-left {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }

  .card-icon-wrap {
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .checkbox-circle {
    width: 18px;
    height: 18px;
    border-radius: 4px;
    border: 1px solid var(--border-color, rgba(255, 255, 255, 0.25));
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .checkbox-circle.checked {
    background: var(--color-primary, #3b82f6);
    border-color: var(--color-primary, #3b82f6);
    color: #fff;
  }

  .added-badge {
    font-size: 10px;
    color: var(--text-muted, #94a3b8);
    background: rgba(255, 255, 255, 0.06);
    padding: 2px 6px;
    border-radius: 4px;
    white-space: nowrap;
  }

  .card-body {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }

  .card-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-primary, #fff);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .card-tag {
    font-size: 11px;
    color: var(--text-secondary, #94a3b8);
  }

  .card-type {
    font-size: 10px;
    color: var(--text-muted, #64748b);
  }

  .modal-footer-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
  }

  .selected-summary {
    font-size: 13px;
    color: var(--text-secondary, #94a3b8);
  }

  .selected-summary strong {
    color: var(--color-primary, #60a5fa);
  }

  .btn-group {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .font-mono {
    font-family: var(--font-mono, monospace);
  }
</style>
