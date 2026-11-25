<script lang="ts">
	import { onMount, tick, untrack } from "svelte";
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import { get } from "svelte/store";
	import { goto } from "$app/navigation";
	import { page } from "$app/stores";
	import { api } from "$lib/api/client";
	import { notifications } from "$lib/stores/notifications";
	import { singboxStatus } from "$lib/stores/singbox";
	import { PageContainer, PageHeader, LoadingSpinner } from "$lib/components/layout";
	import { Tabs, Toggle, SegmentedControl } from "$lib/components/ui";
	import {
		SystemInfoGrid,
		LoggingSettings,
		UpdateSection,
		DownloadSettings,
		DnsRouteSettings,
		IntegrationsSection,
		FircIntegrationSection,
		ThemeSchemeCard,
		SettingsFooter,
		UsageLevelCard,
		AccessCard,
		PingTargetsCard,
		AdvancedCard,
		ServiceActionsCard,
		BackupRestoreCard,
		DevelopChannelGateModal,
		ExperimentalSettingsCard,
		PukhososPatrol,
		SettingsSectionLabel,
		McpSection,
		ObfuscatorRelayCard,
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
		usageLevelLabel,
		isAppearanceSettingsVisible,
		isSectionVisible,
		isRoutingSubTabVisible,
		isUpdateChannelSwitchVisible,
		areDownloadRouteDetailsVisible,
		type UsageLevel,
	} from "$lib/types/usageLevel";
	import { usageLevel } from "$lib/stores/settings";
	import { m } from "$lib/i18n";
	import { hasDevelopChannelQuizPassed } from "$lib/utils/developChannelGate";
	import { experimentalSettingsUnlocked } from "$lib/stores/experimentalSettingsUnlocked";
	import {
		resolveSettingsLink,
		revealSettingsTarget,
		settingsTabHref,
		type SettingsLink,
		type SettingsTab,
	} from "$lib/utils/settingsLink";
	import {
		CircleArrowDown,
		CloudDownload,
		ScrollText,
	} from "lucide-svelte";
	import { downloadErrorToText } from "$lib/utils/downloadError";

	let activeTab = $state<SettingsTab>(resolveSettingsLink($page.url)?.tab ?? 'general');
	const tabs = $derived([
		{ id: 'general', label: m.settings_tab_general() },
		{ id: 'updates', label: m.settings_tab_updates() },
		{ id: 'components', label: m.settings_tab_components() },
		{ id: 'access', label: m.settings_tab_access() },
		{ id: 'logs', label: m.settings_tab_logs() },
		{ id: 'system', label: m.settings_tab_system() },
	] satisfies Array<{ id: SettingsTab; label: string }>);
	/** Блок, к которому привела глубокая ссылка; до ручной смены вкладки. */
	let target = $state.raw<SettingsLink | null>(null);
	let confirmedToken = '';

	// Глубокая ссылка — разовое событие: открыли вкладку, запомнили блок и
	// убрали её из адреса, иначе она возвращала бы на свою вкладку при каждом
	// переключении. Токен подтверждения адреса панели обязан уйти на бэкенд с
	// любой вкладки и до загрузки настроек: не дойдёт — адрес откатится.
	$effect(() => {
		const url = $page.url;
		const link = url.pathname === '/settings' ? resolveSettingsLink(url) : null;
		if (link) void untrack(() => followLink(link));
	});

	async function followLink(link: SettingsLink) {
		if (link.listenConfirm && link.listenConfirm !== confirmedToken) {
			confirmedToken = link.listenConfirm;
			void confirmListenChange(link.listenConfirm);
		}
		activeTab = link.tab;
		target = link;
		// Сначала Tabs дописывает в адрес вкладку, затем ссылка уходит целиком.
		await tick();
		await goto(settingsTabHref(link.tab), { replaceState: true, noScroll: true, keepFocus: true });
	}

	async function confirmListenChange(token: string) {
		try {
			await api.serverListenConfirm(token);
			notifications.success(m.settings_http_confirmed_saved());
		} catch (e) {
			notifications.error(
				m.settings_http_confirm_failed({ error: e instanceof Error ? e.message : String(e) }),
			);
		}
	}

	$effect(() => {
		const anchor = target?.anchor;
		if (loading || !anchor) return;
		void tick().then(() => requestAnimationFrame(() => revealSettingsTarget(anchor)));
	});

	function selectTab(id: string) {
		activeTab = id as SettingsTab;
		target = null;
	}

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	const origin = $derived(typeof window !== "undefined" ? window.location.origin : "");
	const showSingboxIntegration = $derived(isSectionVisible($usageLevel, "singboxTunnels"));
	const showDnsRouteCard = $derived(isRoutingSubTabVisible($usageLevel, "dnsRoutes"));
	const showDownloadRouteDetails = $derived(areDownloadRouteDetailsVisible($usageLevel));
	const downloadRouteLabel = $derived(resolveDownloadRouteLabel(settings, $downloadOutbounds));
	const visibleDownloadRouteLabel = $derived(showDownloadRouteDetails ? downloadRouteLabel : '');
	let updateInfo: UpdateInfo | null = $state(null);
	let systemInfoRefreshing = $state(false);
	let systemInfoUpdatedAt = $state<string | null>(null);
	let systemInfoInFlight: Promise<void> | null = null;
	let developGateOpen = $state(false);
	let footerPatrolWidth = $state(0);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);

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
					notifications.success(m.settings_page_router_info_updated());
				}
			} catch (e) {
				if (!silent) {
					notifications.error(e instanceof Error ? e.message : m.settings_page_system_info_failed());
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
			notifications.error(m.settings_page_download_routes_error({ error: downloadErrorToText(err) }));
			return;
		}
		const list = get(downloadOutbounds);
		const tunnelCount = list.filter((ob) => ob.tag !== 'direct').length;
		const availableTunnelCount = list.filter((ob) => ob.tag !== 'direct' && ob.available).length;
		notifications.success(
			tunnelCount > 0
				? m.settings_page_routes_updated_found({ tunnels: m.settings_page_tunnels_count({ count: tunnelCount }), available: m.settings_page_available_count({ count: availableTunnelCount }) })
				: m.settings_page_routes_updated_none()
		);
	}

	// Общая схема сохранения настроек: запрос → общий стор → уведомление.
	// backendError — показать текст ошибки бэкенда (валидация), иначе fail.
	async function save(request: () => Promise<Settings>, ok: string, fail: string, backendError = false) {
		if (!settings) return;
		saving = true;
		try {
			settings = await request();
			setGlobalSettings(settings);
			notifications.success(ok);
		} catch (e) {
			notifications.error(backendError && e instanceof Error ? e.message : fail);
		} finally {
			saving = false;
		}
	}

	function selectDownloadRoute(routeTag: string, routeKind?: 'direct' | 'awg' | 'singbox' | 'subscription') {
		const normalizedTag = routeTag.trim() || 'direct';
		const normalizedKind = normalizedTag === 'direct' ? 'direct' : routeKind;
		return save(
			() => api.updateSettings({ download: { routeTag: normalizedTag, routeKind: normalizedKind } }),
			m.settings_page_download_route_saved(),
			m.settings_page_download_route_save_failed(),
		);
	}

onMount(() => {
	// Свой таймер мимо стора sysInfo. Данные тут почти статичные: версии,
	// возможности прошивки, состояние kernel-модуля. Всё, что меняется,
	// меняет сам пользователь с этой же страницы, и те пути перечитывают
	// сами — поэтому это страховка, а не источник, и 30 с ей ни к чему.
	// Фоновая вкладка не спрашивает вовсе.
	const stopSystemInfoPoll = startVisiblePoll(() => fetchSystemInfo(true), 120000);

	void (async () => {
		try {
			const [_, appSettings] = await Promise.all([
				fetchSystemInfo(true),
				api.getSettings(),
			]);
			settings = appSettings;
			setGlobalSettings(appSettings);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_load_failed());
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
		stopSystemInfoPoll();
	};
});

$effect(() => {
	if (showDownloadRouteDetails) {
		void ensureDownloadOutboundsLoaded();
	}
});

	function toggleAuth(enabled: boolean) {
		return save(
			() => api.updateSettings({ authEnabled: enabled }),
			enabled ? m.settings_page_auth_enabled() : m.settings_page_auth_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveSessionTtl(hours: number) {
		// 400 с русским сообщением от бэкенда (валидация 1..720) — показываем как есть.
		return save(
			() => api.updateSettings({ sessionTtlHours: hours }),
			m.settings_page_session_ttl_saved(),
			m.settings_page_save_error(),
			true,
		);
	}

	function toggleMcp(enabled: boolean) {
		return save(
			() => api.updateSettings({ mcpEnabled: enabled }),
			enabled ? m.settings_page_mcp_enabled() : m.settings_page_mcp_disabled(),
			m.settings_page_save_error(),
			true,
		);
	}

	function toggleObfuscatorRelay(process: boolean) {
		return save(
			() => api.setObfuscatorRelay(process),
			process ? m.settings_page_phobos_userspace() : m.settings_page_phobos_kernel(),
			m.settings_page_save_error(),
			true,
		);
	}

	function generateApiKey() {
		// Server-side generation: WebCrypto's randomUUID is unavailable
		// over plain HTTP (router LAN context), so the backend produces
		// the UUID via crypto/rand and persists it in one round-trip.
		return save(() => api.regenerateApiKey(), m.settings_page_apikey_generated(), m.settings_page_apikey_generate_failed());
	}

	function toggleLogging(enabled: boolean) {
		return save(
			() => api.updateSettings({ logging: { enabled } }),
			enabled ? m.settings_page_logging_enabled() : m.settings_page_logging_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveLoggingSettings(logging: Settings["logging"]) {
		return save(() => api.updateSettings({ logging }), m.settings_page_logging_saved(), m.settings_page_save_error());
	}

	function toggleDnsAutoRefresh(enabled: boolean) {
		if (!settings) return;
		const current = settings.dnsRoute;
		return save(
			() =>
				api.updateSettings({
					dnsRoute: {
						autoRefreshEnabled: enabled,
						// Дефолты дописываем, только если их нет: остальное блока не трогаем.
						...(enabled && current.refreshIntervalHours === 0 ? { refreshIntervalHours: 6 } : {}),
						...(current.refreshMode ? {} : { refreshMode: "interval" as const }),
					},
				}),
			enabled ? m.settings_page_dns_auto_enabled() : m.settings_page_dns_auto_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveDnsRouteSettings(dnsRoute: Settings["dnsRoute"]) {
		return save(() => api.updateSettings({ dnsRoute }), m.settings_page_dns_auto_saved(), m.settings_page_save_error());
	}

	function savePingTargetsSettings(target: string, connectivityCheckUrl: string) {
		return save(
			() => api.updateSettings({ pingCheck: { defaults: { target } }, connectivityCheckUrl }),
			m.settings_page_ping_targets_saved(),
			m.settings_page_ping_targets_save_failed(),
			true,
		);
	}

	let savingBootstrapDNS = $state(false);

	// Bootstrap-DNS применяется бэкендом сразу: он переписывает адрес в
	// 00-base.json и перечитывает конфиг sing-box без перезапуска.
	async function saveBootstrapDNS(value: string) {
		if (!settings) return;
		savingBootstrapDNS = true;
		try {
			settings = await api.updateSettings({ singboxBootstrapDNS: value });
			setGlobalSettings(settings);
			notifications.success(
				value
					? m.settings_page_bootstrap_set({ value })
					: m.settings_page_bootstrap_cleared(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_bootstrap_save_failed());
		} finally {
			savingBootstrapDNS = false;
		}
	}

	let savingClashPort = $state(false);
	let clashPortError = $state<string | null>(null);

	// Порт Clash API применяется бэкендом сразу: он переписывает
	// external_controller в 00-base.json, перечитывает конфиг sing-box и
	// переставляет собственного клиента. Отказ по занятости порта приходит
	// текстом ошибки и показывается прямо под полем.
	async function saveClashPort(value: number) {
		if (!settings) return;
		savingClashPort = true;
		clashPortError = null;
		try {
			settings = await api.updateSettings({ singboxClashPort: value });
			setGlobalSettings(settings);
			notifications.success(m.settings_page_clash_port_saved({ value }));
		} catch (e) {
			const msg = e instanceof Error ? e.message : m.settings_page_clash_port_save_failed();
			clashPortError = msg;
			notifications.error(msg);
		} finally {
			savingClashPort = false;
		}
	}

	function toggleUpdateCheck(enabled: boolean) {
		return save(
			() => api.updateSettings({ updates: { checkEnabled: enabled } }),
			enabled ? m.settings_page_update_check_enabled() : m.settings_page_update_check_disabled(),
			m.settings_page_save_error(),
		);
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
			settings = await api.updateSettings({ updates: { channel } });
			setGlobalSettings(settings);
			// Кэш проверки относится к прежнему каналу — перепроверяем.
			updateInfo = await api.checkUpdate(true);
			notifications.success(
				channel === 'develop'
					? m.settings_page_channel_develop()
					: m.settings_page_channel_stable(),
			);
		} catch (e) {
			notifications.error(m.settings_page_channel_failed({ error: downloadErrorToText(e) }));
		} finally {
			saving = false;
		}
	}

	async function confirmDevelopChannel() {
		developGateOpen = false;
		await selectChannel('develop');
	}

	function selectUsageLevel(level: UsageLevel) {
		return save(
			() => api.updateSettings({ usageLevel: level }),
			m.settings_level_saved({ level: usageLevelLabel(level) }),
			m.settings_page_level_save_failed(),
		);
	}

	async function refreshSystemInfo() {
		await fetchSystemInfo(false);
	}
</script>

<svelte:head>
	<title>{m.settings_page_document_title()}</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title={m.nav_settings()} />
	<Tabs {tabs} active={activeTab} onchange={selectTab} urlParam="tab" defaultTab="general" />
	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if settings}
		<div class="settings-layout">
			{#if activeTab === 'general'}
				<div class="settings-columns">
					<div class="settings-column">
						<UsageLevelCard
							value={settings.usageLevel}
							{saving}
							onSelect={selectUsageLevel}
							initialExpanded={target?.anchor === 'usage-level'}
						/>
						<ServiceActionsCard showSingbox={showSingboxIntegration} />
						{#if $usageLevel === 'expert' && $experimentalSettingsUnlocked}
							<ExperimentalSettingsCard />
						{/if}
					</div>
					{#if isAppearanceSettingsVisible(settings.usageLevel)}
						<div class="settings-column">
							<ThemeSchemeCard />
						</div>
					{/if}
				</div>
			{:else if activeTab === 'updates'}
				<div class="settings-columns">
					<div class="settings-column">
						<div id="awgm-update" class="settings-block">
							<div class="card">
								<SettingsSectionLabel label={m.settings_page_update_section_title()} icon={CircleArrowDown} tone="green" header />
								<UpdateSection bind:updateInfo bind:settings />
							</div>
						</div>
					</div>
					<div class="settings-column">
						<div class="settings-block">
							<div class="card">
							<SettingsSectionLabel label={m.settings_page_downloads_title()} icon={CloudDownload} tone="orange" header />
							<div class="setting-row toggle-inline-row">
								<div class="flex flex-col gap-1">
									<span class="font-medium">{m.settings_page_update_check_label()}</span>
									<span class="setting-description">{m.settings_page_update_check_description()}</span>
								</div>
								<Toggle
									checked={settings.updates.checkEnabled}
									controlled
									onchange={toggleUpdateCheck}
									disabled={saving}
								/>
							</div>
							{#if systemInfo?.isOS5 && showDnsRouteCard}
								<DnsRouteSettings
									{settings}
									{saving}
									onToggle={toggleDnsAutoRefresh}
									onSave={saveDnsRouteSettings}
								/>
							{/if}
							{#if isUpdateChannelSwitchVisible(settings.usageLevel)}
								<div class="setting-row">
									<div class="flex flex-col gap-1">
										<span class="font-medium">{m.settings_page_channel_label()}</span>
										<span class="setting-description">
											{m.settings_page_channel_description()}
										</span>
									</div>
									<SegmentedControl
										value={settings.updates.channel}
										options={[
											{ value: 'stable', label: m.settings_page_channel_option_stable() },
											{ value: 'develop', label: m.settings_page_channel_option_develop() },
										] satisfies Array<{ value: 'stable' | 'develop'; label: string }>}
										ariaLabel={m.settings_page_channel_label()}
										disabled={saving}
										onchange={(channel) => requestChannel(channel)}
									/>
								</div>
							{/if}
							{#if showDownloadRouteDetails}
									<DownloadSettings
										bind:settings
										{saving}
										outbounds={$downloadOutbounds}
										loading={$downloadOutboundsLoading}
										error={$downloadOutboundsError}
										onRefresh={refreshDownloadOutbounds}
										onSelectRoute={selectDownloadRoute}
									/>
							{/if}
							</div>
						</div>
					</div>
				</div>
			{:else if activeTab === 'components'}
				<div class="settings-columns">
					<div class="settings-column">
						<IntegrationsSection
							showSingbox={showSingboxIntegration}
							bootstrapDNS={settings.singboxBootstrapDNS ?? ''}
							bootstrapSaving={savingBootstrapDNS}
							onsaveBootstrapDNS={saveBootstrapDNS}
							clashPort={settings.singboxClashPort ?? 0}
							clashPortSaving={savingClashPort}
							{clashPortError}
							onsaveClashPort={saveClashPort}
						/>
					</div>
					<div class="settings-column">
						<FircIntegrationSection />
						{#if $usageLevel === 'expert'}
							<ObfuscatorRelayCard
								process={settings.obfuscatorRelayProcess ?? false}
								tripped={settings.obfuscatorKmodTripped ?? ''}
								{saving}
								ontoggle={toggleObfuscatorRelay}
							/>
						{/if}
					</div>
				</div>
			{:else if activeTab === 'access'}
				<div class="settings-columns">
					<div class="settings-column">
						<AccessCard
							authEnabled={settings.authEnabled}
							sessionTtlHours={settings.sessionTtlHours}
							{saving}
							ontoggleAuth={toggleAuth}
							onsaveSessionTtl={saveSessionTtl}
						/>
					</div>
					{#if $usageLevel === 'expert'}
						<div class="settings-column">
							<AdvancedCard
								apiKey={settings.apiKey}
								{origin}
								{saving}
								showFeedback={settings.updates.channel === 'develop'}
								showNdmsProxy={singboxInstalled && showSingboxIntegration}
												ongenerateApiKey={generateApiKey}
							/>
							<McpSection
								enabled={settings.mcpEnabled ?? false}
								{saving}
								{origin}
								ontoggle={toggleMcp}
							/>
						</div>
					{/if}
				</div>
			{:else if activeTab === 'logs'}
				<div class="settings-columns">
					<div class="settings-column">
						<div class="settings-block">
							<div class="card">
							<SettingsSectionLabel label={m.settings_page_logging_title()} icon={ScrollText} tone="slate" header />
							<LoggingSettings
								{settings}
								{saving}
								onToggle={toggleLogging}
								onSave={saveLoggingSettings}
							/>
							</div>
						</div>
					</div>
					{#if $usageLevel === 'expert'}
						<div class="settings-column">
							<PingTargetsCard
								target={settings.pingCheck.defaults.target}
								url={settings.connectivityCheckUrl}
								{saving}
								onsave={savePingTargetsSettings}
							/>
						</div>
					{/if}
				</div>
			{:else if activeTab === 'system'}
				<div class="settings-columns">
					<div class="settings-column">
						{#if systemInfo}
							<SystemInfoGrid
								{systemInfo}
								usageLevel={settings.usageLevel}
								onrefresh={refreshSystemInfo}
								refreshing={systemInfoRefreshing}
								lastUpdated={systemInfoUpdatedAt}
							/>
						{/if}
					</div>
					<div class="settings-column">
						<div class="settings-block" id="settings-backup">
							<BackupRestoreCard />
						</div>
					</div>
				</div>
			{/if}

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


</PageContainer>

<style>
	.settings-footer-patrol-host {
		position: relative;
		overflow: visible;
	}


	@media (max-width: 640px) {
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

	}
</style>
