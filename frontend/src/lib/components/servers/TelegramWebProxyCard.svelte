<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import {
		lastServerVerification,
		recordServerVerification,
		pickFresherVerificationReport,
		clearServerVerification,
	} from '$lib/stores/serverVerification';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Button, Modal, Toggle } from '$lib/components/ui';
	import ExternalAccessWizard from './external-access/ExternalAccessWizard.svelte';
	import ConnectionChecks from './wizard/ConnectionChecks.svelte';
	import TelegramUpstreamsEditor from './TelegramUpstreamsEditor.svelte';
	import {
		EXTERNAL_ACCESS_LABELS,
		formatTelegramCarrierLabel,
		formatTelegramEgressLabel,
		formatShortEgressSummary,
		cleanTunnelDisplayName,
	} from './external-access/connectionLabels';
	import { computeConfigFingerprint, formatDomainList } from './external-access/externalAccessModel';
	import type {
		TgWebProxyConfig,
		TgWebProxyStatus,
		TgWebProxyRevealData,
		TelegramUpstream,
		TelegramUser,
		TgUserRevealData,
		WebProxyEndpoint,
		WebProxyLink,
		TgNetworkPreset,
	} from '$lib/types';
	import type { EgressOption, VerificationReport } from '$lib/types/serverWizard';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import {
		Power,
		RotateCw,
		Copy,
		QrCode,
		ExternalLink,
		Settings,
		KeyRound,
		Send,
		ShieldCheck,
		RefreshCw,
		Smartphone,
		Globe,
		AlertTriangle,
		Trash2,
		Plus,
		Sparkles,
		Compass,
	} from 'lucide-svelte';

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
	let lastCheckError = $state('');

	// Quick Egress modal
	let egressModalOpen = $state(false);
	let quickUpstreams = $state<TelegramUpstream[]>([]);
	let quickEgressSaving = $state(false);

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

	let overallStatusState = $derived.by<'running' | 'partial' | 'stopped'>(() => {
		const mtOnline = Boolean(status?.direct_online);
		const webOnline = Boolean(status?.webproxy_online);
		const cfgEnabled = config ? Boolean(config.enabled) : Boolean(status?.running);
		if (!cfgEnabled) {
			if (!status?.running && !mtOnline && !webOnline) return 'stopped';
			return 'partial';
		}
		if (!status?.running && !mtOnline && !webOnline) return 'stopped';
		if (expectsMt && expectsWeb) {
			if (mtOnline && webOnline) return 'running';
			if (mtOnline || webOnline) return 'partial';
			return 'stopped';
		}
		if (expectsMt) return mtOnline ? 'running' : 'stopped';
		if (expectsWeb) return webOnline ? 'running' : 'stopped';
		return status?.running ? 'running' : 'stopped';
	});

	let partialStatusReason = $derived.by(() => {
		if (overallStatusState !== 'partial') return '';
		if (!config?.enabled && (status?.running || status?.direct_online || status?.webproxy_online)) {
			return 'выключён в настройках, но процесс ещё активен';
		}
		if (expectsMt && !status?.direct_online) {
			return 'недоступен прямой прокси (MTProto)';
		}
		if (expectsWeb && !status?.webproxy_online) {
			return 'недоступна веб-ссылка / мост';
		}
		return 'один из способов подключения недоступен';
	});

	let configEnabledText = $derived(config ? (config.enabled ? 'Включена' : 'Выключена') : '...');
	let processStateText = $derived.by(() => {
		if (status?.running) return 'Запущен';
		if (status?.direct_online || status?.webproxy_online) return 'Запущен частично';
		return 'Остановлен';
	});
	let reachabilityStateText = $derived.by(() => {
		if (checkLoading) return 'Проверяется...';
		if (lastCheckError) return 'Ошибка проверки';
		if (!lastCheckReport) return 'Не проверена';
		if (lastCheckReport.overall_status === 'ok') return 'Проверена (ОК)';
		if (lastCheckReport.overall_status === 'warning') return 'Есть замечания';
		return 'Недоступен / ошибка';
	});

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

	// Tabs for reveal modal
	let shareTab = $state<'webproxy' | 'mtproxy'>('mtproxy');
	let selectedWebProxyId = $state<string>('');
	let selectedRevealUserId = $state<string>('');

	// Reveal modal state
	let revealOpen = $state(false);
	let revealLoading = $state(false);
	let revealData = $state<TgWebProxyRevealData | null>(null);
	let revealQrDataUrl = $state('');
	let qrGenerating = $state(false);
	let revealSecretInput = $state('');
	let revealSecretSaving = $state(false);
	// Reveal returns a single user and the backend allows one disclosure of a
	// given secret per 5 s. Responses are reused for that window only — also
	// across close/reopen of the modal and user switching — so the UI never
	// asks again for a secret it has just received. The TTL is counted from
	// receipt (never refreshed on reuse), which is not earlier than the
	// server-side timestamp, so an expired entry is always re-fetchable.
	const REVEAL_REUSE_MS = 5000;
	const revealCache = new Map<string, { data: TgWebProxyRevealData; at: number }>();

	// Any user, secret or config change can alter what a cached reveal shows
	// (secret, links, enabled flag), so every successful mutation drops it.
	function invalidateRevealCache() {
		revealCache.clear();
	}

	function rememberReveal(data: TgWebProxyRevealData | null) {
		if (data?.user_id) revealCache.set(data.user_id, { data, at: Date.now() });
	}

	function cachedReveal(userId: string): TgWebProxyRevealData | null {
		const hit = revealCache.get(userId);
		if (!hit) return null;
		if (Date.now() - hit.at >= REVEAL_REUSE_MS) {
			revealCache.delete(userId);
			return null;
		}
		return hit.data;
	}

	// Multi-user management state
	let showAddUserModal = $state(false);
	let newUserRemark = $state('');
	let newUserSecret = $state('');
	let addUserLoading = $state(false);
	let userActionLoadingId = $state<string>('');
	let deleteUserTarget = $state<TelegramUser | null>(null);
	let deleteUserLoading = $state(false);

	let tgUsers = $derived<TelegramUser[]>(status?.users || config?.users || []);

	// Settings modal
	let settingsOpen = $state(false);
	let settingsTab = $state<'general' | 'security' | 'diagnostics'>('general');
	let editConfig = $state<Partial<TgWebProxyConfig>>({});
	let editUpstreams = $state<TelegramUpstream[]>([]);
	let saveConfigLoading = $state(false);
	let resetConfirmOpen = $state(false);
	let resetDeleteKeenDNS = $state(false);
	let resetLoading = $state(false);
	let egressOptions = $state<{ value: string; label: string; available: boolean }[]>([
		{ value: 'direct', label: EXTERNAL_ACCESS_LABELS.egressDirect, available: true },
	]);

	function buildEditableUpstreams(cfg?: Partial<TgWebProxyConfig> | null): TelegramUpstream[] {
		const list = cfg?.upstreams;
		if (list && list.length > 0) {
			return list.map((u, idx) => ({
				id: u.id || `up-${idx + 1}`,
				type: u.type === 'socks5' ? 'socks5' : 'direct',
				device: u.device || 'direct',
				address: u.address || '',
				username: u.username || '',
				password: '',
				password_set: Boolean(u.password_set),
				weight: u.weight && u.weight > 0 ? u.weight : 1,
				enabled: Boolean(u.enabled),
			}));
		}
		return [
			{
				id: 'up-1',
				type: 'direct',
				device: (cfg?.upstream_device || 'direct').trim() || 'direct',
				weight: 1,
				enabled: true,
			},
		];
	}

	function validateUpstreamsDraft(list: TelegramUpstream[]): 'empty' | 'socks' | null {
		const enabled = list.filter((u) => u.enabled);
		if (enabled.length === 0) return 'empty';
		for (const u of enabled) {
			if (u.type === 'socks5') {
				const addr = (u.address || '').trim();
				const sep = addr.lastIndexOf(':');
				const host = sep > 0 ? addr.slice(0, sep).trim() : '';
				const portNum = sep > 0 ? Number(addr.slice(sep + 1)) : NaN;
				if (!host || !Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
					return 'socks';
				}
			}
		}
		return null;
	}

	function primaryDirectDevice(list: TelegramUpstream[]): string {
		const enabled = list.filter((u) => u.enabled);
		if (enabled.length === 0 || enabled[0].type !== 'direct') return '';
		return (enabled[0].device || '').trim() || 'direct';
	}

	function summarizeUpstream(u: TelegramUpstream): string {
		if (u.type === 'socks5') {
			return m.tg_upstreams_summary_socks({ addr: (u.address || '').trim() || 'SOCKS5' });
		}
		return formatTelegramEgressLabel(u.device || 'direct', egressOptions);
	}

	let egressSummaryLabel = $derived.by(() => {
		const list = config?.upstreams;
		if (list && list.length > 0) {
			const enabled = list.filter((u) => u.enabled);
			const active = enabled.length > 0 ? enabled : list;
			if (active.length === 1) {
				return summarizeUpstream(active[0]);
			}
			return m.tg_upstreams_summary_multi({
				primary: summarizeUpstream(active[0]),
				extra: active.length - 1,
			});
		}
		return formatTelegramEgressLabel(config?.upstream_device, egressOptions);
	});

	let shortEgressSummary = $derived.by(() => {
		const list = config?.upstreams;
		if (list && list.length > 0) {
			const enabled = list.filter((u) => u.enabled);
			const active = enabled.length > 0 ? enabled : list;
			const first = active[0];
			const firstShort =
				first.type === 'socks5'
					? summarizeUpstream(first)
					: formatShortEgressSummary(summarizeUpstream(first));
			if (active.length > 1) {
				return m.tg_upstreams_summary_multi({
					primary: firstShort,
					extra: active.length - 1,
				});
			}
			return firstShort;
		}
		return formatShortEgressSummary(egressSummaryLabel);
	});

	let currentEgressUnavailable = $derived.by(() => {
		const list = config?.upstreams;
		if (list && list.length > 0) {
			const enabled = list.filter((u) => u.enabled);
			if (enabled.length === 0) return true;
			return enabled.every((u) => {
				if (u.type === 'socks5') return !(u.address || '').trim();
				const dev = (u.device || 'direct').trim();
				if (!dev || dev === 'direct') return false;
				const found = egressOptions.find((o) => o.value === dev);
				return Boolean(found && !found.available);
			});
		}
		const dev = (config?.upstream_device || 'direct').trim();
		if (!dev || dev === 'direct') return false;
		const found = egressOptions.find((o) => o.value === dev);
		return Boolean(found && !found.available);
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

	function resolveEgressIface(opt: EgressOption): string {
		if (opt.id === 'direct' || opt.kind === 'direct') return 'direct';
		if (opt.iface) return opt.iface;
		const match = /\(([^()]+)\)\s*$/.exec(opt.name || '');
		if (match && match[1]) return match[1].trim();
		return opt.id;
	}

	async function loadEgressOptions(currentUpstream?: string) {
		try {
			const caps = await api.getWizardCapabilities('tgwebproxy');
			const seen = new Set<string>();
			const list: { value: string; label: string; available: boolean }[] = [];
			for (const opt of caps.egress_options || []) {
				if (opt.kind === 'socks' || opt.id === 'firc:router') continue;
				const val = resolveEgressIface(opt);
				if (!val || seen.has(val)) continue;
				seen.add(val);
				const cleanName = cleanTunnelDisplayName(opt.name);
				list.push({
					value: val,
					label: opt.available ? cleanName : `${cleanName} (недоступен)`,
					available: opt.available,
				});
			}
			if (list.length === 0) {
				list.push({ value: 'direct', label: EXTERNAL_ACCESS_LABELS.egressDirect, available: true });
				seen.add('direct');
			}
			const cur = (currentUpstream || '').trim();
			if (cur && !seen.has(cur)) {
				list.push({ value: cur, label: `${cur} (текущий интерфейс)`, available: false });
			}
			egressOptions = list;
		} catch {
			const cur = (currentUpstream || '').trim();
			const fallback: { value: string; label: string; available: boolean }[] = [
				{ value: 'direct', label: EXTERNAL_ACCESS_LABELS.egressDirect, available: true },
			];
			if (cur && cur !== 'direct') {
				fallback.push({ value: cur, label: `${cur} (текущий интерфейс)`, available: true });
			}
			egressOptions = fallback;
		}
	}

	// Confirmation modals
	let rotateConfirmOpen = $state(false);
	let rotateSecretLoading = $state(false);

	let revokeConfirmOpen = $state(false);
	let revokeLoading = $state(false);

	let clearCacheConfirmOpen = $state(false);
	let clearCacheLoading = $state(false);

	let wizardOpen = $state(false);

	interface PresetMeta {
		id: TgNetworkPreset;
		label: string;
		badge: string;
		badgeClass: string;
		is_cdn: boolean;
		carrier_mode: string;
		subtitle: string;
		specs: string;
	}

	const WEBPROXY_PRESETS: PresetMeta[] = [
		{
			id: 'cdn_keendns',
			label: 'Внешний сервис + KeenDNS',
			badge: 'Домен + KeenDNS',
			badgeClass: 'badge-success',
			is_cdn: true,
			carrier_mode: 'https-lanes',
			subtitle: 'Через внешний сервис поверх облачного домена KeenDNS',
			specs: 'https-lanes · батч 2 МБ · буфер 32 МБ',
		},
		{
			id: 'cdn_static',
			label: 'Внешний сервис + белый IP',
			badge: 'Домен + белый IP',
			badgeClass: 'badge-info',
			is_cdn: true,
			carrier_mode: 'https-lanes',
			subtitle: 'Через внешний сервис к публичному IP роутера',
			specs: 'https-lanes · батч 512 КБ · буфер 64 МБ',
		},
		{
			id: 'keendns',
			label: 'KeenDNS напрямую',
			badge: 'KeenDNS',
			badgeClass: 'badge-purple',
			is_cdn: false,
			carrier_mode: 'websocket',
			subtitle: 'Прямой облачный домен KeenDNS',
			specs: 'websocket · батч 1 МБ · буфер 32 МБ',
		},
		{
			id: 'static_ip',
			label: 'Публичный адрес напрямую',
			badge: 'Прямой адрес',
			badgeClass: 'badge-neutral',
			is_cdn: false,
			carrier_mode: 'websocket-lanes',
			subtitle: 'Прямой домен на публичном IP роутера',
			specs: 'websocket-lanes · батч 2 МБ · буфер 64 МБ',
		},
	];

	function isKeenCloudDomain(host?: string): boolean {
		const h = (host || '').toLowerCase();
		return (
			h.includes('.netcraze.club') ||
			h.includes('.keenetic.pro') ||
			h.includes('.keenetic.link') ||
			h.includes('.keenetic.name')
		);
	}

	function inferEndpointPreset(
		ep: { hostname?: string; is_cdn?: boolean; preset?: string },
		cfg?: Partial<TgWebProxyConfig> | null
	): TgNetworkPreset {
		const p = (ep.preset || '').toLowerCase();
		if (p === 'cdn_keendns' || p === 'cdn_static' || p === 'keendns' || p === 'static_ip') {
			return p;
		}
		const np = (cfg?.network_preset || '').toLowerCase();
		if (np === 'cdn_keendns' || np === 'keendns') {
			return ep.is_cdn ? 'cdn_keendns' : 'keendns';
		}
		if (np === 'cdn_static' || np === 'static_ip') {
			return ep.is_cdn ? 'cdn_static' : 'static_ip';
		}
		const hasKeenCloud =
			isKeenCloudDomain(ep.hostname) ||
			isKeenCloudDomain(cfg?.direct_host) ||
			isKeenCloudDomain(cfg?.public_hostname) ||
			(cfg?.endpoints || []).some((other) => isKeenCloudDomain(other.hostname));
		if (ep.is_cdn) {
			return hasKeenCloud ? 'cdn_keendns' : 'cdn_static';
		}
		return hasKeenCloud ? 'keendns' : 'static_ip';
	}

	function getPresetMeta(presetId?: string): PresetMeta {
		return WEBPROXY_PRESETS.find((p) => p.id === presetId) || WEBPROXY_PRESETS[0];
	}

	function applyEndpointPreset(ep: WebProxyEndpoint, presetId: TgNetworkPreset, idx: number) {
		const meta = getPresetMeta(presetId);
		ep.preset = meta.id;
		ep.is_cdn = meta.is_cdn;
		ep.carrier_mode = meta.carrier_mode;
		if (idx === 0) {
			editConfig.network_preset = meta.id;
			editConfig.carrier_mode = meta.carrier_mode;
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
		const interval = setInterval(loadStatus, 5000);
		return () => clearInterval(interval);
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
			notifications.error(e instanceof Error ? e.message : 'Ошибка загрузки Telegram Proxy');
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

	async function runQuickCheck() {
		checkLoading = true;
		lastCheckError = '';
		try {
			const res = await api.verifyWizard('tgwebproxy');
			lastCheckReport = res;
			recordServerVerification('tgwebproxy', res);
		} catch (e) {
			lastCheckError = e instanceof Error ? e.message : 'Не удалось выполнить проверку';
		} finally {
			checkLoading = false;
		}
	}

	function openEgressModal() {
		quickUpstreams = buildEditableUpstreams(config);
		void loadEgressOptions(config?.upstream_device);
		egressModalOpen = true;
	}

	async function saveQuickEgress() {
		if (!config) return;
		const vErr = validateUpstreamsDraft(quickUpstreams);
		if (vErr === 'empty') {
			notifications.error(m.tg_upstreams_validation_empty());
			return;
		}
		if (vErr === 'socks') {
			notifications.error(m.tg_upstreams_validation_socks());
			return;
		}
		quickEgressSaving = true;
		try {
			const cleanUpstreams = quickUpstreams.map((u) => ({
				...u,
				device: u.type === 'direct' ? (u.device || '').trim() || 'direct' : '',
				address: u.type === 'socks5' ? (u.address || '').trim() : '',
				username: u.type === 'socks5' ? (u.username || '').trim() : '',
				password: u.type === 'socks5' ? u.password || '' : '',
				weight: u.weight && u.weight >= 1 ? Math.floor(u.weight) : 1,
			}));
			config = await api.updateTgWebProxyConfig({
				...config,
				upstreams: cleanUpstreams,
				upstream_device: primaryDirectDevice(cleanUpstreams),
			});
			invalidateRevealCache();
			notifications.success('Маршрут интернета для Telegram обновлён');
			egressModalOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения маршрута');
		} finally {
			quickEgressSaving = false;
		}
	}

	function syncRevealSecretInput() {
		revealSecretInput = getActiveUserReveal()?.secret || '';
	}

	async function openRevealModal(
		preferredTab?: 'mtproxy' | 'webproxy',
		targetId?: string,
		userId?: string
	) {
		if (revealLoading) return;
		revealLoading = true;
		revealOpen = true;
		if (preferredTab) {
			shareTab = preferredTab;
		} else {
			shareTab = expectsMt ? 'mtproxy' : 'webproxy';
		}
		// A remembered selection is only used while that user still exists; no
		// 'default' fallback: with zero users the backend answers an empty
		// reveal (200) instead of 404 for a non-existent "default" user.
		const rememberedUserId = tgUsers.some((u) => u.id === selectedRevealUserId) ? selectedRevealUserId : '';
		const targetUserId = userId || rememberedUserId || tgUsers[0]?.id || '';
		selectedRevealUserId = targetUserId;
		try {
			const cached = targetUserId ? cachedReveal(targetUserId) : null;
			revealData = cached ?? (await api.revealTgWebProxySecret(targetUserId || undefined));
			if (revealData?.user_id) {
				selectedRevealUserId = revealData.user_id;
			}
			if (!cached) rememberReveal(revealData);
			syncRevealSecretInput();
			if (targetId) {
				selectedWebProxyId = targetId;
			} else if (!selectedWebProxyId && revealData?.web_proxies && revealData.web_proxies.length > 0) {
				selectedWebProxyId = revealData.web_proxies[0].id;
			}
			await updateRevealQR();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка получения ссылок подключения');
			closeRevealModal();
		} finally {
			revealLoading = false;
		}
	}

	function closeRevealModal() {
		revealOpen = false;
		revealData = null;
		revealQrDataUrl = '';
		revealSecretInput = '';
		selectedRevealUserId = '';
	}

	function getActiveUserReveal(): TgUserRevealData | TgWebProxyRevealData | null {
		if (!revealData) return null;
		if (revealData.users && revealData.users.length > 0) {
			if (selectedRevealUserId) {
				const found = revealData.users.find((u) => u.id === selectedRevealUserId);
				if (found) return found;
			}
			return revealData.users[0];
		}
		return revealData;
	}

	function selectRevealUser(userId: string) {
		if (userId === revealData?.user_id) return;
		void openRevealModal(shareTab, selectedWebProxyId || undefined, userId);
	}

	function getActiveWebProxyLink(): WebProxyLink | null {
		const activeUser = getActiveUserReveal();
		const proxies = activeUser?.web_proxies || revealData?.web_proxies;
		if (!proxies || proxies.length === 0) return null;
		if (selectedWebProxyId) {
			const found = proxies.find((p) => p.id === selectedWebProxyId);
			if (found) return found;
		}
		return proxies[0];
	}

	async function updateRevealQR() {
		const activeUser = getActiveUserReveal();
		if (!activeUser) return;
		let url = '';
		if (shareTab === 'webproxy') {
			const activeEp = getActiveWebProxyLink();
			url = activeEp ? activeEp.tg_url || activeEp.bridge_url : activeUser.tg_url || activeUser.bridge_url;
		} else {
			url = activeUser.mtproxy_url;
		}
		if (!url) {
			revealQrDataUrl = '';
			return;
		}
		if (!revealQrDataUrl) {
			qrGenerating = true;
		}
		try {
			revealQrDataUrl = await renderCrispQrDataUrl(url);
		} catch {
			revealQrDataUrl = '';
		} finally {
			qrGenerating = false;
		}
	}

	function selectRevealEndpoint(epId: string) {
		selectedWebProxyId = epId;
		void updateRevealQR();
	}

	function setShareTab(tab: 'webproxy' | 'mtproxy') {
		shareTab = tab;
		if (revealOpen && revealData) {
			void updateRevealQR();
		}
	}

	function isValidSecretInput(raw: string): boolean {
		const s = raw.trim().toLowerCase();
		if (!/^[0-9a-f]+$/.test(s)) return false;
		if (s.length === 32) return true;
		if (s.length === 34 && s.startsWith('dd')) return true;
		return s.length > 34 && s.startsWith('ee') && s.length % 2 === 0;
	}

	function getActiveRevealUserId(): string {
		if (selectedRevealUserId) return selectedRevealUserId;
		const active = getActiveUserReveal();
		if (active && 'id' in active && active.id) return active.id;
		if (active && 'user_id' in active && active.user_id) return active.user_id;
		return tgUsers[0]?.id || '';
	}

	async function handleSaveUserSecret() {
		const targetId = getActiveRevealUserId();
		if (!targetId) return;
		const cleaned = revealSecretInput.trim();
		if (!isValidSecretInput(cleaned)) {
			notifications.error(m.tg_user_secret_error());
			return;
		}
		revealSecretSaving = true;
		try {
			status = await api.tgWebProxyAction('update_user', {
				user_id: targetId,
				secret: cleaned,
			});
			invalidateRevealCache();
			revealData = await api.revealTgWebProxySecret(targetId);
			rememberReveal(revealData);
			syncRevealSecretInput();
			await updateRevealQR();
			notifications.success(m.tg_user_secret_saved());
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.tg_user_secret_error());
		} finally {
			revealSecretSaving = false;
		}
	}

	async function handleRegenerateUserSecret() {
		const targetId = getActiveRevealUserId();
		if (!targetId) return;
		revealSecretSaving = true;
		try {
			status = await api.tgWebProxyAction('update_user', {
				user_id: targetId,
				regenerate_secret: true,
			});
			invalidateRevealCache();
			revealData = await api.revealTgWebProxySecret(targetId);
			rememberReveal(revealData);
			syncRevealSecretInput();
			await updateRevealQR();
			notifications.success(m.tg_user_secret_regenerated());
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.tg_user_secret_error());
		} finally {
			revealSecretSaving = false;
		}
	}

	async function handleAddUser() {
		const trimmedSec = newUserSecret.trim();
		if (trimmedSec && !isValidSecretInput(trimmedSec)) {
			notifications.error(m.tg_user_secret_error());
			return;
		}
		addUserLoading = true;
		try {
			status = await api.tgWebProxyAction('create_user', {
				remark: newUserRemark.trim() || `Пользователь ${tgUsers.length + 1}`,
				...(trimmedSec ? { secret: trimmedSec } : {}),
			});
			invalidateRevealCache();
			newUserRemark = '';
			newUserSecret = '';
			showAddUserModal = false;
			notifications.success('Пользователь Telegram добавлен');
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка создания пользователя');
		} finally {
			addUserLoading = false;
		}
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
				nextEnabled ? `Доступ для «${user.remark}» включён` : `Доступ для «${user.remark}» отключён`
			);
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка изменения статуса пользователя');
		} finally {
			userActionLoadingId = '';
		}
	}

	function openDeleteUserConfirm(user: TelegramUser) {
		deleteUserTarget = user;
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
			notifications.success(`Пользователь «${target.remark}» удалён`);
			await loadData();
			onRefreshParent?.();
			if (revealOpen && selectedRevealUserId === target.id) {
				if (tgUsers.length > 0) {
					selectedRevealUserId = tgUsers[0].id;
					revealData = await api.revealTgWebProxySecret(selectedRevealUserId);
					rememberReveal(revealData);
					syncRevealSecretInput();
					await updateRevealQR();
				} else {
					closeRevealModal();
				}
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка удаления пользователя');
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
			notifications.success(status?.running ? 'Прокси Telegram запущен' : 'Прокси Telegram остановлен');
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка управления сервисом');
		} finally {
			actionLoading = false;
		}
	}

	async function restartServer() {
		actionLoading = true;
		try {
			status = await api.tgWebProxyAction('restart');
			invalidateRevealCache();
			notifications.success('Прокси Telegram перезапущен');
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка перезапуска');
		} finally {
			actionLoading = false;
		}
	}

	function openSettingsModal(tab: 'general' | 'security' | 'diagnostics' = 'general') {
		settingsTab = tab;
		editConfig = config ? JSON.parse(JSON.stringify(config)) : {};
		if (!editConfig.endpoints || editConfig.endpoints.length === 0) {
			const isCDN =
				editConfig.scenario === 'cdn_http' ||
				editConfig.scenario === 'dual' ||
				(editConfig.public_hostname || '').includes('edge') ||
				(editConfig.public_hostname || '').includes('cdn');
			const ep: WebProxyEndpoint = {
				id: 'ep-1',
				hostname: editConfig.public_hostname || '',
				listen_port: 8084,
				admin_port: 8086,
				description: 'Основной домен',
				is_cdn: isCDN,
				enabled: true,
			};
			ep.preset = inferEndpointPreset(ep, editConfig);
			ep.carrier_mode = editConfig.carrier_mode || getPresetMeta(ep.preset).carrier_mode;
			editConfig.endpoints = [ep];
		} else {
			editConfig.endpoints = editConfig.endpoints.map((ep) => {
				const preset = inferEndpointPreset(ep, editConfig);
				const meta = getPresetMeta(preset);
				return {
					...ep,
					preset,
					is_cdn: meta.is_cdn,
					carrier_mode: ep.carrier_mode || meta.carrier_mode,
				};
			});
		}
		if (!editConfig.network_preset && editConfig.endpoints && editConfig.endpoints.length > 0) {
			editConfig.network_preset =
				editConfig.endpoints[0].preset || inferEndpointPreset(editConfig.endpoints[0], editConfig);
		}
		if (!editConfig.upstream_device) {
			editConfig.upstream_device = 'direct';
		}
		editUpstreams = buildEditableUpstreams(editConfig);
		void loadEgressOptions(editConfig.upstream_device);
		settingsOpen = true;
	}

	function addEndpoint() {
		if (!editConfig.endpoints) editConfig.endpoints = [];
		const nextIdx = editConfig.endpoints.length;
		const nextListenPort = nextIdx === 0 ? 8084 : 18084 + (nextIdx - 1) * 3;
		const nextAdminPort = nextIdx === 0 ? 8086 : 18086 + (nextIdx - 1) * 3;
		const isCDN = editConfig.scenario === 'cdn_http' || editConfig.scenario === 'dual';
		const defaultPreset = inferEndpointPreset({ is_cdn: isCDN }, editConfig);
		const meta = getPresetMeta(defaultPreset);
		editConfig.endpoints.push({
			id: `ep-${Date.now().toString(36)}`,
			hostname: '',
			listen_port: nextListenPort,
			admin_port: nextAdminPort,
			description: '',
			preset: meta.id,
			carrier_mode: meta.carrier_mode,
			is_cdn: meta.is_cdn,
			enabled: true,
		});
	}

	function removeEndpoint(idx: number) {
		if (editConfig.endpoints && editConfig.endpoints.length > 1) {
			editConfig.endpoints.splice(idx, 1);
		}
	}

	async function saveSettings() {
		const vErr = validateUpstreamsDraft(editUpstreams);
		if (vErr === 'empty') {
			notifications.error(m.tg_upstreams_validation_empty());
			return;
		}
		if (vErr === 'socks') {
			notifications.error(m.tg_upstreams_validation_socks());
			return;
		}
		saveConfigLoading = true;
		try {
			const cleanUpstreams = editUpstreams.map((u) => ({
				...u,
				device: u.type === 'direct' ? (u.device || '').trim() || 'direct' : '',
				address: u.type === 'socks5' ? (u.address || '').trim() : '',
				username: u.type === 'socks5' ? (u.username || '').trim() : '',
				password: u.type === 'socks5' ? u.password || '' : '',
				weight: u.weight && u.weight >= 1 ? Math.floor(u.weight) : 1,
			}));
			editConfig.upstreams = cleanUpstreams;
			editConfig.upstream_device = primaryDirectDevice(cleanUpstreams);
			if (editConfig.endpoints && editConfig.endpoints.length > 0) {
				editConfig.endpoints = editConfig.endpoints.map((ep) => {
					const p = ep.preset || inferEndpointPreset(ep, editConfig);
					const meta = getPresetMeta(p);
					return {
						...ep,
						preset: p,
						is_cdn: meta.is_cdn,
						carrier_mode: ep.carrier_mode || meta.carrier_mode,
					};
				});
				editConfig.public_hostname = editConfig.endpoints[0].hostname;
				const ep0Preset =
					editConfig.endpoints[0].preset || inferEndpointPreset(editConfig.endpoints[0], editConfig);
				editConfig.network_preset = ep0Preset;
				editConfig.carrier_mode =
					editConfig.endpoints[0].carrier_mode || getPresetMeta(ep0Preset).carrier_mode;
			}
			config = await api.updateTgWebProxyConfig(editConfig);
			invalidateRevealCache();
			notifications.success('Настройки сохранены');
			settingsOpen = false;
			await loadData();
			onRefreshParent?.();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения настроек');
		} finally {
			saveConfigLoading = false;
		}
	}

	async function executeRotateSecret() {
		rotateSecretLoading = true;
		try {
			status = await api.tgWebProxyAction('rotate_secret');
			invalidateRevealCache();
			rotateConfirmOpen = false;
			notifications.success('Новый ключ сгенерирован. Старый ключ активен в течение 7 дней.');
			await loadData();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка ротации ключа');
		} finally {
			rotateSecretLoading = false;
		}
	}

	async function executeRevokeLegacy() {
		revokeLoading = true;
		try {
			status = await api.tgWebProxyAction('revoke_legacy');
			invalidateRevealCache();
			revokeConfirmOpen = false;
			notifications.success('Старый ключ успешно отозван.');
			await loadData();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка отзыва ключа');
		} finally {
			revokeLoading = false;
		}
	}

	async function executeClearScannerCache() {
		clearCacheLoading = true;
		try {
			status = await api.tgWebProxyAction('clear_scanner_cache');
			invalidateRevealCache();
			clearCacheConfirmOpen = false;
			notifications.success('Кэш сетевых сканеров очищен.');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка очистки кэша');
		} finally {
			clearCacheLoading = false;
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
			invalidateRevealCache();
			lastCheckReport = null;
			lastCheckError = '';
			clearServerVerification('tgwebproxy');
			revealData = null;
			revealQrDataUrl = '';
			revealOpen = false;
			resetConfirmOpen = false;
			settingsOpen = false;
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

	function formatDate(isoStr?: string): string {
		if (!isoStr) return '';
		try {
			const d = new Date(isoStr);
			return d.toLocaleString('ru-RU', {
				day: 'numeric',
				month: 'short',
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return isoStr;
		}
	}

	function cleanEndpointDescription(desc?: string): string {
		if (!desc) return '';
		return desc
			.replace(/^Primary\s+Web\s+Proxy$/i, 'Основной веб-прокси')
			.replace(/Selectel\s*CDN\s*\+\s*Static\s*IP/gi, 'Внешний сервис + белый IP')
			.replace(/Selectel\s*CDN/gi, 'Внешний сервис')
			.replace(/KeenDNS\s*Static\s*IP/gi, 'KeenDNS')
			.replace(/\bSelectel\b/gi, 'Внешний сервис')
			.replace(/\bCDN\b/gi, 'Внешний сервис');
	}

	async function copy(text: string, label: string) {
		if (await copyToClipboard(text)) {
			notifications.success(`${label} скопирован`);
		}
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
					<span class="badge badge-neutral">Загрузка...</span>
				{:else if !isConfigured && !loading}
					<span class="badge badge-neutral">Не настроен</span>
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
				<Button variant="secondary" size="sm" onclick={() => openSettingsModal('general')}>
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
					title="Перезапустить прокси Telegram"
					onclick={restartServer}
					disabled={actionLoading}
				>
					<RotateCw size={15} class={actionLoading ? 'spin' : ''} />
				</button>
			</div>
		{/if}
	</header>

	{#if !isConfigured && !loading}
		<div class="unconfigured-card">
			<p class="unconfigured-desc">
				Настройте собственный прокси для приложения Telegram, чтобы сообщения, звонки и медиа работали через ваш роутер без включения полного VPN.
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
		<section class="summary-strip" aria-label="Сводка настроек Telegram">
			<div class="summary-line">
				<div class="summary-left">
					<Compass size={15} class="summary-icon" />
					<span class="summary-text" class:warn-text={currentEgressUnavailable}>
						{shortEgressSummary}
					</span>
					{#if currentEgressUnavailable}
						<span class="inline-warn-badge">Туннель недоступен</span>
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
					<span class="summary-text muted">
						Настройка: <strong>{configEnabledText}</strong> · Процесс: <strong>{processStateText}</strong> · Доступность: <strong>{reachabilityStateText}</strong>
					</span>
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

		<!-- 3. Available Connection Methods Block -->
		<section class="methods-grid">
			{#if expectsMt}
				<div class="method-card">
					<div class="method-top">
						<div class="method-title-wrap">
							<Smartphone size={15} class="method-icon" />
							<span class="method-title">{EXTERNAL_ACCESS_LABELS.telegramMtproto}</span>
						</div>
						<span class="method-status" class:online={status?.direct_online}>
							{status?.direct_online ? 'Готов к подключению' : 'Выключен'}
						</span>
					</div>
					<p class="method-desc">
						Для обычного приложения Telegram на телефоне и компьютере (подключение напрямую к адресу роутера).
					</p>
					<div class="method-footer">
						<span class="method-addr font-mono">
							{config?.direct_host ? `${config.direct_host}:${config?.direct_port || 8443}` : 'Адрес не указан'}
						</span>
					</div>
				</div>
			{/if}

			{#if expectsWeb}
				<div class="method-card">
					<div class="method-top">
						<div class="method-title-wrap">
							<Globe size={15} class="method-icon" />
							<span class="method-title">{EXTERNAL_ACCESS_LABELS.telegramWeb}</span>
						</div>
						<span class="method-status" class:online={status?.webproxy_online}>
							{status?.webproxy_online ? 'Готов к подключению' : 'Выключен'}
						</span>
					</div>
					<p class="method-desc">
						Веб-мост через домен (требует поддержки веб-ссылки или совместимого клиента; не заменяет стандартный MTProto во всех приложениях).
					</p>
					{#if status?.web_proxies && status.web_proxies.length > 0}
						<div class="method-domains">
							{#each status.web_proxies as wp (wp.id)}
								<div class="domain-row">
									<div class="domain-info">
										<span class="domain-host font-mono">{wp.hostname}</span>
										{#if wp.description}
											<span class="domain-desc">{cleanEndpointDescription(wp.description)}</span>
										{/if}
									</div>
								</div>
							{/each}
						</div>
					{:else}
						<div class="method-footer">
							<span class="method-addr font-mono">
								{formatDomainList(config?.public_hostname) || 'Домен не указан'}
							</span>
						</div>
					{/if}
				</div>
			{/if}
		</section>

		<!-- 4. Users / Devices Section -->
		<section class="tg-users-section">
			<div class="tg-users-header">
				<div class="tg-users-title-wrap">
					<h3 class="tg-users-title">Пользователи</h3>
					<span class="tg-users-count">({tgUsers.length})</span>
				</div>
				<Button variant="secondary" size="sm" onclick={() => (showAddUserModal = true)}>
					<Plus size={14} class="mr-1" />
					Добавить пользователя
				</Button>
			</div>

			{#if tgUsers.length > 0}
				<div class="tg-users-list">
					{#each tgUsers as user (user.id)}
						<div class="tg-user-row">
							<div class="tg-user-left">
								<Toggle
									checked={user.enabled}
									disabled={userActionLoadingId === user.id}
									onchange={() => handleToggleUser(user)}
								/>
								<span class="tg-user-name" class:muted-name={!user.enabled}>{user.remark}</span>
								<span class="badge {user.enabled ? 'badge-success' : 'badge-neutral'}">
									{user.enabled ? 'Доступ разрешён' : 'Выключен'}
								</span>
							</div>
							<div class="tg-user-actions">
								<Button
									variant="primary"
									size="sm"
									onclick={() => openRevealModal(undefined, undefined, user.id)}
								>
									<QrCode size={13} class="mr-1" />
									QR / Ссылка
								</Button>
								<button
									type="button"
									class="btn-user-delete"
									title={m.tg_user_delete_btn()}
									aria-label={m.tg_user_delete_btn()}
									disabled={userActionLoadingId === user.id}
									onclick={() => openDeleteUserConfirm(user)}
								>
									<Trash2 size={14} />
								</button>
							</div>
						</div>
					{/each}
				</div>
			{:else}
				<p class="field-hint">{m.tg_users_empty()}</p>
			{/if}
		</section>
	{/if}
</div>

<!-- Modal: Add Telegram User -->
<Modal
	bind:open={showAddUserModal}
	title="Добавить пользователя Telegram"
	onclose={() => (showAddUserModal = false)}
	size="sm"
>
	<div class="egress-modal-body">
		<p class="field-hint">
			Для нового пользователя будет создан отдельный ключ, который работает одновременно для MTProto и веб-ссылки.
		</p>
		<label class="form-field">
			<span class="field-label">Имя пользователя / устройства:</span>
			<input
				type="text"
				bind:value={newUserRemark}
				class="text-input"
				placeholder="Например: Телефон, Планшет, Брат"
				onkeydown={(e) => {
					if (e.key === 'Enter' && !addUserLoading) void handleAddUser();
				}}
			/>
		</label>
		<label class="form-field">
			<span class="field-label">{m.tg_user_secret_optional_label()}</span>
			<input
				type="text"
				bind:value={newUserSecret}
				class="text-input font-mono"
				placeholder={m.tg_user_secret_optional_placeholder()}
				onkeydown={(e) => {
					if (e.key === 'Enter' && !addUserLoading) void handleAddUser();
				}}
			/>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (showAddUserModal = false)} disabled={addUserLoading}>
			Отмена
		</Button>
		<Button variant="primary" onclick={handleAddUser} disabled={addUserLoading}>
			{addUserLoading ? 'Создание...' : 'Создать'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Delete User Confirmation -->
<Modal
	open={Boolean(deleteUserTarget)}
	title={m.tg_user_delete_confirm_title()}
	onclose={() => { if (!deleteUserLoading) deleteUserTarget = null; }}
	size="sm"
>
	{#if deleteUserTarget}
		<div class="confirm-content">
			<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
			<p class="confirm-p">
				{m.tg_user_delete_confirm_body({ name: deleteUserTarget.remark })}
			</p>
			{#if tgUsers.length <= 1}
				<p class="confirm-note">
					{m.tg_user_delete_confirm_last_note()}
				</p>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (deleteUserTarget = null)} disabled={deleteUserLoading}>
			Отмена
		</Button>
		<Button variant="danger" onclick={confirmDeleteUser} disabled={deleteUserLoading}>
			<Trash2 size={14} class="mr-1" />
			{m.tg_user_delete_confirm_action()}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Quick Egress Change -->
<Modal bind:open={egressModalOpen} title="Куда направлять интернет (Telegram)" onclose={() => (egressModalOpen = false)} size="md">
	<div class="egress-modal-body">
		<p class="field-hint">
			Выберите, через какое подключение роутера трафик Telegram будет выходить в интернет.
		</p>
		<TelegramUpstreamsEditor bind:upstreams={quickUpstreams} {egressOptions} />
		{#if currentEgressUnavailable}
			<div class="field-warn">
				Текущий выход недоступен — выберите другой или восстановите туннель.
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (egressModalOpen = false)}>Отмена</Button>
		<Button variant="primary" onclick={saveQuickEgress} disabled={quickEgressSaving}>
			{quickEgressSaving ? 'Сохранение...' : 'Сохранить'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Connect Telegram (Reveal Link & QR) -->
<Modal bind:open={revealOpen} title="Подключить Telegram" onclose={closeRevealModal} size="lg">
	{#if revealLoading}
		<div class="reveal-loading">
			<RefreshCw size={24} class="spin mb-2" />
			<span>Загрузка ссылок подключения...</span>
		</div>
	{:else if revealData}
		{@const activeUser = getActiveUserReveal()}
		<div class="reveal-content">
			{#if tgUsers.length > 0}
				<div class="reveal-user-selector">
					<label for="tg-reveal-user-select" class="ep-selector-label">Пользователь / устройство:</label>
					<select
						id="tg-reveal-user-select"
						value={revealData.user_id || selectedRevealUserId || tgUsers[0].id}
						onchange={(e) => selectRevealUser((e.currentTarget as HTMLSelectElement).value)}
						class="select-input reveal-user-select"
					>
						{#each tgUsers as u (u.id)}
							<option value={u.id}>{u.remark}{u.enabled ? '' : ' (выключен)'}</option>
						{/each}
					</select>
				</div>
			{/if}

			<div class="reveal-tabs">
				{#if expectsMt || activeUser?.mtproxy_url}
					<button
						type="button"
						class="reveal-tab"
						class:active={shareTab === 'mtproxy'}
						onclick={() => setShareTab('mtproxy')}
					>
						<Smartphone size={14} class="mr-1" />
						{EXTERNAL_ACCESS_LABELS.telegramMtproto}
					</button>
				{/if}
				{#if expectsWeb || (activeUser?.web_proxies && activeUser.web_proxies.length > 0)}
					<button
						type="button"
						class="reveal-tab"
						class:active={shareTab === 'webproxy'}
						onclick={() => setShareTab('webproxy')}
					>
						<Globe size={14} class="mr-1" />
						{EXTERNAL_ACCESS_LABELS.telegramWeb}
					</button>
				{/if}
			</div>

			<div class="reveal-body">
				<!-- QR Code Column -->
				<div class="reveal-qr-col">
					{#if qrGenerating}
						<div class="qr-placeholder">Генерация QR...</div>
					{:else if revealQrDataUrl}
						<div class="qr-wrapper">
							<img src={revealQrDataUrl} alt="QR код для Telegram" class="qr-img" />
						</div>
						<div class="qr-hint">
							{#if shareTab === 'mtproxy'}
								Отсканируйте камерой телефона для добавления прокси в приложение Telegram
							{:else}
								Отсканируйте камерой смартфона для открытия веб-ссылки
							{/if}
						</div>
					{/if}
				</div>

				<!-- Links Column -->
				<div class="reveal-links-col">
					{#if shareTab === 'webproxy'}
						{@const activeEp = getActiveWebProxyLink()}
						{@const activeTgUrl = activeEp ? activeEp.tg_url : activeUser?.tg_url || ''}
						{@const activeTmeUrl = activeEp ? activeEp.tme_url : activeUser?.tme_url || ''}
						{@const activeBridgeUrl = activeEp ? activeEp.bridge_url : activeUser?.bridge_url || ''}
						{@const activeWebProxies = activeUser?.web_proxies || revealData.web_proxies || []}

						<div class="honest-note">
							Веб-ссылка предназначена для подключения через домен или совместимый веб-мост. Для обычного приложения Telegram на телефоне и ПК используйте вкладку «Прокси для приложения Telegram (MTProto)», если прямой порт доступен.
						</div>

						{#if activeWebProxies.length > 1}
							<div class="ep-selector-box">
								<span class="ep-selector-label">Выберите домен подключения:</span>
								<div class="ep-chips">
									{#each activeWebProxies as ep (ep.id)}
										{@const epMeta = getPresetMeta(ep.preset || inferEndpointPreset(ep, config))}
										<button
											type="button"
											class="ep-chip"
											class:active={(selectedWebProxyId || activeWebProxies[0].id) === ep.id}
											onclick={() => selectRevealEndpoint(ep.id)}
										>
											<span class="ep-chip-host font-mono">{ep.hostname}</span>
											<span class="badge {epMeta.badgeClass} text-[10px] ml-1">{epMeta.badge}</span>
											{#if ep.description}
												<span class="text-muted text-[11px] ml-1">({cleanEndpointDescription(ep.description)})</span>
											{/if}
										</button>
									{/each}
								</div>
							</div>
						{/if}

						<div class="link-block">
							<div class="link-header">
								<span class="link-label">Ссылка для быстрого открытия (tg://webproxy):</span>
							</div>
							<div class="input-with-actions">
								<input type="text" readonly value={activeTgUrl} class="link-input font-mono" />
								<Button variant="primary" size="sm" onclick={() => copy(activeTgUrl, 'Ссылка')}>
									<Copy size={14} class="mr-1" />
									Копировать
								</Button>
								<a href={activeTgUrl} class="btn-open-tg">
									<Send size={14} class="mr-1" />
									Открыть
								</a>
							</div>
						</div>

						<div class="link-block">
							<div class="link-header">
								<span class="link-label">Веб-ссылка (https://t.me/webproxy):</span>
							</div>
							<div class="input-with-actions">
								<input type="text" readonly value={activeTmeUrl} class="link-input font-mono" />
								<Button variant="secondary" size="sm" onclick={() => copy(activeTmeUrl, 'Веб-ссылка')}>
									<Copy size={14} class="mr-1" />
									Копировать
								</Button>
							</div>
						</div>

						<div class="link-block">
							<div class="link-header">
								<span class="link-label">Адрес веб-моста ({formatTelegramCarrierLabel(activeEp?.carrier_mode || config?.carrier_mode)}):</span>
							</div>
							<div class="input-with-actions">
								<input type="text" readonly value={activeBridgeUrl} class="link-input font-mono" />
								<Button variant="secondary" size="sm" onclick={() => copy(activeBridgeUrl, 'Адрес моста')}>
									<Copy size={14} class="mr-1" />
									Копировать
								</Button>
							</div>
						</div>
					{:else}
						<!-- Direct MTProxy Share -->
						<div class="link-block">
							<div class="link-header">
								<span class="link-label">Прямая ссылка для приложения Telegram (в 1 клик):</span>
							</div>
							<div class="input-with-actions">
								<input type="text" readonly value={activeUser?.mtproxy_url || ''} class="link-input font-mono" />
								<Button variant="primary" size="sm" onclick={() => copy(activeUser?.mtproxy_url || '', 'Ссылка MTProxy')}>
									<Copy size={14} class="mr-1" />
									Копировать
								</Button>
								<a href={activeUser?.mtproxy_url || ''} class="btn-open-tg">
									<Send size={14} class="mr-1" />
									Открыть
								</a>
							</div>
						</div>

						<div class="link-block">
							<div class="link-header">
								<span class="link-label">Ссылка через браузер (t.me):</span>
							</div>
							<div class="input-with-actions">
								<input type="text" readonly value={activeUser?.mtproxy_tme_url || ''} class="link-input font-mono" />
								<Button variant="secondary" size="sm" onclick={() => copy(activeUser?.mtproxy_tme_url || '', 'Ссылка t.me')}>
									<Copy size={14} class="mr-1" />
									Копировать
								</Button>
							</div>
						</div>

						<div class="manual-box">
							<div class="manual-grid">
								<div class="manual-item">
									<span class="m-label">Сервер:</span>
									<div class="m-val-row">
										<code class="m-code">{revealData.direct_host}</code>
										<button type="button" class="copy-mini-btn" onclick={() => copy(revealData?.direct_host || '', 'Сервер')}>
											<Copy size={12} />
										</button>
									</div>
								</div>
								<div class="manual-item">
									<span class="m-label">Порт:</span>
									<div class="m-val-row">
										<code class="m-code">{revealData.direct_port}</code>
										<button type="button" class="copy-mini-btn" onclick={() => copy(String(revealData?.direct_port || 8443), 'Порт')}>
											<Copy size={12} />
										</button>
									</div>
								</div>
								<div class="manual-item manual-item-wide">
									<span class="m-label">{m.tg_user_secret_base_label()}</span>
									<div class="input-with-actions">
										<input
											type="text"
											aria-label={m.tg_user_secret_base_label()}
											bind:value={revealSecretInput}
											placeholder={m.tg_user_secret_placeholder()}
											class="link-input font-mono"
										/>
										<Button
											variant="secondary"
											size="sm"
											onclick={handleRegenerateUserSecret}
											disabled={revealSecretSaving}
										>
											<RefreshCw size={13} class={revealSecretSaving ? 'spin mr-1' : 'mr-1'} />
											{m.tg_user_secret_generate()}
										</Button>
										<Button
											variant="primary"
											size="sm"
											onclick={handleSaveUserSecret}
											disabled={revealSecretSaving}
										>
											{m.tg_user_secret_save()}
										</Button>
									</div>
									<span class="field-hint">{m.tg_user_secret_hint()}</span>
								</div>
								<div class="manual-item manual-item-wide">
									<span class="m-label">Секретный ключ:</span>
									<div class="m-val-row">
										<code class="m-code font-mono text-xs">{activeUser?.mtproxy_secret || revealData.mtproxy_secret}</code>
										<button type="button" class="copy-mini-btn" onclick={() => copy(activeUser?.mtproxy_secret || revealData?.mtproxy_secret || '', 'Секретный ключ')}>
											<Copy size={12} />
										</button>
									</div>
								</div>
							</div>
						</div>
					{/if}
					{#if shareTab === 'webproxy'}
						<div class="manual-box">
							<div class="manual-item manual-item-wide">
								<span class="m-label">{m.tg_user_secret_base_label()}</span>
								<div class="input-with-actions">
									<input
										type="text"
										aria-label={m.tg_user_secret_base_label()}
										bind:value={revealSecretInput}
										placeholder={m.tg_user_secret_placeholder()}
										class="link-input font-mono"
									/>
									<Button
										variant="secondary"
										size="sm"
										onclick={handleRegenerateUserSecret}
										disabled={revealSecretSaving}
									>
										<RefreshCw size={13} class={revealSecretSaving ? 'spin mr-1' : 'mr-1'} />
										{m.tg_user_secret_generate()}
									</Button>
									<Button
										variant="primary"
										size="sm"
										onclick={handleSaveUserSecret}
										disabled={revealSecretSaving}
									>
										{m.tg_user_secret_save()}
									</Button>
								</div>
								<span class="field-hint">{m.tg_user_secret_hint()}</span>
							</div>
						</div>
					{/if}
				</div>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		{@const activeUserForAction = getActiveUserReveal()}
		{#if activeUserForAction}
			{@const targetUserObj = tgUsers.find((u) => u.id === (activeUserForAction as TgUserRevealData).id) || tgUsers[0]}
			{#if targetUserObj}
				<Button
					variant="danger"
					onclick={() => openDeleteUserConfirm(targetUserObj)}
					disabled={revealSecretSaving || deleteUserLoading}
				>
					<Trash2 size={14} class="mr-1" />
					{m.tg_user_delete_btn()}
				</Button>
			{/if}
		{/if}
		<Button variant="secondary" onclick={closeRevealModal}>Закрыть</Button>
	{/snippet}
</Modal>

<!-- Modal: Rotate Secret Confirmation -->
<Modal bind:open={rotateConfirmOpen} title="Ротация ключа Telegram" onclose={() => (rotateConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-warning mb-3" />
		<p class="confirm-p">
			Будет сгенерирован новый секретный ключ.
		</p>
		<p class="confirm-note">
			<strong>Переходный период:</strong> текущий ключ останется активным в течение 7 дней, чтобы ваши устройства успели обновить настройки.
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (rotateConfirmOpen = false)} disabled={rotateSecretLoading}>Отмена</Button>
		<Button variant="primary" onclick={executeRotateSecret} disabled={rotateSecretLoading}>
			<RefreshCw size={14} class={rotateSecretLoading ? 'spin mr-1' : 'mr-1'} />
			{rotateSecretLoading ? 'Ротация...' : 'Подтвердить ротацию'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Revoke Legacy Secret Confirmation -->
<Modal bind:open={revokeConfirmOpen} title="Немедленный отзыв старого ключа" onclose={() => (revokeConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">
			Вы действительно хотите <strong>немедленно аннулировать старый ключ</strong>?
		</p>
		<p class="confirm-note">
			Все клиенты, использующие прежние ссылки, сразу потеряют доступ.
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (revokeConfirmOpen = false)} disabled={revokeLoading}>Отмена</Button>
		<Button variant="danger" onclick={executeRevokeLegacy} disabled={revokeLoading}>
			<Trash2 size={14} class="mr-1" />
			{revokeLoading ? 'Отзыв...' : 'Отозвать немедленно'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Clear Scanner Cache Confirmation -->
<Modal bind:open={clearCacheConfirmOpen} title="Очистка кэша сетевых сканеров" onclose={() => (clearCacheConfirmOpen = false)} size="sm">
	<div class="confirm-content">
		<ShieldCheck size={32} class="confirm-icon text-primary mb-3" />
		<p class="confirm-p">
			Будет очищен кэш сетевой диагностики и перезапущен процесс прокси.
		</p>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (clearCacheConfirmOpen = false)} disabled={clearCacheLoading}>Отмена</Button>
		<Button variant="primary" onclick={executeClearScannerCache} disabled={clearCacheLoading}>
			{clearCacheLoading ? 'Очистка...' : 'Очистить кэш'}
		</Button>
	{/snippet}
</Modal>

<!-- Modal: Settings & Diagnostics -->
<Modal bind:open={settingsOpen} title="Настройки и диагностика Telegram" onclose={() => (settingsOpen = false)} size="lg">
	<div class="settings-modal-wrap">
		<!-- Top wizard & dispatcher banner -->
		<div class="wizard-launch-bar">
			<div class="wizard-launch-info">
				<span class="wizard-launch-title">Пошаговая настройка подключения</span>
				<span class="wizard-launch-sub">Изменение адреса роутера, домена KeenDNS и способа подключения Telegram</span>
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
				Основные параметры
			</button>
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'security'}
				onclick={() => (settingsTab = 'security')}
			>
				Ключи и безопасность
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
				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Прямой адрес MTProxy (для телефонов):</span>
						<input type="text" bind:value={editConfig.direct_host} class="text-input" placeholder="router.example.com" />
						<span class="field-hint">Домен KeenDNS/DDNS или публичный IP роутера</span>
					</label>
					<label class="form-field">
						<span class="field-label">Порт MTProxy:</span>
						<input type="number" bind:value={editConfig.direct_port} class="text-input" placeholder="8443" />
					</label>
				</div>

				<div class="form-row">
					<label class="form-field">
						<span class="field-label">Домен маскировки TLS (SNI):</span>
						<input type="text" bind:value={editConfig.tls_domain} class="text-input" placeholder="www.google.com" />
					</label>
					<div class="form-field"></div>
				</div>

				<TelegramUpstreamsEditor bind:upstreams={editUpstreams} {egressOptions} />

				<div class="settings-section-divider"></div>
				<div class="section-title-row">
					<div>
						<h4 class="section-heading">Домены веб-подключения</h4>
						<p class="section-desc">
							Каждый домен настраивается независимо без удаления остальных.
						</p>
					</div>
					<Button variant="secondary" size="sm" onclick={addEndpoint}>
						<Plus size={14} class="mr-1" />
						Добавить домен
					</Button>
				</div>

				<div class="endpoints-list">
					{#if editConfig.endpoints && editConfig.endpoints.length > 0}
						{#each editConfig.endpoints as ep, idx (ep.id || idx)}
							{@const epPresetMeta = getPresetMeta(ep.preset || inferEndpointPreset(ep, editConfig))}
							<div class="endpoint-card">
								<div class="endpoint-card-header">
									<div class="endpoint-title-group">
										<span class="endpoint-card-title">Домен #{idx + 1} {ep.hostname ? `(${ep.hostname})` : ''}</span>
										<span class="badge {epPresetMeta.badgeClass} text-[10px]">{epPresetMeta.badge}</span>
									</div>
									<div class="endpoint-card-actions">
										<label class="cdn-checkbox-label">
											<input type="checkbox" bind:checked={ep.enabled} class="checkbox-input" />
											<span>Активен</span>
										</label>
										{#if (editConfig.endpoints?.length || 0) > 1}
											<button type="button" class="btn-icon-danger" onclick={() => removeEndpoint(idx)} title="Удалить домен">
												<Trash2 size={14} />
											</button>
										{/if}
									</div>
								</div>

								<div class="ep-preset-bar">
									<span class="ep-preset-label">Схема подключения домена:</span>
									<div class="ep-preset-pills">
										{#each WEBPROXY_PRESETS as p (p.id)}
											<button
												type="button"
												class="ep-preset-pill"
												class:active={(ep.preset || epPresetMeta.id) === p.id}
												onclick={() => applyEndpointPreset(ep, p.id, idx)}
											>
												{p.label}
											</button>
										{/each}
									</div>
									<span class="ep-preset-specs font-mono">{epPresetMeta.subtitle} — {epPresetMeta.specs}</span>
								</div>

								<div class="form-row">
									<label class="form-field flex-1">
										<span class="field-label">Домен:</span>
										<input type="text" bind:value={ep.hostname} class="text-input font-mono" placeholder="tg.example.com" />
									</label>
									<label class="form-field flex-1">
										<span class="field-label">Описание:</span>
										<input type="text" bind:value={ep.description} class="text-input" placeholder="Основной домен" />
									</label>
								</div>
							</div>
						{/each}
					{/if}
				</div>

				<div class="reset-zone">
					<div class="reset-zone-info">
						<div class="reset-zone-title">{m.server_reset_zone_title()}</div>
						<p class="reset-zone-desc">{m.server_reset_tg_zone_desc()}</p>
					</div>
					<Button variant="danger" size="sm" onclick={openResetConfirm}>
						<Trash2 size={14} class="mr-1" />
						{m.server_reset_button()}
					</Button>
				</div>
			</div>
		{:else if settingsTab === 'security'}
			<div class="security-pane">
				{#if status?.legacy_active}
					<div class="grace-alert">
						<div class="grace-alert-left">
							<AlertTriangle size={18} class="grace-icon" />
							<div class="grace-text">
								<span class="grace-title">Активен переходный период старого ключа</span>
								<span class="grace-desc">
									Старые ссылки продолжают работать до <strong>{formatDate(status.legacy_expires_at)}</strong>.
								</span>
							</div>
						</div>
						<Button variant="secondary" size="sm" onclick={() => (revokeConfirmOpen = true)}>
							<ShieldCheck size={14} class="mr-1" />
							Отозвать старый ключ сейчас
						</Button>
					</div>
				{/if}

				<div class="actions-strip">
					<div class="key-display-box">
						<KeyRound size={15} class="key-icon" />
						<span class="key-label">Секретный ключ:</span>
						<code class="key-code font-mono">{status?.secret_masked || '••••••••'}</code>
					</div>

					<div class="actions-right">
						<Button variant="secondary" size="sm" onclick={() => (rotateConfirmOpen = true)}>
							<RefreshCw size={14} class="mr-1" />
							Ротация ключа
						</Button>
						<Button variant="secondary" size="sm" onclick={() => (clearCacheConfirmOpen = true)}>
							<Trash2 size={14} class="mr-1" />
							Сбросить кэш сканеров
						</Button>
					</div>
				</div>
			</div>
		{:else if settingsTab === 'diagnostics'}
			<div class="diagnostics-pane">
				<section class="meta-strip">
					<div class="strip-item">
						<span class="strip-label">MTProto (:{config?.direct_port || 8443})</span>
						<span class="strip-val">
							<span class="health-dot" class:online={status?.direct_online}></span>
							<span class="health-text">{status?.direct_online ? `онлайн (:${config?.direct_port || 8443})` : 'выключен'}</span>
						</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">Веб-мост (:{config?.listen_port || 8085})</span>
						<span class="strip-val">
							<span class="health-dot" class:online={status?.webproxy_online}></span>
							<span class="health-text">{status?.webproxy_online ? `онлайн (:${config?.listen_port || 8085})` : 'выключен'}</span>
						</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">Ядро MTProto (:2398)</span>
						<span class="strip-val">
							<span class="health-dot" class:online={status?.raw_online}></span>
							<span class="health-text">{status?.raw_online ? 'онлайн (:2398)' : 'выключено'}</span>
						</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">Выход ({egressSummaryLabel})</span>
						<span class="strip-val">
							{#if status?.upstream_status === 'ok'}
								<span class="health-dot online"></span>
								<span class="health-text font-mono">норма</span>
							{:else if status?.upstream_status === 'degraded'}
								<span class="health-dot degraded"></span>
								<span class="health-text font-mono">деградация</span>
							{:else}
								<span class="health-dot stopped"></span>
								<span class="health-text font-mono">недоступен</span>
							{/if}
						</span>
					</div>
				</section>

				<ConnectionChecks kind="tgwebproxy" currentFingerprint={cardFingerprint} autoRun={true} />
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (settingsOpen = false)}>Закрыть</Button>
		{#if settingsTab === 'general'}
			<Button variant="primary" onclick={saveSettings} disabled={saveConfigLoading}>
				{saveConfigLoading ? 'Сохранение...' : 'Сохранить параметры'}
			</Button>
		{/if}
	{/snippet}
</Modal>

<!-- Modal: Confirm Telegram Factory Reset -->
<Modal
	bind:open={resetConfirmOpen}
	title={m.server_reset_tg_modal_title()}
	onclose={() => { if (!resetLoading) resetConfirmOpen = false; }}
	size="sm"
>
	<div class="confirm-content">
		<AlertTriangle size={32} class="confirm-icon text-danger mb-3" />
		<p class="confirm-p">{m.server_reset_tg_modal_body()}</p>
		<label class="reset-keendns-check">
			<input type="checkbox" bind:checked={resetDeleteKeenDNS} class="checkbox-input" />
			<div class="reset-keendns-text">
				<span class="reset-keendns-label">{m.server_reset_tg_keendns_label()}</span>
				<span class="reset-keendns-hint">{m.server_reset_tg_keendns_hint()}</span>
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

<ExternalAccessWizard
	bind:open={wizardOpen}
	initialKind="tgwebproxy"
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

	.icon-btn {
		width: 30px;
		height: 30px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		border-radius: var(--radius-sm);
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
		transition: color 0.15s, border-color 0.15s;
	}
	.icon-btn:hover {
		color: var(--color-text-primary);
		border-color: var(--color-accent);
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

	/* Methods Grid */
	.methods-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: 10px;
	}

	.method-card {
		padding: 11px 13px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 8px;
	}

	.method-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	.method-title-wrap {
		display: flex;
		align-items: center;
		gap: 7px;
	}

	:global(.method-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.method-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.method-status {
		font-size: 10.5px;
		padding: 1px 7px;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
	}
	.method-status.online {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.method-desc {
		margin: 0;
		font-size: 11.5px;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.method-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding-top: 6px;
		border-top: 1px solid var(--color-border);
		flex-wrap: wrap;
	}

	.method-addr {
		font-size: 12px;
		color: var(--color-text-primary);
		min-width: 0;
		flex: 1;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.35;
	}

	.method-domains {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 6px;
		border-top: 1px solid var(--color-border);
	}

	.domain-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 4px 0;
		border-bottom: 1px dashed var(--color-border);
	}
	.domain-row:last-child {
		border-bottom: none;
		padding-bottom: 0;
	}

	.domain-info {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		min-width: 0;
		flex: 1;
	}

	.domain-host {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.3;
	}

	.domain-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.3;
	}

	.domain-actions {
		flex-shrink: 0;
	}

	.honest-note {
		font-size: 11.5px;
		color: var(--color-text-secondary);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		line-height: 1.4;
	}

	.spin {
		animation: spin 1s linear infinite;
	}
	@keyframes spin {
		from { transform: rotate(0deg); }
		to { transform: rotate(360deg); }
	}

	.settings-modal-wrap,
	.egress-modal-body,
	.security-pane,
	.diagnostics-pane {
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

	.meta-strip {
		display: flex;
		align-items: center;
		gap: 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.65rem 1rem;
		flex-wrap: wrap;
	}

	.strip-item {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.strip-label {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.strip-val {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.strip-divider {
		width: 1px;
		height: 24px;
		background: var(--color-border);
	}

	.health-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}
	.health-dot.online {
		background: var(--color-success);
		box-shadow: 0 0 3px var(--color-success-border);
	}
	.health-dot.degraded {
		background: var(--color-warning);
		box-shadow: 0 0 3px var(--color-warning-border);
	}
	.health-dot.stopped {
		background: var(--color-error);
	}

	.health-text {
		font-size: 12px;
	}

	.grace-alert {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 1rem;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.grace-alert-left {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	:global(.grace-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
	}

	.grace-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.grace-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-warning);
	}

	.grace-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
	}

	.actions-strip {
		display: flex;
		align-items: center;
		justify-content: space-between;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem 1rem;
		gap: 1rem;
		flex-wrap: wrap;
	}

	.key-display-box {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	:global(.key-icon) {
		color: var(--color-text-muted);
	}

	.key-label {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.key-code {
		font-size: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
	}

	.actions-right {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	/* Reveal Modal Styles */
	.reveal-loading {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		padding: 2.5rem;
		color: var(--color-text-muted);
	}

	.reveal-content {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.reveal-tabs {
		display: flex;
		gap: 8px;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 8px;
		flex-wrap: wrap;
	}

	.reveal-tab {
		display: flex;
		align-items: center;
		padding: 6px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.reveal-tab.active {
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border-color: var(--color-accent-border);
		font-weight: 600;
	}

	.reveal-body {
		display: grid;
		grid-template-columns: 230px 1fr;
		gap: 1.25rem;
		align-items: start;
	}

	@media (max-width: 640px) {
		.reveal-body {
			grid-template-columns: 1fr;
		}
		.form-row {
			grid-template-columns: 1fr;
		}
		.meta-strip {
			flex-direction: column;
			align-items: flex-start;
		}
		.strip-divider {
			display: none;
		}
	}

	.reveal-qr-col {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 8px;
	}

	.qr-wrapper {
		background: white;
		padding: 8px;
		border-radius: var(--radius-sm);
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
	}

	.qr-img {
		width: 210px;
		height: 210px;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-placeholder {
		width: 210px;
		height: 210px;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.qr-hint {
		font-size: 11px;
		color: var(--color-text-muted);
		text-align: center;
		line-height: 1.4;
	}

	.reveal-links-col {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.link-block {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.link-header {
		display: flex;
		align-items: center;
	}

	.link-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.input-with-actions {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	.link-input {
		flex: 1;
		min-width: 160px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 5px 8px;
		font-size: 12px;
		color: var(--color-text-primary);
		outline: none;
	}

	.btn-open-tg {
		display: inline-flex;
		align-items: center;
		background: #229ed9;
		color: white;
		border-radius: var(--radius-sm);
		padding: 5px 10px;
		font-size: 12px;
		font-weight: 500;
		text-decoration: none;
		white-space: nowrap;
	}

	.manual-box {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
	}

	.manual-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}

	.manual-item-wide {
		grid-column: span 2;
	}

	.m-label {
		font-size: 11px;
		color: var(--color-text-muted);
		display: block;
		margin-bottom: 2px;
	}

	.m-val-row {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.m-code {
		font-size: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		flex: 1;
		word-break: break-all;
	}

	.copy-mini-btn {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		padding: 3px 5px;
		border-radius: var(--radius-sm);
		cursor: pointer;
	}

	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
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

	.text-input,
	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.confirm-content {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		padding: 0.5rem 0;
	}

	.confirm-p {
		font-size: 13px;
		color: var(--color-text-primary);
		margin: 0 0 8px 0;
	}

	.confirm-note {
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
		margin: 0;
		background: var(--color-bg-primary);
		padding: 8px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.text-warning {
		color: var(--color-warning);
	}
	.text-danger {
		color: var(--color-error);
	}
	.text-primary {
		color: var(--color-accent);
	}

	.mr-1 {
		margin-right: 4px;
	}
	.ml-1 {
		margin-left: 4px;
	}
	.mb-2 {
		margin-bottom: 8px;
	}
	.mb-3 {
		margin-bottom: 12px;
	}

	.ep-selector-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		background: var(--color-bg-primary);
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.ep-selector-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.ep-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.ep-chip {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 4px;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		font-size: 12px;
		cursor: pointer;
		text-align: left;
		max-width: 100%;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.ep-chip.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}

	.settings-section-divider {
		height: 1px;
		background: var(--color-border);
		margin: 6px 0;
	}

	.section-title-row {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 12px;
	}

	.section-heading {
		font-size: 14px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0 0 2px 0;
	}

	.section-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.endpoints-list {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.endpoint-card {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.endpoint-card-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding-bottom: 6px;
		border-bottom: 1px dashed var(--color-border);
	}

	.endpoint-title-group {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.endpoint-card-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.badge-info {
		background: var(--color-info-tint);
		border: 1px solid var(--color-info-border);
		color: var(--color-info);
	}
	.badge-purple {
		background: var(--color-purple-tint);
		border: 1px solid rgba(187, 154, 247, 0.25);
		color: var(--color-purple);
	}

	.ep-preset-bar {
		display: flex;
		flex-direction: column;
		gap: 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
	}

	.ep-preset-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
	}

	.ep-preset-pills {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.ep-preset-pill {
		font-size: 11px;
		font-weight: 500;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
	}
	.ep-preset-pill.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}

	.ep-preset-specs {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.endpoint-card-actions {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.cdn-checkbox-label {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 12px;
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.checkbox-input {
		width: 16px;
		height: 16px;
		flex-shrink: 0;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	.btn-icon-danger {
		display: flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		color: var(--color-error);
		cursor: pointer;
		padding: 4px;
		border-radius: 4px;
	}

	.flex-1 {
		flex: 1;
	}
	.font-mono {
		font-family: var(--font-mono);
	}

	.tg-users-section {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding-top: 4px;
	}

	.tg-users-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.tg-users-title-wrap {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.tg-users-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.tg-users-count {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.tg-users-list {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.tg-user-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 10px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		transition: border-color var(--transition-fast);
	}

	.tg-user-row:hover {
		border-color: var(--color-text-muted);
	}

	.tg-user-left {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.tg-user-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tg-user-name.muted-name {
		color: var(--color-text-muted);
	}

	.tg-user-actions {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.btn-user-delete {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 30px;
		height: 30px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.btn-user-delete:hover {
		color: var(--color-error);
		border-color: var(--color-error);
		background: var(--color-error-tint);
	}

	.reveal-user-selector {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 8px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.reveal-user-select {
		max-width: 260px;
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
</style>
