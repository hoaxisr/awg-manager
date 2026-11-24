<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { onResourceInvalidated } from '$lib/stores/storeRegistry';
	import { m, formatLocale, uiText, type UiText } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		lastServerVerification,
		recordServerVerification,
		pickFresherVerificationReport,
		clearServerVerification,
	} from '$lib/stores/serverVerification';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Button } from '$lib/components/ui';
	import type { XrayConfig, XrayStatus, XrayClient, XHTTPTuning } from '$lib/types';
	import type { XrayRealityState } from '$lib/types/xray';
	import type { EgressOption, KeenDNSDiscovery, VerificationReport } from '$lib/types/serverWizard';
	import XrayShareModal from './XrayShareModal.svelte';
	import XrayAutoTunnelModal from './XrayAutoTunnelModal.svelte';
	import XrayEndpointsSection from './XrayEndpointsSection.svelte';
	import XrayClientsSection from './XrayClientsSection.svelte';
	import XrayEgressModal from './XrayEgressModal.svelte';
	import XraySettingsModal from './XraySettingsModal.svelte';
	import XrayEndpointModal from './XrayEndpointModal.svelte';
	import XrayCardHeader from './XrayCardHeader.svelte';
	import XraySummaryStrip from './XraySummaryStrip.svelte';
	import XrayAddClientModal from './XrayAddClientModal.svelte';
	import XrayResetModal from './XrayResetModal.svelte';
	import ExternalAccessWizard from './external-access/ExternalAccessWizard.svelte';
	import {
		formatXrayEgressLabel,
		formatShortEgressSummary,
		isLegacySocksEgress,
	} from './external-access/connectionLabels';
	import { computeConfigFingerprint } from './external-access/externalAccessModel';
	import { Settings, Sparkles } from 'lucide-svelte';

	interface Props {
		initialStatus?: XrayStatus | null;
		onOpenDispatcher?: () => void;
		onRefreshParent?: () => void;
	}

	let { initialStatus = null, onOpenDispatcher, onRefreshParent }: Props = $props();

	let status = $state<XrayStatus | null>(untrack(() => initialStatus));
	let config = $state<XrayConfig | null>(null);
	let clients = $derived(status?.clients ?? config?.clients ?? []);
	let endpoints = $derived(config?.endpoints ?? []);
	let keendnsDiscovery = $state<KeenDNSDiscovery | null>(null);
	let loading = $state(true);
	let actionLoading = $state(false);
	let wizardOpen = $state(false);

	// Client modals
	let shareOpen = $state(false);
	let autoTunnelOpen = $state(false);
	let activeClientId = $state('');
	let activeClientRemark = $state('');
	let activeShareEndpointId = $state('');

	// Endpoint (VLESS route) configuration modal
	let endpointModalOpen = $state(false);
	let endpointModal = $state<ReturnType<typeof XrayEndpointModal>>();

	// Direct VLESS + REALITY route (white-IP preset integrated into routes list)
	let realityState = $state<XrayRealityState | null>(null);

	let hasRealityRoute = $derived(
		Boolean(realityState?.settings?.public_key || realityState?.settings?.enabled)
	);

	// Add client modal
	let addClientOpen = $state(false);

	// Settings / Diagnostics modal
	let settingsOpen = $state(false);
	let settingsTab = $state<'general' | 'diagnostics'>('general');
	let resetConfirmOpen = $state(false);

	// Egress modal & routing state
	let egressModalOpen = $state(false);
	let egressOptions = $state<EgressOption[]>([]);

	// Quick connection check summary state
	let checkLoading = $state(false);
	let lastCheckReport = $state<VerificationReport | null>(null);
	let lastCheckError = $state<UiText>('');

	// XHTTP Tuning
	let tuning = $state<XHTTPTuning | null>(null);

	let tunnelInterfaces = $derived(
		egressOptions.filter((o) => o.kind === 'interface' && o.iface && o.iface !== 'direct')
	);

	let hasLegacySocksEgress = $derived(
		isLegacySocksEgress(config?.outbound_mode, config?.outbound_socks_port)
	);

	let currentEgressOption = $derived.by(() => {
		const iface = (config?.outbound_interface || '').trim();
		if (!iface || config?.outbound_mode !== 'interface') return undefined;
		return tunnelInterfaces.find((t) => t.iface === iface || t.id === iface);
	});

	let currentEgressUnavailable = $derived(
		Boolean(
			config?.outbound_mode === 'interface' &&
				config?.outbound_interface &&
				currentEgressOption &&
				!currentEgressOption.available
		)
	);

	let isConfigured = $derived(
		Boolean(
			config?.enabled ||
				status?.running ||
				(config?.public_domain && config.public_domain.trim() !== '') ||
				clients.length > 0 ||
				hasRealityRoute
		)
	);

	let needsCheck = $derived(
		Boolean(
			(status !== null && !status.installed) ||
				status?.unavailable_reason ||
				status?.recovery_required ||
				hasLegacySocksEgress ||
				currentEgressUnavailable ||
				(lastCheckReport && lastCheckReport.overall_status === 'blocked') ||
				(config !== null && status?.running && !(config.public_domain || '').trim() && !realityState?.settings?.enabled)
		)
	);

	let cardFingerprint = $derived(
		computeConfigFingerprint({
			xrayConfig: config,
			xrayStatus: status,
			xrayTuning: tuning,
			tgConfig: null,
			tgStatus: null,
			dispatcherConfig: null,
		})
	);

	let egressSummaryLabel = $derived.by(() => {
		const iface = config?.outbound_interface || '';
		const found = iface ? tunnelInterfaces.find((t) => t.iface === iface || t.id === iface) : undefined;
		return formatXrayEgressLabel({
			outboundMode: config?.outbound_mode,
			outboundInterface: iface,
			outboundSocksPort: config?.outbound_socks_port,
			tunnelName: found?.name,
		});
	});

	let shortEgressSummary = $derived(formatShortEgressSummary(egressSummaryLabel));

	function formatCheckTime(iso?: string): string {
		if (!iso) return '';
		try {
			return new Date(iso).toLocaleTimeString(formatLocale(), {
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return '';
		}
	}

	let checkSummaryText = $derived.by(() => {
		if (checkLoading) return m.servers_xray_check_running();
		const err = uiText(lastCheckError);
		if (err) return m.servers_xray_check_error({ error: err });
		if (!lastCheckReport) return m.servers_xray_check_never();
		const time = formatCheckTime(lastCheckReport.checked_at);
		if (lastCheckReport.overall_status === 'ok') {
			return m.servers_xray_check_ok({ time });
		}
		if (lastCheckReport.overall_status === 'warning') {
			return m.servers_xray_check_warning({ time });
		}
		return m.servers_xray_check_blocked({ time });
	});

	$effect(() => {
		const shared = $lastServerVerification.xray;
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
		const unsubIngress = onResourceInvalidated('servers.ingress', () => void loadStatus());
		return () => {
			unsubIngress();
		};
	});

	async function loadData() {
		loading = true;
		try {
			const [s, c, t, caps, rState] = await Promise.all([
				api.getXrayServerStatus(),
				api.getXrayServerConfig(),
				api.getXrayTuning().catch(() => null),
				api.getWizardCapabilities('xray').catch(() => null),
				api.getXrayReality(false).catch(() => null),
			]);
			status = s;
			config = c;
			if (t) tuning = t;
			if (rState) realityState = rState;
			egressOptions = caps?.egress_options ? [...caps.egress_options] : [];
			if (caps?.keendns) keendnsDiscovery = caps.keendns;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_load_failed());
		} finally {
			loading = false;
		}
	}

	/** Сохранение в дочернем окне: свежий конфиг сразу, затем полная перечитка. */
	async function afterSave(c?: XrayConfig) {
		if (c) config = c;
		await loadData();
		onRefreshParent?.();
	}

	async function runQuickCheck() {
		checkLoading = true;
		lastCheckError = '';
		try {
			const res = await api.verifyWizard('xray');
			lastCheckReport = res;
			recordServerVerification('xray', res);
		} catch (e) {
			lastCheckError = e instanceof Error ? e.message : () => m.servers_xray_check_failed();
		} finally {
			checkLoading = false;
		}
	}

	async function loadStatus() {
		try {
			status = await api.getXrayServerStatus();
			// Открытые окна правят свою копию конфига — не подменяем её под ними.
			if (!settingsOpen && !egressModalOpen && !endpointModalOpen) {
				const c = await api.getXrayServerConfig().catch(() => null);
				if (c) {
					config = c;
				}
			}
		} catch {
			// ignore polling error
		}
	}

	async function togglePower() {
		actionLoading = true;
		try {
			const nextAction = status?.running ? 'stop' : 'start';
			status = await api.xrayServerAction(nextAction);
			realityState = await api.getXrayReality(false).catch(() => realityState);
			notifications.success(status?.running ? m.servers_xray_started() : m.servers_xray_stopped());
			if (config && status) config.enabled = status.running;
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_control_failed());
		} finally {
			actionLoading = false;
		}
	}

	async function restartServer() {
		actionLoading = true;
		try {
			status = await api.xrayServerAction('restart');
			realityState = await api.getXrayReality(false).catch(() => realityState);
			notifications.success(m.servers_xray_restarted());
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_restart_failed());
		} finally {
			actionLoading = false;
		}
	}

	async function handleDeleteClient(client: XrayClient) {
		if (!confirm(m.servers_xray_device_delete_confirm({ name: client.remark }))) return;
		try {
			await api.deleteXrayClient(client.id);
			notifications.success(m.servers_xray_device_deleted({ name: client.remark }));
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_device_delete_failed());
		}
	}

	async function handleToggleClient(client: XrayClient) {
		try {
			await api.toggleXrayClient(client.id, !client.enabled);
			notifications.success(
				client.enabled
					? m.servers_xray_device_access_off({ name: client.remark })
					: m.servers_xray_device_access_on({ name: client.remark })
			);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_device_toggle_failed());
		}
	}

	async function handleRotateClientFP(client: XrayClient) {
		if (!confirm(m.servers_xray_fp_rotate_confirm({ name: client.remark }))) {
			return;
		}
		try {
			await api.rotateXrayClientFP(client.id);
			notifications.success(m.servers_xray_fp_rotated({ name: client.remark }));
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_fp_rotate_failed());
		}
	}

	async function copyClientUuid(client: XrayClient) {
		if (await copyToClipboard(client.id)) {
			notifications.success(m.servers_xray_uuid_copied());
		}
	}

	function openShare(client: XrayClient, endpointId = '') {
		activeClientId = client.id;
		activeClientRemark = client.remark;
		activeShareEndpointId = endpointId;
		shareOpen = true;
	}

	function openAutoTunnel(client: XrayClient) {
		activeClientId = client.id;
		activeClientRemark = client.remark;
		autoTunnelOpen = true;
	}

	function openSettingsModal(tab: 'general' | 'diagnostics' = 'general') {
		settingsTab = tab;
		settingsOpen = true;
	}

	/** Сброс сервера прошёл: прежние проверки и Reality больше не актуальны. */
	async function afterReset() {
		lastCheckReport = null;
		lastCheckError = '';
		clearServerVerification('xray');
		realityState = null;
		settingsOpen = false;
		await loadData();
		onRefreshParent?.();
	}
</script>

<div class="xray-card">
	<!-- 1. Service Header -->
	<XrayCardHeader
		{status}
		{loading}
		{isConfigured}
		{needsCheck}
		{actionLoading}
		onsettings={() => openSettingsModal('general')}
		ontogglepower={togglePower}
		onrestart={restartServer}
	/>

	{#if !isConfigured && !loading}
		<!-- Empty unconfigured state -->
		<div class="unconfigured-card">
			<p class="unconfigured-desc">
				{m.servers_xray_unconfigured_desc()}
			</p>
			<div class="unconfigured-actions">
				<Button variant="primary" onclick={() => (wizardOpen = true)}>
					<Sparkles size={15} class="mr-1" />
					{m.servers_xray_setup_connection()}
				</Button>
				<Button variant="secondary" onclick={() => openSettingsModal('general')}>
					<Settings size={15} class="mr-1" />
					{m.servers_xray_manual_settings()}
				</Button>
			</div>
		</div>
	{:else}
		<XraySummaryStrip
			{shortEgressSummary}
			allowLan={Boolean(config?.allow_lan)}
			{hasLegacySocksEgress}
			{currentEgressUnavailable}
			{checkSummaryText}
			{checkLoading}
			checkStatus={lastCheckReport?.overall_status}
			onegress={() => (egressModalOpen = true)}
			onwizard={() => (wizardOpen = true)}
			oncheck={runQuickCheck}
			ondetails={() => openSettingsModal('diagnostics')}
		/>


		<XrayEndpointsSection
			{config}
			{endpoints}
			{tuning}
			bind:realityState
			{hasRealityRoute}
			onadd={() => endpointModal?.openAdd()}
			onedit={(ep) => endpointModal?.openEdit(ep)}
			oneditreality={() => endpointModal?.openReality()}
			onchanged={afterSave}
			onrealitychanged={() => onRefreshParent?.()}
		/>

		<XrayClientsSection
			{clients}
			onadd={() => (addClientOpen = true)}
			onshare={(c) => openShare(c)}
			ontoggle={(c) => void handleToggleClient(c)}
			onautotunnel={openAutoTunnel}
			oncopyuuid={(c) => void copyClientUuid(c)}
			onrotate={(c) => void handleRotateClientFP(c)}
			ondelete={(c) => void handleDeleteClient(c)}
		/>
	{/if}
</div>

<XrayEgressModal
	bind:open={egressModalOpen}
	{config}
	{tunnelInterfaces}
	{hasLegacySocksEgress}
	{currentEgressUnavailable}
	onsaved={afterSave}
/>


<XrayAddClientModal bind:open={addClientOpen} onadded={() => afterSave()} />

<XraySettingsModal
	bind:open={settingsOpen}
	bind:settingsTab
	{config}
	{status}
	{endpoints}
	{shortEgressSummary}
	{cardFingerprint}
	{onOpenDispatcher}
	onopenwizard={() => (wizardOpen = true)}
	onreset={() => (resetConfirmOpen = true)}
	onsaved={afterSave}
/>

<XrayEndpointModal
	bind:this={endpointModal}
	bind:open={endpointModalOpen}
	bind:realityState
	{config}
	{endpoints}
	{tuning}
	{keendnsDiscovery}
	onsaved={afterSave}
	onkeyschanged={() => onRefreshParent?.()}
/>

<XrayResetModal bind:open={resetConfirmOpen} ondone={afterReset} />

<!-- Modals for Client Actions -->
<XrayShareModal
	bind:open={shareOpen}
	bind:clientId={activeClientId}
	bind:clientRemark={activeClientRemark}
	{clients}
	{endpoints}
	initialEndpointId={activeShareEndpointId}
	publicDomain={config?.public_domain || ''}
	onclose={() => (shareOpen = false)}
/>

<XrayAutoTunnelModal
	bind:open={autoTunnelOpen}
	clientId={activeClientId}
	clientRemark={activeClientRemark}
	{endpoints}
	onclose={() => (autoTunnelOpen = false)}
/>

<ExternalAccessWizard
	bind:open={wizardOpen}
	initialKind="xray"
	onclose={async () => {
		wizardOpen = false;
		await loadData();
		onRefreshParent?.();
	}}
	onapplied={async () => {
		await loadData();
		onRefreshParent?.();
	}}
	onsettled={async () => {
		await loadData();
		onRefreshParent?.();
	}}
/>

<style>
.xray-card {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 1.125rem 1.25rem;
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
	}

	:global(.summary-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
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

	:global(.banner-icon-warn) {
		color: var(--color-warning);
		flex-shrink: 0;
	}
	:global(.banner-icon-err) {
		color: var(--color-error);
		flex-shrink: 0;
	}
	:global(.banner-icon-info) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
</style>
