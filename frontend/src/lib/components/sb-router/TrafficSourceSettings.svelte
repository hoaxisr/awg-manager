<!--
  Общий блок «источник трафика» (deviceMode + NDMS policy).
  Включается в SourceDrawer и StatusDrawer (expert).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { Button } from '$lib/components/ui';
  import OutboundOption from './OutboundOption.svelte';
  import PolicyCombobox from './PolicyCombobox.svelte';
  import type { SingboxRouterSettings } from '$lib/types';

  interface Props {
    cfg: SingboxRouterSettings;
    deviceCount?: number;
    policyExists?: boolean;
    variant?: 'beginner' | 'expert';
    onPatch: (patch: Partial<SingboxRouterSettings>) => void;
  }

  let {
    cfg,
    deviceCount = 0,
    policyExists = true,
    variant = 'beginner',
    onPatch,
  }: Props = $props();

  const policyLabel = $derived(
    variant === 'expert' ? m.sb_router_source_policy_only_expert() : m.sb_router_source_policy_only_beginner(),
  );
  const policySub = $derived(
    variant === 'expert' ? m.sb_router_source_policy_sub_expert() : m.sb_router_source_policy_sub_beginner(),
  );
  const policyFieldLabel = $derived(variant === 'expert' ? m.sb_router_source_field_policy_expert() : m.sb_router_source_field_policy_beginner());
  const allHint = $derived(
    variant === 'expert'
      ? m.sb_router_source_all_hint_expert()
      : m.sb_router_source_all_hint_beginner(),
  );

  function setDeviceMode(mode: 'policy' | 'all') {
    onPatch({ deviceMode: mode });
  }
</script>

<section class="sec">
  <div class="sec-cap">{m.sb_router_source_what_traffic()}</div>
  <div class="card-grid">
    <OutboundOption
      label={policyLabel}
      sub={policySub}
      tone="accent"
      selected={cfg.deviceMode !== 'all'}
      onclick={() => setDeviceMode('policy')}
    />
    <OutboundOption
      label={m.sb_router_source_all_router()}
      sub={m.sb_router_source_all_router_sub()}
      tone="accent"
      selected={cfg.deviceMode === 'all'}
      onclick={() => setDeviceMode('all')}
    />
  </div>
</section>

{#if cfg.deviceMode !== 'all'}
  <section class="sec">
    <div class="sec-cap">NDMS Access Policy</div>
    <div class="field">
      <span class="lbl">{policyFieldLabel}</span>
      <PolicyCombobox value={cfg.policyName} onChange={(name) => onPatch({ policyName: name })} />
    </div>
    {#if cfg.policyName}
      <p class="hint">
        {m.sb_router_source_policy_in_pre()} <strong>{m.sb_router_devices_count({ count: deviceCount })}</strong>.
        {#if variant === 'beginner'}
          {m.sb_router_source_bind_hint()}
        {/if}
      </p>
      <Button
        variant="ghost"
        size="sm"
        href="/routing?tab=policy&policy={encodeURIComponent(cfg.policyName)}"
      >
        {m.sb_router_source_manage_devices()}
      </Button>
    {:else}
      <p class="hint">{m.sb_router_source_pick_policy()}</p>
    {/if}
    {#if cfg.policyName && policyExists === false}
      <p class="warn">{m.sb_router_source_policy_missing({ name: cfg.policyName })}</p>
    {/if}
  </section>
{:else}
  <section class="sec">
    <p class="hint">{allHint}</p>
  </section>
{/if}

<style>
  .sec {
    padding: 14px var(--sp-4);
    border-bottom: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .sec:last-of-type { border-bottom: 0; }
  .sec-cap {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }
  .card-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  @media (max-width: 480px) { .card-grid { grid-template-columns: 1fr; } }
  .field { display: flex; flex-direction: column; gap: 4px; }
  .lbl { font-size: 11px; color: var(--text-muted); font-weight: 500; }
  .hint { margin: 0; font-size: 11.5px; color: var(--text-muted); line-height: 1.4; }
  .hint strong { color: var(--text-primary); font-weight: 600; }
  .warn { margin: 0; font-size: 11.5px; color: var(--color-error, #dc2626); line-height: 1.4; }
</style>
