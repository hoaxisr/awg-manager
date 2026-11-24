<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { m, uiText, type UiText } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { onResourceInvalidated } from '$lib/stores/storeRegistry';
	import { notifications } from '$lib/stores/notifications';
	import {
		lastServerVerification,
		recordServerVerification,
		pickFresherVerificationReport,
		clearServerVerification,
	} from '$lib/stores/serverVerification';
	import { Button, IconButton } from '$lib/components/ui';
	import ExternalAccessWizard from './external-access/ExternalAccessWizard.svelte';
	import { EXTERNAL_ACCESS_LABELS } from './external-access/connectionLabels';
	import { computeConfigFingerprint } from './external-access/externalAccessModel';
	import TgProxySummaryStrip from './telegram-proxy/TgProxySummaryStrip.svelte';
	import TgProxyMethods from './telegram-proxy/TgProxyMethods.svelte';
	import TgProxyUsersSection from './telegram-proxy/TgProxyUsersSection.svelte';
	import TgProxyAddUserModal from './telegram-proxy/TgProxyAddUserModal.svelte';
	import TgProxyDeleteUserModal from './telegram-proxy/TgProxyDeleteUserModal.svelte';
	import TgProxyEgressModal from './telegram-proxy/TgProxyEgressModal.svelte';
	import TgProxyRevealModal from './telegram-proxy/TgProxyRevealModal.svelte';
	import TgProxySettingsModal from './telegram-proxy/TgProxySettingsModal.svelte';
	import TgProxyResetModal from './telegram-proxy/TgProxyResetModal.svelte';
	import {
		egressChoices,
		egressEntriesFrom,
		egressSummary,
		egressUnavailable,
		overallState,
		partialReason,
		shortEgressSummary,
		type EgressChoice,
		type EgressEntry,
	} from './telegram-proxy/tgProxyModel';
	import type { TgWebProxyConfig, TgWebProxyStatus, TelegramUser } from '$lib/types';
	import type { VerificationReport } from '$lib/types/serverWizard';
	import { Power, RotateCw, Settings, Sparkles } from 'lucide-svelte';

	interface Props {
		initialStatus?: TgWebProxyStatus | null;
		onOpenDispatcher?: () => void;
		onRefreshParent?: () => void;
	}

	let { initialStatus = null, onOpenDispatcher, onRefreshParent }: Props = $props();

	let status = $state<TgWebProxyStatus | null>(untrack(() => initialStatus));
	let config = $state<TgWebProxyConfig | null>(null);
	let loading = $state(true);
	let actionLoading = $state(false);

	// Quick check state
	let checkLoading = $state(false);
	let lastCheckReport = $state<VerificationReport | null>(null);
	let lastCheckError = $state<UiText>('');

	let isConfigured = $derived(
		Boolean(
			config?.enabled ||
				status?.running ||
				(config?.direct_host && config.direct_host.trim() !== '') ||
				(config?.public_hostname && config.public_hostname.trim() !== '') ||
				(config?.endpoints && config.endpoints.some((e) => e.hostname && e.hostname.trim() !== ''))
		)
	);

	let expectsMt = $derived.by(() => {
		const scenario = config?.scenario || 'dual';
		return scenario === 'dual' || scenario === 'direct_fake_tls';
	});

	let expectsWeb = $derived.by(() => {
		const scenario = config?.scenario || 'dual';
		return scenario === 'dual' || scenario === 'cdn_http';
	});

	let overallStatusState = $derived(overallState(config, status, expectsMt, expectsWeb));
	let partialStatusReason = $derived(partialReason(overallStatusState, config, status, expectsMt, expectsWeb));

	let cardFingerprint = $derived(
		computeConfigFingerprint({
			xrayConfig: null,
			xrayStatus: null,
			xrayTuning: null,
			tgConfig: config,
			tgStatus: status,
			dispatcherConfig: null,
		})
	);

	// Multi-user management state
	let showAddUserModal = $state(false);
	let userActionLoadingId = $state<string>('');
	let deleteUserTarget = $state<TelegramUser | null>(null);
	let deleteUserLoading = $state(false);

	let tgUsers = $derived<TelegramUser[]>(status?.users || config?.users || []);

	let resetConfirmOpen = $state(false);
	let resetDeleteKeenDNS = $state(false);
	let resetLoading = $state(false);
	let wizardOpen = $state(false);

	let revealModal = $state<TgProxyRevealModal>();
	let settingsModal = $state<TgProxySettingsModal>();
	let egressModal = $state<TgProxyEgressModal>();

	// Выходы храним без готовых подписей: подпись собирается при показе и
	// поэтому следует за языком.
	let egressEntries = $state<EgressEntry[]>([{ value: 'direct', name: '', available: true }]);
	let egressOptions = $derived<EgressChoice[]>(egressChoices(egressEntries));
	let egressSummaryLabel = $derived(egressSummary(config, egressOptions));
	let shortEgressLabel = $derived(shortEgressSummary(config, egressOptions));
	let currentEgressUnavailable = $derived(egressUnavailable(config, egressOptions));

	function invalidateRevealCache() {
		revealModal?.invalidate();
	}

	async function loadEgressOptions(currentUpstream?: string) {
		try {
			const caps = await api.getWizardCapabilities('tgwebproxy');
			egressEntries = egressEntriesFrom(caps.egress_options || [], currentUpstream);
		} catch {
			const cur = (currentUpstream || '').trim();
			const fallback: EgressEntry[] = [{ value: 'direct', name: '', available: true }];
			if (cur && cur !== 'direct') {
				fallback.push({ value: cur, name: cur, available: true, current: true });
			}
			egressEntries = fallback;
		}
	}

	$effect(() => {
		const shared = $lastServerVerification.tgwebproxy;
		if (!shared) return;
		const cur = untrack(() => lastCheckReport);
		const next = pickFresherVerificationReport(shared, cur);
		if (next && next !== cur) {
			lastCheckReport = next;
			lastCheckError = '';
		}
	});

	onMount(() => {
		void loadData();
		// Статус и конфиг — по подсказке servers.ingress (правки ingress и
		// смена рантайма, которую замечает сторож на бэкенде), не по таймеру.
		return onResourceInvalidated('servers.ingress', () => void loadStatus());
	});

	async function loadData() {
		loading = true;
		try {
			const [s, c] = await Promise.all([
				api.getTgWebProxyStatus(),
				api.getTgWebProxyConfig(),
			]);
			status = s;
			config = c;
			await loadEgressOptions(c?.upstream_device);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_load_error());
		} finally {
			loading = false;
		}
	}

	async function loadStatus() {
		try {
			const [s, c] = await Promise.all([
				api.getTgWebProxyStatus(),
				api.getTgWebProxyConfig().catch(() => config),
			]);
			status = s;
			if (c) config = c;
		} catch {
			// ignore polling errors
		}
	}

	/** После любой успешной правки: забыть раскрытые секреты и перечитать всё. */
	async function afterMutation() {
		invalidateRevealCache();
		await loadData();
		onRefreshParent?.();
	}

	async function runQuickCheck() {
		checkLoading = true;
		lastCheckError = '';
		try {
			const res = await api.verifyWizard('tgwebproxy');
			lastCheckReport = res;
			recordServerVerification('tgwebproxy', res);
		} catch (e) {
			lastCheckError = e instanceof Error ? e.message : () => m.servers_tg_check_run_error();
		} finally {
			checkLoading = false;
		}
	}

	function openEgressModal() {
		egressModal?.show();
		void loadEgressOptions(config?.upstream_device);
	}

	async function handleToggleUser(user: TelegramUser) {
		userActionLoadingId = user.id;
		try {
			const nextEnabled = !user.enabled;
			status = await api.tgWebProxyAction('update_user', {
				user_id: user.id,
				enabled: nextEnabled,
			});
			invalidateRevealCache();
			notifications.success(
				nextEnabled
					? m.servers_tg_user_enabled({ name: user.remark })
					: m.servers_tg_user_disabled({ name: user.remark })
			);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_user_toggle_error());
		} finally {
			userActionLoadingId = '';
		}
	}

	async function confirmDeleteUser() {
		const target = deleteUserTarget;
		if (!target) return;
		deleteUserLoading = true;
		userActionLoadingId = target.id;
		try {
			status = await api.tgWebProxyAction('delete_user', { user_id: target.id });
			invalidateRevealCache();
			deleteUserTarget = null;
			notifications.success(m.servers_tg_user_deleted({ name: target.remark }));
			await loadData();
			onRefreshParent?.();
			await revealModal?.afterUserDeleted(target.id);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_user_delete_error());
		} finally {
			deleteUserLoading = false;
			userActionLoadingId = '';
		}
	}

	async function togglePower() {
		actionLoading = true;
		try {
			const nextAction = status?.running ? 'stop' : 'start';
			status = await api.tgWebProxyAction(nextAction);
			invalidateRevealCache();
			notifications.success(status?.running ? m.servers_tg_started() : m.servers_tg_stopped());
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_power_error());
		} finally {
			actionLoading = false;
		}
	}

	async function restartServer() {
		actionLoading = true;
		try {
			status = await api.tgWebProxyAction('restart');
			invalidateRevealCache();
			notifications.success(m.servers_tg_restarted());
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_restart_error());
		} finally {
			actionLoading = false;
		}
	}

	function openResetConfirm() {
		resetDeleteKeenDNS = false;
		resetConfirmOpen = true;
	}

	async function executeFactoryReset() {
		resetLoading = true;
		try {
			const res = await api.resetTgWebProxy({ delete_keendns: resetDeleteKeenDNS });
			lastCheckReport = null;
			lastCheckError = '';
			clearServerVerification('tgwebproxy');
			revealModal?.reset();
			resetConfirmOpen = false;
			settingsModal?.close();
			await loadData();
			onRefreshParent?.();
			notifications.success(m.server_reset_tg_success());
			if (res.keendns_deleted?.length) {
				notifications.info(
					m.server_reset_keendns_deleted({ domains: res.keendns_deleted.join(', ') }),
				);
			}
			if (res.keendns_skipped?.length) {
				notifications.warning(
					m.server_reset_keendns_skipped({ domains: res.keendns_skipped.join(', ') }),
				);
			}
			if (res.warnings?.length) {
				notifications.warning(res.warnings.join('; '));
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.common_error());
		} finally {
			resetLoading = false;
		}
	}

	async function refreshAfterWizard() {
		await loadData();
		onRefreshParent?.();
	}
</script>

<div class="tg-card">
	<!-- 1. Card Header -->
	<header class="card-header">
		<div class="header-left">
			<div class="title-row">
				<span
					class="led"
					class:led-running={overallStatusState === 'running'}
					class:led-warning={overallStatusState === 'partial'}
					class:led-stopped={overallStatusState === 'stopped'}
				></span>
				<h2 class="title">{EXTERNAL_ACCESS_LABELS.telegramTitle}</h2>
				{#if loading && !status}
					<span class="badge badge-neutral">{m.servers_tg_loading()}</span>
				{:else if !isConfigured && !loading}
					<span class="badge badge-neutral">{m.servers_tg_not_configured()}</span>
				{:else if overallStatusState === 'running'}
					<span class="badge badge-success">{EXTERNAL_ACCESS_LABELS.statusRunning}</span>
				{:else if overallStatusState === 'partial'}
					<span class="badge badge-warning">{EXTERNAL_ACCESS_LABELS.statusPartial}</span>
					<span class="partial-reason">({partialStatusReason})</span>
				{:else}
					<span class="badge badge-neutral">{EXTERNAL_ACCESS_LABELS.statusStopped}</span>
				{/if}
			</div>
			<div class="subtitle">
				{EXTERNAL_ACCESS_LABELS.telegramSubtitle}
			</div>
		</div>

		{#if isConfigured}
			<div class="header-actions">
				<Button variant="secondary" size="sm" onclick={() => settingsModal?.show('general')}>
					<Settings size={14} class="mr-1" />
					{m.servers_tg_settings_btn()}
				</Button>
				<Button variant="secondary" size="sm" onclick={togglePower} disabled={actionLoading}>
					<Power size={14} class="mr-1" />
					{status?.running ? m.common_stop() : m.common_start()}
				</Button>
				<IconButton
					size="md"
					title={m.servers_tg_restart_title()}
					ariaLabel={m.servers_tg_restart_title()}
					onclick={restartServer}
					disabled={actionLoading}
				>
					<RotateCw size={15} class={actionLoading ? 'spin' : ''} />
				</IconButton>
			</div>
		{/if}
	</header>

	{#if !isConfigured && !loading}
		<div class="unconfigured-card">
			<p class="unconfigured-desc">
				{m.servers_tg_unconfigured_desc()}
			</p>
			<div class="unconfigured-actions">
				<Button variant="primary" onclick={() => (wizardOpen = true)}>
					<Sparkles size={15} class="mr-1" />
					{m.servers_tg_setup_connection()}
				</Button>
				<Button variant="secondary" onclick={() => settingsModal?.show('general')}>
					<Settings size={15} class="mr-1" />
					{m.servers_tg_manual_settings()}
				</Button>
			</div>
		</div>
	{:else}
		<!-- 2. Short Summary Strip -->
		<TgProxySummaryStrip
			{config}
			{status}
			shortEgressSummary={shortEgressLabel}
			{currentEgressUnavailable}
			{checkLoading}
			lastCheckError={uiText(lastCheckError)}
			{lastCheckReport}
			onchangeegress={openEgressModal}
			onopenwizard={() => (wizardOpen = true)}
			oncheck={runQuickCheck}
			ondetails={() => settingsModal?.show('diagnostics')}
		/>

		<!-- 3. Available Connection Methods Block -->
		<TgProxyMethods {config} {status} {expectsMt} {expectsWeb} />

		<!-- 4. Users / Devices Section -->
		<TgProxyUsersSection
			users={tgUsers}
			busyId={userActionLoadingId}
			onadd={() => (showAddUserModal = true)}
			ontoggle={handleToggleUser}
			onreveal={(user) => revealModal?.show(undefined, undefined, user.id)}
			ondelete={(user) => (deleteUserTarget = user)}
		/>
	{/if}
</div>

<TgProxyAddUserModal
	bind:open={showAddUserModal}
	usersCount={tgUsers.length}
	oncreated={async (next) => {
		status = next;
		await afterMutation();
	}}
/>

<TgProxyDeleteUserModal
	target={deleteUserTarget}
	loading={deleteUserLoading}
	isLast={tgUsers.length <= 1}
	onconfirm={confirmDeleteUser}
	oncancel={() => (deleteUserTarget = null)}
/>

<TgProxyEgressModal
	bind:this={egressModal}
	{config}
	{egressOptions}
	{currentEgressUnavailable}
	onsaved={async (next) => {
		config = next;
		await afterMutation();
	}}
/>

<TgProxyRevealModal
	bind:this={revealModal}
	users={tgUsers}
	{config}
	{expectsMt}
	{expectsWeb}
	deleteBusy={deleteUserLoading}
	ondeleteuser={(user) => (deleteUserTarget = user)}
	onsecretchanged={async (next) => {
		// Кэш раскрытий модалка уже обновила свежим ответом — не сбрасываем.
		status = next;
		await loadData();
		onRefreshParent?.();
	}}
/>

<TgProxySettingsModal
	bind:this={settingsModal}
	{config}
	{status}
	{egressOptions}
	{egressSummaryLabel}
	{cardFingerprint}
	{onOpenDispatcher}
	onopenwizard={() => (wizardOpen = true)}
	onrequestreset={openResetConfirm}
	onloadegress={(dev) => void loadEgressOptions(dev)}
	onsaved={async (next) => {
		config = next;
		await afterMutation();
	}}
	onstatus={async (next, reload) => {
		status = next;
		invalidateRevealCache();
		if (reload) await loadData();
	}}
/>

<TgProxyResetModal
	bind:open={resetConfirmOpen}
	bind:deleteKeenDNS={resetDeleteKeenDNS}
	loading={resetLoading}
	onconfirm={executeFactoryReset}
/>

<ExternalAccessWizard
	bind:open={wizardOpen}
	initialKind="tgwebproxy"
	onclose={async () => {
		wizardOpen = false;
		await refreshAfterWizard();
	}}
	onapplied={refreshAfterWizard}
	onsettled={refreshAfterWizard}
/>

<style>
	.tg-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 1.125rem 1.25rem;
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
	}

	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 0.875rem;
		flex-wrap: wrap;
	}

	.header-left {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.title {
		font-size: 16px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.subtitle {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.partial-reason {
		font-size: 11.5px;
		color: var(--color-warning);
	}

	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
	}
	.badge-success {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.badge-neutral {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.badge-warning {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.led-running {
		background: var(--color-success);
		box-shadow: 0 0 4px var(--color-success-border);
	}
	.led-warning {
		background: var(--color-warning);
		box-shadow: 0 0 4px var(--color-warning-border);
	}
	.led-stopped {
		background: var(--color-text-muted);
		opacity: 0.5;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	/* Empty Unconfigured State */
	.unconfigured-card {
		padding: 1.5rem;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 12px;
	}

	.unconfigured-desc {
		margin: 0;
		font-size: 13px;
		color: var(--color-text-secondary);
		line-height: 1.45;
	}

	.unconfigured-actions {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
</style>
