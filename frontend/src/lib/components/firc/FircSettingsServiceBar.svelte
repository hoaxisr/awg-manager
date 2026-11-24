<script lang="ts">
  import { m } from '$lib/i18n';
  import type { FircStatus } from '$lib/types/firc';
  import { openFircMigrationModal } from '$lib/stores/fircMigrationModal';
  import { StatusDot, Badge, Button } from '$lib/components/ui';

  type ControlAction = 'start' | 'stop' | 'restart' | 'migrate' | 'restartV1';

  interface Props {
    status: FircStatus | null;
    busyAction: string | null;
    pendingRestart: string[];
    oncontrol: (action: ControlAction) => void;
  }

  let { status, busyAction, pendingRestart, oncontrol }: Props = $props();
</script>

<section class="sec">
  <div class="service-row">
    <div class="service-info">
      <StatusDot variant={status?.running ? 'success' : 'muted'} size="md" />
      <div class="service-meta">
        <div class="service-title-row">
          <span class="service-title">{m.firc_settings_daemon_title()}</span>
          <Badge variant={status?.running ? 'success' : 'muted'} size="sm">
            {status?.running ? m.tunnels_status_active() : m.firc_daemon_stopped()}
          </Badge>
          {#if status?.version}
            <span class="mono-meta">v{status.version}</span>
          {/if}
          {#if status?.dnsProxyEndpoint}
            <Badge variant="info" size="sm" mono>DNS {status.dnsProxyEndpoint}</Badge>
          {/if}
        </div>
        <span class="service-sub">
          {m.firc_settings_daemon_sub()}
        </span>
      </div>
    </div>

    <div class="service-actions">
      {#if status?.running}
        <Button
          variant="secondary"
          size="sm"
          loading={busyAction === 'restart'}
          onclick={() => oncontrol('restart')}
        >
          {m.common_restart()}
        </Button>
        <Button
          variant="danger"
          size="sm"
          loading={busyAction === 'stop'}
          onclick={() => oncontrol('stop')}
        >
          {m.common_stop()}
        </Button>
      {:else}
        <Button
          variant="primary"
          size="sm"
          loading={busyAction === 'start'}
          onclick={() => oncontrol('start')}
        >
          {m.common_start()}
        </Button>
      {/if}
      <Button
        variant="secondary"
        size="sm"
        loading={busyAction === 'migrate'}
        onclick={() => openFircMigrationModal('forward')}
      >
        {m.firc_migration_btn()}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        onclick={() => openFircMigrationModal('snapshots')}
      >
        {m.firc_settings_snapshots()}
      </Button>
    </div>
  </div>

  {#if pendingRestart.length > 0}
    <div class="restart-banner">
      <div class="restart-banner-text">
        <strong>{m.firc_settings_restart_title()}</strong> {m.firc_settings_restart_body({ params: pendingRestart.join(', ') })}
      </div>
      <Button
        variant="primary"
        size="sm"
        loading={busyAction === 'restartV1'}
        onclick={() => oncontrol('restartV1')}
      >
        {m.firc_settings_restart_now()}
      </Button>
    </div>
  {/if}
</section>

<style>
  .sec {
    padding: 14px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    gap: 12px;
    width: 100%;
    max-width: 100%;
    min-width: 0;
    box-sizing: border-box;
    overflow: hidden;
  }

  .service-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }

  .service-info {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .service-meta {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }

  .service-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .service-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary);
  }

  .service-sub {
    font-size: 12px;
    color: var(--text-secondary);
  }

  .mono-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-secondary);
  }

  .service-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .restart-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border-radius: 8px;
    background: var(--color-warning-tint);
    border: 1px solid var(--color-warning-border);
    color: var(--text-primary);
    font-size: 12px;
    flex-wrap: wrap;
  }
</style>
