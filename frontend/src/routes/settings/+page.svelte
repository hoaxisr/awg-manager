<script lang="ts">
	import { onMount } from "svelte";
	import { afterNavigate } from "$app/navigation";
	import { page } from "$app/stores";
	import { api } from "$lib/api/client";
	import { notifications } from "$lib/stores/notifications";
	import { singboxStatus } from "$lib/stores/singbox";
	import { hydrarouteStatus } from "$lib/stores/hydraroute";
	import { PageContainer, PageHeader, LoadingSpinner } from "$lib/components/layout";
	import {
		SystemInfoGrid,
		LoggingSettings,
		UpdateSection,
		IntegrationsCard,
		ThemeSchemeCard,
		SettingsFooter,
		HttpServerCard,
		PukhososPatrol,
		SettingsSectionLabel,
		AccessCard,
		DownloadsUpdatesCard,
		PingCheckCard,
		AdvancedCard,
		ActionsCard,
	} from "$lib/components/settings";
	import { setSettings as setGlobalSettings } from "$lib/stores/settings";
	import type {
		SystemInfo,
		Settings,
		UpdateInfo,
	} from "$lib/types";
	import { settingsUpdateHighlight } from "$lib/stores/settingsUpdateHighlight";
	import {
		CircleArrowDown,
		Network,
		ScrollText,
	} from "lucide-svelte";

	const highlightFeedbackFab = $derived($page.url.searchParams.has('feedbackFab'));
	const highlightDownloads = $derived($page.url.searchParams.get('highlight') === 'downloads');

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let updateInfo: UpdateInfo | null = $state(null);
	let singboxInstalling = $state(false);
	let singboxInstallError = $state<string | null>(null);
	let singboxUpdating = $state(false);
	let singboxUpdateError = $state<string | null>(null);
	let systemInfoRefreshing = $state(false);
	let systemInfoUpdatedAt = $state<string | null>(null);
	let systemInfoInFlight: Promise<void> | null = null;
	let footerPatrolWidth = $state(0);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxStatusLoading = $derived(
		$singboxStatus.lastFetchedAt === 0 &&
		($singboxStatus.status === 'idle' || $singboxStatus.status === 'loading')
	);
	const hydraStatusValue = $derived($hydrarouteStatus.data ?? null);
	const hydraStatusLoading = $derived(
		$hydrarouteStatus.lastFetchedAt === 0 &&
		($hydrarouteStatus.status === 'idle' || $hydrarouteStatus.status === 'loading')
	);
	const hydraStatusError = $derived($hydrarouteStatus.error);

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
				<SystemInfoGrid
					{systemInfo}
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
				/>
			</aside>

			<main class="settings-right">
			<ThemeSchemeCard />

				<AccessCard bind:settings bind:saving />

				<div class="settings-block">
					<div class="card">
					<SettingsSectionLabel label="HTTP-сервер" icon={Network} tone="blue" header />
					<HttpServerCard />
					</div>
				</div>

				<DownloadsUpdatesCard
					bind:settings
					bind:saving
					bind:updateInfo
					isOS5={systemInfo.isOS5}
					{highlightDownloads}
				/>

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

			<PingCheckCard bind:settings bind:saving />

			<AdvancedCard bind:settings bind:saving {highlightFeedbackFab} />
			</main>
		</div>

		<ActionsCard />

		<div class="settings-doc-block" id="settings-footer-block">
			<div class="settings-footer-patrol-host" bind:clientWidth={footerPatrolWidth}>
				<PukhososPatrol trackWidth={footerPatrolWidth} />
				<SettingsFooter />
			</div>
		</div>
		</div>
	{/if}
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

	.settings-footer-patrol-host {
		position: relative;
		overflow: visible;
	}

	@media (max-width: 900px) {
		.settings-grid {
			grid-template-columns: 1fr;
		}
		.settings-left {
			position: static;
		}
	}
</style>
