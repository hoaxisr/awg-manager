<script lang="ts">
  import { onMount } from 'svelte';
  import { Modal, Button } from '$lib/components/ui';
  import { AlertTriangle } from 'lucide-svelte';
  import type { MihomoNativeRule, MihomoNativeGroup, MihomoNativeProxy, MihomoNativeSubscription, MihomoNativeRuleProvider } from '$lib/types';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import { formatOutboundHumanName } from '$lib/utils/outboundHumanName';

  interface Props {
    open: boolean;
    rule?: MihomoNativeRule | null;
    isFallback?: boolean;
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    ruleProviders: MihomoNativeRuleProvider[];
    onClose: () => void;
    onSaved: () => void;
  }

  let {
    open,
    rule = null,
    isFallback = false,
    groups = [],
    proxies = [],
    subscriptions = [],
    ruleProviders = [],
    onClose,
    onSaved,
  }: Props = $props();

  let id = $state('');
  let type = $state('DOMAIN-SUFFIX');
  let payload = $state('');
  let outbound = $state('DIRECT');
  let noResolve = $state(false);
  let enabled = $state(true);
  let saving = $state(false);

  const selectedGroupObj = $derived(groups.find((g) => g.name === outbound));
  const isLoadBalanceSelected = $derived(selectedGroupObj?.type === 'load-balance');

  const RULE_TYPE_GROUPS = [
    {
      group: 'Домены и категории',
      items: [
        { value: 'GEOSITE', label: 'GEOSITE (Категория geosite.dat, напр. youtube)' },
        { value: 'DOMAIN-SUFFIX', label: 'DOMAIN-SUFFIX (Домен и поддомены, напр. google.com)' },
        { value: 'DOMAIN', label: 'DOMAIN (Точный домен, напр. example.com)' },
        { value: 'DOMAIN-KEYWORD', label: 'DOMAIN-KEYWORD (Ключевое слово, напр. google)' },
        { value: 'DOMAIN-WILDCARD', label: 'DOMAIN-WILDCARD (Маска, напр. *.google.*)' },
        { value: 'DOMAIN-REGEX', label: 'DOMAIN-REGEX (Регулярное выражение)' },
      ]
    },
    {
      group: 'IP-адреса и сети',
      items: [
        { value: 'IP-CIDR', label: 'IP-CIDR (IPv4/IPv6 подсеть, напр. 91.108.4.0/22)' },
        { value: 'IP-CIDR6', label: 'IP-CIDR6 (IPv6 подсеть)' },
        { value: 'GEOIP', label: 'GEOIP (Страна по базе geoip.dat, напр. RU, US)' },
        { value: 'IP-ASN', label: 'IP-ASN (Номер автономной системы, напр. 13335)' },
      ]
    },
    {
      group: 'Наборы правил и прочие',
      items: [
        { value: 'RULE-SET', label: 'RULE-SET (Внешний rule-provider)' },
        { value: 'MATCH', label: 'MATCH (Все остальные запросы / Default)' },
      ]
    }
  ];

  $effect(() => {
    if (open) {
      if (isFallback) {
        id = rule?.id || '';
        type = 'MATCH';
        payload = '';
        outbound = rule?.outbound || groups[0]?.name || 'DIRECT';
        noResolve = false;
        enabled = true;
      } else if (rule) {
        id = rule.id;
        type = rule.type;
        payload = rule.payload || '';
        outbound = rule.outbound;
        noResolve = rule.noResolve ?? false;
        enabled = rule.enabled ?? true;
      } else {
        id = '';
        type = 'GEOSITE';
        payload = '';
        outbound = groups[0]?.name || 'DIRECT';
        noResolve = false;
        enabled = true;
      }
    }
  });

  const availableOutbounds = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string }> = [
      { value: 'DIRECT', label: 'DIRECT (Прямое подключение / мимо VPN)', group: 'Системные' },
      { value: 'REJECT', label: 'REJECT (Блокировать)', group: 'Системные' },
    ];

    // Groups
    for (const g of groups) {
      if (g.enabled) list.push({ value: g.name, label: `${g.name} (${g.type})`, group: 'Группы прокси Mihomo' });
    }

    // Native Mihomo subscriptions
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(i => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: 'Подписки Mihomo' });
      }
    }

    // Native Mihomo standalone proxies
    for (const p of proxies) {
      if (p.enabled && !list.some(i => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: 'Прокси Mihomo' });
      }
    }

    // AWG & WireGuard tunnels (NuxtAWG, SW, Wireguard3, etc.)
    const awgList = $awgTagsStore.data ?? [];
    for (const t of awgList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.label ? `${t.label} (${t.tag})` : t.tag,
          group: t.kind === 'system' ? 'Системные WireGuard' : 'AWG туннели',
        });
      }
    }

    // Sing-box phase 1 tunnels
    const sbTunList = $singboxTunnels.data ?? [];
    for (const t of sbTunList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag,
          group: 'Туннели Sing-box',
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
        if (!list.some(i => i.value === subTag)) {
          list.push({
            value: subTag,
            label: `${subLabel} (${subTag})`,
            group: 'Подписки Sing-box',
          });
        }
      }
    }

    // Sing-box standalone proxies
    const sbProxiesList = $singboxProxies.data ?? [];
    for (const p of sbProxiesList) {
      if (knownSubTags.has(p.tag) || p.tag.startsWith('sub-')) continue;
      if (!list.some(i => i.value === p.tag)) {
        list.push({
          value: p.tag,
          label: p.tag,
          group: 'Прокси Sing-box',
        });
      }
    }

    return list;
  });

  async function handleSave() {
    if (saving) return;
    if (type !== 'MATCH' && !payload.trim()) {
      notifications.error('Укажите значение (Payload) для правила');
      return;
    }
    saving = true;
    try {
      await api.mihomoNativeSaveRule({
        id: id || undefined,
        type,
        payload: type === 'MATCH' ? '' : payload.trim(),
        outbound,
        noResolve,
        enabled,
      });
      notifications.success('Правило сохранено и применено');
      onSaved();
      onClose();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения правила');
    } finally {
      saving = false;
    }
  }
</script>

<Modal
  {open}
  title={isFallback ? 'Маршрут по умолчанию (Если ничего не подошло)' : rule ? 'Редактирование правила Mihomo' : 'Новое правило Mihomo'}
  onclose={onClose}
  size="md"
>
  <div class="form-stack">
    {#if isFallback}
      <p class="modal-hint">
        Куда направлять трафик, если ни одно из настроенных правил маршрутизации не подошло:
      </p>
    {:else}
      {#if !rule}
        <div class="quick-templates">
          <span class="quick-label">Каталог популярных сервисов:</span>
          <div class="chips-row">
            {#each [
              { label: 'YouTube', t: 'GEOSITE', p: 'youtube' },
              { label: 'Telegram', t: 'GEOSITE', p: 'telegram' },
              { label: 'Discord', t: 'GEOSITE', p: 'discord' },
              { label: 'Instagram', t: 'GEOSITE', p: 'instagram' },
              { label: 'Twitter / X', t: 'GEOSITE', p: 'twitter' },
              { label: 'ChatGPT', t: 'GEOSITE', p: 'openai' },
              { label: 'TikTok', t: 'GEOSITE', p: 'tiktok' },
              { label: 'Spotify', t: 'GEOSITE', p: 'spotify' },
              { label: 'Twitch', t: 'GEOSITE', p: 'twitch' },
              { label: 'Steam', t: 'GEOSITE', p: 'steam' },
              { label: 'RuTracker', t: 'DOMAIN-SUFFIX', p: 'rutracker.org' },
            ] as item}
              <button
                type="button"
                class="chip-btn"
                class:active={type === item.t && payload === item.p}
                onclick={() => {
                  type = item.t;
                  payload = item.p;
                }}
              >
                {item.label}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      <label class="form-group">
        <span class="label-text">Тип правила</span>
        <select bind:value={type} class="select-input">
          {#each RULE_TYPE_GROUPS as grp}
            <optgroup label={grp.group}>
              {#each grp.items as it}
                <option value={it.value}>{it.label}</option>
              {/each}
            </optgroup>
          {/each}
        </select>
      </label>

      {#if type !== 'MATCH'}
        <label class="form-group">
          <span class="label-text">Значение (Payload)</span>
          {#if type === 'RULE-SET' && ruleProviders.length > 0}
            <select bind:value={payload} class="select-input">
              <option value="">-- Выберите провайдер правил --</option>
              {#each ruleProviders as rp}
                <option value={rp.name}>{rp.name} ({rp.behavior})</option>
              {/each}
            </select>
          {:else}
            <input
              type="text"
              bind:value={payload}
              placeholder={type === 'IP-CIDR' ? '91.108.4.0/22' : type === 'GEOSITE' ? 'youtube' : 'example.com'}
              class="text-input"
            />
          {/if}
        </label>
      {/if}
    {/if}

    <label class="form-group">
      <span class="label-text">Назначение (Целевой выход)</span>
      <select bind:value={outbound} class="select-input">
        {#each availableOutbounds as ob}
          <option value={ob.value}>{ob.label}</option>
        {/each}
      </select>
    </label>

    {#if isLoadBalanceSelected}
      <div class="load-balance-warning">
        <div class="warning-header">
          <AlertTriangle size={14} class="warning-icon" />
          <span class="warning-title">Балансировщик нагрузки ({outbound})</span>
        </div>
        <p class="warning-desc">
          Группа распределяет соединения между разными IP-адресами. Это может вызывать разрыв воспроизведения <strong>YouTube и стримингов</strong>, а также сброс авторизаций на сайтах. Для видео рекомендуется группа <strong>url-test</strong> или <strong>fallback</strong>.
        </p>
      </div>
    {/if}

    {#if !isFallback}
      <div class="checkbox-row">
        <label class="check-label">
          <input type="checkbox" bind:checked={noResolve} />
          <span>no-resolve (не разрешать DNS при сопоставлении IP-правил)</span>
        </label>
        <label class="check-label">
          <input type="checkbox" bind:checked={enabled} />
          <span>Правило включено</span>
        </label>
      </div>
    {/if}
  </div>

  {#snippet actions()}
    <Button variant="ghost" onclick={onClose} disabled={saving}>Отмена</Button>
    <Button variant="primary" loading={saving} onclick={handleSave}>Сохранить</Button>
  {/snippet}
</Modal>

<style>
  .modal-hint {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--text-secondary);
    line-height: 1.4;
  }
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

  .load-balance-warning {
    background: rgba(217, 119, 6, 0.05);
    border: 1px solid rgba(217, 119, 6, 0.35);
    border-radius: var(--radius-md, 8px);
    padding: 8px 12px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 12px;
    line-height: 1.4;
  }

  .warning-header {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  :global(.warning-icon) {
    color: var(--color-warning, #d97706);
    flex-shrink: 0;
  }

  .warning-title {
    color: var(--color-warning, #d97706);
    font-weight: 600;
    font-size: 12.5px;
  }

  .warning-desc {
    margin: 0;
    color: var(--text-secondary, #9ba1a6);
  }

  .quick-templates {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px;
    background: var(--bg-tertiary, rgba(255, 255, 255, 0.03));
    border: 1px solid var(--border, rgba(255, 255, 255, 0.08));
    border-radius: var(--radius-md, 8px);
  }

  .quick-label {
    font-size: 11.5px;
    font-weight: 500;
    color: var(--text-muted, #71767b);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }

  .chips-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .chip-btn {
    padding: 4px 9px;
    font-size: 12px;
    font-weight: 500;
    border-radius: 6px;
    background: var(--bg-secondary, rgba(255, 255, 255, 0.05));
    color: var(--text-secondary, #cbd5e1);
    border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .chip-btn:hover {
    background: var(--bg-hover, rgba(255, 255, 255, 0.1));
    color: var(--text-primary, #fff);
    border-color: var(--border-hover, rgba(255, 255, 255, 0.2));
  }

  .chip-btn.active {
    background: rgba(59, 130, 246, 0.2);
    color: #60a5fa;
    border-color: rgba(59, 130, 246, 0.5);
  }
</style>
