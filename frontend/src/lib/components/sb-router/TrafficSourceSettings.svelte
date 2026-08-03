<!--
  Общий блок «источник трафика» (deviceMode + NDMS policy).
  Включается в SourceDrawer и StatusDrawer (expert).
-->
<script lang="ts">
  import { Button } from '$lib/components/ui';
  import OutboundOption from './OutboundOption.svelte';
  import PolicyCombobox from './PolicyCombobox.svelte';
  import { pluralize, DEVICE_WORDS } from '$lib/utils/pluralize';
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
    variant === 'expert' ? 'Только устройства policy' : 'Устройства в политике',
  );
  const policySub = $derived(
    variant === 'expert' ? 'трафик из назначенной policy' : 'только привязанные к NDMS policy',
  );
  const policyFieldLabel = $derived(variant === 'expert' ? 'NDMS policy' : 'Политика');
  const allHint = $derived(
    variant === 'expert'
      ? 'При policy обрабатывается только трафик устройств, привязанных к policy в LAN-настройках NDMS.'
      : 'sing-box перехватывает весь LAN-трафик роутера, без фильтра по NDMS policy.',
  );

  function setDeviceMode(m: 'policy' | 'all') {
    onPatch({ deviceMode: m });
  }
</script>

<section class="sec">
  <div class="sec-cap">Какой трафик обрабатывать</div>
  <div class="card-grid">
    <OutboundOption
      label={policyLabel}
      sub={policySub}
      tone="accent"
      selected={cfg.deviceMode !== 'all'}
      onclick={() => setDeviceMode('policy')}
    />
    <OutboundOption
      label="Весь роутер"
      sub="весь LAN-трафик"
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
        В политике <strong>{pluralize(deviceCount, DEVICE_WORDS)}</strong>.
        {#if variant === 'beginner'}
          Привязку MAC-адресов настраивайте на странице политик.
        {/if}
      </p>
      <Button
        variant="ghost"
        size="sm"
        href="/router/policies?policy={encodeURIComponent(cfg.policyName)}"
      >
        Управление устройствами →
      </Button>
    {:else}
      <p class="hint">Выберите или создайте политику — без неё sing-box не обработает трафик устройств.</p>
    {/if}
    {#if cfg.policyName && policyExists === false}
      <p class="warn">Политика «{cfg.policyName}» не найдена в NDMS — создайте заново или выберите другую.</p>
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
