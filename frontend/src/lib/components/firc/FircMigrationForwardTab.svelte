<script lang="ts">
  // Вкладка «Из Sing-box в FIRC»: что сделать с Sing-box, режим записи,
  // источник правил, FakeIP и выбор туннелей/групп для переноса.
  import { m } from '$lib/i18n';
  import type { FircInstallStatus, FircMigrationSummary } from '$lib/types/firc';
  import { Button, Badge } from '$lib/components/ui';
  import RichText from '$lib/components/ui/RichText.svelte';
  import FircMigrationActionCard from './FircMigrationActionCard.svelte';
  import FircMigrationPickList, { type PickItem } from './FircMigrationPickList.svelte';
  import { isClientRouteGroup, isSubscriptionTunnel, toggleId } from './fircMigrationNotices';

  type PostAction = 'switch_standby' | 'switch_and_stop' | 'switch_and_uninstall' | 'keep_current';
  type RouterSource = 'auto' | 'tproxy' | 'fakeip' | 'both';

  interface Props {
    preview: FircMigrationSummary | null;
    isBusy: boolean;
    runtimeMissing: boolean;
    installStatus: FircInstallStatus | null;
    installingRuntime: boolean;
    activeEngine: string;
    postAction: PostAction;
    mode: 'merge' | 'replace';
    routerSource: RouterSource;
    createSnapshot: boolean;
    includeFakeIP: boolean;
    confirmReplace: boolean;
    selectedTunnelIds: string[];
    selectedGroupIds: string[];
    oninstallRuntime: () => void;
    onrefresh: (routerSource: RouterSource) => void;
  }

  let {
    preview,
    isBusy,
    runtimeMissing,
    installStatus,
    installingRuntime,
    activeEngine,
    postAction = $bindable(),
    mode = $bindable(),
    routerSource = $bindable(),
    createSnapshot = $bindable(),
    includeFakeIP = $bindable(),
    confirmReplace = $bindable(),
    selectedTunnelIds = $bindable(),
    selectedGroupIds = $bindable(),
    oninstallRuntime,
    onrefresh,
  }: Props = $props();

  const tunnels = $derived(preview?.tunnels ?? []);
  const groups = $derived(preview?.groups ?? []);

  const tunnelItems = $derived<PickItem[]>(
    tunnels.map((tun) => {
      const isSub = isSubscriptionTunnel(tun);
      return {
        id: tun.id,
        title: tun.description || tun.id,
        sub: `${tun.device} · id:${tun.id}`,
        badges: [
          { variant: isSub ? 'info' : 'muted', text: isSub ? m.firc_migration_kind_subscription() : m.firc_migration_kind_node() },
        ],
        pill: m.firc_migration_pill_sources({ count: tun.sources?.length ?? 0 }),
      };
    }),
  );

  const groupItems = $derived<PickItem[]>(
    groups.map((grp) => {
      const target = Array.isArray(grp.interface) ? grp.interface.join(' → ') : grp.interface;
      const allow = grp.devices?.allow ?? [];
      const badges: PickItem['badges'] = [];
      if (isClientRouteGroup(grp)) badges.push({ variant: 'warning', text: m.firc_migration_kind_device() });
      if (grp.list?.url) badges.push({ variant: 'info', text: m.firc_migration_srs_list() });
      return {
        id: grp.id,
        title: grp.name,
        sub: allow.length > 0 ? m.firc_migration_group_sub_clients({ target, clients: allow.join(', ') }) : `→ ${target}`,
        badges,
        pill: m.firc_migration_pill_rules({ count: grp.rules?.length ?? 0 }),
      };
    }),
  );

  function pickTunnels(filter: 'all' | 'subs' | 'nodes' | 'none') {
    if (isBusy) return;
    if (filter === 'all') selectedTunnelIds = tunnels.map((t) => t.id);
    else if (filter === 'subs') selectedTunnelIds = tunnels.filter(isSubscriptionTunnel).map((t) => t.id);
    else if (filter === 'nodes') selectedTunnelIds = tunnels.filter((t) => !isSubscriptionTunnel(t)).map((t) => t.id);
    else selectedTunnelIds = [];
  }

  function pickGroups(filter: 'all' | 'rules_only' | 'devices_only' | 'none') {
    if (isBusy) return;
    if (filter === 'all') selectedGroupIds = groups.map((g) => g.id);
    else if (filter === 'rules_only') selectedGroupIds = groups.filter((g) => !isClientRouteGroup(g)).map((g) => g.id);
    else if (filter === 'devices_only') selectedGroupIds = groups.filter(isClientRouteGroup).map((g) => g.id);
    else selectedGroupIds = [];
  }

  const fakeipRange = $derived(preview?.fakeip?.inet4Range || '198.18.0.0/15');
</script>

<div class="wiz-section">
  <div class="wiz-intro-banner">
    <div>
      <strong>{m.firc_migration_fwd_intro_title()}</strong>
      <div class="hint">{m.firc_migration_fwd_intro()}</div>
    </div>
  </div>

  {#if runtimeMissing}
    <div class="wiz-runtime-banner">
      <div class="wiz-runtime-head">
        <strong>{m.firc_wizard_runtime_missing_title()}</strong>
        <Badge variant="error" size="sm">{m.firc_runtime_absent()}</Badge>
      </div>
      <div class="hint">{m.firc_wizard_runtime_missing_desc()}</div>
      <div class="hint">
        {installStatus?.unavailableReason || m.firc_wizard_runtime_missing_keep_hint()}
      </div>
      {#if installStatus?.available}
        <div>
          <Button
            size="sm"
            variant="primary"
            loading={installingRuntime || Boolean(installStatus?.busy)}
            onclick={oninstallRuntime}
          >
            {installingRuntime || installStatus?.busy ? m.firc_runtime_installing() : m.firc_runtime_install()}
          </Button>
        </div>
      {/if}
    </div>
  {/if}

  <div class="wiz-box">
    <div class="lbl">{m.firc_migration_fwd_post_title()}</div>
    <div class="action-cards">
      <FircMigrationActionCard
        name="fwdPostAction"
        value="switch_standby"
        bind:group={postAction}
        disabled={isBusy || runtimeMissing}
        dimmed={runtimeMissing}
        title={m.firc_migration_fwd_post_standby()}
        hint={m.firc_migration_fwd_post_standby_hint()}
        badge={{ variant: 'success', text: m.firc_migration_badge_recommended() }}
      />
      <FircMigrationActionCard
        name="fwdPostAction"
        value="switch_and_stop"
        bind:group={postAction}
        disabled={isBusy || runtimeMissing}
        dimmed={runtimeMissing}
        title={m.firc_migration_fwd_post_stop()}
        hint={m.firc_migration_fwd_post_stop_hint()}
        badge={{ variant: 'info', text: m.firc_migration_badge_save_ram() }}
      />
      <FircMigrationActionCard
        name="fwdPostAction"
        value="switch_and_uninstall"
        bind:group={postAction}
        disabled={isBusy || runtimeMissing}
        dimmed={runtimeMissing}
        title={m.firc_migration_fwd_post_uninstall()}
        hint={m.firc_migration_fwd_post_uninstall_hint()}
        badge={{ variant: 'warning', text: m.firc_migration_badge_max_ram() }}
      />
      <FircMigrationActionCard
        name="fwdPostAction"
        value="keep_current"
        bind:group={postAction}
        disabled={isBusy}
        title={m.firc_migration_fwd_post_keep()}
        hint={m.firc_migration_fwd_post_keep_hint({ engine: activeEngine })}
      />
    </div>
  </div>

  <div class="field-grid-2">
    <div class="field">
      <label class="lbl" for="wiz-fwd-mode">{m.firc_migration_fwd_mode_label()}</label>
      <select
        id="wiz-fwd-mode"
        class="inp"
        value={mode}
        disabled={isBusy}
        onchange={(e) => {
          mode = (e.currentTarget as HTMLSelectElement).value as 'merge' | 'replace';
          confirmReplace = false;
          onrefresh(routerSource);
        }}
      >
        <option value="merge">{m.firc_migration_fwd_mode_merge()}</option>
        <option value="replace">{m.firc_migration_fwd_mode_replace()}</option>
      </select>
    </div>

    <div class="field">
      <label class="lbl" for="wiz-fwd-source">{m.firc_migration_fwd_source_label()}</label>
      <select
        id="wiz-fwd-source"
        class="inp"
        value={routerSource}
        disabled={isBusy}
        onchange={(e) => {
          const v = (e.currentTarget as HTMLSelectElement).value as RouterSource;
          routerSource = v;
          onrefresh(v);
        }}
      >
        <option value="auto">{m.firc_migration_fwd_source_auto()}</option>
        <option value="tproxy">{m.firc_migration_fwd_source_tproxy()}</option>
        <option value="fakeip">{m.firc_migration_fwd_source_fakeip()}</option>
        <option value="both">{m.firc_migration_fwd_source_both()}</option>
      </select>
    </div>
  </div>

  {#if mode === 'replace'}
    <label class="check-row warn-confirm-box">
      <input type="checkbox" disabled={isBusy} bind:checked={confirmReplace} />
      <span><RichText text={m.firc_migration_fwd_confirm_replace()} /></span>
    </label>
  {/if}

  <label class="check-row">
    <input type="checkbox" disabled={isBusy || postAction === 'switch_and_uninstall'} bind:checked={createSnapshot} />
    <span>
      <RichText
        text={postAction === 'switch_and_uninstall'
          ? m.firc_migration_fwd_snapshot_mandatory()
          : m.firc_migration_fwd_snapshot_optional()}
      />
    </span>
  </label>

  {#if preview?.fakeip?.detected}
    <div class="wiz-box fakeip-box">
      <div class="sec-head">
        <span class="lbl">{m.firc_migration_fakeip_detected()}</span>
        {#if preview.fakeip.sourceFile || preview.fakeip.slotFile}
          <Badge variant="info" size="sm" mono>
            {preview.fakeip.sourceFile || preview.fakeip.slotFile}
          </Badge>
        {/if}
      </div>
      <div class="hint">
        <RichText text={m.firc_migration_fakeip_pool({ range: fakeipRange })} />
        {#if preview.fakeip.realDns}
          · <RichText text={m.firc_migration_fakeip_real_dns({ dns: preview.fakeip.realDns })} />
        {/if}
        {#if preview.fakeip.routeFinal || preview.fakeip.finalOutbound}
          · <RichText
            text={m.firc_migration_fakeip_final({
              outbound: preview.fakeip.routeFinal || preview.fakeip.finalOutbound || '',
            })}
          />
        {/if}
      </div>
      <label class="check-row">
        <input type="checkbox" disabled={isBusy} bind:checked={includeFakeIP} />
        <span>
          <RichText
            text={preview.fakeip.realDns
              ? m.firc_migration_fakeip_include_dns({ range: fakeipRange, dns: preview.fakeip.realDns })
              : m.firc_migration_fakeip_include({ range: fakeipRange })}
          />
        </span>
      </label>
    </div>
  {/if}

  <FircMigrationPickList
    label={m.firc_migration_fwd_tunnels_label({ selected: selectedTunnelIds.length, total: tunnels.length })}
    filters={[
      { label: m.firc_migration_filter_all(), run: () => pickTunnels('all') },
      { label: m.firc_migration_filter_subs(), run: () => pickTunnels('subs') },
      { label: m.firc_migration_filter_nodes(), run: () => pickTunnels('nodes') },
      { label: m.firc_migration_filter_none(), run: () => pickTunnels('none') },
    ]}
    items={tunnelItems}
    selected={selectedTunnelIds}
    empty={m.firc_migration_fwd_tunnels_empty()}
    disabled={isBusy}
    ontoggle={(id) => {
      if (!isBusy) selectedTunnelIds = toggleId(selectedTunnelIds, id);
    }}
  />

  <FircMigrationPickList
    label={m.firc_migration_fwd_groups_label({ selected: selectedGroupIds.length, total: groups.length })}
    filters={[
      { label: m.firc_migration_filter_all(), run: () => pickGroups('all') },
      { label: m.firc_migration_filter_groups(), run: () => pickGroups('rules_only') },
      { label: m.firc_migration_filter_devices(), run: () => pickGroups('devices_only') },
      { label: m.firc_migration_filter_none(), run: () => pickGroups('none') },
    ]}
    items={groupItems}
    selected={selectedGroupIds}
    empty={m.firc_migration_fwd_groups_empty()}
    disabled={isBusy}
    ontoggle={(id) => {
      if (!isBusy) selectedGroupIds = toggleId(selectedGroupIds, id);
    }}
  />
</div>

<style>
  .fakeip-box {
    border-color: color-mix(in srgb, var(--color-info, #3b82f6) 40%, var(--border));
    background: color-mix(in srgb, var(--color-info, #3b82f6) 6%, var(--bg-secondary));
  }
</style>
