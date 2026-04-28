<script lang="ts">
	import { onMount } from "svelte";
	import { api } from "$lib/api/client";
	import { notifications } from "$lib/stores/notifications";
	import { singboxStatus } from "$lib/stores/singbox";
	import { PageContainer, LoadingSpinner } from "$lib/components/layout";
	import { Toggle, Modal, Button } from "$lib/components/ui";
	import {
		SystemInfoGrid,
		LoggingSettings,
		UpdateSection,
		DnsRouteSettings,
		IntegrationsCard,
		SettingsFooter,
	} from "$lib/components/settings";
	import type {
		SystemInfo,
		Settings,
		UpdateInfo,
		HydraRouteStatus,
	} from "$lib/types";

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	const origin = $derived(typeof window !== "undefined" ? window.location.origin : "");
	let updateInfo: UpdateInfo | null = $state(null);
	let restarting = $state(false);
	let restartConfirmOpen = $state(false);
	let hydraStatus = $state<HydraRouteStatus | null>(null);
	let hydraBusy = $state(false);
	let singboxInstalling = $state(false);
	let singboxInstallError = $state<string | null>(null);
	let singboxBusy = $state(false);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);
	const singboxRunning = $derived(singboxStatusValue?.running ?? false);
	const hydraInstalled = $derived(hydraStatus?.installed ?? false);
	const hydraRunning = $derived(hydraStatus?.running ?? false);

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
			hydraStatus = await api.controlHydraRoute(action);
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

	onMount(async () => {
		try {
			[systemInfo, settings, updateInfo] = await Promise.all([
				api.getSystemInfo(),
				api.getSettings(),
				api.checkUpdate(),
			]);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Не удалось загрузить настройки");
		} finally {
			loading = false;
		}
		try {
			hydraStatus = await api.getHydraRouteStatus();
		} catch {
			/* ignore - HR may not be available */
		}
	});

	async function toggleAuth(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, authEnabled: enabled });
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
			notifications.success("API ключ сгенерирован");
		} catch {
			notifications.error("Ошибка генерации ключа");
		} finally {
			saving = false;
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
			notifications.success(enabled ? "Автопроверка обновлений включена" : "Автопроверка обновлений отключена");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function restartDaemon() {
		restartConfirmOpen = false;
		restarting = true;
		try {
			await api.restartDaemon();
			notifications.success("AWG Manager перезапускается...");
		} catch {
			notifications.error("Не удалось перезапустить");
			restarting = false;
		}
	}
</script>

<svelte:head>
	<title>Настройки - AWG Manager</title>
</svelte:head>

<PageContainer>
	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else if settings && systemInfo}
		<div class="settings-grid">
			<aside class="settings-left">
				<SystemInfoGrid {systemInfo} />

				<div class="card">
					<div class="section-label">Обновление</div>
					<UpdateSection bind:updateInfo />
				</div>

				<IntegrationsCard
					singboxStatus={singboxStatusValue}
					{hydraStatus}
					{singboxInstalling}
					{singboxInstallError}
					oninstallSingbox={installSingbox}
				/>
			</aside>

			<main class="settings-right">
				<div class="card">
					<div class="section-label">Доступ</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Авторизация</span>
							<span class="setting-description">
								Требовать вход через учётную запись Keenetic для доступа к панели управления
							</span>
						</div>
						<Toggle checked={settings.authEnabled} onchange={toggleAuth} disabled={saving} />
					</div>
				</div>

				<div class="card">
					<div class="section-label">Обновления</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Автопроверка обновлений</span>
							<span class="setting-description">Проверять наличие новых версий раз в сутки</span>
						</div>
						<Toggle
							checked={settings.updates.checkEnabled}
							onchange={toggleUpdateCheck}
							disabled={saving}
						/>
					</div>
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

				{#if systemInfo.isOS5}
					<div class="card">
						<div class="section-label">DNS-маршрутизация</div>
						<DnsRouteSettings
							bind:settings
							{saving}
							onToggle={toggleDnsAutoRefresh}
							onSave={saveDnsRouteSettings}
						/>
					</div>
				{/if}

				<div class="card">
					<div class="section-label">Расширенные</div>
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">API Key</span>
							<span class="setting-description">
								API ключ для доступа к&nbsp;<code>{origin}/api/</code>, если включена авторизация. Передавайте в заголовке <code>Authorization: Bearer &lt;ключ&gt;</code>.
							</span>
						</div>
						<div class="api-key-row">
							<input
								type="text"
								class="api-key-input"
								value={settings.apiKey ?? ""}
								readonly
								placeholder="не сгенерирован"
							/>
							<Button variant="ghost" size="sm" onclick={generateApiKey} disabled={saving}>
								Сгенерировать
							</Button>
						</div>
					</div>
				</div>
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
					variant="ghost"
					size="sm"
					onclick={() => (restartConfirmOpen = true)}
					loading={restarting}
				>
					{restarting ? "Перезапуск..." : "Перезапустить"}
				</Button>
			</div>

			{#if singboxInstalled}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">Sing-box</span>
						<span class="setting-description">
							{singboxRunning ? "Процесс работает" : "Процесс остановлен"}
						</span>
					</div>
					<div class="action-buttons">
						{#if singboxRunning}
							<Button variant="ghost" size="sm" onclick={() => controlSingbox('restart')} loading={singboxBusy}>Перезапустить</Button>
							<Button variant="ghost" size="sm" onclick={() => controlSingbox('stop')} loading={singboxBusy}>Остановить</Button>
						{:else}
							<Button variant="ghost" size="sm" onclick={() => controlSingbox('start')} loading={singboxBusy}>Запустить</Button>
						{/if}
					</div>
				</div>
			{/if}

			{#if hydraInstalled}
				<div class="setting-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">HydraRoute Neo</span>
						<span class="setting-description">
							{hydraRunning ? "Демон работает" : "Демон остановлен"}
						</span>
					</div>
					<div class="action-buttons">
						{#if hydraRunning}
							<Button variant="ghost" size="sm" onclick={() => controlHydra('restart')} loading={hydraBusy}>Перезапустить</Button>
							<Button variant="ghost" size="sm" onclick={() => controlHydra('stop')} loading={hydraBusy}>Остановить</Button>
						{:else}
							<Button variant="ghost" size="sm" onclick={() => controlHydra('start')} loading={hydraBusy}>Запустить</Button>
						{/if}
					</div>
				</div>
			{/if}
		</div>

		<SettingsFooter />
	{/if}

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
	.settings-grid {
		display: grid;
		grid-template-columns: 360px 1fr;
		gap: 1rem;
		align-items: start;
	}

	.settings-left,
	.settings-right {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
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
		margin-top: 0.75rem;
	}

	.action-buttons {
		display: inline-flex;
		gap: 0.375rem;
		flex-shrink: 0;
	}

	.api-key-row {
		display: inline-flex;
		gap: 0.5rem;
		align-items: center;
		flex-shrink: 0;
	}

	.api-key-input {
		width: 22rem;
		max-width: 100%;
		padding: 0.375rem 0.5rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.8rem;
		background: var(--bg, var(--color-bg));
		border: 1px solid var(--border, var(--color-border));
		border-radius: 4px;
		color: var(--text, var(--color-text));
	}
	.api-key-input:read-only {
		opacity: 0.85;
		cursor: text;
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
