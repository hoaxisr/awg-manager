<script lang="ts">
  // Полоса состояния двух движков мастера миграции: установлен ли, запущен ли,
  // и кнопки «Установить» / двухшаговое «Удалить» для неактивного движка.
  import { m } from '$lib/i18n';
  import type { FircInstallStatus } from '$lib/types/firc';
  import { Button, Badge } from '$lib/components/ui';
  import RichText from '$lib/components/ui/RichText.svelte';
  import type { MigrationEngine } from './fircMigrationNotices';

  interface Props {
    activeEngine: string;
    isBusy: boolean;
    singboxMissing: boolean;
    singboxRunning: boolean;
    singboxTunnelsCount: number;
    singboxSubscriptionsCount: number;
    fircMissing: boolean;
    fircRunning: boolean;
    fircTunnelsCount: number;
    fircGroupsCount: number;
    canPurgeSingbox: boolean;
    canPurgeFirc: boolean;
    confirmPurgeEngine: MigrationEngine | null;
    purgingEngine: MigrationEngine | null;
    installingSingbox: boolean;
    installingRuntime: boolean;
    installStatus: FircInstallStatus | null;
    oninstallSingbox: () => void;
    oninstallFirc: () => void;
    onpurge: (engine: MigrationEngine) => void;
    oncancelPurge: () => void;
  }

  let {
    activeEngine,
    isBusy,
    singboxMissing,
    singboxRunning,
    singboxTunnelsCount,
    singboxSubscriptionsCount,
    fircMissing,
    fircRunning,
    fircTunnelsCount,
    fircGroupsCount,
    canPurgeSingbox,
    canPurgeFirc,
    confirmPurgeEngine,
    purgingEngine,
    installingSingbox,
    installingRuntime,
    installStatus,
    oninstallSingbox,
    oninstallFirc,
    onpurge,
    oncancelPurge,
  }: Props = $props();
</script>

{#snippet statusBadges(active: boolean, missing: boolean, running: boolean)}
  <div class="eng-badges">
    {#if active}
      <Badge variant="success" size="sm">{m.firc_migration_engine_active()}</Badge>
    {:else}
      <Badge variant="muted" size="sm">{m.firc_migration_engine_inactive()}</Badge>
    {/if}
    {#if missing}
      <Badge variant="error" size="sm">{m.firc_migration_engine_removed()}</Badge>
    {:else}
      <Badge variant={running ? 'info' : 'muted'} size="sm">
        {running ? m.firc_daemon_running() : m.firc_daemon_stopped()}
      </Badge>
    {/if}
  </div>
{/snippet}

{#snippet purgeControls(engine: MigrationEngine, warning: string, purgeLabel: string)}
  {#if confirmPurgeEngine === engine}
    <div class="warn-confirm-box" style="padding: 6px 8px; width: 100%;">
      <RichText text={warning} />
    </div>
    <div class="eng-confirm-btns">
      <Button variant="ghost" size="sm" disabled={isBusy} onclick={oncancelPurge}>
        {m.common_cancel()}
      </Button>
      <Button
        variant="danger"
        size="sm"
        loading={purgingEngine === engine}
        disabled={isBusy}
        onclick={() => onpurge(engine)}
      >
        {m.firc_migration_purge_confirm()}
      </Button>
    </div>
  {:else}
    <Button variant="danger" size="sm" disabled={isBusy} onclick={() => onpurge(engine)}>
      {purgeLabel}
    </Button>
  {/if}
{/snippet}

<div class="wiz-engines-strip">
  <div class="eng-card" class:eng-active={activeEngine === 'sing-box'}>
    <div class="eng-head">
      <strong>Sing-box</strong>
      {@render statusBadges(activeEngine === 'sing-box', singboxMissing, singboxRunning)}
    </div>
    <div class="eng-meta">
      <RichText
        text={m.firc_migration_engine_meta_singbox({ tunnels: singboxTunnelsCount, subs: singboxSubscriptionsCount })}
      />
    </div>
    <div class="eng-actions">
      {#if singboxMissing}
        <Button variant="primary" size="sm" loading={installingSingbox} disabled={isBusy} onclick={oninstallSingbox}>
          {m.firc_migration_install_singbox()}
        </Button>
      {:else if canPurgeSingbox}
        {@render purgeControls('sing-box', m.firc_migration_purge_warn_singbox(), m.firc_migration_purge_singbox())}
      {/if}
    </div>
  </div>

  <div class="eng-card" class:eng-active={activeEngine === 'firc'}>
    <div class="eng-head">
      <strong>FIRC</strong>
      {@render statusBadges(activeEngine === 'firc', fircMissing, fircRunning)}
    </div>
    <div class="eng-meta">
      <RichText text={m.firc_migration_engine_meta_firc({ tunnels: fircTunnelsCount, groups: fircGroupsCount })} />
    </div>
    <div class="eng-actions">
      {#if fircMissing}
        <Button
          variant="primary"
          size="sm"
          loading={installingRuntime || Boolean(installStatus?.busy)}
          disabled={isBusy || installStatus?.available === false}
          onclick={oninstallFirc}
        >
          {installingRuntime || installStatus?.busy ? m.firc_runtime_installing() : m.firc_runtime_install()}
        </Button>
      {:else if canPurgeFirc}
        {@render purgeControls('firc', m.firc_migration_purge_warn_firc(), m.firc_migration_purge_firc())}
      {/if}
    </div>
  </div>
</div>

<style>
  .wiz-engines-strip {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 8px;
  }

  @media (max-width: 600px) {
    .wiz-engines-strip {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .eng-card {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 9px 11px;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--bg-secondary);
  }

  .eng-card.eng-active {
    border-color: color-mix(in srgb, var(--color-accent) 45%, var(--border));
    background: color-mix(in srgb, var(--color-accent) 5%, var(--bg-secondary));
  }

  .eng-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 12.5px;
    color: var(--text-primary);
  }

  .eng-head strong {
    white-space: nowrap;
    flex-shrink: 0;
  }

  .eng-badges {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
  }

  .eng-meta {
    font-size: 11px;
    color: var(--text-secondary);
  }

  .eng-actions {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 6px;
    margin-top: 2px;
  }
</style>
