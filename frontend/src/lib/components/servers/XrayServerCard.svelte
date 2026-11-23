<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import {
		lastServerVerification,
		recordServerVerification,
		pickFresherVerificationReport,
		clearServerVerification,
	} from '$lib/stores/serverVerification';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Button, Modal, Toggle } from '$lib/components/ui';
	import type {
		XrayConfig,
		XrayStatus,
		XrayClient,
		XrayEndpointConfig,
		XrayMigrationStatus,
		XHTTPTuning,
		XHTTPPreset,
	} from '$lib/types';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';
	import type { EgressOption, KeenDNSDiscovery, VerificationReport } from '$lib/types/serverWizard';
	import RealityFields from './reality/RealityFields.svelte';
	import XrayShareModal from './XrayShareModal.svelte';
	import XrayAutoTunnelModal from './XrayAutoTunnelModal.svelte';
	import ExternalAccessWizard from './external-access/ExternalAccessWizard.svelte';
	import ConnectionChecks from './wizard/ConnectionChecks.svelte';
	import {
		EXTERNAL_ACCESS_LABELS,
		VLESS_PRESET_LABELS,
		getVlessPresetLabel,
		formatXrayEgressLabel,
		formatShortEgressSummary,
		isLegacySocksEgress,
		cleanTunnelDisplayName,
	} from './external-access/connectionLabels';
	import { computeConfigFingerprint, formatDomainList } from './external-access/externalAccessModel';
	import {
		Power,
		RotateCw,
		Plus,
		Trash2,
		Copy,
		QrCode,
		Router,
		Settings,
		AlertTriangle,
		Sparkles,
		Zap,
		Compass,
		ChevronDown,
		ChevronUp,
		ShieldCheck,
		MoreHorizontal,
		ExternalLink,
		Globe,
		KeyRound,
	} from 'lucide-svelte';

	interface Props {
		initialStatus?: XrayStatus | null;
		onOpenDispatcher?: () => void;
		onRefreshParent?: () => void;
	}

	let { initialStatus = null, onOpenDispatcher, onRefreshParent }: Props = $props();

	let status = $state<XrayStatus | null>(untrack(() => initialStatus));
	let config = $state<XrayConfig | null>(null);
	let migrationStatus = $state<XrayMigrationStatus | null>(null);
	let conflictResolving = $state(false);
	let clients = $derived(status?.clients ?? config?.clients ?? []);
	let endpoints = $derived(config?.endpoints ?? []);
	let keendnsDiscovery = $state<KeenDNSDiscovery | null>(null);
	let loading = $state(true);
	let actionLoading = $state(false);
	let wizardOpen = $state(false);

	// Client modals & row menu
	let shareOpen = $state(false);
	let autoTunnelOpen = $state(false);
	let activeClientId = $state('');
	let activeClientRemark = $state('');
	let activeShareEndpointId = $state('');
	let openMenuId = $state<string | null>(null);

	// Endpoint (VLESS route) configuration modal
	let endpointModalOpen = $state(false);
	let editingEndpointId = $state<string | null>(null);
	let editEndpoint = $state<XrayEndpointConfig>({
		id: '',
		name: '',
		enabled: true,
		connection_way: 'direct',
		origin_type: 'public_address',
		public_domain: '',
		public_port: 443,
		origin_host: '',
		origin_scheme: 'https',
		origin_port: 443,
		path: '/cdn-bridge/',
		transport: 'xhttp',
		mode: 'packet-up',
		uplink_method: 'POST',
	});
	let showEndpointAdvancedTuning = $state(false);
	let endpointSaving = $state(false);
	let endpointScheme = $state<'cdn' | 'keendns_public' | 'keendns_cloud' | 'reality'>('keendns_public');

	// Direct VLESS + REALITY route (white-IP preset integrated into routes list)
	let realityState = $state<XrayRealityState | null>(null);
	let realityToggling = $state(false);
	let realityRegenerating = $state(false);
	let realityFormPort = $state(2053);
	let realityFormTransport = $state<XrayRealityTransport>('tcp');
	let realityFormDest = $state('www.microsoft.com:443');
	let realityFormServerName = $state('');
	let realityFormPublicHost = $state('');

	let hasRealityRoute = $derived(
		Boolean(realityState?.settings?.public_key || realityState?.settings?.enabled)
	);
	let totalRoutesCount = $derived(endpoints.length + (hasRealityRoute ? 1 : 0));
	let realityPublicHost = $derived(
		realityState?.settings?.public_host || realityState?.network?.public_host || ''
	);

	// Add client modal
	let addClientOpen = $state(false);
	let newClientRemark = $state('');
	let addClientLoading = $state(false);

	// Settings / Diagnostics modal
	let settingsOpen = $state(false);
	let settingsTab = $state<'general' | 'transport' | 'diagnostics'>('general');
	let editConfig = $state<Partial<XrayConfig>>({});
	let saveConfigLoading = $state(false);
	let resetConfirmOpen = $state(false);
	let resetDeleteKeenDNS = $state(false);
	let resetLoading = $state(false);

	// Egress modal & routing state
	let egressModalOpen = $state(false);
	let egressOptions = $state<EgressOption[]>([]);
	let egressMode = $state<'direct' | 'router' | 'interface' | 'socks'>('direct');
	let egressInterface = $state('');
	let egressSocksPort = $state(1099);
	let egressAllowLan = $state(false);
	let customEgressIface = $state(false);
	let egressSaving = $state(false);

	// Quick connection check summary state
	let checkLoading = $state(false);
	let lastCheckReport = $state<VerificationReport | null>(null);
	let lastCheckError = $state('');

	// XHTTP Tuning
	let tuning = $state<XHTTPTuning | null>(null);
	let tuningSaving = $state(false);
	let showAdvancedTuning = $state(false);

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

	let effectiveUplinkMethod = $derived(
		tuning?.uplink_method || config?.uplink_method || 'POST'
	);

	let effectivePath = $derived.by(() => {
		const base = config?.path || '/cdn-bridge/';
		const suffix = tuning?.stream_path_suffix?.trim();
		if (suffix && !base.endsWith(suffix)) {
			return `${base.replace(/\/+$/, '')}/${suffix}`;
		}
		return base;
	});

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

	let presetLabel = $derived.by(() => {
		return getVlessPresetLabel(tuning?.preset);
	});

	function formatCheckTime(iso?: string): string {
		if (!iso) return '';
		try {
			return new Date(iso).toLocaleTimeString('ru-RU', {
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return '';
		}
	}

	let checkSummaryText = $derived.by(() => {
		if (checkLoading) return 'Проверка: выполняется локальная проверка служб роутера...';
		if (lastCheckError) return `Проверка: ${lastCheckError}`;
		if (!lastCheckReport) return 'Проверка: не запускалась (проверяет локальные службы роутера)';
		const timeStr = formatCheckTime(lastCheckReport.checked_at);
		if (lastCheckReport.overall_status === 'ok') {
			return `Проверка: локальные службы готовы (${timeStr}, без проверки внешней сети)`;
		}
		if (lastCheckReport.overall_status === 'warning') {
			return `Проверка: есть замечания (${timeStr}, локально на роутере)`;
		}
		return `Проверка: обнаружены ошибки (${timeStr}, локально на роутере)`;
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
		const interval = setInterval(loadStatus, 5000);
		const handleDocClick = () => {
			openMenuId = null;
		};
		if (typeof window !== 'undefined') {
			window.addEventListener('click', handleDocClick);
		}
		return () => {
			clearInterval(interval);
			if (typeof window !== 'undefined') {
				window.removeEventListener('click', handleDocClick);
			}
		};
	});

	function syncEgressFromConfig(c: XrayConfig | null) {
		if (!c) return;
		egressAllowLan = Boolean(c.allow_lan);
		const mode =
			c.outbound_mode ||
			(c.outbound_socks_port > 0 ? 'socks' : c.outbound_interface ? 'interface' : 'direct');
		if (mode === 'router') {
			egressMode = 'router';
			egressInterface = '';
		} else if (mode === 'socks') {
			egressMode = 'socks';
			egressSocksPort = c.outbound_socks_port > 0 ? c.outbound_socks_port : 1099;
			egressInterface = '';
		} else if (mode === 'interface' || mode === 'outbound') {
			egressMode = 'interface';
			egressInterface = c.outbound_interface || '';
			if (egressInterface && tunnelInterfaces.length > 0) {
				customEgressIface = !tunnelInterfaces.some((t) => t.iface === egressInterface);
			}
		} else {
			egressMode = 'direct';
			egressInterface = '';
		}
	}

	async function loadData() {
		loading = true;
		try {
			const [s, c, mig, t, caps, rState] = await Promise.all([
				api.getXrayServerStatus(),
				api.getXrayServerConfig(),
				api.getXrayMigrationStatus().catch(() => null),
				api.getXrayTuning().catch(() => null),
				api.getWizardCapabilities('xray').catch(() => null),
				api.getXrayReality(false).catch(() => null),
			]);
			status = s;
			config = c;
			migrationStatus = mig;
			if (t) tuning = t;
			if (rState) realityState = rState;
			egressOptions = caps?.egress_options ? [...caps.egress_options] : [];
			if (caps?.keendns) keendnsDiscovery = caps.keendns;
			syncEgressFromConfig(c);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка загрузки данных сервера VLESS');
		} finally {
			loading = false;
		}
	}

	async function runQuickCheck() {
		checkLoading = true;
		lastCheckError = '';
		try {
			const res = await api.verifyWizard('xray');
			lastCheckReport = res;
			recordServerVerification('xray', res);
		} catch (e) {
			lastCheckError = e instanceof Error ? e.message : 'Не удалось выполнить проверку';
		} finally {
			checkLoading = false;
		}
	}

	async function handleResolveConflict(action: 'keep_new' | 'import_legacy_draft') {
		conflictResolving = true;
		try {
			const res = await api.resolveXrayConflict(action);
			if (res.success) {
				notifications.success(
					action === 'keep_new'
						? 'Активирован сервер VLESS (awgm-xhttpd)'
						: 'Клиенты и параметры Legacy импортированы'
				);
				await loadData();
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка разрешения конфликта');
		} finally {
			conflictResolving = false;
		}
	}

	async function loadStatus() {
		try {
			status = await api.getXrayServerStatus();
			if (!settingsOpen && !egressModalOpen && !egressSaving && !saveConfigLoading && !endpointModalOpen && !endpointSaving) {
				const c = await api.getXrayServerConfig().catch(() => null);
				if (c) {
					config = c;
					syncEgressFromConfig(c);
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
			notifications.success(status?.running ? 'Сервер VLESS запущен' : 'Сервер VLESS остановлен');
			if (config && status) config.enabled = status.running;
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка управления сервером VLESS');
		} finally {
			actionLoading = false;
		}
	}

	async function restartServer() {
		actionLoading = true;
		try {
			status = await api.xrayServerAction('restart');
			realityState = await api.getXrayReality(false).catch(() => realityState);
			notifications.success('Сервер VLESS перезапущен');
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка перезапуска сервера VLESS');
		} finally {
			actionLoading = false;
		}
	}

	async function handleAddClient() {
		if (!newClientRemark.trim()) {
			notifications.error('Введите имя для устройства');
			return;
		}
		addClientLoading = true;
		try {
			const client = await api.addXrayClient(newClientRemark.trim());
			notifications.success(`Устройство «${client.remark}» добавлено`);
			newClientRemark = '';
			addClientOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка добавления устройства');
		} finally {
			addClientLoading = false;
		}
	}

	async function handleDeleteClient(client: XrayClient) {
		openMenuId = null;
		if (!confirm(`Удалить устройство «${client.remark}»?`)) return;
		try {
			await api.deleteXrayClient(client.id);
			notifications.success(`Устройство «${client.remark}» удалено`);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления устройства');
		}
	}

	async function handleToggleClient(client: XrayClient) {
		openMenuId = null;
		try {
			await api.toggleXrayClient(client.id, !client.enabled);
			notifications.success(
				client.enabled
					? `Доступ для «${client.remark}» выключен`
					: `Доступ для «${client.remark}» включён`
			);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения устройства');
		}
	}

	async function handleRotateClientFP(client: XrayClient) {
		openMenuId = null;
		if (!confirm(`Перевыпустить секрет FiniteProto для «${client.remark}»? Потребуется обновить ссылку на клиенте.`)) {
			return;
		}
		try {
			await api.rotateXrayClientFP(client.id);
			notifications.success(`Секрет FiniteProto для «${client.remark}» обновлён`);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка обновления секрета FiniteProto');
		}
	}

	function openPrimaryConnect() {
		const target = clients.find((c) => c.enabled) || clients[0];
		if (!target) {
			addClientOpen = true;
			return;
		}
		openShare(target);
	}

	function openShare(client: XrayClient, endpointId = '') {
		openMenuId = null;
		activeClientId = client.id;
		activeClientRemark = client.remark;
		activeShareEndpointId = endpointId;
		shareOpen = true;
	}

	function createDefaultTuning(preset: XHTTPPreset = 'live_low_latency'): XHTTPTuning {
		return {
			preset,
			uplink_method: preset === 'cdn_get' ? 'GET' : 'POST',
			min_posts_interval_ms: preset === 'cdn_get' ? '5-10' : '',
			max_buffered_posts: preset === 'cdn_get' ? 200 : 1000,
			sc_max_each_post_bytes: preset === 'cdn_get' ? 65536 : 1000000,
			stream_up_server_secs: preset === 'live_low_latency' ? '' : '20-80',
			stream_path_suffix: '',
			enable_xmux: preset === 'balanced',
			x_padding_bytes: '100-500',
			x_padding_header: 'Referer',
			x_padding_placement: 'queryInHeader',
			x_padding_key: '_dc',
			x_padding_method: 'tokenish',
			x_padding_obfs_mode: true,
			session_placement: 'query',
			session_key: 'sid',
			seq_placement: 'query',
			seq_key: 'page',
			fp_chunk_bytes: 131072,
			fp_max_ways: 1,
		};
	}

	function getEndpointScheme(ep: XrayEndpointConfig): 'cdn' | 'keendns_public' | 'keendns_cloud' {
		if (ep.connection_way === 'external_https') return 'cdn';
		if (ep.origin_type === 'cloud_address') return 'keendns_cloud';
		return 'keendns_public';
	}

	function getEndpointBadge(ep: XrayEndpointConfig): { label: string; cls: string } {
		if (ep.connection_way === 'external_https') {
			return { label: 'Через CDN', cls: 'badge-info' };
		}
		if (ep.origin_type === 'cloud_address') {
			return { label: 'KeenDNS · Серый IP / Облако', cls: 'badge-purple' };
		}
		return { label: 'KeenDNS · Белый IP', cls: 'badge-success' };
	}

	function realityDestHost(d?: string): string {
		const v = (d || '').trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '');
		const idx = v.lastIndexOf(':');
		return idx > 0 ? v.slice(0, idx) : v;
	}

	function syncRealityFormFromState() {
		const s = realityState?.settings;
		realityFormPort = s?.public_key ? s.port : realityState?.suggested_port || s?.port || 2053;
		realityFormTransport = (s?.transport as XrayRealityTransport) || 'tcp';
		realityFormDest = s?.dest || 'www.microsoft.com:443';
		realityFormServerName = (s?.server_names && s.server_names[0]) || '';
		realityFormPublicHost = s?.public_host || '';
	}

	function openEditRealityRouteModal() {
		editingEndpointId = 'reality';
		endpointScheme = 'reality';
		showEndpointAdvancedTuning = false;
		syncRealityFormFromState();
		endpointModalOpen = true;
		if (!realityState) {
			void api.getXrayReality(true).then((r) => {
				realityState = r;
				syncRealityFormFromState();
			}).catch(() => {});
		}
	}

	async function handleToggleRealityRoute(nextEnabled: boolean) {
		if (!realityState) return;
		if (!nextEnabled && endpoints.filter((e) => e.enabled).length === 0) {
			notifications.error('Должна оставаться включённой хотя бы одна конфигурация VLESS');
			return;
		}
		realityToggling = true;
		try {
			realityState = await api.updateXrayReality({ enabled: nextEnabled });
			notifications.success(
				nextEnabled ? 'Маршрут «Reality TLS» включён' : 'Маршрут «Reality TLS» выключен'
			);
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения маршрута Reality');
			realityState = await api.getXrayReality(false).catch(() => realityState);
		} finally {
			realityToggling = false;
		}
	}

	async function handleDeleteRealityRoute() {
		if (endpoints.length === 0) {
			notifications.error('Нельзя удалить единственную конфигурацию VLESS');
			return;
		}
		if (!confirm('Удалить маршрут «Reality TLS (Белый IP)»? Ключи будут сброшены, порт закрыт.')) return;
		try {
			realityState = await api.updateXrayReality({ reset: true });
			notifications.success('Маршрут «Reality TLS» удалён');
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления маршрута Reality');
		}
	}

	async function handleRegenerateRealityKeys() {
		if (!confirm('Сгенерировать новые ключи Reality? Все выданные Reality-ссылки перестанут работать, их нужно будет раздать заново.')) {
			return;
		}
		realityRegenerating = true;
		try {
			realityState = await api.updateXrayReality({ regenerate_keys: true });
			notifications.success('Ключи Reality обновлены — раздайте ссылки заново');
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось обновить ключи Reality');
		} finally {
			realityRegenerating = false;
		}
	}

	function openAddEndpointModal(defaultScheme: 'cdn' | 'keendns_public' | 'keendns_cloud' | 'reality' = 'keendns_public') {
		editingEndpointId = null;
		endpointScheme = defaultScheme;
		syncRealityFormFromState();
		const keendnsPub = keendnsDiscovery?.publications?.find(
			(p) => p.compatible && !p.fqdn.toLowerCase().startsWith('tg.')
		)?.fqdn || (keendnsDiscovery?.domain ? `vless.${keendnsDiscovery.domain}` : '');
		const isCDN = defaultScheme === 'cdn';
		const isCloud = defaultScheme === 'keendns_cloud';
		editEndpoint = {
			id: `ep-${Date.now().toString(36)}`,
			name: isCDN ? 'Через CDN' : isCloud ? 'KeenDNS (Серый IP / Облако)' : 'KeenDNS (Белый IP)',
			enabled: true,
			connection_way: isCDN ? 'external_https' : 'direct',
			origin_type: isCloud ? 'cloud_address' : 'public_address',
			public_domain: isCDN ? '' : keendnsPub,
			public_port: 443,
			origin_host: isCDN ? keendnsPub : '',
			origin_scheme: 'https',
			origin_port: 443,
			path: config?.path || '/cdn-bridge/',
			transport: 'xhttp',
			mode: 'packet-up',
			uplink_method: 'POST',
			xhttp_tuning: tuning ? JSON.parse(JSON.stringify(tuning)) : createDefaultTuning('live_low_latency'),
		};
		showEndpointAdvancedTuning = editEndpoint.xhttp_tuning?.preset === 'custom';
		endpointModalOpen = true;
		if (defaultScheme === 'reality' || !realityState) {
			void api.getXrayReality(false).then((r) => {
				realityState = r;
				if (!editingEndpointId) syncRealityFormFromState();
			}).catch(() => {});
		}
	}

	function openEditEndpointModal(ep: XrayEndpointConfig) {
		editingEndpointId = ep.id;
		const cloned: XrayEndpointConfig = JSON.parse(JSON.stringify(ep));
		if (!cloned.public_port) cloned.public_port = config?.public_port || 443;
		if (!cloned.path) cloned.path = config?.path || '/cdn-bridge/';
		if (!cloned.transport) cloned.transport = config?.transport || 'xhttp';
		if (!cloned.mode) cloned.mode = config?.mode || 'packet-up';
		const epTuning = cloned.xhttp_tuning
			? cloned.xhttp_tuning
			: tuning
				? JSON.parse(JSON.stringify(tuning))
				: createDefaultTuning((cloned.uplink_method === 'GET' ? 'cdn_get' : 'live_low_latency') as XHTTPPreset);
		if (!epTuning.fp_chunk_bytes) {
			epTuning.fp_chunk_bytes = 131072;
		}
		if (!epTuning.fp_max_ways) {
			epTuning.fp_max_ways = 1;
		}
		cloned.xhttp_tuning = epTuning;
		editEndpoint = cloned;
		endpointScheme = getEndpointScheme(cloned);
		showEndpointAdvancedTuning = editEndpoint.xhttp_tuning?.preset === 'custom';
		endpointModalOpen = true;
	}

	function selectEndpointScheme(scheme: 'cdn' | 'keendns_public' | 'keendns_cloud' | 'reality') {
		endpointScheme = scheme;
		if (scheme === 'reality') {
			syncRealityFormFromState();
			if (!realityState) {
				void api.getXrayReality(true).then((r) => {
					realityState = r;
					syncRealityFormFromState();
				}).catch(() => {});
			}
			return;
		}
		const keendnsPub = keendnsDiscovery?.publications?.find(
			(p) => p.compatible && !p.fqdn.toLowerCase().startsWith('tg.')
		)?.fqdn || '';
		if (scheme === 'cdn') {
			editEndpoint.connection_way = 'external_https';
			if (!editEndpoint.origin_host && keendnsPub) {
				editEndpoint.origin_host = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name.startsWith('KeenDNS')
			) {
				editEndpoint.name = 'Через CDN';
			}
		} else if (scheme === 'keendns_cloud') {
			editEndpoint.connection_way = 'direct';
			editEndpoint.origin_type = 'cloud_address';
			if (!editEndpoint.public_domain && keendnsPub) {
				editEndpoint.public_domain = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name === 'Через CDN' ||
				editEndpoint.name === 'KeenDNS (Белый IP)' ||
				editEndpoint.name === 'KeenDNS (Белый / Серый IP)'
			) {
				editEndpoint.name = 'KeenDNS (Серый IP / Облако)';
			}
		} else {
			editEndpoint.connection_way = 'direct';
			editEndpoint.origin_type = 'public_address';
			if (!editEndpoint.public_domain && keendnsPub) {
				editEndpoint.public_domain = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name === 'Через CDN' ||
				editEndpoint.name === 'KeenDNS (Серый IP / Облако)' ||
				editEndpoint.name === 'KeenDNS (Белый / Серый IP)'
			) {
				editEndpoint.name = 'KeenDNS (Белый IP)';
			}
		}
	}

	function selectEndpointPreset(preset: XHTTPPreset) {
		if (editEndpoint.mode === 'finiteproto') {
			editEndpoint.mode = 'packet-up';
		}
		if (!editEndpoint.xhttp_tuning) {
			editEndpoint.xhttp_tuning = createDefaultTuning(preset);
			editEndpoint.uplink_method = editEndpoint.xhttp_tuning.uplink_method;
			return;
		}
		const t = editEndpoint.xhttp_tuning;
		t.preset = preset;
		if (preset === 'live_low_latency') {
			t.uplink_method = 'POST';
			t.min_posts_interval_ms = '';
			t.max_buffered_posts = 1000;
			t.sc_max_each_post_bytes = 1000000;
			t.stream_up_server_secs = '';
			t.stream_path_suffix = '';
			t.enable_xmux = false;
			t.x_padding_bytes = '100-500';
		} else if (preset === 'cdn_get') {
			t.uplink_method = 'GET';
			t.min_posts_interval_ms = '5-10';
			t.max_buffered_posts = 200;
			t.sc_max_each_post_bytes = 65536;
			t.stream_up_server_secs = '20-80';
			t.stream_path_suffix = '';
			t.enable_xmux = false;
			t.x_padding_bytes = '100-500';
		} else if (preset === 'balanced') {
			t.uplink_method = 'POST';
			t.min_posts_interval_ms = '';
			t.max_buffered_posts = 1000;
			t.sc_max_each_post_bytes = 1000000;
			t.stream_up_server_secs = '20-80';
			t.stream_path_suffix = '';
			t.enable_xmux = true;
			t.x_padding_bytes = '100-500';
		}
		editEndpoint.uplink_method = t.uplink_method;
	}

	async function saveEndpoint() {
		if (endpointScheme === 'reality') {
			const portNum = Number(realityFormPort);
			if (!Number.isInteger(portNum) || portNum < 1024 || portNum > 65535) {
				notifications.error('Укажите TCP-порт в диапазоне 1024–65535');
				return;
			}
			const destTrim = realityFormDest.trim();
			if (!destTrim) {
				notifications.error('Укажите сайт-маскировку (dest)');
				return;
			}
			endpointSaving = true;
			try {
				const sn = realityFormServerName.trim();
				realityState = await api.updateXrayReality({
					enabled: true,
					port: portNum,
					transport: realityFormTransport,
					dest: destTrim,
					server_names: sn ? [sn] : [realityDestHost(destTrim)],
					public_host: realityFormPublicHost.trim(),
				});
				notifications.success(
					editingEndpointId === 'reality' ? 'Маршрут Reality сохранён' : 'Маршрут Reality добавлен'
				);
				endpointModalOpen = false;
				await loadData();
				onRefreshParent?.();
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения маршрута Reality');
			} finally {
				endpointSaving = false;
			}
			return;
		}
		if (!config) return;
		const dom = (editEndpoint.public_domain || '').trim();
		if (!dom) {
			notifications.error('Укажите публичный домен подключения');
			return;
		}
		endpointSaving = true;
		try {
			const cleanEp: XrayEndpointConfig = {
				...editEndpoint,
				public_domain: dom,
				name: (editEndpoint.name || '').trim() || dom,
				public_port: editEndpoint.public_port || 443,
				path: (editEndpoint.path || '').trim() || '/cdn-bridge/',
				uplink_method: editEndpoint.xhttp_tuning?.uplink_method || editEndpoint.uplink_method || 'POST',
			};
			const currentList = [...endpoints];
			if (editingEndpointId) {
				const idx = currentList.findIndex((e) => e.id === editingEndpointId);
				if (idx >= 0) {
					currentList[idx] = cleanEp;
				} else {
					currentList.push(cleanEp);
				}
			} else {
				currentList.push(cleanEp);
			}
			config = await api.updateXrayServerConfig({
				...config,
				endpoints: currentList,
			});
			notifications.success(editingEndpointId ? 'Конфигурация обновлена' : 'Конфигурация добавлена');
			endpointModalOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения конфигурации');
		} finally {
			endpointSaving = false;
		}
	}

	async function handleToggleEndpoint(ep: XrayEndpointConfig) {
		if (!config) return;
		const nextEnabled = !ep.enabled;
		if (!nextEnabled && endpoints.filter((e) => e.enabled).length <= 1 && !realityState?.settings?.enabled) {
			notifications.error('Должна оставаться включённой хотя бы одна конфигурация VLESS');
			return;
		}
		try {
			const nextList = endpoints.map((item) =>
				item.id === ep.id ? { ...item, enabled: nextEnabled } : item
			);
			config = await api.updateXrayServerConfig({
				...config,
				endpoints: nextList,
			});
			notifications.success(
				nextEnabled ? `Маршрут «${ep.name}» включён` : `Маршрут «${ep.name}» выключен`
			);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переключения маршрута');
		}
	}

	async function handleDeleteEndpoint(ep: XrayEndpointConfig) {
		if (!config) return;
		if (endpoints.length <= 1 && !hasRealityRoute) {
			notifications.error('Нельзя удалить единственную конфигурацию VLESS');
			return;
		}
		if (!confirm(`Удалить конфигурацию «${ep.name || ep.public_domain}»?`)) return;
		try {
			const nextList = endpoints.filter((item) => item.id !== ep.id);
			config = await api.updateXrayServerConfig({
				...config,
				endpoints: nextList,
			});
			notifications.success(`Конфигурация «${ep.name || ep.public_domain}» удалена`);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления конфигурации');
		}
	}

	function openAutoTunnel(client: XrayClient) {
		openMenuId = null;
		activeClientId = client.id;
		activeClientRemark = client.remark;
		autoTunnelOpen = true;
	}

	function openSettingsModal(tab: 'general' | 'transport' | 'diagnostics' = 'general') {
		settingsTab = tab;
		editConfig = config
			? {
					...config,
					outbound_mode:
						config.outbound_mode ||
						(config.outbound_socks_port > 0
							? 'socks'
							: config.outbound_interface
								? 'interface'
								: 'direct'),
				}
			: {};
		settingsOpen = true;
	}

	function openEgressModal() {
		syncEgressFromConfig(config);
		egressModalOpen = true;
	}

	async function saveSettings() {
		saveConfigLoading = true;
		try {
			if (editConfig.outbound_mode === 'direct' || editConfig.outbound_mode === 'router') {
				editConfig.outbound_interface = '';
				editConfig.outbound_socks_port = 0;
			} else if (editConfig.outbound_mode === 'socks') {
				editConfig.outbound_interface = '';
				if (!editConfig.outbound_socks_port || editConfig.outbound_socks_port <= 0) {
					editConfig.outbound_socks_port = 1099;
				}
			} else if (editConfig.outbound_mode === 'interface') {
				editConfig.outbound_socks_port = 0;
			}
			config = await api.updateXrayServerConfig(editConfig);
			notifications.success('Настройки VLESS сохранены');
			settingsOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения настроек');
		} finally {
			saveConfigLoading = false;
		}
	}

	function openResetConfirm() {
		resetDeleteKeenDNS = false;
		resetConfirmOpen = true;
	}

	async function executeFactoryReset() {
		resetLoading = true;
		try {
			const res = await api.resetVlessServer({ delete_keendns: resetDeleteKeenDNS });
			lastCheckReport = null;
			lastCheckError = '';
			clearServerVerification('xray');
			realityState = null;
			migrationStatus = null;
			resetConfirmOpen = false;
			settingsOpen = false;
			await loadData();
			onRefreshParent?.();
			notifications.success(m.server_reset_vless_success());
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

	function selectEgressMode(mode: 'direct' | 'router' | 'interface' | 'socks') {
		egressMode = mode;
		if (mode === 'interface' && !egressInterface && tunnelInterfaces.length > 0) {
			egressInterface = tunnelInterfaces[0].iface || '';
			customEgressIface = false;
		}
		if (mode === 'socks' && (!egressSocksPort || egressSocksPort <= 0)) {
			egressSocksPort = 1099;
		}
	}

	async function saveEgress() {
		if (!config) return;
		if (egressMode === 'interface' && !egressInterface.trim()) {
			notifications.error('Выберите туннель');
			return;
		}
		egressSaving = true;
		try {
			const payload: Partial<XrayConfig> = {
				...config,
				outbound_mode: egressMode,
				outbound_interface: egressMode === 'interface' ? egressInterface.trim() : '',
				outbound_socks_port: egressMode === 'socks' ? (egressSocksPort || 1099) : 0,
				allow_lan: egressAllowLan,
			};
			config = await api.updateXrayServerConfig(payload);
			notifications.success('Маршрут интернета для VLESS обновлён');
			egressModalOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения маршрута');
		} finally {
			egressSaving = false;
		}
	}

	function selectPreset(preset: XHTTPPreset) {
		if (!tuning) {
			tuning = {
				preset,
				uplink_method: preset === 'cdn_get' ? 'GET' : 'POST',
				min_posts_interval_ms: preset === 'cdn_get' ? '5-10' : '',
				max_buffered_posts: preset === 'cdn_get' ? 200 : 1000,
				sc_max_each_post_bytes: preset === 'cdn_get' ? 65536 : 1000000,
				stream_up_server_secs: preset === 'live_low_latency' ? '' : '20-80',
				stream_path_suffix: '',
				enable_xmux: preset === 'balanced',
				x_padding_bytes: '100-500',
				x_padding_header: 'Referer',
				x_padding_placement: 'queryInHeader',
				x_padding_key: '_dc',
				x_padding_method: 'tokenish',
				x_padding_obfs_mode: true,
				session_placement: 'query',
				session_key: 'sid',
				seq_placement: 'query',
				seq_key: 'page',
			};
			return;
		}
		tuning.preset = preset;
		if (preset === 'live_low_latency') {
			tuning.uplink_method = 'POST';
			tuning.min_posts_interval_ms = '';
			tuning.max_buffered_posts = 1000;
			tuning.sc_max_each_post_bytes = 1000000;
			tuning.stream_up_server_secs = '';
			tuning.stream_path_suffix = '';
			tuning.enable_xmux = false;
			tuning.x_padding_bytes = '100-500';
			tuning.x_padding_header = 'Referer';
			tuning.x_padding_placement = 'queryInHeader';
			tuning.x_padding_key = '_dc';
		} else if (preset === 'cdn_get') {
			tuning.uplink_method = 'GET';
			tuning.min_posts_interval_ms = '5-10';
			tuning.max_buffered_posts = 200;
			tuning.sc_max_each_post_bytes = 65536;
			tuning.stream_up_server_secs = '20-80';
			tuning.stream_path_suffix = '';
			tuning.enable_xmux = false;
			tuning.x_padding_bytes = '100-500';
			tuning.x_padding_header = 'Referer';
			tuning.x_padding_placement = 'queryInHeader';
			tuning.x_padding_key = '_dc';
		} else if (preset === 'balanced') {
			tuning.uplink_method = 'POST';
			tuning.min_posts_interval_ms = '';
			tuning.max_buffered_posts = 1000;
			tuning.sc_max_each_post_bytes = 1000000;
			tuning.stream_up_server_secs = '20-80';
			tuning.stream_path_suffix = '';
			tuning.enable_xmux = true;
			tuning.x_padding_bytes = '100-500';
			tuning.x_padding_header = 'Referer';
			tuning.x_padding_placement = 'queryInHeader';
			tuning.x_padding_key = '_dc';
		}
	}

	async function saveTuning() {
		if (!tuning) return;
		tuningSaving = true;
		try {
			tuning = await api.updateXrayTuning(tuning);
			notifications.success('Профиль транспорта применён');
			await loadData();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения параметров транспорта');
		} finally {
			tuningSaving = false;
		}
	}

	async function copy(text: string, label: string) {
		openMenuId = null;
		if (await copyToClipboard(text)) {
			notifications.success(`${label} скопирован`);
		}
	}
</script>

<div class="xray-card">
	<!-- 1. Service Header -->
	<header class="card-header">
		<div class="header-left">
			<div class="title-row">
				<span
					class="led"
					class:led-running={status?.running && !needsCheck}
					class:led-warning={needsCheck}
					class:led-stopped={!status?.running && !needsCheck}
				></span>
				<h2 class="title">{EXTERNAL_ACCESS_LABELS.vlessTitle}</h2>
				{#if loading && !status}
					<span class="badge badge-neutral">Загрузка...</span>
				{:else if !isConfigured && !loading}
					<span class="badge badge-neutral">Не настроен</span>
				{:else if needsCheck}
					<span class="badge badge-warning">{EXTERNAL_ACCESS_LABELS.statusNeedsCheck}</span>
				{:else if status?.running}
					<span class="badge badge-success">{EXTERNAL_ACCESS_LABELS.statusRunning}</span>
				{:else}
					<span class="badge badge-neutral">{EXTERNAL_ACCESS_LABELS.statusStopped}</span>
				{/if}
				{#if status}
					<span
						class="badge font-mono"
						class:badge-info={status.installed && !status.unavailable_reason}
						class:badge-warning={!status.installed || Boolean(status.unavailable_reason)}
					>
						{status.backend || 'awgm-xhttpd'}{status.version ? ` v${status.version.replace(/^awgm-xhttpd\s+/i, '')}` : ''}{status.running && status.pid ? ` · PID ${status.pid}` : ''}
					</span>
				{/if}
			</div>
			<div class="subtitle">
				{EXTERNAL_ACCESS_LABELS.vlessSubtitle}
			</div>
		</div>

		{#if isConfigured}
			<div class="header-actions">
				<Button
					variant="secondary"
					size="sm"
					onclick={() => openSettingsModal('general')}
				>
					<Settings size={14} class="mr-1" />
					Настройки
				</Button>
				<Button
					variant="secondary"
					size="sm"
					onclick={togglePower}
					disabled={actionLoading}
				>
					<Power size={14} class="mr-1" />
					{status?.running ? 'Остановить' : 'Запустить'}
				</Button>
				<button
					type="button"
					class="icon-btn"
					title="Перезапустить сервер VLESS"
					onclick={restartServer}
					disabled={actionLoading}
				>
					<RotateCw size={15} class={actionLoading ? 'spin' : ''} />
				</button>
			</div>
		{/if}
	</header>

	{#if status && (!status.installed || status.unavailable_reason)}
		<div class="alert-banner error">
			<AlertTriangle size={18} class="banner-icon banner-icon-err" />
			<div class="banner-content">
				<div class="banner-title">Демон C-сервера VLESS (awgm-xhttpd) недоступен</div>
				<div class="banner-desc">
					{status.unavailable_reason || status.error || 'Бинарный файл awgm-xhttpd не найден или не прошёл проверку готовности. Резервный запуск на Go или внешнем ядре отключён.'}
				</div>
			</div>
		</div>
	{/if}

	{#if status?.recovery_required}
		<div class="alert-banner warning">
			<AlertTriangle size={18} class="banner-icon banner-icon-warn" />
			<div class="banner-content">
				<div class="banner-title">Требуется восстановление конфигурации</div>
				<div class="banner-desc">{status.recovery_reason || 'Файл настроек повреждён. Исходный файл не был изменён.'}</div>
			</div>
		</div>
	{/if}

	{#if migrationStatus?.discovery?.found && migrationStatus?.discovery?.topology === 'C' && migrationStatus?.decision?.migration_status !== 'migrated'}
		<div class="alert-banner error">
			<AlertTriangle size={18} class="banner-icon banner-icon-err" />
			<div class="banner-content">
				<div class="banner-title">Обнаружен неподдерживаемый профиль Legacy</div>
				<div class="banner-desc">
					{migrationStatus.discovery.conflict_reason || 'Старая конфигурация использует неподдерживаемый транспорт или протокол и сохранена без изменений.'}
				</div>
				<div class="banner-actions">
					<Button variant="secondary" size="sm" disabled={conflictResolving} onclick={() => handleResolveConflict('keep_new')}>
						Использовать сервер VLESS (awgm-xhttpd)
					</Button>
				</div>
			</div>
		</div>
	{:else if migrationStatus?.discovery?.found && migrationStatus?.decision?.migration_status === 'deferred'}
		<div class="alert-banner info">
			<AlertTriangle size={18} class="banner-icon banner-icon-info" />
			<div class="banner-content">
				<div class="banner-title">Обнаружены настройки прежнего сервера</div>
				<div class="banner-desc">Вы можете импортировать клиентов в сервер VLESS (awgm-xhttpd) с сохранением резервной копии или продолжить с текущей конфигурацией.</div>
				<div class="banner-actions">
					<Button variant="primary" size="sm" disabled={conflictResolving} onclick={() => handleResolveConflict('import_legacy_draft')}>
						Импортировать клиентов
					</Button>
					<Button variant="secondary" size="sm" disabled={conflictResolving} onclick={() => handleResolveConflict('keep_new')}>
						Оставить текущие настройки
					</Button>
				</div>
			</div>
		</div>
	{/if}

	{#if !isConfigured && !loading}
		<!-- Empty unconfigured state -->
		<div class="unconfigured-card">
			<p class="unconfigured-desc">
				Настройте подключение телефона, ноутбука или другого роутера за несколько простых шагов. Мастер поможет выбрать адрес роутера или создать домен KeenDNS.
			</p>
			<div class="unconfigured-actions">
				<Button variant="primary" onclick={() => (wizardOpen = true)}>
					<Sparkles size={15} class="mr-1" />
					Настроить подключение
				</Button>
				<Button variant="secondary" onclick={() => openSettingsModal('general')}>
					<Settings size={15} class="mr-1" />
					Настройки вручную
				</Button>
			</div>
		</div>
	{:else}
		<!-- 2. Short 2-line Summary Strip -->
		<section class="summary-strip" aria-label="Сводка настроек VLESS">
			<div class="summary-line">
				<div class="summary-left">
					<Compass size={15} class="summary-icon" />
					<span class="summary-text" class:warn-text={hasLegacySocksEgress || currentEgressUnavailable}>
						{shortEgressSummary}{config?.allow_lan ? ' · LAN открыт' : ''}
					</span>
					{#if currentEgressUnavailable}
						<span class="inline-warn-badge">Туннель недоступен</span>
					{:else if hasLegacySocksEgress}
						<span class="inline-warn-badge">Требуется выбор</span>
					{/if}
				</div>
				<div class="summary-actions">
					<button type="button" class="link-btn" onclick={openEgressModal}>
						Изменить
					</button>
					<span class="dot-sep">·</span>
					<button type="button" class="link-btn" onclick={() => (wizardOpen = true)}>
						Мастер настройки
					</button>
				</div>
			</div>

			<div class="summary-line">
				<div class="summary-left">
					<ShieldCheck size={15} class="summary-icon" />
					<span
						class="summary-text muted"
						class:warn-text={lastCheckReport?.overall_status === 'warning' || lastCheckReport?.overall_status === 'blocked'}
					>
						{checkSummaryText}
					</span>
				</div>
				<div class="summary-actions">
					<button
						type="button"
						class="link-btn"
						onclick={runQuickCheck}
						disabled={checkLoading}
					>
						{checkLoading ? 'Проверка...' : 'Проверить'}
					</button>
					<span class="dot-sep">·</span>
					<button
						type="button"
						class="link-btn"
						onclick={() => openSettingsModal('diagnostics')}
					>
						Подробнее
					</button>
				</div>
			</div>
		</section>

		<!-- 3. Parallel VLESS Configurations (Конфигурации подключения) -->
		<section class="clients-section">
			<div class="section-header">
				<div class="section-title">
					<Globe size={14} class="summary-icon" />
					<span>Конфигурации подключения (Маршруты VLESS)</span>
					<span class="counter">({totalRoutesCount})</span>
				</div>
				<Button variant="secondary" size="sm" onclick={() => openAddEndpointModal()}>
					<Plus size={14} class="mr-1" />
					Добавить конфигурацию
				</Button>
			</div>

			{#if endpoints.length === 0 && !hasRealityRoute}
				<div class="empty-clients">
					<span>Настройте один или несколько параллельных маршрутов подключения (например, через CDN, KeenDNS или прямой Reality TLS по белому IP).</span>
					<Button variant="primary" size="sm" onclick={() => openAddEndpointModal()}>
						<Plus size={14} class="mr-1" />
						Добавить конфигурацию
					</Button>
				</div>
			{:else}
				<div class="clients-list">
					{#each endpoints as ep (ep.id)}
						{@const badge = getEndpointBadge(ep)}
						{@const isFinite = ep.mode === 'finiteproto' || ep.transport === 'finiteproto'}
						{@const epPreset = getVlessPresetLabel(ep.xhttp_tuning?.preset || tuning?.preset)}
						{@const epMethod = ep.xhttp_tuning?.uplink_method || ep.uplink_method || effectiveUplinkMethod}
						<div class="client-item" class:client-disabled={!ep.enabled}>
							<div class="endpoint-info">
								<Toggle
									checked={ep.enabled}
									onchange={() => handleToggleEndpoint(ep)}
								/>
								<div class="endpoint-meta">
									<div class="endpoint-top-line">
										<span class="client-name" class:muted={!ep.enabled}>{ep.name || ep.public_domain}</span>
										<span class="badge {badge.cls}">{badge.label}</span>
										{#if isFinite}
											<span class="tuning-badge font-mono">
												FiniteProto v2 · {Math.round((ep.xhttp_tuning?.fp_chunk_bytes || 131072) / 1024)} КБ · {ep.xhttp_tuning?.fp_max_ways || 1} пот.
											</span>
										{:else}
											<span class="tuning-badge font-mono">XHTTP ({ep.mode || 'packet-up'}) · {epPreset} · {epMethod}</span>
										{/if}
									</div>
									<div class="endpoint-sub-line font-mono">
										<span>{ep.public_domain}:{ep.public_port || config?.public_port || 443}{ep.path || config?.path || '/cdn-bridge/'}</span>
										{#if ep.connection_way === 'external_https' && ep.origin_host}
											<span class="endpoint-origin-arrow">&rarr; {ep.origin_host}</span>
										{/if}
									</div>
								</div>
							</div>

							<div class="client-actions">
								<Button variant="secondary" size="sm" onclick={() => openEditEndpointModal(ep)}>
									<Settings size={14} class="mr-1" />
									Настроить
								</Button>
								{#if endpoints.length > 1 || hasRealityRoute}
									<button
										type="button"
										class="icon-btn danger-btn"
										title="Удалить конфигурацию"
										onclick={() => handleDeleteEndpoint(ep)}
									>
										<Trash2 size={14} />
									</button>
								{/if}
							</div>
						</div>
					{/each}

					{#if hasRealityRoute && realityState}
						{@const rSet = realityState.settings}
						{@const rEnabled = Boolean(rSet.enabled)}
						{@const rWhiteIP = realityState.network?.white_ip}
						{@const rSni = (rSet.server_names && rSet.server_names[0]) || realityDestHost(rSet.dest)}
						<div class="client-item" class:client-disabled={!rEnabled}>
							<div class="endpoint-info">
								<Toggle
									checked={rEnabled}
									disabled={realityToggling}
									onchange={() => void handleToggleRealityRoute(!rEnabled)}
								/>
								<div class="endpoint-meta">
									<div class="endpoint-top-line">
										<span class="client-name" class:muted={!rEnabled}>Напрямую · Reality TLS</span>
										<span class="badge {rWhiteIP === false ? 'badge-warning' : 'badge-success'}">
											Reality TLS · Белый IP
										</span>
										{#if rEnabled && !config?.enabled}
											<span class="badge badge-warning">Сервер VLESS выключен</span>
										{:else if rEnabled && realityState.last_error && !realityState.runtime.running}
											<span class="badge badge-warning">Ошибка запуска</span>
										{:else if rEnabled && realityState.runtime.running && !realityState.wan_port_open}
											<span class="badge badge-warning">Порт {rSet.port} не открыт в WAN</span>
										{/if}
										<span class="tuning-badge font-mono">
											VLESS + REALITY · {rSet.transport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)'}
											{#if realityState.runtime.running}
												· {realityState.runtime.active_conns} подкл.
											{/if}
										</span>
									</div>
									<div class="endpoint-sub-line font-mono">
										<span>{realityPublicHost || 'адрес не определён'}:{rSet.port}</span>
										<span class="endpoint-origin-arrow">&rarr; маскировка под {rSni}</span>
									</div>
									{#if rEnabled && realityState.last_error}
										<div class="inline-warn-msg">{realityState.last_error}</div>
									{/if}
								</div>
							</div>

							<div class="client-actions">
								<Button variant="secondary" size="sm" onclick={openEditRealityRouteModal}>
									<Settings size={14} class="mr-1" />
									Настроить
								</Button>
								{#if endpoints.length > 0}
									<button
										type="button"
										class="icon-btn danger-btn"
										title="Удалить маршрут Reality"
										onclick={() => void handleDeleteRealityRoute()}
									>
										<Trash2 size={14} />
									</button>
								{/if}
							</div>
						</div>
					{/if}
				</div>
			{/if}
		</section>

		<!-- 4. Compact Device List (Устройства) -->
		<section class="clients-section">
			<div class="section-header">
				<div class="section-title">
					<span>Устройства</span>
					<span class="counter">({clients.length})</span>
				</div>
				<Button variant="secondary" size="sm" onclick={() => (addClientOpen = true)}>
					<Plus size={14} class="mr-1" />
					Добавить устройство
				</Button>
			</div>

			{#if clients.length === 0}
				<div class="empty-clients">
					<span>Пока нет добавленных устройств. Добавьте телефон, ноутбук или роутер, чтобы получить QR-код и ссылку.</span>
					<Button variant="primary" size="sm" onclick={() => (addClientOpen = true)}>
						<Plus size={14} class="mr-1" />
						Добавить устройство
					</Button>
				</div>
			{:else}
				<div class="clients-list">
					{#each clients as client (client.id)}
						<div class="client-item" class:client-disabled={!client.enabled}>
							<div class="client-info">
								<Toggle
									checked={client.enabled}
									onchange={() => handleToggleClient(client)}
								/>
								<span class="client-name" class:muted={!client.enabled}>{client.remark || 'Устройство'}</span>
								<span class="client-status-badge" class:active={client.enabled}>
									{client.enabled ? 'Доступ разрешён' : 'Выключен'}
								</span>
							</div>

							<div class="client-actions">
								<Button variant="primary" size="sm" onclick={() => openShare(client)}>
									<QrCode size={14} class="mr-1" />
									QR / Ссылка
								</Button>

								<div class="menu-wrap">
									<button
										type="button"
										class="more-btn"
										aria-label="Действия с устройством"
										onclick={(e) => {
											e.stopPropagation();
											openMenuId = openMenuId === client.id ? null : client.id;
										}}
									>
										<MoreHorizontal size={15} />
										<span>Ещё</span>
									</button>

									{#if openMenuId === client.id}
										<!-- svelte-ignore a11y_click_events_have_key_events -->
										<!-- svelte-ignore a11y_no_static_element_interactions -->
										<div class="dropdown-menu" onclick={(e) => e.stopPropagation()}>
											<button
												type="button"
												class="dropdown-item"
												onclick={() => openAutoTunnel(client)}
											>
												<Router size={13} />
												<span>Подключить другой роутер</span>
											</button>
											<button
												type="button"
												class="dropdown-item"
												onclick={() => copy(client.id, 'UUID')}
											>
												<Copy size={13} />
												<span>Копировать UUID</span>
											</button>
											<button
												type="button"
												class="dropdown-item"
												onclick={() => handleRotateClientFP(client)}
											>
												<RotateCw size={13} />
												<span>Перевыпустить секрет FP</span>
											</button>
											<button
												type="button"
												class="dropdown-item danger"
												onclick={() => handleDeleteClient(client)}
											>
												<Trash2 size={13} />
												<span>Удалить устройство</span>
											</button>
										</div>
									{/if}
								</div>
							</div>
						</div>
					{/each}
				</div>
			{/if}
		</section>
	{/if}
</div>

<!-- Modal: Quick Egress Route Change -->
<Modal bind:open={egressModalOpen} title="Куда направлять интернет (VLESS)" onclose={() => (egressModalOpen = false)} size="md">
	<div class="egress-modal-body">
		<p class="field-hint" style="margin-bottom: 4px;">
			Выберите, как подключившиеся к вашему роутеру устройства будут выходить в интернет.
		</p>

		{#if hasLegacySocksEgress}
			<div class="alert-banner warning" style="margin-bottom: 4px;">
				<AlertTriangle size={16} class="banner-icon banner-icon-warn" />
				<div class="banner-content">
					<div class="banner-title">{EXTERNAL_ACCESS_LABELS.egressLegacyWarning}</div>
					<div class="banner-desc">
						Устаревший локальный выход больше не используется. Выберите один из маршрутов ниже.
					</div>
				</div>
			</div>
		{/if}

		<div class="preset-tabs">
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'direct'}
				onclick={() => selectEgressMode('direct')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressDirect}</div>
				<div class="preset-desc">Через обычного интернет-провайдера вашего роутера</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'router'}
				onclick={() => selectEgressMode('router')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressFirc}</div>
				<div class="preset-desc">По вашим таблицам и правилам маршрутизации на роутере</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'interface'}
				onclick={() => selectEgressMode('interface')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressTunnel}</div>
				<div class="preset-desc">Направлять весь трафик через один выбранный VPN-туннель</div>
			</button>
		</div>

		{#if egressMode === 'interface'}
			<div class="egress-detail-box">
				<label class="form-field">
					<span class="field-label">Выберите туннель:</span>
					<select
						class="select-input"
						value={customEgressIface ? '__custom__' : egressInterface}
						onchange={(e) => {
							const val = (e.currentTarget as HTMLSelectElement).value;
							if (val === '__custom__') {
								customEgressIface = true;
							} else {
								customEgressIface = false;
								egressInterface = val;
							}
						}}
					>
						<option value="" disabled>— Выберите туннель —</option>
						{#each tunnelInterfaces as opt (opt.id)}
							<option value={opt.iface}>
								{cleanTunnelDisplayName(opt.name)}{opt.available ? '' : ' (недоступен)'}
							</option>
						{/each}
						<option value="__custom__">Указать интерфейс вручную...</option>
					</select>
				</label>

				{#if currentEgressUnavailable && egressInterface === config?.outbound_interface}
					<div class="field-warn">
						Текущий выход недоступен — выберите другой или восстановите туннель.
					</div>
				{/if}

				{#if customEgressIface || tunnelInterfaces.length === 0}
					<label class="form-field" style="margin-top: 8px;">
						<span class="field-label">Системное имя интерфейса:</span>
						<input
							type="text"
							bind:value={egressInterface}
							placeholder="например: nwg0, nwg1"
							class="text-input font-mono"
						/>
					</label>
				{/if}
			</div>
		{/if}

		<label class="lan-access-toggle">
			<input type="checkbox" class="checkbox-input" bind:checked={egressAllowLan} />
			<div class="lan-access-text">
				<span class="lan-access-title">Доступ в локальную сеть (LAN)</span>
				<span class="lan-access-desc">Разрешить подключённым устройствам открывать роутер (192.168.x.1) и домашние устройства в локальной сети</span>
			</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (egressModalOpen = false)}>Отмена</Button>
		<Button variant="primary" onclick={saveEgress} disabled={egressSaving}>
			{egressSaving ? 'Сохранение...' : 'Сохранить'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Add Client -->
<Modal bind:open={addClientOpen} title="Добавить устройство VLESS" onclose={() => (addClientOpen = false)} size="sm">
	<form onsubmit={(e) => { e.preventDefault(); void handleAddClient(); }} class="add-client-form">
		<label class="form-field">
			<span class="field-label">Название устройства:</span>
			<input
				type="text"
				bind:value={newClientRemark}
				placeholder="например: Телефон, Ноутбук, Второй роутер"
				class="text-input"
				required
			/>
		</label>
		<p class="field-hint">
			Для каждого устройства создаётся отдельный ключ доступа, который можно в любой момент отключить.
		</p>
	</form>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (addClientOpen = false)}>Отмена</Button>
		<Button variant="primary" onclick={handleAddClient} disabled={addClientLoading}>
			{addClientLoading ? 'Создание...' : 'Добавить'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Settings & Diagnostics -->
<Modal bind:open={settingsOpen} title="Настройки и диагностика VLESS" onclose={() => (settingsOpen = false)} size="lg">
	<div class="settings-modal-wrap">
		<!-- Top wizard & dispatcher banner -->
		<div class="wizard-launch-bar">
			<div class="wizard-launch-info">
				<span class="wizard-launch-title">Пошаговая настройка подключения</span>
				<span class="wizard-launch-sub">Изменение домена, адреса KeenDNS и схемы подключения с сохранением ваших устройств</span>
			</div>
			<div class="wizard-launch-actions">
				{#if onOpenDispatcher}
					<Button
						variant="secondary"
						size="sm"
						onclick={() => {
							settingsOpen = false;
							onOpenDispatcher?.();
						}}
					>
						<ExternalLink size={13} class="mr-1" />
						Входящие подключения
					</Button>
				{/if}
				<Button
					variant="primary"
					size="sm"
					onclick={() => {
						settingsOpen = false;
						wizardOpen = true;
					}}
				>
					<Sparkles size={13} class="mr-1" />
					Открыть мастер
				</Button>
			</div>
		</div>

		<!-- Modal Tabs -->
		<div class="modal-tabs">
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'general'}
				onclick={() => (settingsTab = 'general')}
			>
				Системные параметры
			</button>
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'diagnostics'}
				onclick={() => (settingsTab = 'diagnostics')}
			>
				Диагностика и проверка
			</button>
		</div>

		{#if settingsTab === 'general'}
			<div class="settings-form">
				<div class="cdn-strip">
					<div class="strip-item">
						<span class="strip-label">Активные маршруты</span>
						<span class="strip-val font-mono">
							{endpoints.filter((e) => e.enabled).length} из {endpoints.length} ({config?.public_domain ? formatDomainList(config.public_domain) : 'нет'})
						</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">Внутренний маршрут</span>
						<span class="strip-val font-mono">KeenDNS / CDN &rarr; :{config?.dispatcher_port || 9009} &rarr; :{config?.listen_port || 9008}</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">Выход в интернет</span>
						<span class="strip-val">{shortEgressSummary}</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">C-ядро</span>
						<span class="strip-val font-mono">
							{status?.backend || 'awgm-xhttpd'}{status?.version ? ` (${status.version})` : ''}{status?.binary_path ? ` · ${status.binary_path}` : ''}
						</span>
					</div>
				</div>

				<p class="field-hint">
					Домены, схема подключения (CDN / KeenDNS), пути и профиль транспорта XHTTP настраиваются индивидуально для каждого маршрута в блоке <strong>«Конфигурации подключения (Маршруты VLESS)»</strong> на главной карточке сервера. Ниже расположены только системные порты служб на роутере.
				</p>

				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Локальный порт сервера VLESS (127.0.0.1):</span>
						<input type="number" bind:value={editConfig.listen_port} class="text-input font-mono" />
					</label>
					<label class="form-field">
						<span class="field-label">Внутренний порт диспетчера входящих подключений:</span>
						<input type="number" bind:value={editConfig.dispatcher_port} class="text-input font-mono" />
					</label>
				</div>

				<div class="reset-zone">
					<div class="reset-zone-info">
						<div class="reset-zone-title">{m.server_reset_zone_title()}</div>
						<p class="reset-zone-desc">{m.server_reset_vless_zone_desc()}</p>
					</div>
					<Button variant="danger" size="sm" onclick={openResetConfirm}>
						<Trash2 size={14} class="mr-1" />
						{m.server_reset_button()}
					</Button>
				</div>
			</div>
		{:else if settingsTab === 'diagnostics'}
			<ConnectionChecks kind="xray" currentFingerprint={cardFingerprint} autoRun={true} />
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (settingsOpen = false)}>Закрыть</Button>
		{#if settingsTab === 'general'}
			<Button variant="primary" onclick={saveSettings} disabled={saveConfigLoading}>
				{saveConfigLoading ? 'Сохранение...' : 'Сохранить порты'}
			</Button>
		{/if}
	{/snippet}
</Modal>

<!-- Modal: Add / Edit VLESS Endpoint Configuration -->
<Modal
	bind:open={endpointModalOpen}
	title={editingEndpointId === 'reality'
		? 'Настройка конфигурации: Reality TLS (Белый IP)'
		: editingEndpointId
			? `Настройка конфигурации: ${editEndpoint.name || editEndpoint.public_domain}`
			: 'Новая конфигурация VLESS'}
	onclose={() => (endpointModalOpen = false)}
	size="lg"
>
	<div class="settings-modal-wrap">
		<p class="field-hint">
			Все включённые конфигурации работают на роутере одновременно и используют общий список устройств. Вы можете создать отдельный маршрут через CDN, через KeenDNS (по белому или серому IP) или прямой Reality TLS на свободном порту.
		</p>

		<!-- Scheme Selector -->
		<div class="preset-tabs preset-tabs-4">
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'cdn'}
				onclick={() => selectEndpointScheme('cdn')}
			>
				<div class="preset-name">Через CDN</div>
				<div class="preset-desc">Подключение к домену CDN с проксированием на KeenDNS или белый IP роутера</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'keendns_public'}
				onclick={() => selectEndpointScheme('keendns_public')}
			>
				<div class="preset-name">KeenDNS · Белый IP</div>
				<div class="preset-desc">Прямое подключение к домену KeenDNS с минимальной задержкой и SSL-сертификатом</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'keendns_cloud'}
				onclick={() => selectEndpointScheme('keendns_cloud')}
			>
				<div class="preset-name">KeenDNS · Серый IP / Облако</div>
				<div class="preset-desc">Через облако KeenDNS, когда у провайдера серый IP (CGNAT) без проброса портов</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'reality'}
				onclick={() => selectEndpointScheme('reality')}
			>
				<div class="preset-top-row">
					<span class="preset-name">Reality TLS · Белый IP</span>
					{#if realityState?.network}
						<span class="badge {realityState.network.white_ip ? 'badge-success' : 'badge-warning'}">
							{realityState.network.white_ip ? 'Белый IP' : 'Нужен белый IP'}
						</span>
					{/if}
				</div>
				<div class="preset-desc">Прямой VLESS + REALITY без CDN и домена (порт {realityFormPort || 2053}, TCP или XHTTP)</div>
			</button>
		</div>

		{#if editingEndpointId && editingEndpointId !== 'reality' && endpointScheme === 'reality'}
			<div class="alert-banner info">
				<AlertTriangle size={16} class="banner-icon banner-icon-info" />
				<div class="banner-content">
					<div class="banner-desc">
						Маршрут Reality работает параллельно на отдельном TCP-порту — он будет добавлен или обновлён, не удаляя существующий маршрут «{editEndpoint.name || editEndpoint.public_domain}».
					</div>
				</div>
			</div>
		{/if}

		{#if endpointScheme === 'reality'}
			<div class="settings-form">
				<RealityFields
					rs={realityState}
					bind:port={realityFormPort}
					bind:transport={realityFormTransport}
					bind:dest={realityFormDest}
					bind:serverName={realityFormServerName}
					bind:publicHost={realityFormPublicHost}
					showTransport={false}
					surface="modal"
					onrefreshed={(fresh) => (realityState = fresh)}
				/>

				<div class="tuning-section">
					<div class="tuning-header">
						<div class="tuning-title-group">
							<div class="tuning-title">
								<Zap size={15} class="banner-icon-warn" />
								<span>Протокол транспорта Reality</span>
							</div>
							<span class="tuning-badge font-mono">
								{realityFormTransport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)'} · порт {realityFormPort}
							</span>
						</div>
					</div>

					<div class="preset-tabs preset-tabs-2">
						<button
							type="button"
							class="preset-tab"
							class:active={realityFormTransport === 'tcp'}
							onclick={() => (realityFormTransport = 'tcp')}
						>
							<div class="preset-name">TCP (raw) · Максимальная скорость</div>
							<div class="preset-desc">Рекомендуется: минимальная задержка и нагрузка на CPU роутера. Поддерживается Happ, sing-box, Mihomo и v2rayNG.</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={realityFormTransport === 'xhttp'}
							onclick={() => (realityFormTransport = 'xhttp')}
						>
							<div class="preset-name">XHTTP (stream-one) · HTTP/2</div>
							<div class="preset-desc">HTTP/2-поток внутри Reality TLS. Выглядит как обычный браузерный трафик.</div>
						</button>
					</div>

					{#if realityState?.settings?.public_key}
						<div class="reality-keys-box">
							<div class="reality-key-row">
								<KeyRound size={14} class="summary-icon" />
								<span class="field-label">Public key (pbk):</span>
								<code class="checklist-code font-mono">{realityState.settings.public_key}</code>
								<button
									type="button"
									class="mini-copy-btn"
									onclick={() => copy(realityState?.settings?.public_key || '', 'Публичный ключ Reality')}
								>
									<Copy size={12} />
									<span>Копировать</span>
								</button>
								<Button
									variant="secondary"
									size="sm"
									onclick={() => void handleRegenerateRealityKeys()}
									disabled={realityRegenerating}
								>
									<RotateCw size={13} class="mr-1" />
									{realityRegenerating ? 'Обновление...' : 'Сменить ключи'}
								</Button>
							</div>
							{#if realityState.settings.short_ids && realityState.settings.short_ids.length > 0}
								<div class="reality-key-row">
									<span class="field-label">Short ID (sid):</span>
									<code class="checklist-code font-mono">{realityState.settings.short_ids[0]}</code>
								</div>
							{/if}
						</div>
					{/if}
				</div>
			</div>
		{:else}
		<div class="settings-form">
			<div class="form-row">
				<label class="form-field">
					<span class="field-label">Название конфигурации:</span>
					<input
						type="text"
						bind:value={editEndpoint.name}
						placeholder="например: Через CDN, KeenDNS (Белый IP)"
						class="text-input"
					/>
				</label>
				<label class="form-field">
					<span class="field-label">
						{editEndpoint.connection_way === 'external_https'
							? 'Публичный домен CDN:'
							: 'Домен KeenDNS (4-го уровня):'}
					</span>
					<input
						type="text"
						bind:value={editEndpoint.public_domain}
						placeholder={editEndpoint.connection_way === 'external_https'
							? 'cdn.example.ru'
							: 'vless.example.keenetic.pro'}
						class="text-input font-mono"
					/>
				</label>
			</div>

			{#if keendnsDiscovery?.publications && keendnsDiscovery.publications.length > 0}
				<div class="keendns-Chips-row">
					<span class="picker-chip-label">
						{editEndpoint.connection_way === 'external_https'
							? 'Выбрать источник (Origin) из публикаций KeenDNS на роутере:'
							: 'Выбрать существующий домен KeenDNS на роутере:'}
					</span>
					<div class="keendns-chips">
						{#each keendnsDiscovery.publications as pub (pub.fqdn)}
							<button
								type="button"
								class="keendns-chip font-mono"
								class:active={editEndpoint.connection_way === 'external_https'
									? editEndpoint.origin_host === pub.fqdn
									: editEndpoint.public_domain === pub.fqdn}
								onclick={() => {
									if (editEndpoint.connection_way === 'external_https') {
										editEndpoint.origin_host = pub.fqdn;
									} else {
										editEndpoint.public_domain = pub.fqdn;
									}
								}}
							>
								{pub.fqdn}
							</button>
						{/each}
					</div>
				</div>
			{/if}

			{#if editEndpoint.connection_way === 'external_https'}
				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Адрес источника (Origin KeenDNS или белый IP роутера):</span>
						<input
							type="text"
							bind:value={editEndpoint.origin_host}
							placeholder="vless.example.keenetic.pro"
							class="text-input font-mono"
						/>
					</label>
					<div class="form-field">
						<span class="field-label">Параметры для панели CDN:</span>
						<div class="readonly-pill font-mono">
							Origin: https://{editEndpoint.origin_host || '<домен-keendns>'}:443 · Кеш выкл. для {(editEndpoint.path || '/cdn-bridge/').replace(/\/+$/, '')}*
						</div>
					</div>
				</div>
			{/if}

			<div class="form-row">
				<label class="form-field">
					<span class="field-label">Входящий путь (Path):</span>
					<input
						type="text"
						bind:value={editEndpoint.path}
						placeholder="/cdn-bridge/"
						class="text-input font-mono"
					/>
				</label>
				<label class="form-field">
					<span class="field-label">Публичный порт и режим транспорта:</span>
					<div class="inline-inputs">
						<input
							type="number"
							bind:value={editEndpoint.public_port}
							placeholder="443"
							class="text-input font-mono"
						/>
						<select bind:value={editEndpoint.mode} class="select-input">
							<option value="packet-up">packet-up (XHTTP)</option>
							<option value="stream-up">stream-up (XHTTP)</option>
							<option value="stream-one">stream-one (XHTTP)</option>
							<option value="auto">auto (XHTTP)</option>
							<option value="finiteproto">finiteproto (FiniteProto v2)</option>
						</select>
					</div>
				</label>
			</div>

			<!-- Unified Per-endpoint Transport Profile (XHTTP Presets + FiniteProto v2) -->
			{#if editEndpoint.xhttp_tuning}
				<div class="tuning-section">
					<div class="tuning-header">
						<div class="tuning-title-group">
							<div class="tuning-title">
								<Zap size={15} class="banner-icon-warn" />
								<span>Профиль транспорта для этого маршрута</span>
							</div>
							<span class="tuning-badge font-mono">
								{#if editEndpoint.mode === 'finiteproto'}
									FiniteProto v2 · {Math.round((editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072) / 1024)} КБ · {editEndpoint.xhttp_tuning.fp_max_ways || 1} пот.
								{:else}
									{getVlessPresetLabel(editEndpoint.xhttp_tuning.preset)} · {editEndpoint.xhttp_tuning.uplink_method}
								{/if}
							</span>
						</div>
					</div>

					<div class="preset-tabs preset-tabs-4">
						<button
							type="button"
							class="preset-tab"
							class:active={editEndpoint.mode !== 'finiteproto' && editEndpoint.xhttp_tuning.preset === 'live_low_latency'}
							onclick={() => selectEndpointPreset('live_low_latency')}
						>
							<div class="preset-name">{VLESS_PRESET_LABELS.live_low_latency.title}</div>
							<div class="preset-desc">{VLESS_PRESET_LABELS.live_low_latency.description}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={editEndpoint.mode !== 'finiteproto' && editEndpoint.xhttp_tuning.preset === 'cdn_get'}
							onclick={() => selectEndpointPreset('cdn_get')}
						>
							<div class="preset-name">{VLESS_PRESET_LABELS.cdn_get.title}</div>
							<div class="preset-desc">{VLESS_PRESET_LABELS.cdn_get.description}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={editEndpoint.mode !== 'finiteproto' && editEndpoint.xhttp_tuning.preset === 'balanced'}
							onclick={() => selectEndpointPreset('balanced')}
						>
							<div class="preset-name">{VLESS_PRESET_LABELS.balanced.title}</div>
							<div class="preset-desc">{VLESS_PRESET_LABELS.balanced.description}</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={editEndpoint.mode === 'finiteproto'}
							onclick={() => {
								editEndpoint.mode = 'finiteproto';
								editEndpoint.uplink_method = 'POST';
								if (editEndpoint.xhttp_tuning) {
									if (!editEndpoint.xhttp_tuning.fp_chunk_bytes) editEndpoint.xhttp_tuning.fp_chunk_bytes = 131072;
									if (!editEndpoint.xhttp_tuning.fp_max_ways) editEndpoint.xhttp_tuning.fp_max_ways = 1;
								}
								showEndpointAdvancedTuning = false;
							}}
						>
							<div class="preset-name">FiniteProto v2 (Короткие запросы)</div>
							<div class="preset-desc">Короткие HTTPS-запросы с HMAC-SHA256 аутентификацией (mode=finiteproto&amp;fp_v=2). Требуется клиент с поддержкой FiniteProto v2</div>
						</button>
						<button
							type="button"
							class="preset-tab"
							class:active={editEndpoint.mode !== 'finiteproto' && editEndpoint.xhttp_tuning.preset === 'custom'}
							onclick={() => {
								selectEndpointPreset('custom');
								showEndpointAdvancedTuning = true;
							}}
						>
							<div class="preset-name">{VLESS_PRESET_LABELS.custom.title}</div>
							<div class="preset-desc">{VLESS_PRESET_LABELS.custom.description}</div>
						</button>
					</div>

					{#if editEndpoint.mode === 'finiteproto'}
						<div class="tuning-params">
							<div class="fp-tuning-header">
								<span class="fp-tuning-title">Настройка производительности FiniteProto v2</span>
								<span class="fp-tuning-sub">Выбранные параметры автоматически вшиваются в ссылку и QR-код (для импорта в Rawline)</span>
							</div>

							<div class="fp-param-block">
								<span class="field-label">1. Размер блока данных (chunk_bytes):</span>
								<div class="preset-tabs">
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 65536}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_chunk_bytes = 65536;
										}}
									>
										<div class="preset-name">64 КБ · Экономичный</div>
										<div class="preset-desc">Минимальная нагрузка на память телефона. Подходит для слабого мобильного интернета (3G/LTE).</div>
									</button>
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 131072}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_chunk_bytes = 131072;
										}}
									>
										<div class="preset-name">128 КБ · Рекомендуется</div>
										<div class="preset-desc">Оптимальный баланс: до 600 Мбит/с по Wi-Fi без перегрузки памяти и зависаний телефона.</div>
									</button>
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072) === 262144}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_chunk_bytes = 262144;
										}}
									>
										<div class="preset-name">256 КБ · Максимум</div>
										<div class="preset-desc">Крупные пакеты для ПК и роутеров. На телефонах при многопоточных спидтестах может перегружать ОЗУ.</div>
									</button>
								</div>
							</div>

							<div class="fp-param-block">
								<span class="field-label">2. Параллельные потоки скачивания (max_ways):</span>
								<div class="preset-tabs preset-tabs-4">
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_max_ways || 1) === 1}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_max_ways = 1;
										}}
									>
										<div class="preset-name">1 поток · Рекомендуется</div>
										<div class="preset-desc">Стабильная высокая скорость по Wi-Fi и LTE без лавинной нагрузки на процессор и память телефона.</div>
									</button>
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_max_ways || 1) === 2}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_max_ways = 2;
										}}
									>
										<div class="preset-name">2 потока · Сбалансированный</div>
										<div class="preset-desc">Помогает компенсировать высокую задержку (пинг &gt; 50 мс) на дальних или медленных узлах CDN.</div>
									</button>
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_max_ways || 1) === 3}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_max_ways = 3;
										}}
									>
										<div class="preset-name">3 потока · Агрессивный</div>
										<div class="preset-desc">Для перегруженных CDN с жёстким ограничением скорости на один поток. Повышенная нагрузка на телефон.</div>
									</button>
									<button
										type="button"
										class="preset-tab"
										class:active={(editEndpoint.xhttp_tuning.fp_max_ways || 1) === 4}
										onclick={() => {
											if (editEndpoint.xhttp_tuning) editEndpoint.xhttp_tuning.fp_max_ways = 4;
										}}
									>
										<div class="preset-name">4 потока · Максимум</div>
										<div class="preset-desc">До 4 параллельных запросов на сессию. Рекомендуется только для ПК или при очень высоком пинге.</div>
									</button>
								</div>
							</div>

							<div class="readonly-pill font-mono">
								FiniteProto v2 · HTTP/1.1 POST · Блок {Math.round((editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072) / 1024)} КБ (chunk_bytes={editEndpoint.xhttp_tuning.fp_chunk_bytes || 131072}) · Потоков: {editEndpoint.xhttp_tuning.fp_max_ways || 1} (max_ways={editEndpoint.xhttp_tuning.fp_max_ways || 1})
							</div>
						</div>
					{:else}
						<button
							type="button"
							class="advanced-toggle-btn"
							onclick={() => (showEndpointAdvancedTuning = !showEndpointAdvancedTuning)}
						>
							<span>Расширенные параметры транспорта маршрута</span>
							{#if showEndpointAdvancedTuning || editEndpoint.xhttp_tuning.preset === 'custom'}
								<ChevronUp size={14} />
							{:else}
								<ChevronDown size={14} />
							{/if}
						</button>

						{#if showEndpointAdvancedTuning || editEndpoint.xhttp_tuning.preset === 'custom'}
							<div class="tuning-params">
								<div class="form-row">
									<label class="form-field">
										<span class="field-label">HTTP-метод Uplink:</span>
										{#if editEndpoint.xhttp_tuning.preset === 'custom'}
											<select bind:value={editEndpoint.xhttp_tuning.uplink_method} class="select-input">
												<option value="POST">POST (рекомендуется)</option>
												<option value="GET">GET (для строгих прокси без POST)</option>
											</select>
										{:else}
											<div class="readonly-pill font-mono">{editEndpoint.xhttp_tuning.uplink_method} (packet-up)</div>
										{/if}
									</label>
									<label class="form-field">
										<span class="field-label">Интервал между пакетами (мс):</span>
										{#if editEndpoint.xhttp_tuning.preset === 'custom'}
											<input type="text" bind:value={editEndpoint.xhttp_tuning.min_posts_interval_ms} placeholder="0 или 5-10" class="text-input font-mono" />
										{:else}
											<div class="readonly-pill font-mono">
												{editEndpoint.xhttp_tuning.min_posts_interval_ms ? `${editEndpoint.xhttp_tuning.min_posts_interval_ms} мс` : 'Без задержки (0 мс)'}
											</div>
										{/if}
									</label>
								</div>

								<div class="form-row">
									<label class="form-field">
										<span class="field-label">Мультиплексирование (Xmux):</span>
										{#if editEndpoint.xhttp_tuning.preset === 'custom'}
											<label class="checkbox-label">
												<input type="checkbox" bind:checked={editEndpoint.xhttp_tuning.enable_xmux} class="checkbox-input" />
												<span>Включить Xmux</span>
											</label>
										{:else}
											<div class="readonly-pill">
												{editEndpoint.xhttp_tuning.enable_xmux ? 'Включено' : 'Выключено (изоляция потоков)'}
											</div>
										{/if}
									</label>
									<label class="form-field">
										<span class="field-label">Маскировка заголовков (X-Padding):</span>
										{#if editEndpoint.xhttp_tuning.preset === 'custom'}
											<input type="text" bind:value={editEndpoint.xhttp_tuning.x_padding_bytes} placeholder="100-500" class="text-input font-mono" />
										{:else}
											<div class="readonly-pill font-mono">
												{editEndpoint.xhttp_tuning.x_padding_bytes || '100-500'} B в {editEndpoint.xhttp_tuning.x_padding_header || 'Referer'}
											</div>
										{/if}
									</label>
								</div>
							</div>
						{/if}
					{/if}
				</div>
			{/if}
		</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (endpointModalOpen = false)}>Отмена</Button>
		<Button variant="primary" onclick={saveEndpoint} disabled={endpointSaving}>
			{endpointSaving ? 'Сохранение...' : endpointScheme === 'reality' ? 'Сохранить Reality' : 'Сохранить конфигурацию'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Confirm VLESS Factory Reset -->
<Modal
	bind:open={resetConfirmOpen}
	title={m.server_reset_vless_modal_title()}
	onclose={() => { if (!resetLoading) resetConfirmOpen = false; }}
	size="sm"
>
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">{m.server_reset_vless_modal_body()}</p>
		<label class="reset-keendns-check">
			<input type="checkbox" bind:checked={resetDeleteKeenDNS} class="checkbox-input" />
			<div class="reset-keendns-text">
				<span class="reset-keendns-label">{m.server_reset_vless_keendns_label()}</span>
				<span class="reset-keendns-hint">{m.server_reset_vless_keendns_hint()}</span>
			</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (resetConfirmOpen = false)} disabled={resetLoading}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" onclick={executeFactoryReset} disabled={resetLoading}>
			<Trash2 size={14} class="mr-1" />
			{resetLoading ? m.server_reset_progress() : m.server_reset_confirm_button()}
		</Button>
	{/snippet}
</Modal>

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

	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
		border: 1px solid transparent;
	}
	.badge-success {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}
	.badge-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}
	.badge-neutral {
		background: var(--color-bg-hover);
		border-color: var(--color-border);
		color: var(--color-text-muted);
	}
	.badge-info {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}
	.badge-purple {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}

	.endpoint-info {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 200px;
		flex: 1;
	}

	.endpoint-meta {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
	}

	.endpoint-top-line {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.endpoint-sub-line {
		font-size: 11.5px;
		color: var(--color-text-muted);
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		word-break: break-all;
	}

	.endpoint-origin-arrow {
		color: var(--color-text-secondary);
		opacity: 0.85;
	}

	.danger-btn:hover:not(:disabled) {
		color: var(--color-error) !important;
		border-color: var(--color-error) !important;
	}

	.keendns-Chips-row {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.picker-chip-label {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.keendns-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.keendns-chip {
		padding: 4px 10px;
		font-size: 11.5px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.keendns-chip:hover {
		border-color: var(--color-accent);
		color: var(--color-text-primary);
	}
	.keendns-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.12));
		color: var(--color-accent);
	}
	.chip-tag {
		display: inline-block;
		margin-left: 4px;
		padding: 1px 5px;
		font-size: 9.5px;
		border-radius: 3px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.chip-tag-vless {
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.12));
		border-color: var(--color-accent-border, rgba(59, 130, 246, 0.3));
		color: var(--color-accent);
	}

	.led {
		width: 9px;
		height: 9px;
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
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.icon-btn {
		width: 30px;
		height: 30px;
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
		transition: all var(--t-fast) ease;
	}
	.icon-btn:hover:not(:disabled) {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
		border-color: var(--color-accent);
	}
	.icon-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	/* 2-line Summary Strip */
	.summary-strip {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 9px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.summary-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
		font-size: 12.5px;
	}

	.summary-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		flex-wrap: wrap;
	}

	:global(.summary-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.summary-text {
		color: var(--color-text-primary);
		font-weight: 500;
	}
	.summary-text.muted {
		color: var(--color-text-secondary);
		font-weight: 400;
	}
	.summary-text.warn-text {
		color: var(--color-warning);
	}

	.inline-warn-badge {
		font-size: 11px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		font-weight: 500;
	}

	.summary-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 0;
	}

	.link-btn {
		background: none;
		border: none;
		padding: 0;
		color: var(--color-accent);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
	}
	.link-btn:hover:not(:disabled) {
		text-decoration: underline;
	}
	.link-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.dot-sep {
		color: var(--color-text-muted);
		opacity: 0.6;
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

	/* Clients Section */
	.clients-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}

	.section-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.counter {
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.empty-clients {
		padding: 1.25rem;
		text-align: center;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-size: 12.5px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
	}

	.clients-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.client-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 8px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		gap: 10px;
		flex-wrap: wrap;
	}

	.client-item.client-disabled {
		background: var(--color-bg-secondary);
	}

	.client-info {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 140px;
	}

	.client-name {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.client-name.muted {
		color: var(--color-text-muted);
	}

	.client-status-badge {
		font-size: 10.5px;
		padding: 1px 7px;
		border-radius: 4px;
		background: var(--color-bg-hover);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.client-status-badge.active {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}

	.client-actions {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.menu-wrap {
		position: relative;
	}

	.more-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 5px 9px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}
	.more-btn:hover {
		color: var(--color-text-primary);
		border-color: var(--color-accent);
	}

	.dropdown-menu {
		position: absolute;
		right: 0;
		top: calc(100% + 4px);
		min-width: 205px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		box-shadow: 0 8px 20px rgba(0, 0, 0, 0.35);
		padding: 4px;
		z-index: 30;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.dropdown-item {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 7px 10px;
		background: transparent;
		border: none;
		border-radius: 4px;
		color: var(--color-text-primary);
		font-size: 12px;
		text-align: left;
		cursor: pointer;
	}
	.dropdown-item:hover {
		background: var(--color-bg-hover);
	}
	.dropdown-item.danger {
		color: var(--color-error);
	}

	/* Settings & Egress Modals */
	.settings-modal-wrap,
	.egress-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.wizard-launch-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		flex-wrap: wrap;
	}

	.wizard-launch-info {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.wizard-launch-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.wizard-launch-sub {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.wizard-launch-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.modal-tabs {
		display: flex;
		gap: 6px;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 8px;
		flex-wrap: wrap;
	}

	.modal-tab {
		padding: 6px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
		background: transparent;
		color: var(--color-text-secondary);
		font-size: 12.5px;
		cursor: pointer;
	}
	.modal-tab.active {
		background: var(--color-bg-primary);
		border-color: var(--color-border);
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.cdn-strip {
		display: flex;
		align-items: center;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 12px;
		flex-wrap: wrap;
		gap: 10px;
	}

	.strip-item {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.strip-label {
		font-size: 10px;
		text-transform: uppercase;
		color: var(--color-text-muted);
		letter-spacing: 0.04em;
	}

	.strip-val {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.strip-divider {
		width: 1px;
		height: 22px;
		background: var(--color-border);
	}

	.add-client-form,
	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
	}

	.inline-inputs {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.text-input,
	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
		outline: none;
	}
	.text-input:focus,
	.select-input:focus {
		border-color: var(--color-accent);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.field-warn {
		font-size: 12px;
		color: var(--color-warning);
		margin-top: 6px;
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

	.alert-banner {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 14px;
		border-radius: var(--radius-sm);
		font-size: 12.5px;
	}
	.alert-banner.warning {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-text-primary);
	}
	.alert-banner.error {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-text-primary);
	}
	.alert-banner.info {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-text-primary);
	}
	.banner-content {
		display: flex;
		flex-direction: column;
		gap: 4px;
		flex: 1;
	}
	.banner-title {
		font-weight: 600;
	}
	.banner-desc {
		color: var(--color-text-muted);
		font-size: 12px;
		line-height: 1.4;
	}
	.banner-actions {
		display: flex;
		gap: 8px;
		margin-top: 4px;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.mr-1 {
		margin-right: 4px;
	}

	.tuning-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px;
	}

	.tuning-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.tuning-title-group {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.tuning-title {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tuning-badge {
		font-size: 10px;
		padding: 2px 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.preset-tabs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	.preset-tabs-2,
	.preset-tabs-4 {
		grid-template-columns: repeat(2, 1fr);
	}

	.preset-tab {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.preset-tab:hover {
		border-color: var(--color-accent);
	}
	.preset-tab.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.08));
	}

	.preset-name {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.preset-top-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	.reality-keys-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.reality-key-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		font-size: 12px;
	}

	.checklist-code {
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		font-size: 11px;
		word-break: break-all;
	}

	.mini-copy-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 3px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 11px;
		cursor: pointer;
	}
	.mini-copy-btn:hover {
		border-color: var(--color-accent);
	}

	.inline-warn-msg {
		margin-top: 4px;
		font-size: 11.5px;
		color: var(--color-warning);
	}

	.preset-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.egress-detail-box {
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.lan-access-toggle {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		margin-top: 10px;
		padding: 10px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		cursor: pointer;
	}

	.lan-access-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.lan-access-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.lan-access-desc {
		font-size: 11.5px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.tuning-params {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.fp-tuning-header {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.fp-tuning-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.fp-tuning-sub {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.fp-param-block {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.advanced-toggle-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		align-self: flex-start;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		font-size: 12px;
		cursor: pointer;
		padding: 4px 0;
	}

	.readonly-pill {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 12px;
		color: var(--color-text-primary);
	}

	.checkbox-label {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-text-primary);
		cursor: pointer;
		padding: 7px 0;
	}

	.checkbox-input {
		width: 16px;
		height: 16px;
		flex-shrink: 0;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	.reset-zone {
		margin-top: 10px;
		padding: 12px 14px;
		border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-error) 6%, var(--color-bg-secondary));
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
	}

	.reset-zone-info {
		display: flex;
		flex-direction: column;
		gap: 3px;
		flex: 1 1 260px;
	}

	.reset-zone-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-error);
	}

	.reset-zone-desc {
		margin: 0;
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.confirm-content {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		padding: 0.5rem 0;
		gap: 10px;
	}

	.confirm-p {
		font-size: 13px;
		color: var(--color-text-primary);
		margin: 0;
		line-height: 1.45;
	}

	.reset-keendns-check {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		width: 100%;
		text-align: left;
		padding: 10px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		cursor: pointer;
	}

	.reset-keendns-check input[type='checkbox'] {
		width: 18px;
		height: 18px;
		min-width: 18px;
		flex: 0 0 18px;
		margin-top: 2px;
	}

	.reset-keendns-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		flex: 1 1 0;
		min-width: 0;
	}

	.reset-keendns-label {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.reset-keendns-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.text-danger {
		color: var(--color-error);
	}

	.mr-1 {
		margin-right: 4px;
	}

	.mb-3 {
		margin-bottom: 12px;
	}

	.spin {
		animation: spin 1s linear infinite;
	}
	@keyframes spin {
		from { transform: rotate(0deg); }
		to { transform: rotate(360deg); }
	}

	@media (max-width: 900px) {
		.preset-tabs-4 {
			grid-template-columns: repeat(2, 1fr);
		}
	}

	@media (max-width: 640px) {
		.preset-tabs,
		.preset-tabs-4,
		.form-row,
		.inline-inputs {
			grid-template-columns: 1fr;
		}
		.cdn-strip {
			flex-direction: column;
			align-items: flex-start;
		}
		.strip-divider {
			display: none;
		}
	}
</style>
