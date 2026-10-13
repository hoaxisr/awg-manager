<script lang="ts">
  import { m } from '$lib/i18n';
  import { Modal, Button } from '$lib/components/ui';
  import { Globe, Shield, Sparkles, Check, ChevronDown, ChevronUp } from 'lucide-svelte';
  import type {
    MihomoNativeRuleProvider,
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeSubscription,
  } from '$lib/types';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';

  interface Props {
    open: boolean;
    provider?: MihomoNativeRuleProvider | null;
    groups?: MihomoNativeGroup[];
    proxies?: MihomoNativeProxy[];
    subscriptions?: MihomoNativeSubscription[];
    onClose: () => void;
    onSaved: () => void;
  }

  let {
    open,
    provider = null,
    groups = [],
    proxies = [],
    subscriptions = [],
    onClose,
    onSaved,
  }: Props = $props();

  interface PresetItem {
    id: string;
    title: string;
    description: string;
    icon: string;
    category: 'popular' | 'media' | 'bypass' | 'security';
    behavior: 'classical' | 'domain' | 'ipcidr';
    format: 'yaml' | 'text' | 'mrs';
    url: string;
    interval: number;
    recommendedProxy: 'DIRECT' | 'VPN';
  }

  const PRESETS: PresetItem[] = $derived([
    {
      id: 'rkn-antizapret',
      title: m.mihomo_prov_preset_antizapret_title(),
      description: m.mihomo_prov_preset_antizapret_desc(),
      icon: '🛡️',
      category: 'bypass',
      behavior: 'domain',
      format: 'text',
      url: 'https://raw.githubusercontent.com/runetfreedom/russia-v2ray-rules-dat/release/antizapret-domain.txt',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'youtube',
      title: 'YouTube & Google Video',
      description: m.mihomo_prov_preset_youtube_desc(),
      icon: '▶️',
      category: 'media',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/youtube.mrs',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'openai',
      title: 'ChatGPT & OpenAI',
      description: m.mihomo_prov_preset_openai_desc(),
      icon: '🤖',
      category: 'popular',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/openai.mrs',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'meta-social',
      title: 'Instagram & Facebook (Meta)',
      description: m.mihomo_prov_preset_meta_desc(),
      icon: '📸',
      category: 'bypass',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/meta.mrs',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'telegram-cidr',
      title: m.mihomo_prov_preset_telegram_title(),
      description: m.mihomo_prov_preset_telegram_desc(),
      icon: '✈️',
      category: 'popular',
      behavior: 'ipcidr',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geoip/telegram.mrs',
      interval: 86400,
      recommendedProxy: 'DIRECT',
    },
    {
      id: 'discord',
      title: 'Discord',
      description: m.mihomo_prov_preset_discord_desc(),
      icon: '💬',
      category: 'media',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/discord.mrs',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'tiktok',
      title: 'TikTok',
      description: m.mihomo_prov_preset_tiktok_desc(),
      icon: '🎵',
      category: 'media',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/tiktok.mrs',
      interval: 86400,
      recommendedProxy: 'VPN',
    },
    {
      id: 'category-ru',
      title: m.mihomo_prov_preset_ru_title(),
      description: m.mihomo_prov_preset_ru_desc(),
      icon: '🇷🇺',
      category: 'bypass',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/category-ru.mrs',
      interval: 86400,
      recommendedProxy: 'DIRECT',
    },
    {
      id: 'adaway',
      title: m.mihomo_prov_preset_adaway_title(),
      description: m.mihomo_prov_preset_adaway_desc(),
      icon: '🚫',
      category: 'security',
      behavior: 'domain',
      format: 'mrs',
      url: 'https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/category-ads-all.mrs',
      interval: 86400,
      recommendedProxy: 'DIRECT',
    },
  ]);

  let id = $state('');
  let name = $state('');
  let type = $state<'http' | 'file'>('http');
  let url = $state('');
  let path = $state('');
  let behavior = $state<'classical' | 'domain' | 'ipcidr'>('classical');
  let format = $state<'yaml' | 'text' | 'mrs'>('yaml');
  let interval = $state(86400);
  let proxy = $state('');
  let enabled = $state(true);
  let saving = $state(false);

  // Manual advanced fields accordion
  let showAdvanced = $state(false);

  const proxyOutboundGroupLabels = $derived([
    m.routing_group_system(),
    m.mihomo_group_label_groups(),
    m.mihomo_group_label_subs(),
    m.mihomo_group_label_proxies(),
    m.mihomo_group_label_awg(),
    m.mihomo_group_label_sys_wg(),
    m.mihomo_group_label_sb_tunnels(),
  ]);

  // List of outbound tunnels for downloading rule provider
  const availableProxyOutbounds = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string }> = [
      { value: '', label: m.mihomo_prov_proxy_default(), group: m.routing_group_system() },
      { value: 'DIRECT', label: m.mihomo_prov_proxy_direct(), group: m.routing_group_system() },
    ];

    // Groups
    for (const g of groups) {
      if (g.enabled) list.push({ value: g.name, label: `${g.name} (${g.type})`, group: m.mihomo_group_label_groups() });
    }

    // Subscriptions
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(i => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: m.mihomo_group_label_subs() });
      }
    }

    // Standalone Proxies
    for (const p of proxies) {
      if (p.enabled && !list.some(i => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: m.mihomo_group_label_proxies() });
      }
    }

    // AWG / WG tunnels
    const awgList = $awgTagsStore.data ?? [];
    for (const t of awgList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.label ? `${t.label} (${t.tag})` : t.tag,
          group: t.kind === 'system' ? m.mihomo_group_label_sys_wg() : m.mihomo_group_label_awg(),
        });
      }
    }

    // Singbox tunnels
    const sbTunList = $singboxTunnels.data ?? [];
    for (const t of sbTunList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.kernelInterface ? `${t.tag} (${t.kernelInterface})` : t.tag,
          group: m.mihomo_group_label_sb_tunnels(),
        });
      }
    }

    return list;
  });

  $effect(() => {
    if (open) {
      if (provider) {
        id = provider.id;
        name = provider.name;
        type = provider.type;
        url = provider.url || '';
        path = provider.path || '';
        behavior = provider.behavior || 'classical';
        format = provider.format || 'yaml';
        interval = provider.interval || 86400;
        proxy = provider.proxy || '';
        enabled = provider.enabled ?? true;
        showAdvanced = true;
      } else {
        id = '';
        name = '';
        type = 'http';
        url = '';
        path = '';
        behavior = 'domain';
        format = 'mrs';
        interval = 86400;
        proxy = '';
        enabled = true;
        showAdvanced = false;
      }
    }
  });

  function applyPreset(p: PresetItem) {
    name = p.id;
    type = 'http';
    url = p.url;
    behavior = p.behavior;
    format = p.format;
    interval = p.interval;
    // Auto-select proxy if user wants VPN and one is available
    if (p.recommendedProxy === 'VPN' && !proxy) {
      const firstGroup = groups.find(g => g.enabled);
      if (firstGroup) {
        proxy = firstGroup.name;
      } else {
        const firstAwg = ($awgTagsStore.data ?? [])[0];
        if (firstAwg) proxy = firstAwg.tag;
      }
    } else if (p.recommendedProxy === 'DIRECT') {
      proxy = 'DIRECT';
    }
  }

  async function handleSave() {
    if (saving) return;
    if (!name.trim()) {
      notifications.error(m.mihomo_prov_err_name_required());
      return;
    }
    if (type === 'http' && !url.trim()) {
      notifications.error(m.mihomo_prov_err_url_required());
      return;
    }
    if (type === 'file' && !path.trim()) {
      notifications.error(m.mihomo_prov_err_path_required());
      return;
    }
    saving = true;
    try {
      await api.mihomoNativeSaveRuleProvider({
        id: id || undefined,
        name: name.trim(),
        type,
        url: type === 'http' ? url.trim() : undefined,
        path: type === 'file' ? path.trim() : undefined,
        behavior,
        format,
        interval,
        proxy: type === 'http' && proxy.trim() ? proxy.trim() : undefined,
        enabled,
      });
      notifications.success(m.mihomo_prov_saved());
      onSaved();
      onClose();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.mihomo_prov_save_error());
    } finally {
      saving = false;
    }
  }
</script>

<Modal {open} title={provider ? m.mihomo_prov_modal_edit({ name: provider.name }) : m.mihomo_prov_modal_create()} onclose={onClose} size="lg">
  <div class="form-stack">
    {#if !provider}
      <div class="preset-section">
        <div class="section-header">
          <div class="header-left">
            <Sparkles size={16} class="accent-icon" />
            <span class="section-title">{m.mihomo_prov_catalog_title()}</span>
          </div>
          <span class="section-desc">{m.mihomo_prov_catalog_desc()}</span>
        </div>

        <div class="presets-grid">
          {#each PRESETS as p}
            {@const isSelected = name === p.id && url === p.url}
            <button
              type="button"
              class="preset-card"
              class:selected={isSelected}
              onclick={() => applyPreset(p)}
            >
              <div class="card-top">
                <span class="preset-emoji">{p.icon}</span>
                <span class="preset-name">{p.title}</span>
                {#if isSelected}
                  <Check size={14} class="check-icon" />
                {/if}
              </div>
              <div class="preset-sub">{p.description}</div>
              <div class="preset-badges">
                <span class="mini-badge">{p.format.toUpperCase()}</span>
                <span class="mini-badge">{p.behavior}</span>
              </div>
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <div class="card-box">
      <div class="two-col">
        <label class="form-group">
          <span class="label-text">{m.mihomo_prov_name_lbl()} <span class="req">*</span></span>
          <input type="text" bind:value={name} placeholder="youtube, openai, antizapret..." class="text-input" />
        </label>

        <label class="form-group">
          <span class="label-text">{m.mihomo_prov_source_type_lbl()}</span>
          <select bind:value={type} class="select-input">
            <option value="http">{m.mihomo_prov_source_http()}</option>
            <option value="file">{m.mihomo_prov_source_file()}</option>
          </select>
        </label>
      </div>

      {#if type === 'http'}
        <label class="form-group mt-3">
          <span class="label-text">{m.mihomo_prov_url_lbl()} <span class="req">*</span></span>
          <input type="text" bind:value={url} placeholder="https://raw.githubusercontent.com/.../ruleset.mrs" class="text-input font-mono" />
        </label>

        <!-- Proxy / Route selection for downloading the provider -->
        <div class="download-route-card mt-3">
          <div class="route-header">
            <div class="route-title">
              <Globe size={15} class="route-icon" />
              <span>{m.mihomo_prov_route_title()}</span>
            </div>
            <span class="route-tip">{m.mihomo_prov_route_tip()}</span>
          </div>

          <div class="proxy-selector-row">
            <select bind:value={proxy} class="select-input proxy-select">
              {#each proxyOutboundGroupLabels as grp}
                {@const items = availableProxyOutbounds.filter(o => o.group === grp)}
                {#if items.length > 0}
                  <optgroup label={grp}>
                    {#each items as opt}
                      <option value={opt.value}>{opt.label}</option>
                    {/each}
                  </optgroup>
                {/if}
              {/each}
            </select>
          </div>

          <div class="proxy-hint">
            {#if !proxy || proxy === 'DIRECT'}
              <span>{m.mihomo_prov_route_hint_direct()}</span>
            {:else}
              <span class="vpn-hint">{m.mihomo_prov_route_hint_vpn({ proxy })}</span>
            {/if}
          </div>
        </div>
      {:else}
        <label class="form-group mt-3">
          <span class="label-text">{m.mihomo_prov_path_lbl()} <span class="req">*</span></span>
          <input type="text" bind:value={path} placeholder="/opt/etc/awg-manager/rules.yaml" class="text-input font-mono" />
        </label>
      {/if}
    </div>

    <!-- Advanced Settings Toggle -->
    <div class="advanced-section">
      <button
        type="button"
        class="advanced-toggle-btn"
        onclick={() => (showAdvanced = !showAdvanced)}
      >
        <span>{m.mihomo_prov_advanced_toggle()}</span>
        {#if showAdvanced}
          <ChevronUp size={16} />
        {:else}
          <ChevronDown size={16} />
        {/if}
      </button>

      {#if showAdvanced}
        <div class="advanced-panel">
          <div class="two-col">
            <label class="form-group">
              <span class="label-text">{m.mihomo_prov_behavior_lbl()}</span>
              <select bind:value={behavior} class="select-input">
                <option value="domain">{m.mihomo_prov_behavior_domain()}</option>
                <option value="ipcidr">{m.mihomo_prov_behavior_ipcidr()}</option>
                <option value="classical">{m.mihomo_prov_behavior_classical()}</option>
              </select>
            </label>

            <label class="form-group">
              <span class="label-text">{m.mihomo_prov_format_lbl()}</span>
              <select bind:value={format} class="select-input">
                <option value="mrs">{m.mihomo_prov_format_mrs()}</option>
                <option value="text">{m.mihomo_prov_format_text()}</option>
                <option value="yaml">{m.mihomo_prov_format_yaml()}</option>
              </select>
            </label>
          </div>

          {#if type === 'http'}
            <label class="form-group mt-3">
              <span class="label-text">{m.mihomo_prov_interval_lbl()}</span>
              <div class="interval-row">
                <input type="number" min="300" max="604800" bind:value={interval} class="text-input" />
                <span class="interval-hint">
                  {interval === 86400 ? m.mihomo_prov_interval_24h() : interval === 43200 ? m.mihomo_prov_interval_12h() : m.mihomo_prov_interval_hours({ count: Math.round(interval / 3600) })}
                </span>
              </div>
            </label>
          {/if}
        </div>
      {/if}
    </div>

    <label class="check-label">
      <input type="checkbox" bind:checked={enabled} />
      <span>{m.mihomo_prov_enabled_lbl()}</span>
    </label>
  </div>

  {#snippet actions()}
    <Button variant="ghost" onclick={onClose} disabled={saving}>{m.common_cancel()}</Button>
    <Button variant="primary" loading={saving} onclick={handleSave}>{m.common_save()}</Button>
  {/snippet}
</Modal>

<style>
  .form-stack {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 4px 0;
  }

  .preset-section {
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 8px;
    padding: 12px;
  }

  .section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 10px;
    flex-wrap: wrap;
    gap: 4px;
  }

  .header-left {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .section-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .section-desc {
    font-size: 11px;
    color: var(--text-muted, #71717a);
  }

  :global(.accent-icon) {
    color: var(--accent, #3b82f6);
  }

  .presets-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 8px;
  }

  .preset-card {
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    padding: 8px 10px;
    text-align: left;
    cursor: pointer;
    transition: all 0.15s ease;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .preset-card:hover {
    border-color: var(--accent, #3b82f6);
    transform: translateY(-1px);
    background: rgba(59, 130, 246, 0.05);
  }

  .preset-card.selected {
    border-color: var(--accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
  }

  .card-top {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .preset-emoji {
    font-size: 14px;
  }

  .preset-name {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary, #fff);
    flex: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  :global(.check-icon) {
    color: var(--accent, #3b82f6);
    flex-shrink: 0;
  }

  .preset-sub {
    font-size: 11px;
    color: var(--text-muted, #71717a);
    line-height: 1.3;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .preset-badges {
    display: flex;
    gap: 4px;
    margin-top: 2px;
  }

  .mini-badge {
    font-size: 9px;
    font-family: var(--font-mono, monospace);
    background: rgba(255, 255, 255, 0.05);
    padding: 1px 4px;
    border-radius: 3px;
    color: var(--text-secondary, #a1a1aa);
  }

  .card-box {
    background: var(--bg-secondary, #121217);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 8px;
    padding: 12px;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }

  .req {
    color: #ef4444;
  }

  .mt-3 {
    margin-top: 10px;
  }

  .label-text {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary, #9ba1a6);
  }

  .select-input, .text-input {
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    padding: 7px 10px;
    color: var(--text-primary, #fff);
    font-size: 13px;
    outline: none;
  }

  .select-input:focus, .text-input:focus {
    border-color: var(--accent, #3b82f6);
  }

  .two-col {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }

  .download-route-card {
    background: rgba(59, 130, 246, 0.04);
    border: 1px solid rgba(59, 130, 246, 0.2);
    border-radius: 6px;
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .route-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .route-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  :global(.route-icon) {
    color: var(--accent, #3b82f6);
  }

  .route-tip {
    font-size: 11px;
    color: var(--text-muted, #71717a);
  }

  .proxy-select {
    width: 100%;
  }

  .proxy-hint {
    font-size: 11px;
    color: var(--text-muted, #9ca3af);
  }

  .advanced-section {
    border-top: 1px dashed var(--border, #2e2e38);
    padding-top: 8px;
  }

  .advanced-toggle-btn {
    width: 100%;
    display: flex;
    justify-content: space-between;
    align-items: center;
    background: none;
    border: none;
    font-size: 12px;
    color: var(--text-secondary, #9ba1a6);
    cursor: pointer;
    padding: 4px 0;
  }

  .advanced-toggle-btn:hover {
    color: var(--text-primary, #fff);
  }

  .advanced-panel {
    margin-top: 10px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .interval-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .interval-hint {
    font-size: 12px;
    color: var(--text-secondary, #9ba1a6);
  }

  .font-mono {
    font-family: var(--font-mono, monospace);
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-primary, #fff);
    cursor: pointer;
    margin-top: 2px;
  }
</style>
