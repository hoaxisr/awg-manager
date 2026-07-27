<!--
  Карточка «Доступ»: авторизация в панели, время жизни сессии и вход по
  учётным данным Entware.
-->
<script lang="ts">
	import { Lock } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle, Button } from '$lib/components/ui';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import {
		clampSessionTtlHours,
		SESSION_TTL_DEFAULT_HOURS,
		SESSION_TTL_MIN_HOURS,
		SESSION_TTL_MAX_HOURS,
	} from './sessionTtl';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
	}

	let { settings = $bindable(), saving = $bindable() }: Props = $props();

	async function toggleAuth(enabled: boolean) {
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
</script>

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
	</div>
</div>

<style>
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
</style>
