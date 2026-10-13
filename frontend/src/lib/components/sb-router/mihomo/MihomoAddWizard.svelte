<script lang="ts">
  import { m } from '$lib/i18n';
  import { onMount } from 'svelte';
  import {
    ArrowLeft, Check, Globe, Shield, ShieldOff, Zap, Plus, Layers, Network,
    Tv, Send, MessageSquare, Camera, Sparkles, Film, Music, Gamepad2, ShoppingCart, AlertTriangle
  } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { Button, Card, SegmentedControl } from '$lib/components/ui';
  import type { MihomoNativeGroup, MihomoNativeProxy, MihomoNativeRule, MihomoNativeSubscription, MihomoRuntimeProxy } from '$lib/types';

  interface Props {
    onClose: () => void;
    onSaved: () => void;
    groups: MihomoNativeGroup[];
    proxies: MihomoNativeProxy[];
    subscriptions: MihomoNativeSubscription[];
    runtimeProxies: MihomoRuntimeProxy[];
  }

  let { onClose, onSaved, groups = [], proxies = [], subscriptions = [], runtimeProxies = [] }: Props = $props();

  // Wizard state
  let currentStep = $state<1 | 2 | 3>(1);

  // Step 1: Matcher
  let matcherTab = $state<'catalog' | 'custom'>('catalog');
  let selectedService = $state<{ name: string; type: string; payload: string; icon: unknown } | null>(null);

  let customType = $state<'GEOSITE' | 'DOMAIN-SUFFIX' | 'DOMAIN' | 'IP-CIDR' | 'RULE-SET'>('GEOSITE');
  let customPayload = $state('');

  // Step 2: Outbound
  let outboundType = $state<'group' | 'proxy' | 'direct' | 'reject'>('group');
  let selectedGroup = $state('');
  let selectedProxy = $state('');

  // New Group inline creation (optional)
  let createNewGroup = $state(false);
  let newGroupName = $state('');
  let newGroupType = $state<'url-test' | 'select' | 'fallback'>('url-test');
  let newGroupMembers = $state<string[]>([]);

  // Step 3: Saving
  let saving = $state(false);

  // Popular Services Catalog with icons and Mihomo GEOSITE tags
  const CATALOG_SERVICES = $derived([
    { name: 'YouTube', type: 'GEOSITE', payload: 'youtube', icon: Tv, desc: m.mihomo_wiz_svc_youtube_desc() },
    { name: 'Telegram', type: 'GEOSITE', payload: 'telegram', icon: Send, desc: m.mihomo_wiz_svc_telegram_desc() },
    { name: 'Discord', type: 'GEOSITE', payload: 'discord', icon: MessageSquare, desc: m.mihomo_wiz_svc_discord_desc() },
    { name: 'Instagram', type: 'GEOSITE', payload: 'instagram', icon: Camera, desc: m.mihomo_wiz_svc_instagram_desc() },
    { name: 'Twitter / X', type: 'GEOSITE', payload: 'twitter', icon: Globe, desc: m.mihomo_wiz_svc_twitter_desc() },
    { name: 'OpenAI / ChatGPT', type: 'GEOSITE', payload: 'openai', icon: Sparkles, desc: m.mihomo_wiz_svc_openai_desc() },
    { name: 'TikTok', type: 'GEOSITE', payload: 'tiktok', icon: Film, desc: m.mihomo_wiz_svc_tiktok_desc() },
    { name: 'Spotify', type: 'GEOSITE', payload: 'spotify', icon: Music, desc: m.mihomo_wiz_svc_spotify_desc() },
    { name: 'Twitch', type: 'GEOSITE', payload: 'twitch', icon: Tv, desc: m.mihomo_wiz_svc_twitch_desc() },
    { name: 'Steam', type: 'GEOSITE', payload: 'steam', icon: Gamepad2, desc: m.mihomo_wiz_svc_steam_desc() },
    { name: 'Netflix', type: 'GEOSITE', payload: 'netflix', icon: Film, desc: m.mihomo_wiz_svc_netflix_desc() },
    { name: 'RuTracker', type: 'DOMAIN-SUFFIX', payload: 'rutracker.org', icon: Globe, desc: m.mihomo_wiz_svc_rutracker_desc() },
  ]);

  // Available groups
  const availableGroups = $derived.by(() => {
    const list: Array<{ name: string; type: string; label: string }> = [];
    for (const g of groups) {
      if (g.enabled) list.push({ name: g.name, type: g.type, label: g.name });
    }
    for (const s of subscriptions) {
      if (s.enabled && s.groupName && !list.some(item => item.name === s.groupName)) {
        list.push({ name: s.groupName, type: 'url-test', label: s.name });
      }
    }
    return list;
  });

  // Available individual proxies
  const availableProxies = $derived.by(() => {
    const list: Array<{ name: string; label: string }> = [];
    for (const p of proxies) {
      if (p.enabled) list.push({ name: p.name, label: p.name });
    }
    return list;
  });

  $effect(() => {
    if (availableGroups.length > 0 && !selectedGroup) {
      selectedGroup = availableGroups[0].name;
    }
    if (availableProxies.length > 0 && !selectedProxy) {
      selectedProxy = availableProxies[0].name;
    }
  });

  const selectedGroupObj = $derived(availableGroups.find((g) => g.name === selectedGroup));
  const isSelectedGroupLoadBalance = $derived(selectedGroupObj?.type === 'load-balance');

  const ruleSummary = $derived.by(() => {
    const matcher = matcherTab === 'catalog' && selectedService
      ? `${selectedService.name} (${selectedService.type}: ${selectedService.payload})`
      : `${customType}: ${customPayload.trim()}`;

    const target =
      outboundType === 'reject'
        ? m.mihomo_wiz_target_reject()
        : outboundType === 'proxy'
          ? m.mihomo_wiz_target_proxy({ name: selectedProxy })
          : outboundType === 'group'
            ? createNewGroup
              ? m.mihomo_wiz_target_new_group({ name: newGroupName || m.mihomo_wiz_unnamed(), type: newGroupType })
              : m.mihomo_wiz_target_group({ name: selectedGroup })
            : m.mihomo_wiz_target_direct();
    return { matcher, target };
  });

  const step1Valid = $derived(
    (matcherTab === 'catalog' && selectedService !== null) ||
    (matcherTab === 'custom' && customPayload.trim().length > 0)
  );

  const step2Valid = $derived.by(() => {
    if (outboundType === 'direct' || outboundType === 'reject') return true;
    if (outboundType === 'proxy') return selectedProxy.length > 0;
    if (outboundType === 'group') {
      if (createNewGroup) return newGroupName.trim().length > 0 && newGroupMembers.length > 0;
      return selectedGroup.length > 0;
    }
    return false;
  });

  async function handleCreateRule() {
    if (saving || !step1Valid || !step2Valid) return;
    saving = true;
    try {
      let targetOutbound = 'DIRECT';
      if (outboundType === 'reject') {
        targetOutbound = 'REJECT';
      } else if (outboundType === 'proxy') {
        targetOutbound = selectedProxy;
      } else if (outboundType === 'group') {
        if (createNewGroup) {
          const created = await api.mihomoNativeSaveGroup({
            name: newGroupName.trim(),
            type: newGroupType,
            proxies: newGroupMembers,
            use: [],
            url: 'https://www.gstatic.com/generate_204',
            interval: 300,
            lazy: false,
            tolerance: 50,
            enabled: true,
          }, false);
          targetOutbound = created.name;
        } else {
          targetOutbound = selectedGroup;
        }
      }

      const rType = matcherTab === 'catalog' && selectedService ? selectedService.type : customType;
      const rPayload = matcherTab === 'catalog' && selectedService ? selectedService.payload : customPayload.trim();

      await api.mihomoNativeSaveRule({
        type: rType,
        payload: rPayload,
        outbound: targetOutbound,
        enabled: true,
      }, true);

      notifications.success(m.mihomo_wiz_created_success());
      onSaved();
      onClose();
    } catch (e) {
      notifications.error(e instanceof Error ? e.message : m.mihomo_wiz_create_error());
    } finally {
      saving = false;
    }
  }
</script>

<div class="wizard-overlay">
  <div class="wizard-container">
    <div class="wizard-header">
      <div class="wizard-title-row">
        <button type="button" class="back-btn" onclick={onClose} aria-label={m.mihomo_wiz_close_aria()}>
          <ArrowLeft size={16} /> {m.mihomo_wiz_back_to_rules()}
        </button>
        <h2>{m.mihomo_wiz_title()}</h2>
      </div>
      <div class="stepper">
        <div class="step-pill" class:active={currentStep === 1} class:done={currentStep > 1}>
          <span class="num">1</span> {m.mihomo_wiz_step1_pill()}
        </div>
        <div class="step-pill" class:active={currentStep === 2} class:done={currentStep > 2}>
          <span class="num">2</span> {m.mihomo_wiz_step2_pill()}
        </div>
        <div class="step-pill" class:active={currentStep === 3}>
          <span class="num">3</span> {m.mihomo_wiz_step3_pill()}
        </div>
      </div>
    </div>

    <div class="wizard-body">
      {#if currentStep === 1}
        <div class="step-pane">
          <div class="pane-header">
            <h3>{m.mihomo_wiz_step1_title()}</h3>
            <p class="pane-hint">{m.mihomo_wiz_step1_hint()}</p>
          </div>

          <div class="tab-switch">
            <SegmentedControl
              value={matcherTab}
              options={[
                { value: 'catalog', label: m.mihomo_wiz_tab_catalog() },
                { value: 'custom', label: m.mihomo_wiz_tab_custom() }
              ]}
              ariaLabel={m.mihomo_wiz_matcher_aria()}
              onchange={(v) => matcherTab = v as typeof matcherTab}
            />
          </div>

          {#if matcherTab === 'catalog'}
            <div class="services-grid">
              {#each CATALOG_SERVICES as svc}
                <button
                  type="button"
                  class="service-card"
                  class:selected={selectedService?.name === svc.name}
                  onclick={() => selectedService = svc}
                >
                  <div class="service-icon">
                    <svc.icon size={22} />
                  </div>
                  <div class="service-info">
                    <span class="service-name">{svc.name}</span>
                    <span class="service-desc">{svc.desc}</span>
                  </div>
                  {#if selectedService?.name === svc.name}
                    <div class="check-badge"><Check size={14} /></div>
                  {/if}
                </button>
              {/each}
            </div>
          {:else}
            <div class="custom-form">
              <label class="form-group">
                <span class="label-text">{m.mihomo_wiz_custom_type_lbl()}</span>
                <select bind:value={customType} class="select-input">
                  <option value="GEOSITE">{m.mihomo_wiz_type_geosite()}</option>
                  <option value="DOMAIN-SUFFIX">{m.mihomo_wiz_type_domain_suffix()}</option>
                  <option value="DOMAIN">{m.mihomo_wiz_type_domain()}</option>
                  <option value="IP-CIDR">{m.mihomo_wiz_type_ip_cidr()}</option>
                  <option value="RULE-SET">{m.mihomo_wiz_type_rule_set()}</option>
                </select>
              </label>

              <label class="form-group">
                <span class="label-text">{m.mihomo_wiz_payload_lbl()}</span>
                <input
                  type="text"
                  bind:value={customPayload}
                  placeholder={customType === 'IP-CIDR' ? '91.108.4.0/22' : customType === 'GEOSITE' ? 'youtube' : 'example.com'}
                  class="text-input"
                />
              </label>
            </div>
          {/if}
        </div>
      {:else if currentStep === 2}
        <div class="step-pane">
          <div class="pane-header">
            <h3>{m.mihomo_wiz_step2_title()}</h3>
            <p class="pane-hint">{m.mihomo_wiz_step2_hint()}</p>
          </div>

          <div class="outbound-options">
            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'group'}
              onclick={() => { outboundType = 'group'; createNewGroup = false; }}
            >
              <div class="tile-icon icon-group"><Zap size={20} /></div>
              <div class="tile-content">
                <strong>{m.mihomo_wiz_out_group_title()}</strong>
                <span>{m.mihomo_wiz_out_group_sub()}</span>
              </div>
            </button>

            {#if outboundType === 'group'}
              <div class="sub-options">
                {#if availableGroups.length > 0 && !createNewGroup}
                  <label class="form-group">
                    <span class="label-text">{m.mihomo_wiz_select_group_lbl()}</span>
                    <select bind:value={selectedGroup} class="select-input">
                      {#each availableGroups as g}
                        <option value={g.name}>{g.label} ({g.type})</option>
                      {/each}
                    </select>
                  </label>

                  {#if isSelectedGroupLoadBalance}
                    <div class="load-balance-warning">
                      <div class="warning-header">
                        <AlertTriangle size={14} class="warning-icon" />
                        <span class="warning-title">{m.mihomo_wiz_lb_warn_title({ group: selectedGroup })}</span>
                      </div>
                      <p class="warning-desc">
                        {m.mihomo_wiz_lb_warn_desc()}
                      </p>
                    </div>
                  {/if}

                  <Button variant="ghost" size="sm" onclick={() => createNewGroup = true}>
                    <Plus size={14} /> {m.mihomo_wiz_create_new_group_btn()}
                  </Button>
                {:else}
                  <div class="new-group-box">
                    <h4>{m.mihomo_wiz_new_group_title()}</h4>
                    <label class="form-group">
                      <span class="label-text">{m.mihomo_wiz_new_group_name_lbl()}</span>
                      <input type="text" bind:value={newGroupName} placeholder={m.mihomo_wiz_new_group_placeholder()} class="text-input" />
                    </label>
                    <label class="form-group">
                      <span class="label-text">{m.mihomo_wiz_new_group_members_lbl()}</span>
                      <div class="members-picker">
                        {#each availableProxies as p}
                          <label class="member-check">
                            <input
                              type="checkbox"
                              checked={newGroupMembers.includes(p.name)}
                              onchange={(e) => {
                                if (e.currentTarget.checked) newGroupMembers = [...newGroupMembers, p.name];
                                else newGroupMembers = newGroupMembers.filter(item => item !== p.name);
                              }}
                            />
                            <span>{p.label}</span>
                          </label>
                        {/each}
                      </div>
                    </label>
                    {#if availableGroups.length > 0}
                      <Button variant="ghost" size="sm" onclick={() => createNewGroup = false}>
                        {m.mihomo_wiz_cancel_new_group_btn()}
                      </Button>
                    {/if}
                  </div>
                {/if}
              </div>
            {/if}

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'proxy'}
              onclick={() => outboundType = 'proxy'}
            >
              <div class="tile-icon icon-proxy"><Network size={20} /></div>
              <div class="tile-content">
                <strong>{m.mihomo_wiz_out_proxy_title()}</strong>
                <span>{m.mihomo_wiz_out_proxy_sub()}</span>
              </div>
            </button>

            {#if outboundType === 'proxy'}
              <div class="sub-options">
                <label class="form-group">
                  <span class="label-text">{m.mihomo_wiz_select_server_lbl()}</span>
                  <select bind:value={selectedProxy} class="select-input">
                    {#each availableProxies as p}
                      <option value={p.name}>{p.label}</option>
                    {/each}
                  </select>
                </label>
              </div>
            {/if}

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'direct'}
              onclick={() => outboundType = 'direct'}
            >
              <div class="tile-icon icon-direct"><Globe size={20} /></div>
              <div class="tile-content">
                <strong>{m.mihomo_wiz_out_direct_title()}</strong>
                <span>{m.mihomo_wiz_out_direct_sub()}</span>
              </div>
            </button>

            <button
              type="button"
              class="outbound-tile"
              class:selected={outboundType === 'reject'}
              onclick={() => outboundType = 'reject'}
            >
              <div class="tile-icon icon-reject"><ShieldOff size={20} /></div>
              <div class="tile-content">
                <strong>{m.mihomo_wiz_out_reject_title()}</strong>
                <span>{m.mihomo_wiz_out_reject_sub()}</span>
              </div>
            </button>
          </div>
        </div>
      {:else}
        <div class="step-pane">
          <div class="pane-header">
            <h3>{m.mihomo_wiz_step3_title()}</h3>
            <p class="pane-hint">{m.mihomo_wiz_step3_hint()}</p>
          </div>

          <div class="summary-card">
            <div class="summary-row">
              <span class="summary-label">{m.mihomo_wiz_summary_what_lbl()}</span>
              <strong class="summary-val">{ruleSummary.matcher}</strong>
            </div>
            <div class="summary-divider"></div>
            <div class="summary-row">
              <span class="summary-label">{m.mihomo_wiz_summary_where_lbl()}</span>
              <strong class="summary-val highlight">{ruleSummary.target}</strong>
            </div>
          </div>
        </div>
      {/if}
    </div>

    <div class="wizard-footer">
      {#if currentStep > 1}
        <Button variant="secondary" onclick={() => currentStep -= 1} disabled={saving}>
          {m.mihomo_wiz_back_btn()}
        </Button>
      {:else}
        <Button variant="ghost" onclick={onClose} disabled={saving}>
          {m.common_cancel()}
        </Button>
      {/if}

      <div class="footer-actions">
        {#if currentStep < 3}
          <Button
            variant="primary"
            disabled={(currentStep === 1 && !step1Valid) || (currentStep === 2 && !step2Valid)}
            onclick={() => currentStep += 1}
          >
            {m.mihomo_wiz_next_btn()}
          </Button>
        {:else}
          <Button
            variant="primary"
            loading={saving}
            onclick={handleCreateRule}
          >
            {m.mihomo_wiz_create_apply_btn()}
          </Button>
        {/if}
      </div>
    </div>
  </div>
</div>

<style>
  .wizard-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.65);
    backdrop-filter: blur(4px);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: 16px;
  }

  .wizard-container {
    background: var(--bg-surface, #1e1e24);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 12px;
    width: 100%;
    max-width: 680px;
    max-height: 90vh;
    display: flex;
    flex-direction: column;
    box-shadow: 0 16px 32px rgba(0, 0, 0, 0.4);
    overflow: hidden;
  }

  .wizard-header {
    padding: 20px 24px;
    border-bottom: 1px solid var(--border, #2e2e38);
    background: var(--bg-secondary, #252530);
  }

  .wizard-title-row {
    margin-bottom: 16px;
  }

  .back-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: transparent;
    border: none;
    color: var(--text-secondary, #9ba1a6);
    font-size: 13px;
    cursor: pointer;
    margin-bottom: 6px;
    padding: 0;
  }
  .back-btn:hover { color: var(--text-primary, #fff); }

  .wizard-title-row h2 {
    margin: 0;
    font-size: 18px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .stepper {
    display: flex;
    gap: 8px;
  }

  .step-pill {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    border-radius: 6px;
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    color: var(--text-secondary, #9ba1a6);
    font-size: 12px;
  }

  .step-pill.active {
    background: var(--accent-bg, rgba(59, 130, 246, 0.15));
    border-color: var(--accent, #3b82f6);
    color: var(--text-primary, #fff);
    font-weight: 500;
  }

  .step-pill.done {
    border-color: var(--color-success, #10b981);
    color: var(--text-primary, #fff);
  }

  .step-pill .num {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: rgba(255, 255, 255, 0.1);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 11px;
  }

  .step-pill.active .num {
    background: var(--accent, #3b82f6);
    color: #fff;
  }

  .wizard-body {
    padding: 24px;
    overflow-y: auto;
    flex: 1;
  }

  .pane-header {
    margin-bottom: 20px;
  }

  .pane-header h3 {
    margin: 0 0 4px;
    font-size: 16px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .pane-hint {
    margin: 0;
    font-size: 13px;
    color: var(--text-secondary, #9ba1a6);
  }

  .tab-switch {
    margin-bottom: 16px;
  }

  .services-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: 10px;
  }

  .service-card {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px;
    border-radius: 8px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
    position: relative;
  }

  .service-card:hover {
    border-color: var(--border-hover, #4a4a58);
    background: var(--bg-hover, #2c2c38);
  }

  .service-card.selected {
    border-color: var(--accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
  }

  .service-icon {
    color: var(--accent, #3b82f6);
    flex-shrink: 0;
    margin-top: 2px;
  }

  .service-info {
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .service-name {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .service-desc {
    font-size: 11px;
    color: var(--text-muted, #71717a);
    margin-top: 2px;
    line-height: 1.3;
  }

  .check-badge {
    position: absolute;
    top: 8px;
    right: 8px;
    color: var(--accent, #3b82f6);
  }

  .custom-form {
    display: flex;
    flex-direction: column;
    gap: 16px;
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

  .outbound-options {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .outbound-tile {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px;
    border-radius: 8px;
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    cursor: pointer;
    text-align: left;
    transition: all 0.15s ease;
  }

  .outbound-tile:hover {
    border-color: var(--border-hover, #4a4a58);
  }

  .outbound-tile.selected {
    border-color: var(--accent, #3b82f6);
    background: rgba(59, 130, 246, 0.12);
  }

  .tile-icon {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .icon-group { background: rgba(59, 130, 246, 0.2); color: #3b82f6; }
  .icon-proxy { background: rgba(168, 85, 247, 0.2); color: #a855f7; }
  .icon-direct { background: rgba(16, 185, 129, 0.2); color: #10b981; }
  .icon-reject { background: rgba(239, 68, 68, 0.2); color: #ef4444; }

  .tile-content {
    display: flex;
    flex-direction: column;
  }

  .tile-content strong {
    font-size: 14px;
    color: var(--text-primary, #fff);
  }

  .tile-content span {
    font-size: 12px;
    color: var(--text-secondary, #9ba1a6);
    margin-top: 2px;
  }

  .sub-options {
    padding: 12px 16px;
    margin-top: -4px;
    margin-bottom: 6px;
    border-radius: 0 0 8px 8px;
    background: var(--bg-tertiary, #18181f);
    border: 1px solid var(--border, #2e2e38);
    border-top: none;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .new-group-box {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .new-group-box h4 {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary, #fff);
  }

  .members-picker {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 140px;
    overflow-y: auto;
    padding: 6px;
    border: 1px solid var(--border, #2e2e38);
    border-radius: 6px;
    background: var(--bg-secondary, #252530);
  }

  .member-check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text-primary, #fff);
    cursor: pointer;
  }

  .summary-card {
    background: var(--bg-secondary, #252530);
    border: 1px solid var(--border, #2e2e38);
    border-radius: 8px;
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .summary-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 14px;
  }

  .summary-label {
    color: var(--text-secondary, #9ba1a6);
  }

  .summary-val {
    color: var(--text-primary, #fff);
  }

  .summary-val.highlight {
    color: var(--accent, #3b82f6);
    font-size: 15px;
  }

  .summary-divider {
    height: 1px;
    background: var(--border, #2e2e38);
  }

  .wizard-footer {
    padding: 16px 24px;
    border-top: 1px solid var(--border, #2e2e38);
    background: var(--bg-secondary, #252530);
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .footer-actions {
    display: flex;
    gap: 10px;
  }

  .load-balance-warning {
    background: rgba(217, 119, 6, 0.05);
    border: 1px solid rgba(217, 119, 6, 0.35);
    border-radius: var(--radius-md, 8px);
    padding: 8px 12px;
    font-size: 12px;
    line-height: 1.4;
    margin: 6px 0 10px;
    display: flex;
    flex-direction: column;
    gap: 4px;
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
</style>
