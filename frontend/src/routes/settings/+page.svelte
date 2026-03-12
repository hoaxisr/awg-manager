<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Toggle } from '$lib/components/ui';
	import { PingCheckSettings, SystemInfoGrid, LoggingSettings, BackendSettings, UpdateSection, DnsRouteSettings } from '$lib/components/settings';
	import type { SystemInfo, Settings, UpdateInfo } from '$lib/types';

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let updateInfo: UpdateInfo | null = $state(null);
	// Boot delay local state
	let bootDelay = $state(120);
	let savedBootDelay = $derived(settings?.bootDelaySeconds || 120);
	let bootDelayChanged = $derived(bootDelay !== savedBootDelay);

	$effect(() => {
		bootDelay = savedBootDelay;
	});

	onMount(async () => {
		try {
			[systemInfo, settings, updateInfo] = await Promise.all([
				api.getSystemInfo(),
				api.getSettings(),
				api.checkUpdate()
			]);
		} catch (e) {
			notifications.error('Не удалось загрузить настройки');
		} finally {
			loading = false;
		}
	});

	async function toggleAuth() {
		if (!settings) return;

		saving = true;
		try {
			const newSettings = { ...settings, authEnabled: !settings.authEnabled };
			settings = await api.updateSettings(newSettings);
			notifications.success(
				settings.authEnabled
					? 'Авторизация включена'
					: 'Авторизация отключена'
			);
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function togglePingCheck() {
		if (!settings) return;

		saving = true;
		try {
			const newSettings = {
				...settings,
				pingCheck: {
					...settings.pingCheck,
					enabled: !settings.pingCheck.enabled
				}
			};
			settings = await api.updateSettings(newSettings);
			notifications.success(
				settings.pingCheck.enabled
					? 'Мониторинг туннелей включён'
					: 'Мониторинг туннелей отключён'
			);
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function savePingCheckDefaults() {
		if (!settings) return;

		saving = true;
		try {
			settings = await api.updateSettings(settings);
			notifications.success('Настройки сохранены');
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function toggleLogging() {
		if (!settings) return;

		saving = true;
		try {
			const newSettings = {
				...settings,
				logging: {
					...settings.logging,
					enabled: !settings.logging.enabled
				}
			};
			settings = await api.updateSettings(newSettings);
			notifications.success(
				settings.logging.enabled
					? 'Логирование включено'
					: 'Логирование отключено'
			);
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function saveLoggingSettings() {
		if (!settings) return;

		saving = true;
		try {
			settings = await api.updateSettings(settings);
			notifications.success('Настройки логирования сохранены');
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function toggleDnsAutoRefresh() {
		if (!settings) return;

		saving = true;
		try {
			const enabled = !settings.dnsRoute.autoRefreshEnabled;
			const newSettings = {
				...settings,
				dnsRoute: {
					...settings.dnsRoute,
					autoRefreshEnabled: enabled,
					refreshIntervalHours: enabled && settings.dnsRoute.refreshIntervalHours === 0
						? 6
						: settings.dnsRoute.refreshIntervalHours,
					refreshMode: settings.dnsRoute.refreshMode || 'interval',
				}
			};
			settings = await api.updateSettings(newSettings);
			notifications.success(
				settings.dnsRoute.autoRefreshEnabled
					? 'Автообновление подписок включено'
					: 'Автообновление подписок отключено'
			);
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function saveDnsRouteSettings() {
		if (!settings) return;

		saving = true;
		try {
			settings = await api.updateSettings(settings);
			notifications.success('Настройки автообновления сохранены');
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function saveBootDelay() {
		if (!settings) return;

		saving = true;
		try {
			const newSettings = { ...settings, bootDelaySeconds: bootDelay };
			settings = await api.updateSettings(newSettings);
			notifications.success('Задержка загрузки сохранена');
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function toggleUpdateCheck() {
		if (!settings) return;

		saving = true;
		try {
			const newSettings = {
				...settings,
				updates: {
					...settings.updates,
					checkEnabled: !settings.updates.checkEnabled
				}
			};
			settings = await api.updateSettings(newSettings);
			notifications.success(
				settings.updates.checkEnabled
					? 'Автопроверка обновлений включена'
					: 'Автопроверка обновлений отключена'
			);
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	function handleBackendModeChange(mode: 'auto' | 'kernel' | 'userspace') {
		if (settings) {
			settings = { ...settings, backendMode: mode };
		}
	}

	async function handleBackendRestart(mode: 'auto' | 'kernel' | 'userspace') {
		try {
			await api.changeBackend(mode);
			// Server will restart — wait and reload
			await new Promise(r => setTimeout(r, 3000));
			window.location.reload();
		} catch {
			notifications.error('Ошибка при смене режима');
		}
	}

	async function refreshSystemInfo() {
		try {
			systemInfo = await api.getSystemInfo();
		} catch { /* ignore */ }
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
		<div class="disclaimer-card">
			<div class="disclaimer-content">
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16" class="disclaimer-icon">
					<circle cx="12" cy="12" r="10"/>
					<line x1="12" y1="16" x2="12" y2="12"/>
					<line x1="12" y1="8" x2="12.01" y2="8"/>
				</svg>
				<div class="disclaimer-text">
					<p>AWG Manager — независимый проект, не аффилированный с <strong>Amnezia.org</strong></p>
					<p>Вопросы и поддержка — <a href="https://t.me/awgmanager" target="_blank" rel="noopener">Telegram-группа</a></p>
				</div>
			</div>
		</div>

		<div class="settings-stack">
			<!-- СЕКЦИЯ: Система -->
			<div>
				<div class="section-label">Система</div>
				<div class="card">
					<SystemInfoGrid {systemInfo} />

					<UpdateSection bind:updateInfo />

					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Автопроверка обновлений</span>
							<span class="setting-description">
								Проверять наличие новых версий раз в сутки
							</span>
						</div>
						<Toggle checked={settings.updates.checkEnabled} onchange={() => toggleUpdateCheck()} disabled={saving} />
					</div>
				</div>
			</div>

			<!-- СЕКЦИЯ: Основные -->
			<div>
				<div class="section-label">Основные</div>
				<div class="card">
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Авторизация</span>
							<span class="setting-description">
								Требовать вход через учётную запись Keenetic для доступа к панели управления
							</span>
						</div>
						<Toggle checked={settings.authEnabled} onchange={() => toggleAuth()} disabled={saving} />
					</div>

					<PingCheckSettings
						bind:settings={settings}
						{systemInfo}
						{saving}
						onToggle={togglePingCheck}
						onSaveDefaults={savePingCheckDefaults}
					/>

					<LoggingSettings
						bind:settings={settings}
						{saving}
						onToggle={toggleLogging}
						onSave={saveLoggingSettings}
					/>

					{#if systemInfo.isOS5}
						<DnsRouteSettings
							bind:settings={settings}
							{saving}
							onToggle={toggleDnsAutoRefresh}
							onSave={saveDnsRouteSettings}
						/>
					{/if}
				</div>
			</div>

			<!-- СЕКЦИЯ: Производительность -->
			<div>
				<div class="section-label">Производительность</div>
				<div class="card perf-grid">
					<div class="perf-col">
						<BackendSettings
							{settings}
							{systemInfo}
							{saving}
							onModeChange={handleBackendModeChange}
							onRestart={handleBackendRestart}
						/>
					</div>

					<div class="perf-col perf-col-right">
						<div class="perf-col-item">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Задержка загрузки</span>
								<span class="setting-description">
									Время ожидания инициализации NDMS после перезагрузки роутера
								</span>
							</div>
							<div class="boot-delay-control">
								<input
									type="number"
									class="boot-delay-input"
									min="120"
									max="600"
									step="10"
									bind:value={bootDelay}
									disabled={saving}
								/>
								<span class="setting-description">сек</span>
								{#if bootDelayChanged}
									<button
										class="btn btn-primary btn-sm"
										onclick={saveBootDelay}
										disabled={saving || bootDelay < 120 || bootDelay > 600}
									>
										{saving ? '...' : 'Сохранить'}
									</button>
								{/if}
							</div>
							<p class="form-hint">От 120 до 600 секунд</p>
						</div>
					</div>
				</div>
			</div>

		</div>

		<div class="credits">
			Особая благодарность за появление этого продукта: @paris19891, @The_Immortal, @LionEvil, @dio1122, @Nidre, @rexsniper, @tiffolk, @Shidla
		</div>

	{/if}
</PageContainer>

<style>
	.perf-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0;
		padding: 0;
	}

	.perf-col {
		padding: 1rem;
	}

	.perf-col-right {
		border-left: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.perf-col-item {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		padding: 1rem;
	}

	.perf-col-item:first-child {
		padding-top: 0;
	}

	.perf-col-item:last-child {
		padding-bottom: 0;
	}

	@media (max-width: 640px) {
		.perf-grid {
			grid-template-columns: 1fr;
		}

		.perf-col-right {
			border-left: none;
			border-top: 1px solid var(--border);
		}
	}

	.boot-delay-control {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.boot-delay-input {
		width: 80px;
		padding: 0.4rem 0.6rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.875rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
	}

	.boot-delay-input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.credits {
		margin-top: 1rem;
		text-align: center;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.disclaimer-card {
		margin-bottom: 0.5rem;
	}

	.disclaimer-content {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		padding: 0.875rem 1rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-left: 3px solid var(--text-muted);
		border-radius: var(--radius);
		font-size: 0.8125rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}

	.disclaimer-icon {
		flex-shrink: 0;
		color: var(--text-muted);
		margin-top: 1px;
	}

	.disclaimer-text {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.disclaimer-text p {
		margin: 0;
	}

	.disclaimer-text a {
		color: var(--accent);
		text-decoration: none;
	}

	.disclaimer-text a:hover {
		text-decoration: underline;
	}

</style>
