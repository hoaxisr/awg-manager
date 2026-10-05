<script lang="ts">
	import { onMount } from "svelte";
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import { get } from "svelte/store";
	import { afterNavigate } from "$app/navigation";
	import { page } from "$app/stores";
	import { api } from "$lib/api/client";
	import { notifications } from "$lib/stores/notifications";
	import { singboxStatus } from "$lib/stores/singbox";
	import { hydrarouteStatus } from "$lib/stores/hydraroute";
	import { mcpKeys } from "$lib/stores/mcpKeys";
	import type { PollingState } from "$lib/stores/polling";
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
		McpCard,
		ObfuscatorRelayCard,
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
		McpKey,
		McpKeyCreated,
	} from "$lib/types";
	import { proxyInstallStatus, type ProxySubsystem } from "$lib/stores/proxyInstall";
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
	import RichText from "$lib/components/ui/RichText.svelte";
	import { waitForBackendRestart } from "$lib/restartRecovery";
	import { hasDevelopChannelQuizPassed } from "$lib/utils/developChannelGate";
	import { developFeedbackFabVisible } from "$lib/stores/developFeedbackFab";
	import { experimentalSettingsUnlocked } from "$lib/stores/experimentalSettingsUnlocked";
	import { settingsUpdateHighlight } from "$lib/stores/settingsUpdateHighlight";
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
	let telemtStatusValue = $state<import('$lib/types').TelemtStatus | null>(null);
	let telemtStatusLoading = $state(false);
	let telemtInstalling = $state(false);
	let telemtUpdating = $state(false);
	let telemtRestarting = $state(false);
	let telemtUninstalling = $state(false);
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
					? (enabled ? m.settings_page_ndms_proxy_enabled_toast() : m.settings_page_ndms_proxy_disabled_toast())
					: m.settings_page_state_unchanged(),
			);
		} catch (e) {
			const msg = e instanceof Error ? e.message : m.settings_page_ndms_proxy_toggle_failed();
			if (msg.includes('PROXY_COMPONENT_MISSING') || msg.includes("'proxy'")) {
				notifications.error(m.settings_page_ndms_component_missing());
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
				action === 'restart' ? m.settings_page_singbox_restarted() :
				action === 'stop' ? m.settings_page_singbox_stopped() : m.settings_page_singbox_started(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_control_failed());
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
				action === 'restart' ? m.settings_page_hydra_restarted() :
				action === 'stop' ? m.settings_page_hydra_stopped() : m.settings_page_hydra_started(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_hydra_control_failed());
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
			notifications.success(m.settings_page_singbox_installed());
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
			notifications.success(m.settings_page_singbox_removed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_remove_failed());
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
			notifications.success(m.settings_page_singbox_updated());
		} catch (e) {
			singboxUpdateError = e instanceof Error ? e.message : String(e);
		} finally {
			singboxUpdating = false;
		}
	}

	async function fetchTelemtStatus(silent = false) {
		if (!silent) {
			telemtStatusLoading = true;
		}
		try {
			telemtStatusValue = await api.telemtStatus();
		} catch (e) {
			console.debug('telemt status unavailable:', e);
		} finally {
			if (!silent) {
				telemtStatusLoading = false;
			}
		}
	}

	async function installTelemt() {
		telemtInstalling = true;
		try {
			telemtStatusValue = await api.telemtInstall();
			notifications.success(m.settings_page_telemt_installed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_install_failed());
		} finally {
			telemtInstalling = false;
		}
	}

	async function updateTelemt() {
		telemtUpdating = true;
		try {
			telemtStatusValue = await api.telemtUpdate();
			notifications.success(m.settings_page_telemt_updated());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_update_failed());
		} finally {
			telemtUpdating = false;
		}
	}

	async function restartTelemt() {
		telemtRestarting = true;
		try {
			telemtStatusValue = await api.telemtRestart();
			notifications.success(m.settings_page_telemt_restarted());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_restart_failed());
		} finally {
			telemtRestarting = false;
		}
	}

	async function uninstallTelemt() {
		telemtUninstalling = true;
		try {
			telemtStatusValue = await api.telemtUninstall();
			notifications.success(m.settings_page_telemt_removed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_remove_failed());
		} finally {
			telemtUninstalling = false;
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
			notifications.success(m.settings_page_download_route_saved());
		} catch {
			notifications.error(m.settings_page_download_route_save_failed());
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

	// ── подсистемы прокси (WDTT, FreeTurn, обфускаторы) ─────────────
	// Бинари ставятся и снимаются целиком подсистемой: version-файл у половин
	// общий, а раздельный снос сделал бы статус неоднозначным.
	//
	// Статус живёт в polling-store, подписанном на `proxyrt.instances`: удаление
	// инстанса в другой вкладке иначе оставило бы кнопку «Удалить» запертой до
	// перезагрузки страницы.
	const PROXY_SUBSYSTEMS = [
		{ key: 'wdtt' as const, label: 'WDTT' },
		{ key: 'freeturn' as const, label: 'FreeTurn' },
		{ key: 'obf-phobos' as const, label: 'wg-obfuscator (Phobos)' },
		{ key: 'obf-clusterm' as const, label: 'wg-obfuscator (ClusterM)' },
	];
	let proxyBusy = $state<Record<string, boolean>>({});

	// Автоподписка `$store` работает только с идентификатором, поэтому
	// store'ы разложены по переменным.
	const wdttInstallStore = proxyInstallStatus.wdtt;
	const freeturnInstallStore = proxyInstallStatus.freeturn;
	const obfPhobosInstallStore = proxyInstallStatus['obf-phobos'];
	const obfClusterMInstallStore = proxyInstallStatus['obf-clusterm'];
	const proxyStatuses = $derived({
		wdtt: $wdttInstallStore.data,
		freeturn: $freeturnInstallStore.data,
		'obf-phobos': $obfPhobosInstallStore.data,
		'obf-clusterm': $obfClusterMInstallStore.data,
	});

	async function runProxyBinaries(
		subsystem: ProxySubsystem,
		action: () => Promise<void>,
		okMessage: string,
		failMessage: string,
	) {
		proxyBusy = { ...proxyBusy, [subsystem]: true };
		try {
			await action();
			notifications.success(okMessage);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : failMessage);
		} finally {
			proxyBusy = { ...proxyBusy, [subsystem]: false };
			await proxyInstallStatus[subsystem].refetch();
		}
	}

	const proxyBinaryRows = $derived(
		PROXY_SUBSYSTEMS.map(({ key, label }) => {
			const st = proxyStatuses[key];
			return {
				key,
				label,
				present: st?.binariesPresent === true,
				installAvailable: st?.installAvailable === true,
				updateAvailable: st?.updateAvailable === true,
				installedVersion: st?.installedVersion,
				installVersion: st?.installVersion,
				instances: st?.instances ?? 0,
				busy: proxyBusy[key] === true,
				oninstall: () =>
					void runProxyBinaries(key, () => api.proxyInstall(key),
						m.settings_page_proxy_installed({ label }), m.settings_page_proxy_install_failed({ label })),
				onuninstall: () =>
					void runProxyBinaries(key, () => api.proxyUninstall(key),
						m.settings_page_proxy_removed({ label }), m.settings_page_proxy_remove_failed({ label })),
			};
		// Подсистема без статуса и без возможности установки — не наша арка:
		// строка была бы мёртвой.
		}).filter((row) => row.present || row.installAvailable),
	);

onMount(() => {
	// Свой таймер мимо стора sysInfo. Данные тут почти статичные: версии,
	// возможности прошивки, состояние kernel-модуля. Всё, что меняется,
	// меняет сам пользователь с этой же страницы, и те пути перечитывают
	// сами — поэтому это страховка, а не источник, и 30 с ей ни к чему.
	// Фоновая вкладка не спрашивает вовсе.
	const stopSystemInfoPoll = startVisiblePoll(() => {
		fetchSystemInfo(true);
		fetchTelemtStatus(true);
	}, 120000);

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
			notifications.error(e instanceof Error ? e.message : m.settings_page_load_failed());
		} finally {
			loading = false;
		}

		void fetchTelemtStatus(false);

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

	async function toggleAuth(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, authEnabled: enabled });
			setGlobalSettings(settings);
			notifications.success(enabled ? m.settings_page_auth_enabled() : m.settings_page_auth_disabled());
		} catch {
			notifications.error(m.settings_page_save_error());
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
			notifications.success(m.settings_page_session_ttl_saved());
		} catch (e) {
			// 400 с русским сообщением от бэкенда (валидация 1..720) — показываем как есть.
			notifications.error(e instanceof Error ? e.message : m.settings_page_save_error());
		} finally {
			saving = false;
		}
	}

	// Ключи запрашиваются только пока MCP включён; SSE «mcpKeys» обновляет
	// список через стор, поэтому после create/revoke руками ничего не грузим.
	let mcpKeysState = $state<PollingState<McpKey[]> | null>(null);
	$effect(() => {
		if (!settings?.mcpEnabled) {
			mcpKeysState = null;
			return;
		}
		return mcpKeys.subscribe((s) => {
			if (s.status === "error" && mcpKeysState?.status !== "error") {
				notifications.error(s.error ?? m.settings_page_mcp_keys_load_failed());
			}
			mcpKeysState = s;
		});
	});

	async function toggleMcp(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, mcpEnabled: enabled });
			setGlobalSettings(settings);
			notifications.success(enabled ? m.settings_page_mcp_enabled() : m.settings_page_mcp_disabled());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_save_error());
		} finally {
			saving = false;
		}
	}

	async function toggleObfuscatorRelay(process: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.setObfuscatorRelay(process);
			setGlobalSettings(settings);
			notifications.success(process ? m.settings_page_phobos_userspace() : m.settings_page_phobos_kernel());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_save_error());
		} finally {
			saving = false;
		}
	}

	async function createMcpKey(name: string, readOnly: boolean): Promise<McpKeyCreated> {
		const created = await api.createMcpKey(name, readOnly);
		await mcpKeys.refetch();
		return created;
	}

	async function revokeMcpKey(id: string) {
		try {
			await api.revokeMcpKey(id);
			notifications.success(m.settings_page_key_revoked());
			await mcpKeys.refetch();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_key_revoke_failed());
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
			notifications.success(m.settings_page_apikey_generated());
		} catch {
			notifications.error(m.settings_page_apikey_generate_failed());
		} finally {
			saving = false;
		}
	}

	async function copyApiKey() {
		if (!settings) return;
		const key = (settings.apiKey ?? "").trim();
		if (!key) {
			notifications.info(m.settings_page_apikey_generate_first());
			return;
		}
		if (await copyToClipboard(key)) {
			notifications.success(m.settings_page_apikey_copied());
		} else {
			notifications.error(m.settings_page_apikey_copy_failed());
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
			notifications.success(enabled ? m.settings_page_logging_enabled() : m.settings_page_logging_disabled());
		} catch {
			notifications.error(m.settings_page_save_error());
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
			notifications.success(m.settings_page_logging_saved());
		} catch {
			notifications.error(m.settings_page_save_error());
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
			notifications.success(enabled ? m.settings_page_dns_auto_enabled() : m.settings_page_dns_auto_disabled());
		} catch {
			notifications.error(m.settings_page_save_error());
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
			notifications.success(m.settings_page_dns_auto_saved());
		} catch {
			notifications.error(m.settings_page_save_error());
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
			notifications.success(m.settings_page_ping_targets_saved());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_ping_targets_save_failed());
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
			settings = await api.updateSettings({ ...settings, singboxClashPort: value });
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

	async function toggleUpdateCheck(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, checkEnabled: enabled },
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? m.settings_page_update_check_enabled() : m.settings_page_update_check_disabled());
		} catch {
			notifications.error(m.settings_page_save_error());
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

	async function selectUsageLevel(level: UsageLevel) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, usageLevel: level });
			setGlobalSettings(settings);
			notifications.success(m.settings_level_saved({ level: usageLevelLabel(level) }));
		} catch {
			notifications.error(m.settings_page_level_save_failed());
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
				notifications.success(m.settings_page_restarting());
			} else {
				notifications.warning(m.settings_page_connection_dropped());
			}
			const waitResult = await waitForDaemonRestart(before);
			if (waitResult === 'timeout') {
				restarting = false;
				notifications.warning(m.settings_page_restart_unconfirmed());
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_restart_failed());
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
				throw new Error(m.settings_page_session_expired());
			}

			if (!response.ok) {
				const text = await response.text().catch(() => '');
				throw new Error(m.settings_page_restart_http_failed({ status: response.status, text: text.substring(0, 120) }));
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
	<title>{m.settings_page_document_title()}</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title={m.nav_settings()} />
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
				/>

				<div id="awgm-update" class="settings-block">
					<div class="card settings-highlight-target" class:highlighted={$settingsUpdateHighlight}>
						<SettingsSectionLabel label={m.settings_page_update_section_title()} icon={CircleArrowDown} tone="green" header />
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
					clashPort={settings.singboxClashPort ?? 0}
					clashPortSaving={savingClashPort}
					{clashPortError}
					onsaveClashPort={saveClashPort}
					proxyBinaries={proxyBinaryRows}
					telemtStatus={telemtStatusValue}
					{telemtStatusLoading}
					{telemtInstalling}
					{telemtUpdating}
					{telemtRestarting}
					{telemtUninstalling}
					oninstallTelemt={installTelemt}
					onupdateTelemt={updateTelemt}
					onrestartTelemt={restartTelemt}
					onuninstallTelemt={uninstallTelemt}
					showTelemt={true}
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
					<SettingsSectionLabel label={m.settings_page_access_title()} icon={Lock} tone="blue" header />
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.settings_page_auth_label()}</span>
							<span class="setting-description">
								{m.settings_page_auth_description()}
							</span>
						</div>
						<Toggle checked={settings.authEnabled} onchange={toggleAuth} disabled={saving} />
					</div>
					{#if settings.authEnabled}
						<div class="setting-row session-ttl-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">{m.settings_page_session_ttl_label()}</span>
								<span class="setting-description">
									{m.settings_page_session_ttl_description()}
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
									<span class="input-suffix">{m.settings_page_hours_suffix()}</span>
								</div>
								{#if sessionTtlChanged}
									<Button variant="primary" size="sm" onclick={saveSessionTtl} loading={saving}>
										{saving ? m.common_saving() : m.common_save()}
									</Button>
								{/if}
							</div>
						</div>
					{/if}
					<HttpServerCard />
					</div>
				</div>

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
					<SettingsSectionLabel label={m.settings_page_logging_title()} icon={ScrollText} tone="slate" header />
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
					<SettingsSectionLabel label={m.settings_page_ping_title()} icon={Activity} tone="teal" header />
					<div class="setting-row ping-target-setting">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.settings_page_ping_targets_label()}</span>
							<span class="setting-description">
								{m.settings_page_ping_targets_description()}
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
								<span>{m.settings_page_ping_http_url()}</span>
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
									{m.common_save()}
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
					<SettingsSectionLabel label={m.settings_page_advanced_title()} icon={Wrench} tone="indigo" header />
					<div class="setting-row api-key-setting">
						<div class="flex flex-col gap-1">
							<span class="font-medium">API Key</span>
							<span class="setting-description">
								<RichText
									text={m.settings_page_apikey_description({
										endpoint: `${origin}/api/`,
										token: m.settings_page_apikey_token(),
									})}
								/>
							</span>
						</div>
						<div class="api-key-controls">
							<input
								type="text"
								class="api-key-input"
								value={settings.apiKey ?? ""}
								readonly
								placeholder={m.settings_page_apikey_placeholder()}
								onclick={copyApiKey}
								title={settings.apiKey?.trim()
									? m.settings_page_apikey_click_to_copy()
									: m.settings_page_apikey_generate_hint()}
							/>
							<div class="api-key-action">
								<Button variant="secondary" size="md" onclick={generateApiKey} disabled={saving}>
									{m.common_generate()}
								</Button>
							</div>
						</div>
					</div>
					{#if settings.updates.channel === 'develop'}
					<div class="setting-row toggle-inline-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.settings_page_feedback_label()}</span>
							<span class="setting-description">
								{m.settings_page_feedback_description()}
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
							<span class="font-medium">{m.settings_page_happ_label()}</span>
							<span class="setting-description">
								{m.settings_page_happ_description()}
							</span>
						</div>
						<Button
							variant="secondary"
							size="md"
							onclick={() => (showHappKeysModal = true)}
							disabled={saving}
						>
							{m.settings_page_happ_manage()}
						</Button>
					</div>

					{#if singboxInstalled && showSingboxIntegration}
						<div class="setting-row toggle-inline-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">{m.settings_page_ndms_proxy_label()}</span>
								<span class="setting-description">
									{#if ndmsProxyEnabled}
										{m.settings_page_ndms_proxy_on_1()}
										<br>
										{m.settings_page_ndms_proxy_on_2()}
									{:else}
										{m.settings_page_ndms_proxy_off()}
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

				<McpCard
					enabled={settings.mcpEnabled ?? false}
					{saving}
					keys={mcpKeysState?.data ?? []}
					keysLoading={mcpKeysState?.status === "loading"}
					{origin}
					ontoggle={toggleMcp}
					oncreate={createMcpKey}
					onrevoke={revokeMcpKey}
				/>

				<ObfuscatorRelayCard
					process={settings.obfuscatorRelayProcess ?? false}
					tripped={settings.obfuscatorKmodTripped ?? ''}
					{saving}
					ontoggle={toggleObfuscatorRelay}
				/>

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
			<SettingsSectionLabel label={m.settings_page_actions_title()} icon={Power} tone="red" header />
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_page_restart_label()}</span>
					<span class="setting-description">{m.settings_page_restart_description()}</span>
				</div>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (restartConfirmOpen = true)}
					loading={restarting}
				>
					{restarting ? m.settings_page_restarting_button() : m.common_restart()}
				</Button>
			</div>

			{#if singboxInstalled && showSingboxIntegration}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">Sing-box</span>
						<span class="setting-description">
							{singboxRunning ? m.settings_page_process_running() : m.settings_page_process_stopped()}
						</span>
					</div>
					<div class="action-buttons">
						{#if singboxRunning}
							<span title={singboxStatusValue?.updateAvailable ? m.settings_page_singbox_update_first({ version: singboxStatusValue.requiredVersion }) : ''}>
								<Button
									variant="secondary"
									size="sm"
									onclick={() => controlSingbox('restart')}
									loading={singboxBusy}
									disabled={singboxStatusValue?.updateAvailable ?? false}
								>
									{m.common_restart()}
								</Button>
							</span>
							<Button variant="danger" size="sm" onclick={() => controlSingbox('stop')} loading={singboxBusy}>{m.common_stop()}</Button>
						{:else}
							<Button variant="success" size="sm" onclick={() => controlSingbox('start')} loading={singboxBusy}>{m.common_start()}</Button>
						{/if}
					</div>
				</div>
			{/if}

			{#if hydraInstalled && showHydraIntegration}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">HydraRoute Neo</span>
						<span class="setting-description">
							{hydraRunning ? m.settings_page_daemon_running() : m.settings_page_daemon_stopped()}
						</span>
					</div>
					<div class="action-buttons">
						{#if hydraRunning}
							<Button variant="secondary" size="sm" onclick={() => controlHydra('restart')} loading={hydraBusy}>{m.common_restart()}</Button>
							<Button variant="danger" size="sm" onclick={() => controlHydra('stop')} loading={hydraBusy}>{m.common_stop()}</Button>
						{:else}
							<Button variant="success" size="sm" onclick={() => controlHydra('start')} loading={hydraBusy}>{m.common_start()}</Button>
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
		title={ndmsProxyConfirmEnable ? m.settings_page_ndms_confirm_enable_title() : m.settings_page_ndms_confirm_disable_title()}
		message={ndmsProxyConfirmEnable
			? m.settings_page_ndms_confirm_enable_message()
			: m.settings_page_ndms_confirm_disable_message()}
		secondary={ndmsProxyConfirmEnable
			? m.settings_page_ndms_confirm_enable_secondary()
			: m.settings_page_ndms_confirm_disable_secondary()}
		confirmLabel={ndmsProxyConfirmEnable ? m.common_enable() : m.common_disable()}
		variant={ndmsProxyConfirmEnable ? 'primary' : 'danger'}
		busy={ndmsProxyBusy}
		onConfirm={applyNDMSProxyToggle}
		onClose={() => (ndmsProxyConfirmOpen = false)}
	/>

	<Modal
		open={restartConfirmOpen}
		title={m.settings_page_restart_modal_title()}
		size="sm"
		onclose={() => (restartConfirmOpen = false)}
	>
		<p class="modal-text">
			{m.settings_page_restart_modal_text()}
		</p>
		{#snippet actions()}
			<Button variant="ghost" size="md" onclick={() => (restartConfirmOpen = false)}>{m.common_cancel()}</Button>
			<Button variant="primary" size="md" onclick={restartDaemon}>{m.common_restart()}</Button>
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

	.settings-text-input {
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
