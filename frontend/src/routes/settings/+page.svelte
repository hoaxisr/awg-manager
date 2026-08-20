<script lang="ts">
	import { onMount } from "svelte";
	import { get } from "svelte/store";
	import { afterNavigate } from "$app/navigation";
	import { page } from "$app/stores";
	import { api } from "$lib/api/client";
	import { notifications } from "$lib/stores/notifications";
	import { singboxStatus } from "$lib/stores/singbox";
	import { hydrarouteStatus } from "$lib/stores/hydraroute";
	import { PageContainer, PageHeader, LoadingSpinner } from "$lib/components/layout";
	import { Toggle, Modal, Button, ConfirmModal, SegmentedControl } from "$lib/components/ui";
	import {
		SystemInfoGrid,
		LoggingSettings,
		UpdateSection,
		DownloadSettings,
		DnsRouteSettings,
		IntegrationsCard,
		ThemeSchemeCard,
		SettingsFooter,
		UsageLevelCard,
		HttpServerCard,
		BackupRestoreCard,
		DevelopChannelGateModal,
		ExperimentalSettingsCard,
		PukhososPatrol,
		SettingsSectionLabel,
	} from "$lib/components/settings";
	import HappKeysModal from "$lib/components/subscriptions/HappKeysModal.svelte";
	import { setSettings as setGlobalSettings } from "$lib/stores/settings";
	import {
		downloadOutbounds,
		downloadOutboundsLoading,
		downloadOutboundsError,
		ensureDownloadOutboundsLoaded,
		resolveDownloadRouteLabel,
	} from "$lib/stores/downloadRoute";
	import type {
		SystemInfo,
		Settings,
		UpdateInfo,
	} from "$lib/types";
	import {
		USAGE_LEVEL_LABELS,
		isAppearanceSettingsVisible,
		isSectionVisible,
		isRoutingSubTabVisible,
		isUpdateChannelSwitchVisible,
		areDownloadRouteDetailsVisible,
		type UsageLevel,
	} from "$lib/types/usageLevel";
	import { usageLevel } from "$lib/stores/settings";
	import { waitForBackendRestart } from "$lib/restartRecovery";
	import { hasDevelopChannelQuizPassed } from "$lib/utils/developChannelGate";
	import { developFeedbackFabVisible } from "$lib/stores/developFeedbackFab";
	import { experimentalSettingsUnlocked } from "$lib/stores/experimentalSettingsUnlocked";
	import { settingsUpdateHighlight } from "$lib/stores/settingsUpdateHighlight";
	import { pluralize, AVAILABLE_WORDS, TUNNEL_WORDS } from "$lib/utils/pluralize";
	import {
		CircleArrowDown,
		Lock,
		CloudDownload,
		ScrollText,
		Activity,
		Wrench,
		Power,
	} from "lucide-svelte";
	import { downloadErrorToText } from "$lib/utils/downloadError";
	import { copyToClipboard } from "$lib/utils/clipboard";
	import {
		clampSessionTtlHours,
		SESSION_TTL_DEFAULT_HOURS,
		SESSION_TTL_MIN_HOURS,
		SESSION_TTL_MAX_HOURS,
	} from "$lib/components/settings/sessionTtl";

	const expandUsageLevel = $derived($page.url.searchParams.has('mode'));
	const highlightFeedbackFab = $derived($page.url.searchParams.has('feedbackFab'));
	const defaultPingTarget = "8.8.8.8";
	const defaultConnectivityCheckUrl = "http://connectivitycheck.gstatic.com/generate_204";
	const highlightDownloads = $derived($page.url.searchParams.get('highlight') === 'downloads');

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	const origin = $derived(typeof window !== "undefined" ? window.location.origin : "");
	const showSingboxIntegration = $derived(isSectionVisible($usageLevel, "singboxTunnels"));
	const showHydraIntegration = $derived(isRoutingSubTabVisible($usageLevel, "hrNeo"));
	const showDnsRouteCard = $derived(isRoutingSubTabVisible($usageLevel, "dnsRoutes"));
	const showDownloadRouteDetails = $derived(areDownloadRouteDetailsVisible($usageLevel));
	const downloadRouteLabel = $derived(resolveDownloadRouteLabel(settings, $downloadOutbounds));
	const visibleDownloadRouteLabel = $derived(showDownloadRouteDetails ? downloadRouteLabel : '');
	let updateInfo: UpdateInfo | null = $state(null);
	let restarting = $state(false);
	let restartConfirmOpen = $state(false);
	let hydraBusy = $state(false);
	let singboxInstalling = $state(false);
	let singboxUninstalling = $state(false);
	let singboxInstallError = $state<string | null>(null);
	let singboxUpdating = $state(false);
	let singboxUpdateError = $state<string | null>(null);
	let singboxBusy = $state(false);
	let ndmsProxyBusy = $state(false);
	let ndmsProxyConfirmOpen = $state(false);
	let ndmsProxyConfirmEnable = $state(false); // true = подтверждение включения; false = выключения
	let systemInfoRefreshing = $state(false);
	let systemInfoUpdatedAt = $state<string | null>(null);
	let systemInfoInFlight: Promise<void> | null = null;
	let developGateOpen = $state(false);
	let footerPatrolWidth = $state(0);
	let showHappKeysModal = $state(false);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxStatusLoading = $derived(
		$singboxStatus.lastFetchedAt === 0 &&
		($singboxStatus.status === 'idle' || $singboxStatus.status === 'loading')
	);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);
	const singboxRunning = $derived(singboxStatusValue?.running ?? false);
	const ndmsProxyEnabled = $derived(singboxStatusValue?.ndmsProxyEnabled ?? true);
	const hydraStatusValue = $derived($hydrarouteStatus.data ?? null);
	const hydraStatusLoading = $derived(
		$hydrarouteStatus.lastFetchedAt === 0 &&
		($hydrarouteStatus.status === 'idle' || $hydrarouteStatus.status === 'loading')
	);
	const hydraStatusError = $derived($hydrarouteStatus.error);
	const hydraInstalled = $derived(hydraStatusValue?.installed ?? false);
	const hydraRunning = $derived(hydraStatusValue?.running ?? false);

	function handleNDMSProxyToggleClick(next: boolean) {
		// next — желаемое состояние после клика. Открываем confirm-modal
		// с предупреждением (warning-only — мы не сканим NDMS-policies).
		ndmsProxyConfirmEnable = next;
		ndmsProxyConfirmOpen = true;
	}

	async function applyNDMSProxyToggle() {
		const enabled = ndmsProxyConfirmEnable;
		ndmsProxyBusy = true;
		try {
			const res = await api.singboxToggleNDMSProxy(enabled);
			ndmsProxyConfirmOpen = false;
			// Обновим стор статуса оптимистично — SSE invalidate тоже придёт.
			if (singboxStatusValue) {
				singboxStatus.applyMutationResponse({ ...singboxStatusValue, ndmsProxyEnabled: res.enabled });
			}
			notifications.success(
				res.migrated
					? (enabled ? 'NDMS Proxy включены' : 'NDMS Proxy выключены')
					: 'Состояние не изменилось',
			);
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось переключить NDMS Proxy';
			if (msg.includes('PROXY_COMPONENT_MISSING') || msg.includes("'proxy'")) {
				notifications.error('NDMS-компонент "proxy" не установлен. Установите его в System → Components.');
			} else {
				notifications.error(msg);
			}
		} finally {
			ndmsProxyBusy = false;
		}
	}

	async function controlSingbox(action: 'start' | 'stop' | 'restart') {
		singboxBusy = true;
		try {
			const fresh = await api.singboxControl(action);
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(
				action === 'restart' ? 'Sing-box перезапущен' :
				action === 'stop' ? 'Sing-box остановлен' : 'Sing-box запущен',
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось управлять sing-box');
		} finally {
			singboxBusy = false;
		}
	}

	async function controlHydra(action: 'start' | 'stop' | 'restart') {
		hydraBusy = true;
		try {
			const fresh = await api.controlHydraRoute(action);
			hydrarouteStatus.applyMutationResponse(fresh);
			notifications.success(
				action === 'restart' ? 'HydraRoute перезапущен' :
				action === 'stop' ? 'HydraRoute остановлен' : 'HydraRoute запущен',
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось управлять HydraRoute');
		} finally {
			hydraBusy = false;
		}
	}

	async function installSingbox() {
		singboxInstalling = true;
		singboxInstallError = null;
		try {
			const fresh = await api.singboxInstall();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success("Sing-box установлен");
		} catch (e) {
			singboxInstallError = e instanceof Error ? e.message : String(e);
		} finally {
			singboxInstalling = false;
		}
	}

	async function uninstallSingbox() {
		singboxUninstalling = true;
		try {
			const fresh = await api.singboxUninstall();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success('Sing-box удалён');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось удалить sing-box');
		} finally {
			singboxUninstalling = false;
		}
	}

	async function updateSingbox() {
		singboxUpdating = true;
		singboxUpdateError = null;
		try {
			const fresh = await api.singboxUpdate();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success("Sing-box обновлён");
		} catch (e) {
			singboxUpdateError = e instanceof Error ? e.message : String(e);
		} finally {
			singboxUpdating = false;
		}
	}

	async function fetchSystemInfo(silent = true) {
		if (systemInfoInFlight) {
			return systemInfoInFlight;
		}
		systemInfoRefreshing = true;
		systemInfoInFlight = (async () => {
			try {
				systemInfo = await api.getSystemInfo();
				systemInfoUpdatedAt = new Date().toISOString();
				if (!silent) {
					notifications.success("Информация о роутере обновлена");
				}
			} catch (e) {
				if (!silent) {
					notifications.error(e instanceof Error ? e.message : "Не удалось обновить системную информацию");
				}
			} finally {
				systemInfoRefreshing = false;
				systemInfoInFlight = null;
			}
		})();
		return systemInfoInFlight;
	}

	async function refreshDownloadOutbounds(showNotification = true) {
		await ensureDownloadOutboundsLoaded(true);
		if (!showNotification) return;
		const err = get(downloadOutboundsError);
		if (err) {
			notifications.error(`Маршруты загрузок: ${downloadErrorToText(err)}`);
			return;
		}
		const list = get(downloadOutbounds);
		const tunnelCount = list.filter((ob) => ob.tag !== 'direct').length;
		const availableTunnelCount = list.filter((ob) => ob.tag !== 'direct' && ob.available).length;
		notifications.success(
			tunnelCount > 0
				? `Маршруты обновлены: найдено ${pluralize(tunnelCount, TUNNEL_WORDS)} (${pluralize(availableTunnelCount, AVAILABLE_WORDS)})`
				: 'Маршруты обновлены: туннели не найдены (доступен только Direct)'
		);
	}

	async function selectDownloadRoute(routeTag: string, routeKind?: 'direct' | 'awg' | 'singbox' | 'subscription') {
		if (!settings) return;
		saving = true;
		try {
			const normalizedTag = routeTag.trim() || 'direct';
			const normalizedKind = normalizedTag === 'direct' ? 'direct' : routeKind;
			settings = await api.updateSettings({
				download: { routeTag: normalizedTag, routeKind: normalizedKind },
			});
			setGlobalSettings(settings);
			notifications.success('Маршрут загрузок сохранён');
		} catch {
			notifications.error('Не удалось сохранить маршрут загрузок');
		} finally {
			saving = false;
		}
	}

	function scrollToSettingsHashTarget() {
		if (typeof window === "undefined") return;
		if (window.location.hash !== "#downloads") return;
		window.requestAnimationFrame(() => {
			const target = document.getElementById("downloads");
			target?.scrollIntoView({ behavior: "smooth", block: "start" });
		});
	}

	function scrollToFeedbackFabSetting() {
		if (typeof window === "undefined") return;
		if (!highlightFeedbackFab) return;
		window.requestAnimationFrame(() => {
			document.getElementById("feedback-fab")?.scrollIntoView({ behavior: "smooth", block: "center" });
		});
	}

onMount(() => {
	const timer = setInterval(() => {
		void fetchSystemInfo(true);
	}, 30000);

	void (async () => {
		try {
			const [_, appSettings] = await Promise.all([
				fetchSystemInfo(true),
				api.getSettings(),
			]);
			settings = appSettings;
			setGlobalSettings(appSettings);
			scrollToSettingsHashTarget();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Не удалось загрузить настройки");
		} finally {
			loading = false;
		}

		// Non-critical for first paint: load update state in background.
		api.checkUpdate()
			.then((info) => {
				updateInfo = info;
			})
			.catch(() => {
				// Keep the page interactive; update widget can stay empty on transient errors.
			});

	})();

	return () => {
		clearInterval(timer);
	};
});

$effect(() => {
	if (showDownloadRouteDetails) {
		void ensureDownloadOutboundsLoaded();
	}
});

	async function toggleAuth(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, authEnabled: enabled });
			setGlobalSettings(settings);
			notifications.success(enabled ? "Авторизация включена" : "Авторизация отключена");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	// «Время жизни сессии» — local state + changed-derived + save button
	// (same pattern as DnsRouteSettings). Legacy backends omit the field →
	// fall back to 24 h.
	let sessionTtlLocal = $state<number | null>(SESSION_TTL_DEFAULT_HOURS);
	const savedSessionTtl = $derived(clampSessionTtlHours(settings?.sessionTtlHours));
	const sessionTtlChanged = $derived(sessionTtlLocal !== savedSessionTtl);

	$effect(() => {
		sessionTtlLocal = savedSessionTtl;
	});

	async function saveSessionTtl() {
		if (!settings) return;
		const hours = clampSessionTtlHours(sessionTtlLocal);
		sessionTtlLocal = hours;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, sessionTtlHours: hours });
			setGlobalSettings(settings);
			notifications.success("Время жизни сессии сохранено");
		} catch (e) {
			// 400 с русским сообщением от бэкенда (валидация 1..720) — показываем как есть.
			notifications.error(e instanceof Error ? e.message : "Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function toggleEntwareAuth(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, entwareAuthEnabled: enabled });
			setGlobalSettings(settings);
			notifications.success(
				enabled
					? "Вход по учётным данным Entware включён"
					: "Вход по учётным данным Entware отключён",
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function generateApiKey() {
		if (!settings) return;
		saving = true;
		try {
			// Server-side generation: WebCrypto's randomUUID is unavailable
			// over plain HTTP (router LAN context), so the backend produces
			// the UUID via crypto/rand and persists it in one round-trip.
			settings = await api.regenerateApiKey();
			setGlobalSettings(settings);
			notifications.success("API ключ сгенерирован");
		} catch {
			notifications.error("Ошибка генерации ключа");
		} finally {
			saving = false;
		}
	}

	async function copyApiKey() {
		if (!settings) return;
		const key = (settings.apiKey ?? "").trim();
		if (!key) {
			notifications.info("Сначала сгенерируйте API ключ");
			return;
		}
		if (await copyToClipboard(key)) {
			notifications.success("API ключ скопирован в буфер обмена");
		} else {
			notifications.error("Не удалось скопировать API ключ");
		}
	}

	async function toggleLogging(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				logging: { ...settings.logging, enabled },
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? "Логирование включено" : "Логирование отключено");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function saveLoggingSettings() {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings(settings);
			setGlobalSettings(settings);
			notifications.success("Настройки логирования сохранены");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function toggleDnsAutoRefresh(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				dnsRoute: {
					...settings.dnsRoute,
					autoRefreshEnabled: enabled,
					refreshIntervalHours:
						enabled && settings.dnsRoute.refreshIntervalHours === 0
							? 6
							: settings.dnsRoute.refreshIntervalHours,
					refreshMode: settings.dnsRoute.refreshMode || "interval",
				},
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? "Автообновление подписок включено" : "Автообновление подписок отключено");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function saveDnsRouteSettings() {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings(settings);
			setGlobalSettings(settings);
			notifications.success("Настройки автообновления сохранены");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function savePingTargetsSettings() {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				pingCheck: {
					...settings.pingCheck,
					defaults: {
						...settings.pingCheck.defaults,
						target: settings.pingCheck.defaults.target,
					},
				},
				connectivityCheckUrl: settings.connectivityCheckUrl,
			});
			setGlobalSettings(settings);
			notifications.success("Цели проверки пинга сохранены");
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Ошибка сохранения целей проверки");
		} finally {
			saving = false;
		}
	}

	let savingBootstrapDNS = $state(false);

	// Bootstrap-DNS применяется бэкендом сразу: он переписывает адрес в
	// 00-base.json и перечитывает конфиг sing-box без перезапуска.
	async function saveBootstrapDNS(value: string) {
		if (!settings) return;
		savingBootstrapDNS = true;
		try {
			settings = await api.updateSettings({ ...settings, singboxBootstrapDNS: value });
			setGlobalSettings(settings);
			notifications.success(
				value ? `Bootstrap-DNS: ${value}` : "Bootstrap-DNS сброшен на значение по умолчанию",
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Ошибка сохранения bootstrap-DNS");
		} finally {
			savingBootstrapDNS = false;
		}
	}

	async function toggleUpdateCheck(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, checkEnabled: enabled },
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? "Автопроверка обновлений включена" : "Автопроверка обновлений отключена");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	function requestChannel(channel: 'stable' | 'develop') {
		if (!settings || settings.updates.channel === channel) return;
		if (channel === 'develop' && !hasDevelopChannelQuizPassed()) {
			developGateOpen = true;
			return;
		}
		void selectChannel(channel);
	}

	async function selectChannel(channel: 'stable' | 'develop') {
		if (!settings || settings.updates.channel === channel) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, channel },
			});
			setGlobalSettings(settings);
			// Кэш проверки относится к прежнему каналу — перепроверяем.
			updateInfo = await api.checkUpdate(true);
			notifications.success(
				channel === 'develop'
					? 'Канал обновлений: develop (нестабильный)'
					: 'Канал обновлений изменён на стабильный',
			);
		} catch (e) {
			notifications.error(`Смена канала обновлений: ${downloadErrorToText(e)}`);
		} finally {
			saving = false;
		}
	}

	async function confirmDevelopChannel() {
		developGateOpen = false;
		await selectChannel('develop');
	}

	async function selectUsageLevel(level: UsageLevel) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, usageLevel: level });
			setGlobalSettings(settings);
			notifications.success(`Уровень: ${USAGE_LEVEL_LABELS[level]}`);
		} catch {
			notifications.error("Не удалось сохранить уровень");
		} finally {
			saving = false;
		}
	}

	async function restartDaemon() {
		restartConfirmOpen = false;
		restarting = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			const result = await requestDaemonRestart();
			if (result === 'accepted') {
				notifications.success("AWG Manager перезапускается...");
			} else {
				notifications.warning("Соединение оборвалось, проверяю перезапуск AWG Manager...");
			}
			const waitResult = await waitForDaemonRestart(before);
			if (waitResult === 'timeout') {
				restarting = false;
				notifications.warning('Не удалось подтвердить перезапуск AWG Manager. Обновите страницу вручную.');
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Не удалось перезапустить");
			restarting = false;
		}
	}

	async function requestDaemonRestart(): Promise<'accepted' | 'network-drop'> {
		try {
			const response = await fetch('/api/system/restart', {
				method: 'POST',
				credentials: 'same-origin',
				cache: 'no-store',
				headers: { 'Content-Type': 'application/json' },
			});

			if (response.status === 401) {
				throw new Error('Сессия истекла');
			}

			if (!response.ok) {
				const text = await response.text().catch(() => '');
				throw new Error(`Не удалось перезапустить AWG Manager (${response.status}): ${text.substring(0, 120)}`);
			}

			return 'accepted';
		} catch (e) {
			if (e instanceof TypeError) {
				return 'network-drop';
			}
			throw e;
		}
	}

	async function readBackendInstanceId(): Promise<string | null> {
		const res = await fetch('/api/health', {
			method: 'GET',
			cache: 'no-store',
			credentials: 'same-origin',
		});
		if (!res.ok) {
			return null;
		}
		const body = await res.json().catch(() => null);
		const id = body?.data?.instanceId;
		return typeof id === 'string' && id.length > 0 ? id : null;
	}

	function sleep(ms: number) {
		return new Promise<void>((resolve) => setTimeout(resolve, ms));
	}

	async function waitForDaemonRestart(previousInstanceId: string | null) {
		return waitForBackendRestart({
			previousInstanceId,
			readInstanceId: readBackendInstanceId,
			sleep,
			now: () => Date.now(),
			timeoutMs: 45_000,
			pollMs: 750,
			stableOnlineMs: 3_000,
		});
	}

	async function refreshSystemInfo() {
		await fetchSystemInfo(false);
	}

	let feedbackFabScrolled = $state(false);

	afterNavigate(async ({ to, from }) => {
		if (!to?.url || to.url.pathname !== "/settings") return;
		if (!from?.url || from.url.pathname !== "/settings") {
			await fetchSystemInfo(true);
		}
		scrollToSettingsHashTarget();
		scrollToFeedbackFabSetting();
	});

	$effect(() => {
		if (!highlightFeedbackFab) {
			feedbackFabScrolled = false;
			return;
		}
		if (loading || !settings || feedbackFabScrolled) return;
		feedbackFabScrolled = true;
		scrollToFeedbackFabSetting();
	});
</script>

<svelte:head>
	<title>Настройки - AWG Manager</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Настройки" />
	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if settings && systemInfo}
		<div class="settings-layout">
		<div class="settings-grid">
			<aside class="settings-left">
				<div class="settings-left-sticky">
				<SystemInfoGrid
					{systemInfo}
					usageLevel={settings.usageLevel}
					onrefresh={refreshSystemInfo}
					refreshing={systemInfoRefreshing}
					lastUpdated={systemInfoUpdatedAt}
					autoRefreshMs={30000}
				/>

				<div id="awgm-update" class="settings-block">
					<div class="card settings-highlight-target" class:highlighted={$settingsUpdateHighlight}>
						<SettingsSectionLabel label="Обновление AWGM" icon={CircleArrowDown} tone="green" header />
						<UpdateSection bind:updateInfo bind:settings />
					</div>
				</div>

				<IntegrationsCard
					singboxStatus={singboxStatusValue}
					{singboxStatusLoading}
					hydraStatus={hydraStatusValue}
					{hydraStatusLoading}
					hydraStatusError={hydraStatusError}
					{singboxInstalling}
					{singboxUpdating}
					{singboxInstallError}
					{singboxUpdateError}
					oninstallSingbox={installSingbox}
					onupdateSingbox={updateSingbox}
					onuninstallSingbox={uninstallSingbox}
					{singboxUninstalling}
					showSingbox={showSingboxIntegration}
					showHydra={showHydraIntegration}
					bootstrapDNS={settings.singboxBootstrapDNS ?? ''}
					bootstrapSaving={savingBootstrapDNS}
					onsaveBootstrapDNS={saveBootstrapDNS}
				/>
				</div>
			</aside>

			<main class="settings-right">
			<UsageLevelCard
				value={settings.usageLevel}
				{saving}
				onSelect={selectUsageLevel}
				initialExpanded={expandUsageLevel}
				highlighted={expandUsageLevel}
			/>

			{#if isAppearanceSettingsVisible(settings.usageLevel)}
				<ThemeSchemeCard />
			{/if}

				<div class="settings-block">
					<div class="card">
					<SettingsSectionLabel label="Доступ" icon={Lock} tone="blue" header />
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Авторизация</span>
							<span class="setting-description">
								Требовать вход через учётную запись роутера для доступа к панели управления.
							</span>
						</div>
						<Toggle checked={settings.authEnabled} onchange={toggleAuth} disabled={saving} />
					</div>
					{#if settings.authEnabled}
						<div class="setting-row session-ttl-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Время жизни сессии</span>
								<span class="setting-description">
									Бездействие дольше этого срока завершает сессию. Активность продлевает её.
								</span>
							</div>
							<div class="session-ttl-form">
								<div class="input-with-suffix">
									<input
										type="number"
										id="sessionTtlHours"
										bind:value={sessionTtlLocal}
										min={SESSION_TTL_MIN_HOURS}
										max={SESSION_TTL_MAX_HOURS}
										disabled={saving}
									/>
									<span class="input-suffix">ч.</span>
								</div>
								{#if sessionTtlChanged}
									<Button variant="primary" size="sm" onclick={saveSessionTtl} loading={saving}>
										{saving ? "Сохранение..." : "Сохранить"}
									</Button>
								{/if}
							</div>
						</div>
						<div class="setting-row toggle-inline-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Вход по учётным данным Entware</span>
								<span class="setting-description">
									Проверять логин и пароль по /opt/etc/shadow. Вход без обращения к роутеру — не создаёт уведомлений в журнале Keenetic.
								</span>
							</div>
							<Toggle
								checked={settings.entwareAuthEnabled ?? false}
								onchange={toggleEntwareAuth}
								disabled={saving}
							/>
						</div>
					{/if}
					<HttpServerCard />
					</div>
				</div>

				<div class="settings-block">
					<div class="card">
					<SettingsSectionLabel label="Загрузки и обновления" icon={CloudDownload} tone="orange" header />
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Автопроверка обновлений</span>
							<span class="setting-description">Проверять наличие новых версий раз в сутки.</span>
						</div>
						<Toggle
							checked={settings.updates.checkEnabled}
							onchange={toggleUpdateCheck}
							disabled={saving}
						/>
					</div>
					{#if systemInfo.isOS5 && showDnsRouteCard}
						<DnsRouteSettings
							bind:settings
							{saving}
							onToggle={toggleDnsAutoRefresh}
							onSave={saveDnsRouteSettings}
						/>
					{/if}
					{#if isUpdateChannelSwitchVisible(settings.usageLevel)}
						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Канал обновлений</span>
								<span class="setting-description">
									Ветка develop — свежие, потенциально нестабильные сборки из ветки разработки.
								</span>
							</div>
							<SegmentedControl
								value={settings.updates.channel}
								options={[
									{ value: 'stable', label: 'Стабильный' },
									{ value: 'develop', label: 'Разработка' },
								] satisfies Array<{ value: 'stable' | 'develop'; label: string }>}
								ariaLabel="Канал обновлений"
								disabled={saving}
								onchange={(channel) => requestChannel(channel)}
							/>
						</div>
					{/if}
					{#if showDownloadRouteDetails}
						<div class="settings-highlight-target" class:highlighted={highlightDownloads}>
							<DownloadSettings
								bind:settings
								{saving}
								outbounds={$downloadOutbounds}
								loading={$downloadOutboundsLoading}
								error={$downloadOutboundsError}
								onRefresh={refreshDownloadOutbounds}
								onSelectRoute={selectDownloadRoute}
							/>
						</div>
					{/if}
					</div>
				</div>

				<div class="settings-block">
					<div class="card">
					<SettingsSectionLabel label="Логирование" icon={ScrollText} tone="slate" header />
					<LoggingSettings
						bind:settings
						{saving}
						onToggle={toggleLogging}
						onSave={saveLoggingSettings}
					/>
					</div>
				</div>

				{#if $usageLevel === "expert"}
				<div class="settings-block">
					<div class="card">
					<SettingsSectionLabel label="Проверка пинга" icon={Activity} tone="teal" header />
					<div class="setting-row ping-target-setting">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Цели проверки</span>
							<span class="setting-description">
								ICMP target используется как глобальный адрес для ping-check. HTTP URL вызывается через туннель для проверки доступности и задержки.
							</span>
						</div>
						<div class="ping-target-controls">
							<label class="ping-target-field">
								<span>ICMP target</span>
								<input
									type="text"
									class="settings-text-input"
									bind:value={settings.pingCheck.defaults.target}
									placeholder={defaultPingTarget}
									disabled={saving}
								/>
							</label>
							<label class="ping-target-field">
								<span>HTTP URL проверки</span>
								<input
									type="url"
									class="settings-text-input"
									bind:value={settings.connectivityCheckUrl}
									placeholder={defaultConnectivityCheckUrl}
									disabled={saving}
								/>
							</label>
							<div class="ping-target-action">
								<Button variant="secondary" size="md" onclick={savePingTargetsSettings} disabled={saving}>
									Сохранить
								</Button>
							</div>
						</div>
					</div>
					</div>
				</div>

				<div class="settings-block">
					<div
						id="feedback-fab"
						class="card settings-highlight-target"
						class:highlighted={highlightFeedbackFab}
					>
					<SettingsSectionLabel label="Расширенные" icon={Wrench} tone="indigo" header />
					<div class="setting-row api-key-setting">
						<div class="flex flex-col gap-1">
							<span class="font-medium">API Key</span>
							<span class="setting-description">
								API ключ для доступа к&nbsp;<code>{origin}/api/</code>, если включена авторизация. Передавайте в заголовке <code>Authorization: Bearer &lt;ключ&gt;</code>.
							</span>
						</div>
						<div class="api-key-controls">
							<input
								type="text"
								class="api-key-input"
								value={settings.apiKey ?? ""}
								readonly
								placeholder="не сгенерирован"
								onclick={copyApiKey}
								title={settings.apiKey?.trim()
									? "Нажмите, чтобы скопировать в буфер обмена"
									: "Сначала нажмите «Сгенерировать»"}
							/>
							<div class="api-key-action">
								<Button variant="secondary" size="md" onclick={generateApiKey} disabled={saving}>
									Сгенерировать
								</Button>
							</div>
						</div>
					</div>
					{#if settings.updates.channel === 'develop'}
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Кнопка обратной связи</span>
							<span class="setting-description">
								Плавающая кнопка «!» в правом нижнем углу на канале разработки.
								Помогает быстро сообщить об ошибке или предложить улучшение.
							</span>
						</div>
						<Toggle
							checked={$developFeedbackFabVisible}
							onchange={(v) => developFeedbackFabVisible.set(v)}
						/>
					</div>
					{/if}

					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">RSA-ключи Happ</span>
							<span class="setting-description">
								Ключи для расшифровки приватных подписок.
							</span>
						</div>
						<Button
							variant="secondary"
							size="md"
							onclick={() => (showHappKeysModal = true)}
							disabled={saving}
						>
							Управление ключами
						</Button>
					</div>

					{#if singboxInstalled && showSingboxIntegration}
						<div class="setting-row toggle-inline-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">NDMS Proxy для sing-box туннелей</span>
								<span class="setting-description">
									{#if ndmsProxyEnabled}
										Если включено — для каждого туннеля sing-box создаётся интерфейс ProxyX в роутере.
										<br>
										Необходимо, если используете NDMS-маршрутизацию (Access Policy, политики роутера) для sing-box.
									{:else}
										Выключено — sing-box работает только через свою маршрутизацию. ProxyX-интерфейсы не создаются
										(решает проблему зависания роутера при потере WAN).
									{/if}
								</span>
							</div>
							<Toggle
								checked={ndmsProxyEnabled}
								controlled
								disabled={ndmsProxyBusy}
								onchange={handleNDMSProxyToggleClick}
							/>
						</div>
					{/if}
					</div>
				</div>

				{#if $experimentalSettingsUnlocked}
					<ExperimentalSettingsCard />
				{/if}
				{/if}

				<div class="settings-block" id="settings-backup">
					<BackupRestoreCard />
				</div>
			</main>
		</div>

		<div class="settings-block" id="settings-actions">
			<div class="card actions-card">
			<SettingsSectionLabel label="Действия" icon={Power} tone="red" header />
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Перезапуск AWGM</span>
					<span class="setting-description">Туннели продолжат работать</span>
				</div>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (restartConfirmOpen = true)}
					loading={restarting}
				>
					{restarting ? "Перезапуск..." : "Перезапустить"}
				</Button>
			</div>

			{#if singboxInstalled && showSingboxIntegration}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">Sing-box</span>
						<span class="setting-description">
							{singboxRunning ? "Процесс работает" : "Процесс остановлен"}
						</span>
					</div>
					<div class="action-buttons">
						{#if singboxRunning}
							<span title={singboxStatusValue?.updateAvailable ? `Сначала обновите sing-box до ${singboxStatusValue.requiredVersion}` : ''}>
								<Button
									variant="secondary"
									size="sm"
									onclick={() => controlSingbox('restart')}
									loading={singboxBusy}
									disabled={singboxStatusValue?.updateAvailable ?? false}
								>
									Перезапустить
								</Button>
							</span>
							<Button variant="danger" size="sm" onclick={() => controlSingbox('stop')} loading={singboxBusy}>Остановить</Button>
						{:else}
							<Button variant="success" size="sm" onclick={() => controlSingbox('start')} loading={singboxBusy}>Запустить</Button>
						{/if}
					</div>
				</div>
			{/if}

			{#if hydraInstalled && showHydraIntegration}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">HydraRoute Neo</span>
						<span class="setting-description">
							{hydraRunning ? "Демон работает" : "Демон остановлен"}
						</span>
					</div>
					<div class="action-buttons">
						{#if hydraRunning}
							<Button variant="secondary" size="sm" onclick={() => controlHydra('restart')} loading={hydraBusy}>Перезапустить</Button>
							<Button variant="danger" size="sm" onclick={() => controlHydra('stop')} loading={hydraBusy}>Остановить</Button>
						{:else}
							<Button variant="success" size="sm" onclick={() => controlHydra('start')} loading={hydraBusy}>Запустить</Button>
						{/if}
					</div>
				</div>
			{/if}
			</div>
		</div>

		<div class="settings-doc-block" id="settings-footer-block">
			<div class="settings-footer-patrol-host" bind:clientWidth={footerPatrolWidth}>
				<PukhososPatrol trackWidth={footerPatrolWidth} />
				<SettingsFooter />
			</div>
		</div>
		</div>
	{/if}

	<DevelopChannelGateModal
		open={developGateOpen}
		busy={saving}
		onclose={() => (developGateOpen = false)}
		onpassed={confirmDevelopChannel}
	/>

	<ConfirmModal
		open={ndmsProxyConfirmOpen}
		title={ndmsProxyConfirmEnable ? 'Включить NDMS Proxy?' : 'Выключить NDMS Proxy?'}
		message={ndmsProxyConfirmEnable
			? 'Будут созданы интерфейсы ProxyX в NDMS для текущих туннелей sing-box.'
			: 'Интерфейсы ProxyX будут удалены из NDMS. Sing-box продолжит работать через свою маршрутизацию.'}
		secondary={ndmsProxyConfirmEnable
			? 'Требуется NDMS-компонент "proxy".'
			: 'Проверьте, что никакие правила маршрутизации NDMS (Access Policy, политики роутера) не ссылаются на эти ProxyX — иначе они перестанут работать.'}
		confirmLabel={ndmsProxyConfirmEnable ? 'Включить' : 'Выключить'}
		variant={ndmsProxyConfirmEnable ? 'primary' : 'danger'}
		busy={ndmsProxyBusy}
		onConfirm={applyNDMSProxyToggle}
		onClose={() => (ndmsProxyConfirmOpen = false)}
	/>

	<Modal
		open={restartConfirmOpen}
		title="Перезапуск AWG Manager"
		size="sm"
		onclose={() => (restartConfirmOpen = false)}
	>
		<p class="modal-text">
			Перезапустить процесс AWG Manager? Туннели продолжат работать.
		</p>
		{#snippet actions()}
			<Button variant="ghost" size="md" onclick={() => (restartConfirmOpen = false)}>Отмена</Button>
			<Button variant="primary" size="md" onclick={restartDaemon}>Перезапустить</Button>
		{/snippet}
	</Modal>

	<HappKeysModal
		bind:open={showHappKeysModal}
		onclose={() => (showHappKeysModal = false)}
	/>
</PageContainer>

<style>
	/* Сетка страницы настроек — базовый layout/gap в app.css (.settings-layout) */

	.settings-doc-block {
		margin-top: 0;
	}

	.settings-grid {
		display: grid;
		grid-template-columns: 360px 1fr;
		gap: var(--settings-gap);
		align-items: stretch;
	}

	.settings-left,
	.settings-right {
		display: flex;
		flex-direction: column;
		gap: var(--settings-gap);
		min-width: 0;
	}

	/* Parent stretches with the grid; sticky lives on the inner block so
	   the sidebar stays pinned for the whole right column, then leaves
	   with the grid (Actions / footer below). No nested scroll. */
	.settings-left-sticky {
		display: flex;
		flex-direction: column;
		gap: var(--settings-gap);
		position: sticky;
		top: calc(56px + 0.75rem);
		width: 100%;
	}

	.modal-text {
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		margin: 0;
	}

	.settings-footer-patrol-host {
		position: relative;
		overflow: visible;
	}

	.actions-card > .setting-row {
		align-items: center;
	}

	.action-buttons {
		display: inline-flex;
		gap: 0.375rem;
		flex-shrink: 0;
		align-items: center;
		justify-content: flex-end;
	}

	@media (min-width: 641px) {
		.actions-card > .setting-row > :global(.btn),
		.action-buttons :global(.btn) {
			width: 7.5rem;
			min-width: 7.5rem;
		}

		.action-buttons > span {
			display: inline-flex;
		}

		.action-buttons > span :global(.btn) {
			width: 7.5rem;
			min-width: 7.5rem;
		}
	}

	.api-key-controls {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: stretch;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
	}

	/* «Время жизни сессии» — inline number input + save button (по образцу DnsRouteSettings) */
	.session-ttl-form {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		flex-shrink: 0;
		min-width: 0;
	}

	.input-with-suffix {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}

	.input-suffix {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.session-ttl-form input[type="number"] {
		width: 4.75rem;
	}

	@media (max-width: 640px) {
		.session-ttl-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
		}

		.session-ttl-form {
			flex-wrap: wrap;
			justify-content: flex-end;
		}
	}

	.ping-target-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 0.65rem;
		align-items: start;
	}

	.ping-target-controls {
		display: grid;
		grid-template-columns: minmax(8rem, 0.78fr) minmax(16rem, 1.22fr) 7.5rem;
		gap: 0.5rem 0.625rem;
		width: 100%;
		min-width: 0;
		align-items: end;
	}

	.ping-target-field {
		display: grid;
		gap: 0.25rem;
		min-width: 0;
	}

	.ping-target-field > span {
		color: var(--color-text-secondary);
		font-size: 0.75rem;
		font-weight: 600;
	}

	.ping-target-field input {
		min-width: 0;
	}

	.settings-text-input,
	.api-key-input {
		width: 100%;
		max-width: none;
	}

	.ping-target-action {
		display: flex;
		align-items: stretch;
		justify-content: stretch;
		align-self: end;
		min-width: 0;
	}

	.ping-target-action :global(.btn) {
		width: 100%;
		min-width: 7.5rem;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

	.api-key-input {
		cursor: pointer;
	}

	.api-key-action {
		display: flex;
		align-items: stretch;
		white-space: nowrap;
	}

	.api-key-action :global(.btn) {
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		box-sizing: border-box;
		padding-block: 0;
	}

	.api-key-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
		gap: 1rem;
		align-items: center;
	}
	.api-key-setting > *:first-child {
		min-width: 0;
	}

	@media (min-width: 641px) {
		.ping-target-setting > *:first-child {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
		}

		.ping-target-setting .setting-description {
			white-space: normal;
			overflow: visible;
			text-overflow: clip;
		}

		.ping-target-controls {
			grid-template-rows: auto 32px;
			align-items: stretch;
		}

		.ping-target-field {
			display: contents;
		}

		.ping-target-field > span {
			grid-row: 1;
		}

		.ping-target-field > input {
			grid-row: 2;
		}

		.ping-target-action {
			grid-row: 2;
			align-self: stretch;
		}

		.api-key-setting {
			grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
			align-items: center;
		}

		.api-key-setting > *:first-child {
			display: flex;
			flex-direction: column;
			align-items: flex-start;
			gap: 0.25rem;
		}

		.api-key-setting .setting-description {
			white-space: normal;
			overflow: visible;
			text-overflow: clip;
		}

		.api-key-controls {
			width: 100%;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: stretch;
		}

		.api-key-action {
			display: flex;
		}

		.api-key-action :global(.btn) {
			width: auto;
			min-width: 7.5rem;
			height: 32px;
			min-height: 32px;
			max-height: 32px;
		}
	}

	@media (max-width: 640px) {
		.ping-target-setting {
			grid-template-columns: 1fr;
			align-items: stretch;
		}

		.ping-target-controls {
			grid-template-columns: minmax(0, 1fr);
		}

		.ping-target-action {
			justify-content: stretch;
		}

		.ping-target-action :global(.btn) {
			width: 100%;
		}

		.api-key-controls {
			grid-template-columns: minmax(0, 1fr) auto;
		}

		.api-key-setting {
			grid-template-columns: 1fr;
		}

		.toggle-inline-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.toggle-inline-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}

		.actions-card > .setting-row:has(.action-buttons) {
			flex-direction: column;
			align-items: stretch;
			flex-wrap: nowrap;
			gap: 0.625rem;
		}

		.actions-card > .setting-row:has(.action-buttons) > *:first-child {
			flex: initial;
			width: 100%;
		}

		.actions-card > .setting-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.actions-card > .setting-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}

		.action-buttons {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			justify-content: stretch;
			flex-wrap: nowrap;
			width: 100%;
			gap: 0.5rem;
		}

		.actions-card > .setting-row > :global(.btn) {
			width: min(50%, 10rem);
			min-width: 0;
			margin-left: auto;
		}

		.action-buttons > span {
			display: block;
			width: 100%;
			min-width: 0;
		}

		.action-buttons > span :global(.btn),
		.action-buttons :global(.btn) {
			width: 100%;
			min-width: 0;
		}
	}

	@media (max-width: 900px) {
		.settings-grid {
			grid-template-columns: 1fr;
		}
		.settings-left-sticky {
			position: static;
		}
	}

	.settings-highlight-target.highlighted {
		animation: settings-target-glow 2.8s ease-out forwards;
	}

	@keyframes settings-target-glow {
		0%   { box-shadow: none; }
		12%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 55%, transparent), 0 0 18px 2px color-mix(in srgb, var(--color-accent) 22%, transparent); }
		30%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 20%, transparent); }
		48%  { box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 40%, transparent), 0 0 14px 2px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		65%  { box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 15%, transparent); }
		82%  { box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-accent) 22%, transparent), 0 0 8px 1px color-mix(in srgb, var(--color-accent) 10%, transparent); }
		100% { box-shadow: none; }
	}
</style>
