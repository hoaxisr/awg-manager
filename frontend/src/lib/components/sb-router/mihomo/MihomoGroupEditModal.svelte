<script lang="ts">
  import { Modal, Button } from '$lib/components/ui';
  import { ChevronUp, ChevronDown, X, Zap, Disc, Shield, ArrowDownUp, AlertTriangle } from 'lucide-svelte';
  import type { MihomoNativeGroup, MihomoNativeProxy, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { awgTags } from '$lib/stores/awgTags';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { subscriptionsStore } from '$lib/stores/subscriptions';

  interface Props {
    open: boolean;
    group?: MihomoNativeGroup | null;
    groups?: MihomoNativeGroup[];
    proxies?: MihomoNativeProxy[];
    subscriptions?: MihomoNativeSubscription[];
    runtimeProxies?: MihomoRuntimeProxy[];
    onClose: () => void;
    onSaved: () => void;
    onDelete?: (id: string) => void;
  }

  let {
    open,
    group = null,
    groups = [],
    proxies = [],
    subscriptions = [],
    runtimeProxies = [],
    onClose,
    onSaved,
    onDelete,
  }: Props = $props();

  let id = $state('');
  let name = $state('');
  let type = $state<MihomoNativeGroup['type']>('url-test');
  let selectedProxies = $state<string[]>([]);
  let url = $state('https://www.gstatic.com/generate_204');
  let interval = $state(300);
  let tolerance = $state(50);
  let lazy = $state(true);
  let strategy = $state<MihomoNativeGroup['strategy']>('consistent-hashing');
  let enabled = $state(true);
  let saving = $state(false);

  $effect(() => {
    if (open) {
      if (group) {
        id = group.id;
        name = group.name;
        type = group.type;
        selectedProxies = [...(group.proxies || [])];
        url = group.url || 'https://www.gstatic.com/generate_204';
        interval = group.interval || 300;
        tolerance = group.tolerance || 50;
        lazy = group.lazy ?? true;
        strategy = group.strategy || 'consistent-hashing';
        enabled = group.enabled ?? true;
      } else {
        id = '';
        name = '';
        type = 'url-test';
        selectedProxies = [];
        url = 'https://www.gstatic.com/generate_204';
        interval = 300;
        tolerance = 50;
        lazy = true;
        strategy = 'consistent-hashing';
        enabled = true;
      }
    }
  });

  const availableMembers = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string }> = [
      { value: 'DIRECT', label: 'DIRECT (Прямое подключение)', group: 'Системные' },
      { value: 'REJECT', label: 'REJECT (Блокировать)', group: 'Системные' },
    ];

    // AWG and Native WireGuard tunnels
    const awgList = $awgTags.data ?? [];
    for (const t of awgList) {
      const gName = t.kind === 'managed'
        ? 'AWG туннели (Kernel)'
        : (t.kind === 'system' ? 'Системные WireGuard (Native)' : 'AWG3 туннели');
      if (!list.some((i) => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.iface ? `${t.label} (${t.iface})` : t.label,
          group: gName,
        });
      }
    }

    // Native Mihomo proxies
    for (const p of proxies) {
      if (p.enabled && !list.some((i) => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: 'Нативные прокси Mihomo' });
      }
    }

    // Native Mihomo subscriptions
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some((i) => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: 'Подписки Mihomo' });
      }
    }

    // Sing-box tunnels
    const sbTunList = $singboxTunnels.data ?? [];
    for (const t of sbTunList) {
      if (!list.some((i) => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag,
          group: 'Sing-box туннели',
        });
      }
    }

    // Sing-box subscriptions
    const subList = $subscriptionsStore.data ?? [];
    const knownSubTags = new Set<string>();
    for (const s of subList) {
      if (!s.enabled) continue;
      const subTag = s.selectorTag || s.id;
      const subLabel = s.label || subTag;
      if (subTag) {
        knownSubTags.add(subTag);
        if (s.members) {
          for (const m of s.members) {
            if (m.tag) knownSubTags.add(m.tag);
          }
        }
        if (!list.some((i) => i.value === subTag)) {
          list.push({
            value: subTag,
            label: `${subLabel} (${subTag})`,
            group: 'Подписки Sing-box',
          });
        }
      }
    }

    // Sing-box standalone proxies (excluding subscription outbounds)
    const sbProxiesList = $singboxProxies.data ?? [];
    for (const p of sbProxiesList) {
      if (knownSubTags.has(p.tag) || p.tag.startsWith('sub-')) continue;
      if (!list.some((i) => i.value === p.tag)) {
        list.push({
          value: p.tag,
          label: p.tag,
          group: 'Прокси Sing-box',
        });
      }
    }

    // Mihomo Native Groups (nested groups, excluding current group to avoid cycles)
    const allGroups = groups;
    for (const g of allGroups) {
      if (g.enabled !== false && g.name && g.name !== name && g.id !== id && !list.some((i) => i.value === g.name)) {
        list.push({
          value: g.name,
          label: `${g.name} (${g.type.toUpperCase()})`,
          group: 'Группы прокси Mihomo',
        });
      }
    }

    // Runtime proxies
    for (const rp of runtimeProxies) {
      if (
        rp.name !== 'GLOBAL' &&
        rp.name !== name &&
        !rp.name.startsWith('sub-') &&
        !rp.name.startsWith('mnp-') &&
        !allGroups.some((g) => g.name === rp.name) &&
        !list.some((i) => i.value === rp.name)
      ) {
        list.push({ value: rp.name, label: rp.name, group: 'Runtime узлы' });
      }
    }

    return list;
  });

  const groupedMembers = $derived.by(() => {
    const map: Record<string, Array<{ value: string; label: string; group: string }>> = {};
    for (const item of availableMembers) {
      if (!map[item.group]) map[item.group] = [];
      map[item.group].push(item);
    }
    return map;
  });

  function moveProxy(idx: number, delta: number) {
    const nextIdx = idx + delta;
    if (nextIdx < 0 || nextIdx >= selectedProxies.length) return;
    const copy = [...selectedProxies];
    const [moved] = copy.splice(idx, 1);
    copy.splice(nextIdx, 0, moved);
    selectedProxies = copy;
  }

  function removeProxy(val: string) {
    selectedProxies = selectedProxies.filter((p) => p !== val);
  }

  async function handleSave() {
    if (saving) return;
    if (!name.trim()) {
      notifications.error('Укажите название группы');
      return;
    }
    if (selectedProxies.length === 0) {
      notifications.error('Выберите хотя бы один сервер или туннель для группы');
      return;
    }
    saving = true;
    try {
      await api.mihomoNativeSaveGroup({
        id: id || undefined,
        name: name.trim(),
        type,
        proxies: selectedProxies,
        use: [],
        url,
        interval,
        tolerance,
        lazy,
        strategy: type === 'load-balance' ? strategy : undefined,
        enabled,
      });
      notifications.success('Группа прокси сохранена');
      onSaved();
      onClose();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения группы');
    } finally {
      saving = false;
    }
  }
</script>

<Modal {open} title={group ? `Редактирование группы: ${group.name}` : 'Новая группа прокси'} onclose={onClose} size="lg">
  <div class="form-stack">
    <label class="form-group">
      <span class="label-text">Название группы</span>
      <input type="text" bind:value={name} placeholder="Например: Fast-Proxy или Media" class="text-input" />
    </label>

    <div class="form-group">
      <span class="label-text">Тип группы</span>
      <div class="type-selector-grid">
        <button
          type="button"
          class="type-select-card"
          class:selected={type === 'url-test'}
          onclick={() => (type = 'url-test')}
        >
          <div class="type-card-icon url-test"><Zap size={16} /></div>
          <div class="type-card-info">
            <span class="type-card-name">url-test</span>
            <span class="type-card-desc">Авто-выбор самого быстрого</span>
          </div>
        </button>

        <button
          type="button"
          class="type-select-card"
          class:selected={type === 'select'}
          onclick={() => (type = 'select')}
        >
          <div class="type-card-icon select"><Disc size={16} /></div>
          <div class="type-card-info">
            <span class="type-card-name">select</span>
            <span class="type-card-desc">Ручной выбор активного узла</span>
          </div>
        </button>

        <button
          type="button"
          class="type-select-card"
          class:selected={type === 'fallback'}
          onclick={() => (type = 'fallback')}
        >
          <div class="type-card-icon fallback"><Shield size={16} /></div>
          <div class="type-card-info">
            <span class="type-card-name">fallback</span>
            <span class="type-card-desc">Отказоустойчивый резерв</span>
          </div>
        </button>

        <button
          type="button"
          class="type-select-card"
          class:selected={type === 'load-balance'}
          onclick={() => (type = 'load-balance')}
        >
          <div class="type-card-icon load-balance"><ArrowDownUp size={16} /></div>
          <div class="type-card-info">
            <span class="type-card-name">load-balance</span>
            <span class="type-card-desc">Балансировка нагрузки</span>
          </div>
        </button>
      </div>

      {#if type === 'url-test'}
        <div class="type-explanation-box">
          <div class="type-explanation-header">
            <Zap size={14} class="type-icon-accent" />
            <span class="type-explanation-title">URL-Test (Автовыбор самого быстрого)</span>
          </div>
          <p class="type-explanation-desc">
            Mihomo регулярно проверяет задержку серверов и направляет трафик через узел с наименьшим откликом.
          </p>
          <div class="type-scenario">
            <span class="scenario-badge good">Рекомендуется</span>
            <span>Для веб-серфинга, YouTube и стриминговых сервисов, где важна минимальная задержка.</span>
          </div>
        </div>
      {:else if type === 'select'}
        <div class="type-explanation-box">
          <div class="type-explanation-header">
            <Disc size={14} class="type-icon-accent" />
            <span class="type-explanation-title">Select (Ручной выбор)</span>
          </div>
          <p class="type-explanation-desc">
            Трафик направляется строго в тот узел, который вы вручную выбрали в карточке группы.
          </p>
          <div class="type-scenario">
            <span class="scenario-badge good">Рекомендуется</span>
            <span>Для онлайн-банкинга, криптобирж и задач, требующих постоянного неизменного IP-адреса.</span>
          </div>
        </div>
      {:else if type === 'fallback'}
        <div class="type-explanation-box">
          <div class="type-explanation-header">
            <Shield size={14} class="type-icon-accent" />
            <span class="type-explanation-title">Fallback (Отказоустойчивый резерв)</span>
          </div>
          <p class="type-explanation-desc">
            Трафик всегда идет через первый узел в списке, переключаясь на резервный только при его недоступности.
          </p>
          <div class="type-scenario">
            <span class="scenario-badge good">Рекомендуется</span>
            <span>Для каскадов туннелей (AWG → Sing-box → DIRECT) и максимальной отказоустойчивости.</span>
          </div>
        </div>
      {:else if type === 'load-balance'}
        <div class="type-explanation-box warning-box">
          <div class="type-explanation-header warning">
            <AlertTriangle size={14} class="warning-icon" />
            <span class="type-explanation-title warning">Load-Balance (Балансировка нагрузки)</span>
          </div>
          <p class="type-explanation-desc">
            Mihomo распределяет каждое новое соединение между всеми серверами группы.
          </p>
          <div class="warning-notice">
            <span class="scenario-badge warn">Предостережение</span>
            <span><strong>Не подходит для YouTube, онлайн-кинотеатров и авторизованных сайтов:</strong> потоки разбиваются на чанки, и смена IP между запросами приводит к остановке видео (ошибка 403 / буферизация).</span>
          </div>
          <div class="type-scenario mt-2">
            <span class="scenario-badge info">Подходит для</span>
            <span>Многопоточных загрузок файлов, торрент-клиентов и распределения общего объема данных.</span>
          </div>
        </div>
      {/if}
    </div>

    <div class="form-group">
      <span class="label-text">Выбор участников (Серверы / Туннели / Подписки)</span>
      <div class="members-picker">
        {#each Object.entries(groupedMembers) as [categoryName, members]}
          <div class="category-header">{categoryName}</div>
          {#each members as member}
            <label class="member-check">
              <input
                type="checkbox"
                checked={selectedProxies.includes(member.value)}
                onchange={(e) => {
                  if (e.currentTarget.checked) selectedProxies = [...selectedProxies, member.value];
                  else selectedProxies = selectedProxies.filter((m) => m !== member.value);
                }}
              />
              <span class="member-name">{member.label}</span>
            </label>
          {/each}
        {/each}
      </div>
    </div>

    {#if selectedProxies.length > 0}
      <div class="form-group">
        <span class="label-text">
          {type === 'fallback'
            ? 'Порядок приоритета (1-й — основной, далее по очереди при сбое):'
            : 'Порядок участников в группе:'}
        </span>
        <div class="order-list">
          {#each selectedProxies as proxyVal, idx (proxyVal)}
            {@const member = availableMembers.find((m) => m.value === proxyVal)}
            {@const label = member?.label || proxyVal}
            <div class="order-item">
              <span class="order-idx">{idx + 1}</span>
              <span class="order-name">{label}</span>
              {#if type === 'fallback'}
                {#if idx === 0}
                  <span class="priority-badge primary">Основной</span>
                {:else}
                  <span class="priority-badge backup">Резерв #{idx}</span>
                {/if}
              {/if}
              <div class="order-btn-group">
                <button
                  type="button"
                  class="order-btn"
                  disabled={idx === 0}
                  onclick={() => moveProxy(idx, -1)}
                  title="Поднять выше (выше приоритет)"
                >
                  <ChevronUp size={13} />
                </button>
                <button
                  type="button"
                  class="order-btn"
                  disabled={idx === selectedProxies.length - 1}
                  onclick={() => moveProxy(idx, 1)}
                  title="Опустить ниже (ниже приоритет)"
                >
                  <ChevronDown size={13} />
                </button>
                <button
                  type="button"
                  class="order-btn remove"
                  onclick={() => removeProxy(proxyVal)}
                  title="Удалить из группы"
                >
                  <X size={13} />
                </button>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if type === 'url-test' || type === 'fallback'}
      <div class="test-settings-grid">
        <label class="form-group">
          <span class="label-text">URL проверки задержки</span>
          <input type="text" bind:value={url} class="text-input" />
        </label>

        <div class="two-col">
          <label class="form-group">
            <span class="label-text">Интервал теста (сек)</span>
            <input type="number" min="10" max="86400" bind:value={interval} class="text-input" />
          </label>

          {#if type === 'url-test'}
            <label class="form-group">
              <span class="label-text">Толерантность (мс)</span>
              <input type="number" min="0" max="1000" bind:value={tolerance} class="text-input" />
            </label>
          {/if}
        </div>
      </div>
    {/if}

    {#if type === 'load-balance'}
      <label class="form-group">
        <span class="label-text">Стратегия балансировки</span>
        <select bind:value={strategy} class="select-input">
          <option value="consistent-hashing">Consistent Hashing (По хэшу источника/назначения)</option>
          <option value="round-robin">Round Robin (Поочерёдно)</option>
          <option value="sticky-sessions">Sticky Sessions (Привязка к сессии)</option>
        </select>
      </label>
    {/if}

    <div class="checkbox-row">
      {#if type === 'url-test' || type === 'fallback'}
        <label class="check-label">
          <input type="checkbox" bind:checked={lazy} />
          <span>Lazy (тестировать только когда группа используется трафиком)</span>
        </label>
      {/if}
      <label class="check-label">
        <input type="checkbox" bind:checked={enabled} />
        <span>Группа включена</span>
      </label>
    </div>
  </div>

  {#snippet actions()}
    {#if group?.id && onDelete}
      <Button
        variant="danger"
        onclick={() => {
          const gid = group.id;
          onClose();
          onDelete(gid);
        }}
        disabled={saving}
      >
        Удалить группу
      </Button>
    {/if}
    <Button variant="ghost" onclick={onClose} disabled={saving}>Отмена</Button>
    <Button variant="primary" loading={saving} onclick={handleSave}>Сохранить</Button>
  {/snippet}
</Modal>

<style>
  .form-stack {
    display: flex;
    flex-direction: column;
    gap: 16px;
    padding: 8px 0;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .label-text {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-secondary, #9ba1a6);
  }

  .select-input, .text-input {
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    padding: 8px 12px;
    color: var(--text-primary, #fff);
    font-size: 14px;
    outline: none;
  }

  .select-input:focus, .text-input:focus {
    border-color: var(--accent, #3b82f6);
  }

  .type-selector-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }

  @media (max-width: 520px) {
    .type-selector-grid {
      grid-template-columns: 1fr;
    }
  }

  .type-select-card {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-radius: var(--radius-sm, 6px);
    border: 1px solid var(--color-border, var(--border, #cbd5e1));
    background: var(--color-bg-secondary, var(--bg-secondary, #f8fafc));
    color: var(--color-text-primary, var(--text-primary, #0f172a));
    cursor: pointer;
    text-align: left;
    transition: all var(--t-fast, 0.15s);
  }

  .type-select-card:hover {
    border-color: var(--color-accent, var(--accent, #3b82f6));
    background: var(--color-bg-hover, var(--bg-hover, #f1f5f9));
  }

  .type-select-card.selected {
    border-color: var(--color-accent, var(--accent, #3b82f6));
    background: rgba(59, 130, 246, 0.08);
    box-shadow: 0 0 0 1px var(--color-accent, var(--accent, #3b82f6));
  }

  :global(.dark) .type-select-card {
    background: #1e293b;
    border-color: #334155;
    color: #f8fafc;
  }

  :global(.dark) .type-select-card:hover {
    background: #334155;
    border-color: var(--color-accent, var(--accent, #3b82f6));
  }

  :global(.dark) .type-select-card.selected {
    background: rgba(59, 130, 246, 0.15);
    border-color: var(--color-accent, var(--accent, #3b82f6));
  }

  .type-card-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 6px;
    background: var(--color-bg-tertiary, var(--bg-tertiary, #ffffff));
    color: var(--color-accent, var(--accent, #3b82f6));
    border: 1px solid var(--color-border, var(--border, #cbd5e1));
    flex-shrink: 0;
  }

  :global(.dark) .type-card-icon {
    background: #0f172a;
    border-color: #475569;
  }

  .type-card-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .type-card-name {
    font-size: 12px;
    font-weight: 700;
    line-height: 1.2;
    text-transform: uppercase;
    letter-spacing: 0.02em;
    font-family: var(--font-mono, monospace);
  }

  .type-card-desc {
    font-size: 11px;
    color: var(--color-text-muted, var(--text-muted, #64748b));
    line-height: 1.2;
    margin-top: 2px;
  }

  .members-picker {
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-height: 220px;
    overflow-y: auto;
    padding: 8px 10px;
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    background: var(--bg-tertiary, #18181f);
  }

  .category-header {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--accent, #3b82f6);
    padding: 6px 4px 2px;
    margin-top: 4px;
    border-bottom: 1px solid var(--border, #2e2e38);
  }

  .category-header:first-child {
    margin-top: 0;
    padding-top: 2px;
  }

  .member-check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text-primary, #fff);
    cursor: pointer;
    padding: 4px;
    border-radius: 4px;
  }

  .member-check:hover {
    background: rgba(255, 255, 255, 0.04);
  }

  .member-name {
    font-family: inherit;
    font-size: 13px;
  }

  .order-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    padding: 6px;
    max-height: 180px;
    overflow-y: auto;
  }

  .order-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 8px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 4px;
    font-size: 13px;
  }

  .order-idx {
    font-family: var(--font-mono, monospace);
    font-size: 11px;
    color: var(--text-muted, #71717a);
    width: 16px;
    text-align: center;
  }

  .order-name {
    flex: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-primary, #fff);
  }

  .priority-badge {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    padding: 2px 6px;
    border-radius: 4px;
    letter-spacing: 0.04em;
  }

  .priority-badge.primary {
    background: rgba(59, 130, 246, 0.15);
    color: #60a5fa;
    border: 1px solid rgba(59, 130, 246, 0.3);
  }

  .priority-badge.backup {
    background: rgba(161, 161, 170, 0.15);
    color: #a1a1aa;
    border: 1px solid rgba(161, 161, 170, 0.3);
  }

  .order-btn-group {
    display: flex;
    align-items: center;
    gap: 2px;
  }

  .order-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 22px;
    height: 22px;
    border-radius: 4px;
    background: transparent;
    border: 1px solid var(--border, #2e2e38);
    color: var(--text-secondary, #9ba1a6);
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .order-btn:hover:not(:disabled) {
    background: rgba(255, 255, 255, 0.08);
    color: var(--text-primary, #fff);
  }

  .order-btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }

  .order-btn.remove:hover {
    background: rgba(239, 68, 68, 0.2);
    color: #ef4444;
    border-color: rgba(239, 68, 68, 0.4);
  }

  .test-settings-grid {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 8px;
  }

  .two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }

  .checkbox-row {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 4px;
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text-secondary, #9ba1a6);
    cursor: pointer;
  }

  .check-label span {
    color: var(--text-primary, #fff);
  }

  .type-explanation-box {
    margin-top: 10px;
    padding: 10px 12px;
    border-radius: var(--radius-md, 8px);
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 12.5px;
    line-height: 1.45;
  }

  .type-explanation-box.warning-box {
    border-color: rgba(217, 119, 6, 0.35);
    background: rgba(217, 119, 6, 0.05);
  }

  .type-explanation-header {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .type-explanation-header.warning {
    color: var(--color-warning, #d97706);
  }

  :global(.type-icon-accent) {
    color: var(--accent, #3b82f6);
    flex-shrink: 0;
  }

  :global(.warning-icon) {
    color: var(--color-warning, #d97706);
    flex-shrink: 0;
  }

  .type-explanation-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #ffffff);
  }

  .type-explanation-title.warning {
    color: var(--color-warning, #d97706);
  }

  .type-explanation-desc {
    margin: 0;
    color: var(--text-secondary, #9ba1a6);
    font-size: 12.5px;
  }

  .type-scenario,
  .warning-notice {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: 12px;
    color: var(--text-secondary, #9ba1a6);
    line-height: 1.4;
  }

  .warning-notice {
    color: var(--text-primary, #ffffff);
  }

  .scenario-badge {
    display: inline-block;
    padding: 1px 6px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 600;
    white-space: nowrap;
    flex-shrink: 0;
    line-height: 1.3;
  }

  .scenario-badge.good {
    background: rgba(34, 197, 94, 0.12);
    color: #22c55e;
    border: 1px solid rgba(34, 197, 94, 0.25);
  }

  .scenario-badge.warn {
    background: rgba(239, 68, 68, 0.12);
    color: #ef4444;
    border: 1px solid rgba(239, 68, 68, 0.25);
  }

  .scenario-badge.info {
    background: rgba(59, 130, 246, 0.12);
    color: #3b82f6;
    border: 1px solid rgba(59, 130, 246, 0.25);
  }

  .mt-2 {
    margin-top: 2px;
  }
</style>
