<script lang="ts">
  import { m } from '$lib/i18n';
  import { Modal, Button } from '$lib/components/ui';
  import {
    ShieldCheck, Globe, Lock, Check, AlertTriangle, ArrowRight, Layers, Wifi, Radio
  } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { singboxTunnels } from '$lib/stores/singbox';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
  import type {
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeSubscription,
    MihomoNativeRule
  } from '$lib/types';

  interface Props {
    open: boolean;
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    currentRules?: MihomoNativeRule[];
    onClose: () => void;
    onApplied: () => void;
  }

  let {
    open,
    groups = [],
    proxies = [],
    subscriptions = [],
    onClose,
    onApplied
  }: Props = $props();

  type TemplateId = 'smart_bypass' | 'whitelist_mobile' | 'russia_direct_world_vpn' | 'full_tunnel';

  let selectedTemplate = $state<TemplateId>('smart_bypass');
  let selectedOutbound = $state<string>('');
  let replaceExisting = $state(true);
  let applying = $state(false);

  // Collect all usable VPN / Proxy outbounds
  const availableOutbounds = $derived.by(() => {
    const list: Array<{ value: string; label: string; group: string; groupKey: string }> = [];

    // Proxy groups (Fastest, Select, Fallback)
    for (const g of groups) {
      if (g.enabled) {
        list.push({
          value: g.name,
          label: `${g.name} (${g.type})`,
          group: m.mihomo_group_label_groups(),
          groupKey: 'groups'
        });
      }
    }

    // Native Mihomo subscriptions
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(i => i.value === s.groupName)) {
        list.push({ value: s.groupName, label: s.name, group: m.mihomo_group_label_subs(), groupKey: 'subs' });
      }
    }

    // AWG and WireGuard tunnels
    const awgList = $awgTagsStore.data ?? [];
    for (const t of awgList) {
      if (!list.some(i => i.value === t.tag)) {
        list.push({
          value: t.tag,
          label: t.label ? `${t.label} (${t.tag})` : t.tag,
          group: t.kind === 'system' ? m.mihomo_group_label_sys_wg() : m.mihomo_group_label_awg(),
          groupKey: t.kind === 'system' ? 'sys_wg' : 'awg'
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
          groupKey: 'sb_tunnels'
        });
      }
    }

    // Mihomo Proxies
    for (const p of proxies) {
      if (p.enabled && !list.some(i => i.value === p.name)) {
        list.push({ value: p.name, label: p.name, group: m.mihomo_group_label_proxies(), groupKey: 'proxies' });
      }
    }

    return list;
  });

  // Pick sensible default outbound
  $effect(() => {
    if (availableOutbounds.length > 0 && !selectedOutbound) {
      // Prefer URL-Test or select group if available, else first tunnel
      const urlTestGroup = groups.find(g => g.enabled && g.type === 'url-test');
      const best = (urlTestGroup && availableOutbounds.find(o => o.value === urlTestGroup.name))
        || availableOutbounds.find(o => o.groupKey === 'groups')
        || availableOutbounds[0];
      if (best) selectedOutbound = best.value;
    }
  });

  const TEMPLATES = $derived([
    {
      id: 'smart_bypass' as TemplateId,
      title: m.mihomo_tpl_smart_title(),
      badge: m.mihomo_tpl_smart_badge(),
      badgeTone: 'green',
      icon: ShieldCheck,
      desc: m.mihomo_tpl_smart_desc(),
      pros: [
        m.mihomo_tpl_smart_pro1(),
        m.mihomo_tpl_smart_pro2(),
        m.mihomo_tpl_smart_pro3()
      ],
      rulesCount: 6,
      matchFallback: 'DIRECT'
    },
    {
      id: 'whitelist_mobile' as TemplateId,
      title: m.mihomo_tpl_mobile_title(),
      badge: m.mihomo_tpl_mobile_badge(),
      badgeTone: 'purple',
      icon: Radio,
      desc: m.mihomo_tpl_mobile_desc(),
      pros: [
        m.mihomo_tpl_mobile_pro1(),
        m.mihomo_tpl_mobile_pro2(),
        m.mihomo_tpl_mobile_pro3()
      ],
      rulesCount: 5,
      matchFallback: 'VPN'
    },
    {
      id: 'russia_direct_world_vpn' as TemplateId,
      title: m.mihomo_tpl_ru_world_title(),
      badge: m.mihomo_tpl_ru_world_badge(),
      badgeTone: 'blue',
      icon: Globe,
      desc: m.mihomo_tpl_ru_world_desc(),
      pros: [
        m.mihomo_tpl_ru_world_pro1(),
        m.mihomo_tpl_ru_world_pro2(),
        m.mihomo_tpl_ru_world_pro3()
      ],
      rulesCount: 5,
      matchFallback: 'VPN'
    },
    {
      id: 'full_tunnel' as TemplateId,
      title: m.mihomo_tpl_full_title(),
      badge: m.mihomo_tpl_full_badge(),
      badgeTone: 'amber',
      icon: Lock,
      desc: m.mihomo_tpl_full_desc(),
      pros: [
        m.mihomo_tpl_full_pro1(),
        m.mihomo_tpl_full_pro2()
      ],
      caution: m.mihomo_tpl_full_caution(),
      rulesCount: 2,
      matchFallback: 'VPN'
    }
  ]);

  async function handleApplyTemplate() {
    if (!selectedOutbound) {
      notifications.error(m.mihomo_tpl_err_select_outbound());
      return;
    }

    applying = true;
    try {
      // 1. Generate rules list based on chosen template
      let newRules: Array<{ type: string; payload?: string; outbound: string; noResolve?: boolean; enabled: boolean }> = [];

      if (selectedTemplate === 'smart_bypass') {
        newRules = [
          // Local networks always DIRECT
          { type: 'GEOIP', payload: 'private', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Russia and domestic services DIRECT
          { type: 'GEOSITE', payload: 'category-ru', outbound: 'DIRECT', enabled: true },
          { type: 'GEOIP', payload: 'ru', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Blocked and global services into VPN
          { type: 'GEOSITE', payload: 'youtube', outbound: selectedOutbound, enabled: true },
          { type: 'GEOSITE', payload: 'telegram', outbound: selectedOutbound, enabled: true },
          { type: 'GEOIP', payload: 'telegram', outbound: selectedOutbound, noResolve: true, enabled: true },
          { type: 'GEOSITE', payload: 'openai', outbound: selectedOutbound, enabled: true },
          { type: 'GEOSITE', payload: 'discord', outbound: selectedOutbound, enabled: true },
          { type: 'GEOSITE', payload: 'instagram', outbound: selectedOutbound, enabled: true },
          { type: 'GEOSITE', payload: 'twitter', outbound: selectedOutbound, enabled: true },
          // Everything else DIRECT
          { type: 'MATCH', outbound: 'DIRECT', enabled: true }
        ];
      } else if (selectedTemplate === 'whitelist_mobile') {
        newRules = [
          // Local networks DIRECT
          { type: 'GEOIP', payload: 'private', outbound: 'DIRECT', noResolve: true, enabled: true },
          // White-list of Russia (Gov, Banks, Local Services, Mobile Operators)
          { type: 'GEOSITE', payload: 'category-ru', outbound: 'DIRECT', enabled: true },
          { type: 'GEOIP', payload: 'ru', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Telegram inside VPN (bypass mobile throttling)
          { type: 'GEOSITE', payload: 'telegram', outbound: selectedOutbound, enabled: true },
          { type: 'GEOIP', payload: 'telegram', outbound: selectedOutbound, noResolve: true, enabled: true },
          // Everything else strictly via VPN tunnel to bypass white-list filter
          { type: 'MATCH', outbound: selectedOutbound, enabled: true }
        ];
      } else if (selectedTemplate === 'russia_direct_world_vpn') {
        newRules = [
          // Local networks DIRECT
          { type: 'GEOIP', payload: 'private', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Clean verified Russian database directly
          { type: 'GEOSITE', payload: 'category-ru', outbound: 'DIRECT', enabled: true },
          { type: 'GEOIP', payload: 'ru', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Telegram inside VPN
          { type: 'GEOSITE', payload: 'telegram', outbound: selectedOutbound, enabled: true },
          { type: 'GEOIP', payload: 'telegram', outbound: selectedOutbound, noResolve: true, enabled: true },
          // Everything else into VPN
          { type: 'MATCH', outbound: selectedOutbound, enabled: true }
        ];
      } else if (selectedTemplate === 'full_tunnel') {
        newRules = [
          // Local networks DIRECT
          { type: 'GEOIP', payload: 'private', outbound: 'DIRECT', noResolve: true, enabled: true },
          // Everything into VPN
          { type: 'MATCH', outbound: selectedOutbound, enabled: true }
        ];
      }

      // If replacing existing rules, delete them first (without triggering reload on each deletion)
      if (replaceExisting) {
        const rulesResp = await api.mihomoNativeRules();
        const currentList = Array.isArray(rulesResp) ? rulesResp : ((rulesResp as { items?: MihomoNativeRule[] })?.items ?? []);
        for (const r of currentList) {
          try {
            await api.mihomoNativeDeleteRule(r.id, false);
          } catch {
            // ignore
          }
        }
      }

      // Add each rule in order, applying only on the final rule
      for (let i = 0; i < newRules.length; i++) {
        const isLast = (i === newRules.length - 1);
        await api.mihomoNativeSaveRule({
          type: newRules[i].type,
          payload: newRules[i].payload,
          outbound: newRules[i].outbound,
          noResolve: newRules[i].noResolve,
          enabled: newRules[i].enabled
        }, isLast);
      }

      notifications.success(m.mihomo_tpl_applied_success({ count: newRules.length }));
      onApplied();
      onClose();
    } catch (err) {
      notifications.error(err instanceof Error ? err.message : m.mihomo_tpl_apply_error());
    } finally {
      applying = false;
    }
  }
</script>

<Modal {open} title={m.mihomo_tpl_modal_title()} onclose={onClose} size="lg">
  <div class="template-modal">
    <p class="modal-subtitle">
      {m.mihomo_tpl_modal_subtitle()}
    </p>

    <!-- Outbound Destination Selector -->
    <div class="target-box">
      <div class="target-title">
        <Layers size={16} />
        <span>{m.mihomo_tpl_target_lbl()}</span>
      </div>
      <div class="target-select-wrap">
        <select bind:value={selectedOutbound} class="text-input select-styled">
          {#if availableOutbounds.length === 0}
            <option value="">{m.mihomo_tpl_no_outbounds()}</option>
          {/if}
          {#each availableOutbounds as item}
            <option value={item.value}>[{item.group}] {item.label}</option>
          {/each}
        </select>
      </div>
    </div>

    <!-- Templates Cards -->
    <div class="cards-grid">
      {#each TEMPLATES as t}
        {@const Icon = t.icon}
        <button
          type="button"
          class="tpl-card"
          class:selected={selectedTemplate === t.id}
          onclick={() => (selectedTemplate = t.id)}
        >
          <div class="card-header">
            <div class="card-icon tone-{t.badgeTone}">
              <Icon size={20} />
            </div>
            <div class="card-title-wrap">
              <div class="title-row">
                <span class="card-title">{t.title}</span>
                <span class="badge badge-{t.badgeTone}">{t.badge}</span>
              </div>
            </div>
          </div>

          <p class="card-desc">{t.desc}</p>

          <div class="pros-list">
            {#each t.pros as pro}
              <div class="pro-item">
                <Check size={13} class="check-icon" />
                <span>{pro}</span>
              </div>
            {/each}
          </div>

          {#if t.caution}
            <div class="caution-item">
              <AlertTriangle size={13} />
              <span>{t.caution}</span>
            </div>
          {/if}

          <div class="card-footer">
            <span class="rules-badge">{m.mihomo_tpl_rules_count_badge({ count: t.rulesCount })}</span>
            <span class="fallback-badge">
              {m.mihomo_tpl_fallback_lbl()} <strong>{t.matchFallback === 'DIRECT' ? m.mihomo_tpl_fallback_direct() : 'VPN'}</strong>
            </span>
          </div>
        </button>
      {/each}
    </div>

    <label class="replace-toggle">
      <input type="checkbox" bind:checked={replaceExisting} />
      <span>{m.mihomo_tpl_replace_existing_lbl()}</span>
    </label>
  </div>

  {#snippet actions()}
    <Button variant="ghost" onclick={onClose} disabled={applying}>{m.common_cancel()}</Button>
    <Button variant="primary" onclick={handleApplyTemplate} loading={applying} disabled={applying || !selectedOutbound}>
      {m.mihomo_tpl_apply_btn()}
    </Button>
  {/snippet}
</Modal>

<style>
  .template-modal {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .modal-subtitle {
    margin: 0;
    font-size: 13px;
    line-height: 1.5;
    color: var(--text-secondary, #94a3b8);
  }

  .target-box {
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: var(--color-bg-secondary, rgba(255, 255, 255, 0.04));
    border: 1px solid var(--color-border, rgba(255, 255, 255, 0.1));
    border-radius: 8px;
    padding: 12px;
  }

  .target-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
    color: var(--color-text-primary, #fff);
  }

  .select-styled {
    width: 100%;
    padding: 9px 12px;
    background: var(--color-bg-tertiary, #1e293b);
    border: 1px solid var(--color-border, rgba(255, 255, 255, 0.15));
    border-radius: 6px;
    color: var(--color-text-primary, #fff);
    font-size: 13px;
    outline: none;
    transition: border-color 0.15s ease;
  }

  .select-styled:focus {
    border-color: var(--color-primary, #3b82f6);
  }

  .cards-grid {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .tpl-card {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
    border-radius: 10px;
    background: var(--color-bg-secondary, rgba(255, 255, 255, 0.02));
    border: 1.5px solid var(--color-border, rgba(255, 255, 255, 0.08));
    text-align: left;
    cursor: pointer;
    transition: all 0.18s ease;
  }

  .tpl-card:hover {
    border-color: var(--color-border-hover, rgba(255, 255, 255, 0.2));
    background: var(--color-bg-tertiary, rgba(255, 255, 255, 0.04));
  }

  .tpl-card.selected {
    border-color: var(--color-primary, #3b82f6);
    background: color-mix(in srgb, var(--color-primary, #3b82f6) 8%, var(--color-bg-secondary, #1a2234));
  }

  .card-header {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .card-icon {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .card-icon.tone-green {
    background: rgba(34, 197, 94, 0.15);
    color: #22c55e;
  }

  .card-icon.tone-blue {
    background: rgba(59, 130, 246, 0.15);
    color: #3b82f6;
  }

  .card-icon.tone-amber {
    background: rgba(245, 158, 11, 0.15);
    color: #f59e0b;
  }

  .card-icon.tone-purple {
    background: rgba(168, 85, 247, 0.15);
    color: #c084fc;
  }

  .card-title-wrap {
    flex: 1;
    min-width: 0;
  }

  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    flex-wrap: wrap;
  }

  .card-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .badge {
    font-size: 11px;
    padding: 2px 9px;
    border-radius: 9999px;
    font-weight: 500;
  }

  .badge-green {
    background: rgba(34, 197, 94, 0.12);
    color: #4ade80;
    border: 1px solid rgba(34, 197, 94, 0.25);
  }

  .badge-blue {
    background: rgba(59, 130, 246, 0.12);
    color: #60a5fa;
    border: 1px solid rgba(59, 130, 246, 0.25);
  }

  .badge-amber {
    background: rgba(245, 158, 11, 0.12);
    color: #fbbf24;
    border: 1px solid rgba(245, 158, 11, 0.25);
  }

  .badge-purple {
    background: rgba(168, 85, 247, 0.12);
    color: #c084fc;
    border: 1px solid rgba(168, 85, 247, 0.25);
  }

  .card-desc {
    margin: 0;
    font-size: 12px;
    line-height: 1.45;
    color: var(--color-text-secondary, #94a3b8);
  }

  .pros-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .pro-item {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--color-text-secondary, #cbd5e1);
  }

  .check-icon {
    color: #22c55e;
    flex-shrink: 0;
  }

  .caution-item {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: #fca5a5;
    background: rgba(239, 68, 68, 0.08);
    border: 1px solid rgba(239, 68, 68, 0.2);
    padding: 7px 10px;
    border-radius: 6px;
  }

  .card-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 11.5px;
    border-top: 1px solid var(--color-border, rgba(255, 255, 255, 0.08));
    padding-top: 8px;
    color: var(--color-text-muted, #94a3b8);
  }

  .card-footer strong {
    color: var(--color-text-primary, #fff);
  }

  .replace-toggle {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text-primary, #fff);
    cursor: pointer;
    margin-top: 4px;
  }
</style>
