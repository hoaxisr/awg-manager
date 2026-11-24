<script lang="ts">
  // Вкладка «Возврат на Sing-box»: выбор сценария (сохранённая копия или
  // перенос текущих настроек FIRC) и его параметры.
  import { m } from '$lib/i18n';
  import type { FircReverseMigrationSummary, FircSavedSingboxStatus } from '$lib/types/firc';
  import { Button, Badge } from '$lib/components/ui';
  import FircMigrationActionCard from './FircMigrationActionCard.svelte';
  import FircMigrationSavedScenario from './FircMigrationSavedScenario.svelte';
  import FircMigrationConvertScenario from './FircMigrationConvertScenario.svelte';

  type PostAction = 'switch_to_singbox' | 'switch_and_stop_firc' | 'switch_and_uninstall_firc' | 'keep_current';

  interface Props {
    isBusy: boolean;
    singboxMissing: boolean;
    installingSingbox: boolean;
    saved: FircSavedSingboxStatus | null;
    preview: FircReverseMigrationSummary | null;
    scenario: 'saved' | 'convert';
    savedPostAction: PostAction;
    savedCreateSnapshot: boolean;
    revPostAction: PostAction;
    revMode: 'merge' | 'replace';
    revCreateSnapshot: boolean;
    revConfirmReplace: boolean;
    revSelectedTunnelIds: string[];
    revSelectedGroupIds: string[];
    oninstallSingbox: () => void;
    onrefresh: () => void;
  }

  let {
    isBusy,
    singboxMissing,
    installingSingbox,
    saved,
    preview,
    scenario = $bindable(),
    savedPostAction = $bindable(),
    savedCreateSnapshot = $bindable(),
    revPostAction = $bindable(),
    revMode = $bindable(),
    revCreateSnapshot = $bindable(),
    revConfirmReplace = $bindable(),
    revSelectedTunnelIds = $bindable(),
    revSelectedGroupIds = $bindable(),
    oninstallSingbox,
    onrefresh,
  }: Props = $props();

  // Копия есть на диске, но проверить её нечем, пока бинарь Sing-box удалён.
  const pending = $derived(saved?.verificationStatus === 'validator_unavailable' && (saved?.filesCount ?? 0) > 0);
</script>

<div class="wiz-section">
  {#if singboxMissing}
    <div class="wiz-runtime-banner">
      <div class="wiz-runtime-head">
        <strong>{m.firc_migration_rev_singbox_missing_title()}</strong>
        <Badge variant="error" size="sm">{m.firc_migration_engine_removed()}</Badge>
      </div>
      <div class="hint">{m.firc_migration_rev_singbox_missing_desc()}</div>
      <div>
        <Button size="sm" variant="primary" loading={installingSingbox} disabled={isBusy} onclick={oninstallSingbox}>
          {m.firc_migration_install_singbox()}
        </Button>
      </div>
    </div>
  {/if}

  <div class="wiz-box">
    <div class="lbl">{m.firc_migration_rev_scenario_title()}</div>
    <div class="action-cards">
      <FircMigrationActionCard
        name="returnScenario"
        value="saved"
        bind:group={scenario}
        disabled={isBusy}
        title={m.firc_migration_rev_scenario_saved()}
        hint={m.firc_migration_rev_scenario_saved_hint()}
      >
        {#snippet badges()}
          {#if saved?.available}
            <Badge variant="success" size="sm">{m.firc_migration_rev_badge_available()}</Badge>
          {:else if pending}
            <Badge variant="warning" size="sm">{m.firc_migration_saved_badge_pending()}</Badge>
          {:else}
            <Badge variant="warning" size="sm">{m.firc_migration_rev_badge_no_copy()}</Badge>
          {/if}
        {/snippet}
      </FircMigrationActionCard>
      <FircMigrationActionCard
        name="returnScenario"
        value="convert"
        bind:group={scenario}
        disabled={isBusy}
        title={m.firc_migration_rev_scenario_convert()}
        hint={m.firc_migration_rev_scenario_convert_hint()}
        badge={{ variant: 'info', text: m.firc_migration_rev_badge_convert() }}
      />
    </div>
  </div>

  {#if scenario === 'saved'}
    <FircMigrationSavedScenario
      {saved}
      {pending}
      {isBusy}
      {singboxMissing}
      bind:postAction={savedPostAction}
      bind:createSnapshot={savedCreateSnapshot}
    />
  {:else}
    <FircMigrationConvertScenario
      {preview}
      {isBusy}
      {singboxMissing}
      bind:postAction={revPostAction}
      bind:mode={revMode}
      bind:createSnapshot={revCreateSnapshot}
      bind:confirmReplace={revConfirmReplace}
      bind:selectedTunnelIds={revSelectedTunnelIds}
      bind:selectedGroupIds={revSelectedGroupIds}
      {onrefresh}
    />
  {/if}
</div>
