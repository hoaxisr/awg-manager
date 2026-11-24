<script lang="ts">
  // Сценарий 1 возврата на Sing-box: сохранённая до перехода конфигурация.
  import { m } from '$lib/i18n';
  import type { FircSavedSingboxStatus } from '$lib/types/firc';
  import { Badge } from '$lib/components/ui';
  import RichText from '$lib/components/ui/RichText.svelte';
  import FircMigrationActionCard from './FircMigrationActionCard.svelte';
  import { formatTs } from './fircMigrationNotices';

  type PostAction = 'switch_to_singbox' | 'switch_and_stop_firc' | 'switch_and_uninstall_firc' | 'keep_current';

  interface Props {
    saved: FircSavedSingboxStatus | null;
    pending: boolean;
    isBusy: boolean;
    singboxMissing: boolean;
    postAction: PostAction;
    createSnapshot: boolean;
  }

  let { saved, pending, isBusy, singboxMissing, postAction = $bindable(), createSnapshot = $bindable() }: Props =
    $props();

  const blocked = $derived(!saved?.available || singboxMissing);
</script>

{#snippet details(s: FircSavedSingboxStatus)}
  <div class="hint">
    <RichText text={m.firc_migration_saved_date({ date: formatTs(s.createdAt) })} />
    {#if s.label}
      · <RichText text={m.firc_migration_saved_source({ label: s.label })} />
    {/if}
  </div>
  <div class="hint">
    <RichText
      text={m.firc_migration_saved_contents({
        tunnels: s.tunnelsCount,
        subs: s.subscriptionsCount,
        groups: s.groupsCount,
        files: s.filesCount,
      })}
    />
  </div>
{/snippet}

<div class="wiz-box">
  <div class="sec-head">
    <span class="lbl">{m.firc_migration_saved_title()}</span>
    {#if saved?.available}
      <Badge variant="success" size="sm">{m.firc_migration_saved_badge_verified()}</Badge>
    {:else if pending}
      <Badge variant="warning" size="sm">{m.firc_migration_saved_badge_pending()}</Badge>
    {:else}
      <Badge variant="error" size="sm">{m.firc_migration_saved_badge_unavailable()}</Badge>
    {/if}
  </div>

  {#if saved?.available}
    {@render details(saved)}
    <div class="warn-confirm-box"><RichText text={m.firc_migration_saved_warn_changes()} /></div>
  {:else if pending && saved}
    {@render details(saved)}
    <div class="warn-confirm-box">{m.firc_migration_saved_warn_pending()}</div>
  {:else}
    <div class="warn-confirm-box">
      {#if saved?.unavailableReason}
        <strong>{m.firc_migration_saved_reason()}</strong> {saved.unavailableReason}<br />
      {/if}
      {m.firc_migration_saved_warn_missing()}
    </div>
  {/if}
</div>

<div class="wiz-box">
  <div class="lbl">{m.firc_migration_saved_action_title()}</div>
  <div class="action-cards">
    <FircMigrationActionCard
      name="savedPostAction"
      value="switch_to_singbox"
      bind:group={postAction}
      disabled={isBusy || blocked}
      dimmed={blocked}
      title={m.firc_migration_saved_post_switch()}
      hint={m.firc_migration_saved_post_switch_hint()}
      badge={{ variant: 'success', text: m.firc_migration_badge_recommended() }}
    />
    <FircMigrationActionCard
      name="savedPostAction"
      value="switch_and_stop_firc"
      bind:group={postAction}
      disabled={isBusy || blocked}
      dimmed={blocked}
      title={m.firc_migration_saved_post_stop()}
      hint={m.firc_migration_saved_post_stop_hint()}
    />
    <FircMigrationActionCard
      name="savedPostAction"
      value="switch_and_uninstall_firc"
      bind:group={postAction}
      disabled={isBusy || blocked}
      dimmed={blocked}
      title={m.firc_migration_to_singbox_post_uninstall()}
      hint={m.firc_migration_to_singbox_post_uninstall_hint()}
      badge={{ variant: 'warning', text: m.firc_migration_badge_max_ram() }}
    />
  </div>

  <label class="check-row" style="margin-top: 6px;">
    <input
      type="checkbox"
      disabled={isBusy || blocked || postAction === 'switch_and_uninstall_firc'}
      bind:checked={createSnapshot}
    />
    <span>
      {postAction === 'switch_and_uninstall_firc'
        ? m.firc_migration_saved_snapshot_mandatory()
        : m.firc_migration_saved_snapshot()}
    </span>
  </label>
</div>
