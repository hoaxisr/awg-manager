<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Toggle } from '$lib/components/ui';
	import { SystemInfoGrid, LoggingSettings, UpdateSection, DnsRouteSettings, HiddenTunnelsSettings } from '$lib/components/settings';
	import type { SystemInfo, Settings, UpdateInfo } from '$lib/types';

	let systemInfo: SystemInfo | null = $state(null);
	let settings = $state<Settings | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	let updateInfo: UpdateInfo | null = $state(null);
	let restarting = $state(false);

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

	async function refreshSystemInfo() {
		try {
			systemInfo = await api.getSystemInfo();
		} catch { /* ignore */ }
	}

	async function restartDaemon() {
		if (!confirm('Перезапустить AWG Manager?')) return;
		restarting = true;
		try {
			await api.restartDaemon();
			notifications.success('AWG Manager перезапускается...');
		} catch (e) {
			notifications.error('Не удалось перезапустить');
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

					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Перезапуск</span>
							<span class="setting-description">
								Перезапустить процесс AWG Manager. Туннели продолжат работать.
							</span>
						</div>
						<button class="btn btn-ghost btn-sm" onclick={restartDaemon} disabled={restarting}>
							{restarting ? 'Перезапуск...' : 'Перезапустить'}
						</button>
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

		</div>

		<HiddenTunnelsSettings />

		<div class="credits-section">
			<div class="section-label">Благодарности</div>
			<div class="card">
				<div class="credits-content">
					<span class="credits-nick">@paris19891</span>
					<span class="credits-nick">@The_Immortal</span>
					<span class="credits-nick">@LionEvil</span>
					<span class="credits-nick">@dio1122</span>
					<span class="credits-nick">@Nidre</span>
					<span class="credits-nick">@rexsniper</span>
					<span class="credits-nick">@tiffolk</span>
					<span class="credits-nick">@Shidla</span>
					<span class="credits-nick">@palik_lelyakin</span>
					<span class="credits-nick">@user_shurik</span>
					<span class="credits-nick">@metasevss</span>
					<span class="credits-nick">@reSigo</span>
					<span class="credits-nick">@dnstkrv</span>
					<span class="credits-nick">@JentRy</span>
					<span class="credits-nick">@Il131</span>
					<span class="credits-nick">@Gjkmpjdfntkm</span>
					<span class="credits-nick">@NGC4563</span>
					<span class="credits-nick">@NickHG55</span>
					<span class="credits-nick">@moskinnickolas</span>
					<span class="credits-nick">@antdocraf</span>
					<span class="credits-nick">@primus_ultima</span>
					<span class="credits-nick">@ninja1000sx70</span>
					<span class="credits-nick">@neverny</span>
					<span class="credits-nick">@ToDDiiN</span>
					<span class="credits-nick">@vlzSilver</span>
					<span class="credits-nick">@KomarovIgor</span>
					<span class="credits-nick">@Skverna84</span>
					<span class="credits-nick">@SBogolyubov</span>
					<span class="credits-nick">@Kub26</span>
					<span class="credits-nick">@kentbrokeman</span>
					<span class="credits-nick">@Sergej_Kopyshev</span>
					<span class="credits-nick">@Green_snakee</span>
					<span class="credits-nick">@Console4ka</span>
					<span class="credits-nick">@Vorlam Vorlamov</span>
					<span class="credits-nick">@ras****.com</span>
					<span class="credits-nick">@Даниил_***ов</span>
					<span class="credits-nick">@White3d3</span>
					<span class="credits-nick">@neoplazma</span>
					<span class="credits-nick">@Борис_Д******о</span>
					<span class="credits-nick">@N1KN0</span>
					<span class="credits-nick">@GregMSK</span>
					<span class="credits-nick">@vadim_uv</span>
					<span class="credits-nick">@xProtosx</span>
					<span class="credits-nick">@RaggaSimpson</span>
					<span class="credits-nick">@М****л Л*****о</span>
					<span class="credits-nick">@D***s C************o</span>
					<span class="credits-nick">@MrUndefined86</span>
					<span class="credits-nick">@А*******р Ч******н</span>
					<span class="credits-nick">@vkh_ent</span>
					<span class="credits-nick">@momomol777</span>
					<span class="credits-nick">@Grimrade</span>
				</div>
			</div>
		</div>

	{/if}
</PageContainer>

<style>
	.credits-section {
		margin-top: 0.5rem;
	}

	.credits-content {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.credits-nick {
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted);
		background: var(--bg-primary);
		padding: 0.125rem 0.5rem;
		border-radius: 10px;
		border: 1px solid var(--border);
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
