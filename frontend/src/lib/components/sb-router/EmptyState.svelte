<!--
  Мастер первичной настройки (простой режим): туннель/группа → сервисы в туннель (final=direct) → включить.
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { get } from 'svelte/store';
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { Check, Plus, Zap } from 'lucide-svelte';
  import { api } from '$lib/api/client';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { notifications } from '$lib/stores/notifications';
  import { Button } from '$lib/components/ui';
  import EmptyHero from './EmptyHero.svelte';
  import StepPill from './StepPill.svelte';
  import WizardStep from './WizardStep.svelte';
  import SelectedTemplatesRow from './SelectedTemplatesRow.svelte';
  import SbRouterServiceCatalogModal from './SbRouterServiceCatalogModal.svelte';
  import { templatesSelection, openTemplatesModal, clearSelection } from './templatesStore';
  import { buildTemplateList } from './templatesData';
  import { finishSetup, ensureTunnelDnsInfra, syncTunnelDnsRule } from './emptyStateActions';
  import type {
    MihomoNativeGroup,
    MihomoNativeProxy,
    MihomoNativeSubscription,
  } from '$lib/types';
  import MihomoGroupEditModal from './mihomo/MihomoGroupEditModal.svelte';

  interface Props {
    isMihomo?: boolean;
    onReloadMihomo?: () => void;
  }

  let { isMihomo = false, onReloadMihomo }: Props = $props();

  const options = singboxRouterStore.options;
  const optionsReady = singboxRouterStore.optionsReady;
  const presets = singboxRouterStore.presets;
  const ruleSets = singboxRouterStore.ruleSets;

  let mihomoGroups = $state<MihomoNativeGroup[]>([]);
  let mihomoProxies = $state<MihomoNativeProxy[]>([]);
  let mihomoSubscriptions = $state<MihomoNativeSubscription[]>([]);
  let groupModalOpen = $state(false);

  async function loadMihomoResources() {
    if (!isMihomo) return;
    try {
      const [g, p, s] = await Promise.all([
        api.mihomoNativeGroups().catch(() => []),
        api.mihomoNativeProxies().catch(() => []),
        api.mihomoNativeSubscriptions().catch(() => []),
      ]);
      mihomoGroups = g;
      mihomoProxies = p;
      mihomoSubscriptions = s;
      if (mihomoGroups.length > 0 && !selectedTunnel) {
        selectedTunnel = mihomoGroups[0].name;
      }
    } catch {
      // ignore
    }
  }

  onMount(() => {
    void singboxRouterStore.loadAll();
    if (isMihomo) {
      void loadMihomoResources();
    }
  });

  let selectedTunnel = $state<string | null>(null);
  let finishing = $state(false);

  const tunnelOutbounds = $derived.by(() => {
    const raw = $options
      .filter((g) => g.id !== 'special' && (!isMihomo || g.id !== 'mihomo_groups'))
      .flatMap((g) => g.items);
    if (!isMihomo || mihomoGroups.length === 0) return raw;
    const groupNames = new Set(mihomoGroups.map((g) => g.name.toLowerCase()));
    return raw.filter((ob) => {
      const val = ob.value.toLowerCase();
      const baseLabel = ob.label.replace(/\s*\([^)]*\)$/, '').trim().toLowerCase();
      return !groupNames.has(val) && !groupNames.has(baseLabel);
    });
  });
  const groups = $derived(buildTemplateList($presets, $ruleSets, ''));

  const hasServices = $derived($templatesSelection.size > 0);

  const step1Done = $derived(selectedTunnel !== null);
  const step2Done = $derived(step1Done && hasServices);
  const canFinish = $derived(step1Done && step2Done && !finishing);

  async function handleFinish() {
    if (!canFinish || selectedTunnel === null) return;
    finishing = true;
    try {
      if (isMihomo) {
        const rawTemplates = Array.from(get(templatesSelection));
        const allPresets = get(presets);

        for (const rawId of rawTemplates) {
          const templateId = rawId.replace(/^(svc|rs):/, '');
          const tagLower = templateId.toLowerCase();

          if (
            tagLower.startsWith('geosite-') ||
            tagLower === 'youtube' ||
            tagLower === 'telegram' ||
            tagLower === 'discord' ||
            tagLower === 'instagram' ||
            tagLower === 'openai' ||
            tagLower === 'tiktok' ||
            tagLower === 'spotify' ||
            tagLower === 'steam' ||
            tagLower === 'netflix' ||
            tagLower === 'rutracker' ||
            tagLower === 'twitter' ||
            tagLower === 'antifilter'
          ) {
            const geoTag = tagLower.replace(/^geosite-/, '');
            await api.mihomoNativeSaveRule({
              type: 'GEOSITE',
              payload: geoTag,
              outbound: selectedTunnel,
              enabled: true,
            });
          } else if (tagLower.startsWith('geoip-')) {
            const geoTag = tagLower.replace(/^geoip-/, '');
            await api.mihomoNativeSaveRule({
              type: 'GEOIP',
              payload: geoTag,
              outbound: selectedTunnel,
              noResolve: true,
              enabled: true,
            });
          } else {
            const geoTag = tagLower.replace(/^geosite-/, '');
            await api.mihomoNativeSaveRule({
              type: 'GEOSITE',
              payload: geoTag,
              outbound: selectedTunnel,
              enabled: true,
            });
            if (geoTag === 'telegram' || geoTag === 'netflix' || geoTag === 'twitter' || geoTag === 'facebook') {
              await api.mihomoNativeSaveRule({
                type: 'GEOIP',
                payload: geoTag,
                outbound: selectedTunnel,
                noResolve: true,
                enabled: true,
              });
            }
          }
        }

        // Enable routing in settings
        const currentSettings = await api.singboxRouterGetSettings();
        await api.singboxRouterPutSettings({
          ...currentSettings,
          enabled: true,
          routingEngine: 'mihomo',
          routingMode: 'tproxy',
        });

        if (!isMihomo && selectedTunnel) {
          try {
            await ensureTunnelDnsInfra(selectedTunnel);
            await syncTunnelDnsRule();
          } catch (e) {
            console.error('Failed to configure tunnel DNS infra:', e);
          }
        }
        await api.mihomoReload();
        await singboxRouterStore.loadAll();

        notifications.success(m.sb_router_empty_done_mihomo());
        clearSelection();
        selectedTunnel = null;
        onReloadMihomo?.();
        return;
      }

      const result = await finishSetup({
        tunnelTag: selectedTunnel,
        selectedTemplates: Array.from(get(templatesSelection)),
        customFields: { rulesList: '' },
        groups,
        existingRuleSetTags: get(ruleSets).map((r) => r.tag),
      });
      if (result.failures.length === 0) {
        notifications.success(m.sb_router_empty_done());
      } else {
        const sN = result.successes.length;
        const fN = result.failures.length;
        notifications.error(
          sN > 0
            ? m.sb_router_empty_partial({ done: sN, total: sN + fN })
            : m.sb_router_empty_failed({ count: fN }),
        );
      }
      clearSelection();
      selectedTunnel = null;
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(m.sb_router_common_error({ message: e instanceof Error ? e.message : String(e) }));
    } finally {
      finishing = false;
    }
  }
</script>

{#snippet iconCheck()}<Check size={14} />{/snippet}

<div class="wrap">
  <EmptyHero {isMihomo} />

  <div class="stepper">
    <StepPill n={1} label={isMihomo ? m.sb_router_empty_step_group_tunnel() : m.sb_router_empty_step_tunnel()} active={!step1Done} done={step1Done} />
    <div class="connector"></div>
    <StepPill n={2} label={m.sb_router_empty_step_services()} active={step1Done && !step2Done} done={step2Done} />
    <div class="connector"></div>
    <StepPill n={3} label={m.common_enable()} active={step2Done} done={false} />
  </div>

  <WizardStep
    n={1}
    title={isMihomo ? m.sb_router_empty_pick_tunnel_mihomo() : m.sb_router_empty_pick_tunnel()}
    hint={m.sb_router_empty_pick_tunnel_hint()}
    active={true}
  >
    <div class="tunnel-header">
      <span class="sub-hint">{m.sb_router_empty_available_directions()}</span>
      {#if isMihomo}
        <Button variant="secondary" size="sm" onclick={() => (groupModalOpen = true)}>
          <Plus size={14} /> {m.sb_router_empty_create_proxy_group()}
        </Button>
      {/if}
    </div>

    {#if isMihomo && mihomoGroups.length > 0}
      <div class="section-sub-label">{m.sb_router_mihomo_groups()}:</div>
      <div class="tunnel-chips">
        {#each mihomoGroups as grp (grp.id || grp.name)}
          {@const selected = selectedTunnel === grp.name}
          <button type="button" class="t-chip group-chip" class:selected onclick={() => (selectedTunnel = grp.name)}>
            <Zap size={13} class="icon-accent" />
            <span class="tag">{grp.name}</span>
            <span class="type-badge">{grp.type}</span>
          </button>
        {/each}
      </div>
    {/if}

    {#if tunnelOutbounds.length > 0}
      {#if isMihomo && mihomoGroups.length > 0}
        <div class="section-sub-label mt-2">{m.sb_router_tunnels_and_proxies()}</div>
      {/if}
      <div class="tunnel-chips">
        {#each tunnelOutbounds as ob (ob.value)}
          {@const selected = selectedTunnel === ob.value}
          <button type="button" class="t-chip" class:selected onclick={() => (selectedTunnel = ob.value)}>
            <span class="tag">{ob.label}</span>
          </button>
        {/each}
      </div>
    {:else if $optionsReady && (!isMihomo || mihomoGroups.length === 0)}
      <div class="empty-tunnels">
        {m.sb_router_wizard_no_tunnels()}
        <button type="button" class="link" onclick={() => goto('/')}>{m.sb_router_empty_create_tunnel()}</button>
        {m.sb_router_empty_and_return()}
      </div>
    {/if}
  </WizardStep>

  <WizardStep n={2} title={m.sb_router_empty_services_title()} hint={m.sb_router_empty_services_hint()} active={step1Done}>
    <button type="button" class="picker-btn" onclick={() => openTemplatesModal()}>
      <div class="picker-icon">+</div>
      <div class="picker-text">
        <div class="picker-title">{m.sb_router_empty_pick_services()}</div>
        <div class="picker-sub">{m.sb_router_empty_presets_count({ count: $presets.length })}</div>
      </div>
      <div class="picker-chev">›</div>
    </button>
    <SelectedTemplatesRow />
  </WizardStep>

  <WizardStep n={3} title={m.common_enable()} active={step2Done}>
    <p class="enable-hint">
      {isMihomo ? m.sb_router_empty_enable_hint_mihomo() : m.sb_router_empty_enable_hint()}
    </p>
    <Button variant="primary" size="md" onclick={handleFinish} disabled={!canFinish} iconBefore={iconCheck}>
      {isMihomo ? m.sb_router_empty_enable_btn_mihomo() : m.sb_router_empty_enable_button()}
    </Button>
  </WizardStep>

  <SbRouterServiceCatalogModal existingRuleSetTags={isMihomo ? [] : $ruleSets.map((r) => r.tag)} />

  {#if groupModalOpen}
    <MihomoGroupEditModal
      open={true}
      proxies={mihomoProxies}
      subscriptions={mihomoSubscriptions}
      onClose={() => (groupModalOpen = false)}
      onSaved={() => {
        groupModalOpen = false;
        void loadMihomoResources();
      }}
    />
  {/if}
</div>

<style>
  .wrap { max-width: 720px; margin: 0 auto; padding: var(--sp-4); }
  .stepper {
    display: flex; align-items: center; gap: 8px; margin: 16px 0 24px; padding: 14px;
    background: var(--bg-secondary); border: 1px solid var(--border); border-radius: var(--radius); font-size: 12px;
  }
  .connector { flex: 1; height: 1px; background: var(--border); min-width: 16px; }
  .tunnel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 10px;
  }
  .sub-hint {
    font-size: 13px;
    color: var(--text-secondary);
  }
  .section-sub-label {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    margin: 8px 0 4px;
  }
  .mt-2 {
    margin-top: 12px;
  }
  .tunnel-chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .t-chip {
    display: inline-flex; align-items: center; gap: 8px; padding: 6px 10px; border-radius: var(--radius-sm);
    background: var(--bg-tertiary); border: 1px solid var(--border); cursor: pointer; font-family: inherit; color: inherit;
    transition: all var(--t-fast, 0.15s);
  }
  .t-chip.group-chip {
    border-color: var(--accent-line, var(--border));
  }
  .t-chip.selected { background: var(--accent-soft); border-color: var(--accent); }
  .t-chip .tag { font-family: var(--font-mono); font-size: 12px; font-weight: 500; }
  .icon-accent {
    color: var(--accent);
  }
  .type-badge {
    font-size: 10px;
    padding: 1px 4px;
    border-radius: 3px;
    background: rgba(255, 255, 255, 0.08);
    color: var(--text-muted);
  }
  .empty-tunnels { font-size: 13px; color: var(--text-muted); }
  .link { background: none; border: 0; padding: 0; color: var(--accent); cursor: pointer; font: inherit; text-decoration: underline; }
  .picker-btn {
    display: grid; grid-template-columns: 40px 1fr auto; align-items: center; gap: 12px; width: 100%;
    padding: 12px 14px; border-radius: var(--radius-sm); background: var(--bg-primary);
    border: 1px dashed var(--accent-line); color: var(--text-primary); cursor: pointer; font-family: inherit;
  }
  .picker-btn:hover { border-color: var(--accent); }
  .picker-icon {
    width: 40px; height: 40px; border-radius: var(--radius-sm); background: var(--accent-soft);
    color: var(--accent); display: flex; align-items: center; justify-content: center; font-size: 20px; font-weight: 600;
  }
  .picker-text { text-align: left; }
  .picker-title { font-size: 14px; font-weight: 600; }
  .picker-sub { font-size: 12px; color: var(--text-muted); }
  .picker-chev { font-size: 18px; color: var(--text-muted); }
  .enable-hint { font-size: 13px; color: var(--text-secondary); margin-bottom: 12px; }
</style>
