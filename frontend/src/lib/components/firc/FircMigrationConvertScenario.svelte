<script lang="ts">
  // Сценарий 2 возврата на Sing-box: перенос текущих настроек FIRC в Sing-box.
  import { m } from '$lib/i18n';
  import type { FircReverseMigrationSummary } from '$lib/types/firc';
  import RichText from '$lib/components/ui/RichText.svelte';
  import FircMigrationActionCard from './FircMigrationActionCard.svelte';
  import FircMigrationPickList, { type PickItem } from './FircMigrationPickList.svelte';
  import { toggleId } from './fircMigrationNotices';

  type PostAction = 'switch_to_singbox' | 'switch_and_stop_firc' | 'switch_and_uninstall_firc' | 'keep_current';

  interface Props {
    preview: FircReverseMigrationSummary | null;
    isBusy: boolean;
    singboxMissing: boolean;
    postAction: PostAction;
    mode: 'merge' | 'replace';
    createSnapshot: boolean;
    confirmReplace: boolean;
    selectedTunnelIds: string[];
    selectedGroupIds: string[];
    onrefresh: () => void;
  }

  let {
    preview,
    isBusy,
    singboxMissing,
    postAction = $bindable(),
    mode = $bindable(),
    createSnapshot = $bindable(),
    confirmReplace = $bindable(),
    selectedTunnelIds = $bindable(),
    selectedGroupIds = $bindable(),
    onrefresh,
  }: Props = $props();

  const tunnels = $derived(preview?.tunnels ?? []);
  const groups = $derived(preview?.groups ?? []);
  const skipped = $derived(preview?.skipped ?? []);

  const tunnelItems = $derived<PickItem[]>(
    tunnels.map((tun) => ({
      id: tun.id,
      title: tun.name,
      sub: m.firc_migration_rev_tunnel_sub({ device: tun.device, tag: tun.targetTag }),
      badges: [
        tun.kind === 'subscription'
          ? { variant: 'info', text: m.firc_migration_kind_subscription() }
          : { variant: 'muted', text: m.firc_migration_kind_nodes() },
      ],
      pill: tun.linksCount > 0 ? m.firc_migration_pill_nodes({ count: tun.linksCount }) : undefined,
    })),
  );

  const groupItems = $derived<PickItem[]>(
    groups.map((grp) => ({
      id: grp.id,
      title: grp.name,
      sub: `→ outbound: ${grp.outboundTag}`,
      badges: grp.ruleSetUrl ? [{ variant: 'info', text: m.firc_migration_srs_list() }] : [],
      pill: m.firc_migration_pill_rules({ count: grp.rulesCount }),
    })),
  );
</script>

<div class="wiz-box">
  <div class="lbl">{m.firc_migration_rev_post_title()}</div>
  <div class="action-cards">
    <FircMigrationActionCard
      name="revPostAction"
      value="switch_to_singbox"
      bind:group={postAction}
      disabled={isBusy || singboxMissing}
      dimmed={singboxMissing}
      title={m.firc_migration_rev_post_switch()}
      hint={m.firc_migration_rev_post_switch_hint()}
      badge={{ variant: 'success', text: m.firc_migration_badge_recommended() }}
    />
    <FircMigrationActionCard
      name="revPostAction"
      value="switch_and_stop_firc"
      bind:group={postAction}
      disabled={isBusy || singboxMissing}
      dimmed={singboxMissing}
      title={m.firc_migration_rev_post_stop()}
      hint={m.firc_migration_rev_post_stop_hint()}
    />
    <FircMigrationActionCard
      name="revPostAction"
      value="switch_and_uninstall_firc"
      bind:group={postAction}
      disabled={isBusy || singboxMissing}
      dimmed={singboxMissing}
      title={m.firc_migration_to_singbox_post_uninstall()}
      hint={m.firc_migration_to_singbox_post_uninstall_hint()}
      badge={{ variant: 'warning', text: m.firc_migration_badge_max_ram() }}
    />
    <FircMigrationActionCard
      name="revPostAction"
      value="keep_current"
      bind:group={postAction}
      disabled={isBusy || singboxMissing}
      dimmed={singboxMissing}
      title={m.firc_migration_rev_post_keep()}
      hint={m.firc_migration_rev_post_keep_hint()}
    />
  </div>
</div>

<div class="field-grid-2">
  <div class="field">
    <label class="lbl" for="wiz-rev-mode">{m.firc_migration_rev_mode_label()}</label>
    <select
      id="wiz-rev-mode"
      class="inp"
      value={mode}
      disabled={isBusy}
      onchange={(e) => {
        mode = (e.currentTarget as HTMLSelectElement).value as 'merge' | 'replace';
        confirmReplace = false;
        onrefresh();
      }}
    >
      <option value="merge">{m.firc_migration_rev_mode_merge()}</option>
      <option value="replace">{m.firc_migration_rev_mode_replace()}</option>
    </select>
  </div>
  <div class="field" style="justify-content: flex-end;">
    <label class="check-row">
      <input
        type="checkbox"
        disabled={isBusy || postAction === 'switch_and_uninstall_firc'}
        bind:checked={createSnapshot}
      />
      <span>
        {postAction === 'switch_and_uninstall_firc'
          ? m.firc_migration_rev_snapshot_mandatory()
          : m.firc_migration_rev_snapshot()}
      </span>
    </label>
  </div>
</div>

{#if mode === 'replace'}
  <label class="check-row warn-confirm-box">
    <input type="checkbox" disabled={isBusy} bind:checked={confirmReplace} />
    <span><RichText text={m.firc_migration_rev_confirm_replace()} /></span>
  </label>
{/if}

{#if skipped.length > 0}
  <div class="warn-confirm-box">
    <strong>{m.firc_migration_rev_skipped({ count: skipped.length })}</strong>
    <ul class="skip-list">
      {#each skipped as sk, i (i)}
        <li>{sk}</li>
      {/each}
    </ul>
  </div>
{/if}

<FircMigrationPickList
  label={m.firc_migration_rev_tunnels_label({ selected: selectedTunnelIds.length, total: tunnels.length })}
  filters={[
    { label: m.firc_migration_filter_all(), run: () => (selectedTunnelIds = tunnels.map((t) => t.id)) },
    { label: m.firc_migration_filter_none(), run: () => (selectedTunnelIds = []) },
  ]}
  items={tunnelItems}
  selected={selectedTunnelIds}
  empty={m.firc_migration_rev_tunnels_empty()}
  disabled={isBusy}
  ontoggle={(id) => {
    if (!isBusy) selectedTunnelIds = toggleId(selectedTunnelIds, id);
  }}
/>

<FircMigrationPickList
  label={m.firc_migration_rev_groups_label({ selected: selectedGroupIds.length, total: groups.length })}
  filters={[
    { label: m.firc_migration_filter_all(), run: () => (selectedGroupIds = groups.map((g) => g.id)) },
    { label: m.firc_migration_filter_none(), run: () => (selectedGroupIds = []) },
  ]}
  items={groupItems}
  selected={selectedGroupIds}
  empty={m.firc_migration_rev_groups_empty()}
  disabled={isBusy}
  ontoggle={(id) => {
    if (!isBusy) selectedGroupIds = toggleId(selectedGroupIds, id);
  }}
/>

<style>
  .skip-list {
    margin: 4px 0 0 18px;
    padding: 0;
    font-size: 11px;
  }
</style>
