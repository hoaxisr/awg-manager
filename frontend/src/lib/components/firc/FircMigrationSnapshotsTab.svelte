<script lang="ts">
  // Вкладка «Восстановить резервную копию»: ручной снимок, список снимков,
  // двухшаговое восстановление и удаление.
  import { m } from '$lib/i18n';
  import type { FircInstallStatus, FircSnapshotMeta } from '$lib/types/firc';
  import { Button, Badge } from '$lib/components/ui';
  import RichText from '$lib/components/ui/RichText.svelte';
  import { engineLabel, formatTs, snapTriggerLabel, type MigrationEngine } from './fircMigrationNotices';

  interface Props {
    snapshots: FircSnapshotMeta[];
    isBusy: boolean;
    singboxMissing: boolean;
    fircMissing: boolean;
    installingSingbox: boolean;
    installingRuntime: boolean;
    installStatus: FircInstallStatus | null;
    labelInput: string;
    creating: boolean;
    restoringId: string | null;
    deletingId: string | null;
    confirmRestoreId: string | null;
    missingEngineFor: (snap: FircSnapshotMeta) => MigrationEngine | null;
    oninstallSingbox: () => void;
    oninstallFirc: () => void;
    oncreate: () => void;
    onrestore: (snap: FircSnapshotMeta) => void;
    ondelete: (id: string) => void;
  }

  let {
    snapshots,
    isBusy,
    singboxMissing,
    fircMissing,
    installingSingbox,
    installingRuntime,
    installStatus,
    labelInput = $bindable(),
    creating,
    restoringId,
    deletingId,
    confirmRestoreId = $bindable(),
    missingEngineFor,
    oninstallSingbox,
    oninstallFirc,
    oncreate,
    onrestore,
    ondelete,
  }: Props = $props();
</script>

<div class="wiz-section">
  <div class="wiz-intro-banner">
    <div>
      <strong>{m.firc_migration_snap_intro_title()}</strong>
      <div class="hint"><RichText text={m.firc_migration_snap_intro()} /></div>
    </div>
  </div>

  {#if singboxMissing || fircMissing}
    <div class="wiz-runtime-banner">
      <div class="wiz-runtime-head">
        <strong>{m.firc_migration_snap_missing_title()}</strong>
        <Badge variant="warning" size="sm">{m.firc_migration_snap_missing_badge()}</Badge>
      </div>
      <div class="hint">{m.firc_migration_snap_missing_desc()}</div>
      <div class="eng-confirm-btns">
        {#if singboxMissing}
          <Button size="sm" variant="primary" loading={installingSingbox} disabled={isBusy} onclick={oninstallSingbox}>
            {m.firc_migration_install_singbox()}
          </Button>
        {/if}
        {#if fircMissing}
          <Button
            size="sm"
            variant="primary"
            loading={installingRuntime || Boolean(installStatus?.busy)}
            disabled={isBusy || installStatus?.available === false}
            onclick={oninstallFirc}
          >
            {installingRuntime || installStatus?.busy ? m.firc_runtime_installing() : m.firc_runtime_install()}
          </Button>
        {/if}
      </div>
    </div>
  {/if}

  <div class="wiz-box">
    <div class="lbl">{m.firc_migration_snap_create_title()}</div>
    <div class="snap-create-row">
      <input
        type="text"
        class="inp"
        disabled={isBusy}
        placeholder={m.firc_migration_snap_label_placeholder()}
        bind:value={labelInput}
      />
      <Button variant="secondary" size="sm" loading={creating} disabled={isBusy} onclick={oncreate}>
        {m.firc_migration_snap_create()}
      </Button>
    </div>
  </div>

  <div class="wiz-box">
    <div class="sec-head">
      <span class="lbl">{m.firc_migration_snap_list_title({ count: snapshots.length })}</span>
    </div>

    {#if snapshots.length === 0}
      <div class="hint">{m.firc_migration_snap_list_empty()}</div>
    {:else}
      <div class="snap-list">
        {#each snapshots as snap (snap.id)}
          {@const missingEng = missingEngineFor(snap)}
          {@const confirming = confirmRestoreId === snap.id}
          <div class="snap-card">
            <div class="snap-card-main">
              <div class="snap-card-title">
                <strong>{snap.label || snap.id}</strong>
                <Badge variant={snap.trigger === 'manual' ? 'info' : 'muted'} size="sm">
                  {snapTriggerLabel(snap.trigger)}
                </Badge>
                <Badge variant="success" size="sm" mono>
                  {m.firc_migration_snap_engine({ engine: snap.activeEngine })}
                </Badge>
              </div>
              <div class="hint mono">
                {m.firc_migration_snap_meta({ id: snap.id, time: formatTs(snap.createdAt), files: snap.filesCount })}
              </div>
              <div class="hint">
                <RichText
                  text={m.firc_migration_snap_scope({
                    sbTunnels: snap.singboxTunnelsCount,
                    sbSubs: snap.singboxSubscriptionsCount,
                    sbGroups: snap.singboxGroupsCount,
                    fircTunnels: snap.fircTunnelsCount,
                    fircGroups: snap.fircGroupsCount,
                  })}
                />
              </div>
              {#if missingEng}
                <div class="warn-confirm-box" style="margin-top: 6px;">
                  <RichText
                    text={m.firc_migration_snap_missing_engine({ engine: missingEng, label: engineLabel(missingEng) })}
                  />
                </div>
              {:else if confirming}
                <div class="warn-confirm-box" style="margin-top: 6px;">
                  <RichText
                    text={m.firc_migration_snap_confirm_restore({
                      time: formatTs(snap.createdAt),
                      engine: snap.activeEngine,
                    })}
                  />
                </div>
              {/if}
            </div>
            <div class="snap-card-actions">
              {#if confirming}
                <Button variant="ghost" size="sm" disabled={isBusy} onclick={() => (confirmRestoreId = null)}>
                  {m.common_cancel()}
                </Button>
              {/if}
              <Button
                variant={confirming ? 'danger' : 'primary'}
                size="sm"
                loading={restoringId === snap.id}
                disabled={isBusy || missingEng !== null}
                onclick={() => onrestore(snap)}
              >
                {confirming ? m.firc_migration_snap_confirm_replace() : m.firc_migration_snap_restore()}
              </Button>
              <Button
                variant="danger"
                size="sm"
                loading={deletingId === snap.id}
                disabled={isBusy}
                onclick={() => ondelete(snap.id)}
              >
                {m.common_delete()}
              </Button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>

<style>
  .snap-create-row {
    display: flex;
    gap: 8px;
    align-items: center;
  }

  .snap-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 260px;
    overflow-y: auto;
  }

  .snap-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 9px 10px;
    border-radius: 7px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    flex-wrap: wrap;
  }

  .snap-card-main {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1 1 auto;
    min-width: 0;
  }

  .snap-card-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-primary);
    flex-wrap: wrap;
  }

  .snap-card-actions {
    display: flex;
    gap: 6px;
    flex-shrink: 0;
  }
</style>
