<script lang="ts">
  // Итог последнего действия мастера миграции: конфигурация, снимок, запуск, движок.
  import { m, uiText } from '$lib/i18n';
  import RichText from '$lib/components/ui/RichText.svelte';
  import type { ExecNotice } from './fircMigrationNotices';

  let { notice }: { notice: ExecNotice } = $props();
</script>

<div class="wiz-exec-notice" class:is-error={notice.kind === 'error'}>
  <div class="wiz-exec-row">
    <strong>{m.firc_migration_exec_config()}</strong>
    <span>{uiText(notice.configStatus)}</span>
  </div>
  <div class="wiz-exec-row">
    <strong>{m.firc_migration_exec_snapshot()}</strong>
    <span>{uiText(notice.snapshotStatus)}</span>
  </div>
  {#if notice.fircStartStatus}
    <div class="wiz-exec-row">
      <strong>{m.firc_migration_exec_start()}</strong>
      <span>{uiText(notice.fircStartStatus)}</span>
    </div>
  {/if}
  <div class="wiz-exec-row">
    <strong>{m.firc_migration_exec_engine({ n: notice.fircStartStatus ? 4 : 3 })}</strong>
    <span>{uiText(notice.engineStatus)}</span>
  </div>
  {#if notice.recoveryRequired}
    <div class="wiz-exec-recovery">
      <RichText text={m.firc_migration_exec_recovery()} />
    </div>
  {/if}
</div>

<style>
  .wiz-exec-notice {
    display: flex;
    flex-direction: column;
    gap: 5px;
    padding: 10px 12px;
    border-radius: 8px;
    border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 45%, var(--border));
    background: color-mix(in srgb, var(--color-success, #22c55e) 10%, var(--bg-secondary));
    font-size: 12px;
  }

  .wiz-exec-notice.is-error {
    border-color: color-mix(in srgb, var(--color-danger, #ef4444) 55%, var(--border));
    background: color-mix(in srgb, var(--color-danger, #ef4444) 10%, var(--bg-secondary));
  }

  .wiz-exec-row {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }

  .wiz-exec-recovery {
    margin-top: 4px;
    padding-top: 6px;
    border-top: 1px dashed var(--border);
    font-weight: 600;
    color: var(--color-danger, #ef4444);
  }
</style>
