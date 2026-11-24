<script lang="ts">
	import { onMount } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { fircStatus, fircGroups, fircTunnels, fircTunnelsState, reloadFircAll } from '$lib/stores/firc';
	import { Button, IconButton, StatusDot, Badge, SideDrawer, ConfirmModal } from '$lib/components/ui';
	import StatStrip, { type StatCellData } from '$lib/components/sb-router/StatStrip.svelte';
	import FircGroupsPanel from '$lib/components/firc/FircGroupsPanel.svelte';
	import FircSettingsPanel from '$lib/components/firc/FircSettingsPanel.svelte';
	import FircMonitorPanel from '$lib/components/firc/FircMonitorPanel.svelte';
	import FircJournalPanel from '$lib/components/firc/FircJournalPanel.svelte';
	import { Settings, RotateCw, Power, AlertTriangle } from 'lucide-svelte';

	let settingsDrawerOpen = $state(false);
	let settingsHasUnsaved = $state(false);
	let confirmCloseSettingsOpen = $state(false);
	let activeView = $state<'rules' | 'monitor' | 'journal'>('rules');
	let busy = $state(false);

	function requestCloseSettingsDrawer() {
		if (settingsHasUnsaved) {
			confirmCloseSettingsOpen = true;
			return;
		}
		settingsDrawerOpen = false;
	}

	onMount(() => {
		void reloadFircAll().catch((e) => {
			notifications.error(m.firc_router_load_failed({ error: (e as Error).message }));
		});
	});

	async function handleSelectFircEngine() {
		if (busy) return;
		busy = true;
		try {
			await api.fircUpdateSettings({ routingEngine: 'firc' });
			await Promise.all([reloadFircAll(), singboxRouterStore.reloadSettings()]);
			notifications.success(m.firc_router_engine_switched());
		} catch (e) {
			notifications.error(m.firc_router_engine_switch_failed({ error: (e as Error).message }));
		} finally {
			busy = false;
		}
	}

	async function handleToggleFircDaemon() {
		if (busy) return;
		busy = true;
		try {
			if ($fircStatus?.running) {
				await api.fircStop();
				notifications.success(m.firc_router_daemon_stopped());
			} else {
				await api.fircStart();
				notifications.success(m.firc_router_daemon_started());
			}
			await reloadFircAll();
		} catch (e) {
			notifications.error((e as Error).message || m.firc_router_daemon_control_failed());
		} finally {
			busy = false;
		}
	}

	async function handleRestartFircDaemon() {
		if (busy) return;
		busy = true;
		try {
			if ($fircStatus?.recoveryRequired) {
				await api.fircReconcileRecovery('firc');
				await reloadFircAll();
				notifications.success(m.firc_router_recovered_synced());
			} else {
				await api.fircRestart();
				await reloadFircAll();
				notifications.success(m.firc_router_config_reloaded());
			}
		} catch (e) {
			notifications.error((e as Error).message || m.firc_router_restart_failed());
		} finally {
			busy = false;
		}
	}

	async function handleReconcileRecovery() {
		if (busy) return;
		busy = true;
		try {
			await api.fircReconcileRecovery('firc');
			await reloadFircAll();
			notifications.success(m.firc_router_recovered_chains());
		} catch (e) {
			notifications.error((e as Error).message || m.firc_router_recovery_failed());
		} finally {
			busy = false;
		}
	}

	let totalGroups = $derived($fircGroups.length);
	let enabledGroups = $derived($fircGroups.filter((g) => g.enable !== false).length);
	let totalRules = $derived(
		$fircGroups.reduce((acc, g) => acc + (g.rules?.length ?? 0) + (g.list?.rulesTotal ?? 0), 0),
	);
	let totalTunnels = $derived($fircTunnels.length);
	let runningTunnels = $derived(
		$fircTunnelsState.filter((s) => s.status === 'up').length ||
			$fircTunnels.filter((t) => t.enable !== false).length,
	);

	let restartLabel = $derived($fircStatus?.recoveryRequired ? m.firc_router_restart_recover() : m.firc_router_restart());
	let powerLabel = $derived($fircStatus?.running ? m.firc_router_stop() : m.firc_router_start());

	let statCells = $derived<StatCellData[]>([
		{
			label: m.firc_router_stat_engine(),
			value: $fircStatus?.recoveryRequired ? m.firc_router_stat_engine_recovery() : ($fircStatus?.running ? m.firc_router_stat_engine_active() : m.firc_daemon_stopped()),
			tone: $fircStatus?.recoveryRequired ? 'error' : ($fircStatus?.running ? 'success' : 'error'),
			helpTitle: m.firc_router_stat_engine_help_title(),
			helpText: $fircStatus?.recoveryRequired
				? m.firc_router_stat_engine_help_recovery()
				: m.firc_router_stat_engine_help(),
		},
		{
			label: m.firc_router_stat_groups(),
			value: `${enabledGroups}/${totalGroups}`,
			tone: enabledGroups > 0 ? 'default' : 'muted',
			helpTitle: m.firc_router_stat_groups_help_title(),
			helpText: m.firc_router_stat_groups_help(),
		},
		{
			label: m.firc_router_stat_rules(),
			value: String(totalRules),
			tone: totalRules > 0 ? 'default' : 'muted',
			helpTitle: m.firc_router_stat_rules_help_title(),
			helpText: m.firc_router_stat_rules_help(),
		},
		{
			label: m.firc_router_stat_tunnels(),
			value: `${runningTunnels}/${totalTunnels}`,
			tone: runningTunnels > 0 ? 'success' : 'muted',
			helpTitle: m.firc_router_stat_tunnels_help_title(),
			helpText: m.firc_router_stat_tunnels_help(),
		},
		{
			label: m.firc_router_stat_ports(),
			value: `:${$fircStatus?.dnsPort || 3553} · :${$fircStatus?.inboundPort || 1099}`,
			compact: true,
			tone: 'default',
			helpTitle: m.firc_router_stat_ports_help_title(),
			helpText: m.firc_router_stat_ports_help(),
		},
	]);
</script>

<div class="sb-shell">
	<header class="sb-header">
		<div class="title-block">
			<div class="title-row">
				<h1 class="title">{m.firc_router_title()}</h1>
				<span
					class="status-pill"
					class:is-running={$fircStatus?.running && !$fircStatus?.recoveryRequired}
					class:is-recovery={$fircStatus?.recoveryRequired}
				>
					<StatusDot variant={$fircStatus?.recoveryRequired ? 'error' : ($fircStatus?.running ? 'success' : 'muted')} size="sm" />
					<span>{$fircStatus?.recoveryRequired ? m.firc_router_pill_recovery() : ($fircStatus?.running ? m.firc_router_pill_running() : m.firc_router_pill_stopped())}</span>
				</span>
				{#if $fircStatus?.selected}
					<Badge variant="accent" size="xs">{m.firc_router_active_engine()}</Badge>
				{/if}
			</div>
			<div class="subtitle">
				{m.firc_router_subtitle()}
			</div>
		</div>

		<div class="header-tools">
			{#if !$fircStatus?.selected}
				<Button variant="primary" size="sm" onclick={handleSelectFircEngine} disabled={busy}>
					{m.firc_router_make_active()}
				</Button>
			{/if}

			<IconButton
				size="md"
				onclick={handleRestartFircDaemon}
				disabled={busy}
				title={restartLabel}
				ariaLabel={restartLabel}
			>
				<RotateCw size={15} />
			</IconButton>

			<IconButton
				size="md"
				onclick={handleToggleFircDaemon}
				disabled={busy}
				title={powerLabel}
				ariaLabel={powerLabel}
			>
				<Power size={15} />
			</IconButton>

			<Button variant="outline-primary" size="sm" onclick={() => (settingsDrawerOpen = true)}>
				{#snippet iconBefore()}<Settings size={16} />{/snippet}
				{m.firc_router_params()}
			</Button>
		</div>
	</header>

	<StatStrip cells={statCells} />

	{#if $fircStatus?.recoveryRequired}
		<div class="firc-recovery-banner" role="alert">
			<div class="recovery-content">
				<div class="recovery-title">
					<AlertTriangle size={16} />
					<span>{m.firc_router_recovery_title()}</span>
				</div>
				<div class="recovery-text">
					{$fircStatus.recoveryReason || m.firc_router_recovery_text()}
				</div>
			</div>
			<div class="recovery-actions">
				<Button variant="danger" size="sm" onclick={handleReconcileRecovery} disabled={busy}>
					{m.firc_runtime_repair()}
				</Button>
			</div>
		</div>
	{:else if $fircStatus?.lastError}
		<div class="firc-error-banner">
			{$fircStatus.lastError}
		</div>
	{/if}

	<nav class="firc-views" aria-label={m.firc_router_views()}>
		<button type="button" class:active={activeView === 'rules'} aria-pressed={activeView === 'rules'} onclick={() => (activeView = 'rules')}>{m.firc_global_mode_rules()}</button>
		<button type="button" class:active={activeView === 'monitor'} aria-pressed={activeView === 'monitor'} onclick={() => (activeView = 'monitor')}>{m.firc_router_view_monitor()}</button>
		<button type="button" class:active={activeView === 'journal'} aria-pressed={activeView === 'journal'} onclick={() => (activeView = 'journal')}>{m.firc_router_view_journal()}</button>
	</nav>
	{#if activeView === 'monitor'}
		<FircMonitorPanel />
	{:else if activeView === 'journal'}
		<FircJournalPanel />
	{/if}

	<!-- Keep the editor mounted: changing the view must not discard unsaved rules. -->
	<div class="sb-body" hidden={activeView !== 'rules'}>
		<FircGroupsPanel />
	</div>
</div>

<SideDrawer
	open={settingsDrawerOpen}
	title={m.firc_router_params()}
	width={460}
	onClose={requestCloseSettingsDrawer}
>
	<FircSettingsPanel bind:hasUnsaved={settingsHasUnsaved} />
</SideDrawer>

<ConfirmModal
	open={confirmCloseSettingsOpen}
	title={m.firc_router_unsaved_title()}
	message={m.firc_router_unsaved_message()}
	confirmLabel={m.firc_router_unsaved_discard()}
	cancelLabel={m.firc_router_unsaved_stay()}
	variant="danger"
	onConfirm={() => {
		confirmCloseSettingsOpen = false;
		settingsHasUnsaved = false;
		settingsDrawerOpen = false;
	}}
	onClose={() => {
		confirmCloseSettingsOpen = false;
	}}
/>

<style>
	.firc-views { display: flex; gap: .5rem; margin: .75rem 0; }
	.firc-views button { padding: .45rem 1rem; background: var(--bg-secondary); color: var(--text-secondary); border: 1px solid var(--border); border-radius: 6px; font: inherit; cursor: pointer; }
	.firc-views button.active { border-color: var(--accent); color: var(--text-primary); background: var(--color-accent-tint, var(--bg-tertiary)); }
	.firc-views button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
	.sb-shell {
		display: flex;
		flex-direction: column;
		margin-top: 0.25rem;
		min-width: 0;
		max-width: 100%;
	}

	.sb-header {
		display: flex;
		align-items: center;
		gap: var(--sp-4, 1rem);
		padding-bottom: var(--sp-3, 0.75rem);
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}

	.title-block {
		flex: 1 1 auto;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		flex-wrap: wrap;
	}

	.title {
		margin: 0;
		font-size: var(--fs-h3, 1.125rem);
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.25;
	}

	.status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		font-family: var(--font-mono);
		font-size: 0.72rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-muted);
	}

	.status-pill.is-running {
		color: var(--color-success, #9ece6a);
		border-color: color-mix(in srgb, var(--color-success, #9ece6a) 35%, var(--border));
		background: color-mix(in srgb, var(--color-success, #9ece6a) 10%, var(--bg-secondary));
	}

	.status-pill.is-recovery {
		color: var(--danger, #f7768e);
		border-color: color-mix(in srgb, var(--danger, #f7768e) 40%, var(--border));
		background: color-mix(in srgb, var(--danger, #f7768e) 12%, var(--bg-secondary));
	}

	.subtitle {
		margin-top: 3px;
		font-size: var(--fs-sm, 0.8125rem);
		color: var(--text-muted);
	}

	.header-tools {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		min-width: 0;
		max-width: 100%;
	}

	.firc-error-banner {
		padding: 0.65rem 0.9rem;
		margin-bottom: 0.75rem;
		border-radius: 8px;
		background: rgba(247, 118, 142, 0.12);
		border: 1px solid rgba(247, 118, 142, 0.35);
		color: var(--danger, #f7768e);
		font-size: 0.8125rem;
	}

	.firc-recovery-banner {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding: 0.75rem 1rem;
		margin-bottom: 0.75rem;
		border-radius: 8px;
		background: rgba(247, 118, 142, 0.12);
		border: 1px solid rgba(247, 118, 142, 0.4);
		color: var(--text-primary);
		font-size: 0.8125rem;
	}

	.firc-recovery-banner .recovery-content {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}

	.firc-recovery-banner .recovery-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		color: var(--danger, #f7768e);
	}

	.firc-recovery-banner .recovery-text {
		color: var(--text-secondary);
		font-size: 0.75rem;
		line-height: 1.35;
		word-break: break-word;
	}

	.firc-recovery-banner .recovery-actions {
		flex-shrink: 0;
	}

	.sb-body {
		width: 100%;
	}
</style>
