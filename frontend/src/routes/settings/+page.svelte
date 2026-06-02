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
	import { Toggle, Modal, Button, ConfirmModal } from "$lib/components/ui";
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
		DevelopChannelGateModal,
	} from "$lib/components/settings";
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

	const expandUsageLevel = $derived($page.url.searchParams.has('mode'));

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
			notifications.error(`Ошибка обновления маршрутов: ${err}`);
			return;
		}
		const list = get(downloadOutbounds);
		const tunnelCount = list.filter((ob) => ob.tag !== 'direct').length;
		const availableTunnelCount = list.filter((ob) => ob.tag !== 'direct' && ob.available).length;
		notifications.success(
			tunnelCount > 0
				? `Маршруты обновлены: найдено ${tunnelCount} туннелей (${availableTunnelCount} доступно)`
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
		const fallbackCopy = (text: string): boolean => {
			try {
				const textarea = document.createElement("textarea");
				textarea.value = text;
				textarea.setAttribute("readonly", "");
				textarea.style.position = "fixed";
				textarea.style.top = "-1000px";
				textarea.style.left = "-1000px";
				textarea.style.opacity = "0";
				document.body.appendChild(textarea);
				textarea.focus();
				textarea.select();
				textarea.setSelectionRange(0, textarea.value.length);
				const copied = document.execCommand("copy");
				document.body.removeChild(textarea);
				return copied;
			} catch {
				return false;
			}
		};

		let copied = false;
		try {
			if (navigator.clipboard?.writeText) {
				await navigator.clipboard.writeText(key);
				copied = true;
			}
		} catch {
			copied = false;
		}

		if (!copied) {
			copied = fallbackCopy(key);
		}

		if (copied) {
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
		} catch {
			notifications.error('Ошибка смены канала обновлений');
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

	afterNavigate(async ({ to, from }) => {
		if (!to?.url || to.url.pathname !== "/settings") return;
		if (!from?.url || from.url.pathname !== "/settings") {
			await fetchSystemInfo(true);
		}
		scrollToSettingsHashTarget();
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
				<SystemInfoGrid
					{systemInfo}
					usageLevel={settings.usageLevel}
					onrefresh={refreshSystemInfo}
					refreshing={systemInfoRefreshing}
					lastUpdated={systemInfoUpdatedAt}
					autoRefreshMs={30000}
				/>

				<div class="card">
					<div class="section-label section-label-with-route">
						<span>Обновление AWGM</span>
						{#if showDownloadRouteDetails}
							<span class="section-label-route" title={downloadRouteLabel}>
								через {downloadRouteLabel}
							</span>
						{/if}
					</div>
					<UpdateSection bind:updateInfo downloadRouteLabel={visibleDownloadRouteLabel} />
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
					showSingbox={showSingboxIntegration}
					showHydra={showHydraIntegration}
					downloadRouteLabel={visibleDownloadRouteLabel}
				/>
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

				<div class="card">
					<div class="section-label">Доступ</div>
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Авторизация</span>
							<span class="setting-description">
								Требовать вход через учётную запись роутера для доступа к панели управления.
							</span>
						</div>
						<Toggle checked={settings.authEnabled} onchange={toggleAuth} disabled={saving} />
					</div>
				</div>

				<div class="card">
					<div class="section-label">Загрузки и обновления</div>
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
						<div class="setting-row channel-setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Канал обновлений</span>
								<span class="setting-description">
									develop — свежие, потенциально нестабильные сборки из ветки разработки.
								</span>
							</div>
							<div class="channel-switch">
								<button
									type="button"
									class="channel-option"
									class:active={settings.updates.channel === 'stable'}
									disabled={saving}
									onclick={() => requestChannel('stable')}
								>
									Стабильный
								</button>
								<button
									type="button"
									class="channel-option"
									class:active={settings.updates.channel === 'develop'}
									disabled={saving}
									onclick={() => requestChannel('develop')}
								>
									Канал разработки
								</button>
							</div>
						</div>
					{/if}
					{#if showDownloadRouteDetails}
						<DownloadSettings
							bind:settings
							{saving}
							outbounds={$downloadOutbounds}
							loading={$downloadOutboundsLoading}
							error={$downloadOutboundsError}
							routeSelectorEnabled={singboxInstalled || singboxStatusLoading}
							onRefresh={refreshDownloadOutbounds}
							onSelectRoute={selectDownloadRoute}
						/>
					{/if}
				</div>

				<div class="card">
					<div class="section-label">Логирование</div>
					<LoggingSettings
						bind:settings
						{saving}
						onToggle={toggleLogging}
						onSave={saveLoggingSettings}
					/>
				</div>

				{#if $usageLevel === "expert"}
				<div class="card">
					<div class="section-label">Расширенные</div>
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
				{/if}
			</main>
		</div>

		<div class="card actions-card">
			<div class="section-label">Действия</div>
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

		<div class="settings-doc-block">
			<SettingsFooter />
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
</PageContainer>

<style>
	/* Единый шаг сетки страницы настроек: колонки, стеки, до «Действий», до блока документации, шаг между строками там */
	.settings-layout {
		--settings-gap: 0.765rem;
	}

	.settings-doc-block {
		margin-top: var(--settings-gap);
	}

	.settings-grid {
		display: grid;
		grid-template-columns: 360px 1fr;
		gap: var(--settings-gap);
		align-items: start;
	}

	.settings-left,
	.settings-right {
		display: flex;
		flex-direction: column;
		gap: var(--settings-gap);
	}

	.settings-left {
		position: sticky;
		top: 1rem;
		align-self: start;
	}

	.modal-text {
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		margin: 0;
	}

	.actions-card {
		margin-top: var(--settings-gap);
	}

	/* Между строками — тот же шаг, что и между карточками (сумма половин padding) */
	.actions-card > .setting-row {
		padding-block: calc(var(--settings-gap) * 0.5);
		align-items: center;
	}

	.actions-card > .setting-row:last-of-type {
		padding-bottom: 0;
	}

	.action-buttons {
		display: inline-flex;
		gap: 0.375rem;
		flex-shrink: 0;
		align-items: center;
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

	.section-label-with-route {
		display: flex;
		align-items: baseline;
		gap: 0.45rem;
		min-width: 0;
	}

	.section-label-route {
		text-transform: none;
		letter-spacing: normal;
		font-weight: 500;
		opacity: 0.9;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.api-key-controls {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		min-width: 0;
	}

	.api-key-input {
		width: 100%;
		max-width: none;
		padding: 0.375rem 0.5rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.8rem;
		background: var(--bg, var(--color-bg));
		border: 1px solid var(--border, var(--color-border));
		border-radius: 4px;
		color: var(--text, var(--color-text));
		cursor: pointer;
	}
	.api-key-input:read-only {
		opacity: 0.85;
		cursor: text;
	}
	.api-key-action {
		align-self: auto;
		white-space: nowrap;
	}

	.api-key-setting {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
		gap: 1rem;
		align-items: start;
	}
	.api-key-setting > *:first-child {
		min-width: 0;
	}

	@media (min-width: 641px) {
		.api-key-setting {
			grid-template-columns: minmax(0, 1fr) minmax(0, min(50%, 34rem));
			align-items: start;
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
		}
	}

	@media (max-width: 640px) {
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
			justify-content: flex-end;
			flex-wrap: nowrap;
		}
	}

	@media (max-width: 900px) {
		.settings-grid {
			grid-template-columns: 1fr;
		}
		.settings-left {
			position: static;
		}
	}

	.channel-setting-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: start;
		gap: 0.75rem 1rem;
	}

	.channel-setting-row > *:first-child {
		min-width: 0;
	}

	.channel-setting-row .font-medium {
		white-space: nowrap;
	}

	.channel-setting-row .setting-description {
		max-width: 42rem;
	}

	.channel-setting-row .channel-switch {
		align-self: start;
	}

	@media (max-width: 640px) {
		.channel-setting-row {
			grid-template-columns: 1fr;
			gap: 0.5rem;
		}

		.channel-setting-row .channel-switch {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			width: 100%;
		}

		.channel-setting-row .channel-option {
			width: 100%;
			min-width: 0;
			text-align: center;
		}
	}

	.channel-switch {
		display: inline-flex;
		border: 1px solid var(--border);
		border-radius: 0.5rem;
		overflow: hidden;
		flex-shrink: 0;
	}
	.channel-option {
		padding: 0.35rem 0.75rem;
		font-size: 0.85rem;
		background: transparent;
		color: var(--text-secondary);
		border: none;
		cursor: pointer;
	}
	.channel-option.active {
		background: var(--accent);
		color: #000 !important;
	}
	.channel-option:disabled {
		opacity: 0.6;
		cursor: default;
	}
</style>
